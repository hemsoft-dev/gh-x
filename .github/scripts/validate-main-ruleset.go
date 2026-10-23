package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type ruleset struct {
	Name         string            `json:"name"`
	Target       string            `json:"target"`
	Enforcement  string            `json:"enforcement"`
	BypassActors []json.RawMessage `json:"bypass_actors"`
	Conditions   struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []rulesetRule `json:"rules"`
}

type rulesetRule struct {
	Type       string            `json:"type"`
	Parameters rulesetParameters `json:"parameters"`
}

type rulesetParameters struct {
	RequiredStatusChecks           []requiredStatusCheck `json:"required_status_checks"`
	Strict                         *bool                 `json:"strict_required_status_checks_policy"`
	EnforceOnCreate                *bool                 `json:"do_not_enforce_on_create"`
	RequiredApprovingReviewCount   *int                  `json:"required_approving_review_count"`
	DismissStaleReviewsOnPush      *bool                 `json:"dismiss_stale_reviews_on_push"`
	RequireCodeOwnerReview         *bool                 `json:"require_code_owner_review"`
	RequireLastPushApproval        *bool                 `json:"require_last_push_approval"`
	RequiredReviewThreadResolution *bool                 `json:"required_review_thread_resolution"`
}

type requiredStatusCheck struct {
	Context       string `json:"context"`
	IntegrationID int    `json:"integration_id"`
}

