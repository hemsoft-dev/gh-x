package main

import (
	"encoding/json"
	"fmt"
)

// Updates can move an unseen PR ahead of a saved UPDATED_AT cursor. Recheck
// the leading timestamp cohort after any multi-page acquisition, including all
// ties and one boundary row. Keep discovery pages; never accept a changed scan.
func validateMergedSnapshot(scan *mergedCandidateScanner) error {
	if scan.pages < 2 || len(scan.rows) == 0 {
		return nil
	}
	expected := mergedLeadingCohort(scan.rows)
	// A multi-request consistency probe would itself be vulnerable to cursor
	// movement. Accept only a single response containing the complete cohort.
	if len(expected) > 100 {
		return fmt.Errorf("merged update-time group exceeds one consistency page; retry with a fresh acquisition")
	}
	page, err := fetchMergedConnectionPage(scan.client, scan.owner, scan.name, "", min(100, len(expected)+1), "freshness")
	if err != nil {
		return err
	}
	if len(page.Nodes) > min(100, len(expected)+1) {
		return fmt.Errorf("merged consistency page exceeded requested size")
	}
	var rows []pullRequest
	for _, node := range page.Nodes {
		rows = append(rows, node.pullRequest)
	}
	actual := mergedLeadingCohort(rows)
	if page.PageInfo.HasNextPage && len(actual) == len(rows) {
		return fmt.Errorf("merged update-time group could not be verified in one consistency page")
	}
	if !sameMergedCohort(expected, actual) {
		return fmt.Errorf("merged candidates changed during pagination; retry with a fresh acquisition")
	}

	return nil
}

func mergedLeadingCohort(rows []pullRequest) []pullRequest {
	if len(rows) == 0 {
		return nil
	}
	for i, row := range rows {
		if !row.UpdatedAt.Equal(rows[0].UpdatedAt) {
			return rows[:i]
		}
	}
	return rows
}

func sameMergedCohort(expected, actual []pullRequest) bool {
	if len(expected) != len(actual) {
		return false
	}
	rows := make(map[int]pullRequest, len(expected))
	for _, row := range expected {
		rows[row.Number] = row
	}
	for _, row := range actual {
		previous, ok := rows[row.Number]
		if !ok || !previous.UpdatedAt.Equal(row.UpdatedAt) || !previous.MergedAt.Equal(row.MergedAt) {
			return false
		}
		delete(rows, row.Number)
	}
	return len(rows) == 0
}

// A non-null supplemental field may null its parent PR alias. Retry only the
// affected aliases with metadata/check fields, retaining the original payload
// for supplemental parsing so unavailable evidence stays unavailable.
func recoverMergedMetadata(client *mergedQueryClient, owner, name string, data []byte, numbers []int) ([]byte, error) {
	if !onlyMergedSupplementalErrors(data) {
		return data, nil
	}
	var response selectedMergedResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	missing := missingMergedSupplementalAliases(response, numbers)
	if len(missing) == 0 {
		return data, nil
	}
	recovered, err := client.query(buildPullRequestBatchQuery(owner, name, missing, mergedPRFields))
	if err != nil {
		return nil, err
	}
	nodes, err := parseSelectedMerged(recovered, missing)
	if err != nil {
		return nil, err
	}
	if response.Data.Repository == nil {
		response.Data.Repository = map[string]*mergedPRNode{}
	}
	for _, node := range nodes {
		copy := node
		response.Data.Repository[fmt.Sprintf("pr%d", node.Number)] = &copy
	}
	return json.Marshal(response)
}

func missingMergedSupplementalAliases(response selectedMergedResponse, numbers []int) []int {
	affected := map[int]bool{}
	for _, entry := range response.Errors {
		number, _ := aliasAndFirstField(entry.Path, "pr")
		affected[number] = true
	}
	var missing []int
	for _, number := range numbers {
		if response.Data.Repository[fmt.Sprintf("pr%d", number)] == nil && affected[number] {
			missing = append(missing, number)
		}
	}
	return missing
}
