// Package agentloopmem provides in-memory implementations of
// agentloop.StepStore and agentloop.SessionStore.
//
// Use cases:
//
//   - Local dev / smoke tests where standing up a database is overkill.
//   - Eval suites that need deterministic, ephemeral session state.
//   - Stub stores while an application's real schema is still being
//     designed, so loop wiring can land before the migrations do.
//
// The stores are concurrency-safe (each method takes a mutex) and reset
// on process exit. They MUST NOT be used as the system of record for
// real traffic — there's no durability, no cross-process visibility,
// and no audit trail.
//
// For production, implement agentloop.StepStore + agentloop.SessionStore
// against your own schema — see agentlooptest.StepStoreContract for a
// reusable conformance harness to hold that implementation to the same
// behavioural guarantees the loop relies on.
package agentloopmem
