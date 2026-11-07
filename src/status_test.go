package main

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/repository"
)

func TestParseGitStatus(t *testing.T) {
	output := `# branch.oid abc123
# branch.head main
# branch.upstream origin/main
# branch.ab +0 -0
1 .M N... 100644 100644 100644 abc abc CHANGELOG.md
1 .M N... 100644 100644 100644 abc abc workflow.go
1 .M N... 100644 100644 100644 abc abc workflow_test.go
? status.go
`

	got := parseGitStatus(output)
	if got.Branch != "main" || got.Upstream != "origin/main" {
		t.Fatalf("unexpected branch status: %#v", got)
	}
	if got.Ahead != 0 || got.Behind != 0 {
		t.Fatalf("expected even branch, got ahead=%d behind=%d", got.Ahead, got.Behind)
	}
	if got.Modified != 3 || got.Untracked != 1 {
		t.Fatalf("unexpected change counts: %#v", got)
	}
}

func TestChangeStatusText(t *testing.T) {
	got := changeStatusText(statusSummary{Modified: 3, Untracked: 1})
	if want := "3 modified files, 1 untracked file."; got != want {
		t.Fatalf("changeStatusText() = %q, want %q", got, want)
	}
	if got := changeStatusText(statusSummary{}); got != "Clean working tree." {
		t.Fatalf("expected clean working tree, got %q", got)
	}
}

func TestParseStatusBranchRefs(t *testing.T) {
	output := strings.Join([]string{
		"refs/heads/main\tmain\torigin/main\t\t",
		"refs/heads/old\told\torigin/old\t[gone]\t",
		"refs/remotes/origin/HEAD\torigin\t\t\trefs/remotes/origin/main",
		"refs/remotes/origin/main\torigin/main\t\t\t",
	}, "\n") + "\n"

	got := parseStatusBranchRefs(output)
	attachStatusWorktreePaths(&got, []statusWorktree{{Path: "C:/repo", Branch: "main"}})
	if got.LocalCount != 2 || got.RemoteCount != 1 || got.DanglingCount != 1 {
		t.Fatalf("unexpected branch inventory: %#v", got)
	}
	if got.Local["main"].WorktreePath != "C:/repo" {
		t.Fatalf("expected main worktree path, got %#v", got.Local["main"])
	}
	if defaultBranch := resolveStatusDefaultBranch(got); defaultBranch != "main" {
		t.Fatalf("default branch = %q, want main", defaultBranch)
	}
}

func TestFetchHostedDefaultBranch(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		if got := strings.Join(args, " "); got != "repo view --json defaultBranchRef --jq .defaultBranchRef.name" {
			t.Fatalf("unexpected arguments: %s", got)
		}
		return *bytes.NewBufferString("trunk\n"), bytes.Buffer{}, nil
	}

	if got := fetchHostedDefaultBranch(); got != "trunk" {
		t.Fatalf("hosted default branch = %q, want trunk", got)
	}
}

func TestParseStatusWorktrees(t *testing.T) {
	output := strings.Join([]string{
		"worktree C:/repo", "HEAD abc", "branch refs/heads/main", "",
		"worktree C:/repo worktrees/locked", "HEAD def", "branch refs/heads/locked", "locked maintenance", "",
		"worktree C:/missing", "HEAD fed", "detached", "prunable gitdir file points to non-existent location", "",
	}, "\x00")

	got := parseStatusWorktrees(output)
	if len(got) != 3 {
		t.Fatalf("worktree count = %d, want 3: %#v", len(got), got)
	}
	if !got[0].Primary || got[0].Branch != "main" {
		t.Fatalf("unexpected primary worktree: %#v", got[0])
	}
	if !got[1].Locked || got[1].Path != "C:/repo worktrees/locked" {
		t.Fatalf("unexpected locked worktree: %#v", got[1])
	}
	if !got[2].Prunable || !got[2].Detached || got[2].Head != "fed" || !strings.Contains(got[2].PrunableReason, "non-existent") {
		t.Fatalf("unexpected prunable worktree: %#v", got[2])
	}
}

func TestStatusWorktreeCandidateReason(t *testing.T) {
	base := statusWorktree{Path: "C:/wt", Branch: "feature", Exists: true, CleanKnown: true, Clean: true}
	merged := map[string]bool{"feature": true}
	tests := []struct {
		name        string
		mutate      func(*statusWorktree)
		openHeads   map[string]bool
		mergedKnown bool
		prsKnown    bool
		want        bool
	}{
		{name: "clean merged no open PR", mergedKnown: true, prsKnown: true, want: true},
		{name: "current worktree", mutate: func(w *statusWorktree) { w.Current = true }, mergedKnown: true, prsKnown: true},
		{name: "primary worktree", mutate: func(w *statusWorktree) { w.Primary = true }, mergedKnown: true, prsKnown: true},
		{name: "locked", mutate: func(w *statusWorktree) { w.Locked = true }, mergedKnown: true, prsKnown: true},
		{name: "default branch", mutate: func(w *statusWorktree) { w.Branch = "main" }, mergedKnown: true, prsKnown: true},
		{name: "dirty", mutate: func(w *statusWorktree) { w.Clean = false }, mergedKnown: true, prsKnown: true},
		{name: "unmerged", mergedKnown: false, prsKnown: true},
		{name: "open PR", openHeads: map[string]bool{"feature": true}, mergedKnown: true, prsKnown: true},
		{name: "PR state unavailable", mergedKnown: true, prsKnown: false},
		{name: "Git prunable", mutate: func(w *statusWorktree) { w.Prunable = true; w.Exists = false }, mergedKnown: true, prsKnown: true, want: true},
		{name: "unmerged Git prunable", mutate: func(w *statusWorktree) { w.Prunable = true; w.Exists = false }, prsKnown: true},
		{name: "Git prunable with open PR", mutate: func(w *statusWorktree) { w.Prunable = true; w.Exists = false }, openHeads: map[string]bool{"feature": true}, mergedKnown: true, prsKnown: true},
		{name: "Git prunable with PR state unavailable", mutate: func(w *statusWorktree) { w.Prunable = true; w.Exists = false }, mergedKnown: true},
		{name: "locked Git prunable", mutate: func(w *statusWorktree) { w.Prunable = true; w.Locked = true }},
		{name: "merged detached Git prunable", mutate: func(w *statusWorktree) { w.Prunable = true; w.Detached = true; w.DetachedMerged = true; w.Branch = "" }, prsKnown: true, want: true},
		{name: "merged detached Git prunable with PR state unavailable", mutate: func(w *statusWorktree) { w.Prunable = true; w.Detached = true; w.DetachedMerged = true; w.Branch = "" }},
		{name: "unmerged detached Git prunable", mutate: func(w *statusWorktree) { w.Prunable = true; w.Detached = true; w.Branch = "" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			worktree := base
			if tc.mutate != nil {
				tc.mutate(&worktree)
			}
			reason := statusWorktreeCandidateReason(worktree, "main", merged, tc.openHeads, tc.mergedKnown, tc.prsKnown)
			if got := reason != ""; got != tc.want {
				t.Fatalf("candidate = %v (%q), want %v", got, reason, tc.want)
			}
		})
	}
}

