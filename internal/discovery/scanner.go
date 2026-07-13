package discovery

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Compile-time assertion: AWSScanner must fully satisfy AWSScannerAPI.
// If a method is missing or has the wrong signature this line will not compile,
// giving a clear error at build time rather than a panic at runtime.
var _ AWSScannerAPI = (*AWSScanner)(nil)

// AWSScanner is the production implementation of [AWSScannerAPI].
//
// It holds only the base [aws.Config] — individual service clients are
// constructed per-call with a regional endpoint override. This means a single
// AWSScanner instance can safely scan multiple regions concurrently without any
// shared mutable state.
type AWSScanner struct {
	cfg aws.Config
}

// NewAWSScanner constructs an AWSScanner from a fully initialised [aws.Config].
//
// A typical caller loads the config via the standard credential chain:
//
//	cfg, err := config.LoadDefaultConfig(ctx,
//	    config.WithRegion("us-east-1"),
//	    config.WithSharedConfigProfile(profile),
//	)
//	scanner := discovery.NewAWSScanner(cfg)
func NewAWSScanner(cfg aws.Config) *AWSScanner {
	return &AWSScanner{cfg: cfg}
}

// ─── EC2 instances ────────────────────────────────────────────────────────────

// GetEC2Instances returns every EC2 instance visible to the calling account in
// the given region, including stopped and terminated instances.
//
// Stopped/terminated instances are intentionally included: a long-stopped
// instance still has an attached EBS root volume that is accruing charges, and
// the heuristics engine needs that signal.
//
// The method paginates automatically — AWS returns at most 1,000 instances per
// DescribeInstances page.
func (s *AWSScanner) GetEC2Instances(ctx context.Context, region string) ([]Resource, error) {
	// Build a regional client by copying the base config and overriding the
	// region. The base config is a value type so this copy is safe.
	regionalCfg := s.cfg.Copy()
	regionalCfg.Region = region
	client := ec2.NewFromConfig(regionalCfg)

	var resources []Resource

	// DescribeInstancesPaginator handles the NextToken loop for us.
	paginator := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{
		// No filters: we want everything so the heuristics engine has the full
		// picture. Filtering here would silently hide resources from the report.
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("discovery: DescribeInstances in %s: %w", region, err)
		}

		// A single DescribeInstances page nests instances under Reservations.
		// Each Reservation can contain multiple instances (e.g. launched together),
		// so we iterate both levels.
		for _, reservation := range page.Reservations {
			for _, instance := range reservation.Instances {
				resources = append(resources, mapEC2Instance(instance, region))
			}
		}
	}

	return resources, nil
}

// mapEC2Instance converts an AWS SDK EC2 Instance value into our normalised
// [Resource] struct. All SDK pointer dereferences are guarded with aws.ToString /
// aws.ToTime so a nil field never causes a panic.
func mapEC2Instance(i ec2types.Instance, region string) Resource {
	return Resource{
		ID:           aws.ToString(i.InstanceId),
		Type:         ResourceTypeEC2Instance,
		Region:       region,
		Name:         extractNameTag(i.Tags),
		State:        mapEC2State(i.State),
		Tags:         convertTags(i.Tags),
		CreationTime: aws.ToTime(i.LaunchTime),
	}
}

// mapEC2State extracts the human-readable state name from the nullable
// [ec2types.InstanceState] pointer returned by the SDK.
// Returns an empty string when the state is unavailable (e.g. for spot
// interruptions where the state field may be absent).
func mapEC2State(state *ec2types.InstanceState) string {
	if state == nil {
		return ""
	}
	// InstanceStateName is itself a typed string alias; cast to plain string
	// so the rest of the application has no dependency on the SDK types package.
	return string(state.Name)
}

// ─── Stub implementations ─────────────────────────────────────────────────────
// Each method below satisfies one entry in [AWSScannerAPI]. They return
// (nil, nil) — empty result, no error — which is a valid "no resources found"
// response. Implementations will be added in subsequent commits.

// GetEBSVolumes returns all EBS volumes in the given region.
// TODO: implement using ec2.NewDescribeVolumesPaginator.
func (s *AWSScanner) GetEBSVolumes(ctx context.Context, region string) ([]Resource, error) {
	return nil, nil
}

// GetEBSSnapshots returns all EBS snapshots owned by the calling account in
// the given region.
// TODO: implement using ec2.NewDescribeSnapshotsPaginator with OwnerIds: ["self"].
func (s *AWSScanner) GetEBSSnapshots(ctx context.Context, region string) ([]Resource, error) {
	return nil, nil
}

// GetElasticIPs returns all Elastic IP allocations in the given region.
// TODO: implement using ec2.DescribeAddresses (no pagination needed — EIPs are
// bounded by the account quota, typically ≤5 per region by default).
func (s *AWSScanner) GetElasticIPs(ctx context.Context, region string) ([]Resource, error) {
	return nil, nil
}

// GetLoadBalancers returns all ALB, NLB, and CLB load balancers in the given region.
// TODO: implement using elasticloadbalancingv2.NewDescribeLoadBalancersPaginator
// (ALB/NLB) and elasticloadbalancing.DescribeLoadBalancers (CLB).
func (s *AWSScanner) GetLoadBalancers(ctx context.Context, region string) ([]Resource, error) {
	return nil, nil
}

// GetRDSInstances returns all RDS database instances in the given region.
// TODO: implement using rds.NewDescribeDBInstancesPaginator.
func (s *AWSScanner) GetRDSInstances(ctx context.Context, region string) ([]Resource, error) {
	return nil, nil
}

// GetNATGateways returns all NAT gateways in the given region.
// TODO: implement using ec2.NewDescribeNatGatewaysPaginator.
func (s *AWSScanner) GetNATGateways(ctx context.Context, region string) ([]Resource, error) {
	return nil, nil
}

// ListEnabledRegions returns all AWS regions that are enabled for the calling
// account, used to drive the concurrent multi-region scan loop.
// TODO: implement using ec2.DescribeRegions.
func (s *AWSScanner) ListEnabledRegions(ctx context.Context) ([]string, error) {
	return nil, nil
}

// ─── Tag helpers ──────────────────────────────────────────────────────────────

// convertTags converts the SDK's []ec2types.Tag slice into the plain
// map[string]string used by [Resource]. Returns nil when the input is empty
// so callers can distinguish "no tags" from "empty tag set".
func convertTags(sdkTags []ec2types.Tag) map[string]string {
	if len(sdkTags) == 0 {
		return nil
	}
	tags := make(map[string]string, len(sdkTags))
	for _, t := range sdkTags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return tags
}

// extractNameTag returns the value of the "Name" tag from an SDK tag slice,
// or an empty string if the tag is absent. This is extracted separately from
// convertTags because Name is promoted to its own field on [Resource] for
// quick access without a map lookup.
func extractNameTag(sdkTags []ec2types.Tag) string {
	for _, t := range sdkTags {
		if aws.ToString(t.Key) == "Name" {
			return aws.ToString(t.Value)
		}
	}
	return ""
}
