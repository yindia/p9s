package gatekeeper_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/gatekeeper"
	"github.com/yindia/p9s/internal/engines/gatekeeper/gktest"
	"github.com/yindia/p9s/internal/model"
)

var deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

// detected returns the engine after Detect read the controller's flags,
// with args replacing the manager's when non-nil.
func detected(t *testing.T, args []any) engine.EventSource {
	t.Helper()
	cl := gktest.NewCluster(t, gktest.Options{})
	if args != nil {
		d := cl.Dyn.Resource(deploymentsGVR).Namespace("gatekeeper-system")
		obj, err := d.Get(context.Background(), "gatekeeper-controller-manager", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		cs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		c := cs[0].(map[string]any)
		c["args"] = args
		_ = unstructured.SetNestedSlice(obj.Object, cs, "spec", "template", "spec", "containers")
		if _, err := d.Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	e := gatekeeper.New(cl)
	if _, _, err := e.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e.(engine.EventSource)
}

func parsed(t *testing.T, src engine.EventSource) map[string]model.AdmissionEvent {
	t.Helper()
	out := map[string]model.AdmissionEvent{}
	for _, ev := range gktest.Load(t, "events.yaml") {
		if e, ok := src.ParseEvent(ev); ok {
			out[ev.GetName()] = e
		}
	}
	return out
}

func TestParseAdmissionEvents(t *testing.T) {
	got := parsed(t, detected(t, nil))
	if len(got) != 2 {
		t.Fatalf("parsed %d events, want the denial and the dry-run (audit and unrelated events skipped): %v", len(got), got)
	}
	deny := got["web-1.18e6141ae915dd5a"]
	want := model.AdmissionEvent{
		Engine: "gatekeeper", Policy: model.PolicyKey{Engine: "gatekeeper", Kind: "K8sRequiredLabels", Name: "ns-must-have-owner"},
		Enforcement: model.Enforcement{Mode: model.ModeEnforce, Raw: "deny"}, User: "alice@example.com",
		Target:  model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "scratch"},
		Message: `you must provide labels: {"owner"}`, Time: deny.Time, Count: 2,
	}
	if deny != want {
		t.Errorf("denial:\n got %+v\nwant %+v", deny, want)
	}
	if deny.Time.Format("15:04") != "11:58" {
		t.Errorf("time = %s, want lastTimestamp", deny.Time)
	}
	dry := got["web-2.18e6141ae915aaaa"]
	if dry.Enforcement.Mode != model.ModeAudit || dry.Target.Namespace != "payments" || dry.Count != 1 {
		t.Errorf("dry-run = %+v: want audit mode on payments/worker-7d9f", dry)
	}
}

func TestEventSelectors(t *testing.T) {
	sels := detected(t, nil).EventSelectors()
	if len(sels) != 3 {
		t.Fatalf("selectors = %v: want one per admission reason", sels)
	}
	for _, s := range sels {
		if s.Namespace != "gatekeeper-system" {
			t.Errorf("selector %v: events live in Gatekeeper's namespace by default", s)
		}
	}
	inv := detected(t, []any{"--admission-events-involved-namespace", "--emit-admission-events"}).EventSelectors()
	if inv[0].Namespace != "" {
		t.Errorf("with --admission-events-involved-namespace events are in the resource's namespace: %v", inv)
	}
}

func TestEventsEnabledFollowsTheFlag(t *testing.T) {
	cases := []struct {
		args []any
		on   bool
	}{
		{nil, true}, // fixture: --emit-admission-events=true
		{[]any{"--emit-admission-events"}, true},
		{[]any{"--emit-admission-events", "true"}, true},
		{[]any{"--emit-admission-events", "--operation=webhook"}, true},                 // bare flag, then another
		{[]any{"--emit-admission-events=true", "--emit-admission-events=false"}, false}, // last wins
		{[]any{"--emit-admission-events=false"}, false},
		{[]any{"--operation=webhook"}, false},
	}
	for _, c := range cases {
		on, hint := detected(t, c.args).EventsEnabled()
		if on != c.on || (!on && hint == "") {
			t.Errorf("args %v: EventsEnabled = %v %q, want %v (with a hint when off)", c.args, on, hint, c.on)
		}
	}
}

func TestEventsOffIsAHealthHint(t *testing.T) {
	src := detected(t, []any{})
	e := src.(engine.Engine)
	issues, err := e.Health()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range issues {
		if i.Category == "events" && i.Severity == model.SeverityInfo {
			return
		}
	}
	t.Errorf("Health() = %+v: want an info item about admission events", issues)
}
