// Package config loads ~/.config/p9s/config.yaml: defaults for the start
// view, engines, per-view sort and sync timeout, with per-context
// overrides. Flags override the file; the file overrides built-in defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/yindia/p9s/internal/engine"
)

// Settings are the values a file (or one context of it) may set. Empty
// fields are unset.
type Settings struct {
	StartView string `yaml:"startView"`
	// GroupLabel is read only to reject it: grouping by a team label was
	// removed, and a config that still sets it should say so, not silently
	// group by owner.
	GroupLabel  string            `yaml:"groupLabel"`
	Engines     string            `yaml:"engines"`
	SyncTimeout Duration          `yaml:"syncTimeout"`
	Sort        map[string]string `yaml:"sort"` // view -> "name|violations|age|severity [asc|desc]"
}

// File is the config file.
type File struct {
	Settings `yaml:",inline"`
	Contexts map[string]Settings `yaml:"contexts"`
}

// Duration is a time.Duration written as "60s", "2m".
type Duration time.Duration

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil || v <= 0 {
		return fmt.Errorf("line %d: syncTimeout %q: want a positive duration such as 60s", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

// DefaultPath is $XDG_CONFIG_HOME/p9s/config.yaml, else
// ~/.config/p9s/config.yaml.
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "p9s", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "p9s", "config.yaml")
}

// Load reads path. A missing file at the default path is not an error
// (explicit is true when the user named the file, which must then exist).
func Load(path string, explicit bool) (File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("config: %w", err)
	}
	return Parse(b, path)
}

// Parse decodes and validates a config. Unknown keys are errors with their
// line, so a typo never silently does nothing.
func Parse(b []byte, name string) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) { // empty file: empty config
		return File{}, fmt.Errorf("config %s: %w", name, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return File{}, fmt.Errorf("config %s: only one YAML document is read; remove the extra ones", name)
	}
	if err := f.validate(""); err != nil {
		return File{}, fmt.Errorf("config %s: %w", name, err)
	}
	for ctx, s := range f.Contexts {
		if err := s.validate(ctx); err != nil {
			return File{}, fmt.Errorf("config %s: %w", name, err)
		}
	}
	return f, nil
}

func (s Settings) validate(ctx string) error {
	where := ""
	if ctx != "" {
		where = "contexts." + ctx + "."
	}
	if s.GroupLabel != "" {
		return fmt.Errorf("%sgroupLabel: grouping by a label was removed; violations are grouped by owner (delete the key)", where)
	}
	for view, spec := range s.Sort {
		if _, err := ParseSort(spec); err != nil {
			return fmt.Errorf("%ssort.%s: %w", where, view, err)
		}
	}
	return nil
}

// For returns the settings for a context: the file's top level with that
// context's non-empty values on top.
func (f File) For(context string) Settings {
	out := f.Settings
	o, ok := f.Contexts[context]
	if !ok {
		return out
	}
	if o.StartView != "" {
		out.StartView = o.StartView
	}
	if o.Engines != "" {
		out.Engines = o.Engines
	}
	if o.SyncTimeout != 0 {
		out.SyncTimeout = o.SyncTimeout
	}
	if len(o.Sort) > 0 {
		merged := map[string]string{}
		for k, v := range out.Sort {
			merged[k] = v
		}
		for k, v := range o.Sort {
			merged[k] = v
		}
		out.Sort = merged
	}
	return out
}

// sortKeys are the sortable columns, as the N V A S keys name them.
var sortKeys = map[string]engine.Role{
	"name": engine.RoleName, "violations": engine.RoleCount, "count": engine.RoleCount,
	"age": engine.RoleAge, "severity": engine.RoleSeverity,
}

// ParseSort reads "violations desc", "name", "age asc".
func ParseSort(s string) (engine.Sort, error) {
	f := strings.Fields(strings.ToLower(s))
	if len(f) == 0 || len(f) > 2 {
		return engine.Sort{}, fmt.Errorf("%q: want \"<name|violations|age|severity> [asc|desc]\"", s)
	}
	role, ok := sortKeys[f[0]]
	if !ok {
		return engine.Sort{}, fmt.Errorf("%q: unknown column %q (name, violations, age, severity)", s, f[0])
	}
	out := engine.Sort{Role: role}
	if len(f) == 2 {
		switch f[1] {
		case "asc":
		case "desc":
			out.Desc = true
		default:
			return engine.Sort{}, fmt.Errorf("%q: direction must be asc or desc", s)
		}
	}
	return out, nil
}
