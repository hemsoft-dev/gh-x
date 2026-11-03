package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/muesli/termenv"
)

type atmOptions struct {
	org            string
	limit          int
	authored       bool
	reviewRequired bool
	ready          bool
	json           bool
}

func runAtm(args []string, stdout io.Writer, stderr io.Writer) error {
	options, err := parseAtmOptions(args, stderr)
	if err != nil {
		if errors.Is(err, errHelpDisplayed) {
			return nil
		}
		return err
	}

	return executeAtm(options, stdout)
}

func atmFlags(options *atmOptions, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("atm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		writeAtmUsage(stderr)
	}

	flags.StringVar(&options.org, "org", "", "Organization to search (default: inferred from current repo)")
	flags.StringVar(&options.org, "o", "", "Organization to search (default: inferred from current repo)")
	flags.IntVar(&options.limit, "limit", 30, "Maximum number of pull requests to fetch")
	flags.IntVar(&options.limit, "L", 30, "Maximum number of pull requests to fetch")
	flags.BoolVar(&options.authored, "authored", false, "Show PRs you authored (default: show PRs needing your review)")
	flags.BoolVar(&options.authored, "a", false, "Show PRs you authored (default: show PRs needing your review)")
	flags.BoolVar(&options.reviewRequired, "review-required", false, "Show only PRs where your review is directly requested")
	flags.BoolVar(&options.reviewRequired, "r", false, "Show only PRs where your review is directly requested")
	flags.BoolVar(&options.ready, "ready", false, "Show only PRs ready to merge (open, AI pass, checks pass, all comments resolved)")
	flags.BoolVar(&options.json, "json", false, "Output enriched JSON instead of a table")

	return flags
}

func parseAtmOptions(args []string, stderr io.Writer) (atmOptions, error) {
	options := atmOptions{limit: 30}

	flags := atmFlags(&options, stderr)

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, errHelpDisplayed
		}
		return options, err
	}

	if flags.NArg() > 0 {
		return options, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), ", "))
	}

	if options.limit < 1 {
		return options, errors.New("limit must be greater than zero")
	}

	return options, nil
}

func writeAtmUsage(w io.Writer) {
	fmt.Fprint(w, atmUsage)
}

const atmUsage = `Usage:
  gh x pr atm [flags]

Show open pull requests across an organization that need your attention.
By default, shows PRs needing your review. Use --authored for PRs you created.

Flags:
  -o, --org string        Organization to search (default: inferred from current repo)
  -L, --limit int         Maximum number of pull requests to fetch (default 30)
  -a, --authored          Show PRs you authored instead of PRs needing review
  -r, --review-required   Show only PRs where your review is directly requested
      --ready             Show only PRs ready to merge (open, AI pass, checks pass, all comments resolved)
      --json              Output enriched JSON instead of a table
`

func executeAtm(options atmOptions, stdout io.Writer) error {
	org, err := resolveAtmOrg(options.org)
	if err != nil {
		return fmt.Errorf("cannot determine organization: %w", err)
	}

	login, err := resolveCurrentUser()
	if err != nil {
		return fmt.Errorf("cannot determine current user: %w", err)
	}

	nodes, err := fetchAtmNodes(options, org, login)
	if err != nil {
		return err
	}

	return renderAtmResults(nodes, stdout, org, login, options, time.Now().UTC())
}

func renderAtmResults(nodes []atmPullRequestNode, stdout io.Writer, org, login string, options atmOptions, now time.Time) error {
	rendered := make([]displayPullRequest, 0, len(nodes))
	for _, node := range nodes {
		rendered = append(rendered, mapAtmNode(node, now))
	}

	if options.ready {
		rendered = filterReadyPRs(rendered)
	}

	if options.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rendered)
	}

	return renderAtmTable(stdout, org, login, options, rendered)
}

// fetchAtmNodes fetches ATM pull request nodes using the appropriate search
// strategy based on options.
func fetchAtmNodes(options atmOptions, org, login string) ([]atmPullRequestNode, error) {
	if options.authored || options.reviewRequired {
		return fetchAtmSingleSearch(org, login, options)
	}
	return fetchAtmMultiSearch(org, login, options)
}

