package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const testHead = "0123456789abcdef0123456789abcdef01234567"

var testConfig = config{repo: "HemSoft/gh-x", branch: "chore/changelog-1.2.3", head: testHead}

func validPR() pullRequest {
	var pr pullRequest
	pr.Number = 12
	pr.State = "open"
	pr.ChangedFiles = 1
	pr.User.Login = "github-actions[bot]"
	pr.User.Type = "Bot"
	pr.Head.Ref = testConfig.branch
	pr.Head.SHA = testHead
	pr.Head.Repo.FullName = testConfig.repo
	pr.Base.Ref = "main"
	pr.Base.Repo.FullName = testConfig.repo
	return pr
}

func TestEligibility(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*pullRequest, *[]changedFile)
		wantError bool
	}{
		{"eligible", func(*pullRequest, *[]changedFile) {}, false},
		{"human author", func(p *pullRequest, _ *[]changedFile) { p.User.Login = "HemSoft" }, true},
		{"spoofed bot type", func(p *pullRequest, _ *[]changedFile) { p.User.Type = "User" }, true},
		{"fork", func(p *pullRequest, _ *[]changedFile) { p.Head.Repo.FullName = "fork/gh-x" }, true},
		{"other base", func(p *pullRequest, _ *[]changedFile) { p.Base.Ref = "develop" }, true},
		{"draft", func(p *pullRequest, _ *[]changedFile) { p.Draft = true }, true},
		{"closed", func(p *pullRequest, _ *[]changedFile) { p.State = "closed" }, true},
		{"changed head", func(p *pullRequest, _ *[]changedFile) { p.Head.SHA = strings.Repeat("f", 40) }, true},
		{"other branch", func(p *pullRequest, _ *[]changedFile) { p.Head.Ref = "feature/foo" }, true},
		{"widened diff", func(p *pullRequest, _ *[]changedFile) { p.ChangedFiles = 2 }, true},
		{"workflow edit", func(_ *pullRequest, f *[]changedFile) { (*f)[0].Filename = ".github/workflows/ci.yml" }, true},
		{"rename", func(_ *pullRequest, f *[]changedFile) { (*f)[0].Status = "renamed" }, true},
		{"empty response", func(_ *pullRequest, f *[]changedFile) { *f = nil }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := validPR()
			files := []changedFile{{"CHANGELOG.md", "modified"}}
			tt.mutate(&pr, &files)
			err := eligiblePullRequest(testConfig, pr)
			if err == nil {
				err = eligibleChangelog(testConfig, pr, files)
			}
			if (err != nil) != tt.wantError {
				t.Fatalf("eligibility error=%v, wantError=%v", err, tt.wantError)
			}
		})
	}
}

func TestOrdinaryPullRequestEligibility(t *testing.T) {
	cfg := config{repo: testConfig.repo, branch: "fix/ordinary", head: testHead, number: "12"}
	pr := validPR()
	pr.User.Login = "HemSoft"
	pr.User.Type = "User"
	pr.Head.Ref = cfg.branch
	if err := eligiblePullRequest(cfg, pr); err != nil {
		t.Fatalf("ordinary same-repository PR should be eligible: %v", err)
	}
	pr.Head.Repo.FullName = "someone/gh-x"
	if err := eligiblePullRequest(cfg, pr); err != nil {
		t.Fatalf("ordinary fork PR should be eligible: %v", err)
	}
	pr.Base.Repo.FullName = "someone/gh-x"
	if err := eligiblePullRequest(cfg, pr); err == nil {
		t.Fatal("PR targeting another repository must fail closed")
	}
}

func TestReviewScopeIsExplicit(t *testing.T) {
	tests := []struct {
		name      string
		cfg       config
		want      bool
		wantError bool
	}{
		{"ordinary scope on changelog-shaped branch", config{branch: testConfig.branch, number: "12", scope: "ordinary"}, false, false},
		{"event number defaults to ordinary", config{branch: testConfig.branch, number: "12"}, false, false},
		{"legacy invocation defaults to changelog", config{branch: testConfig.branch}, true, false},
		{"explicit changelog scope", config{branch: testConfig.branch, number: "12", scope: "changelog"}, true, false},
		{"changelog scope rejects other branch", config{branch: "fix/ordinary", number: "12", scope: "changelog"}, false, true},
		{"unknown scope", config{branch: "fix/ordinary", number: "12", scope: "other"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reviewScope(tt.cfg, "review")
			if got != tt.want || (err != nil) != tt.wantError {
				t.Fatalf("reviewScope=%v,%v; want %v,error=%v", got, err, tt.want, tt.wantError)
			}
		})
	}
}