type workflow struct {
	Env         map[string]string      `yaml:"env"`
	Defaults    map[string]any         `yaml:"defaults"`
	On          workflowTriggers       `yaml:"on"`
	Concurrency *workflowConcurrency   `yaml:"concurrency"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

type workflowConcurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

type workflowTriggers struct {
	PullRequest workflowEvent    `yaml:"pull_request"`
	Push        workflowEvent    `yaml:"push"`
	WorkflowRun workflowRunEvent `yaml:"workflow_run"`
}

type workflowEvent struct {
	Branches []string `yaml:"branches"`
	Types    []string `yaml:"types"`
}

type workflowRunEvent struct {
	Workflows []string `yaml:"workflows"`
	Branches  []string `yaml:"branches"`
	Types     []string `yaml:"types"`
}

type workflowJob struct {
	Additional      map[string]any      `yaml:",inline"`
	TimeoutMinutes  int                 `yaml:"timeout-minutes"`
	ContinueOnError bool                `yaml:"continue-on-error"`
	Name            string              `yaml:"name"`
	RunsOn          string              `yaml:"runs-on"`
	If              string              `yaml:"if"`
	Uses            string              `yaml:"uses"`
	Needs           []string            `yaml:"needs"`
	Permissions     map[string]string   `yaml:"permissions"`
	Strategy        workflowStrategy    `yaml:"strategy"`
	Concurrency     workflowConcurrency `yaml:"concurrency"`
	Steps           []workflowStep      `yaml:"steps"`
}

type workflowStrategy struct {
	FailFast *bool `yaml:"fail-fast"`
	Matrix   struct {
		Include []map[string]string `yaml:"include"`
	} `yaml:"matrix"`
}

type workflowStep struct {
	Additional      map[string]any    `yaml:",inline"`
	Background      bool              `yaml:"background"`
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	Run             string            `yaml:"run"`
	Shell           string            `yaml:"shell"`
	Env             map[string]string `yaml:"env"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	With            map[string]string `yaml:"with"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

func main() {
	if err := validateRepository(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Configuration assertions stop at the first invalid invariant. Convert only
// those expected failures to errors; programming panics must still propagate.
func validateRepository() (err error) {
	defer func() {
		if failure := recover(); failure != nil {
			if validationErr, ok := failure.(validationError); ok {
				err = validationErr
				return
			}
			panic(failure)
		}
	}()
	validateVersionedWorkflows()
	return nil
}

func validateVersionedWorkflows() {
	var configuredRuleset ruleset
	loadJSON(".github/rulesets/main.json", &configuredRuleset)
	ci, ciContent := loadWorkflowWithContent(".github/workflows/ci.yml")
	autoRelease, autoReleaseContent := loadWorkflowWithContent(".github/workflows/auto-release.yml")
	authoritativeRunContent := loadText(".github/scripts/verify-authoritative-run.sh")
	qualityToolsContent := loadText(".github/quality-tools.env")

	if err := validateRuleset(configuredRuleset); err != nil {
		fail(err.Error())
	}

	require(equal(ci.On.PullRequest.Branches, "main"), "CI must target pull requests to main")
	require(equal(ci.On.PullRequest.Types, "opened", "synchronize", "reopened", "ready_for_review", "edited"), "CI pull-request events changed")
	require(equal(ci.On.Push.Branches, "main"), "CI must report status for the main branch badge")

	validateSecurityCI(ci, ciContent)
	validateReviewCI(ci)
	validateBuildCI(ci)
	releaseJob := validateReleaseConfiguration(ci, autoRelease, qualityToolsContent)
	validateReleasePublication(releaseJob, autoReleaseContent)
	validateExistingRelease(releaseJob)
	validateChangelogDelivery(releaseJob, authoritativeRunContent)
	fmt.Fprintln(os.Stdout, "main ruleset and release workflows are consistent")
}

func validateSecurityCI(ci workflow, ciContent string) {
	security := ci.Jobs["security-analysis"]
	require(security.Name == "CodeQL Analysis (${{ matrix.language }})", "CI must publish per-language CodeQL checks")
	require(reflect.DeepEqual(security.Permissions, map[string]string{
		"contents":        "read",
		"security-events": "write",
	}), "CodeQL must use least-privilege permissions")
	require(security.Strategy.FailFast != nil && !*security.Strategy.FailFast, "CodeQL must explicitly disable matrix fail-fast")
	require(reflect.DeepEqual(security.Strategy.Matrix.Include, []map[string]string{
		{"language": "go", "build-mode": "autobuild"},
		{"language": "javascript-typescript", "build-mode": "none"},
	}), "CodeQL must analyze Go and JavaScript with supported build modes")
	codeQLInit := namedStep(security, "Initialize CodeQL")
	requirePinnedAction(ciContent, codeQLInit, "github/codeql-action/init", "v4")
	require(reflect.DeepEqual(codeQLInit.With, map[string]string{
		"languages":  "${{ matrix.language }}",
		"build-mode": "${{ matrix.build-mode }}",
	}), "CodeQL init must receive the language and build mode matrix values")
	codeQLAnalyze := namedStep(security, "Analyze with CodeQL")
	requirePinnedAction(ciContent, codeQLAnalyze, "github/codeql-action/analyze", "v4")
	require(actionSHA(codeQLInit.Uses) == actionSHA(codeQLAnalyze.Uses), "CodeQL init and analyze must use the same action revision")

	dependencyReview := ci.Jobs["dependency-review"]
	require(dependencyReview.Name == "Dependency Review", "CI must publish the Dependency Review check")
	require(reflect.DeepEqual(dependencyReview.Permissions, map[string]string{"contents": "read"}), "dependency review must use read-only contents permission")
	reviewStep := namedStep(dependencyReview, "Review dependency changes")
	require(reviewStep.If == "github.event_name == 'pull_request'", "dependency review must run only for pull requests")
	requirePinnedAction(ciContent, reviewStep, "actions/dependency-review-action", "v5")
	require(reviewStep.With["fail-on-severity"] == "high", "dependency review must block high and critical vulnerabilities")
	require(namedStep(dependencyReview, "Skip dependency review outside pull requests").If == "github.event_name != 'pull_request'", "non-PR runs must complete dependency review without a bypass")
}

func validateReviewCI(ci workflow) {
	changelogCheck := namedStep(ci.Jobs["lint"], "Validate changelog release links")
	require(changelogCheck.Env["GH_TOKEN"] == "${{ github.token }}", "changelog validation must authenticate GitHub Release queries")
	require(strings.TrimSpace(changelogCheck.Run) == "go run ./.github/scripts/changelog-check", "CI must run the tested changelog validator")

	codexReview := ci.Jobs["codex-review"]
	require(codexReview.Name == "Current-head Codex Review", "CI must publish the current-head Codex review check")
	require(codexReview.If == "github.event_name == 'pull_request' || startsWith(github.head_ref || github.ref_name, 'chore/changelog-') || inputs.changelog_branch != '' || inputs.changelog_head != ''", "Codex review must run for every pull request and generated changelog dispatch")
	require(reflect.DeepEqual(codexReview.Permissions, map[string]string{"contents": "read", "checks": "read", "pull-requests": "read"}), "Codex verification must remain read-only")
	require(codexReview.Concurrency.Group == "codex-review-${{ github.event.pull_request.number || inputs.changelog_branch || github.ref_name }}-${{ inputs.changelog_head || github.event.pull_request.head.sha || github.sha }}-${{ github.event_name == 'workflow_dispatch' && format('dispatch-{0}', github.run_id) || 'pull-request' }}", "authoritative changelog dispatches must have isolated review concurrency")
	require(codexReview.Concurrency.CancelInProgress, "duplicate non-authoritative review events must replace stale jobs")
	require(len(codexReview.Steps) >= 3 && codexReview.Steps[0].With["ref"] == "${{ github.event_name == 'workflow_dispatch' && inputs.changelog_branch != '' && github.sha || github.event.repository.default_branch }}", "Codex verification must execute trusted helper code")
	codexStep := namedStep(codexReview, "Require a clean current-head Codex review")
	require(codexStep.Env["PULL_REQUEST_NUMBER"] == "${{ github.event.pull_request.number }}", "ordinary review must bind the event pull request number")
	require(codexStep.Env["REVIEW_SETUP_AT"] == "${{ github.event.pull_request.updated_at }}", "ordinary review setup must bind the triggering pull request update")
	require(codexStep.Env["EXPECTED_HEAD"] == "${{ inputs.changelog_head || github.event.pull_request.head.sha || github.sha }}", "Codex review must bind the immutable head")
	require(strings.Contains(codexStep.Run, `REVIEW_SCOPE="$review_scope" go run ./.github/scripts/changelog-merge review`), "Codex verification must select explicit ordinary or changelog scope")
}

func validateBuildCI(ci workflow) {
	require(helperQualityScopesReady(ci), "all maintained Go helpers must participate in shared quality gates")
	require(credentialScanReady(ci.Jobs["credential-scan"]), "credential scanning must require pinned installation, real fixtures and complete source/history scans")
	require(dashboardQualityReady(ci), "dashboard JavaScript quality must use locked tools, shared commands and retained failure evidence")
	crossBuild := namedStep(ci.Jobs["build-and-test"], "Build all release targets")
	require(reflect.DeepEqual(crossBuild.Env, map[string]string{
		"RELEASE_VERSION":    "ci",
		"RELEASE_OUTPUT_DIR": "${{ runner.temp }}/release-targets",
	}), "CI cross-build must use disposable output and a non-release version")
	require(strings.TrimSpace(crossBuild.Run) == `RELEASE_BUILD_DATE="$(git show -s --format=%cs HEAD)"
export RELEASE_BUILD_DATE
go run ./.github/scripts/release-targets build`, "CI must build the canonical release target manifest")

	gate := ci.Jobs["gate"]
	require(gate.Name == "Quality Gate", "CI must publish the Quality Gate check")
	require(windowsInstallerGateReady(ci.Jobs["windows-installer"], gate), "Quality Gate must require Windows PowerShell 5.1 installer regression tests")
	require(windowsLauncherWorkflowReady(ci), "Quality Gate must execute the standalone launcher lifecycle on Windows with pinned Node")
	require(equal(gate.Needs, "build-and-test", "windows-installer", "lint", "quality", "mutation", "security-analysis", "dependency-review", "codex-review", "performance", "credential-scan"), "Quality Gate must depend on every build, quality, security, review, performance and credential job")
	gateStep := namedStep(gate, "Evaluate all gates")
	gateRun := gateStep.Run
	require(reflect.DeepEqual(gateStep.Env, map[string]string{
		"EVENT_NAME":     "${{ github.event_name }}",
		"REF":            "${{ github.ref }}",
		"REF_NAME":       "${{ github.ref_name }}",
		"DEFAULT_BRANCH": "${{ github.event.repository.default_branch }}",
	}), "Quality Gate must bind event and ref identity through environment variables")
	require(strings.Contains(gateRun, `"${{ needs.security-analysis.result }}" != "success"`), "Quality Gate must reject failed CodeQL analysis")
	require(strings.Contains(gateRun, `"${{ needs.dependency-review.result }}" != "success"`), "Quality Gate must reject failed dependency review")
	require(strings.Contains(gateRun, `if [[ "$EVENT_NAME" == "workflow_dispatch" && \`) && strings.Contains(gateRun, `"$REF_NAME" != chore/changelog-*`), "Quality Gate must reject non-changelog branch dispatches")
	require(strings.Contains(gateRun, `if [[ "$EVENT_NAME" == "pull_request" && "${{ needs.codex-review.result }}" != "success" ]]`), "Quality Gate must reject a skipped or failed pull-request Codex review")
	require(strings.Contains(gateRun, `"${{ needs.performance.result }}" != "success"`), "Quality Gate must reject failed performance budgets")
	require(strings.Contains(gateRun, "::error::One or more quality gates failed"), "Quality Gate must report a failed dependency")
	require(strings.Contains(gateRun, "exit 1"), "Quality Gate must fail when a dependency is unsuccessful")
}

func validateReleaseConfiguration(ci, autoRelease workflow, qualityToolsContent string) workflowJob {
	require(equal(autoRelease.On.WorkflowRun.Workflows, "CI Quality Gates"), "auto-release must follow CI Quality Gates")
	require(equal(autoRelease.On.WorkflowRun.Types, "completed"), "auto-release must follow completed CI runs")
	require(equal(autoRelease.On.WorkflowRun.Branches, "main"), "auto-release must follow main-branch CI runs")
	require(autoRelease.Concurrency == nil, "auto-release concurrency must not include skipped workflow runs")
	releaseJob, ok := autoRelease.Jobs["release"]
	require(ok, "auto-release must define the release job")
	require(
		releaseJob.If == "github.event.workflow_run.conclusion == 'success' && (github.event.workflow_run.event == 'push' || github.event.workflow_run.event == 'workflow_dispatch')",
		"auto-release must require successful push or trusted workflow-dispatch CI",
	)
	require(releaseJob.Concurrency.Group == "auto-release", "eligible releases must share one concurrency group")
	require(!releaseJob.Concurrency.CancelInProgress, "an active release must not be cancelled")
	require(reflect.DeepEqual(releaseJob.Permissions, map[string]string{
		"actions": "write", "attestations": "write", "checks": "read", "contents": "write", "id-token": "write", "pull-requests": "write",
	}), "release permissions must be limited to publishing, provenance, CI dispatch, and changelog merge")
	for _, job := range autoRelease.Jobs {
		require(job.Uses != "./.github/workflows/ci.yml", "auto-release must not duplicate the CI suite")
	}

	mutationJob, ok := ci.Jobs["mutation"]
	require(ok, "CI must define the mutation job")
	fixtureStep := namedStep(mutationJob, "Verify mutation threshold fixtures")
	require(strings.TrimSpace(fixtureStep.Run) == "bash .github/scripts/test-mutation-gate.sh", "CI must test distinct uncovered and live mutation failures")
	mutationStep := namedStep(mutationJob, "Enforce: mutation efficacy ≥ 90% and mutator coverage ≥ 90%")
	require(strings.TrimSpace(mutationStep.Run) == "bash .github/scripts/run-mutation-gate.sh", "CI must use the shared mutation threshold gate")
	validateQualitySettings(qualityToolsContent)

	validateTrustedReleaseHelper(releaseJob)

	return releaseJob
}

func validateReleasePublication(releaseJob workflowJob, autoReleaseContent string) {
	check := namedStep(releaseJob, "Check whether release is needed")
	require(stepIndex(releaseJob, "Load trusted release helpers") < stepIndex(releaseJob, "Check whether release is needed"), "trusted release helpers must load before any release decision")
	require(check.Env["RELEASE_SHA"] == "${{ github.event.workflow_run.head_sha }}", "release check must receive the validated SHA through env")
	require(strings.TrimSpace(check.Run) == `go run "$RUNNER_TEMP/release-plan.go" check`, "release check must use the trusted release-plan command")

	version := namedStep(releaseJob, "Determine next version")
	require(version.Env["LATEST_TAG"] == "${{ steps.check.outputs.latest }}", "version step must receive the latest tag through env")
	require(version.Env["VERSION_BASE_TAG"] == "${{ steps.check.outputs.version_base }}", "version step must reserve versions across all semantic tags")
	require(strings.TrimSpace(version.Run) == `go run "$RUNNER_TEMP/release-plan.go" version`, "version step must use the trusted release-plan command")

	notes := namedStep(releaseJob, "Generate release notes")
	require(notes.Env["LATEST_TAG"] == "${{ steps.check.outputs.latest }}", "release notes must receive the latest tag through env")
	require(notes.Env["RELEASE_TAG"] == "${{ steps.version.outputs.tag }}", "release notes must receive the calculated release tag through env")
	require(strings.TrimSpace(notes.Run) == `go run "$RUNNER_TEMP/release-plan.go" notes`, "release notes must use the trusted release-plan command")

	build := namedStep(releaseJob, "Build cross-platform binaries")
	require(build.If == "steps.check.outputs.skip == 'false' || steps.existing_release.outputs.found == 'true'", "new and resumed releases must build the complete asset set")
	require(reflect.DeepEqual(build.Env, map[string]string{
		"RELEASE_VERSION":    "${{ steps.version.outputs.tag || steps.check.outputs.release_tag }}",
		"RELEASE_SHA":        "${{ github.event.workflow_run.head_sha }}",
		"RELEASE_OUTPUT_DIR": "dist",
	}), "release builds must bind the version, validated source, and output directory")
	require(strings.TrimSpace(build.Run) == `RELEASE_BUILD_DATE="$(git show -s --format=%cs "$RELEASE_SHA")"
export RELEASE_BUILD_DATE
if [[ ! -f .github/release-targets.json ]]; then
  RELEASE_TARGET_MANIFEST="$RUNNER_TEMP/release-targets-legacy.json"
  export RELEASE_TARGET_MANIFEST
fi
go run "$RUNNER_TEMP/release-targets.go" build`, "release builds must use the trusted canonical target builder with compatibility for pre-manifest tags")

	attest := namedStep(releaseJob, "Attest release binaries")
	require(attest.If == "steps.check.outputs.skip == 'false'", "provenance must bind only the initial release workflow source identity")
	requirePinnedAction(autoReleaseContent, attest, "actions/attest-build-provenance", "v3")
	require(reflect.DeepEqual(attest.With, map[string]string{"subject-path": "dist/*"}), "provenance must cover every release binary")
	require(!attest.ContinueOnError, "provenance failure must stop release publication")
	require(stepIndex(releaseJob, "Build cross-platform binaries") < stepIndex(releaseJob, "Attest release binaries") && stepIndex(releaseJob, "Attest release binaries") < stepIndex(releaseJob, "Create release"), "release binaries must be built and attested before publication")

	create := namedStep(releaseJob, "Create release")
	require(create.If == "steps.check.outputs.skip == 'false' || steps.existing_release.outputs.found == 'true'", "new and resumed releases must reconcile assets")
	require(create.Env["RELEASE_SHA"] == "${{ github.event.workflow_run.head_sha }}", "release creation must receive the validated SHA through env")
	require(create.Env["RELEASE_TAG"] == "${{ steps.version.outputs.tag || steps.check.outputs.release_tag }}", "release creation must receive the new or resumed release tag")
	require(strings.TrimSpace(create.Run) == `go run "$RUNNER_TEMP/release-plan.go" create`, "release creation must use the trusted release-plan command")
}

func validateExistingRelease(releaseJob workflowJob) {
	existingNotes := namedStep(releaseJob, "Load existing release notes")
	require(existingNotes.ID == "existing_release", "existing release notes step must expose its outcome")
	require(existingNotes.If == "steps.check.outputs.release_tag != ''", "tagged release runs must check for existing release notes")
	require(stepIndex(releaseJob, "Load existing release notes") < stepIndex(releaseJob, "Build cross-platform binaries"), "tagged release existence must be known before asset reconciliation")
	require(!existingNotes.ContinueOnError, "release lookup failures must not be suppressed")
	require(existingNotes.Env["RELEASE_TAG"] == "${{ steps.check.outputs.release_tag }}", "existing release notes must use the tagged head")
	require(strings.Contains(existingNotes.Run, `gh api --include --silent "repos/${GITHUB_REPOSITORY}/releases/tags/${RELEASE_TAG}"`), "existing release lookup must expose its HTTP status")
	require(strings.Contains(existingNotes.Run, `grep -Eq '^HTTP/[^ ]+ 404 '`), "only a confirmed missing release may skip reconciliation")
	require(strings.Contains(existingNotes.Run, `gh release view "$RELEASE_TAG" --json body --jq .body > release-notes.md`), "existing release notes must load the authoritative GitHub Release body")
	require(strings.Contains(existingNotes.Run, `echo "found=true" >> "$GITHUB_OUTPUT"`), "existing release lookup must publish a found result")
	require(strings.Contains(existingNotes.Run, `echo "found=false" >> "$GITHUB_OUTPUT"`), "a confirmed missing release must publish a not-found result")
	require(strings.Contains(existingNotes.Run, `exit 1`), "non-404 release lookup failures must fail reconciliation")

	changelog := namedStep(releaseJob, "Update changelog")
	require(changelog.If == "steps.check.outputs.skip == 'false' || steps.existing_release.outputs.found == 'true'", "changelog update must run for new and confirmed existing releases")
	require(changelog.Env["RELEASE_TAG"] == "${{ steps.version.outputs.tag || steps.check.outputs.release_tag }}", "changelog update must receive the new or resumed release tag")
	require(strings.Contains(changelog.Run, "git switch --detach origin/main"), "changelog reconciliation must start from current main")
	require(strings.Contains(changelog.Run, `go run "$RUNNER_TEMP/release-plan.go" changelog`), "release workflow must use the trusted changelog updater")
}

func validateChangelogDelivery(releaseJob workflowJob, authoritativeRunContent string) {
	mergeChangelog := namedStep(releaseJob, "Queue guarded changelog auto-merge and await completion")
	require(mergeChangelog.If == "steps.check.outputs.skip == 'false' || steps.existing_release.outputs.found == 'true'", "changelog pull request must run for new and confirmed existing releases")
	require(mergeChangelog.Env["RELEASE_TAG"] == "${{ steps.version.outputs.tag || steps.check.outputs.release_tag }}", "changelog pull request must receive the new or resumed release tag")
	require(mergeChangelog.Env["RELEASE_SHA"] == "${{ github.event.workflow_run.head_sha }}", "changelog reconciliation must retain the released commit SHA")
	require(strings.Contains(mergeChangelog.Run, `"repos/${GITHUB_REPOSITORY}/actions/workflows/ci.yml/dispatches"`) && strings.Contains(mergeChangelog.Run, `-f ref="$branch" --jq '.workflow_run_id'`), "changelog pull request must dispatch CI on its exact branch and capture the run ID")
	require(strings.Contains(mergeChangelog.Run, `bash .github/scripts/verify-authoritative-run.sh \`) && strings.Contains(mergeChangelog.Run, `"$GITHUB_REPOSITORY" "$pr_url" "$head_sha" "$ci_run"`), "changelog merge must invoke the authoritative run verifier with exact identities")
	require(strings.Contains(authoritativeRunContent, `timeout 60m gh run watch "$run_id" --repo "$repo" --exit-status`) && strings.Contains(authoritativeRunContent, `"$actual_head" != "$expected_head"`), "authoritative verifier must wait for the dispatched run and verify its head")
	require(
		strings.Contains(authoritativeRunContent, `gh api --paginate "repos/${repo}/actions/runs/${run_id}/jobs?per_page=100" |`) &&
			strings.Contains(authoritativeRunContent, `jq -sr '[.[].jobs[] |`) &&
			strings.Contains(authoritativeRunContent, `select(.name == "Quality Gate")] | if length == 1 then .[0].conclusion else "ambiguous" end`) &&
			strings.Contains(authoritativeRunContent, `"$gate_conclusion" != "success"`),
		"authoritative changelog run must contain exactly one successful Quality Gate",
	)
	require(strings.Contains(authoritativeRunContent, `PR $pr_url expected head $expected_head authoritative run $run_id`) && strings.Contains(authoritativeRunContent, `Inspect $run_url.`), "authoritative validation failures must identify and link the pull request, head, and run")
	require(strings.Contains(mergeChangelog.Run, `branch="chore/changelog-${RELEASE_TAG#v}"`), "release retries must reuse a deterministic changelog branch")
	require(strings.Contains(mergeChangelog.Run, `gh pr list --base main --head "$branch" --state open`), "release retries must reuse an existing open changelog pull request")
	require(strings.Contains(mergeChangelog.Run, `--jq '.[0].url // empty'`), "a missing changelog pull request must produce an empty lookup")
	require(strings.Contains(mergeChangelog.Run, `git push --force-with-lease=`), "release retries must safely refresh the existing changelog branch")
	require(strings.Contains(mergeChangelog.Run, "go run ./.github/scripts/changelog-merge enable"), "changelog update must use guarded native auto-merge and await completion")
	require(strings.Contains(mergeChangelog.Run, `EXPECTED_HEAD="$head_sha"`), "changelog merge must pin its expected head")
	require(strings.Contains(mergeChangelog.Run, `git push --force-with-lease="refs/heads/${branch}:${head_sha}" origin ":refs/heads/${branch}"`), "changelog cleanup must delete only the merged head")
	require(strings.Contains(mergeChangelog.Run, "gh workflow run ci.yml --ref main"), "the bot merge must dispatch main CI for any intervening product changes")
	noDiffStart := strings.Index(mergeChangelog.Run, "if git diff --quiet -- CHANGELOG.md; then")
	require(noDiffStart >= 0, "release retries must recognize an already-current changelog")
	noDiffEnd := strings.Index(mergeChangelog.Run[noDiffStart:], "\nfi\n\nbranch=")
	require(noDiffEnd >= 0, "already-current changelog handling must terminate its conditional")
	noDiffBranch := mergeChangelog.Run[noDiffStart : noDiffStart+noDiffEnd]
	require(strings.Contains(noDiffBranch, `if [[ "$(git rev-parse HEAD)" != "$RELEASE_SHA" ]]; then`), "already-current changelog retries must dispatch only when main advanced past the released commit")
	noDiffDispatch := strings.Index(noDiffBranch, "gh workflow run ci.yml --ref main")
	noDiffExit := strings.Index(noDiffBranch, "exit 0")
	require(noDiffDispatch >= 0 && noDiffExit >= 0 && noDiffDispatch < noDiffExit, "already-current changelog retries must dispatch main CI before returning")
}

type rulesetValidation struct {
	valid   bool
	message string
}

func unconditionalRequiredStep(step workflowStep) bool {
	return step.If == "" && !step.ContinueOnError && !step.Background
}

func requiredWindowsJob(job workflowJob) bool {
	return job.RunsOn == "windows-latest" && job.If == "" && !job.ContinueOnError && len(job.Additional) == 0
}

func windowsInstallerGateReady(installer, gate workflowJob) bool {
	step := namedStep(installer, "Test Windows PowerShell installer")
	evaluator := namedStep(gate, "Evaluate all gates")
	return requiredWindowsJob(installer) && !gate.ContinueOnError &&
		step.Shell == "pwsh" && unconditionalRequiredStep(step) &&
		strings.TrimSpace(step.Run) == `./tests/installer/test-dashboard-hub.ps1 -InstallerPowerShell "$env:WINDIR/System32/WindowsPowerShell/v1.0/powershell.exe"` &&
		slices.Contains(gate.Needs, "windows-installer") && gate.If == "always()" &&
		unconditionalRequiredStep(evaluator) &&
		strings.Contains(evaluator.Run, `"${{ needs.windows-installer.result }}" != "success"`)
}

// Keep the supported evaluator identical to CI; substring checks cannot prove execution.
const qualityGateScript = `echo "Build & Test: ${{ needs.build-and-test.result }}"
echo "Windows 5.1:  ${{ needs.windows-installer.result }}"
echo "Lint:         ${{ needs.lint.result }}"
echo "Quality:      ${{ needs.quality.result }}"
echo "Mutation:     ${{ needs.mutation.result }}"
echo "CodeQL:       ${{ needs.security-analysis.result }}"
echo "Dependencies: ${{ needs.dependency-review.result }}"
echo "Codex review: ${{ needs.codex-review.result }}"
echo "Performance:  ${{ needs.performance.result }}"
echo "Credentials:  ${{ needs.credential-scan.result }}"

if [[ "${{ needs.build-and-test.result }}" != "success" || \
      "${{ needs.windows-installer.result }}" != "success" || \
      "${{ needs.lint.result }}" != "success" || \
      "${{ needs.quality.result }}" != "success" || \
      "${{ needs.mutation.result }}" != "success" || \
      "${{ needs.security-analysis.result }}" != "success" || \
      "${{ needs.dependency-review.result }}" != "success" || \
      "${{ needs.credential-scan.result }}" != "success" || \
      "${{ needs.performance.result }}" != "success" ]]; then
  echo "::error::One or more quality gates failed"
  exit 1
fi
if [[ "$EVENT_NAME" == "workflow_dispatch" && \
      "$REF" != "refs/heads/$DEFAULT_BRANCH" && \
      "$REF_NAME" != chore/changelog-* ]]; then
  echo "::error::A non-changelog branch dispatch cannot publish the required Quality Gate"
  exit 1
fi
if [[ "$EVENT_NAME" == "pull_request" && "${{ needs.codex-review.result }}" != "success" ]]; then
  echo "::error::Every pull request requires a clean current-head Codex review"
  exit 1
fi
if [[ "${{ needs.codex-review.result }}" != "success" && "${{ needs.codex-review.result }}" != "skipped" ]]; then
  echo "::error::Current-head Codex review gate did not pass"
  exit 1
fi
echo "All quality gates passed ✅"`

func windowsAggregateGateReady(gate workflowJob) bool {
	if gate.RunsOn != "ubuntu-latest" || gate.If != "always()" || gate.ContinueOnError || len(gate.Additional) != 0 || len(gate.Steps) != 1 {
		return false
	}
	want := workflowStep{
		Name: "Evaluate all gates", Shell: "bash", Run: qualityGateScript,
		Env: map[string]string{
			"EVENT_NAME":     "${{ github.event_name }}",
			"REF":            "${{ github.ref }}",
			"REF_NAME":       "${{ github.ref_name }}",
			"DEFAULT_BRANCH": "${{ github.event.repository.default_branch }}",
		},
	}
	actual := gate.Steps[0]
	actual.Run = strings.TrimSpace(actual.Run)
	return reflect.DeepEqual(actual, want)
}

func windowsLauncherWorkflowReady(ci workflow) bool {
	return len(ci.Env) == 0 && len(ci.Defaults) == 0 && windowsLauncherGateReady(ci.Jobs["windows-installer"], ci.Jobs["gate"])
}

func windowsLauncherGateReady(installer, gate workflowJob) bool {
	step, found := findWorkflowStep(installer, "Test Windows standalone launcher")
	return found && windowsInstallerGateReady(installer, gate) && windowsAggregateGateReady(gate) && windowsLauncherSetupReady(installer) &&
		step.Shell == "pwsh" && unconditionalRequiredStep(step) && launcherNodeEnvironmentReady(step) &&
		strings.TrimSpace(step.Run) == "node --test src/codex-dashboard/launcher.test.mjs"
}

func launcherNodeEnvironmentReady(step workflowStep) bool {
	options, present := step.Env["NODE_OPTIONS"]
	return present && options == "" && len(step.Env) == 1 && len(step.Additional) == 0
}

func windowsLauncherSetupReady(job workflowJob) bool {
	checkout := requiredActionIndex(job, "actions/checkout")
	node := requiredActionIndex(job, "actions/setup-node")
	launcher := slices.IndexFunc(job.Steps, func(step workflowStep) bool {
		return step.Name == "Test Windows standalone launcher"
	})
	return checkout == 0 && node == checkout+1 && launcher == node+1 && hasSingleLauncherAction(job, "actions/setup-node") && hasSingleLauncherAction(job, "actions/checkout") && reflect.DeepEqual(job.Steps[node].With, map[string]string{"node-version-file": ".node-version"})
}

func hasSingleLauncherAction(job workflowJob, action string) bool {
	count := 0
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, action+"@") {
			count++
		}
	}
	return count == 1
}

