package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

// version and buildDate are injected at build time via ldflags.
var version = "dev"
var buildDate = ""

// Initialized once by main, before any command or updater goroutine starts.
var commandContext = context.Background()

// Change these two constants to move the extension to a different org.
const (
	repoOwner       = "hemsoft-dev"
	repoName        = "gh-x"
	copyrightHolder = "© 2026 HemSoft Developments"
)

var errHelpDisplayed = errors.New("help displayed")

// updateSuccessTimeout and updateErrorTimeout control how long showUpdateNotice
// waits for the async update check. Package-level vars so tests can override.
var (
	updateSuccessTimeout = 500 * time.Millisecond
	updateErrorTimeout   = 2 * time.Second
)

func main() {
	ctx, stop := commandInterruptContext(os.Args[1:])
	commandContext = ctx
	updateCh, err := run(os.Args[1:], os.Stdout, os.Stderr)
	code := completeCommand(os.Stderr, updateCh, err)
	stop()
	if code != 0 {
		os.Exit(code)
	}
}

// Only status needs to intercept interrupts to restore its live terminal frame.
// Other commands retain the default signal behavior of their agent subprocesses.
func commandInterruptContext(args []string) (context.Context, context.CancelFunc) {
	command, _, err := extractTerminalOptions(args)
	if err != nil || len(command) == 0 || (command[0] != "status" && command[0] != "s") {
		return context.Background(), func() {}
	}
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func completeCommand(stderr io.Writer, updateCh <-chan string, err error) int {
	if errors.Is(err, context.Canceled) {
		return 130
	}
	// Print the error before waiting for the update notice so the user gets
	// immediate feedback even when the update check is still in flight.
	timeout := updateSuccessTimeout
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		if hint := errorHint(err); hint != "" {
			fmt.Fprintf(stderr, "Hint: %s\n", hint)
		}
		timeout = updateErrorTimeout
	}
	showUpdateNotice(stderr, updateCh, timeout)
	if err != nil {
		return 1
	}
	return 0
}

type subcommand struct {
	name    string
	banner  bool
	handler func([]string, io.Writer, io.Writer) error
}

// shouldSkipUpdateCheck returns true for subcommands that perform their own
// version check or where an async update check would be inappropriate.
func shouldSkipUpdateCheck(cmd string) bool {
	return cmd == "version" || cmd == "-v" || cmd == "--version"
}

// looksLikeFlag returns true if the argument starts with "-",
// indicating it's a flag rather than a subcommand name.
func looksLikeFlag(arg string) bool {
	return strings.HasPrefix(arg, "-")
}

// looksLikeNumber returns true if the argument is purely digits (a PR/issue number).
func looksLikeNumber(arg string) bool {
	if arg == "" {
		return false
	}
	for _, c := range arg {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func resolveCommand(name string) subcommand {
	commands := []subcommand{
		{"help", true, func(_ []string, out, _ io.Writer) error { writeRootUsage(out); return nil }},
		{"-h", true, func(_ []string, out, _ io.Writer) error { writeRootUsage(out); return nil }},
		{"--help", true, func(_ []string, out, _ io.Writer) error { writeRootUsage(out); return nil }},
		{"version", false, func(_ []string, out, _ io.Writer) error { return runVersion(out) }},
		{"-v", false, func(_ []string, out, _ io.Writer) error { return runVersion(out) }},
		{"--version", false, func(_ []string, out, _ io.Writer) error { return runVersion(out) }},
		{"status", true, runStatus},
		{"s", true, runStatus},
		{"changelog", true, runChangelog},
		{"pr", true, runPr},
		{"run", true, runRunCmd},
		{"issue", true, runIssueCmd},
		{"workflow", true, runWorkflowCmd},
		{"codespace", true, runCodespaceCmd},
		{"monitor", false, runMonitorCmd},
		{"m", false, runMonitorCmd},
	}
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd
		}
	}
	return subcommand{name, true, func(_ []string, _, errw io.Writer) error {
		writeRootUsage(errw)
		return fmt.Errorf("unknown subcommand %q", name)
	}}
}

func resolvePrCommand(name string) subcommand {
	commands := []subcommand{
		{"help", false, func(_ []string, out, _ io.Writer) error { writePrUsage(out); return nil }},
		{"list", false, runList},
		{"view", false, runView},
		{"review", false, runReview},
		{"atm", false, runAtm},
		{"me", false, runMe},
		{"changelog", false, runChangelog},
	}
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd
		}
	}
	return subcommand{name, false, func(_ []string, _, errw io.Writer) error {
		writePrUsage(errw)
		return fmt.Errorf("unknown pr subcommand %q", name)
	}}
}

