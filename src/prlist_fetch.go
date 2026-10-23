package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cli/go-gh/v2/pkg/repository"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ghExecFunc is the single choke point for gh subprocess execution; it adds
// multi-account fallback and can be swapped in tests.
var ghExecFunc = execGH

// fetchPRSupplementalBatchFunc is swapped in tests to avoid real API calls.
var fetchPRSupplementalBatchFunc = fetchPRSupplementalBatch

// prSupplementalData merges parsed supplemental info with the set of PRs whose
// enrichment failed or stayed incomplete. Unavailable PRs render unknown
// columns; every error is carried for display instead of being swallowed.
type prSupplementalData struct {
	Info        map[int]prSupplementalInfo
	Unavailable map[int]bool
	Err         error
}

// fetchSupplementalData retrieves supplemental PR data via GraphQL (best-effort).
func fetchSupplementalData(repo string, prs []pullRequest) (prSupplementalData, string, string) {
	owner, name, err := resolveRepo(repo)
	if err != nil {
		unavailable := make(map[int]bool, len(prs))
		for _, pr := range prs {
			unavailable[pr.Number] = true
		}
		return prSupplementalData{Unavailable: unavailable, Err: err}, "", ""
	}
	numbers := make([]int, len(prs))
	for i, pr := range prs {
		numbers[i] = pr.Number
	}
	fetched, unavailable, fetchErr := fetchPRSupplemental(owner, name, repositoryTargetHost(repo), numbers)
	data := prSupplementalData{Info: fetched, Unavailable: unavailable, Err: fetchErr}
	// Derived per-PR reasons join with any fetch error: a retained PR can
	// still carry truncated or unattributable evidence while the batch also
	// failed, and every rendered unknown column deserves its reason.
	data.Err = joinSupplementalReasons(
		fetchErr,
		incompleteConnectionError(fetched),
		evidenceAmbiguityError(fetched),
		unattributableEvidenceError(fetched),
	)
	return data, owner, name
}

