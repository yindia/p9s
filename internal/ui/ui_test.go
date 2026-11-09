package ui_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/ui"
	. "github.com/yindia/p9s/internal/ui/uitest"
)

// twoEngines is the default backend: alpha (all capabilities) and beta
// (policies and violations only, with an RBAC denial).
func twoEngines() *Backend {
	b := NewBackend(fixtures.Alpha(), fixtures.Beta())
	b.NsNames = []string{"default", "payments", "search"}
	// api-0 belongs to StatefulSet api.
	b.Ownership = &StaticOwners{StaticOwners: query.StaticOwners{
		Parents: map[string]model.ObjectRef{
			"Pod/payments/api-0": {Group: "apps", Version: "v1", Kind: "StatefulSet", Namespace: "payments", Name: "api"},
		},
		// The namespaces are cached: a Namespace target is known to be bare.
		Known: map[string]bool{"Namespace/payments": true, "Namespace/search": true},
	}}
	b.Objects["Pod/payments/api-0"] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "payments", "managedFields": []any{"noise"}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "api", "image": "api:latest"}}},
	}}
	return b
}

func golden(t *testing.T, b *Backend, steps ...Step) {
	t.Helper()
	RequireGolden(t, Run(t, b, Options(fixtures.Now), steps...))
}

func TestPoliciesTwoEngines(t *testing.T) { golden(t, twoEngines()) }

func TestPoliciesEngineArg(t *testing.T) {
	golden(t, twoEngines(), Type(":pol beta\n"), WaitFor("(beta)"))
}

func TestViolations(t *testing.T) { golden(t, twoEngines(), Type(":v\n"), WaitFor("Violations")) }

func TestDrillPolicyToViolations(t *testing.T) {
	// First row is alpha/prefer-limits? Rows sort by key; move to require-owner.
	golden(t, twoEngines(), Type("/require\n"), Key(tea.KeyEnter), WaitFor("require-owner)"))
}

func TestDescribePolicy(t *testing.T) {
	golden(t, twoEngines(), Type("/require\nd"), WaitFor("Summary"))
}

func TestYAML(t *testing.T) {
	golden(t, twoEngines(), Type("/require\ny"), WaitFor("apiVersion"))
}

func TestFilterNoMatch(t *testing.T) {
	golden(t, twoEngines(), Type("/zzz\n"), WaitFor("No rows match"))
}

func TestFilterEscClears(t *testing.T) {
	golden(t, twoEngines(), Type("/beta\n"), WaitFor("/beta"), Type("\x1b"), WaitFor("[4]"))
}

func TestJumpToObject(t *testing.T) {
	golden(t, twoEngines(), Type(":v beta\nog"), WaitFor("image: api:latest"))
}

func TestJumpForbiddenShowsRBAC(t *testing.T) {
	golden(t, twoEngines(), Type(":v alpha\nog"), WaitFor("RBAC"))
}

func TestMutationsHiddenForIncapableEngines(t *testing.T) {
	golden(t, twoEngines(), Type(":mut\n"), WaitFor("Mutations"))
}

func TestMutationsNoCapableEngine(t *testing.T) {
	golden(t, NewBackend(fixtures.Beta()), Type(":mut\n"), WaitFor("No active engine supports"))
}

func TestHealth(t *testing.T) { golden(t, healthBackend(), Type(":health\n"), WaitFor("OVERALL")) }

func TestContexts(t *testing.T) { golden(t, twoEngines(), Type(":ctx\n"), WaitFor("Contexts")) }

func TestSwitchContext(t *testing.T) {
	golden(t, twoEngines(), Type(":ctx\n"), Key(tea.KeyDown), Key(tea.KeyEnter), WaitFor("connected to prod"))
}

func TestEngineSpecificView(t *testing.T) {
	golden(t, twoEngines(), Type(":al/rules\n"), WaitFor("Rules"))
}

func TestUnknownCommand(t *testing.T) {
	golden(t, twoEngines(), Type(":kyv/policies\n"), WaitFor("no active engine provides"))
}

func TestHelp(t *testing.T) { golden(t, twoEngines(), Type("?"), WaitFor("Navigation")) }

func TestNoEngineInstalled(t *testing.T) {
	b := NewBackend()
	b.Dets = []engine.Detection{{Engine: fake.New(fake.Options{Name: "gatekeeper"})}}
	golden(t, b, WaitFor("No supported policy engine"))
}

