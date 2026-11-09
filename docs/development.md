# Development

Setup, the checks every change needs and commit conventions are in
[CONTRIBUTING.md](../CONTRIBUTING.md); the layering is in
[architecture.md](architecture.md). This page covers the local cluster,
the recordings and releases.

```sh
make test        # unit, contract and golden tests
make test-race
make lint        # golangci-lint
make golden      # regenerate goldens after an intentional UI change
make e2e-up      # kind cluster: Gatekeeper + library, Kyverno + PSS, demo workloads, VAPs
make e2e         # e2e-up, then the e2e tests
make e2e-down
make tapes       # render the GIFs in docs/image (vhs, ttyd, ffmpeg and make e2e-up)
make tapes TAPES=docs/tapes/demo.tape   # just one
```

The demo GIFs are recorded from `docs/tapes/*.tape` into `docs/image/`.

**Releasing**: push a tag (`git tag v0.1.0 && git push origin v0.1.0`).
The `release` workflow runs the tests, then GoReleaser builds macOS, Linux
and Windows binaries for amd64 and arm64, publishes the release with
`checksums.txt`, and commits the updated Homebrew formula to
`Formula/p9s.rb` on `main`. `ci` runs lint, tests (race, goldens) and the
e2e suite on kind for every pull request and push to `main`;
`goreleaser-dryrun` checks the configuration
and builds a snapshot on every push and pull request.

The e2e cluster runs Gatekeeper and Kyverno side by side; see
[engines.md](engines.md#running-gatekeeper-and-kyverno-together) for the
two adjustments that needs.
