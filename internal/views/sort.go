package views

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yindia/p9s/internal/engine"
)

// Sorts are default sorts per view command, e.g. from the config file.
type Sorts map[string]engine.Sort

// CanonicalSorts maps aliases ("violations", "policies") to the commands
// specs report ("v", "pol"); engine view commands ("gk/c") are kept as
// written. Use ParseSorts to validate configured names.
func CanonicalSorts(in map[string]engine.Sort) Sorts {
	out, _ := ParseSorts(in)
	return out
}

// ParseSorts is CanonicalSorts with validation: a name must be a shared
// view or alias, or an engine view ("prefix/name"), and two aliases of one
// view (v and violations) must not both be set.
func ParseSorts(in map[string]engine.Sort) (Sorts, error) {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := Sorts{}
	from := map[string]string{}
	var errs []error
	for _, k := range keys {
		c, ok := Shared(k)
		switch {
		case ok:
		case strings.Contains(k, "/"):
			c = k
		default:
			errs = append(errs, fmt.Errorf("sort.%s: unknown view (e.g. pol, v, events, mut, impact, or an engine view like gk/c)", k))
			continue
		}
		if prev, dup := from[c]; dup {
			errs = append(errs, fmt.Errorf("sort.%s and sort.%s name the same view", prev, k))
			continue
		}
		from[c] = k
		out[c] = in[k]
	}
	return out, errors.Join(errs...)
}

// WithSort replaces a spec's default sort when one is configured for its
// command and the view has a column for it; otherwise the view's own
// default stays.
func (s Sorts) WithSort(spec Spec) Spec {
	sort, ok := s[spec.Command()]
	if !ok {
		return spec
	}
	for _, c := range spec.View.Columns {
		if c.Role == sort.Role {
			spec.View.DefaultSort = sort
			return spec
		}
	}
	return spec
}

// Check reports configured sorts a shared view has no column for, e.g.
// sort.v: severity on a version without that column. Engine views (gk/c)
// are known only once their engine loads; Ignored covers those at runtime.
func (s Sorts) Check() error {
	cmds := make([]string, 0, len(s))
	for c := range s {
		cmds = append(cmds, c)
	}
	sort.Strings(cmds)
	var errs []error
	for _, c := range cmds {
		if _, shared := Shared(c); !shared || c == CmdHealth || c == CmdOverview {
			continue
		}
		cols := SharedSpec(Env{}, c).View.Columns
		if hasRole(cols, s[c].Role) {
			continue
		}
		var keys []string
		for _, col := range cols {
			if k := sortKeyFor(col.Role); k != "" && !contains(keys, k) {
				keys = append(keys, k)
			}
		}
		errs = append(errs, fmt.Errorf("sort.%s: the view has no %s column (sortable here: %s)", c, sortKeyFor(s[c].Role), strings.Join(keys, ", ")))
	}
	return errors.Join(errs...)
}

// Ignored returns the configured sort for spec's command when the view has
// no column for it, so callers can say so instead of silently dropping it.
func (s Sorts) Ignored(spec Spec) (engine.Sort, bool) {
	so, ok := s[spec.Command()]
	if !ok || hasRole(spec.View.Columns, so.Role) {
		return engine.Sort{}, false
	}
	return so, true
}

func hasRole(cols []engine.Column, r engine.Role) bool {
	for _, c := range cols {
		if c.Role == r {
			return true
		}
	}
	return false
}

// sortKeyFor is the config spelling of a sortable role.
func sortKeyFor(r engine.Role) string {
	switch r {
	case engine.RoleName, engine.RoleAge, engine.RoleSeverity:
		return string(r)
	case engine.RoleCount:
		return "violations"
	}
	return ""
}

// CheckStartView reports a start view (config startView or -c) that cannot
// open: a shared view or alias, impact <policy>, or an engine view such as
// gk/c (checked when its engine loads).
func CheckStartView(command string) error {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(command), ":"))
	if len(fields) == 0 {
		return nil
	}
	name := strings.ToLower(fields[0])
	if _, ok := Shared(name); ok || name == CmdImpact || strings.Contains(name, "/") {
		return nil
	}
	return fmt.Errorf("%q is not a view (e.g. overview, pol, v, events, mut, health, ns, or an engine view like gk/c)", fields[0])
}
