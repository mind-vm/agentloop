package sandbox

import (
	"fmt"
	"strings"

	"github.com/dop251/goja"
)

// InputRequester is the sandbox-side seam for human-in-the-loop. The
// ask() primitive and the fetch pack's allowUrls auto-prompt both call
// Request — implementations close over the run's HITL transport (a
// terminal, an SSE stream with a reply endpoint, a websocket) and block
// until the user replies.
//
// A nil InputRequester is the documented "no HITL for this run" case:
// FetchPack still requires the type to exist, but an unapproved-domain
// fetch simply fails with a policy-denial error instead of prompting,
// and InputPack documents ask() but errors when it is called.
type InputRequester interface {
	// Request dispatches the prompt and blocks until the user replies
	// (or the underlying transport errors out). The returned value's
	// concrete type depends on InputType:
	//   - "confirm" → bool
	//   - "choice"  → string (the selected option)
	//   - "text"    → string
	//   - "questions" → map[string]any keyed by Question.ID; each value a
	//     string, or a []string / []any of strings for a MultiSelect
	//     question. ask() checks it against the questions before the
	//     script sees it, so an implementation may pass a decoded JSON
	//     body straight through.
	Request(req InputRequestData) (any, error)
}

// InputRequestData is the payload InputRequester.Request receives.
type InputRequestData struct {
	// RequestID is a caller-allocated identifier. Optional.
	RequestID string
	// InputType is one of: "confirm", "choice", "text".
	InputType string
	// Message is the question shown to the user.
	Message string
	// Options is required for InputType == "choice".
	Options []string
	// Placeholder is hint text for InputType == "text" (optional).
	Placeholder string
	// Questions is required for InputType == "questions": one to four
	// questions the user steps through and answers in one reply.
	Questions []Question
}

// Question is one step of a "questions" prompt. The json tags are the
// wire shape a transport forwards to a client.
type Question struct {
	// ID keys the answer in the result.
	ID string `json:"id"`
	// Header is a short label for the step ("Region", "Period").
	Header string `json:"header,omitempty"`
	// Question is the full question shown to the user.
	Question string `json:"question"`
	// Options are what the user picks from, two to six of them.
	Options []QuestionOption `json:"options"`
	// MultiSelect lets the user pick several; the answer is then a list.
	MultiSelect bool `json:"multiSelect,omitempty"`
	// AllowOther adds a free-text answer beside the options.
	AllowOther bool `json:"allowOther,omitempty"`
}

// QuestionOption is one choice. Label is what the answer carries back;
// Description is shown under it to say what picking it means.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question limits. Four steps is where a form stops being a quick
// question; two options is the least that is a choice, and past six a
// list reads better as a search than as buttons.
const (
	maxQuestions  = 4
	minOptions    = 2
	maxOptions    = 6
	maxOtherBytes = 2000
)

// inputTypes is the vocabulary ask() accepts. It is checked before the
// requester is called: a human-loop round trip is the most expensive
// thing a script can do, and a typo in the type is not worth one.
var inputTypes = map[string]bool{"confirm": true, "choice": true, "text": true, "questions": true}

