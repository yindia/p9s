package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/table"
)

// Health dashboard panel IDs.
const (
	PanelWebhooks    = "webhooks"
	PanelControllers = "controllers"
	PanelAudit       = "audit"
	PanelPolicies    = "policies"
)

// Dashboard is :health: graded panels built from the Kubernetes API.
type Dashboard struct {
	SchemaVersion string        `json:"schemaVersion"`
	Overall       query.Overall `json:"overall"`
	// UpdatedAt is when the data last changed.
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
	Panels    []Panel   `json:"panels"`
	// Notices are RBAC gaps that make panels partial.
	Notices []string `json:"notices,omitempty"`
	// Synced is false while caches load: grades are provisional.
	Synced bool `json:"synced"`
}

// Panel is one materialised panel.
type Panel struct {
	ID    string      `json:"id"`
	Title string      `json:"title"`
	Table table.Table `json:"table"`
}

// statusCell renders a check result: the reason when not ok.
func statusCell(s query.Status, reason string) engine.Cell {
	switch s {
	case query.Critical:
		return engine.Cell{Kind: engine.CellSeverity, Severity: model.SeverityError, Text: "✗ " + reason}
	case query.Warning:
		return engine.Cell{Kind: engine.CellSeverity, Severity: model.SeverityWarning, Text: "! " + reason}
	case query.Unknown:
		return engine.Cell{Kind: engine.CellText, Text: "? " + reason}
	default:
		return engine.Cell{Kind: engine.CellSeverity, Severity: model.SeverityInfo, Text: "✓"}
	}
}

// DashboardOf builds :health.
func DashboardOf(env Env) Dashboard {
	now := env.Now()
	es := env.Engines()
	var plat query.Platform
	d := Dashboard{SchemaVersion: table.SchemaVersion}
	if p := env.platform(); p != nil {
		plat = p.Snapshot()
		for _, ai := range p.AccessIssues() {
			d.Notices = append(d.Notices, fmt.Sprintf("platform: forbidden to %s %s — health panels are partial", ai.Verb, ai.Resource))
		}
	}
	if env.UpdatedAt != nil {
		d.UpdatedAt = env.UpdatedAt()
	}
	d.Synced = query.Synced(es) && env.platformSynced()
	synced := func(engine.Filter) bool { return d.Synced }
	empty := "Nothing to check."
	if len(d.Notices) > 0 {
		empty = "No data: see the notice above."
	}
	build := func(id, title string, v engine.View) Panel {
		v.Command, v.Title = CmdHealth, title
		tbl := table.Build(table.Source{View: v, Synced: synced,
			Empty: func(engine.Filter) string { return empty }}, engine.Filter{}, now)
		return Panel{ID: id, Title: title, Table: tbl}
	}

	wh := query.CheckWebhooks(plat, now)
	ct := query.CheckControllers(plat, now)
	au := query.CheckAudit(es, plat, now)
	po := query.CheckPolicies(es)
	for _, r := range wh {
		d.Overall.Add(r.Status)
	}
	for _, r := range ct {
		d.Overall.Add(r.Status)
	}
	for _, r := range au {
		d.Overall.Add(r.Status)
	}
	for _, r := range po {
		d.Overall.Add(r.Status)
	}
	d.Panels = []Panel{
		build(PanelWebhooks, "Webhooks", webhooksView(wh, plat, now)),
		build(PanelControllers, "Controllers", controllersView(ct)),
		build(PanelAudit, "Audit", auditView(env, au, now)),
		build(PanelPolicies, "Policies", policiesPanelView(po)),
	}
	return d
}

func rowsOf(rows []engine.Row) func(engine.Filter) ([]engine.Row, error) {
	return func(engine.Filter) ([]engine.Row, error) { return rows, nil }
}

