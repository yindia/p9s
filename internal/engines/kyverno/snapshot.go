package kyverno

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Report result values.
const (
	resultPass  = "pass"
	resultFail  = "fail"
	resultWarn  = "warn"
	resultError = "error"
	resultSkip  = "skip"

	sourceClassic = "kyverno"
	kindClassicCP = "ClusterPolicy"
	kindClassicP  = "Policy"
)

// snapshot is everything derived from the caches in one pass.
type snapshot struct {
	policies   []model.Policy
	byKey      map[model.PolicyKey]*model.Policy
	violations []model.Violation
	exceptions []*unstructured.Unstructured
	reports    []*unstructured.Unstructured
	mutations  []model.Mutation
	health     []model.HealthIssue
}

func build(policyObjs, exceptions, reports []*unstructured.Unstructured, denied hiddenKinds) *snapshot {
	s := &snapshot{byKey: map[model.PolicyKey]*model.Policy{}, exceptions: exceptions, reports: reports}
	s.policies = make([]model.Policy, 0, len(policyObjs))
	for _, o := range policyObjs {
		s.policies = append(s.policies, toPolicy(o))
		s.mutations = append(s.mutations, mutations(o)...)
		s.health = append(s.health, readiness(o)...)
	}
	for i := range s.policies {
		s.byKey[s.policies[i].Key()] = &s.policies[i]
	}
	s.applyReports(denied)
	return s
}

// applyReports turns failing results into violations and fills each
// policy's count and last-scan time.
func (s *snapshot) applyReports(denied hiddenKinds) {
	idx := newResolver(s.policies)
	orphans := map[string]int{}
	for _, rep := range s.reports {
		scope := scopeRef(rep)
		for _, r := range enginekit.Slice(rep.Object, "results") {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			key, found := idx.resolve(enginekit.Str(m, "policy"), enginekit.Str(m, "source"), rep.GetNamespace())
			if !found {
				if v, ok := unreadableViolation(m, scope, denied); ok {
					s.violations = append(s.violations, v)
					continue
				}
				if k, ok := inferKey(enginekit.Str(m, "policy"), enginekit.Str(m, "source")); ok && denied.hides(k) {
					continue // a hidden policy's passing result: already an access notice, not an orphan
				}
				// Stale results for a deleted (or unreadable) policy.
				orphans[enginekit.Str(m, "policy")]++
				continue
			}
			p := s.byKey[key]
			ts := resultTime(m)
			if ts.After(p.LastAudit) {
				p.LastAudit = ts
			}
			res := enginekit.Str(m, "result")
			if res == resultError {
				s.health = append(s.health, model.HealthIssue{
					Engine: Name, Severity: model.SeverityError, Category: "evaluation",
					Object: scope, Message: fmt.Sprintf("%s: %s", key.Name, enginekit.Str(m, "message")),
				})
			}
			if res != resultFail && res != resultWarn && res != resultError {
				continue
			}
			p.ViolationCount++
			s.violations = append(s.violations, model.Violation{
				Engine: Name, Policy: key, Rule: ruleName(m), Target: resultTarget(m, scope),
				Message: enginekit.Str(m, "message"), Enforcement: resultEnforcement(res, p.Enforcement), Timestamp: ts,
				EvalError: res == resultError,
			})
		}
	}
	s.health = append(s.health, orphanHealth(orphans)...)
	if len(s.reports) == 0 && len(s.policies) > 0 {
		s.health = append(s.health, model.HealthIssue{
			Engine: Name, Severity: model.SeverityInfo, Category: "reports",
			Message: "no policy reports yet: background scans may still be running, or reporting is disabled",
		})
	}
}

// resolver maps a report result's policy reference to a policy key.
type resolver struct {
	cluster    map[string][]model.PolicyKey // name -> cluster-scoped policies
	namespaced map[string][]model.PolicyKey // "ns/name" -> namespaced policies
}

func newResolver(ps []model.Policy) resolver {
	r := resolver{cluster: map[string][]model.PolicyKey{}, namespaced: map[string][]model.PolicyKey{}}
	for _, p := range ps {
		if p.Namespace == "" {
			r.cluster[p.Name] = append(r.cluster[p.Name], p.Key())
		} else {
			k := p.Namespace + "/" + p.Name
			r.namespaced[k] = append(r.namespaced[k], p.Key())
		}
	}
	return r
}

// resolve finds the policy a result refers to. Results name policies as
// "name" or "namespace/name"; the source ("kyverno" for classic policies,
// "Kyverno<Kind>" for CEL ones) breaks ties between same-named kinds.
func (r resolver) resolve(ref, source, reportNS string) (model.PolicyKey, bool) {
	var cands []model.PolicyKey
	if ns, name, ok := strings.Cut(ref, "/"); ok {
		cands = r.namespaced[ns+"/"+name]
	} else {
		cands = append(append(cands, r.cluster[ref]...), r.namespaced[reportNS+"/"+ref]...)
	}
	if len(cands) == 0 {
		return model.PolicyKey{}, false
	}
	for _, c := range cands {
		if sourceMatches(source, c.Kind) {
			return c, true
		}
	}
	return cands[0], true
}

