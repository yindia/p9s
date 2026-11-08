package session

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/yindia/p9s/internal/model"
)

// APIResources lists the kinds the cluster serves that users create
// (preferred versions; verbs create and list; no subresources). A
// partially failed discovery returns what it found with the error.
func (s *Session) APIResources() ([]model.APIResource, error) {
	s.mu.RLock()
	cl := s.cluster
	s.mu.RUnlock()
	if cl == nil {
		return nil, errors.New("not connected")
	}
	lists, err := cl.Discovery().ServerPreferredResources()
	var partial *discovery.ErrGroupDiscoveryFailed
	if err != nil && !errors.As(err, &partial) {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	out := Creatable(lists)
	if err != nil {
		return out, fmt.Errorf("discovery (partial): %w", err)
	}
	return out, nil
}

// Creatable flattens discovery lists to user-creatable resources.
func Creatable(lists []*metav1.APIResourceList) []model.APIResource {
	var out []model.APIResource
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") || !hasVerbs(r.Verbs, "create", "list") {
				continue
			}
			out = append(out, model.APIResource{Group: gv.Group, Version: gv.Version, Kind: r.Kind, Resource: r.Name, Namespaced: r.Namespaced})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func hasVerbs(have metav1.Verbs, want ...string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			found = found || h == w
		}
		if !found {
			return false
		}
	}
	return true
}
