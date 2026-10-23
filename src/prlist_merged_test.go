package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRecentlyMergedSurvivesEmptySearchResults(t *testing.T) {
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		command := strings.Join(args, " ")
		// Reproduce Mini: search through the old name misses existing merges.
		if strings.Contains(command, "pr list") {
			return *bytes.NewBufferString("[]"), bytes.Buffer{}, nil
		}
		if strings.Contains(command, "pullRequest(number:") {
			return selectedMergedTestResponse(t, command, map[int]any{208: map[string]any{"number": 208}}), bytes.Buffer{}, nil
		}
		return *bytes.NewBufferString(`{"data":{"repository":{"pullRequests":{"nodes":[{"number":208,"title":"record release","state":"MERGED","mergedAt":"2026-10-07T00:40:20Z","updatedAt":"2026-10-07T00:40:23Z"}],"pageInfo":{"hasNextPage":false}}}}}`), bytes.Buffer{}, nil
	}
	got, err := fetchPullRequests(listOptions{repo: "HemSoft/gh-x", limit: 5, state: "merged", search: "sort:updated-desc", recentlyMerged: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != 208 {
		t.Fatalf("recent merges = %#v, want existing repository PR #208 despite empty search index", got)
	}
}

func TestMergedRepositoryPaginationAndFields(t *testing.T) {
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })
	calls := 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		calls++
		command := strings.Join(args, " ")
		if strings.Contains(command, "freshness:") {
			rows := []pullRequest{{Number: 1, UpdatedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), MergedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, {Number: 2, UpdatedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).Add(-time.Minute), MergedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
			return mergedFreshnessTestResponse(t, rows, false, ""), bytes.Buffer{}, nil
		}

		if strings.Contains(command, "pullRequest(number:") {
			details := map[int]any{101: json.RawMessage(`{"number":101,"title":"old PR merged today","state":"MERGED","author":{"login":"user","name":"User"},"mergedAt":"2026-10-07T01:00:00Z","updatedAt":"2026-10-07T02:00:00Z","headRefName":"fix","baseRefName":"main","mergeable":"MERGEABLE","reviewDecision":"APPROVED","latestReviews":{"nodes":[{"state":"APPROVED","author":{"login":"reviewer"}}]},"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","name":"CI","status":"COMPLETED","conclusion":"SUCCESS","checkSuite":{"workflowRun":{"workflow":{"name":"Tests"}}}},{"__typename":"StatusContext","context":"legacy","state":"SUCCESS"}]}}}}]}}`)}
			return selectedMergedTestResponse(t, command, details), bytes.Buffer{}, nil
		}
		for _, want := range []string{"--hostname ghe.example.com", `owner: "owner"`, `name: "repo"`, "states: MERGED", "UPDATED_AT, direction: DESC"} {
			if !strings.Contains(command, want) {
				t.Fatalf("query missing %q: %s", want, command)
			}
		}
		if calls == 1 {
			if !strings.Contains(command, "first: 100, after: null") {
				t.Fatalf("initial pagination = %s", command)
			}
			nodes := make([]pullRequest, 100)
			for i := range nodes {
				nodes[i] = pullRequest{Number: i + 1, UpdatedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Minute), MergedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			}
			data, err := json.Marshal(nodes)
			if err != nil {
				t.Fatal(err)
			}
			return *bytes.NewBufferString(fmt.Sprintf(`{"data":{"repository":{"pullRequests":{"nodes":%s,"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`, data)), bytes.Buffer{}, nil
		}
		if calls != 2 || !strings.Contains(command, `first: 1, after: "next"`) {
			t.Fatalf("next pagination = %s", command)
		}
		return *bytes.NewBufferString(`{"data":{"repository":{"pullRequests":{"nodes":[{
		  "number":101,"title":"old PR merged today","state":"MERGED","author":{"login":"user","name":"User"},
		  "mergedAt":"2026-10-07T01:00:00Z","updatedAt":"2026-10-07T02:00:00Z","headRefName":"fix","baseRefName":"main","mergeable":"MERGEABLE","reviewDecision":"APPROVED",
		  "latestReviews":{"nodes":[{"state":"APPROVED","author":{"login":"reviewer"}}]},
		  "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","name":"CI","status":"COMPLETED","conclusion":"SUCCESS","checkSuite":{"workflowRun":{"workflow":{"name":"Tests"}}}},{"__typename":"StatusContext","context":"legacy","state":"SUCCESS"}]}}}}]}
		}],"pageInfo":{"hasNextPage":false}}}}}`), bytes.Buffer{}, nil
	}
	got, err := fetchMergedRepositoryPullRequests(listOptions{repo: "ghe.example.com/owner/repo", limit: 101})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 101 || calls != 9 {
		t.Fatalf("rows = %d, calls = %d, want 101 and 9", len(got), calls)
	}
	var pr pullRequest
	for _, row := range got {
		if row.Number == 101 {
			pr = row
		}
	}
	if pr.Number != 101 || pr.Title != "old PR merged today" || pr.Author.Login != "user" || pr.HeadRefName != "fix" || pr.BaseRefName != "main" || pr.ReviewDecision != "APPROVED" || pr.Mergeable != "MERGEABLE" || pr.State != "MERGED" || pr.MergedAt != time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC) {
		t.Fatalf("lost PR fields: %#v", pr)
	}
	if len(pr.LatestReviews) != 1 || pr.LatestReviews[0].Author.Login != "reviewer" || len(pr.StatusCheckRollup) != 2 || pr.StatusCheckRollup[0].Conclusion != "SUCCESS" || pr.StatusCheckRollup[0].WorkflowName != "Tests" || pr.StatusCheckRollup[1].Context != "legacy" {
		t.Fatalf("lost reviews/checks: %#v", pr)
	}
}

