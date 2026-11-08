// Package views defines the shared (cross-engine) views as engine.View values
// built on package query, resolves ":" commands to views, and summarises
// the header. Every renderer uses it; it must not import anything UI- or
// terminal-specific.
package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/table"
)

// Shared commands.
const (
	CmdPolicies   = "pol"
	CmdViolations = "v"
	CmdMutations  = "mut"
	CmdHealth     = "health"
	CmdContexts   = "ctx"
	CmdNamespaces = "ns"
)

// AllNamespaces is the namespace entry that clears the scope.
const AllNamespaces = query.AllNamespaces

// aliases maps every accepted spelling to a shared command.
var aliases = map[string]string{
	"pol": CmdPolicies, "policy": CmdPolicies, "policies": CmdPolicies,
	"v": CmdViolations, "violation": CmdViolations, "violations": CmdViolations,
	"mut": CmdMutations, "mutation": CmdMutations, "mutations": CmdMutations,
	"health":   CmdHealth,
	"overview": CmdOverview, "ov": CmdOverview,
	"ctx": CmdContexts, "context": CmdContexts, "contexts": CmdContexts,
	"events": CmdViolationsRecent, "recent": CmdViolationsRecent,
	"ns": CmdNamespaces, "namespace": CmdNamespaces, "namespaces": CmdNamespaces,
}

// Shared returns the canonical shared command for an alias.
func Shared(name string) (string, bool) {
	c, ok := aliases[name]
	return c, ok
}

// Env is everything views read. Renderers provide it; functions are called
// on every refresh so they always see current state.
type Env struct {
	Engines     func() []engine.Engine
	Detections  func() []engine.Detection
	ContextName func() string
	Contexts    func() ([]model.KubeContext, error)
	// ConnErr is the error of a failed initial connect, or nil.
	ConnErr func() error
	// Namespaces returns the cached namespace list (or the error that
	// prevented reading it).
	Namespaces func() ([]string, error)
	// Scope is the selected namespace ("" for all).
	Scope func() string
	Now   func() time.Time
	// Owners resolves violation owners; nil or returning nil disables
	// grouping by owner (targets stand for themselves).
	Owners func() Owners
	// GroupLabel is the --group-label key ("" when unset).
	GroupLabel func() string
	// Platform feeds the health dashboard; nil disables it.
	Platform func() Platform
	// Events feeds the recent tab of :v; nil disables it.
	Events func() Events
	// UpdatedAt is when data last changed ("refreshed Xs ago").
	UpdatedAt func() time.Time
}

func (e Env) events() Events {
	if e.Events == nil {
		return nil
	}
	return e.Events()
}

func (e Env) platform() Platform {
	if e.Platform == nil {
		return nil
	}
	return e.Platform()
}

func (e Env) owners() Owners {
	if e.Owners == nil {
		return nil
	}
	return e.Owners()
}

// lookup returns the owners as a query.OwnerLookup, nil-safe.
func (e Env) lookup() query.OwnerLookup {
	if o := e.owners(); o != nil {
		return o
	}
	return nil
}

func (e Env) groupLabel() string {
	if e.GroupLabel == nil {
		return ""
	}
	return e.GroupLabel()
}

// eventsSynced is true when the events watcher is synced or absent.
func (e Env) eventsSynced() bool {
	ev := e.events()
	return ev == nil || ev.SyncStatus().Synced
}

// platformSynced is true when the platform watcher is synced or absent.
func (e Env) platformSynced() bool {
	p := e.platform()
	return p == nil || p.SyncStatus().Synced
}

// ownersSynced is true when owners are synced or not resolved at all.
func (e Env) ownersSynced() bool {
	o := e.owners()
	return o == nil || o.SyncStatus().Synced
}

func (e Env) connErr() error {
	if e.ConnErr == nil {
		return nil
	}
	return e.ConnErr()
}