func webhooksView(rows []query.WebhookRow, p query.Platform, now time.Time) engine.View {
	out := make([]engine.Row, 0, len(rows))
	for _, r := range rows {
		w := r.Webhook
		cert := "-"
		if r.Cert != nil {
			cert = query.ShortDuration(r.Cert.NotAfter.Sub(now))
		}
		endpoints := "-"
		switch {
		case w.Service != "" && !w.EndpointsKnown:
			endpoints = "?"
		case w.Service != "":
			endpoints = fmt.Sprintf("%d/%d", w.ReadyEndpoints(), len(w.Endpoints))
		}
		row := engine.Row{
			ID: w.Engine + "|" + w.Config + "|" + w.Name,
			Cells: []engine.Cell{
				engine.TextCell(w.Engine), engine.TextCell(w.Name), engine.TextCell(w.FailurePolicy),
				engine.TextCell(fmt.Sprintf("%ds", w.TimeoutSecs)), engine.TextCell(endpoints), engine.TextCell(cert),
				statusCell(r.Status, r.Reason),
			},
			Object: w.Raw,
			Detail: func() (engine.Detail, error) { return webhookDetail(r, p, now), nil },
		}
		// g goes to the backing pods: the first one (all are in the detail).
		for _, e := range w.Endpoints {
			if ns, name, ok := strings.Cut(e.Pod, "/"); ok {
				row.Ref = &model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: ns, Name: name}
				break
			}
		}
		out = append(out, row)
	}
	return engine.View{
		Columns: []engine.Column{
			engineColumn, {Title: "WEBHOOK", Role: engine.RoleName, Flex: true}, {Title: "FAILURE"}, {Title: "TIMEOUT"},
			{Title: "ENDPOINTS"}, {Title: "CERT"}, {Title: "STATUS", Role: engine.RoleSeverity},
		},
		Rows: rowsOf(out),
	}
}

// failureMeaning explains a failurePolicy in one line.
var failureMeaning = map[string]string{
	"Ignore": "on error or timeout the API server admits the request without this check (fail-open)",
	"Fail":   "on error or timeout the API server rejects the request (fail-closed)",
}

// impact says in plain language what the webhook's state means today.
func impact(r query.WebhookRow) string {
	w := r.Webhook
	ready := w.ReadyEndpoints()
	switch {
	case r.Cert != nil && r.Status == query.Critical && r.Reason == "cert expired":
		return "Matching requests skip this webhook or fail: the API server no longer trusts its certificate."
	case w.Service != "" && !w.EndpointsKnown:
		return "Endpoints could not be read (RBAC or still loading), so availability is unknown."
	case w.Service != "" && ready == 0 && w.FailurePolicy == "Ignore":
		return "Matching requests are admitted with NO policy check right now: the webhook has no ready endpoint and fails open."
	case w.Service != "" && ready == 0:
		return "Matching requests are rejected right now: the webhook has no ready endpoint and fails closed."
	case w.FailurePolicy == "Ignore":
		return "Working. If every endpoint goes away, matching requests will be admitted without checks."
	default:
		return "Working. If every endpoint goes away, matching requests will be rejected."
	}
}

