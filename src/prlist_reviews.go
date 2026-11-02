package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type reviewThreadInfo struct {
	Total    int
	Resolved int
}

type approverApproval struct {
	Login      string
	ApprovedAt time.Time
}

type decisiveApprovalReview struct {
	approverApproval
	State string
}

type prSupplementalInfo struct {
	Threads                reviewThreadInfo
	ThreadsTruncated       bool
	ClosingIssues          []linkedReference
	ClosingIssuesAvailable bool
	AIReview               string
	AIClean                bool
	HasUnresolvedAIThreads bool
	Approvals              int
	Approvers              []approverApproval
	ApprovalsIncomplete    bool
	Incomplete             bool
	EvidenceAmbiguous      bool
	UnattributableEvidence bool
}

// aiReviewNode holds the fields needed to detect bot reviewer status.
type aiReviewNode struct {
	State         string
	AuthorLogin   string
	AuthorType    string
	ReviewBody    string
	CommentCount  int
	ExplicitClean bool // Set only by parsed reviewer-specific clean evidence.
	OccurredAt    time.Time
	CommitOID     string
}

// aiReviewThread holds thread resolution state and authorship for AI review detection.
type aiReviewThread struct {
	AuthorLogin string
	AuthorType  string
	IsResolved  bool
	CommentID   string // Full database ID of the thread's first comment.
	LowSeverity bool   // Set when a Copilot review lists this thread as Low.
}

// aiReviewComment holds PR conversation comments that may contain a
// current-head Codex review summary.
type aiReviewComment struct {
	Body        string
	AuthorLogin string
	AuthorType  string
	OccurredAt  time.Time
}

// aiReviewReaction holds PR-description reactions used by connected Codex to
// signal that a completed current-head review found no issues.
type aiReviewReaction struct {
	AuthorLogin string
	AuthorType  string
	OccurredAt  time.Time
}

func formatComments(info reviewThreadInfo) string {
	if info.Total == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", info.Resolved, info.Total)
}

// countResolvedThreads counts the number of resolved threads from a slice.
func countResolvedThreads(threads []aiReviewThread) int {
	n := 0
	for _, t := range threads {
		if t.IsResolved {
			n++
		}
	}
	return n
}

// countUniqueApprovers counts unique logins from approved review nodes.
func countUniqueApprovers(logins []string) int {
	seen := make(map[string]bool, len(logins))
	for _, login := range logins {
		if login != "" {
			seen[strings.ToLower(login)] = true
		}
	}
	return len(seen)
}

// Known AI reviewer logins that don't use the [bot] suffix convention.
var knownAIReviewers = map[string]bool{
	"copilot-pull-request-reviewer": true,
}

func isAIReviewer(login string) bool {
	normalized := strings.ToLower(strings.TrimSpace(login))
	return strings.HasSuffix(normalized, "[bot]") || knownAIReviewers[normalized]
}

func isCopilotReviewer(login string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(login)), "[bot]")
	return normalized == "copilot" || normalized == "copilot-pull-request-reviewer"
}

type copilotReviewVerdict uint8

const (
	copilotVerdictUnknown copilotReviewVerdict = iota
	copilotVerdictClean
	copilotVerdictFindings
)

// classifyCopilotReview reads the explicit verdict heading GitHub Copilot
// emits. Copilot submits every verdict as COMMENTED, so state and inline
// comment count alone cannot distinguish a clean review from suppressed
// findings.
func classifyCopilotReview(body string) copilotReviewVerdict {
	for _, line := range strings.Split(body, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
		switch {
		case strings.HasSuffix(heading, "approval recommended"):
			return copilotVerdictClean
		case strings.HasSuffix(heading, "needs a closer look"),
			strings.HasSuffix(heading, "changes recommended"):
			return copilotVerdictFindings
		}
	}
	return copilotVerdictUnknown
}

// currentHeadReviewNodes keeps only review evidence tied to the current head.
// An empty head preserves compatibility for callers that lack commit metadata.
func currentHeadReviewNodes(reviews []aiReviewNode, headRefOID string) []aiReviewNode {
	if headRefOID == "" {
		return reviews
	}

	current := make([]aiReviewNode, 0, len(reviews))
	for _, review := range reviews {
		if strings.EqualFold(review.CommitOID, headRefOID) {
			current = append(current, review)
		}
	}
	return current
}

