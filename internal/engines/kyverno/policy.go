package kyverno

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Raw enforcement values.
const (
	actionEnforce = "Enforce"
	actionAudit   = "Audit"
	celDeny       = "Deny"
	celWarn       = "Warn"
	celAudit      = "Audit"
)

func isClassic(o *unstructured.Unstructured) bool {
	g, _ := enginekit.ParseAPIVersion(o.GetAPIVersion())
	return g == groupClassic
}

func toPolicy(o *unstructured.Unstructured) model.Policy {
	p := model.Policy{Engine: Name, Kind: o.GetKind(), Name: o.GetName(), Namespace: o.GetNamespace(), Raw: o}
	if isClassic(o) {
		p.Enforcement = classicEnforcement(o)
		p.Sources = classicSources(o)
	} else {
		p.Enforcement = celEnforcement(o)
		p.Sources = celSources(o)
	}
	// Only validating policies produce report results; a mutating or
	// generating policy has no violation count to show.
	p.ViolationsKnown = p.Enforcement.Mode != model.ModeMutate && p.Enforcement.Mode != model.ModeUnknown
	p.Guidance = guidance(o)
	p.Severity = o.GetAnnotations()[annSeverity]
	p.Matches, p.MatchesAll, p.MatchKnown = matchRules(o)
	return p
}

// Annotations the Kyverno policy library sets.
const (
	annTitle       = "policies.kyverno.io/title"
	annDescription = "policies.kyverno.io/description"
)

func guidance(o *unstructured.Unstructured) []model.GuidanceItem {
	ann := o.GetAnnotations()
	if d := strings.TrimSpace(ann[annDescription]); d != "" {
		return []model.GuidanceItem{{From: "annotation " + annDescription, Title: ann[annTitle], Text: d}}
	}
	if t := strings.TrimSpace(ann[annTitle]); t != "" {
		return []model.GuidanceItem{{From: "annotation " + annTitle, Title: t, Text: t}}
	}
	return nil
}

// shortKinds are table aliases; detail views keep the full kind.
var shortKinds = map[string]string{
	"ClusterPolicy": "CPol", "Policy": "Pol",
	"ValidatingPolicy": "VPol", "NamespacedValidatingPolicy": "NVPol",
	"ImageValidatingPolicy": "IVPol", "NamespacedImageValidatingPolicy": "NIVPol",
	"MutatingPolicy": "MPol", "NamespacedMutatingPolicy": "NMPol",
	"GeneratingPolicy": "GPol", "NamespacedGeneratingPolicy": "NGPol",
	"DeletingPolicy": "DPol", "NamespacedDeletingPolicy": "NDPol",
	"PolicyException": "PolEx",
}

// ShortKind implements engine.KindNamer.
func (e *Engine) ShortKind(kind string) string {
	if s, ok := shortKinds[kind]; ok {
		return s
	}
	return kind
}

