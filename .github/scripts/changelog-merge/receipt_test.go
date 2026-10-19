package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

const receiptHead = "e221a8953b1130d96a6a2c894478aa92bef773d8"

func receiptFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The v1 fixtures are verbatim comments from PR #225, comments 6262900193 and
// 6262863758. The legacy fixture removes only the receipt's new HTML marker.
func TestCleanCodexReceiptComments(t *testing.T) {
	receipt := receiptFixture(t, "codex-receipt-v1")
	legacy := receiptFixture(t, "codex-receipt-legacy")
	for _, tc := range []struct {
		name, body string
		clean      bool
	}{
		{"current receipt", receipt, true},
		{"legacy receipt", legacy, true},
		{"blank lines", " \n\t\n" + receipt, true},
		{"multiple comments", "<!-- extra metadata -->\n\n" + receipt, true},
		{"multiline comment", "<!-- metadata\ncontinues here\n-->\n" + legacy, true},
		{"alternate wording", "## **Codex Review: Did not find any major issues.**", true},
		{"unclosed comment", "<!-- metadata\n" + legacy, false},
		{"only comments", "<!-- metadata -->\n\n", false},
		{"empty", "", false},
		{"quoted clean receipt after finding", "<!-- metadata -->\nCodex Review: Found an issue.\n" + legacy, false},
		{"finding receipt", strings.Replace(receipt, "Didn't find any major issues", "Found an issue", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanCodexComment(tc.body); got != tc.clean {
				t.Fatalf("clean = %v, want %v", got, tc.clean)
			}
		})
	}
}

func TestCodexSummaryReviewState(t *testing.T) {
	summary := receiptFixture(t, "codex-summary-v1")
	for _, tc := range []struct {
		name, body  string
		clean, fail bool
	}{
		{"real clean summary", summary, true, false},
		{"legacy summary", strings.Replace(summary, strings.Split(summary, "\n")[1]+"\n", "", 1), true, false},
		{"findings", strings.Replace(summary, `"has_any_findings":false`, `"has_any_findings":true`, 1), false, false},
		{"unresolved findings", strings.Replace(summary, `"has_unresolved_findings":false`, `"has_unresolved_findings":true`, 1), false, false},
		{"null findings", strings.Replace(summary, `"has_any_findings":false`, `"has_any_findings":null`, 1), false, false},
		{"missing findings", strings.Replace(summary, `"has_any_findings":false,`, "", 1), false, false},
		{"running state", strings.Replace(summary, `"status":"completed"`, `"status":"running"`, 1), false, false},
		{"running row", strings.Replace(summary, "**Completed**", "**Running**", 1), false, false},
		{"mismatched head", strings.Replace(summary, receiptHead, testHead, 1), false, true},
		{"invalid state", strings.Replace(summary, `{"code_review":`, `{"code_review"`, 1), false, true},
		{"unclosed state", strings.Replace(summary, "}} -->", "}}", 1), false, true},
		{"duplicate state", summary + "\n<!-- codex-review-state:v1 {} -->", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comment := reviewComment{Body: tc.body, Author: actor{"chatgpt-codex-connector[bot]"}}
			got, matched, err := codexCommentActivity(comment, receiptHead)
			if (err != nil) != tc.fail || !tc.fail && (!matched || got.Clean != tc.clean) {
				t.Fatalf("clean=%v matched=%v err=%v, want clean=%v fail=%v", got.Clean, matched, err, tc.clean, tc.fail)
			}
		})
	}
}

func TestRealCodexReceiptsMakeCurrentHeadReady(t *testing.T) {
	stamp := time.Date(2026, 10, 19, 8, 23, 0, 0, time.UTC)
	for _, name := range []string{"codex-receipt-v1", "codex-receipt-legacy"} {
		t.Run(name, func(t *testing.T) {
			receipt := reviewComment{Body: receiptFixture(t, name), CreatedAt: stamp, URL: "receipt-url", Author: actor{"chatgpt-codex-connector"}}
			state := reviewState{HeadRefOID: receiptHead}
			state.Comments.Nodes = []reviewComment{{Body: receiptFixture(t, "codex-summary-v1"), Author: receipt.Author}, receipt}
			state.TimelineItems.Nodes = []timelineItem{
				{TypeName: "PullRequestCommit", Commit: struct{ OID string }{receiptHead}},
				{TypeName: "IssueComment", URL: receipt.URL},
			}
			ready, requested, err := ordinaryReviewReady(state, receiptHead)
			if !ready || !requested || err != nil {
				t.Fatalf("ordinary ready=%v requested=%v err=%v", ready, requested, err)
			}
			ready, requested, err = reviewReady(state, receiptHead)
			if !ready || !requested || err != nil {
				t.Fatalf("changelog ready=%v requested=%v err=%v", ready, requested, err)
			}
			state.HeadRefOID = testHead
			ready, _, err = ordinaryReviewReady(state, testHead)
			if ready || err != nil {
				t.Fatalf("stale receipt ready=%v err=%v", ready, err)
			}
		})
	}
}