func requiredActionIndex(job workflowJob, action string) int {
	for index, step := range job.Steps {
		if isPinnedAction(step.Uses, action) && unconditionalRequiredStep(step) && len(step.Additional) == 0 && len(step.Env) == 0 &&
			(action != "actions/checkout" || checkoutTargetsWorkspace(step)) {
			return index
		}
	}
	return -1
}

func checkoutTargetsWorkspace(step workflowStep) bool {
	return (step.With["repository"] == "" || step.With["repository"] == "${{ github.repository }}") &&
		(step.With["path"] == "" || step.With["path"] == ".") && step.With["ref"] == ""
}

func validateRuleset(configuredRuleset ruleset) error {
	requiredStatusRules := rulesOfType(configuredRuleset, "required_status_checks")
	pullRequestRules := rulesOfType(configuredRuleset, "pull_request")
	if err := firstRulesetError(
		rulesetValidation{configuredRuleset.Name == "main-quality-gate", "unexpected ruleset name"},
		rulesetValidation{configuredRuleset.Target == "branch", "ruleset must target branches"},
		rulesetValidation{configuredRuleset.Enforcement == "active", "ruleset must be active"},
		rulesetValidation{len(configuredRuleset.BypassActors) == 0, "ruleset must not have bypass actors"},
		rulesetValidation{equal(configuredRuleset.Conditions.RefName.Include, "refs/heads/main"), "ruleset must target only main"},
		rulesetValidation{len(configuredRuleset.Conditions.RefName.Exclude) == 0, "ruleset must not exclude refs"},
		rulesetValidation{len(requiredStatusRules) == 1, "ruleset must define one required-status-check rule"},
		rulesetValidation{len(pullRequestRules) == 1, "ruleset must define one pull-request rule"},
		rulesetValidation{len(configuredRuleset.Rules) == 2, "ruleset must define only status-check and pull-request rules"},
	); err != nil {
		return err
	}
	if err := validateRequiredStatusRule(requiredStatusRules[0]); err != nil {
		return err
	}
	return validatePullRequestRule(pullRequestRules[0])
}

