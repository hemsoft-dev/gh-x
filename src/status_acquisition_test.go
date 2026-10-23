package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type statusAcquisitionSample struct {
	FirstContent float64 `json:"first_content_ms"`
	Mode         string  `json:"mode"`
	Milliseconds float64 `json:"ms"`
	Git          int     `json:"git"`
	GH           int     `json:"gh"`
	Keyring      int     `json:"keyring"`
	Cache        string  `json:"cache"`
}

// Opt-in because the fixed external-service latency is inappropriate for
// mutation runs. benchmarks/run.mjs executes this fixture as a required gate.
func TestStatusAcquisitionPerformance(t *testing.T) {
	if os.Getenv("GH_X_STATUS_PERF") != "1" {
		t.Skip("performance runner owns the acquisition fixture")
	}
	defer saveStatusFuncs()()
	savedProgress := statusProgressFunc
	t.Cleanup(func() { statusProgressFunc = savedProgress })
	statusProgressFunc = func(output io.Writer, color bool) *statusProgress {
		return &statusProgress{output: output, color: color, width: 120, height: 40}
	}
	savedGH, savedVersion := ghTransportFunc, version
	t.Cleanup(func() { ghTransportFunc, version = savedGH, savedVersion })
	version = "v0.19.4"
	directory := t.TempDir()
	configuration := t.TempDir()
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(name, "")
	}
	t.Setenv("GH_REPO", "HemSoft/gh-x")
	t.Setenv("GH_CONFIG_DIR", configuration)
	updateDirectory := t.TempDir()
	t.Setenv("GH_X_CACHE_DIR", updateDirectory)
	t.Setenv("GH_X_STATUS_DEBUG", "1")
	if err := os.WriteFile(filepath.Join(configuration, "hosts.yml"), []byte("github.com:\n  user: fixture\n  oauth_token: synthetic-token\n  users:\n    fixture: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = directory
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Performance Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	git("remote", "add", "origin", "https://github.com/HemSoft/gh-x.git")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	linked := filepath.Join(t.TempDir(), "linked")
	git("worktree", "add", "-b", "fixture", linked, "HEAD")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	})
	fixtureDirectory := filepath.Join(cwd, "..", "tests", "behavior", "testdata")
	var mu sync.Mutex
	calls := statusAcquisitionSample{}
	broken := false
	statusKeyringGetFunc = func(string, string) (string, error) {
		mu.Lock()
		calls.Keyring++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return "", keyring.ErrNotFound
	}
	statusCommandFunc = func(name string, args ...string) (string, error) {
		mu.Lock()
		calls.Git++
		mu.Unlock()
		return runStatusCommand(name, args...)
	}
	ghTransportFunc = func(inv ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
		mu.Lock()
		calls.GH++
		failRuns := broken
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		if failRuns && len(inv.Args) > 0 && inv.Args[0] == "run" {
			return bytes.Buffer{}, bytes.Buffer{}, errors.New("fixture workflow unavailable")
		}
		fixture := acquisitionFixture(inv.Args)
		if fixture == "release" {
			return *bytes.NewBufferString("v0.19.4"), bytes.Buffer{}, nil
		}
		if fixture == "" {
			return bytes.Buffer{}, bytes.Buffer{}, fmt.Errorf("unsupported fixture command: %s", strings.Join(inv.Args, " "))
		}
		data, err := os.ReadFile(filepath.Join(fixtureDirectory, fixture))
		return *bytes.NewBuffer(data), bytes.Buffer{}, err
	}
	statusCacheDirectoryFunc = statusCacheDirectory
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	statusNowFunc = func() time.Time { return now }
	execute := func(refresh bool) string {
		t.Helper()
		args := []string{"status"}
		if refresh {
			args = append(args, "--refresh")
		}
		var stderr bytes.Buffer
		output := &statusAcquisitionWriter{started: time.Now()}
		ch, err := run(args, output, &stderr)
		showUpdateNotice(io.Discard, ch, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if output.first <= 0 {
			t.Fatal("status did not emit meaningful content")
		}
		mu.Lock()
		calls.FirstContent = float64(output.first.Nanoseconds()) / 1e6
		mu.Unlock()
		for _, line := range strings.Split(stderr.String(), "\n") {
			if value, ok := strings.CutPrefix(line, "[gh-x] status cache: "); ok {
				return value
			}
		}
		return "unavailable"
	}
	samples := []statusAcquisitionSample{}
	for _, mode := range []string{"cold", "warm", "expired", "refresh", "partial"} {
		for range 7 {
			mu.Lock()
			broken = mode == "partial"
			mu.Unlock()
			if mode != "cold" {
				execute(true)
			} else {
				// These directories belong solely to this synthetic repository.
				for _, path := range []string{filepath.Join(directory, ".git", "gh-x"), updateDirectory} {
					if err := os.RemoveAll(path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "expired" {
				now = now.Add(61 * time.Second)
			}
			mu.Lock()
			calls = statusAcquisitionSample{Mode: mode}
			mu.Unlock()
			started := time.Now()
			cache := execute(mode == "refresh")
			elapsed := time.Since(started)
			mu.Lock()
			calls.Milliseconds = float64(elapsed.Nanoseconds()) / 1e6
			calls.Cache = cache
			samples = append(samples, calls)
			mu.Unlock()
		}
	}
	data, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "STATUS_ACQUISITION=%s\n", data)
}

type statusAcquisitionWriter struct {
	started time.Time
	first   time.Duration
}

func (w *statusAcquisitionWriter) Write(data []byte) (int, error) {
	// Progress serializes writes and joins its animator before run returns.
	if w.first == 0 && bytes.Contains(data, []byte("Repository")) {
		w.first = time.Since(w.started)
	}
	return len(data), nil
}

func acquisitionFixture(args []string) string {
	value := strings.Join(args, " ")
	switch {
	case strings.Contains(value, "releases/latest"):
		return "release"
	case strings.HasPrefix(value, "issue list"):
		return "issue-list.json"
	case strings.HasPrefix(value, "pr list"):
		return "pr-list.json"
	case strings.HasPrefix(value, "run list"):
		return "workflow-runs.json"
	case strings.Contains(value, "rules/branches/"):
		return "required-checks.json"
	case strings.Contains(value, "closedByPullRequestsReferences") && strings.Contains(value, "subIssuesSummary"):
		return "issue-status-enrichment.json"
	case strings.Contains(value, "closedByPullRequestsReferences"):
		return "issue-relationships.json"
	case strings.Contains(value, "subIssuesSummary"):
		return "issue-hierarchy.json"
	case strings.Contains(value, "pullRequests(states: MERGED"):
		return "merged-prs.json"
	case strings.Contains(value, "pullRequest(number: 42)"):
		return "pr-supplemental.json"
	case strings.Contains(value, "pullRequest(number: 43)") && strings.Contains(value, "latestReviews"):
		return "merged-pr-selected.json"
	case strings.Contains(value, "pullRequest(number: 43)"):
		return "merged-pr-supplemental.json"
	default:
		return ""
	}
}
