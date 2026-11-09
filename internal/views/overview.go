package views

import (
	"fmt"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/table"
)

// CmdOverview is the start view.
const CmdOverview = "overview"

// overviewLimit bounds the rows each overview block shows.
const overviewLimit = 5

// Block is one overview panel: a small materialised table (or a
// placeholder for a feature that has not landed), and where enter goes.
type Block struct {
	ID    string      `json:"id"`
	Title string      `json:"title"`
	Table table.Table `json:"table"`
	// Summary is a one-line headline, e.g. health counts.
	Summary string `json:"summary,omitempty"`
	// Placeholder is set instead of rows for blocks of later phases.
	Placeholder string `json:"placeholder,omitempty"`
	// Target is the full view; nil for placeholders.
	Target *BlockTarget `json:"target,omitempty"`
	// Total counts all items behind the block (the table shows the first few).
	Total int `json:"total"`
}

// BlockTarget is a command plus the "/" filter to open it with.
type BlockTarget struct {
	Command string `json:"command"`
	Filter  string `json:"filter,omitempty"`
}

// Overview is the start view: one screen answering "is the policy layer
// working, and what should I look at first".
type Overview struct {
	SchemaVersion string  `json:"schemaVersion"`
	Blocks        []Block `json:"blocks"`
}

// Overview block IDs.
const (
	BlockHealth     = "health"
	BlockViolators  = "violators"
	BlockCandidates = "candidates"
	BlockDenials    = "denials"
)

// OverviewOf builds the overview for the current namespace scope.
func OverviewOf(env Env) Overview {
	f := engine.Filter{Namespace: scopeOf(env)}
	es := env.Engines()
	synced := func(engine.Filter) bool { return query.Synced(es) }
	return Overview{SchemaVersion: table.SchemaVersion, Blocks: []Block{
		healthBlock(env, f),
		violatorsBlock(env, f, synced),
		candidatesBlock(env, f, synced),
		denialsBlock(env, f),
	}}
}

// syncNote marks headline text computed from caches that are still loading.
func syncNote(synced bool) string {
	if synced {
		return ""
	}
	return " (syncing)"
}

// healthBlock summarises the :health dashboard: its graded counts and the
// worst checks across panels. Health is cluster-wide: the :ns scope does not
// apply, and the title says so when a scope is set.
func healthBlock(env Env, f engine.Filter) Block {
	d := DashboardOf(env)
	var bad []engine.Row
	for _, p := range d.Panels {
		for _, r := range p.Table.Rows {
			sev := r.Cells[len(r.Cells)-1]
			if sev.Severity == model.SeverityInfo || sev.Severity == "" {
				continue // ✓ or ? unknown
			}
			bad = append(bad, engine.Row{ID: p.ID + "|" + r.ID, Cells: []engine.Cell{
				engine.TextCell(p.Title), engine.TextCell(r.Cells[0].Text), engine.TextCell(r.Cells[1].Text),
				{Kind: engine.CellSeverity, Severity: sev.Severity, Text: sev.Text},
			}})
		}
	}
	// Critical before warning; panels keep their order within a grade.
	sortBySeverity(bad)
	total := len(bad)
	shown := bad[:min(overviewLimit, total)]
	v := engine.View{Command: CmdOverview, Title: "Health",
		Columns: []engine.Column{{Title: "PANEL"}, engineColumn, {Title: "CHECK", Role: engine.RoleName}, {Title: "STATUS", Role: engine.RoleSeverity, Flex: true}},
		Rows:    rowsOf(shown),
	}
	es := env.Engines()
	synced := query.Synced(es) && env.platformSynced()
	note := syncNote(synced)
	tbl := table.Build(table.Source{View: v, Synced: func(engine.Filter) bool { return synced },
		Empty: func(engine.Filter) string { return "All checks pass." + note }}, engine.Filter{}, env.Now())
	o := d.Overall
	title := "Health"
	if f.Namespace != "" {
		title += " (cluster-wide)"
	}
	summary := fmt.Sprintf("✗ %d critical · ! %d warning · ✓ %d ok", o.Critical, o.Warning, o.OK)
	if o.Unknown > 0 {
		summary += fmt.Sprintf(" · ? %d unknown", o.Unknown)
	}
	return Block{
		ID: BlockHealth, Title: title, Table: tbl, Total: total,
		Summary: summary + note,
		Target:  &BlockTarget{Command: CmdHealth},
	}
}

