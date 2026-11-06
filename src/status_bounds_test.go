package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseStatusBranchRefsAcceptsFiveAndSixFields(t *testing.T) {
	output := strings.Join([]string{
		"refs/heads/main\tmain\torigin/main\t\t",
		"refs/heads/feature\tfeature\t\t[gone]\t\t/work/feature",
		"refs/heads/short\tshort\t\t",
		"",
	}, "\n")
	inventory := parseStatusBranchRefs(output)
	if len(inventory.Refs) != 2 || inventory.LocalCount != 2 || inventory.DanglingCount != 1 {
		t.Fatalf("inventory = %+v", inventory)
	}
	if got := inventory.Local["main"]; got.Upstream != "origin/main" || got.WorktreePath != "" {
		t.Fatalf("five-field ref = %+v", got)
	}
	if got := inventory.Local["feature"]; got.WorktreePath != "/work/feature" {
		t.Fatalf("six-field ref = %+v", got)
	}
}

func TestResolveStatusDefaultBranchFallsBackToAnySymbolicRemoteHEAD(t *testing.T) {
	inventory := statusBranchInventory{Refs: []statusBranchRef{
		{FullName: "refs/remotes/fork/HEAD"},
		{FullName: "refs/remotes/upstream/HEAD", Symref: "refs/remotes/upstream/trunk"},
	}}
	if got := resolveStatusDefaultBranch(inventory); got != "trunk" {
		t.Fatalf("default branch = %q", got)
	}
	inventory.Refs = inventory.Refs[:1]
	if got := resolveStatusDefaultBranch(inventory); got != "" {
		t.Fatalf("a remote HEAD without a target names no branch: %q", got)
	}
}

func TestOpenPullRequestHeadsSkipsBlankHeads(t *testing.T) {
	heads := openPullRequestHeads([]pullRequest{{HeadRefName: ""}, {HeadRefName: "feature"}})
	if len(heads) != 1 || !heads["feature"] {
		t.Fatalf("heads = %v", heads)
	}
}

func TestPrunableWorktreeReasonNamesGitsReason(t *testing.T) {
	merged := map[string]bool{"done": true}
	worktree := statusWorktree{Branch: "done", Prunable: true, PrunableReason: "gitdir file points to non-existent location"}
	if got := statusWorktreeCandidateReason(worktree, "main", merged, nil, true, true); got != "Git marks worktree prunable: gitdir file points to non-existent location" {
		t.Fatalf("reason = %q", got)
	}
	worktree.PrunableReason = ""
	if got := statusWorktreeCandidateReason(worktree, "main", merged, nil, true, true); got != "Git marks worktree prunable" {
		t.Fatalf("reason without detail = %q", got)
	}
}

func TestSameStatusPathCaseRules(t *testing.T) {
	left := filepath.Join("work", "Repo")
	right := filepath.Join("work", "repo")
	if got, want := sameStatusPath(left, right), runtime.GOOS == "windows"; got != want {
		t.Fatalf("case-only difference = %v, want %v on %s", got, want, runtime.GOOS)
	}
	if !sameStatusPath(filepath.Join("work", "x", ".."), "work") {
		t.Fatal("cleaned paths compare equal")
	}
}

func TestParseGitStatusHeaderAheadBehind(t *testing.T) {
	var summary statusSummary
	parseGitStatusHeader("# branch.ab +3 -2", &summary)
	if summary.Ahead != 3 || summary.Behind != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	parseGitStatusHeader("# branch.ab +9", &summary)
	if summary.Ahead != 3 {
		t.Fatalf("a malformed line must not change counts: %+v", summary)
	}
}

func TestStatusXYAndPathFields(t *testing.T) {
	if got := statusXY("1 .M"); got != ".M" {
		t.Fatalf("xy = %q", got)
	}
	if got := statusXY("1"); got != "" {
		t.Fatalf("short xy = %q", got)
	}
	if got := statusPath("? new.txt"); got != "new.txt" {
		t.Fatalf("path = %q", got)
	}
	if got := statusPath("single"); got != "single" {
		t.Fatalf("single-field path = %q", got)
	}
}

