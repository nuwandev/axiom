"""Tiny SVG authoring helper used to produce the presentation diagrams."""
import os
from pathlib import Path

# Generated diagrams are checked in under presentation/diagrams. AXIOM_DIAGRAMS_OUT
# redirects the output (used by generate_svgs.py --check).
OUT_DIR = Path(os.environ.get("AXIOM_DIAGRAMS_OUT", Path(__file__).resolve().parents[2] / "diagrams"))


def write(name, svg):
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    with open(OUT_DIR / name, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(svg)


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


class Svg:
    def __init__(self, pid, w, h, title, desc):
        self.pid, self.w, self.h, self.title, self.desc = pid, w, h, title, desc
        self.p = []

    def add(self, s):
        self.p.append(s)

    def rect(self, x, y, w, h, cls, rx=6):
        self.add(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" class="{cls}"/>')

    def text(self, x, y, s, cls="ax-t", anchor="start", extra=""):
        a = f' text-anchor="{anchor}"' if anchor != "start" else ""
        self.add(f'<text x="{x}" y="{y}" class="{cls}"{a}{extra}>{esc(s)}</text>')

    def lines(self, x, y, items, lh=17, anchor="start"):
        for i, (s, c) in enumerate(items):
            self.text(x, y + i * lh, s, c, anchor)

    def path(self, d, cls, marker=None, start=None):
        m = f' marker-end="url(#{self.pid}-m-{marker})"' if marker else ""
        s = f' marker-start="url(#{self.pid}-m-{start})"' if start else ""
        self.add(f'<path d="{d}" class="{cls}"{m}{s}/>')

    def badge(self, x, y, n):
        self.add(f'<circle cx="{x}" cy="{y}" r="11" class="ax-badge"/>')
        self.text(x, y + 4.5, str(n), "ax-badge-t", "middle")

    def comment(self, s):
        self.add(f"<!-- {s} -->")

    def render(self):
        defs = "".join(
            f'<marker id="{self.pid}-m-{k}" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" '
            f'markerWidth="11" markerHeight="11" orient="auto-start-reverse"><path d="M0,1 L10,5 L0,9 z" class="ax-mk ax-mk-{k}"/></marker>'
            for k in ("ctl", "loc", "data", "aud", "pki")
        )
        head = (
            f'<svg class="ax-svg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" '
            f'role="img" aria-labelledby="{self.pid}-t {self.pid}-d">'
            f'<title id="{self.pid}-t">{esc(self.title)}</title><desc id="{self.pid}-d">{esc(self.desc)}</desc>'
            f"<defs>{defs}</defs>\n"
        )
        return head + "\n".join(self.p) + "\n</svg>\n"