func sourceMatches(source, kind string) bool {
	if source == sourceClassic || source == "" {
		return kind == kindClassicCP || kind == kindClassicP
	}
	base := strings.TrimPrefix(source, "Kyverno")
	return kind == base || kind == "Namespaced"+base
}

func orphanHealth(orphans map[string]int) []model.HealthIssue {
	names := make([]string, 0, len(orphans))
	for n := range orphans {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]model.HealthIssue, 0, len(names))
	for _, n := range names {
		out = append(out, model.HealthIssue{
			Engine: Name, Severity: model.SeverityInfo, Category: "reports",
			Message: fmt.Sprintf("%d report results reference policy %q, which is not visible (deleted or forbidden)", orphans[n], n),
		})
	}
	return out
}

func scopeRef(rep *unstructured.Unstructured) model.ObjectRef {
	scope, ok, _ := unstructured.NestedMap(rep.Object, "scope")
	if !ok {
		return model.ObjectRef{}
	}
	return refFrom(scope)
}

func refFrom(m map[string]any) model.ObjectRef {
	g, v := enginekit.ParseAPIVersion(enginekit.Str(m, "apiVersion"))
	return model.ObjectRef{Group: g, Version: v, Kind: enginekit.Str(m, "kind"),
		Namespace: enginekit.Str(m, "namespace"), Name: enginekit.Str(m, "name")}
}

// resultTarget prefers the result's own resource (older aggregated reports)
// over the report scope (per-resource reports).
func resultTarget(m map[string]any, scope model.ObjectRef) model.ObjectRef {
	for _, r := range enginekit.Slice(m, "resources") {
		if rm, ok := r.(map[string]any); ok {
			return refFrom(rm)
		}
	}
	return scope
}

func ruleName(m map[string]any) string {
	if r := enginekit.Str(m, "rule"); r != "" {
		return r
	}
	if props, ok := m["properties"].(map[string]any); ok {
		if i := enginekit.Str(props, "cel.validationIndex"); i != "" {
			return "validation " + i
		}
	}
	return ""
}

func resultTime(m map[string]any) time.Time {
	secs := enginekit.Int(m, "timestamp", "seconds")
	if secs == 0 {
		return time.Time{}
	}
	return time.Unix(int64(secs), 0).UTC()
}

// resultEnforcement: a "warn" result warned; a "fail" did what its policy
// does (blocked under Enforce, only recorded under Audit).
func resultEnforcement(result string, policy model.Enforcement) model.Enforcement {
	if result == resultWarn {
		return model.Enforcement{Mode: model.ModeWarn, Raw: result}
	}
	mode := policy.Mode
	if mode == model.ModeUnknown {
		mode = model.ModeAudit
	}
	return model.Enforcement{Mode: mode, Raw: result}
}

// unreadableViolation keeps a failing result whose policy RBAC hides (e.g.
// a tenant who may read PolicyReports in their namespace but not
// cluster-wide policies). The key is inferred from the result's source and
// reference; enforcement is unknown.
func unreadableViolation(m map[string]any, scope model.ObjectRef, denied hiddenKinds) (model.Violation, bool) {
	res := enginekit.Str(m, "result")
	if res != resultFail && res != resultWarn {
		return model.Violation{}, false
	}
	key, ok := inferKey(enginekit.Str(m, "policy"), enginekit.Str(m, "source"))
	if !ok || !denied.hides(key) {
		return model.Violation{}, false // readable kind: a deleted policy, an orphan
	}
	return model.Violation{
		Engine: Name, Policy: key, Rule: ruleName(m), Target: resultTarget(m, scope),
		Message: enginekit.Str(m, "message"), Enforcement: model.Enforcement{Mode: model.ModeUnknown, Raw: res},
		Timestamp: resultTime(m), PolicyUnreadable: true,
	}, true
}

// inferKey derives a policy key from a report result. Verified on Kyverno
// v1.19: classic results have source "kyverno" and reference a Policy as
// "ns/name", a ClusterPolicy as "name"; CEL results have source
// "Kyverno<Kind>" for both scopes (a NamespacedValidatingPolicy reports
// "KyvernoValidatingPolicy" with "ns/name"). Other sources are not guessed.
func inferKey(ref, source string) (model.PolicyKey, bool) {
	if ref == "" {
		return model.PolicyKey{}, false
	}
	ns, name, namespaced := strings.Cut(ref, "/")
	if !namespaced {
		ns, name = "", ref
	}
	var kind string
	switch {
	case source == sourceClassic:
		kind = kindClassicCP
		if namespaced {
			kind = kindClassicP
		}
	case strings.HasPrefix(source, "Kyverno"):
		kind = strings.TrimPrefix(source, "Kyverno")
		if namespaced {
			kind = "Namespaced" + kind
		}
	default:
		return model.PolicyKey{}, false
	}
	return model.PolicyKey{Engine: Name, Kind: kind, Namespace: ns, Name: name}, true
}

// hiddenKinds maps a policy kind RBAC hides to the one namespace still
// readable ("" when none is).
type hiddenKinds map[string]string

// hides reports whether the policy with key k cannot be read.
func (h hiddenKinds) hides(k model.PolicyKey) bool {
	ns, ok := h[k.Kind]
	return ok && (ns == "" || k.Namespace != ns)
}
