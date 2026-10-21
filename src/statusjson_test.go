package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStatusJSONHealthVerdicts(t *testing.T) {
	tests := []struct {
		name string
		edit func(*statusDashboard)
		want [5]bool
	}{
		{"clean", func(*statusDashboard) {}, [5]bool{true, true, true, true, true}},
		{"dirty default", func(d *statusDashboard) { d.DefaultStatus.Modified = 2 }, [5]bool{false, true, true, true, true}},
		{"dirty current", func(d *statusDashboard) { d.CurrentStatus.Untracked = 1 }, [5]bool{true, false, true, true, true}},
		{"ahead", func(d *statusDashboard) { d.DefaultStatus.Ahead = 1 }, [5]bool{false, true, true, true, true}},
		{"behind", func(d *statusDashboard) { d.DefaultStatus.Behind = 1 }, [5]bool{false, true, true, true, true}},
		{"no upstream", func(d *statusDashboard) { d.DefaultStatus.Upstream = "" }, [5]bool{false, true, true, true, true}},
		{"gone", func(d *statusDashboard) { d.Branches.Local = map[string]statusBranchRef{"main": {Track: "[gone]"}} }, [5]bool{false, true, true, true, true}},
		{"not checked out", func(d *statusDashboard) { d.DefaultCheckedOut = false }, [5]bool{false, true, true, true, true}},
		{"default unavailable", func(d *statusDashboard) { d.DefaultStatusErr = errors.New("default unavailable") }, [5]bool{false, true, true, true, true}},
		{"branches", func(d *statusDashboard) { d.Branches.LocalCount = 2 }, [5]bool{true, true, false, true, true}},
		{"remote branches", func(d *statusDashboard) { d.Branches.RemoteCount = 2 }, [5]bool{true, true, false, true, true}},
		{"dangling branches", func(d *statusDashboard) { d.Branches.DanglingCount = 1 }, [5]bool{true, true, false, true, true}},
		{"worktrees", func(d *statusDashboard) { d.Worktrees = append(d.Worktrees, statusWorktree{}) }, [5]bool{true, true, true, false, true}},
		{"cleanup", func(d *statusDashboard) { d.Worktrees[0].CleanupCandidate = true }, [5]bool{true, true, true, false, true}},
		{"stashes", func(d *statusDashboard) { d.Stashes = 1 }, [5]bool{true, true, true, true, false}},
		{"stashes unavailable", func(d *statusDashboard) { d.StashesErr = errors.New("stash unavailable") }, [5]bool{true, true, true, true, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := statusDashboard{DefaultBranch: "main", DefaultCheckedOut: true,
				DefaultStatus: statusSummary{Branch: "main", Upstream: "origin/main"},
				CurrentStatus: statusSummary{Branch: "feature", Upstream: "origin/feature"},
				Branches:      statusBranchInventory{LocalCount: 1, RemoteCount: 1}, Worktrees: []statusWorktree{{}}}
			tt.edit(&d)
			health := buildStatusJSONHealth(d)
			got := [5]bool{health.DefaultBranch.Clean, health.CurrentBranch.Clean, health.Branches.Clean, health.Worktrees.Clean, health.Stashes.Clean}
			if got != tt.want {
				t.Fatalf("verdicts = %v, want %v", got, tt.want)
			}
			if (d.DefaultStatusErr != nil) != (health.DefaultBranch.Status == "unavailable") || (d.StashesErr != nil) != (health.Stashes.Status == "unavailable") {
				t.Fatalf("missing unavailable health status: %+v", health)
			}
			if tt.name == "gone" && health.DefaultBranch.Synced {
				t.Fatal("gone upstream must not be synced")
			}
		})
	}
}