// Spec is a view ready to materialise, plus how renderers treat it.
type Spec struct {
	table.Source
	// Scoped specs follow the namespace scope.
	Scoped bool
	// Engine is set for engine-specific views.
	Engine engine.Engine
	// grouping applies to the violations views.
	grouping Grouping
}

// Command is the spec's ":" command.
func (s Spec) Command() string { return s.View.Command }

// Shared builds the spec for a canonical shared command.
func SharedSpec(env Env, cmd string) Spec {
	var v engine.View
	var has query.Capability
	switch cmd {
	case CmdViolations:
		return ViolationsSpec(env, GroupByOwner)
	case CmdViolationsFlat:
		return ViolationsSpec(env, Flat)
	case CmdViolationsRecent:
		return RecentSpec(env)
	case CmdMutations:
		v, has = mutationsView(env), query.HasMutations
	case CmdHealth:
		v, has = healthView(env), query.HasHealth
	case CmdContexts:
		return Spec{Source: table.Source{View: contextsView(env),
			Empty: func(engine.Filter) string { return "No contexts in kubeconfig." }}}
	case CmdNamespaces:
		return Spec{Source: table.Source{View: namespacesView(env)}}
	default:
		v, has = policiesView(env), query.HasPolicies
	}
	return Spec{Scoped: true, Source: table.Source{
		View: v,
		Notices: func(f engine.Filter) []string {
			return query.AccessNotices(query.Capable(env.Engines(), f, has))
		},
		Empty: func(f engine.Filter) string {
			return query.EmptyReason(query.EmptyInput{
				ConnErr: env.connErr(), Engines: env.Engines(), Detections: env.Detections(),
				ContextName: env.ContextName(), Title: v.Title, Filter: f, Has: has,
			})
		},
		Synced: func(f engine.Filter) bool { return query.Synced(query.Capable(env.Engines(), f, has)) },
	}}
}

// EngineSpec builds the spec for one of an engine's own views.
func EngineSpec(e engine.Engine, v engine.View) Spec {
	return Spec{Scoped: true, Engine: e, Source: table.Source{
		View:    v,
		Notices: func(engine.Filter) []string { return query.AccessNotices([]engine.Engine{e}) },
		Empty:   func(engine.Filter) string { return fmt.Sprintf("No %s found.", v.Title) },
		Synced:  func(engine.Filter) bool { return e.SyncStatus().Synced },
	}}
}

// EngineView pairs an engine with one of its views.
type EngineView struct {
	Engine engine.Engine
	View   engine.View
}

// EngineViews lists every view contributed by active engines.
func EngineViews(env Env) []EngineView {
	var out []EngineView
	for _, e := range env.Engines() {
		for _, v := range e.Views() {
			out = append(out, EngineView{Engine: e, View: v})
		}
	}
	return out
}

// Resolve maps a ":" command and its argument to a spec and filter. For
// shared views the argument names an engine; for engine views it seeds the
// text filter. The namespace scope is applied to scoped specs.
func Resolve(env Env, name, args string) (Spec, engine.Filter, error) {
	if name == CmdImpact {
		return ResolveImpact(env, args)
	}
	scope := scopeOf(env)
	if cmd, ok := aliases[name]; ok && cmd != CmdOverview && cmd != CmdHealth {
		f := engine.Filter{}
		if args != "" {
			if !query.IsActive(env.Engines(), args) {
				return Spec{}, f, fmt.Errorf("engine %q is not active in this context", args)
			}
			f.Engine = args
		}
		s := SharedSpec(env, cmd)
		if s.Scoped {
			f.Namespace = scope
		}
		return s, f, nil
	}
	for _, ev := range EngineViews(env) {
		if ev.View.Command == name || contains(ev.View.Aliases, name) {
			return EngineSpec(ev.Engine, ev.View), engine.Filter{Engine: ev.Engine.Name(), Text: args, Namespace: scope}, nil
		}
	}
	if cmd := aliases[name]; cmd == CmdOverview || cmd == CmdHealth {
		return Spec{}, engine.Filter{}, fmt.Errorf(":%s is a dashboard, not a table (use OverviewOf / DashboardOf)", cmd)
	}
	if prefix, _, ok := strings.Cut(name, "/"); ok {
		return Spec{}, engine.Filter{}, fmt.Errorf("no active engine provides :%s (prefix %q)", name, prefix)
	}
	return Spec{}, engine.Filter{}, fmt.Errorf("unknown command :%s (press ? for help)", name)
}

