package sandbox

// EventKind identifies the type of sandbox event. The vocabulary is
// the observability surface every cross-cutting consumer (trace,
// audit, analytics, frontend drawer) reads.
//
// New kinds get added here when a new pack lands; existing values
// are stable.
type EventKind string

const (
	// EventScript fires when Sandbox.Execute begins a JS run.
	EventScript EventKind = "script"
	// EventScriptResult fires when a JS run finishes (success or error).
	EventScriptResult EventKind = "script_result"
	// EventLog is a captured log() / print() / console.log() call.
	EventLog EventKind = "log"
	// EventDocumentSearch is a document-search primitive call.
	EventDocumentSearch EventKind = "document_search"
	// EventWebSearch is a web-search primitive call.
	EventWebSearch EventKind = "web_search"
	// EventFetch is an outbound HTTP request via fetch() / require('http').
	EventFetch EventKind = "fetch"
	// EventMemory historically fired on memory ops; today it's reused
	// for secret reads. Kept named "memory" so existing trace consumers
	// don't break.
	EventMemory EventKind = "memory"
	// EventAsk fires when an input request is dispatched to a human.
	EventAsk EventKind = "ask"
	// EventAskResult fires when the user's response arrives.
	EventAskResult EventKind = "ask_result"
	// EventSkillCall fires when a user-authored skill module is invoked.
	EventSkillCall EventKind = "skill_call"
	// EventAI fires when ai() / aiJSON() / aiSearch() runs.
	EventAI EventKind = "ai"
	// EventPlan fires when plan() emits a reasoning step.
	EventPlan EventKind = "plan"
	// EventEmail fires when sendEmail() attempts a send.
	EventEmail EventKind = "email"
	// EventCard fires when a card() primitive emits a structured
	// artifact (deep-link, session-link, …).
	EventCard EventKind = "card"
	// EventCollInsert / Find / Update / Remove track per-store
	// collection ops. Application-supplied stores emit these.
	EventCollInsert EventKind = "collection_insert"
	EventCollFind   EventKind = "collection_find"
	EventCollUpdate EventKind = "collection_update"
	EventCollRemove EventKind = "collection_remove"
	// EventAssetWrite fires when asset.write() persists a file.
	EventAssetWrite EventKind = "asset_created"
	// EventFileRead is a read of a workspace file — readFile(), and the
	// directory and search primitives that enumerate one.
	EventFileRead EventKind = "file_read"
	// EventFileWrite is a mutation of a workspace file: writeFile() or
	// editFile(). Distinct from EventFileRead because it is the one a
	// reviewer of a run's trace actually needs to find.
	EventFileWrite EventKind = "file_write"
	// EventBlock carries a structured render block (chart/table/process-map/
	// sql/…) emitted by a pack for a rich UI to render. Summary names the block
	// type; the structured spec rides in Event.Payload.
	EventBlock EventKind = "block"
	// EventError fires when a primitive errors.
	EventError EventKind = "error"
	// EventWarning fires for non-fatal degradation the trace should
	// surface — e.g. a capability whose Build failed and was skipped,
	// so the run proceeds with a smaller tool surface than configured.
	EventWarning EventKind = "warning"
	// EventAnswer fires when answer() delivers the run's final result.
	// Result carries the value (a string, or a JSON-encoded object/array).
	// The loop treats a called answer() as terminal.
	EventAnswer EventKind = "answer"
)

// Event is one observability emission from inside the sandbox.
// Summary is the one-line description a trace / drawer renders;
// Detail carries the long-form payload (code, args, response body);
// Result carries the output when the kind implies one (a fetch
// response, an ai() reply).
type Event struct {
	Kind    EventKind `json:"kind"`
	Summary string    `json:"summary"`
	Detail  string    `json:"detail,omitempty"`
	Result  string    `json:"result,omitempty"`
	// Payload carries a structured value when the kind implies one (e.g. a
	// render block's spec). Optional; string-only consumers ignore it.
	Payload map[string]any `json:"payload,omitempty"`
}

// OnEvent is the callback shape Sandbox uses to publish events. A
// caller can wrap this into a Go channel so multiple consumers can
// subscribe without the sandbox itself fanning out.
type OnEvent func(Event)
