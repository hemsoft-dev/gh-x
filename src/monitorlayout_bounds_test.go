package main

import (
	"slices"
	"strings"
	"testing"
)

func flexColumn(title string, width, minimum int) monitorColumn {
	return monitorColumn{Title: title, Width: width, Flex: true, Min: minimum}
}

func columnWidths(columns []monitorColumn) []int {
	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = col.Width
	}
	return widths
}

func TestMonitorTableOverflowCountsGapsBetweenColumnsOnly(t *testing.T) {
	columns := []monitorColumn{{Width: 5}, {Width: 5}, {Width: 3}}
	// 5 + 1 + 5 + 1 + 3: no gap after the last column.
	if got := monitorTableOverflow(columns, 15); got != 0 {
		t.Fatalf("overflow = %d, want 0", got)
	}
	if got := monitorTableOverflow(columns, 10); got != 5 {
		t.Fatalf("overflow = %d, want 5", got)
	}
	if got := monitorTableOverflow(columns[:1], 2); got != 3 {
		t.Fatalf("single column overflow = %d, want 3", got)
	}
}

func TestWidestFlexibleColumnPrefersFirstOfEqualWidths(t *testing.T) {
	if got := widestFlexibleColumn([]monitorColumn{{Width: 9}, {Width: 4}}); got != -1 {
		t.Fatalf("no flexible column = %d, want -1", got)
	}
	columns := []monitorColumn{flexColumn("a", 7, 0), flexColumn("b", 7, 0), {Width: 30}}
	if got := widestFlexibleColumn(columns); got != 0 {
		t.Fatalf("tie = %d, want the first column", got)
	}
	columns[1].Width = 8
	if got := widestFlexibleColumn(columns); got != 1 {
		t.Fatalf("widest = %d, want 1", got)
	}
}

