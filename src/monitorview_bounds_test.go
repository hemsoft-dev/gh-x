package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	// Truecolor parameters of the dark theme's muted and accent foregrounds and
	// its selected background.
	darkMutedForeground   = "38;2;163;178;194"
	darkAccentForeground  = "38;2;120;211;238"
	darkSelectedBackgound = "48;2;36;70;91"
)

// ansiTruncateForTest keeps the first width runes of plain text.
func ansiTruncateForTest(text string, width int) string {
	runes := []rune(text)
	return string(runes[:min(width, len(runes))])
}

// rowsModel returns a 110x32 model whose every PR section holds count rows.
func rowsModel(count int) monitorModel {
	m := sizedModel()
	m.theme = newMonitorTheme(true, false)
	rows := make([]monitorRow, count)
	for i := range rows {
		rows[i] = monitorRowForTest("owner/one", i+1, "open")
	}
	m.data = &monitorFetchResult{FetchedAt: time.Date(2026, 8, 22, 9, 30, 0, 0, time.UTC)}
	for range m.cfg.PRSections {
		m.data.PRSections = append(m.data.PRSections, monitorSectionData{Total: count, Rows: rows})
	}
	for range m.cfg.IssueSections {
		m.data.IssueSections = append(m.data.IssueSections, monitorSectionData{})
	}
	return m
}

// manyReposModel configures count repositories named org/r000, org/r001, ...
func manyReposModel(count int) monitorModel {
	m := rowsModel(1)
	m.cfg.Repos = nil
	for i := range count {
		m.cfg.Repos = append(m.cfg.Repos, fmt.Sprintf("org/r%03d", i))
	}
	return m
}

// manySectionsModel configures count PR sections titled S1, S2, ...
func manySectionsModel(count int) monitorModel {
	m := rowsModel(2)
	m.cfg.PRSections = nil
	for i := range count {
		m.cfg.PRSections = append(m.cfg.PRSections, monitorSection{Title: fmt.Sprintf("S%d", i+1), Filters: "is:open"})
	}
	m.data.PRSections = make([]monitorSectionData, count)
	m.subTab = 0
	return m
}

func TestTooSmallMonitorScreenNamesTheMinimum(t *testing.T) {
	tests := []struct {
		width, height int
		want          string
	}{
		{60, 16, "60x16"},
		{40, 10, "60x16"},
		{61, 16, "bigger window"},
		{60, 17, "bigger window"},
	}
	for _, tt := range tests {
		if got := tooSmallMonitorScreen(tt.width, tt.height); !strings.Contains(got, "at least "+tt.want+".") {
			t.Errorf("%dx%d: %q, want %q", tt.width, tt.height, got, tt.want)
		}
	}
}

func TestSidebarAndMainLinesPastTheEndAreBlank(t *testing.T) {
	lines := []string{"a", "b"}
	if sidebarAt(lines, 1) != "b" || sidebarAt(lines, 2) != "" {
		t.Fatal("sidebarAt bounds")
	}
	if mainAt(lines, 1) != "b" || mainAt(lines, 2) != "" {
		t.Fatal("mainAt bounds")
	}
}

func TestCenterMonitorText(t *testing.T) {
	if got := centerMonitorText("ab", 6); got != "  ab" {
		t.Fatalf("centered = %q", got)
	}
	if got := centerMonitorText("ab", 9); got != "   ab" {
		t.Fatalf("odd gap = %q", got)
	}
	if got := centerMonitorText("abcd", 2); got != "abcd" {
		t.Fatalf("overlong = %q", got)
	}
}

func TestListWidthLeavesTheDividerColumn(t *testing.T) {
	m := sizedModel() // 110 cells: a 22-cell sidebar plus divider
	if got := m.listWidth(); got != 110-23-1 {
		t.Fatalf("listWidth = %d", got)
	}
	m.layout = computeMonitorLayout(60, 20)
	if got := m.listWidth(); got != 59 {
		t.Fatalf("listWidth without sidebar = %d", got)
	}
}