func fetchAtmSingleSearch(org, login string, options atmOptions) ([]atmPullRequestNode, error) {
	searchQuery := buildAtmSearchQuery(org, login, options.reviewRequired)
	query := buildAtmGraphQLQuery(searchQuery, options.limit)
	stdoutBuf, stderrBuf, err := execGHActive("api", "graphql", "-f", fmt.Sprintf("query=%s", query))
	if err != nil {
		return nil, wrapExecError(fmt.Errorf("GraphQL search failed: %w", err), stderrBuf.String())
	}
	return parseAtmSearchResponse(stdoutBuf.Bytes())
}

func fetchAtmMultiSearch(org, login string, options atmOptions) ([]atmPullRequestNode, error) {
	queries := buildAtmNeedsReviewQueries(org, login)
	query := buildAtmMultiSearchQuery(queries, options.limit)
	stdoutBuf, stderrBuf, err := execGHActive("api", "graphql", "-f", fmt.Sprintf("query=%s", query))
	if err != nil {
		return nil, wrapExecError(fmt.Errorf("GraphQL search failed: %w", err), stderrBuf.String())
	}
	nodes, err := parseAtmMultiSearchResponse(stdoutBuf.Bytes())
	if err != nil {
		return nil, err
	}
	return filterUnapprovedNodes(nodes, login), nil
}

func filterUnapprovedNodes(nodes []atmPullRequestNode, login string) []atmPullRequestNode {
	filtered := make([]atmPullRequestNode, 0, len(nodes))
	for _, n := range nodes {
		if !userApprovedPR(n, login) {
			filtered = append(filtered, n)
		}
	}
	return filtered
}

func resolveAtmOrg(orgOverride string) (string, error) {
	if orgOverride != "" {
		return orgOverride, nil
	}
	owner, _, err := resolveRepo("")
	if err != nil {
		return "", fmt.Errorf("not in a repository; use --org to specify organization")
	}
	return owner, nil
}

