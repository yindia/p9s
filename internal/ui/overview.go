package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

// overviewScreen shows the overview blocks; j/k select one, enter opens it.
type overviewScreen struct {
	env      views.Env
	ov       views.Overview
	selected int
	// offset is the first rendered line; it follows the selected block.
	offset int
}

func newOverviewScreen(env views.Env) *overviewScreen {
	s := &overviewScreen{env: env}
	s.refresh()
	return s
}

func (o *overviewScreen) title() string { return "Overview" }

func (o *overviewScreen) refresh() { o.ov = views.OverviewOf(o.env) }

func (o *overviewScreen) move(delta int) {
	o.selected = clampIndex(o.selected+delta, len(o.ov.Blocks)-1)
}

func (o *overviewScreen) current() (views.Block, bool) {
	if o.selected < 0 || o.selected >= len(o.ov.Blocks) {
		return views.Block{}, false
	}
	return o.ov.Blocks[o.selected], true
}

// render stacks the blocks; the selected one's title is marked. Each block
// table is drawn by the regular table renderer without a cursor.
func (o *overviewScreen) render(st styles, width, height int) []string {
	var lines []string
	start, end := 0, 0
	for i, b := range o.ov.Blocks {
		if i == o.selected {
			start = len(lines)
		}
		lines = append(lines, o.blockTitle(st, b, i == o.selected, width))
		if b.Summary != "" {
			lines = append(lines, fit(st.muted.Render("  "+b.Summary), width))
		}
		// Notices and errors are drawn here, not by the table renderer, so
		// they never eat into the block's rows (and show when it is empty).
		for _, n := range b.Table.Notices {
			lines = append(lines, fit(st.notice.Render("  ! "+n), width))
		}
		if b.Table.Error != "" {
			for _, l := range strings.Split(b.Table.Error, "\n") {
				lines = append(lines, fit(st.errorText.Render("  ✗ "+l), width))
			}
		}
		switch {
		case b.Placeholder != "":
			lines = append(lines, fit(st.muted.Render("  "+b.Placeholder), width))
		case len(b.Table.Rows) == 0:
			if b.Table.Error == "" {
				lines = append(lines, fit(st.muted.Render("  "+table.Dash(b.Table.Empty)), width))
			}
		default:
			bare := b.Table
			bare.Notices, bare.Error = nil, ""
			t := &tableScreen{tbl: bare, visible: bare.Rows, cursor: -1}
			lines = append(lines, t.render(st, width, len(bare.Rows)+1)...)
		}
		if i == o.selected {
			end = len(lines)
		}
		lines = append(lines, "")
	}
	// Keep the whole selected block on screen when it fits, else its top.
	if end-o.offset > height {
		o.offset = min(start, end-height)
	}
	if start < o.offset {
		o.offset = start
	}
	o.offset = clampIndex(o.offset, len(lines)-height)
	return padLines(lines[o.offset:], width, height)
}

func (o *overviewScreen) blockTitle(st styles, b views.Block, selected bool, width int) string {
	title := b.Title
	if b.Total > len(b.Table.Rows) && b.Placeholder == "" {
		title += fmt.Sprintf(" — top %d of %d", len(b.Table.Rows), b.Total)
	}
	if b.Target != nil {
		title += "  ⏎ :" + b.Target.Command
		if b.Target.Filter != "" {
			title += " /" + b.Target.Filter
		}
	}
	marker := "  "
	style := st.sectionTitle
	if selected {
		marker = glyphCursor + " "
		style = st.selected
	}
	line := marker + style.Render(title)
	if pad := width - ansi.StringWidth(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return fit(line, width)
}
