// Package agentloopsql is a SQLite-backed agentloop.SessionStore and
// agentloop.StepStore: the durable counterpart to agentloopmem, for a
// CLI or a single-host service that wants sessions to outlive the
// process.
//
// It uses modernc.org/sqlite, a pure-Go driver, so a binary embedding
// this package still cross-compiles with nothing but GOOS and GOARCH.
//
// Both interfaces are implemented by one *Store over one database, so a
// session and its trace stay in a single file that can be copied,
// inspected with the sqlite3 CLI, or deleted wholesale.
//
// # Step ordering
//
// Steps are ordered by insertion, not by RunStep.StepIndex. That is not
// a stylistic choice: the loop derives the next index from the number of
// steps its history window returned, so a session that outgrows that
// window restarts numbering and writes indices it has already used. A
// (session_id, step_index) primary key would reject every write from
// that point on. StepIndex is stored as recorded and read back
// unchanged; the row's own autoincrement id is what LastN orders by.
package agentloopsql
