// Package pricing provides cost estimates for AWS resources.
// For v1, it uses a static pricing snapshot (JSON) embedded directly into the
// binary via go:embed, avoiding slow live calls to the AWS Pricing API.
// The snapshot is refreshed periodically via `make update-pricing`.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/anirudh/opssweep/internal/discovery"
)

//go:embed data/prices.json
var pricesJSON []byte

// PriceEntry holds the monthly on-demand price for a resource configuration.
type PriceEntry struct {
	InstanceType string  `json:"instanceType"`
	Region       string  `json:"region"`
	OS           string  `json:"os,omitempty"`
	MonthlyUSD   float64 `json:"monthlyUSD"`
}

// Catalog is the in-memory pricing lookup table built from the embedded snapshot.
type Catalog struct {
	entries map[string]PriceEntry // key: "<region>/<instanceType>"
}

// Load parses the embedded prices.json and returns a ready-to-use Catalog.
func Load() (*Catalog, error) {
	var entries []PriceEntry
	if err := json.Unmarshal(pricesJSON, &entries); err != nil {
		return nil, fmt.Errorf("pricing: failed to parse embedded prices.json: %w", err)
	}

	catalog := &Catalog{entries: make(map[string]PriceEntry, len(entries))}
	for _, e := range entries {
		catalog.entries[e.Region+"/"+e.InstanceType] = e
	}
	return catalog, nil
}

// EstimateMonthly returns the estimated monthly USD cost for the given resource.
// Returns 0 and a nil error when no price data is available for the resource type.
// TODO: add lookup logic for EBS, EIP, RDS, NAT gateway, and load balancer pricing.
func (c *Catalog) EstimateMonthly(r discovery.Resource) (float64, error) {
	// TODO: implement per-resource-type cost lookup
	return 0, nil
}
