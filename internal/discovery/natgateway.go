package discovery

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// ─── NAT Gateways ─────────────────────────────────────────────────────────────

// GetNATGateways returns every NAT Gateway visible to the calling account in
// the given region.
//
// For each gateway in the "available" state the method also fetches the average
// daily ActiveConnectionCount from CloudWatch over the last [cwLookbackDays]
// days and stores it in [Resource.ConnectionCount]. A gateway with
// ConnectionCount == 0 has handled zero traffic over the lookback window and
// is a strong candidate for removal.
//
// CloudWatch fetch failures are non-fatal: a warning is written to stderr and
// the field is left nil so the rest of the scan continues unaffected.
//
// Non-available gateways (e.g. "deleting", "deleted", "pending") skip the
// CloudWatch call — they are already leaving service and do not need to be
// flagged.
//
// The method paginates automatically — AWS returns at most 1,000 NAT Gateways
// per DescribeNatGateways page.
func (s *AWSScanner) GetNATGateways(ctx context.Context, region string) ([]Resource, error) {
	regionalCfg := s.cfg.Copy()
	regionalCfg.Region = region
	client := ec2.NewFromConfig(regionalCfg)

	var resources []Resource

	// DescribeNatGatewaysPaginator handles the NextToken loop.
	paginator := ec2.NewDescribeNatGatewaysPaginator(client, &ec2.DescribeNatGatewaysInput{
		// No filters: return all states so the heuristics engine sees the
		// full picture. We only call CloudWatch for "available" gateways, but
		// we still surface other states in case they are billing anomalies.
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("discovery: DescribeNatGateways in %s: %w", region, err)
		}

		for _, ngw := range page.NatGateways {
			res := mapNATGateway(ngw, region)

			// ── CloudWatch connection fetch ────────────────────────────────
			// Only "available" gateways are actively billing and capable of
			// routing traffic. Gateways in any other state are already
			// leaving service; fetching metrics for them wastes API quota
			// and produces no actionable signal.
			if res.State == "available" {
				avg, err := FetchNatGatewayConnections(
					ctx, s.cfg, res.ID, region, cwLookbackDays,
				)
				if err != nil {
					// Non-fatal: log and leave ConnectionCount nil.
					// The heuristics engine will skip the connection-count
					// rule for this resource and rely on other signals.
					fmt.Fprintf(os.Stderr,
						"warning: could not fetch connection metrics for %s in %s: %v\n",
						res.ID, region, err,
					)
				} else {
					// Store a pointer — even if avg == 0.0 — so the
					// heuristics engine can distinguish "confirmed zero
					// connections" from "data not fetched" (nil).
					res.ConnectionCount = aws.Float64(avg)
				}
			}

			resources = append(resources, res)
		}
	}

	return resources, nil
}

// mapNATGateway converts an AWS SDK ec2types.NatGateway into our normalised
// [Resource] struct.
//
// This function is a pure data mapper — it makes no network calls. CloudWatch
// enrichment is applied by the caller ([AWSScanner.GetNATGateways]) after this
// function returns, keeping mapping logic side-effect-free and unit-testable.
//
// State: ec2types.NatGatewayState is a typed string alias; it is cast directly
// to string so no other package needs to import the SDK types.
//
// Tags: NAT Gateways use ec2types.TagSpecification at creation but expose
// their tags as []ec2types.Tag on the describe response — the same type used
// by EC2 instances and EBS volumes, so convertTags and extractNameTag apply
// without adaptation.
//
// CreationTime: CreateTime is *time.Time and is safely dereferenced via
// aws.ToTime (returns time.Time{} on nil).
func mapNATGateway(ngw ec2types.NatGateway, region string) Resource {
	return Resource{
		ID:           aws.ToString(ngw.NatGatewayId),
		Type:         ResourceTypeNATGateway,
		Region:       region,
		Name:         extractNameTag(ngw.Tags),
		State:        string(ngw.State),
		Tags:         convertTags(ngw.Tags),
		CreationTime: aws.ToTime(ngw.CreateTime),
		// ConnectionCount is intentionally left nil here.
		// It is populated by GetNATGateways after this mapper returns.
	}
}
