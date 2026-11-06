package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStatusCacheEntryValidation(t *testing.T) {
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	key := statusCacheKey{RemoteFingerprint: statusRemoteFingerprint("https://github.com/owner/repo.git"), MergedLimit: 5, ColorEnabled: true}
	complete := func(at time.Time, cacheKey statusCacheKey, version int) statusCacheEntry {
		return statusCacheEntry{
			Version: version, FetchedAt: at, Key: cacheKey, Repository: "owner/repo", DefaultBranch: "main",
			Issues: []statusCachedIssue{}, PullRequests: []statusCachedPullRequest{},
			PullRequestHeads: []string{}, WorkflowRuns: []statusCachedWorkflowRun{},
			ShowMergedPullRequests: true, MergedPullRequests: []statusCachedPullRequest{},
		}
	}
	tests := []struct {
		name    string
		entry   statusCacheEntry
		expects bool
	}{
		{name: "fresh", entry: complete(now.Add(-59*time.Second), key, statusCacheSchemaVersion), expects: true},
		{name: "expiry boundary", entry: complete(now.Add(-statusCacheTTL), key, statusCacheSchemaVersion)},
		{name: "future", entry: complete(now.Add(time.Second), key, statusCacheSchemaVersion)},
		{name: "wrong schema", entry: complete(now, key, statusCacheSchemaVersion+1)},
		{name: "wrong remote", entry: complete(now, statusCacheKey{RemoteFingerprint: statusRemoteFingerprint("https://github.com/owner/other.git"), MergedLimit: 5, ColorEnabled: true}, statusCacheSchemaVersion)},
		{name: "wrong merged limit", entry: complete(now, statusCacheKey{RemoteFingerprint: key.RemoteFingerprint, MergedLimit: 2, ColorEnabled: true}, statusCacheSchemaVersion)},
		{name: "wrong color mode", entry: complete(now, statusCacheKey{RemoteFingerprint: key.RemoteFingerprint, MergedLimit: 5}, statusCacheSchemaVersion)},
		{name: "incomplete cache", entry: statusCacheEntry{Version: statusCacheSchemaVersion, FetchedAt: now, Key: key}},
		{name: "missing hosted default branch", entry: func() statusCacheEntry {
			entry := complete(now, key, statusCacheSchemaVersion)
			entry.DefaultBranch = ""
			return entry
		}()},
		{name: "missing workflows", entry: func() statusCacheEntry {
			entry := complete(now, key, statusCacheSchemaVersion)
			entry.WorkflowRuns = nil
			return entry
		}()},
		{name: "missing merged rows", entry: func() statusCacheEntry {
			entry := complete(now, key, statusCacheSchemaVersion)
			entry.MergedPullRequests = nil
			return entry
		}()},
		{name: "wrong merged visibility", entry: func() statusCacheEntry {
			entry := complete(now, key, statusCacheSchemaVersion)
			entry.ShowMergedPullRequests = false
			return entry
		}()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validStatusCacheEntry(test.entry, key, now); got != test.expects {
				t.Fatalf("validStatusCacheEntry() = %v, want %v", got, test.expects)
			}
		})
	}
}

