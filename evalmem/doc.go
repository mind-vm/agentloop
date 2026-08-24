// Package evalmem provides an in-memory eval.Store — for local dev,
// CI, and one-shot eval runs. Not for production traffic: no
// durability, no cross-process visibility. Mirrors agentloopmem's
// role for SessionStore/StepStore, for eval.Store.
package evalmem
