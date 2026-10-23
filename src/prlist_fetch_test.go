package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

func TestFetchPullRequestList(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		command := strings.Join(args, " ")
		if !strings.Contains(command, "pr list") || !strings.Contains(command, "--limit 30") || !strings.Contains(command, "--state open") {
			t.Fatalf("unexpected arguments: %s", command)
		}
		return *bytes.NewBufferString("[]"), bytes.Buffer{}, nil
	}

	result, err := fetchPullRequestList(listOptions{repo: "owner/repo", limit: 30, state: "open"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 0 || len(result.Rendered) != 0 {
		t.Fatalf("expected empty result, got %#v", result)
	}
}

func TestFetchPullRequestListError(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, *bytes.NewBufferString("offline"), errors.New("exit status 1")
	}

	_, err := fetchPullRequestList(defaultListOptions(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("expected wrapped stderr, got %v", err)
	}
}

func TestFetchRecentlyMergedPullRequestsExpandsUntilMergeOrderIsProven(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	firstPage := []pullRequest{
		{Number: 1, MergedAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)},
		{Number: 2, MergedAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)},
	}
	for i := 0; i < recentMergedCandidateFloor-2; i++ {
		firstPage = append(firstPage, pullRequest{
			Number:    10 + i,
			MergedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 10, 28-i, 0, 0, 0, 0, time.UTC),
		})
	}
	secondPage := []pullRequest{
		{Number: 3, MergedAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)},
		{Number: 4, MergedAt: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	calls, candidateRows, enrichedRows := 0, 0, 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		calls++
		command := strings.Join(args, " ")
		if strings.Contains(command, "pullRequest(number:") {
			enrichedRows += len(mergedAliasPattern.FindAllStringSubmatch(command, -1))
			return selectedMergedTestResponse(t, command, map[int]any{1: firstPage[0], 3: secondPage[0]}), bytes.Buffer{}, nil
		}
		if !strings.Contains(command, "nodes { number updatedAt mergedAt }") || strings.Contains(command, "latestReviews") || strings.Contains(command, "contexts") {
			t.Fatal("candidate query contains heavy evidence")
		}
		rows := firstPage
		next := true
		if strings.Contains(command, `after: "page25"`) {
			rows = secondPage
			next = false
		} else if !strings.Contains(command, "after: null") {
			t.Fatalf("unexpected cursor: %s", command)
		}
		candidateRows += len(rows)
		data, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		return *bytes.NewBufferString(fmt.Sprintf(`{"data":{"repository":{"pullRequests":{"nodes":%s,"pageInfo":{"hasNextPage":%t,"endCursor":"page25"}}}}}`, data, next)), bytes.Buffer{}, nil
	}

	acquisition, err := fetchMergedAcquisition(listOptions{repo: "owner/repo", limit: 2, state: "merged", recentlyMerged: true}, true)
	got := acquisition.rows
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MERGED_REQUEST_BUDGET fixture=expanding requests=%d candidates=%d enriched=%d", calls, candidateRows, enrichedRows)
	if calls != 3 || candidateRows != 27 || enrichedRows != 2 {
		t.Fatalf("requests=%d candidate rows=%d enriched=%d, want3/27/2", calls, candidateRows, enrichedRows)
	}
	if len(got) != 2 || got[0].Number != 3 || got[1].Number != 1 {
		t.Fatalf("recent merges = %#v, want #3 then #1", got)
	}
}

func TestResolveAuthorFromOrg_SearchError(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, bytes.Buffer{}, fmt.Errorf("network error")
	}

	result := resolveAuthorFromOrg("John Doe", "myorg")
	if result != "" {
		t.Fatalf("expected empty string on error, got %q", result)
	}
}

func TestResolveAuthorFromOrg_MatchFound(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	callCount := 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		callCount++
		var buf bytes.Buffer
		if callCount == 1 {
			// Search returns logins
			buf.WriteString("johndoe\noctocat\n")
			return buf, bytes.Buffer{}, nil
		}
		// Membership check: first login is a member
		if callCount == 2 {
			return buf, bytes.Buffer{}, nil // success = member
		}
		return buf, bytes.Buffer{}, fmt.Errorf("not a member")
	}

	result := resolveAuthorFromOrg("John Doe", "myorg")
	if result != "johndoe" {
		t.Fatalf("expected 'johndoe', got %q", result)
	}
}

func TestResolveAuthorFromOrg_NoMember(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	callCount := 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		callCount++
		var buf bytes.Buffer
		if callCount == 1 {
			buf.WriteString("user1\nuser2\n")
			return buf, bytes.Buffer{}, nil
		}
		// All membership checks fail
		return buf, bytes.Buffer{}, fmt.Errorf("not a member")
	}

	result := resolveAuthorFromOrg("Jane Smith", "myorg")
	if result != "" {
		t.Fatalf("expected empty string when no member found, got %q", result)
	}
}

func TestResolveAuthorFromOrg_EmptyLogins(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		var buf bytes.Buffer
		buf.WriteString("\nnull\n\n")
		return buf, bytes.Buffer{}, nil
	}

	result := resolveAuthorFromOrg("Nobody", "myorg")
	if result != "" {
		t.Fatalf("expected empty string for empty/null logins, got %q", result)
	}
}

func TestResolveAuthorLogin_WithSpaceAndOrg(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	callCount := 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		callCount++
		var buf bytes.Buffer
		if callCount == 1 {
			// Search returns one login
			buf.WriteString("jdoe\n")
			return buf, bytes.Buffer{}, nil
		}
		// Membership check succeeds
		return buf, bytes.Buffer{}, nil
	}

	login, err := resolveAuthorLogin("John Doe", "myorg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if login != "jdoe" {
		t.Fatalf("expected 'jdoe', got %q", login)
	}
}

func TestResolveAuthorLogin_WithSpaceFallbackToSearch(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	callCount := 0
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		callCount++
		var buf bytes.Buffer
		if callCount == 1 {
			// Org search returns empty
			buf.WriteString("\n")
			return buf, bytes.Buffer{}, nil
		}
		// Global user search fallback
		buf.WriteString("globaluser")
		return buf, bytes.Buffer{}, nil
	}

	login, err := resolveAuthorLogin("John Doe", "myorg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if login != "globaluser" {
		t.Fatalf("expected 'globaluser', got %q", login)
	}
}

func TestResolveAuthorLogin_WithSpaceNoOrg(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		var buf bytes.Buffer
		buf.WriteString("founduser")
		return buf, bytes.Buffer{}, nil
	}

	login, err := resolveAuthorLogin("John Doe", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if login != "founduser" {
		t.Fatalf("expected 'founduser', got %q", login)
	}
}

