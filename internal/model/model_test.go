package model_test

import (
	"encoding/json"
	"testing"

	"github.com/yindia/p9s/internal/model"
)

func TestStrings(t *testing.T) {
	tests := []struct{ got, want string }{
		{model.Enforcement{Mode: model.ModeEnforce, Raw: "deny"}.String(), "enforce (engine: deny)"},
		{model.Enforcement{Mode: model.ModeUnknown, Raw: "generate"}.String(), "n/a (engine: generate)"},
		{model.Enforcement{Mode: model.ModeMutate, Raw: "mutate"}.String(), "mutate"},
		{model.ModeUnknown.Label(), "n/a"},
		{model.Enforcement{Mode: model.ModeWarn, Raw: "warn"}.String(), "warn"},
		{model.Enforcement{Mode: model.ModeAudit}.String(), "audit"},
		{model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "a", Name: "p"}.String(), "Pod/a/p"},
		{model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "a"}.String(), "Namespace/a"},
		{model.ObjectRef{Version: "v1"}.APIVersion(), "v1"},
		{model.ObjectRef{Group: "apps", Version: "v1"}.APIVersion(), "apps/v1"},
		{model.PolicyKey{Engine: "gk", Kind: "K", Name: "n"}.String(), "gk:K/n"},
		{model.PolicyKey{Engine: "kyv", Kind: "Policy", Namespace: "ns", Name: "n"}.String(), "kyv:Policy/ns/n"},
		{model.AccessIssue{Engine: "gk", Verb: "list", Resource: "configs", Message: "forbidden"}.String(), "gk: cannot list configs: forbidden"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
	p := model.Policy{Engine: "gk", Kind: "K", Name: "n"}
	if p.Key() != (model.PolicyKey{Engine: "gk", Kind: "K", Name: "n"}) {
		t.Errorf("Key() = %+v", p.Key())
	}
}

// Reports written before PolicyKey had json tags used Go field names;
// --since must still read them.
func TestPolicyKeyReadsOldFieldNames(t *testing.T) {
	var k model.PolicyKey
	if err := json.Unmarshal([]byte(`{"Engine":"kyverno","Kind":"ClusterPolicy","Name":"require-team-label"}`), &k); err != nil {
		t.Fatal(err)
	}
	if k != (model.PolicyKey{Engine: "kyverno", Kind: "ClusterPolicy", Name: "require-team-label"}) {
		t.Errorf("decoded %+v", k)
	}
	b, _ := json.Marshal(k)
	if string(b) != `{"engine":"kyverno","kind":"ClusterPolicy","name":"require-team-label"}` {
		t.Errorf("encoded %s", b)
	}
}
