package query_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
)

func started(t *testing.T, es ...*fake.Engine) []engine.Engine {
	t.Helper()
	out := make([]engine.Engine, len(es))
	for i, e := range es {
		if err := e.Start(t.Context(), nil, func() {}); err != nil {
			t.Fatal(err)
		}
		out[i] = e
	}
	return out
}

func TestPoliciesScopeAndLowerBound(t *testing.T) {
	alpha := fixtures.Alpha()
	ps, _ := alpha.Policies()
	vs, _ := alpha.Violations()
	vs[0].Truncated = true
	alpha.SetData(fake.Data{Policies: ps, Violations: vs})
	es := started(t, alpha, fixtures.Beta())

	all, err := query.Policies(es, engine.Filter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("Policies = %d, %v", len(all), err)
	}
	scoped, _ := query.Policies(es, engine.Filter{Namespace: "payments"})
	got := map[string]query.PolicyEntry{}
	for _, p := range scoped {
		got[p.Name] = p
	}
	if p := got["require-owner"]; p.ViolationCount != 1 || !p.LowerBound {
		t.Errorf("require-owner in payments = %+v; want 1 and a lower bound", p)
	}
	if p := got["no-latest"]; p.ViolationCount != 1 || p.LowerBound {
		t.Errorf("no-latest in payments = %+v", p)
	}
	beta, _ := query.Policies(es, engine.Filter{Engine: "beta"})
	if len(beta) != 1 {
		t.Errorf("engine filter: %d policies", len(beta))
	}
}

func TestViolationsMutationsHealthFilter(t *testing.T) {
	es := started(t, fixtures.Alpha(), fixtures.Beta())
	vs, _ := query.Violations(es, engine.Filter{Namespace: "payments"})
	if len(vs) != 2 {
		t.Errorf("violations in payments = %d, want 2 (Namespace/payments + a pod)", len(vs))
	}
	ms, _ := query.Mutations(es, engine.Filter{})
	hs, _ := query.Health(es, engine.Filter{Engine: "beta"})
	if len(ms) != 1 || len(hs) != 0 {
		t.Errorf("mutations = %d, beta health = %d", len(ms), len(hs))
	}
}

func TestNamespacesFallsBackToViolationsWithoutRBAC(t *testing.T) {
	es := started(t, fixtures.Alpha(), fixtures.Beta())
	listed, _ := query.Namespaces(es, []string{"default", "payments"}, nil)
	if len(listed) != 3 || listed[0].Name != query.AllNamespaces || listed[0].Violations != 3 {
		t.Errorf("listed = %+v", listed)
	}
	fallback, _ := query.Namespaces(es, nil, errors.New("forbidden"))
	var names []string
	for _, n := range fallback[1:] {
		names = append(names, n.Name)
	}
	if strings.Join(names, ",") != "payments,search" {
		t.Errorf("fallback namespaces = %v", names)
	}
}

func TestSyncedAndLatestAudit(t *testing.T) {
	slow := fake.New(fake.Options{Name: "slow", Installed: true, Unsynced: true, Caps: fixtures.AllCaps})
	es := started(t, fixtures.Alpha(), slow)
	if query.Synced(es) {
		t.Error("an unsynced engine must make the set unsynced")
	}
	slow.SetSynced(true)
	if !query.Synced(es) {
		t.Error("all synced")
	}
	if got := query.LatestAudit(es); !got.Equal(fixtures.Now.Add(-30e9)) {
		t.Errorf("LatestAudit = %v", got)
	}
}

func TestEmptyReasonOrder(t *testing.T) {
	es := started(t, fixtures.Beta())
	base := query.EmptyInput{Engines: es, ContextName: "kind", Title: "Mutations", Has: query.HasMutations}
	tests := []struct {
		name string
		in   query.EmptyInput
		want string
	}{
		{"not connected", func() query.EmptyInput { b := base; b.ConnErr = errors.New("timeout"); return b }(), "Not connected: timeout"},
		{"no engines", func() query.EmptyInput {
			b := base
			b.Engines = nil
			b.Detections = []engine.Detection{{Engine: fake.New(fake.Options{Name: "gatekeeper"})}}
			return b
		}(), `No supported policy engine detected in context "kind". Looked for: gatekeeper.`},
		{"no capable engine", base, "No active engine supports mutations."},
		{"filtered", func() query.EmptyInput {
			b := base
			b.Has = query.HasPolicies
			b.Filter = engine.Filter{Namespace: "x"}
			return b
		}(), "No mutations for this selection."},
		{"plain", func() query.EmptyInput { b := base; b.Has = query.HasPolicies; return b }(), "No mutations in this context."},
	}
	for _, tt := range tests {
		if got := query.EmptyReason(tt.in); !strings.HasPrefix(got, tt.want) {
			t.Errorf("%s: %q, want prefix %q", tt.name, got, tt.want)
		}
	}
}

func TestAccessNoticesAndCollectErrors(t *testing.T) {
	es := started(t, fixtures.Alpha(), fixtures.Beta())
	if n := query.AccessNotices(es); len(n) != 1 || !strings.Contains(n[0], "beta: forbidden to list") {
		t.Errorf("notices = %v", n)
	}
	broken := fake.New(fake.Options{Name: "broken", Installed: true}) // no capabilities
	got, err := query.Collect([]engine.Engine{broken, es[0]}, engine.Engine.Policies)
	if err == nil || len(got) != 3 {
		t.Errorf("Collect must keep other engines' data and report the failure: %d, %v", len(got), err)
	}
	if !query.IsActive(es, "beta") || query.IsActive(es, "kyverno") {
		t.Error("IsActive")
	}
}

type failingHealth struct{ engine.Engine }

func (failingHealth) Health() ([]model.HealthIssue, error) { return nil, errors.New("boom") }

func TestHealthSummaryNeverCountsUnknownAsOK(t *testing.T) {
	ok := fake.New(fake.Options{Name: "ok", Installed: true, Caps: engine.Capabilities{Health: true}})
	loading := fake.New(fake.Options{Name: "loading", Installed: true, Unsynced: true, Caps: engine.Capabilities{Health: true}})
	broken := fake.New(fake.Options{Name: "broken", Installed: true, Caps: engine.Capabilities{Health: true}})
	es := started(t, ok, loading, broken)
	es[2] = failingHealth{es[2]}
	es = append(es, started(t, fixtures.Alpha())...) // one warning
	s, err := query.SummariseHealth(es, engine.Filter{})
	if err == nil || !strings.Contains(err.Error(), "broken: boom") {
		t.Errorf("err = %v; a failing engine must be reported", err)
	}
	if s.OK != 1 || s.Unknown != 2 || s.Warnings != 1 {
		t.Errorf("summary = %+v; want 1 ok (ok), 2 unknown (loading, broken), 1 warning (alpha)", s)
	}
}