func webhookDetail(r query.WebhookRow, p query.Platform, now time.Time) engine.Detail {
	w := r.Webhook
	service := table.Dash(w.Service)
	if w.URL != "" {
		service = w.URL
	}
	cert := "-"
	if r.Cert != nil {
		cert = fmt.Sprintf("%s, expires %s (in %s)", table.Dash(r.Cert.CommonName), r.Cert.NotAfter.UTC().Format("2006-01-02"), query.ShortDuration(r.Cert.NotAfter.Sub(now)))
	}
	summary := table.KV(
		"Config", fmt.Sprintf("%s (%s)", w.Config, w.Type), "Webhook", w.Name, "Engine", w.Engine,
		"Failure policy", w.FailurePolicy+" — "+failureMeaning[w.FailurePolicy],
		"Timeout", fmt.Sprintf("%ds", w.TimeoutSecs), "Service", service, "Certificate", cert,
	)
	phase := map[string]string{}
	for _, c := range p.Controllers {
		for _, ps := range c.Pods {
			phase[c.Namespace+"/"+ps.Name] = ps.Phase
		}
	}
	var eps strings.Builder
	switch {
	case w.Service != "" && !w.EndpointsKnown:
		eps.WriteString("(unknown: EndpointSlices not readable)")
	case len(w.Endpoints) == 0:
		eps.WriteString("(none)")
	}
	for _, e := range w.Endpoints {
		state := "not ready"
		if e.Ready {
			state = "ready"
		}
		pod := table.Dash(e.Pod)
		if ph := phase[e.Pod]; ph != "" {
			state += ", pod " + ph
		}
		fmt.Fprintf(&eps, "• %s — %s\n", pod, state)
	}
	scope := fmt.Sprintf("namespaceSelector:\n%s\nobjectSelector:\n%s", indentYAML(w.NamespaceSelector), indentYAML(w.ObjectSelector))
	return engine.Detail{Title: "Webhook " + w.Name, Sections: []engine.Section{
		{Title: "Impact", Body: impact(r)},
		{Title: "Summary", Body: summary},
		{Title: "Endpoints", Body: strings.TrimRight(eps.String(), "\n")},
		{Title: "Namespace scope", Body: scope, Language: "yaml"},
		{Title: "Rules", Body: yamlOr(w.Rules, "(none)"), Language: "yaml"},
	}}
}

func controllersView(rows []query.ControllerRow) engine.View {
	out := make([]engine.Row, 0, len(rows))
	for _, r := range rows {
		c := r.Controller
		ref := model.ObjectRef{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: c.Namespace, Name: c.Name}
		out = append(out, engine.Row{
			ID: c.Engine + "|" + c.Namespace + "/" + c.Name,
			Cells: []engine.Cell{
				engine.TextCell(c.Engine), engine.TextCell(c.Name), engine.TextCell(fmt.Sprintf("%d/%d", c.Ready, c.Desired)),
				engine.CountCell(c.Restarts), engine.TextCell(table.Dash(c.LastReason)), engine.TextCell(table.Dash(r.Tag)),
				statusCell(r.Status, r.Reason),
			},
			Ref: &ref, Object: c.Raw,
			Detail: func() (engine.Detail, error) { return controllerDetail(r), nil },
		})
	}
	return engine.View{
		Columns: []engine.Column{
			engineColumn, {Title: "DEPLOYMENT", Role: engine.RoleName, Flex: true}, {Title: "READY"},
			{Title: "RESTARTS", Role: engine.RoleCount}, {Title: "LAST REASON"}, {Title: "IMAGE"},
			{Title: "STATUS", Role: engine.RoleSeverity},
		},
		Rows: rowsOf(out),
	}
}

func controllerDetail(r query.ControllerRow) engine.Detail {
	c := r.Controller
	var pods strings.Builder
	for _, p := range c.Pods {
		ready := "not ready"
		if p.Ready {
			ready = "ready"
		}
		fmt.Fprintf(&pods, "• %s — %s, %s, %d restarts\n", p.Name, p.Phase, ready, p.Restarts)
	}
	last := "-"
	if !c.LastRestart.IsZero() {
		last = c.LastRestart.UTC().Format("2006-01-02 15:04:05Z") + " (" + table.Dash(c.LastReason) + ")"
	}
	return engine.Detail{Title: "Deployment " + c.Namespace + "/" + c.Name, Sections: []engine.Section{
		{Title: "Summary", Body: table.KV("Engine", c.Engine, "Ready", fmt.Sprintf("%d/%d", c.Ready, c.Desired),
			"Restarts", fmt.Sprint(c.Restarts), "Last termination", last, "Images", strings.Join(c.Images, ", "))},
		{Title: "Pods", Body: table.Dash(strings.TrimRight(pods.String(), "\n"))},
	}}
}

