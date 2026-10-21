package behavior_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type statusJSONResult struct {
	SchemaVersion int
	Repository    struct{ NameWithOwner, RootPath, DefaultBranch string }
	Health        struct {
		DefaultBranch struct{ Clean, CheckedOut, Synced bool }
		CurrentBranch struct {
			Changes struct {
				Clean     bool
				Untracked int
			}
		}
		Branches struct {
			Clean                   bool
			Local, Remote, Dangling int
		}
		Stashes struct {
			Clean bool
			Count int
		}
		Worktrees struct {
			Items []struct {
				Path                                                                  string
				Current, Primary, Locked, Exists, CleanKnown, Clean, CleanupCandidate bool
			}
		}
	}
	Issues []struct {
		Number int
		URL    string
	}
	PullRequests struct {
		Open, Merged []struct {
			Number                        int
			URL, AIReview, Checks, Issues string
		}
		MergedLimit int
	}
	WorkflowRuns []struct{ ID, URL string }
	Sections     []struct {
		Name, Status, Reason string
		Cached               bool
		CacheAgeSeconds      float64
		FetchedAt            *string
	}
	Error *struct{ Code, Message string }
}

func TestCLIBehaviorStatusJSONEarlyErrors(t *testing.T) {
	for _, args := range [][]string{{"status", "--unknown", "--json"}, {"s", "extra", "--json"}, {"status", "--merged=bad", "--json"}} {
		result := runCLI(t, newFixtureRepository(t), "success", args...)
		document := decodeStatusJSON(t, result)
		if result.exitCode == 0 || document.Error == nil || document.Error.Code != "status_failed" || result.calls != "" {
			t.Fatalf("early argument error: %v %+v %+v", args, result, document)
		}
	}
}

func TestCLIBehaviorStatusJSONGlobalErrors(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--json", "--clear=wrong"}, {"s", "--clear=wrong", "--json"},
		{"--clear=wrong", "status", "--json"}, {"status", "--clear", "--json", "--clear=wrong"},
	} {
		result := runCLI(t, newFixtureRepository(t), "success", args...)
		document := decodeStatusJSON(t, result)
		if result.exitCode == 0 || document.Error == nil || document.Error.Code != "status_failed" || result.calls != "" {
			t.Fatalf("global option error: %v %+v %+v", args, result, document)
		}
	}
}