func TestReviewOrdinaryPullRequestByEventNumber(t *testing.T) {
	cfg := config{repo: testConfig.repo, branch: "fix/ordinary", head: testHead, number: "12", scope: "ordinary"}
	var listed, fetchedFiles, mutated bool
	gh := func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "pr":
			if args[1] == "list" {
				listed = true
			} else {
				mutated = true
			}
			return nil, errors.New("ordinary review must not use gh pr commands")
		case strings.Contains(joined, "/files?"):
			fetchedFiles = true
			return nil, errors.New("ordinary review must not inspect changelog files")
		case args[1] == "graphql":
			return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": cleanState()}}}), nil
		default:
			pr := validPR()
			pr.User.Login = "HemSoft"
			pr.User.Type = "User"
			pr.Head.Ref = cfg.branch
			return encode(t, pr), nil
		}
	}
	if err := run(context.Background(), cfg, []string{"review"}, gh); err != nil {
		t.Fatal(err)
	}
	if listed || fetchedFiles || mutated {
		t.Fatalf("ordinary review escaped read-only event scope: list=%v files=%v mutation=%v", listed, fetchedFiles, mutated)
	}
}

func TestOrdinaryReviewRequiresEventNumber(t *testing.T) {
	cfg := config{repo: testConfig.repo, branch: "fix/ordinary", head: testHead, scope: "ordinary"}
	if err := run(context.Background(), cfg, []string{"review"}, func(...string) ([]byte, error) {
		t.Fatal("invalid ordinary review must fail before GitHub access")
		return nil, nil
	}); err == nil || !strings.Contains(err.Error(), "PULL_REQUEST_NUMBER") {
		t.Fatalf("expected event-number error, got %v", err)
	}
	cfg.number = "not-a-number"
	if err := run(context.Background(), cfg, []string{"review"}, func(...string) ([]byte, error) {
		t.Fatal("invalid PR number must fail before GitHub access")
		return nil, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid pull request number") {
		t.Fatalf("expected invalid-number error, got %v", err)
	}
}

func cleanState() reviewState {
	var state reviewState
	state.HeadRefOID = testHead
	comment := reviewComment{Body: "Codex Review: Didn't find any major issues.\n\n**Reviewed commit:** `" + testHead[:10] + "`", URL: "https://github.com/HemSoft/gh-x/pull/12#issuecomment-clean", Author: actor{"chatgpt-codex-connector"}, CreatedAt: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)}
	state.Comments.Nodes = []reviewComment{comment}
	state.TimelineItems.Nodes = []timelineItem{
		{TypeName: "PullRequestCommit", Commit: struct{ OID string }{testHead}},
		{TypeName: "IssueComment", Body: comment.Body, URL: comment.URL, CreatedAt: comment.CreatedAt, Author: comment.Author},
	}
	return state
}

func TestFetchReviewStatePaginatesTimeline(t *testing.T) {
	newer := timelineConnection{Nodes: []timelineItem{{TypeName: "IssueComment", URL: "newer"}}, PageInfo: pageInfo{HasPreviousPage: true, StartCursor: "cursor"}}
	older := timelineConnection{Nodes: []timelineItem{{TypeName: "PullRequestCommit", Commit: struct{ OID string }{testHead}}}}
	calls := 0
	gh := func(args ...string) ([]byte, error) {
		calls++
		timeline := newer
		if strings.Contains(strings.Join(args, " "), "before=cursor") {
			timeline = older
		}
		return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": testHead, "timelineItems": timeline,
		}}}}), nil
	}
	state, err := fetchReviewState(gh, testConfig, "12")
	if err != nil || calls != 2 || len(state.TimelineItems.Nodes) != 2 || state.TimelineItems.Nodes[0].TypeName != "PullRequestCommit" || state.TimelineItems.PageInfo.HasPreviousPage {
		t.Fatalf("timeline pagination failed: calls=%d state=%+v err=%v", calls, state.TimelineItems, err)
	}
}

