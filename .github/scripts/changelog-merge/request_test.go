package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func reviewFixture(t *testing.T, state *reviewState) command {
	t.Helper()
	return func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "api" && args[1] == "graphql":
			return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": *state}}}), nil
		case strings.Contains(strings.Join(args, " "), "/files?"):
			return encode(t, []changedFile{{"CHANGELOG.md", "modified"}}), nil
		case args[0] == "api" && args[1] == "repos/HemSoft/gh-x/pulls/12":
			return encode(t, validPR()), nil
		default:
			t.Fatalf("unexpected API call or optional reviewer request: %v", args)
			return nil, nil
		}
	}
}

func TestWorkflowHasOneRequestOwnerAndReusesVerification(t *testing.T) {
	var release struct {
		Jobs map[string]struct {
			Concurrency struct {
				Group  string
				Cancel bool `yaml:"cancel-in-progress"`
			}
			Steps []struct {
				Run string
				Env map[string]string
			}
		}
	}
	data, err := os.ReadFile("../../workflows/auto-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &release); err != nil {
		t.Fatal(err)
	}
	job := release.Jobs["release"]
	if job.Concurrency.Group != "auto-release" || job.Concurrency.Cancel {
		t.Fatal("automated request owner must serialize rather than cancel")
	}
	requests := 0
	for _, step := range job.Steps {
		if !strings.Contains(step.Run, "changelog-merge request") {
			continue
		}
		requests++
		if step.Env["CODEX_REVIEW_TOKEN"] != "${{ secrets.CODEX_REVIEW_TOKEN }}" || !strings.Contains(step.Run, `GH_TOKEN="$CODEX_REVIEW_TOKEN"`) || !strings.Contains(step.Run, `-z "$CODEX_REVIEW_TOKEN"`) {
			t.Fatal("request must use the explicit connected-user secret and guard missing setup")
		}
		verify := strings.Index(step.Run, "verify-authoritative-run.sh")
		merge := strings.Index(step.Run, "changelog-merge enable")
		if verify < 0 || verify > merge || !strings.Contains(step.Run, ".workflow_run_id") || !strings.Contains(step.Run, `"$GITHUB_REPOSITORY" "$pr_url" "$head_sha" "$ci_run"`) {
			t.Fatal("must verify the exact dispatched run and head before reading the merge gate")
		}
	}
	if requests != 1 {
		t.Fatalf("expected one request owner, got %d", requests)
	}
	data, err = os.ReadFile("../../workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	ci := string(data)
	if strings.Contains(ci, "changelog-merge request") || !strings.Contains(ci, "cancel-in-progress: true") || !strings.Contains(ci, "inputs.changelog_head || github.event.pull_request.head.sha || github.sha") {
		t.Fatal("verification must be read-only and replace duplicate same-head waits")
	}
	for _, required := range []string{
		"name: Current-head Codex Review",
		"if: github.event_name == 'pull_request'",
		"PULL_REQUEST_NUMBER: ${{ github.event.pull_request.number }}",
		"REVIEW_SETUP_AT: ${{ github.event.pull_request.updated_at }}",
		"PR_AUTHOR: ${{ github.event.pull_request.user.login }}",
		`REVIEW_SCOPE="$review_scope" go run ./.github/scripts/changelog-merge review`,
		"needs.codex-review.result",
		`"$EVENT_NAME" == "pull_request"`,
	} {
		if !strings.Contains(ci, required) {
			t.Fatalf("ordinary review enforcement missing %q", required)
		}
	}
	dispatchGuard := `if [[ "$EVENT_NAME" == "workflow_dispatch" && \
      "$REF" != "refs/heads/$DEFAULT_BRANCH" && \
      "$REF_NAME" != chore/changelog-* ]]; then`
	if !strings.Contains(strings.Join(strings.Fields(ci), " "), strings.Join(strings.Fields(dispatchGuard), " ")) {
		t.Fatal("Quality Gate must retain the complete manual-dispatch guard")
	}
}

func TestAuthoritativeDispatchHasAnIsolatedConcurrencyLane(t *testing.T) {
	ci, err := os.ReadFile("../../workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	group := "group: codex-review-${{ github.event.pull_request.number || inputs.changelog_branch || github.ref_name }}-${{ inputs.changelog_head || github.event.pull_request.head.sha || github.sha }}-${{ github.event_name == 'workflow_dispatch' && format('dispatch-{0}', github.run_id) || 'pull-request' }}"
	if !strings.Contains(string(ci), group) {
		t.Fatal("Codex review concurrency must isolate each authoritative dispatch from pull-request events")
	}

	key := func(event, runID, head string) string {
		lane := "pull-request"
		if event == "workflow_dispatch" {
			lane = "dispatch-" + runID
		}
		return "chore/changelog-1.2.3-" + head + "-" + lane
	}
	for _, tt := range []struct {
		name                  string
		first, second         string
		firstRun, secondRun   string
		firstHead, secondHead string
		wantSame              bool
	}{
		{"pull request before dispatch", "pull_request", "workflow_dispatch", "11", "22", testHead, testHead, false},
		{"dispatch before pull request", "workflow_dispatch", "pull_request", "22", "11", testHead, testHead, false},
		{"duplicate edited events", "pull_request", "pull_request", "11", "12", testHead, testHead, true},
		{"authoritative rerun", "workflow_dispatch", "workflow_dispatch", "22", "22", testHead, testHead, true},
		{"head replacement", "pull_request", "pull_request", "11", "12", testHead, strings.Repeat("d", 40), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			same := key(tt.first, tt.firstRun, tt.firstHead) == key(tt.second, tt.secondRun, tt.secondHead)
			if same != tt.wantSame {
				t.Fatalf("same group=%v, want %v", same, tt.wantSame)
			}
		})
	}

	release, err := os.ReadFile("../../workflows/auto-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(release), `bash .github/scripts/verify-authoritative-run.sh`) || !strings.Contains(string(release), `"$GITHUB_REPOSITORY" "$pr_url" "$head_sha" "$ci_run"`) {
		t.Fatal("release workflow must pass exact identities to the authoritative verifier")
	}
	verifier, err := os.ReadFile("../verify-authoritative-run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`timeout 240m gh run watch "$run_id" --repo "$repo" --exit-status`,
		`gh api --paginate "repos/${repo}/actions/runs/${run_id}/jobs?per_page=100" |`,
		`jq -sr '[.[].jobs[] |`,
		`"$gate_conclusion" != "success"`,
		`select(.name == "Quality Gate")] | if length == 1 then .[0].conclusion else "ambiguous" end`,
		`"$actual_head" != "$expected_head"`,
		`PR $pr_url expected head $expected_head authoritative run $run_id`,
		`Inspect $run_url.`,
	} {
		if !strings.Contains(string(verifier), required) {
			t.Fatalf("authoritative run verifier missing %q", required)
		}
	}
}

func markedRequest(login string, at time.Time) reviewComment {
	return reviewComment{Body: "@codex review\n" + requestMarker("codex", testHead), Author: actor{login}, CreatedAt: at, URL: "https://github.com/HemSoft/gh-x/pull/12#issuecomment-request"}
}

func TestRefusalThenConnectedSuccess(t *testing.T) {
	state := cleanState()
	clean := state.Comments.Nodes[0]
	bot := markedRequest("github-actions", clean.CreatedAt.Add(-3*time.Minute))
	refusal := reviewComment{Body: "To use Codex here, create a Codex account and connect to github.", Author: actor{"chatgpt-codex-connector"}, CreatedAt: bot.CreatedAt.Add(time.Second), URL: "https://github.com/HemSoft/gh-x/pull/12#issuecomment-refusal"}
	human := markedRequest("HemSoft", clean.CreatedAt.Add(-time.Minute))
	state.Comments.Nodes = []reviewComment{bot, refusal, human, clean}
	for i := 0; i < 2; i++ {
		if err := waitForReview(context.Background(), reviewFixture(t, &state), testConfig, "12", true); err != nil {
			t.Fatalf("verification rerun must reuse the clean connected result: %v", err)
		}
	}
}

func TestRefusalsReportActionableContext(t *testing.T) {
	for _, message := range []string{"create a Codex account and connect to github", "Codex usage limit reached", "upgrade to a paid plan", "Code review is not enabled", "permission denied"} {
		t.Run(message, func(t *testing.T) {
			state := cleanState()
			request := markedRequest("HemSoft", time.Now().Add(-time.Minute))
			response := reviewComment{Body: message, Author: actor{"chatgpt-codex-connector"}, CreatedAt: time.Now(), URL: "https://github.com/HemSoft/gh-x/pull/12#issuecomment-refusal"}
			state.Comments.Nodes = []reviewComment{request, response}
			err := pendingReview(state, testConfig, "12", time.Now())
			if err == nil {
				t.Fatal("explicit refusal must block")
			}
			for _, text := range []string{"PR #12", testHead, "HemSoft", response.URL, request.URL, "refused"} {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("missing %s in %v", text, err)
				}
			}
		})
	}
}

