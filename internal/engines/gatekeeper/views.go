package gatekeeper

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

const (
	cmdTemplates   = Prefix + "/ct"
	cmdConstraints = Prefix + "/c"
	cmdConfig      = Prefix + "/cfg"
)

// Views implements engine.Engine.
func (e *Engine) Views() []engine.View {
	return []engine.View{
		{
			Command: cmdTemplates, Aliases: []string{Prefix + "/templates"},
			Title: "ConstraintTemplates", Description: "Gatekeeper constraint templates",
			Columns: []engine.Column{
				{Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "KIND", Role: engine.RoleKind}, {Title: "LANG"},
				{Title: "CREATED"}, {Title: "CONSTRAINTS", Role: engine.RoleCount}, {Title: "AGE", Role: engine.RoleAge},
			},
			DefaultSort: engine.Sort{Role: engine.RoleName},
			Rows:        e.templateRows,
		},
		{
			Command: cmdConstraints, Aliases: []string{Prefix + "/constraints"},
			Title: "Constraints", Description: "Gatekeeper constraints of every discovered kind",
			Columns: []engine.Column{
				{Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "KIND", Role: engine.RoleKind},
				{Title: "ENFORCEMENT", Role: engine.RoleMode},
				{Title: "VIOLATIONS", Role: engine.RoleCount}, {Title: "AUDIT AGE", Role: engine.RoleAge},
			},
			DefaultSort: engine.Sort{Role: engine.RoleCount, Desc: true},
			Rows:        e.constraintRows,
		},
		{
			Command: cmdConfig, Aliases: []string{Prefix + "/config"},
			Title: "Config", Description: "Gatekeeper sync and exclusion config",
			Columns: []engine.Column{
				{Title: "NAMESPACE", Role: engine.RoleNamespace}, {Title: "NAME", Role: engine.RoleName, Flex: true},
				{Title: "SYNC"}, {Title: "EXCLUDED"}, {Title: "AGE", Role: engine.RoleAge},
			},
			Rows: e.configRows,
		},
	}
}

func (e *Engine) templateRows(f engine.Filter) ([]engine.Row, error) {
	if !matchesEngine(f) {
		return nil, nil
	}
	s := e.snapshot()
	counts := map[string]int{}
	for _, c := range s.constraints {
		counts[c.GetKind()]++
	}
	rows := make([]engine.Row, 0, len(s.templates))
	for _, t := range s.templates {
		kind := templateKind(t)
		if f.PolicyKind != "" && f.PolicyKind != kind {
			continue
		}
		created, _, _ := unstructured.NestedBool(t.Object, "status", "created")
		rows = append(rows, engine.Row{
			ID: t.GetName(),
			Cells: []engine.Cell{
				engine.TextCell(t.GetName()), engine.TextCell(kind),
				engine.TextCell(sourceLanguages(templateSources(t))),
				engine.BoolCell(created), engine.CountCell(counts[kind]),
				engine.AgeCell(t.GetCreationTimestamp().Time),
			},
			Object: t,
			Detail: func() (engine.Detail, error) { return templateDetail(t), nil },
			Drill:  &engine.Target{Command: cmdConstraints, Filter: engine.Filter{Engine: Name, PolicyKind: kind}},
		})
	}
	return rows, nil
}

func sourceLanguages(srcs []model.Source) string {
	seen := map[model.SourceLanguage]bool{}
	var langs []string
	for _, s := range srcs {
		if !seen[s.Language] {
			seen[s.Language] = true
			langs = append(langs, string(s.Language))
		}
	}
	return strings.Join(langs, ",")
}

func templateDetail(t *unstructured.Unstructured) engine.Detail {
	kind := templateKind(t)
	created, _, _ := unstructured.NestedBool(t.Object, "status", "created")
	summary := enginekit.KV(
		"Name", t.GetName(),
		"Constraint kind", kind,
		"Created", enginekit.YesNo(created),
		"Pods reporting", fmt.Sprint(len(nestedSlice(t, "status", "byPod"))),
	)
	d := engine.Detail{Title: "ConstraintTemplate " + t.GetName(), Sections: []engine.Section{{Title: "Summary", Body: summary}}}
	if errs := templateErrors(t); len(errs) > 0 {
		d.Sections = append(d.Sections, engine.Section{Title: "Errors", Body: strings.Join(errs, "\n")})
	}
	for _, src := range templateSources(t) {
		title := strings.ToUpper(string(src.Language))
		if strings.Contains(src.Origin, " lib ") {
			title += " (" + src.Origin[strings.LastIndex(src.Origin, "lib"):] + ")"
		}
		d.Sections = append(d.Sections, engine.Section{Title: title, Body: src.Code, Language: string(src.Language)})
	}
	schema, ok, _ := unstructured.NestedMap(t.Object, "spec", "crd", "spec", "validation", "openAPIV3Schema")
	body := "(no parameters)"
	if ok {
		body = enginekit.YAML(schema)
	}
	d.Sections = append(d.Sections, engine.Section{Title: "Parameters schema", Body: body, Language: "yaml"})
	return d
}

