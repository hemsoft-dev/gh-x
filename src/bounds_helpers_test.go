package main

import (
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestFitColumnsToTerminalWithFloorsShrinksExactly(t *testing.T) {
	if got := fitColumnsToTerminalWithFloors([]int{20, 20}, []int{0, 1}, nil, 100); !slices.Equal(got, []int{20, 20}) {
		t.Fatalf("a fitting table changed: %v", got)
	}
	// 30 + gap + 10 = 41 cells overflow a 36-cell terminal by 5.
	if got := fitColumnsToTerminalWithFloors([]int{30, 10}, []int{0}, nil, 36); !slices.Equal(got, []int{25, 10}) {
		t.Fatalf("widths = %v, want [25 10]", got)
	}
	// Equal flexible columns give up width from the first one first.
	if got := fitColumnsToTerminalWithFloors([]int{20, 20}, []int{0, 1}, nil, 40); !slices.Equal(got, []int{19, 20}) {
		t.Fatalf("tie = %v, want [19 20]", got)
	}
}

func TestSummarizeMonitorChangesCapsAndCounts(t *testing.T) {
	added := monitorChange{Key: "o/r#pr#1", Kind: monitorChangeAdded}
	removed := monitorChange{Key: "o/r#pr#2", Kind: monitorChangeRemoved}
	changed := monitorChange{Key: "o/r#pr#3", Kind: monitorChangeField, Summary: "State open -> closed"}
	tests := []struct {
		changes  []monitorChange
		maxItems int
		want     string
	}{
		{[]monitorChange{added}, 1, "1 added: o/r#pr#1 added"},
		{[]monitorChange{added, removed}, 0, "1 added, 1 removed, +2 more"},
		{[]monitorChange{removed}, 1, "1 removed: o/r#pr#2 removed"},
		{[]monitorChange{changed}, 1, "1 changed: o/r#pr#3 State open -> closed"},
		{[]monitorChange{added, removed, changed}, 2, "1 added, 1 removed, 1 changed: o/r#pr#1 added, o/r#pr#2 removed, +1 more"},
	}
	for _, tt := range tests {
		if got := summarizeMonitorChanges(tt.changes, tt.maxItems); got != tt.want {
			t.Errorf("summary = %q, want %q", got, tt.want)
		}
	}
}

func TestWriteStarterSectionLimitLine(t *testing.T) {
	var with, without strings.Builder
	writeStarterSection(&with, monitorSection{Title: "Mine", Filters: "is:open", Limit: 5})
	writeStarterSection(&without, monitorSection{Title: "Mine", Filters: "is:open"})
	if !strings.Contains(with.String(), "    limit: 5\n") {
		t.Fatalf("limited section = %q", with.String())
	}
	if strings.Contains(without.String(), "limit") {
		t.Fatalf("unlimited section = %q", without.String())
	}
}

func TestSaveMonitorConfigFillsOnlyAMissingVersion(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultMonitorConfig("owner/one")
	cfg.Version = 0
	if err := saveMonitorConfig(filepath.Join(dir, "a.yml"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != monitorConfigVersion {
		t.Fatalf("version = %d", cfg.Version)
	}
	cfg.Version = 7
	if err := saveMonitorConfig(filepath.Join(dir, "b.yml"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 7 {
		t.Fatalf("an explicit version changed to %d", cfg.Version)
	}
}

func TestValidateMonitorSectionsAcceptsTheMaximumLimit(t *testing.T) {
	if err := validateMonitorSections([]monitorSection{{Title: "All", Filters: "is:open", Limit: maximumMonitorFetch}}); err != nil {
		t.Fatalf("limit %d: %v", maximumMonitorFetch, err)
	}
	if err := validateMonitorSections([]monitorSection{{Title: "All", Filters: "is:open", Limit: maximumMonitorFetch + 1}}); err == nil {
		t.Fatal("a limit above the maximum must fail")
	}
}

func TestParseMonitorIntervalAcceptsItsBounds(t *testing.T) {
	for _, text := range []string{minimumMonitorInterval.String(), maximumMonitorInterval.String()} {
		if _, err := parseMonitorInterval(text); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
	for _, text := range []string{"9s", "6h1s"} {
		if _, err := parseMonitorInterval(text); err == nil {
			t.Errorf("%s must be rejected", text)
		}
	}
}

func TestSanitizeMonitorMessageKeepsExactly160Runes(t *testing.T) {
	text := strings.Repeat("é", 160)
	if got := sanitizeMonitorMessage(text); got != text {
		t.Fatal("a 160-rune message must stay whole")
	}
	if got := sanitizeMonitorMessage(text + "x"); got != text+"…" {
		t.Fatalf("a 161-rune message = %q", got)
	}
}

func TestClampSelectionsPullsSubTabIntoRange(t *testing.T) {
	m := newTestMonitorModel()
	m.cfg.PRSections = []monitorSection{{Title: "A", Filters: "is:open"}, {Title: "B", Filters: "is:open"}, {Title: "C", Filters: "is:open"}}
	m.subTab = 10
	m.clampSelections()
	if m.subTab != 2 {
		t.Fatalf("subTab = %d, want 2", m.subTab)
	}
}

func TestClampBoundedIndexAndStoredSubTab(t *testing.T) {
	if got := clampBoundedIndex(3, 3); got != 0 {
		t.Fatalf("index at the bound = %d", got)
	}
	if got := clampBoundedIndex(2, 3); got != 2 {
		t.Fatalf("index below the bound = %d", got)
	}
	if got := clampStoredSubTab(2, monitorTabIssues, 3, 0); got != 2 {
		t.Fatalf("issues without sections fall back to PR sections: %d", got)
	}
	if got := clampStoredSubTab(2, monitorTabIssues, 3, 2); got != 0 {
		t.Fatalf("issue sections bound the issue tab: %d", got)
	}
}

func TestApplyDefaultSubTabWithoutAnAllOpenSection(t *testing.T) {
	m := newTestMonitorModel()
	m.cfg.PRSections = []monitorSection{{Title: "A", Filters: "is:open"}, {Title: "B", Filters: "is:open"}, {Title: "C", Filters: "is:open"}}
	m.subTab = 5
	m.applyDefaultSubTab()
	if m.subTab != 2 {
		t.Fatalf("subTab = %d, want 2", m.subTab)
	}
	if got := m.currentSection().Title; got != "C" {
		t.Fatalf("current section = %q", got)
	}
	m.subTab = 5
	if got := m.currentSection().Title; got != "C" {
		t.Fatalf("an out-of-range sub-tab shows the last section: %q", got)
	}
}

func TestComputeVisibleRowsLastRepo(t *testing.T) {
	data := &monitorFetchResult{PRSections: []monitorSectionData{{Rows: []monitorRow{
		monitorRowForTest("owner/one", 1, "open"),
		monitorRowForTest("owner/two", 2, "open"),
	}}}}
	rows := computeVisibleRows(data, monitorTabPRs, 0, 2, []string{"owner/one", "owner/two"}, "")
	if len(rows) != 1 || rows[0].Repo != "owner/two" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSelectedRowAndSectionTotalAtTheEnd(t *testing.T) {
	m := rowsModel(2)
	m.cursor = 2
	if _, ok := m.selectedRow(); ok {
		t.Fatal("a cursor past the rows selects nothing")
	}
	if got := monitorSectionTotal(m.data, monitorTabPRs, len(m.data.PRSections)); got != 0 {
		t.Fatalf("total past the sections = %d", got)
	}
}

func TestMonitorThemeResolvedHonoursNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if (monitorTheme{}).resolved().NoColor {
		t.Fatal("an empty NO_COLOR keeps color")
	}
	t.Setenv("NO_COLOR", "1")
	if !(monitorTheme{}).resolved().NoColor {
		t.Fatal("NO_COLOR disables color")
	}
}

func TestApplyMonitorThemeSizesInputs(t *testing.T) {
	m := newTestMonitorModel()
	m.layout = computeMonitorLayout(60, 20)
	m.applyMonitorTheme()
	if got := m.filter.Width(); got != 47 {
		t.Fatalf("filter width = %d, want 47", got)
	}
	// min(58, 60-12) = 48 cells, less the textarea's six-cell gutter.
	if got := m.settings.repos.Width(); got != 42 {
		t.Fatalf("repos width = %d, want 42", got)
	}
	m.layout = computeMonitorLayout(110, 20)
	m.applyMonitorTheme()
	if got := m.settings.repos.Width(); got != 52 {
		t.Fatalf("wide repos width = %d, want 52", got)
	}
}

func TestMonitorIdentityOwnerChanges(t *testing.T) {
	previous := monitorHostIdentity{Viewer: "me", Owners: []string{"a"}}
	if monitorIdentityChanged(previous, monitorHostIdentity{Viewer: "me"}, true) {
		t.Fatal("unknown current owners are not a change")
	}
	if !monitorIdentityChanged(previous, monitorHostIdentity{Viewer: "me", Owners: []string{"b"}}, true) {
		t.Fatal("different owners are a change")
	}
	if monitorHostIdentitiesEqual(monitorHostIdentity{Viewer: "me"}, monitorHostIdentity{Viewer: "me"}, true) {
		t.Fatal("unknown owners cannot prove an equal identity")
	}
	if !monitorHostIdentitiesEqual(previous, previous, true) {
		t.Fatal("identical owners are equal")
	}
}

func TestRenderSettingsScreenFitsTheTerminal(t *testing.T) {
	m := newTestMonitorModel()
	m.layout = computeMonitorLayout(60, 32)
	m.theme = newMonitorTheme(true, true)
	m.settings.open(m.cfg)
	m.applyMonitorTheme()
	lines := strings.Split(stripANSIForTest(m.renderSettingsScreen()), "\n")
	top, bottom := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "╭") {
			top = i
		}
		if strings.HasPrefix(trimmed, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatalf("box not found:\n%s", strings.Join(lines, "\n"))
	}
	// The settings width is min(64, 60-4) = 56; the bordered box is two cells narrower.
	if got := lipglossWidth(strings.TrimSpace(lines[top])); got != 54 {
		t.Fatalf("box width = %d, want 54", got)
	}
	// Title, label, eight repo rows, blank, limit, interval, blank, two hints.
	if got := bottom - top - 1; got != 16 {
		t.Fatalf("box body rows = %d, want 16:\n%s", got, strings.Join(lines[top:bottom+1], "\n"))
	}
}

func TestParseProjectOptionsClosedDaysBounds(t *testing.T) {
	for _, days := range []string{"0", "36500"} {
		if _, err := parseProjectOptions([]string{"--closed-days", days}, io.Discard); err != nil {
			t.Errorf("closed-days %s: %v", days, err)
		}
	}
	for _, days := range []string{"-1", "36501"} {
		if _, err := parseProjectOptions([]string{"--closed-days", days}, io.Discard); err == nil {
			t.Errorf("closed-days %s must fail", days)
		}
	}
}

func TestWriteProjectHidesClosedSectionAtZeroDays(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	var hidden, shown strings.Builder
	if err := writeProject(&hidden, projectDocument{Repository: "o/r"}, projectOptions{closedDays: 0}, now); err != nil {
		t.Fatal(err)
	}
	if err := writeProject(&shown, projectDocument{Repository: "o/r"}, projectOptions{closedDays: 3}, now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hidden.String(), "Recently closed") || !strings.Contains(shown.String(), "Recently closed (last 3 days)") {
		t.Fatalf("hidden = %q, shown = %q", hidden.String(), shown.String())
	}
}

func TestMachineJSONCommandsNeedACommand(t *testing.T) {
	if isProjectJSONCommand(nil, true) || isStatusJSONCommand(nil, true) {
		t.Fatal("no arguments is not a JSON command")
	}
	if !isProjectJSONCommand([]string{"p"}, true) || !isStatusJSONCommand([]string{"s"}, true) {
		t.Fatal("aliases are JSON commands")
	}
}

func TestListOptionsAcceptALimitOfOne(t *testing.T) {
	if options, err := parseRunListOptions([]string{"--limit", "1"}, io.Discard); err != nil || options.limit != 1 {
		t.Fatalf("run list limit 1: %+v %v", options, err)
	}
	if _, err := parseRunListOptions([]string{"--limit", "0"}, io.Discard); err == nil {
		t.Fatal("run list limit 0 must fail")
	}
	if options, err := parseIssueListOptions([]string{"--limit", "1"}, io.Discard); err != nil || options.limit != 1 {
		t.Fatalf("issue list limit 1: %+v %v", options, err)
	}
	if _, err := parseIssueListOptions([]string{"--limit", "0"}, io.Discard); err == nil {
		t.Fatal("issue list limit 0 must fail")
	}
}

func TestFormatElapsedUnitBoundaries(t *testing.T) {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{time.Hour, "1h"},
		{time.Hour + 2*time.Minute, "1h2m"},
	}
	for _, tt := range tests {
		if got := formatElapsed("completed", start, start.Add(tt.elapsed), start.Add(2*time.Hour)); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.elapsed, got, tt.want)
		}
	}
}

func TestLooksLikeNumberAcceptsNine(t *testing.T) {
	if !looksLikeNumber("909") || looksLikeNumber("9a") || looksLikeNumber(":") {
		t.Fatal("digit detection")
	}
}

func TestPraiseIndexesFallBackPastTheEnd(t *testing.T) {
	if got := backlogPraiseAt(len(backlogPraises)); got != backlogPraises[0] {
		t.Fatalf("backlog praise = %q", got)
	}
	if got := workflowPerfectionPraiseAt(len(workflowPerfectionPraises)); got != workflowPerfectionPraises[0] {
		t.Fatalf("workflow praise = %q", got)
	}
	last := len(backlogPraises) - 1
	if got := backlogPraiseAt(last); got != backlogPraises[last] {
		t.Fatalf("last backlog praise = %q", got)
	}
}

func TestTrimCellTextBoundaries(t *testing.T) {
	tests := []struct {
		text  string
		limit int
		want  string
	}{
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "ab..."},
		{"abcdef", 3, "abc"},
		{"abcdef", 0, ""},
	}
	for _, tt := range tests {
		if got := trimCellText(tt.text, tt.limit); got != tt.want {
			t.Errorf("trimCellText(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
		}
	}
}

func TestApplySettingsLimitBounds(t *testing.T) {
	for _, limit := range []string{"1", "100"} {
		m := newTestMonitorModel()
		m.settings.open(m.cfg)
		m.settings.limit.SetValue(limit)
		if err := applySettings(&m.settings, m.cfg); err != nil {
			t.Errorf("limit %s: %v", limit, err)
		}
	}
	for _, limit := range []string{"0", "101"} {
		m := newTestMonitorModel()
		m.settings.open(m.cfg)
		m.settings.limit.SetValue(limit)
		if err := applySettings(&m.settings, m.cfg); err == nil {
			t.Errorf("limit %s must fail", limit)
		}
	}
}

func TestParseCronNumberAcceptsTheMaximum(t *testing.T) {
	if value, ok := parseCronNumber("59", 0, 59); !ok || value != 59 {
		t.Fatalf("59 = %d %v", value, ok)
	}
	if _, ok := parseCronNumber("60", 0, 59); ok {
		t.Fatal("60 must fail")
	}
}

func TestWorkflowYAMLHelpersTolerateOddMappings(t *testing.T) {
	empty := &yaml.Node{Kind: yaml.DocumentNode}
	if documentRoot(empty) != empty {
		t.Fatal("an empty document is its own root")
	}
	dangling := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "on"}}}
	if mappingValue(dangling, "on") != nil {
		t.Fatal("a key without a value has no value")
	}
	if got := formatOnNode(dangling); len(got) != 0 {
		t.Fatalf("a dangling trigger key = %q", got)
	}
}

