package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// One scanner owns fetched pages in update order. Sorting copies for selection
// never changes its boundary or resets the continuation cursor.
type mergedCandidateScanner struct {
	client              *mergedQueryClient
	owner, name, cursor string
	rows                []pullRequest
	exhausted           bool
	pages               int
	connection          string
	seen                map[string]bool
}

func (scan *mergedCandidateScanner) loadTo(limit int) error {
	for len(scan.rows) < limit && !scan.exhausted {
		count := min(100, limit-len(scan.rows))
		connection := scan.connection
		if connection == "" {
			connection = "pullRequests"
		}
		page, err := fetchMergedConnectionPage(scan.client, scan.owner, scan.name, scan.cursor, count, connection)
		if err != nil {
			return err
		}
		if len(page.Nodes) > count {
			return fmt.Errorf("merged candidate page exceeded requested size")
		}
		if err := scan.advance(page); err != nil {
			return err
		}
		scan.pages++
		for _, node := range page.Nodes {
			scan.rows = append(scan.rows, node.pullRequest)
		}
	}
	return nil
}

func (scan *mergedCandidateScanner) advance(page *mergedPRConnection) error {
	if !page.PageInfo.HasNextPage {
		scan.exhausted = true
		return nil
	}
	cursor := page.PageInfo.EndCursor
	if len(page.Nodes) == 0 || cursor == "" || cursor == scan.cursor || scan.seen[cursor] {
		return fmt.Errorf("merged pull request pagination did not advance")
	}
	if scan.seen == nil {
		scan.seen = make(map[string]bool)
	}
	scan.seen[cursor] = true
	scan.cursor = cursor
	return nil
}

func selectMergedCandidates(scan *mergedCandidateScanner, desired int) ([]pullRequest, error) {
	target := max(desired, recentMergedCandidateFloor)
	for {
		if err := scan.loadTo(target); err != nil {
			return nil, err
		}
		selected := append([]pullRequest(nil), scan.rows...)
		sort.SliceStable(selected, func(i, j int) bool { return selected[i].MergedAt.After(selected[j].MergedAt) })
		if len(selected) < desired {
			return selected, nil
		}
		selected = selected[:desired]
		if scan.exhausted || !scan.rows[len(scan.rows)-1].UpdatedAt.After(selected[len(selected)-1].MergedAt) {
			return selected, nil
		}
		if target == recentMergedCandidateLimit {
			return nil, fmt.Errorf("cannot determine the %d most recent merges from %d updated pull requests", desired, target)
		}
		target = min(target*2, recentMergedCandidateLimit)
	}
}

type mergedAcquisition struct {
	rows         []pullRequest
	supplemental prSupplementalData
	owner, name  string
}

func fetchMergedAcquisition(options listOptions, supplemental bool) (mergedAcquisition, error) {
	result := mergedAcquisition{}
	if options.limit < 1 || options.limit > recentMergedCandidateLimit {
		return result, fmt.Errorf("recently merged pull request limit must be between 1 and %d", recentMergedCandidateLimit)
	}
	owner, name, err := resolveRepo(options.repo)
	if err != nil {
		return result, err
	}
	result.owner, result.name = owner, name
	client := mergedQueryClient{host: repositoryTargetHost(options.repo)}
	scan := mergedCandidateScanner{client: &client, owner: owner, name: name}
	selected, err := selectMergedCandidates(&scan, options.limit)
	if err != nil {
		return result, err
	}
	if err := validateMergedSnapshot(&scan); err != nil {
		return result, err
	}
	result.rows, result.supplemental, err = enrichMergedSelection(&client, owner, name, selected, supplemental)
	return result, err
}

// Combined details and supplemental evidence fit in one selected-row query.
// Twenty aliases keep the expanded fragments below Windows' command-line cap.
const mergedSelectionBatchSize = 20

