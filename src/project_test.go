package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func projectFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "project", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func projectTestTransport(t *testing.T, mode string) *[]string {
	t.Helper()
	old := ghExecContextFunc
	calls := []string{}
	ghExecContextFunc = func(_ context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
		value := strings.Join(args, " ")
		calls = append(calls, value)
		var raw []byte
		switch {
		case strings.Contains(value, "projectItems"):
			if mode == "scope" {
				return bytes.Buffer{}, *bytes.NewBufferString("INSUFFICIENT_SCOPES: requires read:project"), errors.New("exit status 1")
			}
			if mode == "network" {
				return bytes.Buffer{}, *bytes.NewBufferString("network offline"), errors.New("exit status 1")
			}
			raw = []byte(projectBatchFixture(t, value, string(projectFixture(t, "memberships"))))
		case strings.Contains(value, "projectV2"):
			raw = projectFixture(t, "project")
		case strings.Contains(value, "closedByPullRequestsReferences"):
			raw = []byte(`{"data":{"repository":{"issue1":{"number":1,"closedByPullRequestsReferences":{"totalCount":1,"nodes":[{"number":42,"url":"https://github.com/example/loops/pull/42"}]}},"issue2":{"number":2,"closedByPullRequestsReferences":{"totalCount":0,"nodes":[]}},"issue3":{"number":3,"closedByPullRequestsReferences":{"totalCount":0,"nodes":[]}},"issue4":{"number":4,"closedByPullRequestsReferences":{"totalCount":0,"nodes":[]}}}}}`)
		case strings.Contains(value, "subIssuesSummary"):
			raw = []byte(`{"data":{"repository":{"issue1":{"number":1,"parent":null,"subIssuesSummary":{"completed":2,"total":5}},"issue2":{"number":2,"parent":null,"subIssuesSummary":{"completed":0,"total":0}},"issue3":{"number":3,"parent":null,"subIssuesSummary":{"completed":0,"total":0}},"issue4":{"number":4,"parent":null,"subIssuesSummary":{"completed":0,"total":0}}}}}`)
		case strings.Contains(value, "state=closed"):
			raw = projectFixture(t, "closed")
		case strings.Contains(value, "state=open"):
			raw = projectFixture(t, "open")
		default:
			t.Fatalf("unexpected transport %s", value)
		}
		return *bytes.NewBuffer(raw), bytes.Buffer{}, nil
	}
	t.Cleanup(func() { ghExecContextFunc = old })
	return &calls
}

