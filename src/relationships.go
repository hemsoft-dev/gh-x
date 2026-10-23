package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const relationshipBatchSize = 30

type linkedReference struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Text   string `json:"-"`
}

func (r linkedReference) displayText() string {
	if r.Text != "" {
		return r.Text
	}
	return fmt.Sprintf("#%d", r.Number)
}

type linkedReferenceConnection struct {
	TotalCount *int              `json:"totalCount"`
	Nodes      []linkedReference `json:"nodes"`
}

func (c *linkedReferenceConnection) complete() bool {
	if c == nil || c.TotalCount == nil || *c.TotalCount > len(c.Nodes) {
		return false
	}
	// A null or malformed node cannot be attributed to a real reference and
	// would normalize into a discarded zero value, so the connection cannot
	// prove its contents.
	for _, node := range c.Nodes {
		if node.Number <= 0 {
			return false
		}
	}
	return true
}

func relationshipDisplay(refs []linkedReference, unavailable bool) (string, []linkedReference) {
	if unavailable {
		return "?", nil
	}

	normalized := normalizeLinkedReferences(refs)
	if len(normalized) == 0 {
		return "-", nil
	}

	parts := make([]string, len(normalized))
	for i, ref := range normalized {
		parts[i] = fmt.Sprintf("#%d", ref.Number)
	}
	return strings.Join(parts, ", "), normalized
}

func normalizeLinkedReferences(refs []linkedReference) []linkedReference {
	normalized := make([]linkedReference, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.Number <= 0 {
			continue
		}
		key := ref.URL
		if key == "" {
			key = fmt.Sprintf("#%d", ref.Number)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, ref)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Number != normalized[j].Number {
			return normalized[i].Number < normalized[j].Number
		}
		return normalized[i].URL < normalized[j].URL
	})
	return normalized
}

func (s tableStyler) relationshipCell(text string, refs []linkedReference) tableCell {
	if text == "" {
		text = "-"
	}
	style := func(value string) string {
		return s.styleRelationshipText(value, refs)
	}
	return tableCell{text: text, styled: style(text), styleFn: style}
}

func (s tableStyler) styleRelationshipText(text string, refs []linkedReference) string {
	if len(refs) == 0 {
		return s.dim(text).styled
	}

	var styled strings.Builder
	remaining := text
	for _, ref := range refs {
		token := ref.displayText()
		index := strings.Index(remaining, token)
		if index < 0 {
			break
		}
		styled.WriteString(s.dim(remaining[:index]).styled)
		styled.WriteString(s.dimLinkCell(token, ref.URL).styled)
		remaining = remaining[index+len(token):]
	}
	styled.WriteString(s.dim(remaining).styled)
	return styled.String()
}

func repositoryTargetHost(repo string) string {
	args := []string{"api", "graphql"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	return targetHost(args)
}

var ghExecContextFunc = execGHContext

func execGHInContext(ctx context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
	if ctx == nil {
		return ghExecFunc(args...)
	}
	return ghExecContextFunc(ctx, args...)
}

type ghExecutor func(args ...string) (bytes.Buffer, bytes.Buffer, error)

func fetchGraphQL(host, query string) ([]byte, error) {
	return fetchGraphQLWithExecutor(host, query, ghExecFunc)
}

func fetchGraphQLContext(ctx context.Context, host, query string) ([]byte, error) {
	return fetchGraphQLWithExecutor(host, query, func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return ghExecContextFunc(ctx, args...)
	})
}

func fetchGraphQLWithExecutor(host, query string, executor ghExecutor) ([]byte, error) {
	args := []string{"api"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	args = append(args, "graphql", "-f", fmt.Sprintf("query=%s", query))
	stdout, stderr, err := executor(args...)
	if err == nil {
		return stdout.Bytes(), nil
	}
	// gh exits nonzero even when the GraphQL response carries valid data
	// beside partial errors, so hand back the payload when one exists and
	// let callers fail closed for exactly the aliases it lacks.
	wrappedErr := ghGraphQLError(err, stderr.String())
	var quota *quotaError
	if errors.As(err, &quota) {
		wrappedErr = quota
	}
	if hasGraphQLDataEnvelope(stdout.Bytes()) {
		return stdout.Bytes(), wrappedErr
	}
	return nil, wrappedErr
}

// ghGraphQLError turns a failed gh subprocess into a display-safe error that
// names the real reason. gh's stderr carries the actionable text; the exit
// status alone is not useful to a reader. gh error output contains request
// results and standard CLI messages, never credentials. The message is capped
// here so the joined enrichment notice keeps room for per-PR reasons.
func ghGraphQLError(err error, stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		return fmt.Errorf("gh api graphql: %w", err)
	}
	return fmt.Errorf("gh api graphql: %s", trimTitle(firstLine(message), 150))
}

// firstLine trims a message to its first non-empty line so diagnostics stay
// single-line in table, JSON, and status output.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if index := strings.IndexAny(s, "\r\n"); index >= 0 {
		return s[:index]
	}
	return s
}

// hasGraphQLDataEnvelope reports whether raw is a JSON object carrying a
// non-null GraphQL "data" member, the shape gh returns for a partially
// successful batch query.
func hasGraphQLDataEnvelope(raw []byte) bool {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	return len(envelope.Data) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null"))
}