func TestStatusCacheRoundTripPreservesRenderedMetadata(t *testing.T) {
	directory := t.TempDir()
	remoteURL := "git@github.com:owner/repo.git"
	useStatusCacheDirectory(t, directory, remoteURL)
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	issueRef := linkedReference{Number: 42, URL: "https://github.com/owner/repo/pull/42"}
	parentRef := linkedReference{Number: 7, URL: "https://github.com/other/repo/issues/7", Text: "other/repo#7"}
	pullRequestRef := linkedReference{Number: 9, URL: "https://github.com/other/repo/issues/9", Text: "other/repo#9"}
	clean := true
	dashboard := statusDashboard{
		Repository:    "owner/repo",
		RepositoryURL: "https://github.com/owner/repo",
		DefaultBranch: "trunk",
		Issues: []displayIssue{{
			Number: 8, PullRequests: "#42", Parent: "#7", Title: "Cached issue", Updated: "59s",
			pullRequestRefs: []linkedReference{issueRef}, parentRefs: []linkedReference{parentRef}, updatedAt: now.Add(-59 * time.Second),
		}},
		PullRequests: []displayPullRequest{{
			Number: 42, Issues: "#9", Title: "Cached PR", AIClean: &clean, Updated: "59s",
			Approvers:        []displayApprover{{Login: "reviewer", ApprovedAt: now.Add(-59 * time.Second), Age: "59s"}},
			checksDowngraded: true, issueRefs: []linkedReference{pullRequestRef}, updatedAt: now.Add(-59 * time.Second),
		}},
		ShowMergedPullRequests: true,
		MergedPullRequests: []displayPullRequest{{
			Number: 41, Title: "Merged PR", mergedAt: now.Add(-2 * time.Hour),
		}},
		WorkflowRuns: []displayWorkflowRun{{
			ID: "100", Title: "CI", Age: "59s", Elapsed: "59s", rawStatus: "in_progress",
			createdAt: now.Add(-59 * time.Second), startedAt: now.Add(-59 * time.Second), updatedAt: now,
		}},
		WorkflowRunsPerfect: true,
	}
	options := statusOptions{mergedLimit: 5}
	saveStatusCache(options, true, now, dashboard, map[string]bool{"feature/cache": true}, true)

	cached, ok := loadStatusCache(options, true, now.Add(10*time.Second))
	if !ok {
		t.Fatal("fresh cache was not loaded")
	}
	var restored statusDashboard
	heads, known := applyStatusCache(&restored, cached, now.Add(10*time.Second))
	if !known || !heads["feature/cache"] || cached.DefaultBranch != "trunk" {
		t.Fatalf("cached pull request or branch metadata = %#v, known=%v, branch=%q", heads, known, cached.DefaultBranch)
	}
	if !reflect.DeepEqual(restored.Issues[0].pullRequestRefs, []linkedReference{issueRef}) ||
		!reflect.DeepEqual(restored.Issues[0].parentRefs, []linkedReference{parentRef}) ||
		restored.Issues[0].parentRefs[0].displayText() != "other/repo#7" {
		t.Fatalf("issue references were not restored: %#v", restored.Issues[0])
	}
	if !restored.PullRequests[0].checksDowngraded || !reflect.DeepEqual(restored.PullRequests[0].issueRefs, []linkedReference{pullRequestRef}) ||
		restored.PullRequests[0].issueRefs[0].displayText() != "other/repo#9" || !restored.PullRequests[0].updatedAt.Equal(now.Add(-59*time.Second)) {
		t.Fatalf("pull request metadata was not restored: %#v", restored.PullRequests[0])
	}
	if restored.Issues[0].Updated != "1m" || restored.PullRequests[0].Updated != "1m" ||
		restored.PullRequests[0].Approvers[0].Age != "1m" || restored.WorkflowRuns[0].Age != "1m" ||
		restored.WorkflowRuns[0].Elapsed != "1m9s" {
		t.Fatalf("warm relative times did not advance: issue=%q, PR=%q, approval=%q, run=%#v",
			restored.Issues[0].Updated, restored.PullRequests[0].Updated,
			restored.PullRequests[0].Approvers[0].Age, restored.WorkflowRuns[0])
	}
	if !restored.MergedPullRequests[0].mergedAt.Equal(now.Add(-2*time.Hour)) || !restored.WorkflowRunsPerfect {
		t.Fatalf("merged or workflow metadata was not restored: %#v", restored)
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name() != ".status-cache.lock" || !strings.HasSuffix(files[1].Name(), ".json") {
		t.Fatalf("cache write was not a single finalized snapshot: %#v", files)
	}
	data, err := os.ReadFile(filepath.Join(directory, files[1].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "authorization", "header"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("cache contains forbidden credential field %q", forbidden)
		}
	}
}

func TestStatusCachePreservesSectionFreshnessDuringSlowFetch(t *testing.T) {
	defer saveStatusFuncs()()
	installStatusDashboardGitFixture()
	directory := t.TempDir()
	gitCommand := statusCommandFunc
	statusCommandFunc = func(name string, args ...string) (string, error) {
		output, err := gitCommand(name, args...)
		if name == "git" && len(args) > 0 && args[0] == "for-each-ref" {
			output = strings.ReplaceAll(output, "refs/remotes/origin/HEAD\torigin\t\t\trefs/remotes/origin/main\n", "")
		}
		return output, err
	}
	statusCacheDirectoryFunc = func() (string, string, error) {
		return directory, statusRemoteFingerprint("owner/repo"), nil
	}
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	statusNowFunc = func() time.Time { return now }
	statusRepoLabelFunc = func(string) string { return "owner/repo" }
	branchCalls, remoteCalls := 0, 0
	statusDefaultBranchFunc = func() string { branchCalls++; return "main" }
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) {
		remoteCalls++
		return issueListResult{}, nil
	}
	statusPullRequestListFunc = func(listOptions, time.Time) (pullRequestListResult, error) {
		remoteCalls++
		return pullRequestListResult{}, nil
	}
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		remoteCalls++
		now = now.Add(50 * time.Second) // Slow remote fetch must not consume the cache lifetime.
		return workflowRunListResult{}, nil
	}
	for i := 0; i < 2; i++ {
		dashboard, err := fetchStatusDashboard(false, statusOptions{mergedLimit: 0})
		if err != nil || dashboard.DefaultBranch != "main" {
			t.Fatalf("status invocation %d: branch=%q, err=%v", i, dashboard.DefaultBranch, err)
		}
	}
	now = now.Add(59 * time.Second)
	if _, err := fetchStatusDashboard(false, statusOptions{mergedLimit: 0}); err != nil {
		t.Fatal(err)
	}
	if branchCalls != 1 || remoteCalls != 5 {
		t.Fatalf("slow fetch must retain hosted branch and fresh workflow data but refresh expired issue/PR sections: %d hosted branch and %d remote calls, want 1 and 5", branchCalls, remoteCalls)
	}
}

