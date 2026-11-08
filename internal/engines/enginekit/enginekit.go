// Package enginekit holds plumbing shared by engine implementations: RBAC
// probing and access tracking, API discovery, informer snapshots, and the
// small rendering helpers detail pages use. It is engine-neutral.
package enginekit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/yindia/p9s/internal/model"
)

// Access records resources RBAC denied, keyed by group-resource.
type Access struct {
	engine string
	mu     sync.RWMutex
	issues map[string]model.AccessIssue
	// home is the namespace to fall back to when a cluster-wide list is
	// denied; "" disables the fallback.
	home string
}

// SetHomeNamespace enables the namespace fallback (see SharedInformer).
func (a *Access) SetHomeNamespace(ns string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.home = ns
}

// HomeNamespace returns the fallback namespace.
func (a *Access) HomeNamespace() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.home
}

// scopedKey is where a cluster-wide denial is kept once the home namespace
// is read instead: a notice, not a denied resource (Denied is false).
func scopedKey(gr string) string { return gr + "@home" }

// Scoped reports whether gr is read in the home namespace only.
func (a *Access) Scoped(gr string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.issues[scopedKey(gr)]
	return ok
}

// CanListIn reports whether gvr can be listed in namespace ns.
func (a *Access) CanListIn(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, ns string) bool {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	_, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1})
	return err == nil
}

// ScopeToHome turns a recorded cluster-wide denial of gvr into a "home
// namespace only" notice. Callers first check the namespace is readable.
func (a *Access) ScopeToHome(gvr schema.GroupVersionResource) {
	gr := gvr.GroupResource().String()
	a.mu.Lock()
	defer a.mu.Unlock()
	issue, ok := a.issues[gr]
	if !ok {
		issue = model.AccessIssue{Engine: a.engine, Resource: gr, Verb: "list", Message: "forbidden"}
	}
	delete(a.issues, gr)
	issue.OnlyNamespace = a.home
	a.issues[scopedKey(gr)] = issue
}

// fallback scopes a cluster-wide denial to the home namespace when that is
// readable, and reports whether it is.
func (a *Access) fallback(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource) bool {
	home := a.HomeNamespace()
	if home == "" || !a.CanListIn(ctx, dyn, gvr, home) {
		return false
	}
	a.ScopeToHome(gvr)
	return true
}

// NewAccess returns an empty tracker for an engine.
func NewAccess(engineName string) *Access {
	return &Access{engine: engineName, issues: map[string]model.AccessIssue{}}
}

// Record stores a denial. The API server's message names the user and verb,
// so it is kept verbatim.
func (a *Access) Record(gvr schema.GroupVersionResource, verb string, err error) {
	a.RecordAs(gvr.GroupResource().String(), verb, err)
}

// RecordAs stores a denial under an explicit key (e.g. "pods@kyverno" for
// namespaced watches), so denials in different namespaces stay apart.
func (a *Access) RecordAs(res, verb string, err error) {
	msg := "forbidden"
	if err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.issues[res] = model.AccessIssue{Engine: a.engine, Resource: res, Verb: verb, Message: msg}
}

// Issues returns the denials sorted by resource.
func (a *Access) Issues() []model.AccessIssue {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]model.AccessIssue, 0, len(a.issues))
	for _, i := range a.issues {
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}

// ProbeTimeout bounds each RBAC probe so a hung API server cannot block Start.
const ProbeTimeout = 10 * time.Second

// Denied reports whether a group-resource has a recorded denial.
func (a *Access) Denied(groupResource string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.issues[groupResource]
	return ok
}

// Clear forgets a denial, e.g. after a later probe succeeded.
func (a *Access) Clear(gvr schema.GroupVersionResource) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.issues, gvr.GroupResource().String())
	delete(a.issues, scopedKey(gvr.GroupResource().String()))
}

// IsDenied reports RBAC or authentication denials.
func IsDenied(err error) bool { return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) }

// CanList probes list access with limit=1 and records a denial (or clears a
// stale one). Other errors are not access problems; the informer will
// surface and retry them.
func (a *Access) CanList(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource) bool {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	_, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	if IsDenied(err) {
		a.Record(gvr, "list", err)
		return false
	}
	a.Clear(gvr)
	return true
}

// WatchErrorHandler records watch denials and defers everything else to
// client-go's default handler (which backs off and retries).
func (a *Access) WatchErrorHandler(gvr schema.GroupVersionResource, notify func()) cache.WatchErrorHandlerWithContext {
	return a.WatchErrorHandlerAs(gvr.GroupResource().String(), notify)
}

// WatchErrorHandlerAs is WatchErrorHandler recording under an explicit key.
func (a *Access) WatchErrorHandlerAs(key string, notify func()) cache.WatchErrorHandlerWithContext {
	return func(ctx context.Context, r *cache.Reflector, err error) {
		if IsDenied(err) {
			a.RecordAs(key, "watch", err)
			notify()
			return
		}
		cache.DefaultWatchErrorHandler(ctx, r, err)
	}
}