// hasCurrentHeadAIReview reports whether the formal review window contains AI
// evidence for the current head. Supplemental review queries fetch the newest
// reviews, so once current-head AI evidence is present, any truncated reviews
// are older and cannot change the latest current-head result.
func hasCurrentHeadAIReview(reviews []aiReviewNode, headRefOID string) bool {
	if headRefOID == "" {
		return false
	}
	for _, review := range currentHeadReviewNodes(reviews, headRefOID) {
		if isAIReviewer(review.AuthorLogin) || review.AuthorType == "Bot" {
			return true
		}
	}
	return false
}

// reviewWindowPredatesEvidence reports whether every omitted formal review is
// known to be older than independently fetched evidence, such as a Codex
// conversation receipt. A missing timestamp leaves the boundary ambiguous.
func reviewWindowPredatesEvidence(reviews []aiReviewNode, evidenceAt time.Time) bool {
	if evidenceAt.IsZero() || len(reviews) == 0 {
		return false
	}
	oldest := reviews[0].OccurredAt
	for _, review := range reviews {
		if review.OccurredAt.IsZero() {
			return false
		}
		if review.OccurredAt.Before(oldest) {
			oldest = review.OccurredAt
		}
	}
	return evidenceAt.After(oldest)
}

func sufficientReviewEvidence(reviews []aiReviewNode, headRefOID string, evidenceAt time.Time) bool {
	return hasCurrentHeadAIReview(reviews, headRefOID) || reviewWindowPredatesEvidence(reviews, evidenceAt)
}

// reviewEvidenceOrderAmbiguous reports whether independently fetched formal
// and conversation evidence cannot be ordered. Equal or missing timestamps do
// not prove which current-head result is newer.
func reviewEvidenceOrderAmbiguous(
	reviews []aiReviewNode,
	headRefOID string,
	evidenceAt time.Time,
	hasConversationEvidence bool,
) bool {
	if !hasConversationEvidence {
		return false
	}
	if evidenceAt.IsZero() {
		return true
	}
	var latestFormal time.Time
	found := false
	for _, review := range currentHeadReviewNodes(reviews, headRefOID) {
		if !isAIReviewer(review.AuthorLogin) && review.AuthorType != "Bot" {
			continue
		}
		found = true
		if review.OccurredAt.IsZero() {
			return true
		}
		if review.OccurredAt.After(latestFormal) {
			latestFormal = review.OccurredAt
		}
	}
	return found && latestFormal.Equal(evidenceAt)
}

var codexSummaryActivity = regexp.MustCompile(`(?m)^\| [^|\n]*\*\*(?:Code|Security) Review\*\* \| [^|\n]*\*\*(Running|Completed)\*\*[^|\n]*<relative-time datetime="([^"]+)">[^|\n]*\| \x60([0-9a-fA-F]{7,40})\x60 \|`)

// codexReviewNode returns parsed evidence plus whether the comment is a valid
// completed current-head summary, even when its clean reaction is unavailable.
func codexReviewNode(comment aiReviewComment, reactions []aiReviewReaction, headRefOID string) (aiReviewNode, bool, bool) {
	if !isCodexReviewer(comment.AuthorLogin) || headRefOID == "" {
		return aiReviewNode{}, false, false
	}
	if strings.Contains(comment.Body, "<!-- codex-pull-request-review-summary -->") {
		return codexSummaryReviewNode(comment, reactions, headRefOID)
	}

	body := strings.ToLower(comment.Body)
	if !strings.Contains(body, "codex review:") {
		return aiReviewNode{}, false, false
	}

	shaLength := min(10, len(headRefOID))
	if !strings.Contains(body, strings.ToLower(headRefOID[:shaLength])) {
		return aiReviewNode{}, false, false
	}

	commentCount := 1
	if strings.Contains(body, "didn't find any major issues") ||
		strings.Contains(body, "did not find any major issues") {
		commentCount = 0
	}

	return aiReviewNode{
		State:         "COMMENTED",
		AuthorLogin:   comment.AuthorLogin,
		AuthorType:    comment.AuthorType,
		CommentCount:  commentCount,
		ExplicitClean: commentCount == 0,
		OccurredAt:    comment.OccurredAt,
		CommitOID:     headRefOID,
	}, true, false
}

