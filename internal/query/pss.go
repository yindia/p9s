package query

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/model"
)

// PSSLevel is a Pod Security Standards profile.
type PSSLevel string

// PSS levels.
const (
	PSSBaseline   PSSLevel = "baseline"
	PSSRestricted PSSLevel = "restricted"
)

// PSSControl is one control of the Pod Security Standards (as named in the
// Kubernetes documentation).
type PSSControl struct {
	Level PSSLevel `json:"level"`
	Name  string   `json:"name"`
}

// PSSControls in documentation order.
var PSSControls = []PSSControl{
	{PSSBaseline, "HostProcess"}, {PSSBaseline, "Host Namespaces"}, {PSSBaseline, "Privileged Containers"},
	{PSSBaseline, "Capabilities"}, {PSSBaseline, "HostPath Volumes"}, {PSSBaseline, "Host Ports"},
	{PSSBaseline, "AppArmor"}, {PSSBaseline, "SELinux"}, {PSSBaseline, "/proc Mount Type"},
	{PSSBaseline, "Seccomp"}, {PSSBaseline, "Sysctls"},
	{PSSRestricted, "Volume Types"}, {PSSRestricted, "Privilege Escalation"}, {PSSRestricted, "Running as Non-root"},
	{PSSRestricted, "Running as Non-root user"}, {PSSRestricted, "Seccomp"}, {PSSRestricted, "Capabilities"},
}

// kyvernoPSS maps the Kyverno policy library's PSS policies (kyverno-policies
// chart) by name. A policy only counts when its own metadata says it is a
// PSS policy (policies.kyverno.io/category contains "Pod Security
// Standards"), so a same-named custom policy is never claimed.
var kyvernoPSS = map[string]PSSControl{
	"disallow-host-process":          {PSSBaseline, "HostProcess"},
	"disallow-host-namespaces":       {PSSBaseline, "Host Namespaces"},
	"disallow-privileged-containers": {PSSBaseline, "Privileged Containers"},
	"disallow-capabilities":          {PSSBaseline, "Capabilities"},
	"disallow-host-path":             {PSSBaseline, "HostPath Volumes"},
	"disallow-host-ports":            {PSSBaseline, "Host Ports"},
	"restrict-apparmor-profiles":     {PSSBaseline, "AppArmor"},
	"disallow-selinux":               {PSSBaseline, "SELinux"},
	"disallow-proc-mount":            {PSSBaseline, "/proc Mount Type"},
	"restrict-seccomp":               {PSSBaseline, "Seccomp"},
	"restrict-sysctls":               {PSSBaseline, "Sysctls"},
	"restrict-volume-types":          {PSSRestricted, "Volume Types"},
	"disallow-privilege-escalation":  {PSSRestricted, "Privilege Escalation"},
	"require-run-as-nonroot":         {PSSRestricted, "Running as Non-root"},
	"require-run-as-non-root-user":   {PSSRestricted, "Running as Non-root user"},
	"restrict-seccomp-strict":        {PSSRestricted, "Seccomp"},
	"disallow-capabilities-strict":   {PSSRestricted, "Capabilities"},
}

// gatekeeperPSS maps gatekeeper-library pod-security-policy templates by
// constraint kind. Templates whose restricted behaviour depends on their
// parameters (capabilities, seccomp, users) are claimed for the control
// they always enforce only.
var gatekeeperPSS = map[string]PSSControl{
	"K8sPSPHostProcess":                       {PSSBaseline, "HostProcess"},
	"K8sPSPHostNamespace":                     {PSSBaseline, "Host Namespaces"},
	"K8sPSPPrivilegedContainer":               {PSSBaseline, "Privileged Containers"},
	"K8sPSPCapabilities":                      {PSSBaseline, "Capabilities"},
	"K8sPSPHostFilesystem":                    {PSSBaseline, "HostPath Volumes"},
	"K8sPSPHostNetworkingPorts":               {PSSBaseline, "Host Ports"},
	"K8sPSPAppArmor":                          {PSSBaseline, "AppArmor"},
	"K8sPSPSELinuxV2":                         {PSSBaseline, "SELinux"},
	"K8sPSPProcMount":                         {PSSBaseline, "/proc Mount Type"},
	"K8sPSPSeccomp":                           {PSSBaseline, "Seccomp"},
	"K8sPSPSeccompV2":                         {PSSBaseline, "Seccomp"},
	"K8sPSPForbiddenSysctls":                  {PSSBaseline, "Sysctls"},
	"K8sPSPVolumeTypes":                       {PSSRestricted, "Volume Types"},
	"K8sPSPAllowPrivilegeEscalationContainer": {PSSRestricted, "Privilege Escalation"},
	"K8sPSPAllowedUsers":                      {PSSRestricted, "Running as Non-root"},
}

// Coverage states.
const (
	CoverageEnforced = "enforced"
	CoverageAudit    = "audit only"
	CoverageMissing  = "missing"
)