func TestProjectRecordedTransport(t *testing.T) {
	for _, mode := range []string{"project", "scope", "network", "label"} {
		t.Run(mode, func(t *testing.T) {
			calls := projectTestTransport(t, mode)
			options := projectOptions{repo: "example/loops", closedDays: 7, groupBy: "auto"}
			if mode == "label" {
				options.groupBy = "label"
				options.closedDays = 0
			}
			data, err := fetchProjectData(options, time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
			if mode == "network" {
				if err == nil || !strings.Contains(err.Error(), "offline") {
					t.Fatalf("network error hidden: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if data.Repository != "example/loops" || len(data.Open) != 4 {
				t.Fatalf("repository/issues: %+v", data)
			}
			if !reflect.DeepEqual(data.Open[0].PullRequests, []linkedReference{{Number: 42, URL: "https://github.com/example/loops/pull/42"}}) || *data.Open[0].SubIssues != (issueSubIssuesSummary{2, 5}) {
				t.Fatalf("enrichment: %+v", data.Open[0])
			}
			d := buildProjectDocument(data, options)
			switch mode {
			case "project":
				if d.Project == nil || d.Project.ID != "PROJECT_1" || d.Project.Owner != "example" || d.Project.Number != 1 || d.Project.Title != "Example loops" || d.Project.URL != "https://github.com/orgs/example/projects/1" || d.Grouping != "project" {
					t.Fatalf("project: %+v", d)
				}
				if len(d.Groups) != 4 || d.Groups[0].Name != "Waiting on Franz" || d.Groups[0].Count != 4 || d.Groups[1].Name != "Chief todo" || d.Groups[2].Name != "Watching" || d.Groups[3].Name != "Done" {
					t.Fatalf("groups: %+v", d.Groups)
				}
				if len(d.Hints) != 0 || len(d.RecentlyClosed) != 4 {
					t.Fatalf("closed/hints: %+v", d)
				}
			case "scope":
				if d.Project != nil || d.Grouping != "label" || len(d.Hints) != 1 || !strings.Contains(d.Hints[0], "read:project") {
					t.Fatalf("fallback: %+v", d)
				}
				var out bytes.Buffer
				if err = writeProject(&out, d, options, time.Now()); err != nil || strings.Count(out.String(), "Hint:") != 1 {
					t.Fatalf("hint output: %s %v", out.String(), err)
				}
			case "label":
				if len(d.RecentlyClosed) != 0 || d.Project != nil || strings.Contains(strings.Join(*calls, "\n"), "projectItems") || strings.Contains(strings.Join(*calls, "\n"), "state=closed") {
					t.Fatalf("disabled work performed: %v", *calls)
				}
			}
		})
	}
}

func TestProjectGroupingAndJSON(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	issue := projectIssue{Number: 7, Title: "Check\tcontrols\n\x1b[31m", URL: "https://github.com/example/loops/issues/7", CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-time.Hour), Labels: []string{"area:dev", "watching", "go"}, PullRequests: []linkedReference{{Number: 8, URL: "https://github.com/example/loops/pull/8"}}, SubIssues: &issueSubIssuesSummary{2, 5}}
	for _, tc := range []struct {
		name      string
		project   bool
		preferred []string
		want      string
		labels    []string
	}{
		{"first label", false, nil, "area:dev", []string{"watching", "go"}},
		{"preferred", false, []string{"absent", "watching"}, "watching", []string{"area:dev", "go"}},
		{"project", true, nil, "No status", issue.Labels},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := projectData{Repository: "example/loops", Open: []projectIssue{issue}}
			if tc.project {
				data.Project = &projectReference{ID: "p", StatusOptions: []projectStatusOption{{"todo", "Todo"}, {"done", "Done"}}}
			}
			d := buildProjectDocument(data, projectOptions{groupLabels: tc.preferred, closedDays: 7})
			g := d.Groups[len(d.Groups)-1]
			if g.Name != tc.want || g.Count != 1 || !reflect.DeepEqual(g.Issues[0].Labels, tc.labels) || !reflect.DeepEqual(issue.Labels, []string{"area:dev", "watching", "go"}) {
				t.Fatalf("group/labels/source mutation: %+v %+v", g, issue)
			}
			var out bytes.Buffer
			if err := writeProject(&out, d, projectOptions{closedDays: 7}, now); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"#7", "2d", "1h", "#8", "2/5", "Recently closed (last 7 days)", "Check controls  [31m"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in %q", want, out.String())
				}
			}
			if strings.Contains(out.String(), "\x1b") {
				t.Fatal("terminal escape leaked")
			}
			out.Reset()
			if err := writeProject(&out, d, projectOptions{json: true}, now); err != nil {
				t.Fatal(err)
			}
			var roundTrip projectDocument
			if err := json.Unmarshal(out.Bytes(), &roundTrip); err != nil || roundTrip.SchemaVersion != 1 || roundTrip.Repository != data.Repository || len(roundTrip.Groups) == 0 {
				t.Fatalf("JSON contract: %s %v", out.String(), err)
			}
			for _, field := range []string{`"recentlyClosed":[]`, `"hints":[]`, `"pullRequests":[{"number":8,"url":`, `"subIssues":{"completed":2,"total":5}`} {
				if !strings.Contains(out.String(), field) {
					t.Fatalf("JSON %s missing %s", out.String(), field)
				}
			}
		})
	}
	data := projectData{Open: []projectIssue{{Number: 2}, {Number: 1, Labels: []string{"z"}}, {Number: 3, Labels: []string{"a"}}}}
	d := buildProjectDocument(data, projectOptions{})
	if got := []string{d.Groups[0].Name, d.Groups[1].Name, d.Groups[2].Name}; !reflect.DeepEqual(got, []string{"No label", "a", "z"}) {
		t.Fatalf("label order %v", got)
	}
}

