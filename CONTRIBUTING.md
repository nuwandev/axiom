# Contributing

Axiom is a small, deliberately boring codebase. Contributions that keep it
that way are welcome.

## Before making a change

For anything beyond a small fix, open an issue first to discuss the
approach — especially for anything touching the API surface, the config
schema, or the security model. See [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md)
for the reasoning behind the current design; changes that weaken an
existing control need a clear justification, not just a passing test suite.

Out of scope for this project (see the architecture notes in the docs for
why): a database, a message broker, a central control plane, a web UI,
webhooks/WebSockets, a reverse/outbound-agent mode, arbitrary command
execution, and a plugin system. Proposals in these directions will likely
be declined.

## Development

Requires Go 1.23+ and a Linux (or other unix) host — the executor and
config-security checks use unix process/file APIs and don't build on
Windows.

```bash
go build ./...
go vet ./...
go test ./...
go test ./... -race
```

Format code with `gofmt` before committing; CI checks `gofmt -l .` is
empty. See [`docs/development.md`](docs/development.md) for the full
breakdown of unit vs. integration tests and what real-host validation
means for this project.

## Pull requests

- Keep changes focused — no unrelated reformatting bundled in.
- Add or update tests for any behavior change.
- Update `docs/` if the change affects the config schema, API surface, or
  installed layout.
- Describe *why*, not just *what*, especially for anything touching
  security-relevant code paths.

## Reporting bugs

Open a GitHub issue with steps to reproduce, your OS/distro, and the Axiom
version (`journalctl -u axiom` output is often useful). For security
vulnerabilities, see [SECURITY.md](SECURITY.md) instead — do not open a
public issue.