func rulesOfType(configuredRuleset ruleset, ruleType string) []rulesetRule {
	matching := make([]rulesetRule, 0, 1)
	for _, rule := range configuredRuleset.Rules {
		if rule.Type == ruleType {
			matching = append(matching, rule)
		}
	}
	return matching
}

func validateRequiredStatusRule(rule rulesetRule) error {
	parameters := rule.Parameters
	return firstRulesetError(
		rulesetValidation{
			reflect.DeepEqual(parameters.RequiredStatusChecks, []requiredStatusCheck{{Context: "Quality Gate", IntegrationID: 15368}}),
			"ruleset must require Quality Gate from GitHub Actions",
		},
		rulesetValidation{parameters.Strict != nil && !*parameters.Strict, "strict mode must remain disabled"},
		rulesetValidation{parameters.EnforceOnCreate != nil && !*parameters.EnforceOnCreate, "checks must apply to new refs"},
	)
}

func validatePullRequestRule(rule rulesetRule) error {
	parameters := rule.Parameters
	return firstRulesetError(
		rulesetValidation{parameters.RequiredApprovingReviewCount != nil && *parameters.RequiredApprovingReviewCount == 0, "pull requests must require exactly zero approving reviews"},
		rulesetValidation{parameters.DismissStaleReviewsOnPush != nil && !*parameters.DismissStaleReviewsOnPush, "stale-review dismissal must remain disabled"},
		rulesetValidation{parameters.RequireCodeOwnerReview != nil && !*parameters.RequireCodeOwnerReview, "code-owner review must remain disabled"},
		rulesetValidation{parameters.RequireLastPushApproval != nil && !*parameters.RequireLastPushApproval, "last-push approval must remain disabled"},
		rulesetValidation{parameters.RequiredReviewThreadResolution != nil && *parameters.RequiredReviewThreadResolution, "pull requests must require resolved review threads"},
	)
}