func runPr(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || looksLikeFlag(args[0]) {
		cmd := resolvePrCommand("list")
		return cmd.handler(args, stdout, stderr)
	}
	// Numeric first arg is shorthand for "view <number>"
	if looksLikeNumber(args[0]) {
		cmd := resolvePrCommand("view")
		return cmd.handler(args, stdout, stderr)
	}
	cmd := resolvePrCommand(args[0])
	return cmd.handler(args[1:], stdout, stderr)
}

func resolveRunCommand(name string) subcommand {
	commands := []subcommand{
		{"help", false, func(_ []string, out, _ io.Writer) error { writeRunUsage(out); return nil }},
		{"list", false, runRunList},
	}
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd
		}
	}
	return subcommand{name, false, func(_ []string, _, errw io.Writer) error {
		writeRunUsage(errw)
		return fmt.Errorf("unknown run subcommand %q", name)
	}}
}

func resolveIssueCommand(name string) subcommand {
	commands := []subcommand{
		{"help", false, func(_ []string, out, _ io.Writer) error { writeIssueUsage(out); return nil }},
		{"list", false, runIssueList},
	}
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd
		}
	}
	return subcommand{name, false, func(_ []string, _, errw io.Writer) error {
		writeIssueUsage(errw)
		return fmt.Errorf("unknown issue subcommand %q", name)
	}}
}

func runIssueCmd(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || looksLikeFlag(args[0]) {
		cmd := resolveIssueCommand("list")
		return cmd.handler(args, stdout, stderr)
	}
	cmd := resolveIssueCommand(args[0])
	return cmd.handler(args[1:], stdout, stderr)
}

func resolveWorkflowCommand(name string) subcommand {
	commands := []subcommand{
		{"help", false, func(_ []string, out, _ io.Writer) error { writeWorkflowUsage(out); return nil }},
		{"list", false, runWorkflowList},
	}
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd
		}
	}
	return subcommand{name, false, func(_ []string, _, errw io.Writer) error {
		writeWorkflowUsage(errw)
		return fmt.Errorf("unknown workflow subcommand %q", name)
	}}
}

func runWorkflowCmd(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || looksLikeFlag(args[0]) {
		cmd := resolveWorkflowCommand("list")
		return cmd.handler(args, stdout, stderr)
	}
	cmd := resolveWorkflowCommand(args[0])
	return cmd.handler(args[1:], stdout, stderr)
}

func resolveCodespaceCommand(name string) subcommand {
	commands := []subcommand{
		{"help", false, func(_ []string, out, _ io.Writer) error { writeCodespaceUsage(out); return nil }},
		{"list", false, runCodespaceList},
	}
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd
		}
	}
	return subcommand{name, false, func(_ []string, _, errw io.Writer) error {
		writeCodespaceUsage(errw)
		return fmt.Errorf("unknown codespace subcommand %q", name)
	}}
}

func runCodespaceCmd(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		cmd := resolveCodespaceCommand("list")
		return cmd.handler(args, stdout, stderr)
	}
	if args[0] == "-h" || args[0] == "--help" {
		writeCodespaceUsage(stdout)
		return nil
	}
	if looksLikeFlag(args[0]) {
		cmd := resolveCodespaceCommand("list")
		return cmd.handler(args, stdout, stderr)
	}
	cmd := resolveCodespaceCommand(args[0])
	return cmd.handler(args[1:], stdout, stderr)
}

func runRunCmd(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || looksLikeFlag(args[0]) {
		cmd := resolveRunCommand("list")
		return cmd.handler(args, stdout, stderr)
	}
	cmd := resolveRunCommand(args[0])
	return cmd.handler(args[1:], stdout, stderr)
}