func auditView(env Env, rows []query.AuditRow, now time.Time) engine.View {
	out := make([]engine.Row, 0, len(rows))
	byName := map[string]engine.Engine{}
	for _, e := range env.Engines() {
		byName[e.Name()] = e
	}
	for _, r := range rows {
		interval := "-"
		if r.Interval > 0 {
			interval = query.ShortDuration(r.Interval)
		}
		last := "never"
		if !r.Never && !r.Last.IsZero() {
			last = query.ShortDuration(now.Sub(r.Last)) + " ago"
		}
		leader := "-"
		if r.Leader != nil {
			leader = r.Leader.Holder
		}
		e := byName[r.Engine]
		out = append(out, engine.Row{
			ID: r.Engine,
			Cells: []engine.Cell{
				engine.TextCell(r.Engine), engine.TextCell(r.Source), engine.TextCell(interval), engine.TextCell(last),
				engine.TextCell(leader), statusCell(r.Status, r.Reason),
			},
			Detail: func() (engine.Detail, error) { return auditDetail(r, e, now), nil },
		})
	}
	return engine.View{
		Columns: []engine.Column{
			engineColumn, {Title: "SOURCE", Role: engine.RoleName}, {Title: "INTERVAL"}, {Title: "LAST RUN"},
			{Title: "LEADER", Flex: true}, {Title: "STATUS", Role: engine.RoleSeverity},
		},
		Rows: rowsOf(out),
	}
}

func auditDetail(r query.AuditRow, e engine.Engine, now time.Time) engine.Detail {
	leader := "(no leader election for audit)"
	if r.Leader != nil {
		renewed := "-"
		if !r.Leader.Renewed.IsZero() {
			renewed = query.ShortDuration(now.Sub(r.Leader.Renewed)) + " ago"
		}
		leader = fmt.Sprintf("%s via Lease %s/%s, renewed %s", table.Dash(r.Leader.Holder), r.Leader.Namespace, r.Leader.Name, renewed)
	}
	d := engine.Detail{Title: "Audit — " + r.Engine, Sections: []engine.Section{
		{Title: "Summary", Body: table.KV("Source", r.Source, "Interval", table.Dash(intervalText(r.Interval)), "Leader", leader)},
	}}
	if e != nil {
		d.Sections = append(d.Sections, engine.Section{Title: "Audit issues", Body: issueList(query.AuditIssues(e))})
	}
	return d
}

func intervalText(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return query.ShortDuration(d)
}

func policiesPanelView(rows []query.PolicyRow) engine.View {
	out := make([]engine.Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, engine.Row{
			ID: r.Engine,
			Cells: []engine.Cell{
				engine.TextCell(r.Engine), engine.TextCell(fmt.Sprintf("%d/%d", r.Loaded, r.Total)),
				engine.CountCell(r.NotEnforced), engine.CountCell(r.CompileErrors), engine.CountCell(r.MissingCRD),
				engine.CountCell(r.NotReady), statusCell(r.Status, r.Reason),
			},
			Detail: func() (engine.Detail, error) {
				return engine.Detail{Title: "Policies — " + r.Engine, Sections: []engine.Section{
					{Title: "Issues", Body: issueList(r.Issues)},
				}}, nil
			},
			Drill: &engine.Target{Command: CmdPolicies, Filter: engine.Filter{Engine: r.Engine}},
		})
	}
	return engine.View{
		Columns: []engine.Column{
			engineColumn, {Title: "LOADED"}, {Title: "NOT ENFORCED", Role: engine.RoleCount}, {Title: "COMPILE ERRORS"},
			{Title: "MISSING CRD"}, {Title: "NOT READY"}, {Title: "STATUS", Role: engine.RoleSeverity, Flex: true},
		},
		Rows: rowsOf(out),
	}
}

func issueList(hs []model.HealthIssue) string {
	if len(hs) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, h := range hs {
		fmt.Fprintf(&b, "• [%s] %s %s\n  %s\n", h.Severity, h.Category, refText(h.Object), h.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

func indentYAML(v any) string {
	if v == nil {
		return "  (all)"
	}
	return "  " + strings.ReplaceAll(table.YAML(v), "\n", "\n  ")
}

func yamlOr(v any, empty string) string {
	if v == nil {
		return empty
	}
	return table.YAML(v)
}