func TestRequestDeadlineSurvivesDuplicateJobsAndTriggers(t *testing.T) {
	state := cleanState()
	start := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	first := markedRequest("HemSoft", start)
	duplicate := markedRequest("HemSoft", start.Add(29*time.Minute))
	state.Comments.Nodes = []reviewComment{first, duplicate}
	for _, elapsed := range []time.Duration{21 * time.Minute, 30*time.Minute - time.Nanosecond} {
		if err := pendingReview(state, testConfig, "12", start.Add(elapsed)); err != nil {
			t.Fatalf("review should still be pending before thirty minutes: %v", err)
		}
	}
	for _, elapsed := range []time.Duration{30 * time.Minute, 40 * time.Minute} {
		if err := pendingReview(state, testConfig, "12", start.Add(elapsed)); err == nil || !strings.Contains(err.Error(), "timed out at 2026-09-05T12:30:00Z") {
			t.Fatalf("duplicate job must retain original terminal timeout: %v", err)
		}
	}
}

func TestSetupWindowDoesNotRestart(t *testing.T) {
	state := cleanState()
	state.Comments.Nodes = nil
	state.CommittedAt = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if err := pendingReview(state, testConfig, "12", state.CommittedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := pendingReview(state, testConfig, "12", state.CommittedAt.Add(10*time.Minute)); err == nil {
		t.Fatal("a rerun must not create a new setup window")
	}
}

func runningActivity(started time.Time) string {
	return `<!-- codex-pull-request-review-summary -->` + "\n| 📝 **Code Review** | 🔄 **Running** since <relative-time datetime=\"" + started.Format(time.RFC3339Nano) + `">now</relative-time> | ` + "`" + testHead[:7] + "` | Manual request |"
}

func TestOrdinaryPendingReviewUsesCurrentHeadActivity(t *testing.T) {
	committed := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	started := committed.Add(time.Minute)
	activity := reviewComment{
		Body:      runningActivity(started),
		Author:    actor{"chatgpt-codex-connector"},
		CreatedAt: committed.Add(-time.Hour), // The connector updates one long-lived summary comment.
		URL:       "https://github.com/HemSoft/gh-x/pull/12#issuecomment-activity",
	}
	state := reviewState{HeadRefOID: testHead, CommittedAt: committed}
	state.Comments.Nodes = []reviewComment{activity}
	state.TimelineItems.Nodes = []timelineItem{{TypeName: "PullRequestCommit", Commit: struct{ OID string }{testHead}}}
	for _, elapsed := range []time.Duration{21 * time.Minute, 30*time.Minute - time.Nanosecond} {
		if err := pendingOrdinaryReview(state, testConfig, "12", started.Add(elapsed)); err != nil {
			t.Fatalf("review should still be pending before thirty minutes: %v", err)
		}
	}
	if err := pendingOrdinaryReview(state, testConfig, "12", started.Add(30*time.Minute)); err == nil || !strings.Contains(err.Error(), "timed out at 2026-09-05T12:31:00Z") {
		t.Fatalf("rerun must retain the current-head activity deadline: %v", err)
	}

	state.Comments.Nodes = nil
	if err := pendingOrdinaryReview(state, testConfig, "12", committed.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "request once with @codex review") {
		t.Fatalf("missing ordinary activity must be actionable: %v", err)
	}
	state.CommittedAt = time.Time{}
	if err := pendingOrdinaryReview(state, testConfig, "12", committed); err == nil || !strings.Contains(err.Error(), "review setup lacks a timestamp") {
		t.Fatalf("missing setup timestamp must fail closed: %v", err)
	}
}

func TestOrdinarySetupWindowUsesTriggerTimestamp(t *testing.T) {
	committed := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	triggered := committed.Add(time.Hour)
	state := reviewState{HeadRefOID: testHead, CommittedAt: committed}
	cfg := testConfig
	cfg.setup = triggered.Format(time.RFC3339Nano)
	if err := pendingOrdinaryReview(state, cfg, "12", triggered.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := pendingOrdinaryReview(state, cfg, "12", triggered.Add(10*time.Minute)); err == nil || !strings.Contains(err.Error(), "no current-head Codex activity") {
		t.Fatalf("event-bound setup window must not reset: %v", err)
	}
	cfg.setup = "invalid"
	if err := pendingOrdinaryReview(state, cfg, "12", triggered); err == nil || !strings.Contains(err.Error(), "invalid review setup timestamp") {
		t.Fatalf("invalid event timestamp must fail closed: %v", err)
	}
}

func TestCompletedSummaryIsCleanOnlyWithoutExactFindings(t *testing.T) {
	completed := time.Now().Add(-time.Minute)
	state := cleanState()
	state.Comments.Nodes = []reviewComment{{
		Body: strings.Replace(runningActivity(completed), "🔄 **Running** since", "✅ **Completed**", 1), Author: actor{"chatgpt-codex-connector"}, URL: "summary-url",
	}}
	ready, requested, err := ordinaryReviewReady(state, testHead)
	if !ready || !requested || err != nil {
		t.Fatalf("completed finding-free summary must be clean: %v,%v,%v", ready, requested, err)
	}
	finding := review{State: "COMMENTED", SubmittedAt: completed.Add(-time.Second), Author: actor{"chatgpt-codex-connector"}}
	finding.Commit.OID = testHead
	state.Reviews.Nodes = []review{finding}
	ready, requested, err = ordinaryReviewReady(state, testHead)
	if ready || !requested || err != nil {
		t.Fatalf("completion status alone must not clear a finding: %v,%v,%v", ready, requested, err)
	}
	request := markedRequest("HemSoft", completed.Add(-time.Second/2))
	state.TimelineItems.Nodes = append(state.TimelineItems.Nodes, timelineItem{
		TypeName: "IssueComment", Body: request.Body, URL: request.URL, CreatedAt: request.CreatedAt, Author: request.Author,
	})
	ready, requested, err = ordinaryReviewReady(state, testHead)
	if !ready || !requested || err != nil {
		t.Fatalf("completed rerun after an old resolved finding must recover: %v,%v,%v", ready, requested, err)
	}
	state.Reviews.Nodes[0].SubmittedAt = completed.Add(time.Second)
	ready, requested, err = ordinaryReviewReady(state, testHead)
	if ready || !requested || err != nil {
		t.Fatalf("newer exact finding must override completed summary: %v,%v,%v", ready, requested, err)
	}
}

func TestCompletedSummaryDoesNotClearSameRunCommentFinding(t *testing.T) {
	completed := time.Now().Add(-time.Minute)
	state := cleanState()
	finding := state.Comments.Nodes[0]
	finding.Body = "Codex found an issue.\nReviewed commit: `" + testHead + "`"
	finding.CreatedAt = completed.Add(-time.Second)
	state.Comments.Nodes = []reviewComment{finding, {
		Body: strings.Replace(runningActivity(completed), "🔄 **Running** since", "✅ **Completed**", 1), Author: actor{"chatgpt-codex-connector"}, URL: "summary-url",
	}}
	state.TimelineItems.Nodes[1].Body = finding.Body
	state.TimelineItems.Nodes[1].CreatedAt = finding.CreatedAt
	ready, requested, err := ordinaryReviewReady(state, testHead)
	if ready || !requested || err != nil {
		t.Fatalf("same-run comment finding must survive completion: %v,%v,%v", ready, requested, err)
	}
}

func TestCurrentHeadSummaryAllowsOlderCommentsToBeTruncated(t *testing.T) {
	completed := time.Now().Add(-time.Minute)
	state := cleanState()
	state.Comments.Nodes = []reviewComment{{
		Body: strings.Replace(runningActivity(completed), "🔄 **Running** since", "✅ **Completed**", 1), Author: actor{"chatgpt-codex-connector"}, URL: "summary-url",
	}}
	state.Comments.PageInfo.HasPreviousPage = true
	ready, requested, err := ordinaryReviewReady(state, testHead)
	if !ready || !requested || err != nil {
		t.Fatalf("validated current-head summary must make older comments irrelevant: %v,%v,%v", ready, requested, err)
	}
	state.Comments.Nodes = cleanState().Comments.Nodes
	ready, _, err = ordinaryReviewReady(state, testHead)
	if ready || err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated comments without a current-head summary must fail: %v,%v", ready, err)
	}
}

func TestRunningSummarySupersedesExactApproval(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	state := cleanState()
	state.Comments.Nodes = []reviewComment{{Body: runningActivity(started), Author: actor{"chatgpt-codex-connector"}, URL: "summary-url"}}
	approval := review{State: "APPROVED", SubmittedAt: started.Add(-time.Second), Author: actor{"chatgpt-codex-connector"}}
	approval.Commit.OID = testHead
	state.Reviews.Nodes = []review{approval}
	ready, requested, err := ordinaryReviewReady(state, testHead)
	if ready || !requested || err != nil {
		t.Fatalf("newer running summary must supersede approval: %v,%v,%v", ready, requested, err)
	}
}

func TestOrdinaryReviewRejectsMissingTimestampWithApproval(t *testing.T) {
	state := cleanState()
	state.Comments.Nodes[0].CreatedAt = time.Time{}
	approval := review{State: "APPROVED", SubmittedAt: time.Now(), Author: actor{"chatgpt-codex-connector"}}
	approval.Commit.OID = testHead
	state.Reviews.Nodes = []review{approval}
	ready, _, err := ordinaryReviewReady(state, testHead)
	if ready || err == nil || !strings.Contains(err.Error(), "lacks a timestamp") {
		t.Fatalf("missing-timestamp receipt must fail closed despite approval: %v,%v", ready, err)
	}
}

func TestOrdinaryActivityTimeIgnoresProseAndRejectsAmbiguity(t *testing.T) {
	started := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	comment := reviewComment{
		Body:   runningActivity(started) + "\nQuoted example: <relative-time datetime=\"2099-01-01T00:00:00Z\">",
		Author: actor{"chatgpt-codex-connector"},
		URL:    "activity-url",
	}
	state := reviewState{HeadRefOID: testHead}
	state.TimelineItems.Nodes = []timelineItem{{TypeName: "PullRequestCommit", Commit: struct{ OID string }{testHead}}}
	state.Comments.Nodes = []reviewComment{comment}
	activity, err := currentHeadOrdinaryCodexActivity(state, testHead)
	if err != nil || !activity.CreatedAt.Equal(started) {
		t.Fatalf("activity=%v err=%v", activity.CreatedAt, err)
	}
	state.Comments.Nodes[0].Body += "\n| 📝 **Code Review** | completed <relative-time datetime=\"2099-01-01T00:00:00Z\">later</relative-time> | `" + testHead[:7] + "` | Manual request |"
	if _, err := currentHeadOrdinaryCodexActivity(state, testHead); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate activity rows must fail closed: %v", err)
	}
}