func TestStatusJSONValues(t *testing.T) {
	now := time.Date(2026, 10, 21, 2, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
	d := sampleStatusDashboard()
	d.localRoot = "/repo"
	d.CurrentStatus = statusSummary{Branch: "feature", Upstream: "origin/feature", Ahead: 2, Behind: 3, Staged: 1, Modified: 2, Deleted: 3, Renamed: 4, Untracked: 5, Conflicted: 6}
	d.Worktrees[0] = statusWorktree{Path: "/repo", Head: "abc", Branch: "main", Detached: true, DetachedMerged: true, Locked: true, Prunable: true, PrunableReason: "missing", Primary: true, Current: true, Exists: true, Clean: true, CleanKnown: true, CleanupCandidate: true, CleanupReason: "merged"}
	d.Issues[0] = displayIssue{Number: 7, PullRequests: "#2", Parent: "#1", SubIssues: "1/2", Title: "issue", Author: "alice", State: "open", Labels: "go", Assignees: "bob", Updated: "1m", URL: "issue-url", updatedAt: now}
	clean := false
	d.PullRequests[0] = displayPullRequest{Number: 2, Issues: "#7", Title: "pr", Author: "bob", State: "open", Review: "required", Approvals: 1, Approvers: []displayApprover{{Login: "alice", ApprovedAt: now, Age: "1m"}}, ApprovalsIncomplete: true, Checks: "pending", Comments: "2/1", AIReview: "fail", AIClean: &clean, Branch: "feature", Updated: "2m", URL: "pr-url", updatedAt: now}
	d.MergedPullRequests[0].mergedAt = now
	d.WorkflowRuns[0] = displayWorkflowRun{ID: "808", URL: "run-url", Status: "*", rawStatus: "in_progress", Title: "CI title", Workflow: "CI", Branch: "main", Event: "push", Elapsed: "8s", Age: "1m", createdAt: now, startedAt: now, updatedAt: now}
	d.WorkflowRunsPerfect = true
	got := buildStatusJSON(d, statusOptions{mergedLimit: 8}, now)
	if got.SchemaVersion != 1 || !got.GeneratedAt.Equal(now) || got.Repository != (statusJSONRepository{"owner/repo", "/repo", "main"}) {
		t.Fatalf("identity/version/time = %+v", got)
	}
	if got.Health.CurrentBranch.Changes != (statusJSONChanges{false, 1, 2, 3, 4, 5, 6}) || got.Health.CurrentBranch.Ahead != 2 || got.Health.CurrentBranch.Behind != 3 {
		t.Fatalf("changes lost: %+v", got.Health.CurrentBranch)
	}
	if got.Health.Worktrees.Items[0] != (statusJSONWorktree{"/repo", "abc", "main", true, true, true, true, "missing", true, true, true, true, true, true, "merged"}) {
		t.Fatalf("worktree fields lost: %+v", got.Health.Worktrees.Items[0])
	}
	wantIssue := statusJSONIssue{7, "#2", "#1", "1/2", "issue", "alice", "open", "go", "bob", "1m", statusJSONTime(now), "issue-url"}
	if !reflect.DeepEqual(got.Issues[0], wantIssue) {
		t.Fatalf("issue fields lost: %+v", got.Issues[0])
	}

	pr := got.PullRequests.Open[0]
	wantPR := statusJSONPR{2, "#7", "pr", "bob", "open", "required", 1, []statusJSONApprover{{"alice", statusJSONTime(now), "1m"}}, true, "pending", "2/1", "fail", &clean, "feature", "2m", statusJSONTime(now), nil, "pr-url"}
	if !reflect.DeepEqual(pr, wantPR) || got.PullRequests.MergedLimit != 8 || !got.PullRequests.Merged[0].MergedAt.Equal(now) {
		t.Fatalf("PR fields lost: %+v", got.PullRequests)
	}
	wantRun := statusJSONRun{"808", "run-url", "*", "in_progress", "CI title", "CI", "main", "push", "8s", "1m", statusJSONTime(now), statusJSONTime(now), statusJSONTime(now)}
	if !reflect.DeepEqual(got.WorkflowRuns[0], wantRun) || !got.WorkflowRunsPerfect {
		t.Fatalf("run fields lost: %+v", got.WorkflowRuns)
	}
	if statusJSONTime(time.Time{}) != nil || statusJSONTime(now).Location() != time.UTC {
		t.Fatal("optional timestamps must be null or UTC")
	}
}

func TestStatusJSONSections(t *testing.T) {
	now := time.Date(2026, 10, 21, 6, 30, 0, 0, time.UTC)
	d := sampleStatusDashboard()
	d.IssuesRelErr = errors.New("relationships unavailable")
	d.PullRequestsErr = errors.New("PR unavailable")
	d.MergedRequiredChecksErr = errors.New("rules unavailable")
	d.WorkflowRunsErr = errors.New("runs unavailable")
	d.remoteSections = []statusCacheSection{{FetchedAt: now.Add(-20 * time.Second)}, {FetchedAt: now}, {FetchedAt: now.Add(time.Second)}, {}}
	d.remoteCached = [4]bool{true, false, true, true}
	got := buildStatusJSONSections(d, now)
	for i, name := range []string{"issues", "pullRequests.open", "pullRequests.merged", "workflowRuns"} {
		if got[i].Name != name || got[i].Status != "unavailable" || got[i].Reason == "" {
			t.Fatalf("missing section failure: %+v", got[i])
		}
	}
	if !got[0].Cached || got[0].CacheAgeSeconds != 20 || got[1].Cached || got[1].CacheAgeSeconds != 0 || got[2].CacheAgeSeconds != 0 || got[3].FetchedAt != nil {
		t.Fatalf("cache metadata: %+v", got)
	}
	d.ShowMergedPullRequests = false
	if section := buildStatusJSONSections(d, now)[2]; section.Status != "disabled" || section.Cached || section.FetchedAt != nil || section.CacheAgeSeconds != 0 {
		t.Fatalf("disabled metadata: %+v", section)
	}
}

func TestStatusJSONMixedCacheProvenance(t *testing.T) {
	defer saveStatusFuncs()()
	now := time.Date(2026, 10, 21, 6, 30, 0, 0, time.UTC)
	statusNowFunc = func() time.Time { return now }
	dashboard := sampleStatusDashboard()
	sections := make([]statusCacheSection, 4)
	for i := range sections {
		sections[i] = statusCacheSection{FetchedAt: now.Add(-10 * time.Second), RetryAfter: now.Add(time.Second)}
	}
	sections[3].RetryAfter = now
	entry := statusCacheEntry{Repository: "owner/repo", FetchedAt: now.Add(-10 * time.Second), Sections: sections,
		Issues: cacheStatusIssues(dashboard.Issues), PullRequests: cacheStatusPullRequests(dashboard.PullRequests),
		MergedPullRequests: cacheStatusPullRequests(dashboard.MergedPullRequests), ShowMergedPullRequests: true,
		WorkflowRuns: cacheStatusWorkflowRuns(dashboard.WorkflowRuns)}
	applyStatusCache(&dashboard, entry, now)
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) {
		t.Fatal("fresh issue section fetched again")
		return issueListResult{}, nil
	}
	statusPullRequestListFunc = func(listOptions, time.Time) (pullRequestListResult, error) {
		t.Fatal("fresh PR section fetched again")
		return pullRequestListResult{}, nil
	}
	calls := 0
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		calls++
		return workflowRunListResult{}, errors.New("new workflow failure")
	}
	retryStatusSections(&dashboard, statusOptions{mergedLimit: 5}, now, nil, true)
	got := buildStatusJSONSections(dashboard, now)
	if calls != 1 || !got[0].Cached || got[0].CacheAgeSeconds != 10 || !got[1].Cached || !got[2].Cached || got[3].Cached || got[3].Status != "unavailable" || got[3].Reason != "new workflow failure" || got[3].CacheAgeSeconds != 0 || !got[3].FetchedAt.Equal(now) {
		t.Fatalf("mixed cache provenance: calls=%d %+v", calls, got)
	}
}

