package views

import (
	"fmt"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/table"
)

// CmdViolationsRecent is the "recent" tab of :v (admission events); :events
// opens it.
const CmdViolationsRecent = "v.recent"

// IsRecent reports whether a spec is the recent tab.
func IsRecent(s Spec) bool { return s.Command() == CmdViolationsRecent }

// recentNote is the reminder that Events are short-lived history.
const recentNote = "Events are kept for a limited time (kube-apiserver --event-ttl, 1h by default): this is recent history only. Server-side dry-run requests are included (the engines do not mark them). Denials by generated ValidatingAdmissionPolicies are not recorded as Events."

// RecentSpec builds the recent tab.
func RecentSpec(env Env) Spec {
	return Spec{Scoped: true, Source: table.Source{
		View: recentView(env),
		Notices: func(engine.Filter) []string {
			var n []string
			if ev := env.events(); ev != nil {
				for _, ai := range ev.AccessIssues() {
					n = append(n, fmt.Sprintf("cannot %s %s — those admission events are missing", ai.Verb, ai.Resource))
				}
				for _, st := range ev.Statuses() {
					if !st.Enabled {
						n = append(n, st.Engine+": "+st.Hint)
					}
				}
			}
			return n
		},
		Empty: func(engine.Filter) string {
			if env.events() == nil {
				return "No active engine records admission events."
			}
			return "No admission events. " + recentNote
		},
		Synced: func(engine.Filter) bool { return env.eventsSynced() },
	}}
}

// RecentEvents returns admission events passing f (engine, policy, and the
// namespace scope by target), newest first.
func RecentEvents(env Env, f engine.Filter) []model.AdmissionEvent {
	ev := env.events()
	if ev == nil {
		return nil
	}
	var out []model.AdmissionEvent
	for _, e := range ev.Events() {
		if (f.Engine == "" || f.Engine == e.Engine) && f.MatchesPolicy(e.Policy) && f.MatchesTarget(e.Target) {
			out = append(out, e)
		}
	}
	return out
}

func recentView(env Env) engine.View {
	return engine.View{
		Command: CmdViolationsRecent, Title: "Violations · recent",
		Columns: []engine.Column{
			{Title: "TIME", Role: engine.RoleAge}, engineColumn, {Title: "POLICY", Role: engine.RoleName},
			{Title: "KIND", Role: engine.RoleKind}, {Title: "ACTION", Role: engine.RoleMode}, {Title: "USER"},
			{Title: "TARGET", Role: engine.RoleTarget}, {Title: "MESSAGE", Role: engine.RoleMessage, Flex: true},
		},
		DefaultSort: engine.Sort{Role: engine.RoleAge},
		Rows: func(f engine.Filter) ([]engine.Row, error) {
			es := env.Engines()
			evs := RecentEvents(env, f)
			rows := make([]engine.Row, 0, len(evs))
			for _, e := range evs {
				rows = append(rows, eventRow(env, es, e))
			}
			return rows, nil
		},
	}
}

func eventRow(env Env, es []engine.Engine, e model.AdmissionEvent) engine.Row {
	target := e.Target
	return engine.Row{
		ID: eventRowID(e),
		Cells: []engine.Cell{
			engine.AgeCell(e.Time), engine.TextCell(e.Engine), engine.TextCell(e.Policy.Name),
			engine.AliasCell(query.ShortKind(es, e.Engine, e.Policy.Kind), e.Policy.Kind),
			engine.ModeCell(e.Enforcement), engine.TextCell(table.Dash(e.User)),
			engine.TextCell(e.Target.String()), engine.TextCell(table.OneLine(e.Message)),
		},
		Ref: &target,
		Detail: func() (engine.Detail, error) {
			return engine.Detail{Title: "Admission event: " + e.Policy.Name, Sections: []engine.Section{
				{Title: "Summary", Body: table.KV(
					"Engine", e.Engine, "Policy", e.Policy.String(), "Action", e.Enforcement.String(),
					"User", table.Dash(e.User), "Target", e.Target.APIVersion()+" "+e.Target.String(),
					"When", table.Age(env.Now(), e.Time)+" ago", "Count", fmt.Sprint(e.Count),
				)},
				{Title: "Message", Body: e.Message},
				{Title: "Note", Body: recentNote},
			}}, nil
		},
	}
}

// eventRowID is the Event object when known, so a repeating (aggregated)
// Event keeps its row and two identical Events stay apart.
func eventRowID(e model.AdmissionEvent) string {
	if e.ID != "" {
		return e.ID
	}
	return fmt.Sprintf("%s|%s|%s|%d", e.Policy, e.Target, e.Message, e.Time.UnixNano())
}
