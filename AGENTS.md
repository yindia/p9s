# AGENTS.md

Instructions for coding agents (and humans) working in this repository.
Workflow, setup and commit rules are in [CONTRIBUTING.md](CONTRIBUTING.md).

## What p9s is

A k9s-style terminal UI (Bubble Tea) and CLI for Kubernetes policy
engines: OPA Gatekeeper, Kyverno and native ValidatingAdmissionPolicies.
Module `github.com/yindia/p9s`, entry point `cmd/p9s`.

## Hard rules

1. **Read-only.** Never add code that creates, updates, patches or deletes
   cluster objects. The only writes are local files (`p9s snapshot`,
   `p9s report`, the config file).
2. **Never show a number the engine cannot provide.** Unknown is `-` with a
   reason (`ViolationsKnown`, `MatchKnown` and similar flags in `model`),
   never `0` or an estimate.
3. **Informers, not polling.** Watch through `enginekit.Access` /
   `enginekit.Sync`. The paths that do one-off reads (VAP param checks,
   discovery) run off the render path.
4. **RBAC gaps are notices.** A forbidden list becomes a `model.AccessIssue`
   and the rest of the UI keeps working; fall back to the home namespace
   where the engine allows it.
5. **Secrets stay redacted** in YAML views, snapshots and reports.
6. **Tests and e2e use only the kind cluster** (`kind-p9s`). Never run
   anything against another kubeconfig context.

## Layering

```
kube → session → engines → model
                         → query → views → table → renderers (internal/ui, internal/render)
```

- `model` imports nothing internal; `engine` only `model`.
- `query` and `table` see `model` and `engine`; `views` adds `query` and `table`.
- `internal/ui` and `internal/render` import only `model`, `engine`,
  `views` and `table`. Counting, grouping and grading belong in `query` or
  `views`, never in a renderer.
- `engines/enginekit` stays engine-neutral.
- An engine lives entirely in `internal/engines/<name>`; adding one must
  not touch `internal/ui` or `internal/model`.

`internal/boundary_test.go` enforces this. If it fails, move the code, do
not widen the allow list.

## Where things go

| Change | Place |
|---|---|
| New engine | `internal/engines/<name>` + fixtures `<name>test`, registered in `cmd/p9s/app/root.go`; follow `docs/adding-an-engine.md` |
| Engine capability | an optional interface in `internal/engine` + a contract case in `internal/engine/enginetest` |
| Cross-engine logic | `internal/query` |
| A screen's rows and columns | `internal/views` / `internal/table` |
| Keys, layout, styling | `internal/ui` |
| CLI output formats | `internal/render` |
| Cluster fixtures for e2e | `hack/manifests/`, installed by `hack/e2e-setup.sh` |

## Gates

Run before you call a change done:

```sh
make lint test
make test-race
make golden   # then review the testdata diff; it must be intended
make e2e      # when engine behaviour or a screen changed
```

CI (`.github/workflows/ci.yml`) runs all of these, plus `go mod tidy` and
a check that `make golden` leaves the tree clean.

## Conventions

- Go: gofmt/goimports, small interfaces, errors wrapped with context, a
  `context.Context` with a timeout on every API call.
- Table-driven tests; goldens for rendered screens; e2e for behaviour that
  depends on a real engine version. Assert on what the engine actually
  produces (verify on kind) rather than on what the docs suggest.
- Comments explain why, especially engine quirks and the version they were
  verified against.
- Conventional commits, signed off (`-s`), no AI co-author trailers.
- Do not run GoReleaser locally; CI's `goreleaser-dryrun` covers it.
