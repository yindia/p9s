package kyverno

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

const (
	cmdClusterPolicies = Prefix + "/cpol"
	cmdPolicies        = Prefix + "/pol"
	cmdCELPolicies     = Prefix + "/vpol"
	cmdReports         = Prefix + "/polr"
	cmdExceptions      = Prefix + "/polex"
)

// Views implements engine.Engine.
func (e *Engine) Views() []engine.View {
	classicCols := []engine.Column{
		{Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "ACTION", Role: engine.RoleMode}, {Title: "READY"}, {Title: "RULES"},
		{Title: "BACKGROUND"}, {Title: "VIOLATIONS", Role: engine.RoleCount}, {Title: "AGE", Role: engine.RoleAge},
	}
	byCount := engine.Sort{Role: engine.RoleCount, Desc: true}
	return []engine.View{
		{
			Command: cmdClusterPolicies, Aliases: []string{Prefix + "/clusterpolicies"},
			Title: "ClusterPolicies", Description: "Kyverno ClusterPolicies (kyverno.io)",
			Columns: classicCols, DefaultSort: byCount,
			Rows: func(f engine.Filter) ([]engine.Row, error) { return e.classicRows(f, kindClassicCP), nil },
		},
		{
			Command: cmdPolicies, Aliases: []string{Prefix + "/policies"},
			Title: "Policies", Description: "Kyverno namespaced Policies (kyverno.io)",
			Columns:     append([]engine.Column{{Title: "NAMESPACE", Role: engine.RoleNamespace}}, classicCols...),
			DefaultSort: byCount,
			Rows:        func(f engine.Filter) ([]engine.Row, error) { return e.classicRows(f, kindClassicP), nil },
		},
		{
			Command: cmdCELPolicies, Aliases: []string{Prefix + "/validatingpolicies"},
			Title: "CEL Policies", Description: "Kyverno CEL policies (policies.kyverno.io): validating, image, mutating, …",
			Columns: []engine.Column{
				{Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "KIND", Role: engine.RoleKind},
				{Title: "NAMESPACE", Role: engine.RoleNamespace}, {Title: "ACTIONS", Role: engine.RoleMode},
				{Title: "READY"}, {Title: "VIOLATIONS", Role: engine.RoleCount}, {Title: "AGE", Role: engine.RoleAge},
			},
			DefaultSort: byCount,
			Rows:        func(f engine.Filter) ([]engine.Row, error) { return e.celRows(f), nil },
		},
		{
			Command: cmdReports, Aliases: []string{Prefix + "/reports"},
			Title: "PolicyReports", Description: "Kyverno policy reports per resource",
			Columns: []engine.Column{
				{Title: "RESOURCE", Role: engine.RoleTarget, Flex: true}, {Title: "NAMESPACE", Role: engine.RoleNamespace},
				{Title: "PASS"}, {Title: "FAIL", Role: engine.RoleCount},
				{Title: "WARN"}, {Title: "ERROR"}, {Title: "SKIP"}, {Title: "AGE", Role: engine.RoleAge},
			},
			DefaultSort: byCount,
			Rows:        func(f engine.Filter) ([]engine.Row, error) { return e.reportRows(f), nil },
		},
		{
			Command: cmdExceptions, Aliases: []string{Prefix + "/exceptions"},
			Title: "PolicyExceptions", Description: "Kyverno policy exceptions",
			Columns: []engine.Column{
				{Title: "NAME", Role: engine.RoleName}, {Title: "NAMESPACE", Role: engine.RoleNamespace},
				{Title: "EXEMPTS", Flex: true}, {Title: "AGE", Role: engine.RoleAge},
			},
			Rows: func(f engine.Filter) ([]engine.Row, error) { return e.exceptionRows(f), nil },
		},
	}
}

func matchesEngine(f engine.Filter) bool { return f.Engine == "" || f.Engine == Name }

// scopedCount is the policy's violation count, restricted to the selected
// namespace when one is set.
func scopedCount(s *snapshot, p model.Policy, f engine.Filter) int {
	if f.Namespace == "" {
		return p.ViolationCount
	}
	n := 0
	for _, v := range s.violations {
		if v.Policy == p.Key() && f.MatchesTarget(v.Target) {
			n++
		}
	}
	return n
}