func TestResolveAuthorLogin_SearchFails(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, bytes.Buffer{}, fmt.Errorf("api error")
	}

	_, err := resolveAuthorLogin("John Doe", "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestResolveAuthorLogin_SearchReturnsEmpty(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, bytes.Buffer{}, nil
	}

	_, err := resolveAuthorLogin("John Doe", "")
	if err == nil {
		t.Fatalf("expected error for empty result, got nil")
	}
}

func TestResolveAuthorLogin_SearchReturnsNull(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		var buf bytes.Buffer
		buf.WriteString("null")
		return buf, bytes.Buffer{}, nil
	}

	_, err := resolveAuthorLogin("John Doe", "")
	if err == nil {
		t.Fatalf("expected error for null result, got nil")
	}
}

func TestFetchPRSupplemental_Empty(t *testing.T) {
	result, unavailable, err := fetchPRSupplemental("owner", "repo", "github.com", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil || unavailable != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}
}

func TestFetchPRSupplemental_SingleBatch(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()

	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		result := make(map[int]prSupplementalInfo)
		for _, n := range prNumbers {
			result[n] = prSupplementalInfo{AIReview: "clean"}
		}
		return result, nil, nil
	}

	result, unavailable, err := fetchPRSupplemental("owner", "repo", "github.com", []int{1, 2, 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 || len(unavailable) != 0 {
		t.Fatalf("expected 3 results with none unavailable, got %d results, %d unavailable", len(result), len(unavailable))
	}
	for _, n := range []int{1, 2, 3} {
		if result[n].AIReview != "clean" {
			t.Fatalf("expected AIReview='clean' for PR %d", n)
		}
	}
}

func TestFetchPRSupplemental_MultipleBatches(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()

	batchCalls := 0
	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		batchCalls++
		result := make(map[int]prSupplementalInfo)
		for _, n := range prNumbers {
			result[n] = prSupplementalInfo{Approvals: batchCalls}
		}
		return result, nil, nil
	}

	// Create 35 PRs to force 2 batches (batch size is 30)
	prs := make([]int, 35)
	for i := range prs {
		prs[i] = i + 1
	}

	result, unavailable, err := fetchPRSupplemental("owner", "repo", "github.com", prs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batchCalls != 2 {
		t.Fatalf("expected 2 batch calls, got %d", batchCalls)
	}
	if len(result) != 35 || len(unavailable) != 0 {
		t.Fatalf("expected 35 results with none unavailable, got %d results, %d unavailable", len(result), len(unavailable))
	}
}

func TestFetchPRSupplemental_BatchError(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()

	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return nil, map[int]bool{1: true, 2: true}, fmt.Errorf("graphql error")
	}

	result, unavailable, err := fetchPRSupplemental("owner", "repo", "github.com", []int{1, 2})
	if err == nil || err.Error() != "graphql error" {
		t.Fatalf("expected graphql error, got %v", err)
	}
	if len(result) != 0 || !unavailable[1] || !unavailable[2] {
		t.Fatalf("failed batches must keep every PR unavailable, got result=%v unavailable=%v", result, unavailable)
	}
}

// capturedCodexbarSupplementalResponse is the supplemental GraphQL response
// captured from HemSoft/codexbar-ios pull request #333 on 2026-09-09 while
// investigating issue #88. Comment prose past 90 characters is truncated;
// review fields from that capture stay verbatim: the closing relationship to
// issue #332, four review threads (one unresolved, Codex-authored), the
// completed current-head Codex review, and zero approvals. The empty reactions
// connection was added when the parser began requiring that query field.
const capturedCodexbarSupplementalResponse = `{
 "data": {
  "repository": {
   "pr333": {
    "number": 333,
    "headRefOid": "53b343204e072b22f6511ac68bf6284aa2c418c2",
    "closingIssuesReferences": {
     "totalCount": 1,
     "nodes": [
      {
       "number": 332,
       "url": "https://github.com/HemSoft/codexbar-ios/issues/332"
      }
     ]
    },
    "comments": {
     "totalCount": 6,
     "nodes": [
      {
       "body": "<!-- codex-pull-request-review-summary -->\n\n## Codex Review Summary\n\nThis comment shows th... [truncated]",
       "createdAt": "2026-09-09T04:26:19Z",
       "author": {
        "login": "chatgpt-codex-connector",
        "__typename": "Bot"
       }
      },
      {
       "body": "@coderabbitai review",
       "createdAt": "2026-09-09T04:26:34Z",
       "author": {
        "login": "HemSoft",
        "__typename": "User"
       }
      },
      {
       "body": "cursor review",
       "createdAt": "2026-09-09T04:26:35Z",
       "author": {
        "login": "HemSoft",
        "__typename": "User"
       }
      },
      {
       "body": "<!-- BUGBOT_FREE_TIER_DISABLED_UPSELL -->\nBugbot is not enabled for your account, so this ... [truncated]",
       "createdAt": "2026-09-09T04:26:39Z",
       "author": {
        "login": "cursor",
        "__typename": "Bot"
       }
      },
      {
       "body": "@codex review",
       "createdAt": "2026-09-09T05:20:29Z",
       "author": {
        "login": "HemSoft",
        "__typename": "User"
       }
      },
      {
       "body": "@coderabbitai review",
       "createdAt": "2026-09-09T05:20:30Z",
       "author": {
        "login": "HemSoft",
        "__typename": "User"
       }
      }
     ]
    },
    "reactions": {
     "totalCount": 0,
     "nodes": []
    },
    "reviewThreads": {
     "totalCount": 4,
     "nodes": [
      {
       "isResolved": true,
       "comments": {
        "nodes": [
         {
          "author": {
           "login": "chatgpt-codex-connector",
           "__typename": "Bot"
          }
         }
        ]
       }
      },
      {
       "isResolved": true,
       "comments": {
        "nodes": [
         {
          "author": {
           "login": "cubic-dev-ai",
           "__typename": "Bot"
          }
         }
        ]
       }
      },
      {
       "isResolved": true,
       "comments": {
        "nodes": [
         {
          "author": {
           "login": "cubic-dev-ai",
           "__typename": "Bot"
          }
         }
        ]
       }
      },
      {
       "isResolved": false,
       "comments": {
        "nodes": [
         {
          "author": {
           "login": "chatgpt-codex-connector",
           "__typename": "Bot"
          }
         }
        ]
       }
      }
     ]
    },
    "reviews": {
     "totalCount": 6,
     "nodes": [
      {
       "state": "COMMENTED",
       "submittedAt": "2026-09-09T04:30:06Z",
       "commit": {
        "oid": "c90b97e5274838c271b3ed3e110da2d49448c013"
       },
       "author": {
        "login": "chatgpt-codex-connector",
        "__typename": "Bot"
       },
       "comments": {
        "totalCount": 1
       }
      },
      {
       "state": "COMMENTED",
       "submittedAt": "2026-09-09T04:40:02Z",
       "commit": {
        "oid": "c90b97e5274838c271b3ed3e110da2d49448c013"
       },
       "author": {
        "login": "cubic-dev-ai",
        "__typename": "Bot"
       },
       "comments": {
        "totalCount": 2
       }
      },
      {
       "state": "COMMENTED",
       "submittedAt": "2026-09-09T05:20:13Z",
       "commit": {
        "oid": "53b343204e072b22f6511ac68bf6284aa2c418c2"
       },
       "author": {
        "login": "HemSoft",
        "__typename": "User"
       },
       "comments": {
        "totalCount": 1
       }
      },
      {
       "state": "COMMENTED",
       "submittedAt": "2026-09-09T05:20:16Z",
       "commit": {
        "oid": "53b343204e072b22f6511ac68bf6284aa2c418c2"
       },
       "author": {
        "login": "HemSoft",
        "__typename": "User"
       },
       "comments": {
        "totalCount": 1
       }
      },
      {
       "state": "COMMENTED",
       "submittedAt": "2026-09-09T05:20:18Z",
       "commit": {
        "oid": "53b343204e072b22f6511ac68bf6284aa2c418c2"
       },
       "author": {
        "login": "HemSoft",
        "__typename": "User"
       },
       "comments": {
        "totalCount": 1
       }
      },
      {
       "state": "COMMENTED",
       "submittedAt": "2026-09-09T05:25:08Z",
       "commit": {
        "oid": "53b343204e072b22f6511ac68bf6284aa2c418c2"
       },
       "author": {
        "login": "chatgpt-codex-connector",
        "__typename": "Bot"
       },
       "comments": {
        "totalCount": 1
       }
      }
     ]
    },
    "approvedReviews": {
     "nodes": []
    }
   }
  }
 }
}`

