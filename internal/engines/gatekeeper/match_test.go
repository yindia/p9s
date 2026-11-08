package gatekeeper_test

import (
	"testing"

	"github.com/yindia/p9s/internal/engines/gatekeeper/gktest"
	"github.com/yindia/p9s/internal/model"
)

func TestConstraintMatchRules(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	eventually(t, "policies", func() bool { return len(policies(t, e)) > 0 })
	p := policies(t, e)["K8sRequiredLabels/ns-must-have-owner"]
	if !p.MatchKnown || p.MatchesAll || len(p.Matches) != 1 || p.Matches[0] != (model.MatchRule{Group: "", Kind: "Namespace"}) {
		t.Errorf("match = %+v all=%v", p.Matches, p.MatchesAll)
	}
}
