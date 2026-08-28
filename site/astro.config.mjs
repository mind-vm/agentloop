// @ts-check
import { defineConfig } from "astro/config";
import { unified } from "@astrojs/markdown-remark";
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
      // The 404 page is src/content/docs/404.mdx. Starlight injects a
      // dedicated /404 route to render it, and that route is deliberately
      // left in place, even though it means every build logs:
      //
      //   Could not render `/404` from route `/[...slug]` as it conflicts
      //   with higher priority route `/404`.
      //
      // That warning is noise: the injected route and the [...slug] catch-all
      // render the identical component, and the built 404.html is byte-for-
      // byte the same either way. `disable404Route: true` silences it — but
      // it also makes the custom 404 unreachable under `astro dev`, which
      // falls back to Astro's own dev error page for every unmatched path,
      // including /404 itself. A 404 page you cannot look at while working on
      // it is worse than a line in the build log.
      social: [
        { icon: "github", label: "GitHub", href: "https://github.com/mind-vm/agentloop" },
      ],
      editLink: {
        baseUrl: "https://github.com/mind-vm/agentloop/edit/main/site/",
      },
      // Five groups, in the order a reader meets them: how to get running
      // (Start here), how to drive it from a terminal (The CLI), why
      // (Concepts), how to plug application-specific pieces in (Extending),
      // and exactly what's available (Reference).
      sidebar: [
        {
          label: "Start here",
          items: [{ autogenerate: { directory: "start" } }],
        },
        {
          label: "The CLI",
          items: [{ autogenerate: { directory: "cli" } }],
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
  //
  // Passed through `unified()` rather than the bare `markdown.rehypePlugins`
  // key, which Astro 7.2 deprecated. Naming the processor explicitly does not
  // shut integrations out of the pipeline: Astro merges plugins registered by
  // integrations into whatever processor is configured, as long as it is a
  // unified one, so Starlight's own Markdown handling is unaffected.
  markdown: {
    processor: unified({ rehypePlugins: [rehypeBaseLinks] }),
  },
});
