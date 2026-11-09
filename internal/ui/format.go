package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/table"
)

const (
	glyphYes      = "✓"
	glyphNo       = "✗"
	glyphCursor   = "▸"
	glyphEllipsis = "…"
	// glyphPartial marks a count that is not final yet (caches syncing).
	glyphPartial = "~"
)

// cellDisplay is the unstyled text of a materialised cell as the TUI shows
// it; column widths use it.
func cellDisplay(c table.Cell, partial bool) string {
	switch c.Kind {
	case table.KindBool:
		if c.Bool != nil && *c.Bool {
			return glyphYes
		}
		return glyphNo
	case table.KindCount:
		if partial && c.Number != nil { // "-" stays "-": it will not change
			return glyphPartial + c.Text
		}
	}
	return c.Text
}

// cellStyled renders a cell, choosing style from its kind and semantic
// value. The data never carries styling.
func cellStyled(st styles, c table.Cell, role engine.Role, partial bool) string {
	text := cellDisplay(c, partial)
	if role == engine.RoleKind {
		return st.muted.Render(text) // kinds are secondary to names
	}
	switch c.Kind {
	case table.KindMode:
		if s, ok := st.modes[c.Mode]; ok {
			return s.Render(text)
		}
	case table.KindSeverity:
		if s, ok := st.severity[c.Severity]; ok {
			return s.Render(text)
		}
	case table.KindCount:
		if partial {
			return st.muted.Render(text) // not a final value yet
		}
		if c.Number != nil && *c.Number > 0 {
			return st.count.Render(text)
		}
		return st.zero.Render(text)
	case table.KindBool:
		if c.Bool != nil && *c.Bool {
			return st.yes.Render(text)
		}
		return st.no.Render(text)
	case table.KindAge:
		return st.muted.Render(text)
	}
	return st.cell.Render(text)
}

// fit pads or truncates s (ANSI-aware) to exactly w columns.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	if sw > w {
		return ansi.Truncate(s, w, glyphEllipsis)
	}
	return s + strings.Repeat(" ", w-sw)
}

// limitNotices keeps notices to about a quarter of height (at least one
// line), so a long list of RBAC gaps never hides the rows under it. The last
// kept line says how many were left out; `p9s get <view> -o json` and the
// report list them all.
func limitNotices(ns []string, height int) []string {
	keep := max(height/4, 1)
	if len(ns) <= keep {
		return ns
	}
	out := append([]string(nil), ns[:keep-1]...)
	return append(out, fmt.Sprintf("+%d more notices (p9s get -o json and p9s report list them all)", len(ns)-keep+1))
}
