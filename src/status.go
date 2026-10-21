package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/muesli/termenv"
)

type statusSummary struct {
	Branch     string
	Upstream   string
	Ahead      int
	Behind     int
	Staged     int
	Modified   int
	Deleted    int
	Renamed    int
	Untracked  int
	Conflicted int
}

type statusBranchRef struct {
	FullName     string
	ShortName    string
	Upstream     string
	Track        string
	Symref       string
	WorktreePath string
}

type statusBranchInventory struct {
	LocalCount    int
	RemoteCount   int
	DanglingCount int
	Local         map[string]statusBranchRef
	Refs          []statusBranchRef
}

type statusWorktree struct {
	Path             string
	Head             string
	Branch           string
	Detached         bool
	DetachedMerged   bool
	Locked           bool
	Prunable         bool
	PrunableReason   string
	Primary          bool
	Current          bool
	Exists           bool
	Clean            bool
	CleanKnown       bool
	CleanupCandidate bool
	CleanupReason    string
}

type statusDashboard struct {
	progress                  *statusProgress
	localRoot                 string
	WorktreesPending          bool
	cacheState                string
	remoteSections            []statusCacheSection
	remoteCached              [4]bool
	Repository                string
	RepositoryURL             string
	DefaultBranch             string
	DefaultStatus             statusSummary
	DefaultCheckedOut         bool
	DefaultStatusErr          error
	CurrentStatus             statusSummary
	Branches                  statusBranchInventory
	Worktrees                 []statusWorktree
	Stashes                   int
	StashesErr                error
	Issues                    []displayIssue
	IssuesErr                 error
	IssuesRelErr              error
	IssuesHierarchyErr        error
	PullRequests              []displayPullRequest
	PullRequestsErr           error
	PullRequestsSuppErr       error
	RequiredChecksErr         error
	ShowMergedPullRequests    bool
	MergedPullRequests        []displayPullRequest
	MergedPullRequestsErr     error
	MergedPullRequestsSuppErr error
	MergedRequiredChecksErr   error
	WorkflowRuns              []displayWorkflowRun
	WorkflowRunsPerfect       bool
	WorkflowRunsErr           error
}

const (
	statusListLimit          = 30
	statusMergedDefaultLimit = 5
	statusWorkflowRunLimit   = 5
	statusBranchFormat       = "%(refname)%09%(refname:short)%09%(upstream:short)%09%(upstream:track)%09%(symref)"
)

type statusOptions struct {
	mergedLimit int
	refresh     bool
	json        bool
	progress    *statusProgress
}

func runStatus(args []string, stdout io.Writer, stderr io.Writer) error {
	options, err := parseStatusArgs(args, stderr)
	if err != nil {
		return statusArgsError(options, stdout, err)
	}

	if options.json {
		return runStatusJSON(options, stdout)
	}

	colorEnabled := term.FromEnv().IsColorEnabled()
	progress := statusProgressFunc(stdout, colorEnabled)
	if progress != nil {
		defer func() { _ = progress.close() }()
		options.progress = progress
		if err := progress.start(); err != nil {
			return err
		}
	}
	dashboard, err := fetchStatusDashboardFunc(colorEnabled, options)
	if progress != nil {
		if outputErr := progress.close(); outputErr != nil {
			return outputErr
		}
	}
	if err != nil {
		return err
	}
	if commandContext.Err() != nil {
		return commandContext.Err()
	}

	if os.Getenv("GH_X_STATUS_DEBUG") == "1" {
		fmt.Fprintf(stderr, "[gh-x] status cache: %s\n", dashboard.cacheState)
	}
	return renderStatus(stdout, dashboard, colorEnabled)
}

func statusFlags(options *statusOptions, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		writeStatusUsage(stderr)
	}
	flags.IntVar(&options.mergedLimit, "merged", statusMergedDefaultLimit, "number of recently merged pull requests to show; 0 hides the section")
	flags.BoolVar(&options.json, "json", false, "write a versioned JSON status document")
	flags.BoolVar(&options.refresh, "refresh", false, "bypass cached GitHub data and refresh it")

	return flags
}

func parseStatusArgs(args []string, stderr io.Writer) (statusOptions, error) {
	options := statusOptions{mergedLimit: statusMergedDefaultLimit}
	flags := statusFlags(&options, stderr)

	parseErr := flags.Parse(args)
	options.json = statusJSONRequested(args)
	if err := parseErr; err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return statusOptions{}, errHelpDisplayed
		}
		return options, err
	}

	if flags.NArg() > 0 {
		return options, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), ", "))
	}
	if options.mergedLimit < 0 {
		return options, fmt.Errorf("--merged must be 0 or greater")
	}

	return options, nil
}

var (
	fetchStatusDashboardFunc  = fetchStatusDashboard
	statusIssueListFunc       = fetchDisplayIssues
	statusPullRequestListFunc = fetchPullRequestList
	statusWorkflowRunListFunc = fetchWorkflowRunList
	statusRepoLabelFunc       = resolveRepoLabel
	statusRepoURLFunc         = resolveRepoURL
	statusDefaultBranchFunc   = fetchHostedDefaultBranch
	statusNowFunc             = func() time.Time { return time.Now().UTC() }
	statusPathExistsFunc      = statusPathExists
)

