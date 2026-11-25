#!/usr/bin/env python3
"""Pre-render diagrams/mermaid/*.mmd to static SVG with a pinned Mermaid CLI.

    cd presentation/tools && npm ci        (once; see README.md)
    python tools/render_mermaid.py         (from presentation/)

Needs Node.js and a local Chrome, Chromium or Edge. Set CHROME_PATH to use a
specific browser. Building the site itself needs none of this: the rendered
SVGs are checked in and Zensical inlines them.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

TOOLS = Path(__file__).resolve().parent
ROOT = TOOLS.parent
SRC = ROOT / "diagrams" / "mermaid"
CLI = TOOLS / "node_modules" / "@mermaid-js" / "mermaid-cli" / "src" / "cli.js"

BROWSERS = [
    os.environ.get("CHROME_PATH", ""),
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
]


def find_browser() -> str:
    for candidate in BROWSERS:
        if candidate and Path(candidate).exists():
            return candidate
    for name in ("google-chrome", "chromium", "chromium-browser", "chrome", "msedge"):
        found = shutil.which(name)
        if found:
            return found
    sys.exit("no Chrome, Chromium or Edge found: install one or set CHROME_PATH")


def render(out_dir: Path) -> list[Path]:
    if not CLI.exists():
        sys.exit("Mermaid CLI is not installed: run `npm ci` in presentation/tools")
    node = shutil.which("node") or sys.exit("node is not on PATH")
    outputs = []
    with tempfile.TemporaryDirectory() as tmp:
        pp = Path(tmp) / "puppeteer.json"
        # The sandbox is disabled only because the browser renders this
        # repository's own diagram sources, never untrusted content.
        pp.write_text(json.dumps({"executablePath": find_browser(), "args": ["--no-sandbox"]}))
        for mmd in sorted(SRC.glob("*.mmd")):
            out = out_dir / f"{mmd.stem}.svg"
            subprocess.run(
                [node, str(CLI), "-i", str(mmd), "-o", str(out),
                 "-c", str(TOOLS / "mermaid" / "config.json"),
                 "-C", str(TOOLS / "mermaid" / "theme.css"),
                 "-I", f"mmd-{mmd.stem}", "-b", "transparent", "-p", str(pp), "-q"],
                check=True,
            )
            # Repository text files are LF; strip the CLI's trailing whitespace.
            text = out.read_text(encoding="utf-8").replace("\r\n", "\n").rstrip() + "\n"
            # A leading "[n] " in a label becomes a bold step number, so it
            # reads as a marker and not as part of the sentence.
            text = re.sub(r"\[(\d+)\] ", r'<tspan class="stepNo">\1</tspan>&#160;&#160;', text)
            out.write_text(text, encoding="utf-8", newline="\n")
            outputs.append(out)
    return outputs


def main() -> int:
    if "--check" not in sys.argv[1:]:
        render(SRC)
        return 0
    with tempfile.TemporaryDirectory() as tmp:
        stale = []
        for generated in render(Path(tmp)):
            current = SRC / generated.name
            if not current.exists() or current.read_bytes().replace(b"\r\n", b"\n") != generated.read_bytes():
                stale.append(generated.name)
    if stale:
        print("out of date: " + ", ".join(stale) + "  (run: python tools/render_mermaid.py)")
        return 1
    print("Mermaid diagrams are up to date")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
