package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/views"
)

var update = flag.Bool("update", false, "rewrite golden files")

func fixtureEnv(t *testing.T, scope string) views.Env {
	t.Helper()
	alpha, beta := fixtures.Alpha(), fixtures.Beta()
	es := []engine.Engine{alpha, beta}
	for _, e := range es {
		if err := e.Start(t.Context(), nil, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	return views.Env{
		Engines: func() []engine.Engine { return es },
		Detections: func() []engine.Detection {
			return []engine.Detection{{Engine: alpha, Installed: true, Version: "v1.2.0"}, {Engine: beta, Installed: true, Version: "0.9.1"}}
		},
		ContextName: func() string { return "kind" },
		Contexts:    func() ([]model.KubeContext, error) { return []model.KubeContext{{Name: "kind", Current: true}}, nil },
		Namespaces:  func() ([]string, error) { return []string{"payments", "search"}, nil },
		Scope:       func() string { return scope },
		Now:         func() time.Time { return fixtures.Now },
	}
}

func golden(t *testing.T, got string) {
	t.Helper()
	p := filepath.Join("testdata", t.Name()+".golden")
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s:\n--- got\n%s\n--- want\n%s", p, got, want)
	}
}

func get(t *testing.T, env views.Env, sorts views.Sorts, out, filter string, args ...string) string {
	t.Helper()
	var b bytes.Buffer
	if err := printView(&b, env, sorts, args[0], strings.Join(args[1:], " "), &getFlags{output: out, filter: filter}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestGetPoliciesTable(t *testing.T) {
	golden(t, get(t, fixtureEnv(t, ""), nil, "table", "", "pol"))
}

func TestGetViolationsInNamespace(t *testing.T) {
	golden(t, get(t, fixtureEnv(t, "payments"), nil, "table", "", "v"))
}

func TestGetFilter(t *testing.T) {
	golden(t, get(t, fixtureEnv(t, ""), nil, "table", "engine:alpha mode:enforce", "pol"))
}

func TestGetHealthDashboard(t *testing.T) {
	golden(t, get(t, fixtureEnv(t, ""), nil, "table", "", "health"))
}

func TestGetOverview(t *testing.T) {
	golden(t, get(t, fixtureEnv(t, ""), nil, "table", "", "overview"))
}

func TestGetImpactWithArgument(t *testing.T) {
	golden(t, get(t, fixtureEnv(t, ""), nil, "table", "", "impact", "require-owner"))
}

func TestGetJSONIsTheTableSchema(t *testing.T) {
	out := get(t, fixtureEnv(t, ""), nil, "json", "", "pol")
	var tbl struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Rows          []any  `json:"rows"`
		Sort          struct {
			Column int  `json:"column"`
			Desc   bool `json:"desc"`
		} `json:"sort"`
	}
	if err := json.Unmarshal([]byte(out), &tbl); err != nil {
		t.Fatal(err)
	}
	if tbl.SchemaVersion != "p9s/v1" || tbl.Command != "pol" || len(tbl.Rows) != 4 || !tbl.Sort.Desc {
		t.Errorf("json = %+v", tbl)
	}
}

func TestGetSortFromConfig(t *testing.T) {
	sorts := views.CanonicalSorts(map[string]engine.Sort{"policies": {Role: engine.RoleName}})
	golden(t, get(t, fixtureEnv(t, ""), sorts, "table", "", "pol"))
}

func TestGetUnknownView(t *testing.T) {
	var b bytes.Buffer
	err := printView(&b, fixtureEnv(t, ""), nil, "nope", "", &getFlags{output: "table"})
	if err == nil || !strings.Contains(err.Error(), "see p9s get --help") || strings.Contains(err.Error(), "press ?") {
		t.Errorf("err = %v", err)
	}
}

func TestFlagsOverrideConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfg, []byte("startView: pol\nengines: kyverno\nsyncTimeout: 5s\nsort:\n  v: severity\n"), 0o600)
	kc := filepath.Join(dir, "kubeconfig")
	_ = os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\ncurrent-context: \"\"\n"), 0o600)
	f := &flags{}
	cmd := newRootCommand(f)
	if err := cmd.ParseFlags([]string{"--config", cfg, "--kubeconfig", kc, "-c", "v"}); err != nil {
		t.Fatal(err)
	}
	st, err := resolveSettings(cmd, f)
	if err != nil {
		t.Fatal(err)
	}
	if st.command != "v" || st.engines != "kyverno" || st.syncTimeout != 5*time.Second {
		t.Errorf("settings = %+v: file fills unset flags, -c wins over startView", st)
	}
	if s, ok := st.sorts["v"]; !ok || s.Role != engine.RoleSeverity {
		t.Errorf("sorts = %+v", st.sorts)
	}
}

