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
// # Command structure
//
// Commands are slash-prefixed (e.g. /scan, /report, /teardown) to distinguish
// them from free-form input. This leaves room for future natural-language
// features ("show me idle resources in us-east-1") without ambiguity.
//
// # Exit conditions
//
// The loop runs until:
//   - The user types /exit or quit
//   - stdin closes (EOF, e.g. piped input exhausted or Ctrl+D in terminal)
//   - An unrecoverable error occurs (e.g. scanner.Scan() fails after retries)
//
// Normal command errors (e.g. AWS API throttling, invalid region) are caught,
// logged, and do NOT terminate the session — the user stays in the shell and
// can retry or try a different command.
func Start(ctx context.Context, cfg aws.Config) {
	// ── Banner ────────────────────────────────────────────────────────────────
	// Print the premium bordered banner once at session start.
	ui.PrintBanner()

	// ── Input loop ────────────────────────────────────────────────────────────
	scanner := bufio.NewScanner(os.Stdin)

	for {
		// Print the minimal prompt: just "> " with no color or prefix.
		// Clean and unobtrusive, letting command output be the star.
		fmt.Print(ansiDim + "> " + ansiReset)

		// Read the next line from stdin.
		// scanner.Scan() returns false on EOF (Ctrl+D) or a read error.
		if !scanner.Scan() {
			break
		}

		// Trim leading/trailing whitespace so "  /help  " is treated as "/help".
		input := strings.TrimSpace(scanner.Text())

		// Skip empty lines — pressing Enter with no input is a no-op.
		if input == "" {
			continue
		}

		// ── Command dispatch ──────────────────────────────────────────────────
		switch input {

		case "/exit", "quit":
			// Clean exit message in green, then return to terminate the loop.
			fmt.Println(ansiGreen + "✓ " + ansiReset + "Goodbye! Session ended.")
			return

		case "/clear":
			// ANSI escape sequence: \033[H moves cursor to home (top-left),
			// \033[2J clears the entire screen buffer.
			fmt.Print("\033[H\033[2J")
			// Reprint the banner so the user sees it again after clearing.
			ui.PrintBanner()

		case "/help":
			printHelp()

		case "/scan":
			runScan(ctx, cfg)

		default:
			// Unrecognized command — print a subtle error without killing
			// the session. Dimmed text keeps it low-key.
			if input != "" {
				fmt.Println(ansiDim + "Unknown command. Type /help" + ansiReset)
			}
		}

		// Add a blank line after command output so the next prompt is visually
		// separated. This prevents the output from running together into an
		// unreadable wall of text.
		fmt.Println()
	}

	// ── Post-loop cleanup ─────────────────────────────────────────────────────
	// If we exit the loop due to scanner.Err() rather than EOF, log it.
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to read input: %v\n", err)
	}
}

// printHelp writes a formatted, two-column list of available commands to stdout.
// Called when the user types /help.
//
// The format is designed for scannability: command names in cyan, short
// descriptions aligned in a second column.
func printHelp() {
	fmt.Println(ansiBold + "Available Commands:" + ansiReset)
	fmt.Println()

	// Two-column format: command name (cyan, left-padded) and description.
	commands := []struct {
		name string
		desc string
	}{
		{"/scan", "Scan AWS regions for idle resources"},
		{"/report", "Generate an HTML FinOps audit report"},
		{"/clear", "Clear the terminal screen"},
		{"/help", "Show this help message"},
		{"/exit", "Exit the interactive shell"},
	}

	for _, cmd := range commands {
		fmt.Printf("  %s%-12s%s  %s\n", ansiCyan, cmd.name, ansiReset, cmd.desc)
	}
}

// runScan executes the full multi-region discovery pipeline and prints the
// waste report to stdout. It is intentionally a standalone function (not a
// method) so it can be called cleanly from the switch without cluttering Start.
//
// Execution order:
//  1. Print a loading message so the user knows something is happening —
//     the scan can take several seconds as it fans out across all regions.
//  2. Initialise the AWS scanner with the session-level config.
//  3. Run RunConcurrentScan, which fans out one goroutine per region and
//     collects all discovered resources into a flat slice.
//  4. Hand the slice to ui.PrintWasteReport, which scores every resource
//     through the heuristics engine, filters below the confidence threshold,
//     calculates monthly waste via the pricing package, and renders the table.
//
// Errors are printed to stderr but do NOT terminate the shell — the user stays
// in the REPL and can retry or try a different command.
func runScan(ctx context.Context, cfg aws.Config) {
	// ── 1. Loading message ────────────────────────────────────────────────────
	fmt.Println(ansiCyan + "[SYSTEM] Scanning AWS regions for idle resources. This may take a moment..." + ansiReset)
	fmt.Println()

	// ── 2. Initialise scanner ─────────────────────────────────────────────────
	// NewAWSScanner holds only the base config; regional EC2/RDS/CloudWatch
	// clients are constructed on demand per-region inside the scanner.
	scanner := discovery.NewAWSScanner(cfg)

	// ── 3. Run the concurrent scan ────────────────────────────────────────────
	// RunConcurrentScan fans out across all enabled regions, collects results,
	// and returns a merged []discovery.Resource slice. The call blocks until
	// all goroutines complete or the context is cancelled (e.g. Ctrl+C).
	resources, err := discovery.RunConcurrentScan(ctx, scanner)
	if err != nil {
		// Context cancellation (Ctrl+C mid-scan) is surfaced here. Print a
		// user-friendly message rather than a raw Go error.
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr,
				ansiRed+"[ERROR]"+ansiReset+" Scan cancelled.\n",
			)
		} else {
			fmt.Fprintf(os.Stderr,
				ansiRed+"[ERROR]"+ansiReset+" Scan failed: %v\n", err,
			)
		}
		return
	}

	// ── 4. Print the waste report ─────────────────────────────────────────────
	// PrintWasteReport handles scoring (heuristics), pricing, and table
	// rendering internally. We pass os.Stdout directly so the output streams
	// to the terminal immediately — no buffering required at this level.
	if err := ui.PrintWasteReport(os.Stdout, resources); err != nil {
		fmt.Fprintf(os.Stderr,
			ansiRed+"[ERROR]"+ansiReset+" Failed to render report: %v\n", err,
		)
	}
}
