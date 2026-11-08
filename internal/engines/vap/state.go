package vap

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Generated-policy markers, verified on kind (Gatekeeper v3.23, Kyverno
// v1.19): Gatekeeper's VAPs are owned (controller) by their
// ConstraintTemplate and their bindings by the constraint; Kyverno's
// (spec.autogen.validatingAdmissionPolicy.enabled) are owned by the
// ValidatingPolicy and labelled app.kubernetes.io/managed-by=kyverno.
const (
	gatekeeperTemplates   = "templates.gatekeeper.sh"
	gatekeeperConstraints = "constraints.gatekeeper.sh"
	// reportSource is the PolicyReport result source Kyverno uses for VAPs.
	reportSource = "ValidatingAdmissionPolicy"
)

type inputs struct {
	policies, bindings, reports []*unstructured.Unstructured
	params                      *paramChecker
}

// vapPolicy is a policy with what only this engine knows.
type vapPolicy struct {
	model.Policy
	generatedBy string
	bindings    []*unstructured.Unstructured
}

type state struct {
	policies      []vapPolicy // sorted by name
	byName        map[string]*vapPolicy
	bindings      []*unstructured.Unstructured
	violations    []model.Violation
	health        []model.HealthIssue
	generatedFrom map[model.PolicyKey][]string
	paramRefs     []paramRef
}

func build(in inputs) *state {
	s := &state{byName: map[string]*vapPolicy{}, generatedFrom: map[model.PolicyKey][]string{}, bindings: in.bindings}
	for _, o := range in.policies {
		s.policies = append(s.policies, vapPolicy{Policy: toPolicy(o), generatedBy: generatedBy(o)})
	}
	sort.Slice(s.policies, func(i, j int) bool { return s.policies[i].Name < s.policies[j].Name })
	for i := range s.policies {
		s.byName[s.policies[i].Name] = &s.policies[i]
	}
	for _, b := range in.bindings {
		name, _, _ := unstructured.NestedString(b.Object, "spec", "policyName")
		p, ok := s.byName[name]
		if !ok {
			s.health = append(s.health, model.HealthIssue{Engine: Name, Severity: model.SeverityError, Category: "bindings",
				Object: refOf(b), Message: fmt.Sprintf("binds policy %q, which does not exist: it enforces nothing", name)})
			continue
		}
		p.bindings = append(p.bindings, b)
		// Generated bindings' params are the generating engine's objects
		// (a Gatekeeper constraint): that engine keeps them in step.
		if ref, ok := paramRefOf(p.Raw, b); ok && p.generatedBy == "" {
			s.paramRefs = append(s.paramRefs, ref)
		}
		for _, o := range b.GetOwnerReferences() {
			if g, _ := enginekit.ParseAPIVersion(o.APIVersion); g == gatekeeperConstraints {
				key := model.PolicyKey{Engine: "gatekeeper", Kind: o.Kind, Name: o.Name}
				s.generatedFrom[key] = append(s.generatedFrom[key], fmt.Sprintf("ValidatingAdmissionPolicy %s, binding %s (%s)",
					name, b.GetName(), strings.Join(actionsOf(b), ", ")))
			}
		}
	}
	for i := range s.policies {
		p := &s.policies[i]
		p.Enforcement = enforcementOf(p.bindings)
		for _, o := range p.Raw.GetOwnerReferences() {
			if g, _ := enginekit.ParseAPIVersion(o.APIVersion); kyvernoGroup(g) {
				key := model.PolicyKey{Engine: "kyverno", Kind: o.Kind, Namespace: p.Raw.GetNamespace(), Name: o.Name}
				s.generatedFrom[key] = append(s.generatedFrom[key], "ValidatingAdmissionPolicy "+p.Name)
			}
		}
		if p.generatedBy == "" {
			s.health = append(s.health, policyHealth(p)...)
		}
	}
	s.applyReports(in.reports)
	s.health = append(s.health, in.params.issues(s.paramRefs)...)
	return s
}

func refOf(o *unstructured.Unstructured) model.ObjectRef {
	g, v := enginekit.ParseAPIVersion(o.GetAPIVersion())
	return model.ObjectRef{Group: g, Version: v, Kind: o.GetKind(), Namespace: o.GetNamespace(), Name: o.GetName()}
}

func toPolicy(o *unstructured.Unstructured) model.Policy {
	p := model.Policy{Engine: Name, Kind: kindPolicy, Name: o.GetName(), Raw: o, MatchKnown: true}
	p.Matches, p.MatchesAll = enginekit.ResourceRules(enginekit.Slice(o.Object, "spec", "matchConstraints", "resourceRules"))
	var b strings.Builder
	for i, v := range enginekit.Slice(o.Object, "spec", "validations") {
		m, _ := v.(map[string]any)
		if i > 0 {
			b.WriteString("\n\n")
		}
		if msg := enginekit.Str(m, "message"); msg != "" {
			fmt.Fprintf(&b, "// %s\n", msg)
		} else if me := enginekit.Str(m, "messageExpression"); me != "" {
			fmt.Fprintf(&b, "// message: %s\n", me)
		}
		b.WriteString(enginekit.Str(m, "expression"))
	}
	if b.Len() > 0 {
		p.Sources = []model.Source{{Language: model.LangCEL, Code: b.String(), Origin: "ValidatingAdmissionPolicy " + o.GetName() + " validations"}}
	}
	return p
}

// generatedBy names the engine object a policy was generated from.
func generatedBy(o *unstructured.Unstructured) string {
	for _, ref := range o.GetOwnerReferences() {
		g, _ := enginekit.ParseAPIVersion(ref.APIVersion)
		switch {
		case g == gatekeeperTemplates:
			return "gatekeeper " + ref.Kind + "/" + ref.Name
		case kyvernoGroup(g):
			return "kyverno " + ref.Kind + "/" + ref.Name
		}
	}
	// The managed-by label alone is not enough: a user VAP copied from a
	// Kyverno manifest would vanish from counts and health.
	return ""
}

func kyvernoGroup(g string) bool { return g == "kyverno.io" || strings.HasSuffix(g, ".kyverno.io") }

func actionsOf(b *unstructured.Unstructured) []string {
	acts, _, _ := unstructured.NestedStringSlice(b.Object, "spec", "validationActions")
	return acts
}

// strictness orders validationActions: Deny > Warn > Audit.
var strictness = map[string]int{"Audit": 1, "Warn": 2, "Deny": 3}

// enforcementOf is the strictest action across a policy's bindings.
func enforcementOf(bindings []*unstructured.Unstructured) model.Enforcement {
	if len(bindings) == 0 {
		return model.Enforcement{Mode: model.ModeUnknown, Raw: "not bound"}
	}
	seen := map[string]bool{}
	best := ""
	for _, b := range bindings {
		for _, a := range actionsOf(b) {
			seen[a] = true
			if strictness[a] > strictness[best] {
				best = a
			}
		}
	}
	raw := make([]string, 0, len(seen))
	for a := range seen {
		raw = append(raw, a)
	}
	sort.Slice(raw, func(i, j int) bool { return strictness[raw[i]] > strictness[raw[j]] })
	mode := map[string]model.EnforcementMode{"Deny": model.ModeEnforce, "Warn": model.ModeWarn, "Audit": model.ModeAudit}[best]
	if mode == "" {
		mode = model.ModeUnknown
	}
	return model.Enforcement{Mode: mode, Raw: strings.Join(raw, ",")}
}

// policyHealth: unbound policies and CEL type-checking warnings.
func policyHealth(p *vapPolicy) []model.HealthIssue {
	ref := refOf(p.Raw)
	var out []model.HealthIssue
	if len(p.bindings) == 0 {
		out = append(out, model.HealthIssue{Engine: Name, Severity: model.SeverityWarning, Category: "bindings", Object: ref,
			Message: "not bound: no ValidatingAdmissionPolicyBinding references it, so it enforces nothing"})
	}
	for _, w := range enginekit.Slice(p.Raw.Object, "status", "typeChecking", "expressionWarnings") {
		m, _ := w.(map[string]any)
		out = append(out, model.HealthIssue{Engine: Name, Severity: model.SeverityWarning, Category: "type checking", Object: ref,
			Message: enginekit.Str(m, "fieldRef") + ": " + strings.TrimSpace(enginekit.Str(m, "warning"))})
	}
	return out
}

// applyReports turns VAP-sourced report results into violations. Counts
// are known only when some result has that source (Kyverno's VAP reports
// are enabled); otherwise there is no audit source and the count is "-".
func (s *state) applyReports(reports []*unstructured.Unstructured) {
	known := false
	counts := map[string]int{}
	for _, rep := range reports {
		scope := refFromMap(rep.Object, "scope")
		for _, r := range enginekit.Slice(rep.Object, "results") {
			m, _ := r.(map[string]any)
			if enginekit.Str(m, "source") != reportSource {
				continue
			}
			known = true
			res := enginekit.Str(m, "result")
			p, ok := s.byName[enginekit.Str(m, "policy")]
			if !ok || p.generatedBy != "" || len(p.bindings) == 0 || (res != "fail" && res != "warn") {
				continue
			}
			counts[p.Name]++
			target := scope
			for _, x := range enginekit.Slice(m, "resources") {
				if rm, ok := x.(map[string]any); ok {
					target = refFromMap(map[string]any{"r": rm}, "r")
					break
				}
			}
			s.violations = append(s.violations, model.Violation{
				Engine: Name, Policy: p.Key(), Rule: enginekit.Str(m, "rule"), Target: target,
				Message: enginekit.Str(m, "message"), Enforcement: p.Enforcement,
			})
		}
	}
	for i := range s.policies {
		p := &s.policies[i]
		p.ViolationsKnown = known && len(p.bindings) > 0
		if p.ViolationsKnown {
			p.ViolationCount = counts[p.Name]
		}
	}
}

func refFromMap(obj map[string]any, field string) model.ObjectRef {
	m, ok := obj[field].(map[string]any)
	if !ok {
		return model.ObjectRef{}
	}
	g, v := enginekit.ParseAPIVersion(enginekit.Str(m, "apiVersion"))
	return model.ObjectRef{Group: g, Version: v, Kind: enginekit.Str(m, "kind"), Namespace: enginekit.Str(m, "namespace"), Name: enginekit.Str(m, "name")}
}
