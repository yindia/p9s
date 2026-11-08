// Package gatekeeper implements engine.Engine for OPA Gatekeeper.
package gatekeeper

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

const (
	// Name is the engine identifier.
	Name = "gatekeeper"
	// Prefix namespaces this engine's commands (":gk/...").
	Prefix = "gk"

	groupTemplates   = "templates.gatekeeper.sh"
	groupConstraints = "constraints.gatekeeper.sh"
	groupConfig      = "config.gatekeeper.sh"

	resourceTemplates = "constrainttemplates"
	resourceConfigs   = "configs"
)

func init() { engine.Register(Name, New) }

// New builds a Gatekeeper engine bound to a cluster.
func New(c engine.Cluster) engine.Engine {
	return &Engine{cluster: c, constraints: map[string]*kindInformer{}, access: newAccess(c), sync: enginekit.NewSync()}
}

// Engine is the Gatekeeper implementation of engine.Engine.
type Engine struct {
	cluster engine.Cluster
	access  *enginekit.Access
	sync    *enginekit.Sync
	// openDiscovery closes the "first constraint-kind discovery" sync gate.
	openDiscovery func()
	// discoveryErr is the last constraint-kind discovery failure, if any.
	discoveryErr atomic.Pointer[error]
	// auditInterval (nanoseconds) is read from the audit deployment at Detect.
	auditInterval atomic.Int64
	intervalRead  atomic.Bool
	// namespace is where the Gatekeeper deployments were found at Detect.
	namespace atomic.Value
	// events records --emit-admission-events and
	// --admission-events-involved-namespace (read at Detect).
	eventsKnown, eventsOn, eventsInvolvedNS atomic.Bool

	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	notify      engine.Notify
	templates   cache.SharedIndexInformer
	configs     cache.SharedIndexInformer
	constraints map[string]*kindInformer // keyed by resource name
	versions    map[string]string        // group -> preferred version
	kick        chan struct{}
	wg          sync.WaitGroup

	// gen counts changes; the snapshot is rebuilt only when it moved.
	gen      atomic.Uint64
	snapMu   sync.Mutex
	snapGen  uint64
	snapData *snapshot
}

// kindInformer watches one generated constraint kind.
type kindInformer struct {
	kind     string
	informer cache.SharedIndexInformer
	stop     chan struct{}
}

var (
	_ engine.Engine         = (*Engine)(nil)
	_ engine.Auditor        = (*Engine)(nil)
	_ engine.Platform       = (*Engine)(nil)
	_ engine.PolicyStatuser = (*Engine)(nil)
	_ engine.EventSource    = (*Engine)(nil)
)

// Name implements engine.Engine.
func (e *Engine) Name() string { return Name }

// Prefix implements engine.Engine.
func (e *Engine) Prefix() string { return Prefix }

// Capabilities implements engine.Engine.
func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{Policies: true, Violations: true, Health: true}
}

// Start implements engine.Engine.
func (e *Engine) Start(ctx context.Context, f dynamicinformer.DynamicSharedInformerFactory, notify engine.Notify) error {
	versions, err := enginekit.PreferredVersions(e.cluster.Discovery(), groupTemplates, groupConfig, groupConstraints)
	if err != nil {
		return fmt.Errorf("discover gatekeeper API groups: %w", err)
	}
	if notify == nil {
		notify = func() {}
	}

	e.mu.Lock()
	e.ctx, e.cancel = context.WithCancel(ctx)
	// Every event bumps the generation, invalidating the cached snapshot.
	upstream := notify
	e.notify = func() { e.gen.Add(1); upstream() }
	notify = e.notify
	e.versions = versions
	e.kick = make(chan struct{}, 1)
	e.mu.Unlock()
	registered := e.sync.Start()
	e.sync.SkipWhen(e.access.Denied)
	e.openDiscovery = e.sync.Gate("constraint kind discovery")

	dyn := e.cluster.Dynamic()
	if v, ok := versions[groupTemplates]; ok {
		gvr := schema.GroupVersionResource{Group: groupTemplates, Version: v, Resource: resourceTemplates}
		// A template change can create or remove a constraint kind.
		e.templates = e.access.SharedInformer(e.ctx, dyn, f, gvr, notify, func() { notify(); e.requestResync() })
		if e.templates != nil {
			e.sync.Track(gvr, e.templates)
		}
	}
	if v, ok := versions[groupConfig]; ok {
		gvr := schema.GroupVersionResource{Group: groupConfig, Version: v, Resource: resourceConfigs}
		e.configs = e.access.SharedInformer(e.ctx, dyn, f, gvr, notify, notify)
		if e.configs != nil {
			e.sync.Track(gvr, e.configs)
		}
	}
	registered()
	e.sync.Watch(e.ctx, notify)

	e.wg.Add(1)
	go e.resyncLoop()
	e.requestResync()
	return nil
}

func (e *Engine) requestResync() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// resyncLoop rediscovers constraint kinds whenever templates change. It is
// event-driven, not a poll: it only runs after a template event.
func (e *Engine) resyncLoop() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.kick:
			err := e.syncConstraintKinds()
			if err != nil && e.ctx.Err() == nil {
				e.discoveryErr.Store(&err)
			} else {
				e.discoveryErr.Store(nil)
			}
			// Open the gate even on failure: the error surfaces in Health,
			// and a sync that can never finish would hang the UI and CLI.
			e.openDiscovery()
			e.notify()
		}
	}
}

