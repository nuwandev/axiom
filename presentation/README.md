# Architecture presentation

A single-page, source-verified architecture presentation of Axiom in an
illustrative banking deployment, followed by the internal implementation. It
is built with [Zensical](https://zensical.org) and lives apart from the
reference documentation in `docs/`, which it does not replace.

The built site is fully static and works offline: no CDN scripts, no web
fonts, and every diagram is an inline SVG.

## Build the site

Requirements: Python 3.10 or newer. Nothing else is needed, because the
diagram SVGs are checked in.

```bash
cd presentation

python -m venv .venv
. .venv/bin/activate              # Windows: .venv\Scripts\activate
pip install -r requirements.txt   # installs zensical==0.1.2

zensical build -f zensical.toml   # static site in ./site
```

Run it in strict mode to fail on any warning:

```bash
zensical build -f zensical.toml -s
```

## Preview locally

```bash
zensical serve -f zensical.toml   # http://localhost:8000, rebuilds on change
```

To preview the built output instead, serve `./site` with any static file
server, for example `python -m http.server --directory site 8000`.

## Change a diagram

There are two kinds of diagram, both checked in as SVG.

| Kind | Source | Regenerate | Needs |
|---|---|---|---|
| Architecture illustrations (topology, host, internals, audit, platforms) | `tools/diagrams/*.py` | `python tools/generate_svgs.py` | Python only |
| Sequence diagrams, flowcharts, state diagram | `diagrams/mermaid/*.mmd` | `python tools/render_mermaid.py` | Node.js and Chrome, Chromium or Edge |

Set up the Mermaid renderer once (it pins `@mermaid-js/mermaid-cli`
12.2.0 and uses your installed browser rather than downloading one):

```bash
cd tools
PUPPETEER_SKIP_DOWNLOAD=1 npm ci   # Windows PowerShell: $env:PUPPETEER_SKIP_DOWNLOAD=1; npm ci
cd ..
```

Then, from `presentation/`:

```bash
python tools/generate_svgs.py      # rewrites diagrams/*.svg
python tools/render_mermaid.py     # rewrites diagrams/mermaid/*.svg (set CHROME_PATH to choose a browser)
```

Both scripts accept `--check`, which regenerates into a temporary directory
and fails if a checked-in SVG is stale. Commit the regenerated SVGs together
with the source change. The SVG output carries no colours of its own: every
colour comes from the CSS variables in `docs/stylesheets/axiom.css`, which is
why the diagrams follow the light and dark switch and print in the light
palette.

## Verify a build

```bash
python tools/check_site.py          # after zensical build
```

It checks that every in-page link resolves, that no page references an
external script, stylesheet, font or image, and that the diagram count
matches the page.

## Layout

| Path | Purpose |
|---|---|
| `zensical.toml` | Zensical project configuration |
| `requirements.txt` | Pins `zensical==0.1.2` |
| `docs/index.md` | The presentation: twelve anchored sections |
| `docs/stylesheets/axiom.css` | Visual system: palettes, diagram vocabulary, mobile and print layout |
| `diagrams/` | Generated architecture SVGs, inlined with `pymdownx.snippets` |
| `diagrams/mermaid/` | Mermaid sources (`.mmd`) and their pre-rendered SVGs |
| `tools/` | Diagram generators, Mermaid renderer config and the site check |

## Notes

- **Printing.** The page prints on A3 landscape with the light palette.
- **Accuracy.** Each behavioural claim names the file or function that backs
  it. When changing the implementation, update the matching section and
  diagram; a claim that no longer matches the code should be removed rather
  than softened.
- The banking topology is an illustration of one deployment pattern. Axiom
  has no concept of tiers, a fleet or a central controller.
