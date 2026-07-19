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
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/anirudh/opssweep/internal/config"
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

// Box-drawing characters for the banner's rounded border. Each one is a
// single Unicode rune that occupies exactly one terminal column, so they
// drop straight into the same width arithmetic as the plain-ASCII
// "+"/"-"/"|" they replace — the "ANSI-safety rule" below covers why that's
// safe (fmt pads by rune count, and these glyphs are single-width, so
// nothing here needs special-casing).
const (
	boxTopLeft     = "╭"
	boxTopRight    = "╮"
	boxBottomLeft  = "╰"
	boxBottomRight = "╯"
	boxHorizontal  = "─"
	boxVertical    = "│"
)

// PrintBanner prints a dynamically-sized two-column welcome box that exactly
// fills the current terminal width. The layout is modeled on the Claude Code
// CLI banner: a rounded border, a welcome line paired with a "Tips for
// getting started" header, a short command list, a divider, and a "Recent
// activity" section, with product/version info tucked into the bottom-left
// corner.
//
// # Layout arithmetic
//
// The format string for every content row is:
//
//	border | space | %-38s | space | border | space | %-*s | space | border
//	 ^               ^                        ^
//	 |               left cell (38 visible chars)     right cell (rightInner chars)
//	 left border + space (1 + 1)
//
// Counting visible chars: 1 + 1 + 38 + 1 + 1 + 1 + rightInner + 1 + 1 = 45 + rightInner
// Setting that equal to `width`:  rightInner = width - 45
//
// # ANSI-safety rule
//
// NEVER pass a string that contains ANSI escape codes to fmt.Sprintf with a
// width verb (%-38s or %-*s). fmt pads strings by rune count, and while a
// single ANSI escape sequence like "\033[1m" is invisible on screen, it is
// still several runes ('\033', '[', '1', 'm'), so it would inflate the
// measured width and push the right border out of alignment.
//
// The safe pattern used throughout this function:
//  1. Pad the plain text with fmt.Sprintf("%-*s", width, plainText).
//  2. THEN wrap the non-space portion in ANSI codes.
//  3. Trailing spaces are kept plain so the terminal cell count stays correct.
func PrintBanner() {
	// ── 1. Detect terminal width ──────────────────────────────────────────────
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width < 60 {
		width = 100
	}

	// ── 2. Column widths ──────────────────────────────────────────────────────
	rightWidth := width - 45
	if rightWidth < 10 {
		rightWidth = 10
	}

	// ── 3. Row printers ────────────────────────────────────────────────────────
	// row prints a plain (uncolored) content row. leftText/rightText are
	// pre-padded to their exact visible widths before the border runes are
	// added, so the border always lands in the same column regardless of
	// content length.
	row := func(leftText, rightText string) {
		leftCol := fmt.Sprintf("%-38s", leftText)
		rightCol := fmt.Sprintf("%-*s", rightWidth, rightText)

		fmt.Printf("%s%s%s %s %s%s%s %s %s%s%s\n",
			ansiAmber, boxVertical, ansiReset,
			leftCol,
			ansiAmber, boxVertical, ansiReset,
			rightCol,
			ansiAmber, boxVertical, ansiReset,
		)
	}

	// colorRow is like row but wraps each cell's non-space content in an ANSI
	// color code. Padding happens first on the plain text; color is injected
	// around only the visible characters so trailing spaces stay plain and
	// the cell width is unaffected.
	colorRow := func(leftText, leftColor, rightText, rightColor string) {
		leftPadded := fmt.Sprintf("%-38s", leftText)
		rightPadded := fmt.Sprintf("%-*s", rightWidth, rightText)

		applyColor := func(padded, color string) string {
			if color == "" {
				return padded
			}
			trimmed := strings.TrimRight(padded, " ")
			if trimmed == "" {
				return padded // nothing to color; return spaces as-is
			}
			return color + trimmed + ansiReset + padded[len(trimmed):]
		}

		leftColored := applyColor(leftPadded, leftColor)
		rightColored := applyColor(rightPadded, rightColor)

		// Exactly 11 %s verbs, 11 string arguments — no mismatch possible.
		fmt.Printf("%s%s%s %s %s%s%s %s %s%s%s\n",
			ansiAmber, boxVertical, ansiReset,
			leftColored,
			ansiAmber, boxVertical, ansiReset,
			rightColored,
			ansiAmber, boxVertical, ansiReset,
		)
	}

	// ── 4. Top border ─────────────────────────────────────────────────────────
	// "╭─ OpsSweep v1.0.0 ────────────────────────────────────────────────╮"
	// Fixed chars = corner + dash + space + space + corner = 5, so:
	//   dashes = width - 5 - len(label)
	const label = "OpsSweep v1.0.0"
	dashes := width - 5 - len(label)
	if dashes < 1 {
		dashes = 1
	}
	top := boxTopLeft + boxHorizontal + " " + label + " " + strings.Repeat(boxHorizontal, dashes) + boxTopRight
	fmt.Printf("%s%s%s\n", ansiAmber, top, ansiReset)

	// ── 5. Content rows ───────────────────────────────────────────────────────
	row("", "")
	colorRow(" Welcome back Anirudh!", ansiBold, " Tips for getting started", ansiOrange+ansiBold)
	row("", " /scan    find idle resources")

	// Pixel-block cloud logo, paired with the rest of the command list.
	// Built from U+2588 FULL BLOCK — like the box-drawing runes above,
	// it's single-width, so it's safe to pad via %-38s without throwing
	// off the border alignment. Each row is a solid bar of a different
	// width/offset; stacking them is what produces the stepped, blocky
	// cloud silhouette instead of a smooth curve.
colorRow("            ████", ansiCyan, " /report  HTML cost report", "")
colorRow("          ████████", ansiCyan, " /help    list all commands", "")
colorRow("        ████████████", ansiCyan, "", "")
colorRow("      ████████████████", ansiCyan, "", "")
colorRow("      ██████    ██████", ansiCyan, "", "")
colorRow("      ██████    ██████", ansiCyan, "", "")
colorRow("      ██████    ██████", ansiCyan, "", "")
colorRow("      ████████████████", ansiCyan, "", "")
colorRow("    ████████████████████", ansiCyan, "", "")
colorRow("    ████████████████████", ansiCyan, "", "")
colorRow("   ██████          ██████", ansiCyan, "", "")
colorRow("     ████          ████", ansiCyan, "", "")
colorRow("     ████          ████", ansiCyan, "", "")
colorRow("      ██            ██", ansiCyan, "", "")

	row("", "")
	colorRow("", "", strings.Repeat(boxHorizontal, rightWidth), ansiDim)
	colorRow("", "", " Recent activity", ansiOrange+ansiBold)
	colorRow(" AWS FinOps & Remediation · v1.0.0", ansiDim, " No recent activity", ansiDim)
	row("", "")

	// ── 6. Bottom border ──────────────────────────────────────────────────────
	bottom := boxBottomLeft + strings.Repeat(boxHorizontal, width-2) + boxBottomRight
	fmt.Printf("%s%s%s\n", ansiAmber, bottom, ansiReset)
	fmt.Println()
}

const (
	// separatorWidth is the character width of the dashed separator line.
	separatorWidth = 72
)

// PrintWasteReport writes a tab-aligned terminal table of idle resources and
// their estimated monthly cost to out.
//
// The confidence threshold applied to filter resources is taken from
// appConfig.Rules.ConfidenceThreshold (0.0–100.0 scale). Only resources whose
// heuristics score × 100 meets or exceeds that threshold are included in the
// report. This replaces the previous hardcoded 0.5 constant so operators can
// tune reporting sensitivity via ~/.opssweep.yaml without recompiling.
func PrintWasteReport(out io.Writer, resources []discovery.Resource, appConfig *config.Config) error {
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
		// score.Confidence is in the 0.0–1.0 range; ConfidenceThreshold is
		// stored as 0.0–100.0 in the config. Multiply before comparing so
		// the units match. Both sides of the comparison are now config-driven
		// rather than hardcoded.
		if (score.Confidence * 100) < appConfig.Rules.ConfidenceThreshold {
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