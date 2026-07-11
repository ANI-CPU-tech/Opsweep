// Package storage manages the local state layer for OpsSweep.
// It stores scan results and teardown audit journal entries using either
// a pure-Go SQLite driver (modernc.org/sqlite, no cgo) or flat JSON files
// under ~/.opssweep/. Start with JSON for MVP simplicity; migrate to SQLite
// once query support is needed (e.g. "show me everything I deleted last week").
package storage

import (
	"time"

	"github.com/anirudh/opssweep/internal/teardown"
)

// ScanRecord is the persisted result of a single scan run.
type ScanRecord struct {
	ID        string    `json:"id"`        // UUID
	Timestamp time.Time `json:"timestamp"`
	AccountID string    `json:"accountId"`
	Regions   []string  `json:"regions"`
	// ResourceCount is a quick summary; full results live in ResourcesJSON.
	ResourceCount int    `json:"resourceCount"`
	ResourcesJSON []byte `json:"resourcesJson"` // serialised []heuristics.Score
}

// Store is the interface that both JSON and SQLite backends implement.
type Store interface {
	// SaveScan persists a scan record.
	SaveScan(record ScanRecord) error
	// LatestScan returns the most recent scan record.
	LatestScan() (*ScanRecord, error)
	// SaveAction persists a teardown action to the audit journal.
	SaveAction(action teardown.Action) error
	// ListActions returns all recorded teardown actions, most recent first.
	ListActions() ([]teardown.Action, error)
	// Close releases any held resources (file handles, DB connections).
	Close() error
}

// JSONStore is the flat-file implementation of Store.
// Files are stored under the provided base directory (default: ~/.opssweep/).
type JSONStore struct {
	baseDir string
}

// NewJSONStore creates a JSONStore rooted at baseDir.
// TODO: implement JSON file read/write for scans and actions.
func NewJSONStore(baseDir string) (*JSONStore, error) {
	// TODO: ensure baseDir exists (os.MkdirAll)
	return &JSONStore{baseDir: baseDir}, nil
}

func (s *JSONStore) SaveScan(record ScanRecord) error   { return nil } // TODO
func (s *JSONStore) LatestScan() (*ScanRecord, error)   { return nil, nil } // TODO
func (s *JSONStore) SaveAction(a teardown.Action) error { return nil } // TODO
func (s *JSONStore) ListActions() ([]teardown.Action, error) { return nil, nil } // TODO
func (s *JSONStore) Close() error                       { return nil } // TODO
