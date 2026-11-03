package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseAtmOptionsDefaults(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseAtmOptions(nil, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.org != "" {
		t.Fatalf("expected empty org, got %q", options.org)
	}
	if options.limit != 30 {
		t.Fatalf("expected limit 30, got %d", options.limit)
	}
	if options.reviewRequired {
		t.Fatal("expected reviewRequired false")
	}
	if options.authored {
		t.Fatal("expected authored false")
	}
	if options.json {
		t.Fatal("expected json false")
	}
}

func TestParseAtmOptionsAllFlags(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"--org", "AcmeCorp", "--limit", "10", "--review-required", "--json"}
	options, err := parseAtmOptions(args, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.org != "AcmeCorp" {
		t.Fatalf("expected org AcmeCorp, got %q", options.org)
	}
	if options.limit != 10 {
		t.Fatalf("expected limit 10, got %d", options.limit)
	}
	if !options.reviewRequired {
		t.Fatal("expected reviewRequired true")
	}
	if !options.json {
		t.Fatal("expected json true")
	}
}

func TestParseAtmOptionsShortFlags(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"-o", "AcmeCorp", "-L", "5", "-r"}
	options, err := parseAtmOptions(args, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.org != "AcmeCorp" {
		t.Fatalf("expected org AcmeCorp, got %q", options.org)
	}
	if options.limit != 5 {
		t.Fatalf("expected limit 5, got %d", options.limit)
	}
	if !options.reviewRequired {
		t.Fatal("expected reviewRequired true")
	}
}

func TestParseAtmOptionsInvalidLimit(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"--limit", "0"}
	_, err := parseAtmOptions(args, &stderr)
	if err == nil {
		t.Fatal("expected error for zero limit")
	}
	if !strings.Contains(err.Error(), "limit must be greater than zero") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseAtmOptionsValidLimitOne(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseAtmOptions([]string{"--limit", "1"}, &stderr)
	if err != nil {
		t.Fatalf("expected no error for limit=1, got: %v", err)
	}
	if opts.limit != 1 {
		t.Fatalf("expected limit 1, got %d", opts.limit)
	}
}

func TestParseAtmOptionsUnexpectedArgs(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"extra"}
	_, err := parseAtmOptions(args, &stderr)
	if err == nil {
		t.Fatal("expected error for unexpected arguments")
	}
	if !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseAtmOptionsHelp(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"-h"}
	_, err := parseAtmOptions(args, &stderr)
	if err != errHelpDisplayed {
		t.Fatalf("expected errHelpDisplayed, got %v", err)
	}
}

