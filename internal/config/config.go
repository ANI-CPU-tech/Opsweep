// Package config implements OpsSweep's persistent configuration management
// engine. It defines the canonical Config struct, provides safe production
// defaults, and handles reading/writing the YAML configuration file that lives
// on disk between sessions.
//
// # Design goals
//
//   - Zero-friction first run: if no config file exists, DefaultConfig() gives
//     the user a safe, working setup without requiring any manual steps.
//   - Human-editable: the YAML format was chosen over JSON or TOML specifically
//     because it supports inline comments, making the generated file
//     self-documenting for users who open it in a text editor.
//   - Separation from AWS credentials: this file stores behavioral preferences
//     (log level, confidence thresholds, target regions) only. AWS credentials
//     continue to flow through the standard SDK credential chain
//     (~/.aws/credentials, environment variables, instance roles). The two
//     concerns are intentionally kept separate.
//
// # Config file location
//
// By convention the config file lives at ~/.opssweep/config.yaml, but the
// functions in this package are path-agnostic — the caller is responsible for
// resolving the path via os.UserHomeDir or a CLI flag.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ─── Top-level struct ────────────────────────────────────────────────────────

// Config is the root configuration object for OpsSweep. It is serialised to
// and deserialised from a YAML file on disk. All fields are optional; missing
// fields are filled in by [DefaultConfig] so the application always has a
// complete, valid configuration regardless of what the user has customised.
//
// YAML example:
//
//	core:
//	  log_level: info
//	  output_format: table
//	rules:
//	  confidence_threshold: 90.0
//	  ignore_tags:
//	    - "env:production"
//	    - "opssweep:ignore"
//	aws:
//	  target_regions:
//	    - us-east-1
//	    - us-west-2
type Config struct {
	// Core holds application-level behavioral settings that are not specific
	// to any AWS service or scanning rule.
	Core CoreConfig `yaml:"core"`

	// Rules holds the knobs that control how the heuristics engine classifies
	// resources. Adjusting these values lets operators tune OpsSweep for their
	// organisation's risk tolerance without changing source code.
	Rules RulesConfig `yaml:"rules"`

	// AWS holds settings that control which parts of an AWS account are
	// included in each scan.
	AWS AWSConfig `yaml:"aws"`
}

// ─── Nested structs ──────────────────────────────────────────────────────────

// CoreConfig contains application-level behavioral settings.
type CoreConfig struct {
	// LogLevel controls the verbosity of runtime log output.
	// Accepted values (case-insensitive): "debug", "info", "warn", "error".
	//
	//   - "debug"  — prints every AWS API call, full resource payloads, and
	//                internal scoring decisions. Useful when diagnosing why a
	//                specific resource is or isn't being flagged.
	//   - "info"   — prints scan progress, region summaries, and the final
	//                results table. This is the recommended production value.
	//   - "warn"   — suppresses progress output; only prints anomalies such as
	//                partial scan failures or permission errors.
	//   - "error"  — silent except for fatal errors that abort the scan.
	//
	// Default: "info"
	LogLevel string `yaml:"log_level"`

	// OutputFormat controls how scan results are rendered to the terminal.
	// Accepted values: "table", "json".
	//
	//   - "table"  — a human-readable tab-aligned table with ANSI colour coding.
	//                Best for interactive sessions.
	//   - "json"   — newline-delimited JSON objects, one per flagged resource.
	//                Best for piping into jq, Splunk, or other tooling.
	//
	// Default: "table"
	OutputFormat string `yaml:"output_format"`
}

