package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Verbatim Git 2.53.0 output from a disposable clone whose origin/HEAD is
// main, after `git tag main` and `git tag release` (issue #276). Git
// shortens both shadowed branches to heads/<name>.
const statusTagShadowBranchFixture = "refs/heads/main\theads/main\torigin/main\t[behind 1]\t\n" +
	"refs/heads/release\theads/release\torigin/release\t\t\n" +
	"refs/remotes/origin/HEAD\torigin\t\t\trefs/remotes/origin/main\n" +
	"refs/remotes/origin/main\torigin/main\t\t\t\n" +
	"refs/remotes/origin/release\torigin/release\t\t\t\n"

const statusTagShadowWorktreeFixture = "worktree /tmp/ghx276/clone\x00HEAD 313cbaa1e29c774a216db5f8c85322459ccf34cb\x00branch refs/heads/main\x00\x00" +
	"worktree /tmp/ghx276/wt-release\x00HEAD 19776e66b4f0fe9e27223ae5685197253ed10758\x00branch refs/heads/release\x00\x00"

const statusTagShadowCurrentFixture = "# branch.oid 313cbaa1e29c774a216db5f8c85322459ccf34cb\n" +
	"# branch.head main\n" +
	"# branch.upstream origin/main\n" +
	"# branch.ab +0 -1\n"

func TestParseStatusBranchRefsKeysTagShadowedBranchesByName(t *testing.T) {
	inventory := parseStatusBranchRefs(statusTagShadowBranchFixture)
	attachStatusWorktreePaths(&inventory, parseStatusWorktrees(statusTagShadowWorktreeFixture))

	if inventory.LocalCount != 2 || inventory.RemoteCount != 2 || inventory.DanglingCount != 0 {
		t.Fatalf("counts = local %d remote %d dangling %d, want 2 2 0", inventory.LocalCount, inventory.RemoteCount, inventory.DanglingCount)
	}
	if got := sortedStatusKeys(inventory.Local); !reflect.DeepEqual(got, []string{"main", "release"}) {
		t.Fatalf("local keys = %v, want [main release]", got)
	}
	if got := inventory.Local["main"]; got.WorktreePath != "/tmp/ghx276/clone" || got.Upstream != "origin/main" || got.Track != "[behind 1]" {
		t.Fatalf("main = %+v", got)
	}
	if got := inventory.Local["release"].WorktreePath; got != "/tmp/ghx276/wt-release" {
		t.Fatalf("release worktree = %q", got)
	}
	if got := resolveStatusDefaultBranch(inventory); got != "main" {
		t.Fatalf("default branch = %q, want main", got)
	}

	dashboard := statusDashboard{DefaultBranch: "main", CurrentStatus: parseGitStatus(statusTagShadowCurrentFixture)}
	populateStatusDefaultBranch(&dashboard, inventory, "/tmp/ghx276/clone")
	if dashboard.DefaultStatusErr != nil || !dashboard.DefaultCheckedOut {
		t.Fatalf("default branch: checked out %v, err %v", dashboard.DefaultCheckedOut, dashboard.DefaultStatusErr)
	}
	if dashboard.DefaultStatus.Behind != 1 || dashboard.DefaultStatus.Branch != "main" {
		t.Fatalf("default status = %+v", dashboard.DefaultStatus)
	}
}

func TestFetchMergedStatusBranchesUsesFullRefNames(t *testing.T) {
	defer saveStatusFuncs()()
	statusCommandFunc = func(name string, args ...string) (string, error) {
		if key := name + " " + strings.Join(args, " "); key != "git for-each-ref --merged=refs/heads/main --format=%(refname) refs/heads" {
			t.Fatalf("unexpected command: %s", key)
		}
		return "refs/heads/main\nrefs/heads/release\nrefs/heads/heads/odd\n", nil
	}
	merged, known := fetchMergedStatusBranches("main")
	want := map[string]bool{"main": true, "release": true, "heads/odd": true}
	if !known || !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged = %v known %v, want %v", merged, known, want)
	}
}

// statusTagShadowSnapshot is everything status derives from local refs that
// a same-named tag must not change.
type statusTagShadowSnapshot struct {
	LocalKeys                          []string
	LocalCount, RemoteCount, Dangling  int
	DefaultBranch                      string
	DefaultCheckedOut                  bool
	DefaultStatus                      statusSummary
	DefaultErr                         string
	Merged                             map[string]bool
	WorktreeBranches, WorktreeCleanups []string
	BranchWorktreeMatches              []bool
	BranchRemote                       string
}