func TestSameTimeReviewsWithDifferentStatesFailClosed(t *testing.T) {
	stamp := time.Now()
	first := reviewComment{Body: "same review", Author: actor{"chatgpt-codex-connector"}, CreatedAt: stamp}
	second := first
	second.Clean = true
	if _, err := latestCodexActivity([]reviewComment{first, second}); err == nil || !strings.Contains(err.Error(), "shares a timestamp") {
		t.Fatalf("same-time review state conflict must fail closed: %v", err)
	}
}

func TestOrdinaryDistinctSameTimeEvidenceFailsClosed(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	state := cleanState()
	state.CommittedAt = started.Add(-time.Minute)
	state.Comments.Nodes[0].CreatedAt = started
	state.Comments.Nodes = append(state.Comments.Nodes, reviewComment{
		Body: runningActivity(started), Author: actor{"chatgpt-codex-connector"}, URL: "activity-url",
	})
	ready, err := currentOrdinaryRequestAllowsClean(state, testConfig, "12")
	if ready || err == nil || !strings.Contains(err.Error(), "shares a timestamp") {
		t.Fatalf("same-time distinct evidence must fail closed: %v,%v", ready, err)
	}
}

func TestOrdinaryLaterRequestIsBoundByTimelineOrder(t *testing.T) {
	cleanAt := time.Now().Add(-2 * time.Minute)
	state := cleanState()
	state.Comments.Nodes[0].CreatedAt = cleanAt
	state.TimelineItems.Nodes = append(state.TimelineItems.Nodes,
		timelineItem{TypeName: "IssueComment", Body: "@codex review", URL: "request-url", CreatedAt: cleanAt.Add(time.Minute), Author: actor{connectedRequester}},
	)
	ready, err := currentOrdinaryRequestAllowsClean(state, testConfig, "12")
	if ready || err != nil {
		t.Fatalf("later same-head request must remain pending: %v,%v", ready, err)
	}
}

