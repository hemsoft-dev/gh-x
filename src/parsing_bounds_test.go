package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMergeMonitorScopeMetadataKeepsTheTightestRateLimit(t *testing.T) {
	earlier := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	result := &monitorFetchResult{Accessible: map[string]bool{}, RateRemaining: 10, RateResetAt: earlier}
	mergeMonitorScopeMetadata(result, &monitorFetchResult{RateRemaining: 10, RateResetAt: later})
	if !result.RateResetAt.Equal(earlier) {
		t.Fatal("an equal remaining budget keeps the first reset time")
	}
	mergeMonitorScopeMetadata(result, &monitorFetchResult{RateRemaining: 4, RateResetAt: later})
	if result.RateRemaining != 4 || !result.RateResetAt.Equal(later) {
		t.Fatal("a lower budget wins")
	}
}

func TestDiffMonitorScopeComparesUnfetchedSnapshotsWithoutErrors(t *testing.T) {
	identities := map[string]monitorHostIdentity{"github.com": {Viewer: "me"}}
	previous := &monitorFetchResult{HostIdentities: identities, PRSections: []monitorSectionData{{Rows: []monitorRow{monitorRowForTest("o/r", 1, "open")}}}}
	current := &monitorFetchResult{HostIdentities: identities, PRSections: []monitorSectionData{{Rows: []monitorRow{monitorRowForTest("o/r", 1, "closed")}}}}
	if changes := diffMonitorScope(previous, current); len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	previous.Error = "unavailable"
	if changes := diffMonitorScope(previous, current); changes != nil {
		t.Fatalf("a failed first snapshot has nothing to compare: %+v", changes)
	}
}

func TestParseIssueHierarchyAliasRejectsIssueZero(t *testing.T) {
	if _, _, _, ok := parseIssueHierarchyAlias("issue0", []byte(`{"number":0}`)); ok {
		t.Fatal("issue number 0 is not an issue")
	}
	if number, _, _, ok := parseIssueHierarchyAlias("issue3", []byte(`{"number":3}`)); !ok || number != 3 {
		t.Fatalf("issue 3 = %d %v", number, ok)
	}
}

func TestParseIssueRelationshipsSkipsIssueZero(t *testing.T) {
	data := []byte(`{"data":{"repository":{
		"issue0":{"number":0,"closedByPullRequestsReferences":{"totalCount":0,"nodes":[]}},
		"issue1":{"number":1,"closedByPullRequestsReferences":{"totalCount":0,"nodes":[]}}}}}`)
	result, _, err := parseIssueRelationships(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result[0]; ok || len(result) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecodeProjectMembershipNeedsAProjectNumber(t *testing.T) {
	if _, err := decodeProjectMembership([]byte(`{"project":{"id":"P","owner":{"login":"o"},"number":0}}`)); err == nil {
		t.Fatal("project number 0 is incomplete")
	}
	membership, err := decodeProjectMembership([]byte(`{"project":{"id":"P","owner":{"login":"o"},"number":2}}`))
	if err != nil || membership.Project.Number != 2 {
		t.Fatalf("membership = %+v err %v", membership, err)
	}
}

func TestFetchMergedAcquisitionAcceptsTheMaximumLimit(t *testing.T) {
	saved := ghExecFunc
	t.Cleanup(func() { ghExecFunc = saved })
	ghExecFunc = func(...string) (bytes.Buffer, bytes.Buffer, error) {
		return bytes.Buffer{}, bytes.Buffer{}, errors.New("no network in tests")
	}
	_, err := fetchMergedAcquisition(listOptions{limit: recentMergedCandidateLimit, repo: "o/r"}, false)
	if err != nil && strings.Contains(err.Error(), "limit must be between") {
		t.Fatalf("the maximum limit is allowed: %v", err)
	}
	if _, err := fetchMergedAcquisition(listOptions{limit: recentMergedCandidateLimit + 1, repo: "o/r"}, false); err == nil || !strings.Contains(err.Error(), "limit must be between") {
		t.Fatalf("a limit past the maximum is rejected: %v", err)
	}
}