func TestBuildAtmSearchQueryAuthor(t *testing.T) {
	got := buildAtmSearchQuery("AcmeCorp", "octocat", false)
	want := "is:pr is:open author:octocat org:AcmeCorp"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildAtmSearchQueryReviewRequired(t *testing.T) {
	got := buildAtmSearchQuery("AcmeCorp", "octocat", true)
	want := "is:pr is:open review-requested:octocat org:AcmeCorp"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildAtmGraphQLQuery(t *testing.T) {
	query := buildAtmGraphQLQuery("is:pr is:open author:octocat org:AcmeCorp", 10)
	if !strings.Contains(query, `"is:pr is:open author:octocat org:AcmeCorp"`) {
		t.Fatal("expected search query in GraphQL")
	}
	if !strings.Contains(query, "first: 10") {
		t.Fatal("expected limit in GraphQL")
	}
	if !strings.Contains(query, "search(") {
		t.Fatal("expected search clause")
	}
	if !strings.Contains(query, "statusCheckRollup") {
		t.Fatal("expected statusCheckRollup in query")
	}
	if !strings.Contains(query, "headRefOid") || !strings.Contains(query, "commit { oid }") {
		t.Fatal("expected current-head review metadata in query")
	}
	if !strings.Contains(query, "reviews(first: 100) {\n          totalCount") {
		t.Fatal("expected review completeness metadata in query")
	}
}

func TestResolveAtmOrg(t *testing.T) {
	// With explicit org override
	got, err := resolveAtmOrg("MyOrg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "MyOrg" {
		t.Fatalf("expected MyOrg, got %q", got)
	}
}

func TestParseAtmSearchResponse(t *testing.T) {
	data := []byte(`{
		"data": {
			"search": {
				"nodes": [
					{
						"number": 42,
						"title": "Fix login",
						"author": {"login": "octocat"},
						"state": "OPEN",
						"isDraft": false,
						"reviewDecision": "APPROVED",
						"updatedAt": "2026-05-10T10:00:00Z",
						"headRefName": "fix/login",
						"baseRefName": "main",
						"url": "https://github.com/AcmeCorp/app/pull/42",
						"repository": {"nameWithOwner": "AcmeCorp/app"},
						"commits": {"nodes": [{"commit": {"statusCheckRollup": {"contexts": {"nodes": [
							{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"}
						]}}}}]},
						"latestReviews": {"nodes": [
							{"state": "APPROVED", "author": {"login": "reviewer1"}, "comments": {"totalCount": 0}}
						]},
						"reviewThreads": {"totalCount": 3, "nodes": [
							{"isResolved": true, "comments": {"nodes": []}},
							{"isResolved": true, "comments": {"nodes": []}},
							{"isResolved": false, "comments": {"nodes": []}}
						]}
					}
				]
			}
		}
	}`)

	nodes, err := parseAtmSearchResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Number != 42 {
		t.Fatalf("expected PR #42, got #%d", nodes[0].Number)
	}
	if nodes[0].Repository.NameWithOwner != "AcmeCorp/app" {
		t.Fatalf("unexpected repo: %s", nodes[0].Repository.NameWithOwner)
	}
}

func TestMapAtmNode(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	node := atmPullRequestNode{
		Number:         7,
		Title:          "Implement the new feature for user management across all services",
		State:          "OPEN",
		IsDraft:        true,
		ReviewDecision: "CHANGES_REQUESTED",
		UpdatedAt:      now.Add(-3 * time.Hour),
		HeadRefName:    "feature/users",
		BaseRefName:    "main",
		URL:            "https://github.com/Org/repo/pull/7",
		Author:         &author{Login: "octocat", Name: "The Octocat"},
	}
	node.Repository.NameWithOwner = "Org/repo"
	node.Commits.Nodes = []struct {
		Commit struct {
			StatusCheckRollup *struct {
				Contexts struct {
					Nodes []checkItem `json:"nodes"`
				} `json:"contexts"`
			} `json:"statusCheckRollup"`
		} `json:"commit"`
	}{
		{Commit: struct {
			StatusCheckRollup *struct {
				Contexts struct {
					Nodes []checkItem `json:"nodes"`
				} `json:"contexts"`
			} `json:"statusCheckRollup"`
		}{
			StatusCheckRollup: &struct {
				Contexts struct {
					Nodes []checkItem `json:"nodes"`
				} `json:"contexts"`
			}{
				Contexts: struct {
					Nodes []checkItem `json:"nodes"`
				}{
					Nodes: []checkItem{
						{Typename: "CheckRun", Name: "ci", Status: "COMPLETED", Conclusion: "SUCCESS"},
					},
				},
			},
		}},
	}
	node.Reviews.Nodes = []atmReviewNode{makeAtmReview("APPROVED", "reviewer1", 0, "")}
	node.ReviewThreads.TotalCount = 2
	node.ReviewThreads.Nodes = []struct {
		IsResolved bool `json:"isResolved"`
		Comments   struct {
			Nodes []struct {
				FullDatabaseID string `json:"fullDatabaseId"`
				Author         struct {
					Login    string `json:"login"`
					Typename string `json:"__typename"`
				} `json:"author"`
			} `json:"nodes"`
		} `json:"comments"`
	}{
		{IsResolved: true}, {IsResolved: false},
	}
	node.ApprovedReviews.Nodes = []struct {
		Author struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		} `json:"author"`
	}{
		{Author: struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		}{Login: "reviewer1", Typename: "User"}},
	}

	dp := mapAtmNode(node, now)

	if dp.Number != 7 {
		t.Fatalf("expected number 7, got %d", dp.Number)
	}
	if dp.Repo != "repo" {
		t.Fatalf("expected repo 'repo', got %q", dp.Repo)
	}
	if dp.State != "draft" {
		t.Fatalf("expected state draft, got %q", dp.State)
	}
	if dp.Review != "changes" {
		t.Fatalf("expected review changes, got %q", dp.Review)
	}
	if dp.Checks != "pass" {
		t.Fatalf("expected checks pass, got %q", dp.Checks)
	}
	if dp.Approvals != 1 {
		t.Fatalf("expected 1 approval, got %d", dp.Approvals)
	}
	if dp.Comments != "1/2" {
		t.Fatalf("expected comments 1/2, got %q", dp.Comments)
	}
	if dp.Author != "The Octocat" {
		t.Fatalf("expected author 'The Octocat', got %q", dp.Author)
	}
	if dp.Updated != "3h" {
		t.Fatalf("expected updated 3h, got %q", dp.Updated)
	}
	if dp.URL != "https://github.com/Org/repo/pull/7" {
		t.Fatalf("unexpected URL: %s", dp.URL)
	}
	// Title should be trimmed to 42 chars
	if len(dp.Title) > 42 {
		t.Fatalf("expected title trimmed to 42, got %d chars: %q", len(dp.Title), dp.Title)
	}
}