func codexSummaryReviewNode(comment aiReviewComment, reactions []aiReviewReaction, headRefOID string) (aiReviewNode, bool, bool) {
	matches := codexSummaryActivity.FindAllStringSubmatch(comment.Body, -1)
	if len(matches) != 1 || matches[0][1] != "Completed" || !strings.HasPrefix(strings.ToLower(headRefOID), strings.ToLower(matches[0][3])) {
		return aiReviewNode{}, false, false
	}
	completedAt, err := time.Parse(time.RFC3339Nano, matches[0][2])
	if err != nil {
		return aiReviewNode{}, false, false
	}
	reactionAt, ok := cleanCodexReactionAt(reactions, completedAt)
	if !ok {
		return aiReviewNode{}, false, true
	}
	return aiReviewNode{
		State:         "COMMENTED",
		AuthorLogin:   comment.AuthorLogin,
		AuthorType:    comment.AuthorType,
		CommentCount:  0,
		ExplicitClean: true,
		OccurredAt:    reactionAt,
		CommitOID:     headRefOID,
	}, true, true
}

func cleanCodexReactionAt(reactions []aiReviewReaction, completedAt time.Time) (time.Time, bool) {
	var latest time.Time
	for _, reaction := range reactions {
		if !isCodexReviewer(reaction.AuthorLogin) || !reaction.OccurredAt.After(completedAt) {
			continue
		}
		if reaction.OccurredAt.After(latest) {
			latest = reaction.OccurredAt
		}
	}
	return latest, !latest.IsZero()
}

func isCodexReviewer(login string) bool {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(login)), "[bot]") == "chatgpt-codex-connector"
}

func sortAIReviewsChronologically(reviews []aiReviewNode) {
	sort.SliceStable(reviews, func(i, j int) bool {
		left, right := reviews[i].OccurredAt, reviews[j].OccurredAt
		if left.IsZero() {
			return !right.IsZero()
		}
		if right.IsZero() {
			return false
		}
		return left.Before(right)
	})
}

// latestAIReviewIsClean returns true when the most recent bot-authored review
// is an explicit clean pass. Copilot COMMENTED reviews require their verdict
// heading; other reviewers retain the state and comment-count contract.
func latestAIReviewIsClean(reviews []aiReviewNode) bool {
	hasBotReview := false
	clean := false
	for _, r := range reviews {
		if !isAIReviewer(r.AuthorLogin) && r.AuthorType != "Bot" {
			continue
		}
		hasBotReview = true
		switch strings.ToUpper(r.State) {
		case "APPROVED":
			clean = r.CommentCount == 0
		case "COMMENTED":
			switch {
			case isCopilotReviewer(r.AuthorLogin):
				clean = copilotReviewIsClean(r.ReviewBody)
			case isCodexReviewer(r.AuthorLogin):
				clean = r.ExplicitClean
			default:
				clean = r.CommentCount == 0
			}
		default:
			clean = false
		}
	}
	return hasBotReview && clean
}

// isAIReviewClean returns true when AI review is effectively clear for the bang
// marker: no unresolved AI threads, and either the latest bot review left zero
// comments or every AI-authored thread from earlier findings has been resolved
// (detectAIReview reports "pass").
func isAIReviewClean(reviews []aiReviewNode, threads []aiReviewThread) bool {
	if hasUnresolvedAIThreads(threads) {
		return false
	}
	if latestAIReviewIsClean(reviews) {
		return true
	}
	if latest, ok := latestAIReview(reviews); ok && isCopilotReviewer(latest.AuthorLogin) {
		// Copilot can pass with Low findings or an unconfirmed verdict, so a
		// passing result does not by itself earn its clean marker.
		return false
	}
	return detectAIReview(reviews, threads) == "pass" && allAIThreadsResolved(threads)
}

// allAIThreadsResolved returns true when at least one AI-authored thread exists
// and every such thread is resolved.
func allAIThreadsResolved(threads []aiReviewThread) bool {
	aiThreadCount := 0
	for _, t := range threads {
		if !isAIReviewer(t.AuthorLogin) && t.AuthorType != "Bot" {
			continue
		}
		aiThreadCount++
		if !t.IsResolved {
			return false
		}
	}
	return aiThreadCount > 0
}