func TestCLIBehaviorStatusJSONProtectedWorktrees(t *testing.T) {
	repository := newFixtureRepository(t)
	defaultWorktree := filepath.Join(t.TempDir(), "default")
	feature := filepath.Join(t.TempDir(), "feature")
	locked := filepath.Join(t.TempDir(), "locked")
	runGit(t, repository, "switch", "-c", "feature/primary")
	runGit(t, repository, "worktree", "add", defaultWorktree, "main")
	runGit(t, repository, "worktree", "add", "-b", "feature/json", feature, "HEAD")
	runGit(t, repository, "worktree", "add", "-b", "feature/locked", locked, "HEAD")
	runGit(t, repository, "worktree", "lock", locked)
	for _, directory := range []string{repository, defaultWorktree, feature, locked} {
		result := runCLI(t, directory, "success", "status", "--json")
		document := decodeStatusJSON(t, result)
		if result.exitCode != 0 || len(document.Health.Worktrees.Items) != 4 {
			t.Fatalf("protected fixture: %+v %+v", result, document)
		}
		for _, worktree := range document.Health.Worktrees.Items {
			if !worktree.Exists || !worktree.CleanKnown || !worktree.Clean || (worktree.CleanupCandidate && (worktree.Primary || worktree.Current || worktree.Locked || filepath.Clean(worktree.Path) == filepath.Clean(defaultWorktree))) {
				t.Fatalf("protected worktree facts from %s: %+v", directory, worktree)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultWorktree, "dirty.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "dirty.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	document := decodeStatusJSON(t, runCLI(t, feature, "success", "status", "--json"))
	for _, worktree := range document.Health.Worktrees.Items {
		if worktree.Primary || worktree.Locked || filepath.Clean(worktree.Path) == filepath.Clean(defaultWorktree) {
			if !worktree.Exists || !worktree.CleanKnown || worktree.Clean || worktree.CleanupCandidate {
				t.Fatalf("dirty protected worktree: %+v", worktree)
			}
		}
	}
}

func decodeStatusJSON(t *testing.T, result cliResult) statusJSONResult {
	t.Helper()
	if strings.Contains(result.stdout, "\x1b") {
		t.Fatalf("ANSI stdout: %q", result.stdout)
	}
	decoder := json.NewDecoder(strings.NewReader(result.stdout))
	var document statusJSONResult
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("JSON: %v\n%+v", err, result)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("extra stdout: %v", err)
	}
	if document.SchemaVersion != 1 {
		t.Fatalf("schema version = %d", document.SchemaVersion)
	}
	return document
}

func TestCLIBehaviorStatusJSON(t *testing.T) {
	repository := newFixtureRepository(t)
	runGit(t, repository, "branch", "--set-upstream-to=origin/main", "main")
	cold := runCLI(t, repository, "success", "status", "--json", "--refresh")
	document := decodeStatusJSON(t, cold)
	if cold.exitCode != 0 || cold.stderr != "" || document.Repository.NameWithOwner != "HemSoft/gh-x" || filepath.Clean(document.Repository.RootPath) != filepath.Clean(repository) || document.Repository.DefaultBranch != "main" {
		t.Fatalf("identity/exit: %+v, %+v", cold, document.Repository)
	}
	if !document.Health.DefaultBranch.Clean || !document.Health.DefaultBranch.Synced || !document.Health.DefaultBranch.CheckedOut || !document.Health.Branches.Clean || !document.Health.Stashes.Clean || !document.Health.CurrentBranch.Changes.Clean {
		t.Fatalf("clean fixture health: %+v", document.Health)
	}
	if len(document.Issues) != 1 || document.Issues[0].Number != 50 || document.Issues[0].URL == "" || len(document.PullRequests.Open) != 1 || document.PullRequests.Open[0].Number != 42 || document.PullRequests.Open[0].URL == "" || document.PullRequests.Open[0].Checks != "pass" || document.PullRequests.Open[0].Issues != "#50" || len(document.PullRequests.Merged) != 1 || document.PullRequests.Merged[0].Number != 43 || len(document.WorkflowRuns) != 1 || document.WorkflowRuns[0].ID == "" || document.WorkflowRuns[0].URL == "" {
		t.Fatalf("lost enriched rows: %+v", document)
	}
	assertStatusJSONFreshness(t, document, false)
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	warm := runCLI(t, repository, "success", "s", "--json")
	document = decodeStatusJSON(t, warm)
	if warm.exitCode != 0 || warm.calls != "" || document.Health.CurrentBranch.Changes.Clean || document.Health.CurrentBranch.Changes.Untracked != 1 || document.Health.DefaultBranch.Clean {
		t.Fatalf("warm cache/local state: %+v, %+v", warm, document.Health)
	}
	assertStatusJSONFreshness(t, document, true)
	refreshed := runCLI(t, repository, "success", "status", "--json", "--refresh")
	assertStatusJSONFreshness(t, decodeStatusJSON(t, refreshed), false)
	if refreshed.exitCode != 0 || !strings.Contains(refreshed.calls, "issue list") || !strings.Contains(refreshed.calls, "run list") {
		t.Fatalf("refresh failed: %+v", refreshed)
	}
	disabled := runCLI(t, repository, "success", "status", "--json", "--merged=0")
	document = decodeStatusJSON(t, disabled)
	if disabled.exitCode != 0 || document.PullRequests.Merged == nil || len(document.PullRequests.Merged) != 0 || document.PullRequests.MergedLimit != 0 || document.Sections[2].Status != "disabled" || document.Sections[2].Cached || document.Sections[2].FetchedAt != nil || strings.Contains(disabled.calls, "states: MERGED") {
		t.Fatalf("merged 0: %+v, %+v", disabled, document)
	}
}

func assertStatusJSONFreshness(t *testing.T, document statusJSONResult, cached bool) {
	t.Helper()
	if len(document.Sections) != 4 {
		t.Fatalf("section count: %+v", document.Sections)
	}
	for _, section := range document.Sections {
		if section.Status != "available" || section.Reason != "" || section.Cached != cached || section.FetchedAt == nil || section.CacheAgeSeconds < 0 {
			t.Fatalf("section freshness: %+v, want cached %v", section, cached)
		}
		if !cached && section.CacheAgeSeconds != 0 {
			t.Fatalf("live section cache age: %+v", section)
		}
	}
}

func TestCLIBehaviorStatusJSONFailure(t *testing.T) {
	result := runCLI(t, newFixtureRepository(t), "issue-list-error", "status", "--json", "--refresh")
	document := decodeStatusJSON(t, result)
	if result.exitCode != 0 || document.Sections[0].Status != "unavailable" || !strings.Contains(document.Sections[0].Reason, "fixture issue list failed") || document.Health.Branches.Local != 1 || len(document.PullRequests.Open) != 1 || document.Sections[1].Status != "available" {
		t.Fatalf("section failure lost local/other data: %+v, %+v", result, document)
	}
	result = runCLI(t, t.TempDir(), "success", "status", "--json")
	document = decodeStatusJSON(t, result)
	if result.exitCode == 0 || document.Error == nil || document.Error.Code != "status_failed" || !strings.Contains(document.Error.Message, "git status") || result.calls != "" {
		t.Fatalf("fatal failure: %+v, %+v", result, document)
	}
}

func TestCLIBehaviorReleaseStatusJSON(t *testing.T) {
	saved := ghXBinaryPath
	ghXBinaryPath = ghXReleaseBinaryPath
	t.Cleanup(func() { ghXBinaryPath = saved })
	result := runCLI(t, newFixtureRepository(t), "success", "--clear", "status", "--json")
	decodeStatusJSON(t, result)
	if result.exitCode != 0 || result.stderr != "" || strings.Contains(result.calls, "releases/latest") {
		t.Fatalf("JSON must skip banners and update checks: %+v", result)
	}
}