func TestMapAtmNodeBotApprovalWithOutstandingComments(t *testing.T) {
	data := []byte(`{
		"data": {
			"search": {
				"nodes": [{
					"number": 15,
					"title": "Bot review",
					"state": "OPEN",
					"reviewDecision": "APPROVED",
					"updatedAt": "2026-07-31T03:00:00Z",
					"repository": {"nameWithOwner": "relias-engineering/some-service"},
					"reviews": {"nodes": [{
						"state": "APPROVED",
						"author": {"login": "review-bot[bot]", "__typename": "Bot"},
						"comments": {"totalCount": 3}
					}]},
					"reviewThreads": {
						"totalCount": 3,
						"nodes": [{
							"isResolved": false,
							"comments": {"nodes": [{
								"author": {"login": "review-bot[bot]", "__typename": "Bot"}
							}]}
						}, {
							"isResolved": false,
							"comments": {"nodes": [{
								"author": {"login": "review-bot[bot]", "__typename": "Bot"}
							}]}
						}, {
							"isResolved": false,
							"comments": {"nodes": [{
								"author": {"login": "review-bot[bot]", "__typename": "Bot"}
							}]}
						}]
					},
					"approvedReviews": {"nodes": [{
						"author": {"login": "review-bot[bot]", "__typename": "Bot"}
					}]}
				}]
			}
		}
	}`)

	nodes, err := parseAtmSearchResponse(data)
	if err != nil {
		t.Fatalf("parseAtmSearchResponse() error = %v", err)
	}
	dp := mapAtmNode(nodes[0], time.Date(2026, 7, 31, 3, 1, 0, 0, time.UTC))

	if dp.Review != "approved" {
		t.Fatalf("Review = %q, want approved", dp.Review)
	}
	if dp.AIReview != "fail" {
		t.Fatalf("AIReview = %q, want fail", dp.AIReview)
	}
	if dp.Approvals != 1 {
		t.Fatalf("Approvals = %d, want 1 bot approval", dp.Approvals)
	}
	if dp.Comments != "0/3" {
		t.Fatalf("Comments = %q, want 0/3", dp.Comments)
	}
}

func TestMapAtmNodeNoChecks(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	node := atmPullRequestNode{
		Number:    1,
		Title:     "Simple",
		State:     "OPEN",
		UpdatedAt: now,
	}
	node.Repository.NameWithOwner = "Org/simple-repo"

	dp := mapAtmNode(node, now)
	if dp.Checks != "-" {
		t.Fatalf("expected checks '-', got %q", dp.Checks)
	}
	if dp.Repo != "simple-repo" {
		t.Fatalf("expected repo 'simple-repo', got %q", dp.Repo)
	}
	if dp.Author != "-" {
		t.Fatalf("expected author '-', got %q", dp.Author)
	}
}

func TestMapAtmNodeFiltersStaleAIReviews(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	node := atmPullRequestNode{
		Number:     8,
		Title:      "Current AI state",
		State:      "OPEN",
		UpdatedAt:  now,
		HeadRefOID: "current-head",
	}
	node.Reviews.TotalCount = 2
	node.Reviews.Nodes = []atmReviewNode{
		makeAtmReview("COMMENTED", "coderabbitai[bot]", 0, "current-head"),
		makeAtmReview("CHANGES_REQUESTED", "coderabbitai[bot]", 1, "old-head"),
	}

	dp := mapAtmNode(node, now)
	if dp.AIReview != "pass" {
		t.Fatalf("AIReview = %q, want pass from current-head review", dp.AIReview)
	}
	if dp.AIClean == nil || !*dp.AIClean {
		t.Fatal("expected current-head clean review to set AIClean")
	}
}

func TestMapAtmNodeFailsClosedWhenReviewsTruncated(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	node := atmPullRequestNode{
		Number:     9,
		Title:      "Incomplete reviews",
		State:      "OPEN",
		UpdatedAt:  now,
		HeadRefOID: "current-head",
	}
	node.Reviews.TotalCount = 101
	node.Reviews.Nodes = []atmReviewNode{
		makeAtmReview("APPROVED", "coderabbitai[bot]", 0, "current-head"),
	}

	dp := mapAtmNode(node, now)
	if dp.AIReview != "?" {
		t.Fatalf("expected incomplete reviews to produce unknown status, got AI=%q", dp.AIReview)
	}
	if dp.AIClean != nil {
		t.Fatalf("expected incomplete reviews to suppress AIClean, got %v", *dp.AIClean)
	}
}

func TestMapAtmNodeFailsClosedWhenThreadsTruncated(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	node := atmPullRequestNode{
		Number:     10,
		Title:      "Incomplete threads",
		State:      "OPEN",
		UpdatedAt:  now,
		HeadRefOID: "current-head",
	}
	node.Reviews.TotalCount = 1
	node.Reviews.Nodes = []atmReviewNode{
		makeAtmReview("APPROVED", "coderabbitai[bot]", 0, "current-head"),
	}
	node.ReviewThreads.TotalCount = 101

	dp := mapAtmNode(node, now)
	if dp.AIReview != "?" {
		t.Fatalf("expected incomplete threads to produce unknown AI status, got %q", dp.AIReview)
	}
	if dp.AIClean != nil {
		t.Fatalf("expected incomplete threads to suppress AIClean, got %v", *dp.AIClean)
	}
}

