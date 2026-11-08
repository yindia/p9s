// Package platform watches the API-side state of the engines' moving parts
// (webhook configurations, the Deployments, Pods, EndpointSlices and Leases
// in the engines' namespaces) for the health dashboard. It reads the API
// only; it never talks to the engines.
package platform

import (
	"context"
	"sort"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
)

// Name is used for access issues and sync status.
const Name = "platform"

// GVRs watched.
var (
	ValidatingWebhooks = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	MutatingWebhooks   = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	EndpointSlices     = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	Deployments        = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	Pods               = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	Leases             = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
)

// Target is one engine's location.
type Target struct {
	Engine string
	Info   engine.PlatformInfo
}

// Watcher holds the informers.
type Watcher struct {
	dyn     dynamic.Interface
	targets []Target
	access  *enginekit.Access
	sync    *enginekit.Sync

	mu        sync.RWMutex
	cancel    context.CancelFunc
	factories []dynamicinformer.DynamicSharedInformerFactory
	cluster   map[schema.GroupVersionResource]cache.SharedIndexInformer
	// namespaced[ns][gvr]
	namespaced map[string]map[schema.GroupVersionResource]cache.SharedIndexInformer
}

// New returns a watcher for the engines' targets.
func New(dyn dynamic.Interface, targets []Target) *Watcher {
	return &Watcher{dyn: dyn, targets: targets, access: enginekit.NewAccess(Name), sync: enginekit.NewSync()}
}

// Start registers informers (after an RBAC probe each) and starts them.
// notify fires on changes and when caches have synced.
func (w *Watcher) Start(ctx context.Context, notify func()) {
	ctx, cancel := context.WithCancel(ctx)
	registered := w.sync.Start()
	w.sync.SkipWhen(w.access.Denied)
	cluster := map[schema.GroupVersionResource]cache.SharedIndexInformer{}
	namespaced := map[string]map[schema.GroupVersionResource]cache.SharedIndexInformer{}

	cf := dynamicinformer.NewDynamicSharedInformerFactory(w.dyn, 0)
	factories := []dynamicinformer.DynamicSharedInformerFactory{cf}
	for _, gvr := range []schema.GroupVersionResource{ValidatingWebhooks, MutatingWebhooks} {
		if inf := w.access.SharedInformer(ctx, w.dyn, cf, gvr, notify, notify); inf != nil {
			cluster[gvr] = inf
			w.sync.Track(gvr, inf)
		}
	}
	nsGVRs := []schema.GroupVersionResource{EndpointSlices, Deployments, Pods, Leases}
	namespaces := w.namespaces()
	// Probe in parallel: serial probes would stall a context switch.
	allowed := make([][]bool, len(namespaces))
	var wg sync.WaitGroup
	for i, ns := range namespaces {
		allowed[i] = make([]bool, len(nsGVRs))
		for j, gvr := range nsGVRs {
			wg.Go(func() { allowed[i][j] = w.canListIn(ctx, gvr, ns) })
		}
	}
	wg.Wait()
	for i, ns := range namespaces {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(w.dyn, 0, ns, nil)
		factories = append(factories, f)
		namespaced[ns] = map[schema.GroupVersionResource]cache.SharedIndexInformer{}
		for j, gvr := range nsGVRs {
			if !allowed[i][j] {
				continue
			}
			key := nsKey(gvr, ns)
			inf := f.ForResource(gvr).Informer()
			enginekit.SetTransform(inf, gvr)
			_ = inf.SetWatchErrorHandlerWithContext(w.access.WatchErrorHandlerAs(key, notify))
			_, _ = inf.AddEventHandler(enginekit.Handler(notify))
			namespaced[ns][gvr] = inf
			// Same key as its access denials: a denied watch never blocks sync.
			w.sync.TrackAs(key, inf)
		}
	}
	registered()
	w.mu.Lock()
	w.cancel, w.factories, w.cluster, w.namespaced = cancel, factories, cluster, namespaced
	w.mu.Unlock()
	for _, f := range factories {
		f.Start(ctx.Done())
	}
	w.sync.Watch(ctx, notify)
}

func (w *Watcher) canListIn(ctx context.Context, gvr schema.GroupVersionResource, ns string) bool {
	ctx, cancel := context.WithTimeout(ctx, enginekit.ProbeTimeout)
	defer cancel()
	_, err := w.dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1})
	if enginekit.IsDenied(err) {
		w.access.RecordAs(nsKey(gvr, ns), "list", err)
		return false
	}
	return true
}

// nsKey names a namespaced watch, e.g. "endpointslices.discovery.k8s.io@kyverno".
func nsKey(gvr schema.GroupVersionResource, ns string) string {
	return gvr.GroupResource().String() + "@" + ns
}