func firstRulesetError(validations ...rulesetValidation) error {
	for _, validation := range validations {
		if !validation.valid {
			return fmt.Errorf("%s", validation.message)
		}
	}
	return nil
}

func loadJSON(path string, target any) {
	data, err := os.ReadFile(path)
	if err != nil {
		fail(fmt.Sprintf("read %s: %v", path, err))
	}
	if err := json.Unmarshal(data, target); err != nil {
		fail(fmt.Sprintf("parse %s: %v", path, err))
	}
}

func loadText(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		fail("read " + path + ": " + err.Error())
	}
	return string(contents)
}

func loadWorkflowWithContent(path string) (workflow, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fail(fmt.Sprintf("read %s: %v", path, err))
	}
	var result workflow
	if err := yaml.Unmarshal(data, &result); err != nil {
		fail(fmt.Sprintf("parse %s: %v", path, err))
	}
	return result, string(data)
}

func stepIndex(job workflowJob, name string) int {
	for index, step := range job.Steps {
		if step.Name == name {
			return index
		}
	}
	fail("missing workflow step: " + name)
	return -1
}

func validateQualitySettings(contents string) {
	for _, setting := range []string{"GREMLINS_VERSION=v0.6.0", "MUTATION_EFFICACY_THRESHOLD=90", "MUTATION_COVERAGE_THRESHOLD=90", "MUTATION_PACKAGE_SCOPE=./src"} {
		key, _, _ := strings.Cut(setting, "=")
		count := 0
		for _, line := range strings.Split(contents, "\n") {
			if strings.HasPrefix(line, key+"=") {
				count++
				require(line == setting, "quality settings must pin "+setting)
			}
		}
		require(count == 1, "quality settings must contain exactly one "+key+" assignment")
	}
}

