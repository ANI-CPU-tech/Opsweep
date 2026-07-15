// Package heuristics implements the idle-detection engine for OpsSweep.
//
// Rather than a binary used/unused check, it computes a weighted confidence
// score per resource using structural state signals, CloudWatch utilization
// metrics, tag signals, and resource age.
//
// The central function is [Evaluate], which takes a single [discovery.Resource]
// and returns a [Score]. The [Engine] type wraps Evaluate with batch processing
// and a configurable idle threshold.
package heuristics

import (
	"fmt"
	"strings"
	"time"

	"github.com/anirudh/opssweep/internal/discovery"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	// maxConfidence is the hard ceiling on the confidence score.
	// Clamping to this value in clamp() prevents floating-point drift above 1.0.
	maxConfidence = 1.0

	// ageThresholdDays is the minimum resource age (in days) that triggers
	// the age-signal confidence bump.
	ageThresholdDays = 30

	// ageBump is the confidence increase applied when a resource has existed
	// for longer than ageThresholdDays.
	ageBump = 0.1

	// tempTagBump is the confidence increase applied when a resource carries
	// a tag that marks it as temporary or experimental (e.g. temp=true).
	tempTagBump = 0.2

	// zombieCPUThresholdPercent is the maximum average CPU utilisation (%) at
	// which a running EC2 instance is classified as a zombie — running but
	// doing no meaningful work. 2% is deliberately conservative: real workloads
	// (web servers, cron jobs, idle daemons) typically sit above 2–5%, while
	// truly forgotten instances hover near 0%.
	zombieCPUThresholdPercent = 2.0

	// zombieConfidence is the idle confidence score assigned to an EC2 instance
	// that passes the zombie CPU check. 0.85 is high but not 1.0 — we
	// acknowledge that CPU alone is not proof of abandonment (e.g. a
	// GPU-compute instance uses near-zero CPU by design).
	zombieConfidence = 0.85

	// activeRunningConfidence is the score forced onto a running EC2 instance
	// whose CPU utilisation is at or above zombieCPUThresholdPercent. Setting
	// it to 0.0 overrides any tag-based bumps that may have been applied
	// earlier, because measured activity is stronger evidence than a tag.
	activeRunningConfidence = 0.0

	// idleNATGatewayConfidence is the idle confidence score assigned to a NAT
	// Gateway that has had zero active connections over the entire lookback
	// window. 0.95 is very high — a gateway with no connections is almost
	// certainly forgotten — but not 1.0, because a brief maintenance window
	// or a rarely-used VPN might legitimately show zero connections over 14
	// days yet still be needed.
	idleNATGatewayConfidence = 0.95

	// idleRDSConfidence is the idle confidence score assigned to an RDS
	// instance that has had zero average connections over the entire lookback
	// window. 0.95 mirrors the NAT Gateway signal strength — a database with
	// no connections is almost certainly abandoned, but 1.0 would be too
	// aggressive given that some batch jobs run less frequently than 14 days.
	idleRDSConfidence = 0.95
)

// ─── Score ────────────────────────────────────────────────────────────────────

// Score is the result of evaluating a single resource through the heuristics
// engine. All fields are populated by [Evaluate]; callers should treat Score
// values as read-only.
type Score struct {
	// Resource is the discovery record that was evaluated.
	Resource discovery.Resource

	// IsIdle is true when the resource is considered idle — i.e. Confidence
	// meets or exceeds the caller's threshold and ShouldSkip is false.
	// Set by [Engine.EvaluateAll] based on the configured IdleThreshold.
	// [Evaluate] itself does not set this field; it only sets Confidence.
	IsIdle bool

	// Confidence is the probability (0.0–1.0) that the resource is idle and
	// safe to remove. 0.0 means definitely active; 1.0 means definitively idle.
	// The value is clamped to [0.0, 1.0] and never exceeds maxConfidence.
	Confidence float64

	// Reasons is an ordered list of human-readable explanations that describe
	// exactly which signals contributed to the final Confidence score. Useful
	// for the report layer and for user trust ("why is this flagged?").
	Reasons []string

	// ShouldSkip is true when the resource carries a protection tag
	// (keep=true, env=prod, env=production). Skip resources are excluded from
	// teardown consideration entirely — no exceptions.
	ShouldSkip bool
}

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds tunable parameters for the heuristics engine.
type Config struct {
	// IdleThreshold is the minimum Confidence score at which a resource is
	// considered idle and flagged for review. Default: 0.6.
	IdleThreshold float64

	// Now is the reference time used for age calculations. Defaults to
	// time.Now() when zero. Exposed for deterministic unit testing.
	Now time.Time
}

