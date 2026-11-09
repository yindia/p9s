package table_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/table"
)

var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func view(rows []engine.Row, err error) engine.View {
	return engine.View{
		Command: "x", Title: "Things",
		Columns: []engine.Column{
			{Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "MODE", Role: engine.RoleMode},
			{Title: "N", Role: engine.RoleCount}, {Title: "AGE", Role: engine.RoleAge}, {Title: "OK"},
			{Title: "SEV", Role: engine.RoleSeverity},
		},
		Rows: func(engine.Filter) ([]engine.Row, error) { return rows, err },
	}
}

func row(id string, n int) engine.Row {
	ref := model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "a", Name: id}
	return engine.Row{
		ID: id,
		Cells: []engine.Cell{
			engine.TextCell(id), engine.ModeCell(model.Enforcement{Mode: model.ModeEnforce, Raw: "deny"}),
			engine.CountCell(n), engine.AgeCell(now.Add(-90 * time.Second)), engine.BoolCell(n > 0),
			engine.SeverityCell(model.SeverityWarning),
		},
		Ref:    &ref,
		Object: &unstructured.Unstructured{Object: map[string]any{"kind": "Pod"}},
		Detail: func() (engine.Detail, error) { return engine.Detail{Title: id}, nil },
		Drill:  &engine.Target{Command: "v"},
	}
}

func TestBuildMaterialisesCells(t *testing.T) {
	tbl := table.Build(table.Source{View: view([]engine.Row{row("api", 12)}, nil)}, engine.Filter{}, now)
	if tbl.SchemaVersion != table.SchemaVersion || tbl.Title != "Things" || len(tbl.Columns) != 6 {
		t.Fatalf("table header = %+v", tbl)
	}
	r := tbl.Rows[0]
	if !r.CanDrill || !r.CanDescribe || !r.HasYAML || r.Target == nil || r.Target.Name != "api" {
		t.Errorf("row capabilities = %+v", r)
	}
	want := []struct {
		kind table.Kind
		text string
		sort string
	}{
		{table.KindText, "api", "api"},
		{table.KindMode, "enforce", "4"},
		{table.KindCount, "12", "000000000012"},
		{table.KindAge, "90s", r.Cells[3].SortKey}, // checked for order below
		{table.KindBool, "yes", "1"},
		{table.KindSeverity, "warning", "1"},
	}
	for i, w := range want {
		c := r.Cells[i]
		if c.Kind != w.kind || c.Text != w.text || c.SortKey != w.sort {
			t.Errorf("cell %d = %+v, want %+v", i, c, w)
		}
	}
	if r.Cells[1].Mode != model.ModeEnforce || r.Cells[5].Severity != model.SeverityWarning || *r.Cells[2].Number != 12 {
		t.Errorf("semantic values lost: %+v", r.Cells)
	}
}

func TestCountSortKeysOrderNumerically(t *testing.T) {
	a := table.BuildCell(engine.CountCell(0), now).SortKey
	_ = a
	a = table.BuildCell(engine.CountCell(9), now).SortKey
	b := table.BuildCell(engine.CountCell(10), now).SortKey
	if a >= b {
		t.Errorf("sort key of 9 (%q) must sort before 10 (%q)", a, b)
	}
	young := table.BuildCell(engine.AgeCell(now.Add(-time.Minute)), now).SortKey
	old := table.BuildCell(engine.AgeCell(now.Add(-time.Hour)), now).SortKey
	never := table.BuildCell(engine.AgeCell(time.Time{}), now)
	if young >= old || old >= never.SortKey {
		t.Errorf("age keys must order youngest, older, never: %q %q %q", young, old, never.SortKey)
	}
	if never.Text != "-" || never.Time != nil {
		t.Errorf("zero age = %+v, want - with no time", never)
	}
	if na := table.BuildCell(engine.NACount(), now); na.Text != "-" || na.Number != nil || na.SortKey >= a {
		t.Errorf("n/a count = %+v: '-' with no number, sorting below 0", na)
	}
}

