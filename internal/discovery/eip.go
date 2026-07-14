package discovery

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// ─── Elastic IPs ──────────────────────────────────────────────────────────────

// GetElasticIPs returns every Elastic IP allocation visible to the calling
// account in the given region.
//
// DescribeAddresses is not paginated — the AWS API returns all allocations in a
// single response. This is safe because EIP allocations are bounded by the
// account quota (default: 5 per region, raiseable to ~300 via a service limit
// increase), so the payload is always small.
//
// An unassociated EIP (AssociationId == nil or empty) accrues a charge of
// $0.005/hr even when nothing is attached. The State field is set to
// "unattached" in that case so the heuristics engine can detect it with a
// simple string comparison rather than re-implementing the nil check.
func (s *AWSScanner) GetElasticIPs(ctx context.Context, region string) ([]Resource, error) {
	// Build a regional client by copying the base config and overriding the
	// region. Same thread-safe pattern used by all other scanner methods.
	regionalCfg := s.cfg.Copy()
	regionalCfg.Region = region
	client := ec2.NewFromConfig(regionalCfg)

	output, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		// No filters: return all allocations so nothing is silently hidden from
		// the heuristics engine.
	})
	if err != nil {
		return nil, fmt.Errorf("discovery: DescribeAddresses in %s: %w", region, err)
	}

	resources := make([]Resource, 0, len(output.Addresses))
	for _, addr := range output.Addresses {
		resources = append(resources, mapElasticIP(addr, region))
	}

	return resources, nil
}

// mapElasticIP converts an AWS SDK ec2types.Address into our normalised
// [Resource] struct.
//
// ID: AllocationId is preferred (present for all VPC-scoped EIPs). For legacy
// EC2-Classic EIPs that have no AllocationId, PublicIp is used as a fallback
// so the resource is never anonymous in the report.
//
// State: "unattached" when AssociationId is nil or empty — the resource is
// allocated but not serving any traffic, yet still billing. "attached" when
// the EIP is associated with an instance or network interface.
//
// CreationTime: DescribeAddresses does not expose an allocation timestamp, so
// this field is left at its zero value (time.Time{}). The age signal in the
// heuristics engine silently skips zero CreationTime values.
func mapElasticIP(addr ec2types.Address, region string) Resource {
	id := aws.ToString(addr.AllocationId)
	if id == "" {
		// EC2-Classic EIPs have no AllocationId. Use the public IP string as a
		// stable identifier so teardown and restore can reference the resource.
		id = aws.ToString(addr.PublicIp)
	}

	state := "attached"
	if aws.ToString(addr.AssociationId) == "" {
		state = "unattached"
	}

	return Resource{
		ID:     id,
		Type:   ResourceTypeElasticIP,
		Region: region,
		Name:   extractNameTag(addr.Tags),
		State:  state,
		Tags:   convertTags(addr.Tags),
		// CreationTime intentionally omitted — not available from DescribeAddresses.
	}
}
