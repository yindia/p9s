package kyverno

import (
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

const annSeverity = "policies.kyverno.io/severity"

// matchRules reads what a policy selects: classic rules'
// match.(any|all)[].resources.kinds (and the legacy match.resources.kinds),
// CEL policies' matchConstraints.resourceRules. Subresource-only entries
// (Pod/exec, deployments/scale) select no kind and are dropped; a classic
// rule naming no kinds (subjects only) makes the match unknown.
func matchRules(o *unstructured.Unstructured) (rules []model.MatchRule, all, known bool) {
	if isClassic(o) {
		for _, r := range enginekit.Slice(o.Object, "spec", "rules") {
			m, _ := r.(map[string]any)
			match, _ := m["match"].(map[string]any)
			var filters []any
			filters = append(filters, enginekit.Slice(match, "any")...)
			filters = append(filters, enginekit.Slice(match, "all")...)
			if res, ok := match["resources"].(map[string]any); ok {
				filters = append(filters, map[string]any{"resources": res})
			}
			ruleKinds := 0
			for _, f := range filters {
				fm, _ := f.(map[string]any)
				kinds, _, _ := unstructured.NestedStringSlice(fm, "resources", "kinds")
				for _, k := range kinds {
					ruleKinds++
					if k == "*" {
						return nil, true, true
					}
					if r, ok := classicKind(k); ok {
						rules = append(rules, r)
					}
				}
			}
			if ruleKinds == 0 {
				return nil, false, false
			}
		}
		return rules, false, true
	}
	rules, all = enginekit.ResourceRules(enginekit.Slice(o.Object, "spec", "matchConstraints", "resourceRules"))
	return rules, all, true
}

// apiVersion matches Kubernetes version strings: v1, v2beta1, v1alpha3.
var apiVersion = regexp.MustCompile(`^v\d+((alpha|beta)\d+)?$`)

// classicKind parses Kyverno kind strings: "Pod", "v1/Pod",
// "apps/v1/Deployment", "velero.io/v1/Backup", "Pod/exec" (a subresource:
// ok is false). A lowercase last segment is a subresource.
func classicKind(s string) (model.MatchRule, bool) {
	parts := strings.Split(s, "/")
	if last := parts[len(parts)-1]; last != "" && strings.ToLower(last[:1]) == last[:1] && last != "*" {
		return model.MatchRule{}, false // Pod/exec, apps/v1/Deployment/scale
	}
	switch {
	case len(parts) == 1:
		return model.MatchRule{Group: "*", Kind: s}, true
	case len(parts) == 2 && apiVersion.MatchString(parts[0]):
		return model.MatchRule{Group: "", Kind: parts[1]}, true // v1/Pod: the core group
	case len(parts) == 2:
		return model.MatchRule{Group: parts[0], Kind: parts[1]}, true // group/Kind
	default:
		return model.MatchRule{Group: parts[0], Kind: parts[len(parts)-1]}, true // group/version/Kind
	}
}
