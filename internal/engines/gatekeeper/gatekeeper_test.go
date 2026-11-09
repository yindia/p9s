package gatekeeper_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engine/enginetest"
	"github.com/yindia/p9s/internal/engines/gatekeeper"
	"github.com/yindia/p9s/internal/engines/gatekeeper/gktest"
	"github.com/yindia/p9s/internal/model"
)

const waitFor = 5 * time.Second

func TestContract(t *testing.T) {
	var cl *gktest.Cluster
	enginetest.Run(t, enginetest.Harness{
		New: func(t *testing.T) engine.Engine {
			cl = gktest.NewCluster(t, gktest.Options{})
			return gatekeeper.New(cl)
		},
		Informers: func(*testing.T) dynamicinformer.DynamicSharedInformerFactory {
			return dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
		},
		Events: func(t *testing.T) []*unstructured.Unstructured { return gktest.Load(t, "events.yaml") },
		NotInstalled: func(t *testing.T) engine.Engine {
			return gatekeeper.New(gktest.NewCluster(t, gktest.Options{NoGatekeeper: true}))
		},
	})
}

// started returns a running engine and a counter of notifications.
func started(t *testing.T, cl *gktest.Cluster) (engine.Engine, *atomic.Int32) {
	t.Helper()
	e := gatekeeper.New(cl)
	ctx, cancel := context.WithCancel(context.Background())
	f := dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
	var n atomic.Int32
	if err := e.Start(ctx, f, func() { n.Add(1) }); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Start(ctx.Done())
	t.Cleanup(func() { e.Stop(); cancel(); f.Shutdown() })
	return e, &n
}

// eventually waits for cond, failing with msg on timeout.
func eventually(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func policies(t *testing.T, e engine.Engine) map[string]model.Policy {
	t.Helper()
	ps, err := e.Policies()
	if err != nil {
		t.Fatalf("Policies: %v", err)
	}
	out := map[string]model.Policy{}
	for _, p := range ps {
		out[p.Kind+"/"+p.Name] = p
	}
	return out
}

func waitPolicies(t *testing.T, e engine.Engine, n int) map[string]model.Policy {
	t.Helper()
	eventually(t, "policies", func() bool { ps, _ := e.Policies(); return len(ps) == n })
	return policies(t, e)
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name          string
		opts          gktest.Options
		wantInstalled bool
		wantVersion   string
	}{
		{name: "installed reads image tag", wantInstalled: true, wantVersion: "v3.17.1"},
		{name: "not installed", opts: gktest.Options{NoGatekeeper: true}},
		{name: "deployments forbidden falls back to unknown", opts: gktest.Options{Forbidden: []string{"deployments"}}, wantInstalled: true, wantVersion: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := gktest.NewCluster(t, tt.opts)
			if tt.wantVersion == "unknown" {
				// Get is allowed by the fake; remove the deployment so only list remains.
				_ = cl.Dyn.Resource(gktest.Deployments).Namespace("gatekeeper-system").Delete(context.Background(), "gatekeeper-controller-manager", metav1.DeleteOptions{})
			}
			installed, version, err := gatekeeper.New(cl).Detect(context.Background())
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if installed != tt.wantInstalled || version != tt.wantVersion {
				t.Errorf("Detect() = %v, %q; want %v, %q", installed, version, tt.wantInstalled, tt.wantVersion)
			}
		})
	}
}

func TestPoliciesNormalizeEnforcement(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	ps := waitPolicies(t, e, 3)

	tests := []struct {
		key      string
		mode     model.EnforcementMode
		raw      string
		count    int
		audited  bool
		language model.SourceLanguage
	}{
		{"K8sRequiredLabels/ns-must-have-owner", model.ModeEnforce, "deny", 3, true, model.LangRego},
		{"K8sRequiredLabels/pods-must-have-app", model.ModeAudit, "dryrun", 1, true, model.LangRego},
		{"K8sReplicaLimits/max-ten-replicas", model.ModeWarn, "scoped", 0, false, model.LangCEL},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			p, ok := ps[tt.key]
			if !ok {
				t.Fatalf("policy %s missing; have %v", tt.key, keys(ps))
			}
			if p.Enforcement.Mode != tt.mode || p.Enforcement.Raw != tt.raw {
				t.Errorf("enforcement = %+v, want %s/%s", p.Enforcement, tt.mode, tt.raw)
			}
			if p.ViolationCount != tt.count {
				t.Errorf("ViolationCount = %d, want %d", p.ViolationCount, tt.count)
			}
			if p.LastAudit.IsZero() == tt.audited {
				t.Errorf("LastAudit = %v, audited want %v", p.LastAudit, tt.audited)
			}
			if len(p.Sources) == 0 || p.Sources[0].Language != tt.language {
				t.Errorf("Sources = %+v, want first language %s", p.Sources, tt.language)
			}
			if p.Raw == nil || p.Engine != gatekeeper.Name {
				t.Errorf("Raw/Engine not set: %+v", p)
			}
		})
	}
}