func fetchStatusDashboard(colorEnabled bool, options statusOptions) (statusDashboard, error) {
	output, err := statusCommandFunc("git", "status", "--porcelain=v2", "--branch")
	if err != nil {
		return statusDashboard{}, fmt.Errorf("git status: %w", err)
	}

	branchOutput, err := statusCommandFunc("git", "for-each-ref", "--format="+statusBranchFormat, "refs/heads", "refs/remotes")
	if err != nil {
		return statusDashboard{}, fmt.Errorf("git branch inventory: %w", err)
	}
	branches := parseStatusBranchRefs(branchOutput)

	worktreeOutput, err := statusCommandFunc("git", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return statusDashboard{}, fmt.Errorf("git worktree list: %w", err)
	}
	worktrees := parseStatusWorktrees(worktreeOutput)
	attachStatusWorktreePaths(&branches, worktrees)

	root, err := statusCommandFunc("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return statusDashboard{}, fmt.Errorf("git repository root: %w", err)
	}
	currentRoot := strings.TrimRight(root, "\r\n")
	dashboard := statusDashboard{
		progress:               options.progress,
		localRoot:              currentRoot,
		Repository:             filepath.Base(currentRoot),
		DefaultBranch:          resolveStatusDefaultBranch(branches),
		CurrentStatus:          parseGitStatus(output),
		Branches:               branches,
		Worktrees:              worktrees,
		WorktreesPending:       true,
		ShowMergedPullRequests: options.mergedLimit > 0,
	}
	dashboard.Stashes, dashboard.StashesErr = fetchStatusStashes()
	populateStatusDefaultBranch(&dashboard, branches, currentRoot)
	dashboard.progress.local(&dashboard)

	openHeads, pullRequestsKnown := fetchStatusCachedRemote(&dashboard, colorEnabled, options)

	dashboard.progress.begin("Checking worktree cleanup")
	merged, mergedKnown := fetchMergedStatusBranches(dashboard.DefaultBranch)
	dashboard.Worktrees = assessStatusWorktrees(worktrees, currentRoot, dashboard.DefaultBranch, merged, openHeads, mergedKnown, pullRequestsKnown)
	dashboard.WorktreesPending = false
	return dashboard, nil
}

func populateStatusDefaultBranch(dashboard *statusDashboard, branches statusBranchInventory, currentRoot string) {
	if ref, ok := branches.Local[dashboard.DefaultBranch]; ok && filepath.Clean(ref.WorktreePath) == filepath.Clean(currentRoot) && dashboard.CurrentStatus.Branch == dashboard.DefaultBranch {
		dashboard.DefaultStatus, dashboard.DefaultCheckedOut, dashboard.DefaultStatusErr = dashboard.CurrentStatus, true, nil
		return
	}
	dashboard.DefaultStatus, dashboard.DefaultCheckedOut, dashboard.DefaultStatusErr = fetchDefaultBranchStatus(dashboard.DefaultBranch, branches)
}

func updateStatusDefaultBranch(dashboard *statusDashboard, branch string) {
	if dashboard.DefaultBranch == branch {
		return
	}
	dashboard.DefaultBranch = branch
	populateStatusDefaultBranch(dashboard, dashboard.Branches, dashboard.localRoot)
	if dashboard.progress != nil {
		dashboard.progress.update(dashboard)
	}
}

func fetchStatusCachedRemote(dashboard *statusDashboard, colorEnabled bool, options statusOptions) (map[string]bool, bool) {
	now := statusNowFunc()
	cacheDirectory, cacheFingerprint, cacheErr := statusCacheDirectoryFunc()
	var openHeads map[string]bool
	var pullRequestsKnown bool
	cached, cacheHit := lookupStatusCache(options, colorEnabled, now, cacheDirectory, cacheFingerprint, cacheErr)
	dashboard.cacheState = "miss"
	if options.refresh {
		dashboard.cacheState = "refresh"
	}
	if cacheErr != nil {
		dashboard.cacheState = "identity-unavailable"
	}
	if cacheHit {
		dashboard.cacheState = "hit"
		if dashboard.DefaultBranch == "" {
			updateStatusDefaultBranch(dashboard, cached.DefaultBranch)
		}
		openHeads, pullRequestsKnown = applyStatusCache(dashboard, cached, now)
		dashboard.progress.cached(dashboard, now)
		if statusSectionsNeedFetch(cached.Sections, now) {
			dashboard.cacheState = "section-refresh"
			openHeads, pullRequestsKnown = retryStatusSections(dashboard, options, now, openHeads, pullRequestsKnown)
			saveStatusCacheIfSameIdentity(options, colorEnabled, now, *dashboard, openHeads, pullRequestsKnown, cacheDirectory, cacheFingerprint)
		}
	} else {
		// Keep the lookup's target. A concurrent branch switch must not
		// publish the old repository's rows under the new target's key.
		if dashboard.DefaultBranch == "" {
			updateStatusDefaultBranch(dashboard, statusDefaultBranchFunc())
		}
		openHeads, pullRequestsKnown = fetchStatusRemoteData(dashboard, options.mergedLimit, colorEnabled, now)
		if cacheErr == nil {
			saveStatusCacheIfSameIdentity(options, colorEnabled, statusNowFunc(), *dashboard, openHeads, pullRequestsKnown, cacheDirectory, cacheFingerprint)
		}
	}

	return openHeads, pullRequestsKnown
}

func fetchStatusStashes() (int, error) {
	output, err := statusCommandFunc("git", "stash", "list", "--format=%H")
	if err != nil {
		return 0, fmt.Errorf("git stash list: %w", err)
	}
	count := 0
	for _, line := range strings.Split(output, "\n") {
		oid := strings.TrimSpace(line)
		if oid == "" {
			continue
		}
		if !statusStashOIDValid(oid) {
			return 0, fmt.Errorf("git stash list: unexpected output: %s", boundedSingleLine(oid, 200))
		}
		count++
	}
	return count, nil
}