func TestConnectFailure(t *testing.T) {
	b := twoEngines()
	b.ConnectErr = errors.New("dial tcp 10.0.0.1:6443: i/o timeout")
	golden(t, b, WaitFor("connect failed"))
}

func TestNamespaceList(t *testing.T) {
	golden(t, twoEngines(), Type(":ns\n"), WaitFor("search"))
}

func TestNamespaceScopeViaList(t *testing.T) {
	golden(t, twoEngines(), Type(":ns\n"), WaitFor("search"), Key(tea.KeyDown), Key(tea.KeyDown), Key(tea.KeyEnter), WaitFor("scope: payments"))
}

func TestNamespaceScopeViolations(t *testing.T) {
	golden(t, twoEngines(), Type(":ns payments\n:v\n"), WaitFor("Violations"))
}

func TestNamespaceScopeClearedWithZero(t *testing.T) {
	golden(t, twoEngines(), Type(":ns payments\n0"), WaitFor("scope: all"))
}

func TestNamespaceRBACFallback(t *testing.T) {
	b := twoEngines()
	b.NsErr = errors.New("forbidden")
	golden(t, b, Type(":ns\n"), WaitFor("seen in violations"))
}

func TestLiveUpdate(t *testing.T) {
	alpha := fixtures.Alpha()
	b := NewBackend(alpha)
	golden(t, b, Do(func() {
		ps, _ := alpha.Policies()
		alpha.SetData(fake.Data{Policies: ps[:1]})
		b.Notify()
	}), WaitFor("[1]"))
}

func TestReadWriteBadge(t *testing.T) {
	b := twoEngines()
	b.ReadWrite = true
	golden(t, b)
}

func TestEditorReadOnlyDiscardsChanges(t *testing.T) {
	golden(t, twoEngines(), Type("/require\ne"), WaitFor("read-only mode"))
}

func TestBackFromDetail(t *testing.T) {
	golden(t, twoEngines(), Type("/require\nd"), WaitFor("Summary"), Type("q"), WaitFor("[1]"))
}

func TestCommandCompletion(t *testing.T) {
	golden(t, twoEngines(), Type(":he\t\n"), WaitFor("Health"))
}

func TestSecretValuesAreRedacted(t *testing.T) {
	key := model.PolicyKey{Engine: "beta", Kind: "BetaPolicy", Namespace: "payments", Name: "no-latest"}
	beta := fake.New(fake.Options{Name: "beta", Installed: true, Version: "0.9.1", Caps: engine.Capabilities{Violations: true},
		Data: fake.Data{Violations: []model.Violation{{Engine: "beta", Policy: key,
			Target: model.ObjectRef{Version: "v1", Kind: "Secret", Namespace: "payments", Name: "db"}, Message: "secret not encrypted"}}}})
	b := NewBackend(beta)
	b.Objects["Secret/payments/db"] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "db", "namespace": "payments"},
		"data": map[string]any{"password": "aHVudGVyMg=="},
	}}
	screen := Run(t, b, Options(fixtures.Now), Type(":v\ng"), WaitFor("redacted"))
	if strings.Contains(screen, "aHVudGVyMg==") {
		t.Fatal("secret value rendered on screen")
	}
	RequireGolden(t, screen)
}

func TestScopedCountsMarkTruncation(t *testing.T) {
	alpha := fixtures.Alpha()
	ps, _ := alpha.Policies()
	vs, _ := alpha.Violations()
	for i := range vs {
		vs[i].Truncated = true
	}
	alpha.SetData(fake.Data{Policies: ps, Violations: vs})
	golden(t, NewBackend(alpha), Type(":ns payments\n"), WaitFor("scope: payments"))
}

// TestSyncingShowsPartialCounts freezes an engine mid-sync: the header says
// so and counts render as provisional ("~2"), never as final values.
func TestSyncingShowsPartialCounts(t *testing.T) {
	alpha := fixtures.Alpha()
	alpha.SetSynced(false)
	golden(t, NewBackend(alpha, fixtures.Beta()), WaitFor("syncing…"))
}

func TestSyncCompletesClearsPartialCounts(t *testing.T) {
	alpha := fixtures.Alpha()
	alpha.SetSynced(false)
	golden(t, NewBackend(alpha), WaitFor("syncing…"), Do(func() { alpha.SetSynced(true) }), WaitFor("audit 30s"))
}