func TestViolationsFromAuditStatus(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	waitPolicies(t, e, 3)
	vs, err := e.Violations()
	if err != nil {
		t.Fatalf("Violations: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("got %d violations, want 3", len(vs))
	}
	var pod *model.Violation
	for i := range vs {
		if vs[i].Target.Kind == "Pod" {
			pod = &vs[i]
		}
	}
	if pod == nil {
		t.Fatal("pod violation missing")
	}
	want := model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "payments", Name: "worker-7d9f"}
	if pod.Target != want {
		t.Errorf("Target = %+v, want %+v", pod.Target, want)
	}
	if pod.Enforcement.Mode != model.ModeAudit || pod.Policy.Name != "pods-must-have-app" || pod.Timestamp.IsZero() {
		t.Errorf("unexpected violation %+v", pod)
	}
}

func TestHealthReportsTemplateProblems(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	waitPolicies(t, e, 3)
	hs, err := e.Health()
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	got := map[string]bool{}
	for _, h := range hs {
		got[h.Category+":"+h.Object.Name] = true
	}
	for _, want := range []string{"template:k8sbroken", "compile:k8sbroken", "audit:max-ten-replicas"} {
		if !got[want] {
			t.Errorf("missing health issue %s; got %v", want, got)
		}
	}
	if got["template:k8srequiredlabels"] {
		t.Error("healthy template reported as unhealthy")
	}
}

func TestViews(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	waitPolicies(t, e, 3)
	views := map[string]engine.View{}
	for _, v := range e.Views() {
		views[v.Command] = v
	}

	t.Run("templates drill into their constraints", func(t *testing.T) {
		eventually(t, "templates", func() bool { r, _ := views["gk/ct"].Rows(engine.Filter{}); return len(r) == 3 })
		rows, _ := views["gk/ct"].Rows(engine.Filter{PolicyKind: "K8sRequiredLabels"})
		if len(rows) != 1 {
			t.Fatalf("got %d rows for kind filter, want 1", len(rows))
		}
		r := rows[0]
		if r.Drill == nil || r.Drill.Command != "gk/c" || r.Drill.Filter.PolicyKind != "K8sRequiredLabels" {
			t.Errorf("Drill = %+v", r.Drill)
		}
		if r.Cells[4].Count != 2 {
			t.Errorf("constraint count = %d, want 2", r.Cells[4].Count)
		}
		d, _ := r.Detail()
		if !hasSection(d, "REGO", "rego") || !hasSection(d, "Parameters schema", "yaml") {
			t.Errorf("template detail sections = %+v", titles(d))
		}
	})

	t.Run("constraints detail shows match, parameters, violations", func(t *testing.T) {
		rows, _ := views["gk/c"].Rows(engine.Filter{PolicyName: "ns-must-have-owner"})
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		d, _ := rows[0].Detail()
		for _, s := range []string{"Match", "Parameters", "Violations (showing 2 of 3; Gatekeeper reports a limited list)"} {
			if !hasSection(d, s, "") {
				t.Errorf("missing section %q in %v", s, titles(d))
			}
		}
		if rows[0].Drill.Command != "v" {
			t.Errorf("constraint should drill to :v, got %+v", rows[0].Drill)
		}
	})

	t.Run("config", func(t *testing.T) {
		eventually(t, "config", func() bool { r, _ := views["gk/cfg"].Rows(engine.Filter{}); return len(r) == 1 })
		rows, _ := views["gk/cfg"].Rows(engine.Filter{})
		if rows[0].Cells[2].Count != 2 || rows[0].Cells[3].Count != 1 {
			t.Errorf("config cells = %+v", rows[0].Cells)
		}
	})
}

