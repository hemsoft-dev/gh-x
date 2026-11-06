package main

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func fullReviewPullRequest() reviewPullRequest {
	return reviewPullRequest{
		Number: 5, Title: "Fix it", Body: "Details here", BaseRefName: "main", HeadRefName: "feature",
		URL: "https://example.test/pr/5", Author: &author{Login: "octo", Name: "Octo Cat"},
	}
}

// promptBuilders covers the read-only prompt and the structured prompt
// used when the review is posted.
var promptBuilders = map[string]prReviewOptions{
	"read-only":  {mode: "strict"},
	"structured": {mode: "strict", post: true},
}

func TestReviewPromptsIncludeOnlyProvidedContext(t *testing.T) {
	for name, options := range promptBuilders {
		t.Run(name, func(t *testing.T) {
			options.repo = "o/r"
			options.instructions = "Be brief"
			full, err := buildReviewPrompt(options, fullReviewPullRequest())
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"Review GitHub pull request #5: Fix it.\n",
				"- URL: https://example.test/pr/5\n",
				"- Repository: o/r\n",
				"- Author: Octo Cat (octo)\n",
				"- Base branch: main\n",
				"- Head branch: feature\n",
				"- Body:\n",
				"- gh pr view 5 --repo o/r --json",
				"- gh pr diff 5 --repo o/r\n",
				"- git diff main...feature\n",
				"\nAdditional instructions:\nBe brief\n",
			} {
				if !strings.Contains(full, want) {
					t.Errorf("full prompt lacks %q", want)
				}
			}

			options.repo = ""
			options.instructions = "   "
			bare, err := buildReviewPrompt(options, reviewPullRequest{Number: 5})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(bare, "Review GitHub pull request #5.\n") || !strings.Contains(bare, "- Repository: current repository\n") {
				t.Fatalf("bare prompt header: %q", bare)
			}
			for _, unwanted := range []string{"#5:", "- URL:", "- Author:", "- Base branch:", "- Head branch:", "- Body:", "git diff", "Additional instructions", "--repo"} {
				if strings.Contains(bare, unwanted) {
					t.Errorf("bare prompt has %q", unwanted)
				}
			}
		})
	}
}

func TestReviewPromptsNeedBothBranchesForGitDiff(t *testing.T) {
	for name, options := range promptBuilders {
		for _, pr := range []reviewPullRequest{{Number: 5, BaseRefName: "main"}, {Number: 5, HeadRefName: "feature"}} {
			prompt, err := buildReviewPrompt(options, pr)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(prompt, "git diff") {
				t.Errorf("%s prompt for %+v suggests git diff", name, pr)
			}
		}
	}
}

func TestReviewAuthorLabel(t *testing.T) {
	tests := []struct {
		author *author
		want   string
	}{
		{nil, ""},
		{&author{Name: "Octo Cat", Login: "octo"}, "Octo Cat (octo)"},
		{&author{Name: "Octo Cat"}, "Octo Cat"},
		{&author{Login: "octo"}, "octo"},
	}
	for _, tt := range tests {
		if got := reviewAuthorLabel(tt.author); got != tt.want {
			t.Errorf("reviewAuthorLabel(%+v) = %q, want %q", tt.author, got, tt.want)
		}
	}
}

func TestReviewLabelsAndFlags(t *testing.T) {
	if got := reviewRepoFlag(prReviewOptions{}); got != "" {
		t.Fatalf("repo flag = %q", got)
	}
	if got := reviewRepoFlag(prReviewOptions{repo: "o/r"}); got != " --repo o/r" {
		t.Fatalf("repo flag = %q", got)
	}
	for agent, want := range map[string]string{"": defaultReviewAgent, "claude": "claude"} {
		if got := reviewAgentLabel(prReviewOptions{agent: agent}); got != want {
			t.Errorf("agent %q label = %q", agent, got)
		}
	}
	if got := reviewAgentLabel(prReviewOptions{agent: "claude", command: "x"}); got != "custom" {
		t.Fatalf("custom command label = %q", got)
	}
}