// DefaultConfig returns the recommended production configuration.
func DefaultConfig() Config {
	return Config{
		IdleThreshold: 0.6,
	}
}

// now returns the reference time, falling back to time.Now() when cfg.Now
// is the zero value. This keeps production behaviour correct while allowing
// tests to inject a fixed timestamp.
func (c Config) now() time.Time {
	if c.Now.IsZero() {
		return time.Now()
	}
	return c.Now
}

// ─── Engine ───────────────────────────────────────────────────────────────────

// Engine applies the heuristics scoring pipeline to a batch of resources.
type Engine struct {
	cfg Config
}

// NewEngine creates an Engine with the given configuration.
func NewEngine(cfg Config) *Engine {
	return &Engine{cfg: cfg}
}

// EvaluateAll scores every resource in the slice and returns the full list of
// Score results. Resources with ShouldSkip=true are included in the output
// (so the report layer can show them as "protected") but have IsIdle=false.
//
// The method is intentionally synchronous — the caller (runner.go) already
// parallelises across regions. There is no benefit to parallelising inside a
// single region's small resource list.
func (e *Engine) EvaluateAll(resources []discovery.Resource) []Score {
	scores := make([]Score, 0, len(resources))
	for _, res := range resources {
		s := Evaluate(res, e.cfg)
		// Apply the idle threshold to set the IsIdle convenience flag.
		if !s.ShouldSkip && s.Confidence >= e.cfg.IdleThreshold {
			s.IsIdle = true
		}
		scores = append(scores, s)
	}
	return scores
}

// ─── Evaluate ─────────────────────────────────────────────────────────────────

// Evaluate computes the idle Score for a single resource by running three
// signal evaluators in order:
//
//  1. [evaluateTags]      — tag-based overrides and bumps (runs first; a
//     protection tag causes an immediate return with ShouldSkip=true)
//  2. [evaluateStructure] — structural/state signals (EBS available, EIP
//     unattached, EC2 stopped)
//  3. [evaluateAge]       — age-based confidence bump
//
// The resulting Confidence is clamped to [0.0, 1.0] before returning.
// IsIdle is NOT set here — that is the responsibility of [Engine.EvaluateAll],
// which applies the configurable threshold.
func Evaluate(res discovery.Resource, cfg Config) Score {
	s := Score{Resource: res}

	// ── 1. Tag signals ────────────────────────────────────────────────────────
	// Tag evaluation runs first and may short-circuit the entire pipeline.
	if done := evaluateTags(res, &s); done {
		return s
	}

	// ── 2. Structural signals ─────────────────────────────────────────────────
	evaluateStructure(res, &s)

	// ── 3. Age signal ─────────────────────────────────────────────────────────
	evaluateAge(res, cfg, &s)

	// Clamp to [0.0, 1.0] — floating-point addition can drift above 1.0.
	s.Confidence = clamp(s.Confidence)

	return s
}

// ─── Signal evaluators ────────────────────────────────────────────────────────

// evaluateTags inspects the resource's tag map for two categories:
//
//   - Protection tags (keep=true, env=prod, env=production): sets ShouldSkip=true
//     and returns true to signal that evaluation should stop immediately.
//
//   - Temporary/experimental tags (temp=true, project=hackathon): adds a
//     tempTagBump to Confidence and continues evaluation (returns false).
//
// Tag keys and values are compared case-insensitively.
func evaluateTags(res discovery.Resource, s *Score) (skipEvaluation bool) {
	for rawKey, rawVal := range res.Tags {
		key := strings.ToLower(rawKey)
		val := strings.ToLower(rawVal)

		switch {
		// ── Protection tags — hard stop, no teardown ever ────────────────────
		case key == "keep" && val == "true":
			s.ShouldSkip = true
			s.Reasons = append(s.Reasons, "protected by tag keep=true")
			return true

		case key == "env" && (val == "prod" || val == "production"):
			s.ShouldSkip = true
			s.Reasons = append(s.Reasons, fmt.Sprintf("protected by tag env=%s", rawVal))
			return true

		// ── Temporary/hackathon tags — raise confidence ───────────────────────
		case key == "temp" && val == "true":
			s.Confidence += tempTagBump
			s.Reasons = append(s.Reasons, fmt.Sprintf("tag temp=true suggests disposable resource (+%.1f)", tempTagBump))

		case key == "project" && val == "hackathon":
			s.Confidence += tempTagBump
			s.Reasons = append(s.Reasons, fmt.Sprintf("tag project=hackathon suggests disposable resource (+%.1f)", tempTagBump))
		}
	}
	return false
}

