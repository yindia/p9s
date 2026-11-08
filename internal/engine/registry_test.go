package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/model"
)

func newRegistry(t *testing.T, engines ...*fake.Engine) *engine.Registry {
	t.Helper()
	r := engine.NewRegistry()
	for _, e := range engines {
		if err := r.Register(e.Name(), e.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestRegisterRejectsDuplicatesAndEmpty(t *testing.T) {
	r := newRegistry(t, fake.New(fake.Options{Name: "a"}))
	if err := r.Register("a", fake.New(fake.Options{Name: "a"}).Factory()); err == nil {
		t.Error("duplicate registration succeeded")
	}
	if err := r.Register("", nil); err == nil {
		t.Error("empty registration succeeded")
	}
}

func TestSelection(t *testing.T) {
	r := newRegistry(t, fake.New(fake.Options{Name: "b"}), fake.New(fake.Options{Name: "a"}))
	tests := []struct {
		spec    string
		want    []string
		wantErr bool
	}{
		{spec: "", want: []string{"a", "b"}},
		{spec: "auto", want: []string{"a", "b"}},
		{spec: "b", want: []string{"b"}},
		{spec: " b , a ,b", want: []string{"b", "a"}},
		{spec: "kyverno", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			regs, err := r.Selection(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			var got []string
			for _, reg := range regs {
				got = append(got, reg.Name)
			}
			if !tt.wantErr && !equal(got, tt.want) {
				t.Errorf("Selection(%q) = %v, want %v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestDetectRunsEveryEngine(t *testing.T) {
	boom := errors.New("discovery down")
	r := newRegistry(t,
		fake.New(fake.Options{Name: "on", Installed: true, Version: "1"}),
		fake.New(fake.Options{Name: "off"}),
		fake.New(fake.Options{Name: "err", DetectErr: boom}),
	)
	regs, _ := r.Selection("auto")
	ds := engine.Detect(context.Background(), regs, nil)
	got := map[string]engine.Detection{}
	for _, d := range ds {
		got[d.Engine.Name()] = d
	}
	if !got["on"].Installed || got["on"].Version != "1" {
		t.Errorf("on = %+v", got["on"])
	}
	if got["off"].Installed {
		t.Error("off reported installed")
	}
	if !errors.Is(got["err"].Err, boom) {
		t.Errorf("err = %v", got["err"].Err)
	}
}

func TestFilterMatchesPolicy(t *testing.T) {
	k := model.PolicyKey{Engine: "gk", Kind: "K", Namespace: "", Name: "n"}
	tests := []struct {
		f    engine.Filter
		want bool
	}{
		{engine.Filter{}, true},
		{engine.Filter{Engine: "gk"}, true},
		{engine.Filter{Engine: "kyv"}, false},
		{engine.Filter{PolicyKind: "K", PolicyName: "n"}, true},
		{engine.Filter{PolicyName: "other"}, false},
		{engine.Filter{PolicyNamespace: "ns"}, false},
	}
	for _, tt := range tests {
		if got := tt.f.MatchesPolicy(k); got != tt.want {
			t.Errorf("%+v.MatchesPolicy = %v, want %v", tt.f, got, tt.want)
		}
	}
	if !(engine.Filter{}).IsZero() || (engine.Filter{Text: "x"}).IsZero() {
		t.Error("IsZero wrong")
	}
}

func TestCellConstructors(t *testing.T) {
	if c := engine.CountCell(3); c.Text != "3" || c.Kind != engine.CellCount {
		t.Errorf("CountCell = %+v", c)
	}
	if c := engine.ModeCell(model.Enforcement{Mode: model.ModeAudit, Raw: "dryrun"}); c.Text != "audit" {
		t.Errorf("ModeCell text = %q: tables show one normalised word", c.Text)
	}
	if c := engine.KnownCount(3, false); !c.NA || c.Text != "-" {
		t.Errorf("unknown count = %+v, want -", c)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNamespaceScope(t *testing.T) {
	f := engine.Filter{Namespace: "payments"}
	tests := []struct {
		ref  model.ObjectRef
		want bool
	}{
		{model.ObjectRef{Kind: "Pod", Namespace: "payments", Name: "a"}, true},
		{model.ObjectRef{Kind: "Pod", Namespace: "search", Name: "a"}, false},
		{model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "payments"}, true},
		{model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "search"}, false},
		{model.ObjectRef{Kind: "ClusterRole", Name: "admin"}, false},
	}
	for _, tt := range tests {
		if got := f.MatchesTarget(tt.ref); got != tt.want {
			t.Errorf("MatchesTarget(%s) = %v, want %v", tt.ref, got, tt.want)
		}
	}
	if !f.MatchesNamespace("") || !f.MatchesNamespace("payments") || f.MatchesNamespace("search") {
		t.Error("MatchesNamespace: cluster-scoped and same-namespace policies must pass, others not")
	}
	if !(engine.Filter{}).MatchesTarget(model.ObjectRef{Namespace: "x"}) {
		t.Error("empty scope must match everything")
	}
}