func TestDefaultReviewOptionsReviewerIdentity(t *testing.T) {
	t.Setenv("GH_X_PR_REVIEW_IDENTITY", "")
	if got := defaultReviewOptions().reviewer; got != defaultReviewer {
		t.Fatalf("default reviewer = %q", got)
	}
	t.Setenv("GH_X_PR_REVIEW_IDENTITY", "Review Bot")
	if got := defaultReviewOptions().reviewer; got != "Review Bot" {
		t.Fatalf("configured reviewer = %q", got)
	}
}

func TestParseReviewOptionsFinishesAfterValidValues(t *testing.T) {
	t.Setenv("GH_X_PR_REVIEW_IDENTITY", "")
	options, err := parseReviewOptions([]string{"12", "--effort", "low", "--mode", "medium", "--base", " main ", "--reviewer", " "}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.effort != "low" || options.mode != "medium" || options.base != "main" || options.reviewer != defaultReviewer {
		t.Fatalf("options = %+v", options)
	}
	if _, err := parseReviewOptions([]string{"12", "--effort", "extreme"}, io.Discard); err == nil {
		t.Fatal("an unknown effort must fail")
	}
	if _, err := parseReviewOptions([]string{"12", "--mode", "lenient"}, io.Discard); err == nil {
		t.Fatal("an unknown mode must fail")
	}
}

func TestAppendReviewFlagWithoutTrailingValue(t *testing.T) {
	valueFlags := map[string]bool{"--model": true}
	flagArgs, index := appendReviewFlag(nil, []string{"--model"}, 0, valueFlags)
	if !slices.Equal(flagArgs, []string{"--model"}) || index != 0 {
		t.Fatalf("flagArgs = %q, index %d", flagArgs, index)
	}
	flagArgs, index = appendReviewFlag(nil, []string{"x", "--model", "gpt"}, 1, valueFlags)
	if !slices.Equal(flagArgs, []string{"--model", "gpt"}) || index != 2 {
		t.Fatalf("flagArgs = %q, index %d", flagArgs, index)
	}
}

func TestNormalizeReviewFindingsDropsOnlyEmptyFindings(t *testing.T) {
	got := normalizeReviewFindings([]reviewFinding{
		{Title: "Title only"},
		{Body: "Body only"},
		{Title: "  ", Body: " ", Suggestion: "orphan"},
	})
	if len(got) != 2 || got[0].Title != "Title only" || got[1].Body != "Body only" {
		t.Fatalf("findings = %+v", got)
	}
}

func TestReviewFindingCountAddsEverySeverity(t *testing.T) {
	result := structuredReviewOutput{
		Critical: make([]reviewFinding, 1),
		Medium:   make([]reviewFinding, 2),
		Nitpicks: make([]reviewFinding, 3),
	}
	if got := reviewFindingCount(result); got != 6 {
		t.Fatalf("count = %d", got)
	}
}

func TestRenderFormalReviewOptionalFields(t *testing.T) {
	reviewedAt := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	options := prReviewOptions{reviewer: "Bot", mode: "strict", agent: "codex", model: "gpt-x"}
	full := renderFormalReview(options, fullReviewPullRequest(), structuredReviewOutput{Summary: "Looks fine", ResidualRisk: "Untested on Windows"}, reviewedAt)
	for _, want := range []string{"**PR:** #5 - Fix it\n", "**Branch:** `feature` -> `main`\n", "**Agent:** codex / gpt-x\n", "## Summary\n\nLooks fine\n", "## Residual Risk\n\nUntested on Windows\n"} {
		if !strings.Contains(full, want) {
			t.Errorf("full report lacks %q", want)
		}
	}

	options.model = ""
	bare := renderFormalReview(options, reviewPullRequest{Number: 5}, structuredReviewOutput{}, reviewedAt)
	for _, want := range []string{"**PR:** #5\n", "**Agent:** codex\n", "No summary was provided by the review agent.", "## Residual Risk\n\nNone identified.\n"} {
		if !strings.Contains(bare, want) {
			t.Errorf("bare report lacks %q", want)
		}
	}
	if strings.Contains(bare, "**Branch:**") || strings.Contains(bare, " / ") {
		t.Fatalf("bare report: %q", bare)
	}

	for _, pr := range []reviewPullRequest{{Number: 5, HeadRefName: "feature"}, {Number: 5, BaseRefName: "main"}} {
		if got := renderFormalReview(options, pr, structuredReviewOutput{}, reviewedAt); !strings.Contains(got, "**Branch:**") {
			t.Errorf("one known branch still shows the branch line: %+v", pr)
		}
	}
}

func TestAppendFormalFindingSectionFormatsEachPart(t *testing.T) {
	var b strings.Builder
	appendFormalFindingSection(&b, "Medium Issues", []reviewFinding{
		{Path: "a.go", Line: 3, Title: "T", Body: "B", Suggestion: "S"},
		{Title: "Only title"},
		{Body: "Only body"},
		{Suggestion: "Only suggestion"},
	})
	want := "## Medium Issues\n\n" +
		"- `a.go:3` **T** - B Suggestion: S\n" +
		"- **Only title**\n" +
		"- - Only body\n" +
		"- Suggestion: Only suggestion\n\n"
	if b.String() != want {
		t.Fatalf("section = %q, want %q", b.String(), want)
	}
}

func TestAppendRecommendationsSectionSkipsBlankEntries(t *testing.T) {
	var b strings.Builder
	appendRecommendationsSection(&b, []string{" ", "Add a test"})
	if want := "## Recommendations\n\n- Add a test\n\n"; b.String() != want {
		t.Fatalf("section = %q", b.String())
	}
}

func TestReviewFindingLocationNeedsAPositiveLine(t *testing.T) {
	if got := reviewFindingLocation(reviewFinding{Path: "a.go", Line: 0}); got != "" {
		t.Fatalf("line 0 = %q", got)
	}
	if got := reviewFindingLocation(reviewFinding{Path: "a.go", Line: 1}); got != "a.go:1" {
		t.Fatalf("line 1 = %q", got)
	}
}

func TestFormatInlineReviewCommentParts(t *testing.T) {
	tests := []struct {
		finding reviewFinding
		want    string
	}{
		{reviewFinding{Title: "T", Body: "B", Suggestion: "S"}, "**[MEDIUM]** 🟡 T\n\nB\n\nSuggestion: S"},
		{reviewFinding{Body: "B"}, "**[MEDIUM]** 🟡\n\nB"},
		{reviewFinding{Title: "T"}, "**[MEDIUM]** 🟡 T"},
		{reviewFinding{Suggestion: "S"}, "**[MEDIUM]** 🟡\n\nSuggestion: S"},
	}
	for _, tt := range tests {
		if got := formatInlineReviewComment("MEDIUM", "🟡", tt.finding); got != tt.want {
			t.Errorf("comment = %q, want %q", got, tt.want)
		}
	}
}

func TestIsReviewLineCommentableRejectsLineZero(t *testing.T) {
	lines := map[string]map[int]bool{"a.go": {0: true, 4: true}}
	if isReviewLineCommentable(lines, "a.go", 0) {
		t.Fatal("line 0 is never commentable")
	}
	if !isReviewLineCommentable(lines, "a.go", 4) {
		t.Fatal("line 4 is commentable")
	}
}

func TestParsePatchNewStartAtTheFirstCharacter(t *testing.T) {
	if line, ok := parsePatchNewStart("+5,2 @@"); !ok || line != 5 {
		t.Fatalf("leading plus = %d %v", line, ok)
	}
	if _, ok := parsePatchNewStart("@@ -1,2 @@"); ok {
		t.Fatal("a hunk without a new range is rejected")
	}
}
