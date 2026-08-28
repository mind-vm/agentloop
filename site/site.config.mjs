// Where the site is deployed. This is the one place the deployment path is
// written, and changing `base` here is now the whole job — content does not
// repeat the prefix anywhere.
//
// Three things consume it. astro.config.mjs configures Astro with it.
// rehype-base-links.mjs prefixes the root-relative links in Markdown content
// at build time, so pages link to `/concepts/` and never to `/agentloop/...`.
// And MDX that builds hrefs in JavaScript rather than Markdown — 404.mdx —
// imports `base` directly, since the rehype plugin does not rewrite JSX.
//
// The one place a link still cannot reach `base` is YAML frontmatter, which
// cannot import: the hero actions in index.mdx are written relative
// ("start/"), which resolves correctly under any base because that page sits
// at the deployment root.
export const site = "https://mind-vm.github.io";
export const base = "/agentloop";
