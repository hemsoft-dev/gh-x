package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const copilotLogin = "copilot-pull-request-reviewer"

// copilotBadge reproduces the picture markup of Copilot's severity badge.
func copilotBadge(severity string) string {
	icon := strings.ToLower(severity)
	return fmt.Sprintf(`<picture><source media="(prefers-color-scheme: dark)" srcset="https://github.githubassets.com/static/images/icons/copilot-code-review/%[1]s-v2-dark.svg"><img src="https://github.githubassets.com/static/images/icons/copilot-code-review/%[1]s-v2-light.png" alt="%[2]s severity" width="62" height="18" align="texttop"></picture>`, icon, severity)
}

func copilotOpenLine(severity, discussion string) string {
	return fmt.Sprintf("- %s [Example finding](https://github.com/o/r/pull/1#discussion_r%s) · New", copilotBadge(severity), discussion)
}

func copilotSection(title string, lines ...string) string {
	return fmt.Sprintf("<details open>\n<summary><strong>%s</strong></summary>\n\n%s\n</details>", title, strings.Join(lines, "\n"))
}

func copilotMissedSection(severities ...string) string {
	entries := make([]string, 0, len(severities))
	for _, severity := range severities {
		entries = append(entries, fmt.Sprintf("<details>\n<summary>%s Example finding</summary>\n\n`file.go:5`\n</details>", copilotBadge(severity)))
	}
	title := fmt.Sprintf("%d previously missed issues", len(severities))
	return copilotSection(title, append([]string{"In code that hasn't changed since last review"}, entries...)...)
}

func copilotBody(heading string, parts ...string) string {
	return "<!-- ccr-overview-v2 -->\n\n### " + heading + "\n\nExample summary.\n\n" + strings.Join(parts, "\n\n") + "\n\n<sub>Review effort: Lite</sub>"
}

const (
	copilotApproval    = "🟢 Approval recommended"
	copilotCloserLook  = "🔵 Needs a closer look"
	copilotChanges     = "🟡 Changes recommended"
	copilotNoOpen      = "**No open issues** 🎉"
	copilotZeroOpen    = "**0 open findings**"
	copilotFirstThread = "4000000001"
	copilotOtherThread = "4000000002"
)

func copilotReview(body string) aiReviewNode {
	return aiReviewNode{State: "COMMENTED", AuthorLogin: copilotLogin, AuthorType: "Bot", ReviewBody: body}
}

func copilotThread(commentID string, resolved bool) aiReviewThread {
	return aiReviewThread{AuthorLogin: copilotLogin, AuthorType: "Bot", IsResolved: resolved, CommentID: commentID}
}

