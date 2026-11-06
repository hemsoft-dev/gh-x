package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func numbersUpTo(count int) []int {
	numbers := make([]int, count)
	for i := range numbers {
		numbers[i] = i + 1
	}
	return numbers
}

func TestRelationshipAndHierarchyFetchesUseOneBatchForAFullBatch(t *testing.T) {
	savedRel, savedHier := fetchIssueRelationshipsBatchFunc, fetchIssueHierarchiesBatchFunc
	t.Cleanup(func() { fetchIssueRelationshipsBatchFunc, fetchIssueHierarchiesBatchFunc = savedRel, savedHier })
	relCalls, hierCalls := 0, 0
	fetchIssueRelationshipsBatchFunc = func(context.Context, string, string, string, []int) (map[int][]linkedReference, map[int]bool, error) {
		relCalls++
		return nil, nil, nil
	}
	fetchIssueHierarchiesBatchFunc = func(context.Context, string, string, string, []int) (map[int]issueHierarchy, map[int]issueHierarchyUnavailable, error) {
		hierCalls++
		return nil, nil, nil
	}
	numbers := numbersUpTo(relationshipBatchSize)
	if _, _, err := fetchIssueRelationshipsContext(context.Background(), "o", "r", "github.com", numbers); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fetchIssueHierarchiesContext(context.Background(), "o", "r", "github.com", numbers); err != nil {
		t.Fatal(err)
	}
	if relCalls != 1 || hierCalls != 1 {
		t.Fatalf("batches = %d relationships, %d hierarchies; want 1 each", relCalls, hierCalls)
	}
}

func TestFetchPRSupplementalUsesOneBatchForAFullBatch(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	t.Cleanup(func() { fetchPRSupplementalBatchFunc = saved })
	var sizes []int
	fetchPRSupplementalBatchFunc = func(_, _, _ string, numbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		sizes = append(sizes, len(numbers))
		return nil, nil, nil
	}
	if _, _, err := fetchPRSupplemental("o", "r", "github.com", numbersUpTo(relationshipBatchSize)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes) != fmt.Sprint([]int{relationshipBatchSize}) {
		t.Fatalf("batch sizes = %v", sizes)
	}
}

