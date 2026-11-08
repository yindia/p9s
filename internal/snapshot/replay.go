package snapshot

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/metadata"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/session"
)

// Connector serves a snapshot as the only context.
func Connector(f *File) session.Connector { return replayConnector{f} }

type replayConnector struct{ f *File }

func (c replayConnector) Contexts() ([]model.KubeContext, string, error) {
	ctx := c.f.Context
	ctx.Current = true
	return []model.KubeContext{ctx}, ctx.Name, nil
}

func (c replayConnector) Connect(name string) (session.Cluster, error) {
	if name != "" && name != c.f.Context.Name {
		return nil, fmt.Errorf("snapshot has only context %q", c.f.Context.Name)
	}
	return NewCluster(c.f)
}

// Cluster replays a snapshot through fake clients.
type Cluster struct {
	f    *File
	dyn  *dynamicfake.FakeDynamicClient
	meta *metadatafake.FakeMetadataClient
	disc *replayDiscovery
}

// NewCluster seeds fake clients with the recorded objects and denials.
func NewCluster(f *File) (*Cluster, error) {
	listKinds := map[schema.GroupVersionResource]string{}
	kinds := map[schema.GroupVersionResource]string{}
	for _, rl := range f.Resources {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range rl.APIResources {
			gvr := gv.WithResource(r.Name)
			listKinds[gvr] = r.Kind + "List"
			kinds[gvr] = r.Kind
		}
	}
	// Recorded resources discovery did not list (core kinds the health
	// and events watchers read by fixed GVR) still need a list kind.
	for _, o := range f.Objects {
		gvr := schema.GroupVersionResource{Group: o.Group, Version: o.Version, Resource: o.Resource}
		if _, ok := listKinds[gvr]; !ok {
			for _, it := range o.Items {
				if k := it.GetKind(); k != "" {
					listKinds[gvr], kinds[gvr] = k+"List", k
					break
				}
			}
		}
		if _, ok := listKinds[gvr]; !ok {
			return nil, fmt.Errorf("snapshot: no kind known for %s (discovery missing from the file)", gvr)
		}
	}
	for _, d := range f.Denied {
		gvr := schema.GroupVersionResource{Group: d.Group, Version: d.Version, Resource: d.Resource}
		if _, ok := listKinds[gvr]; !ok {
			listKinds[gvr] = "UnknownList"
		}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	for _, o := range f.Objects {
		gvr := schema.GroupVersionResource{Group: o.Group, Version: o.Version, Resource: o.Resource}
		for _, it := range o.Items {
			if err := dyn.Tracker().Create(gvr, it.DeepCopy(), it.GetNamespace()); err != nil {
				return nil, fmt.Errorf("snapshot: replay %s %s: %w", gvr, it.GetName(), err)
			}
		}
	}
	scheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(scheme)
	meta := metadatafake.NewSimpleMetadataClient(scheme)
	for _, o := range f.Metadata {
		gvr := schema.GroupVersionResource{Group: o.Group, Version: o.Version, Resource: o.Resource}
		for i := range o.Items {
			it := o.Items[i].DeepCopy()
			if it.Kind == "" {
				if kinds[gvr] == "" {
					return nil, fmt.Errorf("snapshot: no kind known for metadata of %s (discovery missing from the file)", gvr)
				}
				it.APIVersion, it.Kind = schema.GroupVersion{Group: o.Group, Version: o.Version}.String(), kinds[gvr]
			}
			if err := meta.Tracker().Create(gvr, it, it.Namespace); err != nil {
				return nil, fmt.Errorf("snapshot: replay metadata %s %s: %w", gvr, it.Name, err)
			}
		}
	}
	for _, d := range f.Denied {
		gvr := schema.GroupVersionResource{Group: d.Group, Version: d.Version, Resource: d.Resource}
		if d.Metadata {
			meta.PrependReactor("list", d.Resource, denyReactor(gvr, d))
			meta.PrependWatchReactor(d.Resource, denyWatch(gvr, d))
			continue
		}
		dyn.PrependReactor("list", d.Resource, denyReactor(gvr, d))
		dyn.PrependWatchReactor(d.Resource, denyWatch(gvr, d))
	}
	return &Cluster{f: f, dyn: dyn, meta: meta, disc: &replayDiscovery{
		FakeDiscovery: &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: f.Resources}}, groups: f.Groups, preferred: f.Preferred, preferredErr: f.PreferredError,
	}}, nil
}

// denyReactor answers as the API server did: forbidden in the recorded
// scope (all namespaces, or one).
func denyReactor(gvr schema.GroupVersionResource, d Denial) clienttesting.ReactionFunc {
	return func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetResource() != gvr || a.GetNamespace() != d.Namespace {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New(d.Message))
	}
}

func denyWatch(gvr schema.GroupVersionResource, d Denial) clienttesting.WatchReactionFunc {
	return func(a clienttesting.Action) (bool, watch.Interface, error) {
		if a.GetResource() != gvr || a.GetNamespace() != d.Namespace {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New(d.Message))
	}
}

// Context implements session.Cluster.
func (c *Cluster) Context() string { return c.f.Context.Name }

// HomeNamespace replays the recorded context's namespace (tenant fallback).
func (c *Cluster) HomeNamespace() string { return c.f.Context.Namespace }

// Dynamic implements engine.Cluster.
func (c *Cluster) Dynamic() dynamic.Interface { return c.dyn }

// Discovery implements engine.Cluster.
func (c *Cluster) Discovery() discovery.DiscoveryInterface { return c.disc }

// Metadata serves the recorded owner metadata.
func (c *Cluster) Metadata() metadata.Interface { return c.meta }

// NewInformerFactory implements session.Cluster.
func (c *Cluster) NewInformerFactory() dynamicinformer.DynamicSharedInformerFactory {
	return dynamicinformer.NewDynamicSharedInformerFactory(c.dyn, 0)
}

// Get implements session.Cluster: objects in the snapshot only.
func (c *Cluster) Get(ctx context.Context, ref model.ObjectRef) (*unstructured.Unstructured, error) {
	for _, o := range c.f.Objects {
		if o.Group != ref.Group || (ref.Version != "" && o.Version != ref.Version) {
			continue
		}
		for _, it := range o.Items {
			if it.GetKind() == ref.Kind && it.GetNamespace() == ref.Namespace && it.GetName() == ref.Name {
				return it.DeepCopy(), nil
			}
		}
	}
	return nil, fmt.Errorf("%s is not in the snapshot (taken %s)", ref, c.f.TakenAt.Format("2006-01-02 15:04:05Z"))
}

// replayDiscovery answers the two discovery calls engines make from the
// recording.
type replayDiscovery struct {
	*discoveryfake.FakeDiscovery
	groups       *metav1.APIGroupList
	preferred    []*metav1.APIResourceList
	preferredErr string
}

// ServerPreferredResources replays the recorded preferred discovery.
func (d *replayDiscovery) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	if d.preferred == nil && d.preferredErr == "" {
		return nil, errors.New("this snapshot has no preferred discovery (taken by an older p9s)")
	}
	if d.preferredErr != "" {
		return d.preferred, &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{{}: errors.New("recorded: " + d.preferredErr)}}
	}
	return d.preferred, nil
}

func (d *replayDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	if d.groups != nil {
		return d.groups, nil
	}
	return d.FakeDiscovery.ServerGroups()
}

func (d *replayDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	for _, rl := range d.Resources {
		if rl.GroupVersion == gv {
			return rl, nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: gv}, "")
}
