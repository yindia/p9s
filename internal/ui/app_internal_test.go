package ui

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/views"
)

// stubBackend is the smallest Backend for model-level tests.
type stubBackend struct{}

func (stubBackend) Connect(context.Context) error                { return nil }
func (stubBackend) SwitchContext(context.Context, string) error  { return nil }
func (stubBackend) ContextName() string                          { return "stub" }
func (stubBackend) Contexts() ([]model.KubeContext, error)       { return nil, nil }
func (stubBackend) Detections() []engine.Detection               { return nil }
func (stubBackend) Engines() []engine.Engine                     { return nil }
func (stubBackend) Namespaces(context.Context) ([]string, error) { return nil, nil }
func (stubBackend) Updates() <-chan struct{}                     { return nil }
func (stubBackend) ReadOnly() bool                               { return true }
func (stubBackend) Owners() views.Owners                         { return nil }
func (stubBackend) Platform() views.Platform                     { return nil }
func (stubBackend) Events() views.Events                         { return nil }
func (stubBackend) UpdatedAt() time.Time                         { return time.Time{} }
func (stubBackend) GetObject(context.Context, model.ObjectRef) (*unstructured.Unstructured, error) {
	return nil, nil
}

func TestFlashExpiresAfterTTL(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a := New(stubBackend{}, Options{Now: func() time.Time { return now }, TickInterval: -1})
	a.setFlash("connected to kind", false)
	now = now.Add(flashTTL - time.Second)
	a.Update(tickMsg{})
	if a.flash == "" {
		t.Fatal("flash cleared before its TTL")
	}
	now = now.Add(2 * time.Second)
	a.Update(tickMsg{})
	if a.flash != "" {
		t.Errorf("flash %q still shown %s after it was set", a.flash, flashTTL+time.Second)
	}
}