func TestEnrichRendersCapturedCodexbarFixture(t *testing.T) {
	infos, errored, err := parseSupplementalResponse([]byte(capturedCodexbarSupplementalResponse))
	if err != nil {
		t.Fatalf("parseSupplementalResponse returned error: %v", err)
	}
	if len(errored) != 0 {
		t.Fatalf("the captured fixture must carry no error paths, got %v", errored)
	}
	if _, ok := infos[333]; !ok {
		t.Fatalf("fixture must parse PR 333, got %v", infos)
	}

	now := time.Date(2026, 9, 9, 4, 40, 0, 0, time.UTC)
	prs := []pullRequest{{
		Number:    333,
		Title:     "Show reset times on Gemini coding quota metrics",
		State:     "OPEN",
		UpdatedAt: now,
		StatusCheckRollup: []checkItem{{
			Typename: "CheckRun", Name: "build", WorkflowName: "CI", Status: "IN_PROGRESS",
		}},
	}}
	rendered := enrichPullRequests(prs, prSupplementalData{Info: infos}, nil, nil, now)
	row := rendered[0]
	if row.Issues != "#332" {
		t.Fatalf("Issues = %q, want #332 from captured closing relationship", row.Issues)
	}
	if len(row.issueRefs) != 1 || row.issueRefs[0].Number != 332 {
		t.Fatalf("issueRefs = %#v, want issue #332", row.issueRefs)
	}
	if row.Comments != "3/4" {
		t.Fatalf("Comments = %q, want 3/4 from captured threads", row.Comments)
	}
	if row.AIReview != "fail" {
		t.Fatalf("AIReview = %q, want fail for the completed current-head Codex review with an unresolved finding", row.AIReview)
	}
	if row.Checks != "pending" {
		t.Fatalf("Checks = %q, want pending reported independently of enrichment", row.Checks)
	}

	// Table and JSON outputs must agree on the same rendered rows.
	var jsonBuf bytes.Buffer
	if err := renderListOutput(&jsonBuf, listOptions{json: true}, rendered); err != nil {
		t.Fatalf("renderListOutput(json) error: %v", err)
	}
	var decoded []displayPullRequest
	if err := json.Unmarshal(jsonBuf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("JSON rows = %d, want 1", len(decoded))
	}
	if decoded[0].Issues != row.Issues || decoded[0].Comments != row.Comments ||
		decoded[0].AIReview != row.AIReview || decoded[0].Checks != row.Checks {
		t.Fatalf("JSON row %#v disagrees with table row %#v", decoded[0], row)
	}
}

func TestFetchPRSupplementalBatchRecoversHealthyAliasesFromPartialError(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		query := strings.Join(args, " ")
		if !strings.Contains(query, "state body submittedAt") {
			t.Fatalf("supplemental query must request formal review bodies, got %q", args)
		}
		if !strings.Contains(query, "reactions(content: THUMBS_UP") || !strings.Contains(query, "createdAt user { login __typename }") {
			t.Fatalf("supplemental query must request timestamped PR thumbs-up reactions, got %q", args)
		}
		if !strings.Contains(query, "approvedReviews: latestOpinionatedReviews(last: 100) { totalCount pageInfo { hasPreviousPage } nodes { state submittedAt author") {
			t.Fatalf("supplemental query must request paginated latest opinionated reviews, got %q", args)
		}
		body := `{"data":{"repository":{"pr333":{"number":333,"headRefOid":"53b343204e072b22f6511ac68bf6284aa2c418c2","closingIssuesReferences":{"totalCount":1,"nodes":[{"number":332,"url":"https://github.com/HemSoft/codexbar-ios/issues/332"}]},"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},"approvedReviews":{"nodes":[]}},"pr999":null}},"errors":[{"type":"NOT_FOUND","path":["repository","pr999"],"message":"Could not resolve to a PullRequest with the number of 999."}]}`
		return *bytes.NewBufferString(body), *bytes.NewBufferString("gh: Could not resolve to a PullRequest with the number of 99999.\n"), errors.New("exit status 1")
	}

	infos, unavailable, err := fetchPRSupplementalBatch("HemSoft", "codexbar-ios", "github.com", []int{333, 999})
	if err == nil {
		t.Fatal("partial GraphQL error must be carried for display")
	}
	if !strings.Contains(err.Error(), "Could not resolve to a PullRequest") {
		t.Fatalf("error should carry the gh diagnostic, got %v", err)
	}
	if len(infos[333].ClosingIssues) != 1 || infos[333].ClosingIssues[0].Number != 332 {
		t.Fatalf("healthy alias data must survive a partial batch error, got %#v", infos[333])
	}
	if !unavailable[999] {
		t.Fatalf("unparsed alias 999 must be unavailable, got %v", unavailable)
	}
	if unavailable[333] {
		t.Fatal("healthy alias 333 must not be marked unavailable")
	}
}

func TestFetchGraphQLKeepsGenuineFailureClosed(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, *bytes.NewBufferString("gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again.\n"), errors.New("exit status 1")
	}

	data, err := fetchGraphQL("github.com", "query { repository { id } }")
	if err == nil {
		t.Fatal("failure without a data envelope must return an error")
	}
	if data != nil {
		t.Fatalf("failure without a data envelope must not return data, got %s", data)
	}
	if !strings.Contains(err.Error(), "secondary rate limit") {
		t.Fatalf("error should carry the actionable gh diagnostic, got %v", err)
	}
}

