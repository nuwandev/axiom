# Documentation site

The published documentation site (<https://nuwandev.github.io/axiom/>). It is
generated from `README.md` and `docs/*.md`, which stay the single source of
truth: `tools/prepare_docs.py` copies them into `website/docs/` (git-ignored),
adds a unique title and meta description per page, and rewrites links so
pages link to each other on-site and to other repository files on GitHub.

A new file in `docs/` must be added to `PAGES` in `tools/prepare_docs.py`
(and to `nav` in `zensical.toml`); the script fails on an unmapped file or a
link to a file that does not exist.

## Build

From the repository root, with the Zensical version pinned in
`presentation/requirements.txt` installed:

```bash
python website/tools/prepare_docs.py
cd website
zensical build -f zensical.toml -s   # static site in ./site
```

The Pages workflow publishes this site at the root and the architecture
presentation under `/architecture/`.
