# site

The documentation site: [Astro](https://astro.build) with
[Starlight](https://starlight.astro.build), styled with
[Tailwind](https://tailwindcss.com).

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
| `404.mdx` | The not-found page — see `disable404Route` in `astro.config.mjs` |
| `start/` | Install, wire up a loop, run the example CLI |
| `concepts/` | Why JavaScript instead of tool calls; what each package does |
| `extending/` | Capabilities, sandbox composition, stores, policy |
| `reference/` | Known limitations, license, links to pkg.go.dev and GitHub |

## Styling

Starlight owns the layout and supplies the default theme; Tailwind supplies
utility classes on top of it. `src/styles/global.css` is the single entry
point, registered via Starlight's `customCss` in `astro.config.mjs` so it
lands in the right cascade layer — utilities can then override a Starlight
component style without `!important`.

Two things there are load-bearing and explained in the file itself: the
`@layer` order declaration, and the fact that Tailwind's Preflight is
deliberately *not* imported (it would reset the typographic defaults
Starlight uses to render prose).

`@astrojs/starlight-tailwind` bridges the two systems, mapping Tailwind's
`@theme` tokens onto the CSS variables Starlight's components read — so
`bg-accent-600` and Starlight's own accent colour are the same colour.
Retheme the site by overriding `--color-accent-*` / `--color-gray-*` in an
`@theme` block in `global.css`, not by overriding `--sl-*` variables.

## Deployment

`site.config.mjs` holds `site` and `base`, currently set for a GitHub Pages
project site at `/agentloop`. Changing `base` there is the whole job — no
content repeats the prefix, so there is nothing to grep for afterwards.

Links in content are written **without** the deployment prefix —
`[Concepts](/concepts/)`, the path as it would be at the site root — and
`rehype-base-links.mjs` adds the prefix at build time. Two cases sit outside
that plugin: MDX that builds hrefs in JavaScript rather than Markdown
(`404.mdx`) imports `base` directly, and the hero actions in `index.mdx` are
relative (`start/`) because YAML frontmatter cannot import and Starlight does
not prefix hero links itself.

`.github/workflows/pages.yml` builds and deploys on a push to `main` that
touches `site/**`, and on `workflow_dispatch`.

It's deliberately separate from any Go CI: the site is Node/Astro, and
folding it into a Go gate would make Node a build dependency of a library
whose whole point is that it imposes none. A red site build should not be
able to say the library itself is broken.
