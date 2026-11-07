// Command blocks is a runnable demo of ext.BlocksPack behind an HTTP API:
// the agent reads a small sales dataset, shows what it finds as tables,
// charts and KPI panels while it works, and a browser draws each block the
// moment it arrives.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...          # or OPENAI_BASE_URL for any OpenAI-wire endpoint
//	(cd examples/blocks/web && npm install && npm run build)
//	go run ./examples/blocks              # http://localhost:8095
//
// For frontend work, run `npm run dev` in web/ instead of building: Vite
// serves the page and proxies /api here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/uuid"

	"github.com/mind-vm/thinking-core/agentloop"
	"github.com/mind-vm/thinking-core/agentloopmem"
	"github.com/mind-vm/thinking-core/agentrun"
	"github.com/mind-vm/thinking-core/ext"
	"github.com/mind-vm/thinking-core/llm"
	"github.com/mind-vm/thinking-core/sandbox"
)

// guidance names where the numbers live. The pack's default says "data a
// script read"; saying which files turns that from a principle into
// something the model can act on in its first script.
const guidance = `// Present results as blocks the UI renders, then finish with answer() and a short text summary.
// The data is in the workspace (listDir(), readFile()). Every number in a block must come from it —
// never invent one. Parse CSV in a script, keep the rows in state, and build blocks from state.
// When the request leaves real choices open (period, regions, which view), ask them all at once with
// ask({type: "questions"}) before computing. When revising a block from an earlier answer, reuse its id.`

func main() {
	addr := flag.String("addr", defaultAddr(), "listen address ($PORT picks the port)")
	data := flag.String("data", "examples/blocks/data", "directory the agent may read")
	web := flag.String("web", "examples/blocks/web/dist", "built frontend to serve")
	flag.Parse()

	client, err := llm.NewOpenAI(llm.ConfigFromEnv())
	if err != nil {
		log.Fatalf("blocks: %v", err)
	}
	root, err := os.OpenRoot(*data)
	if err != nil {
		log.Fatalf("blocks: open data directory: %v", err)
	}

	broker := agentrun.NewBroker()
	l := agentloop.New(agentloop.Config{
		LLM: client,
		// In memory: a restart forgets every conversation, which is the
		// right trade for a demo and the wrong one for anything else —
		// agentloopsql is the durable pair.
		Sessions:       agentloopmem.NewSessionStore(nil),
		Steps:          agentloopmem.NewStepStore(),
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: capabilities(client, root, broker)},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/run", runHandler(l, broker))
	mux.HandleFunc("POST /api/reply", replyHandler(broker))
	mux.Handle("/", frontend(*web))
	log.Printf("blocks: http://%s (data: %s)", *addr, *data)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// defaultAddr honours $PORT because process managers and preview tools
// hand out ports that way; 8095 otherwise, away from the 8080/8090 that
// other local servers tend to hold.
func defaultAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return "localhost:" + p
	}
	return "localhost:8095"
}

// capabilities is the default bundle without the network — the dataset is
// local and a demo has no business fetching — plus the workspace, read
// only, the blocks, and ask() wired to the page through the broker.
func capabilities(client llm.Client, root *os.Root, broker *agentrun.Broker) []agentloop.Capability {
	caps := slices.DeleteFunc(agentloop.DefaultCapabilities(client, ""), func(c agentloop.Capability) bool {
		return c.Name == "fetch" || c.Name == "http"
	})
	return append(caps,
		agentloop.Capability{
			Name: "workspace",
			Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{ext.WorkspacePack(bc.Ctx, root, ext.WorkspaceOptions{
					// Refusing outright rather than leaving it to the policy:
					// the reason reaches the model, and "read-only" stops it
					// from planning around a write it will never get.
					Refuse: func(string) string {
						return "the demo dataset is read-only; keep derived data in state instead"
					},
				})}, nil
			},
		},
		agentloop.Capability{
			Name: "ask",
			Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
				sid, err := uuid.Parse(bc.SessionID)
				if err != nil {
					return nil, fmt.Errorf("ask: session id %q is not a uuid", bc.SessionID)
				}
				return []sandbox.Pack{sandbox.InputPack(broker.Requester(sid))}, nil
			},
		},
		agentloop.Capability{
			Name: "blocks",
			Build: func(agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{ext.BlocksPack(ext.BlocksOptions{Guidance: guidance})}, nil
			},
		},
	)
}

type runRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"sessionId"`
}

// runHandler runs one turn of a conversation and streams it. The session id
// comes back in the terminal `done` event; sending it with the next message
// continues the same conversation, state and all.
func runHandler(l agentloop.Loop, broker *agentrun.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req runRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
			http.Error(w, `want {"message": "...", "sessionId"?: "..."}`, http.StatusBadRequest)
			return
		}
		sid, err := uuid.Parse(req.SessionID)
		if err != nil {
			sid = uuid.New()
			req.SessionID = sid.String()
		}
		stream := agentrun.Open(w)
		if stream == nil {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		// The demo has one tenant, so the broker's tenant check compares
		// uuid.Nil with itself. An application passes the caller's org
		// here and in Reply, and a reply from another org then finds no run.
		end := broker.Begin(sid, uuid.Nil, stream.Send)
		defer end()
		res, err := l.Run(r.Context(), agentloop.RunRequest{
			SessionID: req.SessionID,
			Scope:     agentloop.Scope{WorkspaceID: "demo"},
			Message:   req.Message,
			OnEvent:   func(e agentloop.RunEvent) { stream.Send(agentrun.FromRunEvent(e)) },
		})
		if errors.Is(err, context.Canceled) {
			return // the client went away; nobody is reading
		}
		stream.Finish(agentrun.SummaryOf(res), err)
	}
}

type replyRequest struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Value     any    `json:"value"`
}

// replyHandler answers a pending ask(). The value goes through as decoded
// JSON; ask() checks it against what was asked before the script sees it.
func replyHandler(broker *agentrun.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req replyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RequestID == "" {
			http.Error(w, `want {"sessionId", "requestId", "value"}`, http.StatusBadRequest)
			return
		}
		sid, err := uuid.Parse(req.SessionID)
		if err != nil {
			http.Error(w, "sessionId is not a uuid", http.StatusBadRequest)
			return
		}
		if err := broker.Reply(sid, uuid.Nil, req.RequestID, req.Value); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// frontend serves the built page, or explains how to build it — a blank
// 404 at the root is the most confusing possible first impression.
func frontend(dir string) http.Handler {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "The frontend is not built.\n\n  cd examples/blocks/web && npm install && npm run build\n\nor run `npm run dev` there and open the URL it prints.\n")
		})
	}
	return http.FileServer(http.Dir(dir))
}
