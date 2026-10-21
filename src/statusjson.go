package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// These DTOs are the public status contract, independent of cache versions and
// internal display structs. Additive fields are compatible with schema version 1.
type statusJSONDocument struct {
	SchemaVersion       int                    `json:"schemaVersion"`
	GeneratedAt         time.Time              `json:"generatedAt"`
	Repository          statusJSONRepository   `json:"repository"`
	Health              statusJSONHealth       `json:"health"`
	Issues              []statusJSONIssue      `json:"issues"`
	PullRequests        statusJSONPullRequests `json:"pullRequests"`
	WorkflowRuns        []statusJSONRun        `json:"workflowRuns"`
	WorkflowRunsPerfect bool                   `json:"workflowRunsPerfect"`
	Sections            []statusJSONSection    `json:"sections"`
}

type statusJSONRepository struct {
	NameWithOwner string `json:"nameWithOwner"`
	RootPath      string `json:"rootPath"`
	DefaultBranch string `json:"defaultBranch"`
}

type statusJSONVerdict struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Clean  bool   `json:"clean"`
}

type statusJSONChanges struct {
	Clean      bool `json:"clean"`
	Staged     int  `json:"staged"`
	Modified   int  `json:"modified"`
	Deleted    int  `json:"deleted"`
	Renamed    int  `json:"renamed"`
	Untracked  int  `json:"untracked"`
	Conflicted int  `json:"conflicted"`
}

type statusJSONBranch struct {
	statusJSONVerdict
	Name       string            `json:"name"`
	CheckedOut bool              `json:"checkedOut"`
	Upstream   string            `json:"upstream"`
	Synced     bool              `json:"synced"`
	Ahead      int               `json:"ahead"`
	Behind     int               `json:"behind"`
	Changes    statusJSONChanges `json:"changes"`
}

type statusJSONHealth struct {
	DefaultBranch statusJSONBranch `json:"defaultBranch"`
	CurrentBranch statusJSONBranch `json:"currentBranch"`
	Branches      struct {
		statusJSONVerdict
		Local    int `json:"local"`
		Remote   int `json:"remote"`
		Dangling int `json:"dangling"`
	} `json:"branches"`
	Worktrees struct {
		statusJSONVerdict
		Total             int                  `json:"total"`
		CleanupCandidates int                  `json:"cleanupCandidates"`
		Items             []statusJSONWorktree `json:"items"`
	} `json:"worktrees"`
	Stashes struct {
		statusJSONVerdict
		Count int `json:"count"`
	} `json:"stashes"`
}

type statusJSONWorktree struct {
	Path             string `json:"path"`
	Head             string `json:"head"`
	Branch           string `json:"branch"`
	Detached         bool   `json:"detached"`
	DetachedMerged   bool   `json:"detachedMerged"`
	Locked           bool   `json:"locked"`
	Prunable         bool   `json:"prunable"`
	PrunableReason   string `json:"prunableReason"`
	Primary          bool   `json:"primary"`
	Current          bool   `json:"current"`
	Exists           bool   `json:"exists"`
	Clean            bool   `json:"clean"`
	CleanKnown       bool   `json:"cleanKnown"`
	CleanupCandidate bool   `json:"cleanupCandidate"`
	CleanupReason    string `json:"cleanupReason"`
}

type statusJSONPullRequests struct {
	Open        []statusJSONPR `json:"open"`
	Merged      []statusJSONPR `json:"merged"`
	MergedLimit int            `json:"mergedLimit"`
}

type statusJSONIssue struct {
	Number       int        `json:"number"`
	PullRequests string     `json:"pullRequests"`
	Parent       string     `json:"parent"`
	SubIssues    string     `json:"subIssues"`
	Title        string     `json:"title"`
	Author       string     `json:"author"`
	State        string     `json:"state"`
	Labels       string     `json:"labels"`
	Assignees    string     `json:"assignees"`
	Updated      string     `json:"updated"`
	UpdatedAt    *time.Time `json:"updatedAt"`
	URL          string     `json:"url"`
}

