// Package vap is the engine for native ValidatingAdmissionPolicies
// (admissionregistration.k8s.io/v1): policies, their bindings, and
// PolicyReport results Kyverno can write for them. Policies that Gatekeeper
// or Kyverno generate from their own policies are recognised and kept out
// of this engine's counts; the source policy shows them instead.
package vap

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Name and Prefix identify the engine.
const (
	Name   = "vap"
	Prefix = "vap"
)

const (
	group           = "admissionregistration.k8s.io"
	reportsGroup    = "wgpolicyk8s.io"
	kindPolicy      = "ValidatingAdmissionPolicy"
	resourcePolicy  = "validatingadmissionpolicies"
	resourceBinding = "validatingadmissionpolicybindings"
	// builtinVersion: VAP is part of the API server, not an installed product.
	builtinVersion = "builtin"
)

func init() { engine.Register(Name, New) }

// New returns the engine for a cluster.
func New(c engine.Cluster) engine.Engine {
	a := enginekit.NewAccess(Name)
	a.SetHomeNamespace(engine.HomeNamespace(c))
	e := &Engine{cluster: c, access: a, sync: enginekit.NewSync(), params: newParamChecker(c)}
	return e
}

// Engine implements engine.Engine for ValidatingAdmissionPolicies.
type Engine struct {
	cluster engine.Cluster
	access  *enginekit.Access
	sync    *enginekit.Sync
	params  *paramChecker
	// showGenerated is the G toggle of :vap/pol.
	showGenerated atomic.Bool
	version       string // the served API version, e.g. "v1"

	mu        sync.RWMutex
	cancel    context.CancelFunc
	policies  cache.SharedIndexInformer
	bindings  cache.SharedIndexInformer
	reports   []cache.SharedIndexInformer
	gen       atomic.Uint64
	snapMu    sync.Mutex
	snapGen   uint64
	snapCache *state
}

var (
	_ engine.Engine    = (*Engine)(nil)
	_ engine.KindNamer = (*Engine)(nil)
	_ engine.Generator = (*Engine)(nil)
)

// Name implements engine.Engine.
func (e *Engine) Name() string { return Name }

// Prefix implements engine.Engine.
func (e *Engine) Prefix() string { return Prefix }

// Capabilities implements engine.Engine.
func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{Policies: true, Violations: true, Health: true}
}

// ShortKind implements engine.KindNamer.
func (e *Engine) ShortKind(kind string) string {
	if kind == kindPolicy {
		return "VAP"
	}
	return kind
}

// Detect implements engine.Engine: the API group serves the policy kind.
func (e *Engine) Detect(context.Context) (bool, string, error) {
	rs, err := enginekit.GroupResources(e.cluster.Discovery(), group)
	if err != nil {
		return false, "", fmt.Errorf("discover %s: %w", group, err)
	}
	r, ok := rs[resourcePolicy]
	if !ok {
		return false, "", nil
	}
	e.version = r.GVR.Version
	return true, builtinVersion, nil
}

func (e *Engine) gvr(resource string) schema.GroupVersionResource {
	v := e.version
	if v == "" {
		v = "v1"
	}
	return schema.GroupVersionResource{Group: group, Version: v, Resource: resource}
}

// Start implements engine.Engine.
func (e *Engine) Start(ctx context.Context, f dynamicinformer.DynamicSharedInformerFactory, notify engine.Notify) error {
	if notify == nil {
		notify = func() {}
	}
	upstream := notify
	notify = func() { e.gen.Add(1); upstream() }
	ctx, cancel := context.WithCancel(ctx)
	registered := e.sync.Start()
	e.sync.SkipWhen(e.access.Denied)
	dyn := e.cluster.Dynamic()
	pol := e.access.SharedInformer(ctx, dyn, f, e.gvr(resourcePolicy), notify, notify)
	bnd := e.access.SharedInformer(ctx, dyn, f, e.gvr(resourceBinding), notify, notify)
	for _, inf := range []struct {
		i   cache.SharedIndexInformer
		res string
	}{{pol, resourcePolicy}, {bnd, resourceBinding}} {
		if inf.i != nil {
			e.sync.Track(e.gvr(inf.res), inf.i)
		}
	}
	// PolicyReports carry results for VAPs when Kyverno writes them; the
	// shared factory dedupes the informer with Kyverno's own.
	var reports []cache.SharedIndexInformer
	if rs, err := enginekit.GroupResources(e.cluster.Discovery(), reportsGroup); err == nil {
		for _, name := range []string{"policyreports", "clusterpolicyreports"} {
			if r, ok := rs[name]; ok {
				if inf := e.access.SharedInformer(ctx, dyn, f, r.GVR, notify, notify); inf != nil {
					e.sync.Track(r.GVR, inf)
					reports = append(reports, inf)
				}
			}
		}
	}
	// Health is final only once every binding's param has been looked up.
	paramsChecked := e.sync.Gate("param checks")
	registered()
	e.sync.Watch(ctx, notify)
	e.mu.Lock()
	e.cancel, e.policies, e.bindings, e.reports = cancel, pol, bnd, reports
	e.mu.Unlock()
	e.params.start(ctx, notify)
	e.gen.Add(1)
	go e.firstParamRound(ctx, paramsChecked, pol, bnd)
	return nil
}

