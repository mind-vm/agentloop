package ext

import (
	"fmt"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop/sandbox"
)

// StoreDoc is one indexed document surfaced to the sandbox by the
// stores primitive.
type StoreDoc struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	ChunkCount int64  `json:"chunkCount"`
}

// StoresBackend is the read surface the stores primitive needs. An
// application adapts its own document catalog to it (scoped to the
// session); this keeps this package free of that dependency.
type StoresBackend interface {
	// List returns every indexed document in the current scope.
	List() ([]StoreDoc, error)
	// Read returns the full text of one document (its chunks joined in
	// source order). An unknown id yields an empty string, no error.
	Read(documentID string) (string, error)
}

// StoresPack exposes a `stores` object with `list()` and `read(id)` for
// inspecting the indexed document corpus from inside the sandbox.
func StoresPack(backend StoresBackend) sandbox.Pack {
	return sandbox.Pack{
		Name:        "stores",
		Description: "Inspect the indexed document corpus — stores.list() / stores.read(id)",
		Prompt: `// --- Stores ---
/** The indexed document corpus. */
declare const stores: {
  /** List indexed documents. */
  list(): { id: string; title: string; chunkCount: number }[];
  /** Full text of one document by id (chunks joined in source order). */
  read(documentId: string): string;
};`,
		HelpEntries: map[string]string{
			"stores": `stores.list() — List indexed documents: [{ id, title, chunkCount }].
stores.read(id) — Return the full text of one document.
  Example: var docs = stores.list(); var text = stores.read(docs[0].id);`,
		},
		Register: func(rt *goja.Runtime, sb *sandbox.Sandbox) {
			listFn := func(goja.FunctionCall) goja.Value {
				if backend == nil {
					panic(rt.NewGoError(fmt.Errorf("stores.list: no stores backend configured")))
				}
				docs, err := backend.List()
				if err != nil {
					panic(rt.NewGoError(fmt.Errorf("stores.list: %w", err)))
				}
				out := make([]map[string]any, 0, len(docs))
				for _, d := range docs {
					out = append(out, map[string]any{"id": d.ID, "title": d.Title, "chunkCount": d.ChunkCount})
				}
				return rt.ToValue(out)
			}
			readFn := func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 || call.Argument(0).String() == "" {
					panic(rt.NewGoError(fmt.Errorf("stores.read: documentId is required")))
				}
				if backend == nil {
					panic(rt.NewGoError(fmt.Errorf("stores.read: no stores backend configured")))
				}
				id := call.Argument(0).String()
				text, err := backend.Read(id)
				if err != nil {
					panic(rt.NewGoError(fmt.Errorf("stores.read(%q): %w", id, err)))
				}
				return rt.ToValue(text)
			}

			obj := rt.NewObject()
			_ = obj.Set("list", listFn)
			_ = obj.Set("read", readFn)
			_ = rt.Set("stores", obj)
		},
	}
}