type statusJSONPR struct {
	Number              int                  `json:"number"`
	Issues              string               `json:"issues"`
	Title               string               `json:"title"`
	Author              string               `json:"author"`
	State               string               `json:"state"`
	Review              string               `json:"review"`
	Approvals           int                  `json:"approvals"`
	Approvers           []statusJSONApprover `json:"approvers"`
	ApprovalsIncomplete bool                 `json:"approvalsIncomplete"`
	Checks              string               `json:"checks"`
	Comments            string               `json:"comments"`
	AIReview            string               `json:"aiReview"`
	AIClean             *bool                `json:"aiClean"`
	Branch              string               `json:"branch"`
	Updated             string               `json:"updated"`
	UpdatedAt           *time.Time           `json:"updatedAt"`
	MergedAt            *time.Time           `json:"mergedAt"`
	URL                 string               `json:"url"`
}

type statusJSONApprover struct {
	Login      string     `json:"login"`
	ApprovedAt *time.Time `json:"approvedAt"`
	Age        string     `json:"age"`
}

type statusJSONRun struct {
	ID        string     `json:"id"`
	URL       string     `json:"url"`
	Status    string     `json:"status"`
	RawStatus string     `json:"rawStatus"`
	Title     string     `json:"title"`
	Workflow  string     `json:"workflow"`
	Branch    string     `json:"branch"`
	Event     string     `json:"event"`
	Elapsed   string     `json:"elapsed"`
	Age       string     `json:"age"`
	CreatedAt *time.Time `json:"createdAt"`
	StartedAt *time.Time `json:"startedAt"`
	UpdatedAt *time.Time `json:"updatedAt"`
}

type statusJSONSection struct {
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason"`
	Cached          bool       `json:"cached"`
	FetchedAt       *time.Time `json:"fetchedAt"`
	CacheAgeSeconds float64    `json:"cacheAgeSeconds"`
}

func isStatusJSONCommand(args []string, jsonOutput bool) bool {
	return jsonOutput && len(args) > 0 && (args[0] == "status" || args[0] == "s")
}

// Reuse the global scan even when flag.Parse stops before reaching --json.
// That scan knows flag values, -- boundaries and the last JSON boolean value.
func statusJSONRequested(args []string) bool {
	_, terminal, _ := extractTerminalOptions(append([]string{"status"}, args...))
	return terminal.json
}

func statusArgsError(options statusOptions, stdout io.Writer, err error) error {
	if errors.Is(err, errHelpDisplayed) {
		return nil
	}
	if options.json {
		return writeStatusJSONError(stdout, err)
	}
	return err
}

func runStatusJSON(options statusOptions, stdout io.Writer) error {
	dashboard, err := fetchStatusDashboardFunc(false, options)
	if err != nil {
		return writeStatusJSONError(stdout, err)
	}
	populateStatusJSONWorktreeFacts(&dashboard)
	if err := commandContext.Err(); err != nil {
		return writeStatusJSONError(stdout, err)
	}
	return json.NewEncoder(stdout).Encode(buildStatusJSON(dashboard, options, statusNowFunc()))
}

func populateStatusJSONWorktreeFacts(dashboard *statusDashboard) {
	for i := range dashboard.Worktrees {
		worktree := &dashboard.Worktrees[i]
		if worktree.CleanKnown {
			continue
		}
		if summary, known := statusJSONWorktreeSummary(*dashboard, *worktree); known {
			worktree.Exists = statusPathExistsFunc(worktree.Path)
			worktree.CleanKnown = worktree.Exists
			worktree.Clean = worktree.Exists && !hasWorkingChanges(summary)
		} else {
			inspectStatusWorktreeCleanliness(worktree)
		}
	}
}

func statusJSONWorktreeSummary(dashboard statusDashboard, worktree statusWorktree) (statusSummary, bool) {
	if worktree.Current {
		return dashboard.CurrentStatus, true
	}
	ref := dashboard.Branches.Local[dashboard.DefaultBranch]
	if dashboard.DefaultCheckedOut && dashboard.DefaultStatusErr == nil &&
		worktree.Branch == dashboard.DefaultBranch && sameStatusPath(worktree.Path, ref.WorktreePath) {
		return dashboard.DefaultStatus, true
	}
	return statusSummary{}, false
}