func TestSortByNameKey(t *testing.T) {
	golden(t, twoEngines(), Type("N"), WaitFor("NAME↑"))
}

func TestSortKeyTogglesDirection(t *testing.T) {
	golden(t, twoEngines(), Type("A"), WaitFor("AGE↑"), Type("A"), WaitFor("AGE↓"))
}

func TestSortKeyWithoutColumn(t *testing.T) {
	golden(t, twoEngines(), Type(":ctx\nS"), WaitFor("no severity column"))
}

func TestFilterByColumnTerms(t *testing.T) {
	golden(t, twoEngines(), Type(":v\n/engine:alpha ns:search\n"), WaitFor("[1]"))
}

func TestFilterModeTerm(t *testing.T) {
	golden(t, twoEngines(), Type("/mode:audit\n"), WaitFor("/mode:audit"))
}

func TestCopyRefAndKubectl(t *testing.T) {
	var copied []string
	opts := Options(fixtures.Now)
	opts.Copy = func(s string) error { copied = append(copied, s); return nil }
	screen := Run(t, twoEngines(), opts, Type(":v beta\no"), WaitFor("flat list"), Type("c"), WaitFor("copied:"), Type("C"), WaitFor("kubectl"))
	want := []string{"Pod/payments/api-0", "kubectl --context=kind-demo get pod/api-0 --namespace=payments -o yaml"}
	if len(copied) != 2 || copied[0] != want[0] || copied[1] != want[1] {
		t.Errorf("copied %q, want %q", copied, want)
	}
	RequireGolden(t, screen)
}

func TestCopyWithoutTarget(t *testing.T) {
	golden(t, twoEngines(), Type("c"), WaitFor("does not point at an object"))
}

// TestHintsFollowTheRow: policies have no target, so no g/c hints; the
// esc hint appears only when there is somewhere to go back to.
func TestHintsFollowTheRow(t *testing.T) {
	golden(t, twoEngines(), Type(":pol\n"), Type("j"), WaitFor("? help"))
}

func TestHelpFitsEightyColumns(t *testing.T) {
	screen := RunSized(t, 80, 40, twoEngines(), Options(fixtures.Now), Type("?"), WaitFor("Navigation"))
	for _, section := range []string{"Navigation", "Views", "Keys", "Engine views"} {
		if !strings.Contains(screen, section) {
			t.Errorf("help at 80x40 is missing section %q", section)
		}
	}
	for i, line := range strings.Split(screen, "\n") {
		if w := len([]rune(strings.TrimRight(line, " "))); w > 80 {
			t.Errorf("line %d is %d columns wide: %q", i, w, line)
		}
	}
	RequireGolden(t, screen)
}

func TestNarrowViolationsGiveMessageTheRest(t *testing.T) {
	RequireGolden(t, RunSized(t, 80, 14, twoEngines(), Options(fixtures.Now), Type(":v\n"), WaitFor("Violations")))
}

func TestHelpScrolls(t *testing.T) {
	golden(t, twoEngines(), Type("?"), WaitFor("Navigation"), Key(tea.KeyPgDown), WaitFor("bt/rules"))
}

// TestTinyTerminalDoesNotPanic: four rows leave no room for the status line.
func TestTinyTerminalDoesNotPanic(t *testing.T) {
	screen := RunSized(t, 60, 4, twoEngines(), Options(fixtures.Now), Type(":v\n"), WaitFor("Violations"))
	if got := len(strings.Split(screen, "\n")); got != 4 {
		t.Errorf("screen has %d lines, want 4", got)
	}
}

// TestFirstSortKeyPressKeepsItsDirection: V on a view already sorted by
// violations (descending) must not flip it to ascending.
func TestFirstSortKeyPressKeepsItsDirection(t *testing.T) {
	screen := Run(t, twoEngines(), Options(fixtures.Now), Type("N"), WaitFor("NAME↑"), Type("V"), WaitFor("VIOLATIONS↓"))
	if !strings.Contains(screen, "VIOLATIONS↓") {
		t.Errorf("first V press should sort most violations first:\n%s", screen)
	}
}

func overviewOpts() ui.Options {
	o := Options(fixtures.Now)
	o.InitialCommand = "" // the real default
	return o
}

func TestOverviewIsTheDefaultStartView(t *testing.T) {
	RequireGolden(t, Run(t, twoEngines(), overviewOpts(), WaitFor("Enforcement candidates")))
}

