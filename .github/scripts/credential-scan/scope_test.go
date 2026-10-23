package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := gitOutput(context.Background(), root, args...)
	if err != nil {
		t.Fatalf("fixture git %s failed", args[0])
	}
	return strings.TrimSpace(string(out))
}

func fixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fixtureGit(t, root, "init", "-b", "main")
	fixtureGit(t, root, "config", "user.email", "fixture@example.invalid")
	fixtureGit(t, root, "config", "user.name", "Fixture")
	writeFixture(t, filepath.Join(root, "README.md"), "fixture\n")
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "commit", "-m", "fixture")
	remote := t.TempDir()
	fixtureGit(t, remote, "init", "--bare")
	fixtureGit(t, root, "remote", "add", "origin", remote)
	fixtureGit(t, root, "push", "-u", "origin", "main")
	return root
}

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryScopeRequiresAllOriginRefs(t *testing.T) {
	for _, test := range []struct {
		name, local, remote string
		wantErr             bool
	}{
		{"complete", "abc refs/remotes/origin/main\ndef refs/tags/v1", "abc refs/heads/main\ndef refs/tags/v1\nxyz refs/tags/v1^{}", false},
		{"missing branch", "abc refs/remotes/origin/main", "abc refs/heads/main\ndef refs/heads/other", true},
		{"changed branch", "abc refs/remotes/origin/main", "def refs/heads/main", true},
		{"missing tag", "abc refs/remotes/origin/main", "abc refs/heads/main\ndef refs/tags/v1", true},
		{"no origin inventory", "abc refs/heads/main", "", true},
		{"bad local inventory", "invalid", "abc refs/heads/main", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote, local, err := verifyOriginRefs(test.local, test.remote)
			if (err != nil) != test.wantErr {
				t.Fatalf("scope result %d/%d, error %v", remote, local, err)
			}
		})
	}
}

func TestHistoryScopeRejectsShallowAndMissingRefs(t *testing.T) {
	root := fixtureRepo(t)
	summary := scanSummary{}
	if err := historyScope(context.Background(), root, &summary); err != nil || summary.OriginRefs != 1 || summary.HistoryCommits != 1 {
		t.Fatalf("scope %+v, error %v", summary, err)
	}
	fixtureGit(t, root, "update-ref", "-d", "refs/remotes/origin/main")
	if err := historyScope(context.Background(), root, &summary); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing origin ref accepted: %v", err)
	}
	writeFixture(t, filepath.Join(root, ".git", "shallow"), fixtureGit(t, root, "rev-parse", "HEAD")+"\n")
	if err := historyScope(context.Background(), root, &summary); err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("shallow history accepted: %v", err)
	}
}

func TestSourceSnapshotIncludesUntrackedAndDoesNotFollowLinks(t *testing.T) {
	root := fixtureRepo(t)
	writeFixture(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	writeFixture(t, filepath.Join(root, "ignored.txt"), "excluded fixture")
	writeFixture(t, filepath.Join(root, "untracked.txt"), "current source")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFixture(t, outside, "outside content must stay outside")
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symbolic link fixture unavailable on this host")
	}
	target := filepath.Join(t.TempDir(), "source")
	count, err := snapshotSource(context.Background(), root, target)
	if err != nil || count != 4 {
		t.Fatalf("snapshot files=%d error=%v", count, err)
	}
	raw, err := os.ReadFile(filepath.Join(target, "link.txt"))
	if err != nil || string(raw) != outside {
		t.Fatal("snapshot followed an external symlink")
	}
	if _, err := os.Stat(filepath.Join(target, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatal("ignored source included")
	}
	if _, err := os.Stat(filepath.Join(target, "untracked.txt")); err != nil {
		t.Fatal("nonignored untracked source missing")
	}
}

func TestSnapshotRejectsUnsupportedAndEscapingInputs(t *testing.T) {
	root := fixtureRepo(t)
	source, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			t.Error("fixture root close failed")
		}
	}()
	target := t.TempDir()
	for _, name := range []string{"../escape", ".git"} {
		if _, err := snapshotFile(source, target, name); err == nil {
			t.Fatal("unsafe snapshot input accepted")
		}
	}
	if copied, err := snapshotFile(source, target, "deleted.txt"); err != nil || copied {
		t.Fatal("deleted source should have no snapshot bytes")
	}
	writeFixture(t, filepath.Join(target, "README.md"), "existing")
	if err := copySnapshotFile(source, "README.md", filepath.Join(target, "README.md")); err == nil {
		t.Fatal("snapshot overwrote an existing output")
	}
}