var fetchIssueRelationshipsBatchFunc = fetchIssueRelationshipsBatchContext
var fetchIssueRelationshipsFunc = fetchIssueRelationshipsContext

func fetchIssueRelationshipsContext(ctx context.Context, owner, name, host string, issueNumbers []int) (map[int][]linkedReference, map[int]bool, error) {
	if len(issueNumbers) == 0 {
		return nil, nil, nil
	}

	result := make(map[int][]linkedReference)
	unavailable := make(map[int]bool)
	var firstErr error
	for start := 0; start < len(issueNumbers); start += relationshipBatchSize {
		if contextErr := markCanceledRelationships(ctx, issueNumbers[start:], unavailable); contextErr != nil {
			firstErr = retainFirstError(firstErr, contextErr)
			break
		}
		end := min(start+relationshipBatchSize, len(issueNumbers))
		batch, batchUnavailable, err := fetchIssueRelationshipsBatchFunc(ctx, owner, name, host, issueNumbers[start:end])
		for number, refs := range batch {
			result[number] = refs
		}
		for number := range batchUnavailable {
			unavailable[number] = true
		}
		firstErr = retainFirstError(firstErr, err)
	}
	return result, unavailable, firstPartialFetchError(firstErr, unavailable, len(issueNumbers), "issues")
}

func markCanceledRelationships(ctx context.Context, remaining []int, unavailable map[int]bool) error {
	contextErr := githubContextError(ctx.Err())
	if contextErr == nil {
		return nil
	}
	for _, number := range remaining {
		unavailable[number] = true
	}
	return contextErr
}

func retainFirstError(first, candidate error) error {
	if first != nil {
		return first
	}
	return candidate
}

func fetchIssueRelationshipsBatchContext(ctx context.Context, owner, name, host string, issueNumbers []int) (map[int][]linkedReference, map[int]bool, error) {
	return fetchIssueRelationshipsBatchWithGraphQL(owner, name, host, issueNumbers, func(host, query string) ([]byte, error) {
		return fetchGraphQLContext(ctx, host, query)
	})
}

func fetchIssueRelationshipsBatchWithGraphQL(owner, name, host string, issueNumbers []int, fetch func(string, string) ([]byte, error)) (map[int][]linkedReference, map[int]bool, error) {
	queryParts := make([]string, 0, len(issueNumbers))
	for _, number := range issueNumbers {
		queryParts = append(queryParts, fmt.Sprintf(
			`issue%d: issue(number: %d) { number closedByPullRequestsReferences(first: 100) { totalCount nodes { number url } } }`,
			number, number,
		))
	}
	query := fmt.Sprintf(
		`query { repository(owner: %q, name: %q) { %s } }`,
		owner, name, strings.Join(queryParts, " "),
	)
	data, err := fetch(host, query)
	unavailable := unavailableIssueNumbers(issueNumbers, nil)
	if data == nil {
		return nil, unavailable, err
	}
	refs, errored, parseErr := parseIssueRelationships(data)
	if parseErr != nil {
		return nil, unavailable, parseErr
	}
	unavailable = unavailableIssueNumbers(issueNumbers, refs)
	for number := range errored {
		unavailable[number] = true
	}
	return refs, unavailable, err
}

// unavailableIssueNumbers lists requested issues that produced no parsed
// relationship data, so a partially failed batch fails closed for exactly
// those issues instead of every issue in the batch.
func unavailableIssueNumbers(issueNumbers []int, refs map[int][]linkedReference) map[int]bool {
	unavailable := make(map[int]bool, len(issueNumbers))
	for _, number := range issueNumbers {
		if _, ok := refs[number]; !ok {
			unavailable[number] = true
		}
	}
	return unavailable
}

func parseIssueRelationships(data []byte) (map[int][]linkedReference, map[int]bool, error) {
	var response struct {
		Data struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, nil, err
	}

	result := make(map[int][]linkedReference)
	for _, raw := range response.Data.Repository {
		var issue struct {
			Number                         int                        `json:"number"`
			ClosedByPullRequestsReferences *linkedReferenceConnection `json:"closedByPullRequestsReferences"`
		}
		if err := json.Unmarshal(raw, &issue); err != nil || issue.Number <= 0 || !issue.ClosedByPullRequestsReferences.complete() {
			continue
		}
		result[issue.Number] = issue.ClosedByPullRequestsReferences.Nodes
	}
	return result, issueAliasesFromErrors(response.Errors), nil
}

// issueAliasesFromErrors walks each GraphQL error path for the batch's issueN
// alias names, because an error path through an alias marks that alias's
// relationship data as failed even when a parseable object survived.
func issueAliasesFromErrors(errors []graphQLError) map[int]bool {
	errored := make(map[int]bool)
	for _, gqlErr := range errors {
		for _, element := range gqlErr.Path {
			var name string
			if json.Unmarshal(element, &name) != nil {
				continue
			}
			if number, ok := aliasNumber(name, "issue"); ok {
				errored[number] = true
			}
		}
	}
	return errored
}
