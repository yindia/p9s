package kyverno_test

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engine/enginetest"
	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
	"github.com/yindia/p9s/internal/engines/kyverno"
	"github.com/yindia/p9s/internal/engines/kyverno/kyvtest"
	"github.com/yindia/p9s/internal/model"
)

func TestContract(t *testing.T) {
	var cl *fakecluster.Cluster
	enginetest.Run(t, enginetest.Harness{
		New: func(t *testing.T) engine.Engine {
			cl = kyvtest.NewCluster(t, kyvtest.Options{})
			return kyverno.New(cl)
		},
		Informers: func(*testing.T) dynamicinformer.DynamicSharedInformerFactory {
			return dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
		},
		Events: func(t *testing.T) []*unstructured.Unstructured { return kyvtest.Load(t, "events.yaml") },
		NotInstalled: func(t *testing.T) engine.Engine {
			return kyverno.New(kyvtest.NewCluster(t, kyvtest.Options{NoKyverno: true}))
		},
	})
}

func started(t *testing.T, cl *fakecluster.Cluster) engine.Engine {
	t.Helper()
	e := kyverno.New(cl)
	ctx, cancel := context.WithCancel(context.Background())
	f := dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
	if err := e.Start(ctx, f, func() {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Start(ctx.Done())
	f.WaitForCacheSync(ctx.Done())
	t.Cleanup(func() { e.Stop(); cancel(); f.Shutdown() })
	return e
}

func policies(t *testing.T, e engine.Engine) map[string]model.Policy {
	t.Helper()
	ps, err := e.Policies()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Policy{}
	for _, p := range ps {
		out[p.Key().String()] = p
	}
	return out
}

func TestDetect(t *testing.T) {
	installed, version, err := kyverno.New(kyvtest.NewCluster(t, kyvtest.Options{})).Detect(context.Background())
	if err != nil || !installed || version != "v1.19.2" {
		t.Errorf("Detect() = %v, %q, %v; want true, v1.19.2", installed, version, err)
	}
	installed, _, err = kyverno.New(kyvtest.NewCluster(t, kyvtest.Options{NoKyverno: true})).Detect(context.Background())
	if err != nil || installed {
		t.Errorf("Detect() without kyverno = %v, %v", installed, err)
	}
}

func TestPoliciesAcrossBothAPIs(t *testing.T) {
	ps := policies(t, started(t, kyvtest.NewCluster(t, kyvtest.Options{})))
	tests := []struct {
		key        string
		mode       model.EnforcementMode
		raw        string
		violations int
		lang       model.SourceLanguage
	}{
		{"kyverno:ValidatingPolicy/disallow-privileged-containers", model.ModeAudit, "Audit", 1, model.LangCEL},
		{"kyverno:ValidatingPolicy/require-limits", model.ModeEnforce, "Deny", 0, model.LangCEL},
		{"kyverno:NamespacedValidatingPolicy/payments/team-quota", model.ModeWarn, "Warn,Audit", 1, model.LangCEL},
		{"kyverno:MutatingPolicy/add-managed-by", model.ModeMutate, "mutate", 0, model.LangCEL},
		// fail + error results count; the error is also a health issue.
		{"kyverno:ClusterPolicy/require-team-label", model.ModeAudit, "Audit", 2, model.LangYAML},
		// validationFailureAction is the default for rules without failureAction.
		{"kyverno:Policy/kube-system/no-latest-tag", model.ModeEnforce, "Enforce", 1, model.LangYAML},
	}
	if len(ps) != len(tests) {
		t.Errorf("got %d policies, want %d: %v", len(ps), len(tests), keysOf(ps))
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			p, ok := ps[tt.key]
			if !ok {
				t.Fatalf("missing; have %v", keysOf(ps))
			}
			if p.Enforcement.Mode != tt.mode || p.Enforcement.Raw != tt.raw {
				t.Errorf("enforcement = %+v, want %s/%s", p.Enforcement, tt.mode, tt.raw)
			}
			if p.ViolationCount != tt.violations {
				t.Errorf("violations = %d, want %d", p.ViolationCount, tt.violations)
			}
			if len(p.Sources) == 0 || p.Sources[0].Language != tt.lang {
				t.Errorf("sources = %+v", p.Sources)
			}
		})
	}
	if last := ps["kyverno:ValidatingPolicy/disallow-privileged-containers"].LastAudit; last.Format(time.RFC3339) != "2026-11-08T11:59:00Z" {
		t.Errorf("LastAudit = %v: should be the newest result, pass or fail", last)
	}
}