func TestStatusCacheDoesNotPublishAcrossConcurrentBranchSwitch(t *testing.T) {
	defer saveStatusFuncs()()
	installStatusDashboardGitFixture()
	directory := t.TempDir()
	fingerprint := "origin-identity"
	identityCalls := 0
	statusCacheDirectoryFunc = func() (string, string, error) {
		identityCalls++
		return directory, fingerprint, nil
	}
	statusRepoLabelFunc = func(string) string { return "owner/origin" }
	statusIssueListFunc = func(issueListOptions, time.Time) (issueListResult, error) {
		return issueListResult{}, nil
	}
	statusPullRequestListFunc = func(listOptions, time.Time) (pullRequestListResult, error) {
		return pullRequestListResult{}, nil
	}
	statusWorkflowRunListFunc = func(runListOptions, time.Time) (workflowRunListResult, error) {
		fingerprint = "upstream-identity" // Another process changed the active branch mid-fetch.
		return workflowRunListResult{}, nil
	}
	if _, err := fetchStatusDashboard(false, statusOptions{mergedLimit: 0}); err != nil {
		t.Fatal(err)
	}
	if identityCalls != 2 {
		t.Fatalf("cold lookup and post-fetch validation made %d identity calls, want 2", identityCalls)
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("old repository data published under new branch remote: %#v", files)
	}
}

func TestLoadStatusCacheIgnoresCorruptAndIncompatibleFiles(t *testing.T) {
	directory := t.TempDir()
	remoteURL := "https://github.com/owner/repo.git"
	useStatusCacheDirectory(t, directory, remoteURL)
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(directory, "status-corrupt.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	incompatible := statusCacheEntry{
		Version:   statusCacheSchemaVersion + 1,
		FetchedAt: now,
		Key:       statusCacheKey{RemoteFingerprint: statusRemoteFingerprint(remoteURL), MergedLimit: 5},
	}
	if err := writeStatusCacheEntry(directory, incompatible, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadStatusCache(statusOptions{mergedLimit: 5}, false, now); ok {
		t.Fatal("corrupt or incompatible cache must be ignored")
	}
}

func TestSaveStatusCacheSkipsFailedSnapshots(t *testing.T) {
	directory := t.TempDir()
	useStatusCacheDirectory(t, directory, "https://github.com/owner/repo.git")
	dashboard := statusDashboard{IssuesErr: errors.New("offline")}
	saveStatusCache(statusOptions{mergedLimit: 5}, false, time.Now().UTC(), dashboard, nil, false)
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("failed status snapshot wrote cache files: %#v", files)
	}
}

