package ext

import (
	"fmt"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop/sandbox"
)

// SearchHit is one semantic-search result surfaced to the sandbox. The
// JSON tags drive the JS-side key names (the pack converts hits to
// plain maps so the runtime sees camelCase keys).
type SearchHit struct {
	DocumentID    string  `json:"documentId"`
	DocumentTitle string  `json:"documentTitle"`
	ChunkIndex    int     `json:"chunkIndex"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
}

// SearchPack exposes a synchronous `documentSearch(query, topK?)`
// primitive backed by a semantic-search / RAG retriever. It takes a
// search callback rather than a concrete retriever so this package
// stays free of that dependency — the application's Capability closes
// the callback over its own retriever and the per-session scope.
//
// topK defaults to 5 when omitted or <= 0.
func SearchPack(search func(query string, topK int) ([]SearchHit, error)) sandbox.Pack {
	return sandbox.Pack{
		Name:        "documentSearch",
		Description: "Semantic search over the indexed document corpus — documentSearch(query, topK?)",
		Prompt: `// --- Document search ---
/** Semantic search over the agent's document corpus. Returns up to topK matches (default 5), highest score first. */
declare function documentSearch(query: string, topK?: number): { documentId: string; documentTitle: string; chunkIndex: number; content: string; score: number }[];`,
		HelpEntries: map[string]string{
			"documentSearch": `documentSearch(query, topK?) — Semantic search over the document corpus.
  Returns an array of { documentId, documentTitle, chunkIndex, content, score }, highest score first.
  Example: var hits = documentSearch("refund policy", 3);`,
		},
		Register: func(rt *goja.Runtime, sb *sandbox.Sandbox) {
			fn := func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 || call.Argument(0).String() == "" {
					panic(rt.NewGoError(fmt.Errorf("documentSearch: query is required")))
				}
				query := call.Argument(0).String()
				topK := 5
				if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) && !goja.IsNull(call.Argument(1)) {
					if n := int(call.Argument(1).ToInteger()); n > 0 {
						topK = n
					}
				}
				if search == nil {
					panic(rt.NewGoError(fmt.Errorf("documentSearch: no search backend configured")))
				}
				hits, err := search(query, topK)
				if err != nil {
					panic(rt.NewGoError(fmt.Errorf("documentSearch(%q): %w", query, err)))
				}
				out := make([]map[string]any, 0, len(hits))
				for _, h := range hits {
					out = append(out, map[string]any{
						"documentId":    h.DocumentID,
						"documentTitle": h.DocumentTitle,
						"chunkIndex":    h.ChunkIndex,
						"content":       h.Content,
						"score":         h.Score,
					})
				}
				return rt.ToValue(out)
			}
			_ = rt.Set("documentSearch", fn)
		},
	}
}