func TestFetchSupplementalDataUsesConfiguredSSHHost(t *testing.T) {
	t.Setenv("GH_REPO", "")
	t.Setenv("GH_HOST", "")
	withRemoteURLStub(t, "git@github.com-hemsoft:HemSoft/codexbar-ios.git")
	withKnownGitHubHostStub(t, func(host string) bool { return host == defaultGitHubHost })
	withSSHConfigHostStub(t, func(host string) string {
		if host != "github.com-hemsoft" {
			t.Fatalf("SSH resolver host = %q, want github.com-hemsoft", host)
		}
		return defaultGitHubHost
	})
	savedBatch := fetchPRSupplementalBatchFunc
	t.Cleanup(func() { fetchPRSupplementalBatchFunc = savedBatch })
	fetchPRSupplementalBatchFunc = func(owner, name, host string, numbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		if owner != "HemSoft" || name != "codexbar-ios" || host != defaultGitHubHost {
			t.Fatalf("supplemental target = %s/%s on %s, want HemSoft/codexbar-ios on github.com", owner, name, host)
		}
		if !reflect.DeepEqual(numbers, []int{335}) {
			t.Fatalf("supplemental numbers = %v, want [335]", numbers)
		}
		return map[int]prSupplementalInfo{335: {
			ClosingIssues:          []linkedReference{{Number: 305}},
			ClosingIssuesAvailable: true,
		}}, nil, nil
	}

	data, owner, name := fetchSupplementalData("HemSoft/codexbar-ios", []pullRequest{{Number: 335}})
	if owner != "HemSoft" || name != "codexbar-ios" {
		t.Fatalf("repository = %s/%s, want HemSoft/codexbar-ios", owner, name)
	}
	if len(data.Info[335].ClosingIssues) != 1 || data.Info[335].ClosingIssues[0].Number != 305 || len(data.Unavailable) != 0 {
		t.Fatalf("supplemental data = %#v, want PR #335 linked to issue #305", data)
	}
}

func TestFetchSupplementalDataFailsClosedWhenRepoUnresolved(t *testing.T) {
	saved := ghExecFunc
	defer func() { ghExecFunc = saved }()
	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, bytes.Buffer{}, errors.New("repo resolution failed")
	}

	prs := []pullRequest{{Number: 7}, {Number: 9}}
	data, owner, name := fetchSupplementalData("no-slash", prs)
	if owner != "" || name != "" {
		t.Fatalf("owner/name = %q/%q, want empty", owner, name)
	}
	if !data.Unavailable[7] || !data.Unavailable[9] {
		t.Fatalf("unresolved repo must fail closed for every PR, got %v", data.Unavailable)
	}
	if data.Err == nil {
		t.Fatal("expected the resolution error to be carried for display")
	}
}

func TestSupplementalNotice(t *testing.T) {
	if got := supplementalNotice(nil); got != "" {
		t.Fatalf("supplementalNotice(nil) = %q, want empty", got)
	}
	long := errors.New("gh: " + strings.Repeat("boom ", 60))
	got := supplementalNotice(long)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("notice must stay single-line, got %q", got)
	}
	if len(got) > 531 {
		t.Fatalf("notice must stay bounded, got %d chars", len(got))
	}
	if !strings.Contains(got, "Supplemental data unavailable: gh: ") {
		t.Fatalf("notice should keep its prefix, got %q", got)
	}
}

func TestParseSupplementalNodeDropsPartialFieldFailure(t *testing.T) {
	// A partial GraphQL failure nulls the failed field inside an otherwise
	// valid PR object. The entry must not parse as available: unknown
	// threads would otherwise render as empty comments and a clean AI
	// review. Reproduces the Codex P1 finding on PR #89.
	raw := json.RawMessage(`{"number":333,"headRefOid":"53b343204e072b22f6511ac68bf6284aa2c418c2","closingIssuesReferences":{"totalCount":1,"nodes":[{"number":332,"url":"https://github.com/HemSoft/codexbar-ios/issues/332"}]},"comments":{"totalCount":2,"nodes":[{"body":"hi","author":{"login":"user","__typename":"User"}}]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":null,"reviews":{"nodes":[]},"approvedReviews":{"nodes":[]}}`)

	envelope := `{"data":{"repository":{"pr333":` + string(raw) + `}},"errors":[{"type":"NOT_FOUND","path":["repository","pr333","reviewThreads"],"message":"Field failed"}]}`
	infos, _, parseErr := parseSupplementalResponse([]byte(envelope))
	if parseErr != nil {
		t.Fatalf("envelope with partial errors must still parse: %v", parseErr)
	}
	if _, present := infos[333]; present {
		t.Fatalf("PR 333 must stay out of the parsed set, got %#v", infos[333])
	}

	unavailable := unavailablePRNumbers([]int{333}, infos)
	if !unavailable[333] {
		t.Fatalf("PR 333 with a failed field must be unavailable, got %v", unavailable)
	}

	// The enrich path then renders unknown columns instead of empty data.
	now := time.Date(2026, 9, 9, 4, 40, 0, 0, time.UTC)
	rendered := enrichPullRequests(
		[]pullRequest{{Number: 333, State: "OPEN", UpdatedAt: now}},
		prSupplementalData{Info: infos, Unavailable: unavailable, Err: fmt.Errorf("gh api graphql: Field failed")},
		nil, nil, now,
	)
	if rendered[0].Comments != "?" || rendered[0].AIReview != "?" || rendered[0].Issues != "?" {
		t.Fatalf("partial field failure must render unknown, got issues=%q comments=%q ai=%q",
			rendered[0].Issues, rendered[0].Comments, rendered[0].AIReview)
	}
}

func TestFetchPRSupplementalSynthesizesPartialPayloadError(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()

	// A recovered payload that omitted the alias renders ? rows; the wrapper
	// must still produce a diagnostic instead of failing silently.
	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return nil, map[int]bool{5: true}, nil
	}

	result, unavailable, err := fetchPRSupplemental("owner", "repo", "github.com", []int{5, 6})
	if err == nil {
		t.Fatal("unavailable aliases without an underlying error must synthesize a diagnostic")
	}
	if !strings.Contains(err.Error(), "1 of 2 requested pull requests") {
		t.Fatalf("synthesized error should count unavailable aliases, got %v", err)
	}
	if !unavailable[5] || len(result) != 0 {
		t.Fatalf("expected PR 5 unavailable, got result=%v unavailable=%v", result, unavailable)
	}
}