func statusStashOIDValid(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func fetchStatusRemoteData(dashboard *statusDashboard, mergedLimit int, colorEnabled bool, now time.Time) (map[string]bool, bool) {
	dashboard.Repository, dashboard.RepositoryURL = resolveStatusRepository(colorEnabled)
	session := newStatusRemoteSession(dashboard.Repository)
	fetchStatusIssueSection(dashboard, session, now)
	openHeads, known := fetchStatusOpenPRSection(dashboard, session, now)
	fetchStatusMergedSection(dashboard, session, mergedLimit, now)
	fetchStatusRunSection(dashboard, session, now)
	return openHeads, known
}

func fetchStatusIssueSection(dashboard *statusDashboard, session statusRemoteSession, now time.Time) {
	dashboard.progress.begin("Loading open issues")
	defer completeStatusSection(dashboard, 0)
	issueOptions := issueListOptions{repo: session.repo, limit: statusListLimit, state: "open", combinedEnrichment: true}
	dashboard.Issues, dashboard.IssuesRelErr, dashboard.IssuesHierarchyErr = nil, nil, nil
	issueResult, issueErr := statusIssueListFunc(issueOptions, now)
	dashboard.IssuesErr = issueErr
	if issueErr == nil {
		dashboard.Issues = issueResult.Display
		dashboard.IssuesRelErr = issueResult.RelErr
		dashboard.IssuesHierarchyErr = issueResult.HierarchyErr
	}

}

func fetchStatusOpenPRSection(dashboard *statusDashboard, session statusRemoteSession, now time.Time) (map[string]bool, bool) {
	dashboard.progress.begin("Loading open pull requests")
	defer completeStatusSection(dashboard, 1)
	prOptions := defaultListOptions()
	prOptions.repo, prOptions.requiredCache = session.repo, session.rules
	prOptions.limit = statusListLimit
	prOptions.state = "open"
	prResult, prErr := statusPullRequestListFunc(prOptions, now)
	dashboard.PullRequests, dashboard.PullRequestsSuppErr, dashboard.RequiredChecksErr = nil, nil, nil
	dashboard.PullRequestsErr = prErr
	if prErr == nil {
		dashboard.PullRequests = prResult.Rendered
		dashboard.PullRequestsSuppErr = prResult.SupplementalErr
		dashboard.RequiredChecksErr = prResult.RequiredChecksErr
	}
	return openPullRequestHeads(prResult.Entries), prErr == nil && len(prResult.Entries) < prOptions.limit
}

func fetchStatusRunSection(dashboard *statusDashboard, session statusRemoteSession, now time.Time) {
	dashboard.progress.begin("Loading workflow runs")
	defer completeStatusSection(dashboard, 3)
	runOptions := runListOptions{repo: session.repo, limit: statusWorkflowRunLimit}
	runResult, runErr := statusWorkflowRunListFunc(runOptions, now)
	dashboard.WorkflowRunsErr = runErr
	dashboard.WorkflowRuns, dashboard.WorkflowRunsPerfect = nil, false
	if runErr == nil {
		dashboard.WorkflowRuns = runResult.Rendered
		dashboard.WorkflowRunsPerfect = isPerfectWorkflowRunStreak(runResult.Entries)
	}
}

func applyStatusCache(dashboard *statusDashboard, cached statusCacheEntry, now time.Time) (map[string]bool, bool) {
	dashboard.Repository = cached.Repository
	dashboard.RepositoryURL = cached.RepositoryURL
	dashboard.Issues = restoreStatusIssues(cached.Issues, now)
	dashboard.PullRequests = restoreStatusPullRequests(cached.PullRequests, now)
	dashboard.ShowMergedPullRequests = cached.ShowMergedPullRequests
	dashboard.MergedPullRequests = restoreStatusPullRequests(cached.MergedPullRequests, now)
	dashboard.WorkflowRuns = restoreStatusWorkflowRuns(cached.WorkflowRuns, now)
	dashboard.WorkflowRunsPerfect = cached.WorkflowRunsPerfect
	restoreStatusFailures(dashboard, cached.Failures)
	dashboard.remoteSections = append([]statusCacheSection(nil), cached.Sections...)
	if len(dashboard.remoteSections) == 0 {
		dashboard.remoteSections = cacheStatusSections(*dashboard, cached.FetchedAt)
	}
	dashboard.remoteCached = [4]bool{true, true, true, true}
	return statusStringSet(cached.PullRequestHeads), cached.PullRequestsKnown
}

func fetchStatusMergedSection(dashboard *statusDashboard, session statusRemoteSession, limit int, now time.Time) {
	defer completeStatusSection(dashboard, 2)
	if limit == 0 {
		return
	}
	dashboard.progress.begin("Loading recently merged pull requests")
	dashboard.ShowMergedPullRequests = true
	options := defaultListOptions()
	options.repo, options.requiredCache = session.repo, session.rules
	options.limit = limit
	options.state = "merged"
	options.recentlyMerged = true
	result, err := statusPullRequestListFunc(options, now)
	dashboard.MergedPullRequests, dashboard.MergedPullRequestsSuppErr, dashboard.MergedRequiredChecksErr = nil, nil, nil
	dashboard.MergedPullRequestsErr = err
	if err != nil {
		return
	}
	dashboard.MergedPullRequests = result.Rendered
	sort.SliceStable(dashboard.MergedPullRequests, func(i, j int) bool {
		return dashboard.MergedPullRequests[i].mergedAt.After(dashboard.MergedPullRequests[j].mergedAt)
	})
	dashboard.MergedPullRequestsSuppErr = result.SupplementalErr
	dashboard.MergedRequiredChecksErr = result.RequiredChecksErr
}

func resolveStatusRepository(colorEnabled bool) (string, string) {
	repository := statusRepoLabelFunc("")
	if !colorEnabled || repository == "" {
		return repository, ""
	}
	repositoryURL, _ := statusRepoURLFunc("")
	return repository, repositoryURL
}

var statusCommandFunc = runStatusCommand

func runStatusCommand(name string, args ...string) (string, error) {
	cmd := exec.CommandContext(commandContext, name, args...)
	cmd.WaitDelay = githubCommandWaitDelay
	output, err := cmd.CombinedOutput()
	if err != nil {
		if commandContext.Err() != nil {
			return "", commandContext.Err()
		}
		text := strings.TrimSpace(string(output))
		if text != "" {
			return "", fmt.Errorf("%s: %w", text, err)
		}
		return "", err
	}
	return string(output), nil
}

func parseStatusBranchRefs(output string) statusBranchInventory {
	inventory := statusBranchInventory{Local: make(map[string]statusBranchRef)}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimRight(line, "\r") == "" {
			continue
		}
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) != 5 && len(fields) != 6 {
			continue
		}
		ref := statusBranchRef{
			FullName:  fields[0],
			ShortName: fields[1],
			Upstream:  fields[2],
			Track:     fields[3],
			Symref:    fields[4],
		}
		if len(fields) == 6 {
			ref.WorktreePath = fields[5]
		}
		inventory.Refs = append(inventory.Refs, ref)
		classifyStatusBranchRef(&inventory, ref)
	}
	return inventory
}

