// Package gktest builds fake clusters with Gatekeeper installed, for tests
// of the gatekeeper engine, the contract suite and UI golden files.
package gktest

import (
	"embed"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
)

//go:embed testdata/*.yaml
var fixtures embed.FS

// GVRs used by the fixtures.
var (
	Templates   = schema.GroupVersionResource{Group: "templates.gatekeeper.sh", Version: "v1", Resource: "constrainttemplates"}
	Configs     = schema.GroupVersionResource{Group: "config.gatekeeper.sh", Version: "v1alpha1", Resource: "configs"}
	Deployments = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

// ConstraintGVR returns the GVR of a generated constraint kind.
func ConstraintGVR(kind string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "constraints.gatekeeper.sh", Version: "v1beta1", Resource: strings.ToLower(kind)}
}

// Options control which fixtures are loaded.
type Options struct {
	// NoGatekeeper builds a cluster without any Gatekeeper API group.
	NoGatekeeper bool
	// Forbidden lists resources (e.g. "configs") whose list/watch is denied.
	Forbidden []string
	// Extra objects to seed.
	Extra []*unstructured.Unstructured
	// ExtraResources are served too, e.g. another engine's CRDs so several
	// engines can share one fake cluster.
	ExtraResources []fakecluster.Resource
}

// Cluster is a fake engine.Cluster with Gatekeeper helpers.
type Cluster struct {
	*fakecluster.Cluster
	resources []fakecluster.Resource
}

// constraintKinds are the kinds the fixture templates generate.
var constraintKinds = []string{"K8sRequiredLabels", "K8sReplicaLimits"}

func constraintResource(kind string) fakecluster.Resource {
	return fakecluster.Resource{GroupVersion: "constraints.gatekeeper.sh/v1beta1", Name: strings.ToLower(kind), Kind: kind}
}

// NewCluster returns a fake cluster seeded with the fixtures.
func NewCluster(t testing.TB, opts Options) *Cluster {
	t.Helper()
	resources := append(
		fakecluster.ResourcesFor("v1", "namespaces:Namespace", "pods:Pod:ns"),
		fakecluster.ResourcesFor("apps/v1", "deployments:Deployment:ns")...)
	// The late kind is registered with the fake client (so it can be listed)
	// but only served by discovery once AddConstraintKind is called.
	late := constraintResource("K8sLateKind")
	var objs []*unstructured.Unstructured
	if !opts.NoGatekeeper {
		resources = append(resources, fakecluster.ResourcesFor("templates.gatekeeper.sh/v1", "constrainttemplates:ConstraintTemplate")...)
		resources = append(resources, fakecluster.ResourcesFor("config.gatekeeper.sh/v1alpha1", "configs:Config:ns")...)
		resources = append(resources, fakecluster.ResourcesFor("mutations.gatekeeper.sh/v1", "assign:Assign", "assignmetadata:AssignMetadata", "modifyset:ModifySet")...)
		resources = append(resources, fakecluster.ResourcesFor("mutations.gatekeeper.sh/v1alpha1", "assignimage:AssignImage")...)
		for _, k := range constraintKinds {
			resources = append(resources, constraintResource(k))
		}
		resources = fakecluster.With(resources, fakecluster.PlatformResources...)
		for _, f := range []string{"templates.yaml", "constraints.yaml", "config.yaml", "deployment.yaml", "platform.yaml", "events.yaml", "mutators.yaml"} {
			objs = append(objs, Load(t, f)...)
		}
	}
	resources = fakecluster.With(resources, opts.ExtraResources...)
	objs = append(objs, opts.Extra...)
	cl := fakecluster.New(t, append(resources, late), objs...)
	// Re-create discovery without the late kind.
	cl.Disc = &fakecluster.Discovery{FakeDiscovery: cl.Disc.FakeDiscovery}
	for _, r := range resources {
		cl.Disc.Add(r)
	}
	for _, res := range opts.Forbidden {
		cl.Forbid(res)
	}
	// Ownership for the violation targets: worker-7d9f belongs to
	// Deployment worker; kyverno fixtures' api-0 to StatefulSet api and
	// coredns to Deployment coredns (for clusters shared with kyvtest).
	owned := fakecluster.DeploymentPods("payments", "worker", "worker-5c9d", map[string]string{"team": "payments"}, "worker-7d9f")
	owned = append(owned, fakecluster.DeploymentPods("kube-system", "coredns", "coredns-6f6b679f8f", nil, "coredns-6f6b679f8f-x2v9q")...)
	owned = append(owned,
		fakecluster.Owned{GVR: fakecluster.StatefulSets, Kind: "StatefulSet", Namespace: "payments", Name: "api", Labels: map[string]string{"team": "checkout"}},
		fakecluster.Owned{GVR: fakecluster.Pods, Kind: "Pod", Namespace: "payments", Name: "api-0", Owner: "StatefulSet/api", OwnerAPI: "apps/v1"},
		fakecluster.Namespace("payments", map[string]string{"team": "payments"}),
		fakecluster.Namespace("default", nil), fakecluster.Namespace("kube-system", nil),
	)
	cl.SeedOwned(t, owned...)
	return &Cluster{Cluster: cl, resources: append(resources, late)}
}

// AddConstraintKind makes discovery serve a new constraint kind, as if a
// template was just created and Gatekeeper generated its CRD.
func (c *Cluster) AddConstraintKind(kind string) { c.Disc.Add(constraintResource(kind)) }

// Load parses a multi-document fixture file.
func Load(t testing.TB, name string) []*unstructured.Unstructured {
	t.Helper()
	b, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	objs, err := fakecluster.Parse(b)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return objs
}