func hasUnresolvedAIThreads(threads []aiReviewThread) bool {
	for _, t := range threads {
		if (isAIReviewer(t.AuthorLogin) || t.AuthorType == "Bot") && !t.IsResolved {
			return true
		}
	}
	return false
}

// detectAIReview determines the AI review status from the latest bot review and
// thread resolution. A later clean review supersedes older findings, but an
// unresolved AI-authored thread still forces a failure unless Copilot listed it
// as Low severity.
func detectAIReview(reviews []aiReviewNode, threads []aiReviewThread) string {
	latest, ok := latestAIReview(reviews)
	if !ok {
		return "-"
	}
	if hasBlockingAIThreads(threads) {
		return "fail"
	}

	switch strings.ToUpper(latest.State) {
	case "APPROVED", "COMMENTED", "CHANGES_REQUESTED":
		if aiReviewPasses(latest, threads) {
			return "pass"
		}
		return "fail"
	default:
		return "-"
	}
}

func aiReviewPasses(review aiReviewNode, threads []aiReviewThread) bool {
	if isCopilotReviewer(review.AuthorLogin) && copilotReviewBlocks(review.ReviewBody) {
		return false
	}
	switch strings.ToUpper(review.State) {
	case "APPROVED":
		return review.CommentCount == 0 || allAIThreadsResolved(threads)
	case "COMMENTED":
		return commentedAIReviewPasses(review, threads)
	case "CHANGES_REQUESTED":
		return allAIThreadsResolved(threads)
	default:
		return false
	}
}

func commentedAIReviewPasses(review aiReviewNode, threads []aiReviewThread) bool {
	switch {
	case isCopilotReviewer(review.AuthorLogin):
		return copilotReviewPasses(review.ReviewBody)
	case isCodexReviewer(review.AuthorLogin):
		return review.ExplicitClean || allAIThreadsResolved(threads)
	default:
		return review.CommentCount == 0 || allAIThreadsResolved(threads)
	}
}

func latestAIReview(reviews []aiReviewNode) (aiReviewNode, bool) {
	for i := len(reviews) - 1; i >= 0; i-- {
		review := reviews[i]
		if isAIReviewer(review.AuthorLogin) || review.AuthorType == "Bot" {
			return review, true
		}
	}
	return aiReviewNode{}, false
}

// graphQLError is one entry from a GraphQL response's errors array.
type graphQLError struct {
	Message string            `json:"message"`
	Path    []json.RawMessage `json:"path"`
}

// parseSupplementalResponse parses the batch envelope into per-PR info plus
// the aliases the GraphQL errors paths mark as failed. GitHub nulls the
// affected field or object for every error and reports its path, so an error
// path through an alias means that alias's data must stay unavailable even
// when a parseable object survived.
func parseSupplementalResponse(data []byte) (map[int]prSupplementalInfo, map[int]bool, error) {
	var resp struct {
		Data struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, nil, err
	}
	result := make(map[int]prSupplementalInfo)
	for _, raw := range resp.Data.Repository {
		if !supplementalConnectionsPresent(raw) {
			continue
		}
		num, info, ok := parsePRSupplementalNode(raw)
		if !ok {
			continue
		}
		result[num] = info
	}
	return result, aliasesFromErrors(resp.Errors), nil
}

// aliasesFromErrors walks each GraphQL error path for the batch's prN alias
// names, because an error path through an alias marks that alias's data as
// failed even when the object survived the failure. An error confined to
// closingIssuesReferences is excluded: that connection keeps its own
// per-field availability flag, so the healthy review and thread data stays
// rendered instead of being replaced wholesale with unknown columns.
func aliasesFromErrors(errors []graphQLError) map[int]bool {
	errored := make(map[int]bool)
	for _, gqlErr := range errors {
		alias, firstField := aliasAndFirstField(gqlErr.Path, "pr")
		if alias <= 0 || firstField == "closingIssuesReferences" {
			continue
		}
		errored[alias] = true
	}
	return errored
}