// SharedInformer registers an informer for gvr on the shared factory after
// an RBAC probe, wiring onChange to every event. It returns nil when denied.
func (a *Access) SharedInformer(ctx context.Context, dyn dynamic.Interface, f dynamicinformer.DynamicSharedInformerFactory,
	gvr schema.GroupVersionResource, notify func(), onChange func()) cache.SharedIndexInformer {
	if !a.CanList(ctx, dyn, gvr) {
		if !a.fallback(ctx, dyn, gvr) {
			return nil
		}
		// A tenant: watch the home namespace with an informer of its own
		// (the shared factory is cluster-wide), stopped with ctx.
		inf := dynamicinformer.NewFilteredDynamicInformer(dyn, gvr, a.HomeNamespace(), 0, cache.Indexers{}, nil).Informer()
		SetTransform(inf, gvr)
		// A later denial of the home watch is a full denial: recorded under
		// gr so it stops blocking sync (Sync skips Denied resources).
		_ = inf.SetWatchErrorHandlerWithContext(a.WatchErrorHandler(gvr, notify))
		_, _ = inf.AddEventHandler(Handler(onChange))
		go inf.RunWithContext(ctx)
		return inf
	}
	inf := f.ForResource(gvr).Informer()
	SetTransform(inf, gvr)
	_ = inf.SetWatchErrorHandlerWithContext(a.WatchErrorHandler(gvr, notify))
	_, _ = inf.AddEventHandler(Handler(onChange))
	return inf
}

// SetTransform installs Transform. It fails only if the informer already
// runs (e.g. a shared informer configured by an earlier caller); the cache
// then keeps full objects, which costs memory but is still correct.
func SetTransform(inf cache.SharedIndexInformer, gvr schema.GroupVersionResource) {
	if err := inf.SetTransform(Transform); err != nil {
		klog.V(2).InfoS("cache transform not installed", "resource", gvr.String(), "err", err)
	}
}

// Handler calls fn on every add, update and delete.
func Handler(fn func()) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { fn() },
		UpdateFunc: func(any, any) { fn() },
		DeleteFunc: func(any) { fn() },
	}
}

// PreferredVersions returns the preferred served version for each group that
// exists. Missing groups are absent from the result.
func PreferredVersions(d discovery.DiscoveryInterface, groups ...string) (map[string]string, error) {
	list, err := d.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("list API groups: %w", err)
	}
	want := map[string]bool{}
	for _, g := range groups {
		want[g] = true
	}
	out := map[string]string{}
	for _, g := range list.Groups {
		if !want[g.Name] {
			continue
		}
		v := g.PreferredVersion.Version
		if v == "" && len(g.Versions) > 0 {
			v = g.Versions[0].Version
		}
		if v != "" {
			out[g.Name] = v
		}
	}
	return out, nil
}

// ServedResources maps resource name to kind for a group/version, skipping
// subresources. A group/version that is not served yields an empty map.
func ServedResources(d discovery.DiscoveryInterface, gv string) (map[string]string, error) {
	list, err := d.ServerResourcesForGroupVersion(gv)
	if apierrors.IsNotFound(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", gv, err)
	}
	out := map[string]string{}
	for _, r := range list.APIResources {
		if !strings.Contains(r.Name, "/") {
			out[r.Name] = r.Kind
		}
	}
	return out, nil
}

// Objects returns deep copies of an informer's cache; nil-safe.
func Objects(inf cache.SharedIndexInformer) []*unstructured.Unstructured {
	if inf == nil {
		return nil
	}
	items := inf.GetStore().List()
	out := make([]*unstructured.Unstructured, 0, len(items))
	for _, it := range items {
		if u, ok := it.(*unstructured.Unstructured); ok {
			out = append(out, u.DeepCopy())
		}
	}
	return out
}

// SortObjects orders by kind, namespace, then name.
func SortObjects(objs []*unstructured.Unstructured) {
	sort.Slice(objs, func(i, j int) bool {
		a, b := objs[i], objs[j]
		if a.GetKind() != b.GetKind() {
			return a.GetKind() < b.GetKind()
		}
		if a.GetNamespace() != b.GetNamespace() {
			return a.GetNamespace() < b.GetNamespace()
		}
		return a.GetName() < b.GetName()
	})
}

// Ref builds an ObjectRef from an object.
func Ref(o *unstructured.Unstructured) model.ObjectRef {
	gvk := o.GroupVersionKind()
	return model.ObjectRef{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind, Namespace: o.GetNamespace(), Name: o.GetName()}
}

// ImageTag returns the tag of an image reference ("" when untagged).
func ImageTag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return ""
	}
	return image[colon+1:]
}

// ContainerImageTag returns the tag of the first container in a pod template
// whose image contains match.
func ContainerImageTag(deploy *unstructured.Unstructured, match string) string {
	containers, _, _ := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		image, _ := m["image"].(string)
		if strings.Contains(image, match) {
			if v := ImageTag(image); v != "" {
				return v
			}
		}
	}
	return ""
}

