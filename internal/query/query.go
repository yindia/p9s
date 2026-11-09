// Package query holds cross-engine logic: collecting from every active
// engine, filtering, counting, totals and the reasons a view is empty. It is
// shared by the TUI and by non-interactive commands, so it must not import
// anything UI- or terminal-specific.
package query

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
)

// AllNamespaces is the namespace entry that clears the scope.
const AllNamespaces = "all"

// Capability selects engines by what they support.
type Capability func(engine.Capabilities) bool

// Capability predicates.
var (
	HasPolicies   Capability = func(c engine.Capabilities) bool { return c.Policies }
	HasViolations Capability = func(c engine.Capabilities) bool { return c.Violations }
	HasMutations  Capability = func(c engine.Capabilities) bool { return c.Mutations }
	HasHealth     Capability = func(c engine.Capabilities) bool { return c.Health }
)

// Capable returns the engines that pass the filter's engine field and have
// the capability.
func Capable(es []engine.Engine, f engine.Filter, has Capability) []engine.Engine {
	var out []engine.Engine
	for _, e := range es {
		if (f.Engine == "" || f.Engine == e.Name()) && has(e.Capabilities()) {
			out = append(out, e)
		}
	}
	return out
}

// Collect gathers items from every engine, keeping partial results when one
// engine fails so a single broken engine never blanks a view.
func Collect[T any](es []engine.Engine, get func(engine.Engine) ([]T, error)) ([]T, error) {
	var out []T
	var errs []error
	for _, e := range es {
		items, err := get(e)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		out = append(out, items...)
	}
	return out, errors.Join(errs...)
}

// PolicyEntry is a policy as a view shows it under a filter.
type PolicyEntry struct {
	model.Policy
	// LowerBound is set when the namespace-scoped count comes from a list
	// the engine capped, so the true number may be higher.
	LowerBound bool
}

// Policies returns the policies passing f, sorted by key. Under a namespace
// scope, ViolationCount counts only reported violations inside it.
func Policies(es []engine.Engine, f engine.Filter) ([]PolicyEntry, error) {
	ps, err := Collect(Capable(es, f, HasPolicies), engine.Engine.Policies)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Key().String() < ps[j].Key().String() })
	scoped, truncated := ScopedCounts(es, f)
	out := make([]PolicyEntry, 0, len(ps))
	for _, p := range ps {
		if !f.MatchesPolicy(p.Key()) || !f.MatchesNamespace(p.Namespace) {
			continue
		}
		if scoped != nil {
			p.ViolationCount = scoped[p.Key()]
		}
		out = append(out, PolicyEntry{Policy: p, LowerBound: truncated[p.Key()]})
	}
	return out, err
}

// ScopedCounts counts reported violations per policy inside f's namespace,
// and which policies' lists were truncated. Nil maps without a namespace.
func ScopedCounts(es []engine.Engine, f engine.Filter) (map[model.PolicyKey]int, map[model.PolicyKey]bool) {
	if f.Namespace == "" {
		return nil, nil
	}
	vs, _ := Collect(Capable(es, f, HasViolations), engine.Engine.Violations)
	counts, truncated := map[model.PolicyKey]int{}, map[model.PolicyKey]bool{}
	for _, v := range vs {
		if v.Truncated {
			truncated[v.Policy] = true
		}
		if f.MatchesTarget(v.Target) {
			counts[v.Policy]++
		}
	}
	return counts, truncated
}

// Violations returns the violations passing f, sorted by ViolationID.
func Violations(es []engine.Engine, f engine.Filter) ([]model.Violation, error) {
	vs, err := Collect(Capable(es, f, HasViolations), engine.Engine.Violations)
	sort.SliceStable(vs, func(i, j int) bool { return ViolationID(vs[i]) < ViolationID(vs[j]) })
	out := vs[:0]
	for _, v := range vs {
		if f.MatchesPolicy(v.Policy) && f.MatchesTarget(v.Target) {
			out = append(out, v)
		}
	}
	return out, err
}

// ViolationID identifies a violation for selection and sorting.
func ViolationID(v model.Violation) string {
	return v.Policy.String() + "|" + v.Target.String() + "|" + v.Message
}

// Mutations returns the mutations passing f's namespace scope, sorted by ID.
func Mutations(es []engine.Engine, f engine.Filter) ([]model.Mutation, error) {
	ms, err := Collect(Capable(es, f, HasMutations), engine.Engine.Mutations)
	out := ms[:0]
	for _, m := range ms {
		if f.MatchesNamespace(m.Namespace) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return MutationID(out[i]) < MutationID(out[j]) })
	return out, err
}