func TestAssessDetachedWorktreeMerge(t *testing.T) {
	defer saveStatusFuncs()()
	statusCommandFunc = func(name string, args ...string) (string, error) {
		if got := name + " " + strings.Join(args, " "); got != "git merge-base --is-ancestor abc refs/heads/main" {
			return "", fmt.Errorf("unexpected command: %s", got)
		}
		return "", nil
	}

	worktree := statusWorktree{Head: "abc", Detached: true, Prunable: true}
	assessDetachedWorktreeMerge(&worktree, "main")
	if !worktree.DetachedMerged {
		t.Fatal("expected detached HEAD merged into main")
	}
}

func TestFetchDefaultBranchStatusNotCheckedOut(t *testing.T) {
	defer saveStatusFuncs()()
	statusCommandFunc = func(name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		if key != "git rev-list --left-right --count refs/heads/trunk...origin/trunk" {
			return "", fmt.Errorf("unexpected command: %s", key)
		}
		return "2\t1\n", nil
	}
	inventory := statusBranchInventory{Local: map[string]statusBranchRef{
		"trunk": {ShortName: "trunk", Upstream: "origin/trunk"},
	}}

	got, checkedOut, err := fetchDefaultBranchStatus("trunk", inventory)
	if err != nil {
		t.Fatal(err)
	}
	if checkedOut || got.Ahead != 2 || got.Behind != 1 {
		t.Fatalf("unexpected branch status: checkedOut=%v summary=%#v", checkedOut, got)
	}
}

