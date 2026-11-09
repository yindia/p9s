# p9s policy report: prod-tokyo-1

| | |
|---|---|
| Cluster | prod |
| Context | prod-tokyo-1 |
| Engines | kyverno v1.19.2 |
| Generated | 2026-11-09T10:00:00Z |
| p9s | v1.0.0 |
| Body SHA-256 | `514ec24d8613e16663c8e337ce225327817482abf5a4e547a07dd6bd1bb736bc` |

> **Notice:** kyverno: forbidden to list clusterpolicies.kyverno.io \| data from it is missing

## 1. Summary

- Policies by enforcement: audit 2, enforce 1
- Violations (engines' own counts): **7** (1 policy has no audit data and is not counted)
- Health: 1 critical, 0 warning, 9 ok

## 2. Violations (2 listed)

> Some engines list fewer violations than they count (Gatekeeper --constraint-violations-limit): this list is partial.

| Engine | Policy | Action | Target | Message |
|---|---|---|---|---|
| kyverno | kyverno:ValidatingPolicy/disallow-host-path | audit | Pod/payments/api-0 | hostPath volumes are forbidden |
| kyverno | kyverno:ValidatingPolicy/disallow-host-path | audit | Pod/payments/api-1 | hostPath volumes are forbidden |

## 3. Fail-open and other health findings

| Severity | Panel | Engine | Check | Status |
|---|---|---|---|---|
| critical | Webhooks | kyverno | validate.kyverno.svc-fail | ✗ fail-open, no endpoints |

## 4. Changes since 2026-11-08T10:00:00Z

- New violations: **1**
- Resolved violations: **0**
- Still failing with a new message: **0**
- Policies added: **0**, removed: **0**
- Enforcement changes: **1**

  - kyverno:ValidatingPolicy/disallow-host-path: audit → enforce

## Not included

- Who can change policy: arrives with the RBAC phase.
