package views

import (
	"fmt"
	"strings"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/table"
)

// engineColumn is shared by every aggregate view.
var engineColumn = engine.Column{Title: "ENGINE", Role: engine.RoleEngine}

// policyCountColumn is the VIOLATIONS column of :pol.
const policyCountColumn = 5

func policiesView(env Env) engine.View {
	return engine.View{
		Command: CmdPolicies, Title: "Policies",
		Columns: []engine.Column{
			engineColumn, {Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "KIND", Role: engine.RoleKind},
			{Title: "NAMESPACE", Role: engine.RoleNamespace}, {Title: "ENFORCEMENT", Role: engine.RoleMode},
			{Title: "VIOLATIONS", Role: engine.RoleCount}, {Title: "AUDIT AGE", Role: engine.RoleAge},
		},
		DefaultSort: engine.Sort{Role: engine.RoleCount, Desc: true},
		Rows: func(f engine.Filter) ([]engine.Row, error) {
			es := env.Engines()
			ps, err := query.Policies(es, f)
			rows := make([]engine.Row, 0, len(ps))
			for _, p := range ps {
				row := policyRow(env, es, p.Policy)
				if p.LowerBound && p.ViolationsKnown {
					// The engine capped its list: the scoped count is a lower bound.
					row.Cells[policyCountColumn].Text += "+"
				}
				rows = append(rows, row)
			}
			return rows, err
		},
	}
}

func policyRow(env Env, es []engine.Engine, p model.Policy) engine.Row {
	key := p.Key()
	return engine.Row{
		ID: key.String(),
		Cells: []engine.Cell{
			engine.TextCell(p.Engine), engine.TextCell(p.Name), engine.AliasCell(query.ShortKind(es, p.Engine, p.Kind), p.Kind),
			engine.TextCell(table.Dash(p.Namespace)), engine.ModeCell(p.Enforcement),
			engine.KnownCount(p.ViolationCount, p.ViolationsKnown), engine.AgeCell(p.LastAudit),
		},
		Object: p.Raw,
		Detail: func() (engine.Detail, error) { return policyDetail(env, p), nil },
		Drill: &engine.Target{Command: CmdViolations, Filter: engine.Filter{
			Engine: key.Engine, PolicyKind: key.Kind, PolicyName: key.Name, PolicyNamespace: key.Namespace,
		}},
		Policy: &key,
	}
}

func policyDetail(env Env, p model.Policy) engine.Detail {
	audit := "never"
	if !p.LastAudit.IsZero() {
		audit = p.LastAudit.UTC().Format("2006-01-02 15:04:05Z") + " (" + table.Age(env.Now(), p.LastAudit) + " ago)"
	}
	violations := "n/a (no audit source)"
	if p.ViolationsKnown {
		violations = fmt.Sprint(p.ViolationCount)
	}
	d := engine.Detail{Title: p.Kind + " " + p.Name, Sections: []engine.Section{{
		Title: "Summary",
		Body: table.KV(
			"Engine", p.Engine, "Kind", p.Kind, "Namespace", table.Dash(p.Namespace), "Name", p.Name,
			"Enforcement", p.Enforcement.String(), "Violations", violations,
			"Last audit", audit,
		),
	}}}
	for _, s := range p.Sources {
		d.Sections = append(d.Sections, engine.Section{
			Title: fmt.Sprintf("%s — %s", strings.ToUpper(string(s.Language)), s.Origin),
			Body:  s.Code, Language: string(s.Language),
		})
	}
	if gen := generatedFrom(env, p.Key()); len(gen) > 0 {
		d.Sections = append(d.Sections, engine.Section{Title: "Generated admission policies", Body: strings.Join(gen, "\n")})
	}
	return d
}

// generatedFrom asks every engine that tracks generated objects what came
// from this policy (e.g. a Gatekeeper constraint's native VAP binding).
func generatedFrom(env Env, key model.PolicyKey) []string {
	var out []string
	for _, e := range env.Engines() {
		if g, ok := e.(engine.Generator); ok && e.Name() != key.Engine {
			out = append(out, g.GeneratedFrom(key)...)
		}
	}
	return out
}