func sortBySeverity(rows []engine.Row) {
	rank := func(r engine.Row) int {
		if r.Cells[3].Severity == model.SeverityError {
			return 0
		}
		return 1
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rank(rows[j]) < rank(rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func policyBlockView(title string, rows func() ([]query.PolicyEntry, error), withMode bool, es func() []engine.Engine) engine.View {
	cols := []engine.Column{
		engineColumn, {Title: "NAME", Role: engine.RoleName, Flex: true}, {Title: "KIND", Role: engine.RoleKind},
	}
	if withMode {
		cols = append(cols, engine.Column{Title: "ENFORCEMENT", Role: engine.RoleMode})
	}
	cols = append(cols, engine.Column{Title: "VIOLATIONS", Role: engine.RoleCount})
	return engine.View{
		Command: CmdOverview, Title: title, Columns: cols,
		Rows: func(engine.Filter) ([]engine.Row, error) {
			ps, err := rows()
			out := make([]engine.Row, 0, len(ps))
			for _, p := range ps {
				cells := []engine.Cell{
					engine.TextCell(p.Engine), engine.TextCell(p.Name),
					engine.AliasCell(query.ShortKind(es(), p.Engine, p.Kind), p.Kind),
				}
				if withMode {
					cells = append(cells, engine.ModeCell(p.Enforcement))
				}
				count := engine.KnownCount(p.ViolationCount, p.ViolationsKnown)
				if p.LowerBound && p.ViolationsKnown {
					count.Text += "+"
				}
				cells = append(cells, count)
				key := p.Key()
				out = append(out, engine.Row{ID: key.String(), Cells: cells, Object: p.Raw, Policy: &key})
			}
			return out, err
		},
	}
}

// violatorsBlock ranks owners (Deployments, CronJobs, …) by affected
// objects across all policies: what a team would fix first.
func violatorsBlock(env Env, f engine.Filter, synced func(engine.Filter) bool) Block {
	vs, err := query.ViolationsWithOwners(env.Engines(), f, env.lookup())
	owners := query.TopOwners(query.GroupViolations(vs))
	shown := owners[:min(overviewLimit, len(owners))]
	note := syncNote(synced(f) && env.ownersSynced())
	const title = "Top violating owners"
	v := engine.View{
		Command: CmdOverview, Title: title,
		Columns: []engine.Column{
			{Title: "OWNER", Role: engine.RoleTarget, Flex: true}, {Title: "POLICIES"},
			{Title: "OBJECTS", Role: engine.RoleCount},
		},
		Rows: func(engine.Filter) ([]engine.Row, error) {
			rows := make([]engine.Row, 0, len(shown))
			for _, o := range shown {
				row := engine.Row{ID: o.Key, Cells: []engine.Cell{
					engine.TextCell(o.Key), engine.CountCell(o.Policies), engine.CountCell(o.Objects),
				}}
				if o.Owner.Name != "" {
					ref := o.Owner
					row.Ref = &ref
				}
				rows = append(rows, row)
			}
			return rows, err
		},
	}
	tbl := table.Build(table.Source{View: v, Synced: func(f engine.Filter) bool { return synced(f) && env.ownersSynced() },
		Empty: func(engine.Filter) string { return "No violations." + note }}, f, env.Now())
	return Block{ID: BlockViolators, Title: title, Table: tbl, Total: len(owners),
		Target: &BlockTarget{Command: CmdViolations}}
}

func candidatesBlock(env Env, f engine.Filter, synced func(engine.Filter) bool) Block {
	all, err := query.EnforcementCandidates(env.Engines(), f)
	clean := 0
	for _, p := range all {
		if p.ViolationsKnown && p.ViolationCount == 0 {
			clean++
		}
	}
	note := syncNote(synced(f))
	shown := all[:min(overviewLimit, len(all))]
	v := policyBlockView("Enforcement candidates", func() ([]query.PolicyEntry, error) { return shown, err }, true, env.Engines)
	tbl := table.Build(table.Source{View: v, Synced: synced,
		Empty: func(engine.Filter) string { return "No policies in audit or warn mode." + note }}, f, env.Now())
	return Block{
		ID: BlockCandidates, Title: "Enforcement candidates (audit or warn)", Table: tbl, Total: len(all),
		Summary: fmt.Sprintf("%d in audit or warn mode, %d with no current violations", len(all), clean) + note,
		Target:  &BlockTarget{Command: CmdPolicies, Filter: "mode:" + string(model.ModeAudit) + "," + string(model.ModeWarn)},
	}
}

// denialsBlock shows the latest admission events that blocked a request.
func denialsBlock(env Env, f engine.Filter) Block {
	ev := env.events()
	if ev == nil {
		return Block{ID: BlockDenials, Title: "Recent admission denials", Placeholder: "No active engine records admission events."}
	}
	var denied []model.AdmissionEvent
	for _, e := range RecentEvents(env, f) {
		if e.Enforcement.Mode == model.ModeEnforce {
			denied = append(denied, e)
		}
	}
	shown := denied[:min(overviewLimit, len(denied))]
	es := env.Engines()
	v := engine.View{Command: CmdOverview, Title: "Recent admission denials",
		Columns: []engine.Column{
			{Title: "TIME", Role: engine.RoleAge}, engineColumn, {Title: "POLICY", Role: engine.RoleName},
			{Title: "USER"}, {Title: "TARGET", Role: engine.RoleTarget, Flex: true},
		},
		Rows: func(engine.Filter) ([]engine.Row, error) {
			rows := make([]engine.Row, 0, len(shown))
			for _, e := range shown {
				r := eventRow(env, es, nil, e)
				r.Cells = []engine.Cell{r.Cells[0], r.Cells[1], r.Cells[2], r.Cells[5], r.Cells[6]}
				rows = append(rows, r)
			}
			return rows, nil
		},
	}
	empty := "No denials in recent Events."
	for _, st := range ev.Statuses() {
		if !st.Enabled {
			empty += " " + st.Engine + " records none: " + st.Hint
		}
	}
	tbl := table.Build(table.Source{View: v, Synced: func(engine.Filter) bool { return env.eventsSynced() },
		Empty: func(engine.Filter) string { return empty }}, f, env.Now())
	return Block{ID: BlockDenials, Title: "Recent admission denials", Table: tbl, Total: len(denied),
		Summary: fmt.Sprintf("%d blocked in recent Events (server-side dry-runs included)", len(denied)),
		Target:  &BlockTarget{Command: "events"}}
}
