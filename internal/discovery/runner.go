package discovery

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
)

// RunConcurrentScan is the top-level entry point for a full account scan.
//
// It drives the entire discovery pipeline:
//  1. Calls [AWSScannerAPI.ListEnabledRegions] to discover which regions to scan.
//  2. Spawns one goroutine per region using an [errgroup.Group].
//  3. Inside each goroutine, fetches EC2 instances, EBS volumes, Elastic IPs,
//     and NAT Gateways in parallel sub-goroutines within that region.
//  4. Merges all results into a single []Resource slice and returns it.
//
// # Concurrency model
//
// The outer errgroup spawns N goroutines — one per region. Each goroutine then
// spawns its own inner errgroup with one goroutine per resource type. This two-
// level fan-out means a 17-region scan with 4 active resource types issues up
// to 68 concurrent API calls. The AWS SDK has built-in retry logic; a separate
// rate limiter (golang.org/x/time/rate) will be wired in once all resource
// types are implemented.
//
// # Error handling
//
// errgroup propagates the first non-nil error and cancels the shared context so
// all in-flight goroutines see ctx.Err() != nil and stop promptly. The caller
// receives that first error; partial results are discarded. This is intentional:
// returning a half-scanned result set to the user is more dangerous than asking
// them to re-run with corrected credentials or permissions.
//
// # Thread safety
//
// All goroutines append to a shared []Resource slice protected by a sync.Mutex.
// The mutex is the simplest correct tool here — a channel-based fan-in would
// add complexity without a measurable throughput benefit at this scale.
func RunConcurrentScan(ctx context.Context, api AWSScannerAPI) ([]Resource, error) {
	// ── Step 1: discover which regions to scan ────────────────────────────────
	regions, err := api.ListEnabledRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("discovery: failed to list enabled regions: %w", err)
	}
	if len(regions) == 0 {
		return nil, nil
	}

	// ── Step 2: shared result buffer ──────────────────────────────────────────
	// Pre-allocate conservatively (17 regions × ~10 resources each as a rough
	// starting estimate). The slice will grow if needed; this just avoids the
	// first few reallocations.
	var (
		mu      sync.Mutex
		results = make([]Resource, 0, len(regions)*10)
	)

	// appendSafe adds a batch of resources to the shared slice under the mutex.
	// Passing a []Resource batch (rather than one resource at a time) reduces
	// lock contention: we lock once per API call result, not once per resource.
	appendSafe := func(batch []Resource) {
		if len(batch) == 0 {
			return
		}
		mu.Lock()
		results = append(results, batch...)
		mu.Unlock()
	}

	// ── Step 3: outer errgroup — one goroutine per region ─────────────────────
	// errgroup.WithContext derives a child context that is cancelled the moment
	// any goroutine returns a non-nil error. All other goroutines check this
	// context on every blocking call and exit early.
	outerGroup, outerCtx := errgroup.WithContext(ctx)

	for _, r := range regions {
		region := r // capture loop variable — required pre-Go 1.22

		outerGroup.Go(func() error {
			return scanRegion(outerCtx, api, region, appendSafe)
		})
	}

	// Wait for every region goroutine to finish (or the first to fail).
	if err := outerGroup.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}

// scanRegion runs all resource-type fetchers for a single region concurrently
// using its own inner errgroup. Results are forwarded to the caller via the
// appendSafe callback.
//
// Adding a new resource type to the scan is a one-liner: add a goroutine that
// calls the relevant AWSScannerAPI method and passes the result to appendSafe.
func scanRegion(
	ctx context.Context,
	api AWSScannerAPI,
	region string,
	appendSafe func([]Resource),
) error {
	innerGroup, innerCtx := errgroup.WithContext(ctx)

	// ── EC2 instances ─────────────────────────────────────────────────────────
	innerGroup.Go(func() error {
		instances, err := api.GetEC2Instances(innerCtx, region)
		if err != nil {
			return fmt.Errorf("region %s: %w", region, err)
		}
		appendSafe(instances)
		return nil
	})

	// ── EBS volumes ───────────────────────────────────────────────────────────
	innerGroup.Go(func() error {
		volumes, err := api.GetEBSVolumes(innerCtx, region)
		if err != nil {
			return fmt.Errorf("region %s: %w", region, err)
		}
		appendSafe(volumes)
		return nil
	})

	// ── Elastic IPs ───────────────────────────────────────────────────────────
	innerGroup.Go(func() error {
		eips, err := api.GetElasticIPs(innerCtx, region)
		if err != nil {
			return fmt.Errorf("region %s: %w", region, err)
		}
		appendSafe(eips)
		return nil
	})

	// ── NAT Gateways ──────────────────────────────────────────────────────────
	innerGroup.Go(func() error {
		ngws, err := api.GetNATGateways(innerCtx, region)
		if err != nil {
			return fmt.Errorf("region %s: %w", region, err)
		}
		appendSafe(ngws)
		return nil
	})

	// TODO: add goroutines for GetEBSSnapshots, GetLoadBalancers, and
	// GetRDSInstances as those methods are implemented.

	return innerGroup.Wait()
}
