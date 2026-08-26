# site

The documentation site: [Astro](https://astro.build) with
[Starlight](https://starlight.astro.build).

```bash
npm install
npm run dev     # serve with live reload
npm run build   # build into site/dist
npm run preview # preview the built site
```

## Where the content lives

Unlike a docs/-sourced site, every page under `src/content/docs/` here is
**hand-written** — there's no repository-root `docs/` directory to generate
from. That's proportional to what agentloop is: one Go module, five
packages, a README. If agentloop grows a `docs/` directory worth publishing
on its own terms, lift the `sync-docs.mjs` pattern from `../sqlb/site`
rather than growing this site's page count by hand indefinitely.

| Directory | Contents |
|---|---|
| `index.mdx` | The landing page |
| `start/` | Install, wire up a loop, run the example CLI |
| `concepts/` | Why JavaScript instead of tool calls; what each package does |
| `extending/` | Capabilities, sandbox composition, stores, policy |
| `reference/` | Known limitations, license, links to pkg.go.dev and GitHub |

## Deployment

`site.config.mjs` holds `site` and `base`, currently set for a GitHub Pages
project site at `/agentloop`. `astro.config.mjs` reads it to configure Astro;
Starlight prefixes its own navigation links automatically, but a handful of
links written out in full in `index.mdx` (the hero `link:` values, which are
frontmatter and not touched by Starlight's base-prefixing) repeat the prefix
and would need updating by hand if `base` ever changes.

`.github/workflows/pages.yml` builds and deploys on a push to `main` that
touches `site/**`, and on `workflow_dispatch`.

It's deliberately separate from any Go CI: the site is Node/Astro, and
folding it into a Go gate would make Node a build dependency of a library
whose whole point is that it imposes none. A red site build should not be
able to say the library itself is broken.
