// Package table materialises an engine.View into a plain, JSON-serialisable
// Table that any renderer (the TUI, `p9s get`, `p9s report`, a future web
// UI) can consume. It holds data only: styling is chosen by each renderer
// from a cell's kind and semantic value, never stored here. It must not
// import anything terminal-specific.
package table

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
)

// SchemaVersion versions every serialised Table, Detail and snapshot.
const SchemaVersion = "p9s/v1"

// Source is a view plus the context a renderer needs around its rows.
type Source struct {
	View engine.View
	// Summary is headline text shown above the rows (not a warning).
	// Optional.
	Summary func(engine.Filter) []string
	// Notices are shown above the rows, e.g. RBAC denials. Optional.
	Notices func(engine.Filter) []string
	// Empty explains why there are no rows. Optional.
	Empty func(engine.Filter) string
	// Synced reports whether the data behind the view finished loading.
	// Nil means always synced.
	Synced func(engine.Filter) bool
}

// Table is a materialised view.
type Table struct {
	SchemaVersion string   `json:"schemaVersion"`
	Command       string   `json:"command"`
	Title         string   `json:"title"`
	Columns       []Column `json:"columns"`
	Rows          []Row    `json:"rows"`
	// Sort is the order Rows are in.
	Sort Sort `json:"sort"`
	// Partial is set while caches are still loading: counts are not final.
	Partial bool     `json:"partial,omitempty"`
	Summary []string `json:"summary,omitempty"`
	Notices []string `json:"notices,omitempty"`
	Error   string   `json:"error,omitempty"`
	// Empty explains an empty table; set only when Rows is empty.
	Empty string `json:"empty,omitempty"`
}

// Column is a table column.
type Column struct {
	Title    string      `json:"title"`
	Role     engine.Role `json:"role,omitempty"`
	Flex     bool        `json:"flex,omitempty"`
	MinWidth int         `json:"-"`
}

// Sort is a column index (-1: unsorted) and direction.
type Sort struct {
	Column int  `json:"column"`
	Desc   bool `json:"desc,omitempty"`
}

// Kind tells a renderer what a cell holds.
type Kind string

// Cell kinds.
const (
	KindText     Kind = "text"
	KindAge      Kind = "age"
	KindCount    Kind = "count"
	KindMode     Kind = "mode"
	KindBool     Kind = "bool"
	KindSeverity Kind = "severity"
)

// Cell is one plain value. Text is always set and is what filters match;
// typed fields carry the semantic value renderers style by.
type Cell struct {
	Kind     Kind                  `json:"kind"`
	Text     string                `json:"text"`
	Mode     model.EnforcementMode `json:"mode,omitempty"`
	Severity model.Severity        `json:"severity,omitempty"`
	Number   *int                  `json:"number,omitempty"`
	Bool     *bool                 `json:"bool,omitempty"`
	Time     *time.Time            `json:"time,omitempty"`
	// Full is the unabbreviated value when Text is an alias.
	Full string `json:"full,omitempty"`
	// SortKey orders rows by this column (numbers and times sort correctly).
	SortKey string `json:"sortKey"`
}

// Row is a materialised row.
type Row struct {
	ID     string           `json:"id"`
	Cells  []Cell           `json:"cells"`
	Target *model.ObjectRef `json:"target,omitempty"`
	// Policy is set when the row is one policy.
	Policy *model.PolicyKey `json:"policy,omitempty"`
	// What the row supports, so renderers only offer valid actions.
	CanDrill    bool `json:"canDrill,omitempty"`
	CanDescribe bool `json:"canDescribe,omitempty"`
	HasYAML     bool `json:"hasYAML,omitempty"`
	// Source keeps the row's functions (detail, drill) for interactive
	// renderers. It is never serialised.
	Source engine.Row `json:"-"`
}

// Build materialises src for filter f, rendering ages against now.
func Build(src Source, f engine.Filter, now time.Time) Table {
	v := src.View
	t := Table{SchemaVersion: SchemaVersion, Command: v.Command, Title: v.Title}
	for _, c := range v.Columns {
		t.Columns = append(t.Columns, Column{Title: c.Title, Role: c.Role, Flex: c.Flex, MinWidth: c.MinWidth})
	}
	if src.Summary != nil {
		t.Summary = src.Summary(f)
	}
	if src.Notices != nil {
		t.Notices = src.Notices(f)
	}
	rows, err := v.Rows(f)
	if err != nil {
		t.Error = err.Error()
	}
	for _, r := range uniqueIDs(rows) {
		t.Rows = append(t.Rows, buildRow(r, now))
	}
	if len(t.Rows) == 0 && src.Empty != nil {
		t.Empty = src.Empty(f)
	}
	t.Partial = src.Synced != nil && !src.Synced(f)
	t.Sort = Sort{Column: -1}
	if col := t.ColumnFor(v.DefaultSort.Role); col >= 0 && v.DefaultSort.Role != engine.RoleNone {
		t = t.Sorted(col, v.DefaultSort.Desc)
	}
	return t
}

