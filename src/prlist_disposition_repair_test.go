package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func dispositionFixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/passive-disposition/chief-153.json")
	if err != nil {
		t.Fatal(err)
	}
	var node map[string]any
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	return node
}
func TestPassiveDispositionStructuredMarker(t *testing.T) {
	head := "e7ea04aa92612d58f36159ef5664db331ee366c1"
	marker := "<!-- sfl request=123 sha=" + head + " -->"
	old := "<!-- sfl request=123 sha=" + strings.Repeat("a", 40) + " -->"
	fence := strings.Repeat(string(rune(96)), 3)
	for _, tc := range []struct {
		name, body string
		pass       bool
	}{
		{"canonical", marker, true},
		{"prose", "The result includes sha=" + head + " -->", false},
		{"stale-with-current-prose", old + "\nQuoted sha=" + head + " -->", false},
		{"duplicate", marker + "\n" + marker, false},
		{"conflicting", old + "\n" + marker, false},
		{"malformed", "<!-- sfl request=oops sha=" + head + " -->", false},
		{"short", "<!-- sfl request=123 sha=" + head[:10] + " -->", false},
		{"quoted", "> " + marker, false},
		{"inline", "Example " + marker, false},
		{"fenced", fence + "html\n" + marker + "\n" + fence, false},
		{"open-fence", "~~~html\n" + marker, false},
		{"stale-with-current-quote", old + "\n> " + marker, false},
		{"malformed-second", marker + "\n<!-- sfl invalid -->", false},
		{"closed-fence", fence + "go\nexample\n" + fence + "\n\n" + marker, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := dispositionFixture(t)
			reviews := node["reviews"].(map[string]any)["nodes"].([]any)
			reviews[1].(map[string]any)["body"] = "## ✅ Verdict: PASS • Round 1\n\n" + tc.body
			raw, _ := json.Marshal(node)
			_, info, ok := parsePRSupplementalNode(raw)
			if !ok || (info.AIReview == "pass") != tc.pass {
				t.Fatalf("pass=%v: %+v", tc.pass, info)
			}
		})
	}
}
func supplementalEnvelope(t *testing.T, node map[string]any, partial bool) []byte {
	t.Helper()
	envelope := map[string]any{"data": map[string]any{"repository": map[string]any{"pr153": node}}}
	if partial {
		envelope["errors"] = []any{map[string]any{"message": "unavailable", "path": []any{"repository", "pr153", "reviewThreads"}}}
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestPassiveDispositionFetchPipeline(t *testing.T) {
	for _, mode := range []string{"merged-candidate", "merged-partial-follow", "merged-moved-head", "merged-other-owner", "candidate", "other-owner", "no-sfl", "stale-copilot", "unresolved", "truncated-reviews", "partial-initial", "follow-error", "partial-follow", "missing-follow", "moved-head", "truncated-follow", "untrusted-reply"} {
		t.Run(mode, func(t *testing.T) {
			merged := strings.HasPrefix(mode, "merged-")
			mode = strings.TrimPrefix(mode, "merged-")
			original := ghExecFunc
			defer func() { ghExecFunc = original }()
			node := dispositionFixture(t)
			full := dispositionFixture(t)
			owner := "hemsoft-dev"
			reviews := node["reviews"].(map[string]any)
			rs := reviews["nodes"].([]any)
			threads := node["reviewThreads"].(map[string]any)["nodes"].([]any)
			comments := threads[0].(map[string]any)["comments"].(map[string]any)
			comments["nodes"] = comments["nodes"].([]any)[:1]
			delete(comments, "totalCount")
			switch mode {
			case "other-owner":
				owner = "relias-engineering"
			case "no-sfl":
				reviews["nodes"] = []any{rs[0]}
				reviews["totalCount"] = 1
			case "stale-copilot":
				rs[0].(map[string]any)["commit"] = map[string]any{"oid": "old"}
			case "unresolved":
				threads[0].(map[string]any)["isResolved"] = false
			case "truncated-reviews":
				reviews["totalCount"] = 101
			case "moved-head":
				full["headRefOid"] = strings.Repeat("a", 40)
			case "truncated-follow":
				full["reviewThreads"].(map[string]any)["totalCount"] = 101
			case "untrusted-reply":
				full["reviewThreads"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["resolvedBy"] = map[string]any{"login": "stranger"}
			}
			calls := 0
			ghExecFunc = func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
				calls++
				query := strings.Join(args, " ")
				if calls == 1 {
					if !strings.Contains(query, "comments(first: 1)") || strings.Contains(query, "comments(first: 20)") {
						t.Fatal("ordinary query expanded")
					}
					return *bytes.NewBuffer(supplementalEnvelope(t, node, mode == "partial-initial")), bytes.Buffer{}, nil
				}
				if calls != 2 || !strings.Contains(query, "comments(first: 20)") || strings.Count(query, "pullRequest(number:") != 1 {
					t.Fatal("unbounded disposition query")
				}
				if mode == "follow-error" {
					return bytes.Buffer{}, bytes.Buffer{}, errors.New("follow-up failed")
				}
				if mode == "missing-follow" {
					return *bytes.NewBufferString("{}"), bytes.Buffer{}, nil
				}
				return *bytes.NewBuffer(supplementalEnvelope(t, full, mode == "partial-follow")), bytes.Buffer{}, nil
			}
			var infos map[int]prSupplementalInfo
			var unavailable map[int]bool
			var err error
			if merged {
				_, info, fetchErr := fetchSelectedMergedBatch(&mergedQueryClient{host: "github.com"}, owner, "chief", []pullRequest{{Number: 153}}, true)
				infos, unavailable, err = info.Info, info.Unavailable, retainFirstError(fetchErr, info.Err)
			} else {
				infos, unavailable, err = fetchPRSupplementalBatch(owner, "chief", "github.com", []int{153})
			}
			wantsFollow := mode == "candidate" || strings.HasPrefix(mode, "follow-") || strings.HasSuffix(mode, "follow") || mode == "moved-head" || mode == "untrusted-reply"
			if (calls == 2) != wantsFollow {
				t.Fatalf("calls=%d mode=%s err=%v", calls, mode, err)
			}
			if mode == "candidate" {
				if unavailable[153] || err != nil || infos[153].AIReview != "pass" {
					t.Fatalf("candidate: %+v %v %v", infos, unavailable, err)
				}
			} else if wantsFollow && infos[153].AIReview == "pass" {
				t.Fatal("invalid follow-up cleared finding")
			}
		})
	}
}