func TestMapAtmNodeAIClean(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	node := atmPullRequestNode{
		Number:    99,
		Title:     "Bot approved cleanly",
		State:     "OPEN",
		UpdatedAt: now,
		URL:       "https://github.com/Org/repo/pull/99",
	}
	node.Repository.NameWithOwner = "Org/repo"
	node.Reviews.Nodes = []atmReviewNode{makeAtmReview("APPROVED", "copilot-pull-request-reviewer", 0, "")}

	dp := mapAtmNode(node, now)
	if dp.AIClean == nil || !*dp.AIClean {
		t.Fatalf("expected AIClean=true for bot APPROVED with 0 comments in ATM")
	}
	if dp.AIReview != "pass" {
		t.Fatalf("expected AIReview='pass', got %q", dp.AIReview)
	}
}

func TestMapAtmNodeCopilotCloserLookIsNotClean(t *testing.T) {
	now := time.Date(2026, 9, 9, 21, 37, 17, 0, time.UTC)
	node := atmPullRequestNode{
		Number:     111,
		Title:      "software request scan skill updates",
		State:      "OPEN",
		UpdatedAt:  now,
		HeadRefOID: "b694974bc2ccc8230686fe42cd50d8f476997383",
	}
	review := makeAtmReview("COMMENTED", "copilot-pull-request-reviewer", 0, node.HeadRefOID)
	review.Body = "### 🔵 Needs a closer look"
	node.Reviews.TotalCount = 1
	node.Reviews.Nodes = []atmReviewNode{review}
	node.ReviewThreads.TotalCount = 1
	node.ReviewThreads.Nodes = append(node.ReviewThreads.Nodes, struct {
		IsResolved bool `json:"isResolved"`
		Comments   struct {
			Nodes []struct {
				FullDatabaseID string `json:"fullDatabaseId"`
				Author         struct {
					Login    string `json:"login"`
					Typename string `json:"__typename"`
				} `json:"author"`
			} `json:"nodes"`
		} `json:"comments"`
	}{IsResolved: true})
	node.ReviewThreads.Nodes[0].Comments.Nodes = append(node.ReviewThreads.Nodes[0].Comments.Nodes, struct {
		FullDatabaseID string `json:"fullDatabaseId"`
		Author         struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		} `json:"author"`
	}{})
	node.ReviewThreads.Nodes[0].Comments.Nodes[0].Author.Login = "copilot-pull-request-reviewer"
	node.ReviewThreads.Nodes[0].Comments.Nodes[0].Author.Typename = "Bot"

	dp := mapAtmNode(node, now)
	if dp.AIReview != "fail" {
		t.Fatalf("AIReview = %q, want fail", dp.AIReview)
	}
	if dp.AIClean != nil {
		t.Fatalf("expected no clean marker, got %v", *dp.AIClean)
	}
	if dp.Comments != "1/1" {
		t.Fatalf("Comments = %q, want 1/1", dp.Comments)
	}
}

func TestMapAtmNodeAICleanNilWhenNotClean(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	node := atmPullRequestNode{
		Number:    99,
		Title:     "not-clean PR",
		UpdatedAt: now.Add(-time.Hour),
	}
	// No reviews at all — should not be marked clean.
	dp := mapAtmNode(node, now)
	if dp.AIClean != nil {
		t.Fatalf("expected AIClean=nil when no bot reviews, got %v", *dp.AIClean)
	}
}

func TestRenderAtmTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	err := renderAtmTable(&buf, "AcmeCorp", "octocat", atmOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No PRs needing review from octocat in AcmeCorp") {
		t.Fatalf("unexpected empty message: %q", buf.String())
	}
}

