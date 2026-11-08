package kyverno_test

import (
	"testing"

	"github.com/yindia/p9s/internal/engines/kyverno/kyvtest"
	"github.com/yindia/p9s/internal/model"
)

func TestMatchRulesAndSeverity(t *testing.T) {
	ps := policies(t, started(t, kyvtest.NewCluster(t, kyvtest.Options{})))
	cp := ps["kyverno:ClusterPolicy/require-team-label"]
	if !cp.MatchKnown || cp.MatchesAll || len(cp.Matches) == 0 || cp.Matches[0] != (model.MatchRule{Group: "*", Kind: "Pod"}) {
		t.Errorf("classic match = %+v / %v", cp.Matches, cp.MatchesAll)
	}
	vp := ps["kyverno:ValidatingPolicy/disallow-privileged-containers"]
	if !vp.MatchKnown || len(vp.Matches) == 0 || vp.Matches[0].Resource != "pods" {
		t.Errorf("CEL match = %+v", vp.Matches)
	}
	if vp.Severity != "medium" {
		t.Errorf("severity = %q, want medium from policies.kyverno.io/severity", vp.Severity)
	}
}