func TestBuildContext(t *testing.T) {
	src := table.Source{
		View:    view(nil, errors.New("gatekeeper: boom")),
		Notices: func(engine.Filter) []string { return []string{"forbidden"} },
		Empty:   func(engine.Filter) string { return "nothing here" },
		Synced:  func(engine.Filter) bool { return false },
	}
	tbl := table.Build(src, engine.Filter{}, now)
	if tbl.Error != "gatekeeper: boom" || tbl.Empty != "nothing here" || !tbl.Partial || len(tbl.Notices) != 1 {
		t.Errorf("table = %+v", tbl)
	}
	tbl = table.Build(table.Source{View: view([]engine.Row{row("a", 1)}, nil), Empty: src.Empty}, engine.Filter{}, now)
	if tbl.Empty != "" || tbl.Partial {
		t.Errorf("Empty must only be set without rows, and nil Synced means synced: %+v", tbl)
	}
}

func TestDuplicateIDsAreMadeUnique(t *testing.T) {
	tbl := table.Build(table.Source{View: view([]engine.Row{row("a", 1), row("a", 1), row("a", 1)}, nil)}, engine.Filter{}, now)
	got := []string{tbl.Rows[0].ID, tbl.Rows[1].ID, tbl.Rows[2].ID}
	if strings.Join(got, ",") != "a,a#1,a#2" {
		t.Errorf("ids = %v", got)
	}
}

func TestMatch(t *testing.T) {
	tbl := table.Build(table.Source{View: view([]engine.Row{row("api", 0), row("worker", 3)}, nil)}, engine.Filter{}, now)
	tests := []struct {
		needle string
		want   int
	}{
		{"", 2}, {"API", 1}, {"enforce", 2}, {"3", 1},
		{"90s", 0}, // ages describe rows, they don't identify them
		{"yes", 0}, // nor do booleans
	}
	for _, tt := range tests {
		if got := len(tbl.Match(tt.needle)); got != tt.want {
			t.Errorf("Match(%q) = %d rows, want %d", tt.needle, got, tt.want)
		}
	}
}

