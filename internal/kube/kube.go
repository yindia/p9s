// Package kube wraps client-go: kubeconfig loading, context switching,
// dynamic and discovery clients, REST mapping and informer factories.
package kube

import (
	"context"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/yindia/p9s/internal/model"
)

const (
	clientQPS   = 50
	clientBurst = 100

	discoveryTimeout = 15 * time.Second
)

// Options select the kubeconfig to load.
type Options struct {
	// Kubeconfig is an explicit path; empty uses KUBECONFIG / ~/.kube/config.
	Kubeconfig string
}

// Loader reads kubeconfig and connects to contexts.
type Loader struct {
	opts Options
}

// NewLoader returns a Loader for the given options.
func NewLoader(opts Options) *Loader { return &Loader{opts: opts} }

func (l *Loader) clientConfig(contextName string) clientcmd.ClientConfig {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if l.opts.Kubeconfig != "" {
		rules.ExplicitPath = l.opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
}

// Contexts lists kubeconfig contexts sorted by name, and the current one.
func (l *Loader) Contexts() ([]model.KubeContext, string, error) {
	raw, err := l.clientConfig("").RawConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	out := make([]model.KubeContext, 0, len(raw.Contexts))
	for name, c := range raw.Contexts {
		out = append(out, model.KubeContext{
			Name:      name,
			Cluster:   c.Cluster,
			User:      c.AuthInfo,
			Namespace: c.Namespace,
			Current:   name == raw.CurrentContext,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, raw.CurrentContext, nil
}

// Connect builds clients for a context. An empty name uses current-context.
func (l *Loader) Connect(contextName string) (*Client, error) {
	cc := l.clientConfig(contextName)
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	if contextName == "" {
		return nil, fmt.Errorf("kubeconfig has no current-context; pass --context")
	}
	if _, ok := raw.Contexts[contextName]; !ok {
		return nil, fmt.Errorf("context %q not found in kubeconfig", contextName)
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build client config for %q: %w", contextName, err)
	}
	cfg.QPS, cfg.Burst = clientQPS, clientBurst
	cfg.UserAgent = "p9s"
	c, err := NewClient(contextName, cfg)
	if err != nil {
		return nil, err
	}
	// Only a namespace the context names: client-go's "default" fallback
	// would send a tenant's fallback watches to a namespace they never chose.
	c.home = raw.Contexts[contextName].Namespace
	return c, nil
}

// Client is a connection to one cluster context.
type Client struct {
	context string
	home    string
	meta    metadata.Interface
	dyn     dynamic.Interface
	disc    discovery.DiscoveryInterface
	mapper  meta.ResettableRESTMapper
}

// NewClient builds clients from a rest config.
func NewClient(contextName string, cfg *rest.Config) (*Client, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	// Discovery calls take no context; a bounded client keeps an unreachable
	// cluster from hanging detection. Watches use the unbounded config.
	dcfg := rest.CopyConfig(cfg)
	dcfg.Timeout = discoveryTimeout
	disc, err := discovery.NewDiscoveryClientForConfig(dcfg)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metadata client: %w", err)
	}
	c := NewClientFrom(contextName, dyn, disc)
	c.meta = meta
	return c, nil
}

// Metadata returns the metadata-only client (nil for clients built by
// NewClientFrom), used for owner resolution.
func (c *Client) Metadata() metadata.Interface { return c.meta }

// NewClientFrom builds a Client from existing clients (used by tests).
func NewClientFrom(contextName string, dyn dynamic.Interface, disc discovery.DiscoveryInterface) *Client {
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc))
	return &Client{context: contextName, dyn: dyn, disc: disc, mapper: mapper}
}

// Context returns the kubeconfig context name.
func (c *Client) Context() string { return c.context }

// HomeNamespace is the namespace set on the kubeconfig context, or "".
func (c *Client) HomeNamespace() string { return c.home }

// Dynamic returns the dynamic client.
func (c *Client) Dynamic() dynamic.Interface { return c.dyn }

// Discovery returns an uncached discovery client, so engines always see
// freshly created CRDs.
func (c *Client) Discovery() discovery.DiscoveryInterface { return c.disc }

// NewInformerFactory returns a dynamic informer factory with no resync:
// updates come from watches only.
func (c *Client) NewInformerFactory() dynamicinformer.DynamicSharedInformerFactory {
	return dynamicinformer.NewDynamicSharedInformerFactory(c.dyn, 0)
}

// Get fetches the object a reference points at.
func (c *Client) Get(ctx context.Context, ref model.ObjectRef) (*unstructured.Unstructured, error) {
	gvr, namespaced, err := c.resolve(ref)
	if err != nil {
		return nil, err
	}
	var ri dynamic.ResourceInterface = c.dyn.Resource(gvr)
	if namespaced {
		ri = c.dyn.Resource(gvr).Namespace(ref.Namespace)
	}
	obj, err := ri.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", ref, err)
	}
	return obj, nil
}

func (c *Client) resolve(ref model.ObjectRef) (schema.GroupVersionResource, bool, error) {
	gk := schema.GroupKind{Group: ref.Group, Kind: ref.Kind}
	versions := []string{}
	if ref.Version != "" {
		versions = append(versions, ref.Version)
	}
	m, err := c.mapper.RESTMapping(gk, versions...)
	if meta.IsNoMatchError(err) {
		// The cache may predate a new CRD; refresh once.
		c.mapper.Reset()
		m, err = c.mapper.RESTMapping(gk, versions...)
	}
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("resolve %s: %w", gk, err)
	}
	return m.Resource, m.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// IsForbidden reports whether err is an RBAC denial.
func IsForbidden(err error) bool { return apierrors.IsForbidden(err) }
