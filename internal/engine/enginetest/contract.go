// Package enginetest is the contract suite every engine.Engine must pass.
// Engine packages call Run from their own tests with a Harness bound to
// fixture data.
package enginetest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
)

// Harness binds the suite to one engine implementation.
type Harness struct {
	// New returns a fresh engine whose cluster has the engine installed and
	// at least one policy.
	New func(t *testing.T) engine.Engine
	// Informers returns the factory passed to Start. May return nil.
	Informers func(t *testing.T) dynamicinformer.DynamicSharedInformerFactory
	// NotInstalled optionally returns an engine bound to a cluster without it.
	NotInstalled func(t *testing.T) engine.Engine
	// Timeout bounds waits for data after Start. Defaults to 5s.
	Timeout time.Duration
	// Events returns sample Kubernetes Events, at least one of them an
	// admission result. Required when the engine is an engine.EventSource.
	Events func(t *testing.T) []*unstructured.Unstructured
}

var validModes = map[model.EnforcementMode]bool{
	model.ModeAudit: true, model.ModeWarn: true, model.ModeEnforce: true, model.ModeMutate: true, model.ModeUnknown: true,
}

// Run executes the contract suite.
func Run(t *testing.T, h Harness) {
	t.Helper()
	if h.Timeout == 0 {
		h.Timeout = 5 * time.Second
	}
	t.Run("identity", func(t *testing.T) { testIdentity(t, h) })
	t.Run("detect", func(t *testing.T) { testDetect(t, h) })
	t.Run("capabilities match methods", func(t *testing.T) { withStarted(t, h, testCapabilities) })
	t.Run("policies are well formed", func(t *testing.T) { withStarted(t, h, testPolicies) })
	t.Run("violations reference policies", func(t *testing.T) { withStarted(t, h, testViolations) })
	t.Run("views are well formed", func(t *testing.T) { withStarted(t, h, testViews) })
	t.Run("views respect engine filter", func(t *testing.T) { withStarted(t, h, testViewEngineFilter) })
	t.Run("concurrent reads", func(t *testing.T) { withStarted(t, h, testConcurrentReads) })
	t.Run("sync status", func(t *testing.T) { testSync(t, h) })
	t.Run("view columns have valid roles", func(t *testing.T) { withStarted(t, h, testColumnRoles) })
	t.Run("optional interfaces", func(t *testing.T) { withStarted(t, h, testOptional) })
	t.Run("drill-downs resolve", func(t *testing.T) { withStarted(t, h, testDrills) })
	t.Run("policy rows carry their key", func(t *testing.T) { withStarted(t, h, testPolicyRows) })
	t.Run("event source", func(t *testing.T) {
		withStarted(t, h, func(t *testing.T, e engine.Engine) { testEventSource(t, h, e) })
	})
	t.Run("stop is idempotent", func(t *testing.T) {
		e := start(t, h)
		e.Stop()
		e.Stop()
	})
}

func testIdentity(t *testing.T, h Harness) {
	e := h.New(t)
	if e.Name() == "" || strings.ContainsAny(e.Name(), " /:") || strings.ToLower(e.Name()) != e.Name() {
		t.Errorf("Name() = %q: want a non-empty lowercase identifier", e.Name())
	}
	if e.Prefix() == "" || strings.ContainsAny(e.Prefix(), " /:") {
		t.Errorf("Prefix() = %q: want a non-empty token without / or :", e.Prefix())
	}
}

func testDetect(t *testing.T, h Harness) {
	e := h.New(t)
	installed, version, err := e.Detect(context.Background())
	if err != nil || !installed {
		t.Fatalf("Detect() = %v, %q, %v: want installed", installed, version, err)
	}
	if version == "" {
		t.Error("Detect() returned an empty version; use \"unknown\" when it cannot be read")
	}
	if h.NotInstalled == nil {
		return
	}
	installed, _, err = h.NotInstalled(t).Detect(context.Background())
	if err != nil || installed {
		t.Errorf("Detect() on a cluster without the engine = %v, %v: want false, nil", installed, err)
	}
}

