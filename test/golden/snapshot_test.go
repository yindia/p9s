package golden

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/engines/gatekeeper"
	"github.com/yindia/p9s/internal/engines/gatekeeper/gktest"
	"github.com/yindia/p9s/internal/engines/kyverno"
	"github.com/yindia/p9s/internal/engines/kyverno/kyvtest"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/render"
	"github.com/yindia/p9s/internal/session"
	"github.com/yindia/p9s/internal/snapshot"
	"github.com/yindia/p9s/internal/table"
	. "github.com/yindia/p9s/internal/ui/uitest"
	"github.com/yindia/p9s/internal/views"
)

var bothRegs = []engine.Registration{{Name: kyverno.Name, Factory: kyverno.New}, {Name: gatekeeper.Name, Factory: gatekeeper.New}}

func bothCluster(t *testing.T) engine.Cluster {
	t.Helper()
	objs := append(append(kyvtest.Load(t, "policies.yaml"), kyvtest.Load(t, "reports.yaml")...), kyvtest.Load(t, "platform.yaml")...)
	objs = append(objs, kyvtest.Load(t, "events.yaml")...)
	return gktest.NewCluster(t, gktest.Options{ExtraResources: kyvtest.Resources[1:], Extra: objs})
}

func runOnce(t *testing.T, c session.Connector) *session.Session {
	t.Helper()
	s, err := session.RunOnce(context.Background(), session.Options{Connector: c, Engines: bothRegs, ReadOnly: true}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// record syncs a session through a recorder and returns the written file,
// read back from disk.
func record(t *testing.T, c session.Connector) *snapshot.File {
	t.Helper()
	rec := snapshot.NewRecorder()
	live := runOnce(t, rec.Connector(c))
	f := rec.File(fixtures.Now, "test", model.KubeContext{Name: live.ContextName()})
	p := filepath.Join(t.TempDir(), "snap.json")
	if err := f.WriteFile(p); err != nil {
		t.Fatal(err)
	}
	back, err := snapshot.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	return back
}

func envOf(s *session.Session) views.Env {
	return views.Env{
		Engines: s.Engines, Detections: s.Detections, ContextName: s.ContextName, Contexts: s.Contexts,
		ConnErr: func() error { return nil }, Namespaces: func() ([]string, error) { return nil, nil },
		Scope: func() string { return "" }, Now: func() time.Time { return fixtures.Now },
		Owners: s.Owners, Platform: s.Platform, Events: s.Events, UpdatedAt: s.UpdatedAt,
	}
}

func text(t *testing.T, env views.Env, cmd string) string {
	t.Helper()
	var b bytes.Buffer
	switch cmd {
	case "health":
		_ = render.Dashboard(&b, views.DashboardOf(env))
	case "overview":
		_ = render.Overview(&b, views.OverviewOf(env))
	case views.CmdViolationsFlat: // not a ":" command; o in the TUI
		_ = render.Table(&b, table.Build(views.SharedSpec(env, cmd).Source, engine.Filter{}, env.Now()))
	default:
		spec, f, err := views.Resolve(env, cmd, "")
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		_ = render.Table(&b, table.Build(spec.Source, f, env.Now()))
	}
	return b.String()
}

func TestSnapshotReplaysEveryView(t *testing.T) {
	c := connector{bothCluster(t)}
	live := runOnce(t, c)
	replay := runOnce(t, snapshot.Connector(record(t, c)))
	for _, cmd := range []string{"pol", "v", "v.flat", "events", "mut", "health", "overview", "gk/ct", "gk/c", "gk/cfg",
		"kyv/cpol", "kyv/vpol", "kyv/polr", "kyv/polex"} {
		want, got := text(t, envOf(live), cmd), text(t, envOf(replay), cmd)
		if got != want {
			t.Errorf("%s differs after replay:\n--- live\n%s\n--- replay\n%s", cmd, want, got)
		}
		if strings.TrimSpace(want) == "" {
			t.Errorf("%s: empty view proves nothing", cmd)
		}
	}
}

func TestSnapshotReplaysDenials(t *testing.T) {
	cl := kyvtest.NewCluster(t, kyvtest.Options{Home: "payments",
		Forbidden: []string{"clusterpolicies", "validatingpolicies", "clusterpolicyreports"}, ClusterWideForbidden: []string{"policyreports"}})
	c := connector{cl}
	f := record(t, c)
	if len(f.Denied) == 0 {
		t.Fatal("denials not recorded")
	}
	f.Context.Namespace = "payments" // the tenant's context namespace
	replay := runOnce(t, snapshot.Connector(f))
	live := runOnce(t, c)
	if want, got := text(t, envOf(live), "v.flat"), text(t, envOf(replay), "v.flat"); got != want {
		t.Errorf("tenant view differs:\n--- live\n%s\n--- replay\n%s", want, got)
	}
}

func TestSnapshotHeader(t *testing.T) {
	replay := runOnce(t, snapshot.Connector(record(t, connector{bothCluster(t)})))
	o := Options(fixtures.Now)
	o.SnapshotAt = fixtures.Now
	RequireGolden(t, Run(t, replay, o, WaitFor("snapshot 2026-11-08 12:00Z"), WaitFor("pods-must-have-app")))
}