func TestStatusDefaultBranchStates(t *testing.T) {
	styler := newTableStyler(&bytes.Buffer{}, false)
	tests := []struct {
		name      string
		dashboard statusDashboard
		want      string
	}{
		{name: "clean and synchronized", dashboard: statusDashboard{DefaultStatus: statusSummary{Upstream: "origin/main"}, DefaultCheckedOut: true}, want: "synced with origin/main · Clean working tree"},
		{name: "ahead", dashboard: statusDashboard{DefaultStatus: statusSummary{Upstream: "origin/main", Ahead: 2}}, want: "ahead of origin/main by 2 commits · not checked out"},
		{name: "behind", dashboard: statusDashboard{DefaultStatus: statusSummary{Upstream: "origin/main", Behind: 1}}, want: "behind origin/main by 1 commit · not checked out"},
		{name: "diverged", dashboard: statusDashboard{DefaultStatus: statusSummary{Upstream: "origin/main", Ahead: 1, Behind: 3}}, want: "diverged from origin/main: 1 ahead, 3 behind · not checked out"},
		{name: "missing upstream", dashboard: statusDashboard{DefaultStatus: statusSummary{Branch: "trunk"}}, want: "no upstream configured · not checked out"},
		{name: "unavailable", dashboard: statusDashboard{DefaultStatusErr: errors.New("default unavailable")}, want: "default unavailable"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusDefaultBranchCell(styler, tc.dashboard).text; got != tc.want {
				t.Fatalf("default branch cell = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchStatusDashboard(t *testing.T) {
	t.Setenv("GH_REPO", "")
	defer saveStatusFuncs()()
	savedCurrent := repositoryCurrentFunc
	t.Cleanup(func() { repositoryCurrentFunc = savedCurrent })
	repositoryCurrentFunc = func() (repository.Repository, error) {
		return repository.Repository{Host: "github.com", Owner: "owner", Name: "repo"}, nil
	}
	statusRepoLabelFunc = func(string) string { return "owner/repo" }
	statusRepoURLFunc = func(string) (string, error) { return "https://github.com/owner/repo", nil }
	statusPathExistsFunc = func(string) bool { return true }
	statusNowFunc = func() time.Time { return time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC) }
	statusIssueListFunc = func(options issueListOptions, _ time.Time) (issueListResult, error) {
		if options.limit != statusListLimit || options.state != "open" {
			t.Fatalf("unexpected issue options: %#v", options)
		}
		return issueListResult{Display: []displayIssue{{Number: 7, Title: "Status dashboard", State: "open"}}}, nil
	}
	statusPullRequestListFunc = func(options listOptions, _ time.Time) (pullRequestListResult, error) {
		switch options.state {
		case "open":
			if options.limit != statusListLimit || options.search != "" {
				t.Fatalf("unexpected open PR options: %#v", options)
			}
			return pullRequestListResult{
				Entries:  []pullRequest{{HeadRefName: "open-branch"}},
				Rendered: []displayPullRequest{{Number: 2, Title: "Open PR", State: "open"}},
			}, nil
		case "merged":
			if options.limit != statusMergedDefaultLimit || options.search != "" || !options.recentlyMerged {
				t.Fatalf("unexpected merged PR options: %#v", options)
			}
			return pullRequestListResult{Rendered: []displayPullRequest{
				{Number: 137, Title: "Older merge", State: "merged", mergedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)},
				{Number: 138, Title: "Newest merge", State: "merged", mergedAt: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)},
			}}, nil
		default:
			t.Fatalf("unexpected PR state: %#v", options)
			return pullRequestListResult{}, nil
		}
	}
	statusWorkflowRunListFunc = func(options runListOptions, now time.Time) (workflowRunListResult, error) {
		if options.limit != statusWorkflowRunLimit || options.repo != "github.com/owner/repo" || options.status != "" || options.workflow != "" || options.branch != "" || options.event != "" || options.user != "" {
			t.Fatalf("unexpected run options: %#v", options)
		}
		if want := statusNowFunc(); !now.Equal(want) {
			t.Fatalf("run fetch time = %s, want %s", now, want)
		}
		entries := successfulWorkflowRuns(statusWorkflowRunLimit)
		rendered := make([]displayWorkflowRun, statusWorkflowRunLimit)
		for i := range rendered {
			rendered[i] = displayWorkflowRun{ID: strconv.Itoa(i + 1), Title: "Run"}
		}
		return workflowRunListResult{Entries: entries, Rendered: rendered}, nil
	}

	branchOutput := strings.Join([]string{
		"refs/heads/feature/status\tfeature/status\torigin/main\t\t",
		"refs/heads/main\tmain\torigin/main\t\t",
		"refs/heads/old\told\torigin/old\t[gone]\t",
		"refs/remotes/origin/HEAD\torigin\t\t\trefs/remotes/origin/main",
		"refs/remotes/origin/main\torigin/main\t\t\t",
	}, "\n") + "\n"
	worktreeOutput := strings.Join([]string{
		"worktree C:/repo", "HEAD a", "branch refs/heads/main", "",
		"worktree C:/repo.worktrees/issue-7 ", "HEAD b", "branch refs/heads/feature/status", "",
		"worktree C:/repo.worktrees/old", "HEAD c", "branch refs/heads/old", "",
	}, "\x00")
	statusCommandFunc = func(name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		switch key {
		case "git status --porcelain=v2 --branch":
			return "# branch.head feature/status\n# branch.upstream origin/main\n# branch.ab +0 -0\n", nil
		case "git for-each-ref --format=" + statusBranchFormat + " refs/heads refs/remotes":
			return branchOutput, nil
		case "git worktree list --porcelain -z":
			return worktreeOutput, nil
		case "git stash list --format=%H":
			return strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 40) + "\n", nil
		case "git rev-parse --show-toplevel":
			return "C:/repo.worktrees/issue-7 \r\n", nil
		case "git -C C:/repo status --porcelain=v2 --branch":
			return "# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -0\n", nil
		case "git for-each-ref --merged=refs/heads/main --format=%(refname) refs/heads":
			return "refs/heads/main\nrefs/heads/feature/status\nrefs/heads/old\n", nil
		case "git -C C:/repo.worktrees/old status --porcelain":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", key)
		}
	}

	got, err := fetchStatusDashboard(true, statusOptions{mergedLimit: statusMergedDefaultLimit})
	if err != nil {
		t.Fatal(err)
	}
	if got.Repository != "owner/repo" || got.RepositoryURL != "https://github.com/owner/repo" || got.DefaultBranch != "main" || !got.DefaultCheckedOut {
		t.Fatalf("unexpected dashboard identity: %#v", got)
	}
	if got.Branches.LocalCount != 3 || got.Branches.RemoteCount != 1 || got.Branches.DanglingCount != 1 {
		t.Fatalf("unexpected branch inventory: %#v", got.Branches)
	}
	if got.Stashes != 2 || got.StashesErr != nil {
		t.Fatalf("stash inventory = %d, err=%v", got.Stashes, got.StashesErr)
	}
	if got.CurrentStatus.Branch != "feature/status" || len(got.Issues) != 1 || len(got.PullRequests) != 1 || len(got.WorkflowRuns) != statusWorkflowRunLimit {
		t.Fatalf("unexpected dashboard data: %#v", got)
	}
	if !got.ShowMergedPullRequests || len(got.MergedPullRequests) != 2 || got.MergedPullRequests[0].Number != 138 || got.MergedPullRequests[1].Number != 137 {
		t.Fatalf("merged pull requests are not newest-first: %#v", got.MergedPullRequests)
	}
	if !got.WorkflowRunsPerfect {
		t.Fatal("five successful runs should produce a clean streak")
	}
	if statusCleanupCandidateCount(got.Worktrees) != 1 || !got.Worktrees[2].CleanupCandidate {
		t.Fatalf("expected only old worktree to be a cleanup candidate: %#v", got.Worktrees)
	}
	if got.Worktrees[1].CleanupCandidate {
		t.Fatal("current feature worktree must never be a cleanup candidate")
	}
}

