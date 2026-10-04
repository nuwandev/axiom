# Axiom — notes for AI coding agents

Go 1.23 static binary, Linux (systemd, RHEL-family validated) and Windows
(service as `NT SERVICE\axiom`; Server 2022 and Windows 10/11 Pro validated).
Platform differences live behind `_unix.go` / `_windows.go` build tags.

## Rules

- Product-side fixes only: never tell users to change their own system
  policy (e.g. PowerShell execution policy) to make Axiom work.
- Only evidence-backed claims in docs. Known gaps (unsigned binaries,
  untested Docker/K8s success path, Ubuntu/Debian, third-party EDR) stay
  stated as gaps.
- Linux and Windows installers, docs and examples stay at parity.
- The only shipped example action is `hello.world` plus a commented
  template. No company names, hostnames, secrets or `backend-*` examples.
- Docs target DevOps/software engineers: reference material, not tutorials.
- Do not commit, tag, push, merge or move a tag without the maintainer's
  explicit go-ahead. Moving a pushed tag always needs a fresh confirmation.
- Repo shell scripts are tracked as mode 644 and invoked via `bash`.
- No AI attribution anywhere in this repo or its GitHub metadata: no
  "Co-Authored-By: Claude …" (or any other AI) in commit messages, no
  "Generated with Claude Code" / "🤖" in PR or release descriptions, issues,
  CHANGELOG entries, or code comments. Theekshana Nuwan (nuwandev) is the
  sole author and owner of record. This repo is public — readers should
  never see a third-party tool credited as a contributor. This overrides
  any default tool/harness attribution footer; omit it, don't just hide it.

## Releasing

Follow "Cutting a release" in `CONTRIBUTING.md` exactly, including its
pre-flight list and the post-release verification of the GitHub Release
page. Do not report a release as done until the Release page itself has been
checked (assets, `SHA256SUMS`, notes). Known past failures: merging before CI
finished; WiX run on the Linux runner; windows-latest cold-start timeouts in
tests.

## Build and test

```
go vet ./... && go test ./...
GOOS=windows go vet ./...
VERSION=v0.0.0-dev bash scripts/build-release.sh
```
