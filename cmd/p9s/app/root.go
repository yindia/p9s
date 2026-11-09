// Package app wires the cobra command: flags, engines, session and UI.
package app

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/yindia/p9s/internal/config"
	"github.com/yindia/p9s/internal/engine"

	// Engines register themselves in engine.Default.
	_ "github.com/yindia/p9s/internal/engines/gatekeeper"
	_ "github.com/yindia/p9s/internal/engines/kyverno"
	_ "github.com/yindia/p9s/internal/engines/vap"
	"github.com/yindia/p9s/internal/session"
	"github.com/yindia/p9s/internal/ui"
)

// Version is set at build time with -ldflags; a "go install …@vX.Y.Z"
// build has none, so it falls back to the module version Go recorded.
var Version = "dev"

func init() { Version = resolveVersion(Version, debug.ReadBuildInfo) }

func resolveVersion(v string, buildInfo func() (*debug.BuildInfo, bool)) string {
	if v != "dev" {
		return v
	}
	if bi, ok := buildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return v
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the p9s version (same as --version)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(c.OutOrStdout(), "p9s version "+Version)
			return err
		},
	}
}

type flags struct {
	kubeconfig string
	context    string
	engines    string
	command    string
	logFile    string
	groupLabel string
	configPath string
	snapshot   string
}

// NewRootCommand builds the p9s command.
func NewRootCommand() *cobra.Command {
	return newRootCommand(&flags{})
}

func newRootCommand(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "p9s",
		Short:         "A terminal UI for Kubernetes policy engines",
		Long:          "p9s browses policies, violations, mutations and engine health for OPA Gatekeeper, Kyverno and native ValidatingAdmissionPolicies using your kubeconfig. It never changes the cluster.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true, // main prints them, without "Error:" for --fail-on results
		Args:          cobra.NoArgs,
		RunE:          func(c *cobra.Command, _ []string) error { return run(c, f) },
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&f.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	pf.StringVar(&f.context, "context", "", "kubeconfig context to use (default: current-context)")
	pf.StringVar(&f.engines, "engine", "auto", "engines to load: auto, or a comma-separated list ("+joinNames()+")")
	pf.StringVar(&f.logFile, "log-file", "", "write client-go logs to this file (default: discarded)")
	pf.StringVar(&f.groupLabel, "group-label", "", "group violations by this label on their owner (falling back to the namespace label), e.g. team")
	pf.StringVar(&f.configPath, "config", "", "config file (default: "+config.DefaultPath()+")")
	pf.StringVar(&f.snapshot, "snapshot", "", "read a file written by p9s snapshot instead of a cluster")
	fl := cmd.Flags()
	// --readonly is kept so existing scripts still run: p9s is always
	// read-only (AGENTS.md rule 1), so the flag has no effect.
	fl.Bool("readonly", true, "")
	_ = fl.MarkDeprecated("readonly", "p9s is always read-only; the flag has no effect")
	fl.StringVarP(&f.command, "command", "c", "overview", "initial view, e.g. overview, pol, v, gk/ct")
	cmd.AddCommand(newGetCommand(f), newSnapshotCommand(f), newReportCommand(f), newVersionCommand())
	return cmd
}

func joinNames() string {
	names := engine.Default.Names()
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func run(cmd *cobra.Command, f *flags) error {
	st, err := resolveSettings(cmd, f)
	if err != nil {
		return err
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
	opts.ReadOnly = true
	sess, err := session.New(opts)
	if err != nil {
		return err
	}
	defer sess.Close()

	uo := ui.Options{InitialCommand: st.command, GroupLabel: st.groupLabel, Sorts: st.sorts,
		NoHighlight: os.Getenv("NO_COLOR") != ""}
	if snap != nil {
		at := snap.TakenAt
		uo.SnapshotAt, uo.Now = at, func() time.Time { return at }
	}
	app := ui.New(sess, uo)
	if _, err := tea.NewProgram(app, tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	return nil
}

// setupLogging keeps client-go's klog output from corrupting the TUI.
func setupLogging(path string) (func(), error) {
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	_ = fs.Set("logtostderr", "false")
	_ = fs.Set("alsologtostderr", "false")
	_ = fs.Set("stderrthreshold", "FATAL")
	if path == "" {
		klog.SetOutput(io.Discard)
		return func() {}, nil
	}
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("--log-file: %w", err)
	}
	klog.SetOutput(lf)
	return func() { klog.Flush(); _ = lf.Close() }, nil
}
