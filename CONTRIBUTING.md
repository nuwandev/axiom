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

Requires Go 1.23+. Linux (or other unix) is the primary development host;
the tree also builds and tests for `GOOS=windows`. Platform-specific code
lives behind `//go:build unix` / `//go:build windows` files — keep shared
logic out of them. See [`docs/development.md`](docs/development.md).

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

## Cutting a release

1. Update [`CHANGELOG.md`](CHANGELOG.md) — move the pending notes under a new
   `## [X.Y.Z] — <date>` heading. `.github/workflows/release.yml` uses this
   section verbatim as the GitHub Release notes, so write it for that
   audience.
2. Run `scripts/sync-doc-versions.sh vX.Y.Z` to repoint the copy-and-run
   install commands in `docs/INSTALL.md` at the new tag.
3. Commit both as `Release vX.Y.Z`, then tag and push the tag:
   `git tag vX.Y.Z && git push origin vX.Y.Z`.
4. That alone publishes the release: `.github/workflows/release.yml` builds
   every artifact with `scripts/build-release.sh` (linux/amd64, linux/arm64
   + RPMs, windows/amd64 + the offline install bundle) — refusing to run if
   step 2 was skipped, same as a local build — and uploads them to a new
   GitHub Release named after the tag. Watch the Actions run; nothing needs
   attaching by hand.
   - To exercise the pipeline without publishing (e.g. after changing
     `scripts/build-release.sh` or the workflow itself), run it manually from
     the Actions tab ("Run workflow") with a throwaway version like
     `v0.0.0-dryrun` — this builds everything and uploads it as a workflow
     run artifact instead of a GitHub Release.

## Reporting bugs

Open a GitHub issue with steps to reproduce, your OS/distro, and the Axiom
version (`journalctl -u axiom` output is often useful). For security
vulnerabilities, see [SECURITY.md](SECURITY.md) instead — do not open a
public issue.
