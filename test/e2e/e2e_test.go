//go:build e2e

// Package e2e runs p9s against a real cluster created by hack/e2e-setup.sh.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/yindia/p9s/cmd/p9s/app"
	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	_ "github.com/yindia/p9s/internal/engines/gatekeeper"
	_ "github.com/yindia/p9s/internal/engines/kyverno"
	"github.com/yindia/p9s/internal/kube"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/session"
	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/ui/uitest"
	"github.com/yindia/p9s/internal/views"
)

// --- Connection, engines and the UI ---

// A real API server under a full suite can take longer than a fake.
func init() { uitest.WaitTimeout = 30 * time.Second }

// ready waits, as the first UI step, until the app's own connect finished
// and every cache it restarted is synced again. ui.App.Init reconnects the
// session, so a session synced before Run is not enough: without this, a
// screen captured during the re-sync shows "syncing…".
// Each connect builds new engine instances, so the app's reconnect is
// done once the engines captured here (before Run) have been replaced.
func ready(t *testing.T, s *session.Session) uitest.Step {
	before := map[engine.Engine]bool{}
	for _, e := range s.Engines() {
		before[e] = true
	}
	return uitest.Do(func() {
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			es := s.Engines()
			if len(es) > 0 && !before[es[0]] && s.Synced() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("session not synced after the UI reconnected: %v", s.Pending())
	})
}

func kubeContext() string {
	if c := os.Getenv("E2E_CONTEXT"); c != "" {
		return c
	}
	return "kind-p9s"
}

