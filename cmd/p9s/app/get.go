package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yindia/p9s/internal/render"
	"github.com/yindia/p9s/internal/session"
	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

// namespacesTimeout bounds the one API call views make outside the caches.
const namespacesTimeout = 10 * time.Second

type getFlags struct {
	output      string
	namespace   string
	filter      string
	syncTimeout time.Duration
	// flat picks the :v layout the TUI reaches with o.
	flat bool
	// stderr gets notes about the filter (likely typos); results go to w.
	stderr io.Writer
}

func newGetCommand(f *flags) *cobra.Command {
	g := &getFlags{}
	cmd := &cobra.Command{
		Use:   "get <view> [args]",
		Short: "Print a view (pol, v, events, mut, health, overview, impact <policy>, gk/c, …) as a table or JSON",
		Long: `get prints the same materialised view the TUI shows, with the same columns and
default sort, then exits. It waits for every cache to sync first.

Examples:
  p9s get pol
  p9s get v -n payments --filter 'engine:kyverno sev:high'
  p9s get v --flat                      # one row per violation (o in the TUI)
  p9s get impact require-team-label -o json
  p9s get health -o json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			g.stderr = c.ErrOrStderr()
			return runGet(c, f, g, args, c.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&g.output, "output", "o", "table", "output format: table or json")
	fl.StringVarP(&g.namespace, "namespace", "n", "", "scope to one namespace (as :ns in the TUI)")
	fl.StringVar(&g.filter, "filter", "", "filter rows: free text, or engine: mode: ns: kind: sev: name: terms")
	fl.BoolVar(&g.flat, "flat", false, "v: one row per violation instead of grouped by owner")
	fl.DurationVar(&g.syncTimeout, "sync-timeout", 0, "how long to wait for caches; 0 uses the config file's syncTimeout, else 60s")
	return cmd
}

func runGet(cmd *cobra.Command, f *flags, g *getFlags, args []string, w io.Writer) error {
	if g.output != "table" && g.output != "json" {
		return fmt.Errorf("-o %q: want table or json", g.output)
	}
	st, err := resolveSettings(cmd, f)
	if err != nil {
		return err
	}
	switch {
	case g.syncTimeout < 0:
		return fmt.Errorf("--sync-timeout %s: must be positive (0 keeps the default)", g.syncTimeout)
	case g.syncTimeout > 0:
		st.syncTimeout = g.syncTimeout
	}
	closeLog, err := setupLogging(f.logFile)
	if err != nil {
		return err
	}
	defer closeLog()
	opts, snap, err := connectOptions(f, st)
	if err != nil {
		return err
	}
	// Connect bounds itself (detection and discovery timeouts); the sync
	// wait has its own limit, so a slow connect never turns a sync timeout
	// into a bare "context deadline exceeded".
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := session.RunOnce(ctx, opts, st.syncTimeout)
	if err != nil {
		return err
	}
	defer sess.Close()
	env := headlessEnv(sess, g.namespace)
	if snap != nil {
		at := snap.TakenAt // ages as of the snapshot
		env.Now = func() time.Time { return at }
	}
	return printView(w, env, st.sorts, args[0], strings.Join(args[1:], " "), g)
}

// printView resolves name like the ":" prompt and prints it.
func printView(w io.Writer, env views.Env, sorts views.Sorts, name, rest string, g *getFlags) error {
	c, _ := views.Shared(name)
	if (c == views.CmdOverview || c == views.CmdHealth) && g.filter != "" {
		return fmt.Errorf("--filter applies to table views; %s is a dashboard", c)
	}
	if (c == views.CmdOverview || c == views.CmdHealth) && strings.TrimSpace(rest) != "" {
		return fmt.Errorf("%s takes no arguments (the dashboard is cluster-wide; -n scopes the overview)", c)
	}
	if g.flat && c != views.CmdViolations {
		return fmt.Errorf("--flat applies to v")
	}
	switch c {
	case views.CmdOverview:
		if g.output == "json" {
			return render.JSON(w, views.OverviewOf(env))
		}
		return render.Overview(w, views.OverviewOf(env))
	case views.CmdHealth:
		if g.output == "json" {
			return render.JSON(w, views.DashboardOf(env))
		}
		return render.Dashboard(w, views.DashboardOf(env))
	}
	spec, f, err := views.Resolve(env, name, rest)
	if err != nil {
		// The TUI's hint points at its help key; here the help is --help.
		return fmt.Errorf("%s", strings.Replace(err.Error(), "(press ? for help)", "(see p9s get --help)", 1))
	}
	if so, ignored := sorts.Ignored(spec); ignored && g.stderr != nil {
		fmt.Fprintf(g.stderr, "note: config sort.%s: %s has no %s column; using the view's default sort\n", spec.Command(), spec.View.Title, so.Role)
	}
	if g.flat {
		spec = views.ViolationsSpec(env, views.Flat)
	}
	tbl := table.Build(sorts.WithSort(spec).Source, f, env.Now())
	if g.filter != "" {
		// A typo here would print "No rows match" and exit 0: a CI step
		// reading that as "clean" is worse than a failed step.
		problems, notes := tbl.CheckFilter(g.filter)
		if len(problems) > 0 {
			return fmt.Errorf("--filter %q: %s", g.filter, strings.Join(problems, "; "))
		}
		for _, n := range notes {
			if g.stderr != nil {
				fmt.Fprintf(g.stderr, "note: --filter: %s\n", n)
			}
		}
	}
	if q := strings.TrimSpace(f.Text + " " + g.filter); q != "" {
		tbl.Rows = tbl.Match(q)
		if len(tbl.Rows) == 0 && tbl.Empty == "" {
			tbl.Empty = "No rows match " + q + "."
		}
	}
	if tbl.Rows == nil {
		tbl.Rows = []table.Row{} // JSON: [] rather than null
	}
	if g.output == "json" {
		return render.JSON(w, tbl)
	}
	return render.Table(w, tbl)
}

// headlessEnv is views.Env for a session without a UI.
func headlessEnv(s *session.Session, namespace string) views.Env {
	return views.Env{
		Engines: s.Engines, Detections: s.Detections, ContextName: s.ContextName, Contexts: s.Contexts,
		ConnErr: func() error { return nil },
		Namespaces: func() ([]string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), namespacesTimeout)
			defer cancel()
			return s.Namespaces(ctx)
		},
		Scope:  func() string { return namespace },
		Now:    time.Now,
		Owners: s.Owners, Platform: s.Platform, Events: s.Events, UpdatedAt: s.UpdatedAt,
	}
}
