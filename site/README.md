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
| `404.mdx` | The not-found page — see `disable404Route` in `astro.config.mjs` |
| `start/` | Install, wire up a loop, run the example CLI |
| `concepts/` | Why JavaScript instead of tool calls; what each package does |
| `extending/` | Capabilities, sandbox composition, stores, policy |
| `reference/` | Known limitations, license, links to pkg.go.dev and GitHub |

## Styling

There is none of our own, deliberately. Starlight is a complete theme, and
every page here is prose plus its built-in components — `Card`, `CardGrid`,
`LinkCard` from `@astrojs/starlight/components`. Reach for those before
writing markup: they already match the theme and follow the light/dark
switch.

The site did briefly carry Tailwind. It was removed once it was clear that
exactly one page used it, to reimplement a card that `LinkCard` already
draws. If a genuinely custom page or component arrives later, Starlight
supports Tailwind properly (`@astrojs/starlight-tailwind`) and it can come
back — but two design systems for four documentation pages was not paying
for itself.

To restyle, override Starlight's `--sl-color-*` custom properties in a
stylesheet passed to Starlight's `customCss`, rather than introducing a
second system.

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