// evaluateStructure applies resource-type-specific structural signals.
// These are the strongest non-tag signals because they reflect AWS-reported
// state rather than inferred behaviour.
//
// Signals applied:
//   - EBS volume in "available" state:        confidence 0.9  (unattached, billing for nothing)
//   - Elastic IP in "unattached" state:        confidence 1.0  (unambiguous waste — allocated,
//     not associated, and billing $0.005/hr with zero utility)
//   - EC2 instance "stopped":                 confidence 0.5  (no compute charge, but attached
//     EBS volumes continue to accrue storage charges)
//   - EC2 instance "running", CPU < 2%:       confidence 0.85 (zombie — powered on but idle)
//   - EC2 instance "running", CPU ≥ 2%:       confidence 0.0  (active — force-clear the score)
//   - EC2 instance "running", no CPU data:    no change       (insufficient data, stay neutral)
//   - NAT Gateway "available", connections=0: confidence 0.95 (idle gateway, fixed hourly charge)
//   - NAT Gateway "available", connections>0: no change       (actively routing traffic)
//   - NAT Gateway "available", no conn data:  no change       (insufficient data, stay neutral)
//   - RDS "available", connections=0:         confidence 0.95 (idle database, still billing)
//   - RDS "available", connections>0:         no change       (actively serving queries)
//   - RDS "available", no conn data:          no change       (insufficient data, stay neutral)
//
// The structural confidence replaces (rather than adds to) any prior confidence
// from tag bumps — but only when it is higher — so a hackathon-tagged stopped
// instance isn't pushed below its tag-derived score.
// The one exception is the "active running" case: confirmed activity hard-resets
// the score to 0.0 because measured utilisation is stronger evidence than any tag.
func evaluateStructure(res discovery.Resource, s *Score) {
	switch res.Type {

	case discovery.ResourceTypeEBSVolume:
		if res.State == "available" {
			applyIfHigher(s, 0.9, "EBS volume is unattached (state=available) — no instance is using it")
		}

	case discovery.ResourceTypeElasticIP:
		if res.State == "unattached" {
			applyIfHigher(s, 1.0,
				"unattached Elastic IP: allocated but not associated with any instance or network interface — billing with zero utility")
		}

	case discovery.ResourceTypeEC2Instance:
		evaluateEC2Structure(res, s)

	case discovery.ResourceTypeNATGateway:
		evaluateNATGatewayStructure(res, s)

	case discovery.ResourceTypeRDSInstance:
		evaluateRDSStructure(res, s)
	}
}

// evaluateEC2Structure handles the EC2-specific structural evaluation,
// separated from evaluateStructure for readability given its branching logic.
//
// Decision tree:
//
//	state == "stopped"
//	    → confidence 0.5 (structural idle signal, EBS still billing)
//
//	state == "running" AND CPUUtilizationPercent != nil
//	    cpu < zombieCPUThresholdPercent (2%)
//	        → confidence 0.85 (zombie: powered on, doing nothing)
//	    cpu >= zombieCPUThresholdPercent
//	        → confidence 0.0  (active: force-clear regardless of tag bumps)
//
//	state == "running" AND CPUUtilizationPercent == nil
//	    → no change (CloudWatch data unavailable; stay neutral, don't flag)
func evaluateEC2Structure(res discovery.Resource, s *Score) {
	switch res.State {

	case "stopped":
		applyIfHigher(s, 0.5,
			"EC2 instance is stopped — not running but attached EBS volumes continue to accrue charges")

	case "running":
		// CPUUtilizationPercent is nil when the CloudWatch fetch was skipped
		// or failed (e.g. insufficient IAM permissions). In that case we have
		// no utilisation evidence either way — leave the score unchanged so
		// the instance is neither falsely flagged nor falsely cleared.
		if res.CPUUtilizationPercent == nil {
			return
		}

		cpu := *res.CPUUtilizationPercent // safe: nil-checked above

		if cpu < zombieCPUThresholdPercent {
			// Zombie instance: running but using negligible CPU over the
			// entire lookback window. applyIfHigher is used so that a
			// stronger signal from another rule (e.g. a future network-IO
			// check) can still push the score higher.
			applyIfHigher(s, zombieConfidence,
				fmt.Sprintf(
					"zombie instance: CPU averaged %.2f%% over lookback period (threshold: <%.0f%%)",
					cpu, zombieCPUThresholdPercent,
				),
			)
		} else {
			// Active instance: measured CPU proves the instance is doing
			// real work. Hard-reset to 0.0, overriding any tag bumps.
			// This is the one place where we use a direct assignment rather
			// than applyIfHigher — we are asserting "definitely not idle",
			// not just "less likely to be idle".
			s.Confidence = activeRunningConfidence
			s.Reasons = append(s.Reasons,
				fmt.Sprintf(
					"instance is active: CPU averaged %.2f%% over lookback period (threshold: ≥%.0f%%)",
					cpu, zombieCPUThresholdPercent,
				),
			)
		}
	}
}