func TestViolationsResolveReportResults(t *testing.T) {
	vs, err := started(t, kyvtest.NewCluster(t, kyvtest.Options{})).Violations()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.Violation{}
	for _, v := range vs {
		got[v.Policy.String()+"|"+v.Target.String()+"|"+v.Enforcement.Raw] = v
	}
	want := []string{
		"kyverno:ValidatingPolicy/disallow-privileged-containers|Pod/payments/api-0|fail",
		"kyverno:ClusterPolicy/require-team-label|Pod/payments/api-0|fail",
		"kyverno:NamespacedValidatingPolicy/payments/team-quota|Pod/payments/api-0|warn",
		"kyverno:Policy/kube-system/no-latest-tag|Pod/kube-system/coredns-6f6b679f8f-x2v9q|fail",
		"kyverno:ClusterPolicy/require-team-label|Namespace/payments|error",
	}
	if len(vs) != len(want) {
		t.Errorf("got %d violations, want %d: %v", len(vs), len(want), keysOfV(got))
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing %s; have %v", w, keysOfV(got))
		}
	}
	v := got["kyverno:ClusterPolicy/require-team-label|Pod/payments/api-0|fail"]
	if v.Rule != "check-team" || v.Enforcement.Mode != model.ModeAudit || v.Target.Version != "v1" {
		t.Errorf("classic violation = %+v", v)
	}
	if cel := got["kyverno:ValidatingPolicy/disallow-privileged-containers|Pod/payments/api-0|fail"]; cel.Rule != "validation 0" {
		t.Errorf("CEL rule = %q, want the validation index", cel.Rule)
	}
	if w := got["kyverno:NamespacedValidatingPolicy/payments/team-quota|Pod/payments/api-0|warn"]; w.Enforcement.Mode != model.ModeWarn {
		t.Errorf("warn result mode = %s", w.Enforcement.Mode)
	}
}

