# ADR-0001: Record architecture decisions

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-08-28
- **Last reviewed:** 2026-08-28
- **Consumers:** this repository's own maintainers, and anyone reading the
  exported API and wondering why it is shaped the way it is.

## Context

agentloop is a small library with an unusually opinionated core: the model acts
by writing JavaScript, return values are threaded server-side rather than
re-serialised into the prompt, and stale code is elided from history. None of
that is obvious from the type signatures, and all of it has been re-argued at
least once already — the code-vs-tool-calls choice was made for a
now-superseded reason (see [ADR-0004](0004-code-as-action.md)), and the licence
was flipped twice in five days ([ADR-0016](0016-mit-and-public.md)).

The `README.md` and the docs site under `site/` are the **rulebook**: what the
library does and how to use it. They deliberately do not carry the reasoning,
because a README that explains every historical trade-off stops being a README.

The sibling repository `mind-vm/studio-apps` keeps its records in
`docs/adr/` under a living-document model. Using a different convention here
would mean two vocabularies for one person's two halves of the same problem.

## Decision

We keep ADRs in `docs/adr/`, under the same living-document model studio-apps
uses:

- **Records are edited in place** when understanding improves. Add a line to
  *Revisions* saying what changed and why. Only write a *new* record when the
  problem changed, not the answer; the old one stays as context with its status
  set to `Replaced`.
- **Status is one word** — `Exploring`, `Working`, `Revisiting`, `Replaced`,
  `Dropped` — and describes maturity, not approval. **Confidence** is separate
  and is about evidence: High (built it, it held under real use), Medium (built
  it, limited use), Low (reasoned about it, have not felt the consequences).
- **Sections are fixed**: Context, Decision, Consequences, What would change our
  mind, Cost of change, Revisions. There is no *Alternatives considered* — a
  record says what we are doing and what it costs, not why every other path was
  rejected.
- **One header line differs from studio-apps.** Its *Apps wanting it* line is
  rule-of-two evidence for a shared platform. agentloop is a published library,
  so the equivalent question is who the decision serves; the line is
  **Consumers**, and "none yet" is still a warning.

## Consequences

**Buys.** The reasoning behind the load-bearing mechanisms survives outside one
person's head and outside commit messages that nobody greps. Several of the
records here were written *from* commit bodies that already contained excellent
reasoning nobody would ever find again.

**Costs.** Fifteen more files to keep true. A record whose *Last reviewed* date
is far behind the code is worse than no record, so this is a standing
maintenance obligation, not a one-off write-up.

## What would change our mind

- **Records start contradicting the README.** Two documents drifting apart is
  worse than one incomplete one; the fix is to fold a `Working` record's rule
  into the README, not to keep both.
- **Nobody reads them.** If a decision gets re-litigated anyway with the record
  sitting right there, the records are in the wrong place — the docs site or the
  package doc comments would be the next thing to try.

## Cost of change

Near zero. Abandoning this is deleting a directory; nothing imports it and no
tooling depends on it.

## Revisions

- 2026-08-28 — Written, together with records 0002–0016. Those fifteen are
  **retroactive**: the decisions predate the record by up to five months, and
  their Context sections are reconstructions from commit bodies, the README,
  and the three ancestor repositories listed in the index. Treat them as such.