func TestFetchReviewStatePaginatesComments(t *testing.T) {
	newer := commentConnection{Nodes: []reviewComment{{URL: "newer"}}, PageInfo: pageInfo{HasPreviousPage: true, StartCursor: "comment-cursor"}}
	older := commentConnection{Nodes: []reviewComment{{URL: "older"}}}
	calls := 0
	gh := func(args ...string) ([]byte, error) {
		calls++
		comments := newer
		if strings.Contains(strings.Join(args, " "), "before=comment-cursor") {
			comments = older
		}
		return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": testHead, "comments": comments,
		}}}}), nil
	}
	state, err := fetchReviewState(gh, testConfig, "12")
	if err != nil || calls != 2 || len(state.Comments.Nodes) != 2 || state.Comments.Nodes[0].URL != "older" || state.Comments.PageInfo.HasPreviousPage {
		t.Fatalf("comment pagination failed: calls=%d state=%+v err=%v", calls, state.Comments, err)
	}
}

func TestPaginationCapsIncludeInitialPage(t *testing.T) {
	comments := commentConnection{PageInfo: pageInfo{HasPreviousPage: true, StartCursor: "cursor"}}
	commentCalls := 0
	commentGH := func(...string) ([]byte, error) {
		commentCalls++
		return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"comments": comments}}}}), nil
	}
	if err := fetchEarlierComments(commentGH, "12", "HemSoft", "gh-x", &comments); err == nil || commentCalls != maxReviewPages-1 {
		t.Fatalf("comment cap excluded initial page: calls=%d err=%v", commentCalls, err)
	}

	timeline := timelineConnection{PageInfo: pageInfo{HasPreviousPage: true, StartCursor: "cursor"}}
	timelineCalls := 0
	timelineGH := func(...string) ([]byte, error) {
		timelineCalls++
		return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"timelineItems": timeline}}}}), nil
	}
	if err := fetchEarlierTimeline(timelineGH, "12", "HemSoft", "gh-x", &timeline); err == nil || timelineCalls != maxReviewPages-1 {
		t.Fatalf("timeline cap excluded initial page: calls=%d err=%v", timelineCalls, err)
	}
}

func TestFetchReviewStateRejectsMissingTimelineCursor(t *testing.T) {
	gh := func(...string) ([]byte, error) {
		timeline := timelineConnection{PageInfo: pageInfo{HasPreviousPage: true}}
		return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": testHead, "timelineItems": timeline,
		}}}}), nil
	}
	if _, err := fetchReviewState(gh, testConfig, "12"); err == nil || !strings.Contains(err.Error(), "pagination is incomplete") {
		t.Fatalf("missing timeline cursor must fail closed: %v", err)
	}
}

