package main

import (
	"regexp"
	"strconv"
	"strings"
)

const copilotLowSeverityLabel = "low severity"

var (
	copilotSectionTitle    = regexp.MustCompile(`(?s)<summary><strong>(.*?)</strong></summary>`)
	copilotSeverityBadge   = regexp.MustCompile(`(?i)alt="([^"]*severity[^"]*)"`)
	copilotDiscussionLink  = regexp.MustCompile(`discussion_r(\d+)`)
	copilotActiveCount     = regexp.MustCompile(`(?i)(\d+)\s+(?:open|previously missed)\b|\bopen\s*\((\d+)\)`)
	copilotResolvedSection = regexp.MustCompile(`(?i)\bresolved\b`)
	copilotNoOpenFindings  = regexp.MustCompile(`(?im)^\s*\*\*\s*(?:no|0) open (?:issues|findings)\s*\*\*`)
)

// copilotFinding is one severity badge from a Copilot review overview.
type copilotFinding struct {
	severity     string
	discussionID string
	active       bool
}

// copilotOverview is the structured part of a Copilot review body. Severity
// appears only here: neither inline comment bodies nor the comment API carry it.
type copilotOverview struct {
	findings         []copilotFinding
	countsConsistent bool
}

// parseCopilotOverview reads the severity badges by section. Every badge counts
// as active unless its section reports it resolved, so an unfamiliar section
// fails closed. A section that states a count the parsed badges do not reach
// marks the overview inconsistent instead of under-reporting its findings.
func parseCopilotOverview(body string) copilotOverview {
	sections := copilotSectionTitle.FindAllStringSubmatchIndex(body, -1)
	overview := copilotOverview{countsConsistent: true}

	preambleEnd := len(body)
	if len(sections) > 0 {
		preambleEnd = sections[0][0]
	}
	overview.findings = parseCopilotFindings(body[:preambleEnd], true)

	for i, section := range sections {
		sectionEnd := len(body)
		if i+1 < len(sections) {
			sectionEnd = sections[i+1][0]
		}
		title := body[section[2]:section[3]]
		findings := parseCopilotFindings(body[section[1]:sectionEnd], !copilotResolvedSection.MatchString(title))
		if !copilotSectionCountMatches(title, len(findings)) {
			overview.countsConsistent = false
		}
		overview.findings = append(overview.findings, findings...)
	}
	return overview
}

func parseCopilotFindings(text string, active bool) []copilotFinding {
	var findings []copilotFinding
	for _, line := range strings.Split(text, "\n") {
		discussionID := ""
		if link := copilotDiscussionLink.FindStringSubmatch(line); link != nil {
			discussionID = link[1]
		}
		for _, badge := range copilotSeverityBadge.FindAllStringSubmatch(line, -1) {
			findings = append(findings, copilotFinding{
				severity:     strings.ToLower(strings.TrimSpace(badge[1])),
				discussionID: discussionID,
				active:       active,
			})
		}
	}
	return findings
}

func copilotSectionCountMatches(title string, parsed int) bool {
	match := copilotActiveCount.FindStringSubmatch(title)
	if match == nil {
		return true
	}
	stated, err := strconv.Atoi(match[1] + match[2])
	return err == nil && stated == parsed
}

func (overview copilotOverview) hasActiveFindings() bool {
	for _, finding := range overview.findings {
		if finding.active {
			return true
		}
	}
	return false
}

// blocks reports whether the overview holds an active finding above Low, or
// cannot be trusted to have listed all of them. Resolving a finding's review
// thread does not clear it: only a later review that stops listing it does.
func (overview copilotOverview) blocks() bool {
	if !overview.countsConsistent {
		return true
	}
	for _, finding := range overview.findings {
		if finding.active && finding.severity != copilotLowSeverityLabel {
			return true
		}
	}
	return false
}

func copilotReviewBlocks(body string) bool {
	return parseCopilotOverview(body).blocks()
}

// copilotReviewPasses requires a recognized verdict and positive evidence: an
// approval, parsed Low findings, or an explicit statement that nothing is open.
// Copilot's heading alone cannot pass a review because it does not track
// severity, and an unfamiliar body stays a failure.
func copilotReviewPasses(body string) bool {
	verdict := classifyCopilotReview(body)
	overview := parseCopilotOverview(body)
	if verdict == copilotVerdictUnknown || overview.blocks() {
		return false
	}
	return verdict == copilotVerdictClean || overview.hasActiveFindings() || copilotNoOpenFindings.MatchString(body)
}

// copilotReviewIsClean is the stricter test behind the clean marker: Copilot
// recommends approval and lists nothing active at any severity.
func copilotReviewIsClean(body string) bool {
	overview := parseCopilotOverview(body)
	return classifyCopilotReview(body) == copilotVerdictClean && !overview.blocks() && !overview.hasActiveFindings()
}

// markLowSeverityThreads flags Copilot threads whose latest mention in any
// Copilot review, across heads, is Low. A thread no review links keeps its
// unknown severity and therefore stays blocking.
func markLowSeverityThreads(reviews []aiReviewNode, threads []aiReviewThread) []aiReviewThread {
	lowSeverity := make(map[string]bool)
	for _, review := range reviews {
		if !isCopilotReviewer(review.AuthorLogin) {
			continue
		}
		for _, finding := range parseCopilotOverview(review.ReviewBody).findings {
			if finding.discussionID != "" {
				lowSeverity[finding.discussionID] = finding.severity == copilotLowSeverityLabel
			}
		}
	}

	marked := make([]aiReviewThread, len(threads))
	for i, thread := range threads {
		thread.LowSeverity = isCopilotReviewer(thread.AuthorLogin) && lowSeverity[thread.CommentID]
		marked[i] = thread
	}
	return marked
}

// hasBlockingAIThreads reports an unresolved AI thread that is not known to be
// Low severity.
func hasBlockingAIThreads(threads []aiReviewThread) bool {
	for _, thread := range threads {
		if (isAIReviewer(thread.AuthorLogin) || thread.AuthorType == "Bot") && !thread.IsResolved && !thread.LowSeverity {
			return true
		}
	}
	return false
}
