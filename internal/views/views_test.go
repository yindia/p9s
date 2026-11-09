package views_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

func env(t *testing.T, scope string) views.Env {
	t.Helper()
	alpha, beta := fixtures.Alpha(), fixtures.Beta()
	es := []engine.Engine{alpha, beta}
	for _, e := range es {
		if err := e.Start(t.Context(), nil, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	return views.Env{
		Engines: func() []engine.Engine { return es },
		Detections: func() []engine.Detection {
			return []engine.Detection{{Engine: alpha, Installed: true, Version: "v1.2.0"}}
		},
		ContextName: func() string { return "kind" },
		Contexts:    func() ([]model.KubeContext, error) { return []model.KubeContext{{Name: "kind", Current: true}}, nil },
		Namespaces:  func() ([]string, error) { return []string{"payments"}, nil },
		Scope:       func() string { return scope },
		Now:         func() time.Time { return fixtures.Now },
	}
}

func TestResolve(t *testing.T) {
	e := env(t, "payments")
	tests := []struct {
		name, args string
		cmd        string
		f          engine.Filter
		err        string
	}{
		{name: "policies", cmd: "pol", f: engine.Filter{Namespace: "payments"}},
		{name: "v", args: "beta", cmd: "v", f: engine.Filter{Engine: "beta", Namespace: "payments"}},
		{name: "ctx", cmd: "ctx"}, // not scoped
		{name: "al/rules", args: "owner", cmd: "al/rules", f: engine.Filter{Engine: "alpha", Text: "owner", Namespace: "payments"}},
		{name: "pol", args: "kyverno", err: `engine "kyverno" is not active`},
		{name: "kyv/cpol", err: `no active engine provides :kyv/cpol (prefix "kyv")`},
		{name: "nope", err: "unknown command :nope"},
		{name: "health", err: ":health is a dashboard"},
	}
	for _, tt := range tests {
		spec, f, err := views.Resolve(e, tt.name, tt.args)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.err)
			}
			continue
		}
		if err != nil || spec.Command() != tt.cmd || f != tt.f {
			t.Errorf("%s: spec %q filter %+v err %v; want %q %+v", tt.name, spec.Command(), f, err, tt.cmd, tt.f)
		}
	}
}

func TestSharedSpecsMaterialise(t *testing.T) {
	e := env(t, "")
	for _, cmd := range []string{views.CmdPolicies, views.CmdViolations, views.CmdMutations, views.CmdHealth, views.CmdContexts, views.CmdNamespaces} {
		spec := views.SharedSpec(e, cmd)
		tbl := table.Build(spec.Source, engine.Filter{}, fixtures.Now)
		if tbl.Command != cmd || tbl.Error != "" || len(tbl.Rows) == 0 || tbl.Partial {
			t.Errorf("%s: %+v", cmd, tbl)
		}
	}
}

func TestTargetAppliesScope(t *testing.T) {
	spec, f, err := views.Target(env(t, "payments"), engine.Target{Command: "v", Filter: engine.Filter{PolicyName: "x"}})
	if err != nil || spec.Command() != "v" || f.Namespace != "payments" || f.PolicyName != "x" {
		t.Errorf("Target = %q %+v %v", spec.Command(), f, err)
	}
	if _, _, err := views.Target(env(t, ""), engine.Target{Command: "gone"}); err == nil {
		t.Error("unknown target must fail")
	}
}

func TestHeaderAndCommands(t *testing.T) {
	e := env(t, "payments")
	h := views.HeaderOf(e)
	if h.Context != "kind" || h.Scope != "payments" || !h.Synced || len(h.Engines) != 1 || h.LastAudit.IsZero() {
		t.Errorf("header = %+v", h)
	}
	if got := views.Complete(e, "he"); got != "health" {
		t.Errorf("Complete(he) = %q", got)
	}
	names := views.CommandNames(e)
	if names[0] != "overview" || names[len(names)-1] != "bt/rules" {
		t.Errorf("CommandNames = %v: shared first, engine views last", names)
	}
	if len(views.HelpCommands()) != 9 || len(views.HelpEngineCommands(e)) != 2 {
		t.Errorf("help commands: %d shared, %d engine", len(views.HelpCommands()), len(views.HelpEngineCommands(e)))
	}
	if c, ok := views.Shared("violations"); !ok || c != "v" {
		t.Error("alias violations -> v")
	}
}

type auditing struct {
	engine.Engine
	interval time.Duration
}

func (a auditing) AuditInterval() time.Duration { return a.interval }

func TestHeaderStalenessUsesEachEnginesInterval(t *testing.T) {
	base := env(t, "")
	es := base.Engines() // alpha last audit 30s ago, beta 10s ago
	tests := []struct {
		name          string
		alpha, beta   time.Duration
		wantEngine    string
		wantStaleness views.Staleness
	}{
		{"both fresh", time.Minute, time.Minute, "alpha", views.Fresh},
		{"stale beyond 2x", 12 * time.Second, time.Hour, "alpha", views.Stale},                    // 30s / 12s = 2.5
		{"overdue beyond 5x", time.Hour, time.Second, "beta", views.Overdue},                      // 10s / 1s = 10
		{"fresh engine does not hide stale one", time.Hour, 3 * time.Second, "beta", views.Stale}, // 10s/3s = 3.3
	}
	for _, tt := range tests {
		e := base
		e.Engines = func() []engine.Engine {
			return []engine.Engine{auditing{es[0], tt.alpha}, auditing{es[1], tt.beta}}
		}
		h := views.HeaderOf(e)
		if h.Audit == nil || h.Audit.Engine != tt.wantEngine || h.Staleness != tt.wantStaleness {
			t.Errorf("%s: audit %+v staleness %s; want %s %s", tt.name, h.Audit, h.Staleness, tt.wantEngine, tt.wantStaleness)
		}
	}
}

