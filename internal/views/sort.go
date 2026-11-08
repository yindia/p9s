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
