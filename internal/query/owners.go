package query

import (
	"sort"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
)

// maxOwnerDepth bounds the ownerRef walk (Pod → ReplicaSet → Deployment is
// two hops; anything deeper is a loop or an exotic controller chain).
const maxOwnerDepth = 6

// OwnerLookup answers ownership questions from cached metadata.
type OwnerLookup interface {
	// Controller returns the controlling owner of ref. ok is false when ref
	// is not cached (its kind is not watched, RBAC hides it, or it is gone),
	// so a walk stops at the deepest owner it could resolve.
	Controller(ref model.ObjectRef) (owner model.ObjectRef, ok bool)
	// Cached reports whether ref is in the cache: one with no controller
	// is then known to be bare, not merely unresolved.
	Cached(ref model.ObjectRef) bool
}

// ResolveOwner walks controller references up from ref to its top-level
// owner. Without a lookup, or for an unowned object, it returns ref. A
// cycle or an over-long chain also returns ref: any node of a loop would
// depend on where the walk started, and the target is the honest answer.
func ResolveOwner(l OwnerLookup, ref model.ObjectRef) model.ObjectRef {
	if l == nil {
		return ref
	}
	cur := ref
	seen := map[string]bool{ref.String(): true}
	for range maxOwnerDepth {
		owner, ok := l.Controller(cur)
		if !ok {
			return cur
		}
		if seen[owner.String()] {
			return ref
		}
		seen[owner.String()] = true
		cur = owner
	}
	return ref
}

// ViolationsWithOwners is Violations with each Owner resolved, filtered by
// f.Owner when set.
func ViolationsWithOwners(es []engine.Engine, f engine.Filter, l OwnerLookup) ([]model.Violation, error) {
	vs, err := Violations(es, f)
	out := vs[:0]
	for _, v := range vs {
		v.Owner = ResolveOwner(l, v.Target)
		if f.Owner != "" && v.Owner.String() != f.Owner {
			continue
		}
		out = append(out, v)
	}
	return out, err
}

// Group is the violations of one policy on one owner.
type Group struct {
	// Key is the owner ref string.
	Key         string
	Owner       model.ObjectRef
	Policy      model.PolicyKey
	Enforcement model.Enforcement
	// Objects counts distinct affected objects (e.g. a Deployment's pods).
	Objects    int
	Violations []model.Violation
}

// FirstMessage is the message of the group's first violation.
func (g Group) FirstMessage() string {
	if len(g.Violations) == 0 {
		return ""
	}
	return g.Violations[0].Message
}

// GroupViolations groups violations (owners already resolved) by owner and
// policy. Groups are ordered most affected objects first, then by key.
func GroupViolations(vs []model.Violation) []Group {
	idx := map[string]int{}
	var out []Group
	seen := map[string]map[string]bool{}
	for _, v := range vs {
		key := v.Owner.String()
		id := key + "|" + v.Policy.String()
		i, ok := idx[id]
		if !ok {
			i = len(out)
			idx[id] = i
			out = append(out, Group{Key: key, Owner: v.Owner, Policy: v.Policy, Enforcement: v.Enforcement})
			seen[id] = map[string]bool{}
		}
		out[i].Violations = append(out[i].Violations, v)
		if t := v.Target.String(); !seen[id][t] {
			seen[id][t] = true
			out[i].Objects++
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Objects != out[j].Objects {
			return out[i].Objects > out[j].Objects
		}
		return out[i].Key+out[i].Policy.String() < out[j].Key+out[j].Policy.String()
	})
	return out
}

// OwnerCount is one owner's footprint across policies.
type OwnerCount struct {
	Key      string
	Owner    model.ObjectRef
	Policies int
	Objects  int
}

// TopOwners ranks owners by affected objects across all their groups.
func TopOwners(groups []Group) []OwnerCount {
	idx := map[string]int{}
	var out []OwnerCount
	objs := map[string]map[string]bool{}
	for _, g := range groups {
		i, ok := idx[g.Key]
		if !ok {
			i = len(out)
			idx[g.Key] = i
			out = append(out, OwnerCount{Key: g.Key, Owner: g.Owner})
			objs[g.Key] = map[string]bool{}
		}
		out[i].Policies++
		for _, v := range g.Violations {
			objs[g.Key][v.Target.String()] = true
		}
	}
	for i := range out {
		out[i].Objects = len(objs[out[i].Key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Objects != out[j].Objects {
			return out[i].Objects > out[j].Objects
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// StaticOwners is an in-memory OwnerLookup (tests, snapshots).
type StaticOwners struct {
	Parents map[string]model.ObjectRef // child ref String() -> owner
	// Known are cached objects without a controller (ref String()).
	Known map[string]bool
}

// Cached implements OwnerLookup.
func (s StaticOwners) Cached(ref model.ObjectRef) bool {
	_, owned := s.Parents[ref.String()]
	return owned || s.Known[ref.String()]
}

// Controller implements OwnerLookup.
func (s StaticOwners) Controller(ref model.ObjectRef) (model.ObjectRef, bool) {
	o, ok := s.Parents[ref.String()]
	return o, ok
}
