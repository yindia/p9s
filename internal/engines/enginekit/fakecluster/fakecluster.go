// Package fakecluster is a fake engine.Cluster for engine tests: a dynamic
// fake client plus a thread-safe fake discovery whose served resources can
// change while an engine runs.
package fakecluster

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/metadata"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

// Resource is a served API resource.
type Resource struct {
	GroupVersion string // "v1", "apps/v1", ...
	Name         string // plural, e.g. "clusterpolicies"
	Kind         string
	Namespaced   bool
}

// GVR returns the resource's GroupVersionResource.
func (r Resource) GVR() schema.GroupVersionResource {
	gv, _ := schema.ParseGroupVersion(r.GroupVersion)
	return gv.WithResource(r.Name)
}

// Cluster implements engine.Cluster, plus Metadata for owner resolution.
type Cluster struct {
	Dyn  *dynamicfake.FakeDynamicClient
	Disc *Discovery
	Meta *metadatafake.FakeMetadataClient
	// Home is the kubeconfig context namespace (engine.HomeNamespacer).
	Home string
}

// HomeNamespace implements engine.HomeNamespacer.
func (c *Cluster) HomeNamespace() string { return c.Home }

// Metadata returns the metadata-only fake (owner resolution).
func (c *Cluster) Metadata() metadata.Interface { return c.Meta }

// Owned describes one object for the metadata fake: who controls it and
// its labels. Owner is "Kind/name" in the same namespace, or "".
type Owned struct {
	GVR       schema.GroupVersionResource
	Kind      string
	Namespace string
	Name      string
	Owner     string // e.g. "ReplicaSet/worker-5c9d"
	OwnerAPI  string // e.g. "apps/v1"
	Labels    map[string]string
}

// SeedOwned stores metadata-only objects for owner resolution.
func (c *Cluster) SeedOwned(t testing.TB, objs ...Owned) {
	t.Helper()
	for _, o := range objs {
		m := &metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: o.GVR.GroupVersion().String(), Kind: o.Kind},
			ObjectMeta: metav1.ObjectMeta{Name: o.Name, Namespace: o.Namespace, Labels: o.Labels},
		}
		if kind, name, ok := strings.Cut(o.Owner, "/"); ok {
			yes := true
			m.OwnerReferences = []metav1.OwnerReference{{APIVersion: o.OwnerAPI, Kind: kind, Name: name, Controller: &yes}}
		}
		if err := c.Meta.Tracker().Create(o.GVR, m, o.Namespace); err != nil {
			t.Fatalf("seed metadata %s/%s: %v", o.Kind, o.Name, err)
		}
	}
}

// Dynamic implements engine.Cluster.
func (c *Cluster) Dynamic() dynamic.Interface { return c.Dyn }

// Discovery implements engine.Cluster.
func (c *Cluster) Discovery() discovery.DiscoveryInterface { return c.Disc }

// New builds a cluster serving resources, seeded with objs. Objects are
// stored under the GVR of the served resource matching their GVK, because
// the fake tracker's plural guess is wrong for many CRDs.
func New(t testing.TB, resources []Resource, objs ...*unstructured.Unstructured) *Cluster {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, r := range resources {
		listKinds[r.GVR()] = r.Kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	scheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(scheme)
	c := &Cluster{Dyn: dyn, Disc: &Discovery{FakeDiscovery: &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}}},
		Meta: metadatafake.NewSimpleMetadataClient(scheme)}
	for _, r := range resources {
		c.Disc.Add(r)
	}
	for _, o := range objs {
		c.Seed(t, resources, o)
	}
	return c
}

// Seed stores an object under its resource's GVR.
func (c *Cluster) Seed(t testing.TB, resources []Resource, o *unstructured.Unstructured) {
	t.Helper()
	for _, r := range resources {
		if r.GroupVersion == o.GetAPIVersion() && r.Kind == o.GetKind() {
			if err := c.Dyn.Tracker().Create(r.GVR(), o, o.GetNamespace()); err != nil {
				t.Fatalf("seed %s %s: %v", o.GetKind(), o.GetName(), err)
			}
			return
		}
	}
	t.Fatalf("seed %s %s: no served resource for %s", o.GetKind(), o.GetName(), o.GetAPIVersion())
}