func resolveCurrentUser() (string, error) {
	stdout, _, err := execGHActive("api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	login := strings.TrimSpace(stdout.String())
	if login == "" {
		return "", fmt.Errorf("empty login returned")
	}
	return login, nil
}

// expandMeReference replaces the "@me" filter value with the active account's
// login so a later multi-account retry cannot reinterpret the identity. The
// "@" prefix is required: a bare "me" is a literal login and passes through.
func expandMeReference(value string) (string, error) {
	if !strings.HasPrefix(value, "@") || !strings.EqualFold(strings.TrimPrefix(value, "@"), "me") {
		return value, nil
	}
	login, err := resolveCurrentUser()
	if err != nil {
		return "", fmt.Errorf("cannot resolve @me: %w", err)
	}
	return login, nil
}

// identityQualifierPattern matches account-relative search qualifiers whose
// @me value must track the active account. Other @me occurrences in --search
// are arbitrary query text and stay untouched.
var identityQualifierPattern = regexp.MustCompile(`(?i)\b(author|assignee|mentions|commenter|involves|review-requested|reviewed-by):@me\b`)

// expandSearchReferences rewrites @me values of identity qualifiers inside a
// --search query so a later multi-account retry cannot reinterpret them.
func expandSearchReferences(search string) (string, error) {
	if !identityQualifierPattern.MatchString(search) {
		return search, nil
	}
	login, err := resolveCurrentUser()
	if err != nil {
		return "", fmt.Errorf("cannot resolve @me: %w", err)
	}
	return identityQualifierPattern.ReplaceAllStringFunc(search, func(match string) string {
		// Trim by length: the matched spelling may be any case (@me, @ME).
		return match[:len(match)-len("@me")] + login
	}), nil
}

// expandListIdentityFilters expands @me author/assignee filters and @me
// references inside --search queries.
func expandListIdentityFilters(author, assignee, search string) (string, string, string, error) {
	expandedAuthor, err := expandMeReference(author)
	if err != nil {
		return "", "", "", err
	}
	expandedAssignee, err := expandMeReference(assignee)
	if err != nil {
		return "", "", "", err
	}
	expandedSearch, err := expandSearchReferences(search)
	if err != nil {
		return "", "", "", err
	}
	return expandedAuthor, expandedAssignee, expandedSearch, nil
}

func buildAtmSearchQuery(org, login string, reviewRequired bool) string {
	if reviewRequired {
		return fmt.Sprintf("is:pr is:open review-requested:%s org:%s", login, org)
	}
	return fmt.Sprintf("is:pr is:open author:%s org:%s", login, org)
}

func buildAtmNeedsReviewQueries(org, login string) []string {
	return []string{
		fmt.Sprintf("is:pr is:open review-requested:%s org:%s", login, org),
		fmt.Sprintf("is:pr is:open assignee:%s org:%s -author:%s", login, org, login),
		fmt.Sprintf("is:pr is:open reviewed-by:%s org:%s -author:%s", login, org, login),
	}
}

const atmPRFieldsFragment = `
        number
        title
        author { login ... on User { name } }
        state
        isDraft
        reviewDecision
        updatedAt
        headRefName
        headRefOid
        baseRefName
        url
        repository { nameWithOwner }
        commits(last: 1) {
          nodes {
            commit {
              statusCheckRollup {
                contexts(first: 100) {
                  nodes {
                    __typename
                    ... on CheckRun { name status conclusion }
                    ... on StatusContext { context state }
                  }
                }
              }
            }
          }
        }
        latestReviews(first: 50) {
          nodes {
            state
            author { login __typename }
            comments { totalCount }
          }
        }
        reviews(first: 100) {
          totalCount
          nodes {
            state
            body
            commit { oid }
            author { login __typename }
            comments { totalCount }
          }
        }
        reviewThreads(first: 100) {
          totalCount
          nodes {
            isResolved
            comments(first: 1) {
              nodes {
                fullDatabaseId
                author { login __typename }
              }
            }
          }
        }
        approvedReviews: reviews(states: [APPROVED], last: 50) {
          nodes {
            author { login __typename }
          }
        }`

func buildAtmGraphQLQuery(searchQuery string, limit int) string {
	return fmt.Sprintf(`{
  search(query: %q, type: ISSUE, first: %d) {
    nodes {
      ... on PullRequest {%s
      }
    }
  }
}`, searchQuery, limit, atmPRFieldsFragment)
}

func buildAtmMultiSearchQuery(queries []string, limit int) string {
	var sb strings.Builder
	sb.WriteString("{\n")
	for i, q := range queries {
		sb.WriteString(fmt.Sprintf(`  q%d: search(query: %q, type: ISSUE, first: %d) {
    nodes {
      ... on PullRequest {%s
      }
    }
  }
`, i, q, limit, atmPRFieldsFragment))
	}
	sb.WriteString("}")
	return sb.String()
}

type atmReviewNode struct {
	State  string `json:"state"`
	Body   string `json:"body"`
	Commit struct {
		OID string `json:"oid"`
	} `json:"commit"`
	Author struct {
		Login    string `json:"login"`
		Typename string `json:"__typename"`
	} `json:"author"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
}

// atmPullRequestNode represents a PR returned from the GraphQL search query.
type atmPullRequestNode struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	Author         *author   `json:"author"`
	State          string    `json:"state"`
	IsDraft        bool      `json:"isDraft"`
	ReviewDecision string    `json:"reviewDecision"`
	UpdatedAt      time.Time `json:"updatedAt"`
	HeadRefName    string    `json:"headRefName"`
	HeadRefOID     string    `json:"headRefOid"`
	BaseRefName    string    `json:"baseRefName"`
	URL            string    `json:"url"`
	Repository     struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkItem `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	LatestReviews struct {
		Nodes []struct {
			State  string `json:"state"`
			Author struct {
				Login    string `json:"login"`
				Typename string `json:"__typename"`
			} `json:"author"`
			Comments struct {
				TotalCount int `json:"totalCount"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"latestReviews"`
	Reviews struct {
		TotalCount int             `json:"totalCount"`
		Nodes      []atmReviewNode `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			IsResolved bool `json:"isResolved"`
			Comments   struct {
				Nodes []struct {
					FullDatabaseID string `json:"fullDatabaseId"`
					Author         struct {
						Login    string `json:"login"`
						Typename string `json:"__typename"`
					} `json:"author"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	ApprovedReviews struct {
		Nodes []struct {
			Author struct {
				Login    string `json:"login"`
				Typename string `json:"__typename"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"approvedReviews"`
}

func parseAtmSearchResponse(data []byte) ([]atmPullRequestNode, error) {
	var resp struct {
		Data struct {
			Search struct {
				Nodes []atmPullRequestNode `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode GraphQL response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("GraphQL error: %s", resp.Errors[0].Message)
	}
	return resp.Data.Search.Nodes, nil
}

func parseAtmMultiSearchResponse(data []byte) ([]atmPullRequestNode, error) {
	var raw struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode GraphQL response: %w", err)
	}
	if len(raw.Errors) > 0 {
		return nil, fmt.Errorf("GraphQL error: %s", raw.Errors[0].Message)
	}

	seen := make(map[string]bool)
	var result []atmPullRequestNode

	for i := 0; ; i++ {
		key := fmt.Sprintf("q%d", i)
		v, ok := raw.Data[key]
		if !ok {
			break
		}
		var search struct {
			Nodes []atmPullRequestNode `json:"nodes"`
		}
		if err := json.Unmarshal(v, &search); err != nil {
			return nil, fmt.Errorf("failed to parse query q%d: %w", i, err)
		}
		for _, node := range search.Nodes {
			prKey := fmt.Sprintf("%s#%d", node.Repository.NameWithOwner, node.Number)
			if !seen[prKey] {
				seen[prKey] = true
				result = append(result, node)
			}
		}
	}

	return result, nil
}

func userApprovedPR(node atmPullRequestNode, login string) bool {
	for _, r := range node.LatestReviews.Nodes {
		if strings.EqualFold(r.Author.Login, login) && strings.EqualFold(r.State, "APPROVED") {
			return true
		}
	}
	return false
}

func mapAtmNode(node atmPullRequestNode, now time.Time) displayPullRequest {
	authorName := "-"
	if node.Author != nil && node.Author.Login != "" {
		authorName = formatAuthor(node.Author.Login, node.Author.Name)
	}

	repoName := extractRepoShortName(node.Repository.NameWithOwner)
	checkItems := extractAtmCheckItems(node)

	var aiNodes []aiReviewNode
	for _, r := range node.Reviews.Nodes {
		aiNodes = append(aiNodes, aiReviewNode{
			State:        r.State,
			CommitOID:    r.Commit.OID,
			AuthorLogin:  r.Author.Login,
			AuthorType:   r.Author.Typename,
			ReviewBody:   r.Body,
			CommentCount: r.Comments.TotalCount,
		})
	}

	aiThreads := extractAtmReviewThreads(node)
	reviewsTruncated := node.Reviews.TotalCount > len(node.Reviews.Nodes)
	threadsTruncated := node.ReviewThreads.TotalCount > len(node.ReviewThreads.Nodes)
	aiReview, aiClean := summarizeSupplementalReviews(
		aiNodes,
		aiThreads,
		node.HeadRefOID,
		anyConnectionTruncated(reviewsTruncated, threadsTruncated),
	)
	var aiCleanValue *bool
	if aiClean {
		aiCleanValue = boolPtr(true)
	}

	threads := reviewThreadInfo{
		Total:    node.ReviewThreads.TotalCount,
		Resolved: countResolvedThreads(aiThreads),
	}

	var approverLogins []string
	for _, r := range node.ApprovedReviews.Nodes {
		approverLogins = append(approverLogins, r.Author.Login)
	}

	return displayPullRequest{
		Number:    node.Number,
		Title:     trimTitle(node.Title, 42),
		Author:    authorName,
		State:     normalizeState(node.State, node.IsDraft),
		Review:    normalizeReviewDecision(node.ReviewDecision),
		Approvals: countUniqueApprovers(approverLogins),
		Checks:    normalizeCheckState(checkItems),
		Comments:  formatComments(threads),
		AIReview:  aiReview,
		AIClean:   aiCleanValue,
		Branch:    formatBranch(node.HeadRefName),
		Updated:   formatRelativeTime(node.UpdatedAt, now),
		URL:       node.URL,
		Repo:      repoName,
		updatedAt: node.UpdatedAt,
	}
}

// extractRepoShortName returns just the repo name from "owner/name".
func extractRepoShortName(nameWithOwner string) string {
	if parts := strings.SplitN(nameWithOwner, "/", 2); len(parts) == 2 {
		return parts[1]
	}
	return nameWithOwner
}

// extractAtmCheckItems pulls check items from the nested commits structure.
func extractAtmCheckItems(node atmPullRequestNode) []checkItem {
	if len(node.Commits.Nodes) == 0 {
		return nil
	}
	commit := node.Commits.Nodes[0].Commit
	if commit.StatusCheckRollup == nil {
		return nil
	}
	return commit.StatusCheckRollup.Contexts.Nodes
}

// extractAtmReviewThreads builds aiReviewThread slice from ATM node data.
func extractAtmReviewThreads(node atmPullRequestNode) []aiReviewThread {
	threads := make([]aiReviewThread, 0, len(node.ReviewThreads.Nodes))
	for _, t := range node.ReviewThreads.Nodes {
		var login, authorType, commentID string
		if len(t.Comments.Nodes) > 0 {
			login = t.Comments.Nodes[0].Author.Login
			authorType = t.Comments.Nodes[0].Author.Typename
			commentID = t.Comments.Nodes[0].FullDatabaseID
		}
		threads = append(threads, aiReviewThread{
			AuthorLogin: login,
			AuthorType:  authorType,
			IsResolved:  t.IsResolved,
			CommentID:   commentID,
		})
	}
	return threads
}

func renderAtmTable(stdout io.Writer, org, login string, options atmOptions, pullRequests []displayPullRequest) error {
	if len(pullRequests) == 0 {
		switch {
		case options.ready:
			fmt.Fprintf(stdout, "No ready-to-merge PRs for %s in %s.\n", login, org)
		case options.authored:
			fmt.Fprintf(stdout, "No open PRs authored by %s in %s.\n", login, org)
		case options.reviewRequired:
			fmt.Fprintf(stdout, "No open PRs requesting review from %s in %s.\n", login, org)
		default:
			fmt.Fprintf(stdout, "No PRs needing review from %s in %s.\n", login, org)
		}
		return nil
	}

	switch {
	case options.ready:
		fmt.Fprintf(stdout, "Ready-to-merge PRs for %s in %s\n\n", login, org)
	case options.authored:
		fmt.Fprintf(stdout, "Open PRs by %s in %s\n\n", login, org)
	case options.reviewRequired:
		fmt.Fprintf(stdout, "PRs requesting review from %s in %s\n\n", login, org)
	default:
		fmt.Fprintf(stdout, "PRs needing review from %s in %s\n\n", login, org)
	}

	colorEnabled := term.FromEnv().IsColorEnabled()
	return renderAtmTableWithStyle(stdout, pullRequests, colorEnabled)
}

func renderAtmTableWithStyle(stdout io.Writer, pullRequests []displayPullRequest, colorEnabled bool) error {
	styler := newTableStyler(stdout, colorEnabled)

	headerLabels := []string{"#", "Title", "Repo", "Author", "State", "Rev", "AI", "Appv", "Checks", "Cmts", "Upd"}
	headers := make([]tableCell, len(headerLabels))
	for i, label := range headerLabels {
		headers[i] = styler.dim(label)
	}

	rows := make([][]tableCell, len(pullRequests))
	for i, pr := range pullRequests {
		rows[i] = []tableCell{
			styler.numberCell(pr.Number, pr.URL),
			styler.plain(pr.Title),
			styler.colored(pr.Repo, termenv.ANSICyan),
			styler.plain(pr.Author),
			styler.stateCell(pr.State),
			styler.reviewCell(pr.Review),
			styler.aiReviewCell(pr.AIReview),
			styler.approvalCell(pr.Approvals),
			styler.checksCell(pr.Checks),
			styler.commentsCell(pr.Comments, pr.AIClean),
			styler.dim(pr.Updated),
		}
	}

	colWidths := computeColumnWidths(headers, rows)

	// Fit to terminal: Title(1), Repo(2), Author(3) are flexible
	flexibleCols := []int{1, 2, 3}
	colWidths = fitColumnsToTerminal(colWidths, flexibleCols, getTerminalWidth())
	rows = truncateCells(rows, colWidths, flexibleCols)

	writeRow(stdout, headers, colWidths)
	for _, row := range rows {
		writeRow(stdout, row, colWidths)
	}

	return nil
}

// filterReadyPRs returns only PRs that are ready to merge:
// open state, AI review pass, checks pass, all comments resolved.
func filterReadyPRs(prs []displayPullRequest) []displayPullRequest {
	filtered := make([]displayPullRequest, 0, len(prs))
	for _, pr := range prs {
		if isReadyPR(pr) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func isReadyPR(pr displayPullRequest) bool {
	return pr.State == "open" &&
		pr.AIReview == "pass" &&
		pr.Checks == "pass" &&
		commentsResolved(pr.Comments)
}

func commentsResolved(comments string) bool {
	if comments == "-" {
		return true
	}
	parts := strings.SplitN(comments, "/", 2)
	return len(parts) == 2 && parts[0] == parts[1]
}
