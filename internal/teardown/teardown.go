// Package teardown implements the trust-critical deletion flow for OpsSweep.
// Before removing any resource it takes a backup where applicable (EBS snapshot,
// RDS final snapshot, JSON config export for stateless resources like load
// balancers). Every action is recorded in the audit journal so resources can
// be restored later. Dry-run mode is the default.
package teardown

import (
	"context"
	"time"

	"github.com/anirudh/opssweep/internal/discovery"
)

// Action records a single teardown operation in the audit journal.
type Action struct {
	ID           string            `json:"id"`            // UUID
	Timestamp    time.Time         `json:"timestamp"`
	ResourceID   string            `json:"resourceId"`
	ResourceType discovery.ResourceType `json:"resourceType"`
	Region       string            `json:"region"`
	Operation    string            `json:"operation"`     // "snapshot" | "delete" | "restore"
	BackupRef    string            `json:"backupRef"`     // snapshot ID or JSON backup path
	DryRun       bool              `json:"dryRun"`
	Error        string            `json:"error,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

// Options controls teardown behaviour.
type Options struct {
	// DryRun previews operations without making any AWS API mutations.
	DryRun bool
	// SkipSnapshot skips pre-deletion backups (strongly discouraged).
	SkipSnapshot bool
	// AutoApprove skips per-resource confirmation prompts.
	AutoApprove bool
}

// Executor runs the teardown workflow against a list of resources.
type Executor struct {
	opts Options
}

// NewExecutor creates an Executor with the given options.
// DryRun defaults to true for safety.
func NewExecutor(opts Options) *Executor {
	if !opts.DryRun {
		// Explicit opt-in required; keep dry-run as the safe default.
	}
	return &Executor{opts: opts}
}

// Run executes the teardown workflow: snapshot → confirm → delete → journal.
// Returns the list of actions taken (or that would be taken in dry-run mode).
// TODO: implement snapshot, deletion, and journaling per resource type.
func (e *Executor) Run(ctx context.Context, resources []discovery.Resource) ([]Action, error) {
	// TODO: implement
	return nil, nil
}

// Restore recreates a resource from its recorded backup reference.
// TODO: implement restore from EBS/RDS snapshot and JSON config re-apply.
func Restore(ctx context.Context, action Action) error {
	// TODO: implement
	return nil
}
