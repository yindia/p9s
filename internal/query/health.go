package query

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
)

// Status grades one health check.
type Status string

// Statuses, worst first.
const (
	Critical Status = "critical"
	Warning  Status = "warning"
	// Unknown: the data needed to grade is unreadable (RBAC) or still
	// loading. Never graded as ok or as a failure.
	Unknown Status = "unknown"
	OK      Status = "ok"
)

// Health check thresholds.
const (
	CertWarnWithin = 14 * 24 * time.Hour
	RestartWindow  = time.Hour
)

// Platform is the API-side state of the engines' moving parts.
type Platform struct {
	Webhooks    []Webhook
	Controllers []Controller
	Leases      []Lease
}

// Webhook is one webhook of a (Validating|Mutating)WebhookConfiguration.
type Webhook struct {
	Engine        string
	Type          string // "validating" or "mutating"
	Config        string
	Name          string
	FailurePolicy string
	TimeoutSecs   int
	// Service is "namespace/name"; empty for URL webhooks.
	Service   string
	URL       string
	Endpoints []Endpoint
	// EndpointsKnown is false when the service's EndpointSlices could not be
	// read or are still loading; Endpoints is then meaningless.
	EndpointsKnown bool
	// CABundle is the PEM bundle the API server trusts for this webhook.
	CABundle          []byte
	NamespaceSelector any
	ObjectSelector    any
	Rules             any
	Raw               *unstructured.Unstructured
}

// Endpoint is one backend of a webhook service.
type Endpoint struct {
	Pod   string // namespace/name, if the EndpointSlice names it
	Ready bool
}

// ReadyEndpoints counts ready backends.
func (w Webhook) ReadyEndpoints() int {
	n := 0
	for _, e := range w.Endpoints {
		if e.Ready {
			n++
		}
	}
	return n
}

// Cert is the longest-lived certificate of a CA bundle.
type Cert struct {
	CommonName string
	NotAfter   time.Time
}

// ParseBundle returns the certificate that expires last. A caBundle is a
// trust pool: during CA rotation it holds the old and the new CA, and the
// webhook keeps working until the last one expires. ok is false for empty
// or unparseable bundles.
func ParseBundle(pemBytes []byte) (Cert, bool) {
	var best Cert
	found := false
	for rest := pemBytes; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if !found || c.NotAfter.After(best.NotAfter) {
			best, found = Cert{CommonName: c.Subject.CommonName, NotAfter: c.NotAfter}, true
		}
	}
	return best, found
}

// Controller is one of an engine's Deployments.
type Controller struct {
	Engine, Namespace, Name string
	Ready, Desired          int
	Restarts                int
	// LastReason is the most recent container termination reason.
	LastReason  string
	LastRestart time.Time
	Images      []string
	Pods        []PodState
	Raw         *unstructured.Unstructured
}

// PodState summarises one controller pod.
type PodState struct {
	Name, Phase string
	Ready       bool
	Restarts    int
}

// Lease is a leader-election lease.
type Lease struct {
	Engine, Namespace, Name, Holder string
	Renewed                         time.Time
}

// Row is one graded line of a health panel.
type Row struct {
	Engine string
	Name   string
	Status Status
	Reason string
}

// WebhookRow is a graded webhook.
type WebhookRow struct {
	Row
	Webhook Webhook
	Cert    *Cert
}

// CheckWebhooks grades every webhook:
//   - critical: failurePolicy Ignore with no ready endpoints. The API server
//     silently skips the webhook, so policy is bypassed (fail-open). Also an
//     expired CA bundle (the API server cannot reach the webhook).
//   - warning: failurePolicy Fail with no ready endpoints (matching requests
//     are rejected: admission is blocked), or a CA expiring in under 14 days.
//   - unknown: endpoints could not be read; never guessed as "none".
func CheckWebhooks(p Platform, now time.Time) []WebhookRow {
	out := make([]WebhookRow, 0, len(p.Webhooks))
	for _, w := range p.Webhooks {
		r := WebhookRow{Row: Row{Engine: w.Engine, Name: w.Name, Status: OK}, Webhook: w}
		if c, ok := ParseBundle(w.CABundle); ok {
			r.Cert = &c
		}
		ready := w.ReadyEndpoints()
		hasService := w.Service != ""
		switch {
		case r.Cert != nil && !r.Cert.NotAfter.After(now):
			r.Status, r.Reason = Critical, "cert expired"
		case hasService && !w.EndpointsKnown:
			r.Status, r.Reason = Unknown, "endpoints unknown"
		case hasService && ready == 0 && w.FailurePolicy == "Ignore":
			r.Status, r.Reason = Critical, "fail-open, no endpoints"
		case hasService && ready == 0:
			r.Status, r.Reason = Warning, "no endpoints: admission blocked"
		case r.Cert != nil && r.Cert.NotAfter.Sub(now) < CertWarnWithin:
			r.Status, r.Reason = Warning, "cert expires in "+shortDuration(r.Cert.NotAfter.Sub(now))
		}
		out = append(out, r)
	}
	sortRows(out, func(r WebhookRow) Row { return r.Row })
	return out
}