func TestReviewEvidence(t *testing.T) {
	tests := []struct {
		name                        string
		mutate                      func(*reviewState)
		ready, requested, wantError bool
	}{
		{"clean receipt", func(*reviewState) {}, true, true, false},
		{"spoofed receipt", func(s *reviewState) { s.Comments.Nodes[0].Author.Login = "HemSoft" }, false, false, false},
		{"stale receipt", func(s *reviewState) {
			s.Comments.Nodes[0].Body = strings.ReplaceAll(s.Comments.Nodes[0].Body, testHead[:10], "aaaaaaaaaa")
		}, false, false, false},
		{"head changed", func(s *reviewState) { s.HeadRefOID = "other" }, false, false, true},
		{"comments truncated", func(s *reviewState) { s.Comments.PageInfo.HasPreviousPage = true }, false, false, true},
		{"reviews truncated", func(s *reviewState) { s.Reviews.PageInfo.HasPreviousPage = true }, false, false, true},
		{"threads truncated", func(s *reviewState) { s.ReviewThreads.PageInfo.HasNextPage = true }, false, false, true},
		{"timeline truncated", func(s *reviewState) { s.TimelineItems.PageInfo.HasPreviousPage = true }, false, false, true},
		{"unresolved conversation", func(s *reviewState) {
			s.ReviewThreads.Nodes = append(s.ReviewThreads.Nodes, struct{ IsResolved bool }{false})
		}, false, true, false},
		{"resolved conversation", func(s *reviewState) {
			s.ReviewThreads.Nodes = append(s.ReviewThreads.Nodes, struct{ IsResolved bool }{true})
		}, true, true, false},
		{"new review running", func(s *reviewState) {
			s.Comments.Nodes = append(s.Comments.Nodes, reviewComment{Author: actor{"chatgpt-codex-connector"}, Body: "<!-- codex-pull-request-review-summary --> `" + testHead[:7] + "` **Running**"})
		}, false, true, false},
		{"tied independent evidence", func(s *reviewState) {
			r := review{Author: actor{"chatgpt-codex-connector"}, State: "COMMENTED", SubmittedAt: s.Comments.Nodes[0].CreatedAt}
			r.Commit.OID = testHead
			s.Reviews.Nodes = []review{r}
		}, false, true, false},
		{"lone approval", func(s *reviewState) {
			r := review{Author: actor{"chatgpt-codex-connector"}, State: "APPROVED", SubmittedAt: s.Comments.Nodes[0].CreatedAt}
			r.Commit.OID = testHead
			s.Comments.Nodes = nil
			s.Reviews.Nodes = []review{r}
		}, true, true, false},
		{"missing review timestamp", func(s *reviewState) {
			r := review{Author: actor{"chatgpt-codex-connector"}, State: "COMMENTED"}
			r.Commit.OID = testHead
			s.Reviews.Nodes = []review{r}
		}, false, true, true},
		{"unquoted receipt", func(s *reviewState) { s.Comments.Nodes[0].Body = strings.ReplaceAll(s.Comments.Nodes[0].Body, "`", "") }, true, true, false},
		{"newer finding", func(s *reviewState) {
			r := review{Author: actor{"chatgpt-codex-connector"}, State: "COMMENTED", SubmittedAt: s.Comments.Nodes[0].CreatedAt.Add(time.Minute)}
			r.Commit.OID = testHead
			s.Reviews.Nodes = append(s.Reviews.Nodes, r)
		}, false, true, false},
		{"spoofed request", func(s *reviewState) {
			s.Comments.Nodes = []reviewComment{{Author: actor{"stranger"}, Body: "<!-- changelog-codex-review-head:" + testHead + " -->"}}
		}, false, false, false},
		{"existing request", func(s *reviewState) {
			s.Comments.Nodes = []reviewComment{{Author: actor{"github-actions[bot]"}, Body: "@codex review\n<!-- changelog-codex-review-head:" + testHead + " -->"}}
		}, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := cleanState()
			tt.mutate(&s)
			ready, requested, err := reviewReady(s, testHead)
			if ready != tt.ready || requested != tt.requested || (err != nil) != tt.wantError {
				t.Fatalf("got (%v,%v,%v), want (%v,%v,error=%v)", ready, requested, err, tt.ready, tt.requested, tt.wantError)
			}
		})
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEnableWaitsForReviewGateAndPinsMerge(t *testing.T) {
	var mergeArgs []string
	reads := 0
	gh := func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "pr" && args[1] == "list":
			return []byte(`[{"number":12}]`), nil
		case strings.Contains(joined, "/files?"):
			return encode(t, []changedFile{{"CHANGELOG.md", "modified"}}), nil
		case args[0] == "api" && args[1] == "graphql":
			return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": cleanState()}}}), nil
		case strings.Contains(joined, "check-runs?per_page"):
			check := checkRun{Status: "completed", Conclusion: "success", HeadSHA: testHead}
			check.Name = "cubic · AI code reviewer"
			check.App.Slug = "cubic-dev-ai"
			check.Output.Summary = "0 issues found"
			return encode(t, map[string]any{"check_runs": []checkRun{check}}), nil
		case strings.Contains(joined, "check-runs?"):
			check := checkRun{Name: "Current-head Codex Review", Status: "completed", Conclusion: "success", HeadSHA: testHead}
			check.App.Slug = "github-actions"
			return encode(t, map[string]any{"check_runs": []checkRun{check}}), nil
		case args[0] == "pr" && args[1] == "merge":
			mergeArgs = append([]string{}, args...)
			return nil, nil
		default:
			reads++
			pr := validPR()
			if len(mergeArgs) > 0 {
				pr.Merged = true
				pr.State = "closed"
			}
			return encode(t, pr), nil
		}
	}
	if err := run(context.Background(), testConfig, []string{"enable"}, gh); err != nil {
		t.Fatal(err)
	}
	want := []string{"pr", "merge", "12", "--repo", "HemSoft/gh-x", "--auto", "--squash", "--match-head-commit", testHead}
	if !reflect.DeepEqual(mergeArgs, want) || reads != 3 {
		t.Fatalf("merge=%v, reads=%d", mergeArgs, reads)
	}
}

