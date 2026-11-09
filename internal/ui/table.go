package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

// tableScreen shows one materialised view: shared and engine-specific views
// alike. It holds screen state only; rows come from table.Build.
type tableScreen struct {
	spec views.Spec
	// vGrouping is the :v grouping this screen had before "]", restored by
	// "["; a screen opened on the recent tab gets the default.
	vGrouping views.Grouping
	filter    engine.Filter
	// text is the "/" filter; preset is the filter the screen was opened
	// with (e.g. from an overview block), which esc treats as the baseline.
	text   string
	preset string
	now    func() time.Time

	tbl     table.Table
	visible []table.Row
	cursor  int
	offset  int
	// moved is set once the user moves the cursor; before that the top row
	// stays selected while data streams in, instead of following whichever
	// row happened to arrive first.
	moved bool

	// onEnter overrides drill-down (used by :ctx and :ns).
	onEnter func(table.Row) tea.Cmd

	// userSort is the sort chosen with N/V/A/S; nil keeps the view default.
	userSort *table.Sort
}

// setSort applies a user-chosen sort that survives refreshes.
func (t *tableScreen) setSort(col int, desc bool) {
	var selected string
	if r, ok := t.current(); ok {
		selected = r.ID
	}
	t.userSort = &table.Sort{Column: col, Desc: desc}
	t.tbl = t.tbl.Sorted(col, desc)
	t.applyText(selected)
}

func newTableScreen(s views.Spec, f engine.Filter, now func() time.Time) *tableScreen {
	return &tableScreen{spec: s, filter: f, text: f.Text, preset: f.Text, now: now}
}

func (t *tableScreen) title() string { return t.spec.View.Title }

// refresh rebuilds the table, keeping the selection on the same row ID.
func (t *tableScreen) refresh() {
	var selected string
	if r, ok := t.current(); ok && t.moved {
		selected = r.ID
	}
	t.tbl = table.Build(t.spec.Source, t.filter, t.now())
	if t.userSort != nil {
		t.tbl = t.tbl.Sorted(t.userSort.Column, t.userSort.Desc)
	}
	t.applyText(selected)
}

func (t *tableScreen) setText(s string) {
	var selected string
	if r, ok := t.current(); ok {
		selected = r.ID
	}
	t.text = s
	t.applyText(selected)
}

func (t *tableScreen) applyText(selected string) {
	t.visible = t.tbl.Match(t.text)
	t.cursor = 0
	for i, r := range t.visible {
		if r.ID == selected {
			t.cursor = i
			break
		}
	}
}

func (t *tableScreen) current() (table.Row, bool) {
	if t.cursor < 0 || t.cursor >= len(t.visible) {
		return table.Row{}, false
	}
	return t.visible[t.cursor], true
}

func (t *tableScreen) move(delta int) { t.moveTo(t.cursor + delta) }

func (t *tableScreen) moveTo(i int) {
	t.moved = true
	t.cursor = clampIndex(i, len(t.visible)-1)
}

// clampIndex bounds v to [0, hi]; a negative hi (empty list) yields 0.
func clampIndex(v, hi int) int {
	return max(0, min(v, hi))
}

// render draws notices, header and rows into exactly height lines.
func (t *tableScreen) render(st styles, width, height int) []string {
	var lines []string
	for _, s := range t.tbl.Summary {
		lines = append(lines, fit(" "+s, width))
	}
	for _, n := range limitNotices(t.tbl.Notices, height) {
		lines = append(lines, fit(st.notice.Render("! "+n), width))
	}
	if t.tbl.Error != "" {
		for _, l := range strings.Split(t.tbl.Error, "\n") {
			lines = append(lines, fit(st.errorText.Render("✗ "+l), width))
		}
	}
	if len(t.visible) == 0 {
		lines = append(lines, "", fit(st.muted.Render("  "+t.emptyText()), width))
		return padLines(lines, width, height)
	}

	widths := t.columnWidths(width)
	lines = append(lines, t.headerLine(st, widths))
	status := t.statusLines(st, width)
	if height-len(lines)-len(status) < 1 {
		status = nil // a tiny terminal keeps at least one row instead
	}
	bodyH := max(height-len(lines)-len(status), 1)
	t.scrollInto(bodyH)
	for i := t.offset; i < len(t.visible) && i < t.offset+bodyH; i++ {
		lines = append(lines, t.rowLine(st, t.visible[i], widths, width, i == t.cursor))
	}
	lines = padLines(lines, width, max(height-len(status), 0))
	return append(lines, status...)
}

// maxStatusLines bounds the selected row's full message under the table.
const maxStatusLines = 2

// statusLines shows the selected row's full message (the column with the
// message role), wrapped, under the table. Views without one get none.
func (t *tableScreen) statusLines(st styles, width int) []string {
	col := t.tbl.ColumnFor(engine.RoleMessage)
	r, ok := t.current()
	if col < 0 || !ok || col >= len(r.Cells) || r.Cells[col].Text == "" {
		return nil
	}
	wrapped := strings.Split(ansi.Wordwrap(r.Cells[col].Text, max(width-4, 10), " "), "\n")
	if len(wrapped) > maxStatusLines {
		wrapped = wrapped[:maxStatusLines]
		wrapped[maxStatusLines-1] = ansi.Truncate(wrapped[maxStatusLines-1]+glyphEllipsis, width-4, glyphEllipsis)
	}
	out := make([]string, 0, len(wrapped))
	for i, l := range wrapped {
		prefix := "  "
		if i == 0 {
			prefix = "» "
		}
		out = append(out, fit(st.muted.Render(" "+prefix+l), width))
	}
	return out
}

func (t *tableScreen) emptyText() string {
	if t.text != "" {
		return "No rows match /" + t.text
	}
	if t.tbl.Empty != "" {
		return t.tbl.Empty
	}
	return "Nothing to show."
}

func (t *tableScreen) scrollInto(h int) {
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = clampIndex(t.offset, len(t.visible)-h)
}

func (t *tableScreen) headerLine(st styles, widths []int) string {
	parts := make([]string, 0, len(widths))
	for i, c := range t.tbl.Columns {
		if i >= len(widths) || widths[i] == 0 {
			continue // hidden: see columnWidths
		}
		title := c.Title
		if i == t.tbl.Sort.Column {
			title += sortArrow(t.tbl.Sort.Desc)
		}
		parts = append(parts, fit(title, widths[i]))
	}
	return st.colHeader.Render("  " + strings.Join(parts, strings.Repeat(" ", colGap)))
}

func (t *tableScreen) rowLine(st styles, r table.Row, widths []int, width int, selected bool) string {
	parts := make([]string, 0, len(widths))
	for i := range widths {
		if widths[i] == 0 {
			continue // hidden: see columnWidths
		}
		var c table.Cell
		if i < len(r.Cells) {
			c = r.Cells[i]
		}
		var role engine.Role
		if i < len(t.tbl.Columns) {
			role = t.tbl.Columns[i].Role
		}
		if selected {
			parts = append(parts, fit(cellDisplay(c, t.tbl.Partial), widths[i]))
		} else {
			parts = append(parts, fit(cellStyled(st, c, role, t.tbl.Partial), widths[i]))
		}
	}
	gap := strings.Repeat(" ", colGap)
	if selected {
		return st.selected.Render(fit(glyphCursor+" "+strings.Join(parts, gap), width))
	}
	return fit("  "+strings.Join(parts, gap), width)
}

func padLines(lines []string, width, height int) []string {
	if len(lines) > height {
		return lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines
}

func sortArrow(desc bool) string {
	if desc {
		return "↓"
	}
	return "↑"
}