func connect(t *testing.T) *session.Session {
	t.Helper()
	regs, err := engine.Default.Selection("auto")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(session.Options{
		Connector: session.KubeConnector(kube.NewLoader(kube.Options{Kubeconfig: os.Getenv("KUBECONFIG")})),
		Engines:   regs, Context: kubeContext(), ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.Connect(ctx); err != nil {
		t.Fatalf("connect %s: %v", kubeContext(), err)
	}
	// Tests start from loaded caches (engines, owners, platform, events), so
	// a header still saying "syncing" is a real bug, not a race.
	if err := s.WaitForSync(ctx, session.DefaultSyncTimeout); err != nil {
		t.Fatalf("sync %s: %v", kubeContext(), err)
	}
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// engineNamed returns an active engine or fails.
func engineNamed(t *testing.T, s *session.Session, name string) engine.Engine {
	t.Helper()
	for _, e := range s.Engines() {
		if e.Name() == name {
			return e
		}
	}
	t.Fatalf("engine %s not active (detections: %v)", name, s.Detections())
	return nil
}

func TestBothEnginesDetectedWithVersions(t *testing.T) {
	s := connect(t)
	var names []string
	for _, e := range s.Engines() {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "gatekeeper,kyverno,vap" {
		t.Fatalf("active engines = %v, want gatekeeper, kyverno and vap", names)
	}
	for _, d := range s.Detections() {
		if d.Version == "" || d.Version == "unknown" {
			t.Errorf("%s: version not read from the controller image: %q", d.Engine.Name(), d.Version)
		}
	}
}

func TestLibraryPoliciesAndViolations(t *testing.T) {
	s := connect(t)
	gk := engineNamed(t, s, "gatekeeper")
	eventually(t, "library constraints", func() bool {
		ps, _ := gk.Policies()
		return len(ps) >= 40
	})
	eventually(t, "audit violations", func() bool {
		vs, _ := gk.Violations()
		return len(vs) > 0
	})
	for _, v := range gk.Views() {
		rows, err := v.Rows(engine.Filter{})
		if err != nil || len(rows) == 0 {
			t.Errorf("view %s: %d rows, err %v", v.Command, len(rows), err)
		}
	}
}

func TestJumpToOffendingObject(t *testing.T) {
	s := connect(t)
	gk := engineNamed(t, s, "gatekeeper")
	eventually(t, "violations", func() bool { vs, _ := gk.Violations(); return len(vs) > 0 })
	vs, _ := gk.Violations()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	obj, err := s.GetObject(ctx, vs[0].Target)
	if err != nil {
		t.Fatalf("get %s: %v", vs[0].Target, err)
	}
	if obj.GetName() != vs[0].Target.Name {
		t.Errorf("got %s, want %s", obj.GetName(), vs[0].Target.Name)
	}
}

func TestUIAgainstCluster(t *testing.T) {
	s := connect(t)
	gk := engineNamed(t, s, "gatekeeper")
	eventually(t, "violations", func() bool { vs, _ := gk.Violations(); return len(vs) > 0 })
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s),
		uitest.WaitFor("gatekeeper  "),
		uitest.Type(":gk/ct\n"), uitest.WaitFor("ConstraintTemplates ["),
		uitest.Type(":ns kube-system\n:v\n"), uitest.WaitFor("ns kube-system"),
	)
	for _, want := range []string{"gatekeeper v3", "kyverno v1", "ns kube-system", "Violations ["} {
		if !strings.Contains(screen, want) {
			t.Errorf("final screen missing %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "syncing…") || strings.Contains(screen, " ~") {
		t.Errorf("caches synced long ago, yet the screen shows provisional data:\n%s", screen)
	}
}

func TestKyvernoPoliciesReportsAndResolution(t *testing.T) {
	s := connect(t)
	kyv := engineNamed(t, s, "kyverno")
	eventually(t, "kyverno policies", func() bool { ps, _ := kyv.Policies(); return len(ps) >= 19 })
	eventually(t, "kyverno violations", func() bool { vs, _ := kyv.Violations(); return len(vs) > 0 })

	vs, _ := kyv.Violations()
	kinds := map[string]bool{}
	for _, v := range vs {
		kinds[v.Policy.Kind] = true
		if v.Target.Kind == "" || v.Target.Name == "" {
			t.Errorf("violation without a target: %+v", v)
		}
	}
	// The chart's CEL policies and the classic ClusterPolicy both report.
	for _, k := range []string{"ValidatingPolicy", "ClusterPolicy"} {
		if !kinds[k] {
			t.Errorf("no violations resolved to %s (got %v)", k, kinds)
		}
	}
	ms, _ := kyv.Mutations()
	if len(ms) == 0 {
		t.Error("the classic mutate rule should appear in mutations")
	}
	for _, v := range kyv.Views() {
		if _, err := v.Rows(engine.Filter{}); err != nil {
			t.Errorf("view %s: %v", v.Command, err)
		}
	}
	if hs, _ := kyv.Health(); len(hs) > 0 {
		for _, h := range hs {
			t.Logf("kyverno health: %s %s %s", h.Severity, h.Object, h.Message)
		}
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// --- Native ValidatingAdmissionPolicies ---

func TestVAPEngineOnRealCluster(t *testing.T) {
	s := connect(t)
	e := engineNamed(t, s, "vap")
	ps, err := e.Policies()
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]model.EnforcementMode{}
	for _, p := range ps {
		modes[p.Name] = p.Enforcement.Mode
		if strings.HasPrefix(p.Name, "gatekeeper-") || strings.HasPrefix(p.Name, "vpol-") {
			t.Errorf("generated policy %s listed as the vap engine's", p.Name)
		}
		if p.ViolationsKnown {
			t.Errorf("%s: Kyverno's VAP reports are off on this cluster: no audit source, not a count", p.Name)
		}
	}
	want := map[string]model.EnforcementMode{"p9s-configmap-owner": model.ModeEnforce, "p9s-limitrange-max": model.ModeWarn, "p9s-unbound": model.ModeUnknown}
	for n, m := range want {
		if modes[n] != m {
			t.Errorf("%s mode = %q, want %q", n, modes[n], m)
		}
	}
	hs, _ := e.Health()
	for frag, sev := range map[string]model.Severity{
		`binds policy "p9s-deleted-policy"`: model.SeverityError,
		"not bound":                         model.SeverityWarning,
		"param ConfigMap default/p9s-no-such-limits does not exist and parameterNotFoundAction is Deny": model.SeverityError,
	} {
		found := false
		for _, h := range hs {
			found = found || (strings.Contains(h.Message, frag) && h.Severity == sev)
		}
		if !found {
			t.Errorf("missing %s health item %q in %+v", sev, frag, hs)
		}
	}
	g := e.(engine.Generator)
	gen := g.GeneratedFrom(model.PolicyKey{Engine: "gatekeeper", Kind: "K8sPSPAllowPrivilegeEscalationContainer", Name: "psp-allow-privilege-escalation-container"})
	if len(gen) == 0 || !strings.Contains(gen[0], "gatekeeper-k8spspallowprivilegeescalationcontainer") {
		t.Errorf("generated from the Gatekeeper constraint = %v", gen)
	}
}

func TestVAPTUIOnRealCluster(t *testing.T) {
	s := connect(t)
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s),
		uitest.Type(":vap/pol\n"), uitest.WaitFor("p9s-configmap-owner"), uitest.Type("G"), uitest.WaitFor("showing generated"))
	for _, want := range []string{"gatekeeper-k8spsp", "gatekeeper ConstraintTempla", "p9s-unbound"} {
		if !strings.Contains(screen, want) {
			t.Errorf("vap/pol with G missing %q:\n%s", want, screen)
		}
	}
	detail := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s),
		uitest.Type(":pol\n/psp-allow-privilege-escalation-container\n"), uitest.WaitFor("Policies [1]"),
		uitest.Type("d"), uitest.WaitFor("Summary"), uitest.Type("G"), uitest.WaitFor("Generated admission policies"))
	if !strings.Contains(detail, "gatekeeper-k8spspallowprivilegeescalationcontainer") {
		t.Errorf("constraint detail should name its generated VAP:\n%s", detail)
	}
}

// --- Sorting, header and filters ---

func envFor(s *session.Session) views.Env {
	return views.Env{
		Engines: s.Engines, Detections: s.Detections, ContextName: s.ContextName, Contexts: s.Contexts,
		Namespaces: func() ([]string, error) { return s.Namespaces(context.Background()) },
		Scope:      func() string { return "" },
		Now:        time.Now,
		Owners:     s.Owners,
		Platform:   s.Platform,
		Events:     s.Events,
		UpdatedAt:  s.UpdatedAt,
	}
}

// TestDefaultSortsOnRealData materialises the shared views exactly as
// `p9s get` will and checks their default order.
func TestDefaultSortsOnRealData(t *testing.T) {
	s, err := session.RunOnce(context.Background(), onceOptions(t), session.DefaultSyncTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	env := envFor(s)

	pol := table.Build(views.SharedSpec(env, views.CmdPolicies).Source, engine.Filter{}, time.Now())
	col := pol.ColumnFor(engine.RoleCount)
	prev := int(^uint(0) >> 1)
	for _, r := range pol.Rows {
		n := r.Cells[col].Number
		if n == nil {
			prev = -1 // "-" rows sort after every real count
			continue
		}
		if prev == -1 || *n > prev {
			t.Fatalf("policies not sorted by violations desc at %s (%d after %d)", r.ID, *n, prev)
		}
		prev = *n
	}

	health := table.Build(views.SharedSpec(env, views.CmdHealth).Source, engine.Filter{}, time.Now())
	rank := map[model.Severity]int{model.SeverityError: 0, model.SeverityWarning: 1, model.SeverityInfo: 2}
	last := 0
	sev := health.ColumnFor(engine.RoleSeverity)
	for _, r := range health.Rows {
		if rk := rank[r.Cells[sev].Severity]; rk < last {
			t.Fatalf("health not sorted by severity: %s after a lower severity", r.Cells[sev].Text)
		} else {
			last = rk
		}
	}

	v := table.Build(views.SharedSpec(env, views.CmdViolations).Source, engine.Filter{}, time.Now())
	kyv := v.Match("engine:kyverno ns:kube-system")
	if len(kyv) == 0 {
		t.Fatal("no kyverno violations in kube-system")
	}
	for _, r := range kyv {
		if r.Cells[0].Text != "kyverno" || r.Target.Namespace != "kube-system" {
			t.Errorf("filter leaked %s", r.ID)
		}
	}
	for _, r := range v.Rows {
		if m := r.Cells[v.ColumnFor(engine.RoleMode)].Text; m != "enforce" && m != "warn" && m != "audit" && m != "mutate" && m != "n/a" {
			t.Errorf("violation mode %q is not a normalised word", m)
		}
	}
}

func TestPhase1TUIOnRealData(t *testing.T) {
	s := connect(t)
	eventually(t, "violations", func() bool { vs, _ := engineNamed(t, s, "kyverno").Violations(); return len(vs) > 0 })
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s),
		uitest.WaitFor("gatekeeper  "),
		uitest.Type(":v\n/engine:kyverno\n"), uitest.WaitFor("/engine:kyverno"),
	)
	if !strings.Contains(screen, "last audit ") || !strings.Contains(screen, " ago (") {
		t.Errorf("header should name the stalest engine:\n%s", screen)
	}
	if !strings.Contains(screen, "OBJECTS↓") || !strings.Contains(screen, "» ") {
		t.Errorf("violations should show the default sort arrow and the message status line:\n%s", screen)
	}
	for _, line := range strings.Split(screen, "\n")[4:] {
		if strings.HasPrefix(strings.TrimLeft(line, " ▸"), "gatekeeper") {
			t.Errorf("engine:kyverno filter leaked a gatekeeper row: %q", line)
		}
	}
}