func TestAbsentOrFailedReviewGateCannotEnableMerge(t *testing.T) {
	for _, conclusion := range []string{"absent", "failure", "skipped"} {
		t.Run(conclusion, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			gh := func(args ...string) ([]byte, error) {
				if conclusion == "absent" {
					return []byte(`{"check_runs":[]}`), nil
				}
				check := checkRun{Name: "Current-head Codex Review", Status: "completed", Conclusion: conclusion, HeadSHA: testHead}
				check.App.Slug = "github-actions"
				return encode(t, map[string]any{"check_runs": []checkRun{check}}), nil
			}
			if err := waitForReviewGate(ctx, gh, testConfig); err == nil {
				t.Fatal("missing review gate must block")
			}
		})
	}
}

func TestAPIErrorFailsClosed(t *testing.T) {
	gh := func(...string) ([]byte, error) { return nil, errors.New("GitHub unavailable") }
	if err := run(context.Background(), testConfig, []string{"enable"}, gh); err == nil {
		t.Fatal("API failure must block")
	}
}

func TestWorkflowReviewGateUsesTrustedCode(t *testing.T) {
	contents, err := os.ReadFile("../../workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs       []string
			Permissions map[string]string
			Steps       []struct {
				With map[string]string
				Run  string
			}
		}
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["codex-review"]
	if job.Steps[0].With["ref"] != "${{ github.event_name == 'workflow_dispatch' && inputs.changelog_branch != '' && github.sha || github.event.repository.default_branch }}" || job.Steps[0].With["persist-credentials"] != "false" {
		t.Fatal("privileged review must use trusted default branch without persisted credentials")
	}
	if job.Permissions["pull-requests"] != "read" || job.Permissions["contents"] != "read" {
		t.Fatal("review permissions changed")
	}
	if !strings.Contains(strings.Join(workflow.Jobs["gate"].Needs, ","), "codex-review") {
		t.Fatal("Quality Gate must depend on current-head Codex review")
	}
	if !strings.Contains(workflow.Jobs["gate"].Steps[0].Run, "needs.codex-review.result") {
		t.Fatal("Quality Gate must enforce current-head Codex review result")
	}
}

func TestRequestsUseTrustedGraphQLIdentity(t *testing.T) {
	for _, login := range []string{"github-actions", "github-actions[bot]", "stranger"} {
		s := cleanState()
		s.Comments.Nodes = []reviewComment{{Author: actor{login}, Body: requestMarker("cubic", testHead)}}
		if got := wasRequested(s, "cubic", testHead); got != (login != "stranger") {
			t.Fatalf("trusted request for %s: %v", login, got)
		}
		if wasRequested(s, "codex", testHead) {
			t.Fatal("Cubic marker must not suppress Codex")
		}
	}
}

