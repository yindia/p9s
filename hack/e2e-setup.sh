#!/usr/bin/env bash
# Creates a kind cluster with Gatekeeper and the gatekeeper-library
# "general" and "pod-security-policy" policies, plus a few workloads that
# violate them so audit has something to report.
set -euo pipefail

CLUSTER="${CLUSTER:-p9s}"
KYVERNO="${KYVERNO:-1}"                         # also install Kyverno + kyverno-policies
GATEKEEPER_VERSION="${GATEKEEPER_VERSION:-}"   # empty = latest chart
LIBRARY_REF="${LIBRARY_REF:-master}"
WORK="${WORK:-$(mktemp -d)}"
CTX="kind-${CLUSTER}"
k() { kubectl --context "$CTX" "$@"; }

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --wait 120s
fi

helm repo add gatekeeper https://open-policy-agent.github.io/gatekeeper/charts --force-update >/dev/null
helm upgrade --install gatekeeper gatekeeper/gatekeeper \
  --kube-context "$CTX" --namespace gatekeeper-system --create-namespace \
  ${GATEKEEPER_VERSION:+--version "$GATEKEEPER_VERSION"} \
  --set auditInterval=30 --set constraintViolationsLimit=20 \
  --set emitAdmissionEvents=true --set emitAuditEvents=true \
  --wait --timeout 5m

if [ ! -d "$WORK/gatekeeper-library" ]; then
  git clone --depth 1 --branch "$LIBRARY_REF" https://github.com/open-policy-agent/gatekeeper-library "$WORK/gatekeeper-library"
fi
LIB="$WORK/gatekeeper-library/library"

# Sync config: referential library policies (unique ingress host, etc.) need
# these kinds replicated into Gatekeeper's cache.
cat <<'YAML' | k apply -f - >/dev/null
apiVersion: config.gatekeeper.sh/v1alpha1
kind: Config
metadata:
  name: config
  namespace: gatekeeper-system
spec:
  sync:
    syncOnly:
      - {group: "", version: v1, kind: Namespace}
      - {group: "", version: v1, kind: Pod}
      - {group: networking.k8s.io, version: v1, kind: Ingress}
  match:
    - excludedNamespaces: ["kube-system", "gatekeeper-system", "kyverno", "p9s-demo"]
      processes: ["webhook"]
YAML

# Templates first, then wait for their CRDs, then one sample constraint each.
for t in "$LIB"/general/*/template.yaml "$LIB"/pod-security-policy/*/template.yaml; do
  k apply -f "$t" >/dev/null
done
echo "waiting for constraint CRDs..."
for i in $(seq 1 60); do
  pending=$(k get constrainttemplates -o jsonpath='{range .items[*]}{.status.created}{"\n"}{end}' | grep -vc true || true)
  [ "$pending" -eq 0 ] && break
  sleep 2
done

applied=0
for dir in "$LIB"/general/* "$LIB"/pod-security-policy/*; do
  c=$(ls "$dir"/samples/*/constraint.yaml 2>/dev/null | head -1 || true)
  [ -n "$c" ] || continue
  if k apply -f "$c" >/dev/null 2>&1; then applied=$((applied+1)); fi
done
echo "applied $applied sample constraints"

# Demo workloads in p9s-demo: excluded from Gatekeeper *admission* (see the
# Config above) so they can be created despite the deny-mode samples, but
# still audited by both engines. Three replicas of one Deployment exercise
# violation grouping by owner; team=payments exercises --group-label.
k apply -f "$(dirname "$0")/manifests/demo-workloads.yaml" >/dev/null
k -n p9s-demo rollout status deploy/checkout --timeout=120s >/dev/null || echo "note: checkout rollout not ready"