// --- Overview ---

func TestOverviewOnRealData(t *testing.T) {
	s := connect(t)
	eventually(t, "violations", func() bool { vs, _ := engineNamed(t, s, "kyverno").Violations(); return len(vs) > 0 })

	ov := views.OverviewOf(envFor(s))
	byID := map[string]views.Block{}
	for _, b := range ov.Blocks {
		byID[b.ID] = b
	}
	if top := byID[views.BlockViolators]; top.Total == 0 || len(top.Table.Rows) == 0 {
		t.Errorf("real cluster should have violating policies: %+v", top)
	}
	if c := byID[views.BlockCandidates]; c.Total == 0 {
		t.Error("kyverno's restricted PSS policies run in Audit: they must be enforcement candidates")
	}

	opts := uitest.Options(time.Now())
	opts.InitialCommand = "" // the real default
	tests := []struct {
		name  string
		moves string
		want  string
	}{
		{"health", "", "OVERALL"},
		{"violators", "j", "Overview › Violations ["},
		{"candidates", "jj", "/mode:audit,warn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []uitest.Step{uitest.WaitFor("Enforcement candidates")}
			if tt.moves != "" {
				steps = append(steps, uitest.Type(tt.moves))
			}
			steps = append(steps, uitest.Key(tea.KeyEnter), uitest.WaitFor(tt.want))
			screen := uitest.Run(t, s, opts, steps...)
			if !strings.Contains(screen, tt.want) {
				t.Errorf("enter on %s should open %q:\n%s", tt.name, tt.want, screen)
			}
		})
	}
}

// --- Violations by owner ---

const checkoutOwner = "Deployment/p9s-demo/checkout"

