package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopmem"
	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/sandbox"
)

// stubClient is an llm.Client that is never called — loopConfig only
// stores it, and these tests only inspect where it was stored.
type stubClient struct{}

func (stubClient) Name() string { return "stub" }
func (stubClient) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}
func (stubClient) Stream(context.Context, llm.CompletionRequest, func(string)) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}

func configFor(o options) agentloop.Config {
	return o.loopConfig(stubClient{}, nil, agentloopmem.NewSessionStore(nil), agentloopmem.NewStepStore())
}

// Without the flag, every context setting must stay exactly where it
// was — the flag is opt-in, and an unsized run is what the CLI already
// shipped.
func TestUnsizedRunIsUnchanged(t *testing.T) {
	cfg := configFor(options{})

	if cfg.ContextBudget.MaxTokens != 0 {
		t.Errorf("ContextBudget.MaxTokens = %d, want unset", cfg.ContextBudget.MaxTokens)
	}
	if cfg.HistoryWindow != 0 {
		t.Errorf("HistoryWindow = %d, want 0 (engine default)", cfg.HistoryWindow)
	}
	if cfg.Compactor != nil {
		t.Error("a compactor was installed without a context window — that is a per-run provider cost the user did not ask for")
	}
	b := cfg.SandboxBuilder.(*agentloop.DefaultSandboxBuilder)
	if b.MaxLogBytes != 0 {
		t.Errorf("MaxLogBytes = %d, want 0 (sandbox default)", b.MaxLogBytes)
	}
}

func TestContextWindowSizesTheLoop(t *testing.T) {
	const window = 8192
	cfg := configFor(options{contextWindow: window})
	p := agentloop.ProfileFor(window)

	if cfg.ContextBudget.MaxTokens != window {
		t.Errorf("ContextBudget.MaxTokens = %d, want %d", cfg.ContextBudget.MaxTokens, window)
	}
	if cfg.HistoryWindow != p.HistoryWindow {
		t.Errorf("HistoryWindow = %d, want the profile's %d", cfg.HistoryWindow, p.HistoryWindow)
	}
	b := cfg.SandboxBuilder.(*agentloop.DefaultSandboxBuilder)
	if b.MaxLogBytes != p.MaxLogBytes {
		t.Errorf("MaxLogBytes = %d, want the profile's %d", b.MaxLogBytes, p.MaxLogBytes)
	}
	if b.MaxLogBytes >= sandbox.DefaultMaxLogBytes {
		t.Errorf("MaxLogBytes = %d, want it below the frontier-sized default for an 8k window", b.MaxLogBytes)
	}
}

// The budget only guarantees a request is sendable; it drops old turns
// rather than summarizing them. A sized run that installed no compactor
// would silently lose the early conversation instead of keeping it in
// summary.
func TestContextWindowInstallsAConfiguredCompactor(t *testing.T) {
	const window = 8192
	cfg := configFor(options{contextWindow: window})

	sc, ok := cfg.Compactor.(*agentloop.SummarizingCompactor)
	if !ok {
		t.Fatalf("Compactor = %T, want a *SummarizingCompactor", cfg.Compactor)
	}
	if sc.LLM == nil {
		t.Fatal("compactor has no client — it would silently no-op")
	}
	p := agentloop.ProfileFor(window)
	if sc.Trigger != p.CompactTrigger || sc.Keep != p.CompactKeep {
		t.Errorf("compactor = %d/%d, want the profile's %d/%d", sc.Trigger, sc.Keep, p.CompactTrigger, p.CompactKeep)
	}
	// Sized to the summarizer's own window, or compaction overflows the
	// very call meant to prevent an overflow.
	if sc.Budget.MaxTokens != window {
		t.Errorf("compactor Budget.MaxTokens = %d, want %d", sc.Budget.MaxTokens, window)
	}
}

// A number the user typed outranks one the profile inferred.
func TestExplicitHistoryWindowBeatsTheProfile(t *testing.T) {
	const window = 8192
	derived := agentloop.ProfileFor(window).HistoryWindow
	if derived == 12 {
		t.Fatal("fixture clashes with the derived value; pick another")
	}

	cfg := configFor(options{contextWindow: window, historyWindow: 12})

	if cfg.HistoryWindow != 12 {
		t.Fatalf("HistoryWindow = %d, want the explicit 12 rather than the derived %d", cfg.HistoryWindow, derived)
	}
	// The rest of the profile still applies — one override is not an
	// opt-out of the whole thing.
	if cfg.ContextBudget.MaxTokens != window {
		t.Error("overriding --history-window discarded the budget too")
	}
}

