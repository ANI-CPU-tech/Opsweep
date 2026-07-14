package discovery

import (
	"context"
	"fmt"
	"os"

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

// cwLookbackDays is the default CloudWatch metric window used when fetching
// CPU utilisation for running EC2 instances. 14 days is long enough to smooth
// out weekend troughs and short-lived spikes while staying within the
// CloudWatch standard-resolution retention window.
const cwLookbackDays = 14

// GetEC2Instances returns every EC2 instance visible to the calling account in
// the given region, including stopped and terminated instances.
//
// For each instance in the "running" state, the method additionally fetches the
// average CPU utilisation over the last [cwLookbackDays] days from CloudWatch
// and stores it in [Resource.CPUUtilizationPercent]. This powers the zombie-
// instance detection in the heuristics engine.
//
// CloudWatch fetch failures are non-fatal: a warning is written to stderr and
// the field is left nil so the rest of the scan continues unaffected.
//
// Stopped and terminated instances skip the CloudWatch call entirely — they
// produce no CPU metrics, so the call would always return zero data points.
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
				res := mapEC2Instance(instance, region)

				// ── CloudWatch CPU fetch ──────────────────────────────────
				// Only running instances produce CPUUtilization metrics.
				// Stopped/terminated instances are skipped to avoid wasting
				// API quota on calls that will always return empty results.
				if res.State == "running" {
					avg, err := FetchEC2CPUUtilization(ctx, s.cfg, res.ID, region, cwLookbackDays)
					if err != nil {
						// Non-fatal: log a warning and leave the field nil.
						// Common causes: insufficient IAM permissions for
						// cloudwatch:GetMetricStatistics, or API rate limiting.
						// The heuristics engine handles nil gracefully by
						// relying on structural signals alone for this instance.
						fmt.Fprintf(os.Stderr,
							"warning: could not fetch CPU metrics for %s in %s: %v\n",
							res.ID, region, err,
						)
					} else {
						// CloudWatch returned data (even if avg == 0.0).
						// Store a pointer so the heuristics engine can
						// distinguish "confirmed 0% CPU" from "no data".
						res.CPUUtilizationPercent = aws.Float64(avg)
					}
				}

				resources = append(resources, res)
			}
		}
	}

	return resources, nil
}