func TestFetchStatusDashboardSkipsMergedPullRequestsWhenDisabled(t *testing.T) {
	defer saveStatusFuncs()()
	statusRepoLabelFunc = func(string) string { return "owner/repo" }
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) { return issueListResult{}, nil }
	prCalls := 0
	statusPullRequestListFunc = func(options listOptions, _ time.Time) (pullRequestListResult, error) {
		prCalls++
		if options.state != "open" {
			t.Fatalf("unexpected pull request fetch: %#v", options)
		}
		return pullRequestListResult{}, nil
	}
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		return workflowRunListResult{}, nil
	}
	installStatusDashboardGitFixture()

	dashboard, err := fetchStatusDashboard(false, statusOptions{mergedLimit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if prCalls != 1 || dashboard.ShowMergedPullRequests {
		t.Fatalf("disabled merged section made %d pull request fetches: %#v", prCalls, dashboard)
	}
}

func TestFetchStatusDashboardTreatsLimitedPRRowsAsIncomplete(t *testing.T) {
	defer saveStatusFuncs()()
	statusRepoLabelFunc = func(string) string { return "owner/repo" }
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) { return issueListResult{}, nil }
	statusPullRequestListFunc = func(listOptions, time.Time) (pullRequestListResult, error) {
		return pullRequestListResult{Entries: make([]pullRequest, statusListLimit)}, nil
	}
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		return workflowRunListResult{}, nil
	}
	installStatusDashboardGitFixture()

	dashboard, err := fetchStatusDashboard(true, statusOptions{mergedLimit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if statusCleanupCandidateCount(dashboard.Worktrees) != 0 {
		t.Fatal("limited PR rows must suppress cleanup candidates")
	}
}

func TestFetchStatusDashboardKeepsLocalHealthWhenGitHubFails(t *testing.T) {
	defer saveStatusFuncs()()
	statusRepoLabelFunc = func(string) string { return "owner/repo" }
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) {
		return issueListResult{}, errors.New("issues offline")
	}
	statusPullRequestListFunc = func(listOptions, time.Time) (pullRequestListResult, error) {
		return pullRequestListResult{}, errors.New("pull requests offline")
	}
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		return workflowRunListResult{}, errors.New("workflow runs offline")
	}
	installStatusDashboardGitFixture()
	statusRepoURLFunc = func(string) (string, error) { return "", errors.New("repository URL offline") }

	dashboard, err := fetchStatusDashboard(true, statusOptions{mergedLimit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.DefaultBranch != "main" || dashboard.Branches.LocalCount != 2 || len(dashboard.Worktrees) != 2 {
		t.Fatalf("local health was not preserved: %#v", dashboard)
	}
	if dashboard.IssuesErr == nil || dashboard.PullRequestsErr == nil || dashboard.WorkflowRunsErr == nil {
		t.Fatalf("GitHub section errors were not preserved: %#v", dashboard)
	}
	if statusCleanupCandidateCount(dashboard.Worktrees) != 0 {
		t.Fatal("unavailable PR data must suppress cleanup candidates")
	}
	if dashboard.RepositoryURL != "" {
		t.Fatalf("repository URL = %q, want empty fallback", dashboard.RepositoryURL)
	}
}

func installStatusDashboardGitFixture() {
	statusPathExistsFunc = func(string) bool { return true }
	statusRepoURLFunc = func(string) (string, error) { return "https://github.com/owner/repo", nil }
	statusCommandFunc = func(name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		switch key {
		case "git status --porcelain=v2 --branch":
			return "# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -0\n", nil
		case "git for-each-ref --format=" + statusBranchFormat + " refs/heads refs/remotes":
			return "refs/heads/main\tmain\torigin/main\t\t\nrefs/heads/old\told\torigin/old\t\t\nrefs/remotes/origin/HEAD\torigin\t\t\trefs/remotes/origin/main\n", nil
		case "git worktree list --porcelain -z":
			return strings.Join([]string{"worktree C:/repo", "HEAD a", "branch refs/heads/main", "", "worktree C:/old", "HEAD b", "branch refs/heads/old", ""}, "\x00"), nil
		case "git stash list --format=%H":
			return "", nil
		case "git rev-parse --show-toplevel":
			return "C:/repo\n", nil
		case "git -C C:/repo status --porcelain=v2 --branch":
			return "# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -0\n", nil
		case "git for-each-ref --merged=refs/heads/main --format=%(refname) refs/heads":
			return "refs/heads/main\nrefs/heads/old\n", nil
		case "git -C C:/old status --porcelain":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", key)
		}
	}
}

func TestFetchStatusDashboardSkipsRepositoryURLWithoutColor(t *testing.T) {
	defer saveStatusFuncs()()
	statusRepoLabelFunc = func(string) string { return "owner/repo" }
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) { return issueListResult{}, nil }
	statusPullRequestListFunc = func(listOptions, time.Time) (pullRequestListResult, error) {
		return pullRequestListResult{}, nil
	}
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		return workflowRunListResult{}, nil
	}
	installStatusDashboardGitFixture()
	statusRepoURLFunc = func(string) (string, error) {
		t.Fatal("plain output must not resolve a repository URL")
		return "", nil
	}

	dashboard, err := fetchStatusDashboard(false, statusOptions{mergedLimit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Repository != "owner/repo" || dashboard.RepositoryURL != "" {
		t.Fatalf("unexpected repository identity: %#v", dashboard)
	}
}

func TestRenderStatusNoColor(t *testing.T) {
	dashboard := sampleStatusDashboard()
	var buf bytes.Buffer
	if err := renderStatus(&buf, dashboard, false); err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	for _, want := range []string{
		"Repository", "owner/repo", "Main", "synced with origin/main", "Current", "feature/status",
		"3 local (1 dangling) · 2 remote", "3 total · 1 cleanup candidate",
		"old — C:/repo.worktrees/old", "Open issues (1)", "#7", "Open pull requests (1)", "#2",
		"Recently merged pull requests (1)", "#138", "Recent workflow runs (1)", "Status run", "808",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\x1b") || strings.Contains(output, "copied") {
		t.Fatalf("expected plain output without clipboard side effects:\n%s", output)
	}
}

func TestRenderStatusShowsOpenPullRequestApprovalDetails(t *testing.T) {
	dashboard := sampleStatusDashboard()
	dashboard.PullRequests[0].Approvals = 1
	dashboard.PullRequests[0].Approvers = []displayApprover{{
		Login:      "reviewer",
		ApprovedAt: time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC),
		Age:        "1h",
	}}

	savedLocal := time.Local
	t.Cleanup(func() { time.Local = savedLocal })
	time.Local = time.UTC
	var buf bytes.Buffer
	if err := renderStatus(&buf, dashboard, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "✓ @reviewer approved 2026-09-22 02:00 PM UTC (1h ago)") {
		t.Fatalf("status output missing approval detail:\n%s", buf.String())
	}
}