// aliasAndFirstField walks one GraphQL error path and returns the batch alias
// number plus the first field element after it, skipping index elements.
func aliasAndFirstField(path []json.RawMessage, prefix string) (int, string) {
	number := 0
	firstField := ""
	sawAlias := false
	for _, element := range path {
		var name string
		if json.Unmarshal(element, &name) != nil {
			continue
		}
		if !sawAlias {
			if n, ok := aliasNumber(name, prefix); ok {
				number = n
				sawAlias = true
			}
			continue
		}
		if firstField == "" {
			firstField = name
		}
	}
	return number, firstField
}

// aliasNumber extracts the number from a batch alias like "pr333" or
// "issue332", given the batch's prefix, so PR and issue enrichment share one
// error-path parser.
func aliasNumber(alias, prefix string) (int, bool) {
	if !strings.HasPrefix(alias, prefix) {
		return 0, false
	}
	number, err := strconv.Atoi(strings.TrimPrefix(alias, prefix))
	if err != nil {
		return 0, false
	}
	return number, true
}

// parsePRSupplementalNode parses a single PR's supplemental data from raw JSON.
// Returns the PR number, supplemental info, and whether parsing succeeded.
func parsePRSupplementalNode(raw json.RawMessage) (int, prSupplementalInfo, bool) {
	var prData supplementalNodeData
	if err := json.Unmarshal(raw, &prData); err != nil {
		return 0, prSupplementalInfo{}, false
	}
	if prData.Number <= 0 {
		return 0, prSupplementalInfo{}, false
	}

	formal, aiNodes, hasCurrentHeadCodexReview, hasCurrentHeadCodexSummary, latestCurrentHeadCodexAt, unattributableReview := collectAIEvidence(&prData)
	sortAIReviewsChronologically(aiNodes)
	aiThreads, unknownUnresolved := parseReviewThreadStates(&prData)

	threadsTruncated := prData.ReviewThreads.TotalCount > len(prData.ReviewThreads.Nodes)
	commentsIncomplete := connectionIncomplete(
		prData.Comments.TotalCount,
		len(prData.Comments.Nodes),
		hasCurrentHeadCodexReview,
	)
	reviewsIncomplete := connectionIncomplete(
		prData.Reviews.TotalCount,
		len(prData.Reviews.Nodes),
		sufficientReviewEvidence(formal, prData.HeadRefOID, latestCurrentHeadCodexAt),
	)
	reactionsIncomplete := hasCurrentHeadCodexSummary && !hasCurrentHeadCodexReview &&
		(prData.Reactions == nil || connectionIncomplete(prData.Reactions.TotalCount, len(prData.Reactions.Nodes), false))
	evidenceOrderAmbiguous := reviewEvidenceOrderAmbiguous(
		formal,
		prData.HeadRefOID,
		latestCurrentHeadCodexAt,
		hasCurrentHeadCodexReview,
	)
	closingIssuesAvailable := closingIssuesConnectionPresent(raw) && prData.ClosingIssuesReferences.complete()
	approvers, approvalsIncomplete := parseApproverSummary(&prData)
	incomplete := supplementalConnectionsIncomplete(
		closingIssuesAvailable,
		commentsIncomplete, threadsTruncated, reviewsIncomplete, reactionsIncomplete, approvalsIncomplete,
	)
	aiReview, aiClean := summarizeSupplementalReviews(
		aiNodes,
		aiThreads,
		prData.HeadRefOID,
		anyConnectionTruncated(commentsIncomplete, threadsTruncated, reviewsIncomplete, reactionsIncomplete, evidenceOrderAmbiguous) || unknownUnresolved || unattributableReview,
	)

	return prData.Number, prSupplementalInfo{
		Threads: reviewThreadInfo{
			Total:    prData.ReviewThreads.TotalCount,
			Resolved: countResolvedThreads(aiThreads),
		},
		ThreadsTruncated:       threadsTruncated,
		ClosingIssues:          closingIssueNodes(prData.ClosingIssuesReferences),
		ClosingIssuesAvailable: closingIssuesAvailable,
		AIReview:               aiReview,
		AIClean:                aiClean,
		HasUnresolvedAIThreads: hasUnresolvedAIThreads(aiThreads),
		Approvals:              len(approvers),
		Approvers:              approvers,
		ApprovalsIncomplete:    approvalsIncomplete,
		Incomplete:             incomplete,
		EvidenceAmbiguous:      evidenceOrderAmbiguous,
		UnattributableEvidence: unknownUnresolved || unattributableReview,
	}, true
}

