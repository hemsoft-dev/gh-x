package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMergedSchemaValidationPreservesRows(t *testing.T) {
	for _, payload := range []string{
		`{"errors":[{"message":"Field 'reviewThreads' doesn't exist on type 'PullRequest'"}]}`,
		`{"data":null,"errors":[{"message":"Field 'reviewThreads' doesn't exist on type 'PullRequest'","path":["query","repository","pr43","reviewThreads"]}]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			saved := ghExecFunc
			t.Cleanup(func() { ghExecFunc = saved })
			selectedCalls := 0
			ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
				query := strings.Join(args, " ")
				if strings.Contains(query, "rules/branches/") {
					return *bytes.NewBufferString("[]"), bytes.Buffer{}, nil
				}
				if strings.Contains(query, "pullRequests(states: MERGED") {
					return mergedBehaviorFixture(t, "merged-prs.json"), bytes.Buffer{}, nil
				}
				selectedCalls++
				if selectedCalls == 1 {
					if !strings.Contains(query, "reviewThreads(") {
						t.Fatal("initial selected query omitted supplemental fields")
					}
					return *bytes.NewBufferString(payload), *bytes.NewBufferString("Field 'reviewThreads' doesn't exist on type 'PullRequest'"), fmt.Errorf("exit status 1")
				}
				if selectedCalls != 2 || strings.Contains(query, "reviewThreads(") || !strings.Contains(query, "latestReviews(") || !strings.Contains(query, "contexts(") {
					t.Fatalf("invalid metadata retry: %s", query)
				}
				return mergedBehaviorFixture(t, "merged-pr-selected.json"), bytes.Buffer{}, nil
			}
			result, err := fetchPullRequestList(listOptions{repo: "HemSoft/gh-x", limit: 5, state: "merged", recentlyMerged: true}, time.Now())
			if err != nil || len(result.Entries) != 1 || result.Entries[0].Title != "Existing merge missing from search" {
				t.Fatalf("lost merged rows: %+v, %v", result, err)
			}
			if selectedCalls != 2 || !result.SupplementalFailed || result.SupplementalErr == nil || !strings.Contains(result.SupplementalErr.Error(), "reviewThreads") {
				t.Fatalf("lost original supplemental failure: %+v, calls=%d", result, selectedCalls)
			}
		})
	}
}

func TestMergedRejectedBatchRecoveryIsBounded(t *testing.T) {
	for _, test := range []struct {
		name, retryPayload string
		supplemental       bool
		wantCalls          int
	}{
		{"metadata schema failure", `{"errors":[{"message":"Field 'latestReviews' doesn't exist on type 'PullRequest'"}]}`, true, 2},
		{"metadata partial failure", `{"data":{"repository":{"pr1":{"number":1}}},"errors":[{"message":"check unavailable","path":["repository","pr1","commits"]}]}`, true, 2},
		{"missing metadata", `{"data":{"repository":{"pr1":null}}}`, true, 2},
		{"metadata only", "", false, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved := ghExecFunc
			t.Cleanup(func() { ghExecFunc = saved })
			calls := 0
			ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
				calls++
				if calls == 1 {
					return *bytes.NewBufferString(`{"errors":[{"message":"schema rejected"}]}`), *bytes.NewBufferString("schema rejected"), fmt.Errorf("exit status 1")
				}
				query := strings.Join(args, " ")
				if calls != 2 || strings.Contains(query, "reviewThreads(") || !strings.Contains(query, "pr1:") || !strings.Contains(query, "pr2:") {
					t.Fatalf("invalid batch retry: %s", query)
				}
				return *bytes.NewBufferString(test.retryPayload), bytes.Buffer{}, nil
			}
			rows, _, err := fetchSelectedMergedBatch(&mergedQueryClient{host: "github.com"}, "owner", "repo", []pullRequest{{Number: 1}, {Number: 2}}, test.supplemental)
			if err == nil || len(rows) != 0 || calls != test.wantCalls {
				t.Fatalf("rows=%+v err=%v calls=%d", rows, err, calls)
			}
		})
	}
}
