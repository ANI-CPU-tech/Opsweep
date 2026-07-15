package discovery

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// ─── RDS instances ────────────────────────────────────────────────────────────

// GetRDSInstances returns every RDS database instance visible to the calling
// account in the given region.
//
// For each instance in the "available" state the method also fetches the
// average daily DatabaseConnections count from CloudWatch over the last
// [cwLookbackDays] days and stores it in [Resource.DatabaseConnections]. An
// instance with DatabaseConnections == 0 has had no client connections over the
// lookback window and is a strong candidate for removal.
//
// CloudWatch fetch failures are non-fatal: a warning is written to stderr and
// the field is left nil so the rest of the scan continues unaffected.
//
// Instances in non-available states (e.g. "stopped", "backing-up",
// "modifying") skip the CloudWatch call — they are either already inactive or
// in a transient state that does not warrant an idle flag.
//
// The method paginates automatically — AWS returns at most 100 DB instances per
// DescribeDBInstances page.
func (s *AWSScanner) GetRDSInstances(ctx context.Context, region string) ([]Resource, error) {
	regionalCfg := s.cfg.Copy()
	regionalCfg.Region = region
	client := rds.NewFromConfig(regionalCfg)

	var resources []Resource

	// DescribeDBInstancesPaginator handles the Marker-based pagination loop.
	paginator := rds.NewDescribeDBInstancesPaginator(client, &rds.DescribeDBInstancesInput{
		// No filters: return all instances so the heuristics engine sees the
		// full account picture.
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("discovery: DescribeDBInstances in %s: %w", region, err)
		}

		for _, db := range page.DBInstances {
			res := mapRDSInstance(db, region)

			// ── CloudWatch connections fetch ───────────────────────────────
			// Only "available" instances are actively billing and capable of
			// accepting connections. Instances in other states are skipped to
			// avoid wasting API quota on calls that produce no actionable signal.
			if res.State == "available" {
				avg, err := FetchRDSConnections(
					ctx, s.cfg, res.ID, region, cwLookbackDays,
				)
				if err != nil {
					// Non-fatal: log and leave DatabaseConnections nil.
					// Common causes: missing cloudwatch:GetMetricStatistics
					// permission, or the instance was just created and has
					// no CloudWatch history yet.
					fmt.Fprintf(os.Stderr,
						"warning: could not fetch connection metrics for RDS %s in %s: %v\n",
						res.ID, region, err,
					)
				} else {
					// Store a pointer even when avg == 0.0 so the heuristics
					// engine can distinguish "confirmed zero connections" from
					// "data not fetched" (nil).
					res.DatabaseConnections = aws.Float64(avg)
				}
			}

			resources = append(resources, res)
		}
	}

	return resources, nil
}

// mapRDSInstance converts an AWS SDK rdstypes.DBInstance into our normalised
// [Resource] struct.
//
// This function is a pure data mapper — it makes no network calls. CloudWatch
// enrichment is applied by the caller ([AWSScanner.GetRDSInstances]) after this
// function returns, keeping mapping logic side-effect-free and unit-testable.
//
// ID: DBInstanceIdentifier is the human-readable name assigned at creation
// (e.g. "my-prod-db"). It is always non-empty for an existing instance and is
// the correct key for CloudWatch dimension lookups.
//
// State: DBInstanceStatus is a plain *string (not a typed enum in the v2 SDK),
// so it is safely dereferenced via aws.ToString.
//
// Tags: RDS tag lists use rdstypes.Tag rather than ec2types.Tag, so we cannot
// reuse convertTags/extractNameTag directly — see convertRDSTags below.
//
// CreationTime: InstanceCreateTime is *time.Time; safely dereferenced via
// aws.ToTime (returns time.Time{} on nil).
func mapRDSInstance(db rdstypes.DBInstance, region string) Resource {
	return Resource{
		ID:           aws.ToString(db.DBInstanceIdentifier),
		Type:         ResourceTypeRDSInstance,
		Region:       region,
		Name:         extractRDSNameTag(db.TagList),
		State:        aws.ToString(db.DBInstanceStatus),
		Tags:         convertRDSTags(db.TagList),
		CreationTime: aws.ToTime(db.InstanceCreateTime),
		// DatabaseConnections is intentionally left nil here.
		// It is populated by GetRDSInstances after this mapper returns.
	}
}

// ─── RDS tag helpers ──────────────────────────────────────────────────────────
// RDS uses rdstypes.Tag (Key/Value *string fields) rather than ec2types.Tag,
// so we need RDS-specific variants of the generic tag helpers in scanner.go.

// convertRDSTags converts an RDS []rdstypes.Tag slice into the plain
// map[string]string used by [Resource].
func convertRDSTags(tags []rdstypes.Tag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return m
}

// extractRDSNameTag returns the value of the "Name" tag from an RDS tag list,
// or an empty string if the tag is absent.
func extractRDSNameTag(tags []rdstypes.Tag) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == "Name" {
			return aws.ToString(t.Value)
		}
	}
	return ""
}