func writeStatusJSONError(stdout io.Writer, cause error) error {
	document := struct {
		SchemaVersion int               `json:"schemaVersion"`
		GeneratedAt   time.Time         `json:"generatedAt"`
		Error         map[string]string `json:"error"`
	}{1, statusNowFunc(), map[string]string{"code": "status_failed", "message": cause.Error()}}
	if err := json.NewEncoder(stdout).Encode(document); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func buildStatusJSON(dashboard statusDashboard, options statusOptions, now time.Time) statusJSONDocument {
	return statusJSONDocument{
		SchemaVersion: 1, GeneratedAt: now,
		Repository: statusJSONRepository{dashboard.Repository, dashboard.localRoot, dashboard.DefaultBranch},
		Health:     buildStatusJSONHealth(dashboard),
		Issues:     buildStatusJSONIssues(dashboard.Issues),
		PullRequests: statusJSONPullRequests{
			Open: buildStatusJSONPRs(dashboard.PullRequests), Merged: buildStatusJSONPRs(dashboard.MergedPullRequests), MergedLimit: options.mergedLimit,
		},
		WorkflowRuns:        buildStatusJSONRuns(dashboard.WorkflowRuns),
		WorkflowRunsPerfect: dashboard.WorkflowRunsPerfect,
		Sections:            buildStatusJSONSections(dashboard, now),
	}
}

func statusDefaultBranchClean(dashboard statusDashboard, ref statusBranchRef) bool {
	return dashboard.DefaultBranch != "" && dashboard.DefaultCheckedOut && dashboard.DefaultStatusErr == nil &&
		statusBranchSeverity(dashboard.DefaultStatus) == statusHealthy && !strings.Contains(ref.Track, "[gone]")
}

func statusBranchesClean(inventory statusBranchInventory) bool {
	return inventory.LocalCount == 1 && inventory.DanglingCount == 0 && inventory.RemoteCount == 1
}

func statusWorktreesClean(worktrees []statusWorktree) bool {
	return len(worktrees) == 1 && statusCleanupCandidateCount(worktrees) == 0
}

func statusJSONVerdictFor(clean bool, err error) statusJSONVerdict {
	verdict := statusJSONVerdict{Status: "available", Clean: clean}
	if err != nil {
		verdict.Status, verdict.Reason, verdict.Clean = "unavailable", err.Error(), false
	}
	return verdict
}

func buildStatusJSONChanges(summary statusSummary) statusJSONChanges {
	return statusJSONChanges{!hasWorkingChanges(summary), summary.Staged, summary.Modified, summary.Deleted, summary.Renamed, summary.Untracked, summary.Conflicted}
}

func buildStatusJSONBranch(summary statusSummary, checkedOut, clean bool, err error) statusJSONBranch {
	return statusJSONBranch{
		statusJSONVerdict: statusJSONVerdictFor(clean, err), Name: summary.Branch, CheckedOut: checkedOut,
		Upstream: summary.Upstream, Synced: summary.Upstream != "" && summary.Ahead == 0 && summary.Behind == 0,
		Ahead: summary.Ahead, Behind: summary.Behind, Changes: buildStatusJSONChanges(summary),
	}
}

func buildStatusJSONHealth(dashboard statusDashboard) statusJSONHealth {
	var health statusJSONHealth
	health.DefaultBranch = buildStatusJSONBranch(dashboard.DefaultStatus, dashboard.DefaultCheckedOut,
		statusDefaultBranchClean(dashboard, dashboard.Branches.Local[dashboard.DefaultBranch]), dashboard.DefaultStatusErr)
	health.DefaultBranch.Name = dashboard.DefaultBranch
	health.DefaultBranch.Synced = health.DefaultBranch.Synced && !strings.Contains(dashboard.Branches.Local[dashboard.DefaultBranch].Track, "[gone]")
	health.CurrentBranch = buildStatusJSONBranch(dashboard.CurrentStatus, true, statusBranchSeverity(dashboard.CurrentStatus) == statusHealthy, nil)
	health.CurrentBranch.Synced = health.CurrentBranch.Synced && !strings.Contains(dashboard.Branches.Local[dashboard.CurrentStatus.Branch].Track, "[gone]")
	health.Branches.statusJSONVerdict = statusJSONVerdictFor(statusBranchesClean(dashboard.Branches), nil)
	health.Branches.Local, health.Branches.Remote, health.Branches.Dangling = dashboard.Branches.LocalCount, dashboard.Branches.RemoteCount, dashboard.Branches.DanglingCount
	health.Worktrees.statusJSONVerdict = statusJSONVerdictFor(statusWorktreesClean(dashboard.Worktrees), nil)
	health.Worktrees.Total, health.Worktrees.CleanupCandidates = len(dashboard.Worktrees), statusCleanupCandidateCount(dashboard.Worktrees)
	health.Worktrees.Items = buildStatusJSONWorktrees(dashboard.Worktrees)
	health.Stashes.statusJSONVerdict = statusJSONVerdictFor(dashboard.Stashes == 0, dashboard.StashesErr)
	health.Stashes.Count = dashboard.Stashes
	return health
}

func buildStatusJSONWorktrees(worktrees []statusWorktree) []statusJSONWorktree {
	items := make([]statusJSONWorktree, 0, len(worktrees))
	for _, w := range worktrees {
		items = append(items, statusJSONWorktree(w))
	}
	return items
}

func buildStatusJSONIssues(issues []displayIssue) []statusJSONIssue {
	items := make([]statusJSONIssue, 0, len(issues))
	for _, issue := range issues {
		items = append(items, statusJSONIssue{
			Number: issue.Number, PullRequests: issue.PullRequests, Parent: issue.Parent, SubIssues: issue.SubIssues,
			Title: issue.Title, Author: issue.Author, State: issue.State, Labels: issue.Labels, Assignees: issue.Assignees,
			Updated: issue.Updated, UpdatedAt: statusJSONTime(issue.updatedAt), URL: issue.URL,
		})
	}
	return items
}

func buildStatusJSONPRs(prs []displayPullRequest) []statusJSONPR {
	items := make([]statusJSONPR, 0, len(prs))
	for _, pr := range prs {
		items = append(items, statusJSONPR{
			Number: pr.Number, Issues: pr.Issues, Title: pr.Title, Author: pr.Author, State: pr.State, Review: pr.Review,
			Approvals: pr.Approvals, Approvers: buildStatusJSONApprovers(pr.Approvers), ApprovalsIncomplete: pr.ApprovalsIncomplete,
			Checks: pr.Checks, Comments: pr.Comments, AIReview: pr.AIReview, AIClean: pr.AIClean, Branch: pr.Branch,
			Updated: pr.Updated, UpdatedAt: statusJSONTime(pr.updatedAt), MergedAt: statusJSONTime(pr.mergedAt), URL: pr.URL,
		})
	}
	return items
}

func buildStatusJSONApprovers(approvers []displayApprover) []statusJSONApprover {
	items := make([]statusJSONApprover, 0, len(approvers))
	for _, approver := range approvers {
		items = append(items, statusJSONApprover{approver.Login, statusJSONTime(approver.ApprovedAt), approver.Age})
	}
	return items
}

func buildStatusJSONRuns(runs []displayWorkflowRun) []statusJSONRun {
	items := make([]statusJSONRun, 0, len(runs))
	for _, run := range runs {
		items = append(items, statusJSONRun{
			ID: run.ID, URL: run.URL, Status: run.Status, RawStatus: run.rawStatus, Title: run.Title, Workflow: run.Workflow,
			Branch: run.Branch, Event: run.Event, Elapsed: run.Elapsed, Age: run.Age,
			CreatedAt: statusJSONTime(run.createdAt), StartedAt: statusJSONTime(run.startedAt), UpdatedAt: statusJSONTime(run.updatedAt),
		})
	}
	return items
}

func buildStatusJSONSections(dashboard statusDashboard, now time.Time) []statusJSONSection {
	names := []string{"issues", "pullRequests.open", "pullRequests.merged", "workflowRuns"}
	items := make([]statusJSONSection, 0, len(names))
	for i, sectionErr := range statusSectionErrors(dashboard) {
		verdict := statusJSONVerdictFor(false, sectionErr)
		section := statusJSONSection{Name: names[i], Status: verdict.Status, Reason: verdict.Reason, Cached: dashboard.remoteCached[i]}
		if len(dashboard.remoteSections) == 4 {
			section.FetchedAt = statusJSONTime(dashboard.remoteSections[i].FetchedAt)
		}
		if section.Cached && section.FetchedAt != nil {
			section.CacheAgeSeconds = max(0, now.Sub(*section.FetchedAt).Seconds())
		}
		if i == 2 && !dashboard.ShowMergedPullRequests {
			section.Status, section.Cached, section.FetchedAt, section.CacheAgeSeconds = "disabled", false, nil, 0
			section.Reason = ""
		}
		items = append(items, section)
	}
	return items
}

func statusJSONTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}