func TestRequiredChecksError(t *testing.T) {
	if got := requiredChecksError(nil); got != nil {
		t.Fatalf("requiredChecksError(nil) = %v, want nil", got)
	}
	err := requiredChecksError(map[string]error{"main": errors.New("unavailable"), "develop": errors.New("unavailable")})
	text := err.Error()
	if !strings.Contains(text, "base develop") || !strings.Contains(text, "base main") || !strings.Contains(text, "unavailable") {
		t.Fatalf("requiredChecksError text = %q, want both branches and reasons", text)
	}
	if strings.Index(text, "develop") > strings.Index(text, "main") {
		t.Fatalf("branches must be listed deterministically, got %q", text)
	}
}

// runAuxiliaryNoticeListExec drives executeList through fetchPullRequestList
// with a mocked gh subprocess, returning both output buffers.
func runAuxiliaryNoticeListExec(t *testing.T, options listOptions) (bytes.Buffer, bytes.Buffer) {
	t.Helper()
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		joined := strings.Join(args, " ")
		out := bytes.Buffer{}
		switch {
		case strings.Contains(joined, "pr list"):
			out.WriteString(`[{"number":42,"title":"Auxiliary diagnostics","state":"OPEN","updatedAt":"2026-09-09T05:00:00Z","headRefName":"feature","baseRefName":"main","url":"https://github.com/owner/repo/pull/42"}]`)
		case args[0] == "api" && len(args) > 1 && strings.HasPrefix(args[1], "repos/"):
			out.WriteString("not-json")
		case args[0] == "api" && strings.Contains(joined, " graphql"):
			return bytes.Buffer{}, *bytes.NewBufferString("gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again.\n"), errors.New("exit status 1")
		default:
			out.WriteString("[]")
		}
		return out, bytes.Buffer{}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := executeList(options, &stdout, &stderr); err != nil {
		t.Fatalf("executeList error: %v", err)
	}
	return stdout, stderr
}

func TestExecuteListRendersAuxiliaryNotices(t *testing.T) {
	stdout, stderr := runAuxiliaryNoticeListExec(t, listOptions{repo: "owner/repo", limit: 30, state: "open"})
	table := stdout.String()
	if !strings.Contains(table, "#42") {
		t.Fatalf("table should render the listed PR:\n%s", table)
	}
	if !strings.Contains(table, "Supplemental data unavailable: gh api graphql: gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again.") {
		t.Fatalf("table should print the supplemental diagnostic:\n%s", table)
	}
	if !strings.Contains(table, "Required check rules unavailable: base main: required check rules: malformed response") {
		t.Fatalf("table should print the required-checks diagnostic:\n%s", table)
	}
	if stderr.Len() != 0 {
		t.Fatalf("table mode should keep diagnostics on stdout, got stderr %q", stderr.String())
	}
}

func TestExecuteListAuxiliaryNoticesGoToStderrInJSONMode(t *testing.T) {
	stdout, stderr := runAuxiliaryNoticeListExec(t, listOptions{repo: "owner/repo", limit: 30, state: "open", json: true})
	var decoded []displayPullRequest
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON stdout must decode as the row array: %v\n%s", err, stdout.String())
	}
	if len(decoded) != 1 || decoded[0].Number != 42 || decoded[0].AIReview != "?" {
		t.Fatalf("JSON rows must carry the listed PR with unknown supplemental columns, got %#v", decoded)
	}
	if !strings.Contains(stderr.String(), "Supplemental data unavailable:") {
		t.Fatalf("JSON stderr should carry the supplemental diagnostic:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Required check rules unavailable:") {
		t.Fatalf("JSON stderr should carry the required-checks diagnostic:\n%s", stderr.String())
	}
}

func TestRunViewRendersSupplementalDataAndNotices(t *testing.T) {
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })

	ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		joined := strings.Join(args, " ")
		out := bytes.Buffer{}
		switch {
		case strings.Contains(joined, "pr view 333"):
			out.WriteString(`{"number":333,"title":"Show reset times on Gemini coding quota metrics","state":"OPEN","updatedAt":"2026-09-09T05:00:00Z","headRefName":"issue-332-gemini","baseRefName":"main","url":"https://github.com/HemSoft/codexbar-ios/pull/333"}`)
		case args[0] == "api" && len(args) > 1 && strings.HasPrefix(args[1], "repos/"):
			out.WriteString("[]")
		case args[0] == "api" && strings.Contains(joined, " graphql"):
			out.WriteString(capturedCodexbarSupplementalResponse)
		default:
			out.WriteString("[]")
		}
		return out, bytes.Buffer{}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runView([]string{"333", "--repo", "HemSoft/codexbar-ios"}, &stdout, &stderr); err != nil {
		t.Fatalf("runView error: %v", err)
	}
	table := stdout.String()
	if !strings.Contains(table, "#333") || !strings.Contains(table, "#332") {
		t.Fatalf("runView should render the PR with its captured relationship:\n%s", table)
	}
	if !strings.Contains(table, "3/4") || !strings.Contains(table, "fail") {
		t.Fatalf("runView should render captured thread and AI columns:\n%s", table)
	}
	if stderr.Len() != 0 {
		t.Fatalf("complete enrichment should print no diagnostics, got stderr %q", stderr.String())
	}
}

func TestParseSupplementalResponseDropsIncompleteConnectionObjects(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		payload string
	}{
		{name: "missing totalCount", field: "reviewThreads", payload: `{"nodes":[]}`},
		{name: "null totalCount", field: "reviews", payload: `{"totalCount":null,"nodes":[]}`},
		{name: "missing nodes", field: "comments", payload: `{"totalCount":0}`},
		{name: "missing reaction nodes", field: "reactions", payload: `{"totalCount":0}`},
		{name: "null nodes", field: "approvedReviews", payload: `{"nodes":null}`},
		{name: "empty object", field: "reviewThreads", payload: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := fmt.Sprintf(`{"data":{"repository":{"pr9":{"number":9,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},"approvedReviews":{"nodes":[]},%q:%s}}}}`, test.field, test.payload)
			infos, _, err := parseSupplementalResponse([]byte(envelope))
			if err != nil {
				t.Fatalf("parseSupplementalResponse error: %v", err)
			}
			if _, present := infos[9]; present {
				t.Fatalf("PR 9 with incomplete %s must not parse as available: %#v", test.field, infos[9])
			}
		})
	}
}

func TestIncompleteConnectionsCarryDiagnostic(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()

	// A PR with more threads than one page returns a truncated connection:
	// the PR stays rendered with its healthy fields while AI data is ?.
	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return map[int]prSupplementalInfo{
			7: {AIReview: "?", Incomplete: true},
		}, nil, nil
	}

	data, _, _ := fetchSupplementalData("owner/repo", []pullRequest{{Number: 7}})
	if data.Err == nil {
		t.Fatal("truncated connections must produce a diagnostic when no fetch error exists")
	}
	if !strings.Contains(data.Err.Error(), "truncated supplemental connections for pull request(s) 7") {
		t.Fatalf("diagnostic should name the affected PR, got %v", data.Err)
	}
	if data.Unavailable[7] {
		t.Fatal("truncated PR stays rendered with healthy fields, not unavailable")
	}
}

