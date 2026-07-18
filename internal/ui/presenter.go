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
	"strings"
	"text/tabwriter"

	"github.com/anirudh/opssweep/internal/discovery"
	"github.com/anirudh/opssweep/internal/heuristics"
	"github.com/anirudh/opssweep/internal/pricing"
)

// ANSI escape codes for styled output.
const (
	ansiBold   = "\033[1m"
	ansiCyan   = "\033[36m"
	ansiAmber  = "\033[38;5;215m"
	ansiDim    = "\033[2m"
	ansiReset  = "\033[0m"
	ansiOrange = "\033[38;5;208m"
)

// PrintBanner prints a Claude-Code-inspired two-column welcome box to stdout.
//
// The box is exactly 80 visible characters wide:
//
//	│ (1) + left cell (38) + │ (1) + right cell (39) + │ (1) = 80
//
// The key design rule that makes alignment work perfectly:
//
//	NEVER pass a string containing ANSI escape codes to fmt.Sprintf with a
//	width verb (%-38s), because ANSI codes add invisible bytes that Sprintf
//	counts as visible width, which shifts everything to the right.
//
// Instead we use fmt.Sprintf("%-38s", plainText) to get correct padding, then
// wrap color codes around the result AFTER padding is done.
func PrintBanner() {
	const (
		lw = 38 // left cell visible width  (between left │ and mid │)
		rw = 39 // right cell visible width (between mid │ and right │)
	)

	bdr := ansiAmber // short alias for the border color
	rst := ansiReset

	// cell pads plain text to exactly `width` visible chars using fmt.Sprintf,
	// then optionally wraps the non-space portion in an ANSI color code.
	// This is the only safe way to colorize text inside a fixed-width box cell.
	cell := func(width int, text, colorCode string) string {
		// Step 1: pad the plain text to the exact visible width.
		padded := fmt.Sprintf("%-*s", width, text)
		if colorCode == "" {
			return padded
		}
		// Step 2: inject color around the text only, not the trailing spaces.
		// This preserves the exact byte count that the terminal will display.
		trimmed := strings.TrimRight(padded, " ")
		trailingSpaces := padded[len(trimmed):]
		return colorCode + trimmed + rst + trailingSpaces
	}

	// row prints one complete content line of the box.
	row := func(leftText, leftColor, rightText, rightColor string) {
		fmt.Printf("%s|%s%s%s|%s%s%s|%s\n",
			bdr, rst,
			cell(lw, leftText, leftColor),
			bdr, rst,
			cell(rw, rightText, rightColor),
			bdr, rst,
		)
	}

	// ── Top border ────────────────────────────────────────────────────────────
	// Total inner width = lw + 1 (mid border) + rw = 78.
	// Title " OpsSweep v1.0.0 " = 18 chars. Remaining dashes = 78 - 18 = 60.
	fmt.Printf("%s+- OpsSweep v1.0.0 %s+%s\n",
		bdr,
		strings.Repeat("-", 60),
		rst,
	)

	// ── Content rows ─────────────────────────────────────────────────────────

	row("", "", "", "")
	row(" Welcome back Anirudh!", ansiBold, "", "")
	row("", "", "", "")

	// Logo (pure ASCII robot face) paired with tips on the right
	row("   +-------+", ansiCyan, " Tips for getting started", ansiOrange+ansiBold)
	row("   | (o)(o)|", ansiCyan, " /scan   find idle resources", "")
	row("   |   ==  |", ansiCyan, " /report  HTML cost report", "")
	row("   +-------+", ansiCyan, " /help   list all commands", "")

	row("", "", "", "")
	row(" AWS FinOps & Remediation", "", " Recent activity", ansiOrange+ansiBold)
	row(" Engine  v1.0.0", ansiDim, " No recent activity", ansiDim)
	row("", "", "", "")

	// ── Bottom border ─────────────────────────────────────────────────────────
	fmt.Printf("%s+%s+%s\n",
		bdr,
		strings.Repeat("-", 78),
		rst,
	)
	fmt.Println()
}

const (
	// idleConfidenceThreshold is the minimum heuristics confidence score for a
	// resource to appear in the waste report.
	idleConfidenceThreshold = 0.5

	// separatorWidth is the character width of the dashed separator line.
	separatorWidth = 72
)

// PrintWasteReport writes a tab-aligned terminal table of idle resources and
// their estimated monthly cost to out.
func PrintWasteReport(out io.Writer, resources []discovery.Resource) error {
	cfg := heuristics.DefaultConfig()

	type row struct {
		res         discovery.Resource
		score       heuristics.Score
		monthlyCost float64
	}

	var (
		rows         []row
		totalSavings float64
	)

	for _, res := range resources {
		score := heuristics.Evaluate(res, cfg)
		if score.ShouldSkip {
			continue
		}
		if score.Confidence < idleConfidenceThreshold {
			continue
		}
		cost := pricing.CalculateMonthlyWaste(res, true)
		totalSavings += cost
		rows = append(rows, row{res: res, score: score, monthlyCost: cost})
	}

	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "No idle resources found. Your account looks clean!")
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(tw, "RESOURCE ID\tTYPE\tREGION\tSTATE\tCONFIDENCE\tMONTHLY WASTE"); err != nil {
		return fmt.Errorf("ui: writing header: %w", err)
	}

	for _, r := range rows {
		line := fmt.Sprintf(
			"%s\t%s\t%s\t%s\t%.0f%%\t$%.2f",
			r.res.ID,
			r.res.Type,
			r.res.Region,
			r.res.State,
			r.score.Confidence*100,
			r.monthlyCost,
		)
		if _, err := fmt.Fprintln(tw, line); err != nil {
			return fmt.Errorf("ui: writing row for %s: %w", r.res.ID, err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("ui: flushing table: %w", err)
	}

	separator := strings.Repeat("-", separatorWidth)
	if _, err := fmt.Fprintln(out, separator); err != nil {
		return fmt.Errorf("ui: writing separator: %w", err)
	}

	if _, err := fmt.Fprintf(out, "TOTAL POTENTIAL SAVINGS: $%.2f/mo\n", totalSavings); err != nil {
		return fmt.Errorf("ui: writing total: %w", err)
	}

	return nil
}

// repeatChar returns a string of n copies of ch.
func repeatChar(ch byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ch
	}
	return string(b)
}
