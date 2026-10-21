package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

type projectOptions struct {
	repo, project, groupBy string
	groupLabels            stringSliceFlag
	closedDays             int
	json                   bool
}

type projectReference struct {
	ID            string                `json:"id"`
	Owner         string                `json:"owner"`
	Number        int                   `json:"number"`
	Title         string                `json:"title"`
	URL           string                `json:"url"`
	StatusOptions []projectStatusOption `json:"statusOptions"`
}

type projectStatusOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type projectIssue struct {
	Number       int                    `json:"number"`
	Title        string                 `json:"title"`
	URL          string                 `json:"url"`
	CreatedAt    time.Time              `json:"createdAt"`
	UpdatedAt    time.Time              `json:"updatedAt"`
	ClosedAt     *time.Time             `json:"closedAt"`
	Labels       []string               `json:"labels"`
	PullRequests []linkedReference      `json:"pullRequests"`
	SubIssues    *issueSubIssuesSummary `json:"subIssues"`
}

type projectGroup struct {
	Name   string         `json:"name"`
	Count  int            `json:"count"`
	Issues []projectIssue `json:"issues"`
}

type projectDocument struct {
	SchemaVersion  int               `json:"schemaVersion"`
	Repository     string            `json:"repository"`
	Project        *projectReference `json:"project"`
	Grouping       string            `json:"grouping"`
	Groups         []projectGroup    `json:"groups"`
	RecentlyClosed []projectIssue    `json:"recentlyClosed"`
	ClosedDays     int               `json:"closedDays"`
	Hints          []string          `json:"hints"`
}

type projectMembership struct {
	Project projectReference `json:"project"`
	Status  struct {
		Name     string `json:"name"`
		OptionID string `json:"optionId"`
	} `json:"fieldValueByName"`
}

type projectData struct {
	Repository   string
	Open, Closed []projectIssue
	Project      *projectReference
	Memberships  map[int][]projectMembership
	Hints        []string
}

var fetchProjectDataFunc = fetchProjectData

const projectUsage = `Usage: gh x project [flags] (alias: p)

Show open issues grouped by GitHub Project Status, or by label.

Flags:
  -R, --repo OWNER/REPO    Repository (default: current checkout)
      --project OWNER/N   Select a Project v2 explicitly
      --group-by MODE     auto (default) or label
      --group-label NAME  Preferred fallback grouping label (repeatable, in priority order)
      --closed-days N     Show issues closed within N days (default 7; 0 hides section)
      --json              Write a separate schemaVersion 1 project document

Project access requires read:project (project also includes it).
Without project access, a single hint explains label fallback.
`

func projectFlags(o *projectOptions, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet("project", flag.ContinueOnError)
	f.SetOutput(out)
	f.Usage = func() { fmt.Fprint(out, projectUsage) }
	f.StringVar(&o.repo, "repo", "", "Repository")
	f.StringVar(&o.repo, "R", "", "Repository")
	f.StringVar(&o.project, "project", "", "Project owner/number")
	f.StringVar(&o.groupBy, "group-by", "auto", "auto or label")
	f.Var(&o.groupLabels, "group-label", "Preferred fallback grouping label")
	f.IntVar(&o.closedDays, "closed-days", 7, "Recently closed window")
	f.BoolVar(&o.json, "json", false, "Project JSON")
	return f
}

func projectJSONRequested(args []string) bool {
	_, terminal, _ := extractTerminalOptions(append([]string{"project"}, args...))
	return terminal.json
}

func parseProjectOptions(args []string, out io.Writer) (projectOptions, error) {
	var o projectOptions
	f := projectFlags(&o, out)
	err := f.Parse(args)
	o.json = projectJSONRequested(args)
	if errors.Is(err, flag.ErrHelp) {
		return o, errHelpDisplayed
	}
	if err != nil {
		return o, err
	}
	if f.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments: %s", strings.Join(f.Args(), " "))
	}
	if o.closedDays < 0 || o.closedDays > 36500 {
		return o, errors.New("closed-days must be between 0 and 36500")
	}
	if o.groupBy != "auto" && o.groupBy != "label" {
		return o, errors.New("group-by must be auto or label")
	}
	if o.project != "" {
		if _, _, err = parseProjectSelector(o.project); err != nil {
			return o, err
		}
	}
	return o, nil
}

func runProject(args []string, stdout, stderr io.Writer) error {
	o, err := parseProjectOptions(args, io.Discard)
	if errors.Is(err, errHelpDisplayed) {
		fmt.Fprint(stdout, projectUsage)
		return nil
	}
	if err == nil {
		var data projectData
		data, err = fetchProjectDataFunc(o, time.Now())
		if err == nil {
			return writeProject(stdout, buildProjectDocument(data, o), o, time.Now())
		}
	}
	if o.json {
		return writeProjectError(stdout, err)
	}
	return err
}

func writeProjectError(out io.Writer, err error) error {
	payload := struct {
		SchemaVersion int    `json:"schemaVersion"`
		Error         string `json:"error"`
	}{1, err.Error()}
	if writeErr := json.NewEncoder(out).Encode(payload); writeErr != nil {
		return writeErr
	}
	return err
}