func TestEnrichPullRequestsDowngradesChecksOnFailedRules(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 40, 0, 0, time.UTC)
	prs := []pullRequest{{
		Number:      42,
		Title:       "Unverifiable required checks",
		State:       "OPEN",
		UpdatedAt:   now,
		BaseRefName: "main",
		StatusCheckRollup: []checkItem{{
			Typename: "CheckRun", Name: "build", WorkflowName: "CI", Status: "COMPLETED", Conclusion: "SUCCESS",
		}},
	}}

	rendered := enrichPullRequests(prs, prSupplementalData{}, nil, map[string]error{"main": errors.New("required check rules: malformed response")}, now)
	if rendered[0].Checks != "pending" {
		t.Fatalf("Checks = %q, want pending when required rules cannot be fetched", rendered[0].Checks)
	}
	if !rendered[0].checksDowngraded {
		t.Fatal("expected the failed-rules downgrade to be recorded")
	}
}

func TestParseSupplementalResponseDropsBrokenThreadNodes(t *testing.T) {
	tests := []struct {
		name       string
		brokenNode string
	}{
		{name: "null thread node", brokenNode: `null`},
		{name: "missing isResolved", brokenNode: `{"comments":{"nodes":[{"author":{"login":"bot[bot]","__typename":"Bot"}}]}}`},
		{name: "null isResolved", brokenNode: `{"isResolved":null,"comments":{"nodes":[]}}`},
		{name: "missing comments", brokenNode: `{"isResolved":false}`},
		{name: "null comments", brokenNode: `{"isResolved":false,"comments":null}`},
		{name: "comments without nodes", brokenNode: `{"isResolved":false,"comments":{}}`},
		{name: "null comments nodes", brokenNode: `{"isResolved":false,"comments":{"nodes":null}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := fmt.Sprintf(`{"data":{"repository":{"pr9":{"number":9,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":1,"nodes":[%s]},"reviews":{"totalCount":0,"nodes":[]},"approvedReviews":{"nodes":[]}}}}}`, test.brokenNode)
			infos, _, err := parseSupplementalResponse([]byte(envelope))
			if err != nil {
				t.Fatalf("parseSupplementalResponse error: %v", err)
			}
			if _, present := infos[9]; present {
				t.Fatalf("PR 9 with broken thread node %s must not parse as available: %#v", test.brokenNode, infos[9])
			}
		})
	}
}

func TestTruncatedThreadsRenderUnknownComments(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 40, 0, 0, time.UTC)
	prs := []pullRequest{{Number: 8, State: "OPEN", UpdatedAt: now}}
	rendered := enrichPullRequests(prs, prSupplementalData{Info: map[int]prSupplementalInfo{
		8: {Threads: reviewThreadInfo{Total: 101, Resolved: 100}, ThreadsTruncated: true, AIReview: "pass"},
	}}, nil, nil, now)
	if rendered[0].Comments != "?" {
		t.Fatalf("Comments = %q, want ? for a truncated thread page", rendered[0].Comments)
	}
	if rendered[0].AIReview != "pass" {
		t.Fatalf("healthy AI data must stay rendered beside truncated threads, got %q", rendered[0].AIReview)
	}
}

func TestEvidenceAmbiguityCarriesOwnDiagnostic(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()

	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return map[int]prSupplementalInfo{
			3: {EvidenceAmbiguous: true},
		}, nil, nil
	}

	data, _, _ := fetchSupplementalData("owner/repo", []pullRequest{{Number: 3}})
	if data.Err == nil {
		t.Fatal("evidence ambiguity must produce a diagnostic when no fetch error exists")
	}
	if !strings.Contains(data.Err.Error(), "cannot order AI review evidence for pull request(s) 3") {
		t.Fatalf("diagnostic should name ambiguity specifically, got %v", data.Err)
	}
	if strings.Contains(data.Err.Error(), "truncated supplemental connections") {
		t.Fatalf("ambiguity alone must not be reported as truncation, got %v", data.Err)
	}
}

func TestJoinSupplementalReasons(t *testing.T) {
	if joinSupplementalReasons(nil, nil) != nil {
		t.Fatal("no reasons must join to nil")
	}
	joined := joinSupplementalReasons(nil, errors.New("b"))
	if joined == nil || joined.Error() != "b" {
		t.Fatalf("single reason must survive joining, got %v", joined)
	}
	joined = joinSupplementalReasons(errors.New("a"), errors.New("b"))
	if joined == nil || !strings.Contains(joined.Error(), "a; b") {
		t.Fatalf("multiple reasons must join with ; got %v", joined)
	}
}

func TestEnrichPullRequestsKeepsReviewStateOnFailedRules(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 40, 0, 0, time.UTC)
	prs := []pullRequest{{
		Number:      50,
		State:       "OPEN",
		UpdatedAt:   now,
		BaseRefName: "main",
		StatusCheckRollup: []checkItem{{
			Typename: "CheckRun", Name: "cubic · AI code reviewer", WorkflowName: "Cubic",
			Status: "IN_PROGRESS",
		}},
	}}

	rendered := enrichPullRequests(prs, prSupplementalData{}, nil, map[string]error{"main": errors.New("required check rules: malformed response")}, now)
	if rendered[0].Checks != "review" {
		t.Fatalf("Checks = %q, want review kept when rules fetch fails while an AI reviewer runs", rendered[0].Checks)
	}

	rendered[0].Checks = "pass"
	downgradeChecksIfMissing(&rendered[0], nil, map[string]error{"main": errors.New("offline")}, "main", nil)
	if rendered[0].Checks != "pending" || !rendered[0].checksDowngraded {
		t.Fatalf("a pass under failed rules must still downgrade to pending, got %q", rendered[0].Checks)
	}
}

