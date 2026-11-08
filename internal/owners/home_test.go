package owners_test

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/owners"
	"github.com/yindia/p9s/internal/query"
)

// clusterWideOnly denies list requests across all namespaces.
func clusterWideOnly(resource string) clienttesting.ReactionFunc {
	deny := forbidden(resource)
	return func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != "" {
			return false, nil, nil
		}
		return deny(a)
	}
}

func TestTenantResolvesOwnersInHomeNamespace(t *testing.T) {
	cl := fakecluster.New(t, nil)
	cl.SeedOwned(t, fakecluster.DeploymentPods("a", "web", "web-5c9d", nil, "web-5c9d-x")...)
	cl.SeedOwned(t, fakecluster.DeploymentPods("b", "api", "api-1", nil, "api-1-y")...)
	for _, r := range []string{"pods", "replicasets", "deployments", "namespaces"} {
		cl.Meta.PrependReactor("list", r, clusterWideOnly(r))
	}
	res := owners.New(cl.Meta)
	res.SetHomeNamespace("a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res.Start(ctx, func() {})
	defer res.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for !res.SyncStatus().Synced && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !res.SyncStatus().Synced {
		t.Fatalf("not synced: %+v", res.SyncStatus())
	}
	got := query.ResolveOwner(res, model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "a", Name: "web-5c9d-x"})
	if got.Kind != "Deployment" || got.Name != "web" {
		t.Errorf("home namespace owner = %+v, want Deployment/a/web", got)
	}
	if got := query.ResolveOwner(res, model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "b", Name: "api-1-y"}); got.Kind != "Pod" {
		t.Errorf("other namespace = %+v: not readable, stays the pod", got)
	}
	scoped, denied := 0, 0
	for _, ai := range res.AccessIssues() {
		if ai.OnlyNamespace == "a" {
			scoped++
		} else {
			denied++ // namespaces: cluster-scoped, no fallback
		}
	}
	if scoped != 3 || denied != 1 {
		t.Errorf("access issues %+v: want pods, replicasets, deployments scoped to a; namespaces denied", res.AccessIssues())
	}
}
