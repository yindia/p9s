package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

// View implements tea.Model.
func (a *App) View() string {
	w, h := max(a.width, 20), max(a.height, chromeLines+1)
	bodyH := h - chromeLines
	lines := make([]string, 0, h)
	lines = append(lines, a.header(w), a.titleBar(w))
	lines = append(lines, a.body(w, bodyH)...)
	lines = append(lines, a.footer(w))
	return strings.Join(lines, "\n")
}

func (a *App) header(w int) string {
	st := a.st
	sep := st.muted.Render(" │ ")
	h := views.HeaderOf(a.env)
	ctx := h.Context
	if a.connecting {
		ctx = "connecting"
		if a.connectingTo != "" {
			ctx += " to " + a.connectingTo
		}
		ctx += "…"
	}
	parts := []string{
		st.brand.Render(" p9s"),
		st.muted.Render("ctx ") + st.ctx.Render(table.Dash(ctx)),
	}
	if !a.opts.SnapshotAt.IsZero() {
		// Offline: say so first, ages are as of this time.
		parts = append(parts, st.notice.Bold(true).Render("snapshot "+a.opts.SnapshotAt.UTC().Format("2006-01-02 15:04Z")))
	}
	parts = append(parts,
		st.muted.Render("ns ")+a.namespaceLabel(),
		a.engineSummary(h),
		a.auditLabel(h),
	)
	mode := st.ro.Render("RO")
	if !a.backend.ReadOnly() {
		mode = st.rw.Render("RW")
	}
	left := strings.Join(parts, sep)
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(mode) - 1
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + mode + " "
}

func (a *App) namespaceLabel() string {
	if a.namespace == "" {
		return a.st.ctx.Render(views.AllNamespaces)
	}
	return a.st.notice.Bold(true).Render(a.namespace)
}

func (a *App) engineSummary(h views.Header) string {
	st := a.st
	var out []string
	for _, e := range h.Engines {
		if e.Failed {
			out = append(out, st.errorText.Render(e.Name+" ✗"))
		} else {
			out = append(out, st.engine.Render(e.Name)+" "+st.muted.Render(e.Version))
		}
	}
	if len(out) == 0 {
		return st.muted.Render("no engines")
	}
	return strings.Join(out, st.muted.Render(" · "))
}

// auditLabel shows the stalest engine's last audit relative to its own
// interval (amber past 2×, red past 5×), naming the engine when several are
// active. While caches load it says so: an age from partial data misleads.
func (a *App) auditLabel(h views.Header) string {
	st := a.st
	if !h.Synced {
		return st.notice.Render("syncing…")
	}
	if h.Audit == nil {
		return st.muted.Render("last audit -")
	}
	text := "last audit " + table.Age(a.now(), h.Audit.Last) + " ago"
	if h.Audit.Never {
		text = "last audit never"
	}
	if len(h.Engines) > 1 {
		text += " (" + h.Audit.Engine + ")"
	}
	switch h.Staleness {
	case views.Overdue:
		return st.errorText.Render(text)
	case views.Stale:
		return st.notice.Render(text)
	default:
		return st.ctx.Render(text)
	}
}

func (a *App) titleBar(w int) string {
	st := a.st
	var crumbs []string
	for _, s := range a.stack {
		crumbs = append(crumbs, s.title())
	}
	title := strings.Join(crumbs, " › ")
	if a.help != nil {
		title = "Help"
	}
	if title == "" {
		title = "p9s"
	}
	text := st.title.Render(title)
	if t, ok := a.top().(*tableScreen); ok && a.help == nil {
		text += st.titleCount.Render(fmt.Sprintf(" [%d]", len(t.visible)))
		f := t.filter
		if t.spec.Engine != nil {
			f.Engine = "" // implicit in an engine's own view
		}
		if sel := views.FilterLabel(f); sel != "" {
			text += st.muted.Render(" " + sel)
		}
		if t.text != "" {
			text += st.notice.Render(" /" + t.text)
		}
	}
	head := st.rule.Render("─ ") + text + " "
	fill := w - ansi.StringWidth(head)
	if fill <= 0 {
		return fit(head, w)
	}
	return head + st.rule.Render(strings.Repeat("─", fill))
}

func (a *App) body(w, h int) []string {
	if a.help != nil {
		return a.help.render(a.st, a.hl, w, h)
	}
	if a.connecting && len(a.stack) == 0 {
		return padLines([]string{"", fit(a.st.muted.Render("  Connecting and detecting policy engines…"), w)}, w, h)
	}
	switch s := a.top().(type) {
	case *tableScreen:
		return s.render(a.st, w, h)
	case *detailScreen:
		return s.render(a.st, a.hl, w, h)
	case *overviewScreen:
		return s.render(a.st, w, h)
	case *healthScreen:
		return s.render(a.st, a, w, h)
	}
	return padLines(nil, w, h)
}

func (a *App) footer(w int) string {
	st := a.st
	switch a.mode {
	case inputCommand:
		return fit(st.prompt.Render(" :")+a.input.View(), w)
	case inputFilter:
		return fit(st.prompt.Render(" /")+a.input.View(), w)
	}
	if a.flash != "" {
		if a.flashErr {
			return fit(st.flashErr.Render(" ✗ "+a.flash), w)
		}
		return fit(st.flashInfo.Render(" "+a.flash), w)
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, h := range a.hints() {
		if i > 0 {
			b.WriteString(st.muted.Render("  "))
		}
		key, rest, _ := strings.Cut(h, " ")
		b.WriteString(st.keyHint.Render(key))
		if rest != "" {
			b.WriteString(st.muted.Render(" " + rest))
		}
	}
	return fit(b.String(), w)
}

// hints lists only the keys that do something on the current screen and
// row, so the footer never advertises an action that would just fail.
func (a *App) hints() []string {
	if a.help != nil {
		return []string{"j/k scroll", "esc close"}
	}
	back := len(a.stack) > 1
	switch s := a.top().(type) {
	case *detailScreen:
		h := []string{"j/k scroll"}
		if s.object != nil {
			h = append(h, "y yaml", "e edit")
		}
		return append(h, "esc back", "? help")
	case *healthScreen:
		h := []string{":cmd", "tab panel", "j/k row"}
		if p := s.panel(); p != nil {
			if r, ok := p.current(); ok {
				if r.CanDescribe {
					h = append(h, "enter detail")
				}
				if r.Target != nil {
					h = append(h, "g goto")
				}
			}
		}
		return append(h, "? help")
	case *overviewScreen:
		h := []string{":cmd", "j/k block"}
		if b, ok := s.current(); ok && b.Target != nil {
			h = append(h, "enter open")
		}
		return append(h, "? help")
	case *tableScreen:
		h := []string{":cmd", "/filter"}
		if r, ok := s.current(); ok {
			if s.onEnter != nil || r.CanDrill || r.CanDescribe || r.HasYAML {
				h = append(h, "enter open")
			}
			if r.CanDescribe || r.HasYAML {
				h = append(h, "d describe")
			}
			if r.HasYAML {
				h = append(h, "y yaml", "e edit")
			}
			if r.Target != nil && r.Target.Name != "" {
				h = append(h, "g goto", "c copy")
			}
			if r.Policy != nil {
				h = append(h, "i impact")
			}
		}
		if views.IsViolations(s.spec) || views.IsRecent(s.spec) {
			h = append(h, "[ ] tab")
		}
		for _, vk := range s.spec.View.Keys {
			h = append(h, vk.Key+" "+vk.Help)
		}
		if back || s.text != "" {
			h = append(h, "esc back")
		}
		return append(h, "? help")
	}
	return []string{":cmd", "? help"}
}