func attachStatusWorktreePaths(inventory *statusBranchInventory, worktrees []statusWorktree) {
	for _, worktree := range worktrees {
		ref, ok := inventory.Local[worktree.Branch]
		if !ok {
			continue
		}
		ref.WorktreePath = worktree.Path
		inventory.Local[worktree.Branch] = ref
	}
}

func classifyStatusBranchRef(inventory *statusBranchInventory, ref statusBranchRef) {
	switch {
	case strings.HasPrefix(ref.FullName, "refs/heads/"):
		inventory.LocalCount++
		inventory.Local[ref.ShortName] = ref
		if strings.Contains(strings.ToLower(ref.Track), "[gone]") {
			inventory.DanglingCount++
		}
	case strings.HasPrefix(ref.FullName, "refs/remotes/") && ref.Symref == "":
		inventory.RemoteCount++
	}
}

func resolveStatusDefaultBranch(inventory statusBranchInventory) string {
	for _, ref := range inventory.Refs {
		if ref.FullName == "refs/remotes/origin/HEAD" {
			return branchNameFromRemoteHEAD(ref)
		}
	}
	for _, ref := range inventory.Refs {
		if strings.HasSuffix(ref.FullName, "/HEAD") && ref.Symref != "" {
			return branchNameFromRemoteHEAD(ref)
		}
	}
	return ""
}

func branchNameFromRemoteHEAD(ref statusBranchRef) string {
	remotePrefix := strings.TrimSuffix(ref.FullName, "/HEAD") + "/"
	if strings.HasPrefix(ref.Symref, remotePrefix) {
		return strings.TrimPrefix(ref.Symref, remotePrefix)
	}
	return ""
}

