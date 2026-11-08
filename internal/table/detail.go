package table

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/yindia/p9s/internal/engine"
)

// Detail is a materialised describe page.
type Detail struct {
	SchemaVersion string    `json:"schemaVersion"`
	Title         string    `json:"title"`
	Sections      []Section `json:"sections"`
}

// Section is plain text, YAML or source code; Language names it.
type Section struct {
	Title    string `json:"title,omitempty"`
	Body     string `json:"body"`
	Language string `json:"language,omitempty"`
}

// BuildDetail materialises an engine.Detail.
func BuildDetail(d engine.Detail) Detail {
	out := Detail{SchemaVersion: SchemaVersion, Title: d.Title, Sections: make([]Section, len(d.Sections))}
	for i, s := range d.Sections {
		out.Sections[i] = Section{Title: s.Title, Body: s.Body, Language: s.Language}
	}
	return out
}

// Age renders t relative to now like kubectl; a zero t renders "-".
func Age(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	return duration.HumanDuration(d)
}

// Dash renders an empty value as "-" (not applicable).
func Dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// KV renders aligned "key: value" lines from alternating pairs.
func KV(pairs ...string) string {
	width := 0
	for i := 0; i < len(pairs); i += 2 {
		width = max(width, len(pairs[i]))
	}
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(&b, "%-*s  %s\n", width+1, pairs[i]+":", pairs[i+1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// OneLine collapses whitespace so multi-line messages fit a table cell.
func OneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