func TestRenderStatusLocalTime(t *testing.T) {
	defer saveStatusFuncs()()
	savedLocal := time.Local
	t.Cleanup(func() { time.Local = savedLocal })
	tests := []struct {
		name, zone, instant, want string
	}{
		{"daylight time", "America/New_York", "2026-09-05T16:32:59Z", "2026-09-05 12:32 PM EDT"},
		{"standard time", "America/New_York", "2026-01-05T17:32:59Z", "2026-01-05 12:32 PM EST"},
		{"noon", "America/New_York", "2026-09-05T16:00:00Z", "2026-09-05 12:00 PM EDT"},
		{"midnight", "America/New_York", "2026-09-05T04:00:00Z", "2026-09-05 12:00 AM EDT"},
		{"previous local date", "America/Los_Angeles", "2026-01-05T02:07:00Z", "2026-01-04 06:07 PM PST"},
		{"next local date", "Asia/Kolkata", "2026-09-05T20:35:00Z", "2026-09-06 02:05 AM IST"},
		{"UTC", "UTC", "2026-09-05T09:03:00Z", "2026-09-05 09:03 AM UTC"},
		{"before spring transition", "America/New_York", "2026-03-08T06:59:00Z", "2026-03-08 01:59 AM EST"},
		{"after spring transition", "America/New_York", "2026-03-08T07:00:00Z", "2026-03-08 03:00 AM EDT"},
		{"before fall transition", "America/New_York", "2026-11-01T05:59:00Z", "2026-11-01 01:59 AM EDT"},
		{"after fall transition", "America/New_York", "2026-11-01T06:00:00Z", "2026-11-01 01:00 AM EST"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			location, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			time.Local = location
			now, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			statusNowFunc = func() time.Time { return now }
			for _, color := range []bool{false, true} {
				var buf bytes.Buffer
				dashboard := sampleStatusDashboard()
				dashboard.IssuesErr = errors.New("offline")
				dashboard.PullRequestsErr = errors.New("offline")
				dashboard.WorkflowRunsErr = errors.New("offline")
				if err := renderStatus(&buf, dashboard, color); err != nil {
					t.Fatal(err)
				}
				output := buf.String()
				for _, want := range []string{"Local time", tc.want, "Repository", "Main", "Current", "Branches", "Worktrees"} {
					if !strings.Contains(output, want) {
						t.Fatalf("color=%t: missing %q:\n%s", color, want, output)
					}
				}
				if strings.Contains(output, "\x1b") != color {
					t.Fatalf("color=%t: unexpected escape sequence presence:\n%q", color, output)
				}
				if strings.Count(output, "Unavailable: offline") != 3 {
					t.Fatalf("color=%t: missing unavailable sections:\n%s", color, output)
				}
			}
		})
	}
}

func TestRenderStatusLocalTimeAdvances(t *testing.T) {
	defer saveStatusFuncs()()
	savedLocal := time.Local
	t.Cleanup(func() { time.Local = savedLocal })
	time.Local = time.UTC
	now := time.Date(2026, 9, 5, 23, 59, 59, 0, time.UTC)
	statusNowFunc = func() time.Time { return now }
	for _, want := range []string{"2026-09-05 11:59 PM UTC", "2026-09-06 12:00 AM UTC"} {
		var buf bytes.Buffer
		if err := renderStatus(&buf, sampleStatusDashboard(), false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing refreshed timestamp %q:\n%s", want, buf.String())
		}
		now = now.Add(time.Minute)
	}
}

func TestRenderStatusColorHasLinksButNoClipboard(t *testing.T) {
	dashboard := sampleStatusDashboard()
	var buf bytes.Buffer
	if err := renderStatus(&buf, dashboard, true); err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	if !strings.Contains(output, "\x1b]8;;https://github.com/owner/repo\x1b\\") {
		t.Fatal("expected clickable repository link")
	}
	if !strings.Contains(output, "\x1b]8;;https://github.com/owner/repo/issues/7") {
		t.Fatal("expected clickable issue link")
	}
	if !strings.Contains(output, "\x1b]8;;https://github.com/owner/repo/pull/138") {
		t.Fatal("expected clickable merged pull request link")
	}
	if !strings.Contains(output, "\x1b]8;;https://github.com/owner/repo/actions/runs/808") {
		t.Fatal("expected clickable workflow run link")
	}
	if strings.Contains(output, "\x1b]52;") || strings.Contains(output, "copied") {
		t.Fatal("status must not copy embedded table commands to the clipboard")
	}
}

func TestStatusRepositoryCellLinksOnlyAvailableURLs(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		url        string
		wantText   string
		wantLink   bool
	}{
		{name: "linked repository", repository: "owner/repo", url: "https://github.com/owner/repo", wantText: "owner/repo", wantLink: true},
		{name: "missing URL", repository: "owner/repo", wantText: "owner/repo"},
		{name: "unavailable repository", url: "https://github.com/owner/repo", wantText: "unavailable"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cell := statusRepositoryCell(newTableStyler(&bytes.Buffer{}, true), test.repository, test.url)
			if cell.text != test.wantText {
				t.Fatalf("cell text = %q, want %q", cell.text, test.wantText)
			}
			if got := strings.Contains(cell.styled, "\x1b]8;;"); got != test.wantLink {
				t.Fatalf("cell link present = %t, want %t", got, test.wantLink)
			}
		})
	}
}