// ControllerRow is a graded controller.
type ControllerRow struct {
	Row
	Controller Controller
	Tag        string
}

// CheckControllers grades every engine Deployment:
//   - critical: no ready replica (while some are desired)
//   - warning: a container restarted in the last hour, or this Deployment's
//     image tag differs from the rest of its engine (a half-finished upgrade).
func CheckControllers(p Platform, now time.Time) []ControllerRow {
	// majority tag per engine; ties keep no majority (every odd one warns).
	counts := map[string]map[string]int{}
	for _, c := range p.Controllers {
		if counts[c.Engine] == nil {
			counts[c.Engine] = map[string]int{}
		}
		if t := imageTag(c.Images); t != "" {
			counts[c.Engine][t]++
		}
	}
	majority := map[string]string{}
	for e, byTag := range counts {
		best, n, tie := "", 0, false
		for t, c := range byTag {
			switch {
			case c > n:
				best, n, tie = t, c, false
			case c == n:
				tie = true
			}
		}
		if !tie {
			majority[e] = best
		}
	}
	out := make([]ControllerRow, 0, len(p.Controllers))
	for _, c := range p.Controllers {
		r := ControllerRow{Row: Row{Engine: c.Engine, Name: c.Name, Status: OK}, Controller: c, Tag: imageTag(c.Images)}
		switch {
		case c.Desired > 0 && c.Ready == 0:
			r.Status, r.Reason = Critical, "not ready"
		case !c.LastRestart.IsZero() && now.Sub(c.LastRestart) < RestartWindow:
			r.Status, r.Reason = Warning, "restarted "+shortDuration(now.Sub(c.LastRestart))+" ago"
		case len(counts[c.Engine]) > 1 && r.Tag != "" && r.Tag != majority[c.Engine]:
			r.Status, r.Reason = Warning, "image tag differs from other "+c.Engine+" deployments"
		}
		out = append(out, r)
	}
	sortRows(out, func(r ControllerRow) Row { return r.Row })
	return out
}

func imageTag(images []string) string {
	for _, img := range images {
		ref := img
		if i := strings.Index(ref, "@"); i >= 0 {
			ref = ref[:i]
		}
		if c := strings.LastIndex(ref, ":"); c > strings.LastIndex(ref, "/") {
			return ref[c+1:]
		}
	}
	return ""
}

// AuditRow is one engine's audit freshness.
type AuditRow struct {
	Row
	Source   string
	Interval time.Duration
	Last     time.Time
	Never    bool
	Leader   *Lease
}

// CheckAudit grades each platform engine's audit: warning when the last run
// is older than 2× its interval, or there has been none.
func CheckAudit(es []engine.Engine, p Platform, now time.Time) []AuditRow {
	audits := map[string]Audit{}
	for _, a := range Audits(es, now) {
		audits[a.Engine] = a
	}
	var out []AuditRow
	for _, e := range es {
		pl, ok := e.(engine.Platform)
		if !ok {
			continue
		}
		info := pl.PlatformInfo()
		a, audited := audits[e.Name()]
		r := AuditRow{Row: Row{Engine: e.Name(), Name: info.AuditSource, Status: OK}, Source: info.AuditSource}
		if au, ok := e.(engine.Auditor); ok {
			r.Interval = au.AuditInterval()
		}
		r.Last, r.Never = a.Last, !audited || a.Never
		for i := range p.Leases {
			if l := p.Leases[i]; l.Engine == e.Name() && l.Name == info.AuditLease {
				r.Leader = &p.Leases[i]
			}
		}
		errs := 0
		for _, h := range AuditIssues(e) {
			if h.Severity == model.SeverityError {
				errs++
			}
		}
		switch {
		case !e.SyncStatus().Synced:
			r.Status, r.Reason = Unknown, "syncing"
		case errs > 0:
			r.Status, r.Reason = Warning, fmt.Sprintf("%d audit %s", errs, plural(errs, "error", "errors"))
		case r.Never && r.Interval > 0:
			r.Status, r.Reason = Warning, "no audit result yet"
		case r.Interval > 0 && now.Sub(r.Last) > time.Duration(StaleWarnRatio*float64(r.Interval)):
			r.Status, r.Reason = Warning, "stale (> 2× interval)"
		}
		out = append(out, r)
	}
	sortRows(out, func(r AuditRow) Row { return r.Row })
	return out
}

