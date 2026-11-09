package ui

import (
	"slices"

	"github.com/charmbracelet/x/ansi"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/table"
)

const (
	colGap = 2
	// maxColWidth caps a column only when the table does not fit; with room
	// to spare every cell is shown in full.
	maxColWidth = 48
	minColWidth = 6
	// minNameWidth is what a trimmed policy name keeps: enough to tell
	// require-team-label from require-run-as-nonroot.
	minNameWidth = 20
	// minMessageWidth is how far a message column shrinks; past that it is
	// hidden, since the selected row's full message is under the table.
	minMessageWidth = 24
	// minKindWidth keeps "K8sContainer…"-style prefixes readable.
	minKindWidth = 12
	// typicalPercent of a column's cells fit its width once a table is too
	// wide; the rest are truncated.
	typicalPercent = 90
	// kindWeight is what showing a kind in full is worth next to a name,
	// owner or target when spare width is handed back.
	kindWeight = 0.25
)

// columnWidths lays out a table's columns for width terminal columns. The
// leading 2 hold the cursor glyph; a width of 0 hides a column.
//
// Every column gets its content width when that fits, and spare width goes
// only to message columns: a count stays next to the name it belongs to
// instead of being pushed to the right edge. When the table is too wide,
// space is taken in order of how little it costs the reader:
//
//  1. the message shrinks to minMessageWidth, then is hidden: a 24-column
//     prefix such as "validation error: label…" says nothing the status
//     line does not, and its room shows owners and policies in full;
//  2. kinds shrink to minKindWidth (the detail spells them out);
//  3. each column narrows to what most of its rows need (the 90th
//     percentile): one overlong name gets an ellipsis instead of widening
//     its column for every row; then cells are capped at maxColWidth;
//  4. the widest remaining column is trimmed, MinWidth columns last.
//
// Whatever is left over afterwards goes back where it shows the most cut
// cells in full (see giveBack).
func (t *tableScreen) columnWidths(width int) []int {
	cols := t.tbl.Columns
	stats := t.columnStats()
	w := make([]int, len(stats))
	for i, st := range stats {
		w[i] = st.natural
	}
	if spare := width - usedWidth(w); spare >= 0 {
		growMessages(cols, w, spare)
		return w
	}
	shrinkRole(cols, w, width, engine.RoleMessage, minMessageWidth)
	if usedWidth(w) > width {
		for i, c := range cols {
			if c.Role == engine.RoleMessage {
				w[i] = 0
			}
		}
	}
	shrinkRole(cols, w, width, engine.RoleKind, minKindWidth)
	if usedWidth(w) > width {
		for i, c := range cols {
			if w[i] > 0 {
				w[i] = min(w[i], stats[i].typical, max(maxColWidth, c.MinWidth))
			}
		}
	}
	trimWidest(cols, w, stats, width)
	giveBack(cols, w, stats, width-usedWidth(w))
	return w
}

// colStats is what a column's cells need: natural fits all of them,
// typical fits typicalPercent of them, and cells holds every width, sorted.
// All three are at least the column title.
type colStats struct {
	natural, typical int
	cells            []int
}

func (t *tableScreen) columnStats() []colStats {
	cols := t.tbl.Columns
	out := make([]colStats, len(cols))
	for i, c := range cols {
		title := ansi.StringWidth(c.Title)
		if i == t.tbl.Sort.Column {
			title++ // room for the sort arrow
		}
		floor := max(title, c.MinWidth, 1)
		cells := make([]int, 0, len(t.visible))
		for _, r := range t.visible {
			if i < len(r.Cells) {
				cells = append(cells, ansi.StringWidth(cellDisplay(r.Cells[i], t.tbl.Partial)))
			}
		}
		slices.Sort(cells)
		st := colStats{natural: floor, typical: floor, cells: cells}
		if len(cells) > 0 {
			st.natural = max(floor, cells[len(cells)-1])
			st.typical = max(floor, cells[(len(cells)*typicalPercent+99)/100-1])
		}
		out[i] = st
	}
	return out
}

// usedWidth is the line width of w: the cursor gutter, visible columns and
// the gaps between them.
func usedWidth(w []int) int {
	used, shown := 2, 0
	for _, x := range w {
		if x > 0 {
			used += x
			shown++
		}
	}
	return used + colGap*max(shown-1, 0)
}

