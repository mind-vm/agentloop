// Where the site is deployed. This is the one place the deployment path is
// written: astro.config.mjs reads it to configure Astro, and the hand-written
// pages under src/content/docs read it for the links Starlight does not
// prefix itself (frontmatter hero/card links, mainly).
//
// Deploying somewhere else — a custom domain, a user page — means changing
// `base` to "/" here and then fixing every link that hard-codes the old
// prefix; there is no generator here to catch a stale one, so grep for
// "/agentloop/" under src/content/docs after changing this.
export const site = "https://mind-vm.github.io";
export const base = "/agentloop";
