package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type dispositionQuery func(string) ([]byte, error)

// Ordinary acquisition stays light for all owners. Only independently eligible
// exact-head candidates receive one bounded follow-up each (not a 30-PR batch).
func enrichPassiveDispositionEvidence(owner, name string, raw []byte, infos map[int]prSupplementalInfo, unavailable map[int]bool, query dispositionQuery) error {
	if !strings.EqualFold(owner, "hemsoft-dev") {
		return nil
	}
	var envelope struct {
		Data struct{ Repository map[string]json.RawMessage }
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	var firstErr error
	for _, node := range envelope.Data.Repository {
		data := parseDispositionCandidate(node, infos, unavailable)
		if data == nil {
			continue
		}
		fields := strings.Replace(prSupplementalFields, "comments(first: 1) { nodes { fullDatabaseId author", "comments(first: 20) { totalCount nodes { fullDatabaseId body createdAt author", 1)
		response, err := query(buildPullRequestBatchQuery(owner, name, []int{data.Number}, fields))
		info, err := parseDispositionFollowup(response, err, data.Number, data.HeadRefOID)
		if err != nil {
			firstErr = retainFirstError(firstErr, err)
			continue
		}
		infos[data.Number] = info
	}
	return firstErr
}

func dispositionFetchCandidate(data *supplementalNodeData) bool {
	if !completePassiveDispositionContext(data) {
		return false
	}
	_, reviews, _, _, _, unknown := collectAIEvidence(data)
	if unknown {
		return false
	}
	sortAIReviewsChronologically(reviews)
	r, index := latestCurrentCopilot(reviews, data.HeadRefOID)
	if index < 0 || !disposableCopilotReview(r) {
		return false
	}
	overview := parseCopilotOverview(r.ReviewBody)
	if !overview.countsConsistent || !overview.blocks() || !exactHeadSFLPass(currentHeadReviewNodes(reviews, data.HeadRefOID), r.OccurredAt, data.HeadRefOID) {
		return false
	}
	return dispositionThreadsEligible(data, overview)
}

func dispositionThreadsEligible(data *supplementalNodeData, overview copilotOverview) bool {
	resolved := make(map[string]bool)
	for _, thread := range data.ReviewThreads.Nodes {
		if !thread.IsResolved {
			return false
		}
		if len(thread.Comments.Nodes) == 0 {
			continue
		}
		first := thread.Comments.Nodes[0]
		if isCopilotReviewer(first.Author.Login) && strings.EqualFold(thread.ResolvedBy.Login, "HemSoft") {
			resolved[first.FullDatabaseID] = true
		}
	}
	return allCopilotFindingsDisposed(overview, resolved)
}

func parseDispositionFollowup(raw []byte, fetchErr error, number int, head string) (prSupplementalInfo, error) {
	if fetchErr != nil {
		return prSupplementalInfo{}, fetchErr
	}
	infos, unavailable, err := parseSupplementalBatch(raw, nil, []int{number})
	if err != nil {
		return prSupplementalInfo{}, err
	}
	if unavailable[number] {
		return prSupplementalInfo{}, fmt.Errorf("PR #%d disposition evidence unavailable", number)
	}
	var envelope struct {
		Data struct{ Repository map[string]json.RawMessage }
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return prSupplementalInfo{}, err
	}
	var data supplementalNodeData
	if err := json.Unmarshal(envelope.Data.Repository[fmt.Sprintf("pr%d", number)], &data); err != nil {
		return prSupplementalInfo{}, err
	}
	if data.HeadRefOID != head {
		return prSupplementalInfo{}, fmt.Errorf("PR #%d disposition head changed", number)
	}
	return infos[number], nil
}

func parseDispositionCandidate(raw json.RawMessage, infos map[int]prSupplementalInfo, unavailable map[int]bool) *supplementalNodeData {
	var data supplementalNodeData
	if json.Unmarshal(raw, &data) != nil || unavailable[data.Number] {
		return nil
	}
	baseline, present := infos[data.Number]
	if !present || baseline.Incomplete || baseline.UnattributableEvidence || !dispositionFetchCandidate(&data) {
		return nil
	}
	return &data
}
