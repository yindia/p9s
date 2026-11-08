// Package engine defines the contract every policy engine implements and the
// registry the application uses to discover them. It must not import any UI
// or engine-specific package.
package engine

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"

	"github.com/yindia/p9s/internal/model"
)

// ErrNotSupported is returned by optional methods an engine does not implement.
var ErrNotSupported = errors.New("not supported by this engine")

// Cluster is the minimal set of clients an engine gets at construction.
type Cluster interface {
	Dynamic() dynamic.Interface
	Discovery() discovery.DiscoveryInterface
}

// Generator is optionally implemented by engines whose objects other
// engines generate (native ValidatingAdmissionPolicies from Gatekeeper
// templates or Kyverno policies): it names what was generated from a
// source policy, so that policy's detail can show it.
type Generator interface {
	GeneratedFrom(source model.PolicyKey) []string
}

// HomeNamespacer is optionally implemented by a Cluster: the kubeconfig
// context's namespace. Engines fall back to watching namespaced resources
// there when RBAC denies listing them cluster-wide (a tenant's access).
type HomeNamespacer interface {
	HomeNamespace() string
}

// HomeNamespace returns c's home namespace, or "" when it has none.
func HomeNamespace(c any) string {
	if h, ok := c.(HomeNamespacer); ok {
		return h.HomeNamespace()
	}
	return ""
}

// Factory builds an engine bound to a cluster. It is called again on every
// context switch, so engines never need to handle a client change.
type Factory func(Cluster) Engine

// Notify is called by an engine whenever its data changed. It must be cheap
// and non-blocking; callers coalesce bursts.
type Notify func()

// Engine is implemented by every policy engine.
//
// Lifecycle: Detect, then Start (only when installed), then any number of
// reads, then Stop. Read methods must be safe for concurrent use and return
// snapshots the caller may keep.
type Engine interface {
	// Name is the stable identifier, e.g. "gatekeeper".
	Name() string
	// Prefix namespaces engine-specific commands, e.g. "gk" for ":gk/ct".
	Prefix() string
	Capabilities() Capabilities

	// Detect reports whether the engine is installed in the cluster, using
	// discovery. An error means detection itself failed (not "absent").
	Detect(ctx context.Context) (installed bool, version string, err error)

	Start(ctx context.Context, informers dynamicinformer.DynamicSharedInformerFactory, notify Notify) error
	Stop()
	// SyncStatus reports whether every informer finished its initial list.
	// Until then reads are partial. It is false before Start. Engines notify
	// once when they become synced, even if every list was empty.
	SyncStatus() SyncStatus

	Policies() ([]model.Policy, error)
	Violations() ([]model.Violation, error)
	// Mutations returns ErrNotSupported unless Capabilities().Mutations.
	Mutations() ([]model.Mutation, error)
	Health() ([]model.HealthIssue, error)
	// AccessIssues lists resources RBAC prevented the engine from reading.
	AccessIssues() []model.AccessIssue

	// Views returns the engine-specific views and their ":" commands.
	Views() []View

	// Evaluator returns ErrNotSupported unless Capabilities().Evaluator.
	Evaluator() (Evaluator, error)
	// Actions returns ErrNotSupported unless Capabilities().Actions.
	Actions() (Actions, error)
}

// SyncStatus is an engine's cache state.
type SyncStatus struct {
	Synced bool
	// Pending names the resources (group-resource form) still loading.
	Pending []string
}

// Capabilities lets the UI hide what an engine cannot do instead of showing
// errors.
type Capabilities struct {
	Policies   bool
	Violations bool
	Mutations  bool
	Health     bool
	Evaluator  bool
	Actions    bool
}

// KindNamer is optionally implemented by engines with long policy kinds to
// give tables a short alias (e.g. "CPol" for ClusterPolicy). Detail views
// keep the full kind.
type KindNamer interface {
	ShortKind(kind string) string
}

// Auditor is optionally implemented by engines with a background audit, to
// say how often it runs, so renderers can flag stale results. 0 means
// periodic audit is disabled: results are then never graded stale.
type Auditor interface {
	AuditInterval() time.Duration
}

// Platform is optionally implemented by engines that run in the cluster, so
// shared health checks can find their webhooks, controllers and leader
// election. All of it is read from the API, never from the engine itself.
type Platform interface {
	PlatformInfo() PlatformInfo
}

// PlatformInfo locates an engine's moving parts.
type PlatformInfo struct {
	// Namespace holds the engine's Deployments, Pods, EndpointSlices and
	// Leases.
	Namespace string
	// WebhookSelector matches the labels of the engine's (Validating|
	// Mutating)WebhookConfigurations. Webhooks whose service lives in
	// Namespace also count as the engine's.
	WebhookSelector map[string]string
	// AuditLease names the Lease held by the audit leader ("" if the engine
	// does not use leader election for audit).
	AuditLease string
	// AuditSource says where audit results come from, e.g. "policy reports".
	AuditSource string
}

// PolicyStatuser is optionally implemented to report how many policies are
// actually in effect.
type PolicyStatuser interface {
	PolicyStatus() PolicyStatus
}

// PolicyStatus counts policies by load state. Counts that do not apply to an
// engine are left at zero.
type PolicyStatus struct {
	Total, Loaded int
	// NotEnforced counts policies some webhook replica does not enforce.
	NotEnforced int
	// CompileErrors counts policies whose code failed to compile.
	CompileErrors int
	// MissingCRD counts templates whose generated CRD does not exist.
	MissingCRD int
	// NotReady counts policies the engine reports as not ready.
	NotReady int
}

// EventSource is optionally implemented by engines that record admission
// decisions as Kubernetes Events, for the "recent" tab of :v.
type EventSource interface {
	// EventSelectors are the Events to watch: a namespace ("" for all) and
	// a server-side field selector, so unrelated events are never cached.
	EventSelectors() []EventSelector
	// ParseEvent turns one Event into an admission event; ok is false for
	// events that are not admission decisions of this engine.
	ParseEvent(ev *unstructured.Unstructured) (model.AdmissionEvent, bool)
	// EventsEnabled reports whether the engine emits admission events; when
	// not, hint says how to turn them on.
	EventsEnabled() (enabled bool, hint string)
}

// EventSelector selects Events to watch.
type EventSelector struct {
	Namespace     string
	FieldSelector string
}

// Evaluator evaluates policies against objects without a cluster round-trip.
type Evaluator interface {
	Evaluate(ctx context.Context, policies, objects []*unstructured.Unstructured) ([]model.Violation, error)
}

// Actions are the write operations an engine supports. Callers must gate
// them behind read-only mode and a confirmation prompt.
type Actions interface {
	SetEnforcement(ctx context.Context, policy model.PolicyKey, mode model.EnforcementMode) error
}
