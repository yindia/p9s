package gatekeeper

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Gatekeeper enforcement actions.
const (
	actionDeny   = "deny"
	actionDryRun = "dryrun"
	actionWarn   = "warn"
	actionScoped = "scoped"

	celEngine  = "K8sNativeValidation"
	regoEngine = "Rego"
)

// Policies implements engine.Engine. Each constraint is one policy.
func (e *Engine) Policies() ([]model.Policy, error) {
	s := e.snapshot()
	out := make([]model.Policy, 0, len(s.constraints))
	for _, c := range s.constraints {
		out = append(out, toPolicy(c, s.templateByKind[c.GetKind()]))
	}
	return out, nil
}

func toPolicy(c, tmpl *unstructured.Unstructured) model.Policy {
	total := totalViolations(c)
	audit := auditTime(c)
	return model.Policy{
		Severity:       c.GetAnnotations()[annSeverity],
		Engine:         Name,
		Kind:           c.GetKind(),
		Name:           c.GetName(),
		Namespace:      c.GetNamespace(),
		Enforcement:    enforcement(c),
		ViolationCount: total,
		// Until the first audit, totalViolations is absent, not zero.
		ViolationsKnown: !audit.IsZero(),
		LastAudit:       audit,
		Sources:         templateSources(tmpl),
		Guidance:        guidance(c, tmpl),
		Raw:             c,
	}
}

// Annotations gatekeeper-library sets on templates (and users may set on
// constraints).
const (
	annDescription = "description"
	annTitle       = "metadata.gatekeeper.sh/title"
)

// guidance collects remediation text: the constraint's own description
// first (it is specific to this use), then the template's title and
// description.
func guidance(c, tmpl *unstructured.Unstructured) []model.GuidanceItem {
	var out []model.GuidanceItem
	if d := strings.TrimSpace(c.GetAnnotations()[annDescription]); d != "" {
		out = append(out, model.GuidanceItem{From: "constraint annotation " + annDescription,
			Title: c.GetAnnotations()[annTitle], Text: d})
	}
	if tmpl != nil {
		ann := tmpl.GetAnnotations()
		from := "template " + tmpl.GetName() + " annotation "
		d, t := strings.TrimSpace(ann[annDescription]), strings.TrimSpace(ann[annTitle])
		switch {
		case d != "" && (len(out) == 0 || out[0].Text != d): // same text twice says nothing new
			out = append(out, model.GuidanceItem{From: from + annDescription, Title: t, Text: d})
		case d == "" && t != "":
			out = append(out, model.GuidanceItem{From: from + annTitle, Title: t, Text: t})
		}
	}
	return out
}

// Enforcement points of scoped enforcement actions.
const (
	pointValidation = "validation.gatekeeper.sh"
	pointAll        = "*"
)

// enforcement normalizes spec.enforcementAction. For "scoped", the mode is
// what admission (the validation webhook) does: actions bound only to audit
// or gator never block a request, so such a constraint counts as audit.
func enforcement(c *unstructured.Unstructured) model.Enforcement {
	raw, _, _ := unstructured.NestedString(c.Object, "spec", "enforcementAction")
	if raw == "" {
		raw = actionDeny // Gatekeeper's default
	}
	if raw != actionScoped {
		return model.Enforcement{Mode: normalizeAction(raw), Raw: raw}
	}
	mode := model.ModeAudit
	for _, sa := range scopedActions(c) {
		if sa.admission {
			mode = enginekit.StrongerMode(mode, normalizeAction(sa.action))
		}
	}
	return model.Enforcement{Mode: mode, Raw: raw}
}

type scopedAction struct {
	action    string
	points    []string
	admission bool
}

func scopedActions(c *unstructured.Unstructured) []scopedAction {
	var out []scopedAction
	for _, s := range enginekit.Slice(c.Object, "spec", "scopedEnforcementActions") {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		sa := scopedAction{action: enginekit.Str(m, "action")}
		for _, p := range enginekit.Slice(m, "enforcementPoints") {
			pm, _ := p.(map[string]any)
			name := enginekit.Str(pm, "name")
			sa.points = append(sa.points, name)
			sa.admission = sa.admission || name == pointValidation || name == pointAll
		}
		out = append(out, sa)
	}
	return out
}

