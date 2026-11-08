package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/metadata"

	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/session"
)

// Recorder collects answers from wrapped clients.
type Recorder struct {
	mu        sync.Mutex
	objects   map[schema.GroupVersionResource]map[string]*unstructured.Unstructured
	meta      map[schema.GroupVersionResource]map[string]metav1.PartialObjectMetadata
	denied    map[string]Denial
	groups    *metav1.APIGroupList
	resources map[string]*metav1.APIResourceList
	// touched are all resources asked about, even when empty or denied, so
	// replay can register them; disc resolves their kinds at File time.
	touched map[schema.GroupVersionResource]bool
	disc    discovery.DiscoveryInterface
	// gone are tombstones (resourceVersion of the delete), so a late list
	// answer cannot bring a deleted object back.
	gone map[string]string
	// frozen stops recording once File was taken; informers keep running.
	frozen   bool
	warnings []string
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder {
	return &Recorder{
		objects:   map[schema.GroupVersionResource]map[string]*unstructured.Unstructured{},
		meta:      map[schema.GroupVersionResource]map[string]metav1.PartialObjectMetadata{},
		denied:    map[string]Denial{},
		resources: map[string]*metav1.APIResourceList{},
		touched:   map[schema.GroupVersionResource]bool{},
		gone:      map[string]string{},
	}
}

// Warnings are problems that made the snapshot less complete.
func (r *Recorder) Warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.warnings...)
}

// newer reports whether resourceVersion a is newer than b. Versions are
// opaque but numeric on every API server in practice; non-numeric ones
// fall back to "the latest answer wins".
func newer(a, b string) bool {
	x, errA := strconv.ParseUint(a, 10, 64)
	y, errB := strconv.ParseUint(b, 10, 64)
	if errA != nil || errB != nil {
		return true
	}
	return x >= y
}

func tombKey(gvr schema.GroupVersionResource, isMeta bool, k string) string {
	return gvr.String() + "|" + strconv.FormatBool(isMeta) + "|" + k
}

func (r *Recorder) touch(gvr schema.GroupVersionResource) {
	r.mu.Lock()
	r.touched[gvr] = true
	r.mu.Unlock()
}

// preferred records the cluster's preferred resources (best effort: a
// partial answer is kept, a failure is a warning).
func (r *Recorder) preferred() ([]*metav1.APIResourceList, string) {
	r.mu.Lock()
	disc := r.disc
	r.mu.Unlock()
	if disc == nil {
		return nil, "no discovery client"
	}
	lists, err := disc.ServerPreferredResources()
	if err != nil {
		r.mu.Lock()
		r.warnings = append(r.warnings, fmt.Sprintf("preferred discovery: %v (reports from this snapshot may miss kinds)", err))
		r.mu.Unlock()
		return lists, err.Error()
	}
	return lists, ""
}

// resolveTouched fetches discovery for group-versions read by fixed GVR
// (core and apps kinds) that engines never asked discovery about.
func (r *Recorder) resolveTouched() {
	r.mu.Lock()
	disc := r.disc
	var missing []string
	seen := map[string]bool{}
	for gvr := range r.touched {
		gv := gvr.GroupVersion().String()
		if _, ok := r.resources[gv]; !ok && !seen[gv] {
			seen[gv] = true
			missing = append(missing, gv)
		}
	}
	r.mu.Unlock()
	if disc == nil {
		return
	}
	for _, gv := range missing {
		l, err := disc.ServerResourcesForGroupVersion(gv)
		r.mu.Lock()
		if err != nil {
			r.warnings = append(r.warnings, fmt.Sprintf("discovery for %s failed (%v): its resources may not replay", gv, err))
		} else {
			r.resources[gv] = l
		}
		r.mu.Unlock()
	}
}

// lastApplied is kubectl's full copy of an object; never recorded.
const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

func objKey(ns, name string) string { return ns + "/" + name }

