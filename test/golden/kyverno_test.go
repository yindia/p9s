package golden

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/engines/gatekeeper"
	"github.com/yindia/p9s/internal/engines/gatekeeper/gktest"
	"github.com/yindia/p9s/internal/engines/kyverno"
	"github.com/yindia/p9s/internal/engines/kyverno/kyvtest"
	"github.com/yindia/p9s/internal/session"
	. "github.com/yindia/p9s/internal/ui/uitest"
)

// bothEngines is one fake cluster with Gatekeeper and Kyverno installed,
// exactly what the e2e kind cluster runs.
func bothEngines(t *testing.T, withGatekeeper bool) *session.Session {
	t.Helper()
	regs := []engine.Registration{{Name: kyverno.Name, Factory: kyverno.New}}
	if !withGatekeeper {
		return newSessionFor(t, kyvtest.NewCluster(t, kyvtest.Options{}), regs)
	}
	objs := append(append(kyvtest.Load(t, "policies.yaml"), kyvtest.Load(t, "reports.yaml")...), kyvtest.Load(t, "platform.yaml")...)
	objs = append(objs, kyvtest.Load(t, "events.yaml")...)
	gk := gktest.NewCluster(t, gktest.Options{
		ExtraResources: kyvtest.Resources[1:], // [0] is apps/v1 deployments, already served
		Extra:          objs,
	})
	regs = append(regs, engine.Registration{Name: gatekeeper.Name, Factory: gatekeeper.New})
	return newSessionFor(t, gk, regs)
}

func newSessionFor(t *testing.T, cl engine.Cluster, regs []engine.Registration) *session.Session {
	t.Helper()
	s, err := session.New(session.Options{Connector: connector{cl}, Engines: regs, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func kyvGolden(t *testing.T, withGK bool, steps ...Step) {
	t.Helper()
	RequireGolden(t, Run(t, bothEngines(t, withGK), Options(fixtures.Now), steps...))
}

var kyvReady = WaitFor("require-team-label")

func TestKyvernoPolicies(t *testing.T) { kyvGolden(t, false, kyvReady) }

func TestKyvernoViolations(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":v\n"), WaitFor("coredns"))
}

func TestKyvernoClusterPolicies(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/cpol\n"), WaitFor("ClusterPolicies ["))
}

func TestKyvernoClusterPolicyDetail(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/cpol\nd"), WaitFor("rule check-team"))
}

func TestKyvernoNamespacedPolicies(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/pol\n"), WaitFor("no-latest-tag"))
}

func TestKyvernoCELPolicies(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/vpol\n"), WaitFor("team-quota"))
}

func TestKyvernoCELPolicyDetail(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/vpol\n/limits\nd"), WaitFor("Match constraints"))
}

func TestKyvernoReports(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/polr\n"), WaitFor("api-0"))
}

func TestKyvernoReportDetail(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/polr\n/api-0\nd"), WaitFor("Results"))
}

func TestKyvernoExceptions(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":kyv/polex\n"), WaitFor("allow-debug-pods"))
}

func TestKyvernoMutations(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":mut\n"), WaitFor("add-managed-by"))
}

func TestKyvernoHealth(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":health\n"), WaitFor("reports-controller-5d4b"))
}

func TestGatekeeperAndKyvernoHealth(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":health\n"), WaitFor("cert expires"))
}

func TestGatekeeperAuditDetailMovesIssues(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":health\n"), WaitFor("OVERALL"),
		Key(tea.KeyTab), Key(tea.KeyTab), Type("d"), WaitFor("Audit issues"))
}

func TestGatekeeperAndKyvernoPolicies(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"))
}

func TestGatekeeperAndKyvernoViolationsInNamespace(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":ns payments\n:v\n"), WaitFor("Violations ["))
}

func TestGatekeeperAndKyvernoHelp(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type("?"), WaitFor("Navigation"))
}

func TestGatekeeperAndKyvernoOverview(t *testing.T) {
	opts := Options(fixtures.Now)
	opts.InitialCommand = ""
	// pods-must-have-app (dryrun) appears in the candidates block once
	// Gatekeeper's constraint informers have synced.
	RequireGolden(t, Run(t, bothEngines(t, true), opts, WaitFor("pods-must-have-app")))
}

func TestGatekeeperAndKyvernoRecentEvents(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":events\n"), WaitFor("no-latest-tag"), WaitFor("Namespace/scratch"))
}

func TestGatekeeperAndKyvernoRecentEventDetail(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":events\n"), WaitFor("no-latest-tag"),
		Key(tea.KeyEnter), WaitFor("Admission event"))
}

func TestGatekeeperImpactTruncated(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":gk/c\n/ns-must-have-owner\n"), WaitFor("/ns-must-have-owner"),
		Type("i"), WaitFor("Showing 2 of 3"))
}

func TestGatekeeperImpactControllerOwner(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":impact pods-must-have-app\n"), WaitFor("Deployment/payments/worker"))
}

func TestKyvernoImpact(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":impact require-team-label\n"), WaitFor("If enforced"))
}

func TestGatekeeperViolationDetailHowToFix(t *testing.T) {
	kyvGolden(t, true, kyvReady, WaitFor("pods-must-have-app"), Type(":v\no/pods-must-have-app\n"), WaitFor("/pods-must-have-app"),
		Type("d"), WaitFor("How to fix"))
}

func TestKyvernoViolationDetailHowToFix(t *testing.T) {
	kyvGolden(t, false, kyvReady, Type(":v\no/disallow-privileged\n"), WaitFor("/disallow-privileged"), Type("d"), WaitFor("How to fix"))
}

func TestKyvernoTenantWithoutPolicyAccess(t *testing.T) {
	cl := kyvtest.NewCluster(t, kyvtest.Options{Forbidden: []string{"clusterpolicies", "policies", "validatingpolicies"}})
	s := newSessionFor(t, cl, []engine.Registration{{Name: kyverno.Name, Factory: kyverno.New}})
	RequireGolden(t, Run(t, s, Options(fixtures.Now), Type(":v\no/require-team-label\n"), WaitFor("/require-team-label"),
		Type("d"), WaitFor("not readable")))
}