func violationRow(env Env, es []engine.Engine, pols map[model.PolicyKey]model.Policy, v model.Violation) engine.Row {
	target := v.Target
	p, readable := pols[v.Policy]
	row := engine.Row{
		ID: query.ViolationID(v),
		Cells: []engine.Cell{
			engine.TextCell(v.Engine), engine.TextCell(v.Policy.Name), engine.AliasCell(query.ShortKind(es, v.Engine, v.Policy.Kind), v.Policy.Kind),
			engine.ModeCell(v.Enforcement), engine.TextCell(v.Target.String()),
			engine.TextCell(table.OneLine(v.Message)),
		},
		Ref:    &target,
		Detail: func() (engine.Detail, error) { return violationDetail(env, v, p, readable), nil },
	}
	if readable {
		row.Object = p.Raw // y / e show the policy
		key := v.Policy
		row.Policy = &key
	} else {
		row.NoObject = missingPolicy(v.Policy, v.PolicyUnreadable)
	}
	return row
}

// noGuidance is the How to fix text when the policy carries none.
const noGuidance = "No guidance in the policy metadata."

// violationDetail is the tenant-facing page for one violation: what is
// wrong, on which object, under which policy, and how to fix it.
func violationDetail(env Env, v model.Violation, p model.Policy, readable bool) engine.Detail {
	owner := "-"
	if v.Owner.Name != "" && v.Owner != v.Target {
		owner = v.Owner.APIVersion() + " " + v.Owner.String()
	}
	d := engine.Detail{Title: "Violation of " + v.Policy.Name}
	if !readable {
		d.Sections = append(d.Sections, engine.Section{Title: "Notice", Body: unreadableNotice(v.Policy, v.PolicyUnreadable)})
	}
	d.Sections = append(d.Sections,
		engine.Section{Title: "Message", Body: v.Message},
		engine.Section{Title: "Target", Body: table.KV(
			"Object", v.Target.APIVersion()+" "+v.Target.String(), "Owner", owner,
			"Audited", table.Age(env.Now(), v.Timestamp)+" ago",
		)},
		engine.Section{Title: "Policy", Body: table.KV(
			"Name", v.Policy.String(), "Engine", v.Engine, "Rule", table.Dash(v.Rule),
			"Enforcement", violationEnforcement(v, p, readable),
		)},
		engine.Section{Title: "How to fix", Body: howToFix(p, readable)},
	)
	return d
}

func unreadableNotice(k model.PolicyKey, rbac bool) string {
	if !rbac {
		return "Policy " + k.String() + " is not loaded (yet, or it failed to load): its enforcement and guidance are unavailable."
	}
	return "Policy " + k.String() + " is not readable with your permissions: its enforcement and guidance are unavailable. The violation itself comes from a report you can read."
}

// missingPolicy explains why y / e have nothing to show for a violation.
func missingPolicy(k model.PolicyKey, rbac bool) string {
	if rbac {
		return "policy " + k.String() + " is not readable with your permissions"
	}
	return "policy " + k.String() + " is not loaded"
}

// violationEnforcement is the violation's action; the policy's own mode is
// added when it differs (e.g. a warn result of an enforcing policy).
func violationEnforcement(v model.Violation, p model.Policy, readable bool) string {
	s := v.Enforcement.String()
	if readable && p.Enforcement.Mode != v.Enforcement.Mode {
		s += " (policy: " + p.Enforcement.String() + ")"
	}
	return s
}

func howToFix(p model.Policy, readable bool) string {
	if !readable {
		return "Unavailable: the policy object is not available."
	}
	if len(p.Guidance) == 0 {
		return noGuidance
	}
	var b strings.Builder
	for i, g := range p.Guidance {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if g.Title != "" && g.Title != g.Text {
			b.WriteString(g.Title + "\n")
		}
		b.WriteString(g.Text + "\n(from " + g.From + ")")
	}
	return b.String()
}