if [ "$KYVERNO" = "1" ]; then
  # Gatekeeper's deny-mode samples would block Kyverno's own namespace and
  # pods, so the namespace is labelled and excluded from Gatekeeper admission
  # (see the Config above).
  k create namespace kyverno --dry-run=client -o yaml \
    | k label --local -f - owner=kyverno.agilebank.demo -o yaml | k apply -f - >/dev/null
  helm repo add kyverno https://kyverno.github.io/kyverno/ --force-update >/dev/null
  helm upgrade --install kyverno kyverno/kyverno --kube-context "$CTX" -n kyverno \
    --set admissionController.replicas=1 --set backgroundController.replicas=1 \
    --set cleanupController.replicas=1 --set reportsController.replicas=1 \
    `# Gatekeeper >=3.22 generates VAP bindings with names longer than 63 bytes;` \
    `# Kyverno turns those into report labels and every report write fails.` \
    --set features.validatingAdmissionPolicyReports.enabled=false \
    --wait --timeout 6m
  # The standard Pod Security Standards policies, in Audit mode. --wait above
  # returns when the pods are Ready, which can be before the policy webhook
  # accepts connections (seen on GitHub runners): retry until it answers.
  for i in 1 2 3 4 5 6; do
    if helm upgrade --install kyverno-policies kyverno/kyverno-policies --kube-context "$CTX" -n kyverno \
      --set podSecurityStandard=restricted --set validationFailureAction=Audit --wait --timeout 3m; then
      break
    fi
    [ "$i" = 6 ] && { echo "kyverno-policies: webhook never became reachable" >&2; exit 1; }
    echo "kyverno-policies: webhook not ready, retrying in 15s ($i/6)"
    sleep 15
  done
  k apply -f "$(dirname "$0")/manifests/kyverno-classic.yaml" 2>&1 | grep -v "deprecated" || true
  # The demo workloads predate Kyverno, so it never admitted them and their
  # reports wait on a background scan (none within minutes on a fresh
  # cluster). Re-admit them: Audit-mode policies report at admission.
  k -n p9s-demo rollout restart deployment >/dev/null
  k -n p9s-demo rollout status deployment --timeout 3m
fi

# Native ValidatingAdmissionPolicies for the vap engine.
k apply -f "$(dirname "$0")/manifests/vap.yaml" >/dev/null

# A namespace-only tenant (view on p9s-demo) for the RBAC e2e tests.
k apply -f "$(dirname "$0")/manifests/tenant.yaml" >/dev/null

"$(dirname "$0")/demo-webhooks.sh" "$CTX"

# The tests read what the engines report, so wait until each has reported
# once: on a fresh cluster Gatekeeper's first audit and Kyverno's reports can
# take several minutes after the pods are Ready (slower on CI runners).
wait_for() { # description, on-timeout command, check command...
  local what="$1" dump="$2"; shift 2
  for i in $(seq 1 120); do
    if "$@" >/dev/null 2>&1; then echo "ready: $what ($((i * 5))s)"; return 0; fi
    [ $((i % 12)) = 0 ] && echo "waiting for $what ($((i * 5))s)"
    sleep 5
  done
  echo "timed out waiting for $what" >&2
  "$dump" >&2 || true
  return 1
}
gatekeeper_audited() {
  k get constraints -o jsonpath='{range .items[*]}{.status.auditTimestamp}{"\n"}{end}' | grep . >/dev/null
}
gatekeeper_dump() { k get constraints; }
wait_for "Gatekeeper audit" gatekeeper_dump gatekeeper_audited
if [ "$KYVERNO" = "1" ]; then
  report_results() {
    k get policyreports -n p9s-demo -o jsonpath='{range .items[*].results[*]}{.policy}={.result}{"\n"}{end}'
  }
  kyverno_reported() { report_results | grep -x 'disallow-privilege-escalation=fail' >/dev/null; }
  kyverno_dump() {
    k get policyreports -n p9s-demo
    report_results | sort | uniq -c
    k get validatingpolicies
    k -n kyverno logs deploy/kyverno-reports-controller --tail=50
  }
  wait_for "Kyverno reports in p9s-demo" kyverno_dump kyverno_reported
fi

echo "done. context: $CTX"