func TestRenderAtmTableEmptyAuthored(t *testing.T) {
	var buf bytes.Buffer
	err := renderAtmTable(&buf, "AcmeCorp", "octocat", atmOptions{authored: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No open PRs authored by octocat in AcmeCorp") {
		t.Fatalf("unexpected empty message: %q", buf.String())
	}
}

func TestRenderAtmTableEmptyReviewRequired(t *testing.T) {
	var buf bytes.Buffer
	err := renderAtmTable(&buf, "AcmeCorp", "user", atmOptions{reviewRequired: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No open PRs requesting review from user in AcmeCorp") {
		t.Fatalf("unexpected empty message: %q", buf.String())
	}
}

func TestRenderAtmTableWithStyleNoColor(t *testing.T) {
	prs := []displayPullRequest{
		{Number: 10, Title: "Fix bug", Repo: "api", Author: "dev", State: "open", Review: "approved", AIReview: "pass", Approvals: 1, Checks: "pass", Comments: "2/2", Updated: "1h", URL: "https://github.com/Org/api/pull/10"},
	}
	var buf bytes.Buffer
	err := renderAtmTableWithStyle(&buf, prs, false)
	if err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	if strings.Contains(output, "\x1b[") {
		t.Fatal("expected no ANSI codes when color disabled")
	}
	if !strings.Contains(output, "#10") {
		t.Fatal("expected PR number")
	}
	if !strings.Contains(output, "api") {
		t.Fatal("expected repo column")
	}
	if !strings.Contains(output, "Repo") {
		t.Fatal("expected Repo header")
	}
}

func TestRenderAtmTableWithStyleColor(t *testing.T) {
	prs := []displayPullRequest{
		{Number: 5, Title: "Add feature", Repo: "web", Author: "user", State: "open", Review: "-", AIReview: "-", Approvals: 0, Checks: "pending", Comments: "-", Updated: "2d", URL: ""},
	}
	var buf bytes.Buffer
	err := renderAtmTableWithStyle(&buf, prs, true)
	if err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	if !strings.Contains(output, "\x1b[") {
		t.Fatal("expected ANSI codes when color enabled")
	}
}

func TestRunAtmSubcommandRouting(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// "pr atm --help" should display help without error
	_, err := run([]string{"pr", "atm", "--help"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected no error for pr atm --help, got: %v", err)
	}
	if !strings.Contains(stderr.String(), "gh x pr atm") {
		t.Fatalf("expected atm usage in stderr, got: %q", stderr.String())
	}
}

func TestParseAtmSearchResponseGraphQLError(t *testing.T) {
	data := []byte(`{
		"data": {"search": {"nodes": []}},
		"errors": [{"type": "INSUFFICIENT_SCOPES", "message": "Your token has not been granted the required scopes"}]
	}`)
	_, err := parseAtmSearchResponse(data)
	if err == nil {
		t.Fatal("expected error for GraphQL error response")
	}
	if !strings.Contains(err.Error(), "Your token has not been granted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRootUsageMentionsAtm(t *testing.T) {
	if !strings.Contains(rootUsage, "atm") {
		t.Fatal("root usage should mention atm subcommand")
	}
}

func TestParseAtmOptionsAuthored(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"--authored"}
	options, err := parseAtmOptions(args, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !options.authored {
		t.Fatal("expected authored true")
	}
}

func TestParseAtmOptionsAuthoredShort(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"-a"}
	options, err := parseAtmOptions(args, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !options.authored {
		t.Fatal("expected authored true with -a flag")
	}
}

func TestBuildAtmNeedsReviewQueries(t *testing.T) {
	queries := buildAtmNeedsReviewQueries("AcmeCorp", "jdoe")
	if len(queries) != 3 {
		t.Fatalf("expected 3 queries, got %d", len(queries))
	}
	if !strings.Contains(queries[0], "review-requested:jdoe") {
		t.Fatalf("query 0 should contain review-requested, got %q", queries[0])
	}
	if !strings.Contains(queries[1], "assignee:jdoe") || !strings.Contains(queries[1], "-author:jdoe") {
		t.Fatalf("query 1 should contain assignee and -author, got %q", queries[1])
	}
	if !strings.Contains(queries[2], "reviewed-by:jdoe") || !strings.Contains(queries[2], "-author:jdoe") {
		t.Fatalf("query 2 should contain reviewed-by and -author, got %q", queries[2])
	}
	for _, q := range queries {
		if !strings.Contains(q, "org:AcmeCorp") {
			t.Fatalf("query should contain org, got %q", q)
		}
	}
}

func TestBuildAtmMultiSearchQuery(t *testing.T) {
	queries := []string{
		"is:pr is:open review-requested:user org:Org",
		"is:pr is:open assignee:user org:Org -author:user",
	}
	result := buildAtmMultiSearchQuery(queries, 10)
	if !strings.Contains(result, "q0: search(") {
		t.Fatal("expected q0 alias")
	}
	if !strings.Contains(result, "q1: search(") {
		t.Fatal("expected q1 alias")
	}
	if !strings.Contains(result, "first: 10") {
		t.Fatal("expected limit")
	}
	if !strings.Contains(result, "statusCheckRollup") {
		t.Fatal("expected PR fields in multi-search query")
	}
	if !strings.Contains(result, "approvedReviews") {
		t.Fatal("expected approvedReviews in multi-search query")
	}
	if !strings.Contains(result, "state\n            body\n            commit") {
		t.Fatal("expected formal review bodies in multi-search query")
	}
}

func TestParseAtmMultiSearchResponse(t *testing.T) {
	data := []byte(`{
		"data": {
			"q0": {
				"nodes": [
					{"number": 1, "title": "PR1", "state": "OPEN", "repository": {"nameWithOwner": "Org/repo1"},
					 "commits": {"nodes": []}, "reviews": {"nodes": []}, "reviewThreads": {"totalCount": 0, "nodes": []},
					 "approvedReviews": {"nodes": []}}
				]
			},
			"q1": {
				"nodes": [
					{"number": 1, "title": "PR1", "state": "OPEN", "repository": {"nameWithOwner": "Org/repo1"},
					 "commits": {"nodes": []}, "reviews": {"nodes": []}, "reviewThreads": {"totalCount": 0, "nodes": []},
					 "approvedReviews": {"nodes": []}},
					{"number": 2, "title": "PR2", "state": "OPEN", "repository": {"nameWithOwner": "Org/repo2"},
					 "commits": {"nodes": []}, "reviews": {"nodes": []}, "reviewThreads": {"totalCount": 0, "nodes": []},
					 "approvedReviews": {"nodes": []}}
				]
			}
		}
	}`)
	nodes, err := parseAtmMultiSearchResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 deduplicated nodes, got %d", len(nodes))
	}
	if nodes[0].Number != 1 || nodes[1].Number != 2 {
		t.Fatalf("unexpected node order: #%d, #%d", nodes[0].Number, nodes[1].Number)
	}
}

func TestParseAtmMultiSearchResponseError(t *testing.T) {
	data := []byte(`{
		"data": {},
		"errors": [{"type": "FORBIDDEN", "message": "access denied"}]
	}`)
	_, err := parseAtmMultiSearchResponse(data)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUserApprovedPR(t *testing.T) {
	node := atmPullRequestNode{}
	node.LatestReviews.Nodes = []struct {
		State  string `json:"state"`
		Author struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		} `json:"author"`
		Comments struct {
			TotalCount int `json:"totalCount"`
		} `json:"comments"`
	}{
		{State: "APPROVED", Author: struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		}{Login: "reviewer1"}},
		{State: "COMMENTED", Author: struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		}{Login: "myuser"}},
	}

	if userApprovedPR(node, "reviewer1") != true {
		t.Fatal("expected reviewer1 to be detected as approved")
	}
	if userApprovedPR(node, "myuser") != false {
		t.Fatal("expected myuser to NOT be detected as approved (state is COMMENTED)")
	}
	if userApprovedPR(node, "nobody") != false {
		t.Fatal("expected unknown user to not be approved")
	}
}

func TestUserApprovedPRCaseInsensitive(t *testing.T) {
	node := atmPullRequestNode{}
	node.LatestReviews.Nodes = []struct {
		State  string `json:"state"`
		Author struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		} `json:"author"`
		Comments struct {
			TotalCount int `json:"totalCount"`
		} `json:"comments"`
	}{
		{State: "APPROVED", Author: struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		}{Login: "Reviewer1"}},
	}
	if !userApprovedPR(node, "reviewer1") {
		t.Fatal("expected case-insensitive match")
	}
}