// A sized config has to survive New's validation, which panics on a
// budget that leaves no room for a prompt.
func TestSizedConfigsConstruct(t *testing.T) {
	for _, window := range []int{1024, 4096, 8192, 32_768, 200_000} {
		cfg := configFor(options{contextWindow: window})
		cfg.SandboxBuilder = &agentloop.DefaultSandboxBuilder{}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("window %d: New panicked: %v", window, r)
				}
			}()
			agentloop.New(cfg)
		}()
	}
}

func TestContextWindowFlagParses(t *testing.T) {
	var o options
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	o.bind(fs)

	if err := fs.Parse([]string{"--context-window", "8192"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.contextWindow != 8192 {
		t.Fatalf("contextWindow = %d, want 8192", o.contextWindow)
	}
	if !o.sized() {
		t.Error("sized() = false with a window set")
	}
}

func TestNegativeContextWindowIsRejected(t *testing.T) {
	o := options{contextWindow: -1, ephemeral: true}
	if err := o.resolve(); err == nil {
		t.Fatal("want an error for a negative --context-window")
	}
}

// --- project instructions, end to end -------------------------------

// buildSession runs the real newSession against a throwaway workspace.
func buildSession(t *testing.T, o options, agentsMD string) *session {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "unused")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/v1") // never dialled

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agentsMD), 0o644); err != nil {
		t.Fatal(err)
	}
	o.cwd = dir
	o.ephemeral = true
	if err := o.resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sess, err := newSession(&o, func(string) {}, bufio.NewReader(strings.NewReader("")))
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	t.Cleanup(func() { sess.close() })
	return sess
}

// bigAgentsMD is an instruction file well past the excerpt bound.
func bigAgentsMD() string {
	var b strings.Builder
	b.WriteString("# Rules\n\nOPENING-RULE: always do the thing.\n\n")
	for i := 0; i < 60; i++ {
		b.WriteString("## Section\n\n")
		b.WriteString(strings.Repeat("guidance text. ", 40))
		b.WriteString("\n\n")
	}
	b.WriteString("## Last\n\nBURIED-RULE: never do the other thing.\n")
	return b.String()
}

// Unsized, a large AGENTS.md is inlined whole — the behaviour the CLI
// already had.
func TestUnsizedRunInlinesProjectInstructions(t *testing.T) {
	sess := buildSession(t, options{}, bigAgentsMD())

	if !strings.Contains(sess.projectCx, "BURIED-RULE") {
		t.Error("unsized run did not inline the whole file")
	}
	if strings.Contains(sess.projectCx, "projectGet") {
		t.Error("unsized run pointed at projectGet without installing it")
	}
}

// Sized, the prompt carries the opening and the rest is retrievable —
// inlining 36 KB against an 8k window would spend the budget before the
// conversation started, and letting the budget drop it would lose it
// outright, since the budget drops rather than summarizes.
func TestSizedRunExcerptsProjectInstructions(t *testing.T) {
	sess := buildSession(t, options{contextWindow: 8192}, bigAgentsMD())

	if !strings.Contains(sess.projectCx, "OPENING-RULE") {
		t.Error("the opening section should still ride in the prompt")
	}
	if strings.Contains(sess.projectCx, "BURIED-RULE") {
		t.Error("the whole file was inlined despite a context window being set")
	}
	if !strings.Contains(sess.projectCx, `projectGet("AGENTS.md")`) {
		t.Errorf("no retrieval pointer in the rendered context:\n%s", sess.projectCx)
	}
	if len(sess.projectCx) > 8192 {
		t.Errorf("rendered context is %d bytes for an 8k-token window", len(sess.projectCx))
	}
}

// A small file is under the excerpt bound either way, so a sized run
// must not gain a pointer to a projectGet that would return nothing new.
func TestSizedRunLeavesASmallFileWhole(t *testing.T) {
	sess := buildSession(t, options{contextWindow: 8192}, "# Rules\n\nONLY-RULE: be brief.\n")

	if !strings.Contains(sess.projectCx, "ONLY-RULE") {
		t.Error("small file was not inlined")
	}
	if strings.Contains(sess.projectCx, "projectGet") {
		t.Error("a complete file was advertised as retrievable — that round-trip returns nothing new")
	}
}