func TestResolveMonitorAllHostQueriesUsesOneBatchForSixteenOwners(t *testing.T) {
	saved := monitorGHExecFunc
	t.Cleanup(func() { monitorGHExecFunc = saved })
	orgs := make([]string, monitorOwnerBatchSize-1)
	for i := range orgs {
		orgs[i] = fmt.Sprintf(`{"login":"org%02d"}`, i)
	}
	response := `{"data":{"viewer":{"login":"me","organizations":{"nodes":[` + strings.Join(orgs, ",") + `],"pageInfo":{"hasNextPage":false}}}}}`
	monitorGHExecFunc = func(context.Context, ...string) (bytes.Buffer, bytes.Buffer, error) {
		return *bytes.NewBufferString(response), bytes.Buffer{}, nil
	}
	requests, err := resolveMonitorAllHostQueries(context.Background(), monitorHostQuery{Host: "github.com"}, monitorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || len(requests[0].OwnerScope) != monitorOwnerBatchSize {
		t.Fatalf("requests = %d", len(requests))
	}
}

func TestFetchIssueEnrichmentCombinesAFullBatch(t *testing.T) {
	savedRel, savedHier, savedExec := fetchIssueRelationshipsFunc, fetchIssueHierarchiesFunc, ghExecContextFunc
	t.Cleanup(func() {
		fetchIssueRelationshipsFunc, fetchIssueHierarchiesFunc, ghExecContextFunc = savedRel, savedHier, savedExec
	})
	separate := 0
	fetchIssueRelationshipsFunc = func(context.Context, string, string, string, []int) (map[int][]linkedReference, map[int]bool, error) {
		separate++
		return nil, nil, nil
	}
	fetchIssueHierarchiesFunc = func(context.Context, string, string, string, []int) (map[int]issueHierarchy, map[int]issueHierarchyUnavailable, error) {
		return nil, nil, nil
	}
	ghExecContextFunc = func(context.Context, ...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, bytes.Buffer{}, errors.New("offline")
	}
	issues := func(count int) []issueEntry {
		out := make([]issueEntry, count)
		for i := range out {
			out[i] = issueEntry{Number: i + 1}
		}
		return out
	}
	fetchIssueEnrichmentDataMode(context.Background(), "o/r", issues(relationshipBatchSize), true)
	if separate != 0 {
		t.Fatalf("a full batch uses the combined query, separate calls = %d", separate)
	}
	fetchIssueEnrichmentDataMode(context.Background(), "o/r", issues(relationshipBatchSize+1), true)
	if separate != 1 {
		t.Fatalf("an oversized list uses separate queries, calls = %d", separate)
	}
}

func TestHostFromHostnameArgsIgnoresATrailingFlag(t *testing.T) {
	if got := hostFromHostnameArgs([]string{"api", "--hostname"}); got != "" {
		t.Fatalf("host = %q", got)
	}
	if got := hostFromHostnameArgs([]string{"api", "--hostname", "ghe.example.com"}); got != "ghe.example.com" {
		t.Fatalf("host = %q", got)
	}
}

func TestIsEmptySearchListForIssues(t *testing.T) {
	if !isEmptySearchList([]string{"issue", "list", "--search", "x"}, []byte("[]")) {
		t.Fatal("an empty issue search list is detected")
	}
	if !isEmptySearchList([]string{"pr", "list", "--search=x"}, []byte(" [] ")) {
		t.Fatal("an empty PR search list is detected")
	}
}

func TestRunGHCmdRawFindsGHOnPathAndPassesStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stand-in for gh")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncat\nprintf '|%s' \"$GHX_PROBE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdout, _, err := runGHCmdRaw(ghInvocation{Args: []string{"x"}, Stdin: []byte("input"), ExtraEnv: []string{"GHX_PROBE=set"}})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "input|set" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDefaultGitRemoteURLReadsOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://example.test/o/r.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	t.Chdir(dir)
	if got := defaultGitRemoteURL(context.Background()); got != "https://example.test/o/r.git" {
		t.Fatalf("remote = %q", got)
	}
}

func TestTrimPromptTextLimits(t *testing.T) {
	tests := []struct {
		value string
		limit int
		want  string
	}{
		{"ab", 1, "a\n[truncated]"},
		{"abc", 2, "ab\n[truncated]"},
		{"abc", 3, "abc"},
		{"abc", 0, "abc"},
		{"ééé", 3, "ééé"},
	}
	for _, tt := range tests {
		if got := trimPromptText(tt.value, tt.limit); got != tt.want {
			t.Errorf("trimPromptText(%q, %d) = %q, want %q", tt.value, tt.limit, got, tt.want)
		}
	}
}

func TestExecuteReviewBaseOverride(t *testing.T) {
	saved := fetchReviewPullRequestFunc
	t.Cleanup(func() { fetchReviewPullRequestFunc = saved })
	fetchReviewPullRequestFunc = func(prReviewOptions) (reviewPullRequest, error) {
		return reviewPullRequest{Number: 9, BaseRefName: "main", HeadRefName: "feature"}, nil
	}
	run := func(base string) string {
		var stdout bytes.Buffer
		options := defaultReviewOptions()
		options.target, options.dryRun, options.base, options.post, options.allowApprove = "9", true, base, false, false
		if err := executeReview(options, &stdout, io.Discard); err != nil {
			t.Fatal(err)
		}
		return stdout.String()
	}
	if got := run(""); !strings.Contains(got, "git diff main...feature") {
		t.Fatalf("without an override the PR base is used:\n%s", got)
	}
	if got := run("release"); !strings.Contains(got, "git diff release...feature") {
		t.Fatalf("an override replaces the base:\n%s", got)
	}
}

