package gatekeeper

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// Admission event reasons Gatekeeper's webhook emits (FailedAdmission seen
// live on v3.23; WarningAdmission and DryrunViolation per its source).
var admissionReasons = []string{"FailedAdmission", "WarningAdmission", "DryrunViolation"}

const (
	webhookSource      = "gatekeeper-webhook"
	emitAdmissionFlag  = "--emit-admission-events"
	involvedNSFlag     = "--admission-events-involved-namespace"
	eventsEnabledHint  = "Gatekeeper admission events are off. Enable them with --emit-admission-events on the controller-manager (Helm: --set emitAdmissionEvents=true)."
	eventMessagePrefix = "Message: "
)

func (e *Engine) readEventFlags(deploy *unstructured.Unstructured) {
	e.eventsKnown.Store(true)
	e.eventsOn.Store(flagTrue(deploy, emitAdmissionFlag))
	e.eventsInvolvedNS.Store(flagTrue(deploy, involvedNSFlag))
}

// flagTrue reads a Go boolean flag: "--flag" and "--flag=true" set it,
// "--flag=false" clears it. ("--flag true" is a bare flag followed by an
// argument for Go's flag package, so it also reads as set.)
func flagTrue(deploy *unstructured.Unstructured, flag string) bool {
	on := false
	for _, c := range enginekit.Slice(deploy.Object, "spec", "template", "spec", "containers") {
		m, _ := c.(map[string]any)
		cmd, _, _ := unstructured.NestedStringSlice(m, "command")
		args, _, _ := unstructured.NestedStringSlice(m, "args")
		for _, a := range append(cmd, args...) {
			if a == flag {
				on = true
			} else if v, ok := strings.CutPrefix(a, flag+"="); ok {
				on = v == "true" || v == "1"
			}
		}
	}
	return on
}

// EventsEnabled implements engine.EventSource. When the controller-manager
// was not visible at Detect it assumes on, so events still show if they exist.
func (e *Engine) EventsEnabled() (bool, string) {
	if e.eventsKnown.Load() && !e.eventsOn.Load() {
		return false, eventsEnabledHint
	}
	return true, ""
}

// EventSelectors implements engine.EventSource. Gatekeeper writes events in
// its own namespace unless --admission-events-involved-namespace is set.
func (e *Engine) EventSelectors() []engine.EventSelector {
	ns := e.PlatformInfo().Namespace
	if e.eventsInvolvedNS.Load() {
		ns = ""
	}
	out := make([]engine.EventSelector, 0, len(admissionReasons))
	for _, r := range admissionReasons {
		out = append(out, engine.EventSelector{Namespace: ns, FieldSelector: "reason=" + r + ",source=" + webhookSource})
	}
	return out
}

// ParseEvent implements engine.EventSource from the event's annotations.
func (e *Engine) ParseEvent(ev *unstructured.Unstructured) (model.AdmissionEvent, bool) {
	ann := ev.GetAnnotations()
	if !slices.Contains(admissionReasons, enginekit.Str(ev.Object, "reason")) ||
		ann["process"] != "admission" || ann["constraint_kind"] == "" {
		return model.AdmissionEvent{}, false
	}
	action := ann["constraint_action"]
	g, v := ann["resource_group"], ann["resource_api_version"]
	out := model.AdmissionEvent{
		Engine:      Name,
		Policy:      model.PolicyKey{Engine: Name, Kind: ann["constraint_kind"], Name: ann["constraint_name"], Namespace: ann["constraint_namespace"]},
		Enforcement: model.Enforcement{Mode: normalizeAction(action), Raw: action},
		User:        ann["request_username"],
		Target: model.ObjectRef{Group: g, Version: v, Kind: ann["resource_kind"],
			Namespace: ann["resource_namespace"], Name: ann["resource_name"]},
		Message: eventMessage(enginekit.Str(ev.Object, "message")),
		Time:    enginekit.EventTime(ev),
		Count:   enginekit.EventCount(ev),
	}
	return out, true
}

// eventMessage keeps the constraint's own message: Gatekeeper prefixes it
// with the webhook, namespace and constraint, which other columns show.
func eventMessage(m string) string {
	if i := strings.Index(m, eventMessagePrefix); i >= 0 {
		return m[i+len(eventMessagePrefix):]
	}
	return m
}
