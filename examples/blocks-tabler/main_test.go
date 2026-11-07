package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/mind-vm/thinking-core/agentloop"
	"github.com/mind-vm/thinking-core/agentloopmem"
	"github.com/mind-vm/thinking-core/agentrun"
	"github.com/mind-vm/thinking-core/llm"
)

// scriptedModel is an OpenAI-wire endpoint that answers each completion
// with the next canned reply, streamed as SSE — enough to drive the whole
// loop with no API key.
func scriptedModel(t *testing.T, replies ...string) *httptest.Server {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		if i >= len(replies) {
			http.Error(w, "the script has no more replies", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": replies[i]}}}})
		stop, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk, stop)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func js(code string) string { return "```javascript\n" + code + "\n```" }

type frame struct{ Slot, ID, Mode, HTML string }

// runTurn posts a message and collects the fragments, answering every ask()
// form with answer as it arrives — the reply has to land while the stream
// is still open, because the script is blocked on it.
func runTurn(t *testing.T, app, sid, message string, answer any) []frame {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"message": message, "sessionId": sid})
	res, err := http.Post(app+"/api/run", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("run: %d %s", res.StatusCode, b)
	}
	var frames []frame
	askID := regexp.MustCompile(`data-ask="([^"]+)"`)
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e agentrun.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		if e.Type != "html" {
			continue
		}
		s := func(k string) string { v, _ := e.Args[k].(string); return v }
		f := frame{Slot: s("slot"), ID: s("id"), Mode: s("mode"), HTML: s("html")}
		frames = append(frames, f)
		if m := askID.FindStringSubmatch(f.HTML); m != nil {
			reply, _ := json.Marshal(map[string]any{"sessionId": sid, "requestId": m[1], "value": answer})
			rr, err := http.Post(app+"/api/reply", "application/json", strings.NewReader(string(reply)))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(rr.Body)
			rr.Body.Close()
			if rr.StatusCode != http.StatusOK || !strings.Contains(string(got), "You answered") {
				t.Fatalf("reply: %d %s", rr.StatusCode, got)
			}
		}
	}
	return frames
}

func find(frames []frame, slot, id string) (frame, bool) {
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Slot == slot && (id == "" || frames[i].ID == id) {
			return frames[i], true
		}
	}
	return frame{}, false
}

func TestRunStreamsTablerFragments(t *testing.T) {
	model := scriptedModel(t,
		js(`var a = ask({type: "questions", questions: [{id: "region", header: "Region", question: "Which region?", options: [{label: "North"}, {label: "South"}]}]});
emitStats({items: [{label: "Orders", value: 24, delta: 0.1}]});
emitTable({id: "t", columns: ["region"], rows: [{region: a.region}]});
emitChart({id: "rev", title: "Revenue", variant: "bar", x: "m", series: ["v"], data: [{m: "Jan", v: 1}]});
answer("Done for " + a.region);`),
		js(`emitChart({id: "rev", title: "Revenue by month", variant: "line", x: "m", series: ["v"], data: [{m: "Jan", v: 1}, {m: "Feb", v: 2}]});
answer("Revised.");`),
	)
	client, err := llm.NewOpenAI(llm.Config{APIKey: "k", BaseURL: model.URL, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	broker := agentrun.NewBroker()
	l := agentloop.New(agentloop.Config{
		LLM:            client,
		Sessions:       agentloopmem.NewSessionStore(nil),
		Steps:          agentloopmem.NewStepStore(),
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: capabilities(client, root, broker)},
	})
	app := httptest.NewServer(newServer(l, broker))
	defer app.Close()
	sid := uuid.NewString()

	frames := runTurn(t, app.URL, sid, "Build me a report.", map[string]any{"region": "South"})
	if f, ok := find(frames, "turn", ""); !ok || !strings.Contains(f.HTML, "Build me a report.") {
		t.Fatalf("no turn frame: %+v", frames)
	}
	if f, ok := find(frames, "steps", "step-1-1"); !ok || !strings.Contains(f.HTML, "ran") {
		t.Errorf("the step never got its result: %+v", f)
	}
	if f, ok := find(frames, "asks", ""); !ok || !strings.Contains(f.HTML, `value="South"`) {
		t.Errorf("no question form: %+v", f)
	}
	if f, ok := find(frames, "blocks", "block-t"); !ok || !strings.Contains(f.HTML, "South") {
		t.Errorf("the table does not show the answer: %+v", f)
	}
	rev, ok := find(frames, "blocks", "block-rev")
	if !ok || !strings.Contains(rev.HTML, "data-tabler-chart") || strings.Contains(rev.HTML, "revised") {
		t.Errorf("first chart: %+v", rev)
	}
	if f, ok := find(frames, "blocks", ""); !ok {
		t.Errorf("no blocks at all")
	} else if _, ok := find(frames, "blocks", "block-1-1"); !ok {
		t.Errorf("the stats block, which has no id, got no generated one: %+v", f)
	}
	if f, ok := find(frames, "answer", ""); !ok || !strings.Contains(f.HTML, "Done for South") {
		t.Errorf("answer: %+v", f)
	}
	if f, ok := find(frames, "status", ""); !ok || f.Mode != "set" || !strings.Contains(f.HTML, "3 blocks") {
		t.Errorf("footer: %+v", f)
	}

	frames = runTurn(t, app.URL, sid, "Make it a line chart.", nil)
	rev2, ok := find(frames, "blocks", "block-rev")
	if !ok || !strings.Contains(rev2.HTML, "revised") || !strings.Contains(rev2.HTML, "Revenue by month") {
		t.Fatalf("the revised chart is not marked revised: %+v", rev2)
	}
	if chartID(rev.HTML) == chartID(rev2.HTML) {
		t.Errorf("the replacement reuses chart id %q", chartID(rev.HTML))
	}
}

func chartID(html string) string {
	m := regexp.MustCompile(`id="(chart-[^"]+)"`).FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	return m[1]
}