func TestStatusTagShadowedBranchesMatchUntaggedRepository(t *testing.T) {
	repo := newStatusTagShadowRepository(t)

	checkedOut := repo.snapshot(t)
	if !checkedOut.DefaultCheckedOut || checkedOut.DefaultErr != "" || checkedOut.DefaultStatus.Behind != 1 {
		t.Fatalf("untagged baseline is not the expected fixture: %+v", checkedOut)
	}
	if !reflect.DeepEqual(checkedOut.WorktreeCleanups, []string{"", "clean, merged branch with no open PR"}) {
		t.Fatalf("untagged baseline cleanup = %v", checkedOut.WorktreeCleanups)
	}
	repo.git(t, "checkout", "-q", "--detach")
	notCheckedOut := repo.snapshot(t)
	if notCheckedOut.DefaultCheckedOut || notCheckedOut.DefaultErr != "" || notCheckedOut.DefaultStatus.Ahead != 0 || notCheckedOut.DefaultStatus.Behind != 1 {
		t.Fatalf("untagged detached baseline = %+v", notCheckedOut)
	}
	repo.git(t, "checkout", "-q", "main")

	// Point each tag at an unrelated root commit so resolving the tag
	// instead of the branch would change ahead/behind and merged state.
	orphan := strings.TrimSpace(repo.git(t, "commit-tree", "-m", "orphan", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"))
	repo.git(t, "tag", "main", orphan)
	repo.git(t, "tag", "done", orphan)
	if short := repo.git(t, "for-each-ref", "--format=%(refname:short)", "refs/heads/main"); strings.TrimSpace(short) != "heads/main" {
		t.Fatalf("fixture tag does not shadow main: short name %q", short)
	}

	if got := repo.snapshot(t); !reflect.DeepEqual(got, checkedOut) {
		t.Fatalf("checked-out default with tags:\n got %+v\nwant %+v", got, checkedOut)
	}
	repo.git(t, "checkout", "-q", "--detach")
	if got := repo.snapshot(t); !reflect.DeepEqual(got, notCheckedOut) {
		t.Fatalf("detached default with tags:\n got %+v\nwant %+v", got, notCheckedOut)
	}
}

type statusTagShadowRepository struct {
	root   string
	linked string
}

func newStatusTagShadowRepository(t *testing.T) statusTagShadowRepository {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := statusTagShadowRepository{root: t.TempDir(), linked: filepath.Join(t.TempDir(), "done")}
	repo.git(t, "init", "-q", "-b", "main")
	repo.git(t, "config", "user.name", "Status Fixture")
	repo.git(t, "config", "user.email", "fixture@example.invalid")
	repo.git(t, "config", "commit.gpgsign", "false")
	repo.git(t, "commit", "-q", "--allow-empty", "-m", "one")
	repo.git(t, "commit", "-q", "--allow-empty", "-m", "two")
	// The remote is never contacted; its fetch refspec maps the upstream.
	repo.git(t, "remote", "add", "origin", "https://example.invalid/owner/repo.git")
	repo.git(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	repo.git(t, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	repo.git(t, "reset", "-q", "--hard", "HEAD~1")
	repo.git(t, "config", "branch.main.remote", "origin")
	repo.git(t, "config", "branch.main.merge", "refs/heads/main")
	repo.git(t, "worktree", "add", "-q", "-b", "done", repo.linked, "HEAD")
	return repo
}

func (repo statusTagShadowRepository) git(t *testing.T, args ...string) string {
	t.Helper()
	output, err := runStatusCommand("git", append([]string{"-C", repo.root}, args...)...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return output
}

// snapshot runs status's local-ref steps against the real repository.
func (repo statusTagShadowRepository) snapshot(t *testing.T) statusTagShadowSnapshot {
	t.Helper()
	defer saveStatusFuncs()()
	statusCommandFunc = func(name string, args ...string) (string, error) {
		if len(args) == 0 || args[0] != "-C" {
			args = append([]string{"-C", repo.root}, args...)
		}
		return runStatusCommand(name, args...)
	}
	statusPathExistsFunc = statusPathExists

	branches := parseStatusBranchRefs(repo.git(t, "for-each-ref", "--format="+statusBranchFormat, "refs/heads", "refs/remotes"))
	worktrees := parseStatusWorktrees(repo.git(t, "worktree", "list", "--porcelain", "-z"))
	attachStatusWorktreePaths(&branches, worktrees)
	root := strings.TrimRight(repo.git(t, "rev-parse", "--show-toplevel"), "\r\n")
	dashboard := statusDashboard{
		DefaultBranch: resolveStatusDefaultBranch(branches),
		CurrentStatus: parseGitStatus(repo.git(t, "status", "--porcelain=v2", "--branch")),
	}
	populateStatusDefaultBranch(&dashboard, branches, root)
	merged, mergedKnown := fetchMergedStatusBranches(dashboard.DefaultBranch)
	if !mergedKnown {
		t.Fatal("merged branches unavailable")
	}
	assessed := assessStatusWorktrees(worktrees, root, dashboard.DefaultBranch, merged, map[string]bool{}, mergedKnown, true)
	branchRemote, err := statusBranchRemote()
	if err != nil {
		t.Fatalf("branch remote: %v", err)
	}

	snapshot := statusTagShadowSnapshot{
		LocalKeys:     sortedStatusKeys(branches.Local),
		LocalCount:    branches.LocalCount,
		RemoteCount:   branches.RemoteCount,
		Dangling:      branches.DanglingCount,
		DefaultBranch: dashboard.DefaultBranch,
		DefaultStatus: dashboard.DefaultStatus,
		Merged:        merged,
		BranchRemote:  branchRemote,
	}
	snapshot.DefaultCheckedOut = dashboard.DefaultCheckedOut
	if dashboard.DefaultStatusErr != nil {
		snapshot.DefaultErr = dashboard.DefaultStatusErr.Error()
	}
	for _, worktree := range assessed {
		snapshot.WorktreeBranches = append(snapshot.WorktreeBranches, worktree.Branch)
		snapshot.WorktreeCleanups = append(snapshot.WorktreeCleanups, worktree.CleanupReason)
		if worktree.Branch != "" {
			snapshot.BranchWorktreeMatches = append(snapshot.BranchWorktreeMatches, sameStatusPath(branches.Local[worktree.Branch].WorktreePath, worktree.Path))
		}
	}
	return snapshot
}

func sortedStatusKeys(refs map[string]statusBranchRef) []string {
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