func growMessages(cols []table.Column, w []int, spare int) {
	var grow []int
	for i, c := range cols {
		if c.Flex && c.Role == engine.RoleMessage {
			grow = append(grow, i)
		}
	}
	for _, i := range grow {
		w[i] += spare / len(grow)
	}
}

// shrinkRole narrows columns with role down to floor (or their MinWidth)
// until the table fits.
func shrinkRole(cols []table.Column, w []int, width int, role engine.Role, floor int) {
	for i, c := range cols {
		over := usedWidth(w) - width
		if c.Role != role || w[i] == 0 || over <= 0 {
			continue
		}
		w[i] -= min(over, max(w[i]-max(floor, c.MinWidth), 0))
	}
}

// trimFloor is the narrowest trimWidest normally takes a column to. Short
// fixed-format cells (counts, ages, modes, severities, engine names) stay
// whole: "kyver…" or "OBJEC…" saves a few columns and tells the reader
// nothing. A policy name keeps enough to tell policies apart, and a column
// with a MinWidth (OWNER) keeps it.
func trimFloor(c table.Column, st colStats) int {
	switch c.Role {
	case engine.RoleCount, engine.RoleAge, engine.RoleMode, engine.RoleSeverity, engine.RoleEngine:
		return st.natural
	case engine.RoleName:
		return max(min(st.natural, minNameWidth), minColWidth)
	}
	return max(minColWidth, c.MinWidth)
}

// trimWidest takes one column at a time from the widest column, so long
// names give up space evenly: first down to each column's trimFloor, with
// MinWidth columns (OWNER) last. On a terminal too narrow for those floors
// every column gives way down to minColWidth.
func trimWidest(cols []table.Column, w []int, stats []colStats, width int) {
	pass := func(eligible func(i int) bool, floor func(i int) int) {
		for usedWidth(w) > width {
			widest := -1
			for i := range w {
				if w[i] == 0 || !eligible(i) || w[i] <= floor(i) {
					continue
				}
				if widest < 0 || w[i] > w[widest] {
					widest = i
				}
			}
			if widest < 0 {
				return
			}
			w[widest]--
		}
	}
	floor := func(i int) int { return trimFloor(cols[i], stats[i]) }
	pass(func(i int) bool { return cols[i].MinWidth == 0 }, floor)
	pass(func(i int) bool { return cols[i].MinWidth > 0 }, floor)
	pass(func(int) bool { return true }, func(int) int { return minColWidth })
}

// giveBack spends spare width on cut columns, each time where it shows the
// most truncated cells in full per column of width; a full kind counts for
// less than a full name, owner or target. Width that completes no cell goes
// to the column with the most cut cells, to show more of them. Hidden
// columns stay hidden.
func giveBack(cols []table.Column, w []int, stats []colStats, spare int) {
	for spare > 0 {
		best, to := bestExtension(cols, w, stats, spare)
		if best < 0 {
			break
		}
		spare -= to - w[best]
		w[best] = to
	}
	if spare <= 0 {
		return
	}
	most, cut := -1, 0
	for i, st := range stats {
		if n := len(st.cells) - upperBound(st.cells, w[i]); w[i] > 0 && n > cut {
			most, cut = i, n
		}
	}
	if most >= 0 {
		w[most] += min(spare, stats[most].natural-w[most])
	}
}

// bestExtension finds the column and new width (within spare) that shows
// the most cut cells in full per added column, weighted by role.
func bestExtension(cols []table.Column, w []int, stats []colStats, spare int) (best, to int) {
	best, bestScore := -1, 0.0
	for i, st := range stats {
		if w[i] == 0 {
			continue
		}
		weight := 1.0
		if cols[i].Role == engine.RoleKind {
			weight = kindWeight
		}
		lo := upperBound(st.cells, w[i])
		for j := lo; j < len(st.cells) && st.cells[j] <= w[i]+spare; j++ {
			score := weight * float64(j+1-lo) / float64(st.cells[j]-w[i])
			if score > bestScore {
				best, to, bestScore = i, st.cells[j], score
			}
		}
	}
	return best, to
}

// upperBound is the index of the first value in sorted greater than v.
func upperBound(sorted []int, v int) int {
	i, _ := slices.BinarySearch(sorted, v+1)
	return i
}
