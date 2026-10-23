package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var mergedAliasPattern = regexp.MustCompile(`pr(\d+): pullRequest\(number: (\d+)\)`)

func selectedMergedTestResponse(t *testing.T, query string, details map[int]any) bytes.Buffer {
	t.Helper()
	repository := map[string]any{}
	for _, match := range mergedAliasPattern.FindAllStringSubmatch(query, -1) {
		number, err := strconv.Atoi(match[1])
		if err != nil || match[1] != match[2] {
			t.Fatal("invalid alias")
		}
		node := details[number]
		if node == nil {
			node = map[string]any{"number": number}
		}
		repository["pr"+match[1]] = node
	}
	raw, err := json.Marshal(map[string]any{"data": map[string]any{"repository": repository}})
	if err != nil {
		t.Fatal(err)
	}
	return *bytes.NewBuffer(raw)
}

func TestMergedSelectionLargeAndCappedAcquisitions(t *testing.T) {
	for _, test := range []struct {
		name                              string
		total, wantRequests, wantEnriched int
		capFailure                        bool
	}{
		{"large", 125, 5, 1, false}, {"cap", 1000, 12, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved := ghExecFunc
			t.Cleanup(func() { ghExecFunc = saved })
			requests, candidates, enriched := 0, 0, 0
			ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
				requests++
				query := strings.Join(args, " ")
				if strings.Contains(query, "pullRequest(number:") {
					enriched += len(mergedAliasPattern.FindAllStringSubmatch(query, -1))
					return selectedMergedTestResponse(t, query, nil), bytes.Buffer{}, nil
				}
				if !strings.Contains(query, "nodes { number updatedAt mergedAt }") || strings.Contains(query, "contexts") || strings.Contains(query, "latestReviews") {
					t.Fatal("heavy candidate query")
				}
				pattern := regexp.MustCompile(`first: (\d+), after: (null|"(\d+)")`)
				match := pattern.FindStringSubmatch(query)
				if match == nil {
					t.Fatal("missing page arguments")
				}
				count, _ := strconv.Atoi(match[1])
				start := 0
				if match[3] != "" {
					start, _ = strconv.Atoi(match[3])
				}
				if start != candidates {
					t.Fatalf("reacquired candidate page: start=%d fetched=%d", start, candidates)
				}
				end := min(start+count, test.total)
				rows := make([]pullRequest, 0, end-start)
				for index := start; index < end; index++ {
					row := pullRequest{Number: index + 1, MergedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC).Add(-time.Duration(index) * time.Minute)}
					if index == test.total-1 && !test.capFailure {
						row.MergedAt = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
						row.UpdatedAt = row.MergedAt
					}
					rows = append(rows, row)
				}
				candidates = end
				raw, err := json.Marshal(rows)
				if err != nil {
					t.Fatal(err)
				}
				return *bytes.NewBufferString(fmt.Sprintf(`{"data":{"repository":{"pullRequests":{"nodes":%s,"pageInfo":{"hasNextPage":%t,"endCursor":"%d"}}}}}`, raw, test.capFailure || end < test.total, end)), bytes.Buffer{}, nil
			}
			rows, err := fetchRecentlyMergedPullRequests(listOptions{repo: "owner/repo", limit: 1})
			if (err != nil) != test.capFailure {
				t.Fatalf("err=%v", err)
			}
			if !test.capFailure && (len(rows) != 1 || rows[0].Number != test.total) {
				t.Fatalf("selection=%v", rows)
			}
			if requests != test.wantRequests || enriched != test.wantEnriched || candidates != test.total {
				t.Fatalf("requests=%d enriched=%d candidates=%d", requests, enriched, candidates)
			}
			t.Logf("MERGED_REQUEST_BUDGET fixture=%s requests=%d candidates=%d enriched=%d", test.name, requests, candidates, enriched)
		})
	}
}