// collectAIEvidence gathers the formal reviews plus any current-head Codex
// conversation receipt, so both evidence sources feed one chronological list.
func collectAIEvidence(prData *supplementalNodeData) (formal, aiNodes []aiReviewNode, hasCurrentHeadCodexReview, hasCurrentHeadCodexSummary bool, latestCurrentHeadCodexAt time.Time, unattributableReview bool) {
	for _, r := range prData.Reviews.Nodes {
		if (r.Author.Login == "" && r.Author.Typename == "") || r.State == "" {
			// A review without attributable authorship or a state may be bot
			// evidence that cannot be classified. A PENDING review carries no
			// commit by design, so commit absence stays legitimate.
			unattributableReview = true
		}
		formal = append(formal, aiReviewNode{
			State:        r.State,
			AuthorLogin:  r.Author.Login,
			AuthorType:   r.Author.Typename,
			ReviewBody:   r.Body,
			CommentCount: r.Comments.TotalCount,
			OccurredAt:   r.SubmittedAt,
			CommitOID:    r.Commit.OID,
		})
	}
	aiNodes = append([]aiReviewNode(nil), formal...)
	reactions := parseCodexReactions(prData.Reactions)
	for _, comment := range prData.Comments.Nodes {
		node, ok, summary := codexReviewNode(aiReviewComment{
			Body:        comment.Body,
			AuthorLogin: comment.Author.Login,
			AuthorType:  comment.Author.Typename,
			OccurredAt:  comment.CreatedAt,
		}, reactions, prData.HeadRefOID)
		hasCurrentHeadCodexSummary = hasCurrentHeadCodexSummary || summary
		if !ok {
			continue
		}
		aiNodes = append(aiNodes, node)
		hasCurrentHeadCodexReview = true
		if node.OccurredAt.After(latestCurrentHeadCodexAt) {
			latestCurrentHeadCodexAt = node.OccurredAt
		}
	}
	return formal, aiNodes, hasCurrentHeadCodexReview, hasCurrentHeadCodexSummary, latestCurrentHeadCodexAt, unattributableReview
}

func parseCodexReactions(connection *reactionConnection) []aiReviewReaction {
	if connection == nil {
		return nil
	}
	reactions := make([]aiReviewReaction, 0, len(connection.Nodes))
	for _, reaction := range connection.Nodes {
		reactions = append(reactions, aiReviewReaction{
			AuthorLogin: reaction.User.Login,
			AuthorType:  reaction.User.Typename,
			OccurredAt:  reaction.CreatedAt,
		})
	}
	return reactions
}

// parseReviewThreadStates maps review threads to their resolution state and
// first-comment authorship, flagging unresolved threads whose author cannot
// be attributed so AI status stays unknown for them.
func parseReviewThreadStates(prData *supplementalNodeData) ([]aiReviewThread, bool) {
	aiThreads := make([]aiReviewThread, 0, len(prData.ReviewThreads.Nodes))
	unknownUnresolved := false
	for _, t := range prData.ReviewThreads.Nodes {
		var login, authorType, commentID string
		if len(t.Comments.Nodes) > 0 {
			login = t.Comments.Nodes[0].Author.Login
			authorType = t.Comments.Nodes[0].Author.Typename
			commentID = t.Comments.Nodes[0].FullDatabaseID
		}
		unknown := login == "" && authorType == ""
		if unknown && !t.IsResolved {
			unknownUnresolved = true
		}
		aiThreads = append(aiThreads, aiReviewThread{
			AuthorLogin: login,
			AuthorType:  authorType,
			IsResolved:  t.IsResolved,
			CommentID:   commentID,
		})
	}
	return aiThreads, unknownUnresolved
}

func parseApproverSummary(prData *supplementalNodeData) ([]approverApproval, bool) {
	incomplete := prData.ApprovedReviews.PageInfo.HasPreviousPage ||
		connectionIncomplete(prData.ApprovedReviews.TotalCount, len(prData.ApprovedReviews.Nodes), false)
	if incomplete {
		return nil, true
	}
	return approverApprovals(prData), false
}

