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

// FetchNatGatewayConnections queries CloudWatch for the ActiveConnectionCount
// metric of a single NAT Gateway over the last `days` days.
//
// ActiveConnectionCount is a Sum statistic — AWS emits the total number of
// active connections observed during each period bucket. With Period=86400 we
// get one data point per day representing the daily connection total. We then
// average those daily sums to produce a single "mean daily connections" figure.
//
// # Return values
//
//   - (avg, nil)  — data available; avg is the mean daily connection count.
//   - (0.0, nil)  — no data points returned for the window. This typically
//     means the NAT gateway had zero traffic (CloudWatch omits zero-value
//     data points for Sum metrics). The caller should store aws.Float64(0.0)
//     to distinguish "confirmed zero" from "fetch not attempted" (nil).
//   - (0.0, err)  — the CloudWatch API call failed.
//
// # Why Sum instead of Average?
//
// ActiveConnectionCount is published by AWS as a Sum metric, not an Average.
// Requesting Statistics=["Average"] on a Sum metric returns per-sample averages
// within the period, which is misleading for connection counting. Sum gives us
// the total connections seen per day, which is what we need to identify a
// gateway that handled zero traffic.
func FetchNatGatewayConnections(
	ctx context.Context,
	cfg aws.Config,
	natGatewayID string,
	region string,
	days int,
) (float64, error) {
	if natGatewayID == "" {
		return 0, fmt.Errorf("cloudwatch: FetchNatGatewayConnections called with empty natGatewayID")
	}
	if days <= 0 {
		return 0, fmt.Errorf("cloudwatch: days must be > 0, got %d", days)
	}

	regionalCfg := cfg.Copy()
	regionalCfg.Region = region
	client := cloudwatch.NewFromConfig(regionalCfg)

	now := time.Now().UTC()
	startTime := now.Add(-time.Duration(days) * 24 * time.Hour)

	input := &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/NATGateway"),
		MetricName: aws.String("ActiveConnectionCount"),

		// NatGatewayId dimension scopes the metric to a specific gateway.
		Dimensions: []cwtypes.Dimension{
			{
				Name:  aws.String("NatGatewayId"),
				Value: aws.String(natGatewayID),
			},
		},

		StartTime:  aws.Time(startTime),
		EndTime:    aws.Time(now),
		Period:     aws.Int32(cwPeriodSeconds),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
	}

	output, err := client.GetMetricStatistics(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("cloudwatch: GetMetricStatistics (ActiveConnectionCount) for %s in %s: %w",
			natGatewayID, region, err)
	}

	return sumDatapoints(output.Datapoints), nil
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

// sumDatapoints calculates the arithmetic mean of the Sum field across all
// returned CloudWatch Datapoint values.
//
// Used for metrics published as Sum statistics (e.g. ActiveConnectionCount).
// Each data point holds the total count for its period bucket; averaging them
// gives "mean daily total", which is the right signal for idle detection:
// a gateway with a mean daily sum of 0 had zero connections every single day.
//
// Returns 0.0 when the slice is empty. CloudWatch omits data points for Sum
// metrics when the value is zero for an entire period, so an empty slice is
// itself evidence of zero activity — the caller should record aws.Float64(0.0)
// rather than nil in the Resource field to capture that distinction.
func sumDatapoints(datapoints []cwtypes.Datapoint) float64 {
	if len(datapoints) == 0 {
		return 0.0
	}

	var total float64
	var count int

	for _, dp := range datapoints {
		// dp.Sum is *float64; skip nil entries defensively.
		if dp.Sum == nil {
			continue
		}
		total += *dp.Sum
		count++
	}

	if count == 0 {
		return 0.0
	}
	return total / float64(count)
}
