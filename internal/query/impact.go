package query

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
)

// Managed says how a violating object is kept alive, which decides what an
// enforcing policy would block.
type Managed string

// Managed values.
const (
	// ManagedController objects are workloads or have a controller: the
	// next rollout or recreate would be rejected.
	ManagedController Managed = "controller"
	// ManagedBare objects have no controller: their next update would be
	// rejected.
	ManagedBare Managed = "bare"
	// ManagedUnknown objects are of kinds p9s does not watch (or that RBAC
	// hides), so their ownerReferences are not known.
	ManagedUnknown Managed = "unknown"
)

// workloadKinds are controllers themselves: a violation on one blocks its
// own updates and, through its pods, its rollouts.
var workloadKinds = map[string]bool{
	"Deployment": true, "StatefulSet": true, "DaemonSet": true,
	"ReplicaSet": true, "Job": true, "CronJob": true,
}

// ImpactOwner is one top-level owner's share of a policy's violations.
type ImpactOwner struct {
	Namespace string // "" for cluster-scoped owners
	Owner     model.ObjectRef
	Managed   Managed
	// Objects counts distinct violating objects under the owner.
	Objects    int
	Violations []model.Violation
}

// FirstMessage is the message of the owner's first violation.
func (o ImpactOwner) FirstMessage() string {
	if len(o.Violations) == 0 {
		return ""
	}
	return o.Violations[0].Message
}

// ImpactNamespace totals one namespace.
type ImpactNamespace struct {
	Name    string // "" for cluster-scoped objects
	Objects int
}

// Impact is what moving a policy to enforce would block: today's
// violations, grouped by namespace and owner. Enforcement only rejects
// future creates and updates; objects that exist keep running.
type Impact struct {
	Policy model.Policy
	// Known is false when the engine has no audit data for the policy, so
	// its impact cannot be told.
	Known bool
	// Total is the engine's own count (Gatekeeper totalViolations, Kyverno
	// report results); Listed is how many violations the engine reported
	// individually. Complete is false when Listed < Total: the groups then
	// cover a sample only.
	Total, Listed int
	Complete      bool
	// Objects counts distinct violating objects among the listed.
	Objects int
	// EvalErrors counts listed results where the policy failed to evaluate;
	// they are not violations and are left out of the groups and Total.
	EvalErrors int
	Owners     []ImpactOwner
	Namespaces []ImpactNamespace
	// ByManaged counts owners per Managed value.
	ByManaged map[Managed]int
}

// Enforcing reports whether the policy already denies requests.
func (i Impact) Enforcing() bool { return i.Policy.Enforcement.Mode == model.ModeEnforce }

// ErrPolicyNotFound is returned when no active engine has the policy.
var ErrPolicyNotFound = errors.New("policy not found")

// ImpactOf computes the enforcement impact of one policy, cluster-wide.
// Owners are resolved through l (nil keeps targets as their own owners).
func ImpactOf(es []engine.Engine, key model.PolicyKey, l OwnerLookup) (Impact, error) {
	f := engine.Filter{Engine: key.Engine, PolicyKind: key.Kind, PolicyName: key.Name, PolicyNamespace: key.Namespace}
	ps, err := Collect(Capable(es, f, HasPolicies), engine.Engine.Policies)
	var p *model.Policy
	for i := range ps {
		if ps[i].Key() == key {
			p = &ps[i]
		}
	}
	if p == nil {
		if err != nil {
			return Impact{}, err
		}
		return Impact{}, fmt.Errorf("%w: %s", ErrPolicyNotFound, key)
	}
	out := Impact{Policy: *p, Known: p.ViolationsKnown, Total: p.ViolationCount, ByManaged: map[Managed]int{}}
	vs, verr := Violations(es, f)
	var mine []model.Violation
	truncated := false
	for _, v := range vs {
		if v.Policy != key {
			continue
		}
		if v.EvalError {
			out.EvalErrors++
			continue
		}
		mine = append(mine, v)
		truncated = truncated || v.Truncated
	}
	out.Listed = len(mine)
	// The engine's count includes evaluation errors; a stale count may be
	// lower than what is listed. Never claim fewer than are shown.
	out.Total = max(out.Total-out.EvalErrors, out.Listed)
	out.Complete = out.Known && !truncated && out.Listed >= out.Total
	out.Owners, out.Namespaces, out.Objects = groupImpact(mine, l)
	for _, o := range out.Owners {
		out.ByManaged[o.Managed]++
	}
	return out, errors.Join(err, verr)
}