func start(t *testing.T, h Harness) engine.Engine {
	t.Helper()
	e := h.New(t)
	var f dynamicinformer.DynamicSharedInformerFactory
	if h.Informers != nil {
		f = h.Informers(t)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := e.Start(ctx, f, func() {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f != nil {
		f.Start(ctx.Done())
	}
	t.Cleanup(e.Stop)
	waitForPolicies(t, e, h.Timeout)
	return e
}

func withStarted(t *testing.T, h Harness, fn func(*testing.T, engine.Engine)) {
	fn(t, start(t, h))
}

// waitForPolicies waits until the informers delivered data. It polls the
// engine's own snapshot, not the cluster.
func waitForPolicies(t *testing.T, e engine.Engine, timeout time.Duration) {
	t.Helper()
	if !e.Capabilities().Policies {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ps, err := e.Policies(); err == nil && len(ps) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no policies visible %s after Start", timeout)
}

func testCapabilities(t *testing.T, e engine.Engine) {
	caps := e.Capabilities()
	check := func(name string, supported bool, err error) {
		t.Helper()
		if supported && err != nil {
			t.Errorf("%s: capability set but returned %v", name, err)
		}
		if !supported && !errors.Is(err, engine.ErrNotSupported) {
			t.Errorf("%s: capability unset, want ErrNotSupported, got %v", name, err)
		}
	}
	_, err := e.Policies()
	check("Policies", caps.Policies, err)
	_, err = e.Violations()
	check("Violations", caps.Violations, err)
	_, err = e.Mutations()
	check("Mutations", caps.Mutations, err)
	_, err = e.Health()
	check("Health", caps.Health, err)
	_, err = e.Evaluator()
	check("Evaluator", caps.Evaluator, err)
	_, err = e.Actions()
	check("Actions", caps.Actions, err)
}

func testPolicies(t *testing.T, e engine.Engine) {
	ps, err := e.Policies()
	if err != nil {
		t.Skipf("policies not supported: %v", err)
	}
	seen := map[model.PolicyKey]bool{}
	for _, p := range ps {
		for _, g := range p.Guidance {
			if strings.TrimSpace(g.Text) == "" || g.From == "" {
				t.Errorf("policy %s: guidance %+v needs text and where it came from", p.Key(), g)
			}
		}
		if p.Engine != e.Name() {
			t.Errorf("policy %s: Engine = %q, want %q", p.Key(), p.Engine, e.Name())
		}
		if p.Kind == "" || p.Name == "" {
			t.Errorf("policy %+v: Kind and Name are required", p.Key())
		}
		if !validModes[p.Enforcement.Mode] {
			t.Errorf("policy %s: invalid mode %q", p.Key(), p.Enforcement.Mode)
		}
		if p.ViolationCount < 0 {
			t.Errorf("policy %s: negative violation count", p.Key())
		}
		if seen[p.Key()] {
			t.Errorf("policy %s: duplicate key", p.Key())
		}
		seen[p.Key()] = true
	}
}

func testViolations(t *testing.T, e engine.Engine) {
	vs, err := e.Violations()
	if err != nil {
		t.Skipf("violations not supported: %v", err)
	}
	ps, _ := e.Policies()
	keys := map[model.PolicyKey]bool{}
	for _, p := range ps {
		keys[p.Key()] = true
	}
	for _, v := range vs {
		if v.Engine != e.Name() || v.Policy.Engine != e.Name() {
			t.Errorf("violation %s: engine fields must be %q", v.Policy, e.Name())
		}
		if len(keys) > 0 && !keys[v.Policy] {
			t.Errorf("violation references unknown policy %s", v.Policy)
		}
		if v.Target.Kind == "" {
			t.Errorf("violation of %s: target kind is required", v.Policy)
		}
		if !validModes[v.Enforcement.Mode] {
			t.Errorf("violation of %s: invalid mode %q", v.Policy, v.Enforcement.Mode)
		}
	}
}

func testViews(t *testing.T, e engine.Engine) {
	seen := map[string]bool{}
	for _, v := range e.Views() {
		for _, cmd := range append([]string{v.Command}, v.Aliases...) {
			if !strings.HasPrefix(cmd, e.Prefix()+"/") {
				t.Errorf("view command %q must start with %q", cmd, e.Prefix()+"/")
			}
			if seen[cmd] {
				t.Errorf("duplicate view command %q", cmd)
			}
			seen[cmd] = true
		}
		if v.Title == "" || len(v.Columns) == 0 || v.Rows == nil {
			t.Errorf("view %q: Title, Columns and Rows are required", v.Command)
			continue
		}
		rows, err := v.Rows(engine.Filter{})
		if err != nil {
			t.Errorf("view %q: Rows: %v", v.Command, err)
		}
		checkRows(t, v, rows)
	}
}

func checkRows(t *testing.T, v engine.View, rows []engine.Row) {
	t.Helper()
	ids := map[string]bool{}
	for _, r := range rows {
		if r.ID == "" || ids[r.ID] {
			t.Errorf("view %q: row IDs must be unique and non-empty, got %q", v.Command, r.ID)
		}
		ids[r.ID] = true
		if len(r.Cells) != len(v.Columns) {
			t.Errorf("view %q row %q: %d cells for %d columns", v.Command, r.ID, len(r.Cells), len(v.Columns))
		}
		if r.Detail != nil {
			if d, err := r.Detail(); err != nil || d.Title == "" {
				t.Errorf("view %q row %q: Detail() = %q, %v", v.Command, r.ID, d.Title, err)
			}
		}
		if r.Drill != nil && r.Drill.Command == "" {
			t.Errorf("view %q row %q: Drill without a command", v.Command, r.ID)
		}
	}
}

func testViewEngineFilter(t *testing.T, e engine.Engine) {
	for _, v := range e.Views() {
		rows, err := v.Rows(engine.Filter{Engine: "some-other-engine"})
		if err != nil {
			t.Errorf("view %q: %v", v.Command, err)
		}
		if len(rows) != 0 {
			t.Errorf("view %q returned %d rows for another engine's filter", v.Command, len(rows))
		}
	}
}

func testConcurrentReads(t *testing.T, e engine.Engine) {
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Policies()
			_, _ = e.Violations()
			_, _ = e.Health()
			_ = e.AccessIssues()
			for _, v := range e.Views() {
				_, _ = v.Rows(engine.Filter{})
			}
		}()
	}
	wg.Wait()
}

// testSync: not synced before Start; synced (nothing pending) soon after,
// with a notify delivered at or after that point so UIs repaint.
func testSync(t *testing.T, h Harness) {
	e := h.New(t)
	if st := e.SyncStatus(); st.Synced {
		t.Errorf("SyncStatus before Start = %+v: must not be synced", st)
	}
	var f dynamicinformer.DynamicSharedInformerFactory
	if h.Informers != nil {
		f = h.Informers(t)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	notified := make(chan struct{}, 64)
	if err := e.Start(ctx, f, func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f != nil {
		f.Start(ctx.Done())
	}
	t.Cleanup(e.Stop)

	deadline := time.Now().Add(h.Timeout)
	var st engine.SyncStatus
	for time.Now().Before(deadline) {
		if st = e.SyncStatus(); st.Synced {
			break
		}
		for _, p := range st.Pending {
			if p == "" {
				t.Fatal("SyncStatus.Pending contains an empty name")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !st.Synced || len(st.Pending) != 0 {
		t.Fatalf("not synced %s after Start: %+v", h.Timeout, st)
	}
	// Drain notifications sent up to now, then require one more within the
	// timeout only if none arrived at all: a synced engine must have told us.
	select {
	case <-notified:
	case <-time.After(h.Timeout):
		t.Error("engine became synced without ever calling notify")
	}
	e.Stop()
	if st := e.SyncStatus(); st.Synced {
		t.Errorf("SyncStatus after Stop = %+v: must not be synced", st)
	}
}

// testColumnRoles: every role is known, a role is used at most once per
// view (sorting and filtering pick the first match), and a default sort
// names a column the view has.
func testColumnRoles(t *testing.T, e engine.Engine) {
	for _, v := range e.Views() {
		seen := map[engine.Role]bool{}
		for _, c := range v.Columns {
			if !engine.ValidRoles[c.Role] {
				t.Errorf("view %q column %q: unknown role %q", v.Command, c.Title, c.Role)
			}
			if c.Role != engine.RoleNone && seen[c.Role] {
				t.Errorf("view %q: role %q used twice", v.Command, c.Role)
			}
			seen[c.Role] = true
		}
		if r := v.DefaultSort.Role; r != engine.RoleNone && !seen[r] {
			t.Errorf("view %q: default sort by %q but no column has that role", v.Command, r)
		}
	}
}

// testOptional checks the optional interfaces engines may implement.
func testOptional(t *testing.T, e engine.Engine) {
	if n, ok := e.(engine.KindNamer); ok {
		ps, _ := e.Policies()
		for _, p := range ps {
			s := n.ShortKind(p.Kind)
			if s == "" || len(s) > len(p.Kind) {
				t.Errorf("ShortKind(%q) = %q: want a non-empty alias no longer than the kind", p.Kind, s)
			}
		}
	}
	if p, ok := e.(engine.Platform); ok {
		info := p.PlatformInfo()
		if info.Namespace == "" || info.AuditSource == "" {
			t.Errorf("PlatformInfo() = %+v: Namespace and AuditSource are required", info)
		}
	}
	if s, ok := e.(engine.PolicyStatuser); ok {
		st := s.PolicyStatus()
		bad := st.NotEnforced < 0 || st.CompileErrors < 0 || st.MissingCRD < 0 || st.NotReady < 0
		if bad || st.Loaded < 0 || st.Loaded > st.Total {
			t.Errorf("PolicyStatus() = %+v: counts must be >= 0 and Loaded <= Total", st)
		}
	}
	if a, ok := e.(engine.Auditor); ok {
		if d := a.AuditInterval(); d < 0 {
			t.Errorf("AuditInterval() = %s: must be >= 0 (0 = audit disabled; else fall back to the engine default)", d)
		}
	}
	ps, _ := e.Policies()
	for _, p := range ps {
		if !p.ViolationsKnown && p.ViolationCount != 0 {
			t.Errorf("policy %s: a count (%d) without ViolationsKnown would be hidden", p.Key(), p.ViolationCount)
		}
	}
}

// sharedCommands are the cross-engine views a row may drill into.
var sharedCommands = map[string]bool{"pol": true, "v": true, "mut": true, "health": true}

// testDrills: enter must always lead somewhere real. A Drill names a
// shared view or one of the engine's own views; rows without a Drill fall
// back to describe, so they need a Detail or an Object.
func testDrills(t *testing.T, e engine.Engine) {
	own := map[string]bool{}
	for _, v := range e.Views() {
		own[v.Command] = true
	}
	for _, v := range e.Views() {
		rows, _ := v.Rows(engine.Filter{})
		for _, r := range rows {
			if r.Drill != nil && !sharedCommands[r.Drill.Command] && !own[r.Drill.Command] {
				t.Errorf("view %q row %q drills into %q, which no view provides", v.Command, r.ID, r.Drill.Command)
			}
			if r.Drill == nil && r.Detail == nil && r.Object == nil {
				t.Errorf("view %q row %q: enter does nothing (no Drill, Detail or Object)", v.Command, r.ID)
			}
		}
	}
}

// unrelatedEvent is an ordinary scheduler Event no engine may claim.
func unrelatedEvent() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"name": "web-0.1", "namespace": "default"},
		"involvedObject": map[string]any{"kind": "Pod", "name": "web-0", "namespace": "default"},
		"reason":         "Scheduled", "source": map[string]any{"component": "default-scheduler"},
		"message": "Successfully assigned default/web-0", "type": "Normal",
		"lastTimestamp": "2026-01-01T00:00:00Z",
	}}
}

// testEventSource checks engines that record admission events: selectors
// are server-side filters, samples parse into complete events, and other
// Events are not claimed.
func testEventSource(t *testing.T, h Harness, e engine.Engine) {
	src, ok := e.(engine.EventSource)
	if !ok {
		return
	}
	sels := src.EventSelectors()
	if len(sels) == 0 {
		t.Error("EventSelectors() is empty: an EventSource must select its Events")
	}
	for _, sel := range sels {
		if !strings.Contains(sel.FieldSelector, "reason=") {
			t.Errorf("selector %+v: filter by reason server-side, not every Event in the cluster", sel)
		}
	}
	if on, hint := src.EventsEnabled(); !on && hint == "" {
		t.Error("EventsEnabled() = false without a hint on how to turn events on")
	}
	if _, ok := src.ParseEvent(unrelatedEvent()); ok {
		t.Error("ParseEvent claimed a scheduler Event")
	}
	if h.Events == nil {
		t.Fatal("engine is an EventSource: set Harness.Events to sample admission Events")
	}
	n := 0
	for _, ev := range h.Events(t) {
		a, ok := src.ParseEvent(ev)
		if !ok {
			continue
		}
		n++
		if a.Engine != e.Name() || a.Policy.Engine != e.Name() {
			t.Errorf("event %s: Engine %q / Policy.Engine %q, want %q", ev.GetName(), a.Engine, a.Policy.Engine, e.Name())
		}
		if a.Policy.Kind == "" || a.Policy.Name == "" || a.Target.Kind == "" || a.Target.Name == "" {
			t.Errorf("event %s: policy %+v and target %+v need a kind and a name", ev.GetName(), a.Policy, a.Target)
		}
		if !validModes[a.Enforcement.Mode] || a.Time.IsZero() || a.Count < 1 {
			t.Errorf("event %s: mode %q, time %s, count %d: want a valid mode, a time and count >= 1",
				ev.GetName(), a.Enforcement.Mode, a.Time, a.Count)
		}
	}
	if n == 0 {
		t.Error("no sample Event parsed: Harness.Events needs admission results")
	}
}

// testPolicyRows checks that every row drilling into one policy's
// violations also carries that policy's key (for i, the enforcement
// impact), and that the key names a policy the engine reports.
func testPolicyRows(t *testing.T, e engine.Engine) {
	ps, _ := e.Policies()
	known := map[model.PolicyKey]bool{}
	for _, p := range ps {
		known[p.Key()] = true
	}
	for _, v := range e.Views() {
		rows, _ := v.Rows(engine.Filter{})
		for _, r := range rows {
			if r.Policy != nil && !known[*r.Policy] {
				t.Errorf("view %q row %q: Policy %s is not among Policies()", v.Command, r.ID, *r.Policy)
			}
			d := r.Drill
			if d == nil || d.Command != "v" || d.Filter.PolicyName == "" {
				continue
			}
			want := model.PolicyKey{Engine: e.Name(), Kind: d.Filter.PolicyKind, Namespace: d.Filter.PolicyNamespace, Name: d.Filter.PolicyName}
			if r.Policy == nil || *r.Policy != want {
				t.Errorf("view %q row %q drills into the violations of %s but Policy = %v", v.Command, r.ID, want, r.Policy)
			}
		}
	}
}
