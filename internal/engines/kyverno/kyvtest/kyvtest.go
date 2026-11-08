// Package kyvtest builds fake clusters with Kyverno installed, for engine,
// contract and UI golden tests.
package kyvtest

import (
	"embed"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
)

//go:embed testdata/*.yaml
var fixtures embed.FS

// Options control which fixtures are loaded.
type Options struct {
	// NoKyverno builds a cluster without Kyverno's API groups.
	NoKyverno bool
	// NoReports serves the report API but seeds no reports.
	NoReports bool
	// Forbidden lists resources (e.g. "policyreports") whose list is denied.
	Forbidden []string
	// ClusterWideForbidden are readable only per namespace (a tenant).
	ClusterWideForbidden []string
	// Home is the context namespace.
	Home string
}

// Resources mirror what Kyverno 1.19 serves. kyverno.io lists v1 first, so
// it is preferred, yet policyexceptions exist only in v2.
var Resources = fakecluster.With(concat(
	fakecluster.ResourcesFor("apps/v1", "deployments:Deployment:ns"),
	fakecluster.ResourcesFor("kyverno.io/v1", "clusterpolicies:ClusterPolicy", "policies:Policy:ns"),
	fakecluster.ResourcesFor("kyverno.io/v2", "policyexceptions:PolicyException:ns", "updaterequests:UpdateRequest:ns"),
	fakecluster.ResourcesFor("policies.kyverno.io/v1",
		"validatingpolicies:ValidatingPolicy", "namespacedvalidatingpolicies:NamespacedValidatingPolicy:ns",
		"mutatingpolicies:MutatingPolicy", "policyexceptions:PolicyException:ns"),
	fakecluster.ResourcesFor("wgpolicyk8s.io/v1alpha2", "policyreports:PolicyReport:ns", "clusterpolicyreports:ClusterPolicyReport"),
), fakecluster.PlatformResources...)

// NewCluster returns a fake cluster seeded with the fixtures.
func NewCluster(t testing.TB, opts Options) *fakecluster.Cluster {
	t.Helper()
	if opts.NoKyverno {
		return fakecluster.New(t, Resources[:1])
	}
	objs := append(append(Load(t, "policies.yaml"), Load(t, "platform.yaml")...), Load(t, "events.yaml")...)
	for _, o := range Load(t, "reports.yaml") {
		if opts.NoReports && o.GetKind() != "Deployment" {
			continue
		}
		objs = append(objs, o)
	}
	cl := fakecluster.New(t, Resources, objs...)
	for _, r := range opts.Forbidden {
		cl.Forbid(r)
	}
	for _, r := range opts.ClusterWideForbidden {
		cl.ForbidClusterWide(r)
	}
	cl.Home = opts.Home
	return cl
}

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

func concat(lists ...[]fakecluster.Resource) []fakecluster.Resource {
	var out []fakecluster.Resource
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}