func TestCopilotSeverityDecidesAIReview(t *testing.T) {
	resolvedMedium := copilotSection("1 resolved since last review", copilotOpenLine("Medium", copilotOtherThread))
	openMedium := copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Medium", copilotFirstThread)))
	openLow := copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread)))
	twoLows := copilotSection("2 open issues", copilotOpenLine("Low", copilotFirstThread), copilotOpenLine("Low", copilotOtherThread))
	otherBotApproval := aiReviewNode{State: "APPROVED", AuthorLogin: "coderabbitai[bot]", AuthorType: "Bot"}
	copilotApproved := func(body string, commentCount int) aiReviewNode {
		return aiReviewNode{State: "APPROVED", AuthorLogin: copilotLogin, AuthorType: "Bot", ReviewBody: body, CommentCount: commentCount}
	}
	tests := []struct {
		name      string
		reviews   []aiReviewNode
		threads   []aiReviewThread
		want      string
		wantClean bool
	}{
		{
			name:    "closer look with nothing open passes without the clean marker",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, copilotNoOpen))},
			want:    "pass",
		},
		{
			name:    "closer look with only low previously missed findings passes",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, copilotNoOpen, copilotMissedSection("Low", "Low", "Low")))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "pass",
		},
		{
			name:    "medium in the resolved section does not count",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, copilotZeroOpen, resolvedMedium))},
			threads: []aiReviewThread{copilotThread(copilotOtherThread, true)},
			want:    "pass",
		},
		{
			name:    "changes recommended with only low open findings passes",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread))))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "pass",
		},
		{
			name:    "open medium fails even when its thread is resolved",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Medium", copilotFirstThread))))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "fail",
		},
		{
			name:    "open high fails even when its thread is resolved",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, copilotSection("1 open issue", copilotOpenLine("High", copilotFirstThread))))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "fail",
		},
		{
			name:    "severity outranks an approval heading",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotApproval, copilotSection("1 open issue", copilotOpenLine("Medium", copilotFirstThread))))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "fail",
		},
		{
			name:    "previously missed medium fails with no thread to resolve",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, copilotNoOpen, copilotMissedSection("Medium")))},
			want:    "fail",
		},
		{
			name:    "mixed severities fail on the highest",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("2 open issues", copilotOpenLine("Low", copilotFirstThread), copilotOpenLine("High", copilotOtherThread))))},
			want:    "fail",
		},
		{
			name:    "older open section wording still counts",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("Open (1)", copilotOpenLine("Medium", copilotFirstThread))))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "fail",
		},
		{
			name:    "findings summary line above the sections counts",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** 1 "+copilotBadge("High")))},
			want:    "fail",
		},
		{
			name:    "unknown severity label fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Critical", copilotFirstThread))))},
			want:    "fail",
		},
		{
			name:    "stated count above the parsed badges fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("2 open issues", copilotOpenLine("Low", copilotFirstThread))))},
			want:    "fail",
		},
		{
			name:    "older stated count above the parsed badges fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("Open (2)", copilotOpenLine("Low", copilotFirstThread))))},
			want:    "fail",
		},
		{
			name:    "closer look with no findings evidence fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook))},
			want:    "fail",
		},
		{
			name:    "unknown heading with an empty findings list fails closed",
			reviews: []aiReviewNode{copilotReview("## Pull request overview\n\n" + copilotNoOpen)},
			want:    "fail",
		},
		{
			name:      "approval with nothing open passes with the clean marker",
			reviews:   []aiReviewNode{copilotReview(copilotBody(copilotApproval, copilotNoOpen))},
			want:      "pass",
			wantClean: true,
		},
		{
			name:      "approval with only resolved findings keeps the clean marker",
			reviews:   []aiReviewNode{copilotReview(copilotBody(copilotApproval, copilotNoOpen, resolvedMedium))},
			threads:   []aiReviewThread{copilotThread(copilotOtherThread, true)},
			want:      "pass",
			wantClean: true,
		},
		{
			name:    "approval with an open low passes without the clean marker",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotApproval, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread))))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "pass",
		},
		{
			name: "copilot changes requested with a medium fails after threads resolve",
			reviews: []aiReviewNode{{
				State: "CHANGES_REQUESTED", AuthorLogin: copilotLogin,
				ReviewBody: copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Medium", copilotFirstThread))),
			}},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "fail",
		},
		{
			name:    "a later review that drops a medium supersedes the earlier one",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Medium", copilotFirstThread)))), copilotReview(copilotBody(copilotCloserLook, copilotNoOpen))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "pass",
		},
		{
			name:    "a later approval from another bot does not clear a medium",
			reviews: []aiReviewNode{copilotReview(openMedium), otherBotApproval},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "fail",
		},
		{
			name:      "a later approval from another bot keeps its pass over an open low",
			reviews:   []aiReviewNode{copilotReview(openLow), otherBotApproval},
			threads:   []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:      "pass",
			wantClean: true,
		},
		{
			name:    "a dismissed copilot review no longer blocks",
			reviews: []aiReviewNode{{State: "DISMISSED", AuthorLogin: copilotLogin, AuthorType: "Bot", ReviewBody: openMedium}},
			want:    "-",
		},
		{
			name:    "a dismissed copilot review does not hide the review before it",
			reviews: []aiReviewNode{copilotReview(openMedium), {State: "DISMISSED", AuthorLogin: copilotLogin, AuthorType: "Bot", ReviewBody: copilotBody(copilotApproval, copilotNoOpen)}},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, true)},
			want:    "fail",
		},
		{
			name:      "copilot approved state with nothing listed earns the clean marker",
			reviews:   []aiReviewNode{copilotApproved(copilotBody("🟢 Approved"), 0)},
			want:      "pass",
			wantClean: true,
		},
		{
			name:    "copilot approved state with an open low has no clean marker",
			reviews: []aiReviewNode{copilotApproved(openLow, 0)},
			want:    "pass",
		},
		{
			name:    "copilot approved state with an open medium fails",
			reviews: []aiReviewNode{copilotApproved(openMedium, 0)},
			want:    "fail",
		},
		{
			name:    "copilot approved state passes with an unresolved low thread",
			reviews: []aiReviewNode{copilotApproved(openLow, 1)},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "pass",
		},
		{
			name:    "copilot approved state with comments and no thread data fails",
			reviews: []aiReviewNode{copilotApproved(openLow, 1)},
			want:    "fail",
		},
		{
			name:    "an unresolved low thread does not clear another bot's changes request",
			reviews: []aiReviewNode{copilotReview(openLow), {State: "CHANGES_REQUESTED", AuthorLogin: "coderabbitai[bot]", AuthorType: "Bot"}},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "fail",
		},
		{
			name:    "an unresolved low thread does not clear a codex review without a clean signal",
			reviews: []aiReviewNode{copilotReview(openLow), {State: "COMMENTED", AuthorLogin: "chatgpt-codex-connector[bot]", AuthorType: "Bot"}},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "fail",
		},
		{
			name:    "an unresolved low thread does not clear another bot's approval with comments",
			reviews: []aiReviewNode{copilotReview(openLow), {State: "APPROVED", AuthorLogin: "coderabbitai[bot]", AuthorType: "Bot", CommentCount: 1}},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "fail",
		},
		{
			name:    "findings tally matching the listed findings passes",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** 2 "+copilotBadge("Low"), twoLows))},
			want:    "pass",
		},
		{
			name:    "findings tally ignores resolved sections",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** 2 "+copilotBadge("Low"), twoLows, resolvedMedium))},
			want:    "pass",
		},
		{
			name:    "findings tally above the listed findings fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** 3 "+copilotBadge("Low"), twoLows))},
			want:    "fail",
		},
		{
			name:    "findings tally sums every severity",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** 1 "+copilotBadge("Low")+" · 2 "+copilotBadge("Low"), twoLows))},
			want:    "fail",
		},
		{
			name:    "findings count with nothing listed fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** 2 "+copilotBadge("Low")))},
			want:    "fail",
		},
		{
			name:    "explicit zero None with closer-look verdict passes without clean marker",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, "**Findings:** None"))},
			want:    "pass",
		},
		{
			name:    "explicit zero 0 with closer-look verdict passes without clean marker",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, "**Findings:** 0"))},
			want:    "pass",
		},
		{
			name:    "explicit zero None with changes verdict passes without clean marker",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** None"))},
			want:    "pass",
		},
		{
			name:    "explicit zero 0 with changes verdict passes without clean marker",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** 0"))},
			want:    "pass",
		},
		{
			name:    "explicit zero with resolved findings passes without clean marker",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, "**Findings:** None", resolvedMedium))},
			want:    "pass",
		},
		{
			name:    "explicit zero does not clear an unresolved thread",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, "**Findings:** 0"))},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "fail",
		},
		{
			name:    "explicit zero does not clear an unknown verdict",
			reviews: []aiReviewNode{copilotReview(copilotBody("Unrecognized verdict", "**Findings:** 0"))},
			want:    "fail",
		},
		{
			name:    "explicit zero above active findings fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, "**Findings:** 0", twoLows))},
			want:    "fail",
		},
		{
			name:      "findings none with nothing listed stays clean",
			reviews:   []aiReviewNode{copilotReview(copilotBody(copilotApproval, "**Findings:** None"))},
			want:      "pass",
			wantClean: true,
		},
		{
			name:    "findings none above a listed finding fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotChanges, "**Findings:** None", twoLows))},
			want:    "fail",
		},
		{
			name:    "findings tally without numbers fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotApproval, "**Findings:** several"))},
			want:    "fail",
		},
		{
			name:    "findings tally too large to read fails closed",
			reviews: []aiReviewNode{copilotReview(copilotBody(copilotApproval, "**Findings:** 99999999999999999999"))},
			want:    "fail",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, clean := summarizeSupplementalReviews(tc.reviews, tc.threads, "", false)
			if got != tc.want {
				t.Fatalf("AI review = %q, want %q", got, tc.want)
			}
			if clean != tc.wantClean {
				t.Fatalf("AI clean = %v, want %v", clean, tc.wantClean)
			}
		})
	}
}