func TestRenderStatusEmptyAndUnavailableSections(t *testing.T) {
	useBacklogPraiseIndex(t, 0)
	t.Run("empty", func(t *testing.T) {
		var buf bytes.Buffer
		if err := renderStatus(&buf, statusDashboard{}, false); err != nil {
			t.Fatal(err)
		}
		output := buf.String()
		if strings.Count(output, backlogPraises[0]) != 2 {
			t.Fatalf("missing empty states:\n%s", output)
		}
		if !strings.Contains(output, "Recent workflow runs (0)") || !strings.Contains(output, "No workflow runs found.") {
			t.Fatalf("missing empty workflow state:\n%s", output)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		var buf bytes.Buffer
		dashboard := statusDashboard{IssuesErr: errors.New("offline\nextra"), PullRequestsErr: errors.New("unauthorized"), WorkflowRunsErr: errors.New("actions offline")}
		if err := renderStatus(&buf, dashboard, false); err != nil {
			t.Fatal(err)
		}
		output := buf.String()
		if !strings.Contains(output, "Unavailable: offline extra") || !strings.Contains(output, "Unavailable: unauthorized") || !strings.Contains(output, "Unavailable: actions offline") {
			t.Fatalf("missing unavailable states:\n%s", output)
		}
	})

	t.Run("merged empty", func(t *testing.T) {
		var buf bytes.Buffer
		dashboard := statusDashboard{ShowMergedPullRequests: true}
		if err := renderStatus(&buf, dashboard, false); err != nil {
			t.Fatal(err)
		}
		output := buf.String()
		if !strings.Contains(output, "Recently merged pull requests (0)") || !strings.Contains(output, "No merged pull requests found.") {
			t.Fatalf("missing merged empty state:\n%s", output)
		}
	})

	t.Run("merged unavailable preserves other sections", func(t *testing.T) {
		var buf bytes.Buffer
		dashboard := sampleStatusDashboard()
		dashboard.MergedPullRequests = nil
		dashboard.MergedPullRequestsErr = errors.New("merged pull requests offline")
		if err := renderStatus(&buf, dashboard, false); err != nil {
			t.Fatal(err)
		}
		output := buf.String()
		for _, want := range []string{"#7", "#2", "Recently merged pull requests", "Unavailable: merged pull requests offline", "Recent workflow runs"} {
			if !strings.Contains(output, want) {
				t.Fatalf("merged pull request failure suppressed %q:\n%s", want, output)
			}
		}
	})

	t.Run("workflow unavailable preserves issues and pull requests", func(t *testing.T) {
		var buf bytes.Buffer
		dashboard := sampleStatusDashboard()
		dashboard.WorkflowRuns = nil
		dashboard.WorkflowRunsErr = errors.New("actions offline")
		if err := renderStatus(&buf, dashboard, false); err != nil {
			t.Fatal(err)
		}
		output := buf.String()
		for _, want := range []string{"#7", "#2", "Recent workflow runs", "Unavailable: actions offline"} {
			if !strings.Contains(output, want) {
				t.Fatalf("workflow failure suppressed %q:\n%s", want, output)
			}
		}
	})
}

func TestPerfectWorkflowRunStreak(t *testing.T) {
	failure := successfulWorkflowRuns(statusWorkflowRunLimit)
	failure[2].Conclusion = "failure"
	inProgress := successfulWorkflowRuns(statusWorkflowRunLimit)
	inProgress[4].Status = "in_progress"
	inProgress[4].Conclusion = ""
	tests := []struct {
		name string
		runs []workflowRun
		want bool
	}{
		{name: "five successes", runs: successfulWorkflowRuns(statusWorkflowRunLimit), want: true},
		{name: "four successes", runs: successfulWorkflowRuns(statusWorkflowRunLimit - 1)},
		{name: "one failure", runs: failure},
		{name: "one in progress", runs: inProgress},
		{name: "no runs"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPerfectWorkflowRunStreak(tc.runs); got != tc.want {
				t.Fatalf("isPerfectWorkflowRunStreak() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenderStatusWorkflowRunSection(t *testing.T) {
	useWorkflowPerfectionPraiseIndex(t, 1)
	dashboard := statusDashboard{
		WorkflowRuns:        []displayWorkflowRun{{Status: "✓", Title: "Perfect build", Branch: "main", Event: "push", ID: "123", URL: "https://example.com/123", Elapsed: "10s", Age: "1m"}},
		WorkflowRunsPerfect: true,
	}
	var buf bytes.Buffer
	if err := renderStatusWorkflowRunSection(&buf, newTableStyler(&buf, false), dashboard); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 7 || lines[0] != "Recent workflow runs (1)" {
		t.Fatalf("unexpected workflow section:\n%s", buf.String())
	}
	if strings.Trim(lines[1], "─") != "" || strings.Trim(lines[3], "─") != "" {
		t.Fatalf("missing bordered workflow header:\n%s", buf.String())
	}
	if !strings.Contains(lines[2], "Title") || !strings.Contains(lines[2], "Workflow") || !strings.Contains(buf.String(), workflowPerfectionPraises[1]) {
		t.Fatalf("missing headers or perfection praise:\n%s", buf.String())
	}
}

func TestParseStatusArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantMerged  int
		wantRefresh bool
	}{
		{name: "default", wantMerged: statusMergedDefaultLimit},
		{name: "override", args: []string{"--merged=2"}, wantMerged: 2},
		{name: "disabled", args: []string{"--merged=0"}, wantMerged: 0},
		{name: "refresh", args: []string{"--refresh"}, wantMerged: statusMergedDefaultLimit, wantRefresh: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseStatusArgs(tc.args, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if got.mergedLimit != tc.wantMerged || got.refresh != tc.wantRefresh {
				t.Fatalf("status options = %#v, want merged=%d refresh=%v", got, tc.wantMerged, tc.wantRefresh)
			}
		})
	}
}

func TestRunStatusRejectsInvalidMergedLimitsBeforeFetching(t *testing.T) {
	defer saveStatusFuncs()()
	fetchStatusDashboardFunc = func(bool, statusOptions) (statusDashboard, error) {
		t.Fatal("invalid arguments must not fetch dashboard data")
		return statusDashboard{}, nil
	}

	for _, args := range [][]string{{"--merged=-1"}, {"--merged=abc"}} {
		var stdout, stderr bytes.Buffer
		if err := runStatus(args, &stdout, &stderr); err == nil {
			t.Fatalf("runStatus(%v) succeeded, want argument error", args)
		}
	}
}

func TestRunStatusAliasesUseFetcher(t *testing.T) {
	useBacklogPraiseIndex(t, 0)
	defer saveStatusFuncs()()
	statusNowFunc = func() time.Time { return time.Date(2026, 9, 5, 16, 32, 0, 0, time.UTC) }
	fetchStatusDashboardFunc = func(_ bool, options statusOptions) (statusDashboard, error) {
		if options.mergedLimit != statusMergedDefaultLimit || options.refresh {
			t.Fatalf("status options = %#v, want default options", options)
		}
		return statusDashboard{Repository: "owner/repo"}, nil
	}

	tests := []struct {
		name    string
		command string
	}{
		{name: "full command", command: "status"},
		{name: "short alias", command: "s"},
	}

	outputs := make(map[string]string, len(tests))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			_, err := run([]string{tc.command}, &stdout, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), "owner/repo") || !strings.Contains(stdout.String(), backlogPraises[0]) {
				t.Fatalf("unexpected status output: %q", stdout.String())
			}
			outputs[tc.command] = stdout.String() + stderr.String()
		})
	}

	if outputs["s"] != outputs["status"] {
		t.Fatalf("status alias output differs\nstatus: %q\nalias:  %q", outputs["status"], outputs["s"])
	}
}