// PolicyRow is one engine's policy load state.
type PolicyRow struct {
	Row
	engine.PolicyStatus
	// Issues are the engine's health items about policies (compile errors,
	// missing CRDs, not ready), shown in the row's detail.
	Issues []model.HealthIssue
}

// policyCategories are the engine health categories the Policies panel
// absorbs; the rest belong to the Audit panel (see AuditIssues).
var policyCategories = map[string]bool{"compile": true, "template": true, "policy": true, "discovery": true, "constraint": true,
	"bindings": true, "params": true, "type checking": true}

// criticalCategories escalate the Policies row to critical: a binding to a
// missing policy enforces nothing, a missing param with Deny rejects every
// matching request. Other inactive policies stay warnings.
var criticalCategories = map[string]bool{"bindings": true, "params": true}

// CheckPolicies grades each engine that reports PolicyStatus: warning when
// any policy is not active (not enforced everywhere, compile error, missing
// CRD, not ready).
func CheckPolicies(es []engine.Engine) []PolicyRow {
	var out []PolicyRow
	for _, e := range es {
		ps, ok := e.(engine.PolicyStatuser)
		if !ok {
			continue
		}
		st := ps.PolicyStatus()
		r := PolicyRow{Row: Row{Engine: e.Name(), Name: "policies", Status: OK}, PolicyStatus: st}
		hs, err := e.Health()
		critical := ""
		for _, h := range hs {
			if policyCategories[h.Category] {
				r.Issues = append(r.Issues, h)
				if h.Severity == model.SeverityError && criticalCategories[h.Category] && critical == "" {
					critical = h.Message
				}
			}
		}
		bad := st.NotEnforced + st.CompileErrors + st.MissingCRD + st.NotReady
		switch {
		case !e.SyncStatus().Synced:
			r.Status, r.Reason = Unknown, "syncing"
		case err != nil:
			r.Status, r.Reason = Unknown, "health unreadable: "+err.Error()
		case critical != "":
			r.Status, r.Reason = Critical, critical
		case bad > 0:
			r.Status, r.Reason = Warning, fmt.Sprintf("%d %s not active", bad, plural(bad, "policy", "policies"))
		}
		out = append(out, r)
	}
	sortRows(out, func(r PolicyRow) Row { return r.Row })
	return out
}

// AuditIssues are an engine's health items about audit (never audited,
// stale or orphaned report results), shown in the Audit row's detail.
func AuditIssues(e engine.Engine) []model.HealthIssue {
	hs, err := e.Health()
	if err != nil {
		return nil
	}
	var out []model.HealthIssue
	for _, h := range hs {
		if !policyCategories[h.Category] {
			out = append(out, h)
		}
	}
	return out
}

// Overall counts statuses across all rows.
type Overall struct {
	Critical, Warning, Unknown, OK int
}

// Add counts one row.
func (o *Overall) Add(s Status) {
	switch s {
	case Critical:
		o.Critical++
	case Warning:
		o.Warning++
	case Unknown:
		o.Unknown++
	default:
		o.OK++
	}
}

var statusRank = map[Status]int{Critical: 0, Warning: 1, Unknown: 2, OK: 3}

// sortRows orders worst first, then by engine and name.
func sortRows[T any](rows []T, row func(T) Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := row(rows[i]), row(rows[j])
		if statusRank[a.Status] != statusRank[b.Status] {
			return statusRank[a.Status] < statusRank[b.Status]
		}
		if a.Engine != b.Engine {
			return a.Engine < b.Engine
		}
		return a.Name < b.Name
	})
}

// shortDuration renders a duration as "9d", "3h", "12m" or "40s".
func shortDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ShortDuration exposes the compact duration format to views.
func ShortDuration(d time.Duration) string { return shortDuration(d) }
