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
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	goprompt "github.com/c-bata/go-prompt"
	"gopkg.in/yaml.v3"

	"github.com/anirudh/opssweep/internal/audit"
	"github.com/anirudh/opssweep/internal/config"
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
	{Text: "/config", Description: "View the currently active configuration."},
	{Text: "/init", Description: "Generate a default ~/.opssweep.yaml configuration file."},
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

		case "/config":
			runConfig()

		case "/init":
			runInit()

		default:
			fmt.Println(ansiDim + "Unknown command. Type /help" + ansiReset)
		}

		// Blank line between command output and the next prompt.
		fmt.Println()
	}

	// Build and run the prompt engine.
	// OptionPrefix sets the prompt string shown to the left of the cursor.
	// OptionTitle sets the terminal window/tab title (visible in most terminals).
	//
	// Color mapping note: go-prompt's Color constants map to real ANSI codes.
	// "DarkGray" = \e[100m (high-intensity black BG) which most terminals
	// render as LIGHT GRAY — that is what caused the washed-out dropdown.
	// "Black" = \e[40m which is true black and renders dark on all terminals.
	p := goprompt.New(
		executor,
		completer,
		goprompt.OptionPrefix("> "),
		goprompt.OptionTitle("OpsSweep"),

		// ── Unselected suggestion rows ────────────────────────────────────────
		// Black (\e[40m) = true dark background. White (\e[97m) = bright white
		// text. Together they give maximum contrast on any terminal theme.
		goprompt.OptionSuggestionBGColor(goprompt.Black),
		goprompt.OptionSuggestionTextColor(goprompt.White),

		// ── Description column (unselected) ──────────────────────────────────
		// Same black background; LightGray (\e[37m) is slightly dimmer than
		// White, creating a visual hierarchy between command name and description.
		goprompt.OptionDescriptionBGColor(goprompt.Black),
		goprompt.OptionDescriptionTextColor(goprompt.LightGray),

		// ── Selected / highlighted row ────────────────────────────────────────
		// Cyan (\e[46m) bg gives a vivid teal highlight that matches the tool's
		// cyan accent. Black (\e[30m) fg on Cyan has the highest contrast ratio.
		goprompt.OptionSelectedSuggestionBGColor(goprompt.Cyan),
		goprompt.OptionSelectedSuggestionTextColor(goprompt.Black),
		goprompt.OptionSelectedDescriptionBGColor(goprompt.Cyan),
		goprompt.OptionSelectedDescriptionTextColor(goprompt.Black),

		// ── Scrollbar ─────────────────────────────────────────────────────────
		// Black track blends into the dropdown; LightGray thumb is visible
		// without distracting from the suggestion content.
		goprompt.OptionScrollbarBGColor(goprompt.Black),
		goprompt.OptionScrollbarThumbColor(goprompt.LightGray),
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

// runScan executes a full discovery scan, prints the waste table (filtered
// by the confidence threshold in the user's config), and optionally runs live
// deletion when --teardown is present.
//
// The config is loaded fresh on each invocation so changes the user makes to
// ~/.opssweep.yaml take effect on the next /scan without restarting the shell.
func runScan(ctx context.Context, cfg aws.Config, flags []string) {
	// Load user config; fall back to safe defaults if the file does not exist.
	appConfig, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to load config: %v\n", err)
		return
	}

	resources := scanResources(ctx, cfg)
	if resources == nil {
		return
	}

	if err := ui.PrintWasteReport(os.Stdout, resources, appConfig); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to render report: %v\n", err)
		return
	}

	if !hasFlag(flags, "--teardown") {
		return
	}

	fmt.Println()
	fmt.Printf("%s[WARNING] Executing live teardown (confidence threshold: %.0f%%). Resources will be permanently deleted.%s\n",
		ansiRed, appConfig.Rules.ConfidenceThreshold, ansiReset)
	fmt.Println()

	// ── Open the audit database ───────────────────────────────────────────────
	// The database MUST be ready before any deletion starts. If we cannot
	// write to the audit trail we abort the teardown entirely — deleting
	// infrastructure without a record of what was deleted is unacceptable.
	dbPath, err := audit.GetDefaultDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			ansiRed+"[ERROR]"+ansiReset+" Cannot determine audit DB path: %v\n", err,
		)
		return
	}

	db, err := audit.InitDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			ansiRed+"[ERROR]"+ansiReset+" Cannot open audit database: %v\n"+
				"         Teardown aborted — no infrastructure will be deleted.\n", err,
		)
		return
	}
	// Always close the connection, even if Process returns an error.
	defer db.Close()

	fmt.Printf("%s[AUDIT]%s Deletion records will be written to %s\n\n",
		ansiCyan, ansiReset, dbPath)

	// Process applies its own internal 0.90 confidence threshold as a safety
	// floor. isDryRun=false means real AWS deletion API calls are made and
	// every successful deletion is written to the audit DB.
	remediator := remediation.NewRemediator(cfg)
	if err := remediator.Process(ctx, resources, false, db); err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Teardown failed: %v\n", err)
	}
}

