// Package fake is an in-memory engine used only by tests. It proves the UI
// and session work with any engine, and with several engines at once.
package fake

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/client-go/dynamic/dynamicinformer"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
)

// Data is the engine's content. Replace it with SetData to simulate a watch event.
type Data struct {
	Policies   []model.Policy
	Violations []model.Violation
	Mutations  []model.Mutation
	Health     []model.HealthIssue
	Access     []model.AccessIssue
}

// Options configure a fake engine.
type Options struct {
	Name      string
	Prefix    string
	Installed bool
	Version   string
	DetectErr error
	// Unsynced keeps the engine reporting a pending sync after Start, to
	// test partial-data rendering. Flip it with SetSynced.
	Unsynced bool
	Caps     engine.Capabilities
	Data     Data
}

// Engine is a fake engine.Engine.
type Engine struct {
	opts Options

	mu       sync.RWMutex
	data     Data
	notify   engine.Notify
	started  bool
	unsynced bool
}

var _ engine.Engine = (*Engine)(nil)

// New returns a fake engine.
func New(opts Options) *Engine {
	if opts.Name == "" {
		opts.Name = "fake"
	}
	if opts.Prefix == "" {
		opts.Prefix = opts.Name
	}
	return &Engine{opts: opts, data: opts.Data, unsynced: opts.Unsynced}
}

// Factory returns an engine.Factory that always yields e, ignoring the cluster.
func (e *Engine) Factory() engine.Factory { return func(engine.Cluster) engine.Engine { return e } }

// SetData swaps the content and notifies like an informer event would.
func (e *Engine) SetData(d Data) {
	e.mu.Lock()
	e.data = d
	n := e.notify
	e.mu.Unlock()
	if n != nil {
		n()
	}
}

// SetSynced flips the sync state and notifies, like informers finishing.
func (e *Engine) SetSynced(synced bool) {
	e.mu.Lock()
	e.unsynced = !synced
	n := e.notify
	e.mu.Unlock()
	if n != nil {
		n()
	}
}

// SyncStatus implements engine.Engine.
func (e *Engine) SyncStatus() engine.SyncStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	switch {
	case !e.started:
		return engine.SyncStatus{Pending: []string{"not started"}}
	case e.unsynced:
		return engine.SyncStatus{Pending: []string{"rules." + e.opts.Name + ".example"}}
	}
	return engine.SyncStatus{Synced: true}
}

// Started reports whether Start was called without a later Stop.
func (e *Engine) Started() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.started
}

func (e *Engine) Name() string                         { return e.opts.Name }
func (e *Engine) Prefix() string                       { return e.opts.Prefix }
func (e *Engine) Capabilities() engine.Capabilities    { return e.opts.Caps }
func (e *Engine) Evaluator() (engine.Evaluator, error) { return nil, engine.ErrNotSupported }
func (e *Engine) Actions() (engine.Actions, error)     { return nil, engine.ErrNotSupported }

func (e *Engine) Detect(context.Context) (bool, string, error) {
	return e.opts.Installed, e.opts.Version, e.opts.DetectErr
}

func (e *Engine) Start(_ context.Context, _ dynamicinformer.DynamicSharedInformerFactory, n engine.Notify) error {
	e.mu.Lock()
	e.notify, e.started = n, true
	synced := !e.unsynced
	e.mu.Unlock()
	if synced && n != nil {
		n() // in-memory data is "synced" at once; say so like informers would
	}
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notify, e.started = nil, false
}

func (e *Engine) Policies() ([]model.Policy, error) {
	if !e.opts.Caps.Policies {
		return nil, engine.ErrNotSupported
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]model.Policy(nil), e.data.Policies...), nil
}

func (e *Engine) Violations() ([]model.Violation, error) {
	if !e.opts.Caps.Violations {
		return nil, engine.ErrNotSupported
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]model.Violation(nil), e.data.Violations...), nil
}

func (e *Engine) Mutations() ([]model.Mutation, error) {
	if !e.opts.Caps.Mutations {
		return nil, engine.ErrNotSupported
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]model.Mutation(nil), e.data.Mutations...), nil
}

func (e *Engine) Health() ([]model.HealthIssue, error) {
	if !e.opts.Caps.Health {
		return nil, engine.ErrNotSupported
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]model.HealthIssue(nil), e.data.Health...), nil
}

func (e *Engine) AccessIssues() []model.AccessIssue {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]model.AccessIssue(nil), e.data.Access...)
}

// Views exposes one engine-specific view listing policies, so tests can
// exercise the "<prefix>/..." command path.
func (e *Engine) Views() []engine.View {
	if !e.opts.Caps.Policies {
		return nil // hide views the engine cannot back
	}
	return []engine.View{{
		Command:     e.opts.Prefix + "/rules",
		Title:       "Rules",
		Description: "fake engine rules",
		Columns: []engine.Column{
			{Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "MODE", Role: engine.RoleMode},
			{Title: "VIOLATIONS", Role: engine.RoleCount},
		},
		Rows: e.ruleRows,
	}}
}

func (e *Engine) ruleRows(f engine.Filter) ([]engine.Row, error) {
	ps, err := e.Policies()
	if err != nil {
		return nil, err
	}
	var rows []engine.Row
	for _, p := range ps {
		if !f.MatchesPolicy(p.Key()) {
			continue
		}
		key := p.Key()
		rows = append(rows, engine.Row{
			ID:    key.String(),
			Cells: []engine.Cell{engine.TextCell(p.Name), engine.ModeCell(p.Enforcement), engine.CountCell(p.ViolationCount)},
			Detail: func() (engine.Detail, error) {
				return engine.Detail{Title: p.Name, Sections: []engine.Section{{Title: "Rule", Body: fmt.Sprintf("%s in %s mode", p.Name, p.Enforcement.Mode)}}}, nil
			},
			Drill:  &engine.Target{Command: "v", Filter: engine.Filter{Engine: p.Engine, PolicyKind: p.Kind, PolicyName: p.Name, PolicyNamespace: p.Namespace}},
			Policy: &key,
		})
	}
	return rows, nil
}