func TestMergeBudgetOutlivesRequiredGates(t *testing.T) {
	contents, err := os.ReadFile("../../workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			TimeoutMinutes int `yaml:"timeout-minutes"`
		}
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	jobBudget := time.Duration(workflow.Jobs["codex-review"].TimeoutMinutes) * time.Minute
	verifier, err := os.ReadFile("../verify-authoritative-run.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, watch, found := strings.Cut(string(verifier), "timeout ")
	if !found {
		t.Fatal("authoritative verifier must have a bounded wait")
	}
	watchLimit, _, _ := strings.Cut(watch, " ")
	watchBudget, err := time.ParseDuration(watchLimit)
	if err != nil || watchBudget <= jobBudget {
		t.Fatal("authoritative verifier must outlive the review CI job")
	}
	if executionTimeout([]string{"enable"}) <= 2*jobBudget {
		t.Fatal("merge must outlive two queued review jobs")
	}
	if executionTimeout([]string{"review"}) >= jobBudget {
		t.Fatal("review must finish within its CI job budget")
	}
	// Auto Release watches the whole authoritative run, so the watch must
	// outlive its slowest job, and the release job must hold the watch plus
	// the guarded merge wait inside GitHub's 360-minute hosted job limit.
	const queueing = 15 * time.Minute
	mutationBudget := time.Duration(workflow.Jobs["mutation"].TimeoutMinutes) * time.Minute
	if mutationBudget <= 0 || watchBudget < mutationBudget+queueing {
		t.Fatalf("authoritative watch %v must outlive Mutation Testing's %v plus %v queueing", watchBudget, mutationBudget, queueing)
	}
	release, err := os.ReadFile("../../workflows/auto-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var releaseWorkflow struct {
		Jobs map[string]struct {
			TimeoutMinutes int `yaml:"timeout-minutes"`
		}
	}
	if err := yaml.Unmarshal(release, &releaseWorkflow); err != nil {
		t.Fatal(err)
	}
	releaseBudget := time.Duration(releaseWorkflow.Jobs["release"].TimeoutMinutes) * time.Minute
	if releaseBudget < watchBudget+executionTimeout([]string{"enable"})+queueing || releaseBudget > 360*time.Minute {
		t.Fatalf("release job %v must cover watch %v, merge wait %v and %v of steps, within 360m", releaseBudget, watchBudget, executionTimeout([]string{"enable"}), queueing)
	}
}

func TestReadOnlyReviewNeverRequests(t *testing.T) {
	gh := func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "pr":
			t.Fatalf("read-only CI attempted mutation: %v", args)
		case args[1] == "graphql":
			state := cleanState()
			state.Comments.Nodes = nil
			state.CommittedAt = time.Now()
			return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": state}}}), nil
		case strings.Contains(joined, "/files?"):
			return encode(t, []changedFile{{"CHANGELOG.md", "modified"}}), nil
		case strings.Contains(joined, "check-runs?"):
			return []byte(`{"total_count":0,"check_runs":[]}`), nil
		default:
			return encode(t, validPR()), nil
		}
		return nil, nil
	}
	ready, err := pollReview(gh, testConfig, "12", true)
	if err != nil || ready {
		t.Fatalf("missing reviews must wait without writes: %v,%v", ready, err)
	}
}

func TestAmbiguousRepeatedChecksFailClosed(t *testing.T) {
	check := checkRun{Name: "cubic · AI code reviewer"}
	check.App.Slug = "cubic-dev-ai"
	if _, err := latestChecks([]checkRun{check, check}, "cubic-dev-ai"); err == nil {
		t.Fatal("missing times must block")
	}
	check.StartedAt = time.Now()
	if _, err := latestChecks([]checkRun{check, check}, "cubic-dev-ai"); err == nil {
		t.Fatal("tied times must block")
	}
}

func TestLegacyChangelogGatePassesDuringRollout(t *testing.T) {
	check := checkRun{Name: "Changelog AI Review", HeadSHA: testHead, Status: "completed", Conclusion: "success", StartedAt: time.Now()}
	check.App.Slug = "github-actions"
	ready, err := passingReviewGate([]checkRun{check}, testHead)
	if !ready || err != nil {
		t.Fatalf("legacy gate must remain valid during rollout, got %v,%v", ready, err)
	}
}

func TestQueuedGateRerunWaitsForTimestamp(t *testing.T) {
	old := checkRun{Name: "Current-head Codex Review", HeadSHA: testHead, Status: "completed", Conclusion: "success", StartedAt: time.Now()}
	old.App.Slug = "github-actions"
	queued := old
	queued.StartedAt = time.Time{}
	queued.Status = "queued"
	queued.Conclusion = ""
	ready, err := passingReviewGate([]checkRun{old, queued}, testHead)
	if ready || err != nil {
		t.Fatalf("queued rerun should wait, got %v,%v", ready, err)
	}
	queued.StartedAt = old.StartedAt.Add(time.Minute)
	queued.Status = "completed"
	queued.Conclusion = "success"
	ready, err = passingReviewGate([]checkRun{old, queued}, testHead)
	if !ready || err != nil {
		t.Fatalf("completed rerun should pass, got %v,%v", ready, err)
	}
}

