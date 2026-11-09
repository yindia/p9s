package ui

import (
	"fmt"
	"strings"

	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

var navigationHelp = [][2]string{
	{"j k ↑ ↓", "move"},
	{"pgup pgdn", "page"},
	{"home end G", "first / last (end where a view uses G, e.g. vap/pol)"},
	{"enter", "drill down (or describe when there is nothing to drill into)"},
	{"esc", "clear filter, else back"},
	{":", "command mode (tab completes)"},
	{"/", "filter: free text, or engine: mode: ns: kind: sev: name: terms"},
	{"0", "all namespaces (clear the :ns scope)"},
	{"?", "this help"},
	{"ctrl+c :q", "quit"},
}

var keysHelp = [][2]string{
	{"d", "describe"},
	{"y", "YAML (Secret values are redacted)"},
	{"e", "open the YAML in $VISUAL or $EDITOR (changes are discarded)"},
	{"g", "go to the source object"},
	{"c", "copy kind/namespace/name of the row's object"},
	{"C", "copy a kubectl get command for it"},
	{"N V A S", "sort by name, count, age, severity (again to reverse)"},
	{"o", ":v grouped by owner ↔ flat list"},
	{"t", ":v grouped by owner ↔ by --group-label"},
	{"[ ]", ":v current tab (audit results) ↔ recent tab (admission events)"},
	{"i", "enforcement impact of the policy on this row"},
}

// helpDetail builds the "?" page: one column in sections, so it scrolls and
// wraps instead of being cut off on narrow terminals.
func helpDetail(env views.Env) table.Detail {
	section := func(title string, rows [][2]string, width int) table.Section {
		var b strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&b, "%-*s  %s\n", width, r[0], r[1])
		}
		return table.Section{Title: title, Body: strings.TrimRight(b.String(), "\n")}
	}
	engineViews := views.HelpEngineCommands(env)
	d := table.Detail{SchemaVersion: table.SchemaVersion, Title: "Help", Sections: []table.Section{
		section("Navigation", navigationHelp, 10),
		section("Views", views.HelpCommands(), 16),
		section("Keys", keysHelp, 10),
	}}
	if len(engineViews) > 0 {
		d.Sections = append(d.Sections, section("Engine views (also reached by drilling down)", engineViews, 16))
	} else {
		d.Sections = append(d.Sections, table.Section{Title: "Engine views", Body: "No engine is active."})
	}
	return d
}