func rules(o *unstructured.Unstructured) []map[string]any {
	var out []map[string]any
	for _, r := range enginekit.Slice(o.Object, "spec", "rules") {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// classicEnforcement: each validate (and verifyImages) rule has its own
// failureAction, defaulting to the deprecated spec.validationFailureAction
// and then to Audit. One enforcing rule makes the policy enforcing. Policies
// without validation rules (pure mutate/generate) have no enforcement mode.
func classicEnforcement(o *unstructured.Unstructured) model.Enforcement {
	def, _, _ := unstructured.NestedString(o.Object, "spec", "validationFailureAction")
	if def == "" {
		def = actionAudit
	}
	actions := map[string]bool{}
	var other []string
	for _, r := range rules(o) {
		if v, ok := r["validate"].(map[string]any); ok {
			actions[orDefault(enginekit.Str(v, "failureAction"), def)] = true
		}
		for _, vi := range enginekit.Slice(r, "verifyImages") {
			vm, _ := vi.(map[string]any)
			actions[orDefault(enginekit.Str(vm, "failureAction"), def)] = true
		}
		for _, t := range []string{"mutate", "generate"} {
			if _, ok := r[t]; ok {
				other = append(other, t)
			}
		}
	}
	if len(actions) == 0 {
		raw := strings.Join(unique(other), ",")
		if raw == "mutate" {
			return model.Enforcement{Mode: model.ModeMutate, Raw: raw}
		}
		return model.Enforcement{Mode: model.ModeUnknown, Raw: raw}
	}
	mode := model.ModeAudit
	var raws []string
	for a := range actions {
		raws = append(raws, a)
		if strings.EqualFold(a, actionEnforce) {
			mode = model.ModeEnforce
		}
	}
	sort.Strings(raws)
	return model.Enforcement{Mode: mode, Raw: strings.Join(raws, ",")}
}

// celEnforcement normalizes spec.validationActions (Deny/Warn/Audit, like
// ValidatingAdmissionPolicy). Mutating, generating and deleting policies
// have no enforcement mode; their raw value names what they do.
func celEnforcement(o *unstructured.Unstructured) model.Enforcement {
	acts, found, _ := unstructured.NestedStringSlice(o.Object, "spec", "validationActions")
	if !found {
		if strings.Contains(o.GetKind(), "Mutating") {
			return model.Enforcement{Mode: model.ModeMutate, Raw: "mutate"}
		}
		for _, verb := range []string{"Generating", "Deleting"} {
			if strings.Contains(o.GetKind(), verb) {
				return model.Enforcement{Mode: model.ModeUnknown, Raw: strings.ToLower(strings.TrimSuffix(verb, "ing")) + "e"}
			}
		}
		return model.Enforcement{Mode: model.ModeUnknown}
	}
	mode := model.ModeUnknown
	for _, a := range acts {
		switch a {
		case celDeny:
			mode = enginekit.StrongerMode(mode, model.ModeEnforce)
		case celWarn:
			mode = enginekit.StrongerMode(mode, model.ModeWarn)
		case celAudit:
			mode = enginekit.StrongerMode(mode, model.ModeAudit)
		}
	}
	return model.Enforcement{Mode: mode, Raw: strings.Join(acts, ",")}
}

func classicSources(o *unstructured.Unstructured) []model.Source {
	var out []model.Source
	for _, r := range rules(o) {
		name := enginekit.Str(r, "name")
		body := map[string]any{}
		for k, v := range r {
			if k != "name" {
				body[k] = v
			}
		}
		out = append(out, model.Source{Language: model.LangYAML, Code: enginekit.YAML(body), Origin: "rule " + name})
		if v, ok := r["validate"].(map[string]any); ok {
			if code := celExpressions(enginekit.Slice(v, "cel", "expressions"), "expression"); code != "" {
				out = append(out, model.Source{Language: model.LangCEL, Code: code, Origin: "rule " + name + " (cel)"})
			}
		}
	}
	return out
}

// celSources gathers every CEL expression of a policies.kyverno.io policy.
func celSources(o *unstructured.Unstructured) []model.Source {
	origin := o.GetKind() + " " + o.GetName()
	var b strings.Builder
	for _, v := range enginekit.Slice(o.Object, "spec", "variables") {
		vm, _ := v.(map[string]any)
		fmt.Fprintf(&b, "// variable %s\n%s\n\n", enginekit.Str(vm, "name"), enginekit.Str(vm, "expression"))
	}
	for _, mc := range enginekit.Slice(o.Object, "spec", "matchConditions") {
		mm, _ := mc.(map[string]any)
		fmt.Fprintf(&b, "// match condition %s\n%s\n\n", enginekit.Str(mm, "name"), enginekit.Str(mm, "expression"))
	}
	b.WriteString(celExpressions(enginekit.Slice(o.Object, "spec", "validations"), "expression"))
	for i, m := range enginekit.Slice(o.Object, "spec", "mutations") {
		mm, _ := m.(map[string]any)
		for _, kind := range []string{"applyConfiguration", "jsonPatch"} {
			if expr, ok, _ := unstructured.NestedString(mm, kind, "expression"); ok {
				fmt.Fprintf(&b, "// mutation %d (%s)\n%s\n\n", i+1, kind, expr)
			}
		}
	}
	for i, g := range enginekit.Slice(o.Object, "spec", "generate") {
		gm, _ := g.(map[string]any)
		fmt.Fprintf(&b, "// generate %d\n%s\n\n", i+1, enginekit.Str(gm, "expression"))
	}
	code := strings.TrimSpace(b.String())
	if code == "" {
		return nil
	}
	return []model.Source{{Language: model.LangCEL, Code: code, Origin: origin}}
}

func celExpressions(items []any, field string) string {
	var b strings.Builder
	for i, it := range items {
		m, _ := it.(map[string]any)
		msg := enginekit.Str(m, "message")
		if msg == "" {
			msg = enginekit.Str(m, "messageExpression")
		}
		fmt.Fprintf(&b, "// validation %d: %s\n%s\n\n", i, msg, enginekit.Str(m, field))
	}
	return b.String()
}

// readiness reports policies Kyverno could not make ready. Classic policies
// use status.conditions[type=Ready]; CEL policies status.conditionStatus.
func readiness(o *unstructured.Unstructured) []model.HealthIssue {
	ref := enginekit.Ref(o)
	issue := func(sev model.Severity, msg string) model.HealthIssue {
		return model.HealthIssue{Engine: Name, Severity: sev, Category: "policy", Object: ref, Message: msg}
	}
	var out []model.HealthIssue
	conds := enginekit.Slice(o.Object, "status", "conditions")
	if !isClassic(o) {
		conds = enginekit.Slice(o.Object, "status", "conditionStatus", "conditions")
		if ready, found, _ := unstructured.NestedBool(o.Object, "status", "conditionStatus", "ready"); found && !ready {
			msg, _, _ := unstructured.NestedString(o.Object, "status", "conditionStatus", "message")
			out = append(out, issue(model.SeverityError, "policy is not ready: "+orDefault(msg, "no reason given")))
		}
	}
	for _, c := range conds {
		cm, _ := c.(map[string]any)
		if enginekit.Str(cm, "status") == "False" {
			out = append(out, issue(model.SeverityWarning,
				fmt.Sprintf("%s: %s", enginekit.Str(cm, "type"), orDefault(enginekit.Str(cm, "message"), enginekit.Str(cm, "reason")))))
		}
	}
	return out
}

// mutations lists classic mutate rules and MutatingPolicy objects.
func mutations(o *unstructured.Unstructured) []model.Mutation {
	if !isClassic(o) {
		if !strings.Contains(o.GetKind(), "Mutating") {
			return nil
		}
		n := len(enginekit.Slice(o.Object, "spec", "mutations"))
		return []model.Mutation{{
			Engine: Name, Kind: o.GetKind(), Name: o.GetName(), Namespace: o.GetNamespace(),
			Location: fmt.Sprintf("%d mutation(s)", n), Targets: celTargets(o), Raw: o,
		}}
	}
	var out []model.Mutation
	for _, r := range rules(o) {
		m, ok := r["mutate"].(map[string]any)
		if !ok {
			continue
		}
		var how []string
		for k := range m {
			how = append(how, k)
		}
		sort.Strings(how)
		out = append(out, model.Mutation{
			Engine: Name, Kind: o.GetKind(), Name: o.GetName(), Namespace: o.GetNamespace(),
			Location: fmt.Sprintf("rule %s (%s)", enginekit.Str(r, "name"), strings.Join(how, ", ")),
			Targets:  strings.Join(classicKinds(r), ","), Raw: o,
		})
	}
	return out
}

// classicKinds collects match kinds from match.any, match.all and the legacy
// match.resources form.
func classicKinds(rule map[string]any) []string {
	var kinds []string
	add := func(m any) {
		mm, _ := m.(map[string]any)
		ks, _, _ := unstructured.NestedStringSlice(mm, "resources", "kinds")
		kinds = append(kinds, ks...)
	}
	match, _ := rule["match"].(map[string]any)
	for _, k := range []string{"any", "all"} {
		for _, m := range enginekit.Slice(match, k) {
			add(m)
		}
	}
	add(match)
	return unique(kinds)
}

func celTargets(o *unstructured.Unstructured) string {
	var res []string
	for _, rr := range enginekit.Slice(o.Object, "spec", "matchConstraints", "resourceRules") {
		m, _ := rr.(map[string]any)
		rs, _, _ := unstructured.NestedStringSlice(m, "resources")
		res = append(res, rs...)
	}
	return strings.Join(unique(res), ",")
}

func unique(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