// mapEC2Instance converts an AWS SDK EC2 Instance value into our normalised
// [Resource] struct. All SDK pointer dereferences are guarded with aws.ToString /
// aws.ToTime so a nil field never causes a panic.
//
// This function is a pure data mapper — it makes no network calls. CloudWatch
// enrichment is applied by the caller ([AWSScanner.GetEC2Instances]) after this
// function returns, so that the mapping logic stays testable in isolation.
func mapEC2Instance(i ec2types.Instance, region string) Resource {
	return Resource{
		ID:           aws.ToString(i.InstanceId),
		Type:         ResourceTypeEC2Instance,
		Region:       region,
		Name:         extractNameTag(i.Tags),
		State:        mapEC2State(i.State),
		Tags:         convertTags(i.Tags),
		CreationTime: aws.ToTime(i.LaunchTime),
		// CPUUtilizationPercent is intentionally left nil here.
		// It is populated by GetEC2Instances after this mapper returns,
		// keeping this function side-effect-free and unit-testable.
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

// ─── EBS volumes ──────────────────────────────────────────────────────────────

// GetEBSVolumes returns every EBS volume visible to the calling account in the
// given region.
//
// Volumes in the "available" state are unattached — they have no running
// instance to justify their cost and are the primary idle signal for EBS.
// All states are returned so the heuristics engine has the complete picture.
//
// The method paginates automatically — AWS returns at most 500 volumes per
// DescribeVolumes page.
func (s *AWSScanner) GetEBSVolumes(ctx context.Context, region string) ([]Resource, error) {
	regionalCfg := s.cfg.Copy()
	regionalCfg.Region = region
	client := ec2.NewFromConfig(regionalCfg)

	var resources []Resource

	// DescribeVolumesPaginator handles the NextToken loop for us.
	paginator := ec2.NewDescribeVolumesPaginator(client, &ec2.DescribeVolumesInput{
		// No filters: return all volumes. Filtering by state here would hide
		// "in-use" volumes attached to stopped instances — still worth flagging.
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("discovery: DescribeVolumes in %s: %w", region, err)
		}

		for _, volume := range page.Volumes {
			resources = append(resources, mapEBSVolume(volume, region))
		}
	}

	return resources, nil
}

// mapEBSVolume converts an AWS SDK ec2types.Volume into our normalised
// [Resource] struct.
//
// State is a value-type enum (ec2types.VolumeState) rather than a pointer, so
// it is cast directly to string without a nil guard.
// CreateTime is a *time.Time pointer and is safely dereferenced via aws.ToTime.
func mapEBSVolume(v ec2types.Volume, region string) Resource {
	return Resource{
		ID:           aws.ToString(v.VolumeId),
		Type:         ResourceTypeEBSVolume,
		Region:       region,
		Name:         extractNameTag(v.Tags),
		State:        string(v.State),
		Tags:         convertTags(v.Tags),
		CreationTime: aws.ToTime(v.CreateTime),
	}
}

// GetEBSSnapshots returns all EBS snapshots owned by the calling account in
// the given region.
// TODO: implement using ec2.NewDescribeSnapshotsPaginator with OwnerIds: ["self"].
func (s *AWSScanner) GetEBSSnapshots(ctx context.Context, region string) ([]Resource, error) {
	return nil, nil
}

// ─── Elastic IPs ──────────────────────────────────────────────────────────────

// GetElasticIPs returns every Elastic IP allocation visible to the calling
// account in the given region.
//
// DescribeAddresses is not paginated — the AWS API returns all EIPs in a single
// response. This is safe because EIP allocations are bounded by the account
// quota (default: 5 per region, raiseable to ~300), so the response is always
// small.
//
// An unassociated EIP (AssociationId == nil) accrues a small hourly charge
// ($0.005/hr as of 2024) even when nothing is attached. The state field is set
// to "unattached" in that case so the heuristics engine can detect it with a
// simple string comparison rather than re-implementing the nil check.
func (s *AWSScanner) GetElasticIPs(ctx context.Context, region string) ([]Resource, error) {
	regionalCfg := s.cfg.Copy()
	regionalCfg.Region = region
	client := ec2.NewFromConfig(regionalCfg)

	output, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		// No filters: return all allocations so nothing is silently hidden.
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
// ID preference: AllocationId is used when present (VPC-scoped EIPs always
// have one). For the rare EC2-Classic EIP that has no AllocationId, we fall
// back to the PublicIp string so the resource is never anonymous.
//
// State: derived from AssociationId — if nil or empty the EIP is unattached
// and billing without serving any traffic. "unattached" is the sentinel value
// the heuristics engine checks for.
//
// CreationTime: the DescribeAddresses API does not expose an allocation
// timestamp, so this field is left at its zero value (time.Time{}).
func mapElasticIP(addr ec2types.Address, region string) Resource {
	id := aws.ToString(addr.AllocationId)
	if id == "" {
		// EC2-Classic EIPs have no AllocationId; use the IP string as a
		// stable identifier so the resource can still be tracked and deleted.
		id = aws.ToString(addr.PublicIp)
	}

	state := "in-use"
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

// ─── Region enumeration ───────────────────────────────────────────────────────

// ListEnabledRegions returns the names of every AWS region that is enabled for
// the calling account.
//
// "Enabled" means opt-in-not-required (always-on commercial regions like
// us-east-1) or opted-in (manually enabled regions like ap-southeast-3).
// Regions with opt-in status "not-opted-in" are excluded automatically by the
// API when AllRegions is false (the default), so no additional filter is
// required.
//
// The EC2 client is constructed without overriding the region so it uses
// whatever region is set in the base config (typically the caller's default
// profile region, e.g. "us-east-1"). DescribeRegions is a global-scope call —
// the response is identical regardless of which regional endpoint is used.
func (s *AWSScanner) ListEnabledRegions(ctx context.Context) ([]string, error) {
	// Use the base config as-is — no regional override needed for this call.
	client := ec2.NewFromConfig(s.cfg)

	output, err := client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		// AllRegions defaults to false, which excludes "not-opted-in" regions.
		// We do not set it explicitly to keep the zero-value semantic clear.
	})
	if err != nil {
		return nil, fmt.Errorf("discovery: DescribeRegions: %w", err)
	}

	regions := make([]string, 0, len(output.Regions))
	for _, r := range output.Regions {
		regions = append(regions, aws.ToString(r.RegionName))
	}
	return regions, nil
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
