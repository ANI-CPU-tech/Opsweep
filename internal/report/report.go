// Package report renders scan results as a styled terminal table (via
// charmbracelet/lipgloss) and as a self-contained static HTML file
// (via html/template + go:embed). Both outputs are generated from the
// same ScanResult data structure.
package report

import (
	_ "embed"

	"github.com/anirudh/opssweep/internal/heuristics"
)

//go:embed templates/report.html.tmpl
var htmlTemplate string

// ScanResult is the top-level data structure passed to both renderers.
type ScanResult struct {
	AccountID    string
	Regions      []string
	TotalMonthly float64
	Scores       []heuristics.Score
}

// Options controls rendering behaviour.
type Options struct {
	// NoRedact suppresses redaction of account IDs and ARNs.
	NoRedact bool
	// JSONOutput emits raw JSON instead of a styled table.
	JSONOutput bool
}

// RenderTerminal prints a styled table of flagged resources to stdout
// using charmbracelet/lipgloss.
// TODO: implement lipgloss table rendering.
func RenderTerminal(result ScanResult, opts Options) error {
	// TODO: implement
	return nil
}

// RenderHTML writes a self-contained static HTML report to the given path.
// The report embeds all styles and SVG charts inline — no external dependencies.
// TODO: implement html/template rendering.
func RenderHTML(result ScanResult, opts Options, outputPath string) error {
	// TODO: implement
	return nil
}
