package query_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
)

func kyvPolicy(name, category string, mode model.EnforcementMode) model.Policy {
	raw := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": name}}}
	if category != "" {
		raw.SetAnnotations(map[string]string{"policies.kyverno.io/category": category})
	}
	return model.Policy{Engine: "kyverno", Kind: "ValidatingPolicy", Name: name, Enforcement: model.Enforcement{Mode: mode}, Raw: raw}
}

func TestPSSCoverageFromMetadataOnly(t *testing.T) {
	ps := []model.Policy{
		kyvPolicy("disallow-host-path", "Pod Security Standards (Baseline)", model.ModeAudit),
		{Engine: "gatekeeper", Kind: "K8sPSPHostFilesystem", Name: "no-host-paths", Enforcement: model.Enforcement{Mode: model.ModeEnforce}},
		kyvPolicy("disallow-privileged-containers", "Pod Security Standards (Baseline)", model.ModeAudit),
		kyvPolicy("disallow-host-ports", "", model.ModeEnforce),            // same name, no PSS metadata: not claimed
		kyvPolicy("require-team-label", "Best Practices", model.ModeAudit), // not a PSS control
	}
	controls, uncat := query.PSSCoverage(ps)
	got := map[string]string{}
	for _, c := range controls {
		got[string(c.Level)+"/"+c.Name] = c.Status
	}
	want := map[string]string{
		"baseline/HostPath Volumes":      query.CoverageEnforced, // enforced by one engine, audited by the other
		"baseline/Privileged Containers": query.CoverageAudit,
		"baseline/Host Ports":            query.CoverageMissing,
		"restricted/Volume Types":        query.CoverageMissing,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if len(controls) != len(query.PSSControls) {
		t.Errorf("%d controls, want every PSS control listed", len(controls))
	}
	if len(uncat) != 2 || uncat[0].Name != "disallow-host-ports" || uncat[1].Name != "require-team-label" {
		t.Errorf("uncategorised = %v: policies without PSS metadata are never guessed", uncat)
	}
}

func TestUnprotectedKinds(t *testing.T) {
	kinds := []model.APIResource{
		{Kind: "Pod", Resource: "pods", Namespaced: true}, {Group: "apps", Kind: "Deployment", Resource: "deployments", Namespaced: true},
		{Kind: "ConfigMap", Resource: "configmaps", Namespaced: true}, {Kind: "Event", Resource: "events", Namespaced: true},
		{Group: "networking.k8s.io", Kind: "Ingress", Resource: "ingresses", Namespaced: true},
		{Group: "kyverno.io", Kind: "ClusterPolicy", Resource: "clusterpolicies"},
	}
	ps := []model.Policy{
		{Engine: "a", Name: "pods", MatchKnown: true, Matches: []model.MatchRule{{Group: "*", Kind: "Pod"}}},
		{Engine: "a", Name: "ingress", MatchKnown: true, Matches: []model.MatchRule{{Group: "networking.k8s.io", Resource: "ingresses"}}},
		{Engine: "a", Name: "any", MatchKnown: true, MatchesAll: true},
		{Engine: "a", Name: "opaque"},
	}
	out, unknown, all := query.Unprotected(ps, kinds)
	if len(out) != 2 || out[0].Kind != "Deployment" || !out[0].ViaPods || out[1].Kind != "ConfigMap" || out[1].ViaPods {
		t.Errorf("unprotected = %+v: Deployment (via Pods) and ConfigMap; Event and engine kinds skipped", out)
	}
	if len(unknown) != 1 || unknown[0].Name != "opaque" || len(all) != 1 || all[0].Name != "any" {
		t.Errorf("unknown %v all %v", unknown, all)
	}
}

func TestPSSDoesNotOverclaim(t *testing.T) {
	notReady := kyvPolicy("disallow-host-namespaces", "Pod Security Standards (Baseline)", model.ModeEnforce)
	_ = unstructured.SetNestedSlice(notReady.Raw.Object, []any{map[string]any{"type": "Ready", "status": "False"}}, "status", "conditions")
	namespaced := kyvPolicy("disallow-privileged-containers", "Pod Security Standards (Baseline)", model.ModeEnforce)
	namespaced.Namespace = "team-a"
	mutate := kyvPolicy("disallow-host-path", "Pod Security Standards (Baseline)", model.ModeMutate)
	users := model.Policy{Engine: "gatekeeper", Kind: "K8sPSPAllowedUsers", Name: "any-user", Enforcement: model.Enforcement{Mode: model.ModeEnforce},
		Raw: &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"parameters": map[string]any{"runAsUser": map[string]any{"rule": "RunAsAny"}}}}}}
	nonroot := model.Policy{Engine: "gatekeeper", Kind: "K8sPSPAllowedUsers", Name: "non-root", Enforcement: model.Enforcement{Mode: model.ModeEnforce},
		Raw: &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
			"parameters": map[string]any{"runAsUser": map[string]any{"rule": "MustRunAsNonRoot"}},
			"match":      map[string]any{"excludedNamespaces": []any{"kube-system"}}}}}}
	caps := model.Policy{Engine: "gatekeeper", Kind: "K8sPSPCapabilities", Name: "caps", Enforcement: model.Enforcement{Mode: model.ModeEnforce},
		Raw: &unstructured.Unstructured{Object: map[string]any{}}}
	controls, uncat := query.PSSCoverage([]model.Policy{notReady, namespaced, mutate, users, nonroot, caps})
	by := map[string]query.ControlCoverage{}
	for _, c := range controls {
		by[string(c.Level)+"/"+c.Name] = c
	}
	for _, k := range []string{"baseline/Host Namespaces", "baseline/Privileged Containers", "baseline/HostPath Volumes"} {
		if by[k].Status != query.CoverageMissing {
			t.Errorf("%s = %s: unready, namespaced and mutating policies cover nothing", k, by[k].Status)
		}
	}
	nr := by["restricted/Running as Non-root"]
	if nr.Status != query.CoverageEnforced || len(nr.Policies) != 1 || nr.Policies[0].Name != "non-root" ||
		len(nr.Caveats) != 1 || nr.Caveats[0] != query.CaveatScoped {
		t.Errorf("non-root = %+v: only MustRunAsNonRoot counts, and its exclusions are a caveat", nr)
	}
	if c := by["baseline/Capabilities"]; c.Status != query.CoverageEnforced || len(c.Caveats) != 1 || c.Caveats[0] != query.CaveatParameters {
		t.Errorf("capabilities = %+v: parameter-dependent", c)
	}
	if len(uncat) != 4 {
		t.Errorf("uncategorised = %v", uncat)
	}
}
