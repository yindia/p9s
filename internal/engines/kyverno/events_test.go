package kyverno_test

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
	"github.com/yindia/p9s/internal/engines/kyverno"
	"github.com/yindia/p9s/internal/engines/kyverno/kyvtest"
	"github.com/yindia/p9s/internal/model"
)

func parsedEvents(t *testing.T, src engine.EventSource) map[string]model.AdmissionEvent {
	t.Helper()
	out := map[string]model.AdmissionEvent{}
	for _, ev := range kyvtest.Load(t, "events.yaml") {
		if e, ok := src.ParseEvent(ev); ok {
			out[ev.GetName()] = e
		}
	}
	return out
}

func TestParseAdmissionEvents(t *testing.T) {
	cl := kyvtest.NewCluster(t, kyvtest.Options{})
	src := started(t, cl).(engine.EventSource)
	got := parsedEvents(t, src)
	if len(got) != 2 {
		t.Fatalf("parsed %d events, want the policy-side admission ones (resource-side and scan skipped): %v", len(got), got)
	}
	blocked := got["no-latest-tag.18e615416a4d4a30"]
	want := model.AdmissionEvent{
		Engine: "kyverno", Policy: model.PolicyKey{Engine: "kyverno", Kind: "Policy", Namespace: "kube-system", Name: "no-latest-tag"},
		Enforcement: model.Enforcement{Mode: model.ModeEnforce, Raw: "Resource Blocked"},
		Target:      model.ObjectRef{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "kube-system", Name: "coredns"},
		Message:     "validation error: images must not use latest. rule no-latest failed at path /spec/template/spec/containers/0/image/",
		Time:        blocked.Time, Count: 1,
	}
	if blocked != want {
		t.Errorf("blocked:\n got %+v\nwant %+v", blocked, want)
	}
	if blocked.Time.IsZero() {
		t.Error("time: want eventTime when lastTimestamp is null")
	}
	passed := got["require-team-label.18e615367fb8cff1"]
	if passed.Enforcement.Mode != model.ModeAudit || passed.Message != "validation error: label 'team' is required." {
		t.Errorf("passed = %+v: an admitted failure is audit, with the rule message", passed)
	}
}

func TestEventSelectorsMatchAdmissionOnly(t *testing.T) {
	sels := kyverno.New(kyvtest.NewCluster(t, kyvtest.Options{})).(engine.EventSource).EventSelectors()
	if len(sels) != 1 || sels[0].FieldSelector != "reason=PolicyViolation,source=kyverno-admission" || sels[0].Namespace != "" {
		t.Errorf("selectors = %+v", sels)
	}
}

func TestOmitEventsDisablesAdmissionEvents(t *testing.T) {
	cases := []struct {
		omit string
		on   bool
	}{
		{"", true},
		{"PolicyApplied,PolicySkipped", true},
		{"PolicyApplied,PolicyViolation", false},
		{"PolicyViolationX", true},
	}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	for _, c := range cases {
		cl := kyvtest.NewCluster(t, kyvtest.Options{})
		setOmit(t, cl, gvr, c.omit)
		e := kyverno.New(cl)
		if _, _, err := e.Detect(context.Background()); err != nil {
			t.Fatal(err)
		}
		on, hint := e.(engine.EventSource).EventsEnabled()
		if on != c.on || (!on && hint == "") {
			t.Errorf("--omitEvents=%q: EventsEnabled = %v %q, want %v", c.omit, on, hint, c.on)
		}
	}
}

