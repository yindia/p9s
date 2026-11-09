# Architecture

p9s is layered so that engines, cross-engine logic and renderers stay apart:
an engine never knows how it is drawn, and a renderer never counts or
collects anything itself.

```
kube → session → engines → model
                         → query   cross-engine collecting, counting and grading
                         → views   shared views, command resolution, overview, health, report
                         → table   materialised, JSON-serialisable tables (p9s/v1)
                              ├─ internal/ui       Bubble Tea renderer: screens and keys only
                              └─ internal/render   text, JSON, Markdown and HTML for the CLI
```

`internal/boundary_test.go` enforces the layering: `model` imports nothing
internal, `query`, `views` and `table` import no terminal code, and the
renderers never reach into engines. An engine lives entirely in
`internal/engines/<name>`, so adding one does not touch the UI or the model
(see [adding-an-engine.md](adding-an-engine.md)).

## Data flow

- **session** connects to a context, detects engines through discovery and
  starts them; on a context switch it tears everything down and starts again.
- **engines** watch their objects with informers (`enginekit.Access`,
  `enginekit.Sync`), normalise them into `model` types and report RBAC gaps
  as notices instead of errors.
- **query** merges engines: violations by owner, enforcement impact,
  health.
- **views** turns queries into screens and the report model; **table**
  materialises them into the `p9s/v1` tables that both the TUI and
  `p9s get -o json` print.

## Layout

```
cmd/p9s/                     cobra commands: TUI, get, snapshot, report
internal/model/              engine-neutral types
internal/engine/             Engine interface, views, registry
internal/engine/enginetest/  the contract every engine passes
internal/engines/enginekit/  shared plumbing: RBAC probes, sync, cache transform, fake cluster
internal/engines/gatekeeper/ Gatekeeper (+ gktest fixtures)
internal/engines/kyverno/    Kyverno (+ kyvtest fixtures)
internal/engines/vap/        ValidatingAdmissionPolicies (+ vaptest fixtures)
internal/owners/             owner resolution from metadata-only informers
internal/platform/           webhooks, controllers and leases for :health
internal/events/             admission Events for the recent tab
internal/query/              cross-engine logic
internal/views/              shared views, dashboards and the report model
internal/table/              materialised tables and details
internal/render/             CLI renderers
internal/snapshot/           recording and replaying a cluster
internal/config/             the config file
internal/kube/               kubeconfig and clients
internal/session/            connection, detection, lifecycle, sync
internal/ui/                 the TUI
test/golden/                 real engines rendered through the UI
test/e2e/                    tests against the kind cluster
docs/tapes/                  vhs scripts for docs/image
```
