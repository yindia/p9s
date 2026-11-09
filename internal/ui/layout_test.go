package ui

import (
	"strings"
	"testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/table"
)

// violationsLike is a :v-shaped table: owner, policy, kind, count, message.
func violationsLike(owner, policy, kind, msg string) *tableScreen {
	cols := []table.Column{
		{Title: "OWNER", Role: engine.RoleTarget, MinWidth: 20},
		{Title: "POLICY", Role: engine.RoleName},
		{Title: "KIND", Role: engine.RoleKind},
		{Title: "OBJECTS", Role: engine.RoleCount},
		{Title: "MESSAGE", Role: engine.RoleMessage, Flex: true},
	}
	row := table.Row{Cells: []table.Cell{{Text: owner}, {Text: policy}, {Text: kind}, {Text: "3"}, {Text: msg}}}
	tbl := table.Table{Columns: cols, Rows: []table.Row{row}, Sort: table.Sort{Column: -1}}
	return &tableScreen{tbl: tbl, visible: tbl.Rows}
}

func TestColumnWidths(t *testing.T) {
	owner := "Deployment/gatekeeper-system/gatekeeper-controller-manager" // 58
	policy := "container-ephemeral-storage-limit"                         // 33
	kind := "K8sContainerEphemeralStorageLimit"                           // 33
	msg := strings.Repeat("validation error: label 'team' is required ", 3)

	tests := []struct {
		name  string
		width int
		check func(t *testing.T, w []int)
	}{
		{"room to spare: full cells, uncapped, spare to the message", 400, func(t *testing.T, w []int) {
			if w[0] != len(owner) || w[1] != len(policy) || w[2] != len(kind) {
				t.Errorf("cells not shown in full: %v", w)
			}
			if usedWidth(w) != 400 {
				t.Errorf("message should take the spare width: used %d of 400", usedWidth(w))
			}
		}},
		{"slightly narrow: the message shrinks, nothing else", 170, func(t *testing.T, w []int) {
			if w[4] < minMessageWidth || w[0] != len(owner) || w[2] != len(kind) {
				t.Errorf("only the message should give way: %v", w)
			}
		}},
		{"narrow: the message is hidden before names are cut", 150, func(t *testing.T, w []int) {
			if w[4] != 0 {
				t.Errorf("message should be hidden: %v", w)
			}
			if w[1] != len(policy) {
				t.Errorf("policy cut while the message could go: %v", w)
			}
		}},
		{"very narrow: kinds give way before owners", 110, func(t *testing.T, w []int) {
			if w[2] > minKindWidth && w[0] < maxColWidth {
				t.Errorf("owner cut while the kind is still wide: %v", w)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := violationsLike(owner, policy, kind, msg).columnWidths(tt.width)
			if used := usedWidth(w); used > tt.width {
				t.Fatalf("layout %v is %d wide, over %d", w, used, tt.width)
			}
			tt.check(t, w)
		})
	}
}

func TestHiddenColumnIsNotDrawn(t *testing.T) {
	ts := violationsLike("Deployment/a/b", "p", "K", "a message nobody sees here")
	w := []int{14, 6, 4, 7, 0}
	header := ts.headerLine(styles{}, w)
	if strings.Contains(header, "MESSAGE") {
		t.Errorf("hidden column in header: %q", header)
	}
	if line := ts.rowLine(styles{}, ts.visible[0], w, 80, false); strings.Contains(line, "nobody") {
		t.Errorf("hidden column in row: %q", line)
	}
}

func TestColumnWidthsIgnoreOneOutlier(t *testing.T) {
	ts := violationsLike("Deployment/gatekeeper-system/gatekeeper-controller-manager",
		"container-must-have-cpu-requests-memory-limits-and-requests", "K8sRequiredResources", "msg")
	for i := 0; i < 9; i++ {
		ts.tbl.Rows = append(ts.tbl.Rows, table.Row{Cells: []table.Cell{
			{Text: "Deployment/kyverno/kyverno-reports-controller"}, {Text: "require-team-label"},
			{Text: "CPol"}, {Text: "3"}, {Text: "msg"}}})
	}
	ts.visible = ts.tbl.Rows
	w := ts.columnWidths(120)
	// The outlier is truncated (the column may get spare width back, but
	// never at the owner's expense).
	if w[1] >= len("container-must-have-cpu-requests-memory-limits-and-requests") {
		t.Errorf("one long policy name widened the column: %v", w)
	}
	if w[0] != len("Deployment/gatekeeper-system/gatekeeper-controller-manager") {
		t.Errorf("owner cut while the policy column held room for one outlier: %v", w)
	}
}