func setOmit(t *testing.T, cl *fakecluster.Cluster, gvr schema.GroupVersionResource, omit string) {
	t.Helper()
	d := cl.Dyn.Resource(gvr).Namespace("kyverno")
	obj, err := d.Get(context.Background(), "kyverno-admission-controller", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if omit == "" {
		return
	}
	cs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	c := cs[0].(map[string]any)
	c["args"] = []any{"--omitEvents=" + omit}
	_ = unstructured.SetNestedSlice(obj.Object, cs, "spec", "template", "spec", "containers")
	if _, err := d.Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestGuidanceFromAnnotations(t *testing.T) {
	e := started(t, kyvtest.NewCluster(t, kyvtest.Options{}))
	ps := policies(t, e)
	g := ps["kyverno:ValidatingPolicy/disallow-privileged-containers"].Guidance
	if len(g) != 1 || g[0].Title != "Disallow Privileged Containers" || g[0].From != "annotation policies.kyverno.io/description" {
		t.Errorf("guidance = %+v", g)
	}
	if g := ps["kyverno:ClusterPolicy/require-team-label"].Guidance; len(g) != 0 {
		t.Errorf("no annotations, no guidance: %+v", g)
	}
}

func TestResultsKeptWhenPoliciesAreForbidden(t *testing.T) {
	cl := kyvtest.NewCluster(t, kyvtest.Options{Forbidden: []string{"clusterpolicies", "policies", "validatingpolicies"}})
	e := started(t, cl)
	vs, err := e.Violations()
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"ClusterPolicy": true, "Policy": true, "ValidatingPolicy": true}
	kept := 0
	for _, v := range vs {
		if !forbidden[v.Policy.Kind] {
			continue // readable kinds resolve as usual
		}
		kept++
		if !v.PolicyUnreadable || v.Enforcement.Mode != model.ModeUnknown || v.Policy.Name == "" {
			t.Errorf("violation %+v: want an inferred key, unknown mode, PolicyUnreadable", v)
		}
	}
	if kept == 0 {
		t.Fatal("results of forbidden policy kinds dropped: a tenant who can read reports must still see them")
	}
	if len(e.AccessIssues()) == 0 {
		t.Error("want access notices for the forbidden policy kinds")
	}
}

func TestForbiddenPoliciesAreNotOrphans(t *testing.T) {
	e := started(t, kyvtest.NewCluster(t, kyvtest.Options{Forbidden: []string{"clusterpolicies", "policies", "validatingpolicies"}}))
	hs, _ := e.Health()
	for _, h := range hs {
		if h.Category == "reports" && h.Severity == model.SeverityInfo && len(h.Message) > 0 && h.Object.Name == "" &&
			containsAll(h.Message, "reference policy") {
			t.Errorf("a forbidden policy reported as orphan: %s", h.Message)
		}
	}
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

// A tenant of payments: reports and namespaced Policies readable there
// only, cluster-scoped policy kinds not at all.
func tenantCluster(t *testing.T) engine.Engine {
	t.Helper()
	cl := kyvtest.NewCluster(t, kyvtest.Options{Home: "payments",
		Forbidden:            []string{"clusterpolicies", "validatingpolicies", "clusterpolicyreports"},
		ClusterWideForbidden: []string{"policyreports", "policies", "namespacedvalidatingpolicies"},
	})
	return started(t, cl)
}

func TestTenantSeesHomeNamespaceResultsOnly(t *testing.T) {
	e := tenantCluster(t)
	vs, _ := e.Violations()
	if len(vs) == 0 {
		t.Fatal("tenant sees no violations")
	}
	for _, v := range vs {
		if v.Target.Namespace != "payments" {
			t.Errorf("violation outside the home namespace: %+v", v)
		}
	}
	scoped := false
	for _, ai := range e.AccessIssues() {
		scoped = scoped || (ai.Resource == "policyreports.wgpolicyk8s.io" && ai.OnlyNamespace == "payments")
	}
	if !scoped {
		t.Errorf("access issues %+v: want policyreports read in payments only", e.AccessIssues())
	}
}

// Namespaced Policies readable only in payments: a kube-system Policy's
// results are kept as unreadable, not dropped as orphans.
func TestPolicyScopedToHomeHidesOtherNamespaces(t *testing.T) {
	cl := kyvtest.NewCluster(t, kyvtest.Options{Home: "payments", ClusterWideForbidden: []string{"policies"}})
	e := started(t, cl)
	vs, _ := e.Violations()
	found := false
	for _, v := range vs {
		if v.Policy.Kind == "Policy" && v.Policy.Namespace == "kube-system" {
			found = true
			if !v.PolicyUnreadable {
				t.Errorf("%+v: a Policy outside the readable namespace is unreadable", v)
			}
		}
	}
	if !found {
		t.Fatalf("kube-system/no-latest-tag results dropped: %+v", vs)
	}
}
