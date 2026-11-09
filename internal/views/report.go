package views

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
)

// ReportSchemaVersion identifies the report JSON.
const ReportSchemaVersion = "p9s/report/v1"

// ReportSet is what p9s report writes: one report per context.
type ReportSet struct {
	SchemaVersion string   `json:"schemaVersion"`
	Reports       []Report `json:"reports"`
}

// Report is one context's security report. Every section comes from query
// and the materialised dashboard; renderers only format it.
type Report struct {
	// Error is set when the context could not be reported on; the other
	// sections are then empty.
	Error       string            `json:"error,omitempty"`
	Header      ReportHeader      `json:"header"`
	Summary     ReportSummary     `json:"summary"`
	PSS         ReportPSS         `json:"pss"`
	Violations  ReportViolations  `json:"violations"`
	Findings    []Finding         `json:"findings"`
	Unprotected ReportUnprotected `json:"unprotected"`
	Since       *ReportDiff       `json:"since,omitempty"`
	Omitted     []string          `json:"omitted"`
	Notices     []string          `json:"notices,omitempty"`
	Policies    []ReportPolicy    `json:"policies"`
	Items       []ReportViolation `json:"items"`
}

// ReportHeader identifies the report. BodySHA256 hashes the report's
// canonical JSON with this field empty.
type ReportHeader struct {
	Cluster     string         `json:"cluster"`
	Context     string         `json:"context"`
	Engines     []ReportEngine `json:"engines"`
	GeneratedAt time.Time      `json:"generatedAt"`
	P9sVersion  string         `json:"p9sVersion"`
	Snapshot    *time.Time     `json:"snapshot,omitempty"`
	BodySHA256  string         `json:"bodySHA256"`
}

// ReportEngine is one active engine.
type ReportEngine struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ReportSummary is section 1.
type ReportSummary struct {
	// PoliciesByMode counts policies per normalised enforcement.
	PoliciesByMode map[string]int `json:"policiesByMode"`
	// Violations sums the engines' own counts; UnknownPolicies have none.
	Violations      int           `json:"violations"`
	UnknownPolicies int           `json:"unknownPolicies"`
	Health          query.Overall `json:"health"`
}

// ReportPSS is section 2.
type ReportPSS struct {
	Controls      []query.ControlCoverage `json:"controls"`
	Uncategorised []model.PolicyKey       `json:"uncategorised"`
}

// ReportViolations is section 3.
type ReportViolations struct {
	BySeverity  []Count `json:"bySeverity"`
	ByNamespace []Count `json:"byNamespace"`
	ByOwner     []Count `json:"byOwner"`
	// Truncated is set when an engine listed fewer violations than it
	// counted: the breakdowns cover the listed ones only.
	Truncated bool `json:"truncated"`
}

// Count is one bucket.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Finding is one graded health check (section 5).
type Finding struct {
	Panel    string `json:"panel"`
	Engine   string `json:"engine"`
	Check    string `json:"check"`
	Status   string `json:"status"`
	Severity string `json:"severity"` // "critical" or "warning"
}

