package gatekeeper

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// annSeverity is not set by gatekeeper-library; users may annotate
// constraints with it.
const annSeverity = "severity"

// matchRules reads spec.match.kinds: [{apiGroups, kinds}]. No kinds
// selects every kind.
func matchRules(c *unstructured.Unstructured) (rules []model.MatchRule, all bool) {
	entries := enginekit.Slice(c.Object, "spec", "match", "kinds")
	if len(entries) == 0 {
		return nil, true
	}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		groups, _, _ := unstructured.NestedStringSlice(m, "apiGroups")
		kinds, _, _ := unstructured.NestedStringSlice(m, "kinds")
		if len(groups) == 0 {
			groups = []string{""}
		}
		for _, g := range groups {
			for _, k := range kinds {
				if g == "*" && k == "*" {
					return nil, true
				}
				rules = append(rules, model.MatchRule{Group: g, Kind: k})
			}
		}
	}
	return rules, false
}
