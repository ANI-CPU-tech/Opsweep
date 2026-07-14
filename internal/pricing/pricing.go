// Package pricing provides cost estimates for AWS resources.
//
// # Two-tier pricing model
//
// Tier 1 — Static heuristic model ([CalculateMonthlyWaste]):
// A fast, zero-latency estimate suitable for CLI output. Uses hardcoded
// representative prices derived from the AWS public pricing page. No network
// call is made; no API key is required. This is the default for the scan
// report because users care about relative magnitude ("$340/mo vs $3.60/mo")
// more than per-cent accuracy at the discovery stage.
//
// Tier 2 — Embedded pricing catalog ([Catalog], [Load]):
// A static JSON snapshot of the AWS Bulk Pricing API, refreshed periodically
// via `make update-pricing` and embedded into the binary via go:embed. This
// gives per-instance-type, per-region accuracy without the ~2–5 second latency
// of a live API call. Use [Catalog.EstimateMonthly] when accurate per-resource
// estimates are needed (e.g. the detailed HTML report).
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/anirudh/opssweep/internal/discovery"
)

// ─── Static prices (Tier 1) ───────────────────────────────────────────────────
//
// These constants are representative monthly costs derived from the AWS public
// pricing page (us-east-1, on-demand, as of 2024). They are intentionally
// round numbers — the goal is fast triage, not billing accuracy.
//
// Sources:
//   - EIP: $0.005/hr × 730 hr/mo = $3.65 → rounded to $3.60
//   - EBS gp3: $0.08/GB-mo × 50 GB (assumed average) = $4.00
//   - t3.medium on-demand Linux: ~$0.0416/hr × 730 hr/mo ≈ $30.37 → $30.00
//   - Stopped EC2: $0.00 compute (AWS does not charge for stopped compute;
//     the attached root EBS volume is accounted for under ResourceTypeEBSVolume)

const (
	staticEIPMonthly         = 3.60  // unattached Elastic IP
	staticEBSVolumeMonthly   = 4.00  // unattached 50 GB gp3 volume (representative)
	staticEC2RunningMonthly  = 30.00 // idle but running t3.medium (representative)
	staticEC2StoppedMonthly  = 0.00  // stopped EC2 — no compute charge
)

// CalculateMonthlyWaste returns the estimated monthly USD cost being wasted by
// an idle resource.
//
// # Design rationale
//
// This function is a deliberately simple, static heuristic model. It trades
// per-cent accuracy for zero latency: no HTTP call, no JSON parse, no region
// lookup. The intent is to surface the order-of-magnitude waste immediately in
// the terminal table so users can triage ("$340/mo vs $3.60/mo") without
// waiting for a live pricing API round-trip.
//
// Rules:
//   - If isIdle is false, returns 0.00 — non-idle resources are not wasted spend.
//   - EIP unattached:     $3.60/mo  (based on $0.005/hr × 730 hr)
//   - EBS available:      $4.00/mo  (based on 50 GB gp3 at $0.08/GB-mo)
//   - EC2 stopped:        $0.00/mo  (AWS charges $0 compute for stopped instances;
//     the root EBS cost is captured separately via the EBS volume entry)
//   - EC2 running (idle): $30.00/mo (based on t3.medium on-demand Linux baseline)
//   - All other types:    $0.00/mo  (not yet modelled; conservative default)
func CalculateMonthlyWaste(res discovery.Resource, isIdle bool) float64 {
	if !isIdle {
		return 0.00
	}

	switch res.Type {

	case discovery.ResourceTypeElasticIP:
		// An unattached EIP is the clearest form of waste: AWS charges the
		// full hourly rate specifically to discourage hoarding of IP addresses.
		if res.State == "unattached" {
			return staticEIPMonthly
		}

	case discovery.ResourceTypeEBSVolume:
		// An "available" EBS volume is unattached — no instance is using it.
		// The $4.00 figure assumes 50 GB gp3; actual cost scales with volume
		// size, which will be addressed in the Catalog lookup (Tier 2).
		if res.State == "available" {
			return staticEBSVolumeMonthly
		}

	case discovery.ResourceTypeEC2Instance:
		switch res.State {
		case "stopped":
			// AWS does not charge for stopped compute. The attached root EBS
			// volume appears as its own Resource and is costed separately.
			// Returning 0.00 here prevents double-counting.
			return staticEC2StoppedMonthly
		case "running":
			// A running instance flagged as idle is the most expensive form of
			// waste. $30.00 is the t3.medium on-demand baseline; real cost
			// depends on instance type (addressed in Tier 2 Catalog lookup).
			return staticEC2RunningMonthly
		}
	}

	// Conservative default for resource types and states not yet modelled.
	// Returning 0.00 rather than an error keeps the call-site simple and
	// ensures we never over-report waste.
	return 0.00
}

// ─── Embedded pricing catalog (Tier 2) ───────────────────────────────────────

//go:embed data/prices.json
var pricesJSON []byte

// PriceEntry holds the monthly on-demand price for a specific resource
// configuration in a specific region.
type PriceEntry struct {
	InstanceType string  `json:"instanceType"`
	Region       string  `json:"region"`
	OS           string  `json:"os,omitempty"`
	MonthlyUSD   float64 `json:"monthlyUSD"`
}

// Catalog is the in-memory pricing lookup table built from the embedded
// prices.json snapshot. Use [Load] to construct one.
type Catalog struct {
	entries map[string]PriceEntry // key: "<region>/<instanceType>"
}

// Load parses the embedded prices.json snapshot and returns a Catalog ready
// for per-resource lookups. The snapshot is compiled into the binary at build
// time via go:embed, so Load never makes a network call.
//
// Returns an error only if the embedded JSON is malformed, which indicates a
// broken build rather than a runtime condition.
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

// EstimateMonthly returns the precise monthly USD cost for the given resource
// using the embedded pricing snapshot. Returns 0.00 when no entry exists for
// the resource type, region, or instance type — the caller should fall back to
// [CalculateMonthlyWaste] in that case.
//
// TODO: implement per-resource-type lookup for EBS (GB-month), EIP (hourly),
// RDS (instance class), NAT gateway (hourly + data), and load balancers (LCU).
func (c *Catalog) EstimateMonthly(r discovery.Resource) (float64, error) {
	// TODO: implement
	return 0, nil
}