// ReportUnprotected is section 7.
type ReportUnprotected struct {
	Approximate bool                    `json:"approximate"`
	Kinds       []query.UnprotectedKind `json:"kinds"`
	// UnknownMatch are policies whose match p9s could not read.
	UnknownMatch []model.PolicyKey `json:"unknownMatch,omitempty"`
	// AllKinds are policies that select every kind (no kind restriction).
	AllKinds []model.PolicyKey `json:"allKinds,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// ReportPolicy and ReportViolation are the raw lists --since compares.
type ReportPolicy struct {
	Key  model.PolicyKey `json:"key"`
	Mode string          `json:"mode"`
}

// ReportViolation is one listed violation. ID is policy, rule and target:
// stable while the violation persists even when its message changes.
type ReportViolation struct {
	Policy  model.PolicyKey `json:"policy"`
	Rule    string          `json:"rule,omitempty"`
	Target  model.ObjectRef `json:"target"`
	Message string          `json:"message"`
	ID      string          `json:"id"`
}

func reportViolationID(v model.Violation) string {
	return v.Policy.String() + "|" + v.Rule + "|" + v.Target.String()
}

// ReportDiff is section 8 (with --since).
type ReportDiff struct {
	Previous           time.Time         `json:"previous"`
	NewViolations      []ReportViolation `json:"newViolations"`
	ResolvedViolations []ReportViolation `json:"resolvedViolations"`
	// ChangedMessages persist with a different message.
	ChangedMessages    []ReportViolation `json:"changedMessages"`
	EnforcementChanged []ModeChange      `json:"enforcementChanged"`
	AddedPolicies      []model.PolicyKey `json:"addedPolicies"`
	RemovedPolicies    []model.PolicyKey `json:"removedPolicies"`
	// Comparable is false when either report had truncated lists: some
	// "new" or "resolved" items may only have moved in or out of the cap.
	Comparable bool `json:"comparable"`
}

// ModeChange is a policy whose enforcement changed.
type ModeChange struct {
	Policy model.PolicyKey `json:"policy"`
	From   string          `json:"from"`
	To     string          `json:"to"`
}

// ReportMeta is what the caller knows beyond the cluster.
type ReportMeta struct {
	Cluster    string
	P9sVersion string
	Generated  time.Time
	Snapshot   time.Time
}

// ReportOf builds one context's report.
func ReportOf(env Env, meta ReportMeta, apiResources func() ([]model.APIResource, error)) Report {
	es := env.Engines()
	ps, perr := query.Collect(query.Capable(es, engine.Filter{}, query.HasPolicies), engine.Engine.Policies)
	vs, verr := query.ViolationsWithOwners(es, engine.Filter{}, env.lookup())
	r := Report{
		Header: ReportHeader{Cluster: meta.Cluster, Context: env.ContextName(), GeneratedAt: meta.Generated.UTC(), P9sVersion: meta.P9sVersion},
		Omitted: []string{
			"Exemptions inventory: not part of this release.",
			"Who can change policy: arrives with the RBAC phase.",
			"Unprotected namespaces: needs the exemptions inventory.",
		},
		Notices: query.AccessNoticesDetailed(es),
	}
	for _, err := range []error{perr, verr} {
		if err != nil {
			r.Notices = append(r.Notices, err.Error())
		}
	}
	if !meta.Snapshot.IsZero() {
		t := meta.Snapshot.UTC()
		r.Header.Snapshot = &t
	}
	for _, d := range env.Detections() {
		if d.Installed && d.Err == nil {
			r.Header.Engines = append(r.Header.Engines, ReportEngine{Name: d.Engine.Name(), Version: d.Version})
		}
	}
	r.Summary = summaryOf(ps)
	d := DashboardOf(env)
	r.Summary.Health = d.Overall
	r.Findings = findingsOf(d)
	r.PSS.Controls, r.PSS.Uncategorised = query.PSSCoverage(ps)
	r.Violations = breakdowns(ps, vs)
	r.Unprotected = unprotectedOf(ps, apiResources)
	for _, p := range ps {
		r.Policies = append(r.Policies, ReportPolicy{Key: p.Key(), Mode: string(p.Enforcement.Mode)})
	}
	for _, v := range vs {
		r.Items = append(r.Items, ReportViolation{Policy: v.Policy, Rule: v.Rule, Target: v.Target, Message: v.Message, ID: reportViolationID(v)})
	}
	sort.Slice(r.Items, func(i, j int) bool { return r.Items[i].ID < r.Items[j].ID })
	return r
}

func summaryOf(ps []model.Policy) ReportSummary {
	s := ReportSummary{PoliciesByMode: map[string]int{}}
	for _, p := range ps {
		s.PoliciesByMode[p.Enforcement.Mode.Label()]++
		if p.ViolationsKnown {
			s.Violations += p.ViolationCount
		} else {
			s.UnknownPolicies++
		}
	}
	return s
}

// findingsOf lists the dashboard's critical and warning checks, worst first.
func findingsOf(d Dashboard) []Finding {
	var out []Finding
	for _, p := range d.Panels {
		for _, row := range p.Table.Rows {
			if len(row.Cells) < 3 {
				continue
			}
			st := row.Cells[len(row.Cells)-1]
			sev := ""
			switch st.Severity {
			case model.SeverityError:
				sev = "critical"
			case model.SeverityWarning:
				sev = "warning"
			default:
				continue
			}
			out = append(out, Finding{Panel: p.Title, Engine: row.Cells[0].Text, Check: row.Cells[1].Text, Status: st.Text, Severity: sev})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == "critical" && out[j].Severity != "critical" })
	return out
}

func breakdowns(ps []model.Policy, vs []model.Violation) ReportViolations {
	sev := map[model.PolicyKey]string{}
	for _, p := range ps {
		sev[p.Key()] = p.Severity
	}
	bySev, byNS := map[string]int{}, map[string]int{}
	var out ReportViolations
	for _, v := range vs {
		s := sev[v.Policy]
		if s == "" {
			s = "unspecified"
		}
		bySev[s]++
		ns := v.Target.Namespace
		if ns == "" || (v.Target.Kind == "Namespace" && v.Target.Group == "") {
			ns = "(cluster)"
		}
		byNS[ns]++
		out.Truncated = out.Truncated || v.Truncated
	}
	out.BySeverity = counts(bySev)
	out.ByNamespace = counts(byNS)
	for _, o := range query.TopOwners(query.GroupViolations(vs)) {
		out.ByOwner = append(out.ByOwner, Count{Name: o.Key, Count: o.Objects})
	}
	if len(out.ByOwner) > reportTopOwners {
		out.ByOwner = out.ByOwner[:reportTopOwners]
	}
	return out
}

// reportTopOwners bounds the owner breakdown.
const reportTopOwners = 20

func counts(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func unprotectedOf(ps []model.Policy, apiResources func() ([]model.APIResource, error)) ReportUnprotected {
	out := ReportUnprotected{Approximate: true}
	if apiResources == nil {
		out.Error = "the cluster's kinds are not available"
		return out
	}
	kinds, err := apiResources()
	if err != nil {
		out.Error = err.Error()
	}
	var validating []model.Policy
	for _, p := range ps {
		if p.Enforcement.Mode != model.ModeMutate {
			validating = append(validating, p)
		}
	}
	out.Kinds, out.UnknownMatch, out.AllKinds = query.Unprotected(validating, kinds)
	return out
}

// Diff compares cur with a previous report of the same context.
func Diff(prev, cur Report) ReportDiff {
	d := ReportDiff{Previous: prev.Header.GeneratedAt, Comparable: !prev.Violations.Truncated && !cur.Violations.Truncated}
	was := map[string]ReportViolation{}
	for _, v := range prev.Items {
		was[v.ID] = v
	}
	now := map[string]bool{}
	for _, v := range cur.Items {
		now[v.ID] = true
		old, ok := was[v.ID]
		switch {
		case !ok:
			d.NewViolations = append(d.NewViolations, v)
		case old.Message != v.Message:
			d.ChangedMessages = append(d.ChangedMessages, v)
		}
	}
	for _, v := range prev.Items {
		if !now[v.ID] {
			d.ResolvedViolations = append(d.ResolvedViolations, v)
		}
	}
	modes := map[model.PolicyKey]string{}
	for _, p := range prev.Policies {
		modes[p.Key] = p.Mode
	}
	still := map[model.PolicyKey]bool{}
	for _, p := range cur.Policies {
		still[p.Key] = true
		m, ok := modes[p.Key]
		switch {
		case !ok:
			d.AddedPolicies = append(d.AddedPolicies, p.Key)
		case m != p.Mode:
			d.EnforcementChanged = append(d.EnforcementChanged, ModeChange{Policy: p.Key, From: m, To: p.Mode})
		}
	}
	for _, p := range prev.Policies {
		if !still[p.Key] {
			d.RemovedPolicies = append(d.RemovedPolicies, p.Key)
		}
	}
	return d
}

// Seal sets the header hash: SHA-256 of the report's JSON with the hash
// field empty. Verifiers recompute it the same way.
func (r *Report) Seal() error {
	r.Header.BodySHA256 = ""
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	sum := sha256.Sum256(b)
	r.Header.BodySHA256 = hex.EncodeToString(sum[:])
	return nil
}

// Worst is the most severe finding level in a report: "critical",
// "warning" or "".
func (r Report) Worst() string {
	worst := ""
	for _, f := range r.Findings {
		if f.Severity == "critical" {
			return "critical"
		}
		worst = "warning"
	}
	return worst
}
