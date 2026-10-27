package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseRetryPreservesUnchangedHead(t *testing.T) {
	data, err := os.ReadFile("../prepare-changelog-pr.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(data), "remote_sha=$(git ls-remote")
	end := strings.Index(string(data), `if [[ "$number" == new ]]; then`)
	if start < 0 || end <= start {
		t.Fatal("missing changelog branch preparation block")
	}
	script := `blocked() { echo "$*" >&2; exit 1; }; branch="chore/changelog-${RELEASE_TAG#v}"; number=new` + "\n" + string(data)[start:end]

	dir := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit("init", "--bare", "remote.git")
	runGit("init", "-b", "main")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.invalid")
	runGit("remote", "add", "origin", "./remote.git")
	write("CHANGELOG.md", "old\n")
	runGit("add", "CHANGELOG.md")
	runGit("commit", "-m", "base")
	runGit("push", "origin", "HEAD:refs/heads/main")
	base := runGit("rev-parse", "HEAD")
	bash := "bash"
	if runtime.GOOS == "windows" {
		bash = "C:/Program Files/Git/bin/bash.exe"
	}
	publish := func(notes, date string, wantFailure bool) string {
		t.Helper()
		runGit("checkout", "--detach", "-f", base)
		write("CHANGELOG.md", notes)
		cmd := exec.Command(bash, "-eu", "-o", "pipefail", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "RELEASE_TAG=v1.2.3", "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); (err != nil) != wantFailure {
			t.Fatalf("publish: %v\n%s", err, out)
		}
		head := strings.Fields(runGit("ls-remote", "origin", "refs/heads/chore/changelog-1.2.3"))[0]
		runGit("checkout", "--detach", "-f", base)
		if runGit("branch", "--list", "chore/changelog-1.2.3") != "" {
			runGit("branch", "-D", "chore/changelog-1.2.3")
		}
		return head
	}
	first := publish("released\n", "2026-09-05T12:00:00Z", false)
	if retry := publish("released\n", "2026-09-05T13:00:00Z", false); retry != first {
		t.Fatal("unchanged retry replaced the reviewed head and reset its request deadline")
	}
	// Main advancing must not recreate an otherwise unchanged changelog PR.
	runGit("checkout", "--detach", "-f", base)
	write("product.txt", "new main content\n")
	runGit("add", "product.txt")
	runGit("commit", "-m", "advance main")
	runGit("push", "origin", "HEAD:refs/heads/main")
	base = runGit("rev-parse", "HEAD")
	if retry := publish("released\n", "2026-09-05T14:00:00Z", false); retry != first {
		t.Fatal("main advancing invalidated unchanged changelog review evidence")
	}
	if changed := publish("corrected release\n", "2026-09-05T15:00:00Z", true); changed != first {
		t.Fatal("conflicting notes must fail closed and retain the reviewed head")
	}
}
