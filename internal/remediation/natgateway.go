package remediation

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// DeleteNatGateway deletes an idle NAT Gateway using the EC2 DeleteNatGateway API.
//
// This function is called only after the heuristics engine has determined with
// high confidence (0.95 for NAT Gateways with zero active connections over the
// lookback period) that the gateway is unused. The caller is responsible for
// ensuring isDryRun=false was explicitly set — this function performs the actual
// destructive deletion.
//
// # Regional configuration
//
// NAT Gateways are regional resources. The provided aws.Config may be a global
// config (e.g. us-west-2 default), but the NAT Gateway exists in a specific region.
// We use cfg.Copy() to create a region-specific config for the EC2 client,
// exactly like the discovery scanners and other deletion logic do.
//
// # Error handling
//
// If the deletion fails (e.g. the NAT Gateway was already deleted, or it became
// active between scan time and deletion time), the error is returned to the
// caller. The Remediator logs it and continues processing the remaining queue
// rather than aborting the entire run.
//
// # AWS Deletion Behavior
//
// DeleteNatGateway is an asynchronous operation. AWS marks the NAT Gateway as
// "deleting" and returns immediately. The actual deletion can take several
// minutes. This function returns success once AWS accepts the deletion request,
// not when the gateway is fully deleted.
func DeleteNatGateway(ctx context.Context, cfg aws.Config, natGatewayID string, region string) error {
	// Create a region-specific config copy. This is the standard pattern used
	// throughout the discovery package — it allows us to use a single base
	// config and override the region per API call.
	regionalCfg := cfg.Copy()
	regionalCfg.Region = region

	// Initialize the EC2 client for the target region.
	client := ec2.NewFromConfig(regionalCfg)

	// Call the DeleteNatGateway API. AWS will reject the deletion if the NAT
	// Gateway doesn't exist or is already being deleted. Both cases return an
	// error which we propagate to the caller.
	_, err := client.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{
		NatGatewayId: aws.String(natGatewayID),
	})

	if err != nil {
		return fmt.Errorf("EC2 DeleteNatGateway API failed: %w", err)
	}

	return nil
}