func TestStatusJSONEscapesDataAndArgumentFailures(t *testing.T) {
	defer saveStatusFuncs()()
	text := "quote\" and newline\n ANSI\x1b[31m"
	fetchStatusDashboardFunc = func(bool, statusOptions) (statusDashboard, error) {
		return statusDashboard{Issues: []displayIssue{{Title: text}}}, nil
	}
	var stdout, stderr bytes.Buffer
	if err := runStatus([]string{"--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var got statusJSONDocument
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Issues[0].Title != text || strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("unsafe data output: %q", stdout.String())
	}
	for _, args := range [][]string{{"--json", "--merged=-1"}, {"--json", "--unknown"}, {"--json", "extra"}} {
		stdout.Reset()
		if err := runStatus(args, &stdout, &stderr); err == nil || !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), "status_failed") {
			t.Fatalf("argument error: %v, %v, %q", args, err, stdout.String())
		}
	}
}

func TestRunStatusJSON(t *testing.T) {
	defer saveStatusFuncs()()
	savedProgress, savedTerminal, savedVersion := statusProgressFunc, terminalOutputFunc, version
	t.Cleanup(func() { statusProgressFunc, terminalOutputFunc, version = savedProgress, savedTerminal, savedVersion })
	version = "v0.21.2"
	terminalOutputFunc = func(io.Writer) bool { return true }
	statusProgressFunc = func(io.Writer, bool) *statusProgress { t.Fatal("JSON must never start progress"); return nil }
	fetchStatusDashboardFunc = func(color bool, options statusOptions) (statusDashboard, error) {
		if color || !options.json || !options.refresh || options.mergedLimit != 0 || options.progress != nil {
			t.Fatalf("JSON options: %+v, color %v", options, color)
		}
		return statusDashboard{}, nil
	}
	for _, name := range []string{"status", "s"} {
		var stdout, stderr bytes.Buffer
		ch, err := run([]string{"--clear", name, "--json", "--refresh", "--merged=0"}, &stdout, &stderr)
		if err != nil || ch != nil || stderr.Len() != 0 || strings.Contains(stdout.String(), "\x1b") {
			t.Fatalf("unexpected output: ch=%v err=%v stdout=%q stderr=%q", ch, err, stdout.String(), stderr.String())
		}
		var document map[string]json.RawMessage
		decoder := json.NewDecoder(&stdout)
		if err := decoder.Decode(&document); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			t.Fatalf("extra stdout: %v", err)
		}
		if string(document["schemaVersion"]) != "1" || string(document["issues"]) != "[]" || string(document["workflowRuns"]) != "[]" {
			t.Fatalf("contract keys/empty arrays: %s", document)
		}
	}
}

