package gatekeeper_test

import (
	"strings"
	"testing"

	"github.com/yindia/p9s/internal/engines/gatekeeper/gktest"
)

func TestGuidanceFromConstraintThenTemplate(t *testing.T) {
	e, _ := started(t, gktest.NewCluster(t, gktest.Options{}))
	eventually(t, "policies", func() bool { return len(policies(t, e)) > 0 })
	ps := policies(t, e)
	app := ps["K8sRequiredLabels/pods-must-have-app"]
	if len(app.Guidance) != 2 || !strings.Contains(app.Guidance[0].Text, "app=") || app.Guidance[0].From != "constraint annotation description" {
		t.Fatalf("pods-must-have-app guidance = %+v: the constraint's description first", app.Guidance)
	}
	if g := app.Guidance[1]; g.Title != "Required Labels" || !strings.Contains(g.From, "template k8srequiredlabels") {
		t.Errorf("template guidance = %+v", g)
	}
	if g := ps["K8sReplicaLimits/max-ten-replicas"].Guidance; len(g) != 0 {
		t.Errorf("a template without annotations gives no guidance: %+v", g)
	}
}