func (r *Recorder) addObject(gvr schema.GroupVersionResource, o *unstructured.Unstructured) {
	o = sanitize(o)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return
	}
	k := objKey(o.GetNamespace(), o.GetName())
	if rv, ok := r.gone[tombKey(gvr, false, k)]; ok && !newer(o.GetResourceVersion(), rv) {
		return // deleted after this answer was produced
	}
	m := r.objects[gvr]
	if m == nil {
		m = map[string]*unstructured.Unstructured{}
		r.objects[gvr] = m
	}
	if prev, ok := m[k]; ok && !newer(o.GetResourceVersion(), prev.GetResourceVersion()) {
		return // an older answer arriving late (list racing a watch)
	}
	m[k] = o
	delete(r.gone, tombKey(gvr, false, k))
}

func (r *Recorder) addMeta(gvr schema.GroupVersionResource, o metav1.PartialObjectMetadata) {
	o.ManagedFields = nil
	if _, ok := o.Annotations[lastApplied]; ok {
		ann := make(map[string]string, len(o.Annotations))
		for k, v := range o.Annotations {
			if k != lastApplied {
				ann[k] = v
			}
		}
		o.Annotations = ann
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return
	}
	k := objKey(o.Namespace, o.Name)
	if rv, ok := r.gone[tombKey(gvr, true, k)]; ok && !newer(o.ResourceVersion, rv) {
		return
	}
	m := r.meta[gvr]
	if m == nil {
		m = map[string]metav1.PartialObjectMetadata{}
		r.meta[gvr] = m
	}
	if prev, ok := m[k]; ok && !newer(o.ResourceVersion, prev.ResourceVersion) {
		return
	}
	m[k] = o
	delete(r.gone, tombKey(gvr, true, k))
}

func (r *Recorder) deny(gvr schema.GroupVersionResource, ns string, isMeta bool, err error) {
	if !apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err) {
		return // transient errors are not part of the cluster's state
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return
	}
	d := Denial{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Namespace: ns, Metadata: isMeta, Message: err.Error()}
	r.denied[denialKey(d)] = d
}

// allow forgets a denial once the same scope answered (RBAC propagated, or
// a retry succeeded): the latest answer is the cluster's state.
func (r *Recorder) allow(gvr schema.GroupVersionResource, ns string, isMeta bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return
	}
	delete(r.denied, denialKey(Denial{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Namespace: ns, Metadata: isMeta}))
}

func denialKey(d Denial) string {
	return d.Group + "/" + d.Version + "/" + d.Resource + "@" + d.Namespace + "|" + map[bool]string{true: "meta"}[d.Metadata]
}

// sanitize drops what a snapshot must not carry: managedFields, kubectl's
// last-applied copy and any Secret data.
func sanitize(o *unstructured.Unstructured) *unstructured.Unstructured {
	o = o.DeepCopy()
	o.SetManagedFields(nil)
	if ann := o.GetAnnotations(); ann != nil {
		delete(ann, lastApplied)
		o.SetAnnotations(ann)
	}
	switch o.GetKind() {
	case "Secret":
		unstructured.RemoveNestedField(o.Object, "data")
		unstructured.RemoveNestedField(o.Object, "stringData")
	case "ConfigMap":
		unstructured.RemoveNestedField(o.Object, "data")
		unstructured.RemoveNestedField(o.Object, "binaryData")
	}
	redactEnv(o.Object)
	return o
}

// podSpecPaths are where pod specs live in the workloads p9s reads.
var podSpecPaths = [][]string{
	{"spec"}, {"spec", "template", "spec"}, {"spec", "jobTemplate", "spec", "template", "spec"},
}

// redactEnv blanks container env values: they routinely carry tokens.
// Names stay, and so do args (engines read their flags from args).
func redactEnv(obj map[string]any) {
	for _, p := range podSpecPaths {
		spec, ok, _ := unstructured.NestedMap(obj, p...)
		if !ok {
			continue
		}
		changed := false
		for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
			cs, ok := spec[field].([]any)
			if !ok {
				continue
			}
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				env, _ := cm["env"].([]any)
				for _, e := range env {
					em, _ := e.(map[string]any)
					if _, has := em["value"]; has {
						em["value"] = redacted
						changed = true
					}
				}
			}
		}
		if changed {
			_ = unstructured.SetNestedMap(obj, spec, p...)
		}
	}
}

