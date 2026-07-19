// Package audit implements OpsSweep's immutable SQLite audit trail.
//
// Every resource deletion performed by the remediation engine is recorded
// as a row in a local SQLite database (~/.opssweep.db). The database is
// append-only by design: rows are never updated or deleted, so the audit
// trail cannot be accidentally or maliciously altered after the fact.
//
// # Why SQLite?
//
// SQLite is embedded directly into the binary (via the go-sqlite3 CGo
// driver). There is no server process to install or configure — the entire
// database is a single file on disk. This keeps OpsSweep self-contained and
// makes the audit log trivially portable: copy the file anywhere and open it
// with any SQLite viewer.
//
// # Schema
//
// A single table, audit_logs, stores one row per deleted resource:
//
//	audit_logs
//	  id             INTEGER PRIMARY KEY AUTOINCREMENT
//	  resource_id    TEXT        — AWS resource identifier (e.g. "vol-0abc123")
//	  resource_type  TEXT        — OpsSweep resource type (e.g. "ec2:ebs-volume")
//	  region         TEXT        — AWS region (e.g. "us-east-1")
//	  monthly_savings REAL       — estimated monthly USD saved by this deletion
//	  deleted_at     DATETIME    — UTC timestamp of when the deletion was logged
//
// # SQL injection prevention
//
// Every write uses parameterized queries (placeholders "?"). Raw string
// interpolation into SQL is never used anywhere in this package, even for
// trusted internal values, so there is no injection surface regardless of
// what the AWS API returns in resource IDs or tags.
package audit

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// The blank import registers the "sqlite3" driver with database/sql.
	// Without this import, sql.Open("sqlite3", ...) would panic with
	// "unknown driver". The driver is provided by the CGo-based
	// github.com/mattn/go-sqlite3 package.
	_ "github.com/mattn/go-sqlite3"
)

// ─── Data types ───────────────────────────────────────────────────────────────

// Record represents a single audit log entry — one deleted AWS resource.
//
// It is intentionally a plain value type (no pointers, no interfaces) so it
// can be safely passed by value between goroutines and across package
// boundaries without aliasing concerns.
type Record struct {
	// ResourceID is the primary AWS identifier for the deleted resource.
	// Examples: "vol-0abc123def456789", "eipalloc-0a1b2c3d", "nat-0deadbeef".
	ResourceID string

	// ResourceType is the normalised OpsSweep type string for the resource.
	// Examples: "ec2:ebs-volume", "ec2:elastic-ip", "ec2:nat-gateway".
	// This mirrors the discovery.ResourceType constants to keep the audit log
	// consistent with the rest of the application's terminology.
	ResourceType string

	// Region is the AWS region where the resource existed.
	// Example: "us-east-1", "eu-west-2".
	Region string

	// MonthlySavings is the estimated monthly USD cost that was being wasted
	// by this resource immediately before deletion. This is the same value
	// shown in the waste report table and comes from the pricing package.
	// Stored as a REAL (IEEE 754 double) in SQLite.
	MonthlySavings float64

	// DeletedAt is the UTC timestamp recorded when LogDeletion was called.
	// It reflects when OpsSweep logged the deletion, which is as close to
	// the actual deletion moment as practical without adding a separate
	// confirmation-poll loop.
	DeletedAt time.Time
}

// ─── Path helper ──────────────────────────────────────────────────────────────

// GetDefaultDBPath returns the canonical path to the OpsSweep audit database:
// ~/.opssweep.db.
//
// The database lives in the user's home directory alongside the config file
// (~/.opssweep.yaml) so all OpsSweep state is co-located and easy to find.
// The .db extension signals to operating systems and file managers that this
// is a SQLite database file.
//
// Returns an error only if the home directory cannot be determined (e.g. the
// HOME environment variable is unset, which is unusual but possible in
// containerised or CI environments).
func GetDefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("audit: could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".opssweep.db"), nil
}

// ─── Initialisation ───────────────────────────────────────────────────────────