// ColumnFor returns the index of the first column with role, or -1.
func (t Table) ColumnFor(role engine.Role) int {
	for i, c := range t.Columns {
		if c.Role == role {
			return i
		}
	}
	return -1
}

// Sorted returns a copy ordered by column col using cell sort keys. Ties
// keep the current order, so sorts compose (e.g. by count, then name).
func (t Table) Sorted(col int, desc bool) Table {
	if col < 0 || col >= len(t.Columns) {
		return t
	}
	rows := append([]Row(nil), t.Rows...)
	key := func(r Row) string {
		if col < len(r.Cells) {
			return r.Cells[col].SortKey
		}
		return ""
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := key(rows[i]), key(rows[j])
		if a == "" || b == "" {
			return a != "" && b == "" // not-applicable values sort last both ways
		}
		if desc {
			return a > b
		}
		return a < b
	})
	t.Rows, t.Sort = rows, Sort{Column: col, Desc: desc}
	return t
}

func buildRow(r engine.Row, now time.Time) Row {
	out := Row{
		ID: r.ID, Target: r.Ref, Policy: r.Policy, Source: r,
		CanDrill: r.Drill != nil, CanDescribe: r.Detail != nil, HasYAML: r.Object != nil,
	}
	out.Cells = make([]Cell, len(r.Cells))
	for i, c := range r.Cells {
		out.Cells[i] = BuildCell(c, now)
	}
	return out
}

// BuildCell materialises one cell.
func BuildCell(c engine.Cell, now time.Time) Cell {
	switch c.Kind {
	case engine.CellAge:
		// The key orders by age (youngest first when ascending), not by
		// timestamp; "never" sorts after every real age.
		cell := Cell{Kind: KindAge, Text: Age(now, c.Time), SortKey: "~"}
		if !c.Time.IsZero() {
			t := c.Time
			cell.Time = &t
			cell.SortKey = fmt.Sprintf("%020d", math.MaxInt64-t.UnixNano())
		}
		return cell
	case engine.CellCount:
		if c.NA {
			// Not applicable sorts below every real count, including 0.
			return Cell{Kind: KindCount, Text: "-", SortKey: ""}
		}
		n := c.Count
		return Cell{Kind: KindCount, Text: c.Text, Number: &n, SortKey: fmt.Sprintf("%012d", n)}
	case engine.CellMode:
		return Cell{Kind: KindMode, Text: c.Text, Mode: c.Mode, SortKey: fmt.Sprint(modeRank[c.Mode])}
	case engine.CellBool:
		b := c.Bool
		text, key := "no", "0"
		if b {
			text, key = "yes", "1"
		}
		return Cell{Kind: KindBool, Text: text, Bool: &b, SortKey: key}
	case engine.CellSeverity:
		rank, ok := policySeverityRank[strings.ToLower(c.Text)]
		if !ok {
			rank = severityRank[c.Severity]
		}
		return Cell{Kind: KindSeverity, Text: c.Text, Severity: c.Severity, SortKey: fmt.Sprint(rank)}
	default:
		return Cell{Kind: KindText, Text: c.Text, Full: c.Full, SortKey: strings.ToLower(c.Text)}
	}
}

var modeRank = map[model.EnforcementMode]int{
	model.ModeUnknown: 0, model.ModeMutate: 1, model.ModeAudit: 2, model.ModeWarn: 3, model.ModeEnforce: 4,
}

// policySeverityRank orders a policy's own severity (see
// engine.PolicySeverityCell); health severities use severityRank.
var policySeverityRank = map[string]int{"info": 0, "low": 1, "medium": 2, "high": 3, "critical": 4}

var severityRank = map[model.Severity]int{
	model.SeverityInfo: 0, model.SeverityWarning: 1, model.SeverityError: 2,
}

// filterKeys maps "/" filter term keys to column roles.
var filterKeys = map[string]engine.Role{
	"engine": engine.RoleEngine, "mode": engine.RoleMode, "kind": engine.RoleKind,
	"ns": engine.RoleNamespace, "namespace": engine.RoleNamespace,
	"sev": engine.RoleSeverity, "severity": engine.RoleSeverity, "name": engine.RoleName,
}

// Term is one parsed filter term: key:value for a column role, or free text.
type Term struct {
	Role  engine.Role
	Value string
}

// ParseFilter splits "mode:audit ns:payments owner" into terms. Unknown
// keys ("foo:bar") are free text, so messages with colons still match.
func ParseFilter(q string) []Term {
	var out []Term
	for _, f := range strings.Fields(strings.ToLower(q)) {
		if k, v, ok := strings.Cut(f, ":"); ok && v != "" {
			if role, known := filterKeys[k]; known {
				out = append(out, Term{Role: role, Value: v})
				continue
			}
		}
		out = append(out, Term{Value: f})
	}
	return out
}

// filterValues are the values a mode: or sev: term can match; anything else
// is a typo that would silently match nothing.
var filterValues = map[engine.Role][]string{
	engine.RoleMode:     {"audit", "warn", "enforce", "mutate", "n/a"},
	engine.RoleSeverity: {"critical", "high", "medium", "low", "info", "warning", "error", "-"},
}