func TestCopilotUnresolvedThreadsFollowTheirSeverity(t *testing.T) {
	lowFirst := copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread)))
	mediumFirst := copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Medium", copilotFirstThread)))
	approval := copilotBody(copilotApproval, copilotNoOpen)
	tests := []struct {
		name    string
		reviews []aiReviewNode
		threads []aiReviewThread
		want    string
	}{
		{
			name:    "unresolved low from an older review passes",
			reviews: []aiReviewNode{copilotReview(lowFirst), copilotReview(approval)},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "pass",
		},
		{
			name:    "unresolved medium from an older review fails",
			reviews: []aiReviewNode{copilotReview(mediumFirst), copilotReview(approval)},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "fail",
		},
		{
			name:    "the latest severity for a thread wins",
			reviews: []aiReviewNode{copilotReview(lowFirst), copilotReview(mediumFirst), copilotReview(approval)},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "fail",
		},
		{
			name:    "unresolved thread no review links fails",
			reviews: []aiReviewNode{copilotReview(lowFirst), copilotReview(approval)},
			threads: []aiReviewThread{copilotThread(copilotOtherThread, false)},
			want:    "fail",
		},
		{
			name:    "unresolved thread with no comment id fails",
			reviews: []aiReviewNode{copilotReview(approval)},
			threads: []aiReviewThread{copilotThread("", false)},
			want:    "fail",
		},
		{
			name:    "another bot's unresolved thread never borrows copilot severity",
			reviews: []aiReviewNode{copilotReview(lowFirst), copilotReview(approval)},
			threads: []aiReviewThread{{AuthorLogin: "coderabbitai[bot]", AuthorType: "Bot", CommentID: copilotFirstThread}},
			want:    "fail",
		},
		{
			name: "another reviewer's body cannot lower a copilot thread",
			reviews: []aiReviewNode{
				{State: "COMMENTED", AuthorLogin: "coderabbitai[bot]", AuthorType: "Bot", ReviewBody: lowFirst},
				copilotReview(approval),
			},
			threads: []aiReviewThread{copilotThread(copilotFirstThread, false)},
			want:    "fail",
		},
		{
			name:    "unresolved human thread does not affect the result",
			reviews: []aiReviewNode{copilotReview(approval)},
			threads: []aiReviewThread{{AuthorLogin: "human-reviewer", AuthorType: "User"}},
			want:    "pass",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := summarizeSupplementalReviews(tc.reviews, tc.threads, "", false)
			if got != tc.want {
				t.Fatalf("AI review = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCopilotUnresolvedLowThreadIsNotClean(t *testing.T) {
	reviews := []aiReviewNode{
		copilotReview(copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread)))),
		copilotReview(copilotBody(copilotApproval, copilotNoOpen)),
	}
	threads := []aiReviewThread{copilotThread(copilotFirstThread, false)}
	aiReview, clean := summarizeSupplementalReviews(reviews, threads, "", false)
	if aiReview != "pass" || clean {
		t.Fatalf("AI = %q clean = %v, want pass without the clean marker", aiReview, clean)
	}
}

