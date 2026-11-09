# Contributing to p9s

Thanks for helping. p9s is a read-only terminal UI for Kubernetes policy
engines; most changes touch one engine, one view or one renderer, and the
layering keeps those apart. Read [AGENTS.md](AGENTS.md) for the rules the
code follows; this file covers the workflow.

## Setup

- Go (the version in `go.mod`)
- [golangci-lint](https://golangci-lint.run) v2
- For e2e: Docker, [kind](https://kind.sigs.k8s.io), Helm, kubectl
- For the demo GIFs: [vhs](https://github.com/charmbracelet/vhs), ttyd, ffmpeg

```sh
git clone https://github.com/yindia/p9s && cd p9s
make build && ./bin/p9s --context <your-context>
```

## Checks

Every pull request must pass what CI runs (`.github/workflows/ci.yml`):

```sh
make lint        # golangci-lint, gofmt, goimports
make test        # unit, contract, golden and import-boundary tests
make test-race
make golden      # must leave the tree unchanged unless the UI changed on purpose
make e2e         # kind cluster with Gatekeeper, Kyverno and VAPs, then the e2e suite
```

`make e2e` creates a kind cluster named `p9s` (context `kind-p9s`) and is
safe to re-run. Point tests only at that cluster: e2e never runs against
your current kubeconfig context. Tear it down with `make e2e-down`.

## What a change needs

- **Tests with the change.** Engine logic gets unit tests against the fake
  cluster in `enginekit` and, for anything the engine exposes, a case in the
  engine contract (`internal/engine/enginetest`). A changed screen gets
  updated goldens (`make golden`, then review the diff) and an e2e check in
  `test/e2e/e2e_test.go` against the real engine.
- **No invented numbers.** If an engine cannot tell us something (a
  violation count, an audit time), the UI shows `-` and says why. Do not
  estimate or default to zero.
- **Read-only.** p9s never creates, patches or deletes cluster objects.
  Missing RBAC is shown as a notice, never as an error that stops the UI.
- **Informers, not polling.** New data sources go through
  `enginekit.Access` and `enginekit.Sync`.
- **Docs.** Update [docs/tui.md](docs/tui.md) (commands, keys), [docs/cli.md](docs/cli.md)
  and [docs/engines.md](docs/engines.md) when you change them, and the matching tape in `docs/tapes/` for a visible change.
  A new engine follows [docs/adding-an-engine.md](docs/adding-an-engine.md).

For anything larger than a fix, open an issue first with a short plan:
what changes, which layers it touches, and how it will be tested.

## Commits and pull requests

- Conventional commit messages, short subject: `feat: impact view for VAPs`,
  `fix: kyverno report counts in namespaced scope`. Types: feat, fix,
  refactor, docs, test, chore, perf, ci.
- Sign off every commit (`git commit -s`): contributions are accepted under
  the [Developer Certificate of Origin](https://developercertificate.org).
- One logical change per pull request; rebase on `main` rather than merging.
- Fill in the pull request template, including how you tested.

## Releases

Maintainers push a `vX.Y.Z` tag. The `release` workflow runs the tests,
GoReleaser publishes the binaries and `checksums.txt`, and commits the
bumped Homebrew formula to `Formula/p9s.rb`. Do not edit the formula by
hand.

## Reporting security issues

See [SECURITY.md](SECURITY.md). Please do not open public issues for them.
