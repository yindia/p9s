package render

import (
	"fmt"
	"html/template"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/views"
)

// Markdown writes a report set as Markdown.
func Markdown(w io.Writer, set views.ReportSet) error {
	var b strings.Builder
	for i, r := range set.Reports {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		markdownReport(&b, r)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func markdownReport(b *strings.Builder, r views.Report) {
	h := r.Header
	fmt.Fprintf(b, "# p9s policy report: %s\n\n", md(h.Context))
	if r.Error != "" {
		fmt.Fprintf(b, "> **This context could not be reported on:** %s\n", md(r.Error))
		return
	}
	fmt.Fprintf(b, "| | |\n|---|---|\n| Cluster | %s |\n| Context | %s |\n| Engines | %s |\n| Generated | %s |\n",
		md(h.Cluster), md(h.Context), md(engines(h)), h.GeneratedAt.Format(time.RFC3339))
	if h.Snapshot != nil {
		fmt.Fprintf(b, "| Snapshot | %s |\n", h.Snapshot.Format(time.RFC3339))
	}
	fmt.Fprintf(b, "| p9s | %s |\n| Body SHA-256 | `%s` |\n\n", md(h.P9sVersion), h.BodySHA256)
	for _, n := range r.Notices {
		fmt.Fprintf(b, "> **Notice:** %s\n\n", md(n))
	}

	s := r.Summary
	b.WriteString("## 1. Summary\n\n")
	fmt.Fprintf(b, "- Policies by enforcement: %s\n", md(modes(s.PoliciesByMode)))
	fmt.Fprintf(b, "- Violations (engines' own counts): **%d**", s.Violations)
	if s.UnknownPolicies > 0 {
		if s.UnknownPolicies == 1 {
			b.WriteString(" (1 policy has no audit data and is not counted)")
		} else {
			fmt.Fprintf(b, " (%d policies have no audit data and are not counted)", s.UnknownPolicies)
		}
	}
	fmt.Fprintf(b, "\n- Health: %d critical, %d warning, %d ok", s.Health.Critical, s.Health.Warning, s.Health.OK)
	if s.Health.Unknown > 0 {
		fmt.Fprintf(b, ", %d unknown", s.Health.Unknown)
	}
	b.WriteString("\n\n")

	v := r.Violations
	fmt.Fprintf(b, "## 2. Violations (%d listed)\n\n", len(r.Items))
	if v.Truncated {
		b.WriteString("> Some engines list fewer violations than they count (Gatekeeper --constraint-violations-limit): this list is partial.\n\n")
	}
	if len(r.Items) == 0 {
		b.WriteString("No violations.\n\n")
	} else {
		b.WriteString("| Engine | Policy | Action | Target | Message |\n|---|---|---|---|---|\n")
		for _, it := range r.Items {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n", md(it.Engine), md(it.Policy.String()), md(dash(it.Action)), md(it.Target.String()), md(it.Message))
		}
		b.WriteString("\n")
	}

	b.WriteString("## 3. Fail-open and other health findings\n\n")
	if len(r.Findings) == 0 {
		b.WriteString("No critical or warning findings.\n\n")
	} else {
		b.WriteString("| Severity | Panel | Engine | Check | Status |\n|---|---|---|---|---|\n")
		for _, f := range r.Findings {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n", f.Severity, md(f.Panel), md(f.Engine), md(f.Check), md(f.Status))
		}
		b.WriteString("\n")
	}

	if d := r.Since; d != nil {
		fmt.Fprintf(b, "## 4. Changes since %s\n\n", d.Previous.Format(time.RFC3339))
		if !d.Comparable {
			b.WriteString("> One of the reports had capped violation lists: some new or resolved items may only have moved in or out of the cap.\n\n")
		}
		fmt.Fprintf(b, "- New violations: **%d**\n- Resolved violations: **%d**\n- Still failing with a new message: **%d**\n",
			len(d.NewViolations), len(d.ResolvedViolations), len(d.ChangedMessages))
		fmt.Fprintf(b, "- Policies added: **%d**, removed: **%d**\n- Enforcement changes: **%d**\n\n",
			len(d.AddedPolicies), len(d.RemovedPolicies), len(d.EnforcementChanged))
		for _, m := range d.EnforcementChanged {
			fmt.Fprintf(b, "  - %s: %s → %s\n", md(m.Policy.String()), md(m.From), md(m.To))
		}
		if len(d.EnforcementChanged) > 0 {
			b.WriteString("\n")
		}
	}
	b.WriteString("## Not included\n\n")
	for _, o := range r.Omitted {
		fmt.Fprintf(b, "- %s\n", md(o))
	}
}

func engines(h views.ReportHeader) string {
	parts := make([]string, 0, len(h.Engines))
	for _, e := range h.Engines {
		parts = append(parts, e.Name+" "+e.Version)
	}
	return dash(strings.Join(parts, ", "))
}

func modes(m map[string]int) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	parts := make([]string, 0, len(ks))
	for _, k := range ks {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return dash(strings.Join(parts, ", "))
}

func keys[K fmt.Stringer](ks []K) string {
	parts := make([]string, 0, len(ks))
	for _, k := range ks {
		parts = append(parts, k.String())
	}
	return strings.Join(parts, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// md escapes Markdown syntax and raw HTML: notices carry API server text.
func md(s string) string {
	return mdEscaper.Replace(s)
}

var mdEscaper = strings.NewReplacer(
	"\\", `\\`, "|", `\|`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
	"<", "&lt;", ">", "&gt;", "#", `\#`, "\n", " ", "\r", " ",
)

// HTML writes a report set as one self-contained page: inline CSS, no
// scripts, no external assets.
func HTML(w io.Writer, set views.ReportSet) error {
	return reportPage.Execute(w, set)
}

var reportPage = template.Must(template.New("report").Funcs(template.FuncMap{
	"time":    func(t time.Time) string { return t.Format(time.RFC3339) },
	"engines": engines,
	"modes":   modes,
	"keys":    func(ks []model.PolicyKey) string { return dash(keys(ks)) },
	"dash":    dash,
}).Parse(htmlTemplate))