// Forbid makes list and watch of a resource return 403.
func (c *Cluster) Forbid(resource string) {
	deny := func(a clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden(a.GetResource().GroupResource(), a.GetVerb())
	}
	c.Dyn.PrependReactor("list", resource, deny)
	c.Dyn.PrependWatchReactor(resource, func(a clienttesting.Action) (bool, watch.Interface, error) {
		return true, nil, forbidden(a.GetResource().GroupResource(), "watch")
	})
}

// ForbidClusterWide denies list and watch of a resource across all
// namespaces while namespaced requests still succeed (a tenant's RoleBinding).
func (c *Cluster) ForbidClusterWide(resource string) {
	c.Dyn.PrependReactor("list", resource, func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != "" {
			return false, nil, nil
		}
		return true, nil, forbidden(a.GetResource().GroupResource(), "list")
	})
	c.Dyn.PrependWatchReactor(resource, func(a clienttesting.Action) (bool, watch.Interface, error) {
		if a.GetNamespace() != "" {
			return false, nil, nil
		}
		return true, nil, forbidden(a.GetResource().GroupResource(), "watch")
	})
}

func forbidden(gr schema.GroupResource, verb string) error {
	return apierrors.NewForbidden(gr, "", fmt.Errorf(`user "viewer" cannot %s resource`, verb))
}

// Parse splits multi-document YAML into objects, decoding like the API
// server does so integers stay int64.
func Parse(b []byte) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for i, doc := range bytes.Split(b, []byte("\n---\n")) {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		js, err := yaml.YAMLToJSON(doc)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(js); err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		out = append(out, u)
	}
	return out, nil
}

// Discovery is a FakeDiscovery whose resources can change concurrently.
type Discovery struct {
	*discoveryfake.FakeDiscovery
	mu        sync.Mutex
	resources []*metav1.APIResourceList
}

// Add serves a resource (appending to its group/version).
func (d *Discovery) Add(r Resource) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res := metav1.APIResource{Name: r.Name, Kind: r.Kind, Namespaced: r.Namespaced, Verbs: []string{"get", "list", "watch"}}
	for _, rl := range d.resources {
		if rl.GroupVersion == r.GroupVersion {
			rl.APIResources = append(rl.APIResources, res)
			return
		}
	}
	d.resources = append(d.resources, &metav1.APIResourceList{GroupVersion: r.GroupVersion, APIResources: []metav1.APIResource{res}})
}

func (d *Discovery) snapshot() []*metav1.APIResourceList {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*metav1.APIResourceList, len(d.resources))
	for i, rl := range d.resources {
		cp := *rl
		cp.APIResources = append([]metav1.APIResource(nil), rl.APIResources...)
		out[i] = &cp
	}
	return out
}

// ServerGroups implements discovery.DiscoveryInterface. The first version
// registered for a group is its preferred version.
func (d *Discovery) ServerGroups() (*metav1.APIGroupList, error) {
	groups := map[string]*metav1.APIGroup{}
	var order []string
	for _, rl := range d.snapshot() {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil {
			return nil, err
		}
		g, ok := groups[gv.Group]
		if !ok {
			g = &metav1.APIGroup{Name: gv.Group}
			groups[gv.Group] = g
			order = append(order, gv.Group)
		}
		v := metav1.GroupVersionForDiscovery{GroupVersion: rl.GroupVersion, Version: gv.Version}
		g.Versions = append(g.Versions, v)
		if g.PreferredVersion.Version == "" {
			g.PreferredVersion = v
		}
	}
	list := &metav1.APIGroupList{}
	for _, n := range order {
		list.Groups = append(list.Groups, *groups[n])
	}
	return list, nil
}

// ServerResourcesForGroupVersion implements discovery.DiscoveryInterface.
func (d *Discovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	for _, rl := range d.snapshot() {
		if rl.GroupVersion == gv {
			return rl, nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: gv}, "")
}

// ResourcesFor builds Resource entries for a group/version from
// "name:Kind" (cluster-scoped) or "name:Kind:ns" (namespaced) specs.
func ResourcesFor(gv string, specs ...string) []Resource {
	out := make([]Resource, 0, len(specs))
	for _, s := range specs {
		parts := strings.Split(s, ":")
		out = append(out, Resource{GroupVersion: gv, Name: parts[0], Kind: parts[1], Namespaced: len(parts) > 2})
	}
	return out
}
