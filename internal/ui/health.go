package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

// healthScreen is the :health dashboard: graded panels, tab between them,
// j/k within one; the regular row actions work on the active panel.
type healthScreen struct {
	env    views.Env
	d      views.Dashboard
	panels []*tableScreen
	active int
	offset int
}

func newHealthScreen(env views.Env) *healthScreen {
	h := &healthScreen{env: env}
	h.refresh()
	return h
}

func (h *healthScreen) title() string { return "Health" }

// refresh rebuilds the dashboard, keeping each panel's selection by row ID.
func (h *healthScreen) refresh() {
	h.d = views.DashboardOf(h.env)
	for i, p := range h.d.Panels {
		if i >= len(h.panels) {
			h.panels = append(h.panels, &tableScreen{now: h.env.Now})
		}
		t := h.panels[i]
		var selected string
		if r, ok := t.current(); ok {
			selected = r.ID
		}
		t.tbl = p.Table
		t.applyText(selected)
	}
	h.panels = h.panels[:len(h.d.Panels)]
	h.active = clampIndex(h.active, len(h.panels)-1)
}

func (h *healthScreen) panel() *tableScreen {
	if h.active < 0 || h.active >= len(h.panels) {
		return nil
	}
	return h.panels[h.active]
}

func (h *healthScreen) switchPanel(delta int) {
	if n := len(h.panels); n > 0 {
		h.active = (h.active + delta + n) % n
	}
}

func (h *healthScreen) render(st styles, a *App, width, height int) []string {
	o := h.d.Overall
	head := fmt.Sprintf("  %s   %s   %s",
		st.title.Render("OVERALL"),
		st.severity["error"].Render(fmt.Sprintf("✗ %d critical", o.Critical)),
		st.severity["warning"].Render(fmt.Sprintf("! %d warning", o.Warning)))
	if o.Unknown > 0 {
		head += "   " + st.muted.Render(fmt.Sprintf("? %d unknown", o.Unknown))
	}
	head += "   " + st.yes.Render(fmt.Sprintf("✓ %d ok", o.OK))
	if !h.d.Synced {
		head += "   " + st.notice.Render("syncing: grades are provisional")
	}
	if !h.d.UpdatedAt.IsZero() {
		refreshed := st.muted.Render("refreshed " + table.Age(a.now(), h.d.UpdatedAt) + " ago")
		if gap := width - ansi.StringWidth(head) - ansi.StringWidth(refreshed) - 1; gap > 0 {
			head += strings.Repeat(" ", gap) + refreshed
		}
	}
	// The overall line and notices stay pinned; only the panels scroll.
	pinned := []string{fit(head, width)}
	for _, n := range h.d.Notices {
		pinned = append(pinned, fit(st.notice.Render("! "+n), width))
	}
	var lines []string
	selLine, titleLine := 0, 0
	for i, p := range h.d.Panels {
		t := h.panels[i]
		title := fmt.Sprintf("─ %s [%d] ", p.Title, len(p.Table.Rows))
		style := st.rule
		if i == h.active {
			style = st.title
		}
		lines = append(lines, "", fit(style.Render(title)+st.rule.Render(strings.Repeat("─", max(width-ansi.StringWidth(title), 0))), width))
		cursor := t.cursor
		if i != h.active {
			t.cursor = -1 // only the active panel shows a selection
		} else {
			titleLine = len(lines) - 1
			selLine = len(lines) + 1 + max(t.cursor, 0)
		}
		bare := *t
		bare.tbl.Notices, bare.tbl.Error = nil, ""
		lines = append(lines, bare.render(st, width, max(len(t.visible)+1, 2))...)
		t.cursor = cursor
	}
	bodyH := max(height-len(pinned), 1)
	// Keep the active panel's title and its selected row on screen; when
	// both do not fit, the row wins.
	if titleLine < h.offset || selLine >= h.offset+bodyH {
		h.offset = titleLine
	}
	if selLine >= h.offset+bodyH {
		h.offset = selLine - bodyH + 1
	}
	h.offset = clampIndex(h.offset, len(lines)-bodyH)
	return append(pinned, padLines(lines[h.offset:], width, bodyH)...)
}