// Target resolves a drill-down target, applying the namespace scope.
func Target(env Env, t engine.Target) (Spec, engine.Filter, error) {
	f := t.Filter
	// An owner expansion names one object: the :ns scope would only hide
	// it (the impact view is cluster-wide; grouped :v rows are in scope).
	if f.Owner == "" {
		f.Namespace = scopeOf(env)
	}
	if t.Command == CmdViolationsFlat || t.Command == CmdViolationsRecent {
		return SharedSpec(env, t.Command), f, nil
	}
	if cmd, ok := aliases[t.Command]; ok {
		return SharedSpec(env, cmd), f, nil
	}
	for _, ev := range EngineViews(env) {
		if ev.View.Command == t.Command {
			return EngineSpec(ev.Engine, ev.View), f, nil
		}
	}
	return Spec{}, f, fmt.Errorf("no view for :%s", t.Command)
}

// CommandNames lists every command available now, shared ones first.
func CommandNames(env Env) []string {
	// Shared commands first: engine views are mostly reached by drilling down.
	names := []string{CmdOverview, CmdPolicies, CmdViolations, "events", CmdImpact, CmdMutations, CmdHealth, CmdNamespaces, CmdContexts}
	for _, ev := range EngineViews(env) {
		names = append(names, ev.View.Command)
	}
	return names
}

// Complete returns the first command starting with prefix, or prefix.
func Complete(env Env, prefix string) string {
	for _, n := range CommandNames(env) {
		if strings.HasPrefix(n, prefix) {
			return n
		}
	}
	return prefix
}

// HelpCommands lists the shared ":" commands with one-line descriptions.
func HelpCommands() [][2]string {
	return [][2]string{
		{":overview", "start view: health, top violators, enforcement candidates"},
		{":pol [engine]", "policies across engines"},
		{":v [engine]", "violations across engines ([ ] switch current / recent)"},
		{":events", "recent admission events (the recent tab of :v)"},
		{":impact <policy>", "what enforcing a policy would block (i on a policy row)"},
		{":mut [engine]", "mutations across engines"},
		{":health [engine]", "engine health issues"},
		{":ns [name|all]", "scope every view to a namespace"},
		{":ctx", "kubeconfig contexts"},
	}
}

// HelpEngineCommands lists the active engines' own views, which are mostly
// reached by drilling down from a shared view.
func HelpEngineCommands(env Env) [][2]string {
	var out [][2]string
	for _, ev := range EngineViews(env) {
		out = append(out, [2]string{":" + ev.View.Command, ev.View.Description})
	}
	return out
}

// HeaderEngine is one engine in the header.
type HeaderEngine struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
}

// Header is the summary line every renderer shows.
type Header struct {
	Context string         `json:"context"`
	Scope   string         `json:"scope"`
	Engines []HeaderEngine `json:"engines"`
	// LastAudit is the newest audit across all engines.
	LastAudit time.Time `json:"lastAudit,omitzero"`
	// Audit is the engine whose audit is stalest relative to its interval;
	// renderers show it so a fresh engine never hides a stale one.
	Audit     *query.Audit `json:"audit,omitempty"`
	Staleness Staleness    `json:"staleness"`
	Synced    bool         `json:"synced"`
}

// Staleness grades audit freshness against the engine's interval.
type Staleness string

// Staleness grades.
const (
	Fresh   Staleness = "fresh"   // under 2× the interval (or no interval known)
	Stale   Staleness = "stale"   // over 2×
	Overdue Staleness = "overdue" // over 5×
)

