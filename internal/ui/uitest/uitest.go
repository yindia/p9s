// Package uitest drives the real Bubble Tea program with teatest and
// compares the final screen with golden files. Run `go test ./... -update`
// to regenerate goldens after an intentional UI change.
package uitest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/ui"
	"github.com/yindia/p9s/internal/views"
)

const (
	// Width and Height of the virtual terminal.
	Width  = 120
	Height = 24
)

// WaitTimeout bounds each WaitFor and the final model. Fake backends answer
// at once; tests against a real cluster raise it (test/e2e uses 30s).
var WaitTimeout = 5 * time.Second

func init() {
	// Goldens are plain text: color would make them unreadable diffs.
	lipgloss.SetColorProfile(termenv.Ascii)
}

// Backend is an in-memory ui.Backend.
type Backend struct {
	mu         sync.Mutex
	Context    string
	Ctxs       []model.KubeContext
	Engs       []engine.Engine
	Dets       []engine.Detection
	Objects    map[string]*unstructured.Unstructured
	NsNames    []string
	NsErr      error
	ConnectErr error
	ReadWrite  bool
	// Ownership resolves violation owners; nil disables grouping by owner.
	Ownership *StaticOwners
	// Plat feeds the health dashboard; nil disables it.
	Plat *StaticPlatform
	// Evs feeds the recent tab of :v; nil disables it.
	Evs *StaticEvents
	// Updated is reported by UpdatedAt.
	Updated time.Time
	updates chan struct{}
}

// StaticOwners is a fixed views.Owners for tests.
type StaticOwners struct {
	query.StaticOwners
	Denied []model.AccessIssue
}

// AccessIssues implements views.Owners.
func (s *StaticOwners) AccessIssues() []model.AccessIssue { return s.Denied }

// SyncStatus implements views.Owners.
func (s *StaticOwners) SyncStatus() engine.SyncStatus { return engine.SyncStatus{Synced: true} }

// StaticPlatform is a fixed views.Platform for tests.
type StaticPlatform struct {
	State  query.Platform
	Denied []model.AccessIssue
}

// Snapshot implements views.Platform.
func (p *StaticPlatform) Snapshot() query.Platform { return p.State }

// AccessIssues implements views.Platform.
func (p *StaticPlatform) AccessIssues() []model.AccessIssue { return p.Denied }

// SyncStatus implements views.Platform.
func (p *StaticPlatform) SyncStatus() engine.SyncStatus { return engine.SyncStatus{Synced: true} }

// StaticEvents is a fixed views.Events for tests.
type StaticEvents struct {
	List   []model.AdmissionEvent
	Status []views.EventStatus
	Denied []model.AccessIssue
}

// Events implements views.Events.
func (e *StaticEvents) Events() []model.AdmissionEvent { return e.List }

// Statuses implements views.Events.
func (e *StaticEvents) Statuses() []views.EventStatus { return e.Status }

// AccessIssues implements views.Events.
func (e *StaticEvents) AccessIssues() []model.AccessIssue { return e.Denied }

// SyncStatus implements views.Events.
func (e *StaticEvents) SyncStatus() engine.SyncStatus { return engine.SyncStatus{Synced: true} }

// Events implements ui.Backend.
func (b *Backend) Events() views.Events {
	if b.Evs == nil {
		return nil
	}
	return b.Evs
}

// Platform implements ui.Backend.
func (b *Backend) Platform() views.Platform {
	if b.Plat == nil {
		return nil
	}
	return b.Plat
}

// UpdatedAt implements ui.Backend.
func (b *Backend) UpdatedAt() time.Time { return b.Updated }

// Owners implements ui.Backend.
func (b *Backend) Owners() views.Owners {
	if b.Ownership == nil {
		return nil
	}
	return b.Ownership
}

// NewBackend returns a connected-on-demand backend with the given engines active.
func NewBackend(engines ...engine.Engine) *Backend {
	b := &Backend{
		Context: "kind-demo",
		Ctxs:    []model.KubeContext{{Name: "kind-demo", Cluster: "kind-demo", User: "kind-demo"}, {Name: "prod", Cluster: "prod-eu", User: "sre", Namespace: "payments"}},
		Objects: map[string]*unstructured.Unstructured{},
		updates: make(chan struct{}, 1),
	}
	for _, e := range engines {
		_, v, _ := e.Detect(context.Background())
		b.Engs = append(b.Engs, e)
		b.Dets = append(b.Dets, engine.Detection{Engine: e, Installed: true, Version: v})
	}
	return b
}

// Connect starts the engines like the real session does, so they report
// synced and notify through Updates.
func (b *Backend) Connect(ctx context.Context) error {
	if b.ConnectErr != nil {
		return b.ConnectErr
	}
	for _, e := range b.Engs {
		if err := e.Start(ctx, nil, b.Notify); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) SwitchContext(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Context = name
	return nil
}

func (b *Backend) ContextName() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Context
}