func TestCodexCommentFindingsSupersedeEarlierCleanEvidence(t *testing.T) {
	for _, tt := range []struct {
		name    string
		delta   time.Duration
		missing bool
		ready   bool
	}{
		{"later finding", time.Minute, false, false},
		{"tied finding", 0, false, false},
		{"earlier finding", -time.Minute, false, true},
		{"ambiguous finding", 0, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := cleanState()
			negative := reviewComment{Author: actor{"chatgpt-codex-connector"}, CreatedAt: state.Comments.Nodes[0].CreatedAt.Add(tt.delta), Body: "Codex Review: Found an issue that should be addressed.\n\n**Reviewed commit:** " + testHead[:10]}
			if tt.missing {
				negative.CreatedAt = time.Time{}
			}
			for _, comments := range [][]reviewComment{{state.Comments.Nodes[0], negative}, {negative, state.Comments.Nodes[0]}} {
				state.Comments.Nodes = comments
				ready, requested, err := reviewReady(state, testHead)
				if ready != tt.ready || !requested || (err != nil) != tt.missing {
					t.Fatalf("got %v,%v,%v", ready, requested, err)
				}
			}
		})
	}
}

func TestCodexCleanReceiptVariants(t *testing.T) {
	state := cleanState()
	state.Comments.Nodes[0].Body = "Codex Review: Did not find any major issues.\n\nReviewed commit: " + testHead
	ready, _, err := reviewReady(state, testHead)
	if !ready || err != nil {
		t.Fatalf("supported alternate receipt: %v,%v", ready, err)
	}
	if cleanCodexComment("Codex Review: Found an issue.\nQuoted: Codex Review: Didn't find any major issues.") {
		t.Fatal("quoted clean text must not clear findings")
	}
}

func TestQueuedAutoMergeIsWithdrawnWhenReviewChanges(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		dirty, expire, disableFails bool
	}{
		{"new finding", true, false, false},
		{"monitor deadline", false, true, false},
		{"withdrawal failure", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.expire {
				cancel()
			}
			disabled := false
			gh := func(args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				switch {
				case args[0] == "pr":
					if !strings.Contains(joined, "--disable-auto") {
						t.Fatalf("unexpected mutation: %v", args)
					}
					disabled = true
					if tc.disableFails {
						return nil, errors.New("API unavailable")
					}
					return nil, nil
				case args[1] == "graphql":
					state := cleanState()
					if tc.dirty {
						state.Comments.Nodes[0].Body = "Codex Review: Found an issue.\n**Reviewed commit:** " + testHead
					}
					return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": state}}}), nil
				case strings.Contains(joined, "/files?"):
					return encode(t, []changedFile{{"CHANGELOG.md", "modified"}}), nil
				case strings.Contains(joined, "check-runs?"):
					check := checkRun{HeadSHA: testHead, Status: "completed", Conclusion: "success"}
					check.Name = "cubic · AI code reviewer"
					check.App.Slug = "cubic-dev-ai"
					check.Output.Summary = "0 issues found"
					return encode(t, map[string]any{"check_runs": []checkRun{check}}), nil
				default:
					return encode(t, validPR()), nil
				}
			}
			err := waitForMerge(ctx, gh, testConfig, "12")
			if err == nil || !disabled {
				t.Fatalf("queued merge must be withdrawn: %v,%v", disabled, err)
			}
			if tc.disableFails && !strings.Contains(err.Error(), "could not disable") {
				t.Fatalf("withdrawal failure must be explicit: %v", err)
			}
		})
	}
}

func TestOnlyWithdrawalSurvivesMonitorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if commandContext(ctx, []string{"pr", "merge", "12", "--disable-auto"}).Err() != nil {
		t.Fatal("withdrawal needs its cleanup window")
	}
	if commandContext(ctx, []string{"pr", "merge", "12", "--auto"}).Err() == nil {
		t.Fatal("normal commands must retain the deadline")
	}
}