func TestHelpFooterShowsFilterOnlyWhenSet(t *testing.T) {
	m := sizedModel()
	m.focus = monitorFocusList
	if got := stripANSIForTest(m.helpFooter()); !strings.HasPrefix(got, " list focus · tab panes") {
		t.Fatalf("footer without a filter = %q", got)
	}
	m.filter.SetValue("bug")
	if got := stripANSIForTest(m.helpFooter()); !strings.HasPrefix(got, " /bug · esc clear · list focus") {
		t.Fatalf("footer with a filter = %q", got)
	}
}

func TestFooterLineSummarizesOnlyRealChanges(t *testing.T) {
	m := rowsModel(2)
	if got := stripANSIForTest(m.footerLine()); strings.Contains(got, "rows ·") {
		t.Fatalf("footer without changes = %q", got)
	}
	m.lastChanges = []monitorChange{{Key: "owner/one#pr#1", Kind: monitorChangeAdded}}
	if got := stripANSIForTest(m.footerLine()); !strings.Contains(got, "rows · 1 added: owner/one#pr#1 added") {
		t.Fatalf("footer with a change = %q", got)
	}
}

func TestRenderTabRowSelectsTheActiveTab(t *testing.T) {
	m := rowsModel(2)
	for tab := range monitorTabCount {
		m.tab = tab
		row := m.renderTabRow()
		var want strings.Builder
		for i, label := range []string{"PRs", "Issues"} {
			if count := m.tabTotal(i); count >= 0 {
				label += fmt.Sprintf(" (%d)", count)
			}
			style := m.theme.Muted
			if i == tab {
				style = m.theme.Selected
			}
			want.WriteString(style.Render(fitMonitorLine(" "+label, monitorTabSlotWidth)))
		}
		if row != want.String() {
			t.Fatalf("tab %d row = %q, want %q", tab, row, want.String())
		}
	}
}

func TestFirstVisibleSectionKeepsTheSelectionWithinTheSlots(t *testing.T) {
	m := manySectionsModel(8) // 86-cell list: four 18-cell slots
	if slots := m.visibleSectionSlots(); slots != 4 {
		t.Fatalf("slots = %d", slots)
	}
	for subTab, want := range map[int]int{0: 0, 1: 0, 3: 2, 5: 4, 7: 4} {
		m.subTab = subTab
		if got := m.firstVisibleSection(); got != want {
			t.Errorf("subTab %d: first = %d, want %d", subTab, got, want)
		}
	}
}

func TestRenderSubTabRowNumbersAndTruncatesSlots(t *testing.T) {
	m := manySectionsModel(2)
	m.cfg.PRSections[0].Title = "abcdefghijklmnopqrstuvwxyz"
	row := m.renderSubTabRow()
	plain := stripANSIForTest(row)
	if !strings.HasPrefix(plain, "1 [abcdefghijk…]") || !strings.Contains(plain, "2 [S2]") {
		t.Fatalf("sub-tab row = %q", plain)
	}
	if !strings.HasPrefix(row, m.theme.Accent.Render(fitMonitorLine("1 [abcdefghijk…]", monitorSubTabSlotWidth))) {
		t.Fatalf("selected slot must use the accent: %q", row)
	}
	if !strings.Contains(row, m.theme.Muted.Render(fitMonitorLine("2 [S2]", monitorSubTabSlotWidth))) {
		t.Fatalf("other slots stay muted: %q", row)
	}
}

func TestListLinesRendersRowsWithFocusedSelection(t *testing.T) {
	m := rowsModel(3)
	m.focus = monitorFocusList
	focused := m.listLines()
	if !strings.Contains(stripANSIForTest(focused), "Title") {
		t.Fatalf("rows must render as a table: %q", stripANSIForTest(focused))
	}
	if !strings.Contains(focused, darkSelectedBackgound) {
		t.Fatal("the focused list highlights its cursor row")
	}
	m.focus = monitorFocusDetail
	if strings.Contains(m.listLines(), darkSelectedBackgound) {
		t.Fatal("an unfocused list uses the quiet selection")
	}
}

