// Package eval is a small LLM-judged eval harness: a Suite of Cases
// (input + judge criteria), each dispatched through an agentloop.Loop
// and scored by a judge llm.Client on a 0-10 scale.
//
// This is a port of studio-apps' core/evals — extracted the same way
// the rest of this module was, with two simplifications specific to
// agentloop rather than a multi-tenant platform:
//
//   - No OwnerID scoping. The original's Store methods took an
//     ownerID to enforce tenant isolation across products sharing one
//     harness. agentloop has no tenancy concept baked into its own
//     stores (see agentloop.SessionStore) — an application that needs
//     scoping can add it to its own Store implementation.
//   - No agents.Runner / llm.Registry indirection. The original
//     dispatched cases through a Runner interface and looked up a
//     judge client from a Registry, because the platform runs many
//     named agents behind one harness. Here, Service just takes an
//     agentloop.Loop directly — its Run method already has exactly
//     the shape a Runner provided — and an llm.Client for judging.
//     SessionStore.Get's documented auto-create-on-unknown-ID behavior
//     means RunSuite needs no separate session-creation step: each
//     case gets a fresh session ID, and the Loop's own SessionStore
//     provisions it.
//
// Service.RunSuite dispatches every Case in a Suite through the Loop,
// has the judge llm.Client rate each response against the case's
// criteria (either a single rubric with PassThreshold, or a
// per-criterion CriteriaItems breakdown scored and rolled up
// separately), and persists the run via Store. A case that errors
// (agent failure or judge failure) records the error inline rather
// than aborting the suite — RunSuite always finishes and returns a
// Run.
//
// Store is a persistence seam, not an opinion: package evalmem ships
// an in-memory implementation for local dev and CI; back it with
// whatever your application already uses for anything that needs to
// survive a process restart.
package eval
