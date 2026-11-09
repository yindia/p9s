# Access and RBAC

p9s works with whatever your identity may read and says what it could not:

- A forbidden resource is a notice above the affected table
  (`! kyverno: forbidden to list clusterpolicies.kyverno.io …`); everything
  else still renders, and a denial never blocks sync.
- **Namespace-only access**: with a namespace set on the kubeconfig
  context, p9s falls back to watching namespaced resources (PolicyReports,
  Pods, ReplicaSets, …) in that namespace when it may not list them
  cluster-wide. A tenant with the built-in `view` role therefore sees their
  violations and owners. When a policy itself is not readable, its
  violations still show, enforcement reads `n/a` and the detail says why.
  Gatekeeper keeps violations on cluster-scoped constraints, so it shows
  nothing without access to them.
- `y` and `e` redact Secret values.
- A cluster without any engine shows an empty state that says so; an
  unreachable context opens `:ctx` with the error.

![p9s as a tenant with view on one namespace: the overview still works, forbidden lists become notices](image/rbac.gif)

## What p9s reads

Everything is `get`, `list` and `watch`; p9s never writes. A role for the
whole cluster:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: p9s-reader
rules:
  # Engines: Gatekeeper, Kyverno and their reports
  - apiGroups: [templates.gatekeeper.sh, constraints.gatekeeper.sh, config.gatekeeper.sh,
                mutations.gatekeeper.sh, kyverno.io, policies.kyverno.io, wgpolicyk8s.io, openreports.io]
    resources: ["*"]
    verbs: [get, list, watch]
  # Native policies and the engines' webhooks
  - apiGroups: [admissionregistration.k8s.io]
    resources: [validatingadmissionpolicies, validatingadmissionpolicybindings,
                validatingwebhookconfigurations, mutatingwebhookconfigurations]
    verbs: [get, list, watch]
  # Owners, admission events, health (endpoints, audit leader election)
  - apiGroups: [""]
    resources: [namespaces, pods, events]
    verbs: [get, list, watch]
  - apiGroups: [apps]
    resources: [deployments, replicasets, statefulsets, daemonsets]
    verbs: [get, list, watch]
  - apiGroups: [batch]
    resources: [jobs, cronjobs]
    verbs: [get, list, watch]
  - apiGroups: [discovery.k8s.io]
    resources: [endpointslices]
    verbs: [get, list, watch]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, list, watch]
```

A VAP's parameter check also reads the param kind it names (for example
`configmaps`); without it, `:health` says the check could not run. For a
tenant limited to one namespace, the built-in `view` role on that namespace
is enough for their violations and owners:
[`hack/manifests/tenant.yaml`](../hack/manifests/tenant.yaml) is the
example the e2e tests use.
