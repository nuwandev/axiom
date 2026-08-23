# Security Policy

Axiom is a security-sensitive control plane: it accepts authenticated
requests and executes server-side actions. Security reports are taken
seriously and handled privately.

## Reporting a vulnerability

**Do not open a public GitHub issue for a security vulnerability.**

Report privately using GitHub's private vulnerability reporting:
[Security Advisories for this repository](https://github.com/nuwandev/axiom/security/advisories/new).

If that's unavailable to you, open a regular issue asking to be pointed to
an alternate private contact — do not include vulnerability details in that
issue.

Please include, where possible:
- affected version/commit,
- a description of the issue and its impact,
- reproduction steps or a proof of concept,
- any suggested fix.

## Scope

In scope: the Axiom agent itself (`cmd/axiom`, `internal/...`) — mTLS
authentication/authorization, request handling, the process executor,
audit logging, and the installation tooling in `scripts/` and
`packaging/`.

Out of scope: vulnerabilities in action scripts you write and configure
yourself (Axiom has no visibility into or responsibility for what a
configured action does), and vulnerabilities in third-party dependencies
(report those upstream; a report here noting the dependency is still
welcome so it can be tracked and updated).

## What to expect

- Acknowledgement of a report within a reasonable time.
- An assessment of severity and, if confirmed, a fix targeted at the next
  release. Coordinated disclosure timing is worked out with the reporter.

## Supported versions

Security fixes are made against the latest released version. See
[Releases](https://github.com/nuwandev/axiom/releases) for what's current.
