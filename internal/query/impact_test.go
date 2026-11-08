package query_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
)

var impactKey = model.PolicyKey{Engine: "e", Kind: "Rule", Name: "need-owner"}

func violation(target model.ObjectRef, truncated bool) model.Violation {
	return model.Violation{Engine: "e", Policy: impactKey, Target: target, Message: "owner label missing", Truncated: truncated}
}

func impactEngine(t *testing.T, mode model.EnforcementMode, total int, known bool, vs ...model.Violation) []engine.Engine {
	t.Helper()
	other := model.PolicyKey{Engine: "e", Kind: "Rule", Name: "other"}
	e := fake.New(fake.Options{Name: "e", Installed: true, Caps: engine.Capabilities{Policies: true, Violations: true},
		Data: fake.Data{
			Policies: []model.Policy{
				{Engine: "e", Kind: "Rule", Name: "need-owner", Enforcement: model.Enforcement{Mode: mode},
					ViolationCount: total, ViolationsKnown: known},
				{Engine: "e", Kind: "Rule", Name: "other", ViolationCount: 1, ViolationsKnown: true},
			},
			Violations: append(vs, model.Violation{Engine: "e", Policy: other, Target: ref("Pod", "a", "web-1-x")}),
		}})
	if err := e.Start(context.Background(), nil, func() {}); err != nil {
		t.Fatal(err)
	}
	return []engine.Engine{e}
}