// InputPack exposes ask() — the synchronous human-in-the-loop primitive.
// The script blocks until the user replies; the value it gets back
// depends on the type it asked for: a bool for confirm, a string for
// choice and text.
//
// It is not in DefaultCapabilities, because nothing generic can supply
// the requester: whether there is a human, and how to reach them, is the
// host's knowledge. A host wraps InputPack in a Capability that closes
// over its own transport, the same way the CLI closes over its terminal.
//
// A nil requester registers the prompt only. ask() is still documented
// to the model, so a script written for an interactive host parses and
// runs, and the call errors — which the model sees and can plan around.
// The alternative, leaving ask() undefined, would turn every such script
// into a ReferenceError with nothing to say about why.
func InputPack(requester InputRequester) Pack {
	return Pack{
		Name:        "input",
		Description: "Human-in-the-loop user interaction",
		Prompt: `// --- Interaction ---

interface AskConfirm { type: "confirm"; message: string }
interface AskChoice { type: "choice"; message: string; options: string[] }
interface AskText { type: "text"; message: string; placeholder?: string }
/** Ask the user to confirm — blocks until they reply; returns their yes/no. */
declare function ask(opts: AskConfirm): boolean;
/** Ask the user to pick one of options — blocks; returns the selected option string. */
declare function ask(opts: AskChoice): string;
/** Ask the user for free text — blocks; returns what they typed. */
declare function ask(opts: AskText): string;
interface Question {
  id: string;            // key of this answer in the result
  header?: string;       // short step label, e.g. "Region"
  question: string;
  options: { label: string; description?: string }[];  // 2–6
  multiSelect?: boolean; // answer becomes string[]
  allowOther?: boolean;  // user may type an answer instead
}
interface AskQuestions { type: "questions"; message?: string; questions: Question[] }
/** Ask 1–4 independent questions at once: the user steps through them and replies once — blocks;
 *  returns { [id]: label } (string[] for multiSelect). When a question depends on an earlier
 *  answer, ask them one after another instead. */
declare function ask(opts: AskQuestions): Record<string, string | string[]>;`,
		HelpEntries: map[string]string{
			"ask": `ask(opts) — Request input from the user. Blocks until response.
  opts.type: "confirm" (yes/no), "choice" (pick from list), "text" (free input)
  opts.message: The question to display
  opts.options: Array of strings (required for choice type)
  opts.placeholder: Hint text (optional, for text type)
  opts.questions: 1–4 {id, question, options: [{label, description?}], header?, multiSelect?, allowOther?} (questions type)
  Returns: boolean (confirm), string (choice/text), {[id]: string | string[]} (questions)
  Example: var a = ask({type: "questions", questions: [
    {id: "period", header: "Period", question: "Which months?", options: [{label: "Q1"}, {label: "Q2"}]},
    {id: "regions", header: "Regions", question: "Which regions?", multiSelect: true,
     options: [{label: "North"}, {label: "South"}, {label: "West"}]}]});
    // a.period === "Q1", a.regions is e.g. ["North", "West"]`,
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			if requester == nil {
				_ = rt.Set("ask", func(call goja.FunctionCall) goja.Value {
					panic(rt.NewGoError(fmt.Errorf("ask: no InputRequester configured for this run")))
				})
				return
			}
			_ = rt.Set("ask", func(call goja.FunctionCall) goja.Value {
				req, err := parseAsk(call)
				if err != nil {
					panic(rt.NewGoError(err))
				}

				s.Emit(Event{Kind: EventAsk, Summary: fmt.Sprintf("ask (%s): %s", req.InputType, askSummary(req)), Detail: req.Message})
				value, err := requester.Request(req)
				if err != nil {
					s.Emit(Event{Kind: EventError, Summary: fmt.Sprintf("ask: %s", err.Error())})
					panic(rt.NewGoError(fmt.Errorf("ask: %w", err)))
				}
				if req.InputType == "questions" {
					// The reply came from outside the sandbox — a browser,
					// a terminal — and the script will branch on it. An
					// answer that is not one of the options offered is a
					// value the script was told it could not receive.
					value, err = checkAnswers(req.Questions, value)
					if err != nil {
						s.Emit(Event{Kind: EventError, Summary: fmt.Sprintf("ask: %s", err.Error())})
						panic(rt.NewGoError(fmt.Errorf("ask: the reply did not answer the questions as asked: %w", err)))
					}
				}
				s.Emit(Event{Kind: EventAskResult, Summary: fmt.Sprintf("ask result: %v", value), Result: fmt.Sprintf("%v", value)})
				return rt.ToValue(value)
			})
		},
	}
}

// parseAsk validates the options object the script passed. Every error
// here is one the script can fix on its next turn, so each names the
// field and what it expects rather than reporting a type mismatch.
func parseAsk(call goja.FunctionCall) (InputRequestData, error) {
	if len(call.Arguments) == 0 || goja.IsUndefined(call.Argument(0)) {
		return InputRequestData{}, fmt.Errorf("ask: options object required")
	}
	opts, ok := call.Argument(0).Export().(map[string]any)
	if !ok {
		return InputRequestData{}, fmt.Errorf("ask: options must be an object")
	}

	inputType, _ := opts["type"].(string)
	message, _ := opts["message"].(string)
	if inputType == "" {
		return InputRequestData{}, fmt.Errorf("ask: type is required (confirm, choice, text, questions)")
	}
	if !inputTypes[inputType] {
		return InputRequestData{}, fmt.Errorf("ask: type must be confirm, choice, text, or questions")
	}
	if inputType == "questions" {
		qs, err := parseQuestions(opts["questions"])
		if err != nil {
			return InputRequestData{}, fmt.Errorf("ask: %w", err)
		}
		return InputRequestData{InputType: inputType, Message: message, Questions: qs}, nil
	}
	if message == "" {
		return InputRequestData{}, fmt.Errorf("ask: message is required")
	}

	var options []string
	if arr, ok := opts["options"].([]any); ok {
		for _, v := range arr {
			if str, ok := v.(string); ok {
				options = append(options, str)
			}
		}
	}
	if inputType == "choice" && len(options) == 0 {
		return InputRequestData{}, fmt.Errorf("ask: choice type requires options array")
	}
	placeholder, _ := opts["placeholder"].(string)

	return InputRequestData{
		InputType:   inputType,
		Message:     message,
		Options:     options,
		Placeholder: placeholder,
	}, nil
}

