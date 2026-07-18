// Package cli implements the interactive REPL shell for OpsSweep.
//
// Rather than a traditional CLI with subcommands and flags, OpsSweep runs as
// a persistent interactive session inspired by tools like Claude Code. The
// user types commands at a prompt (/scan, /report, /teardown) and the tool
// responds immediately without re-initializing AWS credentials or re-parsing
// configuration on every invocation.
//
// This design has three benefits:
//  1. Faster iteration: AWS config is loaded once, not on every command.
//  2. Better UX: stateful context (last scan results, selected resources) can
//     be preserved across commands within a single session.
//  3. Discoverability: /help is always available; users explore by typing
//     rather than reading man pages.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/anirudh/opssweep/internal/discovery"
	"github.com/anirudh/opssweep/internal/remediation"
	"github.com/anirudh/opssweep/internal/report"
	"github.com/anirudh/opssweep/internal/ui"
)

// ANSI escape codes for styled terminal output.
const (
	ansiCyan   = "\033[36m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiDim    = "\033[2m"
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
)

// Start launches the interactive OpsSweep shell (REPL).
//
// The shell prints the premium bordered banner once on startup, then enters
// an infinite loop reading commands from stdin. The AWS config and context are
// passed in once at session start and reused across all commands — no
// re-authentication required.
//
// # Input parsing
//
// Each line of input is split into tokens via strings.Fields. The first token
// is the command (e.g. "/scan"); remaining tokens are treated as arguments
// (e.g. "--teardown", "--output=report.html"). This mirrors how real shells
// tokenise input without requiring a full flag parser.
//
// # Exit conditions
//
// The loop runs until:
//   - The user types /exit or quit
//   - stdin closes (EOF, e.g. piped input exhausted or Ctrl+D in terminal)
//   - An unrecoverable read error occurs
//
// Normal command errors (AWS API failures, file write errors) are caught,
// logged to stderr, and do NOT terminate the session.
func Start(ctx context.Context, cfg aws.Config) {
	// Print the premium bordered banner once at session start.
	ui.PrintBanner()

	lineScanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print(ansiDim + "> " + ansiReset)

		if !lineScanner.Scan() {
			break
		}

		// Split into tokens. strings.Fields handles multiple spaces and tabs,
		// and returns an empty slice for blank lines — no panic risk.
		args := strings.Fields(lineScanner.Text())

		// Empty line: re-show the prompt without any output.
		if len(args) == 0 {
			continue
		}

		command := args[0]  // e.g. "/scan"
		flags := args[1:]   // e.g. ["--teardown"] or []

		// ── Command dispatch ──────────────────────────────────────────────────
		switch command {

		case "/exit", "quit":
			fmt.Println(ansiGreen + "✓ " + ansiReset + "Goodbye! Session ended.")
			return

		case "/clear":
			fmt.Print("\033[H\033[2J")
			ui.PrintBanner()

		case "/help":
			printHelp()

		case "/scan":
			runScan(ctx, cfg, flags)

		case "/report":
			runReport(ctx, cfg, flags)

		default:
			fmt.Println(ansiDim + "Unknown command. Type /help" + ansiReset)
		}

		fmt.Println()
	}

	if err := lineScanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to read input: %v\n", err)
	}
}

// hasFlag reports whether a specific flag string is present in the args slice.
// A linear scan is fine — the number of flags is always tiny.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// scanResources runs the multi-region discovery pipeline and returns the raw
// resource slice. It is shared by runScan and runReport to avoid duplicating
// the scanner initialisation and error handling.
//
// Returns nil on any error; the error has already been written to stderr.
func scanResources(ctx context.Context, cfg aws.Config) []discovery.Resource {
	fmt.Println(ansiCyan + "[SYSTEM] Scanning AWS regions for idle resources. This may take a moment..." + ansiReset)
	fmt.Println()

	s := discovery.NewAWSScanner(cfg)
	resources, err := discovery.RunConcurrentScan(ctx, s)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Scan cancelled.\n")
		} else {
			fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Scan failed: %v\n", err)
		}
		return nil
	}
	return resources
}

// runScan executes a full discovery scan and either:
//   - (default) prints the waste report table to stdout, or
//   - (--teardown) prints the report AND runs live deletion via the Remediator.
//
// The Remediator.Process call receives the full resource slice and handles its
// own confidence filtering internally (threshold: 0.90). There is no need to
// loop and call it per-resource — that would bypass its own queuing logic.
func runScan(ctx context.Context, cfg aws.Config, flags []string) {
	resources := scanResources(ctx, cfg)
	if resources == nil {
		return // error already printed
	}

	// Always print the waste table so the user can see what was found.
	if err := ui.PrintWasteReport(os.Stdout, resources); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to render report: %v\n", err)
		return
	}

	// ── Teardown pass (opt-in) ────────────────────────────────────────────────
	if !hasFlag(flags, "--teardown") {
		return
	}

	// Live teardown: warn loudly before making any mutating AWS API calls.
	fmt.Println()
	fmt.Println(ansiRed + "[WARNING] Executing live teardown. Resources will be permanently deleted." + ansiReset)
	fmt.Println()

	// Process accepts the full slice and applies the 0.90 confidence threshold
	// internally. isDryRun=false triggers real AWS deletion API calls.
	remediator := remediation.NewRemediator(cfg)
	if err := remediator.Process(ctx, resources, false); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Teardown failed: %v\n", err)
	}
}

// runReport executes a full discovery scan and writes a self-contained HTML
// FinOps audit report to disk. The output path defaults to "audit.html" but
// can be overridden with --output=<path>.
//
// Example:
//
//	/report
//	/report --output=reports/2024-q1.html
func runReport(ctx context.Context, cfg aws.Config, flags []string) {
	resources := scanResources(ctx, cfg)
	if resources == nil {
		return // error already printed
	}

	// Determine output path: default to "audit.html", allow override.
	outputPath := "audit.html"
	for _, f := range flags {
		if strings.HasPrefix(f, "--output=") {
			outputPath = strings.TrimPrefix(f, "--output=")
		}
	}

	if err := report.GenerateHTMLReport(resources, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to generate report: %v\n", err)
		return
	}

	fmt.Println(ansiGreen + "[REPORT] Successfully generated FinOps audit at " + outputPath + ansiReset)
}

// printHelp writes a formatted, two-column list of available commands to stdout.
func printHelp() {
	fmt.Println(ansiBold + "Available Commands:" + ansiReset)
	fmt.Println()

	commands := []struct {
		usage string
		desc  string
	}{
		{"/scan", "Scan all AWS regions for idle resources"},
		{"/scan --teardown", "Scan and live-delete high-confidence waste"},
		{"/report", "Scan and write HTML audit to audit.html"},
		{"/report --output=<path>", "Write HTML audit to a custom path"},
		{"/clear", "Clear the terminal screen"},
		{"/help", "Show this help message"},
		{"/exit", "Exit the interactive shell"},
	}

	for _, cmd := range commands {
		fmt.Printf("  %s%-28s%s  %s\n", ansiCyan, cmd.usage, ansiReset, cmd.desc)
	}
}