// joinSupplementalReasons renders every distinct enrichment problem in one
// diagnostic line, so truncated connections and unordered evidence both get
// named when they apply to the same batch. External gh messages are capped
// where they are created; the self-generated per-PR reasons stay whole so
// their PR number lists survive the joined notice.
func joinSupplementalReasons(reasons ...error) error {
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason != nil {
			parts = append(parts, reason.Error())
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// incompleteConnectionError explains rendered unknown columns that come from
// truncated connections instead of a failed fetch, so a PR with more
// supplemental rows than one page holds still says why parts stay unknown.
func incompleteConnectionError(infos map[int]prSupplementalInfo) error {
	numbers := incompleteInfoNumbers(infos, func(info prSupplementalInfo) bool { return info.Incomplete })
	if len(numbers) == 0 {
		return nil
	}
	return fmt.Errorf("truncated supplemental connections for pull request(s) %s", joinPRNumbers(numbers))
}

// evidenceAmbiguityError names PRs whose AI evidence cannot be ordered, so
// an unknown AI column is not misreported as truncated data.
func evidenceAmbiguityError(infos map[int]prSupplementalInfo) error {
	numbers := incompleteInfoNumbers(infos, func(info prSupplementalInfo) bool { return info.EvidenceAmbiguous })
	if len(numbers) == 0 {
		return nil
	}
	return fmt.Errorf("cannot order AI review evidence for pull request(s) %s", joinPRNumbers(numbers))
}

// unattributableEvidenceError names PRs whose review evidence cannot be
// attributed to an author, so an unknown AI column is not misread as a
// confirmed pass or failure.
func unattributableEvidenceError(infos map[int]prSupplementalInfo) error {
	numbers := incompleteInfoNumbers(infos, func(info prSupplementalInfo) bool { return info.UnattributableEvidence })
	if len(numbers) == 0 {
		return nil
	}
	return fmt.Errorf("unattributable review evidence for pull request(s) %s", joinPRNumbers(numbers))
}

func incompleteInfoNumbers(infos map[int]prSupplementalInfo, marked func(prSupplementalInfo) bool) []int {
	numbers := make([]int, 0, len(infos))
	for number, info := range infos {
		if marked(info) {
			numbers = append(numbers, number)
		}
	}
	sort.Ints(numbers)
	return numbers
}

func joinPRNumbers(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, number := range numbers {
		parts[i] = strconv.Itoa(number)
	}
	return strings.Join(parts, ", ")
}

// fetchRequiredChecks retrieves required check contexts per base branch (best-effort).
func fetchRequiredChecks(owner, name string, prs []pullRequest) (map[string]map[string]bool, map[string]error) {
	return fetchRequiredChecksCached(owner, name, prs, nil)
}

type requiredCheckResult struct {
	contexts map[string]bool
	ok       bool
	err      error
}

func fetchRequiredChecksCached(owner, name string, prs []pullRequest, cache map[string]requiredCheckResult) (map[string]map[string]bool, map[string]error) {
	result := make(map[string]map[string]bool)
	failed := make(map[string]error)
	if owner == "" {
		for _, base := range uniqueBaseBranches(prs) {
			failed[base] = errors.New("repository unavailable")
		}
		return result, failed
	}
	for _, base := range uniqueBaseBranches(prs) {
		key := owner + "/" + name + "/" + base
		cached, found := cache[key]
		if !found {
			cached.contexts, cached.ok, cached.err = fetchRequiredCheckContexts(owner, name, base)
			if cache != nil {
				cache[key] = cached
			}
		}
		ctx, ok, err := cached.contexts, cached.ok, cached.err
		switch {
		case err == nil && ok && len(ctx) > 0:
			result[base] = ctx
		case err != nil:
			failed[base] = err
		}
	}
	return result, failed
}

var repositoryCurrentFunc = repository.Current

func resolveRepo(repoOverride string) (string, string, error) {
	return resolveRepoWithExecutor(repoOverride, ghExecFunc)
}

func resolveRepoContext(ctx context.Context, repoOverride string) (string, string, error) {
	executor := func(args ...string) (bytes.Buffer, bytes.Buffer, error) {
		return ghExecContextFunc(ctx, args...)
	}
	return resolveRepoWithExecutor(repoOverride, executor)
}

func resolveRepoWithExecutor(repoOverride string, executor ghExecutor) (string, string, error) {
	if repoOverride == "" {
		repoOverride = strings.TrimSpace(os.Getenv("GH_REPO"))
	}
	if repoOverride != "" {
		parts := strings.Split(repoOverride, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("invalid repo format: %s", repoOverride)
		}
		return parts[len(parts)-2], parts[len(parts)-1], nil
	}

	repo, err := repositoryCurrentFunc()
	if err == nil {
		return repo.Owner, repo.Name, nil
	}

	// Fall back to gh repo view for SSH aliases and non-standard remotes
	stdout, _, execErr := executor("repo", "view", "--json", "owner,name")
	if execErr != nil {
		return "", "", fmt.Errorf("repo resolution failed: %w; fallback: %v", err, execErr)
	}
	return parseRepoViewResponse(stdout.Bytes())
}

func parseRepoViewResponse(data []byte) (string, string, error) {
	var info struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", "", err
	}
	if info.Owner.Login == "" || info.Name == "" {
		return "", "", fmt.Errorf("could not resolve repo from gh repo view")
	}
	return info.Owner.Login, info.Name, nil
}

// resolveAuthorLoginFunc is swapped in tests to avoid real API calls.
var resolveAuthorLoginFunc = resolveAuthorLogin

// resolveAuthorLogin resolves an author value to a GitHub login.
// If the value contains no spaces, it's assumed to be a login already (with
// optional "@" prefix stripped). If it contains a space, it's treated as a
// display name and resolved via org member search (GraphQL), falling back to
// global GitHub user search.
func resolveAuthorLogin(author, org string) (string, error) {
	author = strings.TrimPrefix(author, "@")
	if !strings.Contains(author, " ") {
		return author, nil
	}

	// Try org-scoped member lookup first (most reliable for org repos).
	if org != "" {
		if login := resolveAuthorFromOrg(author, org); login != "" {
			return login, nil
		}
	}

	// Fall back to global GitHub user search.
	query := url.QueryEscape(author + " in:name")
	endpoint := fmt.Sprintf("search/users?q=%s&per_page=1", query)
	stdout, _, err := ghExecFunc("api", endpoint, "--jq", ".items[0].login")
	if err != nil {
		return "", fmt.Errorf("resolving author %q: %w", author, err)
	}
	login := strings.TrimSpace(stdout.String())
	if login == "" || login == "null" {
		return "", fmt.Errorf("no GitHub user found matching name %q", author)
	}
	return login, nil
}

// resolveAuthorFromOrg resolves a display name to a login within an org.
// Searches globally for matching users, then verifies org membership.
// Returns the matching login or empty string if not found.
func resolveAuthorFromOrg(name, org string) string {
	query := url.QueryEscape(name + " in:name")
	endpoint := fmt.Sprintf("search/users?q=%s&per_page=5", query)
	stdout, _, err := ghExecFunc("api", endpoint, "--jq", ".items[].login")
	if err != nil {
		return ""
	}
	logins := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, login := range logins {
		login = strings.TrimSpace(login)
		if login == "" || login == "null" {
			continue
		}
		memberEndpoint := fmt.Sprintf("orgs/%s/members/%s", org, login)
		_, _, memberErr := ghExecFunc("api", memberEndpoint)
		if memberErr == nil {
			return login
		}
	}
	return ""
}

func fetchPRSupplemental(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
	if len(prNumbers) == 0 {
		return nil, nil, nil
	}

	// Batch PRs to avoid exceeding Windows command-line length limits (~32K chars).
	// Each PR's query fragment is ~350 chars; batches of 30 stay well under the limit.
	result := make(map[int]prSupplementalInfo)
	unavailable := make(map[int]bool)
	var firstErr error
	for i := 0; i < len(prNumbers); i += relationshipBatchSize {
		end := i + relationshipBatchSize
		if end > len(prNumbers) {
			end = len(prNumbers)
		}
		batch, batchUnavailable, err := fetchPRSupplementalBatchFunc(owner, name, host, prNumbers[i:end])
		for k, v := range batch {
			result[k] = v
		}
		for number := range batchUnavailable {
			unavailable[number] = true
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return result, unavailable, firstPartialFetchError(firstErr, unavailable, len(prNumbers), "pull requests")
}

// firstPartialFetchError guarantees a diagnostic whenever requested items
// stayed unavailable: a recovered payload that omitted aliases, or a
// structurally empty response, must not render unknown columns silently.
func firstPartialFetchError(firstErr error, unavailable map[int]bool, requested int, kind string) error {
	if firstErr != nil || len(unavailable) == 0 {
		return firstErr
	}
	return fmt.Errorf("%d of %d requested %s returned no supplemental data", len(unavailable), requested, kind)
}

func fetchPRSupplementalBatch(owner, name, host string, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
	query := buildPullRequestBatchQuery(owner, name, prNumbers, prSupplementalFields)

	data, err := fetchGraphQL(host, query)
	return parseSupplementalBatch(data, err, prNumbers)
}

func parseSupplementalBatch(data []byte, err error, prNumbers []int) (map[int]prSupplementalInfo, map[int]bool, error) {
	unavailable := unavailablePRNumbers(prNumbers, nil)
	if data == nil {
		return nil, unavailable, err
	}
	infos, errored, parseErr := parseSupplementalResponse(data)
	if parseErr != nil {
		return nil, unavailable, parseErr
	}
	unavailable = unavailablePRNumbers(prNumbers, infos)
	for number := range errored {
		unavailable[number] = true
	}
	return infos, unavailable, err
}

// unavailablePRNumbers lists requested PRs that produced no parsed
// supplemental info, so a partially failed batch fails closed for exactly
// those PRs instead of every PR in the batch.
func unavailablePRNumbers(prNumbers []int, infos map[int]prSupplementalInfo) map[int]bool {
	unavailable := make(map[int]bool, len(prNumbers))
	for _, number := range prNumbers {
		if _, ok := infos[number]; !ok {
			unavailable[number] = true
		}
	}
	return unavailable
}

const prSupplementalFields = `number headRefOid closingIssuesReferences(first: 100) { totalCount nodes { number url } } comments(last: 100) { totalCount nodes { body createdAt author { login __typename } } } reactions(content: THUMBS_UP, last: 100) { totalCount nodes { createdAt user { login __typename } } } reviewThreads(last: 100) { totalCount nodes { isResolved comments(first: 1) { nodes { author { login __typename } } } } } reviews(last: 100) { totalCount nodes { state body submittedAt commit { oid } author { login __typename } comments { totalCount } } } approvedReviews: latestOpinionatedReviews(last: 100) { totalCount pageInfo { hasPreviousPage } nodes { state submittedAt author { login __typename } } }`

func buildPullRequestBatchQuery(owner, name string, numbers []int, fields string) string {
	parts := make([]string, 0, len(numbers))
	for _, number := range numbers {
		parts = append(parts, fmt.Sprintf("pr%d: pullRequest(number: %d) { %s }", number, number, fields))
	}
	return fmt.Sprintf("query { repository(owner: %q, name: %q) { %s } }", owner, name, strings.Join(parts, " "))
}