func TestOrdinarySameTimeRequestWaits(t *testing.T) {
	stamp := time.Now().Add(-time.Minute)
	state := cleanState()
	state.Comments.Nodes[0].CreatedAt = stamp
	state.TimelineItems.Nodes = append(state.TimelineItems.Nodes,
		timelineItem{TypeName: "IssueComment", Body: "@codex review", URL: "request-url", CreatedAt: stamp, Author: actor{connectedRequester}},
	)
	ready, err := currentOrdinaryRequestAllowsClean(state, testConfig, "12")
	if ready || err != nil {
		t.Fatalf("same-time bound request must wait rather than error: %v,%v", ready, err)
	}
}

func TestOrdinaryRequestBeforeLatestHeadBoundaryIsIgnored(t *testing.T) {
	cleanAt := time.Now().Add(-2 * time.Minute)
	state := cleanState()
	state.Comments.Nodes[0].CreatedAt = cleanAt
	state.TimelineItems.Nodes = append(state.TimelineItems.Nodes,
		timelineItem{TypeName: "IssueComment", Body: "@codex review", URL: "old-request", CreatedAt: cleanAt.Add(time.Minute), Author: actor{connectedRequester}},
		timelineItem{TypeName: "HeadRefForcePushedEvent", CreatedAt: cleanAt.Add(90 * time.Second), AfterCommit: struct{ OID string }{testHead}},
	)
	ready, err := currentOrdinaryRequestAllowsClean(state, testConfig, "12")
	if !ready || err != nil {
		t.Fatalf("request before the current head update must not become pending: %v,%v", ready, err)
	}
}