func validateTrustedReleaseHelper(job workflowJob) {
	trustedHelper := namedStep(job, "Load trusted release helpers")
	require(trustedHelper.Env["TRUSTED_HELPER_SHA"] == "${{ github.workflow_sha }}", "release helpers must bind to the trusted workflow revision")
	require(strings.TrimSpace(trustedHelper.Run) == `git fetch --no-tags origin "$TRUSTED_HELPER_SHA"
git show "$TRUSTED_HELPER_SHA:.github/scripts/release-plan/main.go" > "$RUNNER_TEMP/release-plan.go"
git show "$TRUSTED_HELPER_SHA:.github/scripts/release-targets/main.go" > "$RUNNER_TEMP/release-targets.go"
git show "$TRUSTED_HELPER_SHA:.github/release-targets-legacy.json" > "$RUNNER_TEMP/release-targets-legacy.json"`, "release helpers and the legacy compatibility manifest must load from the trusted workflow revision outside the target checkout")
}

func namedStep(job workflowJob, name string) workflowStep {
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	fail("missing workflow step: " + name)
	return workflowStep{}
}

func equal[T comparable](actual []T, expected ...T) bool {
	return reflect.DeepEqual(actual, expected)
}

func require(condition bool, message string) {
	if !condition {
		fail(message)
	}
}

