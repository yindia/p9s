package gatekeeper

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
)

// webhookOperation is the byPod operation of admission webhook replicas.
const webhookOperation = "webhook"

// PlatformInfo implements engine.Platform. Gatekeeper 3.x does not use a
// Lease for audit (verified on v3.23: no Lease exists), so there is no
// audit leader to show.
func (e *Engine) PlatformInfo() engine.PlatformInfo {
	ns, _ := e.namespace.Load().(string)
	if ns == "" {
		ns = defaultNamespace
	}
	return engine.PlatformInfo{
		Namespace:       ns,
		WebhookSelector: map[string]string{"gatekeeper.sh/system": "yes"},
		AuditSource:     "constraint status",
	}
}

// PolicyStatus implements engine.PolicyStatuser. Templates are loaded when
// their CRD exists and no pod reports an error; a constraint is not enforced
// when some webhook replica is missing from its byPod or reports
// enforced: false.
func (e *Engine) PolicyStatus() engine.PolicyStatus {
	s := e.snapshot()
	st := engine.PolicyStatus{Total: len(s.templates)}
	for _, t := range s.templates {
		created, _, _ := unstructured.NestedBool(t.Object, "status", "created")
		errs := len(templateErrors(t)) > 0
		switch {
		case !created:
			st.MissingCRD++
		case errs:
			st.CompileErrors++
		default:
			st.Loaded++
		}
	}
	webhookPods := map[string]bool{}
	for _, c := range s.constraints {
		for id := range byPod(c, webhookOperation) {
			webhookPods[id] = true
		}
	}
	for _, c := range s.constraints {
		enforced := byPod(c, webhookOperation)
		for id := range webhookPods {
			if on, reported := enforced[id]; !reported || !on {
				st.NotEnforced++
				break
			}
		}
	}
	return st
}

// byPod returns the pod IDs that report operation op for o, mapped to
// whether they enforce it.
func byPod(o *unstructured.Unstructured, op string) map[string]bool {
	out := map[string]bool{}
	for _, p := range enginekit.Slice(o.Object, "status", "byPod") {
		m, _ := p.(map[string]any)
		ops, _, _ := unstructured.NestedStringSlice(m, "operations")
		for _, o := range ops {
			if o == op {
				enforced, _, _ := unstructured.NestedBool(m, "enforced")
				out[enginekit.Str(m, "id")] = enforced
			}
		}
	}
	return out
}