func askSummary(req InputRequestData) string {
	if req.InputType != "questions" {
		return req.Message
	}
	var parts []string
	for _, q := range req.Questions {
		parts = append(parts, q.Question)
	}
	return strings.Join(parts, " / ")
}

// parseQuestions validates a "questions" prompt. Each error names the
// question by position and id, because the script that built the list
// is the one that has to find it.
func parseQuestions(v any) ([]Question, error) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, fmt.Errorf("questions type requires a questions array")
	}
	if len(arr) > maxQuestions {
		return nil, fmt.Errorf("at most %d questions at once, got %d — ask the rest in a second ask()", maxQuestions, len(arr))
	}
	seen := map[string]bool{}
	out := make([]Question, 0, len(arr))
	for i, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions[%d] must be an object", i)
		}
		q := Question{}
		q.ID, _ = m["id"].(string)
		q.Header, _ = m["header"].(string)
		q.Question, _ = m["question"].(string)
		q.MultiSelect, _ = m["multiSelect"].(bool)
		q.AllowOther, _ = m["allowOther"].(bool)
		switch {
		case q.ID == "":
			return nil, fmt.Errorf("questions[%d] needs an id — the key its answer comes back under", i)
		case seen[q.ID]:
			return nil, fmt.Errorf("questions[%d]: id %q is used twice; answers would overwrite each other", i, q.ID)
		case q.Question == "":
			return nil, fmt.Errorf("questions[%d] (%s) needs question text", i, q.ID)
		}
		seen[q.ID] = true
		opts, _ := m["options"].([]any)
		if len(opts) < minOptions || len(opts) > maxOptions {
			return nil, fmt.Errorf("questions[%d] (%s) needs %d–%d options, got %d", i, q.ID, minOptions, maxOptions, len(opts))
		}
		labels := map[string]bool{}
		for j, o := range opts {
			var opt QuestionOption
			switch o := o.(type) {
			case string:
				opt.Label = o
			case map[string]any:
				opt.Label, _ = o["label"].(string)
				opt.Description, _ = o["description"].(string)
			}
			if opt.Label == "" {
				return nil, fmt.Errorf("questions[%d] (%s) option %d needs a label", i, q.ID, j)
			}
			if labels[opt.Label] {
				return nil, fmt.Errorf("questions[%d] (%s) offers %q twice", i, q.ID, opt.Label)
			}
			labels[opt.Label] = true
			q.Options = append(q.Options, opt)
		}
		out = append(out, q)
	}
	return out, nil
}

// checkAnswers holds a reply to the questions it answers and returns it
// in the shape the prompt promises: a label per question, a list of them
// for MultiSelect, free text only where AllowOther said so.
func checkAnswers(qs []Question, v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("want an object keyed by question id, got %T", v)
	}
	out := make(map[string]any, len(qs))
	for _, q := range qs {
		raw, ok := m[q.ID]
		if !ok {
			return nil, fmt.Errorf("no answer for %q", q.ID)
		}
		valid := func(a string) (string, error) {
			for _, o := range q.Options {
				if o.Label == a {
					return a, nil
				}
			}
			t := strings.TrimSpace(a)
			if q.AllowOther && t != "" && len(t) <= maxOtherBytes {
				return t, nil
			}
			return "", fmt.Errorf("%q is not an option of %q", a, q.ID)
		}
		if !q.MultiSelect {
			a, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("answer for %q must be one string", q.ID)
			}
			a, err := valid(a)
			if err != nil {
				return nil, err
			}
			out[q.ID] = a
			continue
		}
		var list []string
		switch l := raw.(type) {
		case []string:
			list = l
		case []any:
			for _, e := range l {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("answer for %q must be a list of strings", q.ID)
				}
				list = append(list, s)
			}
		default:
			return nil, fmt.Errorf("answer for %q must be a list", q.ID)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("answer for %q picks nothing", q.ID)
		}
		picked := make([]any, 0, len(list))
		dup := map[string]bool{}
		for _, a := range list {
			a, err := valid(a)
			if err != nil {
				return nil, err
			}
			if !dup[a] {
				dup[a] = true
				picked = append(picked, a)
			}
		}
		out[q.ID] = picked
	}
	return out, nil
}