func TestApplyStatusXYCountsEachKind(t *testing.T) {
	tests := []struct {
		xy   string
		want statusSummary
	}{
		{"A.", statusSummary{Staged: 1}},
		{".M", statusSummary{Modified: 1}},
		{"M.", statusSummary{Staged: 1, Modified: 1}},
		{"R.", statusSummary{Staged: 1, Renamed: 1}},
		{".D", statusSummary{Deleted: 1}},
		{"D.", statusSummary{Staged: 1, Deleted: 1}},
	}
	for _, tt := range tests {
		var got statusSummary
		applyStatusXY(tt.xy, &got)
		if got != tt.want {
			t.Errorf("applyStatusXY(%q) = %+v, want %+v", tt.xy, got, tt.want)
		}
	}
}

func TestHasWorkingChangesCountsConflicts(t *testing.T) {
	if !hasWorkingChanges(statusSummary{Conflicted: 1}) {
		t.Fatal("a conflict is a working change")
	}
	if hasWorkingChanges(statusSummary{}) {
		t.Fatal("a clean summary has no changes")
	}
}

func TestStatusSectionCountMarksTheLimit(t *testing.T) {
	if got := statusSectionCount(statusListLimit); got != "30+" {
		t.Fatalf("limit = %q", got)
	}
	if got := statusSectionCount(statusListLimit - 1); got != "29" {
		t.Fatalf("below limit = %q", got)
	}
}

