#!/usr/bin/env python3
"""Validate a built presentation site (run from presentation/ after `zensical build`).

Fails if an in-page link has no target, if any HTML page pulls an external
script, stylesheet, font, image or frame, or if the expected diagrams are missing.
"""
import re
import sys
from pathlib import Path

SITE = Path(__file__).resolve().parent.parent / "site"
EXPECTED_SVG_DIAGRAMS = 10  # 5 architecture illustrations + 5 pre-rendered Mermaid
EXTERNAL = re.compile(r"""(?:src|href|data|poster|srcset|action)\s*=\s*["']\s*(?:https?:)?//""", re.I)
CSS_EXTERNAL = re.compile(r"""(?:@import|url\()\s*["']?\s*(?:https?:)?//""", re.I)


def main() -> int:
    pages = sorted(SITE.rglob("*.html"))
    if not pages:
        sys.exit("no built site: run `zensical build -f zensical.toml` first")
    problems = []
    for page in pages:
        html = page.read_text(encoding="utf-8")
        ids = set(re.findall(r'\bid="([^"]+)"', html))
        for target in sorted(set(re.findall(r'href="#([^"]+)"', html))):
            if target not in ids:
                problems.append(f"{page.name}: link #{target} has no target")
        for tag in re.findall(r"<(?:script|link|img|iframe|source)\b[^>]*>", html, re.I):
            if EXTERNAL.search(tag) and "rel=\"canonical\"" not in tag:
                problems.append(f"{page.name}: external resource: {tag[:100]}")
        if re.search(r"fonts\.(googleapis|gstatic)\.com|unpkg\.com|cdn\.jsdelivr", html):
            problems.append(f"{page.name}: references a CDN or web-font host")
        if 'class="mermaid"' in html:
            problems.append(f"{page.name}: contains a runtime Mermaid block")
    for css in SITE.rglob("*.css"):
        if CSS_EXTERNAL.search(css.read_text(encoding="utf-8", errors="replace")):
            problems.append(f"{css.name}: CSS loads an external resource")
    index = (SITE / "index.html").read_text(encoding="utf-8")
    count = len(re.findall(r'<svg\b[^>]*\bid="[^"]+"|<svg class="ax-svg"', index))
    if count != EXPECTED_SVG_DIAGRAMS:
        problems.append(f"index.html: found {count} inline diagrams, expected {EXPECTED_SVG_DIAGRAMS}")
    if problems:
        print("\n".join(problems))
        return 1
    print(f"ok: {len(pages)} page(s), {count} inline diagrams, no external resources, all in-page links resolve")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