func TestMergedRepositoryFailures(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		execErr            error
	}{
		{"command error", "", "offline", errors.New("offline")},
		{"invalid JSON", "not json", "decode merged", nil},
		{"missing repository", `{"data":{"repository":null}}`, "unavailable", nil},
		{"missing connection", `{"data":{"repository":{}}}`, "unavailable", nil},
		{"partial error", `{"data":{"repository":{"pullRequests":{"nodes":[]}}},"errors":[{"message":"denied"}]}`, "denied", nil},
		{"empty cursor", `{"data":{"repository":{"pullRequests":{"nodes":[{"number":1}],"pageInfo":{"hasNextPage":true}}}}}`, "did not advance", nil},
		{"empty page", `{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`, "did not advance", nil},
		{"repeated cursor", `{"data":{"repository":{"pullRequests":{"nodes":[{"number":1}],"pageInfo":{"hasNextPage":true,"endCursor":"same"}}}}}`, "did not advance", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := ghExecFunc
			t.Cleanup(func() { ghExecFunc = saved })
			ghExecFunc = func(...string) (bytes.Buffer, bytes.Buffer, error) {
				return *bytes.NewBufferString(tc.output), bytes.Buffer{}, tc.execErr
			}
			got, err := fetchMergedRepositoryPullRequests(listOptions{repo: "owner/repo", limit: 3})
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(got) != 0 {
				t.Fatalf("rows = %#v, error = %v, want failure containing %q", got, err, tc.want)
			}
		})
	}
}

func TestRecentlyMergedEmptyAndLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   int
		wantErr bool
	}{
		{"truly empty repository", 5, false},
		{"limit exceeds candidate cap", recentMergedCandidateLimit + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := ghExecFunc
			t.Cleanup(func() { ghExecFunc = saved })
			ghExecFunc = func(...string) (bytes.Buffer, bytes.Buffer, error) {
				return *bytes.NewBufferString(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`), bytes.Buffer{}, nil
			}
			got, err := fetchRecentlyMergedPullRequests(listOptions{repo: "owner/repo", limit: tc.limit})
			if (err != nil) != tc.wantErr || len(got) != 0 {
				t.Fatalf("rows = %#v, error = %v, want empty and error=%v", got, err, tc.wantErr)
			}
		})
	}
}

func TestMergedEnterpriseWithoutWorkflowRun(t *testing.T) {
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })
	calls := 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		calls++
		query := strings.Join(args, " ")
		switch calls {
		case 1:
			if strings.Contains(query, "workflowRun") {
				t.Fatal("candidate query carries checks")
			}
			return *bytes.NewBufferString(`{"data":{"repository":{"pullRequests":{"nodes":[{"number":1}],"pageInfo":{"hasNextPage":true,"endCursor":"older"}}}}}`), bytes.Buffer{}, nil
		case 2:
			if !strings.Contains(query, `after: "older"`) {
				t.Fatal("cursor reset")
			}
			return *bytes.NewBufferString(`{"data":{"repository":{"pullRequests":{"nodes":[{"number":2}],"pageInfo":{"hasNextPage":false}}}}}`), bytes.Buffer{}, nil
		case 3:
			return mergedFreshnessTestResponse(t, []pullRequest{{Number: 1}, {Number: 2}}, false, ""), bytes.Buffer{}, nil
		case 4:
			return bytes.Buffer{}, bytes.Buffer{}, errors.New("GraphQL: Field 'workflowRun' doesn't exist on type 'CheckSuite'")
		default:
			if calls != 5 || strings.Contains(query, "workflowRun") {
				t.Fatal("unsupported field retained or extra request")
			}
			return selectedMergedTestResponse(t, query, nil), bytes.Buffer{}, nil
		}
	}
	got, err := fetchMergedRepositoryPullRequests(listOptions{repo: "ghe.example.com/owner/repo", limit: 2})
	if err != nil || len(got) != 2 || calls != 5 {
		t.Fatalf("rows=%#v, calls=%d, err=%v", got, calls, err)
	}
}

func TestMergedChecksIncludeFailureBeyondFirstPage(t *testing.T) {
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })
	calls := 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		calls++
		query := strings.Join(args, " ")
		if strings.Contains(query, "pullRequests(states: MERGED") {
			return *bytes.NewBufferString(`{"data":{"repository":{"pullRequests":{"nodes":[{"number":7}],"pageInfo":{"hasNextPage":false}}}}}`), bytes.Buffer{}, nil
		}
		if strings.Contains(query, "pullRequest(number:") {

			checks := make([]map[string]string, 100)
			for i := range checks {
				checks[i] = map[string]string{"__typename": "CheckRun", "name": fmt.Sprintf("check-%d", i), "status": "COMPLETED", "conclusion": "SUCCESS"}
			}
			data, err := json.Marshal(checks)
			if err != nil {
				t.Fatal(err)
			}
			return selectedMergedTestResponse(t, query, map[int]any{7: json.RawMessage(fmt.Sprintf(`{"number":7,"commits":{"nodes":[{"commit":{"id":"commit-id","statusCheckRollup":{"contexts":{"nodes":%s,"pageInfo":{"hasNextPage":true,"endCursor":"checks-next"}}}}}]}}`, data))}), bytes.Buffer{}, nil
		}
		if !strings.Contains(query, `node(id: "commit-id")`) || !strings.Contains(query, `after: "checks-next"`) {
			t.Fatalf("unexpected continuation: %s", query)
		}
		return *bytes.NewBufferString(`{"data":{"node":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","name":"late-failure","status":"COMPLETED","conclusion":"FAILURE"}],"pageInfo":{"hasNextPage":false}}}}}}`), bytes.Buffer{}, nil
	}
	got, err := fetchMergedRepositoryPullRequests(listOptions{repo: "owner/repo", limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(got) != 1 || len(got[0].StatusCheckRollup) != 101 || normalizeCheckState(got[0].StatusCheckRollup) != "fail" {
		t.Fatalf("rows=%d, calls=%d, want 101 checks including second-page failure", len(got), calls)
	}
}