func (e *Engine) syncConstraintKinds() error {
	version, err := e.constraintVersion()
	if err != nil || version == "" {
		return err
	}
	want, err := enginekit.ServedResources(e.cluster.Discovery(), groupConstraints+"/"+version)
	if err != nil {
		return err
	}

	// Diff under a short lock; never hold it across API calls.
	e.mu.Lock()
	for res, ki := range e.constraints {
		if _, ok := want[res]; !ok {
			close(ki.stop)
			delete(e.constraints, res)
			e.sync.Untrack(schema.GroupVersionResource{Group: groupConstraints, Version: version, Resource: res})
		}
	}
	var missing []string
	for res := range want {
		if _, ok := e.constraints[res]; !ok {
			missing = append(missing, res)
		}
	}
	e.mu.Unlock()

	dyn := e.cluster.Dynamic()
	var allowed []string
	for _, res := range missing {
		gvr := schema.GroupVersionResource{Group: groupConstraints, Version: version, Resource: res}
		if e.access.CanList(e.ctx, dyn, gvr) {
			allowed = append(allowed, res)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return e.ctx.Err() // stopped meanwhile: starting informers now would leak them
	}
	for _, res := range allowed {
		if _, ok := e.constraints[res]; ok {
			continue
		}
		gvr := schema.GroupVersionResource{Group: groupConstraints, Version: version, Resource: res}
		inf := dynamicinformer.NewFilteredDynamicInformer(dyn, gvr, metav1.NamespaceAll, 0, cache.Indexers{}, nil).Informer()
		enginekit.SetTransform(inf, gvr)
		_ = inf.SetWatchErrorHandlerWithContext(e.access.WatchErrorHandler(gvr, e.notify))
		e.sync.Track(gvr, inf)
		_, _ = inf.AddEventHandler(enginekit.Handler(e.notify))
		ki := &kindInformer{kind: want[res], informer: inf, stop: make(chan struct{})}
		e.constraints[res] = ki
		go inf.Run(ki.stop)
	}
	e.gen.Add(1) // the informer set changed
	return nil
}

// constraintVersion returns the preferred constraints version, rediscovering
// it if the group appeared after Start (first template created).
func (e *Engine) constraintVersion() (string, error) {
	e.mu.RLock()
	v := e.versions[groupConstraints]
	e.mu.RUnlock()
	if v != "" {
		return v, nil
	}
	versions, err := enginekit.PreferredVersions(e.cluster.Discovery(), groupConstraints)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.versions[groupConstraints] = versions[groupConstraints]
	e.mu.Unlock()
	return versions[groupConstraints], nil
}

// Stop implements engine.Engine. It cancels first, without the lock, so any
// in-flight API call is interrupted rather than waited on.
func (e *Engine) Stop() {
	e.mu.RLock()
	cancel := e.cancel
	e.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	e.mu.Lock()
	for res, ki := range e.constraints {
		close(ki.stop)
		delete(e.constraints, res)
	}
	e.mu.Unlock()
	e.wg.Wait()
	e.sync.Reset()
}

// SyncStatus implements engine.Engine.
func (e *Engine) SyncStatus() engine.SyncStatus { return e.sync.Status() }

// AccessIssues implements engine.Engine.
func (e *Engine) AccessIssues() []model.AccessIssue { return e.access.Issues() }

// Mutations implements engine.Engine. Mutators land in phase 2.
func (e *Engine) Mutations() ([]model.Mutation, error) { return nil, engine.ErrNotSupported }

// Evaluator implements engine.Engine. Local evaluation lands in phase 4.
func (e *Engine) Evaluator() (engine.Evaluator, error) { return nil, engine.ErrNotSupported }

// Actions implements engine.Engine. Write actions land in phase 5.
func (e *Engine) Actions() (engine.Actions, error) { return nil, engine.ErrNotSupported }

// snapshot is a consistent copy of the informer caches. It is shared between
// callers until the next change, so it must be treated as read-only.
type snapshot struct {
	templates   []*unstructured.Unstructured
	constraints []*unstructured.Unstructured
	configs     []*unstructured.Unstructured
	// templateByKind maps a constraint kind to its template.
	templateByKind map[string]*unstructured.Unstructured
}

// snapshot returns the cached copy, rebuilding it after any change, so
// several reads per UI refresh deep-copy the caches only once.
func (e *Engine) snapshot() snapshot {
	e.snapMu.Lock()
	defer e.snapMu.Unlock()
	gen := e.gen.Load()
	if e.snapData != nil && e.snapGen == gen {
		return *e.snapData
	}
	s := e.buildSnapshot()
	e.snapData, e.snapGen = &s, gen
	return s
}

func (e *Engine) buildSnapshot() snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s := snapshot{templateByKind: map[string]*unstructured.Unstructured{}}
	s.templates = enginekit.Objects(e.templates)
	s.configs = enginekit.Objects(e.configs)
	for _, ki := range e.constraints {
		for _, o := range enginekit.Objects(ki.informer) {
			if o.GetKind() == "" {
				o.SetKind(ki.kind)
			}
			s.constraints = append(s.constraints, o)
		}
	}
	for _, t := range s.templates {
		if k := templateKind(t); k != "" {
			s.templateByKind[k] = t
		}
	}
	enginekit.SortObjects(s.templates)
	enginekit.SortObjects(s.configs)
	enginekit.SortObjects(s.constraints)
	return s
}

// newAccess tracks RBAC denials, falling back to the context's namespace
// for namespaced resources a tenant may read only there.
func newAccess(c engine.Cluster) *enginekit.Access {
	a := enginekit.NewAccess(Name)
	a.SetHomeNamespace(engine.HomeNamespace(c))
	return a
}
