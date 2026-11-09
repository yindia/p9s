# p9s policy report: prod-tokyo-1

| | |
|---|---|
| Cluster | prod |
| Context | prod-tokyo-1 |
| Engines | kyverno v1.19.2 |
| Generated | 2026-11-09T10:00:00Z |
| p9s | v1.0.0 |
| Body SHA-256 | `cbbec8f753574522c52c532878e2835c4a5052a4481c9c042deb414c026e8058` |

> **Notice:** kyverno: forbidden to list clusterpolicies.kyverno.io \| data from it is missing

## 1. Summary

- Policies by enforcement: audit 2, enforce 1
- Violations (engines' own counts): **7** (1 policy has no audit data and is not counted)
- Health: 1 critical, 0 warning, 9 ok

## 2. Pod Security Standards coverage

Mapped from policy metadata only (Kyverno PSS policy names with their PSS category annotation; gatekeeper-library PSP template kinds). Namespace exclusions are not considered.

| Level | Control | Status | Caveats | Policies |
|---|---|---|---|---|
| baseline | HostPath Volumes | audit only | - | kyverno:ValidatingPolicy/disallow-host-path |
| restricted | Volume Types | missing | - |  |

Uncategorised (1): kyverno:ClusterPolicy/require-team-label

## 3. Violations

| Severity (policy metadata) | Count |
|---|---|
| medium | 5 |
| unspecified | 2 |

| Namespace | Count |
|---|---|
| payments | 6 |
| (cluster) | 1 |

| Owner (top 20, objects) | Count |
|---|---|
| Deployment/payments/api | 4 |

## 4. Fail-open and other health findings

| Severity | Panel | Engine | Check | Status |
|---|---|---|---|---|
| critical | Webhooks | kyverno | validate.kyverno.svc-fail | ✗ fail-open, no endpoints |

## 5. Unprotected kinds (approximate)

Served, user-created kinds no active validating policy selects by kind. Approximate: namespace selectors, exclusions and conditions are not read.

| Kind | Group | Scope | Note |
|---|---|---|---|
| ConfigMap | - | namespaced |  |
| Deployment | apps | namespaced | its Pods are checked by Pod policies |

## 6. Changes since 2026-11-08T10:00:00Z

- New violations: **1**
- Resolved violations: **0**
- Still failing with a new message: **0**
- Policies added: **0**, removed: **0**
- Enforcement changes: **1**

  - kyverno:ValidatingPolicy/disallow-host-path: audit → enforce

## Not included

- Who can change policy: arrives with the RBAC phase.
