// Package remediation implements the OpsSweep resource deletion engine.
//
// The Remediator evaluates discovered resources against a confidence threshold
// and either previews (dry-run) or executes (live) the appropriate AWS deletion
// API calls. Dry-run mode is the default and must be explicitly overridden.
//
// Architecture:
//   - The Remediator is the only component in the codebase that makes mutating
//     AWS API calls. All read-only discovery logic lives in [discovery].
//   - It calls [heuristics.Evaluate] and [pricing.CalculateMonthlyWaste]
//     internally, so callers only need to pass raw [discovery.Resource] slices.
//   - Each resource type will have its own deletion method (e.g. deleteEIP,
//     deleteEBSVolume) added in subsequent commits. This file establishes the
//     framework and dry-run path.
package remediation

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/anirudh/opssweep/internal/discovery"
	"github.com/anirudh/opssweep/internal/heuristics"
	"github.com/anirudh/opssweep/internal/pricing"
)

// remediationConfidenceThreshold is the minimum heuristics confidence score a
// resource must reach to be considered for deletion. 0.90 is deliberately high:
// only resources with near-certain idle signals (unattached EIPs at 1.0, zombie
// NAT gateways at 0.95, zombie EC2 at 0.85 after age bump) are included.
//
// Resources scoring between the report threshold (0.50) and this threshold are
// shown in the waste report for user awareness but are never auto-deleted.
const remediationConfidenceThreshold = 0.90

// ANSI escape codes used for terminal colouring.
// These are intentionally kept as package-level constants rather than pulling
// in a colour library — the remediation output is a single line format and does
// not need the full weight of charmbracelet/lipgloss.
const (
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
)

// Remediator evaluates idle resources and executes (or previews) their deletion.
type Remediator struct {
	cfg aws.Config
}

// NewRemediator constructs a Remediator backed by the provided AWS config.
// The config is used to construct per-service, per-region clients when live
// deletion calls are made. In dry-run mode the config is loaded but never used
// to make mutating API calls.
func NewRemediator(cfg aws.Config) *Remediator {
	return &Remediator{cfg: cfg}
}

// Process evaluates each resource in the slice, filters to those meeting the
// [remediationConfidenceThreshold], and either previews or executes deletion.
//
// # Dry-run mode (isDryRun == true)
//
// Prints a yellow warning line for each qualifying resource:
//
//	[DRY RUN] Would delete ec2:elastic-ip: eipalloc-0abc123 (Waste: $3.60/mo)
//
// No AWS API calls are made. This is the safe default — the caller must
// explicitly pass isDryRun=false to trigger live deletions.
//
// # Live mode (isDryRun == false)
//
// Live deletion calls will be added per resource type in subsequent commits.
// Until a resource type has a concrete deletion implementation, Process falls
// back to the dry-run output with a "[NOT YET IMPLEMENTED]" suffix so the
// operator always knows what would happen.
//
// # Filtering
//
// Resources are scored using [heuristics.Evaluate] with [heuristics.DefaultConfig].
// Resources with ShouldSkip=true (protected tags) are excluded regardless of
// confidence. Resources below [remediationConfidenceThreshold] are excluded.
func (r *Remediator) Process(
	ctx context.Context,
	resources []discovery.Resource,
	isDryRun bool,
) error {
	cfg := heuristics.DefaultConfig()

	var queued int // count of resources that met the threshold

	for _, res := range resources {
		// Respect context cancellation — a Ctrl+C during remediation should
		// stop processing immediately without leaving a half-deleted state.
		if ctx.Err() != nil {
			return fmt.Errorf("remediation cancelled: %w", ctx.Err())
		}

		score := heuristics.Evaluate(res, cfg)

		// Hard skip: protection tag present — never delete, no output.
		if score.ShouldSkip {
			continue
		}

		// Soft skip: confidence below remediation threshold.
		if score.Confidence < remediationConfidenceThreshold {
			continue
		}

		queued++
		waste := pricing.CalculateMonthlyWaste(res, true)

		if isDryRun {
			printDryRun(res, waste)
		} else {
			if err := r.deleteResource(ctx, res, waste); err != nil {
				// Log the error and continue rather than aborting the entire
				// run. A single failed deletion (e.g. a race condition where
				// the resource was already deleted) should not block the
				// remaining queue.
				fmt.Fprintf(os.Stderr,
					"%sERROR%s: failed to delete %s %s: %v\n",
					ansiRed, ansiReset, res.Type, res.ID, err,
				)
			}
		}
	}

	if queued == 0 {
		fmt.Fprintln(os.Stdout, "No resources met the remediation threshold. Nothing to delete.")
	}

	return nil
}

