package enginekit

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
)

// Sync tracks whether an engine's informers finished their initial list.
// Besides informers it can hold gates: named conditions an engine opens
// itself, such as "first constraint-kind discovery finished".
type Sync struct {
	mu      sync.RWMutex
	started bool
	checks  map[string]func() bool
	// skip reports resources that must not block sync (e.g. RBAC-denied
	// watches, which can never sync and are already shown as notices).
	skip func(name string) bool
	// changed wakes the watcher; gen counts changes so a watcher that
	// sleeps through an unsynced→synced blip still notifies afterwards.
	changed chan struct{}
	gen     atomic.Uint64
}

// registering is the gate held while an engine registers its informers, so
// it never reports synced between "started" and "everything tracked".
const registering = "registering informers"

// NewSync returns an empty tracker; it reports not-synced until Start.
func NewSync() *Sync {
	return &Sync{checks: map[string]func() bool{}, changed: make(chan struct{}, 1)}
}

// Start marks the engine started and holds a "registering informers" gate.
// Call the returned func once every informer and gate is registered.
func (s *Sync) Start() (registered func()) {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	return s.Gate(registering)
}

// SkipWhen sets the predicate for resources that must not block sync.
func (s *Sync) SkipWhen(skip func(name string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skip = skip
}

// Reset forgets everything (used by Stop).
func (s *Sync) Reset() {
	s.mu.Lock()
	s.started = false
	s.checks = map[string]func() bool{}
	s.mu.Unlock()
	s.signal()
}

// Track records an informer under its group-resource name.
func (s *Sync) Track(gvr schema.GroupVersionResource, inf cache.SharedIndexInformer) {
	s.add(gvr.GroupResource().String(), inf.HasSynced)
}

// TrackAs records an informer under an explicit key (matching the key its
// access denials use, so a denied watch never blocks sync).
func (s *Sync) TrackAs(key string, inf cache.SharedIndexInformer) {
	s.add(key, inf.HasSynced)
}

// Untrack drops an informer that was stopped.
func (s *Sync) Untrack(gvr schema.GroupVersionResource) {
	s.mu.Lock()
	delete(s.checks, gvr.GroupResource().String())
	s.mu.Unlock()
	s.signal()
}

// Gate registers a named condition that is pending until its Open is called.
func (s *Sync) Gate(name string) (open func()) {
	var done atomic.Bool
	s.add(name, done.Load)
	return func() {
		done.Store(true)
		s.signal()
	}
}

func (s *Sync) add(name string, fn func() bool) {
	s.mu.Lock()
	s.checks[name] = fn
	s.mu.Unlock()
	s.signal()
}

func (s *Sync) signal() {
	s.gen.Add(1)
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Status reports synced when started and every check passes.
func (s *Sync) Status() engine.SyncStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.started {
		return engine.SyncStatus{Pending: []string{"not started"}}
	}
	var pending []string
	for name, fn := range s.checks {
		if !fn() && (s.skip == nil || !s.skip(name)) {
			pending = append(pending, name)
		}
	}
	sort.Strings(pending)
	return engine.SyncStatus{Synced: len(pending) == 0, Pending: pending}
}

// watchInterval is how often the watcher re-reads in-memory sync state while
// unsynced (informers expose HasSynced, not a channel). It never touches
// the API server.
const watchInterval = 50 * time.Millisecond

// Watch runs until ctx ends and calls notify whenever the engine is synced
// after any change, so a UI repaints at sync time even when no object event
// arrives (empty lists). Change-based, not transition-based: a check added
// and completed between two wakeups still notifies, since a renderer may
// have drawn the brief unsynced state. One watcher per engine: it ticks only
// while unsynced and otherwise sleeps until something changes.
func (s *Sync) Watch(ctx context.Context, notify func()) {
	go func() {
		notified := ^uint64(0) // nothing notified yet
		tick := time.NewTicker(watchInterval)
		defer tick.Stop()
		for {
			gen := s.gen.Load()
			synced := s.Status().Synced
			if synced && gen != notified {
				notify()
				notified = gen
			}
			wake := s.changed
			var tc <-chan time.Time
			if !synced {
				tc = tick.C
			}
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-tc:
			}
		}
	}()
}

// StripFields controls the cache transform. It exists so the e2e memory
// measurement can compare with and without it; production leaves it on.
var StripFields atomic.Bool

func init() { StripFields.Store(true) }

// lastApplied is a full copy of the object kept by kubectl; p9s never reads it.
const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// Transform drops fields p9s never reads before objects enter informer
// caches: metadata.managedFields and kubectl's last-applied annotation.
// Large clusters keep thousands of objects (one PolicyReport per resource),
// and these fields are often the biggest part of each.
func Transform(obj any) (any, error) {
	if !StripFields.Load() {
		return obj, nil
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return obj, nil // tombstones and other wrappers pass through
	}
	u.SetManagedFields(nil)
	if ann := u.GetAnnotations(); ann != nil {
		if _, has := ann[lastApplied]; has {
			delete(ann, lastApplied)
			if len(ann) == 0 {
				ann = nil
			}
			u.SetAnnotations(ann)
		}
	}
	return u, nil
}