func TestRootUsageMentionsStatusAlias(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "command listing", want: "status     Show repository health, issues, pull requests, and runs (alias: s)"},
		{name: "example", want: "gh x s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(rootUsage, tc.want) {
				t.Fatalf("root usage should contain %q", tc.want)
			}
		})
	}
}

func TestRunStatusHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, err := run([]string{"status", "--help"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gh x status", "branches", "worktrees", "open issues", "open pull requests", "recently merged pull requests", "workflow runs", "--merged int", "0 hides the section", "default 5", "--refresh", "Bypass cached GitHub data"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("expected %q in status usage, got %q", want, stderr.String())
		}
	}
}

func sampleStatusDashboard() statusDashboard {
	return statusDashboard{
		Repository:        "owner/repo",
		RepositoryURL:     "https://github.com/owner/repo",
		DefaultBranch:     "main",
		DefaultStatus:     statusSummary{Branch: "main", Upstream: "origin/main"},
		DefaultCheckedOut: true,
		CurrentStatus:     statusSummary{Branch: "feature/status", Upstream: "origin/main", Modified: 1},
		Branches:          statusBranchInventory{LocalCount: 3, RemoteCount: 2, DanglingCount: 1},
		Worktrees: []statusWorktree{
			{Path: "C:/repo", Branch: "main", Primary: true},
			{Path: "C:/repo.worktrees/current", Branch: "feature/status", Current: true},
			{Path: "C:/repo.worktrees/old", Branch: "old", CleanupCandidate: true, CleanupReason: "clean, merged branch with no open PR"},
		},
		Issues:                 []displayIssue{{Number: 7, Title: "Status dashboard", Author: "alice", State: "open", Updated: "1m", URL: "https://github.com/owner/repo/issues/7"}},
		PullRequests:           []displayPullRequest{{Number: 2, Title: "Open PR", Author: "bob", State: "open", Review: "required", AIReview: "-", Checks: "pending", Comments: "-", Branch: "feature", Updated: "2m", URL: "https://github.com/owner/repo/pull/2"}},
		ShowMergedPullRequests: true,
		MergedPullRequests:     []displayPullRequest{{Number: 138, Title: "Merged PR", Author: "carol", State: "merged", Review: "approved", AIReview: "pass", Checks: "pass", Comments: "-", Branch: "merged-feature", Updated: "3m", URL: "https://github.com/owner/repo/pull/138"}},
		WorkflowRuns:           []displayWorkflowRun{{Status: "✓", Title: "Status run", Workflow: "CI", Branch: "main", Event: "push", ID: "808", URL: "https://github.com/owner/repo/actions/runs/808", Elapsed: "8s", Age: "1m"}},
	}
}

func saveStatusFuncs() func() {
	savedDashboard := fetchStatusDashboardFunc
	savedCommand := statusCommandFunc
	savedIssues := statusIssueListFunc
	savedPullRequests := statusPullRequestListFunc
	savedWorkflowRuns := statusWorkflowRunListFunc
	savedRepoLabel := statusRepoLabelFunc
	savedRepoURL := statusRepoURLFunc
	savedDefaultBranch := statusDefaultBranchFunc
	savedNow := statusNowFunc
	savedPathExists := statusPathExistsFunc
	savedCacheDirectory := statusCacheDirectoryFunc
	savedKeyringGet := statusKeyringGetFunc
	statusCacheDirectoryFunc = func() (string, string, error) {
		return "", "", errors.New("status cache disabled in unit test")
	}
	return func() {
		fetchStatusDashboardFunc = savedDashboard
		statusCommandFunc = savedCommand
		statusIssueListFunc = savedIssues
		statusPullRequestListFunc = savedPullRequests
		statusWorkflowRunListFunc = savedWorkflowRuns
		statusRepoLabelFunc = savedRepoLabel
		statusRepoURLFunc = savedRepoURL
		statusDefaultBranchFunc = savedDefaultBranch
		statusNowFunc = savedNow
		statusPathExistsFunc = savedPathExists
		statusCacheDirectoryFunc = savedCacheDirectory
		statusKeyringGetFunc = savedKeyringGet
	}
}