func TestEmptyListMessageSuggestsOnlySectionsWithItems(t *testing.T) {
	m := manySectionsModel(3)
	m.data.PRSections[1].Total = 0
	m.data.PRSections[2].Total = 4
	if got := m.emptyListMessage(); got != `No items match "S1" · S3 has 4 — press 3` {
		t.Fatalf("message = %q", got)
	}
	m.data.PRSections[2].Total = 0
	if got := m.emptyListMessage(); got != `No items match "S1"` {
		t.Fatalf("message without suggestions = %q", got)
	}
}

func TestKeyInScopeOutsideConfiguredRepos(t *testing.T) {
	m := rowsModel(1)
	m.repoIdx = len(m.cfg.Repos) + 1
	if m.keyInScope("owner/one#pr#1") {
		t.Fatal("a stale repo index matches nothing")
	}
	m.repoIdx = len(m.cfg.Repos)
	if !m.keyInScope("OWNER/TWO#pr#3") || m.keyInScope("owner/one#pr#1") {
		t.Fatal("the last configured repo scopes its own keys")
	}
}

func TestDetailHeaderFollowsFocus(t *testing.T) {
	m := rowsModel(1)
	m.focus = monitorFocusList
	header := m.detailLines()[0]
	if !strings.Contains(header, darkMutedForeground) || strings.Contains(header, darkAccentForeground) {
		t.Fatalf("unfocused header = %q", header)
	}
	m.focus = monitorFocusDetail
	header = m.detailLines()[0]
	if !strings.Contains(header, darkAccentForeground) || !strings.Contains(stripANSIForTest(header), "[focus]") {
		t.Fatalf("focused header = %q", header)
	}
}

func TestDetailTitleReversesOnlyWhenFocusedWithoutColor(t *testing.T) {
	m := rowsModel(1)
	m.theme = newMonitorTheme(true, true)
	reverse := "\x1b[1;7m"
	m.focus = monitorFocusList
	if title := m.detailLines()[1]; strings.Contains(title, reverse) {
		t.Fatalf("unfocused title = %q", title)
	}
	m.focus = monitorFocusDetail
	if title := m.detailLines()[1]; !strings.Contains(title, reverse) {
		t.Fatalf("focused title = %q", title)
	}
}

func TestRenderMainScreenPlacesTheSidebar(t *testing.T) {
	m := manyReposModel(40)
	m.repoIdx = 36 // org/r035, deep enough that the sidebar must scroll
	m.focus = monitorFocusList
	screen := m.renderMainScreen()
	lines := strings.Split(screen, "\n")
	if !strings.HasPrefix(stripANSIForTest(lines[1]), " REPOSITORIES") {
		t.Fatalf("first sidebar line = %q", stripANSIForTest(lines[1]))
	}
	sidebar := make([]string, 0, m.layout.FooterTop)
	for _, line := range lines[m.layout.SidebarTop:m.layout.FooterTop] {
		sidebar = append(sidebar, ansiTruncateForTest(stripANSIForTest(line), m.layout.SidebarWidth))
	}
	if last := sidebar[len(sidebar)-1]; !strings.HasPrefix(last, "org/r035") {
		t.Fatalf("the selected repository must stay visible on the last sidebar row: %q", sidebar)
	}
	// The heading is muted while another pane has focus; the accent marks focus.
	if strings.Contains(lines[1], darkAccentForeground) {
		t.Fatalf("an unfocused sidebar header must not use the accent: %q", lines[1])
	}
	m.focus = monitorFocusSidebar
	if focused := strings.Split(m.renderMainScreen(), "\n")[1]; !strings.Contains(focused, darkAccentForeground) {
		t.Fatalf("a focused sidebar header uses the accent: %q", focused)
	}
}

