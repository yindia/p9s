package app

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yindia/p9s/internal/config"
	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/kube"
	"github.com/yindia/p9s/internal/session"
	"github.com/yindia/p9s/internal/snapshot"
	"github.com/yindia/p9s/internal/views"
)

// settings are the effective options: flags explicitly set, else the
// config file (with the context's overrides), else built-in defaults.
type settings struct {
	command     string
	engines     string
	syncTimeout time.Duration
	sorts       views.Sorts
}

func resolveSettings(cmd *cobra.Command, f *flags) (settings, error) {
	path, explicit := f.configPath, f.configPath != ""
	if !explicit {
		path = config.DefaultPath()
	}
	var file config.File
	if path != "" {
		var err error
		if file, err = config.Load(path, explicit); err != nil {
			return settings{}, err
		}
	}
	ctx := f.context
	if ctx == "" && f.snapshot != "" {
		if snap, err := snapshot.Read(f.snapshot); err == nil {
			ctx = snap.Context.Name // overrides follow the replayed context
		}
	}
	if ctx == "" {
		// Overrides follow the context p9s will connect to.
		if _, current, err := kube.NewLoader(kube.Options{Kubeconfig: f.kubeconfig}).Contexts(); err == nil {
			ctx = current
		}
	}
	fs := file.For(ctx)
	st := settings{command: f.command, engines: f.engines, syncTimeout: session.DefaultSyncTimeout}
	changed := func(name string) bool {
		fl := cmd.Flags().Lookup(name)
		return fl != nil && fl.Changed
	}
	if !changed("command") && fs.StartView != "" {
		if err := views.CheckStartView(fs.StartView); err != nil {
			return settings{}, fmt.Errorf("config %s: startView: %w", path, err)
		}
		st.command = fs.StartView
	}
	if !changed("engine") && fs.Engines != "" {
		st.engines = fs.Engines
	}
	if fs.SyncTimeout != 0 {
		st.syncTimeout = time.Duration(fs.SyncTimeout)
	}
	raw := map[string]engine.Sort{}
	for view, spec := range fs.Sort {
		s, err := config.ParseSort(spec) // validated on load; cannot fail here
		if err != nil {
			return settings{}, err
		}
		raw[view] = s
	}
	sorts, err := views.ParseSorts(raw)
	if err == nil {
		err = sorts.Check()
	}
	if err != nil {
		return settings{}, fmt.Errorf("config %s: %w", path, err)
	}
	st.sorts = sorts
	return st, nil
}

// connectOptions is sessionOptions, replaying --snapshot when set.
func connectOptions(f *flags, st settings) (session.Options, *snapshot.File, error) {
	opts, err := sessionOptions(f, st)
	if err != nil || f.snapshot == "" {
		return opts, nil, err
	}
	snap, err := snapshot.Read(f.snapshot)
	if err != nil {
		return opts, nil, err
	}
	if f.context != "" && f.context != snap.Context.Name {
		return opts, nil, fmt.Errorf("--context %q: the snapshot holds context %q", f.context, snap.Context.Name)
	}
	opts.Connector, opts.Context = snapshot.Connector(snap), snap.Context.Name
	return opts, snap, nil
}

func sessionOptions(f *flags, st settings) (session.Options, error) {
	regs, err := engine.Default.Selection(st.engines)
	if err != nil {
		return session.Options{}, fmt.Errorf("--engine: %w", err)
	}
	return session.Options{
		Connector: session.KubeConnector(kube.NewLoader(kube.Options{Kubeconfig: f.kubeconfig})),
		Engines:   regs,
		Context:   f.context,
		ReadOnly:  true,
	}, nil
}