func TestExplicitConfigMustExist(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "pol", "--config", filepath.Join(t.TempDir(), "none.yaml")})
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "config") {
		t.Errorf("err = %v", err)
	}
}

func TestGetFilterMatchingNothing(t *testing.T) {
	env := fixtureEnv(t, "")
	golden(t, get(t, env, nil, "table", "name:nothing-like-this", "pol"))
	out := get(t, env, nil, "json", "name:nothing-like-this", "pol")
	if !strings.Contains(out, `"rows": []`) || !strings.Contains(out, "No rows match") {
		t.Errorf("json for no matches must have rows [] and say why:\n%s", out)
	}
}

func TestGetFilterRejectedOnDashboards(t *testing.T) {
	for _, v := range []string{"overview", "health"} {
		var b bytes.Buffer
		err := printView(&b, fixtureEnv(t, ""), nil, v, "", &getFlags{output: "table", filter: "engine:alpha"})
		if err == nil || !strings.Contains(err.Error(), "dashboard") {
			t.Errorf("%s --filter: err = %v", v, err)
		}
	}
}

func writeFiles(t *testing.T, cfg string) (cfgPath, kubeconfig string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o600)
	kubeconfig = filepath.Join(dir, "kubeconfig")
	_ = os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: dev
clusters: [{name: c, cluster: {server: "https://127.0.0.1:1"}}]
users: [{name: u, user: {token: t}}]
contexts:
- {name: dev, context: {cluster: c, user: u}}
- {name: prod, context: {cluster: c, user: u}}
`), 0o600)
	return cfgPath, kubeconfig
}

func settingsFor(t *testing.T, cfg string, args ...string) (settings, error) {
	t.Helper()
	cfgPath, kc := writeFiles(t, cfg)
	f := &flags{}
	root := newRootCommand(f)
	all := append([]string{"--config", cfgPath, "--kubeconfig", kc}, args...)
	// Parse as cobra does for "p9s <root flags> get": persistent flags are
	// merged into the subcommand's flag set.
	get, _, err := root.Find([]string{"get"})
	if err != nil {
		t.Fatal(err)
	}
	if err := get.ParseFlags(all); err != nil {
		t.Fatal(err)
	}
	return resolveSettings(get, f)
}

func TestContextSectionFollowsCurrentAndExplicitContext(t *testing.T) {
	const cfg = "engines: auto\ncontexts:\n  dev: {engines: kyverno}\n  prod: {engines: gatekeeper}\n"
	st, err := settingsFor(t, cfg)
	if err != nil || st.engines != "kyverno" {
		t.Errorf("current-context dev: engines = %q, %v", st.engines, err)
	}
	st, err = settingsFor(t, cfg, "--context", "prod")
	if err != nil || st.engines != "gatekeeper" {
		t.Errorf("--context prod: engines = %q, %v", st.engines, err)
	}
	st, err = settingsFor(t, cfg, "--context", "prod", "--engine", "auto")
	if err != nil || st.engines != "auto" {
		t.Errorf("an explicit --engine (even its default value) wins: %q, %v", st.engines, err)
	}
}

func TestSortConfigValidation(t *testing.T) {
	if _, err := settingsFor(t, "sort:\n  polcies: name\n"); err == nil || !strings.Contains(err.Error(), "sort.polcies: unknown view") {
		t.Errorf("typo: err = %v", err)
	}
	if _, err := settingsFor(t, "sort:\n  v: name\n  violations: age\n"); err == nil || !strings.Contains(err.Error(), "name the same view") {
		t.Errorf("alias collision: err = %v", err)
	}
	st, err := settingsFor(t, "sort:\n  gk/c: name\n")
	if err != nil || st.sorts["gk/c"].Role != engine.RoleName {
		t.Errorf("engine view sorts are allowed: %+v, %v", st.sorts, err)
	}
}

func TestNegativeSyncTimeoutIsAnError(t *testing.T) {
	cfgPath, kc := writeFiles(t, "")
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"get", "pol", "--config", cfgPath, "--kubeconfig", kc, "--sync-timeout", "-1s"})
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Errorf("err = %v", err)
	}
}

func TestRootFlagsStillParseForTheTUI(t *testing.T) {
	f := &flags{}
	cmd := newRootCommand(f)
	if err := cmd.ParseFlags([]string{"--context", "x", "--engine", "kyverno", "-c", "pol"}); err != nil {
		t.Fatal(err)
	}
	if f.context != "x" || f.engines != "kyverno" || f.command != "pol" {
		t.Errorf("flags = %+v", f)
	}
}

func TestSnapshotFlagErrors(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) error {
		cmd := NewRootCommand()
		cmd.SetArgs(append(args, "--config", filepath.Join(dir, "none.yaml")))
		cmd.SilenceErrors = true
		cmd.SetOut(&bytes.Buffer{})
		return cmd.Execute()
	}
	_ = os.WriteFile(filepath.Join(dir, "none.yaml"), nil, 0o600)
	if err := run("get", "pol", "--snapshot", filepath.Join(dir, "missing.json")); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("missing snapshot: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"schemaVersion":"other"}`), 0o600)
	if err := run("get", "pol", "--snapshot", filepath.Join(dir, "bad.json")); err == nil || !strings.Contains(err.Error(), "p9s/snapshot/v1") {
		t.Errorf("wrong schema: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "ok.json"), []byte(`{"schemaVersion":"p9s/snapshot/v1","context":{"Name":"prod"}}`), 0o600)
	if err := run("get", "pol", "--snapshot", filepath.Join(dir, "ok.json"), "--context", "dev"); err == nil || !strings.Contains(err.Error(), `holds context "prod"`) {
		t.Errorf("context mismatch: %v", err)
	}
	if err := run("snapshot", "-o", filepath.Join(dir, "x.json"), "--snapshot", filepath.Join(dir, "ok.json")); err == nil || !strings.Contains(err.Error(), "drop --snapshot") {
		t.Errorf("snapshot of a snapshot: %v", err)
	}
	if err := run("snapshot"); err == nil || !strings.Contains(err.Error(), "output") {
		t.Errorf("snapshot without -o: %v", err)
	}
}

func TestReportFailOnLevels(t *testing.T) {
	crit := views.ReportSet{Reports: []views.Report{{Header: views.ReportHeader{Context: "c"}, Findings: []views.Finding{{Severity: "critical"}}}}}
	warn := views.ReportSet{Reports: []views.Report{{Findings: []views.Finding{{Severity: "warning"}}}}}
	var exit *ExitError
	if err := failOn("critical", crit); !errors.As(err, &exit) || exit.Code != 3 {
		t.Errorf("critical finding, --fail-on critical: %v", err)
	}
	if err := failOn("critical", warn); err != nil {
		t.Errorf("warning only, --fail-on critical: %v", err)
	}
	if err := failOn("warning", warn); !errors.As(err, &exit) || exit.Code != 3 {
		t.Errorf("warning, --fail-on warning: %v", err)
	}
	if err := failOn("", crit); err != nil {
		t.Errorf("no --fail-on: %v", err)
	}
}

func TestReportFlagValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	_ = os.WriteFile(cfg, nil, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "old.json"), []byte(`{"schemaVersion":"p9s/v1"}`), 0o600)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"report", "--format", "pdf"}, "want md, html or json"},
		{[]string{"report", "--fail-on", "info"}, "want critical or warning"},
		{[]string{"report", "--since", filepath.Join(dir, "old.json")}, "write it with --format json"},
		{[]string{"report", "--all-contexts", "--context", "a"}, "not both"},
	} {
		cmd := NewRootCommand()
		cmd.SetArgs(append(tt.args, "--config", cfg))
		cmd.SilenceErrors = true
		cmd.SetOut(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestReportAllContextsKeepsGoingAndExits1(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	_ = os.WriteFile(cfg, nil, 0o600)
	kc := filepath.Join(dir, "kubeconfig")
	_ = os.WriteFile(kc, []byte(`apiVersion: v1
kind: Config
current-context: a
clusters: [{name: c, cluster: {server: "https://127.0.0.1:1"}}]
users: [{name: u, user: {token: t}}]
contexts:
- {name: a, context: {cluster: c, user: u}}
- {name: b, context: {cluster: c, user: u}}
`), 0o600)
	out := filepath.Join(dir, "r.json")
	_ = os.WriteFile(out, []byte("old"), 0o644)
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"report", "--all-contexts", "--format", "json", "-o", out, "--kubeconfig", kc, "--config", cfg, "--sync-timeout", "1s"})
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "2 context(s) failed") {
		t.Fatalf("err = %v", err)
	}
	b, _ := os.ReadFile(out)
	var set views.ReportSet
	if json.Unmarshal(b, &set) != nil || len(set.Reports) != 2 || set.Reports[0].Error == "" || set.Reports[1].Header.Context != "b" {
		t.Errorf("report = %s: both contexts listed with their errors", b)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v: want 0600 even over an existing 0644 file", st.Mode().Perm())
	}
}

