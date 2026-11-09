// Package model holds engine-neutral types shared by every policy engine and
// the UI. It must not import any engine-specific or UI package.
package model

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EnforcementMode is the normalized enforcement mode of a policy or violation.
type EnforcementMode string

const (
	ModeAudit   EnforcementMode = "audit"
	ModeWarn    EnforcementMode = "warn"
	ModeEnforce EnforcementMode = "enforce"
	// ModeMutate is for policies that change objects instead of judging them.
	ModeMutate EnforcementMode = "mutate"
	// ModeUnknown means the policy has no enforcement mode (shown as "n/a").
	ModeUnknown EnforcementMode = "unknown"
)

// Label is the one-word form tables show: enforce, warn, audit, mutate or n/a.
func (m EnforcementMode) Label() string {
	if m == ModeUnknown || m == "" {
		return "n/a"
	}
	return string(m)
}

// Enforcement keeps the normalized mode next to the engine's raw value
// (for example Gatekeeper's "deny" or "dryrun").
type Enforcement struct {
	Mode EnforcementMode
	Raw  string
}

// String is the detail form: the normalised word plus the engine's own term,
// e.g. "enforce (engine: deny)".
func (e Enforcement) String() string {
	if e.Raw == "" || e.Raw == string(e.Mode) {
		return e.Mode.Label()
	}
	return fmt.Sprintf("%s (engine: %s)", e.Mode.Label(), e.Raw)
}

// SourceLanguage identifies how a policy's logic is expressed.
type SourceLanguage string

const (
	LangRego SourceLanguage = "rego"
	LangCEL  SourceLanguage = "cel"
	LangYAML SourceLanguage = "yaml"
	LangNone SourceLanguage = ""
)

// Source is a snippet of policy logic.
type Source struct {
	Language SourceLanguage
	Code     string
	// Origin describes where the code came from, e.g. "ConstraintTemplate k8srequiredlabels".
	Origin string
}

// ObjectRef points at a Kubernetes object.
type ObjectRef struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// APIVersion returns group/version, or just version for the core group.
func (r ObjectRef) APIVersion() string {
	if r.Group == "" {
		return r.Version
	}
	return r.Group + "/" + r.Version
}

func (r ObjectRef) String() string {
	if r.Namespace == "" {
		return fmt.Sprintf("%s/%s", r.Kind, r.Name)
	}
	return fmt.Sprintf("%s/%s/%s", r.Kind, r.Namespace, r.Name)
}

// PolicyKey uniquely identifies a policy across engines. Its JSON names are
// part of p9s/v1 output (get -o json, reports); encoding/json reads older
// reports written with Go field names too, as it matches names case-blind.
type PolicyKey struct {
	Engine    string `json:"engine"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func (k PolicyKey) String() string {
	if k.Namespace == "" {
		return fmt.Sprintf("%s:%s/%s", k.Engine, k.Kind, k.Name)
	}
	return fmt.Sprintf("%s:%s/%s/%s", k.Engine, k.Kind, k.Namespace, k.Name)
}

// Policy is an engine-neutral view of a single policy (a Gatekeeper
// constraint, a Kyverno policy, ...).
type Policy struct {
	Engine         string
	Kind           string
	Name           string
	Namespace      string
	Enforcement    Enforcement
	ViolationCount int
	// ViolationsKnown is false when the engine has no audit source for this
	// policy (never audited, or it mutates instead of validating). Views then
	// show "-" instead of a misleading 0.
	ViolationsKnown bool
	// LastAudit is zero when the policy has never been audited.
	LastAudit time.Time
	Sources   []Source
	// Guidance is how-to-fix text from the policy's metadata; empty when
	// the policy carries none.
	Guidance []GuidanceItem
	// Severity is from policy metadata (e.g. policies.kyverno.io/severity);
	// "" when the policy declares none.
	Severity string
	// Matches is what kinds the policy selects, as written in its spec;
	// MatchesAll is set when it selects every kind. Both are empty when the
	// engine could not tell (MatchKnown false).
	Matches    []MatchRule
	MatchesAll bool
	MatchKnown bool
	Raw        *unstructured.Unstructured
}

// MatchRule is one kind or resource a policy selects. Kind or Resource is
// set (Kyverno names kinds, admission rules name resources); Group "*"
// matches every group.
type MatchRule struct {
	Group    string `json:"group,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Resource string `json:"resource,omitempty"`
}

// APIResource is one kind the API server serves.
type APIResource struct {
	Group      string `json:"group,omitempty"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Namespaced bool   `json:"namespaced"`
}

// GuidanceItem is remediation text from policy metadata, e.g. a
// description annotation, with where it came from.
type GuidanceItem struct {
	From  string // e.g. "template annotation description"
	Title string
	Text  string
}

// Key returns the policy's unique key.
func (p Policy) Key() PolicyKey {
	return PolicyKey{Engine: p.Engine, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name}
}

// Violation is one reported policy violation.
type Violation struct {
	Engine string
	Policy PolicyKey
	Rule   string
	Target ObjectRef
	// Owner is the target's top-level controller (Pod → ReplicaSet →
	// Deployment, …), resolved by shared views; the target itself when it
	// has none, zero when not resolved.
	Owner       ObjectRef
	Message     string
	Enforcement Enforcement
	Timestamp   time.Time
	// Truncated is set when the engine reported fewer violations than it counted.
	Truncated bool
	// EvalError marks a result where the policy failed to evaluate (e.g. a
	// Kyverno "error" result) rather than found the object non-compliant.
	EvalError bool
	// PolicyUnreadable is set when RBAC hides the policy object: the key
	// is inferred from the result and enforcement is unknown.
	PolicyUnreadable bool
}

// Mutation is an engine-neutral view of a mutating policy.
type Mutation struct {
	Engine    string
	Kind      string
	Name      string
	Namespace string
	// Targets is a human-readable description of what the mutation applies to.
	Targets string
	// Location is the field path being mutated, if the engine has one.
	Location string
	Raw      *unstructured.Unstructured
}

// Severity of a health issue.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// HealthIssue is a problem with an engine or one of its resources.
type HealthIssue struct {
	Engine   string
	Severity Severity
	Category string
	Object   ObjectRef
	Message  string
}

// AccessIssue records a resource the current identity may not read.
type AccessIssue struct {
	Engine   string
	Resource string
	Verb     string
	Message  string
	// OnlyNamespace is set when the cluster-wide list was denied but the
	// resource is read in this one namespace instead (a tenant's access).
	OnlyNamespace string
}

func (a AccessIssue) String() string {
	if a.OnlyNamespace != "" {
		return fmt.Sprintf("%s: cannot %s %s cluster-wide; reading namespace %s only", a.Engine, a.Verb, a.Resource, a.OnlyNamespace)
	}
	return fmt.Sprintf("%s: cannot %s %s: %s", a.Engine, a.Verb, a.Resource, a.Message)
}

// KubeContext is a kubeconfig context as shown by ":ctx".
type KubeContext struct {
	Name      string
	Cluster   string
	User      string
	Namespace string
	Current   bool
}

// AdmissionEvent is one admission-time policy event (a denial, warning or
// dry-run hit recorded by an engine as a Kubernetes Event).
type AdmissionEvent struct {
	// ID identifies the Event object ("namespace/name"); set by the watcher.
	ID          string
	Engine      string
	Policy      PolicyKey
	Enforcement Enforcement
	// User is the requesting user or service account, when the engine
	// records it.
	User    string
	Target  ObjectRef
	Message string
	// Time is when it last happened; Count how often (Events aggregate).
	Time  time.Time
	Count int
}