func TestOrdinaryReceiptPrefixCannotCrossHeadBoundary(t *testing.T) {
	state := cleanState()
	collision := testHead[:10] + "ffffffffffffffffffffffffffffff"
	state.HeadRefOID = collision
	state.TimelineItems.Nodes = append(state.TimelineItems.Nodes, timelineItem{
		TypeName: "HeadRefForcePushedEvent", CreatedAt: state.Comments.Nodes[0].CreatedAt.Add(time.Minute), AfterCommit: struct{ OID string }{collision},
	})
	cfg := testConfig
	cfg.head = collision
	ready, err := currentOrdinaryRequestAllowsClean(state, cfg, "12")
	if ready || err == nil || !strings.Contains(err.Error(), "distinct pull request heads") {
		t.Fatalf("abbreviated old receipt must not validate a colliding head: %v,%v", ready, err)
	}
}

func TestExactApprovalRecoversCollidingReceipt(t *testing.T) {
	state := cleanState()
	collision := testHead[:10] + "dddddddddddddddddddddddddddddd"
	state.HeadRefOID = collision
	state.TimelineItems.Nodes = append(state.TimelineItems.Nodes, timelineItem{
		TypeName: "HeadRefForcePushedEvent", CreatedAt: state.Comments.Nodes[0].CreatedAt.Add(time.Minute), AfterCommit: struct{ OID string }{collision},
	})
	approval := review{State: "APPROVED", SubmittedAt: state.Comments.Nodes[0].CreatedAt.Add(2 * time.Minute), Author: actor{"chatgpt-codex-connector"}}
	approval.Commit.OID = collision
	state.Reviews.Nodes = []review{approval}
	clean, latest, requested, err := ordinaryCodexEvidence(state, collision)
	if err != nil || clean.IsZero() || !latest.IsZero() || !requested {
		t.Fatalf("exact approval must recover ambiguous receipt: clean=%v latest=%v requested=%v err=%v", clean, latest, requested, err)
	}
}

