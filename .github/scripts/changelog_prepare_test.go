package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPrepareChangelogPR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Auto Release runs this Bash helper on Linux")
	}
	helper, err := filepath.Abs("prepare-changelog-pr.sh")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, mode, wantErr string
		reuse, published    bool
	}{
		{name: "allowed draft creation", mode: "allowed"},
		{name: "denied creation", mode: "denied", wantErr: "gh pr create denied or unavailable"},
		{name: "unavailable lookup", mode: "unavailable", wantErr: "cannot list candidate PRs"},
		{name: "malformed lookup", mode: "malformed-list", wantErr: "invalid candidate PR lookup"},
		{name: "ambiguous lookup", mode: "ambiguous", wantErr: "invalid candidate PR lookup"},
		{name: "malformed creation", mode: "malformed-create", wantErr: "invalid created PR URL"},
		{name: "malformed details", mode: "malformed-pr", wantErr: "candidate PR identity"},
		{name: "unavailable details", mode: "unavailable-pr", wantErr: "cannot verify candidate PR"},
		{name: "safe existing draft", mode: "allowed", reuse: true},
		{name: "published existing ready PR", mode: "ready", reuse: true, published: true},
		{name: "unpublished ready PR", mode: "ready", reuse: true, wantErr: "candidate PR identity"},
		{name: "different head", mode: "wrong-head", reuse: true, wantErr: "candidate PR identity"},
		{name: "different author", mode: "wrong-author", reuse: true, wantErr: "candidate PR identity"},
		{name: "different scope", mode: "wrong-files", reuse: true, wantErr: "may only modify CHANGELOG.md"},
		{name: "unavailable scope", mode: "unavailable-files", reuse: true, wantErr: "cannot verify candidate files"},
		{name: "conflicting retained notes", mode: "conflict", reuse: true, wantErr: "retained candidate differs"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv("GIT_AUTHOR_NAME", "Release Fixture")
			t.Setenv("GIT_COMMITTER_NAME", "Release Fixture")
			t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.invalid")
			t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.invalid")
			changelogFixtureGit(t, "init", "--bare", "remote")
			changelogFixtureGit(t, "init", "-b", "main", "checkout")
			t.Chdir(filepath.Join(root, "checkout"))
			changelogFixtureGit(t, "remote", "add", "origin", filepath.Join(root, "remote"))
			writeChangelogFixture(t, "CHANGELOG.md", "old notes\n", 0600)
			changelogFixtureGit(t, "add", "CHANGELOG.md")
			changelogFixtureGit(t, "commit", "-m", "initial")
			changelogFixtureGit(t, "push", "origin", "main")
			base := changelogFixtureGit(t, "rev-parse", "HEAD")
			retained := ""
			if test.reuse {
				changelogFixtureGit(t, "switch", "-c", "chore/changelog-1.2.3")
				notes := "planned notes\n"
				if test.mode == "conflict" {
					notes = "retained different notes\n"
				}
				writeChangelogFixture(t, "CHANGELOG.md", notes, 0600)
				changelogFixtureGit(t, "commit", "-am", "candidate")
				changelogFixtureGit(t, "push", "origin", "HEAD")
				retained = changelogFixtureGit(t, "rev-parse", "HEAD")
				changelogFixtureGit(t, "switch", "--detach", "origin/main")
			}
			writeChangelogFixture(t, "CHANGELOG.md", "planned notes\n", 0600)
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			writeChangelogFixture(t, filepath.Join(bin, "gh"), changelogGHFixture, 0700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GH_TOKEN", "release-fixture")
			t.Setenv("CODEX_REVIEW_TOKEN", "must-not-be-used")
			t.Setenv("GITHUB_REPOSITORY", "hemsoft-dev/gh-x")
			t.Setenv("RELEASE_TAG", "v1.2.3")
			t.Setenv("RELEASE_PUBLISHED", fmt.Sprint(test.published))
			outputs, log := filepath.Join(root, "outputs"), filepath.Join(root, "calls")
			t.Setenv("GITHUB_OUTPUT", outputs)
			t.Setenv("FIXTURE_LOG", log)
			t.Setenv("FIXTURE_MODE", test.mode)
			t.Setenv("FIXTURE_REUSE", fmt.Sprint(test.reuse))
			t.Setenv("FIXTURE_SOURCE", base)
			// The publication sentinel runs only if the real preparation helper succeeds.
			command := exec.Command("bash", "-euc", `bash "$1"; git switch --detach "$FIXTURE_SOURCE"; git diff --exit-code HEAD; echo publication >> "$FIXTURE_LOG"`, "fixture", helper)
			output, runErr := command.CombinedOutput()
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" {
				if runErr == nil || !strings.Contains(string(output), test.wantErr) || strings.Contains(string(calls), "\npublication\n") || !strings.Contains(string(output), "An authorized owner must resolve") {
					t.Fatalf("denial did not stop publication: %v\n%s\ncalls: %s", runErr, output, calls)
				}
				if _, err := os.Stat(outputs); !os.IsNotExist(err) {
					t.Fatalf("failed preparation emitted successful outputs: %v", err)
				}
			} else {
				if runErr != nil {
					t.Fatalf("prepare: %v\n%s", runErr, output)
				}
				result, err := os.ReadFile(outputs)
				if err != nil {
					t.Fatal(err)
				}
				head := changelogFixtureGit(t, "rev-parse", "origin/chore/changelog-1.2.3")
				if !strings.Contains(string(result), "head_sha="+head+"\n") || !strings.Contains(string(result), "pr_url=https://github.com/hemsoft-dev/gh-x/pull/12\n") {
					t.Fatalf("outputs do not bind the actual candidate: %s", result)
				}
				if !test.reuse && !strings.Contains(string(calls), "--draft") {
					t.Fatal("new candidate was not created as a draft")
				}
			}
			if test.reuse && (strings.Contains(string(calls), "pr create") || retained != changelogFixtureGit(t, "rev-parse", "origin/chore/changelog-1.2.3")) {
				t.Fatal("reuse replaced the PR or its reviewed head")
			}
			if base != changelogFixtureGit(t, "rev-parse", "origin/main") {
				t.Fatal("preparation changed main")
			}
			if test.mode == "denied" && !strings.Contains(string(output), "GitHub Actions is not permitted to create or approve pull requests") {
				t.Fatal("creation denial lost the original GitHub diagnostic")
			}
		})
	}
}

func changelogFixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeChangelogFixture(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareChangelogPRWithoutChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Auto Release runs this Bash helper on Linux")
	}
	helper, err := filepath.Abs("prepare-changelog-pr.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		published bool
	}{
		{name: "published reconciliation is already complete", published: true},
		{name: "unpublished release still requires a candidate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			changelogFixtureGit(t, "init", "-b", "main")
			writeChangelogFixture(t, "CHANGELOG.md", "already recorded\n", 0600)
			changelogFixtureGit(t, "add", "CHANGELOG.md")
			changelogFixtureGit(t, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "recorded")
			t.Setenv("RELEASE_TAG", "v1.2.3")
			t.Setenv("GITHUB_REPOSITORY", "hemsoft-dev/gh-x")
			t.Setenv("RELEASE_PUBLISHED", fmt.Sprint(test.published))
			output, err := exec.Command("bash", helper).CombinedOutput()
			if (err == nil) != test.published {
				t.Fatalf("no-change preparation: %v: %s", err, output)
			}
			if !test.published && !strings.Contains(string(output), "unpublished candidate has no changelog change") {
				t.Fatalf("unexpected denial: %s", output)
			}
		})
	}
}

const changelogGHFixture = `#!/usr/bin/env bash
set -euo pipefail
[[ "$GH_TOKEN" == release-fixture ]] || exit 88
echo "$*" >> "$FIXTURE_LOG"
case "$1 $2" in
  'pr list')
    case "$FIXTURE_MODE" in
      unavailable) echo 'service unavailable' >&2; exit 1;;
      malformed-list) echo null;;
      ambiguous) echo '[{"number":12},{"number":13}]';;
      *) if [[ "$FIXTURE_REUSE" == true ]]; then echo '[{"number":12}]'; else echo '[]'; fi;;
    esac;;
  'pr create')
    case "$FIXTURE_MODE" in
      denied) echo 'GraphQL: GitHub Actions is not permitted to create or approve pull requests (createPullRequest)' >&2; exit 1;;
      malformed-create) echo 'not a PR URL';;
      *) echo 'https://github.com/hemsoft-dev/gh-x/pull/12';;
    esac;;
  'api repos/hemsoft-dev/gh-x/pulls/12')
    [[ "$FIXTURE_MODE" != unavailable-pr ]] || exit 1
    if [[ "$FIXTURE_MODE" == malformed-pr ]]; then echo '{'; exit 0; fi
    head=$(git rev-parse origin/chore/changelog-1.2.3)
    author='github-actions[bot]'; draft=true
    [[ "$FIXTURE_MODE" != wrong-head ]] || head=bad
    [[ "$FIXTURE_MODE" != wrong-author ]] || author=other
    [[ "$FIXTURE_MODE" != ready ]] || draft=false
    jq -n --arg head "$head" --arg author "$author" --argjson draft "$draft" '{state:"open",merged:false,draft:$draft,user:{login:$author,type:"Bot"},base:{ref:"main",repo:{full_name:"hemsoft-dev/gh-x"}},head:{ref:"chore/changelog-1.2.3",sha:$head,repo:{full_name:"hemsoft-dev/gh-x"}},changed_files:1}';;
  'api repos/hemsoft-dev/gh-x/pulls/12/files?per_page=100')
    [[ "$FIXTURE_MODE" != unavailable-files ]] || exit 1
    if [[ "$FIXTURE_MODE" == wrong-files ]]; then echo '[{"filename":"src/main.go","status":"modified"}]'; else echo '[{"filename":"CHANGELOG.md","status":"modified"}]'; fi;;
  *) echo "unexpected gh call $*" >&2; exit 99;;
esac
`