func TestCopyHelpers(t *testing.T) {
	pod := model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"}
	dep := model.ObjectRef{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "a b", Name: "web"}
	ns := model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "payments"}
	tests := []struct{ got, want string }{
		{views.CopyRef(pod), "Pod/payments/api-0"},
		{views.CopyRef(ns), "Namespace/payments"},
		{views.KubectlGet(pod, "kind-p9s"), "kubectl --context=kind-p9s get pod/api-0 --namespace=payments -o yaml"},
		{views.KubectlGet(dep, ""), "kubectl get deployment.apps/web --namespace='a b' -o yaml"},
		{views.KubectlGet(model.ObjectRef{Kind: "Pod", Name: "x;rm -rf /"}, "c'tx"), `kubectl --context='c'\''tx' get 'pod/x;rm -rf /' -o yaml`},
		{views.KubectlGet(pod, "-oops"), "kubectl --context=-oops get pod/api-0 --namespace=payments -o yaml"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}

// TestNeverAuditedEngineIsNotHidden: an engine with validating policies but
// no audit at all (its audit pod is down) must win over a fresh engine.
func TestNeverAuditedEngineIsNotHidden(t *testing.T) {
	base := env(t, "")
	es := base.Engines()
	broken := fake.New(fake.Options{Name: "broken", Installed: true, Caps: engine.Capabilities{Policies: true},
		Data: fake.Data{Policies: []model.Policy{{Engine: "broken", Kind: "K", Name: "p", Enforcement: model.Enforcement{Mode: model.ModeEnforce}}}}})
	if err := broken.Start(t.Context(), nil, func() {}); err != nil {
		t.Fatal(err)
	}
	e := base
	e.Engines = func() []engine.Engine {
		return []engine.Engine{auditing{es[0], time.Hour}, auditing{broken, time.Minute}}
	}
	h := views.HeaderOf(e)
	if h.Audit == nil || h.Audit.Engine != "broken" || !h.Audit.Never || h.Staleness != views.Overdue {
		t.Errorf("header audit = %+v, staleness %s; want broken, never, overdue", h.Audit, h.Staleness)
	}
	// Disabled audit (interval 0) is never graded, and a never-audited engine
	// without an interval is left out rather than flagged.
	e.Engines = func() []engine.Engine {
		return []engine.Engine{auditing{es[0], 0}, auditing{broken, 0}}
	}
	if h := views.HeaderOf(e); h.Audit == nil || h.Audit.Engine != "alpha" || h.Staleness != views.Fresh {
		t.Errorf("ungraded header = %+v %s", h.Audit, h.Staleness)
	}
}

func TestOverviewBlocks(t *testing.T) {
	ov := views.OverviewOf(env(t, ""))
	if ov.SchemaVersion == "" || len(ov.Blocks) != 4 {
		t.Fatalf("overview = %+v", ov)
	}
	byID := map[string]views.Block{}
	for _, b := range ov.Blocks {
		byID[b.ID] = b
	}
	// Fake engines run nowhere (no Platform): the dashboard has nothing to grade.
	if h := byID[views.BlockHealth]; h.Summary != "✗ 0 critical · ! 0 warning · ✓ 0 ok" || h.Target.Command != "health" {
		t.Errorf("health block = %+v", h)
	}
	// Without an owner lookup every target is its own owner: three objects.
	top := byID[views.BlockViolators]
	if top.Total != 3 || top.Table.Rows[0].Cells[2].Text != "1" {
		t.Errorf("top owners = %d, rows %+v", top.Total, top.Table.Rows)
	}
	cand := byID[views.BlockCandidates]
	var names []string
	for _, r := range cand.Table.Rows {
		names = append(names, r.Cells[1].Text)
	}
	// Fewest known violations first; the never-audited one (unknown) last.
	if strings.Join(names, ",") != "prefer-limits,no-latest,observe-images" || cand.Target.Filter != "mode:audit,warn" {
		t.Errorf("candidates = %v (filter %q)", names, cand.Target.Filter)
	}
	if !strings.Contains(cand.Summary, "3 in audit or warn mode, 1 with no current violations") {
		t.Errorf("candidate summary = %q", cand.Summary)
	}
	for _, id := range []string{views.BlockDenials} {
		if b := byID[id]; b.Placeholder == "" || b.Target != nil {
			t.Errorf("%s must be a placeholder without a target until its phase: %+v", id, b)
		}
	}
	// Scoped: only payments counts.
	scoped := views.OverviewOf(env(t, "payments"))
	if got := scoped.Blocks[1].Table.Rows; len(got) != 2 {
		t.Errorf("scoped top owners = %+v", got)
	}
}

func TestOverviewIsNotATableCommand(t *testing.T) {
	if c, ok := views.Shared("ov"); !ok || c != views.CmdOverview {
		t.Error(":ov should alias :overview")
	}
	if names := views.CommandNames(env(t, "")); names[0] != "overview" {
		t.Errorf("overview must lead completion: %v", names)
	}
}
