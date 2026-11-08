// Package session owns the live connection: the current kube context, the
// engines detected in it, and their informers. It is the UI's backend.
package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/metadata"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/events"
	"github.com/yindia/p9s/internal/kube"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/owners"
	"github.com/yindia/p9s/internal/platform"
	"github.com/yindia/p9s/internal/views"
)

const detectTimeout = 20 * time.Second

// Cluster is a connected context.
type Cluster interface {
	engine.Cluster
	Context() string
	NewInformerFactory() dynamicinformer.DynamicSharedInformerFactory
	Get(ctx context.Context, ref model.ObjectRef) (*unstructured.Unstructured, error)
}

// Connector lists contexts and connects to one.
type Connector interface {
	Contexts() ([]model.KubeContext, string, error)
	Connect(contextName string) (Cluster, error)
}

// KubeConnector adapts a kube.Loader to Connector.
func KubeConnector(l *kube.Loader) Connector { return kubeConnector{l} }

type kubeConnector struct{ l *kube.Loader }

func (k kubeConnector) Contexts() ([]model.KubeContext, string, error) { return k.l.Contexts() }
func (k kubeConnector) Connect(name string) (Cluster, error)           { return k.l.Connect(name) }

// Options configure a Session.
type Options struct {
	Connector Connector
	Engines   []engine.Registration
	Context   string
	ReadOnly  bool
	// NoOwners disables owner resolution (memory comparisons, tests).
	NoOwners bool
}

// metadataCluster is implemented by clusters that can resolve owners.
type metadataCluster interface {
	Metadata() metadata.Interface
}

// Session is safe for concurrent use.
type Session struct {
	connector Connector
	regs      []engine.Registration
	initial   string
	readOnly  bool
	noOwners  bool
	updates   chan struct{}

	// switchMu serializes SwitchContext so engine swaps happen in call order.
	switchMu   sync.Mutex
	mu         sync.RWMutex
	cluster    Cluster
	detections []engine.Detection
	cancel     context.CancelFunc
	factory    dynamicinformer.DynamicSharedInformerFactory
	owners     *owners.Resolver
	platform   *platform.Watcher
	events     *events.Watcher
	updatedAt  atomic.Int64 // unix nanos of the last data change
}

// New returns an unconnected session.
func New(opts Options) (*Session, error) {
	if opts.Connector == nil {
		return nil, errors.New("session: connector is required")
	}
	return &Session{
		connector: opts.Connector,
		regs:      opts.Engines,
		initial:   opts.Context,
		readOnly:  opts.ReadOnly,
		noOwners:  opts.NoOwners,
		updates:   make(chan struct{}, 1),
	}, nil
}

// Connect connects to the initial context.
func (s *Session) Connect(ctx context.Context) error {
	return s.SwitchContext(ctx, s.initial)
}

// SwitchContext stops the current engines, connects to name and starts the
// engines detected there. An empty name means the kubeconfig current-context.
func (s *Session) SwitchContext(ctx context.Context, name string) error {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	cl, err := s.connector.Connect(name)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	dctx, cancelDetect := context.WithTimeout(ctx, detectTimeout)
	detections := engine.Detect(dctx, s.regs, cl)
	cancelDetect()

	runCtx, cancel := context.WithCancel(context.Background())
	factory := cl.NewInformerFactory()
	for i, d := range detections {
		if !d.Installed || d.Err != nil {
			continue
		}
		if err := d.Engine.Start(runCtx, factory, s.notify); err != nil {
			detections[i].Err = fmt.Errorf("start %s: %w", d.Engine.Name(), err)
			detections[i].Installed = false
		}
	}
	factory.Start(runCtx.Done())

	var res *owners.Resolver
	if mc, ok := cl.(metadataCluster); ok && !s.noOwners && mc.Metadata() != nil {
		res = owners.New(mc.Metadata())
		res.SetHomeNamespace(engine.HomeNamespace(cl))
		res.Start(runCtx, s.notify)
	}
	var plat *platform.Watcher
	if targets := platformTargets(detections); len(targets) > 0 {
		plat = platform.New(cl.Dynamic(), targets)
		plat.Start(runCtx, s.notify)
	}
	var evs *events.Watcher
	if srcs := eventSources(detections); len(srcs) > 0 {
		evs = events.New(cl.Dynamic(), srcs)
		evs.Start(runCtx, s.notify)
	}

	s.mu.Lock()
	old, oldCancel, oldFactory, oldOwners, oldPlat, oldEvents := s.detections, s.cancel, s.factory, s.owners, s.platform, s.events
	s.cluster, s.detections, s.cancel, s.factory, s.owners, s.platform, s.events = cl, detections, cancel, factory, res, plat, evs
	s.mu.Unlock()
	if oldEvents != nil {
		oldEvents.Stop()
	}

	if oldOwners != nil {
		oldOwners.Stop()
	}
	if oldPlat != nil {
		oldPlat.Stop()
	}
	stopAll(old, oldCancel, oldFactory)
	s.notify()
	return nil
}

