// Package heuristics implements the idle-detection engine for OpsSweep.
// Rather than a binary used/unused check, it computes a weighted confidence
// score per resource using CloudWatch utilization metrics, structural signals,
// resource age, tag signals, and cost magnitude.
package heuristics

import (
	"context"

	"github.com/anirudh/opssweep/internal/discovery"
)

// Score represents the idle-confidence result for a single resource.
type Score struct {
	Resource          discovery.Resource
	IdleConfidence    float64 // 0.0 (definitely active) → 1.0 (definitely idle)
	Signals           []Signal
	ForcedKeep        bool    // true when a keep=true or env=prod tag is present
}

// Signal is a single contributing factor to the idle confidence score.
type Signal struct {
	Name        string
	Description string
	Weight      float64 // contribution to the final score
	Value       float64 // normalised 0.0–1.0 signal strength
}

// Config holds tunable parameters for the heuristics engine.
type Config struct {
	// LookbackDays is the CloudWatch metric window (default: 14).
	LookbackDays int
	// IdleThreshold is the minimum score to flag a resource (default: 0.6).
	IdleThreshold float64
}

// DefaultConfig returns the recommended default heuristics configuration.
func DefaultConfig() Config {
	return Config{
		LookbackDays:  14,
		IdleThreshold: 0.6,
	}
}

// Engine applies idle heuristics to a slice of discovered resources.
type Engine struct {
	cfg Config
}

// NewEngine creates a new heuristics Engine with the given configuration.
func NewEngine(cfg Config) *Engine {
	return &Engine{cfg: cfg}
}

// Score computes the idle confidence score for every resource in the list.
// Resources tagged keep=true or env=prod are marked ForcedKeep and excluded
// from the flagged set regardless of their utilization metrics.
// TODO: implement CloudWatch metric fetching and weighted scoring.
func (e *Engine) Score(ctx context.Context, resources []discovery.Resource) ([]Score, error) {
	// TODO: implement
	return nil, nil
}