// runReport executes a full discovery scan and writes an HTML audit report.
// The output path defaults to "audit.html"; pass --output=<path> to override.
//
// The config is loaded fresh on each invocation so changes to ~/.opssweep.yaml
// take effect immediately without restarting the shell.
func runReport(ctx context.Context, cfg aws.Config, flags []string) {
	// Load user config; fall back to safe defaults if the file does not exist.
	appConfig, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, ansiRed+"[ERROR]"+ansiReset+" Failed to load config: %v\n", err)
		return
	}

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

	// Print the active threshold so the user knows what filter was applied.
	fmt.Printf("%s[REPORT]%s Successfully generated FinOps audit at %s (threshold: %.0f%%)\n",
		ansiGreen, ansiReset, outputPath, appConfig.Rules.ConfidenceThreshold)
}

// runInit generates the default ~/.opssweep.yaml configuration file on disk.
//
// If the file already exists it is overwritten with fresh defaults so the user
// always gets a clean, fully-commented starting point. A green success message
// is printed on completion; a red error message is printed if the write fails
// (e.g. permission denied on the home directory).
func runInit() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			ansiRed+"[ERROR]"+ansiReset+" Could not determine home directory: %v\n", err,
		)
		return
	}

	path := filepath.Join(home, ".opssweep.yaml")

	if err := config.WriteDefaultConfig(path); err != nil {
		fmt.Fprintf(os.Stderr,
			ansiRed+"[ERROR]"+ansiReset+" Failed to write config: %v\n", err,
		)
		return
	}

	fmt.Println(ansiGreen + "[SYSTEM] Successfully generated default configuration at ~/.opssweep.yaml" + ansiReset)
}

// runConfig loads the active configuration and prints it as a formatted YAML
// block so the user can verify exactly which settings OpsSweep is using.
//
// If no config file exists at ~/.opssweep.yaml, LoadDefault returns the
// built-in defaults — the output will show those defaults clearly so the user
// understands what they'd be customising if they ran /init.
func runConfig() {
	cfg, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			ansiRed+"[ERROR]"+ansiReset+" Failed to load config: %v\n", err,
		)
		return
	}

	// Marshal back to YAML so the output exactly matches the file format the
	// user edits. This prevents any confusion between internal field names and
	// the yaml:"..." tag names that appear in ~/.opssweep.yaml.
	out, err := yaml.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			ansiRed+"[ERROR]"+ansiReset+" Failed to format config: %v\n", err,
		)
		return
	}

	fmt.Println(ansiCyan + "[SYSTEM] Active Configuration:" + ansiReset)
	fmt.Print(string(out))
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
		{"/config", "View the currently active configuration"},
		{"/init", "Generate a default ~/.opssweep.yaml config file"},
		{"/clear", "Clear the terminal screen"},
		{"/help", "Show this help message"},
		{"/exit", "Exit the interactive shell"},
	}

	for _, cmd := range commands {
		fmt.Printf("  %s%-28s%s  %s\n", ansiCyan, cmd.usage, ansiReset, cmd.desc)
	}
}
