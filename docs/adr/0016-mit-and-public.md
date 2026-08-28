# ADR-0016: MIT and public

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-08-26
- **Last reviewed:** 2026-08-28
- **Consumers:** every machine that builds against this module, and the docs
  site, which cannot be served from a private repository on this plan.

## Context

The repository was published MIT, switched to an all-rights-reserved notice five
days later when it went private, and switched back a week after that. The middle
state is the one worth recording, because the reasoning that ended it applies to
any future attempt.

**The IP the notice protected is thinner than it looked.** Agents that act by
writing code rather than emitting tool-call JSON are established prior art — the
CodeAct paper (ICML 2024), HuggingFace's smolagents `CodeAgent`, Cloudflare's
Code Mode, Anthropic's code-execution-with-MCP — and smolagents already persists
interpreter state between steps, which is the closest published thing to a novel
mechanism here. What is actually specific to agentloop is the `run(args) →
return` protocol, the shape digest shown in place of the value, stale-code
elision, and `pool`'s context swap. Those are good engineering, none of them is
a moat, and **all four are described in full in the README** — so keeping the
repository private protected none of them.

**What private did cost is concrete**: `GOPRIVATE`, an `insteadOf` rewrite and a
credential on every machine that builds against this. It also took the docs site
down outright — GitHub Pages will not serve a private repository on this plan —
and the site was the only real documentation apart from the README, five pages
that existed nowhere else.

## Decision

The repository is public and MIT-licensed.

- **MIT**, matching the sibling libraries and the packs adapted from
  `owainlewis/neo`, which is MIT and attributed in the file header
  ([ADR-0011](0011-openai-wire-and-retries.md)).
- **Public**, so `go get` works with no credential, and so the Astro/Starlight
  docs site under `site/` can be served from Pages.
- **The value is in the running system, not the source.** The differentiated
  asset is the harness plus the evals plus the operational experience of running
  it ([ADR-0003](0003-a-bounded-work-horse.md)), and a published library that is
  actually adopted is worth more than a private one that is not
  ([ADR-0002](0002-standalone-library-not-an-application.md)).

## Consequences

**Buys.** No credential plumbing anywhere. The docs site works. External
adoption is possible at all, which is the premise of extracting the library.

**Costs.** MIT is irrevocable for every version already published — anyone may
fork what exists. Public also means the design is legible to anyone building the
same thing, though the README gave that away regardless.

## What would change our mind

- **Something genuinely novel and defensible is built here.** Then it belongs in
  a separate private module, not in a relicensing of this one — which is the
  cheaper move and the one the middle state should have been.
- **Support burden from external users exceeds the value of adoption.** That is
  an argument about issue triage, not about the licence.

## Cost of change

Effectively irreversible in the open direction. Every published tag stays MIT
forever; a future relicense binds only new versions and invites a fork of the
last MIT one. Going private again would restore the `GOPRIVATE` tax and take the
site down a second time.

## Revisions

- 2026-08-21 — Published MIT.
- 2026-08-21 — Switched to all-rights-reserved when the repository went private.
- 2026-08-25 — Docs site deleted, because Pages cannot serve a private
  repository on this plan.
- 2026-08-26 — Reverted to MIT, repository public again, and the site restored
  as it stood — with the `jryannel` → `mind-vm` rename applied, which it had
  missed by being deleted before it, and with the two pages that described the
  licence corrected.
- 2026-08-28 — Recorded.