func TestStatusSectionsNoteOnlyAReachedLimit(t *testing.T) {
	issues := func(n int) []displayIssue {
		out := make([]displayIssue, n)
		for i := range out {
			out[i] = displayIssue{Number: i + 1, Title: "issue"}
		}
		return out
	}
	prs := func(n int) []displayPullRequest {
		out := make([]displayPullRequest, n)
		for i := range out {
			out[i] = displayPullRequest{Number: i + 1, Title: "pr"}
		}
		return out
	}
	render := func(dashboard statusDashboard) string {
		var buf bytes.Buffer
		styler := newTableStyler(&buf, false)
		if err := renderStatusIssueSection(&buf, styler, dashboard); err != nil {
			t.Fatal(err)
		}
		if err := renderStatusPullRequestSection(&buf, styler, dashboard); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	full := render(statusDashboard{Issues: issues(statusListLimit), PullRequests: prs(statusListLimit)})
	for _, want := range []string{"Showing 30 issues (limit reached)", "Showing 30 pull requests (limit reached)"} {
		if !strings.Contains(full, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	short := render(statusDashboard{Issues: issues(statusListLimit - 1), PullRequests: prs(statusListLimit - 1)})
	if strings.Contains(short, "limit reached") {
		t.Fatalf("short lists must not claim a limit: %q", short)
	}
}

func TestRenderStatusMergedSectionNoticeOnlyOnError(t *testing.T) {
	render := func(dashboard statusDashboard) string {
		var buf bytes.Buffer
		if err := renderStatusMergedPullRequestSection(&buf, newTableStyler(&buf, false), dashboard); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if got := render(statusDashboard{ShowMergedPullRequests: true}); got != "\nRecently merged pull requests (0)\nNo merged pull requests found.\n" {
		t.Fatalf("merged section = %q", got)
	}
	got := render(statusDashboard{ShowMergedPullRequests: true, MergedPullRequestsSuppErr: errors.New("checks unavailable")})
	if !strings.Contains(got, "checks unavailable") {
		t.Fatalf("merged section with a supplemental error = %q", got)
	}
}

func TestStatusHeaderShowsCurrentOnlyOffTheDefaultBranch(t *testing.T) {
	render := func(current string) string {
		dashboard := statusDashboard{Repository: "demo/service", DefaultBranch: "main", CurrentStatus: statusSummary{Branch: current}, Worktrees: []statusWorktree{{}}}
		var buf bytes.Buffer
		renderStatusHeader(&buf, newTableStyler(&buf, false), dashboard)
		return buf.String()
	}
	if strings.Contains(render("main"), "Current") {
		t.Fatal("the default branch needs no current-branch row")
	}
	if !strings.Contains(render("feature"), "Current") {
		t.Fatal("another branch shows the current-branch row")
	}
}

func progressForTest(width, height int) (*statusProgress, *bytes.Buffer) {
	var buf bytes.Buffer
	return &statusProgress{output: &buf, width: width, height: height, active: "Working"}, &buf
}

func TestStatusProgressTickAndPaintAtTheSmallestSize(t *testing.T) {
	p, buf := progressForTest(2, 3)
	p.tick()
	if !strings.Contains(buf.String(), "\r\x1b[2K") {
		t.Fatalf("a 2x3 terminal still shows the spinner: %q", buf.String())
	}
	p, _ = progressForTest(2, 3)
	p.frame = "header\n"
	p.paint()
	if p.lines != 2 {
		t.Fatalf("a 2x3 terminal paints the frame and spinner: %d lines", p.lines)
	}
	p, _ = progressForTest(1, 3)
	p.frame = "header\n"
	p.paint()
	if p.lines != 0 {
		t.Fatalf("a 1-column terminal paints nothing: %d lines", p.lines)
	}
}

func TestStatusProgressPaintKeepsTwoRowsFree(t *testing.T) {
	p, _ := progressForTest(40, 5)
	p.frame = strings.Repeat("line\n", 10)
	p.paint()
	// Three frame rows (header, note, tail) plus the spinner.
	if p.lines != 4 {
		t.Fatalf("lines = %d, want 4", p.lines)
	}
}

func TestStatusProgressRefreshSizeIgnoresInvalidAndUnchangedSizes(t *testing.T) {
	p, _ := progressForTest(10, 5)
	for _, size := range [][2]int{{0, 5}, {10, 0}, {10, 5}} {
		p.size = func() (int, int, error) { return size[0], size[1], nil }
		if p.refreshSize() || p.width != 10 || p.height != 5 {
			t.Fatalf("size %v changed the progress to %dx%d", size, p.width, p.height)
		}
	}
	p.size = func() (int, int, error) { return 12, 5, nil }
	if !p.refreshSize() || p.width != 12 {
		t.Fatal("a real resize is applied")
	}
	p.size = func() (int, int, error) { return 12, 6, nil }
	if !p.refreshSize() || p.height != 6 {
		t.Fatal("a height-only resize is applied")
	}
}

func TestStatusProgressClear(t *testing.T) {
	if got := statusProgressClear(1); got != "\r\x1b[J" {
		t.Fatalf("one line = %q", got)
	}
	if got := statusProgressClear(3); got != "\x1b[2A\r\x1b[J" {
		t.Fatalf("three lines = %q", got)
	}
}

func TestStatusProgressSpinnerFitsTheWidth(t *testing.T) {
	p, _ := progressForTest(6, 5)
	p.active = "abcdefgh"
	if got := p.spinner(); got != "| ab…" {
		t.Fatalf("spinner = %q", got)
	}
}

func TestStatusProgressLinesSummarizesHiddenLines(t *testing.T) {
	if got := statusProgressLines("a\nb\nc\n", 20, 3); strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("an exact fit keeps every line: %q", got)
	}
	frame := ""
	for i := range 20 {
		frame += string(rune('a'+i)) + "\n"
	}
	got := statusProgressLines(frame, 60, 10)
	want := "a|b|c|d|e|… 11 earlier lines; full output follows|q|r|s|t"
	if strings.Join(got, "|") != want {
		t.Fatalf("lines = %q", strings.Join(got, "|"))
	}
}

func TestStatusProgressUpdateKeepsTheFirstError(t *testing.T) {
	p, _ := progressForTest(40, 10)
	first := errors.New("first write failed")
	p.err = first
	p.update(&statusDashboard{Repository: "demo/service"})
	if !errors.Is(p.err, first) {
		t.Fatalf("err = %v", p.err)
	}
}

func TestNewStatusProgressNeedsARoomyTerminal(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	oldTTY, oldSize, oldVT := terminalOutputFunc, statusTerminalSizeFunc, statusVirtualTerminalFunc
	t.Cleanup(func() { terminalOutputFunc, statusTerminalSizeFunc, statusVirtualTerminalFunc = oldTTY, oldSize, oldVT })
	terminalOutputFunc = func(io.Writer) bool { return true }
	statusVirtualTerminalFunc = func(io.Writer) (func() error, error) { return func() error { return nil }, nil }
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct {
		width, height int
		want          bool
	}{{20, 5, true}, {19, 5, false}, {20, 4, false}} {
		statusTerminalSizeFunc = func(int) (int, int, error) { return tt.width, tt.height, nil }
		if got := newStatusProgress(file, false) != nil; got != tt.want {
			t.Errorf("%dx%d progress = %v, want %v", tt.width, tt.height, got, tt.want)
		}
	}
}