func TestLoadStatusCacheKeepsRepositoriesAndOptionsIsolated(t *testing.T) {
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	options := statusOptions{mergedLimit: 5}
	firstDirectory := t.TempDir()
	useStatusCacheDirectory(t, firstDirectory, "https://github.com/owner/first.git")
	saveStatusCache(options, false, now, statusDashboard{Repository: "owner/first", DefaultBranch: "main", ShowMergedPullRequests: true}, nil, true)

	if cached, ok := loadStatusCache(options, false, now); !ok || cached.Repository != "owner/first" {
		t.Fatalf("first repository cache = %#v, hit=%v", cached, ok)
	}
	statusCacheDirectoryFunc = func() (string, string, error) {
		return firstDirectory, statusRemoteFingerprint("https://github.com/owner/second.git"), nil
	}
	if _, ok := loadStatusCache(options, false, now); ok {
		t.Fatal("different remote reused the first repository cache")
	}
	statusCacheDirectoryFunc = func() (string, string, error) {
		return t.TempDir(), statusRemoteFingerprint("https://github.com/owner/first.git"), nil
	}
	if _, ok := loadStatusCache(options, false, now); ok {
		t.Fatal("different repository directory reused the first cache")
	}
	statusCacheDirectoryFunc = func() (string, string, error) {
		return firstDirectory, statusRemoteFingerprint("https://github.com/owner/first.git"), nil
	}
	if _, ok := loadStatusCache(statusOptions{mergedLimit: 2}, false, now); ok {
		t.Fatal("different merged limit reused an incompatible cache")
	}
}

func TestStatusTargetFingerprintSeparatesGitHubOverrides(t *testing.T) {
	remoteURL := "git@github.com:owner/repo.git"
	t.Setenv("GH_REPO", "owner/repo")
	t.Setenv("GH_HOST", "github.com")
	config := "remote.origin.url\n" + remoteURL + "\x00"
	original := statusTargetFingerprint(config, "origin", "auth-context")
	for _, test := range []struct {
		name, repo, host string
	}{
		{name: "different repository", repo: "owner/other", host: "github.com"},
		{name: "different host", repo: "owner/repo", host: "ghe.example.com"},
		{name: "host qualified repository", repo: "ghe.example.com/owner/repo", host: "github.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GH_REPO", test.repo)
			t.Setenv("GH_HOST", test.host)
			if got := statusTargetFingerprint(config, "origin", "auth-context"); got == original {
				t.Fatal("changed GitHub target reused origin-only fingerprint")
			}
		})
	}
}

func TestStatusCacheDirectoryTracksConfiguredDefaultRemote(t *testing.T) {
	defer saveStatusFuncs()()
	isolateStatusCacheAuthentication(t)
	commonDirectory := t.TempDir()
	remoteConfig := "remote.origin.url\nhttps://github.com/owner/repo.git\x00" +
		"remote.other.url\nhttps://github.com/owner/other.git\x00" +
		"remote.origin.gh-resolved\nbase\x00"
	statusCommandFunc = func(name string, args ...string) (string, error) {
		switch name + " " + strings.Join(args, " ") {
		case "git rev-parse --git-common-dir":
			return commonDirectory, nil
		case "git config --includes --null --get-regexp ^(remote\\..*\\.(url|gh-resolved)|url\\..*)$":
			return remoteConfig, nil
		case "git symbolic-ref -q HEAD":
			return "refs/heads/main\n", nil
		case "git config --includes --get branch.main.remote":
			return "origin", nil
		default:
			return "", errors.New("unexpected git command")
		}
	}
	_, first, err := statusCacheDirectory()
	if err != nil {
		t.Fatal(err)
	}
	remoteConfig = strings.Replace(remoteConfig, "remote.origin.gh-resolved", "remote.other.gh-resolved", 1)
	_, second, err := statusCacheDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("gh repo set-default changed the effective repository but retained the cache key")
	}
}