// RulesConfig contains the knobs that govern the heuristics scoring engine.
type RulesConfig struct {
	// ConfidenceThreshold is the minimum score (0.0–100.0) a resource must
	// reach before it is included in the waste report.
	//
	// The heuristics engine assigns each resource a confidence score that
	// reflects how certain we are that it is genuinely idle and safe to remove:
	//
	//   100.0 — definitively idle (e.g. unattached Elastic IP: there is no
	//            ambiguity; the resource is allocated but not associated with
	//            anything and is billing the account right now).
	//   95.0  — very high confidence (e.g. NAT Gateway or RDS instance with
	//            zero connections over the full 14-day lookback window).
	//   85.0  — high confidence (e.g. running EC2 instance with CPU < 2%
	//            averaged over 14 days — almost certainly a zombie).
	//   50.0  — moderate confidence (e.g. stopped EC2 instance — not billing
	//            for compute but still accruing EBS storage charges).
	//
	// Lowering this value (e.g. to 50.0) broadens the report to include
	// borderline cases. Raising it (e.g. to 95.0) narrows the report to
	// near-certain waste only. The remediation engine always uses its own
	// hard-coded threshold of 0.90 regardless of this setting, so lowering
	// ConfidenceThreshold cannot accidentally enable deletion of uncertain
	// resources.
	//
	// Default: 90.0
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`

	// IgnoreTags is a list of AWS resource tag key:value pairs. Any resource
	// that carries at least one of these tags is excluded from the waste report
	// entirely, regardless of its confidence score.
	//
	// Format: "key:value" (case-sensitive). Examples:
	//   - "env:production"    — protects all resources tagged env=production.
	//   - "opssweep:ignore"   — a purpose-built opt-out tag for resources that
	//                            should never be reported as waste.
	//   - "team:platform"     — protects all resources owned by the platform team.
	//
	// This list supplements the hard-coded protection tags built into the
	// heuristics engine (keep=true, env=prod). Tags listed here are applied
	// at the reporting layer and override the confidence score.
	//
	// Default: ["env:production", "opssweep:ignore"]
	IgnoreTags []string `yaml:"ignore_tags"`
}

// AWSConfig contains settings that control which parts of an AWS account are
// included in each scan.
type AWSConfig struct {
	// TargetRegions is the explicit list of AWS regions to scan. When this
	// list is non-empty, OpsSweep scans only these regions and skips the
	// ListEnabledRegions API call entirely, which saves a round-trip and gives
	// operators precise control over scope.
	//
	// When this list is empty, OpsSweep falls back to its default behaviour:
	// calling ec2:DescribeRegions to discover all regions that are enabled
	// for the calling account and scanning all of them concurrently.
	//
	// Use this field to:
	//   - Restrict scanning to regions where your workloads actually run,
	//     reducing scan time and CloudWatch API costs.
	//   - Exclude regions that are enabled but contain only compliance/
	//     regulatory resources that should never be touched.
	//
	// Default: ["us-east-1", "us-west-2"]
	TargetRegions []string `yaml:"target_regions"`
}

// ─── Constructor ─────────────────────────────────────────────────────────────

// DefaultConfig returns a *Config populated with safe, production-ready
// defaults. It is used in two scenarios:
//
//  1. First run: when no config file exists on disk, the shell calls
//     WriteDefaultConfig to create one, giving the user a self-documenting
//     starting point they can edit without needing to consult documentation.
//
//  2. Partial config: when a config file exists but omits some fields (e.g. a
//     user who only customises Rules.ConfidenceThreshold), callers can merge
//     the parsed config on top of DefaultConfig() to fill in any gaps.
func DefaultConfig() *Config {
	return &Config{
		Core: CoreConfig{
			LogLevel:     "info",
			OutputFormat: "table",
		},
		Rules: RulesConfig{
			ConfidenceThreshold: 90.0,
			IgnoreTags: []string{
				"env:production",
				"opssweep:ignore",
			},
		},
		AWS: AWSConfig{
			TargetRegions: []string{
				"us-east-1",
				"us-west-2",
			},
		},
	}
}

// ─── Persistence ─────────────────────────────────────────────────────────────

// WriteDefaultConfig marshals DefaultConfig() into YAML and writes it to the
// file at path, creating any intermediate directories as needed.
//
// The file is written with 0644 permissions (owner read/write, group and
// others read-only) — appropriate for a config file that may contain
// non-sensitive behavioral preferences. AWS credentials are never written here.
//
// If the file already exists it is overwritten. Callers that want to preserve
// an existing config should check for the file's existence before calling this
// function.
//
// Typical usage:
//
//	home, _ := os.UserHomeDir()
//	path := filepath.Join(home, ".opssweep", "config.yaml")
//	if err := config.WriteDefaultConfig(path); err != nil {
//	    log.Fatalf("could not write config: %v", err)
//	}
func WriteDefaultConfig(path string) error {
	// ── 1. Marshal the default config to YAML ─────────────────────────────────
	// yaml.Marshal serialises the struct using the `yaml:"..."` struct tags.
	// It produces a clean, human-readable YAML document with no trailing
	// whitespace and consistent two-space indentation.
	data, err := yaml.Marshal(DefaultConfig())
	if err != nil {
		// Marshal failure on a statically known struct is a programming error,
		// not a runtime condition — but we propagate it rather than panic so
		// callers can handle it in tests and edge cases.
		return fmt.Errorf("config: failed to marshal default config: %w", err)
	}

	// ── 2. Create intermediate directories ───────────────────────────────────
	// filepath.Dir extracts the directory component of path (e.g.
	// "/home/user/.opssweep" from "/home/user/.opssweep/config.yaml").
	// MkdirAll is a no-op if the directory already exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("config: failed to create config directory %q: %w", dir, err)
	}

	// ── 3. Write the file ─────────────────────────────────────────────────────
	// 0644 = owner rw, group r, others r. Never use 0600 for a config that
	// contains no secrets — overly restrictive permissions confuse users who
	// try to share or inspect the file with standard tools.
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("config: failed to write config file %q: %w", path, err)
	}

	return nil
}
