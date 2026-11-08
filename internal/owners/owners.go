// Package owners resolves violation targets to their top-level owners from
// metadata-only informers (names, labels and ownerReferences; no specs).
// It runs per kube context next to the engines and implements
// query.OwnerLookup.
package owners

import (
	"context"
	"fmt"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
)

// Name is used for access issues and sync status.
const Name = "owners"

// watched are the kinds the walk and label grouping need. Pods, ReplicaSets
// and Jobs carry the ownerRefs of the common chains (Pod → ReplicaSet →
// Deployment, Pod → Job → CronJob); the top-level kinds and Namespaces
// carry the labels used by --group-label.
var watched = []struct {
	gvr  schema.GroupVersionResource
	kind string
}{
	{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod"},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "ReplicaSet"},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "Job"},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment"},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet"},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet"},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, "CronJob"},
	{schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "Namespace"},
}

// Resolver caches ownership metadata.
type Resolver struct {
	client  metadata.Interface
	access  *enginekit.Access
	sync    *enginekit.Sync
	factory metadatainformer.SharedInformerFactory
	// homeFactory watches the home namespace for kinds a tenant may not
	// list cluster-wide; nil without a home namespace.
	homeFactory metadatainformer.SharedInformerFactory

	mu     sync.RWMutex
	stores map[schema.GroupKind]cache.Store // keys "ns/name" or "name"
	cancel context.CancelFunc
}

var _ query.OwnerLookup = (*Resolver)(nil)

// New returns a resolver for a metadata client.
func New(client metadata.Interface) *Resolver {
	return &Resolver{client: client, access: enginekit.NewAccess(Name), sync: enginekit.NewSync(), stores: map[schema.GroupKind]cache.Store{}}
}

// Start probes RBAC and starts the informers. Forbidden kinds are recorded
// as access issues and skipped: resolution then stops at the deepest owner
// that can be read. notify fires once when caches have synced.
func (r *Resolver) Start(ctx context.Context, notify func()) {
	ctx, cancel := context.WithCancel(ctx)
	registered := r.sync.Start()
	r.sync.SkipWhen(r.access.Denied)
	f := metadatainformer.NewSharedInformerFactoryWithOptions(r.client, 0, metadatainformer.WithTransform(transform))
	// A tenant may read namespaced kinds only in their namespace.
	var homeF metadatainformer.SharedInformerFactory
	if home := r.access.HomeNamespace(); home != "" {
		homeF = metadatainformer.NewFilteredSharedInformerFactory(r.client, 0, home, nil)
	}
	// Probe in parallel: on a slow API server serial probes would stall a
	// context switch for len(watched) × ProbeTimeout.
	allowed := make([]bool, len(watched))
	scoped := make([]bool, len(watched))
	var wg sync.WaitGroup
	for i, w := range watched {
		wg.Go(func() {
			allowed[i] = r.canList(ctx, w.gvr)
			if !allowed[i] && homeF != nil && w.kind != "Namespace" && r.canListHome(ctx, w.gvr) {
				allowed[i], scoped[i] = true, true
			}
		})
	}
	wg.Wait()
	stores := map[schema.GroupKind]cache.Store{}
	for i, w := range watched {
		if !allowed[i] {
			continue
		}
		src := f
		if scoped[i] {
			src = homeF
		}
		inf := src.ForResource(w.gvr).Informer()
		if scoped[i] {
			_ = inf.SetTransform(transform) // the filtered factory takes no options
		}
		_ = inf.SetWatchErrorHandlerWithContext(r.access.WatchErrorHandler(w.gvr, func() {}))
		r.sync.Track(w.gvr, inf)
		stores[schema.GroupKind{Group: w.gvr.Group, Kind: w.kind}] = inf.GetStore()
	}
	registered()
	r.mu.Lock()
	r.stores, r.cancel, r.factory, r.homeFactory = stores, cancel, f, homeF
	r.mu.Unlock()
	r.sync.Watch(ctx, notify)
	f.Start(ctx.Done())
	if homeF != nil {
		homeF.Start(ctx.Done())
	}
}

// canListHome probes the home namespace after a cluster-wide denial and,
// when it is readable, turns the denial into a "this namespace only" notice.
func (r *Resolver) canListHome(ctx context.Context, gvr schema.GroupVersionResource) bool {
	home := r.access.HomeNamespace()
	ctx, cancel := context.WithTimeout(ctx, enginekit.ProbeTimeout)
	defer cancel()
	if _, err := r.client.Resource(gvr).Namespace(home).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return false
	}
	r.access.ScopeToHome(gvr)
	return true
}

// SetHomeNamespace enables the tenant fallback to one namespace.
func (r *Resolver) SetHomeNamespace(ns string) { r.access.SetHomeNamespace(ns) }

