package kube_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/yindia/p9s/internal/kube"
	"github.com/yindia/p9s/internal/model"
)

const kubeconfig = `apiVersion: v1
kind: Config
current-context: dev
clusters:
- name: dev-cluster
  cluster: {server: "https://127.0.0.1:6443"}
- name: prod-cluster
  cluster: {server: "https://10.0.0.1:6443"}
users:
- name: dev-user
  user: {token: fake}
contexts:
- name: dev
  context: {cluster: dev-cluster, user: dev-user, namespace: team-a}
- name: prod
  context: {cluster: prod-cluster, user: dev-user}
`

func writeKubeconfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContexts(t *testing.T) {
	l := kube.NewLoader(kube.Options{Kubeconfig: writeKubeconfig(t, kubeconfig)})
	ctxs, cur, err := l.Contexts()
	if err != nil {
		t.Fatal(err)
	}
	if cur != "dev" || len(ctxs) != 2 {
		t.Fatalf("current %q, %d contexts", cur, len(ctxs))
	}
	want := model.KubeContext{Name: "dev", Cluster: "dev-cluster", User: "dev-user", Namespace: "team-a", Current: true}
	if ctxs[0] != want {
		t.Errorf("ctxs[0] = %+v, want %+v", ctxs[0], want)
	}
}

func TestConnect(t *testing.T) {
	l := kube.NewLoader(kube.Options{Kubeconfig: writeKubeconfig(t, kubeconfig)})
	tests := []struct {
		name, ctx, wantCtx, wantErr string
	}{
		{name: "current context", wantCtx: "dev"},
		{name: "explicit context", ctx: "prod", wantCtx: "prod"},
		{name: "missing context", ctx: "nope", wantErr: `context "nope" not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := l.Connect(tt.ctx)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Context() != tt.wantCtx || c.Dynamic() == nil || c.Discovery() == nil || c.NewInformerFactory() == nil {
				t.Errorf("client not fully built for %s", tt.wantCtx)
			}
		})
	}
}

func TestConnectWithoutCurrentContext(t *testing.T) {
	cfg := strings.Replace(kubeconfig, "current-context: dev\n", "", 1)
	if _, err := kube.NewLoader(kube.Options{Kubeconfig: writeKubeconfig(t, cfg)}).Connect(""); err == nil {
		t.Error("expected an error without current-context")
	}
}

func TestBrokenKubeconfig(t *testing.T) {
	l := kube.NewLoader(kube.Options{Kubeconfig: writeKubeconfig(t, "{not yaml")})
	if _, _, err := l.Contexts(); err == nil {
		t.Error("Contexts: expected error")
	}
	if _, err := l.Connect(""); err == nil {
		t.Error("Connect: expected error")
	}
}

func TestGetResolvesKindsThroughDiscovery(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "api-0", "namespace": "payments"},
	}}
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "payments"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), pod, ns)
	disc := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"get", "list"}},
			{Name: "namespaces", Kind: "Namespace", Verbs: []string{"get", "list"}},
		},
	}}}}
	c := kube.NewClientFrom("test", dyn, disc)

	got, err := c.Get(context.Background(), model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"})
	if err != nil || got.GetName() != "api-0" {
		t.Fatalf("Get pod = %v, %v", got, err)
	}
	if _, err := c.Get(context.Background(), model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "payments"}); err != nil {
		t.Errorf("Get cluster-scoped namespace: %v", err)
	}
	if _, err := c.Get(context.Background(), model.ObjectRef{Group: "nope.io", Version: "v1", Kind: "Widget", Name: "x"}); err == nil {
		t.Error("unknown kind should fail")
	}
}

func TestHomeNamespaceIsOnlyAnExplicitContextNamespace(t *testing.T) {
	l := kube.NewLoader(kube.Options{Kubeconfig: writeKubeconfig(t, kubeconfig)})
	dev, err := l.Connect("dev")
	if err != nil {
		t.Fatal(err)
	}
	if dev.HomeNamespace() != "team-a" {
		t.Errorf("dev home = %q, want team-a", dev.HomeNamespace())
	}
	prod, err := l.Connect("prod")
	if err != nil {
		t.Fatal(err)
	}
	if prod.HomeNamespace() != "" {
		t.Errorf("prod home = %q: no namespace in the context, no fallback (not client-go's \"default\")", prod.HomeNamespace())
	}
}