func TestOrdinaryLateReceiptCannotCrossCollidingHeadBoundary(t *testing.T) {
	state := cleanState()
	collision := testHead[:10] + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	state.HeadRefOID = collision
	state.TimelineItems.Nodes = []timelineItem{
		{TypeName: "PullRequestCommit", Commit: struct{ OID string }{testHead}},
		{TypeName: "HeadRefForcePushedEvent", CreatedAt: state.Comments.Nodes[0].CreatedAt.Add(-time.Minute), AfterCommit: struct{ OID string }{collision}},
		{TypeName: "IssueComment", Body: state.Comments.Nodes[0].Body, URL: state.Comments.Nodes[0].URL, CreatedAt: state.Comments.Nodes[0].CreatedAt, Author: state.Comments.Nodes[0].Author},
	}
	cfg := testConfig
	cfg.head = collision
	ready, err := currentOrdinaryRequestAllowsClean(state, cfg, "12")
	if ready || err == nil || !strings.Contains(err.Error(), "distinct pull request heads") {
		t.Fatalf("late old-head receipt must not validate a colliding head: %v,%v", ready, err)
	}
}

func TestOrdinaryUnboundRequestFailsClosed(t *testing.T) {
	state := cleanState()
	state.Comments.Nodes = nil
	approval := review{State: "APPROVED", SubmittedAt: time.Now().Add(-time.Minute), Author: actor{"chatgpt-codex-connector"}}
	approval.Commit.OID = testHead
	state.Reviews.Nodes = []review{approval}
	state.TimelineItems.Nodes = []timelineItem{{TypeName: "IssueComment", Body: "@codex review", CreatedAt: time.Now(), Author: actor{connectedRequester}}}
	ready, err := currentOrdinaryRequestAllowsClean(state, testConfig, "12")
	if ready || err == nil || !strings.Contains(err.Error(), "cannot be bound") {
		t.Fatalf("unbound ordinary request must fail closed: %v,%v", ready, err)
	}
}

func TestOrdinaryRefusalAndNewActivitySupersedeOldClean(t *testing.T) {
	committed := time.Now().Add(-5 * time.Minute)
	state := cleanState()
	state.CommittedAt = committed
	state.Comments.Nodes[0].CreatedAt = committed.Add(time.Minute)
	started := committed.Add(2 * time.Minute)
	activity := reviewComment{
		Body:      runningActivity(started),
		Author:    actor{"chatgpt-codex-connector"},
		CreatedAt: committed.Add(-time.Hour),
		URL:       "activity-url",
	}
	state.Comments.Nodes = append(state.Comments.Nodes, activity)
	ready, err := currentOrdinaryRequestAllowsClean(state, testConfig, "12")
	if ready || err != nil {
		t.Fatalf("newer current-head activity must supersede old clean evidence: %v,%v", ready, err)
	}
	refusal := reviewComment{Body: "permission denied", Author: actor{"chatgpt-codex-connector"}, CreatedAt: committed.Add(3 * time.Minute), URL: "response-url"}
	state.Comments.Nodes = append(state.Comments.Nodes, refusal)
	if err := pendingOrdinaryReview(state, testConfig, "12", committed.Add(4*time.Minute)); err == nil || !strings.Contains(err.Error(), "response-url") {
		t.Fatalf("ordinary refusal must fail immediately with context: %v", err)
	}
	clean := cleanState().Comments.Nodes[0]
	clean.CreatedAt = committed.Add(4 * time.Minute)
	state.Comments.Nodes = append(state.Comments.Nodes, clean)
	ready, err = currentOrdinaryRequestAllowsClean(state, testConfig, "12")
	if !ready || err != nil {
		t.Fatalf("later clean evidence must supersede the refusal: %v,%v", ready, err)
	}
}

