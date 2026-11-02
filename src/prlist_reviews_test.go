package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCountApprovals(t *testing.T) {
	tests := []struct {
		name    string
		reviews []review
		want    int
	}{
		{name: "nil", reviews: nil, want: 0},
		{name: "empty", reviews: []review{}, want: 0},
		{name: "one approved", reviews: []review{{State: "APPROVED"}}, want: 1},
		{name: "mixed", reviews: []review{
			{State: "APPROVED"},
			{State: "COMMENTED"},
			{State: "CHANGES_REQUESTED"},
			{State: "APPROVED"},
		}, want: 2},
		{name: "bot approvals included", reviews: []review{
			{State: "APPROVED", Author: &author{Login: "alice"}},
			{State: "APPROVED", Author: &author{Login: "set-it-free-loop"}},
			{State: "APPROVED", Author: &author{Login: "copilot-pull-request-reviewer"}},
		}, want: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countApprovals(tc.reviews); got != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestSupplementalApprovalsOverridesCountApprovals(t *testing.T) {
	// buildDisplayPullRequest uses countApprovals as initial value.
	// When supplemental data is available, it should override with the
	// accurate count from latestOpinionatedReviews.
	now := time.Now()
	pr := pullRequest{
		Number:      28,
		Title:       "Test PR",
		State:       "OPEN",
		HeadRefName: "test-branch",
		BaseRefName: "main",
		Author:      &author{Login: "user1"},
		// latestReviews has COMMENTED (not APPROVED), so countApprovals returns 0
		LatestReviews: []review{
			{State: "COMMENTED", Author: &author{Login: "coderabbitai"}},
		},
	}

	dp := buildDisplayPullRequest(pr, now)
	if dp.Approvals != 0 {
		t.Fatalf("expected countApprovals fallback = 0, got %d", dp.Approvals)
	}

	// Supplemental data says there is an active approval.
	applySupplementalInfo(&dp, prSupplementalInfo{Approvals: 1}, false)
	if dp.Approvals != 1 {
		t.Fatalf("expected supplemental override = 1, got %d", dp.Approvals)
	}

	applySupplementalInfo(&dp, prSupplementalInfo{ApprovalsIncomplete: true}, false)
	if !dp.ApprovalsIncomplete || dp.Approvals != 0 {
		t.Fatalf("incomplete approval data = count %d, incomplete %v; want 0, true", dp.Approvals, dp.ApprovalsIncomplete)
	}
}

func TestFormatComments(t *testing.T) {
	tests := []struct {
		name string
		info reviewThreadInfo
		want string
	}{
		{name: "none", info: reviewThreadInfo{}, want: "-"},
		{name: "all resolved", info: reviewThreadInfo{Total: 5, Resolved: 5}, want: "5/5"},
		{name: "partial", info: reviewThreadInfo{Total: 5, Resolved: 3}, want: "3/5"},
		{name: "none resolved", info: reviewThreadInfo{Total: 3, Resolved: 0}, want: "0/3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatComments(tc.info); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestIsAIReviewer(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"coderabbitai[bot]", true},
		{"copilot[bot]", true},
		{"copilot-pull-request-reviewer", true},
		{"chatgpt-codex-connector", false},
		{"human-reviewer", false},
		{"dependabot[bot]", true},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.login, func(t *testing.T) {
			if got := isAIReviewer(tc.login); got != tc.want {
				t.Fatalf("isAIReviewer(%q) = %v, want %v", tc.login, got, tc.want)
			}
		})
	}
}

func TestCodexSummaryReviewNode(t *testing.T) {
	const head = "f92ca525d5a267d4a4c544ed1f0f90e1e3d63802"
	const completed = `<!-- codex-pull-request-review-summary -->

| Review | Status | Commit | Review trigger |
| --- | --- | --- | --- |
| 📝 **Code Review** | ✅ **Completed** <relative-time datetime="2026-09-20T21:49:39.029122Z">2026-09-20T21:49:39.029122Z</relative-time> | ` + "`f92ca52`" + ` | PR opened |`
	completedAt := time.Date(2026, 9, 20, 21, 49, 39, 29122000, time.UTC)
	cleanAt := time.Date(2026, 9, 20, 21, 49, 41, 0, time.UTC)
	cleanReaction := aiReviewReaction{AuthorLogin: "chatgpt-codex-connector[bot]", OccurredAt: cleanAt}

	tests := []struct {
		name        string
		comment     aiReviewComment
		reactions   []aiReviewReaction
		head        string
		want        bool
		wantSummary bool
	}{
		{name: "completed current head with clean reaction", comment: aiReviewComment{Body: completed, AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{cleanReaction}, head: head, want: true, wantSummary: true},
		{name: "missing clean reaction", comment: aiReviewComment{Body: completed, AuthorLogin: "chatgpt-codex-connector"}, head: head, wantSummary: true},
		{name: "reaction predates completion", comment: aiReviewComment{Body: completed, AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{{AuthorLogin: "chatgpt-codex-connector[bot]", OccurredAt: completedAt.Add(-time.Second)}}, head: head, wantSummary: true},
		{name: "reaction ties completion", comment: aiReviewComment{Body: completed, AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{{AuthorLogin: "chatgpt-codex-connector[bot]", OccurredAt: completedAt}}, head: head, wantSummary: true},
		{name: "reaction from another actor", comment: aiReviewComment{Body: completed, AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{{AuthorLogin: "human", OccurredAt: cleanAt}}, head: head, wantSummary: true},
		{name: "running review", comment: aiReviewComment{Body: strings.Replace(completed, "Completed", "Running", 1), AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{cleanReaction}, head: head},
		{name: "different head", comment: aiReviewComment{Body: completed, AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{cleanReaction}, head: strings.Repeat("a", 40)},
		{name: "invalid timestamp", comment: aiReviewComment{Body: strings.Replace(completed, "2026-09-20T21:49:39.029122Z", "invalid", 1), AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{cleanReaction}, head: head},
		{name: "ambiguous activity rows", comment: aiReviewComment{Body: completed + "\n" + completed, AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{cleanReaction}, head: head},
		{name: "non-Codex author", comment: aiReviewComment{Body: completed, AuthorLogin: "human"}, reactions: []aiReviewReaction{cleanReaction}, head: head},
		{name: "missing head", comment: aiReviewComment{Body: completed, AuthorLogin: "chatgpt-codex-connector"}, reactions: []aiReviewReaction{cleanReaction}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node, got, summary := codexReviewNode(tc.comment, tc.reactions, tc.head)
			if got != tc.want || summary != tc.wantSummary {
				t.Fatalf("codexReviewNode() matched = %v, summary = %v; want matched = %v, summary = %v", got, summary, tc.want, tc.wantSummary)
			}
			if tc.want && (node.State != "COMMENTED" || node.CommentCount != 0 || node.CommitOID != head || !node.OccurredAt.Equal(cleanAt)) {
				t.Fatalf("unexpected Codex summary node: %#v", node)
			}
		})
	}
}

func TestClassifyCopilotReview(t *testing.T) {
	tests := []struct {
		name string
		body string
		want copilotReviewVerdict
	}{
		{name: "approval recommended", body: "### 🟢 Approval recommended\n\nNo issues found.", want: copilotVerdictClean},
		{name: "needs closer look", body: "### 🔵 Needs a closer look\n\nA portability issue remains.", want: copilotVerdictFindings},
		{name: "changes recommended", body: "### 🟡 Changes recommended\n\nPlease fix the parser.", want: copilotVerdictFindings},
		{name: "verdict after marker", body: "<!-- ccr-overview-v2 -->\n\n### 🟢 Approval recommended", want: copilotVerdictClean},
		{name: "legacy overview", body: "## Pull request overview\n\nCopilot generated no new comments.", want: copilotVerdictUnknown},
		{name: "empty", want: copilotVerdictUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCopilotReview(tc.body); got != tc.want {
				t.Fatalf("classifyCopilotReview() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectAIReview(t *testing.T) {
	tests := []struct {
		name    string
		nodes   []aiReviewNode
		threads []aiReviewThread
		want    string
	}{
		{name: "nil", nodes: nil, want: "-"},
		{name: "empty", nodes: []aiReviewNode{}, want: "-"},
		{name: "no bots", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "human-reviewer", CommentCount: 0},
		}, want: "-"},
		{name: "coderabbit approved", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai[bot]", CommentCount: 0},
		}, want: "pass"},
		{name: "copilot approval recommended", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", ReviewBody: "### 🟢 Approval recommended", CommentCount: 0},
		}, want: "pass"},
		{name: "clean review with unresolved AI thread", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", CommentCount: 0},
		}, threads: []aiReviewThread{
			{AuthorLogin: "coderabbitai[bot]", IsResolved: false},
		}, want: "fail"},
		{name: "copilot zero comments without explicit verdict", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot-pull-request-reviewer", CommentCount: 0},
		}, want: "fail"},
		{name: "Codex zero comments without explicit clean signal", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", CommentCount: 0},
		}, want: "fail"},
		{name: "Codex explicit clean signal", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", ExplicitClean: true},
		}, want: "pass"},
		{name: "copilot needs closer look with resolved threads", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot-pull-request-reviewer", ReviewBody: "### 🔵 Needs a closer look", CommentCount: 0},
		}, threads: []aiReviewThread{
			{AuthorLogin: "copilot-pull-request-reviewer", IsResolved: true},
		}, want: "fail"},
		{name: "copilot changes recommended", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot-pull-request-reviewer", ReviewBody: "### 🟡 Changes recommended", CommentCount: 0},
		}, want: "fail"},
		{name: "bot with comments no threads", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "coderabbitai[bot]", CommentCount: 3},
		}, want: "fail"},
		{name: "bot with comments all threads resolved", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "coderabbitai[bot]", CommentCount: 2},
		}, threads: []aiReviewThread{
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
		}, want: "pass"},
		{name: "bot with comments some threads unresolved", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "coderabbitai[bot]", CommentCount: 2},
		}, threads: []aiReviewThread{
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
			{AuthorLogin: "coderabbitai[bot]", IsResolved: false},
		}, want: "fail"},
		{name: "bot with comments only human threads resolved", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "coderabbitai[bot]", CommentCount: 2},
		}, threads: []aiReviewThread{
			{AuthorLogin: "human-reviewer", IsResolved: true},
		}, want: "fail"},
		{name: "bot changes requested", nodes: []aiReviewNode{
			{State: "CHANGES_REQUESTED", AuthorLogin: "coderabbitai[bot]", CommentCount: 5},
		}, want: "fail"},
		{name: "bot changes requested all threads resolved", nodes: []aiReviewNode{
			{State: "CHANGES_REQUESTED", AuthorLogin: "coderabbitai[bot]", CommentCount: 2},
		}, threads: []aiReviewThread{
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
		}, want: "pass"},
		{name: "mixed bot approved and human", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai[bot]", CommentCount: 0},
			{State: "CHANGES_REQUESTED", AuthorLogin: "human-reviewer", CommentCount: 2},
		}, want: "pass"},
		{name: "issues override approval", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai[bot]", CommentCount: 0},
			{State: "CHANGES_REQUESTED", AuthorLogin: "copilot[bot]", CommentCount: 1},
		}, want: "fail"},
		{name: "issues override approval but threads resolved", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai[bot]", CommentCount: 0},
			{State: "CHANGES_REQUESTED", AuthorLogin: "copilot[bot]", CommentCount: 1},
		}, threads: []aiReviewThread{
			{AuthorLogin: "copilot[bot]", IsResolved: true},
		}, want: "pass"},
		{name: "newer clean review supersedes older findings", nodes: []aiReviewNode{
			{State: "CHANGES_REQUESTED", AuthorLogin: "copilot[bot]", CommentCount: 1},
			{State: "COMMENTED", AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", ExplicitClean: true},
		}, want: "pass"},
		{name: "dismissed bot review ignored", nodes: []aiReviewNode{
			{State: "DISMISSED", AuthorLogin: "coderabbitai[bot]", CommentCount: 0},
		}, want: "-"},
		{name: "graphql bot typename without suffix", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai", AuthorType: "Bot", CommentCount: 0},
		}, want: "pass"},
		{name: "graphql bot typename with issues", nodes: []aiReviewNode{
			{State: "CHANGES_REQUESTED", AuthorLogin: "coderabbitai", AuthorType: "Bot", CommentCount: 3},
		}, want: "fail"},
		{name: "graphql bot typename issues all resolved", nodes: []aiReviewNode{
			{State: "CHANGES_REQUESTED", AuthorLogin: "coderabbitai", AuthorType: "Bot", CommentCount: 3},
		}, threads: []aiReviewThread{
			{AuthorLogin: "coderabbitai", AuthorType: "Bot", IsResolved: true},
			{AuthorLogin: "coderabbitai", AuthorType: "Bot", IsResolved: true},
		}, want: "pass"},
		{name: "thread with empty author not counted", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", CommentCount: 1},
		}, threads: []aiReviewThread{
			{AuthorLogin: "", IsResolved: true},
		}, want: "fail"},
		{name: "mixed ai bots one resolved one unresolved", nodes: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "coderabbitai[bot]", CommentCount: 1},
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", CommentCount: 1},
		}, threads: []aiReviewThread{
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
			{AuthorLogin: "copilot[bot]", IsResolved: false},
		}, want: "fail"},
		{name: "bot approved with comments no threads", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai[bot]", CommentCount: 2},
		}, want: "fail"},
		{name: "bot approved with comments all threads resolved", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai[bot]", CommentCount: 2},
		}, threads: []aiReviewThread{
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
			{AuthorLogin: "coderabbitai[bot]", IsResolved: true},
		}, want: "pass"},
		{name: "bot approved with comments some threads unresolved", nodes: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "copilot[bot]", CommentCount: 3},
		}, threads: []aiReviewThread{
			{AuthorLogin: "copilot[bot]", IsResolved: true},
			{AuthorLogin: "copilot[bot]", IsResolved: false},
		}, want: "fail"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectAIReview(tc.nodes, tc.threads); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestIsAIReviewClean(t *testing.T) {
	tests := []struct {
		name    string
		reviews []aiReviewNode
		threads []aiReviewThread
		want    bool
	}{
		{name: "nil", reviews: nil, want: false},
		{name: "empty", reviews: []aiReviewNode{}, want: false},
		{name: "no bots", reviews: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "human-reviewer", CommentCount: 0},
		}, want: false},
		{name: "copilot approved 0 comments", reviews: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "copilot[bot]", CommentCount: 0},
		}, want: true},
		{name: "copilot approved with comments", reviews: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "copilot[bot]", CommentCount: 2},
		}, want: false},
		{name: "copilot approval recommended", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot-pull-request-reviewer", ReviewBody: "### 🟢 Approval recommended", CommentCount: 0},
		}, want: true},
		{name: "copilot zero comments without explicit verdict", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot-pull-request-reviewer", CommentCount: 0},
		}, want: false},
		{name: "copilot needs closer look after resolved threads", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot-pull-request-reviewer", ReviewBody: "### 🔵 Needs a closer look", CommentCount: 0},
		}, threads: []aiReviewThread{
			{AuthorLogin: "copilot-pull-request-reviewer", IsResolved: true},
		}, want: false},
		{name: "copilot commented with comments", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", CommentCount: 3},
		}, want: false},
		{name: "copilot changes requested", reviews: []aiReviewNode{
			{State: "CHANGES_REQUESTED", AuthorLogin: "copilot[bot]", CommentCount: 2},
		}, want: false},
		{name: "latest bot review clean after earlier issues resolved", reviews: []aiReviewNode{
			{State: "CHANGES_REQUESTED", AuthorLogin: "copilot[bot]", CommentCount: 1},
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", ReviewBody: "### 🟢 Approval recommended", CommentCount: 0},
		}, threads: []aiReviewThread{
			{AuthorLogin: "copilot[bot]", IsResolved: true},
		}, want: true},
		{name: "latest bot review clean but unresolved threads", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", CommentCount: 1},
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", ReviewBody: "### 🟢 Approval recommended", CommentCount: 0},
		}, threads: []aiReviewThread{
			{AuthorLogin: "copilot[bot]", IsResolved: false},
		}, want: false},
		{name: "latest bot review has comments after clean pass", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", CommentCount: 0},
			{State: "COMMENTED", AuthorLogin: "copilot[bot]", CommentCount: 2},
		}, want: false},
		{name: "findings addressed all AI threads resolved", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", CommentCount: 2},
		}, threads: []aiReviewThread{
			{AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", IsResolved: true},
			{AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", IsResolved: true},
		}, want: true},
		{name: "findings with one unresolved thread", reviews: []aiReviewNode{
			{State: "COMMENTED", AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", CommentCount: 2},
		}, threads: []aiReviewThread{
			{AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", IsResolved: true},
			{AuthorLogin: "chatgpt-codex-connector", AuthorType: "Bot", IsResolved: false},
		}, want: false},
		{name: "unresolved human thread ignored", reviews: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "copilot[bot]", CommentCount: 0},
		}, threads: []aiReviewThread{
			{AuthorLogin: "human-reviewer", IsResolved: false},
		}, want: true},
		{name: "graphql bot typename clean", reviews: []aiReviewNode{
			{State: "APPROVED", AuthorLogin: "coderabbitai", AuthorType: "Bot", CommentCount: 0},
		}, want: true},
		{name: "dismissed review", reviews: []aiReviewNode{
			{State: "DISMISSED", AuthorLogin: "copilot[bot]", CommentCount: 0},
		}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAIReviewClean(tc.reviews, tc.threads); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestApplySupplementalInfo(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		dp := displayPullRequest{}
		applySupplementalInfo(&dp, prSupplementalInfo{}, true)
		if dp.Comments != "?" || dp.AIReview != "?" {
			t.Fatalf("expected ? for unavailable supplemental, got comments=%q aiReview=%q", dp.Comments, dp.AIReview)
		}
		if dp.AIClean != nil {
			t.Fatalf("expected AIClean=nil for unavailable supplemental, got %v", *dp.AIClean)
		}
	})

	t.Run("success with data", func(t *testing.T) {
		dp := displayPullRequest{}
		applySupplementalInfo(&dp, prSupplementalInfo{Threads: reviewThreadInfo{Total: 3, Resolved: 2}, AIReview: "pass", Approvals: 1}, false)
		if dp.Comments != "2/3" {
			t.Fatalf("expected comments '2/3', got %q", dp.Comments)
		}
		if dp.AIReview != "pass" {
			t.Fatalf("expected aiReview 'pass', got %q", dp.AIReview)
		}
		if dp.Approvals != 1 {
			t.Fatalf("expected approvals 1, got %d", dp.Approvals)
		}
	})

	t.Run("empty ai review defaults to dash", func(t *testing.T) {
		dp := displayPullRequest{}
		applySupplementalInfo(&dp, prSupplementalInfo{}, false)
		if dp.AIReview != "-" {
			t.Fatalf("expected '-' for empty aiReview, got %q", dp.AIReview)
		}
	})

	t.Run("ai clean propagates", func(t *testing.T) {
		dp := displayPullRequest{}
		applySupplementalInfo(&dp, prSupplementalInfo{Threads: reviewThreadInfo{Total: 2, Resolved: 2}, AIReview: "pass", AIClean: true, Approvals: 1}, false)
		if dp.AIClean == nil || !*dp.AIClean {
			t.Fatalf("expected AIClean=true, got %v", dp.AIClean)
		}
		if dp.Comments != "2/2" {
			t.Fatalf("expected comments '2/2' (clean not embedded), got %q", dp.Comments)
		}
	})

	t.Run("ai not clean omits pointer", func(t *testing.T) {
		dp := displayPullRequest{}
		applySupplementalInfo(&dp, prSupplementalInfo{Threads: reviewThreadInfo{Total: 1, Resolved: 0}, AIReview: "fail", AIClean: false, Approvals: 0}, false)
		if dp.AIClean != nil {
			t.Fatalf("expected AIClean=nil when not clean, got %v", *dp.AIClean)
		}
	})
}