// CheckFilter reports what in a filter query cannot work on this table:
// problems are key:value terms for a column the view lacks (ns: also
// matches row targets, so it is always allowed) and mode:/sev: values no
// row can have. notes are key:value-looking terms with an unknown key: they
// stay free text (messages contain colons), but are likely typos.
func (t Table) CheckFilter(q string) (problems, notes []string) {
	for _, f := range strings.Fields(strings.ToLower(q)) {
		k, v, ok := strings.Cut(f, ":")
		if !ok || v == "" {
			continue
		}
		role, known := filterKeys[k]
		if !known {
			notes = append(notes, fmt.Sprintf("%q is not a filter key (%s); matched as free text", k+":", strings.Join(t.FilterKeys(), ", ")))
			continue
		}
		if role != engine.RoleNamespace && t.ColumnFor(role) < 0 {
			problems = append(problems, fmt.Sprintf("this view has no column for %s: (filter keys here: %s)", k, strings.Join(t.FilterKeys(), ", ")))
			continue
		}
		if allowed, ok := filterValues[role]; ok {
			for _, one := range strings.Split(v, ",") {
				if one != "" && !slices.Contains(allowed, one) {
					problems = append(problems, fmt.Sprintf("%s:%s matches nothing (values: %s)", k, one, strings.Join(allowed, ", ")))
				}
			}
		}
	}
	return problems, notes
}

// FilterKeys lists the key: terms that work on this table, in a stable order.
func (t Table) FilterKeys() []string {
	out := []string{"ns:"}
	for _, k := range []string{"engine", "mode", "kind", "sev", "name"} {
		if t.ColumnFor(filterKeys[k]) >= 0 {
			out = append(out, k+":")
		}
	}
	return out
}

// Match returns the rows matching every term of the filter query. Free text
// matches any text cell (ages and booleans describe rows, they don't
// identify them); key:value terms match the column with that role. ns: also
// matches a row's target namespace, so it works on views without a
// namespace column. A key whose column the view lacks matches nothing.
func (t Table) Match(q string) []Row {
	terms := ParseFilter(q)
	if len(terms) == 0 {
		return t.Rows
	}
	var out []Row
	for _, r := range t.Rows {
		if t.matchesAll(r, terms) {
			out = append(out, r)
		}
	}
	return out
}

func (t Table) matchesAll(r Row, terms []Term) bool {
	for _, term := range terms {
		if !t.matches(r, term) {
			return false
		}
	}
	return true
}

// matches applies one term. A comma in a column term means any of the
// values ("mode:audit,warn"); free text never splits, so "a,b" stays literal.
func (t Table) matches(r Row, term Term) bool {
	if term.Role != engine.RoleNone && strings.Contains(term.Value, ",") {
		for _, v := range strings.Split(term.Value, ",") {
			if v != "" && t.matches(r, Term{Role: term.Role, Value: v}) {
				return true
			}
		}
		return false
	}
	if term.Role == engine.RoleNone {
		for _, c := range r.Cells {
			if c.Kind != KindAge && c.Kind != KindBool && (contains(c.Text, term.Value) || contains(c.Full, term.Value)) {
				return true
			}
		}
		return false
	}
	if term.Role == engine.RoleNamespace && r.Target != nil {
		ref := r.Target
		if strings.EqualFold(ref.Namespace, term.Value) || (ref.Kind == "Namespace" && strings.EqualFold(ref.Name, term.Value)) {
			return true
		}
	}
	col := t.ColumnFor(term.Role)
	if col < 0 || col >= len(r.Cells) {
		return false
	}
	c := r.Cells[col]
	if term.Role == engine.RoleSeverity && !strings.EqualFold(c.Text, term.Value) {
		// Health cells read "✗ fail-open, …": match their level instead.
		_, policySev := policySeverityRank[strings.ToLower(c.Text)]
		return !policySev && c.Severity != "" && strings.EqualFold(string(c.Severity), term.Value)
	}
	if term.Role == engine.RoleMode || term.Role == engine.RoleSeverity || term.Role == engine.RoleNamespace {
		return strings.EqualFold(c.Text, term.Value) // exact: "audit" must not match "n/a"
	}
	return contains(c.Text, term.Value) || contains(c.Full, term.Value)
}

func contains(s, lowerNeedle string) bool {
	return s != "" && strings.Contains(strings.ToLower(s), lowerNeedle)
}

// uniqueIDs suffixes duplicate row IDs (identical violations can repeat) so
// selection tracking stays unambiguous. Rows are copied, not mutated.
func uniqueIDs(rows []engine.Row) []engine.Row {
	seen := make(map[string]int, len(rows))
	out := make([]engine.Row, len(rows))
	for i, r := range rows {
		id := r.ID
		if n := seen[id]; n > 0 {
			r.ID = fmt.Sprintf("%s#%d", id, n)
		}
		seen[id]++
		out[i] = r
	}
	return out
}
