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
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"

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
// The shell prints the branded banner once on startup, then enters an infinite
// loop reading commands from stdin. The AWS config is passed in once at session
// start and reused across all commands — no re-authentication required.
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
//   - The user types /exit or /quit
//   - stdin closes (EOF, e.g. piped input exhausted or Ctrl+D in terminal)
//   - An unrecoverable error occurs (e.g. scanner.Scan() fails after retries)
//
// Normal command errors (e.g. AWS API throttling, invalid region) are caught,
// logged, and do NOT terminate the session — the user stays in the shell and
// can retry or try a different command.
func Start(cfg aws.Config) {
	// ── Banner ────────────────────────────────────────────────────────────────
	// Print once at session start, not on every command invocation.
	ui.PrintBanner()

	// Welcome message — dimmed to recede behind the logo but still visible.
	fmt.Println(ansiDim + "Welcome to the interactive shell. Type /help for available commands." + ansiReset)
	fmt.Println()

	// ── Input loop ────────────────────────────────────────────────────────────
	scanner := bufio.NewScanner(os.Stdin)

	for {
		// Print the styled prompt on the same line as user input.
		// Cyan makes the prompt visually distinct from command output above it.
		fmt.Print(ansiCyan + "opsweep> " + ansiReset)

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

		case "/exit", "/quit":
			fmt.Println(ansiGreen + "Goodbye! 👋" + ansiReset)
			return

		case "/clear":
			// ANSI escape sequence: \033[H moves cursor to home (top-left),
			// \033[2J clears the entire screen buffer.
			fmt.Print("\033[H\033[2J")

		case "/help":
			printHelp()

		case "/scan":
			// TODO: wire the discovery scanner back up in the next step.
			// For now, print a system message so the user knows the command
			// is recognized even though it doesn't do anything yet.
			fmt.Println(ansiYellow + "[SYSTEM]" + ansiReset + " Initiating AWS environment scan...")

		default:
			// Unrecognized command — print a helpful error without killing
			// the session. Red text makes it clear this is an error.
			fmt.Println(ansiRed + "✗ Unknown command:" + ansiReset + " " + input)
			fmt.Println(ansiDim + "  Type /help to see available commands." + ansiReset)
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

// printHelp writes a nicely formatted, color-coded list of available commands
// to stdout. Called when the user types /help.
//
// The format is designed for scannability: command names in bold cyan, short
// descriptions in normal weight, grouped by category (navigation, operations).
func printHelp() {
	fmt.Println(ansiBold + "Available Commands:" + ansiReset)
	fmt.Println()

	// ── Navigation ────────────────────────────────────────────────────────────
	fmt.Println(ansiDim + "Navigation:" + ansiReset)
	fmt.Printf("  %s/help%s      Show this help message\n", ansiCyan, ansiReset)
	fmt.Printf("  %s/clear%s     Clear the terminal screen\n", ansiCyan, ansiReset)
	fmt.Printf("  %s/exit%s      Exit the interactive shell\n", ansiCyan, ansiReset)
	fmt.Println()

	// ── Operations ────────────────────────────────────────────────────────────
	fmt.Println(ansiDim + "Operations:" + ansiReset)
	fmt.Printf("  %s/scan%s      Scan AWS regions for idle resources\n", ansiCyan, ansiReset)
	fmt.Printf("  %s/report%s    Generate an HTML FinOps audit report\n", ansiCyan, ansiReset)
	fmt.Printf("  %s/teardown%s  Safely delete flagged resources\n", ansiCyan, ansiReset)
}