func TestEnrichPullRequests(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	prs := []pullRequest{
		{Number: 1, Title: "PR1", State: "OPEN", UpdatedAt: now},
		{Number: 2, Title: "PR2", State: "OPEN", UpdatedAt: now},
	}
	supp := map[int]prSupplementalInfo{
		1: {Threads: reviewThreadInfo{Total: 1, Resolved: 1}, AIReview: "pass"},
	}
	required := map[string]map[string]bool{}

	rendered := enrichPullRequests(prs, prSupplementalData{Info: supp}, required, nil, now)
	if len(rendered) != 2 {
		t.Fatalf("expected 2, got %d", len(rendered))
	}
	if rendered[0].AIReview != "pass" {
		t.Fatalf("expected 'pass' for PR1, got %q", rendered[0].AIReview)
	}
	if rendered[1].AIReview != "?" {
		t.Fatalf("expected '?' for PR2 (unknown supplemental data), got %q", rendered[1].AIReview)
	}
	if rendered[1].Comments != "?" {
		t.Fatalf("expected '?' comments for PR2, got %q", rendered[1].Comments)
	}
}

func TestEnrichPullRequestsShowsApproversOnlyForOpenGitHubState(t *testing.T) {
	now := time.Date(2026, 9, 22, 16, 0, 0, 0, time.UTC)
	approval := approverApproval{Login: "reviewer", ApprovedAt: now.Add(-2 * time.Hour)}
	prs := []pullRequest{
		{Number: 1, State: "OPEN", UpdatedAt: now},
		{Number: 2, State: "OPEN", IsDraft: true, UpdatedAt: now},
		{Number: 3, State: "CLOSED", UpdatedAt: now},
		{Number: 4, State: "MERGED", UpdatedAt: now},
	}
	info := make(map[int]prSupplementalInfo, len(prs))
	for _, pr := range prs {
		info[pr.Number] = prSupplementalInfo{Approvals: 1, Approvers: []approverApproval{approval}}
	}

	got := enrichPullRequests(prs, prSupplementalData{Info: info}, nil, nil, now)
	if len(got[0].Approvers) != 1 || got[0].Approvers[0].Age != "2h" {
		t.Fatalf("open approvers = %#v, want one approval aged 2h", got[0].Approvers)
	}
	if len(got[1].Approvers) != 1 {
		t.Fatalf("draft GitHub OPEN approvers = %#v, want one", got[1].Approvers)
	}
	if len(got[2].Approvers) != 0 || len(got[3].Approvers) != 0 {
		t.Fatalf("non-open approvers must be hidden, closed=%#v merged=%#v", got[2].Approvers, got[3].Approvers)
	}
	for _, pr := range got {
		if pr.Approvals != 1 {
			t.Fatalf("PR #%d approval count = %d, want 1", pr.Number, pr.Approvals)
		}
	}
}

