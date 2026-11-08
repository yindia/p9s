package vap_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engine/enginetest"
	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
	"github.com/yindia/p9s/internal/engines/vap"
	"github.com/yindia/p9s/internal/engines/vap/vaptest"
	"github.com/yindia/p9s/internal/model"
)

func TestContract(t *testing.T) {
	var cl *fakecluster.Cluster
	enginetest.Run(t, enginetest.Harness{
		New: func(t *testing.T) engine.Engine {
			cl = vaptest.NewCluster(t, vaptest.Options{})
			e := vap.New(cl)
			if _, _, err := e.Detect(context.Background()); err != nil {
				t.Fatal(err)
			}
			return e
		},
		Informers: func(*testing.T) dynamicinformer.DynamicSharedInformerFactory {
			return dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
		},
		NotInstalled: func(t *testing.T) engine.Engine { return vap.New(vaptest.NewCluster(t, vaptest.Options{NoVAP: true})) },
		Events:       func(*testing.T) []*unstructured.Unstructured { return nil },
	})
}

func started(t *testing.T, opts vaptest.Options) engine.Engine {
	t.Helper()
	cl := vaptest.NewCluster(t, opts)
	e := vap.New(cl)
	if _, _, err := e.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
	if err := e.Start(ctx, f, func() {}); err != nil {
		t.Fatal(err)
	}
	f.Start(ctx.Done())
	t.Cleanup(func() { e.Stop(); cancel(); f.Shutdown() })
	eventually(t, "sync", func() bool { return e.SyncStatus().Synced })
	return e
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func byName(t *testing.T, e engine.Engine) map[string]model.Policy {
	t.Helper()
	ps, err := e.Policies()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Policy{}
	for _, p := range ps {
		out[p.Name] = p
	}
	return out
}

func TestDetectAndIdentity(t *testing.T) {
	e := vap.New(vaptest.NewCluster(t, vaptest.Options{}))
	ok, v, err := e.Detect(context.Background())
	if !ok || v != "builtin" || err != nil {
		t.Errorf("Detect = %v %q %v", ok, v, err)
	}
	if ok, _, _ := vap.New(vaptest.NewCluster(t, vaptest.Options{NoVAP: true})).Detect(context.Background()); ok {
		t.Error("detected without the API")
	}
}

func TestEnforcementIsTheStrictestBinding(t *testing.T) {
	ps := byName(t, started(t, vaptest.Options{}))
	if p := ps["require-team"]; p.Enforcement.Mode != model.ModeEnforce || p.Enforcement.Raw != "Deny,Warn,Audit" {
		t.Errorf("require-team = %+v: Deny wins over Audit+Warn", p.Enforcement)
	}
	if p := ps["replica-limit"]; p.Enforcement.Mode != model.ModeWarn {
		t.Errorf("replica-limit = %+v", p.Enforcement)
	}
	if p := ps["unbound-policy"]; p.Enforcement.Mode != model.ModeUnknown || p.Enforcement.Raw != "not bound" {
		t.Errorf("unbound = %+v", p.Enforcement)
	}
	if len(ps) != 4 {
		t.Errorf("policies = %v: generated VAPs (Gatekeeper, Kyverno) are not this engine's", ps)
	}
}

func TestViolationsFromVAPReports(t *testing.T) {
	e := started(t, vaptest.Options{})
	ps := byName(t, e)
	if p := ps["require-team"]; !p.ViolationsKnown || p.ViolationCount != 1 {
		t.Errorf("require-team = known %v count %d", p.ViolationsKnown, p.ViolationCount)
	}
	if p := ps["replica-limit"]; !p.ViolationsKnown || p.ViolationCount != 0 {
		t.Errorf("replica-limit: VAP reports exist, a pass is 0: %+v", p)
	}
	if p := ps["unbound-policy"]; p.ViolationsKnown {
		t.Error("an unbound policy is never evaluated: no count")
	}
	vs, _ := e.Violations()
	if len(vs) != 1 || vs[0].Target.Name != "checkout" || vs[0].Target.Kind != "Deployment" {
		t.Errorf("violations = %+v: the generated policy's result is Gatekeeper's", vs)
	}
}

func TestNoReportsMeansNoAuditSource(t *testing.T) {
	for _, p := range byName(t, started(t, vaptest.Options{NoReports: true})) {
		if p.ViolationsKnown {
			t.Errorf("%s: without VAP reports the count is unknown, not 0", p.Name)
		}
	}
}

func TestHealth(t *testing.T) {
	e := started(t, vaptest.Options{})
	var hs []model.HealthIssue
	eventually(t, "param check", func() bool {
		hs, _ = e.Health()
		for _, h := range hs {
			if h.Category == "params" {
				return true
			}
		}
		return false
	})
	want := map[string]model.Severity{
		"binds policy \"deleted-policy\"": model.SeverityError,
		"not bound":                       model.SeverityWarning,
		"no such key: size":               model.SeverityWarning,
		"param ConfigMap default/no-such-limits does not exist and parameterNotFoundAction is Deny": model.SeverityError,
	}
	for frag, sev := range want {
		found := false
		for _, h := range hs {
			found = found || (strings.Contains(h.Message, frag) && h.Severity == sev)
		}
		if !found {
			t.Errorf("missing %s issue %q in %+v", sev, frag, hs)
		}
	}
	for _, h := range hs {
		if strings.Contains(h.Message, "replica-limits") || strings.Contains(h.Object.Name, "vpol-") {
			t.Errorf("unexpected issue %+v: existing params and generated policies are fine", h)
		}
	}
}

func TestGeneratedFromNamesTheSourcePolicy(t *testing.T) {
	g := started(t, vaptest.Options{}).(engine.Generator)
	gk := g.GeneratedFrom(model.PolicyKey{Engine: "gatekeeper", Kind: "K8sRequiredLabels", Name: "ns-must-have-owner"})
	if len(gk) != 1 || !strings.Contains(gk[0], "gatekeeper-k8srequiredlabels") || !strings.Contains(gk[0], "Deny") {
		t.Errorf("gatekeeper constraint = %v", gk)
	}
	ky := g.GeneratedFrom(model.PolicyKey{Engine: "kyverno", Kind: "ValidatingPolicy", Name: "check-labels"})
	if len(ky) != 1 || !strings.Contains(ky[0], "vpol-check-labels") {
		t.Errorf("kyverno policy = %v", ky)
	}
}

func TestGToggleShowsGenerated(t *testing.T) {
	e := started(t, vaptest.Options{})
	var pol engine.View
	for _, v := range e.Views() {
		if v.Command == "vap/pol" {
			pol = v
		}
	}
	rows, _ := pol.Rows(engine.Filter{})
	if len(rows) != 4 {
		t.Fatalf("default rows = %d, want the 4 native policies", len(rows))
	}
	if msg := pol.Keys[0].Do(); !strings.Contains(msg, "showing") {
		t.Errorf("toggle message %q", msg)
	}
	rows, _ = pol.Rows(engine.Filter{})
	if len(rows) != 6 {
		t.Errorf("with G rows = %d, want 6", len(rows))
	}
	for _, r := range rows {
		if strings.HasPrefix(r.ID, "gatekeeper-") && r.Policy != nil {
			t.Error("a generated row must not offer i: its policy is the source engine's")
		}
	}
}

func TestDeniedPoliciesAreANotice(t *testing.T) {
	e := started(t, vaptest.Options{Forbidden: []string{"validatingadmissionpolicies"}})
	if len(e.AccessIssues()) == 0 || !e.SyncStatus().Synced {
		t.Errorf("access %v synced %v", e.AccessIssues(), e.SyncStatus())
	}
}

func TestUserPoliciesAreNotMistakenForGenerated(t *testing.T) {
	if _, ok := byName(t, started(t, vaptest.Options{}))["copied-from-kyverno"]; !ok {
		t.Error("a label or an unrelated owner (notkyverno.io) must not hide a user's policy")
	}
}

func TestPerRequestParamIsNotFlagged(t *testing.T) {
	e := started(t, vaptest.Options{})
	hs, _ := e.Health()
	for _, h := range hs {
		if h.Object.Name == "replica-limit-per-namespace" {
			t.Errorf("a param read from the request's namespace has no single object to check: %+v", h)
		}
	}
}