func TestVerifierBudgetIncludesLateRequest(t *testing.T) {
	processStart := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	requestPosted := processStart.Add(9 * time.Minute)
	requestDeadline := requestPosted.Add(30 * time.Minute)
	processDeadline := processStart.Add(executionTimeout([]string{"review"}))
	if !processDeadline.After(requestDeadline) {
		t.Fatalf("setup consumed the review window: process expires %s, request expires %s", processDeadline, requestDeadline)
	}
}

func TestPassiveReviewCannotClearAnUnknownTimestamp(t *testing.T) {
	state := cleanState()
	finding := review{State: "CHANGES_REQUESTED", Author: actor{"cubic-dev-ai"}}
	finding.Commit.OID = testHead
	approval := finding
	approval.State, approval.SubmittedAt = "APPROVED", time.Now()
	state.Reviews.Nodes = []review{finding, approval}
	ready, _, _ := reviewReady(state, testHead)
	if ready {
		t.Fatal("missing finding timestamp must remain outstanding")
	}
}

func TestReviewTextIsNotAnAccessRefusal(t *testing.T) {
	for _, body := range []string{"Codex Review: Didn't find any major issues.\nReviewed commit: " + testHead + "\nQuota accounting looks correct.", "The quota variable is unused.", "Consider handling permission denied in this function."} {
		if got := refusalCorrection(body); got != "" {
			t.Fatalf("ordinary review misclassified: %s", body)
		}
	}
}

func TestRefusalAllowsExplanatoryPrefix(t *testing.T) {
	for _, body := range []string{"Sorry, you've reached your Codex usage limit.", "I couldn't start the review. To use Codex here, create a Codex account and connect to github.", "The review cannot continue: quota exceeded.", "Sorry, but you've reached your Codex usage limit.", "I can't start the review because permission denied."} {
		if refusalCorrection(body) == "" {
			t.Fatalf("missed explicit refusal with explanatory prose: %s", body)
		}
	}
}

func TestRefusalMustFollowItsOwnRequest(t *testing.T) {
	state := cleanState()
	request := markedRequest("HemSoft", time.Now().Add(-time.Minute))
	for _, at := range []time.Time{{}, request.CreatedAt} {
		state.Comments.Nodes = []reviewComment{request, {Body: "quota exceeded", Author: actor{"chatgpt-codex-connector"}, CreatedAt: at, URL: "response-url"}}
		if _, _, err := requestRefusal(state, request); err == nil {
			t.Fatal("missing or tied response timestamps must be ambiguous")
		}
	}
	otherHead := markedRequest("HemSoft", request.CreatedAt.Add(time.Second))
	otherHead.Body = strings.ReplaceAll(otherHead.Body, testHead, strings.Repeat("f", 40))
	state.Comments.Nodes = []reviewComment{request, otherHead, {Body: "quota exceeded", Author: actor{"chatgpt-codex-connector"}, CreatedAt: otherHead.CreatedAt.Add(time.Second)}}
	if _, correction, err := requestRefusal(state, request); correction != "" || err != nil {
		t.Fatalf("response to another request must not be attributed to this head: %s,%v", correction, err)
	}
}

