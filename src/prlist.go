package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	jsonFields                 = "number,title,author,state,isDraft,reviewDecision,statusCheckRollup,updatedAt,mergedAt,headRefName,baseRefName,url,latestReviews,mergeable"
	recentMergedCandidateFloor = 25
	recentMergedCandidateLimit = 1000
)

// executeListFunc is swapped in tests to avoid real API calls.
var executeListFunc = executeList

type listOptions struct {
	repo           string
	limit          int
	state          string
	author         string
	assignee       string
	app            string
	base           string
	head           string
	search         string
	draftOnly      bool
	web            bool
	json           bool
	recentlyMerged bool
	labels         stringSliceFlag
	requiredCache  map[string]requiredCheckResult
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type pullRequest struct {
	Number            int         `json:"number"`
	Title             string      `json:"title"`
	State             string      `json:"state"`
	IsDraft           bool        `json:"isDraft"`
	ReviewDecision    string      `json:"reviewDecision"`
	StatusCheckRollup []checkItem `json:"statusCheckRollup"`
	UpdatedAt         time.Time   `json:"updatedAt"`
	MergedAt          time.Time   `json:"mergedAt"`
	HeadRefName       string      `json:"headRefName"`
	BaseRefName       string      `json:"baseRefName"`
	URL               string      `json:"url"`
	Author            *author     `json:"author"`
	LatestReviews     []review    `json:"latestReviews"`
	Mergeable         string      `json:"mergeable"`
}

type author struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

type review struct {
	State  string  `json:"state"`
	Author *author `json:"author"`
}

// checkItem represents a single entry in the statusCheckRollup array.
// CheckRun items use Status+Conclusion; StatusContext items use State.
type checkItem struct {
	Typename     string    `json:"__typename"`
	Name         string    `json:"name"`    // CheckRun name
	Context      string    `json:"context"` // StatusContext context
	WorkflowName string    `json:"workflowName"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	State        string    `json:"state"`
	StartedAt    time.Time `json:"startedAt"`
	CompletedAt  time.Time `json:"completedAt"`
}

type displayApprover struct {
	Login      string    `json:"login"`
	ApprovedAt time.Time `json:"approvedAt"`
	Age        string    `json:"age"`
}

type displayPullRequest struct {
	Number              int               `json:"number"`
	Issues              string            `json:"issues,omitempty"`
	Title               string            `json:"title"`
	Author              string            `json:"author"`
	State               string            `json:"state"`
	Review              string            `json:"review"`
	Approvals           int               `json:"approvals"`
	Approvers           []displayApprover `json:"approvers,omitempty"`
	ApprovalsIncomplete bool              `json:"approvalsIncomplete,omitempty"`
	Checks              string            `json:"checks"`
	Comments            string            `json:"comments"`
	AIReview            string            `json:"aiReview"`
	AIClean             *bool             `json:"aiClean,omitempty"`
	Branch              string            `json:"branch,omitempty"`
	Updated             string            `json:"updated"`
	URL                 string            `json:"url"`
	Repo                string            `json:"repo,omitempty"`

	checksDowngraded bool // unexported; required-check rules downgraded a pass
	issueRefs        []linkedReference
	updatedAt        time.Time // unexported; used for sorting
	mergedAt         time.Time // unexported; used for recent-merge sorting
}

type pullRequestListResult struct {
	Entries                []pullRequest
	Rendered               []displayPullRequest
	SupplementalFailed     bool
	SupplementalErr        error
	RequiredChecksErr      error
	RequiredChecksFailed   bool
	FailedRequiredCheckPRs map[int]bool
}

func defaultListOptions() listOptions {
	return listOptions{
		limit: 30,
		state: "open",
	}
}

func executeList(options listOptions, stdout io.Writer, stderr io.Writer) error {
	result, err := fetchPullRequestList(options, time.Now().UTC())
	if err != nil {
		return err
	}
	if options.web {
		return nil
	}
	if err := renderListOutput(stdout, options, result.Rendered); err != nil {
		return err
	}
	if err := writeSupplementalNotice(stdout, stderr, options.json, result.SupplementalErr); err != nil {
		return err
	}
	return writeRequiredChecksNotice(stdout, stderr, options.json, result.RequiredChecksErr)
}

func fetchPullRequestList(options listOptions, now time.Time) (pullRequestListResult, error) {
	pullRequests, supplemental, repoOwner, repoName, err := acquirePullRequestList(options)
	if err != nil {
		return pullRequestListResult{}, err
	}
	if options.web {
		return pullRequestListResult{}, nil
	}
	requiredByBranch, failedRequiredBranches := fetchRequiredChecksCached(repoOwner, repoName, pullRequests, options.requiredCache)
	failedRequiredPRs := make(map[int]bool)
	for _, pr := range pullRequests {
		if failedRequiredBranches[pr.BaseRefName] != nil {
			failedRequiredPRs[pr.Number] = true
		}
	}
	requiredChecksFailed := len(failedRequiredBranches) > 0
	rendered := enrichPullRequests(pullRequests, supplemental, requiredByBranch, failedRequiredBranches, now)
	return pullRequestListResult{
		Entries:                pullRequests,
		Rendered:               rendered,
		SupplementalFailed:     len(supplemental.Unavailable) > 0,
		SupplementalErr:        supplemental.Err,
		RequiredChecksErr:      requiredChecksError(failedRequiredBranches),
		RequiredChecksFailed:   requiredChecksFailed,
		FailedRequiredCheckPRs: failedRequiredPRs,
	}, nil
}

func fetchPullRequests(options listOptions) ([]pullRequest, error) {
	if options.recentlyMerged {
		return fetchRecentlyMergedPullRequests(options)
	}
	return fetchRawPullRequests(options)
}

func fetchRawPullRequests(options listOptions) ([]pullRequest, error) {
	arguments := buildListArgs(options)
	commandOutput, commandError, err := ghExecFunc(arguments...)
	if err != nil {
		return nil, wrapExecError(err, commandError.String())
	}
	if options.web {
		return nil, nil
	}
	var pullRequests []pullRequest
	if err := json.Unmarshal(commandOutput.Bytes(), &pullRequests); err != nil {
		return nil, fmt.Errorf("decode gh pr list output: %w", err)
	}
	return pullRequests, nil
}

func fetchRecentlyMergedPullRequests(options listOptions) ([]pullRequest, error) {
	return fetchMergedRepositoryPullRequests(options)
}

// requiredChecksError turns failed required-check rules lookups into the
// diagnostic the Checks downgrade needs, preserving each branch's failure
// reason so a pass downgraded to pending always says why.
func requiredChecksError(failedBranches map[string]error) error {
	if len(failedBranches) == 0 {
		return nil
	}
	branches := make([]string, 0, len(failedBranches))
	for branch := range failedBranches {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	parts := make([]string, 0, len(branches))
	for _, branch := range branches {
		parts = append(parts, fmt.Sprintf("base %s: %s", branch, boundedSingleLine(failedBranches[branch].Error(), 80)))
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func wrapExecError(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		return fmt.Errorf("%w: %s", err, stderr)
	}
	return err
}

// runView handles "gh x pr view <number>" and "gh x pr <number>".
// parseViewArgs extracts the PR number and optional repo from the view command arguments.
func parseViewArgs(args []string) (number, repo string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isViewRepoFlag(arg) && i+1 < len(args) {
			i++
			repo = args[i]
		} else if !looksLikeFlag(arg) {
			if number != "" {
				return "", "", fmt.Errorf("usage: gh x pr view <number> [--repo OWNER/REPO]")
			}
			number = arg
		}
	}
	if number == "" {
		return "", "", fmt.Errorf("usage: gh x pr view <number> [--repo OWNER/REPO]")
	}
	return number, repo, nil
}

func runView(args []string, stdout io.Writer, stderr io.Writer) error {
	number, repo, err := parseViewArgs(args)
	if err != nil {
		return err
	}

	// Fetch single PR via gh pr view --json
	ghArgs := []string{"pr", "view", number, "--json", jsonFields}
	if repo != "" {
		ghArgs = append(ghArgs, "--repo", repo)
	}
	commandOutput, commandError, err := ghExecFunc(ghArgs...)
	if err != nil {
		return wrapExecError(err, commandError.String())
	}

	var pr pullRequest
	if err := json.Unmarshal(commandOutput.Bytes(), &pr); err != nil {
		return fmt.Errorf("decode gh pr view output: %w", err)
	}

	prs := []pullRequest{pr}
	supplemental, repoOwner, repoName := fetchSupplementalData(repo, prs)
	requiredByBranch, failedRequiredBranches := fetchRequiredChecks(repoOwner, repoName, prs)
	rendered := enrichPullRequests(prs, supplemental, requiredByBranch, failedRequiredBranches, time.Now().UTC())

	// Render as a single-row table with no limit footer
	opts := defaultListOptions()
	opts.repo = repo
	opts.limit = 0 // suppress "limit reached" footer
	if err := renderTable(stdout, opts, rendered); err != nil {
		return err
	}
	if err := writeSupplementalNotice(stdout, stderr, false, supplemental.Err); err != nil {
		return err
	}
	return writeRequiredChecksNotice(stdout, stderr, false, requiredChecksError(failedRequiredBranches))
}

// appendNonEmpty appends a flag and its value only when value is non-empty.
func appendNonEmpty(args []string, flag, value string) []string {
	if value == "" {
		return args
	}
	return append(args, flag, value)
}

func buildListArgs(options listOptions) []string {
	arguments := []string{"pr", "list"}

	if options.web {
		arguments = append(arguments, "--web")
	} else {
		arguments = append(arguments, "--json", jsonFields)
	}

	arguments = appendNonEmpty(arguments, "--repo", options.repo)
	arguments = append(arguments, "--limit", fmt.Sprintf("%d", options.limit))
	arguments = append(arguments, "--state", options.state)
	arguments = appendNonEmpty(arguments, "--author", options.author)
	arguments = appendNonEmpty(arguments, "--assignee", options.assignee)
	arguments = appendNonEmpty(arguments, "--app", options.app)
	arguments = appendNonEmpty(arguments, "--base", options.base)
	arguments = appendNonEmpty(arguments, "--head", options.head)
	arguments = appendNonEmpty(arguments, "--search", options.search)

	if options.draftOnly {
		arguments = append(arguments, "--draft")
	}

	for _, label := range options.labels {
		arguments = append(arguments, "--label", label)
	}

	return arguments
}

func acquirePullRequestList(options listOptions) ([]pullRequest, prSupplementalData, string, string, error) {
	if options.recentlyMerged && !options.web {
		acquisition, err := fetchMergedAcquisition(options, true)
		return acquisition.rows, acquisition.supplemental, acquisition.owner, acquisition.name, err
	}
	rows, err := fetchPullRequests(options)
	if err != nil || options.web {
		return rows, prSupplementalData{}, "", "", err
	}
	supplemental, owner, name := fetchSupplementalData(options.repo, rows)
	return rows, supplemental, owner, name, nil
}
