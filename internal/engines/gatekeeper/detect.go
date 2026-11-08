package gatekeeper

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engines/enginekit"
)

var deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

const (
	defaultNamespace     = "gatekeeper-system"
	controllerDeployment = "gatekeeper-controller-manager"
	auditDeployment      = "gatekeeper-audit"
	auditIntervalFlag    = "--audit-interval"
	// defaultAuditInterval is Gatekeeper's own default (--audit-interval=60).
	defaultAuditInterval = 60 * time.Second
	systemLabelSelector  = "gatekeeper.sh/system=yes"
	unknownVersion       = "unknown"
)

// Detect implements engine.Engine. Gatekeeper counts as installed when the
// templates.gatekeeper.sh API group is served.
func (e *Engine) Detect(ctx context.Context) (bool, string, error) {
	versions, err := enginekit.PreferredVersions(e.cluster.Discovery(), groupTemplates)
	if err != nil {
		return false, "", fmt.Errorf("discover %s: %w", groupTemplates, err)
	}
	if _, ok := versions[groupTemplates]; !ok {
		return false, "", nil
	}
	e.auditInterval.Store(int64(e.readAuditInterval(ctx)))
	e.intervalRead.Store(true)
	return true, e.version(ctx), nil
}

// readAuditInterval parses --audit-interval (seconds) from the audit
// deployment, falling back to Gatekeeper's default when it is not visible.
// 0 means periodic audit is disabled.
func (e *Engine) readAuditInterval(ctx context.Context) time.Duration {
	d := e.cluster.Dynamic().Resource(deploymentsGVR).Namespace(defaultNamespace)
	obj, err := d.Get(ctx, auditDeployment, metav1.GetOptions{})
	if err != nil {
		return defaultAuditInterval
	}
	if v, ok := enginekit.ContainerArg(obj, auditIntervalFlag); ok {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultAuditInterval
}

// AuditInterval implements engine.Auditor; 0 when audit is disabled.
func (e *Engine) AuditInterval() time.Duration {
	if v := e.auditInterval.Load(); v >= 0 && e.intervalRead.Load() {
		return time.Duration(v)
	}
	return defaultAuditInterval
}

// version reads the controller image tag. It degrades to "unknown" when RBAC
// hides deployments, because detection must not fail on a read-only role.
func (e *Engine) version(ctx context.Context) string {
	d := e.cluster.Dynamic().Resource(deploymentsGVR)
	if obj, err := d.Namespace(defaultNamespace).Get(ctx, controllerDeployment, metav1.GetOptions{}); err == nil {
		if v := enginekit.ContainerImageTag(obj, "gatekeeper"); v != "" {
			e.namespace.Store(obj.GetNamespace())
			e.readEventFlags(obj)
			return v
		}
	}
	list, err := d.List(ctx, metav1.ListOptions{LabelSelector: systemLabelSelector, Limit: 20})
	if err != nil {
		return unknownVersion
	}
	version := unknownVersion
	for i := range list.Items {
		d := &list.Items[i]
		v := enginekit.ContainerImageTag(d, "gatekeeper")
		if v == "" {
			continue
		}
		if version == unknownVersion {
			version = v
			e.namespace.Store(d.GetNamespace())
		}
		// Admission event flags live on the webhook (controller-manager),
		// not on the audit Deployment; unknown leaves events assumed on.
		if strings.Contains(d.GetName(), "controller-manager") {
			e.readEventFlags(d)
			return v
		}
	}
	return version
}