// TestReplicasCollapseIntoOneGroup: the demo Deployment's three pods
// violate the same policies; grouped :v shows one row per policy for the
// Deployment, with OBJECTS = 3.
func TestReplicasCollapseIntoOneGroup(t *testing.T) {
	s, err := session.RunOnce(context.Background(), onceOptions(t), session.DefaultSyncTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	env := envFor(s)
	var groups []table.Row
	eventually(t, "the checkout pods to be audited", func() bool {
		tbl := table.Build(views.SharedSpec(env, views.CmdViolations).Source, engine.Filter{}, time.Now())
		groups = groups[:0]
		for _, r := range tbl.Rows {
			if r.Cells[1].Text == checkoutOwner && *r.Cells[5].Number == 3 {
				groups = append(groups, r)
			}
		}
		return len(groups) > 0
	})
	t.Logf("%d policies with all 3 checkout replicas grouped under %s", len(groups), checkoutOwner)

	// Expanding one group lists exactly its pods.
	spec, f, err := views.Target(env, *groups[0].Source.Drill)
	if err != nil {
		t.Fatal(err)
	}
	flat := table.Build(spec.Source, f, time.Now())
	if len(flat.Rows) < 3 {
		t.Fatalf("expanded group has %d rows, want >= 3", len(flat.Rows))
	}
	for _, r := range flat.Rows {
		if r.Target.Namespace != "p9s-demo" || !strings.HasPrefix(r.Target.Name, "checkout-") {
			t.Errorf("expanded group leaked %s", r.Target)
		}
	}

	// --group-label team: the Deployment's own label wins over the namespace's.
	env.GroupLabel = func() string { return "team" }
	byTeam := table.Build(views.ViolationsSpec(env, views.GroupByLabel).Source, engine.Filter{Namespace: "p9s-demo"}, time.Now())
	found := false
	for _, r := range byTeam.Rows {
		found = found || r.Cells[1].Text == "payments"
		if r.Cells[1].Text == "platform" {
			t.Errorf("pods of a team=payments Deployment grouped under the namespace's team: %v", r.ID)
		}
	}
	if !found {
		t.Error("no team=payments group in p9s-demo")
	}
}

// TestOwnerInformerMemory reports the cost of the metadata-only informers.
func TestOwnerInformerMemory(t *testing.T) {
	measure := func(noOwners bool) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		opts := onceOptions(t)
		opts.NoOwners = noOwners
		s, err := session.RunOnce(context.Background(), opts, session.DefaultSyncTimeout)
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		s.Close()
		time.Sleep(200 * time.Millisecond)
		return after.HeapInuse - min(before.HeapInuse, after.HeapInuse)
	}
	var without, with uint64
	for range 2 {
		without += measure(true)
		with += measure(false)
	}
	t.Logf("heap in use after sync: without owner informers %.1f MiB, with %.1f MiB (+%.1f MiB)",
		float64(without/2)/(1<<20), float64(with/2)/(1<<20), (float64(with)-float64(without))/2/(1<<20))
}

// --- Health dashboard ---

func panel(d views.Dashboard, id string) table.Table {
	for _, p := range d.Panels {
		if p.ID == id {
			return p.Table
		}
	}
	return table.Table{}
}

// cell returns the text of the column titled col in a row.
func cell(t table.Table, r table.Row, col string) string {
	for i, c := range t.Columns {
		if c.Title == col && i < len(r.Cells) {
			return r.Cells[i].Text
		}
	}
	return ""
}

func TestHealthDashboardOnRealCluster(t *testing.T) {
	s, err := session.RunOnce(context.Background(), onceOptions(t), session.DefaultSyncTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := views.DashboardOf(envFor(s))

	wh := panel(d, views.PanelWebhooks)
	rows := map[string]table.Row{}
	for _, r := range wh.Rows {
		rows[cell(wh, r, "WEBHOOK")] = r
	}
	if r := rows["fail-open.p9s.example"]; !strings.HasPrefix(cell(wh, r, "STATUS"), "✗ fail-open") {
		t.Errorf("fixture fail-open webhook = %q, want critical", cell(wh, r, "STATUS"))
	}
	if r := rows["cert-expiring.p9s.example"]; !strings.HasPrefix(cell(wh, r, "STATUS"), "! cert expires") {
		t.Errorf("fixture expiring cert = %q, want warning", cell(wh, r, "STATUS"))
	}
	// The real Gatekeeper webhook: fail-open by chart default, but served.
	if r, ok := rows["validation.gatekeeper.sh"]; !ok || cell(wh, r, "FAILURE") != "Ignore" || strings.HasPrefix(cell(wh, r, "ENDPOINTS"), "0/") {
		t.Errorf("gatekeeper webhook row = %+v", r.Cells)
	}
	if _, ok := rows["validate.kyverno.svc-fail"]; !ok {
		t.Error("kyverno's resource webhook is missing")
	}

	ct := panel(d, views.PanelControllers)
	byName := map[string]table.Row{}
	for _, r := range ct.Rows {
		byName[cell(ct, r, "DEPLOYMENT")] = r
	}
	for _, name := range []string{"gatekeeper-controller-manager", "gatekeeper-audit", "kyverno-admission-controller", "kyverno-reports-controller"} {
		r, ok := byName[name]
		if !ok {
			t.Errorf("controller %s missing", name)
			continue
		}
		if ready := cell(ct, r, "READY"); strings.HasPrefix(ready, "0/") {
			t.Errorf("%s ready %s", name, ready)
		}
	}

	au := panel(d, views.PanelAudit)
	for _, r := range au.Rows {
		switch cell(au, r, "ENGINE") {
		case "kyverno":
			if !strings.HasPrefix(cell(au, r, "LEADER"), "kyverno-reports-controller-") {
				t.Errorf("kyverno audit leader = %q", cell(au, r, "LEADER"))
			}
		case "gatekeeper":
			if cell(au, r, "LEADER") != "-" || cell(au, r, "INTERVAL") != "30s" {
				t.Errorf("gatekeeper audit row = %+v (no lease; --audit-interval=30)", r.Cells)
			}
		}
	}
	if len(panel(d, views.PanelPolicies).Rows) != 3 {
		t.Error("gatekeeper, kyverno and vap report policy status")
	}
	if d.Overall.Critical == 0 || d.Overall.Warning == 0 {
		t.Errorf("overall = %+v; the fixtures guarantee a critical and a warning", d.Overall)
	}
}

func TestHealthTUIOnRealCluster(t *testing.T) {
	s := connect(t)
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s), uitest.Type(":health\n"), uitest.WaitFor("fail-open.p9s.example"),
		uitest.Key(0x09), uitest.WaitFor("detail"))
	for _, want := range []string{"OVERALL", "Controllers [", "refreshed"} { // header stays pinned while panels scroll
		if !strings.Contains(screen, want) {
			t.Errorf("health screen missing %q:\n%s", want, screen)
		}
	}
}