func TestCopilotPassBecomesUnknownWhenEvidenceIsIncomplete(t *testing.T) {
	reviews := []aiReviewNode{copilotReview(copilotBody(copilotCloserLook, copilotNoOpen))}
	aiReview, clean := summarizeSupplementalReviews(reviews, nil, "", true)
	if aiReview != "?" || clean {
		t.Fatalf("AI = %q clean = %v, want unknown", aiReview, clean)
	}
}

func TestMarkLowSeverityThreadsLeavesInputUntouched(t *testing.T) {
	reviews := []aiReviewNode{copilotReview(copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread))))}
	threads := []aiReviewThread{copilotThread(copilotFirstThread, false)}
	marked := markLowSeverityThreads(reviews, threads)
	if !marked[0].LowSeverity {
		t.Fatal("expected the returned thread to be marked low severity")
	}
	if threads[0].LowSeverity {
		t.Fatal("markLowSeverityThreads changed its input")
	}
}

func TestParseCopilotOverview(t *testing.T) {
	body := copilotBody(copilotChanges,
		copilotSection("2 open issues", copilotOpenLine("High", copilotFirstThread), copilotOpenLine("Low", copilotOtherThread)),
		copilotSection("1 resolved since last review", copilotOpenLine("Medium", "4000000003")),
		copilotMissedSection("Low"),
		copilotSection("What changed in this PR", "A file table with no severity."),
	)
	overview := parseCopilotOverview(body)
	if !overview.countsConsistent {
		t.Fatal("expected consistent counts")
	}
	want := []copilotFinding{
		{severity: "high severity", discussionID: copilotFirstThread, active: true},
		{severity: "low severity", discussionID: copilotOtherThread, active: true},
		{severity: "medium severity", discussionID: "4000000003", active: false},
		{severity: "low severity", active: true},
	}
	if len(overview.findings) != len(want) {
		t.Fatalf("findings = %+v, want %+v", overview.findings, want)
	}
	for i := range want {
		if overview.findings[i] != want[i] {
			t.Fatalf("finding %d = %+v, want %+v", i, overview.findings[i], want[i])
		}
	}
}