func run(args []string, stdout io.Writer, stderr io.Writer) (<-chan string, error) {
	args, terminal, err := extractTerminalOptions(args)
	if clearErr := terminal.clearOutput(stdout); clearErr != nil {
		return nil, clearErr
	}
	if err != nil {
		return nil, statusTerminalError(args, terminal, stdout, err)
	}
	var updateCh <-chan string
	statusJSON := isStatusJSONCommand(args, terminal.json)
	skipUpdate := statusJSON || (len(args) > 0 && shouldSkipUpdateCheck(args[0]))
	if !skipUpdate && version != "dev" {
		updateCh = asyncUpdateCheck()
	}

	if len(args) == 0 {
		printBanner(stderr)
		writeRootUsage(stdout)
	} else {
		cmd := resolveCommand(args[0])
		if cmd.banner && !statusJSON {
			printBanner(stderr)
		}
		err = cmd.handler(args[1:], stdout, stderr)
	}

	return updateCh, err
}

func printBanner(w io.Writer) {
	fmt.Fprintf(w, "%s %s %s\n", repoName, formatVersion(version, buildDate), copyrightHolder)
}

func asyncUpdateCheck() <-chan string {
	ch := make(chan string, 1)
	go func() {
		latest, err := fetchAutomaticUpdate()
		if err == nil && latest != "" && isNewerVersion(latest, version) {
			ch <- latest
		}
		close(ch)
	}()
	return ch
}

func showUpdateNotice(w io.Writer, ch <-chan string, timeout time.Duration) {
	if ch == nil {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case latest, ok := <-ch:
		if ok && latest != "" {
			fmt.Fprintf(w, "↑ %s available · gh extension upgrade %s\n", latest, repoName)
		}
	case <-timer.C:
	}
}

func formatVersion(ver, date string) string {
	if date != "" {
		return fmt.Sprintf("%s (%s)", ver, date)
	}
	return ver
}

func runList(args []string, stdout io.Writer, stderr io.Writer) error {
	options, err := parseListOptions(args, stderr)
	if err != nil {
		if errors.Is(err, errHelpDisplayed) {
			return nil
		}

		return err
	}

	options.author, options.assignee, options.search, err = expandListIdentityFilters(options.author, options.assignee, options.search)
	if err != nil {
		return err
	}

	if options.author != "" {
		resolved, err := resolveAuthorLoginFunc(options.author, resolveOrgHint(options.repo))
		if err != nil {
			return err
		}
		options.author = resolved
	}

	return executeListFunc(options, stdout, stderr)
}

func listFlags(options *listOptions, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		writeListUsage(stderr)
	}

	flags.Var(&options.labels, "label", "Filter by label (repeatable)")
	flags.Var(&options.labels, "l", "Filter by label (repeatable)")

	flags.StringVar(&options.repo, "repo", "", "Select another repository using the [HOST/]OWNER/REPO format")
	flags.StringVar(&options.repo, "R", "", "Select another repository using the [HOST/]OWNER/REPO format")
	flags.IntVar(&options.limit, "limit", 30, "Maximum number of pull requests to fetch")
	flags.IntVar(&options.limit, "L", 30, "Maximum number of pull requests to fetch")
	flags.StringVar(&options.state, "state", "open", "Filter by state: open, closed, merged, or all")
	flags.StringVar(&options.state, "s", "open", "Filter by state: open, closed, merged, or all")
	flags.StringVar(&options.author, "author", "", "Filter by author")
	flags.StringVar(&options.author, "A", "", "Filter by author")
	flags.StringVar(&options.assignee, "assignee", "", "Filter by assignee")
	flags.StringVar(&options.assignee, "a", "", "Filter by assignee")
	flags.StringVar(&options.app, "app", "", "Filter by GitHub App author")
	flags.StringVar(&options.base, "base", "", "Filter by base branch")
	flags.StringVar(&options.base, "B", "", "Filter by base branch")
	flags.StringVar(&options.head, "head", "", "Filter by head branch")
	flags.StringVar(&options.head, "H", "", "Filter by head branch")
	flags.StringVar(&options.search, "search", "", "Search pull requests with a GitHub search query")
	flags.StringVar(&options.search, "S", "", "Search pull requests with a GitHub search query")
	flags.BoolVar(&options.draftOnly, "draft", false, "Filter by draft state")
	flags.BoolVar(&options.draftOnly, "d", false, "Filter by draft state")
	flags.BoolVar(&options.web, "web", false, "Open the matching pull requests in the browser")
	flags.BoolVar(&options.web, "w", false, "Open the matching pull requests in the browser")
	flags.BoolVar(&options.json, "json", false, "Output enriched JSON instead of a table")

	return flags
}

