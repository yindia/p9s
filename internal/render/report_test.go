package render_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/render"
	"github.com/yindia/p9s/internal/views"
)

var update = flag.Bool("update", false, "rewrite golden files")

func sample() views.ReportSet {
	k := model.PolicyKey{Engine: "kyverno", Kind: "ValidatingPolicy", Name: "disallow-host-path"}
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	r := views.Report{
		Header: views.ReportHeader{Cluster: "prod", Context: "prod-tokyo-1", GeneratedAt: at, P9sVersion: "v1.0.0",
			Engines: []views.ReportEngine{{Name: "kyverno", Version: "v1.19.2"}}},
		Summary: views.ReportSummary{PoliciesByMode: map[string]int{"audit": 2, "enforce": 1}, Violations: 7, UnknownPolicies: 1,
			Health: query.Overall{Critical: 1, Warning: 0, OK: 9}},
		Violations: views.ReportViolations{Truncated: true},
		Items: []views.ReportViolation{
			{Engine: "kyverno", Action: "audit", Policy: k, Target: model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"},
				Message: "hostPath volumes are forbidden", ID: "a"},
			{Engine: "kyverno", Action: "audit", Policy: k, Target: model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "payments", Name: "api-1"},
				Message: "hostPath volumes are forbidden", ID: "b"},
		},
		Findings: []views.Finding{{Panel: "Webhooks", Engine: "kyverno", Check: "validate.kyverno.svc-fail", Status: "✗ fail-open, no endpoints", Severity: "critical"}},
		Since: &views.ReportDiff{Previous: at.Add(-24 * time.Hour), Comparable: true,
			NewViolations: []views.ReportViolation{{ID: "x"}}, EnforcementChanged: []views.ModeChange{{Policy: k, From: "audit", To: "enforce"}}},
		Omitted: []string{"Who can change policy: arrives with the RBAC phase."},
		Notices: []string{"kyverno: forbidden to list clusterpolicies.kyverno.io | data from it is missing"},
	}
	_ = r.Seal()
	return views.ReportSet{SchemaVersion: views.ReportSchemaVersion, Reports: []views.Report{r}}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		_ = os.WriteFile(p, []byte(got), 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n%s", name, got)
	}
}

func TestReportMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := render.Markdown(&b, sample()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `forbidden to list clusterpolicies.kyverno.io \| data`) {
		t.Error("pipes in text must be escaped for Markdown tables")
	}
	golden(t, "report.md", b.String())
}

func TestReportHTMLIsSelfContained(t *testing.T) {
	var b bytes.Buffer
	if err := render.HTML(&b, sample()); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	for _, bad := range []string{"<script", "<link", "src=", "@import", "http://", "https://"} {
		if strings.Contains(html, bad) {
			t.Errorf("HTML must be self-contained, found %q", bad)
		}
	}
	for _, want := range []string{"prod-tokyo-1", "Pod/payments/api-0", "hostPath volumes are forbidden", "fail-open", "<td>audit</td><td>enforce</td>", "<style>"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	golden(t, "report.html", html)
}

func TestMarkdownEscapesInjectedText(t *testing.T) {
	set := sample()
	set.Reports[0].Notices = []string{`<img src=x onerror=alert(1)> [link](http://evil) **bold** a|b`}
	set.Reports[0].Header.Context = "ctx<script>"
	var b bytes.Buffer
	_ = render.Markdown(&b, set)
	out := b.String()
	for _, bad := range []string{"<img", "<script", "**bold**"} {
		if strings.Contains(out, bad) {
			t.Errorf("unescaped %q in:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, `\[link\]`) {
		t.Errorf("link brackets not escaped:\n%s", out)
	}
}

func TestFailedContextRendersItsError(t *testing.T) {
	set := views.ReportSet{SchemaVersion: views.ReportSchemaVersion, Reports: []views.Report{{Error: "connect: dial tcp: timeout", Header: views.ReportHeader{Context: "dead"}}}}
	var md, html bytes.Buffer
	if err := render.Markdown(&md, set); err != nil || !strings.Contains(md.String(), "could not be reported on") {
		t.Errorf("markdown: %v\n%s", err, md.String())
	}
	if err := render.HTML(&html, set); err != nil || !strings.Contains(html.String(), "dial tcp") {
		t.Errorf("html: %v", err)
	}
}