func (e *Engine) constraintRows(f engine.Filter) ([]engine.Row, error) {
	if !matchesEngine(f) {
		return nil, nil
	}
	s := e.snapshot()
	rows := make([]engine.Row, 0, len(s.constraints))
	for _, c := range s.constraints {
		p := toPolicy(c, s.templateByKind[c.GetKind()])
		if !f.MatchesPolicy(p.Key()) {
			continue
		}
		count := engine.KnownCount(p.ViolationCount, p.ViolationsKnown)
		if f.Namespace != "" && p.ViolationsKnown {
			// Under a namespace scope, count only reported violations inside
			// it; "+" marks a lower bound when Gatekeeper capped the list.
			n, truncated := 0, false
			for _, v := range constraintViolations(c) {
				truncated = truncated || v.Truncated
				if f.MatchesTarget(v.Target) {
					n++
				}
			}
			count = engine.CountCell(n)
			if truncated {
				count.Text += "+"
			}
		}
		rows = append(rows, engine.Row{
			ID: c.GetKind() + "/" + c.GetName(),
			Cells: []engine.Cell{
				engine.TextCell(p.Name), engine.TextCell(p.Kind),
				engine.ModeCell(p.Enforcement), count,
				engine.AgeCell(p.LastAudit),
			},
			Object: c,
			Detail: func() (engine.Detail, error) { return constraintDetail(c), nil },
			Drill: &engine.Target{Command: "v", Filter: engine.Filter{
				Engine: Name, PolicyKind: p.Kind, PolicyName: p.Name, PolicyNamespace: p.Namespace,
			}},
			Policy: &model.PolicyKey{Engine: Name, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name},
		})
	}
	return rows, nil
}

func constraintDetail(c *unstructured.Unstructured) engine.Detail {
	total := totalViolations(c)
	vs := constraintViolations(c)
	summary := enginekit.KV(
		"Kind", c.GetKind(),
		"Name", c.GetName(),
		"Enforcement", enforcement(c).String(),
		"Total violations", fmt.Sprint(total),
		"Reported", fmt.Sprint(len(vs)),
		"Last audit", auditStamp(c),
	)
	if sas := scopedActions(c); len(sas) > 0 {
		summary += "\n\nScoped enforcement: the action depends on where the request is checked." +
			"\nThe table shows what admission (validation.gatekeeper.sh) does."
		for _, sa := range sas {
			summary += fmt.Sprintf("\n  %-7s at %s", sa.action, strings.Join(sa.points, ", "))
		}
	}
	if errs := podErrors(c); len(errs) > 0 {
		summary += "\nErrors:\n  " + strings.Join(errs, "\n  ")
	}
	d := engine.Detail{Title: c.GetKind() + " " + c.GetName(), Sections: []engine.Section{{Title: "Summary", Body: summary}}}
	d.Sections = append(d.Sections,
		engine.Section{Title: "Match", Body: enginekit.NestedYAML(c, "(matches everything)", "spec", "match"), Language: "yaml"},
		engine.Section{Title: "Parameters", Body: enginekit.NestedYAML(c, "(none)", "spec", "parameters"), Language: "yaml"},
		engine.Section{Title: violationsTitle(len(vs), total), Body: violationList(vs)},
	)
	return d
}

// violationsTitle never lets the reported list pass for the whole set:
// Gatekeeper caps status.violations (--constraint-violations-limit) while
// totalViolations stays exact.
func violationsTitle(shown, total int) string {
	if shown < total {
		return fmt.Sprintf("Violations (showing %d of %d; Gatekeeper reports a limited list)", shown, total)
	}
	return fmt.Sprintf("Violations (%d)", shown)
}

func violationList(vs []model.Violation) string {
	if len(vs) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "• [%s] %s\n  %s\n", v.Enforcement.Raw, v.Target, v.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (e *Engine) configRows(f engine.Filter) ([]engine.Row, error) {
	if !matchesEngine(f) {
		return nil, nil
	}
	s := e.snapshot()
	rows := make([]engine.Row, 0, len(s.configs))
	for _, c := range s.configs {
		rows = append(rows, engine.Row{
			ID: c.GetNamespace() + "/" + c.GetName(),
			Cells: []engine.Cell{
				engine.TextCell(c.GetNamespace()), engine.TextCell(c.GetName()),
				engine.CountCell(len(nestedSlice(c, "spec", "sync", "syncOnly"))),
				engine.CountCell(len(nestedSlice(c, "spec", "match"))),
				engine.AgeCell(c.GetCreationTimestamp().Time),
			},
			Object: c,
			Detail: func() (engine.Detail, error) { return configDetail(c), nil },
		})
	}
	return rows, nil
}

func configDetail(c *unstructured.Unstructured) engine.Detail {
	return engine.Detail{Title: "Config " + c.GetNamespace() + "/" + c.GetName(), Sections: []engine.Section{
		{Title: "Sync (replicated into OPA)", Body: enginekit.NestedYAML(c, "(nothing synced)", "spec", "sync"), Language: "yaml"},
		{Title: "Match (excluded namespaces/processes)", Body: enginekit.NestedYAML(c, "(no exclusions)", "spec", "match"), Language: "yaml"},
		{Title: "Validation", Body: enginekit.NestedYAML(c, "(default)", "spec", "validation"), Language: "yaml"},
		{Title: "Readiness", Body: enginekit.NestedYAML(c, "(default)", "spec", "readiness"), Language: "yaml"},
	}}
}

func matchesEngine(f engine.Filter) bool { return f.Engine == "" || f.Engine == Name }

func auditStamp(c *unstructured.Unstructured) string {
	t := auditTime(c)
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}

func nestedSlice(o *unstructured.Unstructured, path ...string) []any {
	return enginekit.Slice(o.Object, path...)
}