type validationError string

func (err validationError) Error() string { return string(err) }

func fail(message string) { panic(validationError(message)) }

var actionSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func isPinnedAction(uses, expectedAction string) bool {
	action, sha, found := strings.Cut(uses, "@")
	if !found {
		return false
	}
	return action == expectedAction && actionSHAPattern.MatchString(sha)
}

func actionSHA(uses string) string {
	_, sha, _ := strings.Cut(uses, "@")
	return sha
}

func requirePinnedAction(workflowContent string, step workflowStep, expectedAction string, expectedMajor string) {
	require(isPinnedAction(step.Uses, expectedAction), fmt.Sprintf("%s must use a pinned commit SHA", expectedAction))
	version, ok := actionVersionComment(workflowContent, expectedAction)
	require(ok, fmt.Sprintf("%s must include a release-version comment", expectedAction))
	require(strings.HasPrefix(version, expectedMajor+"."), fmt.Sprintf("%s must use supported major version %s (found %s)", expectedAction, expectedMajor, version))
}

func actionVersionComment(workflowContent, action string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^[ \t]*(?:-[ \t]+)?uses:[ \t]+` + regexp.QuoteMeta(action) + `@[0-9a-f]{40}[ \t]+#[ \t]+(v\d+(?:\.\d+)*)`)
	matches := pattern.FindStringSubmatch(workflowContent)
	if len(matches) < 2 {
		return "", false
	}
	return matches[1], true
}

func helperQualityScopesReady(ci workflow) bool {
	if !workflowCommandsReady(ci, helperQualityCommands) {
		return false
	}
	crap, _ := findWorkflowStep(ci.Jobs["quality"], "Enforce: CRAP score < 30")
	return crap.Shell == "pwsh"
}

func workflowCommandsReady(ci workflow, commands map[string]map[string]string) bool {
	for job, steps := range commands {
		for name, command := range steps {
			step, found := findWorkflowStep(ci.Jobs[job], name)
			if !found || strings.TrimSpace(step.Run) != command || step.If != "" || step.ContinueOnError {
				return false
			}
		}
	}
	return true
}

