// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";

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
});