// MutationID identifies a mutation.
func MutationID(m model.Mutation) string {
	return m.Engine + ":" + m.Kind + "/" + m.Namespace + "/" + m.Name
}

// Health returns the health issues passing f's namespace scope, sorted by ID.
func Health(es []engine.Engine, f engine.Filter) ([]model.HealthIssue, error) {
	hs, err := Collect(Capable(es, f, HasHealth), engine.Engine.Health)
	out := hs[:0]
	for _, h := range hs {
		if f.MatchesNamespace(h.Object.Namespace) {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return HealthID(out[i]) < HealthID(out[j]) })
	return out, err
}

// HealthID identifies a health issue.
func HealthID(h model.HealthIssue) string {
	return h.Engine + "|" + h.Category + "|" + h.Object.String() + "|" + h.Message
}

// NamespaceCount is one namespace's violation count.
type NamespaceCount struct {
	Name       string
	Violations int
	// LowerBound is set when some engine capped its reported list.
	LowerBound bool
}

// Namespaces counts violations per namespace. The first entry is "all",
// using the engines' exact totals. names is the cluster's namespace list;
// when it could not be read (namesErr), namespaces seen in violations are
// used instead.
func Namespaces(es []engine.Engine, names []string, namesErr error) ([]NamespaceCount, error) {
	vs, err := Collect(Capable(es, engine.Filter{}, HasViolations), engine.Engine.Violations)
	counts := map[string]int{}
	lowerBound := false
	for _, v := range vs {
		lowerBound = lowerBound || v.Truncated
		if v.Target.Namespace != "" {
			counts[v.Target.Namespace]++
		} else if v.Target.Kind == "Namespace" {
			counts[v.Target.Name]++
		}
	}
	// No list (an error, or a snapshot taken before namespaces were
	// recorded: a cluster always has some): use the namespaces violations
	// name, so :ns still scopes.
	if namesErr != nil || len(names) == 0 {
		names = sortedKeys(counts)
	}
	out := []NamespaceCount{{Name: AllNamespaces, Violations: TotalViolations(es, len(vs))}}
	for _, n := range names {
		out = append(out, NamespaceCount{Name: n, Violations: counts[n], LowerBound: lowerBound})
	}
	return out, err
}

// TotalViolations sums the engines' own totals, which are exact even when
// reported lists are capped; reported is the floor.
func TotalViolations(es []engine.Engine, reported int) int {
	ps, _ := Collect(Capable(es, engine.Filter{}, HasPolicies), engine.Engine.Policies)
	total := 0
	for _, p := range ps {
		total += p.ViolationCount
	}
	return max(total, reported)
}

// LatestAudit is the most recent audit time across all policies.
func LatestAudit(es []engine.Engine) time.Time {
	ps, _ := Collect(Capable(es, engine.Filter{}, HasPolicies), engine.Engine.Policies)
	var latest time.Time
	for _, p := range ps {
		if p.LastAudit.After(latest) {
			latest = p.LastAudit
		}
	}
	return latest
}

// Synced reports whether every engine finished loading.
func Synced(es []engine.Engine) bool {
	for _, e := range es {
		if !e.SyncStatus().Synced {
			return false
		}
	}
	return true
}

// maxNamedResources is how many resources a grouped access notice names
// before it says "+N more".
const maxNamedResources = 2

// AccessNotices describes the engines' RBAC denials for a view: one line per
// engine and kind of gap. A tenant can be denied dozens of Gatekeeper
// constraint kinds; one line per resource would push the table they can see
// off the screen. AccessNoticesDetailed lists every resource.
func AccessNotices(es []engine.Engine) []string {
	type group struct {
		engine, verb, namespace string
		resources               []string
	}
	var groups []*group
	index := map[[3]string]*group{}
	for _, e := range es {
		for _, ai := range e.AccessIssues() {
			key := [3]string{ai.Engine, ai.Verb, ai.OnlyNamespace}
			g := index[key]
			if g == nil {
				g = &group{engine: ai.Engine, verb: ai.Verb, namespace: ai.OnlyNamespace}
				index[key] = g
				groups = append(groups, g)
			}
			g.resources = append(g.resources, ai.Resource)
		}
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		names := nameSome(g.resources)
		switch {
		case g.namespace != "":
			out = append(out, fmt.Sprintf("%s: no cluster-wide access to %s — showing namespace %s only", g.engine, names, g.namespace))
		case len(g.resources) == 1:
			out = append(out, fmt.Sprintf("%s: forbidden to %s %s — data from it is missing", g.engine, g.verb, names))
		default:
			out = append(out, fmt.Sprintf("%s: forbidden to %s %d resources (%s) — data from them is missing", g.engine, g.verb, len(g.resources), names))
		}
	}
	return out
}