func TestProjectJSONArguments(t *testing.T) {
	oldFetch, oldTTY := fetchProjectDataFunc, terminalOutputFunc
	defer func() { fetchProjectDataFunc = oldFetch; terminalOutputFunc = oldTTY }()
	terminalOutputFunc = func(io.Writer) bool { return true }
	calls := 0
	fetchProjectDataFunc = func(projectOptions, time.Time) (projectData, error) {
		calls++
		return projectData{Repository: "example/loops"}, nil
	}
	for _, args := range [][]string{
		{"project", "--unknown", "--json"}, {"p", "--json", "--closed-days=-1"}, {"p", "extra", "--json"}, {"--clear=bad", "p", "--json"}, {"p", "--clear=bad", "--json"}, {"p", "--project=broken", "--json"}, {"p", "--group-by=bad", "--json"}, {"p", "--closed-days=bad", "--json"}, {"p", "--closed-days=36501", "--json"}, {"p", "--json", "--json=false", "--json=true", "--unknown"},
	} {
		var out, stderr bytes.Buffer
		_, err := run(args, &out, &stderr)
		var doc struct {
			SchemaVersion int
			Error         string
		}
		if err == nil || json.Unmarshal(out.Bytes(), &doc) != nil || doc.SchemaVersion != 1 || doc.Error == "" || calls != 0 || strings.Contains(out.String(), "\x1b") {
			t.Fatalf("args %v: out=%q stderr=%q err=%v calls=%d", args, out.String(), stderr.String(), err, calls)
		}
	}
	for _, args := range [][]string{{"p", "--json", "--clear"}, {"project", "--json"}, {"p", "--group-label", "--clear", "--json"}, {"p", "--group-label", "--", "--json"}} {
		var out, stderr bytes.Buffer
		ch, err := run(args, &out, &stderr)
		if err != nil || ch != nil || stderr.Len() != 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("success args=%v out=%s stderr=%s err=%v", args, out.String(), stderr.String(), err)
		}
	}
	for _, args := range [][]string{{"p", "--", "--json"}, {"p", "--group-label", "--json", "--unknown"}, {"p", "--json", "--json=false", "--unknown"}} {
		var out bytes.Buffer
		_, err := run(args, &out, io.Discard)
		if err == nil || out.Len() != 0 {
			t.Fatalf("nonJSON error %v => %q %v", args, out.String(), err)
		}
	}
	for _, args := range [][]string{{"p", "--help"}, {"project", "-h", "--json"}} {
		var out bytes.Buffer
		_, err := run(args, &out, io.Discard)
		if err != nil || !strings.Contains(out.String(), "--closed-days") {
			t.Fatalf("help %q %v", out.String(), err)
		}
	}
	for _, failure := range []error{errors.New("fetch failed"), context.Canceled} {
		fetchProjectDataFunc = func(projectOptions, time.Time) (projectData, error) { return projectData{}, failure }
		var out bytes.Buffer
		if err := runProject([]string{"--json"}, &out, io.Discard); !errors.Is(err, failure) || !json.Valid(out.Bytes()) {
			t.Fatalf("error %s %v", out.String(), err)
		}
		if err := runProject(nil, io.Discard, io.Discard); !errors.Is(err, failure) {
			t.Fatalf("terminal error %v", err)
		}
	}
	for _, jsonMode := range []bool{true, false} {
		if err := writeProject(failProjectWriter{}, projectDocument{}, projectOptions{json: jsonMode}, time.Now()); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("output failure lost %v", err)
		}
	}
	if err := writeProjectError(failProjectWriter{}, errors.New("cause")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

type failProjectWriter struct{}

func (failProjectWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestProjectClosedBoundary(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	before, after := cutoff.Add(-time.Nanosecond), cutoff.Add(time.Nanosecond)
	pr := json.RawMessage(`{}`)
	entries := []projectRESTIssue{{Number: 1, ClosedAt: &cutoff}, {Number: 2, ClosedAt: &before}, {Number: 3, ClosedAt: &after}, {Number: 4}, {Number: 5, ClosedAt: &after, PullRequest: &pr}}
	got := filterProjectIssues(entries, "closed", cutoff)
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 3 {
		t.Fatalf("closed boundary: %+v", got)
	}
	if got := filterProjectIssues(entries, "open", cutoff); len(got) != 4 {
		t.Fatalf("PR included or open filtered: %v", got)
	}
	for _, tc := range []struct {
		value string
		bad   bool
	}{{"owner/1", false}, {"owner/0", true}, {"owner/-1", true}, {"/1", true}, {"owner/a", true}, {"owner/1/2", true}, {"owner name/2", true}} {
		_, _, err := parseProjectSelector(tc.value)
		if (err != nil) != tc.bad {
			t.Fatalf("selector %s %v", tc.value, err)
		}
	}
}

func TestProjectNoStatusAndUnknownEnrichment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id, option string
		want       string
	}{
		{"matching status", "p", "todo", "Todo"}, {"unknown option", "p", "deleted", "No status"}, {"unset status", "p", "", "No status"}, {"other project", "other", "todo", "No status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := projectMembership{Project: projectReference{ID: tc.id}}
			m.Status.OptionID = tc.option
			data := projectData{Project: &projectReference{ID: "p", StatusOptions: []projectStatusOption{{"todo", "Todo"}}}, Memberships: map[int][]projectMembership{1: {m}}}
			group, label := projectIssueGroup(projectIssue{Number: 1}, data, projectOptions{})
			if group.Name != tc.want || label != "" {
				t.Fatalf("group %+v label%s", group, label)
			}
		})
	}
	var out bytes.Buffer
	renderProjectSection(&out, "Empty", []projectIssue{{Number: 1}}, time.Now())
	if strings.Count(out.String(), "?") != 2 {
		t.Fatalf("unknown data disguised as empty: %s", out.String())
	}
}

func projectBatchFixture(t *testing.T, query, raw string) string {
	t.Helper()
	aliases := regexp.MustCompile(`issue(\d+): issue\(`).FindAllStringSubmatch(query, -1)
	if len(aliases) == 0 {
		return raw
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	repository := body["data"].(map[string]any)["repository"].(map[string]any)
	node := repository["issue"]
	delete(repository, "issue")
	for _, alias := range aliases {
		repository["issue"+alias[1]] = node
	}
	result, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(result)
}

func TestProjectDiagnosticWriteFailure(t *testing.T) {
	cause := errors.New("primary fetch failure")
	err := writeProjectError(failProjectWriter{}, cause)
	if !errors.Is(err, cause) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost primary or output failure: %v", err)
	}
	if err := runProject([]string{"--help"}, failProjectWriter{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("help write error ignored: %v", err)
	}
}
