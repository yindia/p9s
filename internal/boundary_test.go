package internal_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportBoundaries enforces the layering: model and engine stay free of
// UI and engine-specific code, and the UI only knows model and engine.
// Kyverno must be addable without touching internal/ui or internal/model.
func TestImportBoundaries(t *testing.T) {
	const mod = "github.com/yindia/p9s/internal/"
	rules := []struct {
		pkg   string
		allow []string
		// direct checks only the package's own imports; otherwise the whole
		// dependency closure is checked.
		direct bool
	}{
		{pkg: "model"},
		{pkg: "engine", allow: []string{"model"}},
		// Cross-engine logic and its materialised form serve every renderer.
		{pkg: "query", allow: []string{"model", "engine"}},
		{pkg: "table", allow: []string{"model", "engine"}},
		{pkg: "views", allow: []string{"model", "engine", "query", "table"}},
		// The TUI renders views and tables; it never reaches into query (or
		// engines) itself, so it can hold no collecting or counting logic.
		{pkg: "ui", allow: []string{"model", "engine", "views", "table"}, direct: true},
		// Non-interactive output renders the same materialised views.
		{pkg: "render", allow: []string{"model", "engine", "views", "table"}, direct: true},
		// Shared engine plumbing must stay engine-neutral.
		{pkg: "engines/enginekit", allow: []string{"model", "engine"}},
	}
	for _, r := range rules {
		format := "{{join .Deps \" \"}}"
		if r.direct {
			format = "{{join .Imports \" \"}}"
		}
		out, err := exec.Command("go", "list", "-f", format, "./"+r.pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", r.pkg, err)
		}
		ok := map[string]bool{mod + r.pkg: true}
		for _, a := range r.allow {
			ok[mod+a] = true
		}
		for _, dep := range strings.Fields(string(out)) {
			if strings.HasPrefix(dep, mod) && !ok[dep] {
				t.Errorf("internal/%s must not depend on %s (allowed: %v)", r.pkg, dep, r.allow)
			}
		}
	}
}

// TestRendererNeutralLayers keeps query, views and table free of anything
// terminal-specific, so `p9s get`, `p9s report` and a future web renderer
// can share them.
func TestRendererNeutralLayers(t *testing.T) {
	banned := []string{
		"github.com/charmbracelet/", "github.com/muesli/", "github.com/alecthomas/chroma",
		"github.com/yindia/p9s/internal/ui",
	}
	for _, pkg := range []string{"query", "views", "table"} {
		out, err := exec.Command("go", "list", "-deps", "./"+pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", pkg, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, b := range banned {
				if strings.HasPrefix(dep, b) {
					t.Errorf("internal/%s must stay renderer-neutral but depends on %s", pkg, dep)
				}
			}
		}
	}
}