func enrichMergedSelection(client *mergedQueryClient, owner, name string, selected []pullRequest, supplemental bool) ([]pullRequest, prSupplementalData, error) {
	result := prSupplementalData{Info: map[int]prSupplementalInfo{}, Unavailable: map[int]bool{}}
	var rows []pullRequest
	for start := 0; start < len(selected); start += mergedSelectionBatchSize {
		batch := selected[start:min(start+mergedSelectionBatchSize, len(selected))]
		fetched, info, err := fetchSelectedMergedBatch(client, owner, name, batch, supplemental)
		if err != nil {
			return nil, result, err
		}
		rows = append(rows, fetched...)
		for number, item := range info.Info {
			result.Info[number] = item
		}
		for number := range info.Unavailable {
			result.Unavailable[number] = true
		}
		result.Err = retainFirstError(result.Err, info.Err)
	}
	result.Err = joinSupplementalReasons(firstPartialFetchError(result.Err, result.Unavailable, len(selected), "pull requests"), incompleteConnectionError(result.Info), evidenceAmbiguityError(result.Info), unattributableEvidenceError(result.Info))
	return rows, result, nil
}

func fetchSelectedMergedBatch(client *mergedQueryClient, owner, name string, selected []pullRequest, supplemental bool) ([]pullRequest, prSupplementalData, error) {
	numbers := make([]int, len(selected))
	for i, row := range selected {
		numbers[i] = row.Number
	}
	fields := mergedPRFields
	if supplemental {
		fields += "\n" + prSupplementalFields
	}
	data, fetchErr := client.query(buildPullRequestBatchQuery(owner, name, numbers, fields))
	supplementalData := data
	data, recoveryErr := recoverMergedBatch(client, owner, name, data, numbers, supplemental, fetchErr)
	if recoveryErr != nil {
		return nil, prSupplementalData{}, recoveryErr
	}
	nodes, err := parseSelectedMerged(data, numbers)
	if err != nil {
		return nil, prSupplementalData{}, retainFirstError(fetchErr, err)
	}
	rows := make([]pullRequest, 0, len(nodes))
	for _, node := range nodes {
		if err := completeMergedChecks(client, &node); err != nil {
			return nil, prSupplementalData{}, err
		}
		rows = append(rows, node.pullRequestRow())
	}
	info := prSupplementalData{}
	if supplemental {
		info.Info, info.Unavailable, info.Err = parseSupplementalBatch(supplementalData, fetchErr, numbers)
	}
	return rows, info, nil
}

func parseSelectedMerged(data []byte, numbers []int) ([]mergedPRNode, error) {
	var response selectedMergedResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode selected merged pull requests: %w", err)
	}
	for _, entry := range response.Errors {
		if !mergedSupplementalOnlyError(entry) {
			return nil, fmt.Errorf("selected merged pull requests: %s", entry.Message)
		}
	}
	nodes := make([]mergedPRNode, 0, len(numbers))
	for _, number := range numbers {
		node := response.Data.Repository[fmt.Sprintf("pr%d", number)]
		if node == nil || node.Number != number {
			return nil, fmt.Errorf("selected merged pull request #%d unavailable", number)
		}
		nodes = append(nodes, *node)
	}
	return nodes, nil
}

func mergedSupplementalOnlyError(entry graphQLError) bool {
	number, field := aliasAndFirstField(entry.Path, "pr")
	return number > 0 && slices.Contains([]string{"headRefOid", "closingIssuesReferences", "comments", "reactions", "reviewThreads", "reviews", "approvedReviews"}, field)
}

func onlyMergedSupplementalErrors(data []byte) bool {
	var envelope struct {
		Errors []graphQLError `json:"errors"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Errors) == 0 {
		return false
	}
	for _, entry := range envelope.Errors {
		if !mergedSupplementalOnlyError(entry) {
			return false
		}
	}
	return true
}

type selectedMergedResponse struct {
	Data struct {
		Repository map[string]*mergedPRNode `json:"repository"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}
