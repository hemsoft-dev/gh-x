package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type projectPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type projectRESTIssue struct {
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	URL         string           `json:"html_url"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	ClosedAt    *time.Time       `json:"closed_at"`
	Labels      []issueLabel     `json:"labels"`
	PullRequest *json.RawMessage `json:"pull_request"`
}

func projectREST(ctx context.Context, host, path string, target any) error {
	out, stderr, err := execGHInContext(ctx, "api", "--hostname", host, path)
	if err != nil {
		return wrapExecError(err, stderr.String())
	}
	if err = json.Unmarshal(out.Bytes(), target); err != nil {
		return fmt.Errorf("parse project response: %w", err)
	}
	return nil
}

func fetchProjectIssues(ctx context.Context, host, repo, state string, days int, now time.Time) ([]projectIssue, error) {
	result := []projectIssue{}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	for page := 1; ; page++ {
		path := fmt.Sprintf("repos/%s/issues?state=%s&per_page=100&page=%d&sort=created&direction=asc", repo, state, page)
		if state == "closed" {
			path += "&since=" + url.QueryEscape(cutoff.UTC().Format(time.RFC3339))
		}
		var entries []projectRESTIssue
		if err := projectREST(ctx, host, path, &entries); err != nil {
			return nil, err
		}
		if entries == nil {
			return nil, errors.New("issue list response is null")
		}
		result = append(result, filterProjectIssues(entries, state, cutoff)...)

		if len(entries) < 100 {
			break
		}
	}
	return result, nil
}

func filterProjectIssues(entries []projectRESTIssue, state string, cutoff time.Time) []projectIssue {
	result := []projectIssue{}
	for _, entry := range entries {
		if entry.PullRequest != nil {
			continue
		}
		if state == "closed" && (entry.ClosedAt == nil || entry.ClosedAt.Before(cutoff)) {
			continue
		}
		issue := projectIssue{Number: entry.Number, Title: entry.Title, URL: entry.URL, CreatedAt: entry.CreatedAt.UTC(), UpdatedAt: entry.UpdatedAt.UTC(), ClosedAt: statusJSONTimePointer(entry.ClosedAt), Labels: []string{}, PullRequests: []linkedReference{}}
		for _, label := range entry.Labels {
			issue.Labels = append(issue.Labels, label.Name)
		}
		result = append(result, issue)
	}
	return result
}

func statusJSONTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	return statusJSONTime(*value)
}

func fetchProjectData(o projectOptions, now time.Time) (projectData, error) {
	ctx, cancel, err := projectOperationContext()
	if err != nil {
		return projectData{}, err
	}
	defer cancel()
	owner, name, err := resolveRepoContext(ctx, o.repo)
	if err != nil {
		return projectData{}, err
	}
	data := projectData{Repository: owner + "/" + name, Memberships: map[int][]projectMembership{}, Hints: []string{}}
	target := o.repo
	if target == "" {
		target = statusSessionRepository(data.Repository)
	}
	host := repositoryTargetHost(target)
	data.Open, err = fetchProjectIssues(ctx, host, data.Repository, "open", 0, now)
	if err != nil {
		return data, err
	}
	data.Closed = []projectIssue{}
	if o.closedDays > 0 {
		data.Closed, err = fetchProjectIssues(ctx, host, data.Repository, "closed", o.closedDays, now)
		if err != nil {
			return data, err
		}
		sort.SliceStable(data.Closed, func(i, j int) bool { return data.Closed[i].ClosedAt.After(*data.Closed[j].ClosedAt) })
	}
	enrichProjectIssues(ctx, owner, name, host, data.Open, &data)
	enrichProjectIssues(ctx, owner, name, host, data.Closed, &data)
	if ctx.Err() != nil {
		return data, ctx.Err()
	}
	if err = loadProjectGrouping(ctx, host, owner, name, o, &data); err != nil {
		return data, err
	}

	return data, nil
}

func loadProjectGrouping(ctx context.Context, host, owner, name string, o projectOptions, data *projectData) error {
	if o.groupBy == "label" {
		return nil
	}
	var err error
	data.Memberships, err = fetchProjectMemberships(ctx, owner, name, host, data.Open)
	if err == nil {
		data.Project, err = selectIssueProject(ctx, host, data.Memberships, o.project)
	}
	if err == nil {
		return nil
	}
	if !projectScopeError(err) {
		return err
	}
	data.Project = nil
	data.Hints = append(data.Hints, "Hint: project unavailable or missing read:project access; grouping by label. Check --project or run gh auth refresh -s read:project.")
	return nil
}

func enrichProjectIssues(ctx context.Context, owner, name, host string, issues []projectIssue, data *projectData) {
	numbers := make([]int, len(issues))
	for i, issue := range issues {
		numbers[i] = issue.Number
	}
	refs, missing, relErr := fetchProjectPullRequests(ctx, owner, name, host, numbers)
	summaries, unavailable, hierarchyErr := fetchIssueHierarchiesFunc(ctx, owner, name, host, numbers)
	for i := range issues {
		n := issues[i].Number
		if !missing[n] {
			issues[i].PullRequests = append([]linkedReference{}, refs[n]...)
		} else {
			issues[i].PullRequests = nil
		}
		if h, ok := summaries[n]; ok && !unavailable[n].SubIssues {
			summary := h.SubIssues
			issues[i].SubIssues = &summary
		}
	}
	if relErr != nil {
		data.Hints = append(data.Hints, "Linked PRs unavailable: "+relErr.Error())
	}
	if hierarchyErr != nil {
		data.Hints = append(data.Hints, "Sub-issue progress unavailable: "+hierarchyErr.Error())
	}
}

func projectScopeError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	value := strings.ToLower(err.Error())
	return errors.Is(err, errProjectUnavailable) || strings.Contains(value, "read:project") || strings.Contains(value, "insufficient_scopes") || strings.Contains(value, "resource not accessible by integration") || strings.Contains(value, "resource not accessible by personal access token")
}

var errProjectUnavailable = errors.New("project not found or not accessible")

type projectGraphQLError struct {
	graphQLError
	Type string `json:"type"`
}

func projectQuery(ctx context.Context, host, query string, target any) error {
	raw, fetchErr := fetchGraphQLContext(ctx, host, query)
	if len(raw) == 0 && fetchErr != nil {
		return fetchErr
	}
	var envelope struct {
		Data   json.RawMessage       `json:"data"`
		Errors []projectGraphQLError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return errors.Join(fetchErr, err)
	}
	if err := projectResponseError(envelope.Errors); err != nil {
		return errors.Join(fetchErr, err)
	}
	if fetchErr != nil {
		return fetchErr
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("project response has no data")
	}
	return json.Unmarshal(envelope.Data, target)
}

func projectResponseError(entries []projectGraphQLError) error {
	var result error
	for _, entry := range entries {
		err := errors.New(entry.Message)
		if entry.Type == "NOT_FOUND" && projectMissingPath(entry.Path) {
			err = errors.Join(err, errProjectUnavailable)
		}
		result = errors.Join(result, err)
	}
	return result
}

func projectMissingPath(path []json.RawMessage) bool {
	var field string
	return len(path) > 0 && json.Unmarshal(path[len(path)-1], &field) == nil && field == "projectV2"
}

type projectMembershipPage struct {
	Nodes    []json.RawMessage `json:"nodes"`
	PageInfo projectPageInfo   `json:"pageInfo"`
}

func fetchProjectMemberships(ctx context.Context, owner, name, host string, issues []projectIssue) (map[int][]projectMembership, error) {
	result := map[int][]projectMembership{}
	for start := 0; start < len(issues); start += relationshipBatchSize {
		batch := issues[start:min(start+relationshipBatchSize, len(issues))]
		pages, err := fetchProjectMembershipBatch(ctx, owner, name, host, batch)
		if err != nil {
			return nil, err
		}
		for _, issue := range batch {
			items, err := completeProjectMemberships(ctx, owner, name, host, issue.Number, pages[issue.Number])
			if err != nil {
				return nil, err
			}
			result[issue.Number] = items
		}
	}
	return result, nil
}

type projectMembershipNode struct {
	ProjectItems *projectMembershipPage `json:"projectItems"`
}

func fetchProjectMembershipBatch(ctx context.Context, owner, name, host string, issues []projectIssue) (map[int]projectMembershipPage, error) {
	fields := make([]string, 0, len(issues))
	for _, issue := range issues {
		fields = append(fields, fmt.Sprintf("issue%d: %s", issue.Number, projectMembershipField(issue.Number, "")))
	}
	query := fmt.Sprintf(`query { repository(owner:%q,name:%q) { %s } }`, owner, name, strings.Join(fields, " "))
	var raw struct {
		Repository map[string]*projectMembershipNode `json:"repository"`
	}
	if err := projectQuery(ctx, host, query, &raw); err != nil {
		return nil, err
	}
	result := map[int]projectMembershipPage{}
	for _, issue := range issues {
		node := raw.Repository[fmt.Sprintf("issue%d", issue.Number)]
		if !validProjectMembershipNode(node) {
			return nil, fmt.Errorf("project memberships for issue #%d unavailable", issue.Number)
		}
		result[issue.Number] = *node.ProjectItems
	}
	return result, nil
}

func validProjectMembershipNode(node *projectMembershipNode) bool {
	return node != nil && node.ProjectItems != nil && node.ProjectItems.Nodes != nil
}

func completeProjectMemberships(ctx context.Context, owner, name, host string, number int, page projectMembershipPage) ([]projectMembership, error) {
	result, err := decodeProjectMemberships(page.Nodes)
	if err != nil || !page.PageInfo.HasNextPage {
		return result, err
	}
	cursor := ""
	if err = advanceProjectCursor(&cursor, page.PageInfo); err != nil {
		return nil, err
	}
	tail, err := fetchProjectMembershipTail(ctx, owner, name, host, number, cursor)
	if err != nil {
		return nil, err
	}
	return append(result, tail...), nil
}

func decodeProjectMemberships(nodes []json.RawMessage) ([]projectMembership, error) {
	result := make([]projectMembership, 0, len(nodes))
	for _, node := range nodes {
		m, err := decodeProjectMembership(node)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}

func fetchProjectMembershipTail(ctx context.Context, owner, name, host string, number int, cursor string) ([]projectMembership, error) {
	result := []projectMembership{}
	for {
		page, err := fetchProjectMembershipPage(ctx, owner, name, host, number, cursor)
		if err != nil {
			return nil, err
		}
		items, err := decodeProjectMemberships(page.Nodes)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if !page.PageInfo.HasNextPage {
			return result, nil
		}
		if err = advanceProjectCursor(&cursor, page.PageInfo); err != nil {
			return nil, err
		}
	}
}

func projectMembershipField(number int, cursor string) string {
	return fmt.Sprintf(`issue(number:%d) { projectItems(first:100,includeArchived:false%s) { nodes { project { id number title url owner { ... on Organization { login } ... on User { login } } } fieldValueByName(name:"Status") { ... on ProjectV2ItemFieldSingleSelectValue { name optionId } } } pageInfo { hasNextPage endCursor } } }`, number, projectAfter(cursor))
}

func advanceProjectCursor(cursor *string, page projectPageInfo) error {
	if page.EndCursor == "" || page.EndCursor == *cursor {
		return errors.New("project pagination did not advance")
	}
	*cursor = page.EndCursor
	return nil
}

func fetchProjectMembershipPage(ctx context.Context, owner, name, host string, number int, cursor string) (projectMembershipPage, error) {
	q := fmt.Sprintf(`query { repository(owner:%q,name:%q) { %s } }`, owner, name, projectMembershipField(number, cursor))
	var raw struct {
		Repository struct {
			Issue *projectMembershipNode `json:"issue"`
		} `json:"repository"`
	}

	if err := projectQuery(ctx, host, q, &raw); err != nil {
		return projectMembershipPage{}, err
	}
	if !validProjectMembershipNode(raw.Repository.Issue) {
		return projectMembershipPage{}, fmt.Errorf("project memberships for issue #%d unavailable", number)
	}
	return *raw.Repository.Issue.ProjectItems, nil
}

func projectAfter(cursor string) string {
	if cursor == "" {
		return ""
	}
	return fmt.Sprintf(",after:%q", cursor)
}

func decodeProjectMembership(raw []byte) (projectMembership, error) {
	var wire struct {
		Project struct {
			ID    string `json:"id"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
			Number int    `json:"number"`
			Title  string `json:"title"`
			URL    string `json:"url"`
		} `json:"project"`
		Status struct {
			Name     string `json:"name"`
			OptionID string `json:"optionId"`
		} `json:"fieldValueByName"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return projectMembership{}, err
	}
	p := wire.Project
	if p.ID == "" || p.Owner.Login == "" || p.Number <= 0 {
		return projectMembership{}, errors.New("project membership metadata incomplete")
	}
	return projectMembership{Project: projectReference{ID: p.ID, Owner: p.Owner.Login, Number: p.Number, Title: p.Title, URL: p.URL}, Status: wire.Status}, nil
}

func parseProjectSelector(value string) (string, int, error) {
	owner, number, ok := strings.Cut(value, "/")
	n, err := strconv.Atoi(number)
	if !ok || owner == "" || strings.ContainsAny(owner, " /\t\n") || err != nil || n <= 0 {
		return "", 0, errors.New("project must be OWNER/NUMBER with a positive number")
	}
	return owner, n, nil
}

func selectIssueProject(ctx context.Context, host string, memberships map[int][]projectMembership, selector string) (*projectReference, error) {
	if selector != "" {
		return fetchSelectedProject(ctx, host, selector)
	}
	candidates := map[string]projectReference{}
	counts := map[string]int{}
	for _, items := range memberships {
		for _, m := range items {
			candidates[m.Project.ID] = m.Project
			counts[m.Project.ID]++
		}
	}
	ordered := make([]projectReference, 0, len(candidates))
	for _, p := range candidates {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if counts[a.ID] != counts[b.ID] {
			return counts[a.ID] > counts[b.ID]
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Number < b.Number
	})
	if len(ordered) == 0 {
		return nil, nil
	}
	return fetchSelectedProject(ctx, host, fmt.Sprintf("%s/%d", ordered[0].Owner, ordered[0].Number))
}

type projectFieldsPage struct {
	Nodes []struct {
		Name    string                `json:"name"`
		Options []projectStatusOption `json:"options"`
	} `json:"nodes"`
	PageInfo projectPageInfo `json:"pageInfo"`
}

type projectMetadata struct {
	ID     string             `json:"id"`
	Number int                `json:"number"`
	Title  string             `json:"title"`
	URL    string             `json:"url"`
	Fields *projectFieldsPage `json:"fields"`
}

func fetchSelectedProject(ctx context.Context, host, selector string) (*projectReference, error) {
	owner, number, err := parseProjectSelector(selector)
	if err != nil {
		return nil, err
	}
	result := projectReference{Owner: owner, StatusOptions: []projectStatusOption{}}
	cursor := ""
	for {
		p, err := fetchProjectFields(ctx, host, owner, number, cursor)
		if err != nil {
			return nil, err
		}
		result.ID, result.Number, result.Title, result.URL = p.ID, p.Number, p.Title, p.URL
		appendProjectStatusOptions(&result, *p.Fields)
		if !p.Fields.PageInfo.HasNextPage {
			break
		}
		if err = advanceProjectCursor(&cursor, p.Fields.PageInfo); err != nil {
			return nil, err
		}
	}
	if len(result.StatusOptions) == 0 {
		return nil, errors.New("selected project has no single-select Status options")
	}
	return &result, nil
}

func appendProjectStatusOptions(result *projectReference, page projectFieldsPage) {
	for _, f := range page.Nodes {
		if f.Name == "Status" {
			result.StatusOptions = append(result.StatusOptions, f.Options...)
		}
	}
}

func fetchProjectFields(ctx context.Context, host, owner string, number int, cursor string) (*projectMetadata, error) {
	fields := fmt.Sprintf(`projectV2(number:%d) { id number title url fields(first:100%s) { nodes { ... on ProjectV2SingleSelectField { name options { id name } } } pageInfo { hasNextPage endCursor } } }`, number, projectAfter(cursor))
	query := fmt.Sprintf(`query { repositoryOwner(login:%q) { ... on Organization { %s } ... on User { %s } } }`, owner, fields, fields)
	var response struct {
		RepositoryOwner *struct {
			ProjectV2 *projectMetadata `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	if err := projectQuery(ctx, host, query, &response); err != nil {
		return nil, err
	}
	if response.RepositoryOwner == nil {
		return nil, errors.New("project owner response unavailable")
	}
	if response.RepositoryOwner.ProjectV2 == nil {
		return nil, fmt.Errorf("project %s/%d: %w", owner, number, errProjectUnavailable)
	}
	p := response.RepositoryOwner.ProjectV2
	if p.ID == "" || p.Number != number || p.Fields == nil {
		return nil, errors.New("project metadata incomplete")
	}
	return p, nil
}

