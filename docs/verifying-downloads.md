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
curl -LO https://github.com/nuwandev/axiom/releases/download/v1.2.0/SHA256SUMS
curl -LO https://github.com/nuwandev/axiom/releases/download/v1.2.0/SHA256SUMS.asc
curl -LO https://raw.githubusercontent.com/nuwandev/axiom/v1.2.0/packaging/rpm/axiom-signing-public.asc

gpg --import axiom-signing-public.asc
gpg --verify SHA256SUMS.asc SHA256SUMS   # expect "Good signature from ... <theekshanabro40@gmail.com>"

sha256sum -c SHA256SUMS --ignore-missing  # then check the asset(s) you downloaded
```

A signature that doesn't verify, or a checksum mismatch, means the file is
not what this project published — do not run it; open an issue instead.

## Verify an RPM directly (also works offline, no SHA256SUMS needed)

```bash
sudo rpm --import axiom-signing-public.asc
rpm -K axiom-1.2.0-1.x86_64.rpm   # expect "... rsa sha256 (md5) pgp md5 OK"
```

## If `AXIOM_GPG_PRIVATE_KEY` isn't configured

Signing is best-effort: `scripts/build-release.sh` builds unsigned RPMs
(with a warning) and the release workflow skips `SHA256SUMS.asc` if the
`AXIOM_GPG_PRIVATE_KEY` repository secret isn't set. An unsigned release
still publishes `SHA256SUMS` for plain checksum verification.
