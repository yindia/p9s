package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/table"
)

// detailScreen is a scrollable page: describe output, YAML or source code.
type detailScreen struct {
	heading string
	detail  table.Detail
	// object backs "e" from a YAML page.
	object *unstructured.Unstructured
	offset int
	// lines is the last render, cached for scrolling bounds.
	lines []string
}

func newDetailScreen(d table.Detail, obj *unstructured.Unstructured) *detailScreen {
	return &detailScreen{heading: d.Title, detail: d, object: obj}
}

func newYAMLScreen(title string, obj *unstructured.Unstructured) *detailScreen {
	d := table.Detail{SchemaVersion: table.SchemaVersion, Title: title, Sections: []table.Section{{Body: table.ObjectYAML(obj), Language: "yaml"}}}
	return newDetailScreen(d, obj)
}

func (d *detailScreen) title() string { return d.heading }

func (d *detailScreen) build(st styles, hl highlighter, width int) []string {
	var out []string
	for i, s := range d.detail.Sections {
		if i > 0 {
			out = append(out, "")
		}
		if s.Title != "" {
			out = append(out, st.sectionTitle.Render(s.Title))
		}
		body := hl.highlight(s.Body, s.Language)
		for _, l := range strings.Split(body, "\n") {
			// Prose wraps at words; code keeps its spacing and wraps hard.
			var wrapped string
			if s.Language == "" {
				wrapped = ansi.Wrap(l, max(width-2, 1), "")
			} else {
				wrapped = ansi.Hardwrap(l, max(width-2, 1), true)
			}
			out = append(out, strings.Split(wrapped, "\n")...)
		}
	}
	for i, l := range out {
		out[i] = " " + l
	}
	return out
}

func (d *detailScreen) render(st styles, hl highlighter, width, height int) []string {
	d.lines = d.build(st, hl, width)
	d.offset = clampIndex(d.offset, len(d.lines)-height)
	end := min(d.offset+height, len(d.lines))
	view := make([]string, 0, height)
	for _, l := range d.lines[d.offset:end] {
		view = append(view, fit(l, width))
	}
	return padLines(view, width, height)
}

func (d *detailScreen) scroll(delta int) { d.offset = max(d.offset+delta, 0) }

func (d *detailScreen) top() { d.offset = 0 }

func (d *detailScreen) bottom() { d.offset = len(d.lines) }

// text returns the plain content, used by the editor action.
func (d *detailScreen) text() string {
	parts := make([]string, 0, len(d.detail.Sections))
	for _, s := range d.detail.Sections {
		parts = append(parts, s.Body)
	}
	return strings.Join(parts, "\n\n")
}
