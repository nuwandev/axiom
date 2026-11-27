#!/usr/bin/env python3
"""Assemble website/docs from README.md and docs/*.md.

The repository docs stay the single source of truth. This script copies them,
adds a unique <title> and meta description per page, and rewrites relative
links: links between pages stay on-site, links to other repository files point
at GitHub, because those files are not part of the published site.
"""
import posixpath
import re
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "website" / "docs"
REPO = "https://github.com/nuwandev/axiom-mtls-agent"

# source (relative to repo root) -> (published name, title, description)
PAGES = {
    "README.md": (
        "index.md",
        "Server automation over mutual TLS, an SSH alternative for CI/CD",
        "The Axiom mTLS agent is an open source Go agent that lets CI/CD trigger predefined deploy and restart actions on Linux and Windows servers over mutual TLS, without SSH.",
    ),
    "docs/getting-started.md": (
        "getting-started.md",
        "Getting started: install Axiom on Linux and run a first action",
        "Install the Axiom agent on a RHEL-family Linux host, create certificates and trigger your first action over mutual TLS.",
    ),
    "docs/INSTALL.md": (
        "install-linux.md",
        "Install Axiom on RHEL, Rocky Linux and AlmaLinux",
        "Complete Linux reference for Axiom: RPM and script install, systemd hardening, SELinux, upgrade, rollback, uninstall and troubleshooting.",
    ),
    "docs/INSTALL-WINDOWS.md": (
        "install-windows.md",
        "Install Axiom on Windows Server 2022 and Windows 10/11",
        "Install and operate the Axiom agent as a Windows service under NT SERVICE\\axiom, with NTFS ACL checks, MSI install, upgrade and rollback.",
    ),
    "docs/configuration.md": (
        "configuration.md",
        "Axiom configuration reference (YAML)",
        "Reference for the Axiom YAML config: listener, TLS, clients and allowed actions, jobs, audit and logging settings.",
    ),
    "docs/actions.md": (
        "actions.md",
        "Axiom actions: define server-local scripts with validated parameters",
        "How Axiom actions work: declared scripts, regex-validated parameters passed as environment variables, timeouts, exclusivity and job output.",
    ),
    "docs/certificates.md": (
        "certificates.md",
        "mTLS certificate lifecycle for Axiom",
        "Issue, renew and rotate the CA, server and per-pipeline client certificates Axiom uses for mutual TLS authentication.",
    ),
    "docs/jenkins-integration.md": (
        "jenkins-integration.md",
        "Trigger Axiom from Jenkins and other CI/CD systems",
        "Call the Axiom HTTPS API from Jenkins or any CI/CD system with a client certificate: trigger an action, poll the job, fetch logs.",
    ),
    "docs/security.md": (
        "security.md",
        "Axiom security model",
        "What the Axiom agent enforces: mutual TLS, default-deny authorization, no shell, file-integrity checks at startup and fail-closed auditing.",
    ),
    "docs/THREAT-MODEL.md": (
        "threat-model.md",
        "Axiom threat model",
        "Threat analysis of the Axiom automation agent: assets, trust boundaries, attacker capabilities, mitigations and residual risks.",
    ),
    "docs/windows-security.md": (
        "windows-security.md",
        "Axiom security model on Windows",
        "How Axiom is contained on Windows: virtual service account, NTFS ACL verification, Job Objects and PowerShell execution policy handling.",
    ),
    "docs/verifying-downloads.md": (
        "verifying-downloads.md",
        "Verify Axiom release downloads: GPG, SHA256SUMS, SLSA, SBOM",
        "Verify an Axiom release before installing: GPG-signed SHA256SUMS and RPMs, SLSA build provenance and the CycloneDX SBOM.",
    ),
    "docs/audit-validation.md": (
        "audit-validation.md",
        "Validating the Axiom audit path on a real host",
        "Procedures for checking that Axiom audit records reach journald on Linux and the Application event log on Windows, including a fault-injection step.",
    ),
    "docs/WINDOWS-SERVER-2022-VALIDATION.md": (
        "windows-server-2022-validation.md",
        "Axiom Windows Server 2022 production validation",
        "End-to-end real-host validation of Axiom on Windows Server 2022: install, ACLs, service start, mTLS, recovery and uninstall.",
    ),
    "docs/postman-testing.md": (
        "postman-testing.md",
        "Test the Axiom API with Postman",
        "Import the Axiom Postman collection and call the API with a client certificate to trigger actions and read job status.",
    ),
    "docs/development.md": (
        "development.md",
        "Developing Axiom: build, test and test layers",
        "Build the Axiom Go binary, run unit, integration and real-host tests, and understand how the test layers are split.",
    ),
}

LINK = re.compile(r"(\]\()([^)\s]+)(\))")
SOURCE_TO_NAME = {src: name for src, (name, _, _) in PAGES.items()}


def rewrite_target(source: str, target: str) -> str:
    if re.match(r"^[a-z][a-z0-9+.-]*:|^#|^//", target, re.I):
        return target
    path, _, fragment = target.partition("#")
    resolved = posixpath.normpath(posixpath.join(posixpath.dirname(source), path))
    suffix = f"#{fragment}" if fragment else ""
    if resolved in SOURCE_TO_NAME:
        return SOURCE_TO_NAME[resolved] + suffix
    local = ROOT / resolved
    if not local.exists():
        sys.exit(f"{source}: link target does not exist in the repo: {target}")
    kind = "tree" if local.is_dir() else "blob"
    return f"{REPO}/{kind}/main/{resolved}{suffix}"


def convert(source: str) -> str:
    _, title, description = PAGES[source]
    text = (ROOT / source).read_text(encoding="utf-8")
    text = LINK.sub(lambda m: m[1] + rewrite_target(source, m[2]) + m[3], text)
    front = f'---\ntitle: "{title}"\ndescription: "{description}"\n---\n\n'
    return front + text


def main() -> None:
    unmapped = sorted(p.name for p in (ROOT / "docs").glob("*.md") if f"docs/{p.name}" not in PAGES)
    if unmapped:
        sys.exit(f"docs not mapped in PAGES: {', '.join(unmapped)}")
    if OUT.exists():
        shutil.rmtree(OUT)
    OUT.mkdir(parents=True)
    for source, (name, _, description) in PAGES.items():
        if not 100 <= len(description) <= 170:
            sys.exit(f"{source}: description is {len(description)} characters")
        (OUT / name).write_text(convert(source), encoding="utf-8", newline="\n")
    print(f"prepared {len(PAGES)} pages in {OUT}")


if __name__ == "__main__":
    main()
