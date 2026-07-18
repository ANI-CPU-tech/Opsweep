package remediation

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// DeleteEBSVolume deletes an unattached EBS volume using the AWS EC2 API.
//
// This function is called only after the heuristics engine has verified that
// the volume meets the deletion threshold (typically confidence >= 0.90 for
// unattached volumes). It does not re-check the volume state — the caller is
// responsible for ensuring the volume is safe to delete.
//
// # Regional configuration
//
// The function creates a regional copy of the provided AWS config using
// cfg.Copy() and overrides the region, ensuring the EC2 client targets the
// correct region regardless of the default region in the base config.
//
// # Error handling
//
// Returns an error if the DeleteVolume API call fails. Common failure modes:
//   - VolumeNotFound: the volume was already deleted (possible race condition)
//   - InvalidVolume.InUse: the volume became attached between the heuristics
//     check and the deletion call (the caller should log this as a warning,
//     not a hard failure, since the volume is now in use).
//
// The caller is responsible for logging errors and deciding whether to
// continue processing the deletion queue or abort.
func DeleteEBSVolume(
	ctx context.Context,
	cfg aws.Config,
	volumeID string,
	region string,
) error {
	// Create a regional copy of the config. This ensures the EC2 client
	// targets the correct region regardless of the default region set at
	// the top level (which might be us-east-1 or any other region).
	regionalCfg := cfg.Copy()
	regionalCfg.Region = region

	// Initialize the EC2 client for the target region.
	client := ec2.NewFromConfig(regionalCfg)

	// Call the DeleteVolume API. This is a synchronous operation — if it
	// succeeds, the volume is immediately queued for deletion and will be
	// removed within minutes.
	_, err := client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{
		VolumeId: aws.String(volumeID),
	})

	if err != nil {
		return fmt.Errorf("DeleteVolume API call failed: %w", err)
	}

	return nil
}
