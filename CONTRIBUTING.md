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

Pre-flight (do these before tagging; each has bitten a release before):

- `main` CI is green on the exact commit you will tag, including the
  `windows build, vet, test` job. Never merge a release PR before its CI
  finishes.
- If `.github/workflows/release.yml`, `scripts/build-release.sh` or
  `scripts/build-msi.sh` changed, run the workflow manually (Actions >
  Release > "Run workflow", version `v0.0.0-dryrun`) on that branch first and
  confirm all three jobs pass.
- README, the docs, `postman/`, the RPM description and the GitHub repo
  description/topics describe every supported platform (Linux and Windows)
  and carry no stale version numbers or "Linux only" wording.
- No company names, hostnames, secrets or opinionated example actions:
  `git grep -nIiE "backend-deploy|BEGIN .*PRIVATE KEY"` is empty and the
  only shipped example action is `hello.world`.
- Linux and Windows install paths stay at parity (installer, docs, example).
- No AI attribution anywhere (commits, PR/release text, docs, code
  comments) — see the rule in `CLAUDE.md`.
- RPM/checksum signing still works: the `AXIOM_GPG_PRIVATE_KEY` repo secret
  is set and `verify RPM signatures` in the `build` job passes. See
  [`docs/verifying-downloads.md`](docs/verifying-downloads.md). If that key
  is ever rotated, update `GPG_KEY_ID` in `release.yml` and republish
  `packaging/rpm/axiom-signing-public.asc` and the fingerprint in that doc.

1. Update [`CHANGELOG.md`](CHANGELOG.md) — move the pending notes under a new
   `## [X.Y.Z] — <date>` heading (use the actual release date).
   `.github/workflows/release.yml` uses this section verbatim as the GitHub
   Release notes, so write it for that audience.
2. Run `scripts/sync-doc-versions.sh vX.Y.Z` to repoint the copy-and-run
   install commands in `docs/INSTALL.md` at the new tag.
3. Commit both as `Release vX.Y.Z`, then tag and push the tag:
   `git tag vX.Y.Z && git push origin vX.Y.Z`.
4. That alone publishes the release. `.github/workflows/release.yml` runs
   three jobs: `build` (linux/amd64, linux/arm64 + RPMs, windows/amd64 exe
   + offline zip bundle, via `scripts/build-release.sh`), `msi` (the Windows
   MSI via `scripts/build-msi.sh` on a Windows runner — WiX is Windows-only),
   and `publish` (regenerates `SHA256SUMS` over everything and creates the
   GitHub Release). It refuses to run if step 2 was skipped. Watch the run.
   - The dry run described above builds everything and uploads it as a
     workflow run artifact instead of a GitHub Release.
5. After it finishes, verify the Release page: all files present
   (2 RPMs, 2 Linux binaries, Windows exe/zip/msi, `SHA256SUMS`), checksums
   cover every file, notes are the CHANGELOG section. A green run alone is
   not proof the release is correct.

## Reporting bugs

Open a GitHub issue with steps to reproduce, your OS/distro, and the Axiom
version (`journalctl -u axiom` output is often useful). For security
vulnerabilities, see [SECURITY.md](SECURITY.md) instead — do not open a
public issue.