func TestParseCopilotOverviewUnresolvedSectionIsActive(t *testing.T) {
	overview := parseCopilotOverview(copilotBody(copilotChanges, copilotSection("1 unresolved issue", copilotOpenLine("Medium", copilotFirstThread))))
	if !overview.hasActiveFindings() || !overview.blocks() {
		t.Fatalf("expected an unresolved section to stay active: %+v", overview)
	}
}

func TestParseCopilotOverviewWithoutFindings(t *testing.T) {
	overview := parseCopilotOverview(copilotBody(copilotApproval, copilotNoOpen))
	if len(overview.findings) != 0 || overview.hasActiveFindings() || overview.blocks() {
		t.Fatalf("expected an empty overview: %+v", overview)
	}
}

func copilotSupplementalJSON(t *testing.T, head string, body string, commentID string, resolved bool) json.RawMessage {
	t.Helper()
	node := map[string]any{
		"number":     7,
		"headRefOid": head,
		"comments":   map[string]any{"totalCount": 0, "nodes": []any{}},
		"reactions":  map[string]any{"totalCount": 0, "nodes": []any{}},
		"reviewThreads": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{
			"isResolved": resolved,
			"comments": map[string]any{"nodes": []any{map[string]any{
				"fullDatabaseId": commentID,
				"author":         map[string]any{"login": copilotLogin, "__typename": "Bot"},
			}}},
		}}},
		"reviews": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{
			"state": "COMMENTED", "body": body,
			"submittedAt": time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
			"commit":      map[string]any{"oid": head},
			"author":      map[string]any{"login": copilotLogin, "__typename": "Bot"},
			"comments":    map[string]any{"totalCount": 1},
		}}},
		"approvedReviews": map[string]any{"totalCount": 0, "pageInfo": map[string]any{"hasPreviousPage": false}, "nodes": []any{}},
	}
	raw, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParsePRSupplementalNodeUsesThreadSeverity(t *testing.T) {
	head := strings.Repeat("a", 40)
	lowOpen := copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread)))
	mediumOpen := copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Medium", copilotFirstThread)))
	tests := []struct {
		name         string
		body         string
		resolved     bool
		wantAI       string
		wantUnresAI  bool
		wantComments int
	}{
		{name: "unresolved low passes", body: lowOpen, resolved: false, wantAI: "pass", wantUnresAI: true},
		{name: "resolved low passes", body: lowOpen, resolved: true, wantAI: "pass", wantComments: 1},
		{name: "unresolved medium fails", body: mediumOpen, resolved: false, wantAI: "fail", wantUnresAI: true},
		{name: "resolved medium fails", body: mediumOpen, resolved: true, wantAI: "fail", wantComments: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, info, ok := parsePRSupplementalNode(copilotSupplementalJSON(t, head, tc.body, copilotFirstThread, tc.resolved))
			if !ok {
				t.Fatal("expected the node to parse")
			}
			if info.AIReview != tc.wantAI {
				t.Fatalf("AIReview = %q, want %q", info.AIReview, tc.wantAI)
			}
			if info.AIClean {
				t.Fatal("expected no clean marker")
			}
			if info.HasUnresolvedAIThreads != tc.wantUnresAI {
				t.Fatalf("HasUnresolvedAIThreads = %v, want %v", info.HasUnresolvedAIThreads, tc.wantUnresAI)
			}
			if info.Threads.Resolved != tc.wantComments {
				t.Fatalf("resolved threads = %d, want %d", info.Threads.Resolved, tc.wantComments)
			}
		})
	}
}

