package events_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	clienttesting "k8s.io/client-go/testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
	"github.com/yindia/p9s/internal/events"
	"github.com/yindia/p9s/internal/model"
)

// stub claims Events whose reason is "Denied". Its two selectors overlap,
// as they do on a client that ignores field selectors.
type stub struct{ on bool }

func (stub) EventSelectors() []engine.EventSelector {
	return []engine.EventSelector{{Namespace: "sys", FieldSelector: "reason=Denied"}, {Namespace: "sys", FieldSelector: "reason=Denied,type=Warning"}}
}

func (stub) ParseEvent(ev *unstructured.Unstructured) (model.AdmissionEvent, bool) {
	if r, _, _ := unstructured.NestedString(ev.Object, "reason"); r != "Denied" {
		return model.AdmissionEvent{}, false
	}
	ts, _, _ := unstructured.NestedString(ev.Object, "lastTimestamp")
	at, _ := time.Parse(time.RFC3339, ts)
	return model.AdmissionEvent{Engine: "stub", Policy: model.PolicyKey{Engine: "stub", Kind: "Rule", Name: ev.GetName()},
		Target: model.ObjectRef{Kind: "Pod", Namespace: "default", Name: "p"}, Time: at, Count: 1}, true
}

func (s stub) EventsEnabled() (bool, string) {
	if s.on {
		return true, ""
	}
	return false, "turn it on"
}

func event(name, reason, ts string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Event",
		"metadata": map[string]any{"name": name, "namespace": "sys"},
		"reason":   reason, "lastTimestamp": ts, "type": "Warning",
	}}
}

func cluster(t *testing.T, objs ...*unstructured.Unstructured) *fakecluster.Cluster {
	return fakecluster.New(t, fakecluster.ResourcesFor("v1", "events:Event:ns"), objs...)
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

func started(t *testing.T, cl *fakecluster.Cluster, srcs map[string]engine.EventSource) (*events.Watcher, *atomic.Int32) {
	t.Helper()
	w := events.New(cl.Dyn, srcs)
	var n atomic.Int32
	w.Start(context.Background(), func() { n.Add(1) })
	t.Cleanup(w.Stop)
	eventually(t, "sync", func() bool { return w.SyncStatus().Synced })
	return w, &n
}

func TestEventsParsedDedupedNewestFirst(t *testing.T) {
	cl := cluster(t,
		event("old", "Denied", "2026-11-08T10:00:00Z"),
		event("new", "Denied", "2026-11-08T11:00:00Z"),
		event("other", "Scheduled", "2026-11-08T11:30:00Z"),
	)
	w, _ := started(t, cl, map[string]engine.EventSource{"stub": stub{on: true}})
	got := w.Events()
	if len(got) != 2 || got[0].Policy.Name != "new" || got[1].Policy.Name != "old" {
		t.Fatalf("Events() = %+v: want new, old once each", got)
	}
}

func TestNewEventNotifies(t *testing.T) {
	cl := cluster(t)
	w, n := started(t, cl, map[string]engine.EventSource{"stub": stub{on: true}})
	before := n.Load()
	_, err := cl.Dyn.Resource(events.EventsGVR).Namespace("sys").Create(context.Background(),
		event("late", "Denied", "2026-11-08T11:00:00Z"), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new event", func() bool { return len(w.Events()) == 1 && n.Load() > before })
}

func TestDeniedEventsAreANoticeNotABlocker(t *testing.T) {
	cl := cluster(t)
	cl.Forbid("events")
	w, _ := started(t, cl, map[string]engine.EventSource{"stub": stub{on: true}})
	issues := w.AccessIssues()
	if len(issues) != 1 || issues[0].Resource != "events in sys (stub)" || issues[0].Verb != "list" {
		t.Fatalf("AccessIssues() = %+v: want one readable denial per engine and namespace", issues)
	}
	if strings.Contains(issues[0].Resource, "reason=") {
		t.Error("the field selector leaked into the notice")
	}
	if len(w.Events()) != 0 {
		t.Error("events from a denied watch")
	}
}

func TestStatusesAndStop(t *testing.T) {
	cl := cluster(t)
	w, _ := started(t, cl, map[string]engine.EventSource{"b": stub{on: false}, "a": stub{on: true}})
	st := w.Statuses()
	if len(st) != 2 || st[0].Engine != "a" || !st[0].Enabled || st[1].Enabled || st[1].Hint == "" {
		t.Errorf("Statuses() = %+v", st)
	}
	w.Stop()
	w.Stop()
	if w.SyncStatus().Synced || len(w.Events()) != 0 {
		t.Error("after Stop: want no events and not synced")
	}
}

func TestListDeniedAfterProbeDoesNotBlockSync(t *testing.T) {
	cl := cluster(t, event("a", "Denied", "2026-11-08T11:00:00Z"))
	// The probe's list is allowed; every later list and watch is forbidden,
	// as when RBAC changes between the probe and the informer starting.
	var lists atomic.Int32
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", nil)
	cl.Dyn.PrependReactor("list", "events", func(clienttesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) == 1 {
			return false, nil, nil
		}
		return true, nil, forbidden
	})
	cl.Dyn.PrependWatchReactor("events", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, nil, forbidden
	})
	src := map[string]engine.EventSource{"stub": oneSelector{}}
	w := events.New(cl.Dyn, src)
	w.Start(context.Background(), func() {})
	t.Cleanup(w.Stop)
	eventually(t, "sync despite the forbidden list", func() bool { return w.SyncStatus().Synced })
	if len(w.AccessIssues()) != 1 {
		t.Errorf("AccessIssues() = %+v: want the denial", w.AccessIssues())
	}
}

// oneSelector is stub with a single selector, so only one probe runs.
type oneSelector struct{ stub }

func (oneSelector) EventSelectors() []engine.EventSelector {
	return []engine.EventSelector{{Namespace: "sys", FieldSelector: "reason=Denied"}}
}

func TestEventIDIsTheEventObject(t *testing.T) {
	cl := cluster(t, event("a", "Denied", "2026-11-08T11:00:00Z"))
	w, _ := started(t, cl, map[string]engine.EventSource{"stub": stub{on: true}})
	eventually(t, "the event", func() bool { return len(w.Events()) == 1 })
	if id := w.Events()[0].ID; id != "sys/a" {
		t.Errorf("ID = %q, want sys/a", id)
	}
}