// firstParamRound opens the param gate after the initial lists landed and
// the params they reference were checked once.
func (e *Engine) firstParamRound(ctx context.Context, open func(), infs ...cache.SharedIndexInformer) {
	var synced []cache.InformerSynced
	for _, i := range infs {
		if i != nil {
			synced = append(synced, i.HasSynced)
		}
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return
	}
	e.snapshot() // queues the refs
	e.params.waitIdle(ctx)
	open()
}

// Stop implements engine.Engine.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.cancel
	e.cancel, e.policies, e.bindings, e.reports = nil, nil, nil, nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.gen.Add(1)
	e.sync.Reset()
}

// SyncStatus implements engine.Engine.
func (e *Engine) SyncStatus() engine.SyncStatus { return e.sync.Status() }

// AccessIssues implements engine.Engine.
func (e *Engine) AccessIssues() []model.AccessIssue { return e.access.Issues() }

// Policies implements engine.Engine. Generated policies belong to the
// engine that generated them and are not listed.
func (e *Engine) Policies() ([]model.Policy, error) {
	s := e.snapshot()
	out := make([]model.Policy, 0, len(s.policies))
	for _, p := range s.policies {
		if p.generatedBy == "" {
			out = append(out, p.Policy)
		}
	}
	return out, nil
}

// Violations implements engine.Engine: PolicyReport results with a VAP
// source, for non-generated policies.
func (e *Engine) Violations() ([]model.Violation, error) {
	return append([]model.Violation(nil), e.snapshot().violations...), nil
}

// Mutations implements engine.Engine.
func (e *Engine) Mutations() ([]model.Mutation, error) { return nil, engine.ErrNotSupported }

// Health implements engine.Engine.
func (e *Engine) Health() ([]model.HealthIssue, error) {
	return append([]model.HealthIssue(nil), e.snapshot().health...), nil
}

// Evaluator implements engine.Engine.
func (e *Engine) Evaluator() (engine.Evaluator, error) { return nil, engine.ErrNotSupported }

// Actions implements engine.Engine.
func (e *Engine) Actions() (engine.Actions, error) { return nil, engine.ErrNotSupported }

// GeneratedFrom implements engine.Generator.
func (e *Engine) GeneratedFrom(source model.PolicyKey) []string {
	return e.snapshot().generatedFrom[source]
}

// snapshot is the derived state, rebuilt after any change.
func (e *Engine) snapshot() *state {
	e.snapMu.Lock()
	defer e.snapMu.Unlock()
	g := e.gen.Load() + e.params.generation()
	if e.snapCache != nil && e.snapGen == g {
		return e.snapCache
	}
	e.mu.RLock()
	pol, bnd, reps := e.policies, e.bindings, e.reports
	e.mu.RUnlock()
	in := inputs{params: e.params}
	if pol != nil {
		in.policies = enginekit.Objects(pol)
	}
	if bnd != nil {
		in.bindings = enginekit.Objects(bnd)
	}
	for _, r := range reps {
		in.reports = append(in.reports, enginekit.Objects(r)...)
	}
	s := build(in)
	e.params.want(s.paramRefs)
	e.snapCache, e.snapGen = s, g
	return s
}

var _ engine.PolicyStatuser = (*Engine)(nil)

// PolicyStatus implements engine.PolicyStatuser for :health: unbound
// policies are not enforced; type-checking warnings count as compile
// errors (the API server accepted the policy, but an expression may never
// evaluate as written). Generated policies are their engine's.
func (e *Engine) PolicyStatus() engine.PolicyStatus {
	s := e.snapshot()
	var st engine.PolicyStatus
	for _, p := range s.policies {
		if p.generatedBy != "" {
			continue
		}
		st.Total++
		unbound := len(p.bindings) == 0
		warned := len(enginekit.Slice(p.Raw.Object, "status", "typeChecking", "expressionWarnings")) > 0
		if unbound {
			st.NotEnforced++
		}
		if warned {
			st.CompileErrors++
		}
		if !unbound && !warned {
			st.Loaded++
		}
	}
	return st
}