func TestMapAtmNodeUsesThreadSeverity(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	head := strings.Repeat("b", 40)
	body := copilotBody(copilotChanges, copilotSection("1 open issue", copilotOpenLine("Low", copilotFirstThread)))
	raw := fmt.Sprintf(`{
		"number": 7, "title": "t", "state": "OPEN", "updatedAt": %q, "headRefOid": %q,
		"reviews": {"totalCount": 1, "nodes": [{"state": "COMMENTED", "body": %q, "commit": {"oid": %q},
			"author": {"login": %q, "__typename": "Bot"}, "comments": {"totalCount": 1}}]},
		"reviewThreads": {"totalCount": 1, "nodes": [{"isResolved": false, "comments": {"nodes": [
			{"fullDatabaseId": %q, "author": {"login": %q, "__typename": "Bot"}}]}}]}
	}`, now.Format(time.RFC3339), head, body, head, copilotLogin, copilotFirstThread, copilotLogin)
	var node atmPullRequestNode
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	dp := mapAtmNode(node, now)
	if dp.AIReview != "pass" {
		t.Fatalf("AIReview = %q, want pass", dp.AIReview)
	}
	if dp.AIClean != nil {
		t.Fatalf("expected no clean marker, got %v", *dp.AIClean)
	}
	if isReadyPR(dp) {
		t.Fatal("an unresolved low thread must keep the PR out of the ready list")
	}
}

func TestSupplementalQueriesRequestThreadCommentIDs(t *testing.T) {
	if !strings.Contains(prSupplementalFields, "comments(first: 20) { totalCount nodes { fullDatabaseId body createdAt author") {
		t.Fatal("supplemental query must fetch the first thread comment's fullDatabaseId")
	}
	if !strings.Contains(atmPRFieldsFragment, "fullDatabaseId") {
		t.Fatal("atm query must fetch the first thread comment's fullDatabaseId")
	}
}

func TestCopilotReviewWindowCompleteness(t *testing.T) {
	head := strings.Repeat("a", 40)
	for _, severity := range []string{"Medium", "High"} {
		for _, resolved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resolved=%v", severity, resolved), func(t *testing.T) {
				body := copilotBody(copilotChanges, copilotSection("1 previously missed issue", copilotOpenLine(severity, copilotFirstThread)))
				var node map[string]any
				if err := json.Unmarshal(copilotSupplementalJSON(t, head, body, copilotFirstThread, true), &node); err != nil {
					t.Fatal(err)
				}
				if !resolved {
					// Previously missed findings can have no review thread at all.
					node["reviewThreads"] = map[string]any{"totalCount": 0, "nodes": []any{}}
				}
				connection := node["reviews"].(map[string]any)
				copilot := connection["nodes"].([]any)[0]
				reviews := []any{copilot}
				for range 100 {
					reviews = append(reviews, map[string]any{
						"state": "APPROVED", "submittedAt": "2026-11-02T13:00:00Z",
						"commit":   map[string]any{"oid": head},
						"author":   map[string]any{"login": "cubic-dev-ai[bot]", "__typename": "Bot"},
						"comments": map[string]any{"totalCount": 0},
					})
				}
				for _, tc := range []struct {
					name  string
					nodes []any
					total int
					want  string
				}{
					{"complete blocking Copilot", reviews, 101, "fail"},
					{"omitted blocking Copilot", reviews[1:], 101, "?"},
					{"complete other bot only", reviews[1:], 100, "pass"},
				} {
					t.Run(tc.name, func(t *testing.T) {
						connection["nodes"], connection["totalCount"] = tc.nodes, tc.total
						raw, err := json.Marshal(node)
						if err != nil {
							t.Fatal(err)
						}
						_, info, ok := parsePRSupplementalNode(raw)
						if !ok || info.AIReview != tc.want || (tc.want == "?" && (info.AIClean || !info.Incomplete)) {
							t.Fatalf("AI=%q clean=%v incomplete=%v ok=%v; want %q", info.AIReview, info.AIClean, info.Incomplete, ok, tc.want)
						}
					})
				}
			})
		}
	}
}

func TestSufficientReviewEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, login, state, commit, head string
		want                             bool
	}{
		{"current Copilot", copilotLogin, "COMMENTED", "head", "head", true},
		{"stale Copilot", copilotLogin, "COMMENTED", "old", "head", false},
		{"dismissed Copilot", copilotLogin, "DISMISSED", "head", "head", false},
		{"other bot", "cubic-dev-ai[bot]", "APPROVED", "head", "head", false},
		{"unknown head", copilotLogin, "COMMENTED", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviews := []aiReviewNode{{AuthorLogin: tc.login, State: tc.state, CommitOID: tc.commit}}
			if got := sufficientReviewEvidence(reviews, tc.head); got != tc.want {
				t.Fatalf("sufficientReviewEvidence = %v, want %v", got, tc.want)
			}
		})
	}
	if sufficientReviewEvidence(nil, "head") {
		t.Fatal("empty review window cannot establish Copilot's verdict")
	}
}