func successfulWorkflowRuns(count int) []workflowRun {
	runs := make([]workflowRun, count)
	for i := range runs {
		runs[i] = workflowRun{Status: "completed", Conclusion: "success"}
	}
	return runs
}

func TestRenderStatusShowsSupplementalDiagnostics(t *testing.T) {
	dashboard := statusDashboard{
		Repository:    "owner/repo",
		DefaultBranch: "main",
		Branches:      statusBranchInventory{Local: map[string]statusBranchRef{}},
		Issues: []displayIssue{
			{Number: 332, Title: "Linked issue", State: "open", PullRequests: "?"},
			{Number: 300, Title: "Healthy issue", State: "open", PullRequests: "#333"},
		},
		IssuesRelErr:       fmt.Errorf("gh api graphql: Could not resolve to a Issue with the number of 998."),
		IssuesHierarchyErr: errors.New("hierarchy fields unavailable"),
		PullRequests: []displayPullRequest{
			{Number: 333, Title: "Open PR", State: "open", AIReview: "?", Comments: "?", Checks: "pending", Branch: "feature", Updated: "2m"},
		},
		PullRequestsSuppErr: errors.New("gh api graphql: You have exceeded a secondary rate limit."),
	}

	var buf bytes.Buffer
	if err := renderStatus(&buf, dashboard, false); err != nil {
		t.Fatalf("renderStatus error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "Supplemental data unavailable: gh api graphql: You have exceeded a secondary rate limit.") {
		t.Fatalf("pull-request section must explain unavailable supplemental data:\n%s", output)
	}
	if !strings.Contains(output, "Pull request relationships unavailable: gh api graphql: Could not resolve to a Issue with the number of 998.") {
		t.Fatalf("issue section must explain unavailable relationships:\n%s", output)
	}
	if !strings.Contains(output, "Issue hierarchy unavailable: hierarchy fields unavailable") {
		t.Fatalf("issue section must explain unavailable hierarchy data:\n%s", output)
	}
	// The healthy issue relationship still renders beside the diagnostic.
	if !strings.Contains(output, "#300") {
		t.Fatalf("healthy issue row must keep its relationship:\n%s", output)
	}
}

func TestFetchStatusDashboardKeepsSupplementalDiagnostics(t *testing.T) {
	defer saveStatusFuncs()()
	statusRepoLabelFunc = func(string) string { return "owner/repo" }
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) {
		return issueListResult{
			Display:      []displayIssue{{Number: 7, State: "open", PullRequests: "?", Parent: "?", SubIssues: "?"}},
			RelErr:       errors.New("relationships offline"),
			HierarchyErr: errors.New("hierarchy offline"),
		}, nil
	}
	statusPullRequestListFunc = func(listOptions, time.Time) (pullRequestListResult, error) {
		return pullRequestListResult{
			Entries:            []pullRequest{{HeadRefName: "feature"}},
			Rendered:           []displayPullRequest{{Number: 2, Title: "Open PR", State: "open", AIReview: "?", Comments: "?"}},
			SupplementalFailed: true,
			SupplementalErr:    errors.New("supplemental offline"),
		}, nil
	}
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		return workflowRunListResult{}, nil
	}
	installStatusDashboardGitFixture()

	dashboard, err := fetchStatusDashboard(true, statusOptions{mergedLimit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.IssuesRelErr == nil || dashboard.IssuesRelErr.Error() != "relationships offline" {
		t.Fatalf("IssuesRelErr = %v, want relationships offline", dashboard.IssuesRelErr)
	}
	if dashboard.IssuesHierarchyErr == nil || dashboard.IssuesHierarchyErr.Error() != "hierarchy offline" {
		t.Fatalf("IssuesHierarchyErr = %v, want hierarchy offline", dashboard.IssuesHierarchyErr)
	}
	if dashboard.PullRequestsSuppErr == nil || dashboard.PullRequestsSuppErr.Error() != "supplemental offline" {
		t.Fatalf("PullRequestsSuppErr = %v, want supplemental offline", dashboard.PullRequestsSuppErr)
	}
}

func TestRenderStatusPullRequestNoticeWithNoRows(t *testing.T) {
	dashboard := statusDashboard{
		Repository:          "owner/repo",
		DefaultBranch:       "main",
		Branches:            statusBranchInventory{Local: map[string]statusBranchRef{}},
		PullRequestsSuppErr: errors.New("gh api graphql: You have exceeded a secondary rate limit."),
	}
	var buf bytes.Buffer
	if err := renderStatus(&buf, dashboard, false); err != nil {
		t.Fatalf("renderStatus error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "Supplemental data unavailable: gh api graphql: You have exceeded a secondary rate limit.") {
		t.Fatalf("empty PR list must still print the supplemental diagnostic:\n%s", output)
	}
	if !strings.Contains(output, "Open pull requests (0)") {
		t.Fatalf("expected the zero-count section header:\n%s", output)
	}
}

func TestRenderStatusShowsRequiredChecksNotice(t *testing.T) {
	dashboard := statusDashboard{
		Repository:        "owner/repo",
		DefaultBranch:     "main",
		Branches:          statusBranchInventory{Local: map[string]statusBranchRef{}},
		PullRequests:      []displayPullRequest{{Number: 2, Title: "PR", State: "open", Checks: "pending"}},
		RequiredChecksErr: errors.New("base main: rules offline"),
	}
	var buf bytes.Buffer
	if err := renderStatus(&buf, dashboard, false); err != nil {
		t.Fatalf("renderStatus error: %v", err)
	}
	if !strings.Contains(buf.String(), "Required check rules unavailable: base main: rules offline") {
		t.Fatalf("status must explain the required-checks downgrade:\n%s", buf.String())
	}
}