func TestUnattributableThreadsForceUnknownAI(t *testing.T) {
	envelope := `{"data":{"repository":{"pr9":{"number":9,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":1,"nodes":[{"isResolved":false,"comments":{"nodes":[{"author":null}]}}]},"reviews":{"totalCount":1,"nodes":[{"state":"APPROVED","author":{"login":"bot[bot]","__typename":"Bot"},"commit":{"oid":"abc"},"comments":{"totalCount":0}}]},"approvedReviews":{"nodes":[]}}}}}`
	infos, errored, err := parseSupplementalResponse([]byte(envelope))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if len(errored) != 0 {
		t.Fatalf("a null comment author without an error path is legitimate data, got %v", errored)
	}
	info, ok := infos[9]
	if !ok {
		t.Fatalf("a null comment author is legitimate data and must parse, got %#v", infos)
	}
	if info.AIReview != "?" {
		t.Fatalf("AIReview = %q, want ? when an unresolved thread has no attributable author", info.AIReview)
	}
	if !info.UnattributableEvidence {
		t.Fatal("expected the unattributable-evidence flag to be set")
	}
	if info.HasUnresolvedAIThreads {
		t.Fatal("an unattributable thread must not be counted as a confirmed AI finding")
	}

	// The rendered diagnostic names the PR and the reason.
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()
	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return infos, nil, nil
	}
	data, _, _ := fetchSupplementalData("owner/repo", []pullRequest{{Number: 9}})
	if data.Err == nil || !strings.Contains(data.Err.Error(), "unattributable review evidence for pull request(s) 9") {
		t.Fatalf("diagnostic should name unattributable evidence, got %v", data.Err)
	}
}

func TestUnattributableFormalReviewForcesUnknownAI(t *testing.T) {
	envelope := `{"data":{"repository":{"pr9":{"number":9,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":1,"nodes":[{"state":"APPROVED","author":null,"commit":{"oid":"abc"},"comments":{"totalCount":0}}]},"approvedReviews":{"nodes":[]}}}}}`
	infos, errored, err := parseSupplementalResponse([]byte(envelope))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if len(errored) != 0 {
		t.Fatalf("a null review author without an error path is legitimate data, got %v", errored)
	}
	info, ok := infos[9]
	if !ok {
		t.Fatalf("a null review author is legitimate data and must parse, got %#v", infos)
	}
	if info.AIReview != "?" {
		t.Fatalf("AIReview = %q, want ? when a formal review has no attributable author", info.AIReview)
	}
	if !info.UnattributableEvidence {
		t.Fatal("expected the unattributable-evidence flag to be set")
	}

	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()
	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return infos, nil, nil
	}
	data, _, _ := fetchSupplementalData("owner/repo", []pullRequest{{Number: 9}})
	if data.Err == nil || !strings.Contains(data.Err.Error(), "unattributable review evidence for pull request(s) 9") {
		t.Fatalf("diagnostic should name unattributable evidence, got %v", data.Err)
	}
}

func TestUnattributableFormalReviewFieldsForceUnknownAI(t *testing.T) {
	// A field-level failure always carries a GraphQL error path through the
	// alias, which keeps the PR's data unavailable even when the object
	// survived with zero values.
	envelope := `{"data":{"repository":{"pr9":{"number":9,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":1,"nodes":[{"state":"APPROVED","author":{"login":"bot[bot]","__typename":"Bot"},"commit":null,"comments":{"totalCount":0}}]},"approvedReviews":{"nodes":[]}}}},"errors":[{"type":null,"path":["repository","pr9","reviews"],"message":"Field failed"}]}`
	infos, errored, err := parseSupplementalResponse([]byte(envelope))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if !errored[9] {
		t.Fatalf("the error path through pr9 must mark the alias, got %v", errored)
	}
	if _, ok := infos[9]; !ok {
		t.Fatal("the envelope still parses; the fetch layer unions the error mark into unavailable")
	}

	// A PENDING review carries no commit by design and stays legitimate data.
	pending := `{"data":{"repository":{"pr9":{"number":9,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":1,"nodes":[{"state":"PENDING","author":{"login":"user","__typename":"User"},"commit":null,"comments":{"totalCount":0}}]},"approvedReviews":{"nodes":[]}}}}}`
	infos, errored, err = parseSupplementalResponse([]byte(pending))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if len(errored) != 0 {
		t.Fatalf("a PENDING review without a commit must not be marked, got %v", errored)
	}
	info, ok := infos[9]
	if !ok {
		t.Fatalf("the PENDING review PR must parse, got %#v", infos)
	}
	if info.UnattributableEvidence {
		t.Fatal("a PENDING review must not count as unattributable evidence")
	}

	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()
	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return nil, map[int]bool{9: true}, nil
	}
	data, _, _ := fetchSupplementalData("owner/repo", []pullRequest{{Number: 9}})
	if !data.Unavailable[9] {
		t.Fatalf("the error-marked PR must stay unavailable, got %v", data.Unavailable)
	}
}

func TestNullRelationshipNodesStayUnavailable(t *testing.T) {
	// Issue side: a null node inside the connection must not render "-" (no
	// linked PRs); the issue stays unknown instead.
	issueEnvelope := `{"data":{"repository":{"issue7":{"number":7,"closedByPullRequestsReferences":{"totalCount":1,"nodes":[null]}}}}}`
	refs, errored, err := parseIssueRelationships([]byte(issueEnvelope))
	if err != nil {
		t.Fatalf("parseIssueRelationships error: %v", err)
	}
	if _, present := refs[7]; present {
		t.Fatalf("issue 7 with a null relationship node must stay unavailable, got %#v", refs[7])
	}
	if len(errored) != 0 {
		t.Fatalf("a null relationship node without an error path is malformed data, got %v", errored)
	}

	// PR side: the closing-issues connection with a null node is unavailable,
	// so the relationship column renders ? rather than a false empty value.
	prEnvelope := `{"data":{"repository":{"pr9":{"number":9,"closingIssuesReferences":{"totalCount":1,"nodes":[null]},"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},"approvedReviews":{"nodes":[]}}}}}`
	infos, errored, err := parseSupplementalResponse([]byte(prEnvelope))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if len(errored) != 0 {
		t.Fatalf("a null relationship node without an error path is malformed data, got %v", errored)
	}
	info, ok := infos[9]
	if !ok {
		t.Fatalf("the PR itself must still parse, got %#v", infos)
	}
	if info.ClosingIssuesAvailable {
		t.Fatal("closing-issues connection with a null node must be unavailable")
	}
	now := time.Date(2026, 9, 9, 4, 40, 0, 0, time.UTC)
	rendered := enrichPullRequests([]pullRequest{{Number: 9, State: "OPEN", UpdatedAt: now}}, prSupplementalData{Info: infos}, nil, nil, now)
	if rendered[0].Issues != "?" {
		t.Fatalf("Issues = %q, want ? for a null relationship node", rendered[0].Issues)
	}
}

func TestFetchErrorJoinsDerivedPerPRReasons(t *testing.T) {
	saved := fetchPRSupplementalBatchFunc
	defer func() { fetchPRSupplementalBatchFunc = saved }()

	// One batch fails with a fetch error while a retained PR also carries
	// truncated evidence; both reasons must reach the diagnostic.
	fetchPRSupplementalBatchFunc = func(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
		return map[int]prSupplementalInfo{
			2: {Incomplete: true},
		}, map[int]bool{5: true}, errors.New("gh api graphql: rate limit")
	}

	data, _, _ := fetchSupplementalData("owner/repo", []pullRequest{{Number: 2}, {Number: 5}})
	if data.Err == nil {
		t.Fatal("combined failures must produce a diagnostic")
	}
	if !strings.Contains(data.Err.Error(), "rate limit") {
		t.Fatalf("the fetch error must be carried, got %v", data.Err)
	}
	if !strings.Contains(data.Err.Error(), "truncated supplemental connections for pull request(s) 2") {
		t.Fatalf("the per-PR reason must be joined beside the fetch error, got %v", data.Err)
	}
}