func TestExtractRepoShortName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"owner/repo", "repo"},
		{"repo-only", "repo-only"},
		{"org/sub/repo", "sub/repo"},
	}
	for _, tc := range tests {
		if got := extractRepoShortName(tc.input); got != tc.want {
			t.Fatalf("extractRepoShortName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractAtmCheckItems(t *testing.T) {
	t.Run("with check items", func(t *testing.T) {
		node := atmPullRequestNode{}
		node.Commits.Nodes = []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkItem `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		}{
			{Commit: struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkItem `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			}{
				StatusCheckRollup: &struct {
					Contexts struct {
						Nodes []checkItem `json:"nodes"`
					} `json:"contexts"`
				}{
					Contexts: struct {
						Nodes []checkItem `json:"nodes"`
					}{Nodes: []checkItem{{Typename: "CheckRun", Name: "ci"}}},
				},
			}},
		}
		items := extractAtmCheckItems(node)
		if len(items) != 1 || items[0].Name != "ci" {
			t.Fatalf("expected 1 check item 'ci', got %v", items)
		}
	})

	t.Run("empty commits", func(t *testing.T) {
		node := atmPullRequestNode{}
		if items := extractAtmCheckItems(node); items != nil {
			t.Fatalf("expected nil for empty commits, got %v", items)
		}
	})

	t.Run("nil rollup", func(t *testing.T) {
		node := atmPullRequestNode{}
		node.Commits.Nodes = []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkItem `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		}{{}}
		if items := extractAtmCheckItems(node); items != nil {
			t.Fatalf("expected nil for nil rollup, got %v", items)
		}
	})
}

func TestExtractAtmReviewThreads(t *testing.T) {
	node := atmPullRequestNode{}
	node.ReviewThreads.Nodes = []struct {
		IsResolved bool `json:"isResolved"`
		Comments   struct {
			Nodes []struct {
				FullDatabaseID string `json:"fullDatabaseId"`
				Author         struct {
					Login    string `json:"login"`
					Typename string `json:"__typename"`
				} `json:"author"`
			} `json:"nodes"`
		} `json:"comments"`
	}{
		{
			IsResolved: true,
			Comments: struct {
				Nodes []struct {
					FullDatabaseID string `json:"fullDatabaseId"`
					Author         struct {
						Login    string `json:"login"`
						Typename string `json:"__typename"`
					} `json:"author"`
				} `json:"nodes"`
			}{
				Nodes: []struct {
					FullDatabaseID string `json:"fullDatabaseId"`
					Author         struct {
						Login    string `json:"login"`
						Typename string `json:"__typename"`
					} `json:"author"`
				}{{Author: struct {
					Login    string `json:"login"`
					Typename string `json:"__typename"`
				}{Login: "bot[bot]", Typename: "Bot"}}},
			},
		},
		{IsResolved: false},
	}

	threads := extractAtmReviewThreads(node)
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(threads))
	}
	if threads[0].AuthorLogin != "bot[bot]" || !threads[0].IsResolved {
		t.Fatalf("first thread wrong: %+v", threads[0])
	}
	if threads[1].AuthorLogin != "" || threads[1].IsResolved {
		t.Fatalf("second thread wrong: %+v", threads[1])
	}
}

func TestFilterUnapprovedNodes(t *testing.T) {
	makeNode := func(state, login string) atmPullRequestNode {
		n := atmPullRequestNode{}
		n.LatestReviews.Nodes = append(n.LatestReviews.Nodes, struct {
			State  string `json:"state"`
			Author struct {
				Login    string `json:"login"`
				Typename string `json:"__typename"`
			} `json:"author"`
			Comments struct {
				TotalCount int `json:"totalCount"`
			} `json:"comments"`
		}{
			State: state,
			Author: struct {
				Login    string `json:"login"`
				Typename string `json:"__typename"`
			}{Login: login},
		})
		return n
	}

	approved := makeNode("APPROVED", "testuser")
	pending := makeNode("COMMENTED", "other")

	tests := []struct {
		name    string
		nodes   []atmPullRequestNode
		login   string
		wantLen int
	}{
		{
			name:    "empty input",
			nodes:   nil,
			login:   "testuser",
			wantLen: 0,
		},
		{
			name:    "filters out approved PRs",
			nodes:   []atmPullRequestNode{approved, pending},
			login:   "testuser",
			wantLen: 1,
		},
		{
			name:    "keeps all when none approved",
			nodes:   []atmPullRequestNode{pending},
			login:   "testuser",
			wantLen: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterUnapprovedNodes(tc.nodes, tc.login)
			if len(got) != tc.wantLen {
				t.Fatalf("filterUnapprovedNodes() returned %d nodes, want %d", len(got), tc.wantLen)
			}
		})
	}
}

func TestRenderAtmResultsJSON(t *testing.T) {
	nodes := []atmPullRequestNode{
		{
			Number: 1,
			Title:  "Test PR",
			URL:    "https://github.com/test/repo/pull/1",
		},
	}
	var buf bytes.Buffer
	options := atmOptions{json: true}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	err := renderAtmResults(nodes, &buf, "testorg", "testuser", options, now)
	if err != nil {
		t.Fatalf("renderAtmResults(json) error: %v", err)
	}
	if !strings.Contains(buf.String(), `"number": 1`) {
		t.Fatalf("JSON output missing PR number: %s", buf.String())
	}
}

func TestRenderAtmResultsAIReviewPass(t *testing.T) {
	nodes := []atmPullRequestNode{
		{
			Number: 42,
			Title:  "Reviewed by bot",
			URL:    "https://github.com/test/repo/pull/42",
		},
	}
	nodes[0].Reviews.Nodes = []atmReviewNode{makeAtmReview("APPROVED", "copilot-pull-request-reviewer[bot]", 0, "")}
	var buf bytes.Buffer
	options := atmOptions{json: true}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	err := renderAtmResults(nodes, &buf, "testorg", "testuser", options, now)
	if err != nil {
		t.Fatalf("renderAtmResults error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, `"aiReview": "pass"`) {
		t.Fatalf("expected aiReview='pass' for bot-approved PR, got:\n%s", output)
	}
}

func TestParseAtmOptionsReady(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"--ready"}
	options, err := parseAtmOptions(args, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !options.ready {
		t.Fatal("expected ready true")
	}
}

func TestIsReadyPR(t *testing.T) {
	tests := []struct {
		name string
		pr   displayPullRequest
		want bool
	}{
		{
			name: "all criteria met with no comments",
			pr:   displayPullRequest{State: "open", AIReview: "pass", Checks: "pass", Comments: "-"},
			want: true,
		},
		{
			name: "all criteria met with resolved comments",
			pr:   displayPullRequest{State: "open", AIReview: "pass", Checks: "pass", Comments: "3/3"},
			want: true,
		},
		{
			name: "draft state",
			pr:   displayPullRequest{State: "draft", AIReview: "pass", Checks: "pass", Comments: "-"},
			want: false,
		},
		{
			name: "closed state",
			pr:   displayPullRequest{State: "closed", AIReview: "pass", Checks: "pass", Comments: "-"},
			want: false,
		},
		{
			name: "AI review fail",
			pr:   displayPullRequest{State: "open", AIReview: "fail", Checks: "pass", Comments: "-"},
			want: false,
		},
		{
			name: "AI review dash",
			pr:   displayPullRequest{State: "open", AIReview: "-", Checks: "pass", Comments: "-"},
			want: false,
		},
		{
			name: "checks fail",
			pr:   displayPullRequest{State: "open", AIReview: "pass", Checks: "fail", Comments: "-"},
			want: false,
		},
		{
			name: "checks pending",
			pr:   displayPullRequest{State: "open", AIReview: "pass", Checks: "pending", Comments: "-"},
			want: false,
		},
		{
			name: "unresolved comments",
			pr:   displayPullRequest{State: "open", AIReview: "pass", Checks: "pass", Comments: "1/3"},
			want: false,
		},
		{
			name: "zero resolved of total",
			pr:   displayPullRequest{State: "open", AIReview: "pass", Checks: "pass", Comments: "0/2"},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadyPR(tc.pr); got != tc.want {
				t.Fatalf("isReadyPR() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCommentsResolved(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"-", true},
		{"2/2", true},
		{"0/0", true},
		{"1/3", false},
		{"0/2", false},
		{"?", false},
	}
	for _, tc := range tests {
		if got := commentsResolved(tc.input); got != tc.want {
			t.Fatalf("commentsResolved(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestFilterReadyPRs(t *testing.T) {
	prs := []displayPullRequest{
		{Number: 1, State: "open", AIReview: "pass", Checks: "pass", Comments: "-"},
		{Number: 2, State: "draft", AIReview: "pass", Checks: "pass", Comments: "-"},
		{Number: 3, State: "open", AIReview: "pass", Checks: "pass", Comments: "2/2"},
		{Number: 4, State: "open", AIReview: "fail", Checks: "pass", Comments: "-"},
		{Number: 5, State: "open", AIReview: "pass", Checks: "fail", Comments: "-"},
		{Number: 6, State: "open", AIReview: "pass", Checks: "pass", Comments: "1/3"},
	}
	got := filterReadyPRs(prs)
	if len(got) != 2 {
		t.Fatalf("expected 2 ready PRs, got %d", len(got))
	}
	if got[0].Number != 1 || got[1].Number != 3 {
		t.Fatalf("expected PRs #1 and #3, got #%d and #%d", got[0].Number, got[1].Number)
	}
}

func TestRenderAtmTableEmptyReady(t *testing.T) {
	var buf bytes.Buffer
	err := renderAtmTable(&buf, "AcmeCorp", "octocat", atmOptions{ready: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No ready-to-merge PRs for octocat in AcmeCorp") {
		t.Fatalf("unexpected empty message: %q", buf.String())
	}
}

func TestRenderAtmTableReadyHeader(t *testing.T) {
	prs := []displayPullRequest{
		{Number: 1, Title: "Ready PR", Repo: "api", Author: "dev", State: "open", Review: "approved", AIReview: "pass", Approvals: 1, Checks: "pass", Comments: "-", Updated: "1h"},
	}
	var buf bytes.Buffer
	err := renderAtmTable(&buf, "AcmeCorp", "octocat", atmOptions{ready: true}, prs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Ready-to-merge PRs for octocat in AcmeCorp") {
		t.Fatalf("unexpected header: %q", buf.String())
	}
}

func TestRenderAtmResultsReadyFilter(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	nodes := []atmPullRequestNode{
		{Number: 1, Title: "Ready", State: "OPEN", UpdatedAt: now},
		{Number: 2, Title: "Draft", State: "OPEN", IsDraft: true, UpdatedAt: now},
	}
	nodes[0].Repository.NameWithOwner = "Org/repo"
	nodes[1].Repository.NameWithOwner = "Org/repo"

	// Give node 1 passing checks, passing AI, and no threads (all resolved vacuously)
	nodes[0].Commits.Nodes = []struct {
		Commit struct {
			StatusCheckRollup *struct {
				Contexts struct {
					Nodes []checkItem `json:"nodes"`
				} `json:"contexts"`
			} `json:"statusCheckRollup"`
		} `json:"commit"`
	}{{Commit: struct {
		StatusCheckRollup *struct {
			Contexts struct {
				Nodes []checkItem `json:"nodes"`
			} `json:"contexts"`
		} `json:"statusCheckRollup"`
	}{StatusCheckRollup: &struct {
		Contexts struct {
			Nodes []checkItem `json:"nodes"`
		} `json:"contexts"`
	}{Contexts: struct {
		Nodes []checkItem `json:"nodes"`
	}{Nodes: []checkItem{{Typename: "CheckRun", Name: "ci", Status: "COMPLETED", Conclusion: "SUCCESS"}}}}}}}
	nodes[0].Reviews.Nodes = []atmReviewNode{makeAtmReview("APPROVED", "copilot-pull-request-reviewer[bot]", 0, "")}

	var buf bytes.Buffer
	options := atmOptions{json: true, ready: true}
	err := renderAtmResults(nodes, &buf, "Org", "user", options, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, `"number": 1`) {
		t.Fatalf("expected ready PR #1 in output, got:\n%s", output)
	}
	if strings.Contains(output, `"number": 2`) {
		t.Fatalf("did not expect draft PR #2 in output, got:\n%s", output)
	}
}

func makeAtmReview(state, login string, commentCount int, commitOID string) atmReviewNode {
	review := atmReviewNode{State: state}
	review.Author.Login = login
	review.Comments.TotalCount = commentCount
	review.Commit.OID = commitOID
	return review
}