func TestNewConstraintKindIsDiscoveredAtRuntime(t *testing.T) {
	cl := gktest.NewCluster(t, gktest.Options{})
	e, _ := started(t, cl)
	waitPolicies(t, e, 3)

	cl.AddConstraintKind("K8sLateKind")
	late := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "constraints.gatekeeper.sh/v1beta1", "kind": "K8sLateKind",
		"metadata": map[string]any{"name": "late"},
	}}
	if _, err := cl.Dyn.Resource(gktest.ConstraintGVR("K8sLateKind")).Create(context.Background(), late, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// A template event is what triggers rediscovery.
	tmpl := gktest.Load(t, "templates.yaml")[0]
	tmpl.SetLabels(map[string]string{"touched": "yes"})
	if _, err := cl.Dyn.Resource(gktest.Templates).Update(context.Background(), tmpl, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ps := waitPolicies(t, e, 4)
	if _, ok := ps["K8sLateKind/late"]; !ok {
		t.Errorf("late constraint missing: %v", keys(ps))
	}
}

func TestLiveUpdatesNotify(t *testing.T) {
	cl := gktest.NewCluster(t, gktest.Options{})
	e, n := started(t, cl)
	waitPolicies(t, e, 3)
	before := n.Load()
	if err := cl.Dyn.Resource(gktest.ConstraintGVR("K8sRequiredLabels")).Delete(context.Background(), "pods-must-have-app", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitPolicies(t, e, 2)
	if n.Load() <= before {
		t.Error("deleting a constraint did not notify")
	}
}

func TestRBACForbiddenIsReportedNotFatal(t *testing.T) {
	cl := gktest.NewCluster(t, gktest.Options{Forbidden: []string{"configs", "k8sreplicalimits"}})
	e, _ := started(t, cl)
	waitPolicies(t, e, 2)
	eventually(t, "access issues", func() bool { return len(e.AccessIssues()) == 2 })
	var res []string
	for _, a := range e.AccessIssues() {
		res = append(res, a.Resource)
		if !strings.Contains(a.Message, "forbidden") {
			t.Errorf("access message %q should carry the API error", a.Message)
		}
	}
	want := "configs.config.gatekeeper.sh,k8sreplicalimits.constraints.gatekeeper.sh"
	if strings.Join(res, ",") != want {
		t.Errorf("access issues = %v, want %s", res, want)
	}
}

func TestNoGatekeeperStartsEmpty(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{NoGatekeeper: true}))
	ps, err := e.Policies()
	if err != nil || len(ps) != 0 {
		t.Errorf("Policies() = %d, %v; want empty, nil", len(ps), err)
	}
	for _, v := range e.Views() {
		if rows, err := v.Rows(engine.Filter{}); err != nil || len(rows) != 0 {
			t.Errorf("view %s: %d rows, %v", v.Command, len(rows), err)
		}
	}
}

func TestUnsupportedFeatures(t *testing.T) {
	e := gatekeeper.New(gktest.NewCluster(t, gktest.Options{}))
	if e.Capabilities().Actions || e.Capabilities().Evaluator {
		t.Error("p9s is read-only and does not evaluate: no actions or evaluator")
	}
}

