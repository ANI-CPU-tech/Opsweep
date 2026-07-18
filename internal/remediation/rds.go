package remediation

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// DeleteRDSInstance deletes an idle RDS database instance using the
// RDS DeleteDBInstance API.
//
// This function is called only after the heuristics engine has determined with
// high confidence (0.95 for databases with zero connections over the lookback
// period) that the instance is unused. The caller is responsible for ensuring
// isDryRun=false was explicitly set — this function performs the actual
// destructive deletion.
//
// # SkipFinalSnapshot
//
// SkipFinalSnapshot is set to true unconditionally. AWS requires this field to
// be explicitly set when no final snapshot is desired — without it, the API
// returns a validation error unless FinalDBSnapshotIdentifier is also provided.
// For OpsSweep's use case (confirmed-idle databases), skipping the snapshot is
// the correct behaviour. If the operator wants a snapshot, they should handle
// it before running teardown.
//
// # Regional configuration
//
// RDS instances are regional resources. The provided aws.Config may be a global
// config (e.g. us-west-2 default), but the instance exists in a specific
// region. We use cfg.Copy() to create a region-specific config for the RDS
// client, exactly like the discovery scanners and other deletion logic do.
//
// # Error handling
//
// If the deletion fails (e.g. the instance was already deleted, or it is in a
// state that cannot be deleted such as "creating" or "modifying"), the error is
// returned to the caller. The Remediator logs it and continues processing the
// remaining queue rather than aborting the entire run.
//
// # AWS Deletion Behavior
//
// DeleteDBInstance is an asynchronous operation. AWS marks the instance as
// "deleting" and returns immediately. The actual deletion can take several
// minutes. This function returns success once AWS accepts the deletion request,
// not when the instance is fully removed.
func DeleteRDSInstance(ctx context.Context, cfg aws.Config, dbIdentifier string, region string) error {
	// Create a region-specific config copy. This is the standard pattern used
	// throughout the discovery package — it allows us to use a single base
	// config and override the region per API call.
	regionalCfg := cfg.Copy()
	regionalCfg.Region = region

	// Initialize the RDS client for the target region.
	client := rds.NewFromConfig(regionalCfg)

	// Call the DeleteDBInstance API.
	// SkipFinalSnapshot must be true — without it AWS will return a
	// DBInstanceSnapshotQuotaExceeded or validation error unless a
	// FinalDBSnapshotIdentifier is provided alongside.
	_, err := client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(dbIdentifier),
		SkipFinalSnapshot:    aws.Bool(true),
	})

	if err != nil {
		return fmt.Errorf("RDS DeleteDBInstance API failed: %w", err)
	}

	return nil
}
