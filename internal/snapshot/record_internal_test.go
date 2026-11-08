package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/watch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/model"
)

func TestSanitizeDropsSecretsAndNoise(t *testing.T) {
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "s", "namespace": "a",
			"annotations":   map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{...}", "keep": "me"},
			"managedFields": []any{map[string]any{"manager": "kubectl"}}},
		"data":       map[string]any{"password": "aHVudGVyMg=="},
		"stringData": map[string]any{"token": "plain"},
	}}
	r := NewRecorder()
	r.addObject(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, secret)
	f := r.File(metav1.Now().Time, "test", model.KubeContext{Name: "c"})
	got := f.Objects[0].Items[0]
	if _, ok := got.Object["data"]; ok {
		t.Error("Secret data recorded")
	}
	if _, ok := got.Object["stringData"]; ok {
		t.Error("Secret stringData recorded")
	}
	if len(got.GetManagedFields()) != 0 || got.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != "" || got.GetAnnotations()["keep"] != "me" {
		t.Errorf("metadata = %+v", got.Object["metadata"])
	}
	if _, ok := secret.Object["data"]; !ok {
		t.Error("sanitize must not modify the caller's object")
	}
}

func TestMetadataDropsLastApplied(t *testing.T) {
	r := NewRecorder()
	r.addMeta(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Name: "p", Annotations: map[string]string{lastApplied: "{}", "keep": "me"}, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "x"}},
	}})
	got := r.File(metav1.Now().Time, "test", model.KubeContext{}).Metadata[0].Items[0]
	if _, ok := got.Annotations[lastApplied]; ok || got.Annotations["keep"] != "me" || got.ManagedFields != nil {
		t.Errorf("metadata = %+v", got.ObjectMeta)
	}
}

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func obj(kind, name, rv string, extra map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": "a", "resourceVersion": rv}}
	for k, v := range extra {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func TestEnvAndConfigMapDataAreRedacted(t *testing.T) {
	r := NewRecorder()
	dep := obj("Deployment", "d", "1", map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "c", "args": []any{"--emit-admission-events"},
			"env": []any{map[string]any{"name": "TOKEN", "value": "s3cret"}, map[string]any{"name": "FROM", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "x"}}}}}},
	}}}})
	r.addObject(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, dep)
	r.addObject(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, obj("ConfigMap", "cm", "1", map[string]any{"data": map[string]any{"k": "v"}}))
	f := r.File(metav1.Now().Time, "test", model.KubeContext{})
	b, _ := json.Marshal(f)
	for _, leak := range []string{"s3cret", `"k":"v"`} {
		if strings.Contains(string(b), leak) {
			t.Errorf("snapshot leaks %q", leak)
		}
	}
	for _, keep := range []string{"--emit-admission-events", `"TOKEN"`, "secretKeyRef"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("snapshot lost %q (needed to read engine flags)", keep)
		}
	}
}

func TestOlderAnswersNeverOverwriteNewer(t *testing.T) {
	r := NewRecorder()
	r.addObject(podsGVR, obj("Pod", "p", "20", map[string]any{"spec": "new"}))
	r.addObject(podsGVR, obj("Pod", "p", "10", map[string]any{"spec": "old"})) // a late list
	r.forget(podsGVR, false, obj("Pod", "q", "30", nil))
	r.addObject(podsGVR, obj("Pod", "q", "25", nil)) // listed before its delete
	f := r.File(metav1.Now().Time, "test", model.KubeContext{})
	items := f.Objects[0].Items
	if len(items) != 1 || items[0].GetName() != "p" || items[0].Object["spec"] != "new" {
		t.Errorf("items = %+v: newest version only, deleted object not resurrected", items)
	}
}

func TestSuccessClearsEarlierDenial(t *testing.T) {
	r := NewRecorder()
	r.recordList(podsGVR, "a", nil, apierrors.NewForbidden(podsGVR.GroupResource(), "", nil))
	r.recordList(podsGVR, "", nil, apierrors.NewForbidden(podsGVR.GroupResource(), "", nil))
	r.recordList(podsGVR, "a", &unstructured.UnstructuredList{}, nil)
	f := r.File(metav1.Now().Time, "test", model.KubeContext{})
	if len(f.Denied) != 1 || f.Denied[0].Namespace != "" {
		t.Errorf("denied = %+v: the namespace that later answered is not denied; cluster-wide still is", f.Denied)
	}
}

func TestFrozenAfterFile(t *testing.T) {
	r := NewRecorder()
	r.addObject(podsGVR, obj("Pod", "p", "1", nil))
	_ = r.File(metav1.Now().Time, "test", model.KubeContext{})
	r.addObject(podsGVR, obj("Pod", "late", "2", nil))
	if f := r.File(metav1.Now().Time, "test", model.KubeContext{}); len(f.Objects[0].Items) != 1 {
		t.Error("objects recorded after File changed the snapshot")
	}
}

func TestTeeWatchStopsWithoutReader(t *testing.T) {
	r := NewRecorder()
	fw := watch.NewRaceFreeFake()
	tw := r.tee(fw, podsGVR, false)
	fw.Add(obj("Pod", "p", "1", nil)) // nobody reads tw.ResultChan()
	time.Sleep(20 * time.Millisecond)
	tw.Stop()
	tw.Stop() // twice is fine
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-tw.ResultChan():
			if !open {
				return // the tee goroutine exited
			}
		case <-deadline:
			t.Fatal("result channel still open after Stop: the tee goroutine leaked")
		}
	}
}

func TestWriteFileReplacesAtomicallyWith0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewRecorder().File(metav1.Now().Time, "test", model.KubeContext{Name: "c"})
	if err := f.WriteFile(p); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 even over a 0644 file", st.Mode().Perm())
	}
	if back, err := Read(p); err != nil || back.Context.Name != "c" {
		t.Errorf("read back = %+v, %v", back, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".p9s-snapshot-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}
