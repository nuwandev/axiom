# Verifying downloads

Every release publishes `SHA256SUMS`, covering every file in that release,
plus a detached GPG signature over that file (`SHA256SUMS.asc`), and RPMs
are additionally signed individually. All three are signed by the same
key, built and used only in `.github/workflows/release.yml` — Axiom has no
other mechanism that touches it.

```
pub   rsa4096 2026-10-05 [SC] [expires: 2028-10-04]
      8A83 62B0 CB1B 8B2D 518A  3CAF C6A4 ECAE A8BD A887
uid   Axiom Release Signing (nuwandev/axiom RPM package signing) <theekshanabro40@gmail.com>
```

The public key is [`packaging/rpm/axiom-signing-public.asc`](../packaging/rpm/axiom-signing-public.asc)
in this repository — check it out of a checkout you trust (e.g. a tagged
release, not a random fork), not just copy-pasted from an issue or chat.

## Verify the checksums file itself (covers every release asset)

```bash
curl -LO https://github.com/nuwandev/axiom/releases/download/v2.0.1/SHA256SUMS
curl -LO https://github.com/nuwandev/axiom/releases/download/v2.0.1/SHA256SUMS.asc
curl -LO https://raw.githubusercontent.com/nuwandev/axiom/v2.0.1/packaging/rpm/axiom-signing-public.asc

gpg --import axiom-signing-public.asc
gpg --verify SHA256SUMS.asc SHA256SUMS   # expect "Good signature from ... <theekshanabro40@gmail.com>"

sha256sum -c SHA256SUMS --ignore-missing  # then check the asset(s) you downloaded
```

A signature that doesn't verify, or a checksum mismatch, means the file is
not what this project published — do not run it; open an issue instead.

## Verify an RPM directly (also works offline, no SHA256SUMS needed)

```bash
sudo rpm --import axiom-signing-public.asc
rpm -K axiom-2.0.1-1.x86_64.rpm   # expect "digests signatures OK"
```

## Verify build provenance (optional)

Releases from v2.0.1 carry a SLSA build-provenance attestation recorded by
GitHub Actions. With the GitHub CLI:

```bash
gh attestation verify axiom-v2.0.1-linux-amd64 -R nuwandev/axiom
```

It confirms the file was produced by this repository's `release.yml`
workflow from the release tag.

## How signing is enforced

Signing happens in the release workflow's `publish` job only
(`scripts/sign-release.sh`); the `build` job that compiles the code never
receives the key. For a real release (a `vX.Y.Z` tag push) the workflow fails
if the `AXIOM_GPG_PRIVATE_KEY` repository secret is missing, so an unsigned
release cannot be published. The script also verifies the RPM signatures and
`SHA256SUMS.asc` against the public key committed in this repository before
anything is uploaded; a signature made by a different key fails the release.

A manual `workflow_dispatch` dry run does not publish anything. If the secret
is missing it proceeds with a warning and produces unsigned artifacts, so do
not treat dry-run artifacts as releasable.

Local `scripts/build-release.sh` builds are unsigned unless `GPG_KEY_FILE` and
`GPG_KEY_ID` are set; they are for development and testing only.
