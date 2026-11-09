package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yindia/p9s/internal/config"
	"github.com/yindia/p9s/internal/engine"
)

const sample = `startView: v
engines: kyverno
syncTimeout: 90s
sort:
  pol: name
  v: violations desc
contexts:
  prod:
    startView: health
    sort:
      v: age
`

func TestParseAndContextOverrides(t *testing.T) {
	f, err := config.Parse([]byte(sample), "test")
	if err != nil {
		t.Fatal(err)
	}
	dev := f.For("dev")
	if dev.StartView != "v" || dev.Engines != "kyverno" || time.Duration(dev.SyncTimeout) != 90*time.Second {
		t.Errorf("top level = %+v", dev)
	}
	prod := f.For("prod")
	if prod.StartView != "health" || prod.Sort["v"] != "age" || prod.Sort["pol"] != "name" {
		t.Errorf("prod = %+v: overrides on top, the rest inherited", prod)
	}
	if f.Sort["v"] != "violations desc" {
		t.Error("an override must not change the top level")
	}
}

func TestUnknownKeysAndBadValuesAreErrors(t *testing.T) {
	tests := []struct{ in, want string }{
		{"startview: v\n", "line 1: field startview not found"},
		{"contexts:\n  prod:\n    grouplabel: x\n", "line 3: field grouplabel not found"},
		// Grouping by a label was removed: say so instead of ignoring it.
		{"groupLabel: team\n", "groupLabel: grouping by a label was removed"},
		{"contexts:\n  prod:\n    groupLabel: team\n", "contexts.prod.groupLabel: grouping by a label was removed"},
		{"syncTimeout: soon\n", `syncTimeout "soon"`},
		{"sort:\n  v: owners\n", `unknown column "owners"`},
		{"sort:\n  v: name sideways\n", "direction must be asc or desc"},
		{"contexts:\n  prod:\n    sort:\n      v: nope\n", "contexts.prod.sort.v"},
	}
	for _, tt := range tests {
		_, err := config.Parse([]byte(tt.in), "test")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%q: err = %v, want %q", tt.in, err, tt.want)
		}
	}
}

func TestEmptyAndMissingFiles(t *testing.T) {
	if _, err := config.Parse(nil, "empty"); err != nil {
		t.Errorf("empty file: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "none.yaml")
	if _, err := config.Load(missing, false); err != nil {
		t.Errorf("missing default file must be fine: %v", err)
	}
	if _, err := config.Load(missing, true); err == nil {
		t.Error("a --config file that does not exist is an error")
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(p, []byte(sample), 0o600)
	if f, err := config.Load(p, true); err != nil || f.StartView != "v" {
		t.Errorf("load = %+v, %v", f, err)
	}
}

func TestParseSort(t *testing.T) {
	s, err := config.ParseSort("Violations DESC")
	if err != nil || s != (engine.Sort{Role: engine.RoleCount, Desc: true}) {
		t.Errorf("ParseSort = %+v, %v", s, err)
	}
}

func TestDefaultPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if p := config.DefaultPath(); p != "/x/p9s/config.yaml" {
		t.Errorf("DefaultPath = %q", p)
	}
}

func TestMultipleDocumentsAreAnError(t *testing.T) {
	if _, err := config.Parse([]byte("startView: v\n---\nstartView: pol\n"), "test"); err == nil || !strings.Contains(err.Error(), "one YAML document") {
		t.Errorf("err = %v", err)
	}
	if _, err := config.Parse([]byte("# only comments\n"), "test"); err != nil {
		t.Errorf("comments only: %v", err)
	}
}