// --- Admission events ---

// dryRunDenied sends a server-side dry-run create of a one-replica
// Deployment: nothing is persisted, but the webhooks still run and record
// Events. Gatekeeper's replica-limits (min 3) denies it; Kyverno's audit
// policies record failures for it.
func dryRunDenied(t *testing.T) string {
	t.Helper()
	cl, err := session.KubeConnector(kube.NewLoader(kube.Options{Kubeconfig: os.Getenv("KUBECONFIG")})).Connect(kubeContext())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("p9s-e2e-denied-%d", time.Now().UnixNano()%1_000_000)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": "default"},
		"spec": map[string]any{
			"replicas": int64(1),
			"selector": map[string]any{"matchLabels": map[string]any{"app": name}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": name}},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": "c", "image": "nginx:latest"}}},
			},
		},
	}}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	_, err = cl.Dynamic().Resource(gvr).Namespace("default").Create(context.Background(), obj, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("dry-run create of %s: err = %v, want an admission denial", name, err)
	}
	return name
}

func eventsFor(s *session.Session, target string) []model.AdmissionEvent {
	ev := s.Events()
	if ev == nil {
		return nil
	}
	var out []model.AdmissionEvent
	for _, e := range ev.Events() {
		if e.Target.Name == target {
			out = append(out, e)
		}
	}
	return out
}

func TestAdmissionEventsOnRealCluster(t *testing.T) {
	s := connect(t)
	name := dryRunDenied(t)
	var got []model.AdmissionEvent
	eventually(t, "admission events for "+name+" from both engines", func() bool {
		got = eventsFor(s, name)
		engines := map[string]bool{}
		for _, e := range got {
			engines[e.Engine] = true
		}
		return engines["gatekeeper"] && engines["kyverno"]
	})
	for _, e := range got {
		switch e.Engine {
		case "gatekeeper":
			if e.Policy.Kind == "" || e.Enforcement.Mode != model.ModeEnforce || e.User == "" || e.Message == "" ||
				e.Target != (model.ObjectRef{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "default", Name: name}) {
				t.Errorf("gatekeeper event = %+v: want an enforce denial with user, message and the full target", e)
			}
		case "kyverno":
			if e.Enforcement.Mode != model.ModeAudit || e.Policy.Name == "" || strings.Contains(e.Message, "fail;") {
				t.Errorf("kyverno event = %+v: want an audit-mode result with the rule message only", e)
			}
		}
	}
	for _, st := range s.Events().Statuses() {
		if !st.Enabled {
			t.Errorf("%s: events reported off on a cluster that emits them: %s", st.Engine, st.Hint)
		}
	}
	o := views.OverviewOf(envFor(s))
	for _, b := range o.Blocks {
		if b.ID == views.BlockDenials && (b.Placeholder != "" || b.Total == 0) {
			t.Errorf("denials block = %+v: want the fresh denial counted", b)
		}
	}
}

func TestRecentTabTUIOnRealCluster(t *testing.T) {
	s := connect(t)
	name := dryRunDenied(t)
	eventually(t, "the denial event", func() bool { return len(eventsFor(s, name)) > 0 })
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s), uitest.Type(":v\n"), uitest.WaitFor("Violations"),
		uitest.Type("]"), uitest.WaitFor("Violations · recent"), uitest.Type("/"+name+"\n"), uitest.WaitFor("gatekeeper"))
	for _, want := range []string{"enforce", "kubernetes-admin"} {
		if !strings.Contains(screen, want) {
			t.Errorf("recent tab missing %q:\n%s", want, screen)
		}
	}
}

// --- Enforcement impact ---

// The e2e cluster caps Gatekeeper at constraintViolationsLimit=20; this
// library constraint has more violations than that on kind.
var cappedConstraint = model.PolicyKey{Engine: "gatekeeper", Kind: "K8sContainerRatios", Name: "container-must-meet-memory-and-cpu-ratio"}