// ControlCoverage is one PSS control and the policies that implement it.
// Caveats say why the status may overstate coverage.
type ControlCoverage struct {
	PSSControl
	Status   string            `json:"status"`
	Policies []model.PolicyKey `json:"policies,omitempty"`
	Caveats  []string          `json:"caveats,omitempty"`
}

// Caveats.
const (
	CaveatParameters = "as strict as its parameters (not checked)"
	CaveatScoped     = "has namespace or resource exclusions"
)

// paramDependent templates enforce a PSS control only as strictly as their
// parameters say.
var paramDependent = map[string]bool{
	"K8sPSPCapabilities": true, "K8sPSPSeccomp": true, "K8sPSPSeccompV2": true, "K8sPSPVolumeTypes": true,
	"K8sPSPHostFilesystem": true, "K8sPSPHostNetworkingPorts": true, "K8sPSPAppArmor": true,
	"K8sPSPSELinuxV2": true, "K8sPSPForbiddenSysctls": true, "K8sPSPProcMount": true,
}

// PSSCoverage maps policies to controls from their metadata only. Policies
// that map to no control are returned as uncategorised: nothing is guessed.
func PSSCoverage(ps []model.Policy) (controls []ControlCoverage, uncategorised []model.PolicyKey) {
	byControl := map[PSSControl][]model.Policy{}
	for _, p := range ps {
		if p.Enforcement.Mode == model.ModeUnknown || p.Enforcement.Mode == model.ModeMutate {
			uncategorised = append(uncategorised, p.Key()) // validates nothing it can claim
			continue
		}
		if c, ok := pssControl(p); ok {
			byControl[c] = append(byControl[c], p)
			continue
		}
		uncategorised = append(uncategorised, p.Key())
	}
	for _, c := range PSSControls {
		cc := ControlCoverage{PSSControl: c, Status: CoverageMissing}
		caveats := map[string]bool{}
		for _, p := range byControl[c] {
			cc.Policies = append(cc.Policies, p.Key())
			switch {
			case p.Enforcement.Mode == model.ModeEnforce:
				cc.Status = CoverageEnforced
			case cc.Status == CoverageMissing:
				cc.Status = CoverageAudit
			}
			if p.Engine == "gatekeeper" && paramDependent[p.Kind] {
				caveats[CaveatParameters] = true
			}
			if hasExclusions(p) {
				caveats[CaveatScoped] = true
			}
		}
		for _, k := range []string{CaveatParameters, CaveatScoped} {
			if caveats[k] {
				cc.Caveats = append(cc.Caveats, k)
			}
		}
		controls = append(controls, cc)
	}
	sort.Slice(uncategorised, func(i, j int) bool { return uncategorised[i].String() < uncategorised[j].String() })
	return controls, uncategorised
}

const kyvernoCategory = "policies.kyverno.io/category"

func pssControl(p model.Policy) (PSSControl, bool) {
	switch p.Engine {
	case "kyverno":
		// Only cluster-wide, ready library policies cover the cluster.
		if p.Raw == nil || p.Namespace != "" || !strings.Contains(p.Raw.GetAnnotations()[kyvernoCategory], "Pod Security Standards") ||
			notReady(p.Raw) {
			return PSSControl{}, false
		}
		c, ok := kyvernoPSS[p.Name]
		return c, ok
	case "gatekeeper":
		c, ok := gatekeeperPSS[p.Kind]
		if ok && p.Kind == "K8sPSPAllowedUsers" && !mustRunAsNonRoot(p.Raw) {
			return PSSControl{}, false // the template allows any user unless told otherwise
		}
		return c, ok
	}
	return PSSControl{}, false
}

// notReady reads a Kyverno policy's Ready condition.
func notReady(o *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" && m["status"] == "False" {
			return true
		}
	}
	return false
}

func mustRunAsNonRoot(o *unstructured.Unstructured) bool {
	if o == nil {
		return false
	}
	rule, _, _ := unstructured.NestedString(o.Object, "spec", "parameters", "runAsUser", "rule")
	return rule == "MustRunAsNonRoot"
}

// hasExclusions reports namespace or resource carve-outs written in the
// policy itself (Config-level and webhook exclusions are not read here).
func hasExclusions(p model.Policy) bool {
	if p.Raw == nil {
		return false
	}
	o := p.Raw.Object
	for _, path := range [][]string{
		{"spec", "match", "excludedNamespaces"}, {"spec", "match", "namespaces"}, {"spec", "match", "namespaceSelector"},
		{"spec", "matchConstraints", "namespaceSelector"}, {"spec", "matchConstraints", "excludeResourceRules"},
	} {
		if v, ok, _ := unstructured.NestedFieldNoCopy(o, path...); ok && !empty(v) {
			return true
		}
	}
	rules, _, _ := unstructured.NestedSlice(o, "spec", "rules")
	for _, r := range rules {
		m, _ := r.(map[string]any)
		if ex, ok := m["exclude"]; ok && !empty(ex) {
			return true
		}
	}
	return false
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}
