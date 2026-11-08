package platform_test

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit/fakecluster"
	"github.com/yindia/p9s/internal/platform"
)

func obj(m map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: m} }

func TestSnapshotAttributesWebhooksAndCountsEndpoints(t *testing.T) {
	resources := fakecluster.With(fakecluster.ResourcesFor("apps/v1", "deployments:Deployment:ns"), fakecluster.PlatformResources...)
	cl := fakecluster.New(t, resources,
		obj(map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration",
			"metadata": map[string]any{"name": "by-label", "labels": map[string]any{"owner": "eng"}},
			"webhooks": []any{map[string]any{"name": "w1", "clientConfig": map[string]any{"service": map[string]any{"namespace": "elsewhere", "name": "svc"}}}}}),
		obj(map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration",
			"metadata": map[string]any{"name": "by-namespace"},
			"webhooks": []any{
				map[string]any{"name": "w2", "failurePolicy": "Ignore", "clientConfig": map[string]any{"service": map[string]any{"namespace": "eng", "name": "svc"}}},
			}}),
		obj(map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration",
			"metadata": map[string]any{"name": "someone-else"},
			"webhooks": []any{map[string]any{"name": "w3", "clientConfig": map[string]any{"service": map[string]any{"namespace": "other", "name": "svc"}}}}}),
		obj(map[string]any{"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
			"metadata": map[string]any{"name": "svc-1", "namespace": "eng", "labels": map[string]any{"kubernetes.io/service-name": "svc"}},
			"endpoints": []any{
				map[string]any{"conditions": map[string]any{"ready": true}, "targetRef": map[string]any{"namespace": "eng", "name": "p1"}},
				map[string]any{"conditions": map[string]any{}}, // unknown readiness counts as ready
				map[string]any{"conditions": map[string]any{"ready": false}},
			}}),
		obj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "ctrl", "namespace": "eng"},
			"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "ctrl"}}}}),
	)
	w := platform.New(cl.Dynamic(), []platform.Target{{Engine: "eng", Info: engine.PlatformInfo{
		Namespace: "eng", WebhookSelector: map[string]string{"owner": "eng"}, AuditSource: "x",
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	synced := make(chan struct{}, 4)
	w.Start(ctx, func() {
		select {
		case synced <- struct{}{}:
		default:
		}
	})
	t.Cleanup(func() { w.Stop(); cancel() })
	deadline := time.Now().Add(5 * time.Second)
	for !w.SyncStatus().Synced && time.Now().Before(deadline) {
		<-synced
	}
	s := w.Snapshot()
	names := map[string]int{}
	for _, wh := range s.Webhooks {
		names[wh.Name] = wh.ReadyEndpoints()
	}
	if _, ok := names["w3"]; ok || len(names) != 2 {
		t.Errorf("webhooks = %v; want w1 (label) and w2 (service namespace) only", names)
	}
	if names["w2"] != 2 {
		t.Errorf("w2 ready endpoints = %d, want 2 (ready + unknown)", names["w2"])
	}
	for _, wh := range s.Webhooks {
		if wh.Name == "w1" && wh.EndpointsKnown {
			t.Error("w1's service namespace is not watched: endpoints must be unknown, not none")
		}
		if wh.Name == "w2" && !wh.EndpointsKnown {
			t.Error("w2's endpoints are watched and synced")
		}
	}
	for _, wh := range s.Webhooks {
		if wh.Name == "w1" && (wh.FailurePolicy != "Fail" || wh.TimeoutSecs != 10) {
			t.Errorf("API defaults not applied: %+v", wh)
		}
	}
	if len(s.Controllers) != 1 || s.Controllers[0].Desired != 1 {
		t.Errorf("controllers = %+v; spec.replicas unset defaults to 1", s.Controllers)
	}
}

func TestDeniedEndpointSlicesDoNotBlockSyncOrFakeOutages(t *testing.T) {
	cl := fakecluster.New(t, fakecluster.With(fakecluster.ResourcesFor("apps/v1", "deployments:Deployment:ns"), fakecluster.PlatformResources...),
		obj(map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration",
			"metadata": map[string]any{"name": "cfg"},
			"webhooks": []any{map[string]any{"name": "w", "failurePolicy": "Ignore",
				"clientConfig": map[string]any{"service": map[string]any{"namespace": "eng", "name": "svc"}}}}}))
	cl.Forbid("endpointslices")
	w := platform.New(cl.Dynamic(), []platform.Target{{Engine: "eng", Info: engine.PlatformInfo{Namespace: "eng", AuditSource: "x"}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 4)
	w.Start(ctx, func() {
		select {
		case done <- struct{}{}:
		default:
		}
	})
	t.Cleanup(func() { w.Stop(); cancel() })
	// Any notify can be an ordinary event; wait for sync itself.
	deadline := time.After(5 * time.Second)
	for !w.SyncStatus().Synced {
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("a denied resource blocked sync: %+v", w.SyncStatus())
		}
	}
	issues := w.AccessIssues()
	if len(issues) != 1 || issues[0].Resource != "endpointslices.discovery.k8s.io@eng" {
		t.Errorf("issues = %+v", issues)
	}
	if wh := w.Snapshot().Webhooks; len(wh) != 1 || wh[0].EndpointsKnown {
		t.Errorf("webhook = %+v; endpoints must be unknown", wh)
	}
}