// InitDB opens (or creates) the SQLite database at dbPath and ensures the
// audit_logs table exists with the correct schema.
//
// # Idempotency
//
// InitDB is safe to call on every application start. The
// "CREATE TABLE IF NOT EXISTS" DDL statement is a no-op when the table
// already exists, so calling InitDB twice never loses data.
//
// # Caller responsibilities
//
// The caller is responsible for closing the returned *sql.DB when the
// application shuts down (typically via defer db.Close()). Failing to close
// the database does not corrupt SQLite's WAL journal, but it does leave the
// file locked on some operating systems.
//
// # Returned errors
//
// InitDB returns an error if:
//   - The directory containing dbPath does not exist and cannot be created.
//   - The SQLite driver cannot open the file (e.g. permission denied).
//   - The CREATE TABLE statement fails (e.g. disk full).
func InitDB(dbPath string) (*sql.DB, error) {
	// ── 1. Ensure the parent directory exists ─────────────────────────────────
	// sql.Open does not create intermediate directories, so we must do it
	// ourselves. This mirrors the pattern used by WriteDefaultConfig in the
	// config package.
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("audit: failed to create database directory %q: %w", dir, err)
	}

	// ── 2. Open the database file ─────────────────────────────────────────────
	// sql.Open does not actually connect to the database — it validates the
	// driver name and DSN format. The real connection is established lazily
	// on the first query. We call db.Ping() below to force an immediate
	// connection attempt so InitDB fails fast if the file is inaccessible.
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("audit: failed to open database %q: %w", dbPath, err)
	}

	// Ping forces the lazy connection to resolve now, surfacing permission
	// errors at init time rather than on the first write.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: failed to connect to database %q: %w", dbPath, err)
	}

	// ── 3. Create the audit_logs table ────────────────────────────────────────
	// "CREATE TABLE IF NOT EXISTS" is idempotent — it succeeds whether or not
	// the table already exists, so this statement never drops or modifies
	// existing data.
	//
	// Column notes:
	//   id              — SQLite's AUTOINCREMENT guarantees monotonically
	//                     increasing IDs. Gaps in the sequence are allowed
	//                     (and expected after rollbacks), but IDs are never
	//                     reused, which is important for an audit trail.
	//   resource_id     — TEXT because AWS IDs are opaque alphanumeric strings
	//                     that we never do arithmetic on.
	//   resource_type   — TEXT; matches discovery.ResourceType values.
	//   region          — TEXT; AWS region name (e.g. "us-east-1").
	//   monthly_savings — REAL; IEEE 754 double, sufficient for USD amounts.
	//   deleted_at      — DATETIME; SQLite has no native datetime type —
	//                     "DATETIME" is an affinity hint that tools like DB
	//                     Browser for SQLite use for display purposes. The
	//                     actual value is stored as an ISO 8601 TEXT string
	//                     when written via database/sql's time.Time support.
	const schema = `
		CREATE TABLE IF NOT EXISTS audit_logs (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			resource_id     TEXT    NOT NULL,
			resource_type   TEXT    NOT NULL,
			region          TEXT    NOT NULL,
			monthly_savings REAL    NOT NULL DEFAULT 0,
			deleted_at      DATETIME NOT NULL
		)`

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: failed to create audit_logs table: %w", err)
	}

	return db, nil
}

// ─── Write operations ─────────────────────────────────────────────────────────

// LogDeletion inserts a single Record into the audit_logs table.
//
// # Parameterized queries
//
// The INSERT statement uses "?" placeholders rather than string interpolation.
// This is the single most important security property of this function.
//
// Consider what would happen without parameterization if a resource ID
// contained a SQL metacharacter — for example, an adversarially crafted tag
// value like:
//
//	'); DROP TABLE audit_logs; --
//
// With string interpolation that would become valid SQL and destroy the audit
// table. With parameterized queries the entire string is treated as a literal
// value bound to a single column — the SQL parser never sees it as syntax.
//
// The database/sql driver handles all escaping automatically when you supply
// values as separate arguments to Exec or Query. You never need to call any
// quoting or escaping function manually.
//
// # Atomicity
//
// Each LogDeletion call is its own implicit transaction (SQLite's default
// auto-commit mode). If the write fails, the row is not partially inserted —
// the failure is clean and the caller can decide whether to retry or continue.
func LogDeletion(db *sql.DB, rec Record) error {
	// The five "?" placeholders correspond to the five value arguments that
	// follow the query string. database/sql matches them positionally:
	//   ?1 → rec.ResourceID
	//   ?2 → rec.ResourceType
	//   ?3 → rec.Region
	//   ?4 → rec.MonthlySavings
	//   ?5 → rec.DeletedAt
	//
	// time.Time values are automatically serialised to ISO 8601 UTC strings
	// by the go-sqlite3 driver (e.g. "2024-07-19T14:32:01Z").
	const query = `
		INSERT INTO audit_logs
			(resource_id, resource_type, region, monthly_savings, deleted_at)
		VALUES
			(?, ?, ?, ?, ?)`

	_, err := db.Exec(query,
		rec.ResourceID,
		rec.ResourceType,
		rec.Region,
		rec.MonthlySavings,
		rec.DeletedAt,
	)
	if err != nil {
		return fmt.Errorf("audit: failed to log deletion of %s %q: %w",
			rec.ResourceType, rec.ResourceID, err)
	}

	return nil
}
