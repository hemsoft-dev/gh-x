package main

import (
	"strings"
	"time"
)

// applyPassiveDispositions recognizes HemSoft's explicit, head-bound thread
// dispositions. It never interprets the rationale's prose as a verdict.
// The authenticated resolver and reply author supply the disposition authority;
// the selected reviewer's exact-head PASS supplies independent review evidence.
func applyPassiveDispositions(data *supplementalNodeData, reviews []aiReviewNode) []aiReviewNode {
	if !completePassiveDispositionContext(data) {
		return reviews
	}
	current := currentHeadReviewNodes(reviews, data.HeadRefOID)
	copilot, index := latestCurrentCopilot(reviews, data.HeadRefOID)
	if index < 0 || !disposableCopilotReview(copilot) {
		return reviews
	}
	overview := parseCopilotOverview(copilot.ReviewBody)
	if !overview.countsConsistent || !overview.hasActiveFindings() || !exactHeadSFLPass(current, copilot.OccurredAt, data.HeadRefOID) {
		return reviews
	}
	disposed := disposedCopilotThreads(data, copilot.OccurredAt)
	if !allCopilotFindingsDisposed(overview, disposed) {
		return reviews
	}
	result := append([]aiReviewNode(nil), reviews...)
	result[index].PassiveDisposed = true
	return result
}

func exactHeadSFLPass(reviews []aiReviewNode, after time.Time, head string) bool {
	// Only the latest standing SFL review can supply this authority.
	for i := len(reviews) - 1; i >= 0; i-- {
		r := reviews[i]
		if strings.TrimSuffix(strings.ToLower(r.AuthorLogin), "[bot]") != "hemsoft-sfl-reviewer" {
			continue
		}
		return r.AuthorType == "Bot" && r.State == "COMMENTED" && r.OccurredAt.After(after) &&
			strings.HasPrefix(r.ReviewBody, "## ✅ Verdict: PASS • Round ") &&
			strings.Contains(r.ReviewBody, "sha="+head+" -->")
	}
	return false
}

func latestCurrentCopilot(reviews []aiReviewNode, head string) (aiReviewNode, int) {
	for i := len(reviews) - 1; i >= 0; i-- {
		r := reviews[i]
		if isCopilotReviewer(r.AuthorLogin) && strings.EqualFold(r.CommitOID, head) && !strings.EqualFold(r.State, "DISMISSED") {
			return r, i
		}
	}
	return aiReviewNode{}, -1
}

func disposableCopilotReview(r aiReviewNode) bool {
	return strings.EqualFold(r.State, "COMMENTED") && !r.OccurredAt.IsZero() && classifyCopilotReview(r.ReviewBody) != copilotVerdictUnknown
}

func disposedCopilotThreads(data *supplementalNodeData, after time.Time) map[string]bool {
	disposed := make(map[string]bool)
	for _, thread := range data.ReviewThreads.Nodes {
		if !thread.IsResolved {
			return nil
		}
		if !strings.EqualFold(thread.ResolvedBy.Login, "HemSoft") ||
			thread.Comments.TotalCount != len(thread.Comments.Nodes) || len(thread.Comments.Nodes) < 2 {
			continue
		}
		first := thread.Comments.Nodes[0]
		if !isCopilotReviewer(first.Author.Login) {
			continue
		}
		for _, reply := range thread.Comments.Nodes[1:] {
			if trustedDispositionReply(reply.Author.Login, reply.Author.Typename, reply.CreatedAt, after, reply.Body, data.HeadRefOID) {
				disposed[first.FullDatabaseID] = true
			}
		}
	}

	return disposed
}

func trustedDispositionReply(login, kind string, at, after time.Time, body, head string) bool {
	return strings.EqualFold(login, "HemSoft") && kind == "User" && at.After(after) && strings.Contains(body, "`"+head+"`")
}

func completePassiveDispositionContext(data *supplementalNodeData) bool {
	return strings.EqualFold(data.Repository.Owner.Login, "hemsoft-dev") &&
		len(data.HeadRefOID) == 40 && data.Reviews.TotalCount == len(data.Reviews.Nodes) &&
		data.ReviewThreads.TotalCount == len(data.ReviewThreads.Nodes)
}

func allCopilotFindingsDisposed(overview copilotOverview, disposed map[string]bool) bool {
	matched := false
	for _, finding := range overview.findings {
		if !finding.active || finding.tally {
			continue
		}
		if finding.discussionID == "" || !disposed[finding.discussionID] {
			return false
		}
		matched = true
	}
	return matched
}