// evaluateNATGatewayStructure handles NAT Gateway idle detection.
//
// Decision tree:
//
//	state == "available" AND ConnectionCount != nil
//	    *ConnectionCount == 0
//	        → confidence 0.95 (idle: paying hourly, routing nothing)
//	    *ConnectionCount > 0
//	        → no change (actively routing — don't flag)
//
//	state == "available" AND ConnectionCount == nil
//	    → no change (CloudWatch data unavailable; stay neutral)
//
//	any other state ("deleting", "deleted", "pending", "failed")
//	    → no change (already leaving service; not actionable)
//
// Note: we use strict equality (*ConnectionCount == 0) rather than a threshold
// because ActiveConnectionCount is a Sum metric — even a single connection per
// day produces a non-zero value. Any non-zero sum means real traffic occurred.
func evaluateNATGatewayStructure(res discovery.Resource, s *Score) {
	if res.State != "available" {
		return
	}

	// ConnectionCount nil means the CloudWatch fetch was skipped or failed.
	// Stay neutral — don't flag or clear the score.
	if res.ConnectionCount == nil {
		return
	}

	if *res.ConnectionCount == 0 {
		applyIfHigher(s, idleNATGatewayConfidence,
			"idle NAT Gateway: 0 active connections over lookback period — paying hourly charge with no traffic routed",
		)
	}
	// Non-zero connections: gateway is routing traffic. Leave the score
	// unchanged rather than force-clearing it, because a tag bump (e.g.
	// temp=true) should still be visible to the user even for active gateways.
}

// evaluateRDSStructure handles RDS instance idle detection.
//
// Decision tree:
//
//	state == "available" AND DatabaseConnections != nil
//	    *DatabaseConnections == 0
//	        → confidence 0.95 (idle: paying hourly, zero clients connected)
//	    *DatabaseConnections > 0
//	        → no change (actively serving queries — don't flag)
//
//	state == "available" AND DatabaseConnections == nil
//	    → no change (CloudWatch data unavailable; stay neutral)
//
//	any other state ("stopped", "backing-up", "modifying", etc.)
//	    → no change (transient or already inactive; not actionable)
//
// We use strict equality (*DatabaseConnections == 0) rather than a threshold
// because DatabaseConnections is an Average statistic — even a single
// connection per day raises the average above zero. Any non-zero mean means
// real client activity occurred during the lookback window.
func evaluateRDSStructure(res discovery.Resource, s *Score) {
	if res.State != "available" {
		return
	}

	// DatabaseConnections nil means the CloudWatch fetch was skipped or failed.
	// Stay neutral — don't flag or clear the score.
	if res.DatabaseConnections == nil {
		return
	}

	if *res.DatabaseConnections == 0 {
		applyIfHigher(s, idleRDSConfidence,
			"idle RDS database: 0 average connections over lookback period — paying instance hours with no client activity",
		)
	}
	// Non-zero connections: database is serving queries. Leave the score
	// unchanged — same reasoning as evaluateNATGatewayStructure.
}

// evaluateAge adds ageBump to the confidence score when the resource's
// CreationTime is known and is older than ageThresholdDays. Older resources
// are more likely to be forgotten side-project leftovers.
//
// CreationTime zero-values (EIPs, resources where the API does not expose a
// creation timestamp) are silently skipped — no bump, no penalty.
func evaluateAge(res discovery.Resource, cfg Config, s *Score) {
	if res.CreationTime.IsZero() {
		return
	}

	age := cfg.now().Sub(res.CreationTime)
	threshold := time.Duration(ageThresholdDays) * 24 * time.Hour

	if age >= threshold {
		s.Confidence += ageBump
		days := int(age.Hours() / 24)
		s.Reasons = append(s.Reasons,
			fmt.Sprintf("resource is %d days old (older than %d-day threshold) (+%.1f)", days, ageThresholdDays, ageBump),
		)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// applyIfHigher sets s.Confidence to value and appends reason only when value
// exceeds the current Confidence. This prevents a structural signal from
// lowering a score that was already raised by tag bumps.
func applyIfHigher(s *Score, value float64, reason string) {
	if value > s.Confidence {
		s.Confidence = value
		s.Reasons = append(s.Reasons, reason)
	}
}

// clamp constrains v to the range [0.0, maxConfidence].
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > maxConfidence {
		return maxConfidence
	}
	return v
}