func TestParsePRSupplementalNode(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		raw := []byte(`{
			"number": 42,
			"comments": {"totalCount": 0, "nodes": []},
			"reviewThreads": {
				"totalCount": 2,
				"nodes": [
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot[bot]", "__typename": "Bot"}}]}},
					{"isResolved": false, "comments": {"nodes": [{"author": {"login": "user1", "__typename": "User"}}]}}
				]
			},
			"reviews": {
				"totalCount": 2,
				"nodes": [
					{"state": "APPROVED", "author": {"login": "copilot[bot]", "__typename": "Bot"}, "commit": {"oid": "abc"}, "comments": {"totalCount": 0}},
					{"state": "APPROVED", "author": {"login": "carol", "__typename": "User"}, "commit": {"oid": "abc"}, "comments": {"totalCount": 0}}
				]
			},
			"approvedReviews": {
				"nodes": [
					{"state": "APPROVED", "submittedAt": "2026-09-22T13:00:00Z", "author": {"login": "alice", "__typename": "User"}},
					{"state": "APPROVED", "submittedAt": "2026-09-22T15:00:00Z", "author": {"login": "Alice", "__typename": "User"}},
					{"state": "APPROVED", "submittedAt": "2026-09-22T14:00:00Z", "author": {"login": "bob", "__typename": "User"}},
					{"state": "APPROVED", "submittedAt": "2026-09-22T12:00:00Z", "author": {"login": "carol", "__typename": "User"}}
				]
			}
		}`)
		num, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if num != 42 {
			t.Fatalf("expected number 42, got %d", num)
		}
		if info.Threads.Total != 2 {
			t.Fatalf("expected total threads 2, got %d", info.Threads.Total)
		}
		if info.Threads.Resolved != 1 {
			t.Fatalf("expected resolved 1, got %d", info.Threads.Resolved)
		}
		if info.Approvals != 3 || len(info.Approvers) != 3 {
			t.Fatalf("expected 3 unique approvers, got count=%d details=%d", info.Approvals, len(info.Approvers))
		}
		if info.Approvers[0].Login != "Alice" || !info.Approvers[0].ApprovedAt.Equal(time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC)) {
			t.Fatalf("latest Alice approval = %#v, want 15:00 UTC", info.Approvers[0])
		}
		if !info.AIClean {
			t.Fatalf("expected AIClean=true for bot APPROVED with 0 comments")
		}
	})

	t.Run("AI clean when latest review clean and prior comments resolved", func(t *testing.T) {
		raw := []byte(`{
			"number": 438,
			"comments": {"totalCount": 0, "nodes": []},
			"reviewThreads": {
				"totalCount": 2,
				"nodes": [
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}}
				]
			},
			"reviews": {
				"totalCount": 2,
				"nodes": [
					{"state": "COMMENTED", "body": "### 🟡 Changes recommended", "author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}, "commit": {"oid": "abc"}, "comments": {"totalCount": 2}},
					{"state": "COMMENTED", "body": "### 🟢 Approval recommended", "author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}, "commit": {"oid": "abc"}, "comments": {"totalCount": 0}}
				]
			},
			"approvedReviews": {"nodes": []}
		}`)
		num, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if num != 438 {
			t.Fatalf("expected number 438, got %d", num)
		}
		if info.Threads.Total != 2 || info.Threads.Resolved != 2 {
			t.Fatalf("expected comments 2/2, got %d/%d", info.Threads.Resolved, info.Threads.Total)
		}
		if info.AIReview != "pass" {
			t.Fatalf("expected AIReview pass after all AI threads resolved, got %q", info.AIReview)
		}
		if !info.AIClean {
			t.Fatalf("expected AIClean=true when latest bot review is clean and all threads resolved")
		}
	})

	t.Run("captured current-head Copilot closer-look verdict fails", func(t *testing.T) {
		raw := []byte(`{
			"number": 111,
			"headRefOid": "b694974bc2ccc8230686fe42cd50d8f476997383",
			"comments": {"totalCount": 1, "nodes": [{
				"body": "Snyk checks have passed.",
				"createdAt": "2026-09-01T18:48:59Z",
				"author": {"login": "snyk-io", "__typename": "User"}
			}]},
			"reviewThreads": {
				"totalCount": 7,
				"nodes": [
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]}}
				]
			},
			"reviews": {"totalCount": 1, "nodes": [{
				"state": "COMMENTED",
				"body": "### 🔵 Needs a closer look\n\nA few updated instructions are internally inconsistent and one portable command is not portable.",
				"submittedAt": "2026-09-09T21:37:17Z",
				"commit": {"oid": "b694974bc2ccc8230686fe42cd50d8f476997383"},
				"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"},
				"comments": {"totalCount": 0}
			}]},
			"approvedReviews": {"nodes": []}
		}`)

		num, info, ok := parsePRSupplementalNode(raw)
		if !ok || num != 111 {
			t.Fatalf("expected captured PR 111, got number=%d ok=%v", num, ok)
		}
		if info.AIReview != "fail" || info.AIClean {
			t.Fatalf("expected closer-look verdict to fail without clean marker, got review=%q clean=%v", info.AIReview, info.AIClean)
		}
		if info.Threads.Total != 7 || info.Threads.Resolved != 7 {
			t.Fatalf("expected captured threads 7/7, got %d/%d", info.Threads.Resolved, info.Threads.Total)
		}
		if info.Approvals != 0 {
			t.Fatalf("expected no formal approvals, got %d", info.Approvals)
		}
	})

	t.Run("current-head clean Codex comment counts as AI pass", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "682de6badb7404709e1183f4e8ed194c9ae6e34a",
			"comments": {"nodes": [{
				"body": "Codex Review: Didn't find any major issues. Nice work!\n\n**Reviewed commit:** 682de6badb",
				"createdAt": "2026-07-18T20:00:00Z",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"nodes": []},
			"approvedReviews": {"nodes": []}
		}`)

		num, info, ok := parsePRSupplementalNode(raw)
		if !ok || num != 47 {
			t.Fatalf("expected PR 47, got number=%d ok=%v", num, ok)
		}
		if info.AIReview != "pass" || !info.AIClean {
			t.Fatalf("expected clean AI pass, got review=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("current-head completed Codex summary and PR reaction count as AI pass", func(t *testing.T) {
		raw := []byte(`{
			"number": 140,
			"headRefOid": "f92ca525d5a267d4a4c544ed1f0f90e1e3d63802",
			"comments": {"totalCount": 1, "nodes": [{
				"body": "<!-- codex-pull-request-review-summary -->\n\n| Review | Status | Commit | Review trigger |\n| --- | --- | --- | --- |\n| 📝 **Code Review** | ✅ **Completed** <relative-time datetime=\"2026-09-20T21:49:39.029122Z\">2026-09-20T21:49:39.029122Z</relative-time> | ` + "`f92ca52`" + ` | PR opened |",
				"createdAt": "2026-09-20T21:46:24Z",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reactions": {"totalCount": 1, "nodes": [{
				"createdAt": "2026-09-20T21:49:41Z",
				"user": {"login": "chatgpt-codex-connector[bot]", "__typename": "User"}
			}]},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"totalCount": 0, "nodes": []},
			"approvedReviews": {"nodes": []}
		}`)

		num, info, ok := parsePRSupplementalNode(raw)
		if !ok || num != 140 {
			t.Fatalf("expected PR 140, got number=%d ok=%v", num, ok)
		}
		if info.AIReview != "pass" || !info.AIClean {
			t.Fatalf("expected clean AI pass from current-head summary and reaction, got review=%q clean=%v", info.AIReview, info.AIClean)
		}
		if info.Approvals != 0 {
			t.Fatalf("PR reaction must not count as a formal approval, got %d", info.Approvals)
		}
	})

	t.Run("Codex reaction completeness fails closed unless clean evidence is present", func(t *testing.T) {
		const base = `{
			"number": 140,
			"headRefOid": "f92ca525d5a267d4a4c544ed1f0f90e1e3d63802",
			"closingIssuesReferences": {"totalCount": 0, "nodes": []},
			"comments": {"totalCount": 1, "nodes": [{
				"body": "<!-- codex-pull-request-review-summary -->\n\n| Review | Status | Commit | Review trigger |\n| --- | --- | --- | --- |\n| 📝 **Code Review** | ✅ **Completed** <relative-time datetime=\"2026-09-20T21:49:39.029122Z\">done</relative-time> | ` + "`f92ca52`" + ` | PR opened |",
				"createdAt": "2026-09-20T21:46:24Z",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reactions": REACTIONS,
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"totalCount": 0, "nodes": []},
			"approvedReviews": {"nodes": []}
		}`
		tests := []struct {
			name        string
			reactions   string
			wantReview  string
			wantClean   bool
			wantPartial bool
		}{
			{name: "complete connection without reaction", reactions: `{"totalCount":0,"nodes":[]}`, wantReview: "-"},
			{name: "truncated connection without reaction", reactions: `{"totalCount":101,"nodes":[]}`, wantReview: "?", wantPartial: true},
			{name: "truncated connection with matching reaction", reactions: `{"totalCount":101,"nodes":[{"createdAt":"2026-09-20T21:49:41Z","user":{"login":"chatgpt-codex-connector[bot]","__typename":"User"}}]}`, wantReview: "pass", wantClean: true},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				raw := []byte(strings.Replace(base, "REACTIONS", tc.reactions, 1))
				_, info, ok := parsePRSupplementalNode(raw)
				if !ok {
					t.Fatal("expected valid supplemental node")
				}
				if info.AIReview != tc.wantReview || info.AIClean != tc.wantClean || info.Incomplete != tc.wantPartial {
					t.Fatalf("got AI=%q clean=%v incomplete=%v; want AI=%q clean=%v incomplete=%v", info.AIReview, info.AIClean, info.Incomplete, tc.wantReview, tc.wantClean, tc.wantPartial)
				}
			})
		}
	})

	t.Run("newer formal review overrides older clean Codex comment", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "682de6badb7404709e1183f4e8ed194c9ae6e34a",
			"comments": {"nodes": [{
				"body": "Codex Review: Didn't find any major issues. Nice work!\n\n**Reviewed commit:** 682de6badb",
				"createdAt": "2026-07-18T20:00:00Z",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"nodes": [{
				"state": "COMMENTED",
				"submittedAt": "2026-07-18T21:00:00Z",
				"commit": {"oid": "682de6badb7404709e1183f4e8ed194c9ae6e34a"},
				"author": {"login": "coderabbitai[bot]", "__typename": "Bot"},
				"comments": {"totalCount": 1}
			}]},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIClean {
			t.Fatal("expected newer formal review with findings to keep AIClean false")
		}
		if info.AIReview != "fail" {
			t.Fatalf("expected newer formal review to set AIReview=fail, got %q", info.AIReview)
		}
	})

	t.Run("newer clean Codex comment overrides older formal review", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "682de6badb7404709e1183f4e8ed194c9ae6e34a",
			"comments": {"nodes": [{
				"body": "Codex Review: Didn't find any major issues. Nice work!\n\n**Reviewed commit:** 682de6badb",
				"createdAt": "2026-07-18T21:00:00Z",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"nodes": [{
				"state": "COMMENTED",
				"submittedAt": "2026-07-18T20:00:00Z",
				"commit": {"oid": "682de6badb7404709e1183f4e8ed194c9ae6e34a"},
				"author": {"login": "coderabbitai[bot]", "__typename": "Bot"},
				"comments": {"totalCount": 1}
			}]},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if !info.AIClean {
			t.Fatal("expected newer clean Codex comment to set AIClean true")
		}
		if info.AIReview != "pass" {
			t.Fatalf("expected newer Codex comment to set AIReview=pass, got %q", info.AIReview)
		}
	})

	t.Run("stale formal AI review is ignored", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "current-head",
			"comments": {"totalCount": 0, "nodes": []},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"totalCount": 1, "nodes": [{
				"state": "APPROVED",
				"commit": {"oid": "old-head"},
				"author": {"login": "coderabbitai[bot]", "__typename": "Bot"},
				"comments": {"totalCount": 0}
			}]},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "-" || info.AIClean {
			t.Fatalf("expected stale formal review to be ignored, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("stale clean Codex comment is ignored", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "aaaaaaaaaabbbbbbbbbbccccccccccdddddddddd",
			"comments": {"nodes": [{
				"body": "Codex Review: Didn't find any major issues. Nice work!\n\n**Reviewed commit:** 682de6badb",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"nodes": []},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "-" || info.AIClean {
			t.Fatalf("expected stale review to be ignored, got review=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("current-head Codex findings count as AI failure", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "682de6badb7404709e1183f4e8ed194c9ae6e34a",
			"comments": {"nodes": [{
				"body": "Codex Review: Found an issue that should be addressed.\n\n**Reviewed commit:** 682de6badb",
				"createdAt": "2026-07-18T20:00:00Z",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"nodes": []},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "fail" || info.AIClean {
			t.Fatalf("expected AI failure, got review=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("truncated supplemental connections fail closed", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "current-head",
			"comments": {
				"totalCount": 101,
				"nodes": [{
					"body": "Codex Review: Didn't find any major issues. Reviewed commit: current-he",
					"createdAt": "2026-07-18T20:00:00Z",
					"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
				}]
			},
			"reviewThreads": {"totalCount": 101, "nodes": []},
			"reviews": {
				"totalCount": 101,
				"nodes": [{
					"state": "APPROVED",
					"commit": {"oid": "current-head"},
					"author": {"login": "coderabbitai[bot]", "__typename": "Bot"},
					"comments": {"totalCount": 0}
				}]
			},
			"approvedReviews": {"totalCount": 1, "nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "?" || info.AIClean {
			t.Fatalf("expected truncated data to fail closed, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("more than 100 historical reviews keep complete current-head evidence", func(t *testing.T) {
		raw := []byte(`{
			"number": 593,
			"headRefOid": "current-head",
			"comments": {
				"totalCount": 49,
				"nodes": [{
					"body": "Codex Review: Didn't find any major issues. Reviewed commit: current-he",
					"createdAt": "2026-08-24T16:00:00Z",
					"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
				}]
			},
			"reviewThreads": {
				"totalCount": 2,
				"nodes": [
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "cubic-dev-ai[bot]", "__typename": "Bot"}}]}},
					{"isResolved": true, "comments": {"nodes": [{"author": {"login": "cubic-dev-ai[bot]", "__typename": "Bot"}}]}}
				]
			},
			"reviews": {
				"totalCount": 159,
				"nodes": [{
					"state": "COMMENTED",
					"submittedAt": "2026-08-24T15:00:00Z",
					"commit": {"oid": "old-head"},
					"author": {"login": "cubic-dev-ai[bot]", "__typename": "Bot"},
					"comments": {"totalCount": 1}
				}]
			},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "pass" || !info.AIClean {
			t.Fatalf("expected complete current-head evidence to pass despite old review truncation, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("independent review and comment pagination fails closed when ordering is ambiguous", func(t *testing.T) {
		raw := []byte(`{
			"number": 593,
			"headRefOid": "current-head",
			"comments": {
				"totalCount": 101,
				"nodes": [{
					"body": "Codex Review: Didn't find any major issues. Reviewed commit: current-he",
					"createdAt": "2026-08-24T14:00:00Z",
					"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
				}]
			},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {
				"totalCount": 159,
				"nodes": [{
					"state": "COMMENTED",
					"submittedAt": "2026-08-24T15:00:00Z",
					"commit": {"oid": "old-head"},
					"author": {"login": "cubic-dev-ai[bot]", "__typename": "Bot"},
					"comments": {"totalCount": 1}
				}]
			},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "?" || info.AIClean {
			t.Fatalf("expected ambiguous independent pagination to fail closed, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("tied current-head formal and conversation evidence fails closed", func(t *testing.T) {
		raw := []byte(`{
			"number": 593,
			"headRefOid": "current-head",
			"comments": {
				"totalCount": 49,
				"nodes": [{
					"body": "Codex Review: Didn't find any major issues. Reviewed commit: current-he",
					"createdAt": "2026-08-24T16:00:00Z",
					"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
				}]
			},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {
				"totalCount": 159,
				"nodes": [{
					"state": "COMMENTED",
					"submittedAt": "2026-08-24T16:00:00Z",
					"commit": {"oid": "current-head"},
					"author": {"login": "cubic-dev-ai[bot]", "__typename": "Bot"},
					"comments": {"totalCount": 1}
				}]
			},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "?" || info.AIClean {
			t.Fatalf("expected tied current-head evidence to fail closed, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("current-head conversation evidence without a timestamp fails closed", func(t *testing.T) {
		raw := []byte(`{
			"number": 593,
			"headRefOid": "current-head",
			"comments": {"totalCount": 1, "nodes": [{
				"body": "Codex Review: Didn't find any major issues. Reviewed commit: current-he",
				"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
			}]},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"totalCount": 1, "nodes": [{
				"state": "APPROVED",
				"submittedAt": "2026-08-24T16:00:00Z",
				"commit": {"oid": "current-head"},
				"author": {"login": "cubic-dev-ai[bot]", "__typename": "Bot"},
				"comments": {"totalCount": 0}
			}]},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "?" || info.AIClean {
			t.Fatalf("expected missing conversation evidence timestamp to fail closed, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("truncated reviews without current-head AI evidence fail closed", func(t *testing.T) {
		raw := []byte(`{
			"number": 594,
			"headRefOid": "current-head",
			"comments": {"totalCount": 0, "nodes": []},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {
				"totalCount": 159,
				"nodes": [{
					"state": "APPROVED",
					"commit": {"oid": "old-head"},
					"author": {"login": "cubic-dev-ai[bot]", "__typename": "Bot"},
					"comments": {"totalCount": 0}
				}]
			},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "?" || info.AIClean {
			t.Fatalf("expected incomplete current-head evidence to fail closed, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("truncated older conversation comments keep recent review evidence", func(t *testing.T) {
		raw := []byte(`{
			"number": 47,
			"headRefOid": "current-head",
			"comments": {
				"totalCount": 101,
				"nodes": [{
					"body": "Codex Review: Didn't find any major issues. Reviewed commit: current-he",
					"createdAt": "2026-07-18T20:00:00Z",
					"author": {"login": "chatgpt-codex-connector", "__typename": "Bot"}
				}]
			},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"totalCount": 0, "nodes": []},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "pass" || !info.AIClean {
			t.Fatalf("expected recent clean review to survive old comment truncation, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("truncated comments without current-head Codex evidence fail closed", func(t *testing.T) {
		raw := []byte(`{
			"number": 48,
			"headRefOid": "current-head",
			"comments": {"totalCount": 101, "nodes": []},
			"reviewThreads": {"totalCount": 0, "nodes": []},
			"reviews": {"totalCount": 1, "nodes": [{
				"state": "APPROVED",
				"commit": {"oid": "current-head"},
				"author": {"login": "coderabbitai[bot]", "__typename": "Bot"},
				"comments": {"totalCount": 0}
			}]},
			"approvedReviews": {"nodes": []}
		}`)

		_, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			t.Fatal("expected valid supplemental node")
		}
		if info.AIReview != "?" || info.AIClean {
			t.Fatalf("expected missing current-head Codex evidence to fail closed, got AI=%q clean=%v", info.AIReview, info.AIClean)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, _, ok := parsePRSupplementalNode([]byte(`{invalid`))
		if ok {
			t.Fatalf("expected ok=false for invalid JSON")
		}
	})

	t.Run("zero number returns not-ok", func(t *testing.T) {
		_, _, ok := parsePRSupplementalNode([]byte(`{"number": 0, "reviewThreads": {"totalCount": 0, "nodes": []}, "reviews": {"nodes": []}, "approvedReviews": {"nodes": []}}`))
		if ok {
			t.Fatalf("expected ok=false for zero PR number")
		}
	})
}

func TestCountResolvedThreads(t *testing.T) {
	threads := []aiReviewThread{
		{IsResolved: true},
		{IsResolved: false},
		{IsResolved: true},
	}
	if got := countResolvedThreads(threads); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
	if got := countResolvedThreads(nil); got != 0 {
		t.Fatalf("expected 0 for nil, got %d", got)
	}
}

func TestConnectionIncomplete(t *testing.T) {
	tests := []struct {
		name               string
		totalCount         int
		fetchedCount       int
		sufficientEvidence bool
		want               bool
	}{
		{name: "complete connection", totalCount: 100, fetchedCount: 100, want: false},
		{name: "truncated without sufficient evidence", totalCount: 101, fetchedCount: 100, want: true},
		{name: "truncated with sufficient evidence", totalCount: 159, fetchedCount: 100, sufficientEvidence: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectionIncomplete(tt.totalCount, tt.fetchedCount, tt.sufficientEvidence); got != tt.want {
				t.Fatalf("connectionIncomplete() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReviewWindowPredatesEvidence(t *testing.T) {
	t0 := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t1.Add(time.Hour)
	reviews := []aiReviewNode{{OccurredAt: t2}, {OccurredAt: t1}}

	if !reviewWindowPredatesEvidence(reviews, t2) {
		t.Fatal("expected newer evidence to establish the formal review boundary")
	}
	if reviewWindowPredatesEvidence(reviews, t0) {
		t.Fatal("expected older evidence to leave the formal review boundary ambiguous")
	}
	if reviewWindowPredatesEvidence(reviews, t1) {
		t.Fatal("expected equal timestamps across connections to remain ambiguous")
	}
	if reviewWindowPredatesEvidence([]aiReviewNode{{}}, t2) {
		t.Fatal("expected a missing formal review timestamp to fail closed")
	}
	if reviewWindowPredatesEvidence(reviews, time.Time{}) {
		t.Fatal("expected a missing evidence timestamp to fail closed")
	}
}

func TestReviewEvidenceOrderAmbiguous(t *testing.T) {
	evidenceAt := time.Date(2026, 8, 24, 16, 0, 0, 0, time.UTC)
	head := "current-head"

	tests := []struct {
		name    string
		reviews []aiReviewNode
		want    bool
	}{
		{name: "no formal AI evidence", reviews: []aiReviewNode{{AuthorLogin: "human", CommitOID: head, OccurredAt: evidenceAt}}, want: false},
		{name: "formal evidence is older", reviews: []aiReviewNode{{AuthorType: "Bot", CommitOID: head, OccurredAt: evidenceAt.Add(-time.Minute)}}, want: false},
		{name: "formal evidence is newer", reviews: []aiReviewNode{{AuthorType: "Bot", CommitOID: head, OccurredAt: evidenceAt.Add(time.Minute)}}, want: false},
		{name: "formal evidence is tied", reviews: []aiReviewNode{{AuthorType: "Bot", CommitOID: head, OccurredAt: evidenceAt}}, want: true},
		{name: "formal evidence timestamp is missing", reviews: []aiReviewNode{{AuthorType: "Bot", CommitOID: head}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reviewEvidenceOrderAmbiguous(tt.reviews, head, evidenceAt, true); got != tt.want {
				t.Fatalf("reviewEvidenceOrderAmbiguous() = %v, want %v", got, tt.want)
			}
		})
	}

	if reviewEvidenceOrderAmbiguous([]aiReviewNode{{AuthorType: "Bot", CommitOID: head}}, head, time.Time{}, false) {
		t.Fatal("expected absent conversation evidence to avoid a cross-connection ambiguity")
	}
	if !reviewEvidenceOrderAmbiguous(nil, head, time.Time{}, true) {
		t.Fatal("expected present conversation evidence with a missing timestamp to fail closed")
	}
}

func TestCountUniqueApprovers(t *testing.T) {
	logins := []string{"alice", "Alice", "bob", "", "alice"}
	if got := countUniqueApprovers(logins); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
	if got := countUniqueApprovers(nil); got != 0 {
		t.Fatalf("expected 0 for nil, got %d", got)
	}
}

func TestParsePRSupplementalNodeMarksTruncatedApprovalsIncomplete(t *testing.T) {
	raw := []byte(`{
		"number": 42,
		"closingIssuesReferences": {"totalCount": 0, "nodes": []},
		"comments": {"totalCount": 0, "nodes": []},
		"reactions": {"totalCount": 0, "nodes": []},
		"reviewThreads": {"totalCount": 0, "nodes": []},
		"reviews": {"totalCount": 0, "nodes": []},
		"approvedReviews": {
			"totalCount": 101,
			"pageInfo": {"hasPreviousPage": true},
			"nodes": [{"state":"APPROVED","submittedAt":"2026-09-22T15:00:00Z","author":{"login":"alice"}}]
		}
	}`)

	number, info, ok := parsePRSupplementalNode(raw)
	if !ok || number != 42 {
		t.Fatalf("parse result = number %d, ok %v; want 42, true", number, ok)
	}
	if !info.ApprovalsIncomplete || !info.Incomplete {
		t.Fatalf("incomplete flags = approvals %v, supplemental %v; want both true", info.ApprovalsIncomplete, info.Incomplete)
	}
	if info.Approvals != 0 || len(info.Approvers) != 0 {
		t.Fatalf("partial approvals must not render: count=%d details=%#v", info.Approvals, info.Approvers)
	}
}

func TestApproverApprovalsKeepsLatestDecisionAndSortsApprovals(t *testing.T) {
	var data supplementalNodeData
	raw := []byte(`{"approvedReviews":{"nodes":[
		{"state":"APPROVED","submittedAt":"2026-09-22T13:00:00Z","author":{"login":"Alice"}},
		{"state":"CHANGES_REQUESTED","submittedAt":"2026-09-22T13:30:00Z","author":{"login":"carol"}},
		{"state":"CHANGES_REQUESTED","submittedAt":"2026-09-22T14:00:00Z","author":{"login":"bob"}},
		{"state":"DISMISSED","submittedAt":"2026-09-22T14:30:00Z","author":{"login":"dave"}},
		{"state":"APPROVED","submittedAt":"2026-09-22T14:45:00Z","author":{"login":"eve"}},
		{"state":"CHANGES_REQUESTED","submittedAt":"2026-09-22T14:45:00Z","author":{"login":"eve"}},
		{"state":"APPROVED","submittedAt":"2026-09-22T15:00:00Z","author":{"login":"alice"}},
		{"state":"APPROVED","submittedAt":"2026-09-22T16:00:00Z","author":{"login":"bob"}},
		{"state":"APPROVED","submittedAt":"2026-09-22T17:00:00Z","author":{"login":"carol"}},
		{"state":"CHANGES_REQUESTED","submittedAt":"2026-09-22T18:00:00Z","author":{"login":"carol"}},
		{"state":"APPROVED","submittedAt":"2026-09-22T19:00:00Z","author":{"login":""}}
	]}}`)
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}

	got := approverApprovals(&data)
	if len(got) != 2 {
		t.Fatalf("approvers = %#v, want Alice and Bob only", got)
	}
	if got[0].Login != "bob" || !got[0].ApprovedAt.Equal(time.Date(2026, 9, 22, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("latest Bob approval = %#v, want 16:00 UTC", got[0])
	}
	if got[1].Login != "alice" || !got[1].ApprovedAt.Equal(time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("latest Alice approval = %#v, want 15:00 UTC", got[1])
	}
}

func TestAllAIThreadsResolved(t *testing.T) {
	t.Run("all resolved", func(t *testing.T) {
		threads := []aiReviewThread{
			{AuthorLogin: "copilot[bot]", IsResolved: true},
			{AuthorLogin: "human", IsResolved: false},
		}
		if !allAIThreadsResolved(threads) {
			t.Fatalf("expected true when all AI threads resolved")
		}
	})

	t.Run("unresolved AI thread", func(t *testing.T) {
		threads := []aiReviewThread{
			{AuthorLogin: "copilot[bot]", IsResolved: false},
		}
		if allAIThreadsResolved(threads) {
			t.Fatalf("expected false when AI thread unresolved")
		}
	})

	t.Run("no AI threads", func(t *testing.T) {
		threads := []aiReviewThread{
			{AuthorLogin: "human", IsResolved: false},
		}
		if allAIThreadsResolved(threads) {
			t.Fatalf("expected false when no AI threads exist")
		}
	})
}

func TestParseSupplementalResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{
			name:    "valid with one PR",
			input:   `{"data":{"repository":{"pr42":{"number":42,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":2,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},"approvedReviews":{"nodes":[]}}}}}`,
			wantLen: 1,
		},
		{
			name:    "empty repository",
			input:   `{"data":{"repository":{}}}`,
			wantLen: 0,
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, errored, err := parseSupplementalResponse([]byte(tc.input))
			_ = errored
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got result with %d entries", len(result))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != tc.wantLen {
				t.Fatalf("got %d entries, want %d", len(result), tc.wantLen)
			}
		})
	}
}

func TestParseSupplementalResponseWithThreadComments(t *testing.T) {
	// Thread with empty Comments.Nodes — tests boundary mutation at line 967
	emptyComments := `{"data":{"repository":{"pr42":{
		"number":42,
		"comments":{"totalCount":0,"nodes":[]},
		"reactions":{"totalCount":0,"nodes":[]},
		"reviewThreads":{
			"totalCount":1,
			"nodes":[{"isResolved":false,"comments":{"nodes":[]}}]
		},
		"reviews":{"totalCount":0,"nodes":[]},
		"approvedReviews":{"nodes":[]}
	}}}}`

	result, _, err := parseSupplementalResponse([]byte(emptyComments))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d entries, want 1", len(result))
	}

	// Thread with bot comments — tests negation mutation at line 967
	botComments := `{"data":{"repository":{"pr42":{
		"number":42,
		"comments":{"totalCount":0,"nodes":[]},
		"reactions":{"totalCount":0,"nodes":[]},
		"reviewThreads":{
			"totalCount":1,
			"nodes":[{
				"isResolved":true,
				"comments":{"nodes":[{
					"author":{"login":"copilot-pull-request-reviewer[bot]","__typename":"Bot"}
				}]}
			}]
		},
		"reviews":{"totalCount":1,"nodes":[{
			"state":"COMMENTED",
			"body":"### 🟢 Approval recommended",
			"author":{"login":"copilot-pull-request-reviewer[bot]"},
			"commit":{"oid":"abc"},
			"comments":{"totalCount":1}
		}]},
		"approvedReviews":{"nodes":[]}
	}}}}`

	result2, _, err := parseSupplementalResponse([]byte(botComments))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result2) != 1 {
		t.Fatalf("got %d entries, want 1", len(result2))
	}
	info, ok := result2[42]
	if !ok {
		t.Fatal("expected supplemental info for PR 42")
	}
	// An explicit clean verdict with all AI threads resolved reports pass.
	if info.AIReview != "pass" {
		t.Fatalf("expected AIReview='pass' (bot review with resolved threads), got %q", info.AIReview)
	}
}
