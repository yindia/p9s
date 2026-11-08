package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/session"
)

type fakeCluster struct {
	name string
	dyn  *dynamicfake.FakeDynamicClient
}

func (c *fakeCluster) Context() string            { return c.name }
func (c *fakeCluster) Dynamic() dynamic.Interface { return c.dyn }
func (c *fakeCluster) Discovery() discovery.DiscoveryInterface {
	return &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}}
}
func (c *fakeCluster) NewInformerFactory() dynamicinformer.DynamicSharedInformerFactory {
	return dynamicinformer.NewDynamicSharedInformerFactory(c.dyn, 0)
}
func (c *fakeCluster) Get(context.Context, model.ObjectRef) (*unstructured.Unstructured, error) {
	return &unstructured.Unstructured{Object: map[string]any{"kind": "Pod"}}, nil
}

type connector struct {
	current string
	fail    map[string]error
}

func (c *connector) Contexts() ([]model.KubeContext, string, error) {
	return []model.KubeContext{{Name: "a"}, {Name: "b"}}, c.current, nil
}

func (c *connector) Connect(name string) (session.Cluster, error) {
	if name == "" {
		name = c.current
	}
	if err := c.fail[name]; err != nil {
		return nil, err
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	ns := func(n string) runtime.Object {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": n}}}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{nsGVR: "NamespaceList"}, ns("zeta"), ns("alpha"))
	return &fakeCluster{name: name, dyn: dyn}, nil
}

func newSession(t *testing.T, conn *connector, engines ...*fake.Engine) *session.Session {
	t.Helper()
	var regs []engine.Registration
	for _, e := range engines {
		regs = append(regs, engine.Registration{Name: e.Name(), Factory: e.Factory()})
	}
	s, err := session.New(session.Options{Connector: conn, Engines: regs, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestConnectStartsOnlyDetectedEngines(t *testing.T) {
	on := fake.New(fake.Options{Name: "on", Installed: true, Caps: engine.Capabilities{Policies: true}})
	off := fake.New(fake.Options{Name: "off"})
	broken := fake.New(fake.Options{Name: "broken", Installed: true, DetectErr: errors.New("boom")})
	s := newSession(t, &connector{current: "a"}, on, off, broken)

	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.ContextName(); got != "a" {
		t.Errorf("ContextName = %q", got)
	}
	active := s.Engines()
	if len(active) != 1 || active[0].Name() != "on" {
		t.Errorf("active engines = %v", names(active))
	}
	if !on.Started() || off.Started() || broken.Started() {
		t.Error("only detected, error-free engines may start")
	}
	if len(s.Detections()) != 3 {
		t.Errorf("Detections = %d, want 3", len(s.Detections()))
	}
}

func TestTwoEnginesActiveAtOnce(t *testing.T) {
	a := fake.New(fake.Options{Name: "a", Installed: true})
	b := fake.New(fake.Options{Name: "b", Installed: true})
	s := newSession(t, &connector{current: "a"}, a, b)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := names(s.Engines()); len(got) != 2 {
		t.Errorf("active = %v, want both", got)
	}
}

func TestSwitchContextRestartsEngines(t *testing.T) {
	// Real factories build a fresh engine per context; mirror that.
	var built []*fake.Engine
	factory := func(engine.Cluster) engine.Engine {
		e := fake.New(fake.Options{Name: "e", Installed: true})
		built = append(built, e)
		return e
	}
	s, err := session.New(session.Options{Connector: &connector{current: "a"}, Engines: []engine.Registration{{Name: "e", Factory: factory}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.SwitchContext(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if s.ContextName() != "b" || len(built) != 2 || built[0].Started() || !built[1].Started() {
		t.Errorf("after switch: ctx=%q, old engine must stop and new one start", s.ContextName())
	}
	ctxs, _ := s.Contexts()
	if !ctxs[1].Current || ctxs[0].Current {
		t.Errorf("current flag not moved: %+v", ctxs)
	}
}

func TestSwitchToBrokenContextKeepsOldConnection(t *testing.T) {
	conn := &connector{current: "a", fail: map[string]error{"b": errors.New("unreachable")}}
	s := newSession(t, conn, fake.New(fake.Options{Name: "e", Installed: true}))
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.SwitchContext(context.Background(), "b"); err == nil {
		t.Fatal("expected error")
	}
	if s.ContextName() != "a" || len(s.Engines()) != 1 {
		t.Error("a failed switch must keep the previous connection")
	}
}

func TestUpdatesAreCoalesced(t *testing.T) {
	e := fake.New(fake.Options{Name: "e", Installed: true})
	s := newSession(t, &connector{current: "a"}, e)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	drain(s)
	for range 50 {
		e.SetData(fake.Data{})
	}
	select {
	case <-s.Updates():
	case <-time.After(time.Second):
		t.Fatal("no update delivered")
	}
	select {
	case <-s.Updates():
		t.Fatal("bursts must coalesce into one pending update")
	default:
	}
}

func TestGetObjectRequiresConnection(t *testing.T) {
	s := newSession(t, &connector{current: "a"})
	if _, err := s.GetObject(context.Background(), model.ObjectRef{}); err == nil {
		t.Error("GetObject before Connect should fail")
	}
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObject(context.Background(), model.ObjectRef{Kind: "Pod", Name: "x"}); err != nil {
		t.Error(err)
	}
	if !s.ReadOnly() {
		t.Error("ReadOnly lost")
	}
}

func TestNewRequiresConnector(t *testing.T) {
	if _, err := session.New(session.Options{}); err == nil {
		t.Error("expected error")
	}
}

func drain(s *session.Session) {
	for {
		select {
		case <-s.Updates():
		default:
			return
		}
	}
}

func names(es []engine.Engine) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestNamespaces(t *testing.T) {
	s := newSession(t, &connector{current: "a"})
	if _, err := s.Namespaces(context.Background()); err == nil {
		t.Error("Namespaces before Connect should fail")
	}
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	names, err := s.Namespaces(context.Background())
	if err != nil || len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Errorf("Namespaces = %v, %v; want sorted [alpha zeta]", names, err)
	}
}

func TestWaitForSyncAndRunOnce(t *testing.T) {
	slow := fake.New(fake.Options{Name: "slow", Installed: true, Unsynced: true})
	ready := fake.New(fake.Options{Name: "ready", Installed: true})
	s := newSession(t, &connector{current: "a"}, slow, ready)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Synced() {
		t.Fatal("must not be synced while an engine is pending")
	}
	err := s.WaitForSync(context.Background(), 100*time.Millisecond)
	var te *session.SyncTimeoutError
	if !errors.As(err, &te) || te.Pending["slow"] == nil || te.Pending["ready"] != nil {
		t.Fatalf("WaitForSync err = %v; want a timeout naming only slow", err)
	}
	if want := "caches did not sync within 100ms: slow (rules.slow.example)"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	go func() { time.Sleep(20 * time.Millisecond); slow.SetSynced(true) }()
	if err := s.WaitForSync(context.Background(), 2*time.Second); err != nil {
		t.Fatalf("WaitForSync after sync: %v", err)
	}

	once, err := session.RunOnce(context.Background(), session.Options{
		Connector: &connector{current: "a"},
		Engines:   []engine.Registration{{Name: "ready", Factory: fake.New(fake.Options{Name: "ready", Installed: true}).Factory()}},
	}, time.Second)
	if err != nil || !once.Synced() {
		t.Fatalf("RunOnce = %v, %v", once, err)
	}
	once.Close()

	_, err = session.RunOnce(context.Background(), session.Options{
		Connector: &connector{current: "a"},
		Engines:   []engine.Registration{{Name: "slow", Factory: fake.New(fake.Options{Name: "slow", Installed: true, Unsynced: true}).Factory()}},
	}, 50*time.Millisecond)
	if !errors.As(err, &te) {
		t.Errorf("RunOnce with a stuck engine = %v; want SyncTimeoutError", err)
	}
}

func TestNoEventSourceMeansNoEventsWatcher(t *testing.T) {
	s := newSession(t, &connector{current: "a"}, fake.New(fake.Options{Name: "a", Installed: true}))
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Events() != nil {
		t.Error("Events() without an EventSource engine: want nil so views say no engine records them")
	}
	if _, pending := s.Pending()["events"]; pending {
		t.Error("an absent events watcher must not hold up sync")
	}
}