func fetchHostedDefaultBranch() string {
	stdout, _, err := ghExecFunc("repo", "view", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

func parseStatusWorktrees(output string) []statusWorktree {
	var worktrees []statusWorktree
	var current statusWorktree
	active := false
	appendCurrent := func() {
		if !active {
			return
		}
		current.Primary = len(worktrees) == 0
		worktrees = append(worktrees, current)
		current = statusWorktree{}
		active = false
	}

	for _, field := range strings.Split(output, "\x00") {
		if field == "" {
			appendCurrent()
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" && active {
			appendCurrent()
		}
		active = applyStatusWorktreeField(&current, key, value) || active
	}
	appendCurrent()
	return worktrees
}

func applyStatusWorktreeField(worktree *statusWorktree, key, value string) bool {
	switch key {
	case "worktree":
		worktree.Path = value
		return true
	case "HEAD":
		worktree.Head = value
	case "branch":
		worktree.Branch = strings.TrimPrefix(value, "refs/heads/")
	case "detached":
		worktree.Detached = true
	case "locked":
		worktree.Locked = true
	case "prunable":
		worktree.Prunable = true
		worktree.PrunableReason = value
	}
	return false
}

func fetchDefaultBranchStatus(branch string, inventory statusBranchInventory) (statusSummary, bool, error) {
	if branch == "" {
		return statusSummary{}, false, errors.New("default branch unavailable")
	}
	ref, ok := inventory.Local[branch]
	if !ok {
		return statusSummary{Branch: branch}, false, fmt.Errorf("local branch %s not found", branch)
	}
	if ref.WorktreePath != "" {
		output, err := statusCommandFunc("git", "-C", ref.WorktreePath, "status", "--porcelain=v2", "--branch")
		if err != nil {
			return statusSummary{Branch: branch, Upstream: ref.Upstream}, true, err
		}
		return parseGitStatus(output), true, nil
	}

	summary := statusSummary{Branch: branch, Upstream: ref.Upstream}
	if ref.Upstream == "" {
		return summary, false, nil
	}
	output, err := statusCommandFunc("git", "rev-list", "--left-right", "--count", branch+"..."+ref.Upstream)
	if err != nil {
		return summary, false, err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 {
		return summary, false, fmt.Errorf("unexpected rev-list output: %q", strings.TrimSpace(output))
	}
	summary.Ahead = parseSignedCount(fields[0])
	summary.Behind = parseSignedCount(fields[1])
	return summary, false, nil
}

func fetchMergedStatusBranches(defaultBranch string) (map[string]bool, bool) {
	if defaultBranch == "" {
		return nil, false
	}
	output, err := statusCommandFunc("git", "for-each-ref", "--merged=refs/heads/"+defaultBranch, "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, false
	}
	merged := make(map[string]bool)
	for _, branch := range strings.Fields(output) {
		merged[branch] = true
	}
	return merged, true
}

func openPullRequestHeads(entries []pullRequest) map[string]bool {
	heads := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.HeadRefName != "" {
			heads[entry.HeadRefName] = true
		}
	}
	return heads
}

func assessStatusWorktrees(worktrees []statusWorktree, currentRoot, defaultBranch string, merged, openHeads map[string]bool, mergedKnown, pullRequestsKnown bool) []statusWorktree {
	for i := range worktrees {
		worktree := &worktrees[i]
		worktree.Current = sameStatusPath(worktree.Path, currentRoot)
		assessStatusWorktreeCleanliness(worktree, defaultBranch)
		assessDetachedWorktreeMerge(worktree, defaultBranch)
		worktree.CleanupReason = statusWorktreeCandidateReason(*worktree, defaultBranch, merged, openHeads, mergedKnown, pullRequestsKnown)
		worktree.CleanupCandidate = worktree.CleanupReason != ""
	}
	return worktrees
}

func assessDetachedWorktreeMerge(worktree *statusWorktree, defaultBranch string) {
	if !worktree.Prunable || !worktree.Detached || worktree.Head == "" || defaultBranch == "" {
		return
	}
	_, err := statusCommandFunc("git", "merge-base", "--is-ancestor", worktree.Head, "refs/heads/"+defaultBranch)
	worktree.DetachedMerged = err == nil
}

func assessStatusWorktreeCleanliness(worktree *statusWorktree, defaultBranch string) {
	if worktree.Prunable || worktree.Primary || worktree.Current || worktree.Locked || worktree.Branch == defaultBranch {
		return
	}
	inspectStatusWorktreeCleanliness(worktree)
}

func inspectStatusWorktreeCleanliness(worktree *statusWorktree) {
	worktree.Exists = statusPathExistsFunc(worktree.Path)
	if !worktree.Exists {
		return
	}
	output, err := statusCommandFunc("git", "-C", worktree.Path, "status", "--porcelain")
	if err != nil {
		return
	}
	worktree.CleanKnown = true
	worktree.Clean = strings.TrimSpace(output) == ""
}

func statusWorktreeCandidateReason(worktree statusWorktree, defaultBranch string, merged, openHeads map[string]bool, mergedKnown, pullRequestsKnown bool) string {
	if statusWorktreeProtected(worktree, defaultBranch) {
		return ""
	}
	if worktree.Prunable {
		if !pullRequestsKnown || (worktree.Detached && !worktree.DetachedMerged) {
			return ""
		}
		if !worktree.Detached && !statusBranchSafeForCleanup(worktree.Branch, merged, openHeads, mergedKnown, pullRequestsKnown) {
			return ""
		}
		if worktree.PrunableReason != "" {
			return "Git marks worktree prunable: " + worktree.PrunableReason
		}
		return "Git marks worktree prunable"
	}
	if !statusLinkedWorktreeSafeForCleanup(worktree, merged, openHeads, mergedKnown, pullRequestsKnown) {
		return ""
	}
	return "clean, merged branch with no open PR"
}

func statusWorktreeProtected(worktree statusWorktree, defaultBranch string) bool {
	return worktree.Primary || worktree.Current || worktree.Locked || worktree.Branch == defaultBranch
}

func statusLinkedWorktreeSafeForCleanup(worktree statusWorktree, merged, openHeads map[string]bool, mergedKnown, pullRequestsKnown bool) bool {
	if worktree.Branch == "" || !worktree.Exists || !worktree.CleanKnown || !worktree.Clean {
		return false
	}
	return statusBranchSafeForCleanup(worktree.Branch, merged, openHeads, mergedKnown, pullRequestsKnown)
}

func statusBranchSafeForCleanup(branch string, merged, openHeads map[string]bool, mergedKnown, pullRequestsKnown bool) bool {
	if !mergedKnown || !pullRequestsKnown {
		return false
	}
	return merged[branch] && !openHeads[branch]
}

func statusPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sameStatusPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func parseGitStatus(output string) statusSummary {
	var summary statusSummary
	seen := make(map[string]bool)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}

		parseGitStatusLine(line, seen, &summary)
	}

	return summary
}

func parseGitStatusLine(line string, seen map[string]bool, summary *statusSummary) {
	switch {
	case strings.HasPrefix(line, "# "):
		parseGitStatusHeader(line, summary)
	case strings.HasPrefix(line, "? "):
		if markSeen(seen, strings.TrimPrefix(line, "? ")) {
			summary.Untracked++
		}
	case strings.HasPrefix(line, "u "):
		if path := statusPath(line); markSeen(seen, path) {
			summary.Conflicted++
		}
	case strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 "):
		if path := statusPath(line); markSeen(seen, path) {
			applyStatusXY(statusXY(line), summary)
		}
	}
}

func parseGitStatusHeader(line string, summary *statusSummary) {
	switch {
	case strings.HasPrefix(line, "# branch.head "):
		summary.Branch = strings.TrimPrefix(line, "# branch.head ")
	case strings.HasPrefix(line, "# branch.upstream "):
		summary.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
	case strings.HasPrefix(line, "# branch.ab "):
		fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
		if len(fields) == 2 {
			summary.Ahead = parseSignedCount(fields[0])
			summary.Behind = parseSignedCount(fields[1])
		}
	}
}

