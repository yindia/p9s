package owners_test

import (
	"context"
	"testing"
	"time"

	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/owners"
	"github.com/yindia/p9s/internal/query"
)

func started(t *testing.T, forbid ...string) *owners.Resolver {
	t.Helper()
	cl := fakecluster.New(t, nil)
	cl.SeedOwned(t, fakecluster.DeploymentPods("a", "web", "web-5c9d", map[string]string{"team": "web"}, "web-5c9d-x", "web-5c9d-y")...)
	cl.SeedOwned(t, fakecluster.Namespace("a", map[string]string{"team": "platform"}))
	for _, r := range forbid {
		cl.Meta.PrependReactor("list", r, forbidden(r))
	}
	res := owners.New(cl.Meta)
	ctx, cancel := context.WithCancel(context.Background())
	notified := make(chan struct{}, 4)
	res.Start(ctx, func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	t.Cleanup(func() { res.Stop(); cancel() })
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatalf("owners never synced: %+v", res.SyncStatus())
	}
	return res
}

func TestResolverWalksPodToDeployment(t *testing.T) {
	res := started(t)
	pod := model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "a", Name: "web-5c9d-x"}
	got := query.ResolveOwner(res, pod)
	if got.Kind != "Deployment" || got.Name != "web" || got.Group != "apps" || got.Version != "v1" || got.Namespace != "a" {
		t.Errorf("owner = %+v", got)
	}
	if !res.Cached(got) || !res.Cached(model.ObjectRef{Kind: "Namespace", Name: "a"}) {
		t.Error("the deployment and its namespace are cached")
	}
	if _, ok := res.Controller(model.ObjectRef{Kind: "Secret", Namespace: "a", Name: "x"}); ok {
		t.Error("unwatched kinds are not resolvable")
	}
	if !res.SyncStatus().Synced || len(res.AccessIssues()) != 0 {
		t.Errorf("status %+v issues %v", res.SyncStatus(), res.AccessIssues())
	}
}

func TestForbiddenReplicaSetsStopAtDeepestOwner(t *testing.T) {
	res := started(t, "replicasets")
	pod := model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "a", Name: "web-5c9d-x"}
	if got := query.ResolveOwner(res, pod); got.Kind != "ReplicaSet" || got.Name != "web-5c9d" {
		t.Errorf("owner with replicasets forbidden = %+v; want the ReplicaSet", got)
	}
	issues := res.AccessIssues()
	if len(issues) != 1 || issues[0].Resource != "replicasets.apps" {
		t.Errorf("issues = %+v", issues)
	}
	if !res.SyncStatus().Synced {
		t.Error("a forbidden kind must not block sync")
	}
}

func TestOwnerEdgeCases(t *testing.T) {
	cl := fakecluster.New(t, nil)
	cl.SeedOwned(t,
		// A core Deployment and a same-named CRD kind in another group.
		fakecluster.Owned{GVR: fakecluster.Deployments, Kind: "Deployment", Namespace: "a", Name: "web", Labels: map[string]string{"team": "core"}},
		// A pod owned through the legacy extensions group.
		fakecluster.Owned{GVR: fakecluster.ReplicaSets, Kind: "ReplicaSet", Namespace: "a", Name: "web-1", Owner: "Deployment/web", OwnerAPI: "extensions/v1beta1"},
		fakecluster.Owned{GVR: fakecluster.Pods, Kind: "Pod", Namespace: "a", Name: "web-1-x", Owner: "ReplicaSet/web-1", OwnerAPI: "apps/v1"},
		// A mirror pod owned by a Node.
		fakecluster.Owned{GVR: fakecluster.Pods, Kind: "Pod", Namespace: "kube-system", Name: "etcd-n1", Owner: "Node/n1", OwnerAPI: "v1"},
		fakecluster.Namespace("a", map[string]string{"team": "ns"}),
	)
	res := owners.New(cl.Meta)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	res.Start(ctx, func() {
		select {
		case done <- struct{}{}:
		default:
		}
	})
	t.Cleanup(func() { res.Stop(); cancel() })
	<-done

	legacy := query.ResolveOwner(res, model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "a", Name: "web-1-x"})
	if legacy.Group != "apps" || legacy.Kind != "Deployment" || legacy.Name != "web" {
		t.Errorf("legacy extensions owner = %+v; want the apps Deployment", legacy)
	}
	if !res.Cached(legacy) {
		t.Error("the canonicalised owner must be found in the cache")
	}
	if res.Cached(model.ObjectRef{Group: "foo.io", Kind: "Deployment", Namespace: "a", Name: "web"}) {
		t.Error("a CRD kind named Deployment in another group must not match the core Deployment")
	}
	node := query.ResolveOwner(res, model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "kube-system", Name: "etcd-n1"})
	if node.Kind != "Node" || node.Namespace != "" {
		t.Errorf("cluster-scoped owner = %+v; want Node without a namespace", node)
	}
	if !res.Cached(model.ObjectRef{Kind: "Namespace", Namespace: "a", Name: "a"}) {
		t.Error("a Namespace target carrying its own name as namespace must still resolve")
	}
}