func TestMonitorDetailMetadataOptionalLines(t *testing.T) {
	labels := func(lines []monitorDetailLine) string {
		parts := make([]string, len(lines))
		for i, line := range lines {
			parts[i] = line.label
		}
		return strings.Join(parts, ",")
	}
	bare := monitorRow{Kind: monitorKindIssue}
	if got := labels(monitorDetailMetadata(bare)); got != "Repo,Author,State,Parent,Sub-issues,Assignees,Updated" {
		t.Fatalf("bare issue = %s", got)
	}
	full := monitorRow{Kind: monitorKindIssue, Labels: []string{"a", "b"}, Milestone: "v1"}
	lines := monitorDetailMetadata(full)
	if got := labels(lines); got != "Repo,Author,State,Parent,Sub-issues,Assignees,Labels,Milestone,Updated" {
		t.Fatalf("full issue = %s", got)
	}
	if lines[6].value != "a, b" || lines[7].value != "v1" {
		t.Fatalf("values = %+v", lines)
	}
}

func TestDetailMonitorStateAndReview(t *testing.T) {
	for state, want := range map[string]string{"open": "open", "draft": "draft", "merged": "merged · approved"} {
		if got := detailMonitorState(monitorRow{State: state, Review: "approved"}); got != want {
			t.Errorf("state %q = %q, want %q", state, got, want)
		}
	}
	for ai, want := range map[string]string{"-": "approvals 2", "": "approvals 2", "pass": "AI pass, approvals 2"} {
		if got := detailMonitorReview(monitorRow{AIReview: ai, Approvals: 2}); got != want {
			t.Errorf("AI %q = %q, want %q", ai, got, want)
		}
	}
}

func TestRenderMonitorMetadataLinesSkipsEmptyValues(t *testing.T) {
	theme := newMonitorTheme(true, true)
	lines := renderMonitorMetadataLines([]monitorDetailLine{
		{label: "Empty", value: ""},
		{label: "Dash", value: "-"},
		{label: "AI", value: "x"},
		{label: "Parent", value: "", showEmpty: true},
	}, 40, theme)
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	if got := stripANSIForTest(lines[0]); got != "AI:     x" {
		t.Fatalf("aligned item = %q", got)
	}
}

func TestRenderMonitorMetadataItemAlignsAndTruncates(t *testing.T) {
	theme := newMonitorTheme(true, true)
	item := monitorDetailLine{label: "AI", value: strings.Repeat("v", 30)}
	got := stripANSIForTest(renderMonitorMetadataItem(item, 6, 30, theme))
	if want := "AI:     " + strings.Repeat("v", 21) + "…"; got != want {
		t.Fatalf("item = %q, want %q", got, want)
	}
	// The value keeps at least ten cells however narrow the pane is.
	got = stripANSIForTest(renderMonitorMetadataItem(item, 6, 8, theme))
	if want := "AI:     " + strings.Repeat("v", 9) + "…"; got != want {
		t.Fatalf("narrow item = %q, want %q", got, want)
	}
}

func TestMonitorDetailContentPacksMetadataWhileItFits(t *testing.T) {
	theme := newMonitorTheme(true, true)
	row := monitorRow{Kind: monitorKindIssue, Repo: "o/r", Author: "me", State: "open", Assignees: "x", Updated: "1h", Body: "Body"}
	items := renderMonitorMetadataLines(monitorDetailMetadata(row), 80, theme)
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	pair := items[0] + " · " + items[1]
	width := lipglossWidth(pair)
	lines := monitorDetailContent(row, width, theme)
	if lines[0] != pair {
		t.Fatalf("first line = %q, want %q", lines[0], pair)
	}
	rest := items[2]
	for _, item := range items[3:] {
		if lipglossWidth(rest+" · "+item) > width {
			break
		}
		rest += " · " + item
	}
	if lines[1] != rest {
		t.Fatalf("second line = %q, want %q", lines[1], rest)
	}
	last := len(lines) - 1
	if lines[last-1] != "" || stripANSIForTest(lines[last]) != "Body" {
		t.Fatalf("body must follow a blank line: %q", lines)
	}
	joined := strings.Join(lines, "\n")
	for _, item := range items {
		if !strings.Contains(joined, item) {
			t.Fatalf("metadata item %q missing from %q", item, lines)
		}
	}
}