// Mutators are listed at the version that serves each kind (AssignImage only
// at v1alpha1 on Gatekeeper v3.23.1), with what they set and where.
func TestMutators(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	if !e.Capabilities().Mutations {
		t.Fatal("gatekeeper must advertise mutations")
	}
	var ms []model.Mutation
	eventually(t, "three mutators cached", func() bool {
		ms, _ = e.Mutations()
		return len(ms) == 3
	})
	got := map[string]string{}
	for _, m := range ms {
		got[m.Kind+"/"+m.Name] = m.Location + " | " + m.Targets
	}
	want := map[string]string{
		"Assign/default-pull-policy":  "spec.containers[name:*].imagePullPolicy | Pod",
		"AssignMetadata/owner-label":  "metadata.labels.owner | Deployment, StatefulSet",
		"AssignImage/registry-mirror": "spec.containers[name:*].image | Pod",
	}
	if len(got) != len(want) {
		t.Fatalf("mutations = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func hasSection(d engine.Detail, title, lang string) bool {
	for _, s := range d.Sections {
		if strings.HasPrefix(s.Title, title) && (lang == "" || s.Language == lang) {
			return true
		}
	}
	return false
}

func titles(d engine.Detail) []string {
	var out []string
	for _, s := range d.Sections {
		out = append(out, s.Title)
	}
	return out
}

func keys(m map[string]model.Policy) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestConstraintsViewCountsWithinNamespaceScope(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	waitPolicies(t, e, 3)
	var view engine.View
	for _, v := range e.Views() {
		if v.Command == "gk/c" {
			view = v
		}
	}
	tests := []struct {
		ns   string
		want map[string]int
	}{
		{"", map[string]int{"ns-must-have-owner": 3, "pods-must-have-app": 1}},
		{"payments", map[string]int{"ns-must-have-owner": 1, "pods-must-have-app": 1}},
		{"default", map[string]int{"ns-must-have-owner": 1, "pods-must-have-app": 0}},
	}
	for _, tt := range tests {
		rows, _ := view.Rows(engine.Filter{Namespace: tt.ns})
		for _, r := range rows {
			name := r.Cells[0].Text
			if want, ok := tt.want[name]; ok && r.Cells[3].Count != want {
				t.Errorf("ns %q %s: count %d, want %d", tt.ns, name, r.Cells[3].Count, want)
			}
		}
	}
}

func TestScopedEnforcementUsesAdmissionPoint(t *testing.T) {
	scoped := func(actions ...map[string]any) *unstructured.Unstructured {
		var list []any
		for _, a := range actions {
			list = append(list, a)
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "constraints.gatekeeper.sh/v1beta1", "kind": "K8sRequiredLabels",
			"metadata": map[string]any{"name": "x"},
			"spec":     map[string]any{"enforcementAction": "scoped", "scopedEnforcementActions": list},
		}}
	}
	at := func(action string, points ...string) map[string]any {
		var ps []any
		for _, p := range points {
			ps = append(ps, map[string]any{"name": p})
		}
		return map[string]any{"action": action, "enforcementPoints": ps}
	}
	tests := []struct {
		name string
		obj  *unstructured.Unstructured
		want model.EnforcementMode
	}{
		{"deny only in audit is audit", scoped(at("deny", "audit.gatekeeper.sh")), model.ModeAudit},
		{"deny at admission is enforce", scoped(at("deny", "validation.gatekeeper.sh"), at("dryrun", "audit.gatekeeper.sh")), model.ModeEnforce},
		{"wildcard point counts as admission", scoped(at("warn", "*")), model.ModeWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := gktest.NewCluster(t, gktest.Options{})
			if err := cl.Dyn.Tracker().Create(gktest.ConstraintGVR("K8sRequiredLabels"), tt.obj, ""); err != nil {
				t.Fatal(err)
			}
			e, _ := started(t, cl)
			ps := waitPolicies(t, e, 4)
			if got := ps["K8sRequiredLabels/x"].Enforcement; got.Mode != tt.want || got.Raw != "scoped" {
				t.Errorf("enforcement = %+v, want %s (scoped)", got, tt.want)
			}
		})
	}
}

func TestTruncatedViolationsAreFlagged(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	waitPolicies(t, e, 3)
	vs, _ := e.Violations()
	for _, v := range vs {
		// ns-must-have-owner reports 2 of 3; pods-must-have-app reports 1 of 1.
		want := v.Policy.Name == "ns-must-have-owner"
		if v.Truncated != want {
			t.Errorf("%s on %s: Truncated = %v, want %v", v.Policy.Name, v.Target, v.Truncated, want)
		}
	}
}

func TestStopDuringResyncDoesNotLeakInformers(t *testing.T) {
	cl := gktest.NewCluster(t, gktest.Options{})
	for range 20 {
		e := gatekeeper.New(cl)
		ctx, cancel := context.WithCancel(context.Background())
		f := dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
		if err := e.Start(ctx, f, func() {}); err != nil {
			t.Fatal(err)
		}
		e.Stop() // races the initial resync on purpose
		if ps, _ := e.Policies(); len(ps) != 0 {
			// After Stop no constraint informers may remain registered.
			t.Fatalf("informers survived Stop: %d policies visible", len(ps))
		}
		cancel()
	}
}

func TestAuditIntervalFromAuditDeployment(t *testing.T) {
	audit := func(arg string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "gatekeeper-audit", "namespace": "gatekeeper-system"},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"name": "manager", "image": "openpolicyagent/gatekeeper:v3.17.1", "args": []any{arg}}},
			}}},
		}}
	}
	tests := []struct {
		name string
		arg  string // "" deletes the audit deployment
		want time.Duration
	}{
		{"default when the audit deployment is not visible", "", 60 * time.Second},
		{"parsed", "--audit-interval=30", 30 * time.Second},
		{"zero means audit disabled", "--audit-interval=0", 0},
	}
	for _, tt := range tests {
		cl := gktest.NewCluster(t, gktest.Options{})
		d := cl.Dyn.Resource(gktest.Deployments).Namespace("gatekeeper-system")
		if tt.arg == "" {
			_ = d.Delete(context.Background(), "gatekeeper-audit", metav1.DeleteOptions{})
		} else if _, err := d.Update(context.Background(), audit(tt.arg), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		e := gatekeeper.New(cl)
		if _, _, err := e.Detect(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := e.(engine.Auditor).AuditInterval(); got != tt.want {
			t.Errorf("%s: AuditInterval = %s, want %s", tt.name, got, tt.want)
		}
	}
}
