// Package report renders scan results as a styled terminal table (via
// charmbracelet/lipgloss) and as a self-contained static HTML file
// (via html/template + go:embed). Both outputs are generated from the
// same ScanResult data structure.
package report

import (
	"fmt"
	"html/template"
	"os"
	"time"

	_ "embed"

	"github.com/anirudh/opssweep/internal/discovery"
	"github.com/anirudh/opssweep/internal/heuristics"
	"github.com/anirudh/opssweep/internal/pricing"
)

//go:embed templates/report.html.tmpl
var htmlTemplate string

// reportConfidenceThreshold is the minimum heuristics confidence score a
// resource must reach to be included in the HTML report. Mirrors the
// remediation threshold — only high-signal findings are worth surfacing.
const reportConfidenceThreshold = 0.90

// ReportResource is a flattened view of a single flagged resource for use
// inside the HTML template. It avoids exposing raw discovery.Resource pointer
// fields to the template and pre-formats values for display.
type ReportResource struct {
	// ResourceType is the human-readable AWS resource type (e.g. "ec2:elastic-ip").
	ResourceType string
	// ID is the primary AWS identifier (e.g. "eipalloc-0abc123def").
	ID string
	// Region is the AWS region where the resource lives (e.g. "us-east-1").
	Region string
	// ConfidenceScore is the heuristics confidence percentage formatted as a
	// two-decimal float (e.g. 0.95), suitable for {{printf "%.0f%%" ...}}.
	ConfidenceScore float64
	// MonthlyWaste is the estimated monthly USD cost being wasted.
	MonthlyWaste float64
	// Reasons is a human-readable list of signals that contributed to the score.
	Reasons []string
}

// ReportData is the top-level struct executed against the HTML template.
// All template actions ({{.TotalWaste}}, {{range .Resources}}) reference
// fields on this struct.
type ReportData struct {
	// TotalWaste is the sum of MonthlyWaste across all flagged resources.
	TotalWaste float64
	// ResourceCount is the number of resources that met the report threshold.
	ResourceCount int
	// ScanDate is the formatted timestamp when the report was generated.
	ScanDate string
	// Resources is the slice of flagged resources sorted by descending waste.
	Resources []ReportResource
}

// GenerateHTMLReport evaluates all resources through the heuristics engine,
// filters those meeting [reportConfidenceThreshold], computes waste estimates,
// and writes a self-contained HTML report to outputPath.
//
// The template is embedded at build time via go:embed — no external files are
// read at runtime beyond the output path itself.
//
// Returns an error if the template fails to parse, if any resource cannot be
// evaluated, or if the output file cannot be created or written.
func GenerateHTMLReport(resources []discovery.Resource, outputPath string) error {
	cfg := heuristics.DefaultConfig()

	// ── 1. Score and filter ───────────────────────────────────────────────────
	var flagged []ReportResource
	var totalWaste float64

	for _, res := range resources {
		score := heuristics.Evaluate(res, cfg)

		// Skip protected resources and those below the confidence threshold.
		if score.ShouldSkip || score.Confidence < reportConfidenceThreshold {
			continue
		}

		waste := pricing.CalculateMonthlyWaste(res, true)
		totalWaste += waste

		flagged = append(flagged, ReportResource{
			ResourceType:    string(res.Type),
			ID:              res.ID,
			Region:          res.Region,
			ConfidenceScore: score.Confidence,
			MonthlyWaste:    waste,
			Reasons:         score.Reasons,
		})
	}

	// ── 2. Assemble template data ─────────────────────────────────────────────
	data := ReportData{
		TotalWaste:    totalWaste,
		ResourceCount: len(flagged),
		ScanDate:      time.Now().Format("January 2, 2006 at 15:04 MST"),
		Resources:     flagged,
	}

	// ── 3. Parse and execute the template ────────────────────────────────────
	// html/template is used (not text/template) so that any dynamic values
	// injected into the HTML are automatically escaped against XSS.
	//
	// We register a "mul" helper so the template can compute percentages
	// (e.g. {{mul .ConfidenceScore 100.0}}) without requiring pre-formatted
	// strings in the data struct — keeping the struct clean and the formatting
	// logic in the template where it belongs.
	tmpl, err := template.New("report").Funcs(template.FuncMap{
		"mul": func(a, b float64) float64 { return a * b },
	}).Parse(htmlTemplate)
	if err != nil {
		return fmt.Errorf("report: failed to parse HTML template: %w", err)
	}

	// ── 4. Write to output file ───────────────────────────────────────────────
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("report: failed to create output file %q: %w", outputPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("report: failed to render HTML template: %w", err)
	}

	return nil
}

// ScanResult is the top-level data structure passed to both renderers.
// Retained for backward compatibility with existing callers of RenderTerminal
// and RenderHTML.
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
