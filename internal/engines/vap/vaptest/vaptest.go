// Package vaptest builds fake clusters with ValidatingAdmissionPolicies,
// for engine, contract and UI golden tests.
package vaptest

import (
	"embed"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
)

//go:embed testdata/*.yaml
var fixtures embed.FS

// Options control the cluster.
type Options struct {
	// NoVAP builds a cluster that does not serve the API.
	NoVAP bool
	// NoReports seeds no PolicyReports (no audit source).
	NoReports bool
	// Forbidden resources are denied to list.
	Forbidden []string
}

// Resources is what a v1.30+ API server serves for VAP, plus PolicyReports.
var Resources = fakecluster.With(fakecluster.ResourcesFor("admissionregistration.k8s.io/v1",
	"validatingadmissionpolicies:ValidatingAdmissionPolicy", "validatingadmissionpolicybindings:ValidatingAdmissionPolicyBinding"),
	append(append(fakecluster.ResourcesFor("wgpolicyk8s.io/v1alpha2", "policyreports:PolicyReport:ns"),
		fakecluster.ResourcesFor("v1", "configmaps:ConfigMap:ns")...), fakecluster.PlatformResources...)...)

// NewCluster returns a seeded fake cluster.
func NewCluster(t testing.TB, opts Options) *fakecluster.Cluster {
	t.Helper()
	if opts.NoVAP {
		return fakecluster.New(t, fakecluster.ResourcesFor("apps/v1", "deployments:Deployment:ns"))
	}
	objs := Load(t, "policies.yaml")
	if !opts.NoReports {
		objs = append(objs, Load(t, "reports.yaml")...)
	}
	cl := fakecluster.New(t, Resources, objs...)
	for _, r := range opts.Forbidden {
		cl.Forbid(r)
	}
	return cl
}

// Load parses a fixture file.
func Load(t testing.TB, name string) []*unstructured.Unstructured {
	t.Helper()
	b, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := fakecluster.Parse(b)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return objs
}