func (w *Watcher) namespaces() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range w.targets {
		if ns := t.Info.Namespace; ns != "" && !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out
}

// Stop stops the informers.
func (w *Watcher) Stop() {
	w.mu.Lock()
	cancel, fs := w.cancel, w.factories
	w.cancel, w.factories, w.cluster, w.namespaced = nil, nil, nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, f := range fs {
		go f.Shutdown()
	}
	w.sync.Reset()
}

// SyncStatus reports whether the caches are loaded.
func (w *Watcher) SyncStatus() engine.SyncStatus { return w.sync.Status() }

// AccessIssues lists what RBAC hides; affected panels are then partial.
func (w *Watcher) AccessIssues() []model.AccessIssue { return w.access.Issues() }

// Snapshot assembles the current state. Each namespace's workloads and each
// webhook are attributed to exactly one engine.
func (w *Watcher) Snapshot() query.Platform {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var p query.Platform
	seenNS := map[string]bool{}
	for _, t := range w.targets {
		ns := t.Info.Namespace
		if seenNS[ns] {
			continue // engines sharing a namespace: the first one owns it
		}
		seenNS[ns] = true
		infs := w.namespaced[ns]
		pods := enginekit.Objects(infs[Pods])
		p.Controllers = append(p.Controllers, controllers(t.Engine, enginekit.Objects(infs[Deployments]), pods)...)
		p.Leases = append(p.Leases, leases(t.Engine, enginekit.Objects(infs[Leases]))...)
	}
	for _, kind := range []struct {
		gvr schema.GroupVersionResource
		typ string
	}{{ValidatingWebhooks, "validating"}, {MutatingWebhooks, "mutating"}} {
		for _, cfg := range enginekit.Objects(w.cluster[kind.gvr]) {
			p.Webhooks = append(p.Webhooks, w.webhooks(kind.typ, cfg)...)
		}
	}
	sort.SliceStable(p.Webhooks, func(i, j int) bool {
		return p.Webhooks[i].Engine+p.Webhooks[i].Config+p.Webhooks[i].Name < p.Webhooks[j].Engine+p.Webhooks[j].Config+p.Webhooks[j].Name
	})
	return p
}

// slices returns the EndpointSlices of a namespace, and whether they are
// known: the namespace is watched, readable and synced. Unknown endpoints
// must never be graded as "none".
func (w *Watcher) slices(ns string) ([]*unstructured.Unstructured, bool) {
	inf := w.namespaced[ns][EndpointSlices]
	if inf == nil || !inf.HasSynced() {
		return nil, false
	}
	return enginekit.Objects(inf), true
}

// owner attributes a webhook to one engine: the config's labels match an
// engine's selector, else its service lives in an engine's namespace.
func (w *Watcher) owner(cfg *unstructured.Unstructured, svcNS string) (string, bool) {
	for _, t := range w.targets {
		if len(t.Info.WebhookSelector) > 0 &&
			labels.SelectorFromSet(t.Info.WebhookSelector).Matches(labels.Set(cfg.GetLabels())) {
			return t.Engine, true
		}
	}
	for _, t := range w.targets {
		if svcNS != "" && svcNS == t.Info.Namespace {
			return t.Engine, true
		}
	}
	return "", false
}

// webhooks returns cfg's webhooks that belong to an engine.
func (w *Watcher) webhooks(typ string, cfg *unstructured.Unstructured) []query.Webhook {
	var out []query.Webhook
	for _, raw := range enginekit.Slice(cfg.Object, "webhooks") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		svcNS, _, _ := unstructured.NestedString(m, "clientConfig", "service", "namespace")
		svcName, _, _ := unstructured.NestedString(m, "clientConfig", "service", "name")
		eng, ok := w.owner(cfg, svcNS)
		if !ok {
			continue
		}
		wh := query.Webhook{
			Engine: eng, Type: typ, Config: cfg.GetName(), Name: enginekit.Str(m, "name"),
			FailurePolicy: enginekit.Str(m, "failurePolicy"), TimeoutSecs: enginekit.Int(m, "timeoutSeconds"),
			NamespaceSelector: m["namespaceSelector"], ObjectSelector: m["objectSelector"], Rules: m["rules"],
			Raw: cfg,
		}
		if wh.FailurePolicy == "" {
			wh.FailurePolicy = "Fail" // the API default for v1
		}
		if wh.TimeoutSecs == 0 {
			wh.TimeoutSecs = 10 // the API default for v1
		}
		wh.URL, _, _ = unstructured.NestedString(m, "clientConfig", "url")
		if ca, _, _ := unstructured.NestedString(m, "clientConfig", "caBundle"); ca != "" {
			wh.CABundle = decodeCA(ca)
		}
		if svcName != "" {
			wh.Service = svcNS + "/" + svcName
			if slices, known := w.slices(svcNS); known {
				wh.Endpoints, wh.EndpointsKnown = endpoints(slices, svcName), true
			}
		}
		out = append(out, wh)
	}
	return out
}

