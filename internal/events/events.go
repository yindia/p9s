// Package events watches the Kubernetes Events in which engines record
// admission decisions (denials, warnings, dry-run hits) for the "recent"
// tab of :v. Only server-side field-selected Events are cached.
package events

import (
	"context"
	"sort"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Name is used for access issues and sync status.
const Name = "events"

// EventsGVR is core v1 Events (what both engines write).
var EventsGVR = schema.GroupVersionResource{Version: "v1", Resource: "events"}

// Status says whether an engine records admission events.
type Status struct {
	Engine  string
	Enabled bool
	Hint    string
}

type watch struct {
	src engine.EventSource
	inf cache.SharedIndexInformer
}

// Watcher holds one informer per engine selector.
type Watcher struct {
	dyn    dynamic.Interface
	srcs   map[string]engine.EventSource
	access *enginekit.Access
	sync   *enginekit.Sync

	mu      sync.RWMutex
	cancel  context.CancelFunc
	watches []watch
}

// New returns a watcher for the engines that are event sources.
func New(dyn dynamic.Interface, srcs map[string]engine.EventSource) *Watcher {
	return &Watcher{dyn: dyn, srcs: srcs, access: enginekit.NewAccess(Name), sync: enginekit.NewSync()}
}

// Start probes RBAC and starts the informers; notify fires on new events.
// Probes run in parallel so a slow API server does not stall a context
// switch for every selector in turn.
func (w *Watcher) Start(ctx context.Context, notify func()) {
	ctx, cancel := context.WithCancel(ctx)
	registered := w.sync.Start()
	plan := w.plan()
	// Informers are tracked per selector but denied per namespace: map one
	// to the other so a watch forbidden after a good probe stops blocking
	// sync. The map is written here only, before any informer runs.
	accessOf := make(map[string]string, len(plan))
	for _, p := range plan {
		accessOf[p.syncKey()] = p.accessKey()
	}
	w.sync.SkipWhen(func(key string) bool { return w.access.Denied(accessOf[key]) })
	allowed := make([]bool, len(plan))
	var wg sync.WaitGroup
	for i, p := range plan {
		wg.Go(func() { allowed[i] = w.canList(ctx, p) })
	}
	wg.Wait()
	var ws []watch
	for i, p := range plan {
		if !allowed[i] {
			continue
		}
		fs := p.sel.FieldSelector
		inf := dynamicinformer.NewFilteredDynamicInformer(w.dyn, EventsGVR, p.sel.Namespace, 0, cache.Indexers{},
			func(o *metav1.ListOptions) { o.FieldSelector = fs }).Informer()
		enginekit.SetTransform(inf, EventsGVR)
		_ = inf.SetWatchErrorHandlerWithContext(w.access.WatchErrorHandlerAs(p.accessKey(), notify))
		_, _ = inf.AddEventHandler(enginekit.Handler(notify))
		w.sync.TrackAs(p.syncKey(), inf)
		ws = append(ws, watch{src: p.src, inf: inf})
	}
	registered()
	w.mu.Lock()
	w.cancel, w.watches = cancel, ws
	w.mu.Unlock()
	for _, wt := range ws {
		go wt.inf.RunWithContext(ctx)
	}
	w.sync.Watch(ctx, notify)
}

// planned is one informer to start: an engine's selector.
type planned struct {
	engine string
	src    engine.EventSource
	sel    engine.EventSelector
}

// syncKey is unique per informer.
func (p planned) syncKey() string {
	return p.engine + "#" + p.sel.Namespace + "#" + p.sel.FieldSelector
}

// accessKey is what RBAC denies (events in a namespace), and reads as the
// resource in notices.
func (p planned) accessKey() string {
	where := "cluster-wide"
	if p.sel.Namespace != "" {
		where = "in " + p.sel.Namespace
	}
	return "events " + where + " (" + p.engine + ")"
}

// plan lists the selectors of every source in a stable order.
func (w *Watcher) plan() []planned {
	names := make([]string, 0, len(w.srcs))
	for n := range w.srcs {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []planned
	for _, name := range names {
		for _, sel := range w.srcs[name].EventSelectors() {
			out = append(out, planned{engine: name, src: w.srcs[name], sel: sel})
		}
	}
	return out
}

func (w *Watcher) canList(ctx context.Context, p planned) bool {
	ctx, cancel := context.WithTimeout(ctx, enginekit.ProbeTimeout)
	defer cancel()
	_, err := w.dyn.Resource(EventsGVR).Namespace(p.sel.Namespace).List(ctx, metav1.ListOptions{Limit: 1, FieldSelector: p.sel.FieldSelector})
	if enginekit.IsDenied(err) {
		w.access.RecordAs(p.accessKey(), "list", err)
		return false
	}
	return true
}

// Stop stops the informers.
func (w *Watcher) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel, w.watches = nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.sync.Reset()
}

// Events returns every parsed admission event, newest first.
func (w *Watcher) Events() []model.AdmissionEvent {
	w.mu.RLock()
	ws := w.watches
	w.mu.RUnlock()
	var out []model.AdmissionEvent
	// One Event object is one result even if two selectors of an engine
	// overlap (or a client ignores field selectors).
	seen := map[string]bool{}
	for _, wt := range ws {
		for _, ev := range enginekit.Objects(wt.inf) {
			id := ev.GetNamespace() + "/" + ev.GetName()
			if seen[id] {
				continue
			}
			if e, ok := wt.src.ParseEvent(ev); ok {
				seen[id] = true
				e.ID = id
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return newer(out[i], out[j]) })
	return out
}

// newer orders events newest first, then by a stable identity.
func newer(a, b model.AdmissionEvent) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.After(b.Time)
	}
	if a.Policy != b.Policy {
		return a.Policy.String() < b.Policy.String()
	}
	if a.Target != b.Target {
		return a.Target.String() < b.Target.String()
	}
	return a.Message < b.Message
}

// Statuses says, per engine, whether admission events are recorded.
func (w *Watcher) Statuses() []Status {
	out := make([]Status, 0, len(w.srcs))
	for name, src := range w.srcs {
		on, hint := src.EventsEnabled()
		out = append(out, Status{Engine: name, Enabled: on, Hint: hint})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Engine < out[j].Engine })
	return out
}

// SyncStatus reports whether the caches are loaded.
func (w *Watcher) SyncStatus() engine.SyncStatus { return w.sync.Status() }

// AccessIssues lists what RBAC hides.
func (w *Watcher) AccessIssues() []model.AccessIssue { return w.access.Issues() }
