package views_test

import (
	"testing"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/views"
)

type staticEvents struct {
	list   []model.AdmissionEvent
	synced bool
}

func (s staticEvents) Events() []model.AdmissionEvent    { return s.list }
func (s staticEvents) Statuses() []views.EventStatus     { return nil }
func (s staticEvents) AccessIssues() []model.AccessIssue { return nil }
func (s staticEvents) SyncStatus() engine.SyncStatus     { return engine.SyncStatus{Synced: s.synced} }

func admission(engineName, ns string, mode model.EnforcementMode, ago time.Duration) model.AdmissionEvent {
	return model.AdmissionEvent{Engine: engineName, Policy: model.PolicyKey{Engine: engineName, Kind: "Rule", Name: "p-" + ns},
		Enforcement: model.Enforcement{Mode: mode}, Target: model.ObjectRef{Kind: "Pod", Namespace: ns, Name: "x"},
		Time: fixtures.Now.Add(-ago), Count: 1}
}

func withEvents(t *testing.T, scope string, ev views.Events) views.Env {
	e := env(t, scope)
	e.Events = func() views.Events { return ev }
	return e
}

func TestRecentEventsFilter(t *testing.T) {
	ev := staticEvents{synced: true, list: []model.AdmissionEvent{
		admission("alpha", "payments", model.ModeEnforce, time.Minute),
		admission("beta", "payments", model.ModeWarn, 2*time.Minute),
		admission("alpha", "search", model.ModeEnforce, 3*time.Minute),
	}}
	e := withEvents(t, "payments", ev)
	if got := views.RecentEvents(e, engine.Filter{Namespace: "payments"}); len(got) != 2 {
		t.Errorf("namespace filter: %d events, want 2", len(got))
	}
	if got := views.RecentEvents(e, engine.Filter{Engine: "beta"}); len(got) != 1 || got[0].Engine != "beta" {
		t.Errorf("engine filter: %+v", got)
	}
	spec, f, err := views.Resolve(e, "events", "")
	if err != nil || !views.IsRecent(spec) || f.Namespace != "payments" {
		t.Fatalf("Resolve(events) = %v, %+v, %v: want the scoped recent tab", spec.Command(), f, err)
	}
}

func TestDenialsBlockShowsOnlyBlockedRequests(t *testing.T) {
	ev := staticEvents{synced: true, list: []model.AdmissionEvent{
		admission("alpha", "payments", model.ModeEnforce, time.Minute),
		admission("beta", "payments", model.ModeWarn, 2*time.Minute),
		admission("alpha", "search", model.ModeAudit, 3*time.Minute),
	}}
	o := views.OverviewOf(withEvents(t, "", ev))
	for _, b := range o.Blocks {
		if b.ID != views.BlockDenials {
			continue
		}
		if b.Total != 1 || len(b.Table.Rows) != 1 || b.Target == nil || b.Target.Command != "events" {
			t.Errorf("denials block = total %d, rows %d, target %+v: want the one enforce event, opening :events",
				b.Total, len(b.Table.Rows), b.Target)
		}
		return
	}
	t.Fatal("no denials block")
}

func TestRecentTabWaitsForEventsSync(t *testing.T) {
	e := withEvents(t, "", staticEvents{synced: false})
	if views.HeaderOf(e).Synced {
		t.Error("header synced while the events watcher is loading")
	}
}
