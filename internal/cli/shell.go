// Package cli implements the interactive REPL shell for OpsSweep.
//
// The shell uses github.com/c-bata/go-prompt to provide a real-time
// autocomplete dropdown as the user types — the same UX pattern used by
// tools like Claude Code and kube-prompt. go-prompt takes over stdin/stdout
// in raw terminal mode, so the bufio.Scanner approach is replaced entirely.
//
// Architecture:
//   - [completer] supplies dropdown suggestions filtered by prefix as the user
//     types. It is called by go-prompt on every keystroke.
//   - [executor] receives the confirmed input string when the user presses
//     Enter. It trims, tokenises, and dispatches to command handlers.
//   - [Start] prints the banner, then hands control to prompt.New(...).Run(),
//     which owns the event loop for the rest of the session.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	goprompt "github.com/c-bata/go-prompt"

	"github.com/anirudh/opssweep/internal/discovery"
	"github.com/anirudh/opssweep/internal/remediation"
	"github.com/anirudh/opssweep/internal/report"
	"github.com/anirudh/opssweep/internal/ui"
)

// ANSI escape codes for styled terminal output.
// go-prompt renders the prompt prefix itself, so these are only used inside
// executor output — not in the prompt string.
const (
	ansiCyan   = "\033[36m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiDim    = "\033[2m"
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
)

// suggestions is the static list of all commands shown in the autocomplete
// dropdown. go-prompt calls completer on every keystroke; completer filters
// this slice by the prefix the user has typed so far.
var suggestions = []goprompt.Suggest{
	{Text: "/scan", Description: "Find idle resources. Pass --teardown to delete."},
	{Text: "/report", Description: "Generate an HTML cost report."},
	{Text: "/clear", Description: "Clear the terminal screen."},
	{Text: "/help", Description: "List all commands."},
	{Text: "/exit", Description: "Exit the application."},
}

// completer is the go-prompt Completer. It is called on every keystroke and
// returns the subset of suggestions whose Text begins with whatever the user
// has typed before the cursor.
//
// ignoreCase=true means "/SC" still matches "/scan", which feels natural.
func completer(d goprompt.Document) []goprompt.Suggest {
	return goprompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}

// Start prints the banner and launches the interactive go-prompt session.
//
// go-prompt.Run() takes over the terminal in raw mode and blocks until the
// process exits. The executor closure captures ctx and cfg so every command
// handler has access to the AWS session without global variables.
func Start(ctx context.Context, cfg aws.Config) {
	ui.PrintBanner()

	// executor is the go-prompt Executor. It is called with the full input
	// line string each time the user presses Enter.
	//
	// It is defined as a closure here so it captures ctx and cfg by reference,
	// making them available to every command handler without threading them
	// through global state.
	executor := func(input string) {
		// Tokenise: strings.Fields splits on any whitespace and returns []
		// for blank input, so there is no index-out-of-bounds risk.
		args := strings.Fields(input)
		if len(args) == 0 {
			return
		}

		command := args[0] // e.g. "/scan"
		flags := args[1:]  // e.g. ["--teardown"] or []

		switch command {

		case "/exit", "quit":
			// go-prompt owns the event loop so we cannot simply return here —
			// we must call os.Exit to terminate the process. A clean farewell
			// message is printed before exiting.
			fmt.Println(ansiGreen + "✓ " + ansiReset + "Goodbye! Session ended.")
			os.Exit(0)

		case "/clear":
			// \033[H — move cursor to home. \033[2J — erase entire display.
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

		// Blank line between command output and the next prompt.
		fmt.Println()
	}

	// Build and run the prompt engine.
	// OptionPrefix sets the prompt string shown to the left of the cursor.
	// OptionTitle sets the terminal window/tab title (visible in most terminals).
	// The color options style the dropdown to match the amber/dark theme.
	p := goprompt.New(
		executor,
		completer,
		goprompt.OptionPrefix("> "),
		goprompt.OptionTitle("OpsSweep"),
		// Dropdown background — dark so it contrasts with the terminal.
		goprompt.OptionSuggestionBGColor(goprompt.DarkGray),
		goprompt.OptionSuggestionTextColor(goprompt.White),
		// Highlighted (selected) row in the dropdown.
		goprompt.OptionSelectedSuggestionBGColor(goprompt.Brown),
		goprompt.OptionSelectedSuggestionTextColor(goprompt.White),
		// Description column (right side of each suggestion row).
		goprompt.OptionDescriptionBGColor(goprompt.DarkGray),
		goprompt.OptionDescriptionTextColor(goprompt.LightGray),
		goprompt.OptionSelectedDescriptionBGColor(goprompt.Brown),
		goprompt.OptionSelectedDescriptionTextColor(goprompt.White),
	)
	p.Run()
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// hasFlag reports whether flag is present anywhere in args.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// scanResources runs the multi-region discovery pipeline and returns the raw
// resource slice. On error it writes to stderr and returns nil; callers check
// for nil before proceeding.
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

// runScan executes a full discovery scan, prints the waste table, and
// optionally runs live deletion when --teardown is present.
func runScan(ctx context.Context, cfg aws.Config, flags []string) {
	resources := scanResources(ctx, cfg)
	if resources == nil {
		return
	}

	if err := ui.PrintWasteReport(os.Stdout, resources); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to render report: %v\n", err)
		return
	}

	if !hasFlag(flags, "--teardown") {
		return
	}

	fmt.Println()
	fmt.Println(ansiRed + "[WARNING] Executing live teardown. Resources will be permanently deleted." + ansiReset)
	fmt.Println()

	// Process takes the full slice and applies the 0.90 confidence threshold
	// internally. isDryRun=false means real AWS deletion API calls are made.
	remediator := remediation.NewRemediator(cfg)
	if err := remediator.Process(ctx, resources, false); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Teardown failed: %v\n", err)
	}
}

// runReport executes a full discovery scan and writes an HTML audit report.
// The output path defaults to "audit.html"; pass --output=<path> to override.
func runReport(ctx context.Context, cfg aws.Config, flags []string) {
	resources := scanResources(ctx, cfg)
	if resources == nil {
		return
	}

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

// printHelp writes a formatted two-column command reference to stdout.
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