// groupImpact groups violations by namespace, then owner; namespaces and
// owners with most objects first, ties by name.
func groupImpact(vs []model.Violation, l OwnerLookup) ([]ImpactOwner, []ImpactNamespace, int) {
	idx := map[string]int{}
	var owners []ImpactOwner
	seen := map[string]map[string]bool{}
	all := map[string]bool{}
	for _, v := range vs {
		v.Target = normalizeRef(v.Target)
		v.Owner = normalizeRef(ResolveOwner(l, v.Target))
		key := v.Owner.String()
		i, ok := idx[key]
		if !ok {
			i = len(owners)
			idx[key] = i
			seen[key] = map[string]bool{}
			owners = append(owners, ImpactOwner{
				Namespace: namespaceOf(v.Owner), Owner: v.Owner, Managed: managed(l, v.Target, v.Owner),
			})
		}
		o := &owners[i]
		o.Violations = append(o.Violations, v)
		if t := v.Target.String(); !seen[key][t] {
			seen[key][t] = true
			all[t] = true
			o.Objects++
		}
	}
	nsTotals := map[string]int{}
	for _, o := range owners {
		nsTotals[o.Namespace] += o.Objects
	}
	sort.SliceStable(owners, func(i, j int) bool {
		a, b := owners[i], owners[j]
		if a.Namespace != b.Namespace {
			if nsTotals[a.Namespace] != nsTotals[b.Namespace] {
				return nsTotals[a.Namespace] > nsTotals[b.Namespace]
			}
			return a.Namespace < b.Namespace
		}
		if a.Objects != b.Objects {
			return a.Objects > b.Objects
		}
		return a.Owner.String() < b.Owner.String()
	})
	var nss []ImpactNamespace
	for _, o := range owners {
		if len(nss) == 0 || nss[len(nss)-1].Name != o.Namespace {
			nss = append(nss, ImpactNamespace{Name: o.Namespace, Objects: nsTotals[o.Namespace]})
		}
	}
	return owners, nss, len(all)
}

// normalizeRef drops the namespace some engines put on Namespace objects
// (their own name), so one Namespace is one key.
func normalizeRef(r model.ObjectRef) model.ObjectRef {
	if r.Kind == "Namespace" && r.Group == "" {
		r.Namespace = ""
	}
	return r
}

// namespaceOf is where an owner lives; a Namespace object is cluster-scoped
// even when the engine reports its name as its namespace.
func namespaceOf(ref model.ObjectRef) string {
	if ref.Kind == "Namespace" && ref.Group == "" {
		return ""
	}
	return ref.Namespace
}

// managed classifies the owner of a violating target.
func managed(l OwnerLookup, target, owner model.ObjectRef) Managed {
	if owner != target || workloadKinds[owner.Kind] {
		return ManagedController
	}
	if l == nil {
		return ManagedUnknown
	}
	if _, cached := l.Labels(target); cached {
		return ManagedBare // cached with no controller reference
	}
	if target.Kind == "Namespace" && target.Group == "" {
		if _, ok := l.NamespaceLabels(target.Name); ok {
			return ManagedBare
		}
	}
	return ManagedUnknown
}

// FindPolicies resolves a name typed in ":impact": "name", "kind/name",
// "ns/name" for namespaced policies, or "engine:kind/name" (engine name or
// prefix), matched case-insensitively on kind and exactly on name.
func FindPolicies(es []engine.Engine, query string) ([]model.PolicyKey, error) {
	eng, rest, hasEngine := strings.Cut(query, ":")
	if !hasEngine {
		eng, rest = "", query
	}
	if eng != "" {
		known := false
		for _, e := range es {
			if e.Prefix() == eng {
				eng = e.Name()
			}
			known = known || e.Name() == eng
		}
		if !known {
			return nil, fmt.Errorf("no active engine %q (use an engine name or prefix before ':')", eng)
		}
	}
	parts := strings.Split(rest, "/")
	ps, err := Collect(Capable(es, engine.Filter{Engine: eng}, HasPolicies), engine.Engine.Policies)
	var out []model.PolicyKey
	for _, p := range ps {
		if policyMatches(p.Key(), parts) {
			out = append(out, p.Key())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, err
}

func policyMatches(k model.PolicyKey, parts []string) bool {
	switch len(parts) {
	case 1:
		return k.Name == parts[0]
	case 2:
		return k.Name == parts[1] && (strings.EqualFold(k.Kind, parts[0]) || k.Namespace == parts[0])
	case 3:
		return strings.EqualFold(k.Kind, parts[0]) && k.Namespace == parts[1] && k.Name == parts[2]
	}
	return false
}