// violationEnforcement reads a violation's action. Scoped constraints report
// a list (enforcementActions) instead of a single enforcementAction.
func violationEnforcement(m map[string]any) model.Enforcement {
	if a := enginekit.Str(m, "enforcementAction"); a != "" && a != actionScoped {
		return model.Enforcement{Mode: normalizeAction(a), Raw: a}
	}
	mode := model.ModeUnknown
	var raws []string
	for _, a := range enginekit.Slice(m, "enforcementActions") {
		s, _ := a.(string)
		raws = append(raws, s)
		mode = enginekit.StrongerMode(mode, normalizeAction(s))
	}
	return model.Enforcement{Mode: mode, Raw: strings.Join(raws, ",")}
}

func normalizeAction(a string) model.EnforcementMode {
	switch a {
	case actionDeny:
		return model.ModeEnforce
	case actionWarn:
		return model.ModeWarn
	case actionDryRun:
		return model.ModeAudit
	default:
		return model.ModeUnknown
	}
}

func totalViolations(c *unstructured.Unstructured) int {
	return enginekit.Int(c.Object, "status", "totalViolations")
}

func auditTime(c *unstructured.Unstructured) time.Time {
	ts, _, _ := unstructured.NestedString(c.Object, "status", "auditTimestamp")
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

func templateKind(t *unstructured.Unstructured) string {
	k, _, _ := unstructured.NestedString(t.Object, "spec", "crd", "spec", "names", "kind")
	return k
}

// templateSources extracts rego (legacy targets[].rego and code[] Rego
// engine) and CEL (K8sNativeValidation) sources from a template.
func templateSources(t *unstructured.Unstructured) []model.Source {
	if t == nil {
		return nil
	}
	origin := "ConstraintTemplate " + t.GetName()
	var out []model.Source
	targets, _, _ := unstructured.NestedSlice(t.Object, "spec", "targets")
	for _, tg := range targets {
		m, ok := tg.(map[string]any)
		if !ok {
			continue
		}
		if rego, _ := m["rego"].(string); rego != "" {
			out = append(out, model.Source{Language: model.LangRego, Code: rego, Origin: origin})
		}
		libs, _, _ := unstructured.NestedStringSlice(m, "libs")
		for i, lib := range libs {
			out = append(out, model.Source{Language: model.LangRego, Code: lib, Origin: fmt.Sprintf("%s lib %d", origin, i+1)})
		}
		code, _, _ := unstructured.NestedSlice(m, "code")
		for _, c := range code {
			out = append(out, codeSources(c, origin)...)
		}
	}
	return out
}

func codeSources(c any, origin string) []model.Source {
	m, ok := c.(map[string]any)
	if !ok {
		return nil
	}
	eng, _ := m["engine"].(string)
	switch eng {
	case regoEngine:
		rego, _, _ := unstructured.NestedString(m, "source", "rego")
		if rego == "" {
			return nil
		}
		return []model.Source{{Language: model.LangRego, Code: rego, Origin: origin}}
	case celEngine:
		return celSources(m, origin)
	default:
		return nil
	}
}

func celSources(m map[string]any, origin string) []model.Source {
	var code string
	vars, _, _ := unstructured.NestedSlice(m, "source", "variables")
	for _, v := range vars {
		vm, _ := v.(map[string]any)
		name, _ := vm["name"].(string)
		expr, _ := vm["expression"].(string)
		code += fmt.Sprintf("// variable %s\n%s\n\n", name, expr)
	}
	vals, _, _ := unstructured.NestedSlice(m, "source", "validations")
	for i, v := range vals {
		vm, _ := v.(map[string]any)
		expr, _ := vm["expression"].(string)
		msg, _ := vm["message"].(string)
		if msg == "" {
			msg, _ = vm["messageExpression"].(string)
		}
		code += fmt.Sprintf("// validation %d: %s\n%s\n\n", i+1, msg, expr)
	}
	if code == "" {
		return nil
	}
	return []model.Source{{Language: model.LangCEL, Code: code, Origin: origin}}
}

// Violations implements engine.Engine, from each constraint's audit status.
func (e *Engine) Violations() ([]model.Violation, error) {
	s := e.snapshot()
	var out []model.Violation
	for _, c := range s.constraints {
		out = append(out, constraintViolations(c)...)
	}
	return out, nil
}

func constraintViolations(c *unstructured.Unstructured) []model.Violation {
	key := model.PolicyKey{Engine: Name, Kind: c.GetKind(), Namespace: c.GetNamespace(), Name: c.GetName()}
	ts := auditTime(c)
	items, _, _ := unstructured.NestedSlice(c.Object, "status", "violations")
	// Gatekeeper caps status.violations (constraintViolationsLimit).
	truncated := len(items) < totalViolations(c)
	out := make([]model.Violation, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { return enginekit.Str(m, k) }
		out = append(out, model.Violation{
			Engine: Name,
			Policy: key,
			Target: model.ObjectRef{
				Group: str("group"), Version: str("version"), Kind: str("kind"),
				Namespace: str("namespace"), Name: str("name"),
			},
			Message:     str("message"),
			Enforcement: violationEnforcement(m),
			Timestamp:   ts,
			Truncated:   truncated,
		})
	}
	return out
}

// Health implements engine.Engine. Phase 1 covers template creation and
// compile errors plus constraints that have never been audited.
func (e *Engine) Health() ([]model.HealthIssue, error) {
	s := e.snapshot()
	var out []model.HealthIssue
	if on, hint := e.EventsEnabled(); !on {
		out = append(out, model.HealthIssue{Engine: Name, Severity: model.SeverityInfo, Category: "events", Message: hint})
	}
	if errp := e.discoveryErr.Load(); errp != nil {
		out = append(out, model.HealthIssue{
			Engine: Name, Severity: model.SeverityError, Category: "discovery",
			Message: "constraint kinds could not be discovered: " + (*errp).Error(),
		})
	}
	for _, t := range s.templates {
		out = append(out, templateHealth(t)...)
	}
	for _, c := range s.constraints {
		for _, msg := range podErrors(c) {
			out = append(out, model.HealthIssue{
				Engine: Name, Severity: model.SeverityError, Category: "constraint",
				Object: enginekit.Ref(c), Message: msg,
			})
		}
		if auditTime(c).IsZero() {
			out = append(out, model.HealthIssue{
				Engine: Name, Severity: model.SeverityInfo, Category: "audit",
				Object:  enginekit.Ref(c),
				Message: "constraint has not been audited yet",
			})
		}
	}
	return out, nil
}

func templateHealth(t *unstructured.Unstructured) []model.HealthIssue {
	ref := enginekit.Ref(t)
	var out []model.HealthIssue
	if created, _, _ := unstructured.NestedBool(t.Object, "status", "created"); !created {
		out = append(out, model.HealthIssue{
			Engine: Name, Severity: model.SeverityWarning, Category: "template",
			Object: ref, Message: "constraint CRD has not been created",
		})
	}
	for _, msg := range templateErrors(t) {
		out = append(out, model.HealthIssue{
			Engine: Name, Severity: model.SeverityError, Category: "compile",
			Object: ref, Message: msg,
		})
	}
	return out
}

// templateErrors returns the distinct errors reported by any pod.
func templateErrors(t *unstructured.Unstructured) []string { return podErrors(t) }

// podErrors returns the distinct status.byPod[].errors of a template or
// constraint, prefixed with their code.
func podErrors(o *unstructured.Unstructured) []string {
	pods, _, _ := unstructured.NestedSlice(o.Object, "status", "byPod")
	seen := map[string]bool{}
	for _, p := range pods {
		pm, _ := p.(map[string]any)
		errs, _, _ := unstructured.NestedSlice(pm, "errors")
		for _, e := range errs {
			em, _ := e.(map[string]any)
			msg, _ := em["message"].(string)
			if code, _ := em["code"].(string); code != "" {
				msg = code + ": " + msg
			}
			if msg != "" {
				seen[msg] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// annSeverity is not set by gatekeeper-library; users may annotate
// constraints with it.
const annSeverity = "severity"