func endpoints(slices []*unstructured.Unstructured, service string) []query.Endpoint {
	var out []query.Endpoint
	for _, s := range slices {
		if s.GetLabels()["kubernetes.io/service-name"] != service {
			continue
		}
		for _, raw := range enginekit.Slice(s.Object, "endpoints") {
			m, _ := raw.(map[string]any)
			// Ready is "unknown means ready" per the EndpointSlice API.
			ready, found, _ := unstructured.NestedBool(m, "conditions", "ready")
			e := query.Endpoint{Ready: ready || !found}
			if name, _, _ := unstructured.NestedString(m, "targetRef", "name"); name != "" {
				ns, _, _ := unstructured.NestedString(m, "targetRef", "namespace")
				e.Pod = ns + "/" + name
			}
			out = append(out, e)
		}
	}
	return out
}

func controllers(engineName string, deploys, pods []*unstructured.Unstructured) []query.Controller {
	var out []query.Controller
	for _, d := range deploys {
		c := query.Controller{
			Engine: engineName, Namespace: d.GetNamespace(), Name: d.GetName(), Raw: d,
			Ready:   enginekit.Int(d.Object, "status", "readyReplicas"),
			Desired: desiredReplicas(d),
		}
		for _, ct := range enginekit.Slice(d.Object, "spec", "template", "spec", "containers") {
			m, _ := ct.(map[string]any)
			c.Images = append(c.Images, enginekit.Str(m, "image"))
		}
		sel := podSelector(d)
		for _, p := range pods {
			if sel == nil || !sel.Matches(labels.Set(p.GetLabels())) {
				continue
			}
			c.Pods = append(c.Pods, podState(p, &c))
		}
		out = append(out, c)
	}
	return out
}

// podSelector parses spec.selector (matchLabels and matchExpressions);
// nil when absent or invalid, so no pod is wrongly attributed.
func podSelector(d *unstructured.Unstructured) labels.Selector {
	raw, ok, _ := unstructured.NestedMap(d.Object, "spec", "selector")
	if !ok {
		return nil
	}
	var ls metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &ls); err != nil {
		return nil
	}
	sel, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil || sel.Empty() {
		return nil
	}
	return sel
}

// desiredReplicas reads spec.replicas, which the API defaults to 1 when
// unset; an unset value is not "scaled to zero".
func desiredReplicas(d *unstructured.Unstructured) int {
	if _, found, _ := unstructured.NestedFieldNoCopy(d.Object, "spec", "replicas"); !found {
		return 1
	}
	return enginekit.Int(d.Object, "spec", "replicas")
}

// podState summarises a pod and folds its restarts into the controller.
func podState(p *unstructured.Unstructured, c *query.Controller) query.PodState {
	phase, _, _ := unstructured.NestedString(p.Object, "status", "phase")
	ps := query.PodState{Name: p.GetName(), Phase: phase}
	for _, cond := range enginekit.Slice(p.Object, "status", "conditions") {
		m, _ := cond.(map[string]any)
		if enginekit.Str(m, "type") == "Ready" {
			ps.Ready = enginekit.Str(m, "status") == "True"
		}
	}
	for _, cs := range enginekit.Slice(p.Object, "status", "containerStatuses") {
		m, _ := cs.(map[string]any)
		n := enginekit.Int(m, "restartCount")
		ps.Restarts += n
		c.Restarts += n
		term, ok, _ := unstructured.NestedMap(m, "lastState", "terminated")
		if !ok {
			continue
		}
		if at, err := time.Parse(time.RFC3339, enginekit.Str(term, "finishedAt")); err == nil && at.After(c.LastRestart) {
			c.LastRestart, c.LastReason = at, enginekit.Str(term, "reason")
		}
	}
	return ps
}

func leases(engineName string, objs []*unstructured.Unstructured) []query.Lease {
	var out []query.Lease
	for _, l := range objs {
		holder, _, _ := unstructured.NestedString(l.Object, "spec", "holderIdentity")
		renew, _, _ := unstructured.NestedString(l.Object, "spec", "renewTime")
		r, _ := time.Parse(time.RFC3339Nano, renew)
		out = append(out, query.Lease{Engine: engineName, Namespace: l.GetNamespace(), Name: l.GetName(), Holder: holder, Renewed: r})
	}
	return out
}
