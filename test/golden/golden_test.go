// Package golden renders the real Gatekeeper engine through the real session
// and UI against a fake cluster. It lives outside internal/ui so the UI
// package never imports an engine.
package golden

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/metadata"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/engines/gatekeeper"
	"github.com/yindia/p9s/internal/engines/gatekeeper/gktest"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/session"
	. "github.com/yindia/p9s/internal/ui/uitest"
)

type cluster struct {
	engine.Cluster
	dyn  dynamic.Interface
	meta metadata.Interface
}

// Metadata exposes the fake metadata client so the session resolves owners.
func (c cluster) Metadata() metadata.Interface { return c.meta }

func (cluster) Context() string { return "kind-gatekeeper" }

// HomeNamespace forwards the fake's context namespace (tenant tests).
func (c cluster) HomeNamespace() string { return engine.HomeNamespace(c.Cluster) }
func (c cluster) NewInformerFactory() dynamicinformer.DynamicSharedInformerFactory {
	return dynamicinformer.NewDynamicSharedInformerFactory(c.dyn, 0)
}
func (cluster) Get(context.Context, model.ObjectRef) (*unstructured.Unstructured, error) {
	return nil, errors.New("not used")
}

type connector struct{ cl engine.Cluster }

func (c connector) Contexts() ([]model.KubeContext, string, error) {
	return []model.KubeContext{{Name: "kind-gatekeeper"}}, "kind-gatekeeper", nil
}
func (c connector) Connect(string) (session.Cluster, error) {
	cl := cluster{Cluster: c.cl, dyn: c.cl.Dynamic()}
	if m, ok := c.cl.(interface{ Metadata() metadata.Interface }); ok {
		cl.meta = m.Metadata()
	}
	return cl, nil
}

func newSession(t *testing.T, withAlpha bool) *session.Session {
	t.Helper()
	regs := []engine.Registration{{Name: gatekeeper.Name, Factory: gatekeeper.New}}
	if withAlpha {
		regs = append(regs, engine.Registration{Name: "alpha", Factory: fixtures.Alpha().Factory()})
	}
	s, err := session.New(session.Options{Connector: connector{gktest.NewCluster(t, gktest.Options{})}, Engines: regs, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func golden(t *testing.T, withAlpha bool, steps ...Step) {
	t.Helper()
	RequireGolden(t, Run(t, newSession(t, withAlpha), Options(fixtures.Now), steps...))
}

// ready waits until the constraint informers delivered all fixtures.
var ready = WaitFor("pods-must-have-app")

func TestGatekeeperPolicies(t *testing.T) { golden(t, false, ready) }

func TestGatekeeperAndFakeEngineTogether(t *testing.T) {
	golden(t, true, WaitFor("require-owner"), ready)
}

func TestGatekeeperViolations(t *testing.T) {
	golden(t, false, ready, Type(":v\n"), WaitFor("Violations [3]"))
}

func TestTemplates(t *testing.T) {
	golden(t, false, ready, Type(":gk/ct\n"), WaitFor("k8sreplicalimits"))
}

func TestTemplateDetailShowsRegoAndSchema(t *testing.T) {
	golden(t, false, ready, Type(":gk/ct\n/requiredlabels\nd"), WaitFor("Parameters schema"))
}

func TestTemplateDrillToConstraints(t *testing.T) {
	golden(t, false, ready, Type(":gk/ct\n/requiredlabels\n"), WaitFor("/requiredlabels"), Key(tea.KeyEnter), WaitFor("K8sRequiredLabels)"))
}

func TestConstraints(t *testing.T) {
	golden(t, false, ready, Type(":gk/c\n"), WaitFor("max-ten-replicas"))
}

func TestConstraintDetail(t *testing.T) {
	golden(t, false, ready, Type(":gk/c\n/owner\nd"), WaitFor("showing 2 of 3"))
}

func TestConstraintsInNamespaceScope(t *testing.T) {
	golden(t, false, ready, Type(":ns default\n:gk/c\n"), WaitFor("Constraints"))
}

func TestConfig(t *testing.T) {
	golden(t, false, ready, Type(":gk/cfg\n"), WaitFor("gatekeeper-system"))
}

func TestConfigDetail(t *testing.T) {
	golden(t, false, ready, Type(":gk/cfg\nd"), WaitFor("Readiness"))
}

func TestTemplateDetailScrolledToEnd(t *testing.T) {
	golden(t, false, ready, Type(":gk/ct\n/requiredlabels\nd"), WaitFor("Parameters schema"), Type("G"), WaitFor("type: object"))
}
