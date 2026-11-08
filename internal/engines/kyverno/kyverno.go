// Package kyverno implements engine.Engine for Kyverno: classic kyverno.io
// policies, the CEL policies.kyverno.io family, and PolicyReports.
package kyverno

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

const (
	// Name is the engine identifier.
	Name = "kyverno"
	// Prefix namespaces this engine's commands (":kyv/...").
	Prefix = "kyv"

	groupClassic   = "kyverno.io"
	groupCEL       = "policies.kyverno.io"
	groupWGReports = "wgpolicyk8s.io" //nolint:gosec // G101 false positive: an API group, not a credential
	groupOpenRep   = "openreports.io"
)

// Resource roles. Policy resources of policies.kyverno.io are discovered by
// name (every "*policies" resource), so new CEL kinds need no code change.
type role int

const (
	rolePolicy role = iota
	roleException
	roleReport
)

var classicRoles = map[string]role{
	"clusterpolicies": rolePolicy, "policies": rolePolicy, "policyexceptions": roleException,
}

var reportResources = map[string]map[string]bool{
	groupWGReports: {"policyreports": true, "clusterpolicyreports": true},
	groupOpenRep:   {"reports": true, "clusterreports": true},
}

func init() { engine.Register(Name, New) }

// New builds a Kyverno engine bound to a cluster.
func New(c engine.Cluster) engine.Engine {
	return &Engine{cluster: c, access: newAccess(c), sync: enginekit.NewSync()}
}

// Engine is the Kyverno implementation of engine.Engine.
type Engine struct {
	cluster engine.Cluster
	access  *enginekit.Access
	sync    *enginekit.Sync
	// scanInterval (nanoseconds) is read from the reports controller at Detect.
	scanInterval atomic.Int64
	// namespace is where the Kyverno deployments were found at Detect.
	namespace atomic.Value
	// omitViolations is set when --omitEvents drops PolicyViolation events.
	omitViolations atomic.Bool

	mu        sync.RWMutex
	cancel    context.CancelFunc
	informers []watched
	// policyRes are the discovered policy kinds, readable or not: results
	// of a kind RBAC hides are kept with an inferred key.
	policyRes []enginekit.Resource

	gen      atomic.Uint64
	snapMu   sync.Mutex
	snapGen  uint64
	snapData *snapshot
}

type watched struct {
	res      enginekit.Resource
	role     role
	informer cache.SharedIndexInformer
}

var (
	_ engine.Engine         = (*Engine)(nil)
	_ engine.KindNamer      = (*Engine)(nil)
	_ engine.Auditor        = (*Engine)(nil)
	_ engine.Platform       = (*Engine)(nil)
	_ engine.PolicyStatuser = (*Engine)(nil)
	_ engine.EventSource    = (*Engine)(nil)
)

// reportsLease is the Lease the reports controller (Kyverno's audit) holds
// for leader election (verified on v1.19).
const reportsLease = "kyverno-reports-controller"

// PlatformInfo implements engine.Platform.
func (e *Engine) PlatformInfo() engine.PlatformInfo {
	ns, _ := e.namespace.Load().(string)
	if ns == "" {
		ns = defaultNamespace
	}
	return engine.PlatformInfo{
		Namespace:       ns,
		WebhookSelector: map[string]string{"webhook.kyverno.io/managed-by": "kyverno"},
		AuditLease:      reportsLease,
		AuditSource:     "policy reports",
	}
}

// PolicyStatus implements engine.PolicyStatuser: a policy with a readiness
// error (e.g. a CEL compile error) is not ready.
func (e *Engine) PolicyStatus() engine.PolicyStatus {
	s := e.snapshot()
	st := engine.PolicyStatus{Total: len(s.policies)}
	for _, p := range s.policies {
		ready := true
		for _, h := range readiness(p.Raw) {
			if h.Severity == model.SeverityError {
				ready = false
			}
		}
		if ready {
			st.Loaded++
		} else {
			st.NotReady++
		}
	}
	return st
}

// Name implements engine.Engine.
func (e *Engine) Name() string { return Name }

// Prefix implements engine.Engine.
func (e *Engine) Prefix() string { return Prefix }

// Capabilities implements engine.Engine. Kyverno mutates natively, so the
// mutations view is backed from day one.
func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{Policies: true, Violations: true, Mutations: true, Health: true}
}

