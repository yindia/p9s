package app

import (
	"strings"
	"testing"
)

func TestFlagsAndDefaults(t *testing.T) {
	cmd := NewRootCommand()
	ro, err := cmd.Flags().GetBool("readonly")
	if err != nil || !ro {
		t.Errorf("--readonly default = %v, %v; want true", ro, err)
	}
	if e, _ := cmd.PersistentFlags().GetString("engine"); e != "auto" {
		t.Errorf("--engine default = %q", e)
	}
	for _, f := range []string{"kubeconfig", "context", "offline", "config"} {
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
		{[]string{"--offline", "dir"}, "phase 4"},
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