func TestJSONIsVersionedAndPlain(t *testing.T) {
	tbl := table.Build(table.Source{View: view([]engine.Row{row("api", 2)}, nil)}, engine.Filter{}, now)
	b, err := json.Marshal(tbl)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"schemaVersion":"p9s/v1"`, `"kind":"count"`, `"number":2`, `"mode":"enforce"`, `"role":"name"`,
		`"target":{"version":"v1","kind":"Pod","namespace":"a","name":"api"}`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, `"Source"`) || strings.Contains(s, `"Drill"`) || strings.Contains(s, `"Detail"`) {
		t.Errorf("row functions must not serialise: %s", s)
	}
	var back table.Table
	if err := json.Unmarshal(b, &back); err != nil || back.Rows[0].Cells[0].Text != "api" {
		t.Errorf("round trip = %+v, %v", back, err)
	}
}

func TestDetailAndHelpers(t *testing.T) {
	d := table.BuildDetail(engine.Detail{Title: "T", Sections: []engine.Section{{Title: "Rego", Body: "x", Language: "rego"}}})
	if d.SchemaVersion != table.SchemaVersion || d.Sections[0].Language != "rego" {
		t.Errorf("detail = %+v", d)
	}
	if table.Age(now, now.Add(time.Minute)) != "0s" || table.Age(now, time.Time{}) != "-" {
		t.Error("Age must clamp future times and dash zero times")
	}
	if table.Dash("") != "-" || table.Dash("x") != "x" || table.OneLine("a\n  b") != "a b" {
		t.Error("Dash/OneLine")
	}
	// Short labels pad to a shared width, so sections line up with each other.
	if got := table.KV("A", "1", "Long", "2"); got != "A:             1\nLong:          2" {
		t.Errorf("KV = %q", got)
	}
	// A label past that width widens its own section.
	if got := table.KV("Failure policy", "Ignore", "A", "1"); got != "Failure policy:  Ignore\nA:               1" {
		t.Errorf("KV with a long label = %q", got)
	}
}

func TestObjectYAMLRedactsSecrets(t *testing.T) {
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata":   map[string]any{"name": "db", "managedFields": []any{"x"}},
		"data":       map[string]any{"password": "aHVudGVyMg=="},
		"stringData": map[string]any{"token": "plain"},
	}}
	y := table.ObjectYAML(secret)
	if strings.Contains(y, "aHVudGVyMg==") || strings.Contains(y, "plain") || strings.Contains(y, "managedFields") {
		t.Fatalf("secret leaked or managedFields kept:\n%s", y)
	}
	if strings.Count(y, table.Redacted) != 2 {
		t.Errorf("want both values redacted:\n%s", y)
	}
	cm := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "data": map[string]any{"k": "v"}}}
	if !strings.Contains(table.ObjectYAML(cm), "k: v") {
		t.Error("non-secret data must be shown")
	}
}

func sortView(rows []engine.Row, def engine.Sort) table.Source {
	v := view(rows, nil)
	v.DefaultSort = def
	return table.Source{View: v}
}

func ids(rows []table.Row) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return strings.Join(out, ",")
}

func TestDefaultAndUserSort(t *testing.T) {
	rows := []engine.Row{row("b", 2), row("a", 10), row("c", 2)}
	tbl := table.Build(sortView(rows, engine.Sort{Role: engine.RoleCount, Desc: true}), engine.Filter{}, now)
	if got := ids(tbl.Rows); got != "a,b,c" {
		t.Errorf("default count desc = %s; want a (10) first, ties in arrival order", got)
	}
	if tbl.Sort.Column != 2 || !tbl.Sort.Desc {
		t.Errorf("Sort = %+v", tbl.Sort)
	}
	byName := tbl.Sorted(tbl.ColumnFor(engine.RoleName), false)
	if ids(byName.Rows) != "a,b,c" || ids(tbl.Rows) != "a,b,c" {
		t.Errorf("Sorted must return a copy: %s / %s", ids(byName.Rows), ids(tbl.Rows))
	}
	if got := ids(tbl.Sorted(0, true).Rows); got != "c,b,a" {
		t.Errorf("name desc = %s", got)
	}
	unsorted := table.Build(sortView(rows, engine.Sort{}), engine.Filter{}, now)
	if unsorted.Sort.Column != -1 || ids(unsorted.Rows) != "b,a,c" {
		t.Errorf("no default sort keeps view order: %+v %s", unsorted.Sort, ids(unsorted.Rows))
	}
}

func TestFilterTerms(t *testing.T) {
	mk := func(id, ns string, mode model.EnforcementMode, engineName string) engine.Row {
		ref := model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: ns, Name: id}
		return engine.Row{ID: id, Ref: &ref, Cells: []engine.Cell{
			engine.TextCell(engineName), engine.TextCell(id), engine.ModeCell(model.Enforcement{Mode: mode}),
			engine.TextCell(ns), engine.TextCell("message about " + id),
		}}
	}
	v := engine.View{Command: "v", Title: "V", Columns: []engine.Column{
		{Title: "ENGINE", Role: engine.RoleEngine}, {Title: "NAME", Role: engine.RoleName},
		{Title: "MODE", Role: engine.RoleMode}, {Title: "NS", Role: engine.RoleNamespace}, {Title: "MSG", Role: engine.RoleMessage},
	}, Rows: func(engine.Filter) ([]engine.Row, error) {
		return []engine.Row{
			mk("api", "payments", model.ModeAudit, "kyverno"),
			mk("web", "search", model.ModeEnforce, "gatekeeper"),
			mk("job", "payments", model.ModeEnforce, "gatekeeper"),
			mk("gen", "search", model.ModeUnknown, "kyverno"),
		}, nil
	}}
	tbl := table.Build(table.Source{View: v}, engine.Filter{}, now)
	tests := []struct {
		q, want string
	}{
		{"mode:audit", "api"},
		{"mode:n/a", "gen"},
		{"engine:kyverno", "api,gen"},
		{"ns:payments", "api,job"},
		{"ns:payments mode:enforce", "job"},
		{"engine:gatekeeper web", "web"},
		{"MODE:Enforce", "web,job"},    // case-insensitive
		{"about:api", ""},              // unknown key is free text: "about:api" appears nowhere
		{"sev:error", ""},              // no severity column: nothing matches
		{"message", "api,web,job,gen"}, // free text
	}
	for _, tt := range tests {
		if got := ids(tbl.Match(tt.q)); got != tt.want {
			t.Errorf("Match(%q) = %q, want %q", tt.q, got, tt.want)
		}
	}
	if terms := table.ParseFilter("mode: ns:x"); len(terms) != 2 || terms[0].Role != engine.RoleNone || terms[1].Role != engine.RoleNamespace {
		t.Errorf("ParseFilter = %+v", terms)
	}
}

func TestAliasCellsFilterByFullValue(t *testing.T) {
	v := engine.View{Command: "p", Title: "P", Columns: []engine.Column{
		{Title: "NAME", Role: engine.RoleName}, {Title: "KIND", Role: engine.RoleKind},
	}, Rows: func(engine.Filter) ([]engine.Row, error) {
		return []engine.Row{
			{ID: "a", Cells: []engine.Cell{engine.TextCell("a"), engine.AliasCell("CPol", "ClusterPolicy")}},
			{ID: "b", Cells: []engine.Cell{engine.TextCell("b"), engine.AliasCell("VPol", "ValidatingPolicy")}},
		}, nil
	}}
	tbl := table.Build(table.Source{View: v}, engine.Filter{}, now)
	for q, want := range map[string]string{"kind:clusterpolicy": "a", "kind:cpol": "a", "validatingpolicy": "b", "kind:policy": "a,b"} {
		if got := ids(tbl.Match(q)); got != want {
			t.Errorf("Match(%q) = %q, want %q", q, got, want)
		}
	}
	if c := table.BuildCell(engine.AliasCell("Pol", "Pol"), now); c.Full != "" {
		t.Errorf("an alias equal to the value needs no Full: %+v", c)
	}
}

func TestNotApplicableSortsLastBothWays(t *testing.T) {
	na := row("na", 0)
	na.Cells[2] = engine.NACount()
	rows := []engine.Row{na, row("one", 1), row("five", 5)}
	tbl := table.Build(sortView(rows, engine.Sort{}), engine.Filter{}, now)
	if got := ids(tbl.Sorted(2, true).Rows); got != "five,one,na" {
		t.Errorf("desc = %s", got)
	}
	if got := ids(tbl.Sorted(2, false).Rows); got != "one,five,na" {
		t.Errorf("asc = %s", got)
	}
}

func TestFilterOrValues(t *testing.T) {
	mk := func(id string, m model.EnforcementMode) engine.Row {
		return engine.Row{ID: id, Cells: []engine.Cell{engine.TextCell(id), engine.ModeCell(model.Enforcement{Mode: m})}}
	}
	v := engine.View{Command: "p", Title: "P", Columns: []engine.Column{
		{Title: "NAME", Role: engine.RoleName}, {Title: "MODE", Role: engine.RoleMode},
	}, Rows: func(engine.Filter) ([]engine.Row, error) {
		return []engine.Row{mk("a", model.ModeAudit), mk("w", model.ModeWarn), mk("e", model.ModeEnforce)}, nil
	}}
	tbl := table.Build(table.Source{View: v}, engine.Filter{}, now)
	for q, want := range map[string]string{
		"mode:audit,warn": "a,w", "mode:warn,": "w", "mode:audit,warn name:w": "w", "a,w": "",
	} {
		if got := ids(tbl.Match(q)); got != want {
			t.Errorf("Match(%q) = %q, want %q", q, got, want)
		}
	}
}

// A policy's own severity filters exactly (sev:high) and sorts by weight,
// not alphabetically; a policy without one shows "-" and sorts lowest.
func TestPolicySeverityFilterAndSort(t *testing.T) {
	mk := func(id, sev string) engine.Row {
		return engine.Row{ID: id, Cells: []engine.Cell{engine.TextCell(id), engine.PolicySeverityCell(sev)}}
	}
	v := engine.View{Command: "pol", Title: "P", Columns: []engine.Column{
		{Title: "NAME", Role: engine.RoleName}, {Title: "SEVERITY", Role: engine.RoleSeverity},
	}, Rows: func(engine.Filter) ([]engine.Row, error) {
		return []engine.Row{mk("a", "medium"), mk("b", "critical"), mk("c", ""), mk("d", "high"), mk("e", "low")}, nil
	}}
	tbl := table.Build(table.Source{View: v}, engine.Filter{}, now)
	ids := func(rs []table.Row) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(tbl.Match("sev:high")); got != "d" {
		t.Errorf("sev:high = %q, want d", got)
	}
	if got := ids(tbl.Match("sev:high,critical")); got != "b,d" {
		t.Errorf("sev:high,critical = %q, want b,d", got)
	}
	if c := tbl.Rows[2].Cells[1]; c.Text != "-" {
		t.Errorf("no severity shows %q, want -", c.Text)
	}
	rank := map[string]string{}
	for _, r := range tbl.Rows {
		rank[r.ID] = r.Cells[1].SortKey
	}
	if rank["b"] <= rank["d"] || rank["d"] <= rank["a"] || rank["a"] <= rank["e"] || rank["e"] < rank["c"] {
		t.Errorf("sort keys do not order critical > high > medium > low >= none: %v", rank)
	}
}

func TestCheckFilter(t *testing.T) {
	tbl := table.Build(table.Source{View: view(nil, nil)}, engine.Filter{}, now) // NAME MODE N AGE OK SEV
	for _, tc := range []struct {
		q               string
		problems, notes int
	}{
		{"mode:audit sev:warning name:x ns:a", 0, 0},
		{"mode:audti", 1, 0},
		{"sev:high,hgih", 1, 0},
		{"engine:kyverno", 1, 0}, // no ENGINE column
		{"foo:bar", 0, 1},        // free text, but probably a typo
		{"plain text", 0, 0},
	} {
		p, n := tbl.CheckFilter(tc.q)
		if len(p) != tc.problems || len(n) != tc.notes {
			t.Errorf("CheckFilter(%q) = %q, %q; want %d problems, %d notes", tc.q, p, n, tc.problems, tc.notes)
		}
	}
}

// sev: on a health STATUS cell ("✗ fail-open, …") matches its level.
func TestSeverityFilterMatchesHealthLevel(t *testing.T) {
	v := engine.View{Command: "h", Title: "H", Columns: []engine.Column{{Title: "CHECK", Role: engine.RoleName}, {Title: "STATUS", Role: engine.RoleSeverity}},
		Rows: func(engine.Filter) ([]engine.Row, error) {
			return []engine.Row{
				{ID: "a", Cells: []engine.Cell{engine.TextCell("a"), {Kind: engine.CellSeverity, Severity: model.SeverityError, Text: "✗ fail-open"}}},
				{ID: "b", Cells: []engine.Cell{engine.TextCell("b"), {Kind: engine.CellSeverity, Severity: model.SeverityInfo, Text: "✓"}}},
			}, nil
		}}
	tbl := table.Build(table.Source{View: v}, engine.Filter{}, now)
	if got := tbl.Match("sev:error"); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("sev:error = %v, want a", got)
	}
}