// redacted replaces values a snapshot must not carry.
const redacted = "<redacted by p9s snapshot>"

// File returns the recording.
func (r *Recorder) File(takenAt time.Time, version string, ctx model.KubeContext) *File {
	r.resolveTouched()
	preferred, perr := r.preferred()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen = true // informers keep running; the snapshot is this moment
	f := &File{SchemaVersion: SchemaVersion, TakenAt: takenAt.UTC(), P9sVersion: version, Context: ctx, Groups: r.groups,
		Preferred: preferred, PreferredError: perr}
	gvs := make([]string, 0, len(r.resources))
	for gv := range r.resources {
		gvs = append(gvs, gv)
	}
	sort.Strings(gvs)
	for _, gv := range gvs {
		f.Resources = append(f.Resources, r.resources[gv])
	}
	for gvr, m := range r.objects {
		o := Objects{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource}
		for _, k := range sortedKeys(m) {
			o.Items = append(o.Items, m[k])
		}
		f.Objects = append(f.Objects, o)
	}
	sort.Slice(f.Objects, func(i, j int) bool {
		return gvrString(f.Objects[i].Group, f.Objects[i].Version, f.Objects[i].Resource) < gvrString(f.Objects[j].Group, f.Objects[j].Version, f.Objects[j].Resource)
	})
	for gvr, m := range r.meta {
		o := MetadataObjects{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource}
		for _, k := range sortedKeys(m) {
			o.Items = append(o.Items, m[k])
		}
		f.Metadata = append(f.Metadata, o)
	}
	sort.Slice(f.Metadata, func(i, j int) bool {
		return gvrString(f.Metadata[i].Group, f.Metadata[i].Version, f.Metadata[i].Resource) < gvrString(f.Metadata[j].Group, f.Metadata[j].Version, f.Metadata[j].Resource)
	})
	for _, k := range sortedKeys(r.denied) {
		f.Denied = append(f.Denied, r.denied[k])
	}
	return f
}

func gvrString(g, v, r string) string { return g + "/" + v + "/" + r }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Connector wraps c so every cluster it connects records into r.
func (r *Recorder) Connector(c session.Connector) session.Connector { return recConnector{c, r} }

type recConnector struct {
	session.Connector
	r *Recorder
}

func (c recConnector) Connect(name string) (session.Cluster, error) {
	cl, err := c.Connector.Connect(name)
	if err != nil {
		return nil, err
	}
	c.r.mu.Lock()
	c.r.disc = cl.Discovery()
	c.r.mu.Unlock()
	rc := &recCluster{Cluster: cl, dyn: recDynamic{cl.Dynamic(), c.r}, disc: recDiscovery{cl.Discovery(), c.r}}
	if m, ok := cl.(interface{ Metadata() metadata.Interface }); ok && m.Metadata() != nil {
		rc.meta = recMetadata{m.Metadata(), c.r}
	}
	return rc, nil
}

// recCluster serves recording clients in place of the real ones.
type recCluster struct {
	session.Cluster
	dyn  dynamic.Interface
	disc discovery.DiscoveryInterface
	meta metadata.Interface
}

func (c *recCluster) Dynamic() dynamic.Interface              { return c.dyn }
func (c *recCluster) Discovery() discovery.DiscoveryInterface { return c.disc }
func (c *recCluster) HomeNamespace() string                   { return homeOf(c.Cluster) }
func (c *recCluster) NewInformerFactory() dynamicinformer.DynamicSharedInformerFactory {
	return dynamicinformer.NewDynamicSharedInformerFactory(c.dyn, 0)
}

// Metadata is nil-safe for clusters without a metadata client.
func (c *recCluster) Metadata() metadata.Interface {
	if c.meta == nil {
		return nil
	}
	return c.meta
}

func homeOf(c any) string {
	if h, ok := c.(interface{ HomeNamespace() string }); ok {
		return h.HomeNamespace()
	}
	return ""
}

// --- dynamic -----------------------------------------------------------

type recDynamic struct {
	dynamic.Interface
	r *Recorder
}

