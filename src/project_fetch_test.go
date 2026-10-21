package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func projectExecutor(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	old := ghExecContextFunc
	ghExecContextFunc = func(_ context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
		raw, err := fn(strings.Join(args, " "))
		return *bytes.NewBufferString(raw), bytes.Buffer{}, err
	}
	t.Cleanup(func() { ghExecContextFunc = old })
}

func TestProjectPagination(t *testing.T) {
	t.Run("REST", func(t *testing.T) {
		calls := 0
		projectExecutor(t, func(q string) (string, error) {
			calls++
			if !strings.Contains(q, "--hostname example.test") || !strings.Contains(q, "state=closed") || !strings.Contains(q, "since=2026-10-14T12%3A00%3A00Z") {
				t.Fatalf("request %s", q)
			}
			if calls == 1 {
				rows := make([]projectRESTIssue, 100)
				for i := range rows {
					closed := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
					rows[i] = projectRESTIssue{Number: i + 1, ClosedAt: &closed, Labels: []issueLabel{{Name: "go"}}}
				}
				raw, err := json.Marshal(rows)
				return string(raw), err
			}
			if !strings.Contains(q, "page=2") {
				t.Fatalf("cursor request %s", q)
			}
			return `[]`, nil
		})
		got, err := fetchProjectIssues(context.Background(), "example.test", "example/loops", "closed", 7, time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
		if err != nil || calls != 2 || len(got) != 100 || got[99].Number != 100 || !reflect.DeepEqual(got[0].Labels, []string{"go"}) {
			t.Fatalf("paging len=%d calls=%d err=%v", len(got), calls, err)
		}
	})
	t.Run("memberships and fields", func(t *testing.T) {
		membershipCalls, fieldCalls := 0, 0
		projectExecutor(t, func(q string) (string, error) {
			var raw map[string]any
			name := "memberships"
			if strings.Contains(q, "projectV2") {
				name = "project"
			}
			if err := json.Unmarshal(projectFixture(t, name), &raw); err != nil {
				t.Fatal(err)
			}
			data := raw["data"].(map[string]any)
			var connection map[string]any
			var calls int
			if name == "memberships" {
				membershipCalls++
				calls = membershipCalls
				connection = data["repository"].(map[string]any)["issue"].(map[string]any)["projectItems"].(map[string]any)
			} else {
				fieldCalls++
				calls = fieldCalls
				connection = data["repositoryOwner"].(map[string]any)["projectV2"].(map[string]any)["fields"].(map[string]any)
			}
			connection["pageInfo"] = map[string]any{"hasNextPage": calls == 1, "endCursor": "next"}
			if calls == 2 {
				if !strings.Contains(q, `after:"next"`) {
					t.Fatalf("cursor missing %s", q)
				}
				connection["nodes"] = []any{}
			}
			value, err := json.Marshal(raw)
			if name == "memberships" {
				return projectBatchFixture(t, q, string(value)), err
			}
			return string(value), err
		})
		got, err := fetchProjectMemberships(context.Background(), "example", "loops", "github.com", []projectIssue{{Number: 2}})
		if err != nil || len(got[2]) != 1 || got[2][0].Status.OptionID != "13ed22ba" || membershipCalls != 2 {
			t.Fatalf("memberships %+v %d %v", got, membershipCalls, err)
		}
		p, err := fetchSelectedProject(context.Background(), "github.com", "example/1")
		if err != nil || len(p.StatusOptions) != 4 || fieldCalls != 2 {
			t.Fatalf("fields %+v %d %v", p, fieldCalls, err)
		}
	})
}

func TestProjectTransportFailures(t *testing.T) {
	for _, raw := range []string{`null`, `{`, `{"errors":[{"message":"denied"}]}`, `{"data":null}`, `{"data":{"repository":{"issue":null}}}`, `{"data":{"repository":{"issue":{"projectItems":null}}}}`, `{"data":{"repository":{"issue":{"projectItems":{"nodes":[null],"pageInfo":{}}}}}}`} {
		t.Run(raw, func(t *testing.T) {
			projectExecutor(t, func(string) (string, error) { return raw, nil })
			if _, err := fetchProjectMemberships(context.Background(), "example", "loops", "github.com", []projectIssue{{Number: 1}}); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
	for _, raw := range []string{`null`, `{`, `{"data":{"repositoryOwner":null}}`, `{"data":{"repositoryOwner":{"projectV2":null}}}`, `{"data":{"repositoryOwner":{"projectV2":{"id":"p","number":1,"fields":null}}}}`, `{"data":{"repositoryOwner":{"projectV2":{"id":"p","number":1,"fields":{"nodes":[],"pageInfo":{}}}}}}`} {
		t.Run("fields "+raw, func(t *testing.T) {
			projectExecutor(t, func(string) (string, error) { return raw, nil })
			if _, err := fetchSelectedProject(context.Background(), "github.com", "example/1"); err == nil {
				t.Fatal("missing status accepted")
			}
		})
	}
	for _, raw := range []string{`null`, `{`} {
		t.Run("REST "+raw, func(t *testing.T) {
			projectExecutor(t, func(string) (string, error) { return raw, nil })
			if _, err := fetchProjectIssues(context.Background(), "github.com", "example/loops", "open", 0, time.Now()); err == nil {
				t.Fatal("invalid REST accepted")
			}
		})
	}
	for _, next := range []string{"", "same"} {
		cursor := "same"
		if err := advanceProjectCursor(&cursor, projectPageInfo{HasNextPage: true, EndCursor: next}); err == nil || cursor != "same" {
			t.Fatalf("nonadvancing cursor %s %v", cursor, err)
		}
	}
	cursor := "old"
	if err := advanceProjectCursor(&cursor, projectPageInfo{EndCursor: "new"}); err != nil || cursor != "new" {
		t.Fatalf("cursor not advanced %s %v", cursor, err)
	}
	if _, err := decodeProjectMembership([]byte(`{`)); err == nil {
		t.Fatal("invalid membership accepted")
	}
	if _, err := fetchSelectedProject(context.Background(), "github.com", "invalid"); err == nil {
		t.Fatal("invalid selector accepted")
	}
}

func TestProjectSelection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		memberships map[int][]projectMembership
		selector    string
		expected    string
	}{
		{"none", nil, "", ""},
		{"explicit", nil, "example/1", "example"},
		{"most issues", map[int][]projectMembership{1: {{Project: projectReference{ID: "b", Owner: "z", Number: 1}}, {Project: projectReference{ID: "a", Owner: "example", Number: 1}}}, 2: {{Project: projectReference{ID: "a", Owner: "example", Number: 1}}}}, "", "example"},
		{"owner tie", map[int][]projectMembership{1: {{Project: projectReference{ID: "b", Owner: "z", Number: 1}}, {Project: projectReference{ID: "a", Owner: "example", Number: 1}}}}, "", "example"},
		{"number tie", map[int][]projectMembership{1: {{Project: projectReference{ID: "b", Owner: "example", Number: 2}}, {Project: projectReference{ID: "a", Owner: "example", Number: 1}}}}, "", "example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			projectExecutor(t, func(q string) (string, error) {
				calls++
				if !strings.Contains(q, `login:"example"`) || !strings.Contains(q, "number:1") {
					t.Fatalf("chosen %s", q)
				}
				return string(projectFixture(t, "project")), nil
			})
			got, err := selectIssueProject(context.Background(), "github.com", tc.memberships, tc.selector)
			if err != nil {
				t.Fatal(err)
			}
			if tc.expected == "" {
				if got != nil || calls != 0 {
					t.Fatalf("no project %+v calls%d", got, calls)
				}
			} else if got == nil || got.Owner != tc.expected || calls != 1 {
				t.Fatalf("selection %+v %d", got, calls)
			}
		})
	}
}

