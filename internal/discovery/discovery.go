// Package discovery enumerates AWS resources across all enabled regions
// using the AWS SDK v2. It covers EC2 instances, EBS volumes and snapshots,
// Elastic IPs, load balancers (ALB/NLB/CLB), RDS instances, and NAT gateways.
// Scanning is performed concurrently across regions using goroutines with a
// client-side rate limiter to avoid API throttling.
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// ResourceType enumerates the AWS resource types that OpsSweep can discover.
type ResourceType string

const (
	ResourceTypeEC2Instance   ResourceType = "ec2:instance"
	ResourceTypeEBSVolume     ResourceType = "ec2:ebs-volume"
	ResourceTypeEBSSnapshot   ResourceType = "ec2:ebs-snapshot"
	ResourceTypeElasticIP     ResourceType = "ec2:elastic-ip"
	ResourceTypeALB           ResourceType = "elasticloadbalancing:alb"
	ResourceTypeNLB           ResourceType = "elasticloadbalancing:nlb"
	ResourceTypeCLB           ResourceType = "elasticloadbalancing:clb"
	ResourceTypeRDSInstance   ResourceType = "rds:instance"
	ResourceTypeNATGateway    ResourceType = "ec2:nat-gateway"
)

// Resource represents a discovered AWS resource with its metadata.
type Resource struct {
	ID           string
	Type         ResourceType
	Region       string
	Name         string            // from the Name tag if present
	Tags         map[string]string // all resource tags
	CreatedAt    string            // RFC3339 timestamp
	StateRaw     string            // raw state string from AWS (e.g. "available", "in-use")
	RawMetadata  map[string]any    // service-specific fields for heuristic scoring
}

// Scanner discovers resources across one or more AWS regions.
type Scanner struct {
	cfg     aws.Config
	regions []string
}

// NewScanner creates a Scanner using the provided AWS config.
// If regions is empty, the scanner will enumerate all enabled regions.
func NewScanner(cfg aws.Config, regions []string) *Scanner {
	return &Scanner{cfg: cfg, regions: regions}
}

// Scan runs resource discovery across all configured regions concurrently
// and returns the aggregated list of discovered resources.
// TODO: implement concurrent multi-region scanning with errgroup + rate limiter.
func (s *Scanner) Scan(ctx context.Context) ([]Resource, error) {
	// TODO: implement
	return nil, nil
}
