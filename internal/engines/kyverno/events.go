package kyverno

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Verified on Kyverno v1.19: admission-time results are Events with
// reason PolicyViolation and reportingComponent kyverno-admission (the
// "source" field selector matches it; background scans use kyverno-scan).
// Each is emitted twice: on the policy (with the resource in "related") and
// on the resource; only the policy-side one is used. "action" is
// "Resource Blocked" when the request was denied and "Resource Passed" when
// the failure was only recorded (Audit), with messages
// "<Kind> ns/name: [rule] fail (blocked); msg" and "...: fail; msg".
const (
	violationReason = "PolicyViolation"
	admissionSource = "kyverno-admission"
	omitHint        = "Kyverno omits PolicyViolation events (--omitEvents on the admission controller)."
)

// EventSelectors implements engine.EventSource.
func (e *Engine) EventSelectors() []engine.EventSelector {
	return []engine.EventSelector{{FieldSelector: "reason=" + violationReason + ",source=" + admissionSource}}
}

// EventsEnabled implements engine.EventSource.
func (e *Engine) EventsEnabled() (bool, string) {
	if e.omitViolations.Load() {
		return false, omitHint
	}
	return true, ""
}

// ParseEvent implements engine.EventSource.
func (e *Engine) ParseEvent(ev *unstructured.Unstructured) (model.AdmissionEvent, bool) {
	if enginekit.Str(ev.Object, "reason") != violationReason {
		return model.AdmissionEvent{}, false
	}
	if src, _, _ := unstructured.NestedString(ev.Object, "source", "component"); src != admissionSource &&
		enginekit.Str(ev.Object, "reportingComponent") != admissionSource {
		return model.AdmissionEvent{}, false
	}
	involved, _, _ := unstructured.NestedMap(ev.Object, "involvedObject")
	related, hasRelated, _ := unstructured.NestedMap(ev.Object, "related")
	kind := enginekit.Str(involved, "kind")
	if !hasRelated || !isPolicyKind(kind) {
		return model.AdmissionEvent{}, false // the resource-side duplicate
	}
	g, v := enginekit.ParseAPIVersion(enginekit.Str(related, "apiVersion"))
	key := model.PolicyKey{Engine: Name, Kind: kind, Name: enginekit.Str(involved, "name"), Namespace: enginekit.Str(involved, "namespace")}
	out := model.AdmissionEvent{
		Engine: Name, Policy: key,
		Target: model.ObjectRef{Group: g, Version: v, Kind: enginekit.Str(related, "kind"),
			Namespace: enginekit.Str(related, "namespace"), Name: enginekit.Str(related, "name")},
		Message: violationMessage(enginekit.Str(ev.Object, "message")),
		Time:    enginekit.EventTime(ev),
		Count:   enginekit.EventCount(ev),
	}
	out.Enforcement = e.eventEnforcement(key, enginekit.Str(ev.Object, "action"))
	return out, true
}

// Event actions.
const (
	actionBlocked = "Resource Blocked"
	actionPassed  = "Resource Passed"
)

// eventEnforcement maps the event's action: blocked is a denial; passed is
// a recorded failure, in the policy's mode unless that mode would claim a
// denial (e.g. a rule-level Audit override in an Enforce policy).
func (e *Engine) eventEnforcement(key model.PolicyKey, action string) model.Enforcement {
	switch action {
	case actionBlocked:
		return model.Enforcement{Mode: model.ModeEnforce, Raw: action}
	case actionPassed:
		mode := model.ModeAudit
		if p, ok := e.snapshot().byKey[key]; ok && p.Enforcement.Mode == model.ModeWarn {
			mode = model.ModeWarn
		}
		return model.Enforcement{Mode: mode, Raw: action}
	}
	return model.Enforcement{Mode: model.ModeUnknown, Raw: action}
}

func isPolicyKind(kind string) bool {
	_, ok := shortKinds[kind]
	return ok && kind != "PolicyException"
}

// violationMessage drops the "<Kind> ns/name: [rule] fail; " (or
// "fail (blocked); ") prefix that repeats the target and rule.
func violationMessage(m string) string {
	for _, sep := range []string{"fail (blocked); ", "fail; "} {
		if i := strings.Index(m, sep); i >= 0 {
			return m[i+len(sep):]
		}
	}
	return m
}
