// Prefixes root-relative links in Markdown content with the deployment `base`.
//
// Without this, every cross-page link in src/content/docs has to spell the
// deployment prefix out — `/agentloop/concepts/` — because Astro does not
// rewrite links written inside Markdown, and Starlight only prefixes the
// navigation it generates itself. That put the prefix in two dozen places and
// made `base` a value you could only change by grepping, with nothing to catch
// a link you missed. site.config.mjs used to carry a warning to that effect.
//
// With this plugin, content links are written without the prefix — plain
// `/concepts/`, the path as it would be at the site root — and the prefix is
// applied at build time from the one place it is defined.
//
// Markdown links only. Links built in MDX with JSX (`<a href={...}>`) are
// separate node types that this deliberately does not touch, so a page like
// 404.mdx that constructs hrefs in JavaScript still imports `base` directly.
// Images are not handled either; there are none in content today, and a
// root-relative `![](/x.png)` would need the same treatment added here.
import { base } from "./site.config.mjs";

// Trailing slash stripped so joining below is unambiguous. A `base` of "/"
// collapses to "", which correctly makes this plugin a no-op: at the site root
// a root-relative link is already right.
const prefix = base.replace(/\/+$/, "");

/** Depth-first walk over a hast tree. Inlined to avoid a dependency on
 * unist-util-visit for what is a few lines of recursion. */
function walk(node, fn) {
  fn(node);
  for (const child of node.children ?? []) walk(child, fn);
}

export function rehypeBaseLinks() {
  return (tree) => {
    if (!prefix) return;
    walk(tree, (node) => {
      if (node.type !== "element" || node.tagName !== "a") return;
      const href = node.properties?.href;
      if (typeof href !== "string") return;
      // Root-relative internal links only. "//host" is protocol-relative and
      // "https://" is absolute — both point off-site. Bare fragments and
      // relative paths already resolve against the current page.
      if (!href.startsWith("/") || href.startsWith("//")) return;
      // Idempotent: leave a link that already carries the prefix alone, so a
      // stray hard-coded one does not end up doubled.
      if (href === prefix || href.startsWith(`${prefix}/`)) return;
      node.properties.href = `${prefix}${href}`;
    });
  };
}