func TestNewFindingBeforeQueuePreventsAutoMerge(t *testing.T) {
	gatePassed := false
	gh := func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "pr" && args[1] == "list":
			return []byte(`[{"number":12}]`), nil
		case args[0] == "pr":
			t.Fatalf("dirty evidence must prevent mutation: %v", args)
		case args[1] == "graphql":
			state := cleanState()
			if gatePassed {
				state.Comments.Nodes[0].Body = "Codex Review: Found an issue.\n**Reviewed commit:** " + testHead
			}
			return encode(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": state}}}), nil
		case strings.Contains(joined, "/files?"):
			return encode(t, []changedFile{{"CHANGELOG.md", "modified"}}), nil
		case strings.Contains(joined, "check-runs?"):
			check := checkRun{HeadSHA: testHead, Status: "completed", Conclusion: "success"}
			check.Name = "cubic · AI code reviewer"
			check.App.Slug = "cubic-dev-ai"
			check.Output.Summary = "0 issues found"
			if strings.Contains(joined, "check_name=Current-head") {
				gatePassed = true
				check.App.Slug = "github-actions"
				check.Name = "Current-head Codex Review"
			}
			return encode(t, map[string]any{"check_runs": []checkRun{check}}), nil
		default:
			return encode(t, validPR()), nil
		}
		return nil, nil
	}
	if err := run(context.Background(), testConfig, []string{"enable"}, gh); err == nil {
		t.Fatal("finding during gate wait must block enabling auto-merge")
	}
}

func TestMergeDuringGuardReadStillCompletesSuccessfully(t *testing.T) {
	reads := 0
	gh := func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if args[0] == "pr" {
			t.Fatalf("completed merge must not be withdrawn: %v", args)
		}
		if strings.Contains(joined, "/files?") {
			return encode(t, []changedFile{{"CHANGELOG.md", "modified"}}), nil
		}
		reads++
		pr := validPR()
		if reads > 1 {
			pr.Merged = true
			pr.State = "closed"
		}
		return encode(t, pr), nil
	}
	if err := waitForMerge(context.Background(), gh, testConfig, "12"); err != nil {
		t.Fatal(err)
	}
	if reads != 3 {
		t.Fatalf("expected open, merged eligibility, merged reconciliation; got %d reads", reads)
	}
}

func TestExpiredReviewDoesNotPollOrRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gh := func(...string) ([]byte, error) {
		t.Fatal("expired review must not read evidence or request reviewers")
		return nil, nil
	}
	if err := waitForReview(ctx, gh, testConfig, "12", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled review, got %v", err)
	}
}

func TestReviewDecisionClearing(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	finding := review{State: "CHANGES_REQUESTED", SubmittedAt: when, Author: actor{Login: "reviewer"}}
	finding.Commit.OID = testHead
	tests := []struct {
		name, author, head, state string
		after                     time.Duration
		want                      bool
		missingTimestamp          bool
	}{
		{name: "missing finding timestamp", author: "reviewer", head: testHead, state: "APPROVED", after: time.Minute, missingTimestamp: true},
		{name: "later approval", author: "reviewer", head: testHead, state: "APPROVED", after: time.Minute, want: true},
		{name: "later dismissal", author: "reviewer", head: testHead, state: "DISMISSED", after: time.Minute, want: true},
		{name: "different author", author: "other", head: testHead, state: "APPROVED", after: time.Minute},
		{name: "different head", author: "reviewer", head: "other", state: "APPROVED", after: time.Minute},
		{name: "same timestamp", author: "reviewer", head: testHead, state: "APPROVED"},
		{name: "later comment", author: "reviewer", head: testHead, state: "COMMENTED", after: time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			later := review{State: test.state, SubmittedAt: when.Add(test.after), Author: actor{Login: test.author}}
			later.Commit.OID = test.head
			state := reviewState{}
			candidate := finding
			if test.missingTimestamp {
				candidate.SubmittedAt = time.Time{}
			}
			state.Reviews.Nodes = []review{candidate, later}
			if got := clearedReviewDecision(state, candidate); got != test.want {
				t.Fatalf("cleared = %v, want %v", got, test.want)
			}
		})
	}
}
