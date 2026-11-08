package ui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

var quitCommands = map[string]bool{"q": true, "q!": true, "quit": true}

// runCommand executes a ":" command line such as "pol gatekeeper" or "gk/c".
// It returns a tea.Quit when the command quits.
func (a *App) runCommand(line string) tea.Cmd {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), ":"))
	if len(fields) == 0 {
		return nil
	}
	name, args := strings.ToLower(fields[0]), strings.Join(fields[1:], " ")
	if quitCommands[name] {
		return tea.Quit
	}
	if name == "help" {
		a.openHelp()
		return nil
	}
	cmd, _ := views.Shared(name)
	if cmd == views.CmdNamespaces && args != "" {
		return a.setNamespace(args)
	}
	if cmd == views.CmdHealth {
		if args != "" {
			a.setFlash(":health takes no arguments (the dashboard is cluster-wide)", true)
			return nil
		}
		a.help = nil
		a.stack = []screen{newHealthScreen(a.env)}
		return nil
	}
	if cmd == views.CmdOverview {
		if args != "" {
			a.setFlash(":overview takes no arguments (it follows the :ns scope)", true)
			return nil
		}
		a.help = nil
		a.stack = []screen{newOverviewScreen(a.env)}
		return nil
	}
	spec, f, err := views.Resolve(a.env, name, args)
	if err != nil {
		a.setFlash(err.Error(), true)
		return nil
	}
	a.help = nil
	s := a.newScreen(spec, f)
	if spec.Command() == views.CmdNamespaces {
		// ":ns" stacks on the current view so picking a namespace returns to it.
		a.push(s)
		return a.loadNamespaces()
	}
	a.stack = nil
	a.push(s)
	return nil
}

// newScreen wraps a spec in screen state, wiring the interactive rows.
func (a *App) newScreen(spec views.Spec, f engine.Filter) *tableScreen {
	s := newTableScreen(a.opts.Sorts.WithSort(spec), f, a.now)
	switch spec.Command() {
	case views.CmdContexts:
		s.onEnter = a.switchContext
	case views.CmdNamespaces:
		s.onEnter = func(r table.Row) tea.Cmd { return a.setNamespace(r.ID) }
	}
	return s
}

// setNamespace scopes every view to ns ("all", "-" or "" clear it) and
// re-filters the whole stack so going back keeps the scope.
func (a *App) setNamespace(ns string) tea.Cmd {
	if ns == views.AllNamespaces || ns == "-" {
		ns = ""
	}
	a.namespace = ns
	for _, s := range a.stack {
		if t, ok := s.(*tableScreen); ok && t.spec.Scoped {
			t.filter.Namespace = ns
		}
	}
	if t, ok := a.top().(*tableScreen); ok && t.spec.Command() == views.CmdNamespaces {
		if len(a.stack) > 1 {
			a.pop()
		} else {
			a.stack = []screen{newOverviewScreen(a.env)}
		}
	}
	a.refreshTop()
	if ns == "" {
		a.setFlash("namespace scope: all", false)
	} else {
		a.setFlash("namespace scope: "+ns, false)
	}
	return nil
}

func (a *App) loadNamespaces() tea.Cmd {
	b, gen := a.backend, a.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), getTimeout)
		defer cancel()
		names, err := b.Namespaces(ctx)
		return namespacesMsg{gen: gen, names: names, err: err}
	}
}
