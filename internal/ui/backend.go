// Package ui is the Bubble Tea terminal UI. It depends only on
// internal/model and internal/engine; engine-specific screens come from
// Engine.Views().
package ui

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/views"
)

// Backend is what the UI needs from the live session.
type Backend interface {
	Connect(ctx context.Context) error
	SwitchContext(ctx context.Context, name string) error
	ContextName() string
	Contexts() ([]model.KubeContext, error)
	// Detections covers every selected engine, installed or not.
	Detections() []engine.Detection
	// Engines are the active (installed and started) engines.
	Engines() []engine.Engine
	GetObject(ctx context.Context, ref model.ObjectRef) (*unstructured.Unstructured, error)
	// Namespaces lists cluster namespaces for ":ns". It may fail on RBAC;
	// the UI then falls back to namespaces seen in violations.
	Namespaces(ctx context.Context) ([]string, error)
	// Updates fires, coalesced, whenever engine data changed.
	Updates() <-chan struct{}
	ReadOnly() bool
	// Owners resolves violation owners for grouping; nil disables it.
	Owners() views.Owners
	// Platform feeds the health dashboard; nil disables it.
	Platform() views.Platform
	// Events feeds the recent tab of :v; nil disables it.
	Events() views.Events
	// UpdatedAt is when data last changed.
	UpdatedAt() time.Time
}

// Options tune the UI. Zero values are production defaults.
type Options struct {
	// Now is the clock used for ages; defaults to time.Now.
	Now func() time.Time
	// SnapshotAt is set when replaying a snapshot: the header says so and
	// Now should return it, so ages read as of that time.
	SnapshotAt time.Time
	// Sorts override views' default sorts (config file). Nil keeps them.
	Sorts views.Sorts
	// NoHighlight disables chroma syntax highlighting.
	NoHighlight bool
	// Editor overrides $EDITOR.
	Editor string
	// InitialCommand runs after connecting, e.g. "v" or "gk/ct". Defaults to "pol".
	InitialCommand string
	// TickInterval controls how often ages re-render and flashes expire.
	// Zero means 1s; negative disables.
	TickInterval time.Duration
	// Copy puts text on the clipboard. Defaults to OSC 52 on the terminal,
	// which works over SSH.
	Copy func(string) error
}
