package agentloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mind-vm/thinking-core/agentloop"
	"github.com/mind-vm/thinking-core/llm"
)

func script(code string) llm.CompletionResponse {
	return llm.CompletionResponse{Content: "```javascript\n" + code + "\n```"}
}

func runScripted(t *testing.T, resps ...llm.CompletionResponse) (agentloop.RunResult, *fakeLLM) {
	t.Helper()
	fake := newFakeLLM("fake", resps...)
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	loop := agentloop.New(agentloop.Config{LLM: fake, Sessions: sessions, Steps: newMemorySteps(), SandboxBuilder: dataBuilder{}})
	res, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "report"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, fake
}

// A script that logs and answers has answered before reading what it
// logged: the run goes on, the model reads the log, and the answer() from a
// script that logs nothing is the one that counts.
func TestRun_AnswerWithUnreadLogIsHeld(t *testing.T) {
	res, fake := runScripted(t,
		script(`log(["sales.csv", "regions.csv"]); answer("Listed the files.")`),
		script(`log("region,total"); answer("Read the header.")`),
		script(`answer("Revenue was 42.")`),
	)
	if res.FinalText != "Revenue was 42." {
		t.Fatalf("final = %q, want the answer from the script that logged nothing", res.FinalText)
	}
	calls := fake.Calls()
	if len(calls) != 3 {
		t.Fatalf("LLM calls = %d, want 3", len(calls))
	}
	msgs := calls[1].Messages
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "regions.csv") || !strings.Contains(last, "answer(result) in a script that logs nothing") {
		t.Errorf("the next call should carry the unread log and say how to finish:\n%s", last)
	}
}

// With nothing logged there is nothing unread, so answer() ends the run
// as it always did — after a first script or after several.
func TestRun_AnswerWithoutLogEndsTheRun(t *testing.T) {
	if res, fake := runScripted(t, script(`answer("42")`)); res.FinalText != "42" || len(fake.Calls()) != 1 {
		t.Errorf("an answer with no log: final = %q after %d calls", res.FinalText, len(fake.Calls()))
	}
	res, fake := runScripted(t,
		script(`state.n = 42; log("n is", state.n)`),
		script(`answer("n = " + state.n)`),
	)
	if res.FinalText != "n = 42" || len(fake.Calls()) != 2 {
		t.Errorf("final = %q after %d calls", res.FinalText, len(fake.Calls()))
	}
}