func chooseLabel(labels []string, preferred []string) string {
	for _, name := range preferred {
		for _, label := range labels {
			if label == name {
				return label
			}
		}
	}
	if len(labels) > 0 {
		return labels[0]
	}
	return ""
}

func projectIssueGroup(issue projectIssue, data projectData, o projectOptions) (string, string) {
	if data.Project == nil {
		label := chooseLabel(issue.Labels, o.groupLabels)
		if label == "" {
			return "No label", ""
		}
		return label, label
	}
	for _, m := range data.Memberships[issue.Number] {
		if m.Project.ID == data.Project.ID {
			for _, option := range data.Project.StatusOptions {
				if m.Status.OptionID == option.ID {
					return option.Name, ""
				}
			}
		}
	}
	return "No status", ""
}

func buildProjectDocument(data projectData, o projectOptions) projectDocument {
	d := projectDocument{SchemaVersion: 1, Repository: data.Repository, Project: data.Project, Grouping: "label", Groups: []projectGroup{}, RecentlyClosed: append([]projectIssue{}, data.Closed...), ClosedDays: o.closedDays, Hints: append([]string{}, data.Hints...)}
	indexes := map[string]int{}
	addGroup := func(name string) {
		indexes[name] = len(d.Groups)
		d.Groups = append(d.Groups, projectGroup{Name: name, Issues: []projectIssue{}})
	}
	if data.Project != nil {
		d.Grouping = "project"
		for _, option := range data.Project.StatusOptions {
			addGroup(option.Name)
		}
	}
	for _, issue := range data.Open {
		name, groupLabel := projectIssueGroup(issue, data, o)
		if _, ok := indexes[name]; !ok {
			addGroup(name)
		}
		issue.Labels = withoutProjectGroupLabel(issue.Labels, groupLabel)
		index := indexes[name]
		d.Groups[index].Issues = append(d.Groups[index].Issues, issue)
		d.Groups[index].Count++
	}
	if data.Project == nil {
		sort.SliceStable(d.Groups, func(i, j int) bool { return d.Groups[i].Name < d.Groups[j].Name })
	}
	return d
}

func withoutProjectGroupLabel(labels []string, groupLabel string) []string {
	result := []string{}
	for _, label := range labels {
		if label != groupLabel {
			result = append(result, label)
		}
	}
	return result
}

func writeProject(out io.Writer, d projectDocument, o projectOptions, now time.Time) error {
	if o.json {
		return json.NewEncoder(out).Encode(d)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Repository %s\n", d.Repository)
	if d.Project != nil {
		fmt.Fprintf(&b, "Project %s · %s\n", projectCell(d.Project.Title), projectCell(d.Project.URL))
	}
	counts := make([]string, 0, len(d.Groups))
	for _, g := range d.Groups {
		counts = append(counts, fmt.Sprintf("%s %d", projectCell(g.Name), g.Count))
	}
	fmt.Fprintln(&b, strings.Join(counts, " · "))
	for _, hint := range d.Hints {
		fmt.Fprintln(&b, projectCell(hint))
	}
	for _, g := range d.Groups {
		renderProjectSection(&b, g.Name, g.Issues, now)
	}
	if o.closedDays > 0 {
		renderProjectSection(&b, fmt.Sprintf("Recently closed (last %d days)", o.closedDays), d.RecentlyClosed, now)
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func renderProjectSection(out io.Writer, name string, issues []projectIssue, now time.Time) {
	fmt.Fprintf(out, "\n%s (%d)\n", projectCell(name), len(issues))
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "Issue\tTitle\tAge\tUpdated\tLabels\tPRs\tSub")
	for _, issue := range issues {
		prs := []string{}
		if issue.PullRequests == nil {
			prs = append(prs, "?")
		}
		for _, pr := range issue.PullRequests {
			prs = append(prs, fmt.Sprintf("#%d", pr.Number))
		}
		progress := "?"
		if issue.SubIssues != nil {
			progress = fmt.Sprintf("%d/%d", issue.SubIssues.Completed, issue.SubIssues.Total)
		}
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\t%s\n", issue.Number, projectCell(issue.Title), formatRelativeTime(issue.CreatedAt, now), formatRelativeTime(issue.UpdatedAt, now), projectCell(strings.Join(issue.Labels, ", ")), strings.Join(prs, ", "), progress)
	}
	_ = w.Flush()
}

func projectCell(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, value)
}

func isProjectJSONCommand(args []string, enabled bool) bool {
	return enabled && len(args) > 0 && (args[0] == "project" || args[0] == "p")
}

func machineJSONCommand(args []string, enabled bool) bool {
	return isStatusJSONCommand(args, enabled) || isProjectJSONCommand(args, enabled)
}

func commandTerminalError(args []string, terminal terminalOptions, out io.Writer, err error) error {
	if isProjectJSONCommand(args, terminal.json) {
		return writeProjectError(out, err)
	}
	return statusTerminalError(args, terminal, out, err)
}