// HeaderOf summarises the current state.
func HeaderOf(env Env) Header {
	h := Header{Context: env.ContextName(), Scope: scopeOf(env)}
	for _, d := range env.Detections() {
		switch {
		case d.Err != nil:
			h.Engines = append(h.Engines, HeaderEngine{Name: d.Engine.Name(), Failed: true})
		case d.Installed:
			h.Engines = append(h.Engines, HeaderEngine{Name: d.Engine.Name(), Version: d.Version})
		}
	}
	es, now := env.Engines(), env.Now()
	audits := query.Audits(es, now)
	h.LastAudit = query.Newest(audits)
	h.Synced = query.Synced(es) && env.ownersSynced() && env.platformSynced() && env.eventsSynced()
	h.Staleness = Fresh
	if a, ok := query.Stalest(es, now); ok {
		h.Audit = &a
		switch {
		case a.Never || a.Ratio > query.StaleErrorRatio:
			h.Staleness = Overdue
		case a.Ratio > query.StaleWarnRatio:
			h.Staleness = Stale
		}
	}
	return h
}

// CopyRef is the "kind/namespace/name" form copied by "c".
func CopyRef(r model.ObjectRef) string {
	if r.Namespace == "" {
		return r.Kind + "/" + r.Name
	}
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// KubectlGet is the command copied by "C": it fetches the target as YAML
// from the same context. Group-qualified so CRD kinds resolve unambiguously.
func KubectlGet(r model.ObjectRef, contextName string) string {
	res := strings.ToLower(r.Kind)
	if r.Group != "" {
		res += "." + r.Group
	}
	cmd := "kubectl"
	if contextName != "" {
		// "=" form: a context name starting with "-" stays a value.
		cmd += " --context=" + shellQuote(contextName)
	}
	cmd += " get " + shellQuote(res+"/"+r.Name)
	if r.Namespace != "" {
		cmd += " --namespace=" + shellQuote(r.Namespace)
	}
	return cmd + " -o yaml"
}

// shellQuote quotes a value for a POSIX shell when it needs it, so a pasted
// command can never run something else.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, unsafeShellRune) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func scopeOf(env Env) string {
	if env.Scope == nil {
		return ""
	}
	return env.Scope()
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// FilterLabel describes a filter's policy selection, e.g.
// "(gatekeeper:K8sRequiredLabels:ns-must-have-owner)", or "" for none.
func FilterLabel(f engine.Filter) string {
	var parts []string
	for _, p := range []string{f.Engine, f.PolicyKind, f.PolicyName} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	scope := f.Owner
	if f.Group != "" {
		scope = f.Group
	}
	if len(parts) == 0 && scope == "" {
		return ""
	}
	label := strings.Join(parts, ":")
	if scope != "" {
		label = strings.TrimSpace(label + " on " + scope)
	}
	return "(" + label + ")"
}

// JumpError explains a failed fetch of a row's target, mapping RBAC
// denials to a plain message.
func JumpError(ref model.ObjectRef, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "forbidden") || strings.Contains(msg, "Unauthorized") {
		return fmt.Errorf("RBAC: not allowed to get %s", ref)
	}
	return err
}

func unsafeShellRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	default:
		return !strings.ContainsRune("-._:/@", r)
	}
}

// Events are the engines' recent admission events.
type Events interface {
	Events() []model.AdmissionEvent
	Statuses() []EventStatus
	AccessIssues() []model.AccessIssue
	SyncStatus() engine.SyncStatus
}

// EventStatus says whether an engine records admission events.
type EventStatus struct {
	Engine  string
	Enabled bool
	Hint    string
}

// Platform is the API-side state of the engines' moving parts.
type Platform interface {
	Snapshot() query.Platform
	AccessIssues() []model.AccessIssue
	SyncStatus() engine.SyncStatus
}

// Owners resolves violation targets to top-level owners for grouping.
type Owners interface {
	query.OwnerLookup
	AccessIssues() []model.AccessIssue
	SyncStatus() engine.SyncStatus
}
