package remediation

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// ReleaseElasticIP releases an unattached Elastic IP using the EC2 ReleaseAddress API.
//
// This function is called only after the heuristics engine has determined with
// maximum confidence (1.0 for unattached EIPs) that the address is unused. The
// caller is responsible for ensuring isDryRun=false was explicitly set — this
// function performs the actual destructive release operation.
//
// # Regional configuration
//
// Elastic IPs are regional resources. The provided aws.Config may be a global
// config (e.g. us-west-2 default), but the EIP exists in a specific region.
// We use cfg.Copy() to create a region-specific config for the EC2 client,
// exactly like the discovery scanners and EBS deletion logic do.
//
// # Error handling
//
// If the release fails (e.g. the EIP was already released, or it became
// attached between scan time and deletion time), the error is returned to the
// caller. The Remediator logs it and continues processing the remaining queue
// rather than aborting the entire run.
func ReleaseElasticIP(ctx context.Context, cfg aws.Config, allocationID string, region string) error {
	// Create a region-specific config copy. This is the standard pattern used
	// throughout the discovery package — it allows us to use a single base
	// config and override the region per API call.
	regionalCfg := cfg.Copy()
	regionalCfg.Region = region

	// Initialize the EC2 client for the target region.
	client := ec2.NewFromConfig(regionalCfg)

	// Call the ReleaseAddress API. AWS will reject the release if the EIP
	// is currently attached to a resource or if it doesn't exist. Both cases
	// return an error which we propagate to the caller.
	_, err := client.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
		AllocationId: aws.String(allocationID),
	})

	if err != nil {
		return fmt.Errorf("EC2 ReleaseAddress API failed: %w", err)
	}

	return nil
}
