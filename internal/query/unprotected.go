package query

import (
	"strings"

	"github.com/yindia/p9s/internal/model"
)

// podControllers create Pods: a policy on Pods also rejects what they
// create (admission sees the Pods), though not the controller object.
var podControllers = map[string]bool{
	"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true,
	"Job": true, "CronJob": true, "ReplicationController": true,
}

// plumbingKinds are written by the control plane, not users (group/Kind).
var plumbingKinds = map[string]bool{
	"/Event": true, "events.k8s.io/Event": true, "coordination.k8s.io/Lease": true, "/Endpoints": true,
	"discovery.k8s.io/EndpointSlice": true, "apps/ControllerRevision": true,
}

// engineGroup reports the policy engines' own API groups.
func engineGroup(g string) bool {
	return strings.HasSuffix(g, "gatekeeper.sh") || strings.HasSuffix(g, "kyverno.io") || g == "wgpolicyk8s.io"
}

// UnprotectedKind is a served kind no active policy selects.
type UnprotectedKind struct {
	model.APIResource
	// ViaPods is set for Pod controllers when Pod policies exist: the
	// controller object is unchecked but the Pods it creates are.
	ViaPods bool `json:"viaPods,omitempty"`
}

// Unprotected lists served, user-created kinds that no policy's match
// selects. It is approximate: it reads match kinds only, not namespace
// selectors, exclusions or conditions; policies whose match the engine
// could not read count as matching nothing and are returned as unknown.
// allKinds are the policies selecting every kind (no kind restriction).
// They are left out of the kind check: such a policy's rule usually only
// inspects one kind, so counting it would hide every gap.
func Unprotected(ps []model.Policy, kinds []model.APIResource) (out []UnprotectedKind, unknown, allKinds []model.PolicyKey) {
	var rules []model.MatchRule
	for _, p := range ps {
		switch {
		case !p.MatchKnown:
			unknown = append(unknown, p.Key())
		case p.MatchesAll:
			allKinds = append(allKinds, p.Key())
		default:
			rules = append(rules, p.Matches...)
		}
	}
	pods := matchesAny(rules, model.APIResource{Kind: "Pod", Resource: "pods"})
	for _, k := range kinds {
		if plumbingKinds[k.Group+"/"+k.Kind] || engineGroup(k.Group) || matchesAny(rules, k) {
			continue
		}
		out = append(out, UnprotectedKind{APIResource: k, ViaPods: pods && podControllers[k.Kind]})
	}
	return out, unknown, allKinds
}

func matchesAny(rules []model.MatchRule, k model.APIResource) bool {
	for _, r := range rules {
		if r.Group != "*" && r.Group != k.Group {
			continue
		}
		if (r.Kind != "" && (r.Kind == "*" || strings.EqualFold(r.Kind, k.Kind))) ||
			(r.Resource != "" && (r.Resource == "*" || r.Resource == k.Resource)) {
			return true
		}
	}
	return false
}