func TestStatusRemoteConfigHonorsIncludesAndWorktrees(t *testing.T) {
	defer saveStatusFuncs()()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, err := runStatusCommand("git", append([]string{"-C", repo}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/owner/repo.git")
	git("remote", "add", "other", "https://github.com/owner/other.git")
	include := filepath.Join(t.TempDir(), "remote.conf")
	if err := os.WriteFile(include, []byte("[remote \"other\"]\n  gh-resolved = base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("config", "--local", "include.path", include)
	statusCommandFunc = func(name string, args ...string) (string, error) {
		return runStatusCommand(name, append([]string{"-C", repo}, args...)...)
	}
	included, err := statusRemoteConfig()
	if err != nil || !strings.Contains(included, "remote.other.gh-resolved\nbase") {
		t.Fatalf("included remote selector missing: %q, err=%v", included, err)
	}
	if err := os.WriteFile(include, []byte("[remote \"origin\"]\n  gh-resolved = base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := statusRemoteConfig()
	if err != nil || changed == included {
		t.Fatalf("changed include reused remote fingerprint input: %q, err=%v", changed, err)
	}
	git("config", "--local", "--unset", "include.path")
	git("config", "--local", "extensions.worktreeConfig", "true")
	git("config", "--worktree", "remote.other.gh-resolved", "base")
	worktree, err := statusRemoteConfig()
	if err != nil || !strings.Contains(worktree, "remote.other.gh-resolved\nbase") {
		t.Fatalf("worktree remote selector missing: %q, err=%v", worktree, err)
	}
}

func TestStatusBranchRemoteSeparatesTrackedRemotesAcrossBranches(t *testing.T) {
	defer saveStatusFuncs()()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, err := runStatusCommand("git", append([]string{"-C", repo}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial")
	git("remote", "add", "origin", "https://github.com/owner/one.git")
	git("remote", "add", "upstream", "https://github.com/owner/two.git")
	for _, name := range []string{"origin-work", "upstream-work", "another-origin-work"} {
		git("branch", name)
	}
	git("config", "--local", "branch.origin-work.remote", "origin")
	git("config", "--local", "branch.upstream-work.remote", "upstream")
	git("config", "--local", "branch.another-origin-work.remote", "origin")
	statusCommandFunc = func(name string, args ...string) (string, error) {
		return runStatusCommand(name, append([]string{"-C", repo}, args...)...)
	}
	config, err := statusRemoteConfig()
	if err != nil {
		t.Fatal(err)
	}
	git("checkout", "-q", "origin-work")
	origin, err := statusBranchRemote()
	if err != nil || origin != "origin" {
		t.Fatalf("origin branch remote = %q, err=%v", origin, err)
	}
	git("checkout", "-q", "upstream-work")
	upstream, err := statusBranchRemote()
	if err != nil || upstream != "upstream" ||
		statusTargetFingerprint(config, origin, "auth") == statusTargetFingerprint(config, upstream, "auth") {
		t.Fatalf("upstream branch reused origin identity: %q, err=%v", upstream, err)
	}
	git("checkout", "-q", "another-origin-work")
	otherOrigin, err := statusBranchRemote()
	if err != nil || statusTargetFingerprint(config, origin, "auth") != statusTargetFingerprint(config, otherOrigin, "auth") {
		t.Fatalf("same remote across linked branches lost shared identity: %q, err=%v", otherOrigin, err)
	}
}

func TestStatusRemoteConfigTracksURLRewrites(t *testing.T) {
	defer saveStatusFuncs()()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		output, err := runStatusCommand("git", append([]string{"-C", repo}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(output)
	}
	git("init", "-q")
	git("remote", "add", "origin", "work:repo.git")
	statusCommandFunc = func(name string, args ...string) (string, error) {
		return runStatusCommand(name, append([]string{"-C", repo}, args...)...)
	}
	git("config", "--local", "url.https://github.com/one/.insteadOf", "work:")
	firstURL := git("remote", "get-url", "origin")
	first, err := statusRemoteConfig()
	if err != nil {
		t.Fatal(err)
	}
	git("config", "--local", "--unset", "url.https://github.com/one/.insteadOf")
	git("config", "--local", "url.https://github.com/two/.insteadOf", "work:")
	secondURL := git("remote", "get-url", "origin")
	second, err := statusRemoteConfig()
	if err != nil || first == second || firstURL == secondURL ||
		!strings.Contains(firstURL, "github.com/one/") || !strings.Contains(secondURL, "github.com/two/") {
		t.Fatalf("effective URL rewrite was not isolated: %q to %q, err=%v", firstURL, secondURL, err)
	}
}

func TestStatusCacheDirectoryWithoutOrigin(t *testing.T) {
	defer saveStatusFuncs()()
	isolateStatusCacheAuthentication(t)
	commonDirectory := t.TempDir()
	remoteConfig := "remote.upstream.url\nhttps://github.com/owner/repo.git\x00"
	statusCommandFunc = func(name string, args ...string) (string, error) {
		switch name + " " + strings.Join(args, " ") {
		case "git rev-parse --git-common-dir":
			return commonDirectory, nil
		case "git config --includes --null --get-regexp ^(remote\\..*\\.(url|gh-resolved)|url\\..*)$":
			return remoteConfig, nil
		case "git symbolic-ref -q HEAD":
			return "refs/heads/main\n", nil
		case "git config --includes --get branch.main.remote":
			if remoteConfig == "" {
				return "", nil
			}
			return "upstream", nil
		default:
			return "", errors.New("unexpected git command")
		}
	}
	authContext, err := statusAuthContext()
	if err != nil {
		t.Fatal(err)
	}
	_, upstream, err := statusCacheDirectory()
	if err != nil || upstream != statusTargetFingerprint(remoteConfig, "upstream", authContext) {
		t.Fatalf("upstream-only checkout cache = %q, err=%v", upstream, err)
	}
	t.Setenv("GH_REPO", "owner/repo")
	remoteConfig = ""
	// Run the real no-match query in a private repository so the result does
	// not depend on whether the test's working directory is inside a clone.
	repo := t.TempDir()
	if _, err := runStatusCommand("git", "-C", repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	configNoMatches := func() (string, error) {
		return runStatusCommand("git", "-C", repo, "config", "--local", "--get-regexp", `^nonexistent-gh-x-cache-config-key$`)
	}
	var exitErr *exec.ExitError
	if output, err := configNoMatches(); output != "" || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("real git config no-match = %q, err=%v; want exit status 1", output, err)
	}
	previousCommand := statusCommandFunc
	statusCommandFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "config" {
			return configNoMatches()
		}
		return previousCommand(name, args...)
	}
	_, override, err := statusCacheDirectory()
	if err != nil || override != statusTargetFingerprint("", "", authContext) || override == upstream {
		t.Fatalf("GH_REPO-only checkout cache = %q, err=%v", override, err)
	}
}

func TestStatusCacheDirectoryIsolatesAuthenticationContext(t *testing.T) {
	defer saveStatusFuncs()()
	isolateStatusCacheAuthentication(t)
	commonDirectory := t.TempDir()
	configDirectory := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", configDirectory)
	configPath := filepath.Join(configDirectory, "hosts.yml")
	if err := os.WriteFile(configPath, []byte("github.com:\n  user: alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyringToken := "fake-keyring-token-a"
	statusKeyringGetFunc = func(service, user string) (string, error) {
		if service != "gh:github.com" {
			t.Fatalf("unexpected credential service %q", service)
		}
		return keyringToken, nil
	}
	statusCommandFunc = func(name string, args ...string) (string, error) {
		switch name + " " + strings.Join(args, " ") {
		case "git rev-parse --git-common-dir":
			return commonDirectory, nil
		case "git config --includes --null --get-regexp ^(remote\\..*\\.(url|gh-resolved)|url\\..*)$":
			return "remote.origin.url\nhttps://github.com/owner/repo.git\x00", nil
		case "git symbolic-ref -q HEAD":
			return "refs/heads/main\n", nil
		case "git config --includes --get branch.main.remote":
			return "origin", nil
		default:
			return "", errors.New("unexpected git command")
		}
	}
	_, alice, err := statusCacheDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("github.com:\n  user: bob\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, bob, err := statusCacheDirectory()
	if err != nil || bob == alice {
		t.Fatalf("switched account reused cache key: %q, err=%v", bob, err)
	}
	keyringToken = "fake-keyring-token-b"
	_, rotated, err := statusCacheDirectory()
	if err != nil || rotated == bob {
		t.Fatalf("changed credential-store token reused cache key: %q, err=%v", rotated, err)
	}
	statusKeyringGetFunc = func(string, string) (string, error) { return "", errors.New("credential store unavailable") }
	if _, _, err := statusCacheDirectory(); err == nil {
		t.Fatal("unavailable credential store must disable cache reuse")
	}
	t.Setenv("GH_TOKEN", "test-token-a")
	_, tokenA, err := statusCacheDirectory()
	if err != nil || tokenA == rotated {
		t.Fatalf("environment token reused account key: %q, err=%v", tokenA, err)
	}
	t.Setenv("GH_TOKEN", "test-token-b")
	_, tokenB, err := statusCacheDirectory()
	if err != nil || tokenB == tokenA {
		t.Fatalf("changed environment token reused cache key: %q, err=%v", tokenB, err)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	_, tokenOnly, err := statusCacheDirectory()
	if err != nil || tokenOnly == tokenB {
		t.Fatalf("token-only authentication cache = %q, err=%v", tokenOnly, err)
	}
}

func TestStatusAuthContextTracksFallbackAccountToken(t *testing.T) {
	defer saveStatusFuncs()()
	configDirectory := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", configDirectory)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	content := "github.com:\n  user: alice\n  users:\n    alice: {}\n    bob: {}\n"
	if err := os.WriteFile(filepath.Join(configDirectory, "hosts.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	fallbackToken := "fallback-token-a"
	statusKeyringGetFunc = func(service, user string) (string, error) {
		if service != "gh:github.com" {
			t.Fatalf("unexpected credential service %q", service)
		}
		if user == "bob" {
			return fallbackToken, nil
		}
		return "active-token", nil
	}
	first, err := statusAuthContext()
	if err != nil {
		t.Fatal(err)
	}
	fallbackToken = "fallback-token-b"
	second, err := statusAuthContext()
	if err != nil || first == second {
		t.Fatalf("fallback account token rotation reused cache identity: %q, err=%v", second, err)
	}
}

func TestStatusAuthContextUsesGHECloudKeyringDespiteEnterpriseToken(t *testing.T) {
	defer saveStatusFuncs()()
	configDirectory := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", configDirectory)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "unrelated-enterprise-token")
	if err := os.WriteFile(filepath.Join(configDirectory, "hosts.yml"), []byte("tenant.ghe.com:\n  user: alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := "keyring-token-a"
	statusKeyringGetFunc = func(service, user string) (string, error) {
		if service != "gh:tenant.ghe.com" {
			t.Fatalf("unexpected credential service %q", service)
		}
		return token, nil
	}
	first, err := statusAuthContext()
	if err != nil {
		t.Fatal(err)
	}
	token = "keyring-token-b"
	second, err := statusAuthContext()
	if err != nil || first == second {
		t.Fatalf("GHE Cloud token rotation retained old auth context: %q, err=%v", second, err)
	}
	t.Setenv("GH_TOKEN", "explicit-cloud-token")
	statusKeyringGetFunc = func(string, string) (string, error) {
		t.Fatal("GH_TOKEN must override GHE Cloud keyring")
		return "", nil
	}
	if _, err := statusAuthContext(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusCLIConfigDirPrecedence(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got := statusCLIConfigDir(); got != filepath.Join(xdg, "gh") {
		t.Fatalf("XDG config directory = %q", got)
	}
	configured := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", configured)
	if got := statusCLIConfigDir(); got != configured {
		t.Fatalf("GH_CONFIG_DIR override = %q", got)
	}
}

func TestStatusCacheDirectoryWithGlobCharacters(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "[work]")
	useStatusCacheDirectory(t, directory, "https://github.com/owner/repo.git")
	now := time.Date(2026, 9, 21, 3, 30, 0, 0, time.UTC)
	options := statusOptions{mergedLimit: 5}
	saveStatusCache(options, false, now, statusDashboard{Repository: "owner/repo", DefaultBranch: "main", ShowMergedPullRequests: true}, nil, true)
	if cached, ok := loadStatusCache(options, false, now.Add(time.Second)); !ok || cached.Repository != "owner/repo" {
		t.Fatalf("literal cache directory with glob characters did not load: %#v, hit=%v", cached, ok)
	}
}

func loadStatusCache(options statusOptions, colorEnabled bool, now time.Time) (statusCacheEntry, bool) {
	directory, fingerprint, err := statusCacheDirectoryFunc()
	if err != nil {
		return statusCacheEntry{}, false
	}
	return loadStatusCacheAt(options, colorEnabled, now, directory, fingerprint)
}

func saveStatusCache(options statusOptions, colorEnabled bool, now time.Time, dashboard statusDashboard, heads map[string]bool, known bool) {
	directory, fingerprint, err := statusCacheDirectoryFunc()
	if err == nil {
		saveStatusCacheAt(options, colorEnabled, now, dashboard, heads, known, directory, fingerprint)
	}
}

func useStatusCacheDirectory(t *testing.T, directory, remoteURL string) {
	t.Helper()
	saved := statusCacheDirectoryFunc
	statusCacheDirectoryFunc = func() (string, string, error) {
		return directory, statusRemoteFingerprint(remoteURL), nil
	}
	t.Cleanup(func() {
		statusCacheDirectoryFunc = saved
	})
}

// Cache unit tests own auth files and the keyring seam. Never inherit a
// developer token or call the platform credential store from a fixture.
func isolateStatusCacheAuthentication(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", directory)
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_REPO", "GH_HOST"} {
		t.Setenv(name, "")
	}
	previous := statusKeyringGetFunc
	t.Cleanup(func() { statusKeyringGetFunc = previous })
	statusKeyringGetFunc = func(string, string) (string, error) {
		return "", errors.New("unexpected keyring access in isolated cache fixture")
	}
	return directory
}

func TestStatusCacheAuthenticationFixtureIgnoresAmbientCredentials(t *testing.T) {
	tests := []struct{ name, hosts, token string }{
		{name: "missing authentication"},
		{name: "configured developer account", hosts: "github.com:\n  user: developer\n"},
		{name: "environment tokens", hosts: "github.com:\n  user: developer\n", token: "synthetic-ambient-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ambient := t.TempDir()
			t.Setenv("GH_CONFIG_DIR", ambient)
			if test.hosts != "" {
				if err := os.WriteFile(filepath.Join(ambient, "hosts.yml"), []byte(test.hosts), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
				t.Setenv(name, test.token)
			}
			directory := isolateStatusCacheAuthentication(t)
			if directory == ambient {
				t.Fatal("fixture retained developer configuration")
			}
			for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
				if os.Getenv(name) != "" {
					t.Fatalf("fixture inherited %s", name)
				}
			}
			auth, err := statusAuthContext()
			if err != nil || auth != "" {
				t.Fatalf("isolated authentication = %q, %v", auth, err)
			}
			if test.hosts != "" {
				bytes, err := os.ReadFile(filepath.Join(ambient, "hosts.yml"))
				if err != nil || string(bytes) != test.hosts {
					t.Fatalf("ambient config changed: %q, %v", bytes, err)
				}
			}
		})
	}
}

func TestStatusAuthContextConfiguredUnavailableKeyringFailsClosed(t *testing.T) {
	directory := isolateStatusCacheAuthentication(t)
	contents := []byte("github.com:\n  user: fixture-user\n")
	path := filepath.Join(directory, "hosts.yml")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	unavailable := errors.New("synthetic keyring unavailable")
	calls := 0
	statusKeyringGetFunc = func(service, user string) (string, error) {
		calls++
		if service != "gh:github.com" || user != "" {
			t.Fatalf("keyring target = %q/%q", service, user)
		}
		return "", unavailable
	}
	auth, err := statusAuthContext()
	if !errors.Is(err, unavailable) || auth != "" || calls != 1 {
		t.Fatalf("unavailable keyring = %q, %v, calls=%d", auth, err, calls)
	}
	persisted, readErr := os.ReadFile(path)
	if readErr != nil || string(persisted) != string(contents) {
		t.Fatalf("authentication config mutated: %q, %v", persisted, readErr)
	}
}