// ContainerArg returns the value of a flag (e.g. "--audit-interval") on any
// container in a pod template, in either "--flag=value" or "--flag value"
// form. command and args are both searched.
func ContainerArg(deploy *unstructured.Unstructured, flag string) (string, bool) {
	flag = strings.TrimSuffix(flag, "=")
	containers, _, _ := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		m, _ := c.(map[string]any)
		cmd, _, _ := unstructured.NestedStringSlice(m, "command")
		args, _, _ := unstructured.NestedStringSlice(m, "args")
		all := make([]string, 0, len(cmd)+len(args))
		all = append(append(all, cmd...), args...)
		for i, a := range all {
			if v, ok := strings.CutPrefix(a, flag+"="); ok {
				return v, true
			}
			if a == flag && i+1 < len(all) {
				return all[i+1], true
			}
		}
	}
	return "", false
}

// Int reads an integer field that may be int64 (API decoding) or float64
// (plain JSON decoding).
func Int(obj map[string]any, path ...string) int {
	v, ok, _ := unstructured.NestedFieldNoCopy(obj, path...)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// Str reads a string field from a map, "" when absent.
func Str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// Slice reads a nested slice, nil when absent.
func Slice(obj map[string]any, path ...string) []any {
	s, _, _ := unstructured.NestedSlice(obj, path...)
	return s
}

// YAML renders a value as YAML for detail pages.
func YAML(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<cannot render: %v>", err)
	}
	return strings.TrimRight(string(b), "\n")
}

// NestedYAML renders a nested field, or empty when absent.
func NestedYAML(o *unstructured.Unstructured, empty string, path ...string) string {
	v, ok, _ := unstructured.NestedFieldNoCopy(o.Object, path...)
	if !ok || v == nil {
		return empty
	}
	return YAML(v)
}

// KV renders aligned "key: value" lines from alternating pairs.
func KV(pairs ...string) string {
	width := 0
	for i := 0; i < len(pairs); i += 2 {
		width = max(width, len(pairs[i]))
	}
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(&b, "%-*s  %s\n", width+1, pairs[i]+":", pairs[i+1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// YesNo renders a bool for detail pages.
func YesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// StrongerMode returns the stricter of two enforcement modes.
func StrongerMode(a, b model.EnforcementMode) model.EnforcementMode {
	rank := map[model.EnforcementMode]int{model.ModeUnknown: 0, model.ModeAudit: 1, model.ModeWarn: 2, model.ModeEnforce: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Resource is a served API resource.
type Resource struct {
	GVR        schema.GroupVersionResource
	Kind       string
	Namespaced bool
}

// GroupResources returns every resource a group serves, keyed by resource
// name. When several versions serve the same resource the preferred version
// wins; other versions fill in resources only they serve (e.g. kyverno.io
// serves policies in v1 but policyexceptions only in v2).
func GroupResources(d discovery.DiscoveryInterface, group string) (map[string]Resource, error) {
	list, err := d.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("list API groups: %w", err)
	}
	out := map[string]Resource{}
	for _, g := range list.Groups {
		if g.Name != group {
			continue
		}
		versions := []string{g.PreferredVersion.Version}
		for _, v := range g.Versions {
			if v.Version != g.PreferredVersion.Version {
				versions = append(versions, v.Version)
			}
		}
		for _, v := range versions {
			if v == "" {
				continue
			}
			rl, err := d.ServerResourcesForGroupVersion(group + "/" + v)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("discover %s/%s: %w", group, v, err)
			}
			for _, r := range rl.APIResources {
				if strings.Contains(r.Name, "/") {
					continue
				}
				if _, seen := out[r.Name]; !seen {
					out[r.Name] = Resource{
						GVR:  schema.GroupVersionResource{Group: group, Version: v, Resource: r.Name},
						Kind: r.Kind, Namespaced: r.Namespaced,
					}
				}
			}
		}
	}
	return out, nil
}

// ParseAPIVersion splits "group/version" (or "v1") into its parts.
func ParseAPIVersion(apiVersion string) (group, version string) {
	if g, v, ok := strings.Cut(apiVersion, "/"); ok {
		return g, v
	}
	return "", apiVersion
}

// EventTime is when a Kubernetes Event last happened: lastTimestamp, then
// eventTime, then series.lastObservedTime, then creation.
func EventTime(ev *unstructured.Unstructured) time.Time {
	// Latest occurrence first: core lastTimestamp, then the events.k8s.io
	// series, then eventTime (the first occurrence of a series).
	for _, path := range [][]string{{"lastTimestamp"}, {"series", "lastObservedTime"}, {"eventTime"}} {
		if s, ok, _ := unstructured.NestedString(ev.Object, path...); ok && s != "" {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
				if t, err := time.Parse(layout, s); err == nil && !t.IsZero() {
					return t
				}
			}
		}
	}
	return ev.GetCreationTimestamp().Time
}

// EventCount is how often an Event happened: core count, else the
// events.k8s.io series count, else 1.
func EventCount(ev *unstructured.Unstructured) int {
	if n := Int(ev.Object, "count"); n > 0 {
		return n
	}
	if n := Int(ev.Object, "series", "count"); n > 0 {
		return n
	}
	return 1
}
