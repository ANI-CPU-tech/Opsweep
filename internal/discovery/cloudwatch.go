package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// ─── CloudWatch metric fetching ───────────────────────────────────────────────

// cwPeriodSeconds is the granularity of each CloudWatch data point.
//
// 86400 = one day (24 × 60 × 60). Daily aggregation is the right granularity
// for idle detection: we want to know whether the instance did any real work
// across an entire day, not whether it spiked for 5 minutes during a cron job.
//
// CloudWatch constraint: for a StartTime older than 15 days, the period must
// be a multiple of 300 s (5 min). For StartTime older than 63 days it must be
// a multiple of 3600 s (1 hr). 86400 satisfies all three tiers, so this value
// is safe for any lookback window up to the CloudWatch 15-month retention limit.
const cwPeriodSeconds int32 = 86400

// FetchEC2CPUUtilization queries CloudWatch for the average CPU utilisation of
// a single EC2 instance over the last `days` days and returns the overall mean
// across all returned daily data points.
//
// # Return values
//
//   - (avg, nil)  — data was available; avg is the mean daily CPU % (0.0–100.0).
//   - (0.0, nil)  — the API returned no data points for the window. This can
//     mean the instance was stopped the entire time, CloudWatch simply has no
//     data yet (newly launched), or detailed monitoring is not enabled. The
//     caller should treat 0.0 returned alongside a nil error as "no data",
//     which is distinct from "0% CPU confirmed" — use the [Resource]
//     CPUUtilizationPercent pointer field (nil vs *0.0) to record that
//     distinction in the struct.
//   - (0.0, err)  — the CloudWatch API call failed.
//
// # Rate limiting
//
// GetMetricStatistics is subject to CloudWatch API throttling (default: 400
// TPS shared across all GetMetric* calls per account). The runner.go concurrency
// layer will be augmented with a golang.org/x/time/rate limiter before this
// function is called at scale. For now, errors are wrapped and returned so the
// caller can decide whether to retry or skip.
//
// # Why GetMetricStatistics instead of GetMetricData?
//
// GetMetricData supports batch queries (multiple metrics in one call) and is
// generally preferred for large-scale monitoring. However, GetMetricStatistics
// is simpler, returns pre-aggregated Datapoint structs, and has no cost per
// metric for standard-resolution metrics — making it the right choice for a
// single-instance, single-metric lookup in a CLI context.
func FetchEC2CPUUtilization(
	ctx context.Context,
	cfg aws.Config,
	instanceID string,
	region string,
	days int,
) (float64, error) {
	if instanceID == "" {
		return 0, fmt.Errorf("cloudwatch: FetchEC2CPUUtilization called with empty instanceID")
	}
	if days <= 0 {
		return 0, fmt.Errorf("cloudwatch: days must be > 0, got %d", days)
	}

	// Build a regional CloudWatch client. Same pattern as the EC2 scanner:
	// copy the base config and override the region so this function is safe
	// to call concurrently from multiple goroutines targeting different regions.
	regionalCfg := cfg.Copy()
	regionalCfg.Region = region
	client := cloudwatch.NewFromConfig(regionalCfg)

	now := time.Now().UTC()
	startTime := now.Add(-time.Duration(days) * 24 * time.Hour)

	input := &cloudwatch.GetMetricStatisticsInput{
		// Namespace and MetricName identify the standard EC2 CPU metric.
		// These are AWS-defined constants — no typo risk from string literals
		// here because they are the canonical values documented in the AWS SDK.
		Namespace:  aws.String("AWS/EC2"),
		MetricName: aws.String("CPUUtilization"),

		// InstanceId dimension scopes the metric to one specific instance.
		// Without this dimension the call would return aggregate data across
		// all EC2 instances in the region, which is not what we want.
		Dimensions: []cwtypes.Dimension{
			{
				Name:  aws.String("InstanceId"),
				Value: aws.String(instanceID),
			},
		},

		StartTime:  aws.Time(startTime),
		EndTime:    aws.Time(now),
		Period:     aws.Int32(cwPeriodSeconds),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticAverage},
	}

	output, err := client.GetMetricStatistics(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("cloudwatch: GetMetricStatistics for %s in %s: %w",
			instanceID, region, err)
	}

	return averageDatapoints(output.Datapoints), nil
}

// averageDatapoints calculates the arithmetic mean of the Average field across
// all returned CloudWatch Datapoint values.
//
// CloudWatch returns one Datapoint per period bucket. With Period=86400 and a
// 14-day window, we get up to 14 data points. Averaging them gives a single
// "mean daily CPU%" figure that is easy to reason about and threshold against.
//
// Returns 0.0 when the slice is empty — the caller is responsible for
// distinguishing "no data returned" from "genuinely 0% CPU" by checking
// whether the returned float came from an empty slice.
func averageDatapoints(datapoints []cwtypes.Datapoint) float64 {
	if len(datapoints) == 0 {
		return 0.0
	}

	var sum float64
	var count int

	for _, dp := range datapoints {
		// dp.Average is *float64. Skip nil entries defensively — the SDK
		// should never return a Datapoint without an Average when
		// Statistics=["Average"] was requested, but nil-guarding here
		// prevents a panic if that assumption ever breaks.
		if dp.Average == nil {
			continue
		}
		sum += *dp.Average
		count++
	}

	if count == 0 {
		return 0.0
	}
	return sum / float64(count)
}