func (r *Resolver) canList(ctx context.Context, gvr schema.GroupVersionResource) bool {
	ctx, cancel := context.WithTimeout(ctx, enginekit.ProbeTimeout)
	defer cancel()
	_, err := r.client.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		r.access.Record(gvr, "list", err)
		return false
	}
	return true
}

// Stop stops the informers.
func (r *Resolver) Stop() {
	r.mu.Lock()
	cancel, f, hf := r.cancel, r.factory, r.homeFactory
	r.cancel, r.factory, r.homeFactory, r.stores = nil, nil, nil, map[schema.GroupKind]cache.Store{}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if f != nil {
		go f.Shutdown()
	}
	if hf != nil {
		go hf.Shutdown()
	}
	r.sync.Reset()
}

// SyncStatus reports whether the metadata caches are loaded.
func (r *Resolver) SyncStatus() engine.SyncStatus { return r.sync.Status() }

// AccessIssues lists kinds RBAC hides; grouping is then partial.
func (r *Resolver) AccessIssues() []model.AccessIssue { return r.access.Issues() }

func (r *Resolver) get(ref model.ObjectRef) (*metav1.PartialObjectMetadata, bool) {
	gk := canonical(schema.GroupKind{Group: ref.Group, Kind: ref.Kind})
	ns, name := ref.Namespace, ref.Name
	if gk == namespaceGK {
		ns = "" // a Namespace target may carry its own name as namespace
	}
	r.mu.RLock()
	st := r.stores[gk]
	r.mu.RUnlock()
	if st == nil {
		return nil, false
	}
	key := name
	if ns != "" {
		key = ns + "/" + name
	}
	obj, ok, err := st.GetByKey(key)
	if err != nil || !ok {
		return nil, false
	}
	m, ok := obj.(*metav1.PartialObjectMetadata)
	return m, ok
}

// Controller implements query.OwnerLookup.
func (r *Resolver) Controller(ref model.ObjectRef) (model.ObjectRef, bool) {
	m, ok := r.get(ref)
	if !ok {
		return model.ObjectRef{}, false
	}
	for _, o := range m.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			return ownerRef(o, ref.Namespace), true
		}
	}
	return model.ObjectRef{}, false
}

// Labels implements query.OwnerLookup.
func (r *Resolver) Labels(ref model.ObjectRef) (map[string]string, bool) {
	m, ok := r.get(ref)
	if !ok {
		return nil, false
	}
	return m.Labels, true
}

// NamespaceLabels implements query.OwnerLookup.
func (r *Resolver) NamespaceLabels(ns string) (map[string]string, bool) {
	m, ok := r.get(model.ObjectRef{Kind: "Namespace", Name: ns})
	if !ok {
		return nil, false
	}
	return m.Labels, true
}

var namespaceGK = schema.GroupKind{Kind: "Namespace"}

// legacyGroups maps deprecated API groups of watched kinds to today's, so a
// Deployment referenced as extensions/v1beta1 is the same owner as apps/v1.
var legacyGroups = map[schema.GroupKind]string{
	{Group: "extensions", Kind: "Deployment"}: "apps", {Group: "extensions", Kind: "ReplicaSet"}: "apps",
	{Group: "extensions", Kind: "DaemonSet"}: "apps",
}

func canonical(gk schema.GroupKind) schema.GroupKind {
	if g, ok := legacyGroups[gk]; ok {
		gk.Group = g
	}
	return gk
}

// clusterScoped are owner kinds without a namespace. ownerReferences never
// cross namespaces, so any other owner shares the child's namespace.
var clusterScoped = map[schema.GroupKind]bool{
	{Kind: "Node"}: true, {Kind: "Namespace"}: true, {Kind: "PersistentVolume"}: true,
	{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}: true,
	{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}:         true,
}

func ownerRef(o metav1.OwnerReference, ns string) model.ObjectRef {
	g, v := enginekit.ParseAPIVersion(o.APIVersion)
	gk := canonical(schema.GroupKind{Group: g, Kind: o.Kind})
	if gk.Group != g {
		v = "v1" // the canonical group's served version for these kinds
	}
	if clusterScoped[gk] {
		ns = ""
	}
	return model.ObjectRef{Group: gk.Group, Version: v, Kind: o.Kind, Namespace: ns, Name: o.Name}
}

// transform keeps only what ownership needs: name, namespace, labels and
// ownerReferences. Annotations and managedFields are the bulk of metadata.
func transform(obj any) (any, error) {
	m, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return obj, nil
	}
	m.ManagedFields = nil
	m.Annotations = nil
	m.UID = ""
	m.Finalizers = nil
	return m, nil
}

// Notice describes partial grouping caused by RBAC, for views.
func Notice(issues []model.AccessIssue) string {
	if len(issues) == 0 {
		return ""
	}
	var res []string
	for _, i := range issues {
		res = append(res, i.Resource)
	}
	return fmt.Sprintf("owners: cannot list %s cluster-wide — violations group under the deepest owner p9s can see", strings.Join(res, ", "))
}
