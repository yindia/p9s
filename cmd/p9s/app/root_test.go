package app

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestFlagsAndDefaults(t *testing.T) {
	cmd := NewRootCommand()
	// --readonly survives as a deprecated no-op so old scripts still run.
	if fl := cmd.Flags().Lookup("readonly"); fl == nil || fl.Deprecated == "" {
		t.Errorf("--readonly should be kept and marked deprecated: %+v", fl)
	}
	if cmd.Flags().Lookup("offline") != nil {
		t.Error("--offline was never implemented and must not be advertised")
	}
	if e, _ := cmd.PersistentFlags().GetString("engine"); e != "auto" {
		t.Errorf("--engine default = %q", e)
	}
	for _, f := range []string{"kubeconfig", "context", "config", "snapshot"} {
		if cmd.Flags().Lookup(f) == nil && cmd.PersistentFlags().Lookup(f) == nil {
			t.Errorf("missing flag --%s", f)
		}
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--offline", "dir"}, "unknown flag: --offline"},
		{[]string{"--engine", "nope"}, `unknown engine "nope"`},
		{[]string{"extra-arg"}, "unknown command"},
	}
	for _, tt := range tests {
		cmd := NewRootCommand()
		cmd.SetArgs(tt.args)
		cmd.SilenceErrors = true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestSetupLogging(t *testing.T) {
	closeFn, err := setupLogging("")
	if err != nil {
		t.Fatal(err)
	}
	closeFn()
	closeFn, err = setupLogging(t.TempDir() + "/p9s.log")
	if err != nil {
		t.Fatal(err)
	}
	closeFn()
	if _, err := setupLogging("/nonexistent-dir/x.log"); err == nil {
		t.Error("expected error for unwritable log path")
	}
}

func TestResolveVersion(t *testing.T) {
	bi := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, true }
	}
	for _, tc := range []struct {
		ldflags, module, want string
	}{
		{"0.0.2", "v0.0.1", "0.0.2"}, // -ldflags wins
		{"dev", "v0.0.2", "v0.0.2"},  // go install …@v0.0.2
		{"dev", "(devel)", "dev"},    // go run in the repo
	} {
		if got := resolveVersion(tc.ldflags, bi(tc.module)); got != tc.want {
			t.Errorf("resolveVersion(%q, %q) = %q, want %q", tc.ldflags, tc.module, got, tc.want)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	var b strings.Builder
	cmd := NewRootCommand()
	cmd.SetOut(&b)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil || !strings.HasPrefix(b.String(), "p9s version ") {
		t.Errorf("version: %q, %v", b.String(), err)
	}
}