func TestImpactOnRealCluster(t *testing.T) {
	s := connect(t)
	im, err := query.ImpactOf(s.Engines(), cappedConstraint, s.Owners())
	if err != nil {
		t.Fatal(err)
	}
	if im.Total <= 20 || im.Listed != 20 || im.Complete {
		t.Fatalf("impact = total %d, listed %d, complete %v: want > 20 counted, the 20 listed, incomplete", im.Total, im.Listed, im.Complete)
	}
	if im.ByManaged[query.ManagedController] == 0 {
		t.Errorf("system pods (kube-system Deployments, DaemonSets) should be controller-managed: %+v", im.ByManaged)
	}
	sum := 0
	for _, n := range im.Namespaces {
		sum += n.Objects
	}
	if sum != im.Objects {
		t.Errorf("namespace totals %d != objects %d", sum, im.Objects)
	}

	// Kyverno reports are complete: listed matches counted.
	keys, err := query.FindPolicies(s.Engines(), "kyverno:ClusterPolicy/require-team-label")
	if err != nil || len(keys) != 1 {
		t.Fatalf("FindPolicies = %v, %v", keys, err)
	}
	ky, err := query.ImpactOf(s.Engines(), keys[0], s.Owners())
	if err != nil {
		t.Fatal(err)
	}
	if !ky.Known || !ky.Complete || ky.Listed != ky.Total || ky.Total == 0 {
		t.Errorf("kyverno impact = total %d listed %d complete %v: reports are complete", ky.Total, ky.Listed, ky.Complete)
	}
}

func TestImpactTUIOnRealCluster(t *testing.T) {
	s := connect(t)
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s),
		uitest.Type(":gk/c\n/"+cappedConstraint.Name+"\n"), uitest.WaitFor("Constraints [1]"),
		uitest.Type("i"), uitest.WaitFor("Showing 20 of"))
	for _, want := range []string{"Already enforcing", "--constraint-violations-limit", "controller-managed", "kube-system"} {
		if !strings.Contains(screen, want) {
			t.Errorf("impact screen missing %q:\n%s", want, screen)
		}
	}
}

// --- Violation detail and namespace-only RBAC ---

// tenantKubeconfig writes a kubeconfig for the p9s-tenant ServiceAccount
// (view on p9s-demo only, from hack/manifests/tenant.yaml) with p9s-demo
// as the context namespace.
func tenantKubeconfig(t *testing.T) string {
	t.Helper()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		rules.ExplicitPath = kc
	}
	raw, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	admin, ok := raw.Contexts[kubeContext()]
	if !ok {
		t.Fatalf("context %s not in kubeconfig", kubeContext())
	}
	cl, err := session.KubeConnector(kube.NewLoader(kube.Options{Kubeconfig: os.Getenv("KUBECONFIG")})).Connect(kubeContext())
	if err != nil {
		t.Fatal(err)
	}
	req := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authentication.k8s.io/v1", "kind": "TokenRequest",
		"metadata": map[string]any{"name": "p9s-tenant", "namespace": "p9s-demo"},
		"spec":     map[string]any{"expirationSeconds": int64(600)},
	}}
	sa := schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	tok, err := cl.Dynamic().Resource(sa).Namespace("p9s-demo").Create(context.Background(), req, metav1.CreateOptions{}, "token")
	if err != nil {
		t.Fatalf("token for p9s-tenant (run hack/e2e-setup.sh): %v", err)
	}
	token, _, _ := unstructured.NestedString(tok.Object, "status", "token")
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kind"] = raw.Clusters[admin.Cluster]
	cfg.AuthInfos["tenant"] = &clientcmdapi.AuthInfo{Token: token}
	cfg.Contexts["tenant"] = &clientcmdapi.Context{Cluster: "kind", AuthInfo: "tenant", Namespace: "p9s-demo"}
	cfg.CurrentContext = "tenant"
	path := filepath.Join(t.TempDir(), "tenant.kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func connectTenant(t *testing.T) *session.Session {
	t.Helper()
	regs, err := engine.Default.Selection("auto")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(session.Options{
		Connector: session.KubeConnector(kube.NewLoader(kube.Options{Kubeconfig: tenantKubeconfig(t)})),
		Engines:   regs, Context: "tenant", ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitForSync(ctx, session.DefaultSyncTimeout); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTenantSeesNamespaceViolations(t *testing.T) {
	s := connectTenant(t)
	ky := engineNamed(t, s, "kyverno")
	vs, err := ky.Violations()
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) == 0 {
		t.Fatal("a tenant with view on p9s-demo must see its PolicyReport violations")
	}
	for _, v := range vs {
		if v.Target.Namespace != "p9s-demo" || !v.PolicyUnreadable || v.Policy.Kind == "" {
			t.Errorf("violation %+v: want p9s-demo targets with an inferred, unreadable policy", v)
		}
	}
	notice := false
	for _, ai := range ky.AccessIssues() {
		notice = notice || (ai.Resource == "policyreports.wgpolicyk8s.io" && ai.OnlyNamespace == "p9s-demo")
	}
	if !notice {
		t.Errorf("access issues %+v: want a notice that reports are read in p9s-demo only", ky.AccessIssues())
	}
}

func TestTenantViolationDetailTUI(t *testing.T) {
	s := connectTenant(t)
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s), uitest.Type(":v\no/disallow-privilege-escalation\n"),
		uitest.WaitFor("disallow-privilege-escalation"), uitest.Type("d"), uitest.WaitFor("How to fix"))
	for _, want := range []string{"not readable with your permissions", "/p9s-demo/", "Owner:    apps/v1 Deployment/p9s-demo/checkout"} {
		if !strings.Contains(screen, want) {
			t.Errorf("tenant detail missing %q:\n%s", want, screen)
		}
	}
}

