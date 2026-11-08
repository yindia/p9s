package query_test

import (
	"testing"

	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
)

func ref(kind, ns, name string) model.ObjectRef {
	return model.ObjectRef{Kind: kind, Namespace: ns, Name: name}
}

var chains = query.StaticOwners{
	Parents: map[string]model.ObjectRef{
		"Pod/a/web-1-x":      ref("ReplicaSet", "a", "web-1"),
		"Pod/a/web-1-y":      ref("ReplicaSet", "a", "web-1"),
		"ReplicaSet/a/web-1": ref("Deployment", "a", "web"),
		"Pod/a/backup-1-z":   ref("Job", "a", "backup-1"),
		"Job/a/backup-1":     ref("CronJob", "a", "backup"),
		"Pod/a/db-0":         ref("StatefulSet", "a", "db"),
		"Pod/a/agent-q":      ref("DaemonSet", "a", "agent"),
		// orphan-rs's ReplicaSet is not cached (RBAC or gone): stop there.
		"Pod/a/orphan-x": ref("ReplicaSet", "a", "orphan"),
	},
	Objects: map[string]map[string]string{
		"Deployment/a/web": {"team": "web"},
		"CronJob/a/backup": {},
	},
	NSLabels: map[string]map[string]string{"a": {"team": "platform"}, "b": {}},
}

func TestResolveOwnerChains(t *testing.T) {
	tests := []struct{ target, want string }{
		{"Pod/a/web-1-x", "Deployment/a/web"},
		{"Pod/a/backup-1-z", "CronJob/a/backup"},
		{"Pod/a/db-0", "StatefulSet/a/db"},
		{"Pod/a/agent-q", "DaemonSet/a/agent"},
		{"Pod/a/orphan-x", "ReplicaSet/a/orphan"}, // deepest resolvable
		{"Pod/a/bare", "Pod/a/bare"},              // unowned: itself
	}
	for _, tt := range tests {
		var r model.ObjectRef
		_, _ = fmtSscan(tt.target, &r)
		if got := query.ResolveOwner(chains, r).String(); got != tt.want {
			t.Errorf("ResolveOwner(%s) = %s, want %s", tt.target, got, tt.want)
		}
	}
	if got := query.ResolveOwner(nil, ref("Pod", "a", "x")); got.Name != "x" {
		t.Error("no lookup: a target is its own owner")
	}
	loop := query.StaticOwners{Parents: map[string]model.ObjectRef{
		"Pod/a/p": ref("Pod", "a", "p"), // self-owned: must terminate
	}}
	if got := query.ResolveOwner(loop, ref("Pod", "a", "p")); got.Name != "p" {
		t.Errorf("loop = %v", got)
	}
	cycle := query.StaticOwners{Parents: map[string]model.ObjectRef{
		"Pod/a/x":        ref("ReplicaSet", "a", "r"),
		"ReplicaSet/a/r": ref("Deployment", "a", "d"),
		"Deployment/a/d": ref("ReplicaSet", "a", "r"),
	}}
	if got := query.ResolveOwner(cycle, ref("Pod", "a", "x")); got.String() != "Pod/a/x" {
		t.Errorf("a cycle must resolve to the target itself, got %v", got)
	}
}

// fmtSscan parses "Kind/ns/name".
func fmtSscan(s string, r *model.ObjectRef) (int, error) {
	var parts [3]string
	i := 0
	for _, c := range s {
		if c == '/' && i < 2 {
			i++
			continue
		}
		parts[i] += string(c)
	}
	*r = ref(parts[0], parts[1], parts[2])
	return 3, nil
}

func TestGroupingByOwnerAndLabel(t *testing.T) {
	pol := model.PolicyKey{Engine: "e", Kind: "K", Name: "p"}
	other := model.PolicyKey{Engine: "e", Kind: "K", Name: "q"}
	var vs []model.Violation
	for _, target := range []string{"Pod/a/web-1-x", "Pod/a/web-1-y", "Pod/a/db-0"} {
		var r model.ObjectRef
		_, _ = fmtSscan(target, &r)
		vs = append(vs, model.Violation{Policy: pol, Target: r, Message: "m " + target})
	}
	vs = append(vs, model.Violation{Policy: other, Target: ref("Pod", "a", "web-1-x")})
	vs = append(vs, model.Violation{Policy: pol, Target: ref("Pod", "b", "lone")})
	for i := range vs {
		vs[i].Owner = query.ResolveOwner(chains, vs[i].Target)
	}

	groups := query.GroupViolations(vs, chains, "")
	if len(groups) != 4 || groups[0].Key != "Deployment/a/web" || groups[0].Objects != 2 || groups[0].Policy != pol {
		t.Fatalf("first group = %+v (of %d)", groups[0], len(groups))
	}
	if groups[0].FirstMessage() != "m Pod/a/web-1-x" {
		t.Errorf("first message = %q", groups[0].FirstMessage())
	}

	byTeam := query.GroupViolations(vs, chains, "team")
	got := map[string]int{}
	for _, g := range byTeam {
		got[g.Key+"|"+g.Policy.Name] = g.Objects
	}
	// web has its own label; db falls back to namespace a's; b has none.
	want := map[string]int{"web|p": 2, "web|q": 1, "platform|p": 1, query.NoGroup + "|p": 1}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("label group %s = %d, want %d (all: %v)", k, got[k], n, got)
		}
	}

	top := query.TopOwners(groups)
	if top[0].Key != "Deployment/a/web" || top[0].Policies != 2 || top[0].Objects != 2 {
		t.Errorf("top owner = %+v", top[0])
	}
	if query.GroupValue(chains, ref("Namespace", "", "a"), "team") != "platform" {
		t.Error("a Namespace target groups by its own labels")
	}
}
