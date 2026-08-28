// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";

import { rehypeBaseLinks } from "./rehype-base-links.mjs";
import { base, site } from "./site.config.mjs";

// Every page under src/content/docs is hand-written — there is no docs/
// directory in the repo root to generate from, unlike larger sibling
// projects. That is proportional to what agentloop is: one Go module, five
// packages, a README. If that changes (a docs/ directory worth publishing
// on its own), lift the sync-docs.mjs pattern from ../sqlb/site rather than
// growing this file's SOURCES-shaped state by hand.
export default defineConfig({
  site,
  base,
  integrations: [
    starlight({
      title: "agentloop",
      description:
        "A small Go engine for LLM agents that think by writing JavaScript " +
        "instead of calling named tools.",
      // The 404 page is src/content/docs/404.mdx, served through the normal
      // [...slug] route. Starlight otherwise injects a second, higher-priority
      // /404 route; with a 404 entry in the collection both routes build the
      // same path, and Astro warns about the collision on every build. The two
      // render the identical component, so dropping the injected one costs
      // nothing here and keeps the build log clean.
      //
      // Two consequences worth knowing. Deleting 404.mdx now removes the 404
      // page entirely rather than falling back to Starlight's built-in one.
      // And the injected route was the one marked `prerender`, which is moot
      // for this static build but would matter if the site ever moved to SSR —
      // revisit this line if it does.
      disable404Route: true,
      social: [
        { icon: "github", label: "GitHub", href: "https://github.com/mind-vm/agentloop" },
      ],
      editLink: {
        baseUrl: "https://github.com/mind-vm/agentloop/edit/main/site/",
      },
      // Four groups, in the order a reader meets them: why (Concepts), how to
      // get running (Start here), how to plug application-specific pieces in
      // (Extending), and exactly what's available (Reference).
      sidebar: [
        {
          label: "Start here",
          items: [{ autogenerate: { directory: "start" } }],
        },
        {
          label: "Concepts",
          items: [{ autogenerate: { directory: "concepts" } }],
        },
        {
          label: "Extending",
          items: [{ autogenerate: { directory: "extending" } }],
        },
        {
          label: "Reference",
          items: [
            { autogenerate: { directory: "reference" } },
            {
              label: "API reference (pkg.go.dev)",
              link: "https://pkg.go.dev/github.com/mind-vm/agentloop",
              attrs: { target: "_blank" },
            },
            {
              label: "Source on GitHub",
              link: "https://github.com/mind-vm/agentloop",
              attrs: { target: "_blank" },
            },
          ],
        },
      ],
    }),
  ],
  // Content links are written without the deployment prefix — `/concepts/`,
  // not `/agentloop/concepts/` — and this adds it at build time from the one
  // place `base` is defined. See rehype-base-links.mjs for what it does and
  // does not cover.
  markdown: {
    rehypePlugins: [rehypeBaseLinks],
  },
});
