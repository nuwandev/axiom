# Architecture presentation

A single-page, source-verified architecture presentation of Axiom in an
illustrative banking deployment, followed by the internal implementation. It
is built with [Zensical](https://zensical.org) and lives apart from the
reference documentation in `docs/`, which it does not replace.

## Build

From this directory:

```bash
python -m venv .venv
. .venv/bin/activate          # Windows: .venv\Scripts\activate
pip install -r requirements.txt

zensical build -f zensical.toml     # writes ./site
zensical serve -f zensical.toml     # live preview on http://localhost:8000
```

`zensical build -f zensical.toml -s` runs the build in strict mode.

## Layout

| Path | Purpose |
|---|---|
| `zensical.toml` | Zensical project configuration |
| `docs/index.md` | The presentation: twelve anchored sections |
| `docs/stylesheets/axiom.css` | Visual system: light and dark palettes, diagram vocabulary, mobile and print layout |
| `diagrams/*.svg` | Hand-maintained SVG diagrams, inlined into the page with `pymdownx.snippets` |

The SVG files carry no colours of their own. Every colour comes from the CSS
variables in `axiom.css`, which is why the diagrams follow the light and dark
switch and print in the light palette. The sequence diagrams, flowcharts and
state diagram are Mermaid code blocks in `docs/index.md`.

## Notes

- **Mermaid needs network access at view time.** Zensical loads the Mermaid
  runtime from a CDN, so the five Mermaid diagrams render only when the
  viewer can reach it. The five SVG diagrams, fonts and all other content are
  local; no third-party fonts are requested.
- **Printing.** The page prints on A3 landscape with the light palette. The
  two tall sequence diagrams can span two pages.
- **Accuracy.** Each behavioural claim names the file or function that backs
  it. When changing the implementation, update the matching section and
  diagram; a claim that no longer matches the code should be removed rather
  than softened.
- The banking topology is an illustration of one deployment pattern. Axiom
  has no concept of tiers, a fleet or a central controller.
