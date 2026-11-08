package kyverno

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

var deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

const (
	defaultNamespace    = "kyverno"
	admissionDeployment = "kyverno-admission-controller"
	reportsDeployment   = "kyverno-reports-controller"
	scanIntervalFlag    = "--backgroundScanInterval"
	// defaultScanInterval is Kyverno's own default background scan interval.
	defaultScanInterval = time.Hour
	partOfSelector      = "app.kubernetes.io/part-of=kyverno"
	unknownVersion      = "unknown"
)

// Detect implements engine.Engine. Kyverno counts as installed when either
// of its policy API groups is served.
func (e *Engine) Detect(ctx context.Context) (bool, string, error) {
	versions, err := enginekit.PreferredVersions(e.cluster.Discovery(), groupClassic, groupCEL)
	if err != nil {
		return false, "", fmt.Errorf("discover kyverno API groups: %w", err)
	}
	if len(versions) == 0 {
		return false, "", nil
	}
	e.scanInterval.Store(int64(e.readScanInterval(ctx)))
	return true, e.version(ctx), nil
}

// readScanInterval parses --backgroundScanInterval from the reports
// controller, falling back to Kyverno's default when it is not visible.
func (e *Engine) readScanInterval(ctx context.Context) time.Duration {
	obj, err := e.cluster.Dynamic().Resource(deploymentsGVR).Namespace(defaultNamespace).Get(ctx, reportsDeployment, metav1.GetOptions{})
	if err != nil {
		return defaultScanInterval
	}
	if v, ok := enginekit.ContainerArg(obj, scanIntervalFlag); ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultScanInterval
}

// AuditInterval implements engine.Auditor: Kyverno's background scan.
func (e *Engine) AuditInterval() time.Duration {
	if v := e.scanInterval.Load(); v > 0 {
		return time.Duration(v)
	}
	return defaultScanInterval
}

// version reads the admission controller's image tag, degrading to
// "unknown" when RBAC hides deployments.
func (e *Engine) version(ctx context.Context) string {
	d := e.cluster.Dynamic().Resource(deploymentsGVR)
	if obj, err := d.Namespace(defaultNamespace).Get(ctx, admissionDeployment, metav1.GetOptions{}); err == nil {
		if v := enginekit.ContainerImageTag(obj, "kyverno"); v != "" {
			e.namespace.Store(obj.GetNamespace())
			e.readOmitEvents(obj)
			return v
		}
	}
	list, err := d.List(ctx, metav1.ListOptions{LabelSelector: partOfSelector, Limit: 20})
	if err != nil {
		return unknownVersion
	}
	for i := range list.Items {
		if v := enginekit.ContainerImageTag(&list.Items[i], "kyverno/kyverno"); v != "" {
			e.namespace.Store(list.Items[i].GetNamespace())
			e.readOmitEvents(&list.Items[i])
			return v
		}
	}
	return unknownVersion
}

// Policies implements engine.Engine.
func (e *Engine) Policies() ([]model.Policy, error) {
	return append([]model.Policy(nil), e.snapshot().policies...), nil
}

// Violations implements engine.Engine: failing, warning and erroring report
// results. Kyverno reports are not capped, so nothing is truncated.
func (e *Engine) Violations() ([]model.Violation, error) {
	return append([]model.Violation(nil), e.snapshot().violations...), nil
}

// Mutations implements engine.Engine.
func (e *Engine) Mutations() ([]model.Mutation, error) {
	return append([]model.Mutation(nil), e.snapshot().mutations...), nil
}

// Health implements engine.Engine.
func (e *Engine) Health() ([]model.HealthIssue, error) {
	return append([]model.HealthIssue(nil), e.snapshot().health...), nil
}

// readOmitEvents records whether the admission controller drops
// PolicyViolation events (--omitEvents takes a comma-separated list).
func (e *Engine) readOmitEvents(d *unstructured.Unstructured) {
	omit, _ := enginekit.ContainerArg(d, "--omitEvents")
	e.omitViolations.Store(slices.Contains(strings.Split(omit, ","), violationReason))
}