func parseListOptions(args []string, stderr io.Writer) (listOptions, error) {
	options := defaultListOptions()

	flags := listFlags(&options, stderr)

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, errHelpDisplayed
		}

		return options, err
	}

	if flags.NArg() > 0 {
		return options, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), ", "))
	}

	return options, validateListOptions(options, flags)
}

func validateListOptions(options listOptions, flags *flag.FlagSet) error {
	if options.limit < 1 {
		return errors.New("limit must be greater than zero")
	}
	return validateOutputOptions(options)
}

func validateOutputOptions(options listOptions) error {
	if options.web && options.json {
		return errors.New("--web and --json cannot be used together")
	}
	return nil
}

func runVersion(w io.Writer) error {
	return runVersionTestable(w, version)
}

func isNewerVersion(candidate, current string) bool {
	candidateParts, candidateOK := parseSemanticVersion(candidate)
	currentParts, currentOK := parseSemanticVersion(current)
	if !candidateOK || !currentOK {
		return false
	}

	for i := range candidateParts {
		if candidateParts[i] != currentParts[i] {
			return candidateParts[i] > currentParts[i]
		}
	}
	return false
}

func parseSemanticVersion(value string) ([3]int, bool) {
	var parts [3]int
	segments := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), "v"), ".")
	if len(segments) != len(parts) {
		return parts, false
	}
	for i, segment := range segments {
		number, err := strconv.Atoi(segment)
		if err != nil || number < 0 {
			return parts, false
		}
		parts[i] = number
	}
	return parts, true
}

func fetchLatestRelease(owner, repo string) (string, error) {
	stdoutBuf, stderrBuf, err := ghExecFunc(
		"api", fmt.Sprintf("repos/%s/%s/releases/latest", owner, repo),
		"--jq", ".tag_name",
	)
	if err != nil {
		return "", fmt.Errorf("%s: %w", stderrBuf.String(), err)
	}
	return strings.TrimSpace(stdoutBuf.String()), nil
}

// fetchLatestReleaseFunc is swapped in tests to avoid real API calls.
var fetchLatestReleaseFunc = fetchLatestRelease

func runVersionTestable(w io.Writer, ver string) error {
	installCmd := "gh extension install " + repoOwner + "/" + repoName
	upgradeCmd := "gh extension upgrade " + repoName

	fmt.Fprintf(w, "%s %s %s · %s\n", repoName, formatVersion(ver, buildDate), copyrightHolder, installCmd)

	latest, err := fetchLatestReleaseFunc(repoOwner, repoName)
	if err != nil || latest == "" {
		fmt.Fprintf(w, "⚠ Could not check for updates\n")
		return nil
	}

	switch {
	case ver == "dev":
		fmt.Fprintf(w, "⚙ Dev build · latest release: %s\n", latest)
	case isNewerVersion(latest, ver):
		fmt.Fprintf(w, "↑ %s available · %s\n", latest, upgradeCmd)
	default:
		fmt.Fprintf(w, "✓ Up to date\n")
	}

	return nil
}

func writeRootUsage(w io.Writer) {
	fmt.Fprint(w, rootUsage)
}

func writePrUsage(w io.Writer) {
	fmt.Fprint(w, prUsage)
}

func writeRunUsage(w io.Writer) {
	fmt.Fprint(w, runUsage)
}

func writeIssueUsage(w io.Writer) {
	fmt.Fprint(w, issueUsage)
}

func writeWorkflowUsage(w io.Writer) {
	fmt.Fprint(w, workflowUsage)
}

func writeListUsage(w io.Writer) {
	fmt.Fprint(w, listUsage)
}

// errorHint returns a user-friendly hint for common error patterns.
// errorHintRule maps error message patterns to user-friendly hints.
type errorHintRule struct {
	patterns []string // all patterns must match (AND logic)
	hint     string
}

var errorHintRules = []errorHintRule{
	{[]string{"401"}, "Your GitHub token may be expired. Run `gh auth login` to re-authenticate."},
	{[]string{"bad credentials"}, "Your GitHub token may be expired. Run `gh auth login` to re-authenticate."},
	{[]string{"403", "saml"}, "Your org requires SSO. Run `gh auth login` and authorize the token for your org."},
	{[]string{"403"}, "Permission denied. Check token scopes with `gh auth status` or re-login with `gh auth login`."},
	{[]string{"could not resolve host"}, "Network error. Check your internet connection or proxy settings."},
	{[]string{"no such host"}, "Network error. Check your internet connection or proxy settings."},
	{[]string{"timeout"}, "Request timed out. GitHub may be experiencing issues, or check your network."},
	{[]string{"timed out"}, "Request timed out. GitHub may be experiencing issues, or check your network."},
	{[]string{"404", "not found"}, "Repository not found. Check the repo name or your access permissions."},
}