// nameSome joins up to maxNamedResources names and counts the rest.
func nameSome(rs []string) string {
	if len(rs) <= maxNamedResources+1 {
		return strings.Join(rs, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(rs[:maxNamedResources], ", "), len(rs)-maxNamedResources)
}

// AccessNoticesDetailed describes every RBAC denial on its own line, for a
// report that someone fixing the access will work through.
func AccessNoticesDetailed(es []engine.Engine) []string {
	var out []string
	for _, e := range es {
		for _, ai := range e.AccessIssues() {
			if ai.OnlyNamespace != "" {
				out = append(out, fmt.Sprintf("%s: no cluster-wide access to %s — showing namespace %s only", ai.Engine, ai.Resource, ai.OnlyNamespace))
				continue
			}
			out = append(out, fmt.Sprintf("%s: forbidden to %s %s — data from it is missing", ai.Engine, ai.Verb, ai.Resource))
		}
	}
	return out
}

// IsActive reports whether an engine with that name is active.
func IsActive(es []engine.Engine, name string) bool {
	for _, e := range es {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// EmptyInput is what EmptyReason needs to explain an empty aggregate view.
type EmptyInput struct {
	ConnErr     error
	Engines     []engine.Engine
	Detections  []engine.Detection
	ContextName string
	Title       string
	Filter      engine.Filter
	Has         Capability
}

// EmptyReason explains an empty aggregate view in order of likelihood: not
// connected, no engine at all, no engine with the capability, no data.
func EmptyReason(in EmptyInput) string {
	title := strings.ToLower(in.Title)
	switch {
	case in.ConnErr != nil:
		return "Not connected: " + in.ConnErr.Error() + ". Use :ctx to pick another context."
	case len(in.Engines) == 0:
		return NoEngineText(in.Detections, in.ContextName)
	case len(Capable(in.Engines, in.Filter, in.Has)) == 0:
		return fmt.Sprintf("No active engine supports %s.", title)
	case !in.Filter.IsZero():
		return fmt.Sprintf("No %s for this selection.", title)
	default:
		return fmt.Sprintf("No %s in this context.", title)
	}
}

// NoEngineText explains a context without any supported engine.
func NoEngineText(dets []engine.Detection, contextName string) string {
	var names []string
	for _, d := range dets {
		names = append(names, d.Engine.Name())
	}
	sort.Strings(names)
	msg := fmt.Sprintf("No supported policy engine detected in context %q.", contextName)
	if len(names) > 0 {
		msg += " Looked for: " + strings.Join(names, ", ") + "."
	}
	return msg + " Use :ctx to switch context."
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ShortKind returns the engine's table alias for a policy kind, or kind.
func ShortKind(es []engine.Engine, engineName, kind string) string {
	for _, e := range es {
		if e.Name() != engineName {
			continue
		}
		if n, ok := e.(engine.KindNamer); ok {
			if s := n.ShortKind(kind); s != "" {
				return s
			}
		}
	}
	return kind
}

// Staleness thresholds, as multiples of an engine's audit interval.
const (
	StaleWarnRatio  = 2.0
	StaleErrorRatio = 5.0
)

// Audit is the freshness of one engine's audit results.
type Audit struct {
	Engine string
	// Last is the newest audit; zero when Never.
	Last time.Time
	// Interval is how often the engine audits; zero when unknown or disabled,
	// in which case the audit is never graded stale.
	Interval time.Duration
	// Ratio is age / interval (0 without an interval; +Inf when Never).
	Ratio float64
	// Never is set when the engine has policies it should audit but no
	// audit result at all, e.g. its audit pod is down.
	Never bool
}

// Audits computes every engine's audit freshness in one pass over its
// policies. Engines with nothing to audit (no validating policies) are left
// out, as are never-audited engines that do not audit periodically.
func Audits(es []engine.Engine, now time.Time) []Audit {
	var out []Audit
	for _, e := range Capable(es, engine.Filter{}, HasPolicies) {
		ps, err := e.Policies()
		if err != nil {
			continue
		}
		a := Audit{Engine: e.Name()}
		auditable := false
		for _, p := range ps {
			if p.LastAudit.After(a.Last) {
				a.Last = p.LastAudit
			}
			auditable = auditable || (p.Enforcement.Mode != model.ModeMutate && p.Enforcement.Mode != model.ModeUnknown)
		}
		if au, ok := e.(engine.Auditor); ok {
			a.Interval = au.AuditInterval()
		}
		switch {
		case !a.Last.IsZero() && a.Interval > 0:
			a.Ratio = float64(now.Sub(a.Last)) / float64(a.Interval)
		case a.Last.IsZero() && auditable && a.Interval > 0:
			a.Never, a.Ratio = true, math.Inf(1)
		case a.Last.IsZero():
			continue
		}
		out = append(out, a)
	}
	return out
}

// Stalest picks the audit that is oldest relative to its own interval, so a
// fresh engine never hides a stale (or never-run) one. Ungraded audits
// (no interval) rank below graded ones and among themselves by age.
func Stalest(es []engine.Engine, now time.Time) (Audit, bool) {
	audits := Audits(es, now)
	if len(audits) == 0 {
		return Audit{}, false
	}
	best := audits[0]
	for _, a := range audits[1:] {
		if a.Ratio > best.Ratio || (a.Ratio == best.Ratio && a.Last.Before(best.Last)) {
			best = a
		}
	}
	return best, true
}

// Newest is the most recent audit time among audits.
func Newest(audits []Audit) time.Time {
	var t time.Time
	for _, a := range audits {
		if a.Last.After(t) {
			t = a.Last
		}
	}
	return t
}

// HealthSummary counts health issues by severity. OK counts engines that
// answered, are synced and have no error or warning; Unknown counts engines
// whose health could not be read or is still loading (never "ok").
type HealthSummary struct {
	Errors, Warnings, Infos, OK, Unknown int
}

// SummariseHealth computes the overview's health block.
func SummariseHealth(es []engine.Engine, f engine.Filter) (HealthSummary, error) {
	var s HealthSummary
	var errs []error
	for _, e := range Capable(es, f, HasHealth) {
		hs, err := e.Health()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			s.Unknown++
			continue
		}
		bad := false
		for _, h := range hs {
			if !f.MatchesNamespace(h.Object.Namespace) {
				continue
			}
			switch h.Severity {
			case model.SeverityError:
				s.Errors++
				bad = true
			case model.SeverityWarning:
				s.Warnings++
				bad = true
			default:
				s.Infos++
			}
		}
		switch {
		case !e.SyncStatus().Synced:
			s.Unknown++
		case !bad:
			s.OK++
		}
	}
	return s, errors.Join(errs...)
}

// TopViolators returns the policies with known, non-zero violation counts,
// most first (ties by key). Under a namespace scope the scoped counts are
// used. Callers show the first few and report len() as the total.
func TopViolators(es []engine.Engine, f engine.Filter) ([]PolicyEntry, error) {
	ps, err := Policies(es, f)
	out := make([]PolicyEntry, 0, len(ps))
	for _, p := range ps {
		if p.ViolationsKnown && p.ViolationCount > 0 {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ViolationCount > out[j].ViolationCount })
	return out, err
}

// EnforcementCandidates returns policies in audit or warn mode, fewest
// violations first: a candidate with none can move to enforce without
// blocking anything that exists today. Unknown counts sort last.
func EnforcementCandidates(es []engine.Engine, f engine.Filter) ([]PolicyEntry, error) {
	ps, err := Policies(es, f)
	out := make([]PolicyEntry, 0, len(ps))
	for _, p := range ps {
		if m := p.Enforcement.Mode; m == model.ModeAudit || m == model.ModeWarn {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ViolationsKnown != b.ViolationsKnown {
			return a.ViolationsKnown
		}
		return a.ViolationCount < b.ViolationCount
	})
	return out, err
}

// PolicyIndex maps every readable policy of the engines by key, for
// details that show a violation's policy. Engine errors are left to the
// views' own notices; a policy missing here is shown as unreadable.
func PolicyIndex(es []engine.Engine) map[model.PolicyKey]model.Policy {
	ps, _ := Collect(Capable(es, engine.Filter{}, HasPolicies), engine.Engine.Policies)
	out := make(map[model.PolicyKey]model.Policy, len(ps))
	for _, p := range ps {
		out[p.Key()] = p
	}
	return out
}
