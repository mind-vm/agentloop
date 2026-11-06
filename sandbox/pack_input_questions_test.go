package sandbox

import (
	"strings"
	"testing"
)

const twoQuestions = `[
  {id: "period", header: "Period", question: "Which months?", options: [{label: "Q1", description: "Jan–Mar"}, {label: "Q2"}]},
  {id: "regions", header: "Regions", question: "Which regions?", multiSelect: true, allowOther: true,
   options: ["North", "South", "West"]}
]`

// The reply arrives as decoded JSON — []any, not []string — which is what
// a browser transport hands over. The script must get a real JS array
// back: Array.isArray, includes and join all have to work on it.
func TestAskQuestionsReturnsAnswersByID(t *testing.T) {
	a := &answering{reply: map[string]any{"period": "Q2", "regions": []any{"West", "North", "West", " Benelux "}}}
	s, _ := askIn(a)
	_, ret, err := s.ExecuteTurn(`
var r = ask({type: "questions", questions: `+twoQuestions+`});
state.out = r.period + "|" + Array.isArray(r.regions) + "|" + r.regions.includes("North") + "|" + r.regions.join(",");
`, nil)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if want := `"out":"Q2|true|true|West,North,Benelux"`; !strings.Contains(string(ret), want) {
		t.Errorf("state = %s, want %s (deduplicated, other trimmed)", ret, want)
	}
	if len(a.seen) != 1 {
		t.Fatalf("asked %d times, want once for both questions", len(a.seen))
	}
	q := a.seen[0].Questions
	if len(q) != 2 || q[0].Options[0].Description != "Jan–Mar" || q[1].Options[2].Label != "West" || !q[1].MultiSelect || !q[1].AllowOther {
		t.Errorf("questions not carried to the requester intact: %+v", q)
	}
}

// []string is the other shape a Go requester (the CLI) naturally returns.
func TestAskQuestionsAcceptsAStringSlice(t *testing.T) {
	s, _ := askIn(&answering{reply: map[string]any{"period": "Q1", "regions": []string{"South"}}})
	if _, _, err := s.ExecuteTurn(`var r = ask({type: "questions", questions: `+twoQuestions+`}); if (r.regions[0] !== "South") throw new Error("got " + r.regions);`, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAskQuestionsRejectsAReplyThatWasNotOffered(t *testing.T) {
	cases := map[string]any{
		"unknown option":          map[string]any{"period": "Q3", "regions": []any{"North"}},
		"other where not allowed": map[string]any{"period": "  ", "regions": []any{"North"}},
		"missing answer":          map[string]any{"period": "Q1"},
		"list for single":         map[string]any{"period": []any{"Q1"}, "regions": []any{"North"}},
		"empty multi":             map[string]any{"period": "Q1", "regions": []any{}},
		"not an object":           "Q1",
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := askIn(&answering{reply: reply})
			_, _, err := s.ExecuteTurn(`ask({type: "questions", questions: `+twoQuestions+`})`, nil)
			if err == nil || !strings.Contains(err.Error(), "did not answer the questions as asked") {
				t.Fatalf("reply %v accepted: %v", reply, err)
			}
		})
	}
}

// A malformed prompt never reaches a person: it fails in the script.
func TestAskQuestionsValidatesThePrompt(t *testing.T) {
	cases := map[string]string{
		`[]`: "requires a questions array",
		`[{id: "a", question: "?", options: ["x", "y"]}, {id: "a", question: "?", options: ["x", "y"]}]`: `id "a" is used twice`,
		`[{question: "?", options: ["x", "y"]}]`:                                                         "needs an id",
		`[{id: "a", question: "?", options: ["x"]}]`:                                                     "needs 2–6 options, got 1",
		`[{id: "a", question: "?", options: ["x", "x"]}]`:                                                `offers "x" twice`,
		`[{id: "a", options: ["x", "y"]}]`:                                                               "needs question text",
		`[1,2,3,4,5]`:                                                                                    "at most 4 questions",
	}
	for qs, want := range cases {
		t.Run(want, func(t *testing.T) {
			a := &answering{reply: map[string]any{}}
			s, _ := askIn(a)
			_, _, err := s.ExecuteTurn(`ask({type: "questions", questions: `+qs+`})`, nil)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
			if len(a.seen) != 0 {
				t.Error("a malformed prompt reached the requester")
			}
		})
	}
}