func TestOverviewEnterOpensFilteredView(t *testing.T) {
	RequireGolden(t, Run(t, twoEngines(), overviewOpts(), WaitFor("Enforcement candidates"),
		Type("jj"), Key(tea.KeyEnter), WaitFor("/mode:audit,warn")))
}

func TestOverviewEscReturns(t *testing.T) {
	RequireGolden(t, Run(t, twoEngines(), overviewOpts(), WaitFor("Enforcement candidates"),
		Key(tea.KeyEnter), WaitFor("OVERALL"), Type("\x1b"), WaitFor("Top violating")))
}

func TestOverviewPlaceholderHasNothingToOpen(t *testing.T) {
	RequireGolden(t, Run(t, twoEngines(), overviewOpts(), WaitFor("Enforcement candidates"),
		Type("jjj"), Key(tea.KeyEnter), WaitFor("nothing to open")))
}

func TestOverviewInNamespaceScope(t *testing.T) {
	RequireGolden(t, Run(t, twoEngines(), overviewOpts(), WaitFor("Enforcement candidates"),
		Type(":ns search\n:overview\n"), WaitFor("ns search")))
}

func TestOverviewWithoutEngines(t *testing.T) {
	b := NewBackend()
	b.Dets = []engine.Detection{{Engine: fake.New(fake.Options{Name: "gatekeeper"})}}
	RequireGolden(t, Run(t, b, overviewOpts(), WaitFor("Recent admission denials")))
}

// TestOverviewScrollsToSelectedBlock: at 24 rows the lower blocks only
// become visible by moving to them.
func TestOverviewScrollsToSelectedBlock(t *testing.T) {
	b := NewBackend(fixtures.Alpha(), fixtures.Beta())
	screen := RunSized(t, 100, 14, b, overviewOpts(), WaitFor("Health"), Type("jjj"), WaitFor("▸ Recent admission denials"))
	if !strings.Contains(screen, "▸ Recent admission denials") || strings.Contains(screen, "▸ Health") {
		t.Errorf("selected block must be on screen:\n%s", screen)
	}
	RequireGolden(t, screen)
}

func TestOverviewWhileSyncing(t *testing.T) {
	alpha := fixtures.Alpha()
	alpha.SetSynced(false)
	RequireGolden(t, Run(t, NewBackend(alpha), overviewOpts(), WaitFor("(syncing)")))
}

// TestOverviewPresetFilterEscReturns: one esc from a view opened with a
// preset filter goes straight back to the overview.
func TestOverviewPresetFilterEscReturns(t *testing.T) {
	screen := Run(t, twoEngines(), overviewOpts(), WaitFor("Enforcement candidates"),
		Type("jj"), Key(tea.KeyEnter), WaitFor("/mode:audit,warn"), Type("\x1b"), WaitFor("▸ Enforcement candidates"))
	if !strings.Contains(screen, "─ Overview ─") {
		t.Errorf("one esc should return to the overview:\n%s", screen)
	}
}

func TestOverviewRejectsArguments(t *testing.T) {
	screen := Run(t, twoEngines(), overviewOpts(), WaitFor("Enforcement candidates"), Type(":ov gatekeeper\n"), WaitFor("takes no arguments"))
	if !strings.Contains(screen, "takes no arguments") {
		t.Error(":ov with an argument must be rejected")
	}
}

func TestViolationsFlatToggle(t *testing.T) {
	golden(t, twoEngines(), Type(":v\no"), WaitFor("flat list"))
}

func TestGroupExpandsToItsViolations(t *testing.T) {
	golden(t, twoEngines(), Type(":v\n/api\n"), WaitFor("/api"), Key(tea.KeyEnter), WaitFor("Violations › Violations"))
}

func TestGroupingKeyOutsideViolations(t *testing.T) {
	golden(t, twoEngines(), Type("o"), WaitFor("grouping applies to :v"))
}

func TestOwnerRBACNotice(t *testing.T) {
	b := twoEngines()
	b.Ownership.Denied = []model.AccessIssue{{Engine: "owners", Resource: "replicasets.apps", Verb: "list"}}
	golden(t, b, Type(":v\n"), WaitFor("cannot list replicasets.apps"))
}