// approverApprovals returns only reviewers whose latest opinionated review is
// approval. It defensively deduplicates logins even though GitHub's
// latestOpinionatedReviews connection normally returns one review per user.
func approverApprovals(prData *supplementalNodeData) []approverApproval {
	latest := make(map[string]decisiveApprovalReview, len(prData.ApprovedReviews.Nodes))
	for _, review := range prData.ApprovedReviews.Nodes {
		login := strings.TrimSpace(review.Author.Login)
		if login == "" {
			continue
		}
		key := strings.ToLower(login)
		candidate := decisiveApprovalReview{
			approverApproval: approverApproval{Login: login, ApprovedAt: review.SubmittedAt},
			State:            review.State,
		}
		current, found := latest[key]
		if !found || preferApprovalDecision(candidate, current) {
			latest[key] = candidate
		}
	}

	approvers := make([]approverApproval, 0, len(latest))
	for _, review := range latest {
		if strings.EqualFold(review.State, "APPROVED") {
			approvers = append(approvers, review.approverApproval)
		}
	}
	sort.Slice(approvers, func(i, j int) bool {
		if approvers[i].ApprovedAt.Equal(approvers[j].ApprovedAt) {
			return strings.ToLower(approvers[i].Login) < strings.ToLower(approvers[j].Login)
		}
		return approvers[i].ApprovedAt.After(approvers[j].ApprovedAt)
	})
	return approvers
}

func preferApprovalDecision(candidate, current decisiveApprovalReview) bool {
	if !candidate.ApprovedAt.Equal(current.ApprovedAt) {
		return candidate.ApprovedAt.After(current.ApprovedAt)
	}
	candidateApproved := strings.EqualFold(candidate.State, "APPROVED")
	currentApproved := strings.EqualFold(current.State, "APPROVED")
	if candidateApproved != currentApproved {
		return !candidateApproved
	}
	return strings.EqualFold(candidate.State, current.State) && candidate.Login < current.Login
}