// Start implements engine.Engine. Kyverno's CRDs ship with Kyverno, so the
// resource set is fixed for the life of a context.
func (e *Engine) Start(ctx context.Context, f dynamicinformer.DynamicSharedInformerFactory, notify engine.Notify) error {
	if notify == nil {
		notify = func() {}
	}
	upstream := notify
	notify = func() { e.gen.Add(1); upstream() }

	resources, err := e.discover()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	registered := e.sync.Start()
	e.sync.SkipWhen(e.access.Denied)
	var ws []watched
	var policyRes []enginekit.Resource
	for _, w := range resources {
		if w.role == rolePolicy {
			policyRes = append(policyRes, w.res)
		}
		inf := e.access.SharedInformer(ctx, e.cluster.Dynamic(), f, w.res.GVR, notify, notify)
		if inf != nil {
			w.informer = inf
			ws = append(ws, w)
			e.sync.Track(w.res.GVR, inf)
		}
	}
	registered()
	e.sync.Watch(ctx, notify)
	e.mu.Lock()
	e.cancel, e.informers, e.policyRes = cancel, ws, policyRes
	e.mu.Unlock()
	e.gen.Add(1)
	return nil
}

// discover finds every resource the engine reads, in a stable order.
func (e *Engine) discover() ([]watched, error) {
	var out []watched
	add := func(group string, pick func(name string) (role, bool)) error {
		rs, err := enginekit.GroupResources(e.cluster.Discovery(), group)
		if err != nil {
			return fmt.Errorf("discover %s: %w", group, err)
		}
		names := make([]string, 0, len(rs))
		for n := range rs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if r, ok := pick(n); ok {
				out = append(out, watched{res: rs[n], role: r})
			}
		}
		return nil
	}
	steps := []struct {
		group string
		pick  func(string) (role, bool)
	}{
		{groupClassic, func(n string) (role, bool) { r, ok := classicRoles[n]; return r, ok }},
		{groupCEL, func(n string) (role, bool) {
			switch {
			case n == "policyexceptions":
				return roleException, true
			case strings.HasSuffix(n, "policies"):
				return rolePolicy, true
			}
			return 0, false
		}},
		{groupWGReports, func(n string) (role, bool) { return roleReport, reportResources[groupWGReports][n] }},
		{groupOpenRep, func(n string) (role, bool) { return roleReport, reportResources[groupOpenRep][n] }},
	}
	for _, s := range steps {
		if err := add(s.group, s.pick); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Stop implements engine.Engine. The shared factory owns the informers; the
// session shuts it down after Stop.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.informers = nil
	e.gen.Add(1)
	e.sync.Reset()
}

// SyncStatus implements engine.Engine.
func (e *Engine) SyncStatus() engine.SyncStatus { return e.sync.Status() }

// AccessIssues implements engine.Engine.
func (e *Engine) AccessIssues() []model.AccessIssue { return e.access.Issues() }

// Evaluator implements engine.Engine. Local evaluation lands in phase 4.
func (e *Engine) Evaluator() (engine.Evaluator, error) { return nil, engine.ErrNotSupported }

// Actions implements engine.Engine. Write actions land in phase 5.
func (e *Engine) Actions() (engine.Actions, error) { return nil, engine.ErrNotSupported }

// objects returns every cached object of a role, with kind and API version
// filled in from discovery (list items can omit them).
func (e *Engine) objects(r role) []*unstructured.Unstructured {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []*unstructured.Unstructured
	for _, w := range e.informers {
		if w.role != r {
			continue
		}
		for _, o := range enginekit.Objects(w.informer) {
			if o.GetKind() == "" {
				o.SetKind(w.res.Kind)
			}
			if o.GetAPIVersion() == "" {
				o.SetAPIVersion(w.res.GVR.GroupVersion().String())
			}
			out = append(out, o)
		}
	}
	enginekit.SortObjects(out)
	return out
}

// hiddenPolicyKinds are the policy kinds RBAC hides, fully or outside
// the home namespace.
func (e *Engine) hiddenPolicyKinds() hiddenKinds {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := hiddenKinds{}
	for _, r := range e.policyRes {
		gr := r.GVR.GroupResource().String()
		switch {
		case e.access.Denied(gr):
			out[r.Kind] = ""
		case e.access.Scoped(gr):
			out[r.Kind] = e.access.HomeNamespace()
		}
	}
	return out
}

// snapshot returns the cached derived data, rebuilt after any change. It is
// shared until the next change and must be treated as read-only.
func (e *Engine) snapshot() *snapshot {
	e.snapMu.Lock()
	defer e.snapMu.Unlock()
	gen := e.gen.Load()
	if e.snapData != nil && e.snapGen == gen {
		return e.snapData
	}
	s := build(e.objects(rolePolicy), e.objects(roleException), e.objects(roleReport), e.hiddenPolicyKinds())
	e.snapData, e.snapGen = s, gen
	return s
}

// newAccess tracks RBAC denials, falling back to the context's namespace
// for namespaced resources a tenant may read only there.
func newAccess(c engine.Cluster) *enginekit.Access {
	a := enginekit.NewAccess(Name)
	a.SetHomeNamespace(engine.HomeNamespace(c))
	return a
}