func TestAdminViolationDetailHasGuidance(t *testing.T) {
	s := connect(t)
	screen := uitest.Run(t, s, uitest.Options(time.Now()), ready(t, s), uitest.Type(":v\no/disallow-privilege-escalation\n"),
		uitest.WaitFor("disallow-privilege-escalation"), uitest.Type("d"), uitest.WaitFor("How to fix"))
	for _, want := range []string{"Disallow Privilege Escalation", "policies.kyverno.io/description"} {
		if !strings.Contains(screen, want) {
			t.Errorf("admin detail missing %q:\n%s", want, screen)
		}
	}
}

// --- p9s get, snapshot and report ---

// p9s runs the real command line and returns stdout.
func p9s(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := app.NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	// An empty config by default, so the developer's own file never leaks
	// in; args come last so a test's --config wins.
	empty := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--context", kubeContext(), "--config", empty}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		base = append(base, "--kubeconfig", kc)
	}
	cmd.SetArgs(append(append([]string{args[0]}, base...), args[1:]...))
	err := cmd.Execute()
	return out.String(), err
}

func TestGetOnRealCluster(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("sort:\n  pol: name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := p9s(t, "get", "pol", "--config", cfg)
	if err != nil {
		t.Fatalf("get pol: %v\n%s", err, out)
	}
	if !strings.Contains(out, "NAME↑") || !strings.Contains(out, "gatekeeper") || !strings.Contains(out, "kyverno") {
		t.Errorf("get pol (sorted by name from the config file):\n%s", out)
	}

	out, err = p9s(t, "get", "v", "-n", "p9s-demo", "--filter", "engine:kyverno", "-o", "json")
	if err != nil {
		t.Fatalf("get v: %v\n%s", err, out)
	}
	var tbl struct {
		SchemaVersion string `json:"schemaVersion"`
		Partial       bool   `json:"partial"`
		Rows          []struct {
			Cells []struct {
				Text string `json:"text"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &tbl); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if tbl.SchemaVersion != "p9s/v1" || tbl.Partial || len(tbl.Rows) == 0 {
		t.Fatalf("table = %+v", tbl)
	}
	for _, r := range tbl.Rows {
		if r.Cells[0].Text != "kyverno" || !strings.Contains(r.Cells[1].Text, "/p9s-demo/") {
			t.Errorf("row %+v: want kyverno rows in p9s-demo", r)
		}
	}

	out, err = p9s(t, "get", "health")
	if err != nil || !strings.Contains(out, "fail-open.p9s.example") || !strings.Contains(out, "OVERALL") {
		t.Errorf("get health: %v\n%s", err, out)
	}
	if _, err := p9s(t, "get", "nope"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("get nope: err = %v", err)
	}
}

// ageless masks ages, which differ between a live run and a replay.
var ageless = regexp.MustCompile(`\b\d+(s|m|h|d)\b|\b\d+m\d+s\b`)

// sameAfterRetries compares a live reading with one from a fresh
// snapshot. A live cluster moves between the two (Gatekeeper audits every
// 30s here), so only a difference that persists across attempts fails;
// the exact comparison lives in test/golden's fake-cluster round trip.
func sameAfterRetries(t *testing.T, what string, read func(snapshot string) (live, replay string)) {
	t.Helper()
	var live, replay string
	for attempt := 0; attempt < 3; attempt++ {
		file := filepath.Join(t.TempDir(), "snap.json")
		if out, err := p9s(t, "snapshot", "-o", file); err != nil {
			t.Fatalf("snapshot: %v\n%s", err, out)
		}
		if live, replay = read(file); live == replay {
			return
		}
	}
	t.Errorf("%s differs between live and snapshot in 3 attempts:\n--- live\n%s\n--- snapshot\n%s", what, live, replay)
}

func TestSnapshotRoundTripOnRealCluster(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snap.json")
	if out, err := p9s(t, "snapshot", "-o", file); err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("snapshot file %v %v: want 0600", st, err)
	}
	b, _ := os.ReadFile(file)
	if strings.Contains(string(b), "managedFields") || strings.Contains(string(b), "last-applied-configuration") {
		t.Error("snapshot carries managedFields or last-applied annotations")
	}
	for _, view := range []string{"pol", "gk/c", "kyv/polr", "health"} {
		sameAfterRetries(t, view, func(snap string) (string, string) {
			live, err := p9s(t, "get", view)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := p9s(t, "get", view, "--snapshot", snap)
			if err != nil {
				t.Fatal(err)
			}
			return ageless.ReplaceAllString(live, "AGE"), ageless.ReplaceAllString(replay, "AGE")
		})
	}
}

func TestReportOnRealCluster(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "r1.json")
	if out, err := p9s(t, "report", "--format", "json", "-o", first); err != nil {
		t.Fatalf("report json: %v\n%s", err, out)
	}
	var set struct {
		SchemaVersion string `json:"schemaVersion"`
		Reports       []struct {
			Header struct {
				Context    string `json:"context"`
				BodySHA256 string `json:"bodySHA256"`
			} `json:"header"`
			PSS struct {
				Controls []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				} `json:"controls"`
			} `json:"pss"`
			Findings []struct {
				Check    string `json:"check"`
				Severity string `json:"severity"`
			} `json:"findings"`
		} `json:"reports"`
	}
	b, _ := os.ReadFile(first)
	if err := json.Unmarshal(b, &set); err != nil || len(set.Reports) != 1 {
		t.Fatalf("json: %v", err)
	}
	r := set.Reports[0]
	if r.Header.Context != kubeContext() || len(r.Header.BodySHA256) != 64 || len(r.PSS.Controls) != 17 {
		t.Errorf("report header/pss = %+v / %d controls", r.Header, len(r.PSS.Controls))
	}
	critical := false
	for _, f := range r.Findings {
		critical = critical || (f.Check == "fail-open.p9s.example" && f.Severity == "critical")
	}
	if !critical {
		t.Errorf("the demo fail-open webhook must be a critical finding: %+v", r.Findings)
	}
	md, err := p9s(t, "report", "--since", first)
	if err != nil || !strings.Contains(md, "## 6. Changes since") || !strings.Contains(md, "## 2. Pod Security Standards coverage") {
		t.Errorf("report --since: %v\n%s", err, md)
	}
	_, err = p9s(t, "report", "--fail-on", "critical")
	var exit *app.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Errorf("--fail-on critical with a fail-open webhook: err = %v, want exit code 2", err)
	}
	html := filepath.Join(dir, "r.html")
	if _, err := p9s(t, "report", "--format", "html", "-o", html); err != nil {
		t.Fatal(err)
	}
	if h, _ := os.ReadFile(html); strings.Contains(string(h), "<script") || !strings.Contains(string(h), "Pod Security Standards") {
		t.Error("html report: want self-contained page with the PSS section")
	}
}

func TestReportFromSnapshotMatchesLive(t *testing.T) {
	strip := regexp.MustCompile("(?m)^\\| (Generated|Snapshot|Body SHA-256) .*\n")
	sameAfterRetries(t, "report", func(snap string) (string, string) {
		replay, err := p9s(t, "report", "--snapshot", snap)
		if err != nil {
			t.Fatal(err)
		}
		live, err := p9s(t, "report")
		if err != nil {
			t.Fatal(err)
		}
		return strip.ReplaceAllString(live, ""), strip.ReplaceAllString(replay, "")
	})
}

// --- Sync and memory ---

func onceOptions(t *testing.T) session.Options {
	t.Helper()
	regs, err := engine.Default.Selection("auto")
	if err != nil {
		t.Fatal(err)
	}
	return session.Options{
		Connector: session.KubeConnector(kube.NewLoader(kube.Options{Kubeconfig: os.Getenv("KUBECONFIG")})),
		Engines:   regs, Context: kubeContext(), ReadOnly: true,
	}
}

func TestRunOnceSyncsBothEngines(t *testing.T) {
	start := time.Now()
	s, err := session.RunOnce(context.Background(), onceOptions(t), session.DefaultSyncTimeout)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	defer s.Close()
	t.Logf("connected and synced in %s", time.Since(start).Round(time.Millisecond))
	if !s.Synced() || len(s.Engines()) != 3 {
		t.Fatalf("synced=%v engines=%d pending=%v", s.Synced(), len(s.Engines()), s.Pending())
	}
	// Synced means complete: counts must already be final, not still rising.
	for _, e := range s.Engines() {
		ps, _ := e.Policies()
		if len(ps) == 0 {
			t.Errorf("%s synced with no policies", e.Name())
		}
	}
}

func TestRunOnceTimeoutNamesPendingResources(t *testing.T) {
	_, err := session.RunOnce(context.Background(), onceOptions(t), time.Nanosecond)
	var te *session.SyncTimeoutError
	if err == nil {
		t.Skip("cluster synced within 1ns; cannot exercise the timeout path")
	}
	if !errorsAs(err, &te) || !strings.Contains(err.Error(), "caches did not sync within") {
		t.Fatalf("err = %v, want a SyncTimeoutError", err)
	}
	t.Logf("timeout error: %v", err)
}

// TestMemoryFootprint compares heap in use after sync with and without the
// cache transform. Numbers go in the PR/commit description.
func TestMemoryFootprint(t *testing.T) {
	measure := func(strip bool) (uint64, int) {
		enginekit.StripFields.Store(strip)
		defer enginekit.StripFields.Store(true)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		s, err := session.RunOnce(context.Background(), onceOptions(t), session.DefaultSyncTimeout)
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		objects := 0
		for _, e := range s.Engines() {
			ps, _ := e.Policies()
			vs, _ := e.Violations()
			objects += len(ps) + len(vs)
		}
		s.Close()
		time.Sleep(200 * time.Millisecond) // let the factory shut down before the next run
		return after.HeapInuse - min(before.HeapInuse, after.HeapInuse), objects
	}
	// A discarded warm-up run lets heap from earlier tests' sessions settle
	// (in a full suite it otherwise lands in whichever run comes first).
	measure(true)
	time.Sleep(time.Second)
	var on, off uint64
	for range 2 { // alternate to even out warm-up effects
		o, n := measure(false)
		off += o
		w, _ := measure(true)
		on += w
		t.Logf("policies+violations visible: %d", n)
	}
	off, on = off/2, on/2
	t.Logf("heap in use after sync: without transform %.1f MiB, with transform %.1f MiB (%.0f%% less)",
		float64(off)/(1<<20), float64(on)/(1<<20), 100*(1-float64(on)/float64(off)))
	// Heap sampling is noisy; only a clear regression fails.
	if float64(on) > 1.1*float64(off) {
		t.Errorf("transform increased memory: %d > %d (+10%% tolerance)", on, off)
	}
}