func TestRenderMonitorDetailHintAndTitleWidth(t *testing.T) {
	theme := newMonitorTheme(true, true)
	row := monitorRow{Kind: monitorKindIssue, Number: 7, Title: strings.Repeat("t", 80), Repo: "o/r", Body: strings.Repeat("word ", 40)}
	lines := monitorDetailContent(row, 40, theme)
	out := strings.Split(renderMonitorDetail(row, 40, 5, false, 2, theme), "\n")
	title := stripANSIForTest(out[0])
	if want := fmt.Sprintf(" 3–6/%d", len(lines)); !strings.HasSuffix(title, want) {
		t.Fatalf("title = %q, want hint %q", title, want)
	}
	if got := lipglossWidth(out[0]); got != 40 {
		t.Fatalf("title width = %d, want 40: %q", got, title)
	}
}

func TestDigitSectionIndexAcceptsOneThroughNine(t *testing.T) {
	for text, want := range map[string]int{"1": 0, "9": 8} {
		if got, ok := digitSectionIndex(text); !ok || got != want {
			t.Errorf("%q = %d %v", text, got, ok)
		}
	}
	for _, text := range []string{"0", ":", "10", ""} {
		if _, ok := digitSectionIndex(text); ok {
			t.Errorf("%q must not select a section", text)
		}
	}
}

func TestSectionSelectionIgnoresMissingSections(t *testing.T) {
	m := manySectionsModel(3)
	m.subTab = 1
	model, _ := m.jumpToSection(3)
	if model.(monitorModel).subTab != 1 {
		t.Fatal("jumping past the last section keeps the selection")
	}
	model, _ = m.jumpToSection(2)
	if model.(monitorModel).subTab != 2 {
		t.Fatal("jumping to the last section selects it")
	}
	model, _ = m.selectSubTabByClick(3)
	if model.(monitorModel).subTab != 1 {
		t.Fatal("a click past the last section keeps the selection")
	}
}

func TestSelectVisibleSubTabStopsAtTheVisibleSlots(t *testing.T) {
	m := manySectionsModel(8)
	model, _ := m.selectVisibleSubTab(4)
	if model.(monitorModel).subTab != 0 {
		t.Fatal("a click right of the last visible slot selects nothing")
	}
	model, _ = m.selectVisibleSubTab(3)
	if model.(monitorModel).subTab != 3 {
		t.Fatal("the last visible slot is selectable")
	}
}

func TestEnsureCursorVisibleScrollsOneRowPastTheWindow(t *testing.T) {
	m := rowsModel(40)
	height := m.layout.ListHeight - 1
	m.setCursor(height - 1)
	if m.offset != 0 {
		t.Fatalf("the last visible row keeps offset 0, got %d", m.offset)
	}
	m.setCursor(height)
	if m.offset != 1 {
		t.Fatalf("one row past the window scrolls by one, got %d", m.offset)
	}
	m.setCursor(height + 4)
	if m.offset != 5 {
		t.Fatalf("offset = %d, want 5", m.offset)
	}
}