func TestIssueHierarchyFieldChecks(t *testing.T) {
	parent := issueParentReference{Number: 0, URL: "https://example.test/1"}
	parent.Repository.NameWithOwner = "o/r"
	if validIssueParent(parent) {
		t.Fatal("issue number 0 is not a parent")
	}
	parent.Number = 1
	if !validIssueParent(parent) {
		t.Fatal("a complete parent is valid")
	}
	if hasJSONValue(map[string]json.RawMessage{"x": {}}, "x") {
		t.Fatal("an empty raw value is absent")
	}
}

func TestGraphQLAliasZeroIsIgnored(t *testing.T) {
	path := func(parts ...string) []json.RawMessage {
		raw := make([]json.RawMessage, len(parts))
		for i, part := range parts {
			raw[i], _ = json.Marshal(part)
		}
		return raw
	}
	if mergedSupplementalOnlyError(graphQLError{Path: path("pr0", "comments")}) {
		t.Fatal("alias 0 is not a supplemental field")
	}
	if !mergedSupplementalOnlyError(graphQLError{Path: path("pr1", "comments")}) {
		t.Fatal("alias 1 comments is a supplemental field")
	}
	if got := aliasesFromErrors([]graphQLError{{Path: path("pr0", "reviews")}, {Path: path("pr2", "reviews")}}); len(got) != 1 || !got[2] {
		t.Fatalf("aliases = %v", got)
	}
}
