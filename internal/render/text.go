// Package render prints materialised views for non-interactive commands
// (p9s get): plain text tables and JSON. Like the TUI it only renders; all
// collecting, counting and sorting happened in query, views and table.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

// JSON writes v indented, with a trailing newline.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Table writes t as aligned plain text: notices, header, rows. Cells are
// never truncated, so output stays greppable.
func Table(w io.Writer, t table.Table) error {
	var b strings.Builder
	writeTable(&b, t, "")
	_, err := io.WriteString(w, b.String())
	return err
}

func writeTable(b *strings.Builder, t table.Table, indent string) {
	for _, s := range t.Summary {
		fmt.Fprintf(b, "%s%s\n", indent, s)
	}
	for _, n := range t.Notices {
		fmt.Fprintf(b, "%s! %s\n", indent, n)
	}
	if t.Partial {
		fmt.Fprintf(b, "%s! caches are still loading: counts are not final\n", indent)
	}
	if t.Error != "" {
		fmt.Fprintf(b, "%s✗ %s\n", indent, t.Error)
	}
	if len(t.Rows) == 0 {
		if t.Empty != "" {
			fmt.Fprintf(b, "%s%s\n", indent, t.Empty)
		}
		return
	}
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = runewidth.StringWidth(header(t, i, c))
	}
	for _, r := range t.Rows {
		for i := range t.Columns {
			if i < len(r.Cells) {
				widths[i] = max(widths[i], runewidth.StringWidth(r.Cells[i].Text))
			}
		}
	}
	cells := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cells[i] = header(t, i, c)
	}
	line(b, indent, cells, widths)
	for _, r := range t.Rows {
		for i := range t.Columns {
			cells[i] = ""
			if i < len(r.Cells) {
				cells[i] = r.Cells[i].Text
			}
		}
		line(b, indent, cells, widths)
	}
}

// header marks the sorted column like the TUI does.
func header(t table.Table, i int, c table.Column) string {
	if i == t.Sort.Column && t.Sort.Column >= 0 && len(t.Rows) > 0 {
		if t.Sort.Desc {
			return c.Title + "↓"
		}
		return c.Title + "↑"
	}
	return c.Title
}

func line(b *strings.Builder, indent string, cells []string, widths []int) {
	b.WriteString(indent)
	for i, c := range cells {
		if i == len(cells)-1 {
			b.WriteString(c) // no trailing padding
			break
		}
		b.WriteString(c)
		b.WriteString(strings.Repeat(" ", widths[i]-runewidth.StringWidth(c)+2))
	}
	b.WriteString("\n")
}

// Overview writes the overview's blocks one after another.
func Overview(w io.Writer, o views.Overview) error {
	var b strings.Builder
	for i, blk := range o.Blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("== " + blk.Title + "\n")
		if blk.Summary != "" {
			b.WriteString(blk.Summary + "\n")
		}
		if blk.Placeholder != "" {
			b.WriteString(blk.Placeholder + "\n")
			continue
		}
		writeTable(&b, blk.Table, "")
		if n := len(blk.Table.Rows); blk.Total > n {
			fmt.Fprintf(&b, "(%d of %d)\n", n, blk.Total)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Dashboard writes :health: the overall grade, notices, then each panel.
func Dashboard(w io.Writer, d views.Dashboard) error {
	var b strings.Builder
	o := d.Overall
	fmt.Fprintf(&b, "OVERALL  ✗ %d critical  ! %d warning  ✓ %d ok", o.Critical, o.Warning, o.OK)
	if o.Unknown > 0 {
		fmt.Fprintf(&b, "  ? %d unknown", o.Unknown)
	}
	if !d.Synced {
		b.WriteString("  (syncing: grades are provisional)")
	}
	b.WriteString("\n")
	for _, n := range d.Notices {
		fmt.Fprintf(&b, "! %s\n", n)
	}
	for _, p := range d.Panels {
		fmt.Fprintf(&b, "\n== %s\n", p.Title)
		writeTable(&b, p.Table, "")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