func dashboardQualityReady(ci workflow) bool {
	if !workflowCommandsReady(ci, dashboardQualityCommands) {
		return false
	}
	for _, job := range []string{"build-and-test", "quality", "performance"} {
		if !dashboardNodePinReady(ci.Jobs[job]) {
			return false
		}
	}
	return dashboardEvidenceReady(ci.Jobs["quality"]) && dashboardDiagnosticsReady(ci.Jobs["quality"])
}

func dashboardDiagnosticsReady(job workflowJob) bool {
	for _, name := range []string{"Install dashboard quality tools", "Test dashboard quality gate"} {
		if namedStep(job, name).Shell != "bash" {
			return false
		}
	}
	return true
}

func dashboardEvidenceReady(job workflowJob) bool {
	upload, found := findWorkflowStep(job, "Upload dashboard quality evidence")
	return found && upload.If == "always()" && !upload.ContinueOnError &&
		upload.With["name"] == "dashboard-quality" && upload.With["path"] == "${{ runner.temp }}/dashboard-quality" &&
		upload.With["retention-days"] == "14" && upload.With["if-no-files-found"] == "error" && isPinnedAction(upload.Uses, "actions/upload-artifact")
}

func dashboardNodePinReady(job workflowJob) bool {
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-node@") {
			return isPinnedAction(step.Uses, "actions/setup-node") && unconditionalRequiredStep(step) &&
				step.With["node-version-file"] == ".node-version" && step.With["node-version"] == ""
		}
	}
	return false
}

var dashboardQualityCommands = map[string]map[string]string{
	"quality": {
		"Install dashboard quality tools": `mkdir -p "$RUNNER_TEMP/dashboard-quality"
node .github/scripts/dashboard-quality/install.mjs 2>&1 | tee "$RUNNER_TEMP/dashboard-quality/install.log"`,
		"Test dashboard quality gate": `mkdir -p "$RUNNER_TEMP/dashboard-quality"
node --test .github/scripts/dashboard-quality/*.test.mjs 2>&1 | tee "$RUNNER_TEMP/dashboard-quality/fixtures.log"`,
		"Enforce dashboard JavaScript quality": `node .github/scripts/dashboard-quality/run.mjs "$RUNNER_TEMP/dashboard-quality"`,
	},
}

func findWorkflowStep(job workflowJob, name string) (workflowStep, bool) {
	for _, step := range job.Steps {
		if step.Name == name {
			return step, true
		}
	}
	return workflowStep{}, false
}

// Keep these commands canonical: a comment or unrelated command containing a
// helper path cannot stand in for an analyzer's actual arguments.
var helperQualityCommands = map[string]map[string]string{
	"build-and-test": {
		"Build": `go build -o "$RUNNER_TEMP/bin/" ./... ./.github/scripts/...`,
		"Vet":   `go vet ./... ./.github/scripts/...`,
		"Test Go packages (race detection enabled)": `go list ./... ./.github/scripts/... | grep -v '/tests/behavior$' > "$RUNNER_TEMP/go-packages.list"
mapfile -t packages < "$RUNNER_TEMP/go-packages.list"
go test -race -count=1 -coverprofile=coverage.out "${packages[@]}"`,
	},
	"lint": {
		"Staticcheck":                       `staticcheck ./... ./.github/scripts/...`,
		"Gocritic (anti-pattern detection)": `gocritic check ./... ./.github/scripts/...`,
		"Errcheck (unchecked errors)":       `errcheck -exclude .errcheck_excludes ./... ./.github/scripts/...`,
		"Dead code detection": `output=$(deadcode ./... ./.github/scripts/... 2>&1 || true)
if [ -n "$output" ]; then
  echo "::error::Dead code detected:"
  echo "$output"
  exit 1
fi`,
		"Vulnerability scan": `govulncheck ./... ./.github/scripts/...`,
	},
	"quality": {
		"Enforce: cyclomatic complexity ≤ 10": `if ! command -v gocyclo &>/dev/null; then
  echo "::error::gocyclo not installed"
  exit 1
fi
violations=$(gocyclo -over 10 -ignore "_test\.go" . .github/scripts 2>&1 || true)
if [ -n "$violations" ]; then
  count=$(echo "$violations" | wc -l)
  echo "::error::${count} function(s) exceed cyclomatic complexity 10:"
  echo "$violations"
  exit 1
fi`,
		"Enforce: cognitive complexity ≤ 15": `if ! command -v gocognit &>/dev/null; then
  echo "::error::gocognit not installed"
  exit 1
fi
violations=$(gocognit -over 15 -ignore "_test\.go" . .github/scripts 2>&1 || true)
if [ -n "$violations" ]; then
  count=$(echo "$violations" | wc -l)
  echo "::error::${count} function(s) exceed cognitive complexity 15:"
  echo "$violations"
  exit 1
fi`,
		"Enforce: CRAP score < 30": `. ./.agents/skills/perfection/scripts/perfection-audit.ps1
Assert-CrapThreshold coverage.out 30.0`,
	},
}

func credentialScanReady(job workflowJob) bool {
	if len(job.Steps) != 5 || !isPinnedAction(job.Steps[0].Uses, "actions/checkout") || !isPinnedAction(job.Steps[1].Uses, "actions/setup-go") {
		return false
	}
	expected := workflowJob{
		Name: "Credential Scan", RunsOn: "ubuntu-latest", TimeoutMinutes: 10,
		Steps: []workflowStep{
			{Uses: job.Steps[0].Uses, With: map[string]string{"fetch-depth": "0"}},
			{Uses: job.Steps[1].Uses, With: map[string]string{"go-version-file": "go.mod"}},
			{Name: "Install credential scanner", Run: "bash .github/scripts/install-quality-tools.sh credentials"},
			{Name: "Test credential scanner fixtures", Run: "go test -race -count=1 -timeout=90s ./.github/scripts/credential-scan -run '^TestGitleaks' -gitleaks-integration"},
			{Name: "Scan source and reachable history", Run: `go run ./.github/scripts/credential-scan -report "$RUNNER_TEMP/credential-scope.json"`},
		},
	}
	return reflect.DeepEqual(job, expected)
}
