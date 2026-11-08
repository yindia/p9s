# Security policy

p9s is read-only: it lists and watches cluster objects with your
kubeconfig credentials and never writes to the cluster. Reports of a way
to make it write, leak Secret values (in views, snapshots or reports), or
run untrusted input from a cluster are especially welcome.

## Reporting

Please report vulnerabilities privately through
[GitHub security advisories](https://github.com/yindia/p9s/security/advisories/new),
not in a public issue. Include the version (`p9s --version`), the engine
versions involved and steps to reproduce. You should get a reply within a
week.

## Supported versions

Fixes go into the latest release.
