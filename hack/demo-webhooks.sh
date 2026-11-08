#!/usr/bin/env bash
# Health-dashboard fixtures: a fail-open webhook with no endpoints and one
# whose CA expires in 7 days. Both live in gatekeeper-system (so p9s files
# them under Gatekeeper) and match only the unused API group p9s.example, so
# they never intercept a real request.
set -euo pipefail
CTX="${1:?kube context}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 7 -subj "/CN=p9s-demo-expiring" \
  -keyout "$WORK/key.pem" -out "$WORK/cert.pem" 2>/dev/null
CA="$(base64 < "$WORK/cert.pem" | tr -d '\n')"
kubectl --context "$CTX" apply -f - >/dev/null <<YAML
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: p9s-demo-webhooks
webhooks:
  - name: fail-open.p9s.example
    failurePolicy: Ignore
    timeoutSeconds: 3
    sideEffects: None
    admissionReviewVersions: [v1]
    clientConfig:
      service: {namespace: gatekeeper-system, name: p9s-demo-missing}
    rules:
      - {apiGroups: [p9s.example], apiVersions: [v1], operations: [CREATE], resources: [widgets]}
  - name: cert-expiring.p9s.example
    failurePolicy: Ignore
    timeoutSeconds: 3
    sideEffects: None
    admissionReviewVersions: [v1]
    clientConfig:
      service: {namespace: gatekeeper-system, name: gatekeeper-webhook-service}
      caBundle: $CA
    rules:
      - {apiGroups: [p9s.example], apiVersions: [v1], operations: [CREATE], resources: [widgets]}
YAML