func mutationsView(env Env) engine.View {
	return engine.View{
		Command: CmdMutations, Title: "Mutations",
		Columns: []engine.Column{
			engineColumn, {Title: "NAME", Role: engine.RoleName}, {Title: "KIND", Role: engine.RoleKind},
			{Title: "NAMESPACE", Role: engine.RoleNamespace},
			{Title: "LOCATION"}, {Title: "APPLIES TO", Flex: true},
		},
		DefaultSort: engine.Sort{Role: engine.RoleName},
		Rows: func(f engine.Filter) ([]engine.Row, error) {
			es := env.Engines()
			ms, err := query.Mutations(es, f)
			rows := make([]engine.Row, 0, len(ms))
			for _, m := range ms {
				rows = append(rows, engine.Row{
					ID: query.MutationID(m),
					Cells: []engine.Cell{
						engine.TextCell(m.Engine), engine.TextCell(m.Name), engine.AliasCell(query.ShortKind(es, m.Engine, m.Kind), m.Kind),
						engine.TextCell(table.Dash(m.Namespace)),
						engine.TextCell(table.Dash(m.Location)), engine.TextCell(table.Dash(m.Targets)),
					},
					Object: m.Raw,
				})
			}
			return rows, err
		},
	}
}

func healthView(env Env) engine.View {
	return engine.View{
		Command: CmdHealth, Title: "Health",
		Columns: []engine.Column{
			engineColumn, {Title: "SEVERITY", Role: engine.RoleSeverity}, {Title: "CATEGORY"},
			{Title: "OBJECT", Role: engine.RoleTarget}, {Title: "MESSAGE", Role: engine.RoleMessage, Flex: true},
		},
		DefaultSort: engine.Sort{Role: engine.RoleSeverity, Desc: true},
		Rows: func(f engine.Filter) ([]engine.Row, error) {
			hs, err := query.Health(env.Engines(), f)
			rows := make([]engine.Row, 0, len(hs))
			for _, h := range hs {
				row := engine.Row{
					ID: query.HealthID(h),
					Cells: []engine.Cell{
						engine.TextCell(h.Engine), engine.SeverityCell(h.Severity), engine.TextCell(h.Category),
						engine.TextCell(refText(h.Object)), engine.TextCell(table.OneLine(h.Message)),
					},
				}
				if h.Object.Name != "" {
					ref := h.Object
					row.Ref = &ref
				}
				rows = append(rows, row)
			}
			return rows, err
		},
	}
}

func contextsView(env Env) engine.View {
	return engine.View{
		Command: CmdContexts, Title: "Contexts",
		Columns: []engine.Column{
			{Title: "CURRENT"}, {Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "CLUSTER"},
			{Title: "USER"}, {Title: "NAMESPACE", Role: engine.RoleNamespace},
		},
		Rows: func(engine.Filter) ([]engine.Row, error) {
			ctxs, err := env.Contexts()
			if err != nil {
				return nil, err
			}
			rows := make([]engine.Row, 0, len(ctxs))
			for _, c := range ctxs {
				cur := ""
				if c.Current {
					cur = "*"
				}
				rows = append(rows, engine.Row{ID: c.Name, Cells: []engine.Cell{
					engine.TextCell(cur), engine.TextCell(c.Name), engine.TextCell(c.Cluster),
					engine.TextCell(c.User), engine.TextCell(table.Dash(c.Namespace)),
				}})
			}
			return rows, nil
		},
	}
}

// namespacesView lists namespaces with violation counts. The namespace list
// comes from env.Namespaces (loaded once by the renderer), so informer
// events never trigger API calls.
func namespacesView(env Env) engine.View {
	return engine.View{
		Command: CmdNamespaces, Title: "Namespaces",
		Columns: []engine.Column{
			{Title: "SCOPE"}, {Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "VIOLATIONS", Role: engine.RoleCount},
		},
		Rows: func(engine.Filter) ([]engine.Row, error) {
			names, namesErr := env.Namespaces()
			counts, err := query.Namespaces(env.Engines(), names, namesErr)
			scope := scopeOf(env)
			rows := make([]engine.Row, 0, len(counts))
			for _, c := range counts {
				mark := ""
				if c.Name == scope || (c.Name == query.AllNamespaces && scope == "") {
					mark = "*"
				}
				count := engine.CountCell(c.Violations)
				if c.LowerBound {
					count.Text += "+" // some engine capped its reported list
				}
				rows = append(rows, engine.Row{ID: c.Name, Cells: []engine.Cell{
					engine.TextCell(mark), engine.TextCell(c.Name), count,
				}})
			}
			return rows, err
		},
	}
}

// refText renders an object reference, or "-" for engine-wide issues.
func refText(r model.ObjectRef) string {
	if r.Kind == "" && r.Name == "" {
		return "-"
	}
	return r.String()
}
