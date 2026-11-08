package enginekit_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engines/enginekit"
)

func TestSyncGatesAndChecks(t *testing.T) {
	s := enginekit.NewSync()
	if st := s.Status(); st.Synced {
		t.Fatal("not started must not be synced")
	}
	registered := s.Start()
	// Started but still registering: must not look synced, even with no
	// informers tracked yet (the window an RBAC probe could otherwise open).
	if st := s.Status(); st.Synced {
		t.Fatalf("synced while registering: %+v", st)
	}
	open := s.Gate("discovery")
	registered()
	if st := s.Status(); st.Synced || len(st.Pending) != 1 || st.Pending[0] != "discovery" {
		t.Fatalf("pending gate: %+v", st)
	}
	notified := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Watch(ctx, func() { notified <- struct{}{} })
	open()
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("no notify after the gate opened")
	}
	if !s.Status().Synced {
		t.Error("synced after gate opened")
	}

	// A new pending check flips back; completing it notifies again, from the
	// same single watcher.
	reopen := s.Gate("new kind")
	if s.Status().Synced {
		t.Error("a new pending check must unsync")
	}
	reopen()
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("no second notify after re-sync")
	}

	// Denied resources never block sync.
	never := s.Gate("forbidden.example")
	s.SkipWhen(func(name string) bool { return name == "forbidden.example" })
	if !s.Status().Synced {
		t.Error("a skipped (RBAC-denied) resource must not block sync")
	}
	_ = never
	gvr := schema.GroupVersionResource{Group: "g", Version: "v1", Resource: "things"}
	s.Untrack(gvr) // untracking unknown resources is harmless
	s.Reset()
	if s.Status().Synced {
		t.Error("Reset must return to not started")
	}
}

func TestTransformStripsUnusedFields(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "Pod"}}
	u.SetName("p")
	u.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl"}})
	u.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{...}", "keep": "me"})
	out, err := enginekit.Transform(u)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*unstructured.Unstructured)
	if len(got.GetManagedFields()) != 0 {
		t.Error("managedFields kept")
	}
	if ann := got.GetAnnotations(); len(ann) != 1 || ann["keep"] != "me" {
		t.Errorf("annotations = %v", ann)
	}

	only := &unstructured.Unstructured{Object: map[string]any{}}
	only.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"})
	out, _ = enginekit.Transform(only)
	if _, has, _ := unstructured.NestedMap(out.(*unstructured.Unstructured).Object, "metadata", "annotations"); has {
		t.Error("an emptied annotations map should be dropped")
	}

	if v, _ := enginekit.Transform("tombstone"); v != "tombstone" {
		t.Error("non-unstructured values pass through")
	}

	enginekit.StripFields.Store(false)
	defer enginekit.StripFields.Store(true)
	u.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl"}})
	out, _ = enginekit.Transform(u)
	if len(out.(*unstructured.Unstructured).GetManagedFields()) != 1 {
		t.Error("StripFields=false must leave objects untouched")
	}
}

func TestContainerArgForms(t *testing.T) {
	deploy := func(cmd, args []any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "c", "command": cmd, "args": args}},
		}}}}}
	}
	tests := []struct {
		cmd, args []any
		want      string
		ok        bool
	}{
		{nil, []any{"--audit-interval=30"}, "30", true},
		{nil, []any{"--log-level", "INFO", "--audit-interval", "120"}, "120", true},
		{[]any{"/manager", "--audit-interval=0"}, nil, "0", true},
		{nil, []any{"--audit-interval"}, "", false}, // flag with no value
		{nil, []any{"--audit-intervalx=5"}, "", false},
	}
	for _, tt := range tests {
		v, ok := enginekit.ContainerArg(deploy(tt.cmd, tt.args), "--audit-interval")
		if v != tt.want || ok != tt.ok {
			t.Errorf("%v %v: got %q %v, want %q %v", tt.cmd, tt.args, v, ok, tt.want, tt.ok)
		}
	}
}
