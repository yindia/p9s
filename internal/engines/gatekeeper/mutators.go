package gatekeeper

import (
	"context"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// groupMutations holds Gatekeeper's mutators. Verified on Gatekeeper
// v3.23.1: Assign, AssignMetadata and ModifySet are served at v1, AssignImage
// only at v1alpha1, so each kind is watched at the newest version serving it.
const groupMutations = "mutations.gatekeeper.sh"

var (
	mutatorResources = []string{"assign", "assignmetadata", "modifyset", "assignimage"}
	mutatorVersions  = []string{"v1", "v1beta1", "v1alpha1"}
)

// startMutators watches every mutator kind the cluster serves. It must run
// between sync.Start and the registered() call of Start.
func (e *Engine) startMutators(ctx context.Context, dyn dynamic.Interface, f dynamicinformer.DynamicSharedInformerFactory, notify engine.Notify) {
	if _, ok := e.versions[groupMutations]; !ok {
		return // mutation CRDs not installed (mutation is optional in Gatekeeper)
	}
	served := map[string]map[string]string{} // version -> resource -> kind
	for _, v := range mutatorVersions {
		if rs, err := enginekit.ServedResources(e.cluster.Discovery(), groupMutations+"/"+v); err == nil {
			served[v] = rs
		}
	}
	for _, res := range mutatorResources {
		for _, v := range mutatorVersions {
			if _, ok := served[v][res]; !ok {
				continue
			}
			gvr := schema.GroupVersionResource{Group: groupMutations, Version: v, Resource: res}
			if inf := e.access.SharedInformer(ctx, dyn, f, gvr, notify, notify); inf != nil {
				e.mutators[res] = inf
				e.sync.Track(gvr, inf)
			}
			break
		}
	}
}

// mutation describes one mutator: what it sets and which kinds it applies to.
func mutation(o *unstructured.Unstructured) model.Mutation {
	location, _, _ := unstructured.NestedString(o.Object, "spec", "location")
	return model.Mutation{
		Engine: Name, Kind: o.GetKind(), Name: o.GetName(), Namespace: o.GetNamespace(),
		Location: location, Targets: mutatorTargets(o), Raw: o,
	}
}

// mutatorTargets lists the kinds a mutator applies to: spec.applyTo for
// Assign, ModifySet and AssignImage; AssignMetadata has none and is scoped
// by spec.match.kinds alone.
func mutatorTargets(o *unstructured.Unstructured) string {
	var kinds []string
	add := func(list []any) {
		for _, it := range list {
			m, _ := it.(map[string]any)
			ks, _, _ := unstructured.NestedStringSlice(m, "kinds")
			kinds = append(kinds, ks...)
		}
	}
	applyTo, _, _ := unstructured.NestedSlice(o.Object, "spec", "applyTo")
	add(applyTo)
	if len(kinds) == 0 {
		match, _, _ := unstructured.NestedSlice(o.Object, "spec", "match", "kinds")
		add(match)
	}
	if len(kinds) == 0 {
		return "-"
	}
	sort.Strings(kinds)
	return strings.Join(slices.Compact(kinds), ", ")
}

// mutatorObjects copies every mutator informer's cache.
func mutatorObjects(infs map[string]cache.SharedIndexInformer) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, inf := range infs {
		out = append(out, enginekit.Objects(inf)...)
	}
	enginekit.SortObjects(out)
	return out
}
