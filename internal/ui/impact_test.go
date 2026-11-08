package ui_test

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	. "github.com/yindia/p9s/internal/ui/uitest"
)

// The default :pol rows sort by key: alpha observe-images, prefer-limits,
// require-owner, then beta no-latest.

func TestImpactOfAuditPolicy(t *testing.T) {
	golden(t, twoEngines(), Type("/no-latest\n"), WaitFor("/no-latest"), Type("i"), WaitFor("If enforced"))
}

func TestImpactOfEnforcingPolicy(t *testing.T) {
	golden(t, twoEngines(), Type("/require-owner\n"), WaitFor("/require-owner"), Type("i"), WaitFor("Already enforcing"))
}

func TestImpactWithNoViolations(t *testing.T) {
	golden(t, twoEngines(), Type("/prefer-limits\n"), WaitFor("/prefer-limits"), Type("i"), WaitFor("nothing that exists"))
}

func TestImpactUnknown(t *testing.T) {
	golden(t, twoEngines(), Type("/observe-images\n"), WaitFor("/observe-images"), Type("i"), WaitFor("Impact unknown"))
}

func TestImpactCommand(t *testing.T) {
	golden(t, twoEngines(), Type(":impact no-latest\n"), WaitFor("If enforced"))
}

func TestImpactCommandNeedsAPolicy(t *testing.T) {
	golden(t, twoEngines(), Type(":impact\n"), WaitFor("needs a policy"))
}

func TestImpactKeyOnNonPolicyRow(t *testing.T) {
	golden(t, twoEngines(), Type(":ctx\n"), WaitFor("Contexts"), Type("i"), WaitFor("i works on policy and violation rows"))
}

func TestImpactOwnerExpandsToViolations(t *testing.T) {
	golden(t, twoEngines(), Type(":impact no-latest\n"), WaitFor("If enforced"), Key(tea.KeyEnter), WaitFor("api-0"))
}

func TestImpactFromEngineView(t *testing.T) {
	golden(t, twoEngines(), Type(":al/rules\n/require\n"), WaitFor("/require"), Type("i"), WaitFor("Already enforcing"))
}

// truncatedBackend has a policy Gatekeeper-style capped: 40 counted, 3
// listed, and a second engine with a same-named policy.
func truncatedBackend() *Backend {
	key := model.PolicyKey{Engine: "alpha", Kind: "AlphaRule", Name: "need-team"}
	v := func(ns, name string) model.Violation {
		return model.Violation{Engine: "alpha", Policy: key, Target: model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: ns, Name: name},
			Message: "label team is required", Truncated: true, Timestamp: fixtures.Now}
	}
	alpha := fake.New(fake.Options{Name: "alpha", Prefix: "al", Installed: true, Version: "v1", Caps: fixtures.AllCaps, Data: fake.Data{
		Policies: []model.Policy{{Engine: "alpha", Kind: "AlphaRule", Name: "need-team", Enforcement: model.Enforcement{Mode: model.ModeWarn, Raw: "warn"},
			ViolationCount: 40, ViolationsKnown: true, LastAudit: fixtures.Now}},
		Violations: []model.Violation{v("payments", "api-0"), v("payments", "bare"), v("search", "idx-0")},
	}})
	beta := fake.New(fake.Options{Name: "beta", Prefix: "bt", Installed: true, Version: "v2",
		Caps: engine.Capabilities{Policies: true, Violations: true}, Data: fake.Data{
			Policies: []model.Policy{{Engine: "beta", Kind: "BetaPolicy", Name: "need-team", ViolationsKnown: true, LastAudit: fixtures.Now}},
		}})
	b := NewBackend(alpha, beta)
	b.Ownership = &StaticOwners{}
	b.Ownership.Parents = map[string]model.ObjectRef{
		"Pod/payments/api-0": {Group: "apps", Version: "v1", Kind: "StatefulSet", Namespace: "payments", Name: "api"},
	}
	b.Ownership.Objects = map[string]map[string]string{"Pod/payments/bare": {}}
	return b
}

func TestImpactTruncatedShowsNofM(t *testing.T) {
	golden(t, truncatedBackend(), Type(":impact al:AlphaRule/need-team\n"), WaitFor("Showing 3 of 40"))
}

func TestImpactAmbiguousName(t *testing.T) {
	golden(t, truncatedBackend(), Type(":impact need-team\n"), WaitFor("ambiguous"))
}

func TestImpactIsClusterWide(t *testing.T) {
	golden(t, truncatedBackend(), Type(":ns search\n:impact alpha:AlphaRule/need-team\n"), WaitFor("Showing 3 of 40"))
}

func TestImpactDrillIgnoresNamespaceScope(t *testing.T) {
	golden(t, truncatedBackend(), Type(":ns search\n:impact alpha:AlphaRule/need-team\n"), WaitFor("Showing 3 of 40"),
		Key(tea.KeyEnter), WaitFor("Pod/payments/bare"))
}