func TestProjectAcquisitionFailures(t *testing.T) {
	for _, phase := range []string{"open", "closed", "cancel", "scope metadata"} {
		t.Run(phase, func(t *testing.T) {
			projectTestTransport(t, "project")
			original := ghExecContextFunc
			ghExecContextFunc = func(ctx context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
				value := strings.Join(args, " ")
				if strings.Contains(value, "state="+phase) {
					return bytes.Buffer{}, *bytes.NewBufferString("failed " + phase), errors.New("transport")
				}
				if phase == "scope metadata" && strings.Contains(value, "projectV2") {
					return bytes.Buffer{}, *bytes.NewBufferString("requires read:project"), errors.New("transport")
				}
				return original(ctx, args...)
			}
			prior := commandContext
			if phase == "cancel" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				commandContext = ctx
				t.Cleanup(func() { commandContext = prior })
			}
			data, err := fetchProjectData(projectOptions{repo: "example/loops", closedDays: 7}, time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
			if phase == "scope metadata" {
				if err != nil || data.Project != nil || len(data.Hints) != 1 {
					t.Fatalf("metadata scope %+v %v", data, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("failed phase %s ignored", phase)
			}
		})
	}
	for _, message := range []string{"INSUFFICIENT_SCOPES", "requires read:project", "Resource not accessible by integration", "Resource not accessible by personal access token"} {
		if !projectScopeError(errors.New(message)) {
			t.Fatalf("scope denial not recognized %s", message)
		}
	}
	if projectScopeError(errors.New("401 Bad credentials")) || projectScopeError(errors.New("offline")) {
		t.Fatal("non-scope error hidden")
	}
}

func TestProjectMembershipBatchBudget(t *testing.T) {
	issues := make([]projectIssue, 65)
	for i := range issues {
		issues[i].Number = i + 1
	}
	batchCalls, tailCalls := 0, 0
	projectExecutor(t, func(q string) (string, error) {
		aliases := regexp.MustCompile(`issue(\d+): issue\(`).FindAllStringSubmatch(q, -1)
		raw := projectBatchFixture(t, q, string(projectFixture(t, "memberships")))
		var body map[string]any
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		repository := body["data"].(map[string]any)["repository"].(map[string]any)
		if len(aliases) == 0 {
			tailCalls++
			if !strings.Contains(q, `issue(number:31)`) || !strings.Contains(q, `after:"issue31-next"`) {
				t.Fatalf("incorrect issue continuation %s", q)
			}
		} else {
			batchCalls++
			if len(aliases) > relationshipBatchSize {
				t.Fatalf("unbounded batch %d", len(aliases))
			}
			for _, alias := range aliases {
				number, err := strconv.Atoi(alias[1])
				if err != nil {
					t.Fatal(err)
				}
				page := repository["issue"+alias[1]].(map[string]any)["projectItems"].(map[string]any)
				page["pageInfo"] = map[string]any{"hasNextPage": number == 31, "endCursor": "issue31-next"}
			}
		}
		value, err := json.Marshal(body)
		return string(value), err
	})
	got, err := fetchProjectMemberships(context.Background(), "example", "loops", "github.com", issues)
	if err != nil || len(got) != 65 || batchCalls != 3 || tailCalls != 1 || len(got[31]) != 2 || len(got[1]) != 1 || len(got[65]) != 1 {
		t.Fatalf("batch budget issues=%d batches=%d tails=%d issue31=%d err=%v", len(got), batchCalls, tailCalls, len(got[31]), err)
	}
}