func TestChangelogPreparationPrecedesPublication(t *testing.T) {
	data, err := os.ReadFile("../workflows/auto-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var release struct {
		Jobs map[string]struct {
			Steps []struct {
				Name, ID, Run, If string
				ContinueOnError   bool `yaml:"continue-on-error"`
				Env               map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &release); err != nil {
		t.Fatal(err)
	}
	prepared, published := false, false
	for _, step := range release.Jobs["release"].Steps {
		if step.ID == "changelog_pr" {
			if step.ContinueOnError || !strings.Contains(step.Run, `bash "$RUNNER_TEMP/prepare-changelog-pr.sh"`) || !strings.Contains(step.Run, `git switch --detach "$RELEASE_SHA"`) {
				t.Fatal("candidate preparation must fail closed and restore the validated release source")
			}
			if step.Env["GH_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" || step.Env["CODEX_REVIEW_TOKEN"] != "" {
				t.Fatal("candidate creation must retain the job token identity")
			}
			prepared = true
		}
		if step.Name == "Attest release binaries" || step.Name == "Create release" {
			if !prepared || step.ContinueOnError || strings.Contains(step.If, "always()") {
				t.Fatalf("%s can publish before successful changelog preparation", step.Name)
			}
			published = true
		}
		if strings.Contains(step.Run, "changelog-merge request") {
			if !published || !strings.Contains(step.Run, `gh pr ready "$pr_url"`) || step.Env["EXPECTED_HEAD"] != "${{ steps.changelog_pr.outputs.head_sha }}" {
				t.Fatal("review must follow publication and retain the prepared head")
			}
		}
	}
	if !prepared || !published {
		t.Fatal("release must prepare a real candidate before publishing")
	}
}
