package ui_test

import (
	"testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	. "github.com/yindia/p9s/internal/ui/uitest"
	"github.com/yindia/p9s/internal/views"
)

func TestConfiguredSortSurvivesRegroup(t *testing.T) {
	o := Options(fixtures.Now)
	o.Sorts = views.CanonicalSorts(map[string]engine.Sort{"pol": {Role: engine.RoleName}, "v": {Role: engine.RoleName}})
	RequireGolden(t, Run(t, twoEngines(), o, Type(":v\no"), WaitFor("flat list"), Type("o"), WaitFor("grouped by owner")))
}

func TestConfiguredSortOnPolicies(t *testing.T) {
	o := Options(fixtures.Now)
	o.Sorts = views.CanonicalSorts(map[string]engine.Sort{"policies": {Role: engine.RoleName}})
	RequireGolden(t, Run(t, twoEngines(), o, WaitFor("NAME↑")))
}
