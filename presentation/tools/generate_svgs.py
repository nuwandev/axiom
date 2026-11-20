#!/usr/bin/env python3
"""Regenerate the hand-authored SVG diagrams in presentation/diagrams.

    python tools/generate_svgs.py           write the SVGs
    python tools/generate_svgs.py --check   regenerate into a temp dir and fail
                                            if the checked-in SVGs differ

Needs only the Python standard library.
"""
import os
import runpy
import sys
import tempfile
from pathlib import Path

TOOLS = Path(__file__).resolve().parent
ROOT = TOOLS.parent
SCRIPTS = ["topology", "host", "internal", "audit", "platforms"]


def generate(out_dir: Path) -> list[Path]:
    os.environ["AXIOM_DIAGRAMS_OUT"] = str(out_dir)
    sys.path.insert(0, str(TOOLS / "diagrams"))
    for name in SCRIPTS:
        runpy.run_path(str(TOOLS / "diagrams" / f"{name}.py"), run_name="__main__")
    return [out_dir / f"{n}.svg" for n in SCRIPTS]


def main() -> int:
    if "--check" not in sys.argv[1:]:
        generate(ROOT / "diagrams")
        return 0
    with tempfile.TemporaryDirectory() as tmp:
        stale = []
        for generated in generate(Path(tmp)):
            current = ROOT / "diagrams" / generated.name
            norm = lambda p: p.read_bytes().replace(b"\r\n", b"\n") if p.exists() else None
            if norm(generated) != norm(current):
                stale.append(generated.name)
    if stale:
        print("out of date: " + ", ".join(stale) + "  (run: python tools/generate_svgs.py)")
        return 1
    print("SVG diagrams are up to date")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
