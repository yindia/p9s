package engine

import (
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/model"
)

// View is an engine-specific table the UI renders generically.
type View struct {
	// Command is the ":" command, and must start with "<Prefix>/".
	Command     string
	Aliases     []string
	Title       string
	Description string
	Columns     []Column
	// DefaultSort orders rows when the user has not chosen a sort.
	DefaultSort Sort
	// Rows returns the rows matching the filter. Engines apply the policy
	// fields of the filter themselves; the UI applies Filter.Text.
	Rows func(Filter) ([]Row, error)
	// Keys are view-specific single-key actions (an uppercase letter the
	// shared keys do not use), e.g. a toggle. Optional.
	Keys []ViewKey
}

// ViewKey is a view-specific key. Do changes view state (never the
// cluster) and returns the message to show; rows are rebuilt afterwards.
type ViewKey struct {
	Key  string
	Help string
	Do   func() string
}

// Sort orders a view by the column with Role.
type Sort struct {
	Role Role
	Desc bool
}

// Role says what a column holds. Renderers use it for sort keys
// (shift+n/v/a/s), "/" filter terms (engine:, mode:, ns:, kind:) and the
// status line (the message column).
type Role string

// Column roles.
const (
	RoleNone      Role = ""
	RoleName      Role = "name"
	RoleKind      Role = "kind"
	RoleEngine    Role = "engine"
	RoleNamespace Role = "namespace"
	RoleMode      Role = "mode"
	RoleCount     Role = "count"
	RoleAge       Role = "age"
	RoleSeverity  Role = "severity"
	RoleMessage   Role = "message"
	RoleTarget    Role = "target"
)

// ValidRoles lists every known role.
var ValidRoles = map[Role]bool{
	RoleNone: true, RoleName: true, RoleKind: true, RoleEngine: true, RoleNamespace: true, RoleMode: true,
	RoleCount: true, RoleAge: true, RoleSeverity: true, RoleMessage: true, RoleTarget: true,
}

// Column describes a table column.
type Column struct {
	Title string
	Role  Role
	// MinWidth is a hint; the UI may widen columns to fit content.
	MinWidth int
	// Flex columns absorb remaining width.
	Flex bool
}

// CellKind tells the UI how to render a cell.
type CellKind int

const (
	CellText CellKind = iota
	CellAge
	CellCount
	CellMode
	CellBool
	CellSeverity
)

// Cell is one table cell. Times are rendered relative to the UI clock, which
// keeps engines free of "now" and makes output deterministic in tests.
type Cell struct {
	Kind     CellKind
	Text     string
	Time     time.Time
	Count    int
	Mode     model.EnforcementMode
	Bool     bool
	Severity model.Severity
	// NA marks a count the engine cannot provide (rendered "-", not 0).
	NA bool
	// Full is the unabbreviated value when Text is an alias (e.g. "CPol"
	// for ClusterPolicy); filters match either.
	Full string
}

// AliasCell shows a short alias but keeps the full value for filtering.
func AliasCell(short, full string) Cell {
	c := Cell{Kind: CellText, Text: short}
	if full != short {
		c.Full = full
	}
	return c
}

// TextCell returns a plain text cell.
func TextCell(s string) Cell { return Cell{Kind: CellText, Text: s} }

// AgeCell returns a cell rendered as the age of t. A zero t renders as "-".
func AgeCell(t time.Time) Cell { return Cell{Kind: CellAge, Time: t} }

// CountCell returns a numeric cell highlighted when non-zero.
func CountCell(n int) Cell { return Cell{Kind: CellCount, Count: n, Text: strconv.Itoa(n)} }

// NACount returns a count cell for a number the engine cannot provide.
func NACount() Cell { return Cell{Kind: CellCount, NA: true, Text: "-"} }

// KnownCount returns CountCell(n) when known, otherwise NACount.
func KnownCount(n int, known bool) Cell {
	if !known {
		return NACount()
	}
	return CountCell(n)
}

// ModeCell returns an enforcement-mode cell showing the one normalised word;
// the engine's own term belongs in detail views.
func ModeCell(e model.Enforcement) Cell {
	return Cell{Kind: CellMode, Mode: e.Mode, Text: e.Mode.Label()}
}

// SeverityCell returns a health severity, styled by its meaning.
func SeverityCell(s model.Severity) Cell {
	return Cell{Kind: CellSeverity, Severity: s, Text: string(s)}
}

// BoolCell returns a cell rendered as a yes/no indicator.
func BoolCell(b bool) Cell { return Cell{Kind: CellBool, Bool: b} }

// Row is a table row.
type Row struct {
	// ID is stable across refreshes so the UI can keep the selection.
	ID    string
	Cells []Cell
	// Object backs the YAML (y) and editor (e) actions. Optional.
	Object *unstructured.Unstructured
	// Ref backs the jump-to-object (g) action. Optional.
	Ref *model.ObjectRef
	// Detail backs the describe (d) action. Optional.
	Detail func() (Detail, error)
	// Drill is where enter navigates. When nil, enter shows Detail.
	Drill *Target
	// NoObject explains why y / e have nothing to show when Object is nil
	// (e.g. RBAC hides the policy). Optional.
	NoObject string
	// Policy is set on rows that are one policy, for actions on it such as
	// the enforcement impact (i). Optional.
	Policy *model.PolicyKey
}

// Detail is a describe page made of sections.
type Detail struct {
	Title    string
	Sections []Section
}

// Section is one block of a Detail page. Language selects highlighting:
// "rego", "yaml", "cel" or "" for plain text.
type Section struct {
	Title    string
	Body     string
	Language string
}

// Target is a navigation target: a ":" command plus a filter.
type Target struct {
	Command string
	Filter  Filter
}

// Filter narrows the engine-neutral views. Empty fields match everything.
type Filter struct {
	Engine          string
	PolicyKind      string
	PolicyName      string
	PolicyNamespace string
	// Namespace scopes results to objects in one namespace (the ":ns"
	// selection). It applies to violation targets and namespaced policies;
	// cluster-scoped policies always pass it.
	Namespace string
	// Owner and Group select one violation group in the shared views (a
	// top-level owner's ref string, or a group-label value). Engines ignore
	// them.
	Owner string
	Group string
	// Text is applied as the "/" filter.
	Text string
}

// IsZero reports whether the filter matches everything.
func (f Filter) IsZero() bool { return f == Filter{} }

// MatchesPolicy reports whether a policy key passes the filter.
func (f Filter) MatchesPolicy(k model.PolicyKey) bool {
	return match(f.Engine, k.Engine) &&
		match(f.PolicyKind, k.Kind) &&
		match(f.PolicyName, k.Name) &&
		match(f.PolicyNamespace, k.Namespace)
}

// MatchesNamespace reports whether an object in namespace ns passes the
// namespace scope. Cluster-scoped objects (ns == "") always pass.
func (f Filter) MatchesNamespace(ns string) bool {
	return f.Namespace == "" || ns == "" || ns == f.Namespace
}

// MatchesTarget reports whether a violation target is inside the scope.
// Unlike MatchesNamespace, cluster-scoped targets are excluded when a
// namespace is selected, because they are not "in" that namespace.
func (f Filter) MatchesTarget(ref model.ObjectRef) bool {
	if f.Namespace == "" || ref.Namespace == f.Namespace {
		return true
	}
	// The Namespace object itself belongs to its own scope.
	return ref.Group == "" && ref.Kind == "Namespace" && ref.Name == f.Namespace
}

func match(want, got string) bool { return want == "" || want == got }