func (e *Engine) classicRows(f engine.Filter, kind string) []engine.Row {
	if !matchesEngine(f) {
		return nil
	}
	s := e.snapshot()
	var rows []engine.Row
	for _, p := range s.policies {
		if p.Kind != kind || !f.MatchesPolicy(p.Key()) || !f.MatchesNamespace(p.Namespace) {
			continue
		}
		o := p.Raw
		background, found, _ := unstructured.NestedBool(o.Object, "spec", "background")
		cells := []engine.Cell{
			engine.TextCell(p.Name), engine.ModeCell(p.Enforcement), engine.BoolCell(len(readiness(o)) == 0),
			engine.TextCell(ruleSummary(o)), engine.BoolCell(background || !found),
			engine.KnownCount(scopedCount(s, p, f), p.ViolationsKnown), engine.AgeCell(o.GetCreationTimestamp().Time),
		}
		if kind == kindClassicP {
			cells = append([]engine.Cell{engine.TextCell(p.Namespace)}, cells...)
		}
		rows = append(rows, policyRow(p, cells, classicDetail))
	}
	return rows
}

func (e *Engine) celRows(f engine.Filter) []engine.Row {
	if !matchesEngine(f) {
		return nil
	}
	s := e.snapshot()
	var rows []engine.Row
	for _, p := range s.policies {
		if isClassic(p.Raw) || !f.MatchesPolicy(p.Key()) || !f.MatchesNamespace(p.Namespace) {
			continue
		}
		cells := []engine.Cell{
			engine.TextCell(p.Name), engine.AliasCell(e.ShortKind(p.Kind), p.Kind), engine.TextCell(dash(p.Namespace)),
			engine.ModeCell(p.Enforcement), engine.BoolCell(len(readiness(p.Raw)) == 0),
			engine.KnownCount(scopedCount(s, p, f), p.ViolationsKnown), engine.AgeCell(p.Raw.GetCreationTimestamp().Time),
		}
		rows = append(rows, policyRow(p, cells, celDetail))
	}
	return rows
}

func policyRow(p model.Policy, cells []engine.Cell, detail func(model.Policy) engine.Detail) engine.Row {
	key := p.Key()
	return engine.Row{
		ID: key.String(), Cells: cells, Object: p.Raw,
		Detail: func() (engine.Detail, error) { return detail(p), nil },
		Drill: &engine.Target{Command: "v", Filter: engine.Filter{
			Engine: Name, PolicyKind: key.Kind, PolicyName: key.Name, PolicyNamespace: key.Namespace,
		}},
		Policy: &key,
	}
}

func ruleSummary(o *unstructured.Unstructured) string {
	counts := map[string]int{}
	order := []string{"validate", "mutate", "generate", "verifyImages"}
	for _, r := range rules(o) {
		for _, t := range order {
			if _, ok := r[t]; ok {
				counts[t]++
			}
		}
	}
	var parts []string
	for _, t := range order {
		if counts[t] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[t], t))
		}
	}
	return dash(strings.Join(parts, ", "))
}

func summary(p model.Policy) engine.Section {
	o := p.Raw
	ann := o.GetAnnotations()
	body := enginekit.KV(
		"Kind", p.Kind, "Namespace", dash(p.Namespace), "Name", p.Name,
		"Enforcement", p.Enforcement.String(), "Violations", fmt.Sprint(p.ViolationCount),
		"Title", dash(ann["policies.kyverno.io/title"]),
		"Category", dash(ann["policies.kyverno.io/category"]),
		"Severity", dash(ann["policies.kyverno.io/severity"]),
	)
	if d := ann["policies.kyverno.io/description"]; d != "" {
		body += "\n\n" + strings.TrimSpace(d)
	}
	for _, h := range readiness(o) {
		body += "\n! " + h.Message
	}
	return engine.Section{Title: "Summary", Body: body}
}

func classicDetail(p model.Policy) engine.Detail {
	d := engine.Detail{Title: p.Kind + " " + p.Name, Sections: []engine.Section{summary(p)}}
	for _, s := range p.Sources {
		d.Sections = append(d.Sections, engine.Section{Title: strings.ToUpper(string(s.Language)) + " — " + s.Origin, Body: s.Code, Language: string(s.Language)})
	}
	return d
}

func celDetail(p model.Policy) engine.Detail {
	o := p.Raw
	d := engine.Detail{Title: p.Kind + " " + p.Name, Sections: []engine.Section{
		summary(p),
		{Title: "Match constraints", Body: enginekit.NestedYAML(o, "(none)", "spec", "matchConstraints"), Language: "yaml"},
	}}
	for _, s := range p.Sources {
		d.Sections = append(d.Sections, engine.Section{Title: "CEL", Body: s.Code, Language: "cel"})
	}
	return d
}