func TestWidestFlexAboveFloorsAndProtection(t *testing.T) {
	tests := []struct {
		name    string
		columns []monitorColumn
		floors  []int
		protect int
		want    int
	}{
		{"nothing above its floor", []monitorColumn{flexColumn("a", 4, 4), {Width: 9}}, []int{4, 9}, -1, -1},
		{"only the first column qualifies", []monitorColumn{flexColumn("a", 6, 4), flexColumn("b", 4, 4)}, []int{4, 4}, -1, 0},
		{"equal widths keep the first", []monitorColumn{flexColumn("a", 6, 4), flexColumn("b", 6, 4)}, []int{4, 4}, -1, 0},
		{"wider later column wins", []monitorColumn{flexColumn("a", 6, 4), flexColumn("b", 7, 4)}, []int{4, 4}, -1, 1},
		{"protected column is the last resort", []monitorColumn{flexColumn("a", 9, 4), flexColumn("b", 4, 4)}, []int{4, 4}, 0, 0},
		{"others shrink before the protected one", []monitorColumn{flexColumn("a", 9, 4), flexColumn("b", 5, 4)}, []int{4, 4}, 0, 1},
		{"protected column at its floor", []monitorColumn{flexColumn("a", 4, 4), flexColumn("b", 4, 4)}, []int{4, 4}, 0, -1},
		{"protected column above its floor", []monitorColumn{flexColumn("a", 4, 4), flexColumn("b", 5, 4)}, []int{4, 4}, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := widestFlexAbove(tt.columns, tt.floors, tt.protect); got != tt.want {
				t.Fatalf("widestFlexAbove = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestShrinkMonitorColumnsIsFairWithoutAPrimaryColumn(t *testing.T) {
	columns := []monitorColumn{flexColumn("a", 10, 10), flexColumn("b", 10, 10)}
	shrinkMonitorColumns(columns, 12)
	// Both columns sit at their minimum, so the hard pass alternates between
	// them: neither keeps its width while the other is squeezed.
	if got := columnWidths(columns); !slices.Equal(got, []int{5, 6}) {
		t.Fatalf("widths = %v, want [5 6]", got)
	}
}

func TestShrinkMonitorColumnsTakesFromTheWidestFirst(t *testing.T) {
	columns := []monitorColumn{
		{Title: "Title", Width: 20, Flex: true, Primary: true, Min: 18},
		flexColumn("Repo", 12, 8),
		flexColumn("Author", 12, 8),
	}
	shrinkMonitorColumns(columns, 38)
	// 44 + 2 gaps overflows by 8: the widest columns give width first, the
	// rest is shared evenly, and no column drops below its minimum.
	if got := columnWidths(columns); !slices.Equal(got, []int{18, 9, 9}) {
		t.Fatalf("widths = %v, want [18 9 9]", got)
	}
}

func TestGrowMonitorColumnsCanWidenTheFirstColumn(t *testing.T) {
	columns := []monitorColumn{flexColumn("a", 5, 0), {Width: 3}}
	growMonitorColumns(columns, 12)
	if got := columnWidths(columns); !slices.Equal(got, []int{8, 3}) {
		t.Fatalf("widths = %v, want [8 3]", got)
	}
}

func TestPlanMonitorColumnsAcceptsShortRows(t *testing.T) {
	columns := []monitorColumn{{Title: "#", Width: 6}, flexColumn("Title", 10, 4)}
	planned := planMonitorColumns(columns, [][]string{{"12"}}, 30)
	if len(planned) != 2 || monitorTableOverflow(planned, 30) != 0 {
		t.Fatalf("planned = %+v", planned)
	}
}

func TestTruncateMonitorCellZeroWidthIsEmpty(t *testing.T) {
	if got := truncateMonitorCell("abc", 0); got != "" {
		t.Fatalf("zero width = %q", got)
	}
	if got := truncateMonitorCell("abc", 1); got != "…" {
		t.Fatalf("one cell = %q", got)
	}
}

func TestPadToFillsShortText(t *testing.T) {
	if got := padTo(5, "ab"); got != "   " {
		t.Fatalf("padTo = %q", got)
	}
	if got := padTo(1, "abc"); got != "" {
		t.Fatalf("overlong padTo = %q", got)
	}
}

func TestRenderMonitorTableNarrowWidthDropsRepoColumn(t *testing.T) {
	rows := []monitorRow{monitorRowForTest("owner/one", 1, "open")}
	narrow := stripANSIForTest(renderMonitorTable(monitorTableRenderInput{Kind: monitorKindPR, Rows: rows, Height: 3, Width: 60}))
	if strings.Contains(narrow, "Repo") {
		t.Fatalf("a 60-cell table keeps only the compact columns:\n%s", narrow)
	}
	wider := stripANSIForTest(renderMonitorTable(monitorTableRenderInput{Kind: monitorKindPR, Rows: rows, Height: 3, Width: 62}))
	if !strings.Contains(wider, "Repo") {
		t.Fatalf("a 62-cell table shows the repository:\n%s", wider)
	}
}

func TestRenderMonitorTableKeepsTheLastCellInsideTheWidth(t *testing.T) {
	row := monitorRowForTest("owner/one", 1, "open")
	row.Updated = "12345"
	out := renderMonitorTable(monitorTableRenderInput{Kind: monitorKindPR, Rows: []monitorRow{row}, Height: 2, Width: 140})
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	for _, line := range lines {
		if got := lipglossWidth(line); got != 140 {
			t.Fatalf("line width = %d, want 140: %q", got, stripANSIForTest(line))
		}
	}
	if !strings.HasSuffix(strings.TrimRight(stripANSIForTest(lines[1]), " "), "12345") {
		t.Fatalf("last cell was cut: %q", stripANSIForTest(lines[1]))
	}
}

func TestComputeMonitorLayoutSidebarWidth(t *testing.T) {
	if got := computeMonitorLayout(100, 30); got.SidebarWidth != 22 || got.MainLeft != 23 {
		t.Fatalf("100 cells = sidebar %d, main %d", got.SidebarWidth, got.MainLeft)
	}
	if got := computeMonitorLayout(125, 30); got.SidebarWidth != 25 {
		t.Fatalf("125 cells = sidebar %d, want width/5", got.SidebarWidth)
	}
	if got := computeMonitorLayout(80, 30); got.SidebarWidth != 0 || got.MainLeft != 0 {
		t.Fatalf("80 cells = sidebar %d, main %d", got.SidebarWidth, got.MainLeft)
	}
}

func TestHitMonitorLocationEdges(t *testing.T) {
	narrow := computeMonitorLayout(80, 30)
	wide := computeMonitorLayout(120, 30)
	tests := []struct {
		name   string
		layout monitorLayout
		x, y   int
		want   monitorHit
	}{
		{"left edge of the main area", narrow, 0, narrow.ListTop + 1, monitorHit{area: "list", index: 0}},
		{"third list row", narrow, 3, narrow.ListTop + 3, monitorHit{area: "list", index: 2}},
		{"heading row", narrow, 5, 0, monitorHit{area: "scope"}},
		{"right edge", narrow, narrow.Width, 5, monitorHit{area: "none"}},
		{"last column", narrow, narrow.Width - 1, narrow.TabTop, monitorHit{area: "tab", index: (narrow.Width - 1) / monitorTabSlotWidth}},
		{"sidebar divider", wide, wide.SidebarWidth, wide.ListTop + 1, monitorHit{area: "none"}},
		{"last sidebar column", wide, wide.SidebarWidth - 1, wide.SidebarTop + 2, monitorHit{area: "sidebar", index: 1}},
		{"first main column", wide, wide.MainLeft, wide.TabTop, monitorHit{area: "tab", index: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hitMonitorLocation(tt.layout, tt.x, tt.y); got != tt.want {
				t.Fatalf("hit(%d,%d) = %+v, want %+v", tt.x, tt.y, got, tt.want)
			}
		})
	}
}

func TestMonitorSidebarFirstKeepsSelectionVisible(t *testing.T) {
	if got := monitorSidebarFirst(10, 5); got != 7 {
		t.Fatalf("first = %d, want 7", got)
	}
	if got := monitorSidebarFirst(2, 5); got != 0 {
		t.Fatalf("first = %d, want 0", got)
	}
}

func TestEllipsizeMonitorLeftKeepsTheTail(t *testing.T) {
	tests := []struct {
		text  string
		width int
		want  string
	}{
		{"abcdef", 4, "…def"},
		{"abcdef", 2, "…f"},
		{"abcd", 4, "abcd"},
		{"abc", 0, ""},
		{"/name", 3, "…me"},
	}
	for _, tt := range tests {
		if got := ellipsizeMonitorLeft(tt.text, tt.width); got != tt.want {
			t.Errorf("ellipsizeMonitorLeft(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
		}
	}
}

func TestTruncateMonitorRepoNameLeadingSlash(t *testing.T) {
	if got := truncateMonitorRepoName("/abc", 3); got != "…bc" {
		t.Fatalf("leading slash = %q", got)
	}
	if got := truncateMonitorRepoName("owner/repository", 12); got != "…/repository" {
		t.Fatalf("org absorbs the cut: %q", got)
	}
}

func TestSidebarBadgeShowsOnlyNonZeroCounts(t *testing.T) {
	theme := newMonitorTheme(true, true)
	tests := []struct {
		counts monitorRepoCounts
		want   string
	}{
		{monitorRepoCounts{PRs: 0, Issues: 2}, "2is"},
		{monitorRepoCounts{PRs: 3, Issues: 0}, "3pr"},
		{monitorRepoCounts{PRs: 1, Issues: 4}, "1pr 4is"},
		{monitorRepoCounts{}, ""},
	}
	for _, tt := range tests {
		if got := stripANSIForTest(sidebarBadge(tt.counts, false, true, theme)); got != tt.want {
			t.Errorf("badge(%+v) = %q, want %q", tt.counts, got, tt.want)
		}
	}
}

func TestRenderMonitorSidebarEntrySpacing(t *testing.T) {
	theme := newMonitorTheme(true, true)
	// With no badge the full width belongs to the name.
	if got := renderMonitorSidebarEntry("abcdefghij", false, monitorRepoCounts{Accessible: true}, false, true, 10, theme); got != "abcdefghij" {
		t.Fatalf("unbadged entry = %q", got)
	}
	got := stripANSIForTest(renderMonitorSidebarEntry("ab", false, monitorRepoCounts{Accessible: true, PRs: 2}, false, true, 10, theme))
	if got != "ab     2pr" {
		t.Fatalf("badged entry = %q", got)
	}
}

func TestRenderMonitorSidebarHighlightsOnlyTheSelection(t *testing.T) {
	theme := newMonitorTheme(true, false)
	counts := map[string]monitorRepoCounts{"owner/one": {Accessible: true}, "owner/two": {Accessible: true}}
	lines := strings.Split(renderMonitorSidebar([]string{"owner/one", "owner/two"}, 2, counts, 6, 20, false, theme), "\n")
	if len(lines) != 6 {
		t.Fatalf("lines = %q", lines)
	}
	if lines[2] != "owner/one"+strings.Repeat(" ", 11) {
		t.Fatalf("unselected entry must stay plain: %q", lines[2])
	}
	if want := theme.Selected.Render(fitMonitorLine("owner/two", 20)); lines[3] != want {
		t.Fatalf("selected entry = %q, want %q", lines[3], want)
	}
}
