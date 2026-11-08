package ui_test

import (
	"testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	. "github.com/yindia/p9s/internal/ui/uitest"
)

func TestViolationDetailWithGuidance(t *testing.T) {
	golden(t, twoEngines(), Type(":v\no/Namespace/search\n"), WaitFor("/Namespace/search"), Type("d"), WaitFor("How to fix"))
}

func TestViolationDetailWithoutGuidance(t *testing.T) {
	golden(t, twoEngines(), Type(":v\no/no-latest\n"), WaitFor("/no-latest"), Type("d"), WaitFor("No guidance"))
}

func TestGroupDetailWithGuidance(t *testing.T) {
	golden(t, twoEngines(), Type(":v\n/Namespace/search\n"), WaitFor("/Namespace/search"), Type("d"), WaitFor("How to fix"))
}

func TestViolationYAMLIsThePolicy(t *testing.T) {
	golden(t, twoEngines(), Type(":v\no/Namespace/search\n"), WaitFor("/Namespace/search"), Type("y"), WaitFor("kind: AlphaRule"))
}

// tenantBackend has a violation whose policy RBAC hides: Policies() does
// not list it.
func tenantBackend() *Backend {
	key := model.PolicyKey{Engine: "kyverno", Kind: "ClusterPolicy", Name: "require-team-label"}
	e := fake.New(fake.Options{Name: "kyverno", Prefix: "kyv", Installed: true, Version: "v1.19.2",
		Caps: engine.Capabilities{Policies: true, Violations: true}, Data: fake.Data{
			Violations: []model.Violation{{Engine: "kyverno", Policy: key, Rule: "check-team",
				Target:      model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "p9s-demo", Name: "web-0"},
				Message:     "validation error: label 'team' is required.",
				Enforcement: model.Enforcement{Mode: model.ModeUnknown, Raw: "fail"}, Timestamp: fixtures.Now, PolicyUnreadable: true}},
			Access: []model.AccessIssue{{Engine: "kyverno", Resource: "clusterpolicies.kyverno.io", Verb: "list", Message: "forbidden"}},
		}})
	return NewBackend(e)
}

func TestViolationDetailPolicyUnreadable(t *testing.T) {
	golden(t, tenantBackend(), Type(":v\no"), WaitFor("flat list"), Type("d"), WaitFor("not readable"))
}

func TestViolationYAMLPolicyUnreadable(t *testing.T) {
	golden(t, tenantBackend(), Type(":v\no"), WaitFor("flat list"), Type("y"), WaitFor("not readable with your permissions"))
}

func TestImpactFromViolationRow(t *testing.T) {
	golden(t, twoEngines(), Type(":v\no/Namespace/search\n"), WaitFor("/Namespace/search"), Type("i"), WaitFor("Already enforcing"))
}