func TestRunStatusJSONErrors(t *testing.T) {
	defer saveStatusFuncs()()
	failure := errors.New("not a git repository")
	fetchStatusDashboardFunc = func(bool, statusOptions) (statusDashboard, error) { return statusDashboard{}, failure }
	var stdout, stderr bytes.Buffer
	if err := runStatus([]string{"--json"}, &stdout, &stderr); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	var document struct {
		SchemaVersion int
		Error         struct{ Code, Message string }
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 1 || document.Error.Code != "status_failed" || document.Error.Message != failure.Error() {
		t.Fatalf("error JSON = %+v", document)
	}
	if err := writeStatusJSONError(clearErrorWriter{}, failure); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("write error = %v", err)
	}
	fetchStatusDashboardFunc = func(bool, statusOptions) (statusDashboard, error) { return statusDashboard{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	savedContext := commandContext
	commandContext = ctx
	t.Cleanup(func() { commandContext = savedContext })
	stdout.Reset()
	if err := runStatus([]string{"--json"}, &stdout, &stderr); !errors.Is(err, context.Canceled) || !strings.Contains(stdout.String(), "context canceled") {
		t.Fatalf("cancellation = %v, %q", err, stdout.String())
	}
}

func TestStatusJSONV1Contract(t *testing.T) {
	now := time.Date(2026, 10, 21, 6, 30, 0, 0, time.UTC)
	dashboard := sampleStatusDashboard()
	dashboard.localRoot = "/repo"
	got, err := json.MarshalIndent(buildStatusJSON(dashboard, statusOptions{mergedLimit: 5}, now), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/status-json-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Fatalf("version 1 contract changed:\n%s", got)
	}
}

func TestStatusJSONApproverUTC(t *testing.T) {
	at := time.Date(2026, 10, 21, 2, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
	rows := buildStatusJSONPRs([]displayPullRequest{{Approvers: []displayApprover{{Login: "alice", ApprovedAt: at}, {Login: "unknown"}}}})
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Approvers []struct{ ApprovedAt *string }
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got[0].Approvers[0].ApprovedAt == nil || *got[0].Approvers[0].ApprovedAt != "2026-10-21T06:30:00Z" || got[0].Approvers[1].ApprovedAt != nil {
		t.Fatalf("approval timestamps must be UTC or null: %s", data)
	}
}

func TestStatusJSONProtectedWorktreeAcquisition(t *testing.T) {
	defer saveStatusFuncs()()
	dashboard := statusDashboard{
		DefaultBranch: "main", DefaultCheckedOut: true,
		CurrentStatus: statusSummary{Modified: 1}, DefaultStatus: statusSummary{Staged: 1},
		Branches: statusBranchInventory{Local: map[string]statusBranchRef{"main": {WorktreePath: "/default"}}},
		Worktrees: []statusWorktree{
			{Path: "/current", Current: true}, {Path: "/default", Branch: "main"},
			{Path: "/locked", Locked: true}, {Path: "/missing", Prunable: true},
			{Path: "/failed", Primary: true}, {Path: "/known", Exists: true, CleanKnown: true, Clean: true},
		},
	}
	statusPathExistsFunc = func(path string) bool {
		if path == "/known" {
			t.Fatal("known worktree should not be inspected again")
		}
		return path != "/missing"
	}
	calls := []string{}
	statusCommandFunc = func(name string, args ...string) (string, error) {
		if name != "git" || len(args) != 4 || args[0] != "-C" || args[2] != "status" || args[3] != "--porcelain" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		calls = append(calls, args[1])
		if args[1] == "/failed" {
			return "", errors.New("inaccessible")
		}
		return "", nil
	}
	populateStatusJSONWorktreeFacts(&dashboard)
	if !reflect.DeepEqual(calls, []string{"/locked", "/failed"}) {
		t.Fatalf("protected summary reuse: %v", calls)
	}
	for i, want := range [][3]bool{{true, true, false}, {true, true, false}, {true, true, true}, {false, false, false}, {true, false, false}, {true, true, true}} {
		worktree := dashboard.Worktrees[i]
		if got := [3]bool{worktree.Exists, worktree.CleanKnown, worktree.Clean}; got != want {
			t.Fatalf("facts for %s = %v, want %v", worktree.Path, got, want)
		}
		if worktree.CleanupCandidate {
			t.Fatalf("facts must not change cleanup verdict: %+v", worktree)
		}
	}
}

func TestStatusJSONRequestedScan(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{[]string{"--unknown", "--json"}, true}, {[]string{"extra", "--json"}, true},
		{[]string{"--json", "--json=false"}, false}, {[]string{"--json=false", "--json"}, true},
		{[]string{"--", "--json"}, false}, {[]string{"--merged", "--json"}, false},
	} {
		if got := statusJSONRequested(tt.args); got != tt.want {
			t.Fatalf("scan %v = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestStatusJSONGlobalErrorsNeverClear(t *testing.T) {
	forceTerminal(t, true)
	for _, args := range [][]string{
		{"status", "--json", "--clear", "--clear=wrong"},
		{"--clear=wrong", "status", "--clear", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		_, err := run(args, &stdout, &stderr)
		if err == nil || !json.Valid(stdout.Bytes()) || strings.Contains(stdout.String(), "\x1b") || !strings.Contains(stdout.String(), "status_failed") {
			t.Fatalf("global JSON error: %v, %v stdout=%q stderr=%q", args, err, stdout.String(), stderr.String())
		}
	}
}

func TestStatusGlobalErrorPreservesEarlierClearState(t *testing.T) {
	forceTerminal(t, true)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--clear=wrong", "status", "--clear"}, ""},
		{[]string{"--clear", "status", "--clear=wrong", "--clear=false"}, terminalClearSequence},
	} {
		var stdout, stderr bytes.Buffer
		_, err := run(tt.args, &stdout, &stderr)
		if err == nil || stdout.String() != tt.want {
			t.Fatalf("terminal state %v: err=%v stdout=%q want=%q", tt.args, err, stdout.String(), tt.want)
		}
	}
}