// Bad config values are errors, not silently ignored (docs/cli.md).
func TestConfigStartViewAndSortColumnsAreChecked(t *testing.T) {
	if _, err := settingsFor(t, "startView: nosuchview\n"); err == nil || !strings.Contains(err.Error(), `startView: "nosuchview" is not a view`) {
		t.Errorf("startView typo: err = %v", err)
	}
	for _, ok := range []string{"startView: v\n", "startView: impact require-team-label\n", "startView: gk/c\n"} {
		if _, err := settingsFor(t, ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if _, err := settingsFor(t, "sort:\n  mut: violations\n"); err == nil || !strings.Contains(err.Error(), "sort.mut: the view has no violations column") {
		t.Errorf("sort on a missing column: err = %v", err)
	}
	if _, err := settingsFor(t, "sort:\n  v: severity\n  pol: severity\n"); err != nil {
		t.Errorf(":v and :pol have a SEVERITY column: %v", err)
	}
}

// A filter typo fails instead of printing "No rows match" with exit 0.
func TestGetFilterTypoIsAnError(t *testing.T) {
	var b, e bytes.Buffer
	err := printView(&b, fixtureEnv(t, ""), nil, "pol", "", &getFlags{output: "table", filter: "mode:audti", stderr: &e})
	if err == nil || !strings.Contains(err.Error(), "mode:audti matches nothing") {
		t.Errorf("mode typo: err = %v", err)
	}
	err = printView(&b, fixtureEnv(t, ""), nil, "pol", "", &getFlags{output: "table", filter: "foo:bar", stderr: &e})
	if err != nil || !strings.Contains(e.String(), `"foo:" is not a filter key`) {
		t.Errorf("unknown key: err = %v, stderr %q", err, e.String())
	}
}

func TestGetViolationLayouts(t *testing.T) {
	env := fixtureEnv(t, "")
	var b bytes.Buffer
	if err := printView(&b, env, nil, "v", "", &getFlags{output: "table", flat: true}); err != nil || !strings.Contains(b.String(), "TARGET") {
		t.Errorf("--flat: %v\n%s", err, b.String())
	}
	if err := printView(&b, env, nil, "pol", "", &getFlags{output: "table", flat: true}); err == nil || !strings.Contains(err.Error(), "applies to v") {
		t.Errorf("--flat on pol: %v", err)
	}
	if err := printView(&b, env, nil, "health", "gatekeeper", &getFlags{output: "table"}); err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("health with an argument: %v", err)
	}
}

// The p9s/v1 JSON shape is pinned here (docs/cli.md "JSON output"): camelCase
// policy keys, unknown counts marked, not 0.
func TestGetPoliciesJSONSchema(t *testing.T) {
	out := get(t, fixtureEnv(t, ""), nil, "json", "", "pol")
	for _, want := range []string{`"schemaVersion": "p9s/v1"`, `"engine": "alpha"`, `"unknown": true`} {
		if !strings.Contains(out, want) {
			t.Errorf("json missing %s", want)
		}
	}
	if strings.Contains(out, `"Engine"`) {
		t.Error("policy keys must use camelCase JSON names")
	}
	golden(t, out)
}
