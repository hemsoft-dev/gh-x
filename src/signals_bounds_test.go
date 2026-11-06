package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCopilotTallyMatchesAddsEveryCount(t *testing.T) {
	preamble := `**Findings:** 1 <img alt="High severity"> · 2 <img alt="Low severity">`
	if !copilotTallyMatches(preamble, 3) {
		t.Fatal("1 + 2 stated findings match three listed findings")
	}
	if copilotTallyMatches(preamble, 1) {
		t.Fatal("the first count alone is not the tally")
	}
}

func TestParseCopilotFindingsReadsEveryBadgeOnALine(t *testing.T) {
	line := `<img alt="High severity"> and <img alt="Low severity">`
	findings := parseCopilotFindings(line, true)
	if len(findings) != 2 || findings[0].severity != "high severity" || findings[1].severity != "low severity" {
		t.Fatalf("findings = %+v", findings)
	}
}

const sflHead = "0123456789abcdef0123456789abcdef01234567"

func sflPassBody(prefix string) string {
	return prefix + "## ✅ Verdict: PASS • Round 1\n\nAll good.\n<!-- sfl request=4 sha=" + sflHead + " -->"
}

func TestSFLStructuredHeadNeedsASingleMarker(t *testing.T) {
	if got := sflStructuredHead(sflPassBody("")); got != sflHead {
		t.Fatalf("head = %q", got)
	}
	quoted := "## ✅ Verdict: PASS • Round 1\n\nSee <!-- sfl note --> above.\n<!-- sfl request=4 sha=" + sflHead + " -->"
	if got := sflStructuredHead(quoted); got != "" {
		t.Fatalf("a second marker makes the receipt ambiguous: %q", got)
	}
}

func TestExactHeadSFLPassAtTheFirstReview(t *testing.T) {
	after := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	reviews := []aiReviewNode{{
		AuthorLogin: "hemsoft-sfl-reviewer[bot]", AuthorType: "Bot", State: "COMMENTED",
		OccurredAt: after.Add(time.Minute), ReviewBody: sflPassBody(""),
	}}
	if !exactHeadSFLPass(reviews, after, sflHead) {
		t.Fatal("the only SFL review is the latest one")
	}
}

func TestLatestCurrentCopilotWithoutAReview(t *testing.T) {
	if _, index := latestCurrentCopilot(nil, sflHead); index != -1 {
		t.Fatalf("index = %d, want -1", index)
	}
}

func TestValidStatusCacheEntryAtAgeZero(t *testing.T) {
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	key := statusCacheKey{RemoteFingerprint: statusRemoteFingerprint("https://github.com/owner/repo.git"), MergedLimit: 5, ColorEnabled: true}
	entry := statusCacheEntry{
		Version: statusCacheSchemaVersion, FetchedAt: now, Key: key, Repository: "owner/repo", DefaultBranch: "main",
		Issues: []statusCachedIssue{}, PullRequests: []statusCachedPullRequest{},
		PullRequestHeads: []string{}, WorkflowRuns: []statusCachedWorkflowRun{},
		ShowMergedPullRequests: true, MergedPullRequests: []statusCachedPullRequest{},
	}
	if !validStatusCacheEntry(entry, key, now) {
		t.Fatal("an entry fetched this instant is fresh")
	}
}

func TestStatusDashboardCacheable(t *testing.T) {
	if !statusDashboardCacheable(statusDashboard{DefaultBranch: "main"}) {
		t.Fatal("a dashboard without errors is cacheable")
	}
	if statusDashboardCacheable(statusDashboard{}) {
		t.Fatal("a dashboard without a default branch is not cacheable")
	}
	if statusDashboardCacheable(statusDashboard{DefaultBranch: "main", WorkflowRunsErr: os.ErrDeadlineExceeded}) {
		t.Fatal("a failed section is not cacheable")
	}
}

func TestPruneStatusCacheFilesAgeBoundary(t *testing.T) {
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	dir := t.TempDir()
	write := func(name string, age time.Duration) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	fresh := write("fresh.json", time.Minute)
	boundary := write("boundary.json", 2*statusCacheTTL)
	pruneStatusCacheFiles(dir, "", now)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a fresh file was pruned: %v", err)
	}
	if _, err := os.Stat(boundary); !os.IsNotExist(err) {
		t.Fatalf("a file exactly two lifetimes old is pruned: %v", err)
	}
}

func TestStatusCLIConfigDirFallsBackToHomeConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prefers AppData")
	}
	home := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", filepath.Join(home, "appdata"))
	t.Setenv("HOME", home)
	if got := statusCLIConfigDir(); got != filepath.Join(home, ".config", "gh") {
		t.Fatalf("config dir = %q", got)
	}
}

func TestNormalizeLinkedReferencesOrdersEqualNumbersByURL(t *testing.T) {
	got := normalizeLinkedReferences([]linkedReference{
		{Number: 7, URL: "https://example.test/b/7"},
		{Number: 7, URL: "https://example.test/a/7"},
		{Number: 3, URL: "https://example.test/c/3"},
	})
	urls := make([]string, len(got))
	for i, ref := range got {
		urls[i] = ref.URL
	}
	if strings.Join(urls, " ") != "https://example.test/c/3 https://example.test/a/7 https://example.test/b/7" {
		t.Fatalf("order = %q", urls)
	}
}

func TestParseViewArgsWithATrailingRepoFlag(t *testing.T) {
	number, repo, err := parseViewArgs([]string{"12", "--repo"})
	if err != nil || number != "12" || repo != "" {
		t.Fatalf("number %q repo %q err %v", number, repo, err)
	}
	number, repo, err = parseViewArgs([]string{"--repo", "o/r", "12"})
	if err != nil || number != "12" || repo != "o/r" {
		t.Fatalf("number %q repo %q err %v", number, repo, err)
	}
}