// healthBackend adds platform state: a fail-open webhook without endpoints,
// one with a certificate expiring in 9 days, a crash-looping controller.
func healthBackend() *Backend {
	b := twoEngines()
	b.Updated = fixtures.Now.Add(-2 * time.Second)
	b.Plat = &StaticPlatform{State: query.Platform{
		Webhooks: []query.Webhook{
			{Engine: "alpha", Type: "validating", Config: "alpha-webhooks", Name: "validate.alpha.example", FailurePolicy: "Ignore",
				TimeoutSecs: 3, Service: "alpha/alpha-webhook"},
			{Engine: "beta", Type: "validating", Config: "beta-webhooks", Name: "validate.beta.example", FailurePolicy: "Fail",
				TimeoutSecs: 10, Service: "beta/beta-webhook", Endpoints: []query.Endpoint{{Pod: "beta/beta-0", Ready: true}}},
		},
		Controllers: []query.Controller{
			{Engine: "alpha", Namespace: "alpha", Name: "alpha-controller", Ready: 0, Desired: 2, Restarts: 14, LastReason: "CrashLoopBackOff",
				Images: []string{"alpha:v1.2.0"}},
			{Engine: "beta", Namespace: "beta", Name: "beta-controller", Ready: 1, Desired: 1, Images: []string{"beta:0.9.1"}},
		},
	}}
	return b
}

func TestHealthWebhookDetail(t *testing.T) {
	golden(t, healthBackend(), Type(":health\n"), WaitFor("OVERALL"), Key(tea.KeyEnter), WaitFor("Impact"))
}

func TestHealthTabSwitchesPanels(t *testing.T) {
	golden(t, healthBackend(), Type(":health\n"), WaitFor("OVERALL"), Key(tea.KeyTab), WaitFor("detail"))
}

func TestOverviewHealthFromDashboard(t *testing.T) {
	RequireGolden(t, Run(t, healthBackend(), overviewOpts(), WaitFor("critical")))
}

func TestHealthRejectsArguments(t *testing.T) {
	golden(t, healthBackend(), Type(":health gatekeeper\n"), WaitFor("takes no arguments"))
}

// A tenant can be denied dozens of resources; the notices must not push
// the rows they can see off the screen.
func TestManyAccessIssuesKeepRowsVisible(t *testing.T) {
	b := twoEngines()
	var access []model.AccessIssue
	for i := range 40 {
		access = append(access, model.AccessIssue{Engine: "beta", Verb: "list", Resource: fmt.Sprintf("kind%02d.beta.example", i)})
	}
	b.Engs[1].(*fake.Engine).SetAccess(access)
	screen := RunSized(t, 120, 14, b, Options(fixtures.Now), Type(":v\n"), WaitFor("Violations"))
	if !strings.Contains(screen, "forbidden to list 40 resources") || !strings.Contains(screen, "StatefulSet/payments/api") {
		t.Fatalf("want one grouped notice and the violation rows:\n%s", screen)
	}
	RequireGolden(t, screen)
}

// On a narrow terminal the engine versions and list give way first; the
// audit age and the RO/RW mode stay on the header line.
func TestHeaderNarrowKeepsAuditAndMode(t *testing.T) {
	for _, width := range []int{80, 70} {
		screen := RunSized(t, width, 10, twoEngines(), Options(fixtures.Now), Type(":v\n"), WaitFor("Violations"))
		head := strings.SplitN(screen, "\n", 2)[0]
		if !strings.Contains(head, "last audit") || !strings.Contains(head, "RO") {
			t.Errorf("width %d header lost the audit age or the mode: %q", width, head)
		}
	}
}

// A footer message belongs to the view it was set in.
func TestFlashDoesNotFollowIntoNextView(t *testing.T) {
	screen := Run(t, twoEngines(), Options(fixtures.Now), Type(":v\n"), WaitFor("Violations"), Type("o"), WaitFor("flat list"),
		Type(":pol\n"), WaitFor("─ Policies"))
	if strings.Contains(screen, "flat list") {
		t.Errorf("the :v message followed into :pol:\n%s", screen)
	}
}

func TestFilterTypoIsFlagged(t *testing.T) {
	screen := Run(t, twoEngines(), Options(fixtures.Now), WaitFor("connected to"),
		Type("/sev:hgih\n"), WaitFor("matches nothing"))
	if !strings.Contains(screen, "sev:hgih matches nothing") {
		t.Errorf("want the typo flagged:\n%s", screen)
	}
}
