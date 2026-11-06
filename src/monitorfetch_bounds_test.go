package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWriteMonitorSearchAliasFragmentByKind(t *testing.T) {
	var pr, issue strings.Builder
	writeMonitorSearchAlias(&pr, "pr0", monitorKindPR, "is:open", 5, false)
	writeMonitorSearchAlias(&issue, "is0", monitorKindIssue, "is:open", 5, false)
	if !strings.Contains(pr.String(), "... on PullRequest {") || strings.Contains(pr.String(), "... on Issue {") {
		t.Fatalf("PR alias = %q", pr.String())
	}
	if !strings.Contains(issue.String(), "... on Issue {") || strings.Contains(issue.String(), "... on PullRequest {") {
		t.Fatalf("issue alias = %q", issue.String())
	}
}

func fakeHostFetcher(warnings map[string][]string, failures map[string]error) monitorHostFetcher {
	return func(_ context.Context, query monitorHostQuery, cfg *monitorConfig, now time.Time) (*monitorFetchResult, error) {
		if err := failures[query.Host]; err != nil {
			return nil, err
		}
		result := newMonitorFetchResult(cfg, now)
		result.Warnings = warnings[query.Host]
		return result, nil
	}
}

func TestExecuteMonitorQueriesQualifiesOnlyMultiHostWarnings(t *testing.T) {
	cfg := monitorTestConfig()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	warnings := map[string][]string{"github.com": {"slow"}, "ghe.test": {"rate"}}

	single, err := executeMonitorQueries(context.Background(), cfg, []monitorHostQuery{{Host: "github.com"}}, now, fakeHostFetcher(warnings, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(single.Warnings, "|") != "slow" || single.Incomplete {
		t.Fatalf("single host = %q incomplete %v", single.Warnings, single.Incomplete)
	}

	multi, err := executeMonitorQueries(context.Background(), cfg, []monitorHostQuery{{Host: "github.com"}, {Host: "ghe.test"}}, now, fakeHostFetcher(warnings, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(multi.Warnings, "|"); !strings.Contains(got, "github.com: slow") || !strings.Contains(got, "ghe.test: rate") || multi.Incomplete {
		t.Fatalf("multi host = %q incomplete %v", got, multi.Incomplete)
	}

	partial, err := executeMonitorQueries(context.Background(), cfg, []monitorHostQuery{{Host: "github.com"}, {Host: "ghe.test"}}, now,
		fakeHostFetcher(warnings, map[string]error{"ghe.test": errors.New("offline")}))
	if err != nil {
		t.Fatal(err)
	}
	if !partial.Incomplete {
		t.Fatal("a failed host leaves the result incomplete")
	}
}

func TestIssueHierarchyUnsupportedFallsBackToStderr(t *testing.T) {
	message := "Field 'subIssuesSummary' doesn't exist on type 'Issue'"
	if !issueHierarchyUnsupported([]byte(`{"data":{}}`), message) {
		t.Fatal("stderr explains the unsupported field when stdout has no errors")
	}
	if issueHierarchyUnsupported([]byte(`{"errors":[{"message":"rate limited"}]}`), message) {
		t.Fatal("stdout errors take precedence over stderr")
	}
}

func TestMergeMonitorFetchResultKeepsTheTightestRateLimit(t *testing.T) {
	earlier := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	cfg := monitorTestConfig()
	dst := newMonitorFetchResult(cfg, earlier)
	dst.RateRemaining, dst.RateResetAt = 10, earlier
	src := newMonitorFetchResult(cfg, earlier)
	src.RateRemaining, src.RateResetAt = 10, later
	mergeMonitorFetchResult(dst, src, false, "github.com")
	if !dst.RateResetAt.Equal(earlier) {
		t.Fatal("an equal remaining budget keeps the first reset time")
	}
	src.RateRemaining = 5
	mergeMonitorFetchResult(dst, src, false, "github.com")
	if dst.RateRemaining != 5 || !dst.RateResetAt.Equal(later) {
		t.Fatalf("a lower budget wins: %d %s", dst.RateRemaining, dst.RateResetAt)
	}
}

func TestParseMonitorHostResponseWithoutData(t *testing.T) {
	cfg := monitorTestConfig()
	now := time.Now()
	if _, err := parseMonitorHostResponse([]byte(`{"data":null}`), cfg, nil, now); err == nil || !strings.Contains(err.Error(), "contained no data") {
		t.Fatalf("null data = %v", err)
	}
	if _, err := parseMonitorHostResponse([]byte(`{"data":null,"errors":[{"message":"bad credentials"}]}`), cfg, nil, now); err == nil || err.Error() != "GraphQL error: bad credentials" {
		t.Fatalf("null data with errors = %v", err)
	}
}

func TestMonitorWarningsSkipsBlanksAndKeepsThree(t *testing.T) {
	got := monitorWarnings([]monitorGraphQLError{{Message: "a"}, {Message: " "}, {Message: "b"}, {Message: "c"}, {Message: "d"}})
	if strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("warnings = %q", got)
	}
}

func TestMonitorHierarchyErrorRowRejectsMissingSections(t *testing.T) {
	result := &monitorFetchResult{IssueSections: []monitorSectionData{{Rows: []monitorRow{{Number: 1}}, sourceIndexes: []int{0}}}}
	if _, _, ok := monitorHierarchyErrorRow(result, []any{"is1", "nodes", float64(0), "parent"}); ok {
		t.Fatal("a section past the end is not a row")
	}
	row, field, ok := monitorHierarchyErrorRow(result, []any{"is0", "nodes", float64(0), "parent"})
	if !ok || field != "parent" || row.Number != 1 {
		t.Fatalf("row = %+v %q %v", row, field, ok)
	}
}

func TestFormatMonitorLogin(t *testing.T) {
	if formatMonitorLogin(nil) != "-" || formatMonitorLogin(&author{}) != "-" {
		t.Fatal("unknown authors render as a dash")
	}
	if got := formatMonitorLogin(&author{Login: "octo"}); got == "-" || !strings.Contains(got, "octo") {
		t.Fatalf("login = %q", got)
	}
}

func TestPrintMonitorQuerySingleHostIsBare(t *testing.T) {
	isolateMonitorHome(t)
	configPath, err := monitorConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultMonitorConfig("")
	cfg.Repos = []string{"HemSoft/gh-x"}
	if err := saveMonitorConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	savedExec := monitorGHExecFunc
	t.Cleanup(func() { monitorGHExecFunc = savedExec })
	monitorGHExecFunc = func(context.Context, ...string) (bytes.Buffer, bytes.Buffer, error) {
		return *bytes.NewBufferString(`{"data":{"viewer":{"login":"HemSoft","organizations":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`), bytes.Buffer{}, nil
	}
	var stdout bytes.Buffer
	if err := printMonitorQuery(&stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if strings.Contains(output, "# github.com") || strings.Contains(output, "query Monitor1") {
		t.Fatalf("one host needs no header:\n%s", output)
	}
	if strings.HasSuffix(output, "\n\n") || !strings.HasSuffix(output, "\n") {
		t.Fatalf("output must end with exactly one newline: %q", output[max(0, len(output)-20):])
	}
}

func TestPrintMonitorQuerySeparatesHostsWithOneBlankLine(t *testing.T) {
	isolateMonitorHome(t)
	configPath, err := monitorConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultMonitorConfig("")
	cfg.Repos = []string{"HemSoft/gh-x", "ghe.example.com/Acme/Widgets"}
	if err := saveMonitorConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	savedExec := monitorGHExecFunc
	t.Cleanup(func() { monitorGHExecFunc = savedExec })
	monitorGHExecFunc = func(context.Context, ...string) (bytes.Buffer, bytes.Buffer, error) {
		return *bytes.NewBufferString(`{"data":{"viewer":{"login":"HemSoft","organizations":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`), bytes.Buffer{}, nil
	}
	var stdout bytes.Buffer
	if err := printMonitorQuery(&stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if strings.Count(output, "\n\n# ") != 1 || strings.HasSuffix(output, "\n\n") {
		t.Fatalf("hosts must be separated by exactly one blank line:\n%s", output)
	}
}

func TestBootstrapMonitorFirstLaunchPicksTheLastSection(t *testing.T) {
	setup := func(t *testing.T, saved *monitorSessionState) monitorModel {
		t.Helper()
		isolateMonitorHome(t)
		cfg := monitorTestConfig()
		cfg.PRSections = []monitorSection{{Title: "A", Filters: "is:open"}, {Title: "B", Filters: "is:open"}, {Title: "C", Filters: "is:open"}}
		cfg.IssueSections = []monitorSection{{Title: "X", Filters: "is:open"}, {Title: "Y", Filters: "is:open"}}
		configPath, err := monitorConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := saveMonitorConfig(configPath, cfg); err != nil {
			t.Fatal(err)
		}
		if saved != nil {
			statePath, err := monitorStatePath()
			if err != nil {
				t.Fatal(err)
			}
			if err := saveMonitorState(statePath, *saved); err != nil {
				t.Fatal(err)
			}
		}
		model, err := bootstrapMonitorModel()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(model.cancelRefresh)
		return model
	}
	t.Run("first launch", func(t *testing.T) {
		if model := setup(t, nil); model.tab != monitorTabPRs || model.subTab != 2 {
			t.Fatalf("tab %d subTab %d, want PR section 2", model.tab, model.subTab)
		}
	})
	t.Run("saved selection", func(t *testing.T) {
		model := setup(t, &monitorSessionState{SubTab: 1, RepoIndex: 2})
		if model.subTab != 1 || model.repoIdx != 2 {
			t.Fatalf("subTab %d repo %d, want 1 and 2", model.subTab, model.repoIdx)
		}
	})
}