func (e *Engine) reportRows(f engine.Filter) []engine.Row {
	if !matchesEngine(f) {
		return nil
	}
	var rows []engine.Row
	for _, rep := range e.snapshot().reports {
		scope := scopeRef(rep)
		if !f.MatchesNamespace(rep.GetNamespace()) {
			continue
		}
		res := scope.Kind + "/" + scope.Name
		if scope.Kind == "" {
			res = rep.GetName()
		}
		count := func(k string) engine.Cell { return engine.CountCell(enginekit.Int(rep.Object, "summary", k)) }
		pass := engine.TextCell(fmt.Sprint(enginekit.Int(rep.Object, "summary", resultPass)))
		row := engine.Row{
			ID: rep.GetNamespace() + "/" + rep.GetName(),
			Cells: []engine.Cell{
				engine.TextCell(res), engine.TextCell(dash(rep.GetNamespace())), pass,
				count(resultFail), count(resultWarn), count(resultError), count(resultSkip),
				engine.AgeCell(rep.GetCreationTimestamp().Time),
			},
			Object: rep,
			Detail: func() (engine.Detail, error) { return reportDetail(rep), nil },
		}
		if scope.Name != "" {
			row.Ref = &scope
		}
		rows = append(rows, row)
	}
	return rows
}

func reportDetail(rep *unstructured.Unstructured) engine.Detail {
	scope := scopeRef(rep)
	var b strings.Builder
	for _, r := range enginekit.Slice(rep.Object, "results") {
		m, _ := r.(map[string]any)
		rule := ruleName(m)
		if rule != "" {
			rule = " / " + rule
		}
		fmt.Fprintf(&b, "• [%s] %s%s\n  %s\n", enginekit.Str(m, "result"), enginekit.Str(m, "policy"), rule, enginekit.Str(m, "message"))
	}
	return engine.Detail{Title: "PolicyReport for " + scope.String(), Sections: []engine.Section{
		{Title: "Summary", Body: enginekit.NestedYAML(rep, "(none)", "summary"), Language: "yaml"},
		{Title: "Results", Body: strings.TrimRight(b.String(), "\n")},
	}}
}

func (e *Engine) exceptionRows(f engine.Filter) []engine.Row {
	if !matchesEngine(f) {
		return nil
	}
	var rows []engine.Row
	for _, ex := range e.snapshot().exceptions {
		if !f.MatchesNamespace(ex.GetNamespace()) {
			continue
		}
		rows = append(rows, engine.Row{
			ID: ex.GetAPIVersion() + "/" + ex.GetNamespace() + "/" + ex.GetName(),
			Cells: []engine.Cell{
				engine.TextCell(ex.GetName()), engine.TextCell(dash(ex.GetNamespace())),
				engine.TextCell(exempts(ex)), engine.AgeCell(ex.GetCreationTimestamp().Time),
			},
			Object: ex,
			Detail: func() (engine.Detail, error) {
				return engine.Detail{Title: "PolicyException " + ex.GetName(), Sections: []engine.Section{
					{Title: "Exceptions", Body: enginekit.NestedYAML(ex, "(none)", "spec", "exceptions"), Language: "yaml"},
					{Title: "Policy refs", Body: enginekit.NestedYAML(ex, "(none)", "spec", "policyRefs"), Language: "yaml"},
					{Title: "Match", Body: enginekit.NestedYAML(ex, "(none)", "spec", "match"), Language: "yaml"},
				}}, nil
			},
		})
	}
	return rows
}

// exempts summarizes which policies an exception covers, for both the
// classic (spec.exceptions[].policyName) and CEL (spec.policyRefs) forms.
func exempts(ex *unstructured.Unstructured) string {
	var names []string
	for _, x := range enginekit.Slice(ex.Object, "spec", "exceptions") {
		m, _ := x.(map[string]any)
		names = append(names, enginekit.Str(m, "policyName"))
	}
	for _, x := range enginekit.Slice(ex.Object, "spec", "policyRefs") {
		m, _ := x.(map[string]any)
		names = append(names, enginekit.Str(m, "name"))
	}
	return dash(strings.Join(unique(names), ", "))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