func TestHealth(t *testing.T) {
	hs, _ := started(t, kyvtest.NewCluster(t, kyvtest.Options{})).Health()
	var msgs []string
	for _, h := range hs {
		msgs = append(msgs, string(h.Severity)+" "+h.Category+" "+h.Object.Name+": "+h.Message)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		"error policy require-limits: policy is not ready: failed to compile",
		"warning policy require-limits: WebhookConfigured: compilation failed",
		"error evaluation payments: require-team-label: failed to evaluate",
		`info reports : 1 report results reference policy "deleted-policy"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing health issue %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "disallow-privileged-containers") {
		t.Errorf("ready policy reported unhealthy:\n%s", all)
	}
}

func TestNoReportsIsAHealthHint(t *testing.T) {
	hs, _ := started(t, kyvtest.NewCluster(t, kyvtest.Options{NoReports: true})).Health()
	found := false
	for _, h := range hs {
		found = found || (h.Category == "reports" && strings.Contains(h.Message, "no policy reports yet"))
	}
	if !found {
		t.Error("missing 'no policy reports yet' hint")
	}
}

func TestMutations(t *testing.T) {
	ms, err := started(t, kyvtest.NewCluster(t, kyvtest.Options{})).Mutations()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.Mutation{}
	for _, m := range ms {
		got[m.Kind+"/"+m.Name] = m
	}
	if m := got["ClusterPolicy/require-team-label"]; m.Location != "rule default-pull-policy (patchStrategicMerge)" || m.Targets != "Pod" {
		t.Errorf("classic mutation = %+v", m)
	}
	if m := got["MutatingPolicy/add-managed-by"]; m.Targets != "configmaps" || m.Location != "1 mutation(s)" {
		t.Errorf("CEL mutation = %+v", m)
	}
	if len(ms) != 2 {
		t.Errorf("got %d mutations, want 2", len(ms))
	}
}

func TestViews(t *testing.T) {
	e := started(t, kyvtest.NewCluster(t, kyvtest.Options{}))
	views := map[string]engine.View{}
	for _, v := range e.Views() {
		views[v.Command] = v
	}
	count := func(cmd string, f engine.Filter) int {
		rows, err := views[cmd].Rows(f)
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		return len(rows)
	}
	tests := []struct {
		cmd  string
		f    engine.Filter
		want int
	}{
		{"kyv/cpol", engine.Filter{}, 1},
		{"kyv/pol", engine.Filter{}, 1},
		{"kyv/vpol", engine.Filter{}, 4},
		{"kyv/vpol", engine.Filter{Namespace: "kube-system"}, 3}, // cluster-scoped ones stay
		{"kyv/polr", engine.Filter{}, 3},
		{"kyv/polr", engine.Filter{Namespace: "payments"}, 2}, // its report + the cluster report
		{"kyv/polex", engine.Filter{}, 1},
		{"kyv/polex", engine.Filter{Engine: "gatekeeper"}, 0},
	}
	for _, tt := range tests {
		if got := count(tt.cmd, tt.f); got != tt.want {
			t.Errorf("%s %+v: %d rows, want %d", tt.cmd, tt.f, got, tt.want)
		}
	}

	rows, _ := views["kyv/polr"].Rows(engine.Filter{Namespace: "payments"})
	var api *engine.Row
	for i := range rows {
		if rows[i].Ref != nil && rows[i].Ref.Name == "api-0" {
			api = &rows[i]
		}
	}
	if api == nil {
		t.Fatal("report for api-0 missing or not jumpable")
	}
	d, _ := api.Detail()
	if len(d.Sections) != 2 || !strings.Contains(d.Sections[1].Body, "require-team-label / check-team") {
		t.Errorf("report detail = %+v", d)
	}

	ex, _ := views["kyv/polex"].Rows(engine.Filter{})
	if ex[0].Cells[2].Text != "require-team-label" {
		t.Errorf("exception exempts = %q", ex[0].Cells[2].Text)
	}
}

func TestRBACForbiddenReportsAreNotFatal(t *testing.T) {
	cl := kyvtest.NewCluster(t, kyvtest.Options{Forbidden: []string{"policyreports"}})
	e := started(t, cl)
	issues := e.AccessIssues()
	if len(issues) != 1 || issues[0].Resource != "policyreports.wgpolicyk8s.io" {
		t.Errorf("access issues = %+v", issues)
	}
	ps, _ := e.Policies()
	if len(ps) != 6 {
		t.Errorf("policies still load without reports: got %d", len(ps))
	}
}

func TestLiveUpdates(t *testing.T) {
	cl := kyvtest.NewCluster(t, kyvtest.Options{})
	e := started(t, cl)
	gvr := kyvtest.Resources[1].GVR() // clusterpolicies
	if err := cl.Dyn.Resource(gvr).Delete(context.Background(), "require-team-label", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := policies(t, e)["kyverno:ClusterPolicy/require-team-label"]; !ok {
			// Its report results become orphans rather than violations.
			vs, _ := e.Violations()
			for _, v := range vs {
				if v.Policy.Name == "require-team-label" {
					t.Error("violations of a deleted policy are still listed")
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("deleted policy still visible")
}

func keysOf(m map[string]model.Policy) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfV(m map[string]model.Violation) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
