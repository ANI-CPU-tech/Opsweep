// Package ui handles all terminal output for OpsSweep.
//
// It is intentionally kept thin: no AWS calls, no scoring logic. It receives
// already-discovered resources, runs them through the heuristics and pricing
// packages, and writes formatted output to an [io.Writer]. Accepting an
// io.Writer (rather than writing directly to os.Stdout) keeps every function
// in this package fully testable without capturing stdout.
package ui

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/anirudh/opssweep/internal/discovery"
	"github.com/anirudh/opssweep/internal/heuristics"
	"github.com/anirudh/opssweep/internal/pricing"
)

// ANSI escape codes for the banner.
const (
	ansiBold  = "\033[1m"
	ansiCyan  = "\033[36m"
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

// PrintBanner prints the OpsSweep ASCII art banner to stdout.
// It is called once at process startup, before any AWS or Cobra logic runs,
// so the logo is always the first thing a user sees regardless of which
// subcommand they invoke.
func PrintBanner() {
	// Block-letter ASCII art for "OpsSweep" generated in a clean, modern
	// style. Each line is a raw string — no escape sequences inside the art
	// itself so the font is easy to edit without accidental color breaks.
	const art = `
  ██████╗ ██████╗ ███████╗███████╗██╗    ██╗███████╗███████╗██████╗
 ██╔═══██╗██╔══██╗██╔════╝██╔════╝██║    ██║██╔════╝██╔════╝██╔══██╗
 ██║   ██║██████╔╝███████╗███████╗██║ █╗ ██║█████╗  █████╗  ██████╔╝
 ██║   ██║██╔═══╝ ╚════██║╚════██║██║███╗██║██╔══╝  ██╔══╝  ██╔═══╝
 ╚██████╔╝██║     ███████║███████║╚███╔███╔╝███████╗███████╗██║
  ╚═════╝ ╚═╝     ╚══════╝╚══════╝ ╚══╝╚══╝ ╚══════╝╚══════╝╚═╝
`
	// Print the art in bold cyan, then the subtitle in a dimmed style so it
	// recedes visually behind the logo without disappearing entirely.
	fmt.Print(ansiBold + ansiCyan + art + ansiReset)
	fmt.Println(ansiDim + "              AWS FinOps & Remediation Engine" + ansiReset)
	fmt.Println()
}

const (
	// idleConfidenceThreshold is the minimum heuristics confidence score for a
	// resource to appear in the waste report. Resources below this threshold are
	// considered "probably active" and are silently omitted.
	// 0.5 means "more likely idle than not" — deliberately permissive at the
	// reporting stage so users see borderline cases. The teardown stage applies
	// a stricter threshold before any destructive action.
	idleConfidenceThreshold = 0.5

	// separatorWidth is the character width of the dashed line printed between
	// the resource rows and the totals footer. Wide enough to span all columns
	// at typical terminal widths.
	separatorWidth = 72
)

// PrintWasteReport writes a tab-aligned terminal table of idle resources and
// their estimated monthly cost to out.
//
// For each resource in the slice, it:
//  1. Runs [heuristics.Evaluate] to compute an idle confidence score.
//  2. Skips the resource if it is protected (ShouldSkip=true) or if its
//     confidence is below [idleConfidenceThreshold].
//  3. Calls [pricing.CalculateMonthlyWaste] to get the monthly cost estimate.
//  4. Prints one tab-separated row per qualifying resource.
//
// After the resource rows it prints a separator and a TOTAL POTENTIAL SAVINGS
// line. If no resources meet the threshold the function prints a short "nothing
// found" message instead of an empty table.
//
// The function flushes the underlying tabwriter before returning so the caller
// does not need to do anything after the call completes.
func PrintWasteReport(out io.Writer, resources []discovery.Resource) error {
	// Use the default heuristics config (IdleThreshold=0.6, time.Now() for age).
	// The presenter applies its own looser threshold (0.5) at display time so
	// borderline resources are visible in the report even if the engine would
	// not flag them for teardown.
	cfg := heuristics.DefaultConfig()

	// ── Evaluate and filter ───────────────────────────────────────────────────
	type row struct {
		res        discovery.Resource
		score      heuristics.Score
		monthlyCost float64
	}

	var (
		rows         []row
		totalSavings float64
	)

	for _, res := range resources {
		score := heuristics.Evaluate(res, cfg)

		// Hard skip: protected tag (keep=true, env=prod). Never show these.
		if score.ShouldSkip {
			continue
		}

		// Soft skip: not idle enough to report.
		if score.Confidence < idleConfidenceThreshold {
			continue
		}

		cost := pricing.CalculateMonthlyWaste(res, true)
		totalSavings += cost

		rows = append(rows, row{res: res, score: score, monthlyCost: cost})
	}

	// ── Empty state ───────────────────────────────────────────────────────────
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "No idle resources found. Your account looks clean!")
		return err
	}

	// ── Build the tabwriter ───────────────────────────────────────────────────
	// tabwriter.NewWriter pads each column to align tabs across all rows.
	// MinWidth=0, TabWidth=0 let the content drive column widths.
	// Padding=2 adds two spaces between columns for visual breathing room.
	// AlignRight=false (flag 0) left-aligns all columns.
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	// ── Header ────────────────────────────────────────────────────────────────
	if _, err := fmt.Fprintln(tw, "RESOURCE ID\tTYPE\tREGION\tSTATE\tCONFIDENCE\tMONTHLY WASTE"); err != nil {
		return fmt.Errorf("ui: writing header: %w", err)
	}

	// ── Resource rows ─────────────────────────────────────────────────────────
	for _, r := range rows {
		line := fmt.Sprintf(
			"%s\t%s\t%s\t%s\t%.0f%%\t$%.2f",
			r.res.ID,
			r.res.Type,
			r.res.Region,
			r.res.State,
			r.score.Confidence*100, // display as percentage, e.g. "90%"
			r.monthlyCost,
		)
		if _, err := fmt.Fprintln(tw, line); err != nil {
			return fmt.Errorf("ui: writing row for %s: %w", r.res.ID, err)
		}
	}

	// Flush here so the separator is printed after the aligned table, not
	// interleaved with unflushed tab-padded content.
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("ui: flushing table: %w", err)
	}

	// ── Footer ────────────────────────────────────────────────────────────────
	separator := repeatChar('-', separatorWidth)
	if _, err := fmt.Fprintln(out, separator); err != nil {
		return fmt.Errorf("ui: writing separator: %w", err)
	}

	if _, err := fmt.Fprintf(out, "TOTAL POTENTIAL SAVINGS: $%.2f/mo\n", totalSavings); err != nil {
		return fmt.Errorf("ui: writing total: %w", err)
	}

	return nil
}

// repeatChar returns a string of n copies of ch.
// Used to build the separator line without importing strings or bytes.
func repeatChar(ch byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ch
	}
	return string(b)
}
