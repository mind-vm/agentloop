# ADR-NNNN: Short imperative title

- **Status:** Exploring | Working | Revisiting | Replaced by [ADR-NNNN](.) | Dropped
- **Confidence:** High | Medium | Low
- **Decided:** YYYY-MM-DD
- **Last reviewed:** YYYY-MM-DD
- **Consumers:** <who this actually serves — a named application, the library's
  public API, or the repo's own tooling. "The library's own API" is an answer;
  "none yet" is a warning worth writing down.>

## Context

The problem and the constraints that are actually real, in a few sentences. Not
the argument that got us here — the facts a reader needs to judge the decision.

## Decision

What we are doing, stated plainly. Present tense, active voice.

## Consequences

**Buys.** The benefits, concretely. Two or three bullets.

**Costs.** The real price, without hedging. Every decision has one.

## What would change our mind

Two or three bullets. The observation that should trigger a revisit, specific
enough to recognise: "if a second consumer needs X", not "if it turns out badly".
For a library the most useful trigger is usually a count — of callers who had to
work around it, of copies of the same shim, of the same bug reported twice.

## Cost of change

What reversing this would take, in a line or two. Note asymmetry where it exists
— cheap in one direction and expensive in the other is the usual case, and the
most important thing to know. For anything on the exported API, say whether the
bill lands on downstream callers or stays inside the module.

## Revisions

- YYYY-MM-DD — Written.

<!-- What shipped and which callers adopted it goes here — not in the Status
     field. Keep Status to one word. -->

---

Keep the whole thing under a page. A record that argues its case at length has
already failed: the reader wants the decision and the price, not the debate.
