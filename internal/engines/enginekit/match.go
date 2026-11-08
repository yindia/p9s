package enginekit

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/model"
)

// ResourceRules reads admission-style matchConstraints.resourceRules
// ([{apiGroups, resources, operations}]), shared by ValidatingAdmission-
// Policies and Kyverno CEL policies. Subresource-only and DELETE-only
// entries select no created object and are dropped; "*" in both group and
// resource selects everything (all is true).
func ResourceRules(entries []any) (rules []model.MatchRule, all bool) {
	for _, r := range entries {
		m, _ := r.(map[string]any)
		if ops, _, _ := unstructured.NestedStringSlice(m, "operations"); onlyDelete(ops) {
			continue
		}
		groups, _, _ := unstructured.NestedStringSlice(m, "apiGroups")
		resources, _, _ := unstructured.NestedStringSlice(m, "resources")
		if len(groups) == 0 {
			groups = []string{""}
		}
		for _, g := range groups {
			for _, res := range resources {
				if strings.Contains(res, "/") {
					continue // a subresource only
				}
				if g == "*" && res == "*" {
					return nil, true
				}
				rules = append(rules, model.MatchRule{Group: g, Resource: res})
			}
		}
	}
	return rules, false
}

func onlyDelete(ops []string) bool {
	if len(ops) == 0 {
		return false
	}
	for _, op := range ops {
		if op != "DELETE" {
			return false
		}
	}
	return true
}
