package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/session"
	"github.com/yindia/p9s/internal/snapshot"
)

func newSnapshotCommand(f *flags) *cobra.Command {
	var out string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "snapshot -o <file.json>",
		Short: "Record what p9s reads from the cluster, for offline use with --snapshot",
		Long: `snapshot syncs like the TUI and records every answer the engines, owner
resolver and health and events watchers received (and every RBAC denial),
then writes them as versioned JSON. "p9s --snapshot <file>" and
"p9s --snapshot <file> get ..." replay it through the real engines.
Never recorded: Secret and ConfigMap data, container env values,
managedFields and kubectl's last-applied copy. Policies, reports, webhook
configurations and workload specs are recorded as read: share the file as
you would a "kubectl get -o yaml" of them.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return runSnapshot(c, f, out, timeout, c.ErrOrStderr()) },
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "file to write (required)")
	cmd.Flags().DurationVar(&timeout, "sync-timeout", 0, "how long to wait for caches; 0 uses the config file's syncTimeout, else 60s")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func runSnapshot(cmd *cobra.Command, f *flags, out string, timeout time.Duration, log io.Writer) error {
	if f.snapshot != "" {
		return fmt.Errorf("snapshot reads a live cluster; drop --snapshot")
	}
	st, err := resolveSettings(cmd, f)
	if err != nil {
		return err
	}
	if timeout < 0 {
		return fmt.Errorf("--sync-timeout %s: must be positive (0 keeps the default)", timeout)
	}
	if timeout > 0 {
		st.syncTimeout = timeout
	}
	closeLog, err := setupLogging(f.logFile)
	if err != nil {
		return err
	}
	defer closeLog()
	opts, err := sessionOptions(f, st)
	if err != nil {
		return err
	}
	rec := snapshot.NewRecorder()
	opts.Connector = rec.Connector(opts.Connector)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := session.RunOnce(ctx, opts, st.syncTimeout)
	if err != nil {
		return err
	}
	defer sess.Close()
	file := rec.File(time.Now(), Version, currentContext(sess))
	if err := file.WriteFile(out); err != nil {
		return err
	}
	for _, w := range rec.Warnings() {
		_, _ = fmt.Fprintf(log, "warning: %s\n", w)
	}
	_, _ = fmt.Fprintf(log, "wrote %s: context %s, %d resources\n", out, file.Context.Name, len(file.Objects))
	return nil
}

func currentContext(s *session.Session) model.KubeContext {
	name := s.ContextName()
	if cs, err := s.Contexts(); err == nil {
		for _, c := range cs {
			if c.Name == name {
				return c
			}
		}
	}
	return model.KubeContext{Name: name}
}
