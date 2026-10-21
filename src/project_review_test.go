package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestProjectReviewOperationTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		expected    time.Duration
		expire      bool
	}{
		{"default", "", 30 * time.Second, false}, {"configured", "25ms", 25 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(githubCommandTimeoutEnv, tc.value)
			old := ghExecContextFunc
			t.Cleanup(func() { ghExecContextFunc = old })
			ghExecContextFunc = func(ctx context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("project operation lacks a deadline")
				}
				remaining := time.Until(deadline)
				if remaining <= 0 || remaining > tc.expected || remaining < tc.expected-time.Second {
					t.Fatalf("unexpected deadline %v for %v", remaining, tc.expected)
				}
				if tc.expire {
					<-ctx.Done()
					return bytes.Buffer{}, bytes.Buffer{}, ctx.Err()
				}
				return *bytes.NewBufferString(`[]`), bytes.Buffer{}, nil
			}
			_, err := fetchProjectData(projectOptions{repo: "example/loops", groupBy: "label"}, time.Now())
			if tc.expire {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline failure lost %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("invalid configuration", func(t *testing.T) {
		t.Setenv(githubCommandTimeoutEnv, "bad")
		projectExecutor(t, func(string) (string, error) { return "", errors.New("unexpected transport after invalid timeout") })
		_, err := fetchProjectData(projectOptions{repo: "example/loops"}, time.Now())
		if err == nil || !strings.Contains(err.Error(), githubCommandTimeoutEnv) {
			t.Fatalf("invalid timeout accepted %v", err)
		}
	})
}

func TestProjectReviewBatchRequestCount(t *testing.T) {
	issues := make([]projectIssue, 65)
	for i := range issues {
		issues[i].Number = i + 1
	}
	calls := 0
	projectExecutor(t, func(query string) (string, error) {
		calls++
		aliases := regexp.MustCompile(`issue(\d+): issue\(`).FindAllStringSubmatch(query, -1)
		nodes := map[string]any{}
		node := map[string]any{"projectItems": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}}
		if len(aliases) == 0 {
			nodes["issue"] = node
		} else {
			for _, alias := range aliases {
				nodes["issue"+alias[1]] = node
			}
		}
		raw, err := json.Marshal(map[string]any{"data": map[string]any{"repository": nodes}})
		return string(raw), err
	})
	got, err := fetchProjectMemberships(context.Background(), "example", "loops", "github.com", issues)
	if err != nil || len(got) != 65 || calls != 3 {
		t.Fatalf("membership budget: issues=%d requests=%d want3 error=%v", len(got), calls, err)
	}
}

func TestProjectReviewReservedGroupNames(t *testing.T) {
	for _, mode := range []string{"label", "project"} {
		t.Run(mode, func(t *testing.T) {
			data := projectData{Open: []projectIssue{{Number: 1}, {Number: 2, Labels: []string{"No label"}}}}
			if mode == "project" {
				data.Project = &projectReference{ID: "p", StatusOptions: []projectStatusOption{{ID: "none", Name: "No status"}}}
				membership := projectMembership{Project: projectReference{ID: "p"}}
				membership.Status.OptionID = "none"
				data.Memberships = map[int][]projectMembership{2: {membership}}
			}
			doc := buildProjectDocument(data, projectOptions{})
			if len(doc.Groups) != 2 || doc.Groups[0].Count != 1 || doc.Groups[1].Count != 1 || doc.Groups[0].Name == doc.Groups[1].Name {
				t.Fatalf("reserved name collapsed distinct groups: %+v", doc.Groups)
			}
			encoded, err := json.Marshal(doc.Groups)
			if err != nil {
				t.Fatal(err)
			}
			var groups []struct{ ID string }
			if err = json.Unmarshal(encoded, &groups); err != nil {
				t.Fatal(err)
			}
			if groups[0].ID == "" || groups[1].ID == "" || groups[0].ID == groups[1].ID {
				t.Fatalf("JSON group identity lost: %s", encoded)
			}
		})
	}
	t.Run("duplicate status names", func(t *testing.T) {
		data := projectData{Project: &projectReference{ID: "p", StatusOptions: []projectStatusOption{{"one", "Todo"}, {"two", "Todo"}}}}
		for i, option := range data.Project.StatusOptions {
			n := i + 1
			data.Open = append(data.Open, projectIssue{Number: n})
			if data.Memberships == nil {
				data.Memberships = map[int][]projectMembership{}
			}
			m := projectMembership{Project: projectReference{ID: "p"}}
			m.Status.OptionID = option.ID
			data.Memberships[n] = []projectMembership{m}
		}
		doc := buildProjectDocument(data, projectOptions{})
		if len(doc.Groups) != 2 || len(doc.Groups[0].Issues) != 1 || len(doc.Groups[1].Issues) != 1 || doc.Groups[0].Issues[0].Number != 1 || doc.Groups[1].Issues[0].Number != 2 {
			t.Fatalf("status identities collapsed: %+v", doc.Groups)
		}
	})
}

