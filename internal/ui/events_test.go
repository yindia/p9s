package ui_test

import (
	"time"

	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	. "github.com/yindia/p9s/internal/ui/uitest"
	"github.com/yindia/p9s/internal/views"
)

// eventsBackend adds recent admission events: two alpha denials (one in
// payments) and a beta warning; beta does not emit denials by default.
func eventsBackend() *Backend {
	b := twoEngines()
	ref := func(ns, name string) model.ObjectRef {
		return model.ObjectRef{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: ns, Name: name}
	}
	b.Evs = &StaticEvents{
		List: []model.AdmissionEvent{
			{Engine: "alpha", Policy: model.PolicyKey{Engine: "alpha", Kind: "RequiredLabels", Name: "must-have-owner"},
				Enforcement: model.Enforcement{Mode: model.ModeEnforce, Raw: "deny"}, User: "alice@example.com",
				Target: ref("payments", "checkout"), Message: "missing label owner", Time: fixtures.Now.Add(-2 * time.Minute), Count: 3},
			{Engine: "beta", Policy: model.PolicyKey{Engine: "beta", Kind: "ClusterPolicy", Name: "disallow-latest"},
				Enforcement: model.Enforcement{Mode: model.ModeWarn, Raw: "warn"},
				Target:      ref("search", "indexer"), Message: "image tag latest", Time: fixtures.Now.Add(-10 * time.Minute), Count: 1},
			{Engine: "alpha", Policy: model.PolicyKey{Engine: "alpha", Kind: "RequiredLabels", Name: "must-have-owner"},
				Enforcement: model.Enforcement{Mode: model.ModeEnforce, Raw: "deny"}, User: "ci-bot",
				Target: ref("default", "web"), Message: "missing label owner", Time: fixtures.Now.Add(-40 * time.Minute), Count: 1},
		},
		Status: []views.EventStatus{{Engine: "alpha", Enabled: true}, {Engine: "beta", Enabled: false, Hint: "beta admission events are off."}},
	}
	return b
}

func TestEventsCommandOpensRecentTab(t *testing.T) {
	golden(t, eventsBackend(), Type(":events\n"), WaitFor("Violations · recent"))
}

func TestViolationsTabSwitch(t *testing.T) {
	golden(t, eventsBackend(), Type(":v\n]"), WaitFor("recent: admission"))
}

func TestViolationsTabBackRestoresGrouping(t *testing.T) {
	golden(t, eventsBackend(), Type(":v\no"), WaitFor("flat list"), Type("]"), WaitFor("recent: admission"),
		Type("["), WaitFor("current: audit"))
}

func TestRecentTabInNamespaceScope(t *testing.T) {
	golden(t, eventsBackend(), Type(":ns payments\n:events\n"), WaitFor("Violations · recent"))
}

func TestRecentEventDetail(t *testing.T) {
	golden(t, eventsBackend(), Type(":events\n"), WaitFor("Violations · recent"), Key(tea.KeyEnter), WaitFor("Admission event"))
}

func TestRecentTabEmpty(t *testing.T) {
	b := twoEngines()
	b.Evs = &StaticEvents{}
	golden(t, b, Type(":events\n"), WaitFor("No admission events"))
}

func TestRecentTabWithoutEventSource(t *testing.T) {
	golden(t, twoEngines(), Type(":events\n"), WaitFor("No active engine records"))
}

func TestRecentTabRBACNotice(t *testing.T) {
	b := eventsBackend()
	b.Evs.Denied = []model.AccessIssue{{Engine: "events", Resource: "events in alpha-system (alpha)", Verb: "list"}}
	golden(t, b, Type(":events\n"), WaitFor("cannot list events"))
}

func TestTabKeysOutsideViolations(t *testing.T) {
	golden(t, eventsBackend(), Type("]"), WaitFor("switch the tabs"))
}

func TestOverviewDenialsBlock(t *testing.T) {
	RequireGolden(t, Run(t, eventsBackend(), overviewOpts(), WaitFor("Recent admission denials"), WaitFor("alice")))
}

func TestOverviewDenialsOpenRecentTab(t *testing.T) {
	RequireGolden(t, Run(t, eventsBackend(), overviewOpts(), WaitFor("alice"),
		Type("jjj"), Key(tea.KeyEnter), WaitFor("Violations · recent")))
}

func TestRecentFirstThenCurrentTabIsDefaultGrouping(t *testing.T) {
	golden(t, eventsBackend(), Type(":events\n"), WaitFor("Violations · recent"), Type("["), WaitFor("current: audit"))
}

func TestDrilledPolicyFilterCarriesToRecentTab(t *testing.T) {
	b := eventsBackend()
	b.Evs.List = append(b.Evs.List, model.AdmissionEvent{Engine: "alpha",
		Policy:      model.PolicyKey{Engine: "alpha", Kind: "AlphaRule", Name: "require-owner"},
		Enforcement: model.Enforcement{Mode: model.ModeEnforce, Raw: "deny"}, User: "bob",
		Target: model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "scratch"}, Message: "label owner is required",
		Time: fixtures.Now.Add(-time.Minute), Count: 1})
	golden(t, b, Type("/require\n"), Key(tea.KeyEnter), WaitFor("require-owner)"), Type("]"), WaitFor("recent: admission"))
}
