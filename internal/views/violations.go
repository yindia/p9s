package views

import (
	"fmt"
	"strings"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/table"
)

// CmdViolationsFlat is the ungrouped violations list. It is not a typed
// command: groups expand into it, and "o" toggles to it.
const CmdViolationsFlat = "v.flat"

// ownerMinWidth fits "StatefulSet/namespace/name" for typical names.
const ownerMinWidth = 24

// Grouping is how :v groups violations.
type Grouping int

// Groupings.
const (
	// GroupByOwner groups by top-level owner (Deployment, CronJob, …) and policy.
	GroupByOwner Grouping = iota
	// GroupByLabel groups by the --group-label value of the owner (or its
	// namespace) and policy.
	GroupByLabel
	// Flat lists every violation.
	Flat
)

// IsViolations reports whether a spec is one of the violations views.
func IsViolations(s Spec) bool {
	c := s.Command()
	return c == CmdViolations || c == CmdViolationsFlat
}

// GroupingOf returns a violations spec's grouping.
func GroupingOf(s Spec) Grouping { return s.grouping }

// ViolationsSpec builds :v with a grouping. GroupByLabel falls back to
// GroupByOwner when no group label is configured.
func ViolationsSpec(env Env, g Grouping) Spec {
	if g == GroupByLabel && env.groupLabel() == "" {
		g = GroupByOwner
	}
	var v engine.View
	switch g {
	case Flat:
		v = flatViolationsView(env)
	default:
		v = groupedViolationsView(env, g)
	}
	has := query.HasViolations
	s := Spec{Scoped: true, grouping: g, Source: table.Source{
		View: v,
		Notices: func(f engine.Filter) []string {
			n := query.AccessNotices(query.Capable(env.Engines(), f, has))
			if o := env.owners(); o != nil && g != Flat {
				if msg := ownerNotice(o.AccessIssues()); msg != "" {
					n = append(n, msg)
				}
			}
			return n
		},
		Empty: func(f engine.Filter) string {
			return query.EmptyReason(query.EmptyInput{
				ConnErr: env.connErr(), Engines: env.Engines(), Detections: env.Detections(),
				ContextName: env.ContextName(), Title: "Violations", Filter: f, Has: has,
			})
		},
		Synced: func(f engine.Filter) bool {
			return query.Synced(query.Capable(env.Engines(), f, has)) && (g == Flat || env.ownersSynced())
		},
	}}
	return s
}

func ownerNotice(issues []model.AccessIssue) string {
	if len(issues) == 0 {
		return ""
	}
	var res, scoped []string
	home := ""
	for _, i := range issues {
		if i.OnlyNamespace != "" {
			scoped, home = append(scoped, i.Resource), i.OnlyNamespace
			continue
		}
		res = append(res, i.Resource)
	}
	var parts []string
	if len(res) > 0 {
		parts = append(parts, "cannot list "+strings.Join(res, ", ")+" cluster-wide")
	}
	if len(scoped) > 0 {
		parts = append(parts, "reading "+strings.Join(scoped, ", ")+" in namespace "+home+" only")
	}
	return "owners: " + strings.Join(parts, "; ") + " — violations group under the deepest owner p9s can see"
}

func flatViolationsView(env Env) engine.View {
	return engine.View{
		Command: CmdViolationsFlat, Title: "Violations",
		Columns: []engine.Column{
			engineColumn, {Title: "POLICY", Role: engine.RoleName}, {Title: "KIND", Role: engine.RoleKind},
			{Title: "ACTION", Role: engine.RoleMode}, {Title: "SEVERITY", Role: engine.RoleSeverity}, {Title: "TARGET", Role: engine.RoleTarget},
			{Title: "MESSAGE", Role: engine.RoleMessage, Flex: true},
		},
		// Rows arrive ordered by policy key, so a stable sort by name keeps
		// each policy's violations together.
		DefaultSort: engine.Sort{Role: engine.RoleName},
		Rows: func(f engine.Filter) ([]engine.Row, error) {
			es := env.Engines()
			vs, err := query.ViolationsWithOwners(es, f, env.lookup(), env.groupLabel())
			pols := query.PolicyIndex(es)
			rows := make([]engine.Row, 0, len(vs))
			for _, v := range vs {
				rows = append(rows, violationRow(env, es, pols, v))
			}
			return rows, err
		},
	}
}