// printDryRun writes a single yellow dry-run preview line to stdout.
// Format: [DRY RUN] Would delete <type>: <id> (Waste: $<cost>/mo)
func printDryRun(res discovery.Resource, waste float64) {
	fmt.Fprintf(os.Stdout,
		"%s%s[DRY RUN]%s Would delete %s: %s (Waste: $%.2f/mo)\n",
		ansiBold, ansiYellow,
		ansiReset,
		res.Type,
		res.ID,
		waste,
	)
}

// deleteResource dispatches to the appropriate type-specific deletion method.
//
// Each case will be implemented in a dedicated file (e.g. delete_eip.go,
// delete_ebs.go) in subsequent commits. Until then, unimplemented types fall
// through to the stub which prints a dry-run line with a "[NOT YET IMPLEMENTED]"
// suffix so the operator is never silently misled.
func (r *Remediator) deleteResource(
	ctx context.Context,
	res discovery.Resource,
	waste float64,
) error {
	switch res.Type {
	case discovery.ResourceTypeElasticIP:
		return r.deleteElasticIP(ctx, res, waste)
	case discovery.ResourceTypeEBSVolume:
		return r.deleteEBSVolume(ctx, res, waste)
	case discovery.ResourceTypeNATGateway:
		return r.deleteNATGateway(ctx, res, waste)
	case discovery.ResourceTypeRDSInstance:
		return r.deleteRDSInstance(ctx, res, waste)
	// TODO: case discovery.ResourceTypeEC2Instance: return r.terminateEC2(ctx, res)

	default:
		// Deletion not yet implemented for this resource type.
		// Print a skip message so the operator knows this type isn't handled yet.
		fmt.Fprintf(os.Stdout,
			"%s[SKIP]%s Teardown not yet implemented for %s\n",
			ansiYellow,
			ansiReset,
			res.Type,
		)
		return nil
	}
}

// deleteEBSVolume deletes an unattached EBS volume using the DeleteEBSVolume function.
// On success, prints a green confirmation line to stdout.
// On failure, returns an error which the caller will log to stderr.
func (r *Remediator) deleteEBSVolume(
	ctx context.Context,
	res discovery.Resource,
	waste float64,
) error {
	if err := DeleteEBSVolume(ctx, r.cfg, res.ID, res.Region); err != nil {
		return err
	}

	// Print success message in green
	fmt.Fprintf(os.Stdout,
		"%s[TEARDOWN]%s Successfully deleted EBS volume %s in %s\n",
		ansiGreen,
		ansiReset,
		res.ID,
		res.Region,
	)

	return nil
}

// deleteElasticIP releases an unattached Elastic IP using the ReleaseElasticIP function.
// On success, prints a green confirmation line to stdout.
// On failure, returns an error which the caller will log to stderr.
func (r *Remediator) deleteElasticIP(
	ctx context.Context,
	res discovery.Resource,
	waste float64,
) error {
	if err := ReleaseElasticIP(ctx, r.cfg, res.ID, res.Region); err != nil {
		return err
	}

	// Print success message in green
	fmt.Fprintf(os.Stdout,
		"%s[TEARDOWN]%s Successfully released Elastic IP %s in %s\n",
		ansiGreen,
		ansiReset,
		res.ID,
		res.Region,
	)

	return nil
}

// deleteNATGateway deletes an idle NAT Gateway using the DeleteNatGateway function.
// On success, prints a green confirmation line to stdout.
// On failure, returns an error which the caller will log to stderr.
func (r *Remediator) deleteNATGateway(
	ctx context.Context,
	res discovery.Resource,
	waste float64,
) error {
	if err := DeleteNatGateway(ctx, r.cfg, res.ID, res.Region); err != nil {
		return err
	}

	// Print success message in green
	fmt.Fprintf(os.Stdout,
		"%s[TEARDOWN]%s Successfully deleted NAT Gateway %s in %s\n",
		ansiGreen,
		ansiReset,
		res.ID,
		res.Region,
	)

	return nil
}

// deleteRDSInstance deletes an idle RDS database instance using the DeleteRDSInstance function.
// On success, prints a green confirmation line to stdout.
// On failure, returns an error which the caller will log to stderr.
func (r *Remediator) deleteRDSInstance(
	ctx context.Context,
	res discovery.Resource,
	waste float64,
) error {
	if err := DeleteRDSInstance(ctx, r.cfg, res.ID, res.Region); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout,
		"%s[TEARDOWN]%s Successfully deleted RDS Database %s in %s\n",
		ansiGreen,
		ansiReset,
		res.ID,
		res.Region,
	)

	return nil
}