func TestRequesterAccessUsesSelectedRepository(t *testing.T) {
	cfg := testConfig
	cfg.repo = "HemSoft/another-repo"
	gh := func(args ...string) ([]byte, error) {
		if args[1] == "user" {
			return []byte(`{"login":"HemSoft","type":"User"}`), nil
		}
		if args[1] != "repos/"+cfg.repo {
			t.Fatalf("checked wrong repository: %v", args)
		}
		return []byte(`{"permissions":{"push":true}}`), nil
	}
	if err := verifyRequester(gh, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestRefusalDoesNotOverrideLaterHeadReceipt(t *testing.T) {
	state := cleanState()
	clean := state.Comments.Nodes[0]
	request := markedRequest("github-actions", clean.CreatedAt.Add(-time.Minute))
	state.Comments.Nodes = append(state.Comments.Nodes, request, reviewComment{Body: "create a Codex account", Author: actor{"chatgpt-codex-connector"}, CreatedAt: request.CreatedAt.Add(time.Second)})
	ready, err := pollReview(reviewFixture(t, &state), testConfig, "12", true)
	if !ready || err != nil {
		t.Fatalf("PR 78 legacy unmarked human recovery must accept its explicit head receipt: %v,%v", ready, err)
	}
	state.Comments.Nodes = append(state.Comments.Nodes, markedRequest("HemSoft", time.Now()))
	ready, err = pollReview(reviewFixture(t, &state), testConfig, "12", true)
	if ready || err != nil {
		t.Fatalf("new current-head request supersedes old clean receipt: %v,%v", ready, err)
	}
}

func TestSerializedRequestInvocationsPostOnce(t *testing.T) {
	state := cleanState()
	state.Comments.Nodes = nil
	read := reviewFixture(t, &state)
	writes := 0
	gh := func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "api" && args[1] == "user":
			return []byte(`{"login":"HemSoft","type":"User"}`), nil
		case args[0] == "api" && args[1] == "repos/HemSoft/gh-x":
			return []byte(`{"permissions":{"push":true}}`), nil
		case args[0] == "pr":
			writes++
			if args[1] != "comment" || !strings.Contains(strings.Join(args, " "), requestMarker("codex", testHead)) {
				t.Fatalf("unexpected mutation %v", args)
			}
			state.Comments.Nodes = append(state.Comments.Nodes, markedRequest("HemSoft", time.Now()))
			return []byte(state.Comments.Nodes[0].URL), nil
		default:
			return read(args...)
		}
	}
	// Auto Release's non-canceling concurrency group serializes these callers;
	// all deduplication state here comes back from GitHub, not process memory.
	for i := 0; i < 3; i++ {
		if err := ensureRequest(gh, testConfig, "12"); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 {
		t.Fatalf("expected one persisted trigger, got %d", writes)
	}
}

func TestUnsupportedRequesterNeverPosts(t *testing.T) {
	for _, identity := range []string{`{"login":"github-actions[bot]","type":"Bot"}`, `{"login":"other","type":"User"}`, `{"login":"HemSoft","type":"Bot"}`, `null`} {
		state := cleanState()
		state.Comments.Nodes = nil
		read := reviewFixture(t, &state)
		gh := func(args ...string) ([]byte, error) {
			if args[0] == "api" && args[1] == "user" {
				return []byte(identity), nil
			}
			return read(args...)
		}
		if err := ensureRequest(gh, testConfig, "12"); err == nil {
			t.Fatalf("unsupported identity accepted: %s", identity)
		}
	}
	gh := func(...string) ([]byte, error) { return nil, errors.New("token-secret must not appear") }
	if err := verifyRequester(gh, testConfig); err == nil || strings.Contains(err.Error(), "token-secret") {
		t.Fatalf("authentication error must be actionable and redacted: %v", err)
	}
}

func TestTerminalRequestNeverRetriggers(t *testing.T) {
	state := cleanState()
	state.Comments.Nodes = []reviewComment{markedRequest("HemSoft", time.Now().Add(-time.Hour))}
	for i := 0; i < 2; i++ {
		if err := ensureRequest(reviewFixture(t, &state), testConfig, "12"); err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("terminal timeout must not retrigger: %v", err)
		}
	}
	state.Comments.Nodes = append(state.Comments.Nodes, reviewComment{Body: "quota exceeded", Author: actor{"chatgpt-codex-connector"}, CreatedAt: time.Now()})
	if err := ensureRequest(reviewFixture(t, &state), testConfig, "12"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("terminal refusal must not retrigger: %v", err)
	}
}

func TestLateCompletionAddsEvidenceWithoutRestartingWait(t *testing.T) {
	state := cleanState()
	request := markedRequest("HemSoft", time.Now().Add(-time.Hour))
	state.Comments.Nodes = []reviewComment{request}
	gh := reviewFixture(t, &state) // Any request/comment mutation fails this fixture.
	if err := waitForReview(context.Background(), gh, testConfig, "12", true); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("absent evidence must retain the expired deadline: %v", err)
	}
	clean := cleanState().Comments.Nodes[0]
	clean.CreatedAt = time.Now()
	state.Comments.Nodes = append(state.Comments.Nodes, clean)
	if err := waitForReview(context.Background(), gh, testConfig, "12", true); err != nil {
		t.Fatalf("genuine later head-specific completion needs no new wait: %v", err)
	}
	if !requestStart(state, request, testHead).Equal(request.CreatedAt) {
		t.Fatal("new evidence must not change the original request/deadline")
	}
}

func TestCodexAlonePassesWithoutOptionalProducts(t *testing.T) {
	state := cleanState()
	ready, err := pollReview(reviewFixture(t, &state), testConfig, "12", true)
	if err != nil || !ready {
		t.Fatalf("clean Codex-only review must pass: %v, %v", ready, err)
	}
}

func TestRefusalIsImmediate(t *testing.T) {
	state := cleanState()
	state.Comments.Nodes = []reviewComment{
		{Body: "@codex review\n" + requestMarker("codex", testHead), Author: actor{"github-actions"}, CreatedAt: time.Now().Add(-time.Minute)},
		{Body: "To use Codex here, create a Codex account and connect to github.", Author: actor{"chatgpt-codex-connector"}, CreatedAt: time.Now()},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := waitForReview(ctx, reviewFixture(t, &state), testConfig, "12", true)
	if err == nil || !strings.Contains(err.Error(), "refused") || ctx.Err() != nil {
		t.Fatalf("must report refusal before waiting: %v", err)
	}
}