func stopAll(ds []engine.Detection, cancel context.CancelFunc, f dynamicinformer.DynamicSharedInformerFactory) {
	for _, d := range ds {
		if d.Installed && d.Err == nil {
			d.Engine.Stop()
		}
	}
	if cancel != nil {
		cancel()
	}
	if f != nil {
		go f.Shutdown()
	}
}

// Close stops all engines.
func (s *Session) Close() {
	s.mu.Lock()
	ds, cancel, f, res, plat, evs := s.detections, s.cancel, s.factory, s.owners, s.platform, s.events
	s.detections, s.cancel, s.factory, s.owners, s.platform, s.events = nil, nil, nil, nil, nil, nil
	s.mu.Unlock()
	if evs != nil {
		evs.Stop()
	}
	if res != nil {
		res.Stop()
	}
	if plat != nil {
		plat.Stop()
	}
	stopAll(ds, cancel, f)
}

func (s *Session) notify() {
	s.updatedAt.Store(time.Now().UnixNano())
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

// Updates fires (coalesced) whenever any engine's data changed.
func (s *Session) Updates() <-chan struct{} { return s.updates }

// ReadOnly reports whether writes are disabled.
func (s *Session) ReadOnly() bool { return s.readOnly }

// ContextName is the connected context, or "" before Connect.
func (s *Session) ContextName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cluster == nil {
		return ""
	}
	return s.cluster.Context()
}

// Contexts lists kubeconfig contexts, marking the connected one current.
func (s *Session) Contexts() ([]model.KubeContext, error) {
	ctxs, _, err := s.connector.Contexts()
	if err != nil {
		return nil, err
	}
	cur := s.ContextName()
	out := make([]model.KubeContext, len(ctxs))
	for i, c := range ctxs {
		c.Current = c.Name == cur
		out[i] = c
	}
	return out, nil
}

// Detections returns the detection results for every selected engine.
func (s *Session) Detections() []engine.Detection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]engine.Detection(nil), s.detections...)
}

// Engines returns the active (installed and started) engines.
func (s *Session) Engines() []engine.Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []engine.Engine
	for _, d := range s.detections {
		if d.Installed && d.Err == nil {
			out = append(out, d.Engine)
		}
	}
	return out
}

var namespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// Namespaces lists the cluster's namespaces, sorted.
func (s *Session) Namespaces(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	cl := s.cluster
	s.mu.RUnlock()
	if cl == nil {
		return nil, errors.New("not connected")
	}
	list, err := cl.Dynamic().Resource(namespacesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	out := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		out = append(out, n.GetName())
	}
	sort.Strings(out)
	return out, nil
}

// platformTargets locates the active engines that run in the cluster.
func platformTargets(ds []engine.Detection) []platform.Target {
	var out []platform.Target
	for _, d := range ds {
		if !d.Installed || d.Err != nil {
			continue
		}
		if p, ok := d.Engine.(engine.Platform); ok {
			out = append(out, platform.Target{Engine: d.Engine.Name(), Info: p.PlatformInfo()})
		}
	}
	return out
}

// eventSources are the active engines that record admission events.
func eventSources(ds []engine.Detection) map[string]engine.EventSource {
	out := map[string]engine.EventSource{}
	for _, d := range ds {
		if !d.Installed || d.Err != nil {
			continue
		}
		if src, ok := d.Engine.(engine.EventSource); ok {
			out[d.Engine.Name()] = src
		}
	}
	return out
}

// Events returns the admission events watcher, or nil.
func (s *Session) Events() views.Events {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.events == nil {
		return nil
	}
	return eventsAdapter{s.events}
}

// eventsAdapter converts watcher statuses to the views type.
type eventsAdapter struct{ w *events.Watcher }

func (a eventsAdapter) Events() []model.AdmissionEvent    { return a.w.Events() }
func (a eventsAdapter) AccessIssues() []model.AccessIssue { return a.w.AccessIssues() }
func (a eventsAdapter) SyncStatus() engine.SyncStatus     { return a.w.SyncStatus() }
func (a eventsAdapter) Statuses() []views.EventStatus {
	var out []views.EventStatus
	for _, st := range a.w.Statuses() {
		out = append(out, views.EventStatus{Engine: st.Engine, Enabled: st.Enabled, Hint: st.Hint})
	}
	return out
}

// Platform returns the health watcher of the current context, or nil.
func (s *Session) Platform() views.Platform {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.platform == nil {
		return nil
	}
	return s.platform
}

// UpdatedAt is when engine or platform data last changed.
func (s *Session) UpdatedAt() time.Time {
	if n := s.updatedAt.Load(); n > 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// Owners returns the owner resolver of the current context, or nil when
// owners are not resolved (no metadata client, or disabled).
func (s *Session) Owners() views.Owners {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.owners == nil {
		return nil
	}
	return s.owners
}

// GetObject fetches the object a reference points at.
func (s *Session) GetObject(ctx context.Context, ref model.ObjectRef) (*unstructured.Unstructured, error) {
	s.mu.RLock()
	cl := s.cluster
	s.mu.RUnlock()
	if cl == nil {
		return nil, errors.New("not connected")
	}
	return cl.Get(ctx, ref)
}
