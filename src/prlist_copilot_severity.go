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
	copilotFindingsTally   = regexp.MustCompile(`(?im)^\s*\*\*Findings:\*\*(.*)$`)
	copilotHTMLTag         = regexp.MustCompile(`<[^>]*>`)
	copilotTallyNumber     = regexp.MustCompile(`\d+`)
)

// copilotFinding is one severity badge from a Copilot review overview.
type copilotFinding struct {
	severity     string
	discussionID string
	active       bool
	tally        bool
}

// copilotOverview is the structured part of a Copilot review body. Severity
// appears only here: neither inline comment bodies nor the comment API carry it.
type copilotOverview struct {
	findings          []copilotFinding
	countsConsistent  bool
	explicitZeroTally bool
}

// parseCopilotOverview reads the severity badges by section. Every badge counts
// as active unless its section reports it resolved, so an unfamiliar section
// fails closed. A section or preamble tally that states a count the listed
// badges do not reach marks the overview inconsistent instead of under-reporting
// its findings.
func parseCopilotOverview(body string) copilotOverview {
	sections := copilotSectionTitle.FindAllStringSubmatchIndex(body, -1)
	overview := copilotOverview{countsConsistent: true}

	preambleEnd := len(body)
	if len(sections) > 0 {
		preambleEnd = sections[0][0]
	}
	overview.findings = parseCopilotPreambleFindings(body[:preambleEnd])

	listedActive := 0
	for i, section := range sections {
		sectionEnd := len(body)
		if i+1 < len(sections) {
			sectionEnd = sections[i+1][0]
		}
		title := body[section[2]:section[3]]
		active := !copilotResolvedSection.MatchString(title)
		findings := parseCopilotFindings(body[section[1]:sectionEnd], active)
		if !copilotSectionCountMatches(title, len(findings)) {
			overview.countsConsistent = false
		}
		if active {
			listedActive += len(findings)
		}
		overview.findings = append(overview.findings, findings...)
	}
	preamble := body[:preambleEnd]
	if !copilotTallyMatches(preamble, listedActive) {
		overview.countsConsistent = false
	} else {
		overview.explicitZeroTally = listedActive == 0 && copilotFindingsTally.MatchString(preamble)
	}
	return overview
}

// copilotTallyMatches checks the preamble's "**Findings:** 1 <High> · 2 <Low>"
// line against the active findings its sections list. The tally puts a count
// before one badge per severity, so its total can't be read from the badges. A
// tally that is neither numbers nor "None" can't be trusted.
func copilotTallyMatches(preamble string, listedActive int) bool {
	tally := copilotFindingsTally.FindStringSubmatch(preamble)
	if tally == nil {
		return true
	}
	text := strings.TrimSpace(copilotHTMLTag.ReplaceAllString(tally[1], ""))
	numbers := copilotTallyNumber.FindAllString(text, -1)
	if len(numbers) == 0 {
		return strings.EqualFold(text, "none") && listedActive == 0
	}
	stated := 0
	for _, number := range numbers {
		count, err := strconv.Atoi(number)
		if err != nil {
			return false
		}
		stated += count
	}
	return stated == listedActive
}

// Tally badges retain their conservative severity signal, but are not individual
// findings that can name a review thread. Only the preamble has this meaning.
func parseCopilotPreambleFindings(text string) []copilotFinding {
	var findings []copilotFinding
	for _, line := range strings.Split(text, "\n") {
		parsed := parseCopilotFindings(line, true)
		for i := range parsed {
			parsed[i].tally = copilotFindingsTally.MatchString(line)
		}
		findings = append(findings, parsed...)
	}
	return findings
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

// latestCopilotReviewBlocks applies the severity rule to Copilot's latest review
// that still stands, so a later review from another bot can't clear its findings.
func latestCopilotReviewBlocks(reviews []aiReviewNode) bool {
	for i := len(reviews) - 1; i >= 0; i-- {
		review := reviews[i]
		if isCopilotReviewer(review.AuthorLogin) && !strings.EqualFold(review.State, "DISMISSED") {
			return !review.PassiveDisposed && parseCopilotOverview(review.ReviewBody).blocks()
		}
	}
	return false
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
	return verdict == copilotVerdictClean || overview.hasActiveFindings() || overview.explicitZeroTally || copilotNoOpenFindings.MatchString(body)
}

// copilotReviewListsNothing reports a readable overview with nothing active at
// any severity.
func copilotReviewListsNothing(body string) bool {
	overview := parseCopilotOverview(body)
	return !overview.blocks() && !overview.hasActiveFindings()
}

// copilotReviewIsClean is the stricter test behind the clean marker: Copilot
// recommends approval and lists nothing active at any severity.
func copilotReviewIsClean(body string) bool {
	return classifyCopilotReview(body) == copilotVerdictClean && copilotReviewListsNothing(body)
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

// hasLowSeverityThread reports a thread Copilot listed as Low. Only a Copilot
// verdict may rely on it: another reviewer's verdict needs its threads resolved.
func hasLowSeverityThread(threads []aiReviewThread) bool {
	for _, thread := range threads {
		if thread.LowSeverity {
			return true
		}
	}
	return false
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
