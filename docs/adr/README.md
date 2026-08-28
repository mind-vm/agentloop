# Architecture Decision Records

An ADR records a decision that shaped this library, and — more importantly —
*why*, so a future reader can tell whether the reasoning still holds.

`README.md` and the docs site under `site/` are the **rulebook**: what agentloop
does and how to use it. ADRs are where those rules come from — the context, the
decision, the price, and the conditions under which we would change our minds.
When something in the README makes you ask "but why is it like that?", the
answer should be a record here.

## These are living documents

A record here is **not** a contract, a sign-off, or a historical artefact. It is
the current best understanding of a problem and the solution chosen for it.
Understanding changes; when it does, the record changes with it. This follows
the model the sibling repository `mind-vm/studio-apps` uses in its own
`docs/adr/`, and [ADR-0001](0001-record-architecture-decisions.md) records why.

So:

- **Edit records in place** when your understanding improves. Add a line to
  *Revisions* saying what changed and why.
- **Reversing a decision is normal.** It means you learned something. Record
  what you learned — [ADR-0016](0016-mit-and-public.md) is a decision that was
  reversed twice in five days and the record is more useful for saying so.
- **Write them early**, while the reasoning is fresh. A record written after the
  fact tends to justify rather than explain; records 0002–0016 here were written
  retroactively and read like it.
- **Keep the Status field one word.** What shipped goes in *Revisions*.
- **Do not add process.** If you changed something architectural, write down why.

Write a *new* record instead of editing only when the **problem** changed, not
the answer — that is genuinely a different decision, and the old one stays as
context with its status set to `Replaced`.

## When to write one

Write one when a choice is hard to reverse, has real trade-offs, or will
otherwise be re-litigated every few months by someone who does not know why it
went the way it did. Signals: you argued about it, you rejected a reasonable
alternative, or the answer is going to surprise someone.

Do not write one for choices with an obvious default, or for things the code
already says plainly. Those belong in a doc comment.

## Status and confidence

Status describes maturity, not approval:

| Status | Meaning |
|---|---|
| **Exploring** | Direction chosen, not yet built enough to trust. Expect movement. |
| **Working** | Implemented, and holding up in practice so far. |
| **Revisiting** | Something has challenged it. Actively being reconsidered. |
| **Replaced** | The problem changed. Points at the record that replaced it, and stays for the reasoning trail. |
| **Dropped** | Abandoned with nothing replacing it. Stays for the reasoning trail. |

**Confidence** is separate, and is about evidence: High (built it, it held under
real use), Medium (built it, limited use), Low (reasoned about it, have not felt
the consequences yet). Being honest here is the point — `Working` / `Low` is a
useful signal, not an embarrassment.

## The Consumers line

studio-apps records carry an *Apps wanting it* line, which is rule-of-two
evidence for a shared platform. agentloop is a published library, so the header
line here is **Consumers** and the question is who the decision actually serves:
a named application, the library's own public API, or the repo's tooling. "None
yet" is still a warning — see [ADR-0002](0002-standalone-library-not-an-application.md),
where it is the central risk of the record.

## Reviewing

Each record carries a **Last reviewed** date. When you touch an area, glance at
its record: if it still reads true, bump the date; if it does not, fix it. A
record whose review date is far behind the code is a stale document, and stale
documents are worse than absent ones.

The **What would change our mind** section is where a record earns its keep. It
names the observation that should trigger a revisit. If you hit one of those
conditions, that is your cue to reopen the record — not to work around it
quietly.

## Writing one

Copy [`0000-template.md`](0000-template.md), take the next free number, and use a
short kebab-case slug. Numbers are allocated in order and never reused.

**Keep it short.** A page is usually enough. The sections are Context, Decision,
Consequences, What would change our mind, Cost of change, Revisions — and that
is the whole set. There is deliberately no *Alternatives considered*: a record
says what we are doing and what it costs, not why every other path was rejected.
Where a rejected option is genuinely close, name it in a line inside the section
it bears on.

## Index

| # | Title | Status | Confidence |
|---|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Working | High |
| [0002](0002-standalone-library-not-an-application.md) | A standalone library, not an application | Working | High |
| [0003](0003-a-bounded-work-horse.md) | A bounded work-horse, not a general agent | Revisiting | Medium |
| [0004](0004-code-as-action.md) | Code as action — one tool, one JavaScript block per turn | Working | High |
| [0005](0005-carry-by-return-see-by-log.md) | Carry by `return`, see by `log()` — show shape, not values | Working | High |
| [0006](0006-elide-stale-code-per-run.md) | Elide stale code, scoped to the current run | Working | High |
| [0007](0007-goja-synchronous-runtime.md) | goja in-process — ES5.1, synchronous, no event loop | Working | Medium |
| [0008](0008-sandbox-is-the-trust-boundary.md) | Capabilities build packs; the sandbox is the trust boundary | Working | High |
| [0009](0009-thin-core-optional-packages.md) | Keep the core thin; optional surfaces are separate packages | Working | High |
| [0010](0010-store-interfaces-and-conformance.md) | Small store interfaces, plus a conformance kit to hold them | Working | Medium |
| [0011](0011-openai-wire-and-retries.md) | One LLM protocol — the OpenAI wire, with retries in the client | Working | Medium |
| [0012](0012-a-thrown-turn-is-routine.md) | A thrown turn is routine, not a failure | Working | Medium |
| [0013](0013-context-control-from-one-number.md) | Three units of context control, derived from one number | Working | Medium |
| [0014](0014-retrieval-over-inlining.md) | Retrieval over inlining for project instructions and skills | Working | Medium |
| [0015](0015-observability-seams.md) | Observability is a seam, and off unless wired | Working | Medium |
| [0016](0016-mit-and-public.md) | MIT and public | Working | High |

## Lineage

This loop has been written more than once, and several records reconstruct
reasoning that predates this repository:

- **`valiro-ai/thinking-core`** (through 2026-03) — the earliest ancestor. An
  embeddable Go library with a goja sandbox, where the agent called a `think`
  tool and kept state on a persistent `data` global. Source for
  [ADR-0004](0004-code-as-action.md) and [ADR-0007](0007-goja-synchronous-runtime.md).
- **`mind-vm/studio-apps` `core/agentloop`** — the platform-module version, and
  the richest source. Its `CONCEPT.md` is the design rationale behind
  [ADR-0003](0003-a-bounded-work-horse.md), [ADR-0005](0005-carry-by-return-see-by-log.md)
  and [ADR-0006](0006-elide-stale-code-per-run.md); its `ASSESSMENT.md` (2026-07)
  is the honest weakness review behind [ADR-0007](0007-goja-synchronous-runtime.md).
  studio-apps' own `docs/adr/` holds the platform-side records — 0015, 0017,
  0033–0039, 0044, 0048 and 0049 all bear on this loop.
- **mybrain** — the application this repository was extracted from on 2026-08-21.
- **valiro-go `internal/agent`** — an independent reimplementation, and the
  comparison that surfaced the elision bug in
  [ADR-0006](0006-elide-stale-code-per-run.md).

> Records **0002–0016 were written retroactively on 2026-08-28**, reconstructed
> from commit bodies, the README, and the ancestor repositories above. The
> decisions themselves predate the records by up to five months, and none of them
> carries the reasoning a record written at the time would have. Treat their
> *Context* sections as reconstructions.