func TestClosingIssueErrorPathsKeepHealthyData(t *testing.T) {
	// An error confined to closingIssuesReferences keeps the healthy review
	// and thread data rendered; only the Issues column goes unknown.
	envelope := `{"data":{"repository":{"pr9":{"number":9,"headRefOid":"abc","closingIssuesReferences":null,"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":1,"nodes":[{"state":"APPROVED","author":{"login":"bot[bot]","__typename":"Bot"},"commit":{"oid":"abc"},"comments":{"totalCount":0}}]},"approvedReviews":{"nodes":[]}}}},"errors":[{"type":"NOT_FOUND","path":["repository","pr9","closingIssuesReferences"],"message":"Field failed"}]}`
	infos, errored, err := parseSupplementalResponse([]byte(envelope))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if errored[9] {
		t.Fatalf("a closing-issues-only error path must not mark the whole alias, got %v", errored)
	}
	info, ok := infos[9]
	if !ok {
		t.Fatalf("the PR must parse, got %#v", infos)
	}
	if info.ClosingIssuesAvailable {
		t.Fatal("the closing-issues connection must stay unavailable per field")
	}
	if info.AIReview != "pass" {
		t.Fatalf("AIReview = %q, want the healthy bot review preserved", info.AIReview)
	}

	// The same path into a different connection still marks the whole alias.
	threadsEnvelope := `{"data":{"repository":{"pr9":{"number":9,"headRefOid":"abc","closingIssuesReferences":{"totalCount":0,"nodes":[]},"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":null,"reviews":{"totalCount":0,"nodes":[]},"approvedReviews":{"nodes":[]}}}},"errors":[{"type":"NOT_FOUND","path":["repository","pr9","reviewThreads"],"message":"Field failed"}]}`
	_, errored, err = parseSupplementalResponse([]byte(threadsEnvelope))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if !errored[9] {
		t.Fatalf("an error through reviewThreads must mark the alias, got %v", errored)
	}
}

func TestJoinedReasonsStayBoundedPerReason(t *testing.T) {
	longStderr := "gh: " + strings.Repeat("very long rate limit detail ", 20)
	fetchErr := ghGraphQLError(errors.New("exit status 1"), longStderr)
	joined := joinSupplementalReasons(fetchErr, errors.New("truncated supplemental connections for pull request(s) 2"))
	text := joined.Error()
	if !strings.Contains(text, "truncated supplemental connections for pull request(s) 2") {
		t.Fatalf("the per-PR reason must keep its full number list beside the capped fetch error, got %q", text)
	}
	if strings.HasSuffix(text, "2; ") {
		t.Fatal("joined reasons must not gain a trailing separator")
	}
	if len(text) > 500 {
		t.Fatalf("the joined notice must stay bounded, got %d chars", len(text))
	}
}

func TestTrimTitleIsRuneSafe(t *testing.T) {
	multibyte := strings.Repeat("字", 200)
	trimmed := trimTitle(multibyte, 150)
	if got := utf8.RuneCountInString(trimmed); got != 150 {
		t.Fatalf("trimTitle rune count = %d, want 150", got)
	}
	if !strings.HasSuffix(trimmed, "...") {
		t.Fatalf("trimTitle should end with an ellipsis, got suffix %q", trimmed[len(trimmed)-6:])
	}
	small := trimTitle(multibyte, 3)
	if got := utf8.RuneCountInString(small); got != 3 {
		t.Fatalf("trimTitle small rune count = %d, want 3", got)
	}
}

func TestTrimCellTextRespectsDisplayWidth(t *testing.T) {
	wide := strings.Repeat("字", 100)
	trimmed := trimCellText(wide, 51)
	if width := runewidth.StringWidth(trimmed); width > 51 {
		t.Fatalf("wide cell width = %d, want at most 51", width)
	}
	if !strings.HasSuffix(trimmed, "...") {
		t.Fatalf("trimmed cell should end with an ellipsis, got %q", trimmed)
	}
	ascii := strings.Repeat("a", 80)
	if got := trimCellText(ascii, 51); runewidth.StringWidth(got) > 51 {
		t.Fatalf("ascii cell width = %d, want at most 51", runewidth.StringWidth(got))
	} else if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated ascii cell should end with an ellipsis, got %q", got)
	}
	if got := trimCellText("short", 51); got != "short" {
		t.Fatalf("short cell = %q, want unchanged", got)
	}
}

func TestWideTitleRendersWithoutPanic(t *testing.T) {
	wide := strings.Repeat("字", 200)
	prs := []displayPullRequest{buildDisplayPullRequest(pullRequest{
		Number: 9, Title: wide, State: "OPEN", UpdatedAt: time.Now(),
		BaseRefName: "main", HeadRefName: "feature", URL: "https://github.com/owner/repo/pull/9",
	}, time.Now())}
	var buf bytes.Buffer
	if err := renderPullRequestRows(&buf, prs, false); err != nil {
		t.Fatalf("renderPullRequestRows error: %v", err)
	}
	if !strings.Contains(buf.String(), "字") {
		t.Fatal("the wide title should still render truncated content")
	}
}

func TestUnattributableEvidenceKeepsComputedFailure(t *testing.T) {
	envelope := `{"data":{"repository":{"pr9":{"number":9,"headRefOid":"head","closingIssuesReferences":{"totalCount":0,"nodes":[]},"comments":{"totalCount":0,"nodes":[]},"reactions":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":2,"nodes":[{"state":"COMMENTED","author":null,"commit":{"oid":"old"},"comments":{"totalCount":1}},{"state":"CHANGES_REQUESTED","author":{"login":"bot[bot]","__typename":"Bot"},"commit":{"oid":"head"},"comments":{"totalCount":1}}]},"approvedReviews":{"nodes":[]}}}}}`
	infos, errored, err := parseSupplementalResponse([]byte(envelope))
	if err != nil {
		t.Fatalf("parseSupplementalResponse error: %v", err)
	}
	if len(errored) != 0 {
		t.Fatalf("no error paths expected, got %v", errored)
	}
	info, ok := infos[9]
	if !ok {
		t.Fatalf("the PR must parse, got %#v", infos)
	}
	if !info.UnattributableEvidence {
		t.Fatal("expected the unattributable-evidence flag")
	}
	if info.AIReview != "fail" {
		t.Fatalf("AIReview = %q, want the computed fail preserved over unknown evidence", info.AIReview)
	}
}
