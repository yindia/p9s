package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yindia/p9s/internal/kube"
	"github.com/yindia/p9s/internal/render"
	"github.com/yindia/p9s/internal/session"
	"github.com/yindia/p9s/internal/views"
)

// ExitError carries a process exit code (see the README's exit codes).
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Exit codes of p9s report.
const (
	exitFindings = 2 // findings at or above --fail-on
)

type reportFlags struct {
	format      string
	output      string
	allContexts bool
	since       string
	failOn      string
	syncTimeout time.Duration
}

func newReportCommand(f *flags) *cobra.Command {
	rf := &reportFlags{}
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Write a security report (Markdown, HTML or JSON) for one or more contexts",
		Long: `report summarises policies, Pod Security Standards coverage, violations, health
findings and unprotected kinds for security teams. --context takes a
comma-separated list; --all-contexts reports on every kubeconfig context.

Exit codes: 0 ok; 1 error; 2 findings at or above --fail-on.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return runReport(c, f, rf) },
	}
	fl := cmd.Flags()
	fl.StringVar(&rf.format, "format", "md", "md, html or json")
	fl.StringVarP(&rf.output, "output", "o", "", "file to write (default: stdout)")
	fl.BoolVar(&rf.allContexts, "all-contexts", false, "report on every kubeconfig context")
	fl.StringVar(&rf.since, "since", "", "a previous report (JSON) to compare with")
	fl.StringVar(&rf.failOn, "fail-on", "", "exit 2 when a finding is at or above: critical or warning")
	fl.DurationVar(&rf.syncTimeout, "sync-timeout", 0, "how long to wait for caches per context; 0 uses the config file, else 60s")
	return cmd
}

func runReport(cmd *cobra.Command, f *flags, rf *reportFlags) error {
	if rf.format != "md" && rf.format != "html" && rf.format != "json" {
		return fmt.Errorf("--format %q: want md, html or json", rf.format)
	}
	if rf.failOn != "" && rf.failOn != "critical" && rf.failOn != "warning" {
		return fmt.Errorf("--fail-on %q: want critical or warning", rf.failOn)
	}
	if rf.syncTimeout < 0 {
		return fmt.Errorf("--sync-timeout %s: must be positive", rf.syncTimeout)
	}
	var prev *views.ReportSet
	if rf.since != "" {
		p, err := readReportSet(rf.since)
		if err != nil {
			return err
		}
		prev = p
	}
	contexts, err := reportContexts(f, rf)
	if err != nil {
		return err
	}
	closeLog, err := setupLogging(f.logFile)
	if err != nil {
		return err
	}
	defer closeLog()

	set := views.ReportSet{SchemaVersion: views.ReportSchemaVersion}
	var failed []string
	for _, name := range contexts {
		r, err := reportFor(cmd, f, rf, name)
		if err != nil {
			// One unreachable context must not lose the others' reports.
			failed = append(failed, contextLabel(name))
			r = views.Report{Error: err.Error(), Header: views.ReportHeader{Context: contextLabel(name), GeneratedAt: time.Now().UTC(), P9sVersion: Version}}
		} else if prev != nil {
			since(prev, &r)
		}
		if err := r.Seal(); err != nil {
			return err
		}
		set.Reports = append(set.Reports, r)
	}
	if err := writeReport(cmd.OutOrStdout(), rf, set); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("report written, but %d context(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return failOn(rf.failOn, set)
}

func contextLabel(name string) string {
	if name == "" {
		return "(current context)"
	}
	return name
}

// since compares r with the previous report of its context, or says why
// it cannot.
func since(prev *views.ReportSet, r *views.Report) {
	for _, p := range prev.Reports {
		if p.Header.Context == r.Header.Context && p.Error == "" {
			d := views.Diff(p, *r)
			r.Since = &d
			return
		}
	}
	r.Notices = append(r.Notices, "--since: the previous report has no report for context "+r.Header.Context)
}

// reportContexts resolves --context a,b, --all-contexts or the default.
func reportContexts(f *flags, rf *reportFlags) ([]string, error) {
	if f.snapshot != "" {
		if rf.allContexts || strings.Contains(f.context, ",") {
			return nil, errors.New("a snapshot holds one context: drop --all-contexts and lists in --context")
		}
		return []string{f.context}, nil
	}
	if rf.allContexts {
		if f.context != "" {
			return nil, errors.New("use --context or --all-contexts, not both")
		}
		cs, _, err := kube.NewLoader(kube.Options{Kubeconfig: f.kubeconfig}).Contexts()
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(cs))
		for _, c := range cs {
			out = append(out, c.Name)
		}
		return out, nil
	}
	var out []string
	for _, c := range strings.Split(f.context, ",") {
		out = append(out, strings.TrimSpace(c)) // "" is the current context
	}
	return out, nil
}

func reportFor(cmd *cobra.Command, f *flags, rf *reportFlags, name string) (views.Report, error) {
	one := *f
	one.context = name
	st, err := resolveSettings(cmd, &one)
	if err != nil {
		return views.Report{}, err
	}
	if rf.syncTimeout > 0 {
		st.syncTimeout = rf.syncTimeout
	}
	opts, snap, err := connectOptions(&one, st)
	if err != nil {
		return views.Report{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := session.RunOnce(ctx, opts, st.syncTimeout)
	if err != nil {
		return views.Report{}, err
	}
	defer sess.Close()
	env := headlessEnv(sess, "", "")
	meta := views.ReportMeta{P9sVersion: Version, Generated: time.Now(), Cluster: clusterOf(sess)}
	if snap != nil {
		at := snap.TakenAt
		env.Now = func() time.Time { return at }
		meta.Snapshot = at
	}
	return views.ReportOf(env, meta, sess.APIResources), nil
}

// clusterOf is the kubeconfig cluster name of the session's context.
func clusterOf(s *session.Session) string {
	return currentContext(s).Cluster
}

// writeReport renders fully first, then writes stdout or replaces -o
// atomically (temp file, rename) with mode 0600.
func writeReport(stdout io.Writer, rf *reportFlags, set views.ReportSet) error {
	var buf bytes.Buffer
	var err error
	switch rf.format {
	case "html":
		err = render.HTML(&buf, set)
	case "json":
		err = render.JSON(&buf, set)
	default:
		err = render.Markdown(&buf, set)
	}
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if rf.output == "" {
		_, err := stdout.Write(buf.Bytes())
		return err
	}
	return atomicWrite(rf.output, buf.Bytes())
}

func atomicWrite(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".p9s-report-*")
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("report: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("report: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("report: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	return nil
}

func readReportSet(path string) (*views.ReportSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--since: %w", err)
	}
	var set views.ReportSet
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("--since %s: not a p9s report JSON: %w", path, err)
	}
	if set.SchemaVersion != views.ReportSchemaVersion {
		return nil, fmt.Errorf("--since %s: schema %q, want %q (write it with --format json)", path, set.SchemaVersion, views.ReportSchemaVersion)
	}
	return &set, nil
}

// failOn returns exit code 2 when any report has a finding at or above
// the level.
func failOn(level string, set views.ReportSet) error {
	if level == "" {
		return nil
	}
	for _, r := range set.Reports {
		w := r.Worst()
		if w == "critical" || (level == "warning" && w == "warning") {
			return &ExitError{Code: exitFindings, Err: fmt.Errorf("%s: %s findings (--fail-on %s)", r.Header.Context, w, level)}
		}
	}
	return nil
}