// supplementalNodeData mirrors the supplemental GraphQL query's per-PR shape.
type supplementalNodeData struct {
	Number                  int                        `json:"number"`
	HeadRefOID              string                     `json:"headRefOid"`
	ClosingIssuesReferences *linkedReferenceConnection `json:"closingIssuesReferences"`
	Comments                struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"createdAt"`
			Author    struct {
				Login    string `json:"login"`
				Typename string `json:"__typename"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
	Reactions     *reactionConnection `json:"reactions"`
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
	Reviews struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			State       string    `json:"state"`
			Body        string    `json:"body"`
			SubmittedAt time.Time `json:"submittedAt"`
			Commit      struct {
				OID string `json:"oid"`
			} `json:"commit"`
			Author struct {
				Login    string `json:"login"`
				Typename string `json:"__typename"`
			} `json:"author"`
			Comments struct {
				TotalCount int `json:"totalCount"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviews"`
	ApprovedReviews struct {
		TotalCount int `json:"totalCount"`
		PageInfo   struct {
			HasPreviousPage bool `json:"hasPreviousPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			State       string    `json:"state"`
			SubmittedAt time.Time `json:"submittedAt"`
			Author      struct {
				Login    string `json:"login"`
				Typename string `json:"__typename"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"approvedReviews"`
}

type reactionConnection struct {
	TotalCount int `json:"totalCount"`
	Nodes      []struct {
		CreatedAt time.Time `json:"createdAt"`
		User      struct {
			Login    string `json:"login"`
			Typename string `json:"__typename"`
		} `json:"user"`
	} `json:"nodes"`
}

// supplementalConnectionsPresent reports whether every connection the
// supplemental summary consumes was fully returned for this PR. GitHub nulls
// an individual field of an otherwise valid object when that sub-query fails
// and can also return a connection object with absent counts, so an entry
// with an incomplete connection cannot prove its threads, comments, or
// reviews and must stay unavailable. closingIssuesReferences keeps its own
// per-field availability flag.
func supplementalConnectionsPresent(raw json.RawMessage) bool {
	var fields struct {
		Comments        json.RawMessage `json:"comments"`
		Reactions       json.RawMessage `json:"reactions"`
		ReviewThreads   json.RawMessage `json:"reviewThreads"`
		Reviews         json.RawMessage `json:"reviews"`
		ApprovedReviews json.RawMessage `json:"approvedReviews"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}
	return countedConnectionPresent(fields.Comments) &&
		countedConnectionPresent(fields.Reactions) &&
		reviewThreadsPresent(fields.ReviewThreads) &&
		countedConnectionPresent(fields.Reviews) &&
		nodeConnectionPresent(fields.ApprovedReviews)
}

// countedConnectionPresent requires both totalCount and nodes; a missing
// count would default the summary to a false zero.
func countedConnectionPresent(raw json.RawMessage) bool {
	var connection struct {
		TotalCount json.RawMessage `json:"totalCount"`
		Nodes      json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &connection); err != nil {
		return false
	}
	return jsonValuePresent(connection.TotalCount) && jsonValuePresent(connection.Nodes)
}

// reviewThreadsPresent additionally validates the nested shape that drives
// AI classification: every thread node must be an object carrying isResolved
// and a comments connection that includes its nodes list, or a missing node
// list would silently classify an unresolvable thread as resolved non-AI.
func reviewThreadsPresent(raw json.RawMessage) bool {
	if !countedConnectionPresent(raw) {
		return false
	}
	var connection struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &connection); err != nil {
		return false
	}
	for _, node := range connection.Nodes {
		var thread struct {
			IsResolved json.RawMessage `json:"isResolved"`
			Comments   struct {
				Nodes json.RawMessage `json:"nodes"`
			} `json:"comments"`
		}
		if err := json.Unmarshal(node, &thread); err != nil {
			return false
		}
		if !jsonValuePresent(thread.IsResolved) || !jsonValuePresent(thread.Comments.Nodes) {
			return false
		}
	}
	return true
}

// nodeConnectionPresent requires only nodes; approvedReviews carries no
// count in the supplemental query.
func nodeConnectionPresent(raw json.RawMessage) bool {
	var connection struct {
		Nodes json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &connection); err != nil {
		return false
	}
	return jsonValuePresent(connection.Nodes)
}

func jsonValuePresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func closingIssueNodes(connection *linkedReferenceConnection) []linkedReference {
	if !connection.complete() {
		return nil
	}
	return connection.Nodes
}

// supplementalConnectionsIncomplete reports whether any consumed connection
// was truncated or absent, so rendered unknown columns need a diagnostic.
func supplementalConnectionsIncomplete(closingIssuesAvailable bool, truncated ...bool) bool {
	return anyConnectionTruncated(truncated...) || !closingIssuesAvailable
}

// closingIssuesConnectionPresent requires the connection's totalCount and
// nodes keys, so a failed closingIssuesReferences sub-query is not mistaken
// for a legitimately empty relationship.
func closingIssuesConnectionPresent(raw json.RawMessage) bool {
	var fields struct {
		ClosingIssues json.RawMessage `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}
	return countedConnectionPresent(fields.ClosingIssues)
}

func anyConnectionTruncated(truncated ...bool) bool {
	for _, value := range truncated {
		if value {
			return true
		}
	}
	return false
}

func connectionIncomplete(totalCount, fetchedCount int, sufficientEvidence bool) bool {
	return totalCount > fetchedCount && !sufficientEvidence
}

func summarizeSupplementalReviews(
	aiNodes []aiReviewNode,
	aiThreads []aiReviewThread,
	headRefOID string,
	aiIncomplete bool,
) (aiReview string, aiClean bool) {
	currentReviews := currentHeadReviewNodes(aiNodes, headRefOID)
	markedThreads := markLowSeverityThreads(aiNodes, aiThreads)
	aiReview = detectAIReview(currentReviews, markedThreads)
	aiClean = isAIReviewClean(currentReviews, markedThreads)
	if aiIncomplete && aiReview != "fail" {
		// Unknown evidence widens an inconclusive result to unknown, but it
		// cannot overturn a computed failure.
		aiReview = "?"
		aiClean = false
	}
	return
}

func boolPtr(b bool) *bool { return &b }