func groupedViolationsView(env Env, g Grouping) engine.View {
	label := ""
	// Owners are what this view is about: keep them readable when space is
	// short, and let the message give way first.
	first := engine.Column{Title: "OWNER", Role: engine.RoleTarget, MinWidth: ownerMinWidth}
	if g == GroupByLabel {
		label = env.groupLabel()
		first = engine.Column{Title: strings.ToUpper(label)}
	}
	return engine.View{
		Command: CmdViolations, Title: "Violations",
		Columns: []engine.Column{
			engineColumn, first, {Title: "POLICY", Role: engine.RoleName}, {Title: "KIND", Role: engine.RoleKind},
			{Title: "ACTION", Role: engine.RoleMode}, {Title: "SEVERITY", Role: engine.RoleSeverity}, {Title: "OBJECTS", Role: engine.RoleCount},
			{Title: "MESSAGE", Role: engine.RoleMessage, Flex: true},
		},
		DefaultSort: engine.Sort{Role: engine.RoleCount, Desc: true},
		Rows: func(f engine.Filter) ([]engine.Row, error) {
			es := env.Engines()
			l := env.lookup()
			vs, err := query.ViolationsWithOwners(es, f, l, env.groupLabel())
			groups := query.GroupViolations(vs, l, label)
			pols := query.PolicyIndex(es)
			rows := make([]engine.Row, 0, len(groups))
			for _, gr := range groups {
				rows = append(rows, groupRow(env, es, pols, gr, g))
			}
			return rows, err
		},
	}
}

func groupRow(env Env, es []engine.Engine, pols map[model.PolicyKey]model.Policy, gr query.Group, g Grouping) engine.Row {
	key := gr.Policy
	drill := engine.Filter{Engine: key.Engine, PolicyKind: key.Kind, PolicyName: key.Name, PolicyNamespace: key.Namespace}
	first := engine.TextCell(gr.Key)
	if g == GroupByLabel {
		drill.Group = gr.Key
	} else {
		drill.Owner = gr.Key
	}
	row := engine.Row{
		ID: gr.Key + "|" + key.String(),
		Cells: []engine.Cell{
			engine.TextCell(key.Engine), first, engine.TextCell(key.Name),
			engine.AliasCell(query.ShortKind(es, key.Engine, key.Kind), key.Kind),
			engine.ModeCell(gr.Enforcement), engine.PolicySeverityCell(pols[key].Severity), engine.CountCell(gr.Objects),
			engine.TextCell(table.OneLine(gr.FirstMessage())),
		},
		Drill:  &engine.Target{Command: CmdViolationsFlat, Filter: drill},
		Detail: func() (engine.Detail, error) { return groupDetail(env, gr, g, pols), nil },
	}
	if p, ok := pols[key]; ok {
		row.Object = p.Raw // y / e show the policy
		row.Policy = &key
	} else {
		row.NoObject = missingPolicy(key, gr.Violations[0].PolicyUnreadable)
	}
	if g != GroupByLabel && gr.Owner.Name != "" {
		owner := gr.Owner
		row.Ref = &owner
	}
	return row
}

func groupDetail(env Env, gr query.Group, g Grouping, pols map[model.PolicyKey]model.Policy) engine.Detail {
	by := "Owner"
	if g == GroupByLabel {
		by = strings.ToUpper(env.groupLabel()[:1]) + env.groupLabel()[1:]
	}
	body := table.KV(
		by, gr.Key, "Policy", gr.Policy.String(), "Action", gr.Enforcement.String(),
		"Affected objects", fmt.Sprint(gr.Objects), "Violations", fmt.Sprint(len(gr.Violations)),
	)
	var b strings.Builder
	for _, v := range gr.Violations {
		fmt.Fprintf(&b, "• %s\n  %s\n", v.Target, v.Message)
	}
	p, readable := pols[gr.Policy]
	d := engine.Detail{Title: gr.Policy.Name + " on " + gr.Key}
	if !readable {
		d.Sections = append(d.Sections, engine.Section{Title: "Notice", Body: unreadableNotice(gr.Policy, gr.Violations[0].PolicyUnreadable)})
	}
	d.Sections = append(d.Sections,
		engine.Section{Title: "Summary", Body: body},
		engine.Section{Title: "Objects", Body: strings.TrimRight(b.String(), "\n")},
		engine.Section{Title: "How to fix", Body: howToFix(p, readable)},
	)
	return d
}