func (d recDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	d.r.touch(gvr)
	return recResource{NamespaceableResourceInterface: d.Interface.Resource(gvr), gvr: gvr, r: d.r}
}

type recResource struct {
	dynamic.NamespaceableResourceInterface
	gvr schema.GroupVersionResource
	r   *Recorder
}

func (x recResource) Namespace(ns string) dynamic.ResourceInterface {
	return recNamespaced{ResourceInterface: x.NamespaceableResourceInterface.Namespace(ns), gvr: x.gvr, ns: ns, r: x.r}
}

func (x recResource) List(ctx context.Context, o metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	l, err := x.NamespaceableResourceInterface.List(ctx, o)
	x.r.recordList(x.gvr, "", l, err)
	return l, err
}

func (x recResource) Get(ctx context.Context, name string, o metav1.GetOptions, sub ...string) (*unstructured.Unstructured, error) {
	u, err := x.NamespaceableResourceInterface.Get(ctx, name, o, sub...)
	if err == nil && len(sub) == 0 {
		x.r.addObject(x.gvr, u)
	}
	return u, err
}

type recNamespaced struct {
	dynamic.ResourceInterface
	gvr schema.GroupVersionResource
	ns  string
	r   *Recorder
}

func (x recNamespaced) List(ctx context.Context, o metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	l, err := x.ResourceInterface.List(ctx, o)
	x.r.recordList(x.gvr, x.ns, l, err)
	return l, err
}

func (x recNamespaced) Get(ctx context.Context, name string, o metav1.GetOptions, sub ...string) (*unstructured.Unstructured, error) {
	u, err := x.ResourceInterface.Get(ctx, name, o, sub...)
	if err == nil && len(sub) == 0 {
		x.r.addObject(x.gvr, u)
	}
	return u, err
}

func (r *Recorder) recordList(gvr schema.GroupVersionResource, ns string, l *unstructured.UnstructuredList, err error) {
	if err != nil {
		r.deny(gvr, ns, false, err)
		return
	}
	r.allow(gvr, ns, false)
	for i := range l.Items {
		r.addObject(gvr, &l.Items[i])
	}
}

// --- metadata ----------------------------------------------------------

type recMetadata struct {
	metadata.Interface
	r *Recorder
}

func (m recMetadata) Resource(gvr schema.GroupVersionResource) metadata.Getter {
	m.r.touch(gvr)
	return recMetaResource{Getter: m.Interface.Resource(gvr), gvr: gvr, r: m.r}
}

type recMetaResource struct {
	metadata.Getter
	gvr schema.GroupVersionResource
	r   *Recorder
}

func (x recMetaResource) Namespace(ns string) metadata.ResourceInterface {
	return recMetaNamespaced{ResourceInterface: x.Getter.Namespace(ns), gvr: x.gvr, ns: ns, r: x.r}
}

func (x recMetaResource) List(ctx context.Context, o metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	l, err := x.Getter.List(ctx, o)
	x.r.recordMetaList(x.gvr, "", l, err)
	return l, err
}

type recMetaNamespaced struct {
	metadata.ResourceInterface
	gvr schema.GroupVersionResource
	ns  string
	r   *Recorder
}

func (x recMetaNamespaced) List(ctx context.Context, o metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	l, err := x.ResourceInterface.List(ctx, o)
	x.r.recordMetaList(x.gvr, x.ns, l, err)
	return l, err
}

func (r *Recorder) recordMetaList(gvr schema.GroupVersionResource, ns string, l *metav1.PartialObjectMetadataList, err error) {
	if err != nil {
		r.deny(gvr, ns, true, err)
		return
	}
	r.allow(gvr, ns, true)
	for _, o := range l.Items {
		r.addMeta(gvr, o)
	}
}

// --- discovery ---------------------------------------------------------

type recDiscovery struct {
	discovery.DiscoveryInterface
	r *Recorder
}

func (d recDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	g, err := d.DiscoveryInterface.ServerGroups()
	if err == nil {
		d.r.mu.Lock()
		d.r.groups = g
		d.r.mu.Unlock()
	}
	return g, err
}