func TestProjectFallbackSuffixCollision(t *testing.T) {
	data := projectData{Open: []projectIssue{{Number: 1}, {Number: 2, Labels: []string{"No label"}}, {Number: 3, Labels: []string{"No label (unlabeled)"}}}}
	doc := buildProjectDocument(data, projectOptions{})
	names := map[string]bool{}
	for _, group := range doc.Groups {
		if names[group.Name] {
			t.Fatalf("ambiguous fallback display name: %+v", doc.Groups)
		}
		names[group.Name] = true
	}
	if len(names) != 3 {
		t.Fatalf("collapsed fallback groups: %+v", doc.Groups)
	}
}

func TestProjectRecordedHiddenProjectFallback(t *testing.T) {
	for _, exitFailure := range []bool{true, false} {
		t.Run(map[bool]string{true: "gh exits nonzero", false: "HTTP success with errors"}[exitFailure], func(t *testing.T) {
			projectTestTransport(t, "project")
			previous := ghExecContextFunc
			ghExecContextFunc = func(ctx context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
				if strings.Contains(strings.Join(args, " "), "projectV2") {
					var err error
					if exitFailure {
						err = errors.New("exit status 1")
					}
					return *bytes.NewBuffer(projectFixture(t, "project-hidden")), *bytes.NewBufferString("gh: Could not resolve to a ProjectV2 with the number 1."), err
				}
				return previous(ctx, args...)
			}
			data, err := fetchProjectData(projectOptions{repo: "example/loops", project: "example/1"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			doc := buildProjectDocument(data, projectOptions{})
			if doc.Project != nil || doc.Grouping != "label" || len(doc.Hints) != 1 || !strings.Contains(doc.Hints[0], "read:project") || !strings.Contains(doc.Hints[0], "--project") {
				t.Fatalf("hidden project did not fall back safely: %+v", doc)
			}
		})
	}
}

func TestProjectHiddenClassification(t *testing.T) {
	for _, tc := range []struct {
		name, kind, path string
		missing          bool
	}{
		{"project missing", "NOT_FOUND", `["repositoryOwner","projectV2"]`, true},
		{"issue missing", "NOT_FOUND", `["repository","issue1"]`, false},
		{"different error", "FORBIDDEN", `["repositoryOwner","projectV2"]`, false},
		{"missing path", "NOT_FOUND", `[]`, false},
		{"numeric path", "NOT_FOUND", `[1]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path []json.RawMessage
			if err := json.Unmarshal([]byte(tc.path), &path); err != nil {
				t.Fatal(err)
			}
			err := projectResponseError([]projectGraphQLError{{graphQLError: graphQLError{Message: "test failure", Path: path}, Type: tc.kind}})
			if errors.Is(err, errProjectUnavailable) != tc.missing {
				t.Fatalf("incorrect access classification %v", err)
			}
		})
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if projectScopeError(errors.Join(errProjectUnavailable, cause)) {
			t.Fatal("project fallback hid cancellation or deadline")
		}
	}
}

func TestProjectMembershipQueriesExcludeArchived(t *testing.T) {
	calls := 0
	projectExecutor(t, func(query string) (string, error) {
		calls++
		if !strings.Contains(query, "includeArchived:false") {
			t.Fatalf("membership request includes archived board items: %s", query)
		}
		raw := projectBatchFixture(t, query, string(projectFixture(t, "memberships")))
		var body map[string]any
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		repository := body["data"].(map[string]any)["repository"].(map[string]any)
		for _, value := range repository {
			page := value.(map[string]any)["projectItems"].(map[string]any)
			page["pageInfo"] = map[string]any{"hasNextPage": calls == 1, "endCursor": "next"}
			if calls > 1 {
				page["nodes"] = []any{}
			}
		}
		data, err := json.Marshal(body)
		return string(data), err
	})
	got, err := fetchProjectMemberships(context.Background(), "example", "loops", "github.com", []projectIssue{{Number: 1}})
	if err != nil || calls != 2 || len(got[1]) != 1 {
		t.Fatalf("active membership pagination: %+v calls%d error%v", got, calls, err)
	}
}

func TestProjectRecordedMergedClosingPR(t *testing.T) {
	projectExecutor(t, func(query string) (string, error) {
		if strings.Contains(query, "closedByPullRequestsReferences") {
			if !strings.Contains(query, "includeClosedPrs:true") {
				t.Fatalf("project query must include closed and merged PRs: %s", query)
			}
			return string(projectFixture(t, "merged-closing-pr")), nil
		}
		if strings.Contains(query, "subIssuesSummary") {
			return `{"data":{"repository":{"issue230":{"number":230,"parent":null,"subIssuesSummary":{"completed":0,"total":0}}}}}`, nil
		}
		t.Fatalf("unexpected request: %s", query)
		return "", nil
	})
	issues := []projectIssue{{Number: 230}}
	var data projectData
	enrichProjectIssues(context.Background(), "hemsoft-dev", "gh-x", "github.com", issues, &data)
	if len(issues[0].PullRequests) != 1 || issues[0].PullRequests[0].Number != 231 || issues[0].PullRequests[0].URL != "https://github.com/hemsoft-dev/gh-x/pull/231" || len(data.Hints) != 0 {
		t.Fatalf("merged closing PR lost: %+v hints%v", issues, data.Hints)
	}
}