func TestRenderTableRepositoryHeading(t *testing.T) {
	var empty, full bytes.Buffer
	if err := renderTable(&empty, listOptions{repo: "o/r"}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(empty.String(), "Pull requests for") {
		t.Fatalf("an empty list has no heading: %q", empty.String())
	}
	if err := renderTable(&full, listOptions{repo: "o/r"}, []displayPullRequest{{Number: 1, Title: "x"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(full.String(), "Pull requests for o/r\n\n") {
		t.Fatalf("a populated list names the repository: %q", full.String())
	}
	if got := resolveRepoLabel("o/r"); got != "o/r" {
		t.Fatalf("label = %q", got)
	}
}

func TestRenderIssueTableRepositoryHeading(t *testing.T) {
	var out bytes.Buffer
	if err := renderIssueTable(&out, []displayIssue{{Number: 1, Title: "x"}}, issueListOptions{repo: "o/r"}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Issues for o/r\n\n") {
		t.Fatalf("issue table = %q", out.String())
	}
}

func TestQuotaArgumentHelpers(t *testing.T) {
	if !quotaBrowserList([]string{"issue", "list", "--web"}) || !quotaBrowserList([]string{"pr", "list", "-w"}) {
		t.Fatal("browser lists are detected for issues and PRs")
	}
	if got := quotaEndpoint([]string{"api", "--paginate"}); got != "" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := quotaEndpoint([]string{"api", "--paginate", "repos/o/r"}); got != "repos/o/r" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestResolveMonitorEditorDefault(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	want := "vi"
	if runtime.GOOS == "windows" {
		want = "notepad"
	}
	if got := resolveMonitorEditor(); got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}
}

func TestUpdateCachePathHonoursOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GH_X_CACHE_DIR", dir)
	if got := updateCachePath(); !strings.HasPrefix(got, dir) {
		t.Fatalf("cache path = %q, want under %q", got, dir)
	}
}

func TestCompleteCommandPrintsHintsOnlyWhenKnown(t *testing.T) {
	closed := make(chan string)
	close(closed)
	var plain, hinted bytes.Buffer
	completeCommand(&plain, closed, errors.New("something odd"))
	if strings.Contains(plain.String(), "Hint:") {
		t.Fatalf("an unknown error has no hint: %q", plain.String())
	}
	completeCommand(&hinted, closed, errors.New("HTTP 401 bad credentials"))
	if !strings.Contains(hinted.String(), "Hint: Your GitHub token may be expired") {
		t.Fatalf("a known error explains itself: %q", hinted.String())
	}
}

func gitRepoWithWorkflow(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	workflows := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ci.yml", "release.yaml"} {
		if err := os.WriteFile(filepath.Join(workflows, name), []byte("on: push\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadWorkflowContentFromTheRepositoryRoot(t *testing.T) {
	root := gitRepoWithWorkflow(t)
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })
	ghExecFunc = func(...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, bytes.Buffer{}, errors.New("no network in tests")
	}
	t.Chdir(filepath.Join(root, "sub"))
	for _, path := range []string{".github/workflows/ci.yml", ".github/workflows/release.yaml"} {
		content, err := readWorkflowContent(workflowListOptions{}, workflowEntry{Path: path})
		if err != nil || string(content) != "on: push\n" {
			t.Fatalf("%s: %q %v", path, content, err)
		}
	}
	if _, err := readWorkflowContent(workflowListOptions{}, workflowEntry{Path: "README.md"}); err == nil {
		t.Fatal("only YAML workflows are read")
	}
	enriched := fetchWorkflowTriggers(workflowListOptions{}, []workflowEntry{{Path: ".github/workflows/ci.yml"}})
	if enriched[0].Triggers == "unknown" || enriched[0].Triggers == "" {
		t.Fatalf("triggers = %q", enriched[0].Triggers)
	}
}

func TestReadLocalWorkflowContentOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flow.yml"), []byte("on: push\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	content, err := readLocalWorkflowContent("flow.yml")
	if err != nil || string(content) != "on: push\n" {
		t.Fatalf("content %q err %v", content, err)
	}
	if _, err := readLocalWorkflowContent("missing.yml"); err == nil {
		t.Fatal("a missing workflow fails")
	}
}