func errorHint(err error) string {
	lower := strings.ToLower(err.Error())
	for _, rule := range errorHintRules {
		if matchesAll(lower, rule.patterns) {
			return rule.hint
		}
	}
	return ""
}

func matchesAll(s string, patterns []string) bool {
	for _, p := range patterns {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

const rootUsage = `gh-x adds opinionated commands for GitHub CLI.

Usage:
  gh x [global flags] <command> [flags]

Global flags:
  --clear    Clear the terminal before output (default false)
             May also appear among command flags; ignored for JSON and non-TTY stdout.

Available Commands:
  status     Show repository health, issues, pull requests, and runs (alias: s)
  changelog  Show release notes for recent versions
  pr         Pull request commands (list, me, atm, review, changelog)
  issue      Issue commands (list)
  run        Workflow run commands (list)
  workflow   Workflow commands (list)
  codespace  Codespace commands (list)
  monitor    Multi-repo issues + PRs dashboard (alias: m)
  version    Show version, author, and update availability

Examples:
  gh x status
  gh x s
  gh x changelog
  gh x changelog 3
  gh x pr list
  gh x pr list --author "@me" --state all
  gh x pr me --org AcmeCorp
  gh x pr atm --review-required
  gh x pr review
  gh x pr changelog
  gh x run list
  gh x monitor
  gh x m
  gh x run list --status failure
  gh x run list --workflow "CI" --branch main
  gh x issue list
  gh x issue list --state all --assignee "@me"
  gh x workflow list
  gh x codespace list
  gh x codespace list --org hemsoft-dev
  gh x version
`

const prUsage = `Pull request commands for gh-x.

Usage:
  gh x pr [command] [flags]

Available Commands:
  list       Render a denser pull request list than gh pr list (default)
  me         Show all your open PRs (authored + assigned) across an org
  atm        Show open PRs across an org that need your attention
  review     Run a read-only agentic PR review
  changelog  Show release notes for recent versions

Examples:
  gh x pr list
  gh x pr list --author "@me" --state all
  gh x pr list --json
  gh x pr me
  gh x pr me --org AcmeCorp
  gh x pr atm
  gh x pr atm --org hemsoft-dev
  gh x pr atm --review-required
  gh x pr review 42 --agent codex
  gh x pr changelog
  gh x pr changelog --version 0.3.0
`

const runUsage = `Workflow run commands for gh-x.

Usage:
  gh x run [command] [flags]

Available Commands:
  list       List workflow runs with clickable run IDs (default)

Examples:
  gh x run list
  gh x run list --status failure
  gh x run list --workflow "CI Quality Gates"
  gh x run list --branch main
  gh x run list --limit 50
`

const issueUsage = `Issue commands for gh-x.

Usage:
  gh x issue [command] [flags]

Available Commands:
  list       List issues for the current repository (default)

Examples:
  gh x issue list
  gh x issue list --state all
  gh x issue list --author "@me"
  gh x issue list --assignee octocat
  gh x issue list --label bug --label urgent
  gh x issue list --milestone "Sprint 1"
`

const workflowUsage = `Workflow commands for gh-x.

Usage:
  gh x workflow [command] [flags]

Available Commands:
  list       List workflows for the current repository (default)

Examples:
  gh x workflow list
  gh x workflow list --repo owner/repo
`

const listUsage = `Usage:
  gh x pr list [flags]

Flags:
  -R, --repo string       Select another repository using the [HOST/]OWNER/REPO format
  -L, --limit int         Maximum number of pull requests to fetch (default 30)
  -s, --state string      Filter by state: open, closed, merged, or all (default "open")
  -A, --author string     Filter by author
  -a, --assignee string   Filter by assignee
      --app string        Filter by GitHub App author
  -B, --base string       Filter by base branch
  -H, --head string       Filter by head branch
  -l, --label string      Filter by label (repeatable)
  -S, --search string     Search pull requests with a GitHub search query
  -d, --draft             Filter by draft state
  -w, --web               Open the matching pull requests in the browser
      --json              Output enriched JSON instead of a table
`