func TestMergedCombinedEvidenceAndPartialFailures(t *testing.T) {
	for _, test := range []struct {
		name, failedField    string
		wantErr, wantPartial bool
	}{
		{"complete", "", false, false}, {"supplemental error", "comments", false, true}, {"check error", "commits", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved := ghExecFunc
			t.Cleanup(func() { ghExecFunc = saved })
			candidateRequests, selectedRequests := 0, 0
			ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
				query := strings.Join(args, " ")
				if strings.Contains(query, "rules/branches/") {
					return *bytes.NewBufferString("[]"), bytes.Buffer{}, nil
				}
				if strings.Contains(query, "pullRequests(states: MERGED") {
					candidateRequests++
					return mergedBehaviorFixture(t, "merged-prs.json"), bytes.Buffer{}, nil
				}
				if !strings.Contains(query, "latestReviews") || !strings.Contains(query, "approvedReviews") {
					t.Fatal("details not combined with supplemental query")
				}
				selectedRequests++
				body := mergedBehaviorFixture(t, "merged-pr-selected.json")
				if test.failedField == "" {
					return body, bytes.Buffer{}, nil
				}
				var response map[string]any
				if err := json.Unmarshal(body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				response["errors"] = []any{map[string]any{"message": "fixture denied", "path": []string{"repository", "pr43", test.failedField}}}
				raw, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return *bytes.NewBuffer(raw), *bytes.NewBufferString("fixture denied"), fmt.Errorf("exit status1")
			}
			result, err := fetchPullRequestList(listOptions{repo: "HemSoft/gh-x", limit: 5, state: "merged", recentlyMerged: true}, time.Now())
			if (err != nil) != test.wantErr {
				t.Fatalf("err=%v", err)
			}
			if !test.wantErr {
				if len(result.Entries) != 1 || result.Entries[0].Title != "Existing merge missing from search" {
					t.Fatalf("lost metadata: %#v", result.Entries)
				}
				if result.SupplementalFailed != test.wantPartial {
					t.Fatalf("partial=%v err=%v", result.SupplementalFailed, result.SupplementalErr)
				}
			}
			t.Logf("MERGED_REQUEST_BUDGET fixture=populated candidate_requests=%d selected_requests=%d", candidateRequests, selectedRequests)
			if candidateRequests != 1 || selectedRequests != 1 {
				t.Fatalf("candidate requests=%d selected requests=%d", candidateRequests, selectedRequests)
			}
		})
	}
}

func mergedBehaviorFixture(t *testing.T, name string) bytes.Buffer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "tests", "behavior", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return *bytes.NewBuffer(raw)
}

func TestMergedSelectedErrorsAndQuerySize(t *testing.T) {
	for _, raw := range []string{"invalid", `{"data":{"repository":null}}`, `{"data":{"repository":{"pr1":null}}}`, `{"data":{"repository":{"pr1":{"number":2}}}}`, `{"data":{"repository":{"pr1":{"number":1}}},"errors":[{"message":"global failure"}]}`} {
		if _, err := parseSelectedMerged([]byte(raw), []int{1}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	numbers := make([]int, mergedSelectionBatchSize)
	for i := range numbers {
		numbers[i] = 10000000 + i
	}
	query := buildPullRequestBatchQuery(strings.Repeat("o", 39), strings.Repeat("r", 100), numbers, mergedPRFields+prSupplementalFields)
	if len(query) > 30000 {
		t.Fatalf("selected batch has %d query bytes; Windows reserve exceeded", len(query))
	}
	t.Logf("Selected batch query bytes=%d", len(query))
	for _, raw := range []string{"invalid", `{}`, `{"errors":[{"message":"global"}]}`} {
		if onlyMergedSupplementalErrors([]byte(raw)) {
			t.Fatalf("accepted unrelated failure: %s", raw)
		}
	}
}

func TestMergedRecordedGitHubResponse(t *testing.T) {
	raw, err := os.ReadFile("testdata/merged/github-pr240-2026-10-22.json")
	if err != nil {
		t.Fatal(err)
	}
	page, err := parseMergedPRPage(raw)
	if err != nil || len(page.Nodes) != 1 {
		t.Fatalf("candidate capture: %v", err)
	}
	nodes, err := parseSelectedMerged(raw, []int{240})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("selected capture: %v", err)
	}
	row := nodes[0].pullRequestRow()
	if row.Number != 240 || row.State != "MERGED" || len(row.StatusCheckRollup) == 0 {
		t.Fatalf("capture lost selected data: %#v", row)
	}
	infos, missing, err := parseSupplementalBatch(raw, nil, []int{240})
	if err != nil || missing[240] || infos[240].AIReview != "pass" {
		t.Fatalf("supplemental capture: %+v, %+v, %v", infos, missing, err)
	}
}