func (d recDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	l, err := d.DiscoveryInterface.ServerResourcesForGroupVersion(gv)
	if err == nil {
		d.r.mu.Lock()
		d.r.resources[gv] = l
		d.r.mu.Unlock()
	}
	return l, err
}

// --- watches -----------------------------------------------------------
//
// Informers in client-go v0.37 load their initial state through streaming
// watch-lists, not List, so watch events are recorded too.

func (x recResource) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	w, err := x.NamespaceableResourceInterface.Watch(ctx, o)
	if err != nil {
		x.r.deny(x.gvr, "", false, err)
		return nil, err
	}
	return x.r.tee(w, x.gvr, false), nil
}

func (x recNamespaced) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	w, err := x.ResourceInterface.Watch(ctx, o)
	if err != nil {
		x.r.deny(x.gvr, x.ns, false, err)
		return nil, err
	}
	return x.r.tee(w, x.gvr, false), nil
}

func (x recMetaResource) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	w, err := x.Getter.Watch(ctx, o)
	if err != nil {
		x.r.deny(x.gvr, "", true, err)
		return nil, err
	}
	return x.r.tee(w, x.gvr, true), nil
}

func (x recMetaNamespaced) Watch(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	w, err := x.ResourceInterface.Watch(ctx, o)
	if err != nil {
		x.r.deny(x.gvr, x.ns, true, err)
		return nil, err
	}
	return x.r.tee(w, x.gvr, true), nil
}

// teeWatch forwards events while recording the objects they carry.
type teeWatch struct {
	inner watch.Interface
	out   chan watch.Event
	done  chan struct{}
	once  sync.Once
}

func (r *Recorder) tee(w watch.Interface, gvr schema.GroupVersionResource, isMeta bool) watch.Interface {
	t := &teeWatch{inner: w, out: make(chan watch.Event), done: make(chan struct{})}
	go func() {
		defer close(t.out)
		for ev := range w.ResultChan() {
			r.recordEvent(gvr, isMeta, ev)
			select {
			case t.out <- ev:
			case <-t.done:
				return
			}
		}
	}()
	return t
}

func (t *teeWatch) ResultChan() <-chan watch.Event { return t.out }

func (t *teeWatch) Stop() {
	t.once.Do(func() { close(t.done) })
	t.inner.Stop()
}

func (r *Recorder) recordEvent(gvr schema.GroupVersionResource, isMeta bool, ev watch.Event) {
	switch ev.Type {
	case watch.Added, watch.Modified:
	case watch.Deleted:
		r.forget(gvr, isMeta, ev.Object)
		return
	default:
		return // bookmarks and errors carry no object state
	}
	if isMeta {
		if m, ok := ev.Object.(*metav1.PartialObjectMetadata); ok {
			r.addMeta(gvr, *m)
		}
		return
	}
	if u, ok := ev.Object.(*unstructured.Unstructured); ok {
		r.addObject(gvr, u)
	}
}

func (r *Recorder) forget(gvr schema.GroupVersionResource, isMeta bool, o runtime.Object) {
	acc, err := meta.Accessor(o)
	if err != nil {
		return
	}
	k := objKey(acc.GetNamespace(), acc.GetName())
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return
	}
	r.gone[tombKey(gvr, isMeta, k)] = acc.GetResourceVersion()
	if isMeta {
		delete(r.meta[gvr], k)
		return
	}
	delete(r.objects[gvr], k)
}

// watchListUnsupported forwards client-go's watch-list capability check
// (util/watchlist): a wrapper must not make a client that cannot stream
// watch-lists (e.g. a fake) look like one that can, or informers wait for
// a bookmark that never comes.
func watchListUnsupported(inner any) bool {
	u, ok := inner.(interface{ IsWatchListSemanticsUnSupported() bool })
	return ok && u.IsWatchListSemanticsUnSupported()
}

// IsWatchListSemanticsUnSupported implements client-go's capability check.
func (d recDynamic) IsWatchListSemanticsUnSupported() bool { return watchListUnsupported(d.Interface) }

// IsWatchListSemanticsUnSupported implements client-go's capability check.
func (m recMetadata) IsWatchListSemanticsUnSupported() bool { return watchListUnsupported(m.Interface) }