func TestImpactGroupsByNamespaceThenOwner(t *testing.T) {
	lookup := chains
	lookup.Objects = map[string]map[string]string{"Pod/b/bare": {}}
	es := impactEngine(t, model.ModeAudit, 5, true,
		violation(ref("Pod", "a", "web-1-x"), false),
		violation(ref("Pod", "a", "web-1-y"), false),
		violation(ref("Pod", "a", "db-0"), false),
		violation(ref("Pod", "b", "bare"), false),
		violation(ref("ConfigMap", "b", "cfg"), false),
	)
	im, err := query.ImpactOf(es, impactKey, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !im.Known || !im.Complete || im.Total != 5 || im.Listed != 5 || im.Objects != 5 || im.Enforcing() {
		t.Fatalf("impact = %+v", im)
	}
	want := []struct {
		owner   string
		managed query.Managed
		objects int
	}{
		{"Deployment/a/web", query.ManagedController, 2},
		{"StatefulSet/a/db", query.ManagedController, 1},
		{"ConfigMap/b/cfg", query.ManagedUnknown, 1}, // not watched: ownership unknown
		{"Pod/b/bare", query.ManagedBare, 1},         // cached, no controller
	}
	if len(im.Owners) != len(want) {
		t.Fatalf("owners = %+v", im.Owners)
	}
	for i, w := range want {
		o := im.Owners[i]
		if o.Owner.String() != w.owner || o.Managed != w.managed || o.Objects != w.objects {
			t.Errorf("owner %d = %s %s %d, want %+v", i, o.Owner, o.Managed, o.Objects, w)
		}
	}
	if len(im.Namespaces) != 2 || im.Namespaces[0] != (query.ImpactNamespace{Name: "a", Objects: 3}) {
		t.Errorf("namespaces = %+v: most objects first", im.Namespaces)
	}
	if im.ByManaged[query.ManagedController] != 2 || im.ByManaged[query.ManagedBare] != 1 {
		t.Errorf("by managed = %+v", im.ByManaged)
	}
}

func TestImpactTruncatedIsNeverComplete(t *testing.T) {
	es := impactEngine(t, model.ModeAudit, 40, true,
		violation(ref("Namespace", "", "x"), true), violation(ref("Namespace", "", "y"), true))
	im, err := query.ImpactOf(es, impactKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if im.Complete || im.Total != 40 || im.Listed != 2 {
		t.Errorf("impact = total %d listed %d complete %v: want 40, 2, incomplete", im.Total, im.Listed, im.Complete)
	}
	if len(im.Namespaces) != 1 || im.Namespaces[0].Name != "" {
		t.Errorf("Namespace targets are cluster-scoped: %+v", im.Namespaces)
	}
}

func TestImpactUnknownAndEnforcing(t *testing.T) {
	im, err := query.ImpactOf(impactEngine(t, model.ModeEnforce, 0, false), impactKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if im.Known || im.Complete || !im.Enforcing() {
		t.Errorf("impact = %+v: unknown, so not complete; enforce mode", im)
	}
}

func TestImpactPolicyNotFound(t *testing.T) {
	_, err := query.ImpactOf(impactEngine(t, model.ModeAudit, 0, true), model.PolicyKey{Engine: "e", Kind: "Rule", Name: "gone"}, nil)
	if !errors.Is(err, query.ErrPolicyNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestFindPolicies(t *testing.T) {
	es := impactEngine(t, model.ModeAudit, 0, true)
	tests := []struct {
		q    string
		want int
	}{
		{"need-owner", 1}, {"rule/need-owner", 1}, {"e:Rule/need-owner", 1},
		{"Other/need-owner", 0}, {"missing", 0},
	}
	for _, tt := range tests {
		got, err := query.FindPolicies(es, tt.q)
		if err != nil || len(got) != tt.want {
			t.Errorf("FindPolicies(%q) = %v, %v; want %d", tt.q, got, err, tt.want)
		}
	}
}

func TestImpactStaleTotalNeverBelowListed(t *testing.T) {
	es := impactEngine(t, model.ModeAudit, 1, true,
		violation(ref("Pod", "b", "p1"), false), violation(ref("Pod", "b", "p2"), false), violation(ref("Pod", "b", "p3"), false))
	im, _ := query.ImpactOf(es, impactKey, nil)
	if im.Total != 3 || !im.Complete {
		t.Errorf("total %d complete %v: a stale count of 1 must not undercut the 3 listed", im.Total, im.Complete)
	}
}

func TestImpactUnknownCountWithListed(t *testing.T) {
	im, _ := query.ImpactOf(impactEngine(t, model.ModeAudit, 0, false, violation(ref("Pod", "b", "p1"), false)), impactKey, nil)
	if im.Known || im.Complete || im.Listed != 1 {
		t.Errorf("impact = %+v: listed rows without an engine count are never complete", im)
	}
}

func TestImpactSkipsEvaluationErrors(t *testing.T) {
	bad := violation(ref("Pod", "b", "p2"), false)
	bad.EvalError = true
	im, _ := query.ImpactOf(impactEngine(t, model.ModeAudit, 2, true, violation(ref("Pod", "b", "p1"), false), bad), impactKey, nil)
	if im.EvalErrors != 1 || im.Total != 1 || im.Listed != 1 || len(im.Owners) != 1 || !im.Complete {
		t.Errorf("impact = total %d listed %d errors %d owners %d: errors are not violations", im.Total, im.Listed, im.EvalErrors, len(im.Owners))
	}
}

func TestImpactNormalizesNamespaceRefs(t *testing.T) {
	es := impactEngine(t, model.ModeAudit, 2, true,
		violation(ref("Namespace", "x", "x"), false), violation(ref("Namespace", "", "x"), false))
	im, _ := query.ImpactOf(es, impactKey, nil)
	if len(im.Owners) != 1 || im.Objects != 1 || im.Owners[0].Namespace != "" {
		t.Errorf("owners %+v objects %d: one Namespace, cluster-scoped", im.Owners, im.Objects)
	}
}

func TestFindPoliciesUnknownEngine(t *testing.T) {
	if _, err := query.FindPolicies(impactEngine(t, model.ModeAudit, 0, true), "nope:Rule/need-owner"); err == nil {
		t.Error("an unknown engine prefix must say so, not just find nothing")
	}
}