func parseSignedCount(value string) int {
	value = strings.TrimPrefix(value, "+")
	n, _ := strconv.Atoi(value)
	if n < 0 {
		return -n
	}
	return n
}

func statusXY(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

func statusPath(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return line
	}
	return fields[len(fields)-1]
}

func markSeen(seen map[string]bool, path string) bool {
	if path == "" || seen[path] {
		return false
	}
	seen[path] = true
	return true
}

func applyStatusXY(xy string, summary *statusSummary) {
	if len(xy) < 2 {
		return
	}

	x := xy[0]
	y := xy[1]

	if x != '.' {
		summary.Staged++
	}
	if x == 'R' {
		summary.Renamed++
	}
	if x == 'M' || y == 'M' {
		summary.Modified++
	}
	if x == 'D' || y == 'D' {
		summary.Deleted++
	}
}

func renderStatus(stdout io.Writer, dashboard statusDashboard, colorEnabled bool) error {
	styler := newTableStyler(stdout, colorEnabled)
	renderStatusHeader(stdout, styler, dashboard)
	if err := renderStatusIssueSection(stdout, styler, dashboard); err != nil {
		return err
	}
	if err := renderStatusPullRequestSection(stdout, styler, dashboard); err != nil {
		return err
	}
	if err := renderStatusMergedPullRequestSection(stdout, styler, dashboard); err != nil {
		return err
	}
	return renderStatusWorkflowRunSection(stdout, styler, dashboard)
}

func isPerfectWorkflowRunStreak(runs []workflowRun) bool {
	if len(runs) != statusWorkflowRunLimit {
		return false
	}
	for _, run := range runs {
		if run.Status != "completed" || run.Conclusion != "success" {
			return false
		}
	}
	return true
}

func hasWorkingChanges(summary statusSummary) bool {
	return summary.Staged+summary.Modified+summary.Deleted+summary.Renamed+summary.Untracked+summary.Conflicted > 0
}

func changeStatusText(summary statusSummary) string {
	parts := make([]string, 0, 6)
	appendCount := func(count int, singular, pluralText string) {
		if count > 0 {
			parts = append(parts, plural(count, singular, pluralText))
		}
	}

	appendCount(summary.Staged, "staged file", "staged files")
	appendCount(summary.Modified, "modified file", "modified files")
	appendCount(summary.Deleted, "deleted file", "deleted files")
	appendCount(summary.Renamed, "renamed file", "renamed files")
	appendCount(summary.Untracked, "untracked file", "untracked files")
	appendCount(summary.Conflicted, "conflicted file", "conflicted files")

	if len(parts) == 0 {
		return "Clean working tree."
	}
	return strings.Join(parts, ", ") + "."
}

func plural(count int, singular, pluralText string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, pluralText)
}

type statusSeverity int

const (
	statusHealthy statusSeverity = iota
	statusAttention
	statusBlocked
	statusUnavailable
)

func renderStatusHeader(stdout io.Writer, styler tableStyler, dashboard statusDashboard) {
	worktreeRow := statusWorktreeInventoryRow(styler, dashboard.Worktrees)
	if dashboard.WorktreesPending {
		worktreeRow = []tableCell{styler.plain(""), styler.dim("Worktrees"), styler.dim(fmt.Sprintf("%s · cleanup pending", plural(len(dashboard.Worktrees), "total", "total")))}
	}
	rows := [][]tableCell{
		{styler.plain(""), styler.dim("Repository"), statusRepositoryCell(styler, dashboard.Repository, dashboard.RepositoryURL)},
		{styler.plain(""), styler.dim("Local time"), styler.plain(statusNowFunc().Local().Format("2006-01-02 03:04 PM MST"))},
		statusDefaultBranchRow(styler, dashboard),
		statusBranchInventoryRow(styler, dashboard.Branches),
		worktreeRow,
		statusStashInventoryRow(styler, dashboard.Stashes, dashboard.StashesErr),
	}
	if dashboard.CurrentStatus.Branch != dashboard.DefaultBranch || dashboard.DefaultStatusErr != nil {
		rows = append(rows, []tableCell{styler.plain(""), styler.dim("Current"), statusCurrentBranchCell(styler, dashboard.CurrentStatus)})
	}

	widths := computeColumnWidths([]tableCell{styler.plain("✓"), styler.plain(""), styler.plain("")}, rows)
	for _, row := range rows {
		writeRow(stdout, row, widths)
	}
	renderStatusCleanupCandidates(stdout, styler, dashboard.Worktrees)
}

func statusRepositoryCell(styler tableStyler, repository, repositoryURL string) tableCell {
	if repository == "" {
		return statusHeaderValue(styler, "unavailable", statusUnavailable)
	}
	return styler.linkCell(repository, repositoryURL, termenv.ANSIGreen)
}