func (b *Backend) Contexts() ([]model.KubeContext, error) {
	cur := b.ContextName()
	out := make([]model.KubeContext, len(b.Ctxs))
	for i, c := range b.Ctxs {
		c.Current = c.Name == cur
		out[i] = c
	}
	return out, nil
}

func (b *Backend) Detections() []engine.Detection {
	if b.ConnectErr != nil {
		return nil // like the real session: no connection, no detection
	}
	return b.Dets
}

func (b *Backend) Engines() []engine.Engine {
	if b.ConnectErr != nil {
		return nil
	}
	return b.Engs
}

func (b *Backend) GetObject(_ context.Context, ref model.ObjectRef) (*unstructured.Unstructured, error) {
	if o, ok := b.Objects[ref.String()]; ok {
		return o, nil
	}
	return nil, errors.New(`pods "` + ref.Name + `" is forbidden: User "viewer" cannot get resource`)
}

func (b *Backend) Namespaces(context.Context) ([]string, error) { return b.NsNames, b.NsErr }

func (b *Backend) Updates() <-chan struct{} { return b.updates }

func (b *Backend) ReadOnly() bool { return !b.ReadWrite }

// Notify simulates a coalesced engine update.
func (b *Backend) Notify() {
	select {
	case b.updates <- struct{}{}:
	default:
	}
}

// Runner wraps a running program. Waits search all output produced since
// the last input step, so several waits can match the same frame.
type Runner struct {
	tm  *teatest.TestModel
	buf bytes.Buffer
}

// input discards output rendered before the next input, then runs fn.
func (r *Runner) input(t *testing.T, fn func()) {
	t.Helper()
	r.drain(t)
	r.buf.Reset()
	fn()
}

func (r *Runner) drain(t *testing.T) {
	t.Helper()
	b, err := io.ReadAll(r.tm.Output())
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	r.buf.Write(b)
}

// Step is one interaction with the running program.
type Step func(t *testing.T, r *Runner)

// Type sends text, translating "\n" to Enter, "\x1b" to Esc and "\t" to Tab.
func Type(s string) Step {
	return func(t *testing.T, r *Runner) {
		r.input(t, func() {
			for _, c := range s {
				switch c {
				case '\n':
					r.tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
				case '\x1b':
					r.tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
				case '\t':
					r.tm.Send(tea.KeyMsg{Type: tea.KeyTab})
				default:
					r.tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{c}})
				}
			}
		})
	}
}

// Key sends a special key such as tea.KeyDown.
func Key(k tea.KeyType) Step {
	return func(t *testing.T, r *Runner) { r.input(t, func() { r.tm.Send(tea.KeyMsg{Type: k}) }) }
}

// Do runs fn as an input step, e.g. to simulate an informer event.
func Do(fn func()) Step {
	return func(t *testing.T, r *Runner) { r.input(t, fn) }
}

// WaitFor blocks until output since the last input contains any of texts.
// This is the deterministic way to wait for async work.
func WaitFor(texts ...string) Step {
	return func(t *testing.T, r *Runner) {
		t.Helper()
		deadline := time.Now().Add(WaitTimeout)
		for {
			r.drain(t)
			for _, text := range texts {
				if bytes.Contains(r.buf.Bytes(), []byte(text)) {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q; last output:\n%s", texts, tail(r.buf.String()))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func tail(s string) string {
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}

// Options returns deterministic UI options.
func Options(now time.Time) ui.Options {
	return ui.Options{
		Now: func() time.Time { return now }, NoHighlight: true, TickInterval: -1, Editor: "true",
		// Most goldens exercise a table view; overview tests set it explicitly.
		InitialCommand: "pol",
		Copy:           func(string) error { return nil }, // never touch the real clipboard in tests
	}
}

// Run starts the app at the default size, waits for it to connect, runs
// steps and returns the final screen.
func Run(t *testing.T, b ui.Backend, opts ui.Options, steps ...Step) string {
	t.Helper()
	return RunSized(t, Width, Height, b, opts, steps...)
}

// RunSized is Run with an explicit terminal size.
func RunSized(t *testing.T, width, height int, b ui.Backend, opts ui.Options, steps ...Step) string {
	t.Helper()
	app := ui.New(b, opts)
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(width, height))
	r := &Runner{tm: tm}
	WaitFor("connected to", "connect failed")(t, r)
	for _, s := range steps {
		s(t, r)
	}
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(WaitTimeout)).(*ui.App)
	return final.View()
}

// RequireGolden compares a screen with testdata/<TestName>.golden.
func RequireGolden(t *testing.T, screen string) {
	t.Helper()
	golden.RequireEqual(t, []byte(trimRight(screen)))
}

// trimRight drops trailing spaces so goldens stay diff-friendly.
func trimRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}
