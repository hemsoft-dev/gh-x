package main

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const statusFailureCount = 10

type statusCacheSection struct {
	FetchedAt  time.Time `json:"fetchedAt"`
	RetryAfter time.Time `json:"retryAfter"`
}

func statusFailureFields(dashboard *statusDashboard) []*error {
	return []*error{
		&dashboard.IssuesErr, &dashboard.IssuesRelErr, &dashboard.IssuesHierarchyErr,
		&dashboard.PullRequestsErr, &dashboard.PullRequestsSuppErr, &dashboard.RequiredChecksErr,
		&dashboard.MergedPullRequestsErr, &dashboard.MergedPullRequestsSuppErr, &dashboard.MergedRequiredChecksErr,
		&dashboard.WorkflowRunsErr,
	}
}

func cacheStatusFailures(dashboard statusDashboard) []string {
	if statusDashboardCacheable(dashboard) {
		return nil
	}
	failures := make([]string, statusFailureCount)
	for i, field := range statusFailureFields(&dashboard) {
		if *field != nil {
			failures[i] = boundedSingleLine((*field).Error(), 1000)
		}
	}
	return failures
}

func restoreStatusFailures(dashboard *statusDashboard, failures []string) {
	fields := statusFailureFields(dashboard)
	for i, message := range failures {
		if message != "" {
			*fields[i] = errors.New(message)
		}
	}
}

func statusSectionErrors(dashboard statusDashboard) []error {
	return []error{
		errors.Join(dashboard.IssuesErr, dashboard.IssuesRelErr, dashboard.IssuesHierarchyErr),
		errors.Join(dashboard.PullRequestsErr, dashboard.PullRequestsSuppErr, dashboard.RequiredChecksErr),
		errors.Join(dashboard.MergedPullRequestsErr, dashboard.MergedPullRequestsSuppErr, dashboard.MergedRequiredChecksErr),
		dashboard.WorkflowRunsErr,
	}
}

func markStatusSectionFetch(dashboard *statusDashboard, section int, now time.Time) {
	if len(dashboard.remoteSections) != 4 {
		dashboard.remoteSections = make([]statusCacheSection, 4)
	}
	dashboard.remoteSections[section] = statusCacheSection{FetchedAt: now}
	dashboard.remoteCached[section] = false
}

func cacheStatusSections(dashboard statusDashboard, now time.Time) []statusCacheSection {
	sections := append([]statusCacheSection(nil), dashboard.remoteSections...)
	if len(sections) != 4 {
		sections = make([]statusCacheSection, 4)
	}
	for i, err := range statusSectionErrors(dashboard) {
		if sections[i].FetchedAt.IsZero() {
			sections[i].FetchedAt = now
		}
		if sections[i].RetryAfter.IsZero() {
			sections[i].RetryAfter = sections[i].FetchedAt.Add(statusRetryDelay(err, sections[i].FetchedAt))
		}
	}
	return sections
}

var statusRetryAfterPattern = regexp.MustCompile(`(?i)retry-after:\s*(\d+)`)
var statusResetPattern = regexp.MustCompile(`(?i)x-ratelimit-reset:\s*(\d+)`)

func statusRetryDelay(err error, now time.Time) time.Duration {
	if err == nil {
		return statusCacheTTL
	}
	var quota *quotaError
	if errors.As(err, &quota) {
		return max(time.Second, quota.Until.Sub(now))
	}
	delay := 5 * time.Second
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "rate limit") || strings.Contains(message, "429") {
		delay = time.Minute
	}
	return max(delay, statusHeaderDelay(message, now))
}

func statusHeaderDelay(message string, now time.Time) time.Duration {
	delay := time.Duration(0)
	if match := statusRetryAfterPattern.FindStringSubmatch(message); len(match) == 2 {
		seconds, err := strconv.ParseInt(match[1], 10, 32)
		if err == nil {
			delay = time.Duration(seconds) * time.Second
		}
	}
	if match := statusResetPattern.FindStringSubmatch(message); len(match) == 2 {
		seconds, err := strconv.ParseInt(match[1], 10, 64)
		if err == nil {
			delay = max(delay, time.Unix(seconds, 0).Sub(now))
		}
	}
	return delay
}

func validStatusCacheSections(sections []statusCacheSection, now time.Time) bool {
	if !statusCacheSectionShapeValid(sections, now) {
		return false
	}
	for _, section := range sections {
		if now.Before(section.RetryAfter) {
			return true
		}
	}
	return false
}

func statusCacheSectionShapeValid(sections []statusCacheSection, now time.Time) bool {
	if len(sections) != 4 {
		return false
	}
	for _, section := range sections {
		if section.FetchedAt.IsZero() || section.FetchedAt.After(now) || section.RetryAfter.Before(section.FetchedAt) {
			return false
		}
	}
	return true
}

func statusSectionsNeedFetch(sections []statusCacheSection, now time.Time) bool {
	for _, section := range sections {
		if !now.Before(section.RetryAfter) {
			return true
		}
	}
	return false
}

func retryStatusSections(dashboard *statusDashboard, options statusOptions, now time.Time, heads map[string]bool, known bool) (map[string]bool, bool) {
	session := newStatusRemoteSession(dashboard.Repository)
	sections := dashboard.remoteSections
	if !now.Before(sections[0].RetryAfter) {
		fetchStatusIssueSection(dashboard, session, now)
	}
	if !now.Before(sections[1].RetryAfter) {
		heads, known = fetchStatusOpenPRSection(dashboard, session, now)
	}
	if !now.Before(sections[2].RetryAfter) {
		fetchStatusMergedSection(dashboard, session, options.mergedLimit, now)
	}
	if !now.Before(sections[3].RetryAfter) {
		fetchStatusRunSection(dashboard, session, now)
	}
	return heads, known
}