func projectOperationContext() (context.Context, context.CancelFunc, error) {
	timeout, err := configuredTimeout(githubCommandTimeoutEnv, defaultGitHubCommandTimeout)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(commandContext, timeout)
	return ctx, cancel, nil
}

// Project reports include all linked PRs, including closed and merged records.
// Status keeps its existing acquisition query and serialized behavior.
func fetchProjectPullRequests(ctx context.Context, owner, name, host string, numbers []int) (map[int][]linkedReference, map[int]bool, error) {
	result := map[int][]linkedReference{}
	missing := map[int]bool{}
	var firstErr error
	for start := 0; start < len(numbers); start += relationshipBatchSize {
		if err := markCanceledRelationships(ctx, numbers[start:], missing); err != nil {
			firstErr = retainFirstError(firstErr, err)
			break
		}
		batch, unavailable, err := fetchProjectPullRequestsBatch(ctx, owner, name, host, numbers[start:min(start+relationshipBatchSize, len(numbers))])
		for number, refs := range batch {
			result[number] = refs
		}
		for number := range unavailable {
			missing[number] = true
		}
		firstErr = retainFirstError(firstErr, err)
	}
	return result, missing, firstPartialFetchError(firstErr, missing, len(numbers), "issues")
}

func fetchProjectPullRequestsBatch(ctx context.Context, owner, name, host string, numbers []int) (map[int][]linkedReference, map[int]bool, error) {
	fields := make([]string, 0, len(numbers))
	for _, number := range numbers {
		fields = append(fields, fmt.Sprintf(`issue%d: issue(number: %d) { number closedByPullRequestsReferences(first: 100, includeClosedPrs:true) { totalCount nodes { number url } } }`, number, number))
	}
	query := fmt.Sprintf(`query { repository(owner:%q,name:%q) { %s } }`, owner, name, strings.Join(fields, " "))
	raw, err := fetchGraphQLContext(ctx, host, query)
	missing := unavailableIssueNumbers(numbers, nil)
	if raw == nil {
		return nil, missing, err
	}
	refs, failed, parseErr := parseIssueRelationships(raw)
	if parseErr != nil {
		return nil, missing, errors.Join(err, parseErr)
	}
	missing = unavailableIssueNumbers(numbers, refs)
	for number := range failed {
		missing[number] = true
	}
	return refs, missing, err
}
