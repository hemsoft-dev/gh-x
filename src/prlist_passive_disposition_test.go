package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPassiveDisposition(t *testing.T) {
	for _, name := range []string{"chief-153.json", "chief-160.json"} {
		raw, err := os.ReadFile("testdata/passive-disposition/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, control := range []string{"disposed", "earlier-overview", "unresolved", "no-reply", "wrong-resolver", "wrong-author", "wrong-head-reply", "no-sfl", "stale-sfl", "failed-sfl", "spoofed-sfl", "ambiguous-time", "no-head-metadata", "wrong-owner", "truncated-replies", "truncated-threads", "truncated-reviews", "unknown-body", "unlinked-finding", "formal-changes-requested"} {
			t.Run(name+"/"+control, func(t *testing.T) {
				var node map[string]any
				if err := json.Unmarshal(raw, &node); err != nil {
					t.Fatal(err)
				}
				reviews := node["reviews"].(map[string]any)
				rs := reviews["nodes"].([]any)
				copilot := rs[0].(map[string]any)
				sfl := rs[1].(map[string]any)
				threads := node["reviewThreads"].(map[string]any)
				thread := threads["nodes"].([]any)[0].(map[string]any)
				comments := thread["comments"].(map[string]any)
				reply := comments["nodes"].([]any)[1].(map[string]any)
				switch control {
				case "earlier-overview":
					earlier := make(map[string]any)
					for key, value := range copilot {
						earlier[key] = value
					}
					earlier["submittedAt"] = "2026-11-02T12:00:00Z"
					reviews["nodes"] = append([]any{earlier}, rs...)
					reviews["totalCount"] = 4
				case "unresolved":
					thread["isResolved"] = false
				case "no-reply":
					comments["nodes"] = comments["nodes"].([]any)[:1]
				case "wrong-resolver":
					thread["resolvedBy"] = map[string]any{"login": "stranger"}
				case "wrong-author":
					reply["author"] = map[string]any{"login": "stranger", "__typename": "User"}
				case "wrong-head-reply":
					reply["body"] = "Evidence for an older head"
				case "no-sfl":
					reviews["nodes"] = []any{copilot}
					reviews["totalCount"] = 1
				case "stale-sfl":
					sfl["commit"] = map[string]any{"oid": "old"}
				case "failed-sfl":
					sfl["body"] = "## Verdict: FAIL"
				case "spoofed-sfl":
					sfl["author"] = map[string]any{"login": "stranger[bot]", "__typename": "Bot"}
				case "ambiguous-time":
					sfl["submittedAt"] = copilot["submittedAt"]
				case "no-head-metadata":
					sfl["body"] = "## ✅ Verdict: PASS • Round 1"
				case "wrong-owner":
					node["repository"] = map[string]any{"owner": map[string]any{"login": "relias-engineering"}}
				case "truncated-replies":
					comments["totalCount"] = 100
				case "truncated-threads":
					threads["totalCount"] = 101
				case "truncated-reviews":
					reviews["totalCount"] = 101
				case "unknown-body":
					copilot["body"] = "### Unknown verdict\n<summary><strong>2 open issues</strong></summary>\n<img alt=\"Unknown severity\"> finding"
				case "unlinked-finding":
					copilot["body"] = "### Changes recommended\n<summary><strong>1 open issue</strong></summary>\n<img alt=\"High severity\"> finding without link"
				case "formal-changes-requested":
					copilot["state"] = "CHANGES_REQUESTED"
				}
				data, err := json.Marshal(node)
				if err != nil {
					t.Fatal(err)
				}
				_, info, ok := parsePRSupplementalNode(data)
				if !ok {
					t.Fatal("parse failed")
				}
				if control == "disposed" || control == "earlier-overview" {
					if info.AIReview != "pass" || !info.AIClean {
						t.Fatalf("want pass and clean, got %+v", info)
					}
				} else if info.AIReview == "pass" {
					t.Fatalf("unsafe pass: %+v", info)
				}
			})
		}
	}
}

func TestPassiveDispositionTally(t *testing.T) {
	raw, err := os.ReadFile("testdata/passive-disposition/chief-160.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range []string{"disposed", "unresolved", "wrong-resolver", "no-reply", "no-sfl", "mismatch", "unlinked", "tally-only", "section-tally"} {
		t.Run(control, func(t *testing.T) {
			var node map[string]any
			if err := json.Unmarshal(raw, &node); err != nil {
				t.Fatal(err)
			}
			reviews := node["reviews"].(map[string]any)
			rs := reviews["nodes"].([]any)
			copilot := rs[0].(map[string]any)
			threads := node["reviewThreads"].(map[string]any)
			thread := threads["nodes"].([]any)[0].(map[string]any)
			comments := thread["comments"].(map[string]any)
			first := comments["nodes"].([]any)[0].(map[string]any)
			id := first["fullDatabaseId"].(string)
			tally := "**Findings:** 1 " + copilotBadge("High")
			finding := copilotOpenLine("High", id)
			if control == "mismatch" {
				tally = "**Findings:** 2 " + copilotBadge("High")
			}
			if control == "unlinked" {
				finding = "- " + copilotBadge("High") + " Unlinked finding"
			}
			copilot["body"] = copilotBody(copilotChanges, tally, copilotSection("1 open issue", finding))
			switch control {
			case "unresolved":
				thread["isResolved"] = false
			case "wrong-resolver":
				thread["resolvedBy"] = map[string]any{"login": "stranger"}
			case "no-reply":
				comments["nodes"] = comments["nodes"].([]any)[:1]
			case "no-sfl":
				reviews["nodes"] = []any{copilot}
				reviews["totalCount"] = 1
			case "section-tally":
				copilot["body"] = copilotBody(copilotChanges, "**Findings:** 2 "+copilotBadge("High"), copilotSection("2 open issues", finding, "**Findings:** 1 "+copilotBadge("High")))
			case "tally-only":
				copilot["body"] = copilotBody(copilotChanges, "**Findings:** 0 "+copilotBadge("High"))
			}
			data, err := json.Marshal(node)
			if err != nil {
				t.Fatal(err)
			}
			_, info, ok := parsePRSupplementalNode(data)
			if !ok {
				t.Fatal("parse failed")
			}
			if control == "disposed" {
				if info.AIReview != "pass" || !info.AIClean {
					t.Fatalf("want pass and clean, got %+v", info)
				}
			} else if info.AIReview == "pass" {
				t.Fatalf("unsafe pass: %+v", info)
			}
		})
	}
}