func statusDefaultBranchLabel(branch string) string {
	if branch == "" {
		return "Default"
	}
	runes := []rune(branch)
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

func statusDefaultBranchCell(styler tableStyler, dashboard statusDashboard) tableCell {
	if dashboard.DefaultStatusErr != nil {
		return statusHeaderValue(styler, conciseStatusError(dashboard.DefaultStatusErr), statusUnavailable)
	}
	return statusBranchCell(styler, dashboard.DefaultStatus, dashboard.DefaultCheckedOut)
}

func statusDefaultBranchRow(styler tableStyler, dashboard statusDashboard) []tableCell {
	marker := styler.plain("")
	ref := dashboard.Branches.Local[dashboard.DefaultBranch]
	if statusDefaultBranchClean(dashboard, ref) {
		marker = styler.colored("✓", termenv.ANSIGreen)
	}
	return []tableCell{marker, styler.dim(statusDefaultBranchLabel(dashboard.DefaultBranch)), statusDefaultBranchCell(styler, dashboard)}
}

func statusCurrentBranchCell(styler tableStyler, summary statusSummary) tableCell {
	branch := summary.Branch
	if branch == "" {
		branch = "detached HEAD"
	}
	cell := statusBranchCell(styler, summary, true)
	cell.text = branch + " · " + cell.text
	cell.styled = branch + " · " + cell.styled
	return cell
}

func statusBranchCell(styler tableStyler, summary statusSummary, checkedOut bool) tableCell {
	parts := []string{compactBranchStatusText(summary)}
	if checkedOut {
		parts = append(parts, strings.TrimSuffix(changeStatusText(summary), "."))
	} else {
		parts = append(parts, "not checked out")
	}
	return statusHeaderValue(styler, strings.Join(parts, " · "), statusBranchSeverity(summary))
}

func compactBranchStatusText(summary statusSummary) string {
	if summary.Upstream == "" {
		return "no upstream configured"
	}
	switch {
	case summary.Ahead == 0 && summary.Behind == 0:
		return "synced with " + summary.Upstream
	case summary.Ahead > 0 && summary.Behind == 0:
		return fmt.Sprintf("ahead of %s by %s", summary.Upstream, plural(summary.Ahead, "commit", "commits"))
	case summary.Ahead == 0 && summary.Behind > 0:
		return fmt.Sprintf("behind %s by %s", summary.Upstream, plural(summary.Behind, "commit", "commits"))
	default:
		return fmt.Sprintf("diverged from %s: %d ahead, %d behind", summary.Upstream, summary.Ahead, summary.Behind)
	}
}

func statusBranchSeverity(summary statusSummary) statusSeverity {
	if summary.Behind > 0 || summary.Conflicted > 0 {
		return statusBlocked
	}
	if summary.Ahead > 0 || summary.Upstream == "" || hasWorkingChanges(summary) {
		return statusAttention
	}
	return statusHealthy
}

func statusBranchInventoryRow(styler tableStyler, inventory statusBranchInventory) []tableCell {
	text := fmt.Sprintf("%d local (%d dangling) · %d remote", inventory.LocalCount, inventory.DanglingCount, inventory.RemoteCount)
	clean := statusBranchesClean(inventory)
	return statusInventoryRow(styler, "Branches", text, clean)
}

func statusWorktreeInventoryRow(styler tableStyler, worktrees []statusWorktree) []tableCell {
	candidates := statusCleanupCandidateCount(worktrees)
	text := fmt.Sprintf("%d total · %d cleanup %s", len(worktrees), candidates, pluralWord(candidates, "candidate", "candidates"))
	return statusInventoryRow(styler, "Worktrees", text, statusWorktreesClean(worktrees))
}

func statusStashInventoryRow(styler tableStyler, count int, err error) []tableCell {
	if err != nil {
		text := boundedSingleLine("Unavailable: "+conciseStatusError(err), 60)
		return []tableCell{styler.plain(""), styler.dim("Stashes"), statusHeaderValue(styler, text, statusUnavailable)}
	}
	return statusInventoryRow(styler, "Stashes", plural(count, "stash", "stashes"), count == 0)
}

func statusInventoryRow(styler tableStyler, label, text string, clean bool) []tableCell {
	marker := styler.plain("")
	severity := statusAttention
	if clean {
		marker = styler.colored("✓", termenv.ANSIGreen)
		severity = statusHealthy
	}
	return []tableCell{marker, styler.dim(label), statusHeaderValue(styler, text, severity)}
}

func pluralWord(count int, singular, pluralText string) string {
	if count == 1 {
		return singular
	}
	return pluralText
}

func statusHeaderValue(styler tableStyler, text string, severity statusSeverity) tableCell {
	switch severity {
	case statusAttention:
		return styler.colored(text, termenv.ANSIYellow)
	case statusBlocked:
		return styler.colored(text, termenv.ANSIRed)
	case statusUnavailable:
		return styler.dim(text)
	default:
		return styler.colored(text, termenv.ANSIGreen)
	}
}

func statusCleanupCandidateCount(worktrees []statusWorktree) int {
	count := 0
	for _, worktree := range worktrees {
		if worktree.CleanupCandidate {
			count++
		}
	}
	return count
}

func renderStatusCleanupCandidates(stdout io.Writer, styler tableStyler, worktrees []statusWorktree) {
	candidates := make([]statusWorktree, 0)
	for _, worktree := range worktrees {
		if worktree.CleanupCandidate {
			candidates = append(candidates, worktree)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
	for _, candidate := range candidates {
		branch := candidate.Branch
		if branch == "" {
			branch = "detached"
		}
		text := fmt.Sprintf("  %s — %s (%s)", branch, candidate.Path, candidate.CleanupReason)
		fmt.Fprintln(stdout, styler.colored(text, termenv.ANSIYellow).styled)
	}
}

func renderStatusIssueSection(stdout io.Writer, styler tableStyler, dashboard statusDashboard) error {
	fmt.Fprintln(stdout)
	if dashboard.IssuesErr != nil {
		fmt.Fprintln(stdout, "Open issues")
		fmt.Fprintln(stdout, styler.dim("Unavailable: "+conciseStatusError(dashboard.IssuesErr)).styled)
		return nil
	}
	fmt.Fprintf(stdout, "Open issues (%s)\n", statusSectionCount(len(dashboard.Issues)))
	if len(dashboard.Issues) == 0 {
		writeBacklogPraise(stdout)
		return nil
	}
	if err := renderIssueRows(stdout, dashboard.Issues, styler.colorEnabled); err != nil {
		return err
	}
	if dashboard.IssuesRelErr != nil {
		fmt.Fprintln(stdout, styler.dim("Pull request relationships unavailable: "+conciseStatusError(dashboard.IssuesRelErr)).styled)
	}
	if dashboard.IssuesHierarchyErr != nil {
		fmt.Fprintln(stdout, styler.dim("Issue hierarchy unavailable: "+conciseStatusError(dashboard.IssuesHierarchyErr)).styled)
	}
	if len(dashboard.Issues) >= statusListLimit {
		fmt.Fprintf(stdout, "\nShowing %d issues (limit reached). Use gh x issue list --limit to show more.\n", statusListLimit)
	}
	return nil
}

func renderStatusPullRequestSection(stdout io.Writer, styler tableStyler, dashboard statusDashboard) error {
	fmt.Fprintln(stdout)
	if dashboard.PullRequestsErr != nil {
		fmt.Fprintln(stdout, "Open pull requests")
		fmt.Fprintln(stdout, styler.dim("Unavailable: "+conciseStatusError(dashboard.PullRequestsErr)).styled)
		return nil
	}
	fmt.Fprintf(stdout, "Open pull requests (%s)\n", statusSectionCount(len(dashboard.PullRequests)))
	if dashboard.PullRequestsSuppErr != nil {
		fmt.Fprintln(stdout, styler.dim(supplementalNotice(dashboard.PullRequestsSuppErr)).styled)
	}
	if dashboard.RequiredChecksErr != nil {
		fmt.Fprintln(stdout, styler.dim("Required check rules unavailable: "+boundedSingleLine(dashboard.RequiredChecksErr.Error(), 500)).styled)
	}
	if len(dashboard.PullRequests) == 0 {
		writeBacklogPraise(stdout)
		return nil
	}
	if err := renderPullRequestRows(stdout, dashboard.PullRequests, styler.colorEnabled); err != nil {
		return err
	}
	if len(dashboard.PullRequests) >= statusListLimit {
		fmt.Fprintf(stdout, "\nShowing %d pull requests (limit reached). Use gh x pr list --limit to show more.\n", statusListLimit)
	}
	return nil
}

func renderStatusMergedPullRequestSection(stdout io.Writer, styler tableStyler, dashboard statusDashboard) error {
	if !dashboard.ShowMergedPullRequests {
		return nil
	}
	fmt.Fprintln(stdout)
	if dashboard.MergedPullRequestsErr != nil {
		fmt.Fprintln(stdout, "Recently merged pull requests")
		fmt.Fprintln(stdout, styler.dim("Unavailable: "+conciseStatusError(dashboard.MergedPullRequestsErr)).styled)
		return nil
	}
	fmt.Fprintf(stdout, "Recently merged pull requests (%d)\n", len(dashboard.MergedPullRequests))
	if dashboard.MergedPullRequestsSuppErr != nil {
		fmt.Fprintln(stdout, styler.dim(supplementalNotice(dashboard.MergedPullRequestsSuppErr)).styled)
	}
	if dashboard.MergedRequiredChecksErr != nil {
		fmt.Fprintln(stdout, styler.dim("Required check rules unavailable: "+boundedSingleLine(dashboard.MergedRequiredChecksErr.Error(), 500)).styled)
	}
	if len(dashboard.MergedPullRequests) == 0 {
		fmt.Fprintln(stdout, "No merged pull requests found.")
		return nil
	}
	return renderPullRequestRows(stdout, dashboard.MergedPullRequests, styler.colorEnabled)
}

func renderStatusWorkflowRunSection(stdout io.Writer, styler tableStyler, dashboard statusDashboard) error {
	fmt.Fprintln(stdout)
	if dashboard.WorkflowRunsErr != nil {
		fmt.Fprintln(stdout, "Recent workflow runs")
		fmt.Fprintln(stdout, styler.dim("Unavailable: "+conciseStatusError(dashboard.WorkflowRunsErr)).styled)
		return nil
	}
	fmt.Fprintf(stdout, "Recent workflow runs (%d)\n", len(dashboard.WorkflowRuns))
	if len(dashboard.WorkflowRuns) == 0 {
		fmt.Fprintln(stdout, "No workflow runs found.")
		return nil
	}
	renderWorkflowRunRows(stdout, dashboard.WorkflowRuns, styler.colorEnabled)
	if dashboard.WorkflowRunsPerfect {
		fmt.Fprintln(stdout)
		writeWorkflowPerfectionPraise(stdout)
	}
	return nil
}

func statusSectionCount(count int) string {
	if count >= statusListLimit {
		return fmt.Sprintf("%d+", count)
	}
	return strconv.Itoa(count)
}

func conciseStatusError(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	return trimTitle(text, 120)
}

func writeStatusUsage(w io.Writer) {
	fmt.Fprint(w, statusUsage)
}

const statusUsage = `Usage:
  gh x status [flags]

Show repository health, branches, worktrees, stashes, open issues, open pull requests,
recently merged pull requests, and the five most recent workflow runs.

Flags:
      --merged int   Number of recently merged pull requests to show; 0 hides the section (default 5)
      --json         Write one versioned JSON status document
      --refresh      Bypass cached GitHub data and refresh it
`