func TestScrollDetailPagesByThePaneHeight(t *testing.T) {
	m := rowsModel(1)
	m.data.PRSections[m.subTab].Rows[0].Body = strings.Repeat("line\n\n", 40)
	m.detailScroll = 20
	model, _ := m.scrollDetail(tea.KeyPressMsg{Code: tea.KeyPgUp})
	page := m.layout.DetailHeight - 2
	if got := model.(monitorModel).detailScroll; got != 20-page {
		t.Fatalf("page up = %d, want %d", got, 20-page)
	}
	model, _ = m.scrollDetail(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if got := model.(monitorModel).detailScroll; got != 20+page {
		t.Fatalf("page down = %d, want %d", got, 20+page)
	}
}

func TestDetailBodyLineCountUsesTheMainPaneWidth(t *testing.T) {
	m := rowsModel(1)
	m.data.PRSections[m.subTab].Rows[0].Body = strings.Repeat("word ", 60)
	row, _ := m.selectedRow()
	want := len(monitorDetailContent(row, m.layout.Width-m.layout.MainLeft, m.theme))
	if got := detailBodyLineCount(m); got != want {
		t.Fatalf("line count = %d, want %d", got, want)
	}
}

func TestQuitMonitorNotesOnlyFailedStateSaves(t *testing.T) {
	savedWriter, savedSave := accountWarningWriter, saveMonitorState
	t.Cleanup(func() { accountWarningWriter, saveMonitorState = savedWriter, savedSave })
	var notes bytes.Buffer
	accountWarningWriter = &notes
	saveMonitorState = func(string, monitorSessionState) error { return nil }
	m := rowsModel(1)
	m.quitMonitor()
	if notes.Len() != 0 {
		t.Fatalf("a successful save prints nothing: %q", notes.String())
	}
	saveMonitorState = func(string, monitorSessionState) error { return errors.New("disk full") }
	m.quitMonitor()
	if !strings.Contains(notes.String(), "could not save session state: disk full") {
		t.Fatalf("notes = %q", notes.String())
	}
}

func TestHandleClickAddsScrollOffsets(t *testing.T) {
	m := manyReposModel(40)
	m.repoIdx = 36
	first := monitorSidebarFirst(m.repoIdx, m.layout.FooterTop-1)
	if first != 36-(m.layout.FooterTop-3) {
		t.Fatalf("first = %d", first)
	}
	model, _ := m.handleClick(tea.Mouse{X: 1, Y: m.layout.SidebarTop + 3})
	if got := model.(monitorModel).repoIdx; got != first+2 {
		t.Fatalf("sidebar click = %d, want %d", got, first+2)
	}

	list := rowsModel(40)
	list.offset = 5
	model, _ = list.handleClick(tea.Mouse{X: list.layout.MainLeft + 2, Y: list.layout.ListTop + 3})
	if got := model.(monitorModel).cursor; got != 7 {
		t.Fatalf("list click = %d, want 7", got)
	}
}

func TestSelectSidebarIndexBounds(t *testing.T) {
	m := rowsModel(1)
	m.repoIdx = 1
	if got := m.selectSidebarIndex(0).(monitorModel); got.repoIdx != 0 || got.focus != monitorFocusSidebar {
		t.Fatalf("index 0 = %+v", got.repoIdx)
	}
	last := len(m.cfg.Repos)
	if got := m.selectSidebarIndex(last).(monitorModel); got.repoIdx != last {
		t.Fatalf("last index = %d", got.repoIdx)
	}
	if got := m.selectSidebarIndex(last + 1).(monitorModel); got.repoIdx != 1 {
		t.Fatalf("past the end = %d", got.repoIdx)
	}
}

func TestSelectTabByClickBounds(t *testing.T) {
	m := rowsModel(1)
	m.tab = monitorTabIssues
	if got := m.selectTabByClick(0).(monitorModel); got.tab != monitorTabPRs {
		t.Fatalf("slot 0 = %d", got.tab)
	}
	if got := m.selectTabByClick(monitorTabCount).(monitorModel); got.tab != monitorTabIssues {
		t.Fatalf("slot past the tabs = %d", got.tab)
	}
}
