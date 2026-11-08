package enginekit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/dynamicinformer"
	clienttesting "k8s.io/client-go/testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
)

func pod(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": ns}}}
}

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func TestSharedInformerFallsBackToHomeNamespace(t *testing.T) {
	cl := fakecluster.New(t, fakecluster.ResourcesFor("v1", "pods:Pod:ns"), pod("a", "x"), pod("b", "y"))
	cl.ForbidClusterWide("pods")
	a := enginekit.NewAccess("e")
	a.SetHomeNamespace("a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
	inf := a.SharedInformer(ctx, cl.Dyn, f, podsGVR, func() {}, func() {})
	if inf == nil {
		t.Fatal("no informer: the home namespace is readable")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !inf.HasSynced() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if keys := inf.GetStore().ListKeys(); len(keys) != 1 || keys[0] != "a/x" {
		t.Errorf("cached %v: want only the home namespace", keys)
	}
	issues := a.Issues()
	if len(issues) != 1 || issues[0].OnlyNamespace != "a" || issues[0].Resource != "pods" {
		t.Errorf("issues = %+v: one 'namespace a only' notice", issues)
	}
	if a.Denied("pods") || !a.Scoped("pods") {
		t.Error("a scoped resource is not denied (it must still count for sync)")
	}
	a.Clear(podsGVR)
	if len(a.Issues()) != 0 {
		t.Error("Clear must forget the scoped notice too")
	}
}

func TestHomeListDeniedLaterIsAFullDenial(t *testing.T) {
	cl := fakecluster.New(t, fakecluster.ResourcesFor("v1", "pods:Pod:ns"), pod("a", "x"))
	var homeLists atomic.Int32
	cl.Dyn.PrependReactor("list", "pods", func(act clienttesting.Action) (bool, runtime.Object, error) {
		if act.GetNamespace() == "" || homeLists.Add(1) > 1 { // cluster-wide, or after the probe
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil)
		}
		return false, nil, nil
	})
	a := enginekit.NewAccess("e")
	a.SetHomeNamespace("a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inf := a.SharedInformer(ctx, cl.Dyn, dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0), podsGVR, func() {}, func() {})
	if inf == nil {
		t.Fatal("probe passed: want an informer")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !a.Denied("pods") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !a.Denied("pods") {
		t.Error("a denied home watch must be recorded as denied, so it stops blocking sync")
	}
}
