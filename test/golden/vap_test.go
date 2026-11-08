package golden

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/engines/vap"
	"github.com/yindia/p9s/internal/engines/vap/vaptest"
	. "github.com/yindia/p9s/internal/ui/uitest"
)

func vapGolden(t *testing.T, steps ...Step) {
	t.Helper()
	s := newSessionFor(t, vaptest.NewCluster(t, vaptest.Options{}), []engine.Registration{{Name: vap.Name, Factory: vap.New}})
	RequireGolden(t, Run(t, s, Options(fixtures.Now), steps...))
}

// vapReady waits for the rows and for sync (the header leaves "syncing…"
// once the first param round finished; vap has no audit, hence "-").
var vapReady = WaitFor("vap builtin │ last audit -")

func TestVAPPolicies(t *testing.T) {
	vapGolden(t, vapReady, Type(":vap/pol\n"), WaitFor("ValidatingAdmissionPolicies"))
}

func TestVAPShowGenerated(t *testing.T) {
	vapGolden(t, vapReady, Type(":vap/pol\n"), WaitFor("ValidatingAdmissionPolicies"), Type("G"), WaitFor("showing generated"))
}

func TestVAPPolicyDetail(t *testing.T) {
	vapGolden(t, vapReady, Type(":vap/pol\n/require-team\n"), WaitFor("/require-team"), Type("d"), WaitFor("Bindings"))
}

func TestVAPUnboundDetail(t *testing.T) {
	vapGolden(t, vapReady, Type(":vap/pol\n/unbound\n"), WaitFor("/unbound"), Type("d"), WaitFor("Summary"), Type("G"), WaitFor("Not bound"))
}

func TestVAPBindings(t *testing.T) {
	vapGolden(t, vapReady, Type(":vap/b\n"), WaitFor("ValidatingAdmissionPolicyBindings"))
}

func TestVAPDrillToBindings(t *testing.T) {
	vapGolden(t, vapReady, Type(":vap/pol\n/replica\n"), WaitFor("/replica"), Key(tea.KeyEnter), WaitFor("replica-limit-missing"))
}

func TestVAPHealth(t *testing.T) {
	vapGolden(t, vapReady, Type(":health\n"), WaitFor("OVERALL"), WaitFor("vap"))
}

func TestVAPInSharedPolicies(t *testing.T) { vapGolden(t, vapReady, WaitFor("replica-limit")) }
