package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/mind-vm/thinking-core/agentloopsql"
	"github.com/mind-vm/thinking-core/sandbox"
)

// approvalMode decides what happens when a capability asks permission.
type approvalMode string

const (
	// approvalPrompt asks the user. The default when stdin is a terminal.
	approvalPrompt approvalMode = "prompt"
	// approvalAuto approves without asking.
	approvalAuto approvalMode = "auto"
	// approvalDeny refuses without asking. The default when stdin is not
	// a terminal, so an unattended run fails closed instead of blocking
	// forever on an answer nobody is there to give.
	approvalDeny approvalMode = "deny"
)

func parseApprovalMode(s string) (approvalMode, error) {
	switch approvalMode(s) {
	case approvalPrompt, approvalAuto, approvalDeny:
		return approvalMode(s), nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("unknown --approve mode %q — use prompt, auto, or deny", s)
	}
}

// errNoTerminal is returned for a question that cannot be answered
// without someone at the keyboard. It reaches the agent as a catchable
// JS error, which is the honest outcome: the capability is unavailable
// this run, not silently answered on the user's behalf.
var errNoTerminal = errors.New("no terminal available to answer this prompt")

// terminalPrompter implements sandbox.InputRequester against the
// terminal, remembering approvals so a resumed session does not re-ask.
//
// It shares one buffered reader with whatever else reads stdin — the
// chat REPL, in particular. Two independent bufio.Readers over the same
// file would race for buffered bytes, and an approval prompt would
// swallow the user's next chat message.
type terminalPrompter struct {
	in   *bufio.Reader
	out  io.Writer
	mode approvalMode

	// store persists approvals; nil for an ephemeral run, which simply
	// forgets them when the process ends.
	store *agentloopsql.Store
	// sessionID is read at request time because chat's /new changes it
	// mid-process, and grants must not carry across that boundary.
	sessionID func() string

	mu sync.Mutex
	// remembered memoises this process's answers, so a run that asks the
	// same question twice only troubles the user once even without a store.
	remembered map[string]bool
}

func newTerminalPrompter(in *bufio.Reader, out io.Writer, mode approvalMode, store *agentloopsql.Store, sessionID func() string) *terminalPrompter {
	return &terminalPrompter{
		in: in, out: out, mode: mode, store: store, sessionID: sessionID,
		remembered: map[string]bool{},
	}
}

// Request implements sandbox.InputRequester.
func (p *terminalPrompter) Request(req sandbox.InputRequestData) (any, error) {
	switch req.InputType {
	case "confirm":
		return p.confirm(req)
	case "choice":
		if p.mode != approvalPrompt {
			// auto and deny are answers to "may I?", and a choice is not
			// that question — guessing an option would put words in the
			// user's mouth.
			return nil, errNoTerminal
		}
		return p.choice(req)
	case "text":
		if p.mode != approvalPrompt {
			return nil, errNoTerminal
		}
		return p.text(req)
	default:
		return nil, fmt.Errorf("unsupported prompt type %q", req.InputType)
	}
}

func (p *terminalPrompter) confirm(req sandbox.InputRequestData) (any, error) {
	switch p.mode {
	case approvalAuto:
		return true, nil
	case approvalDeny:
		return false, nil
	}

	if granted, ok := p.recall(req.Message); ok {
		return granted, nil
	}

	answer, err := p.yesNo(req)
	if err != nil {
		return nil, err
	}
	p.remember(req.Message, answer)
	return answer, nil
}

// yesNo puts one y/N question to the terminal and reads the answer.
func (p *terminalPrompter) yesNo(req sandbox.InputRequestData) (bool, error) {
	fmt.Fprintf(p.out, "\n%s [y/N] ", req.Message)
	line, err := p.readLine()
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}

// questions returns the InputRequester behind the agent's ask()
// primitive: the same terminal, none of the memory.
//
// The prompter remembers a confirm as a grant, and that is right for
// "may I run this?" — a retried turn should not ask twice, and a yes is
// worth keeping for the session. It is wrong for a question the script
// itself is asking. "Delete the drafts?" answered yes once is not a
// standing permission, must not be answered on the user's behalf next
// time, and must not turn up in `sessions show` as something the
// session approved. Auto and deny are likewise answers to "may I?", so
// under either mode a question has no answer and reports errNoTerminal,
// which is already how choice and text behave.
func (p *terminalPrompter) questions() sandbox.InputRequester {
	return questioner{p}
}

type questioner struct{ p *terminalPrompter }

// Request implements sandbox.InputRequester.
func (q questioner) Request(req sandbox.InputRequestData) (any, error) {
	if q.p.mode != approvalPrompt {
		return nil, errNoTerminal
	}
	switch req.InputType {
	case "confirm":
		return q.p.yesNo(req)
	case "choice":
		return q.p.choice(req)
	case "text":
		return q.p.text(req)
	case "questions":
		return q.p.askQuestions(req)
	default:
		return nil, fmt.Errorf("unsupported prompt type %q", req.InputType)
	}
}

// askQuestions walks the steps one after another, the way a terminal can.
// It returns labels, not numbers, because labels are what ask() checks the
// reply against — and what the script branches on.
func (p *terminalPrompter) askQuestions(req sandbox.InputRequestData) (any, error) {
	if req.Message != "" {
		fmt.Fprintf(p.out, "\n%s\n", req.Message)
	}
	out := make(map[string]any, len(req.Questions))
	for i, q := range req.Questions {
		head := fmt.Sprintf("(%d/%d)", i+1, len(req.Questions))
		if q.Header != "" {
			head += " " + q.Header
		}
		fmt.Fprintf(p.out, "\n%s — %s\n", head, q.Question)
		for j, o := range q.Options {
			fmt.Fprintf(p.out, "  %d) %s", j+1, o.Label)
			if o.Description != "" {
				fmt.Fprintf(p.out, " — %s", o.Description)
			}
			fmt.Fprintln(p.out)
		}
		hint := "Choose [1]"
		if q.MultiSelect {
			hint = "Choose one or more, e.g. 1,3 [1]"
		}
		if q.AllowOther {
			hint += ", or type your own answer"
		}
		fmt.Fprint(p.out, hint+" ")
		line, err := p.readLine()
		if err != nil {
			return nil, err
		}
		picked, err := pickOptions(q, strings.TrimSpace(line))
		if err != nil {
			return nil, err
		}
		if q.MultiSelect {
			out[q.ID] = picked
		} else {
			out[q.ID] = picked[0]
		}
	}
	return out, nil
}

// pickOptions reads "2" or "1,3" as option numbers. Anything else is the
// user's own answer when the question allows one; ask() decides whether it
// stands, so this does not second-guess it.
func pickOptions(q sandbox.Question, line string) ([]string, error) {
	if line == "" {
		return []string{q.Options[0].Label}, nil
	}
	parts := []string{line}
	if q.MultiSelect {
		parts = strings.Split(line, ",")
	}
	var picked []string
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			if q.AllowOther {
				return []string{line}, nil
			}
			return nil, fmt.Errorf("%q is not one of the %d options offered", line, len(q.Options))
		}
		if n < 1 || n > len(q.Options) {
			return nil, fmt.Errorf("%d is not one of the %d options offered", n, len(q.Options))
		}
		picked = append(picked, q.Options[n-1].Label)
	}
	return picked, nil
}

func (p *terminalPrompter) choice(req sandbox.InputRequestData) (any, error) {
	if len(req.Options) == 0 {
		return nil, errors.New("a choice prompt arrived with no options")
	}
	fmt.Fprintf(p.out, "\n%s\n", req.Message)
	for i, opt := range req.Options {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, opt)
	}
	fmt.Fprint(p.out, "Choose [1] ")

	line, err := p.readLine()
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return req.Options[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(req.Options) {
		return nil, fmt.Errorf("%q is not one of the %d options offered", line, len(req.Options))
	}
	return req.Options[n-1], nil
}

func (p *terminalPrompter) text(req sandbox.InputRequestData) (any, error) {
	fmt.Fprintf(p.out, "\n%s", req.Message)
	if req.Placeholder != "" {
		fmt.Fprintf(p.out, " (%s)", req.Placeholder)
	}
	fmt.Fprint(p.out, "\n> ")

	line, err := p.readLine()
	if err != nil {
		return nil, err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readLine reads one answer. End-of-input means nobody is going to
// answer, which is a refusal to be reported rather than a zero value to
// be passed off as one.
func (p *terminalPrompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) != "" {
			return line, nil
		}
		fmt.Fprintln(p.out)
		return "", fmt.Errorf("%w: %v", errNoTerminal, err)
	}
	return line, nil
}

// recall reports a remembered answer for this question, from this
// process or from a previous run of the same session.
func (p *terminalPrompter) recall(question string) (granted, known bool) {
	p.mu.Lock()
	if answer, ok := p.remembered[question]; ok {
		p.mu.Unlock()
		return answer, true
	}
	p.mu.Unlock()

	if p.store == nil {
		return false, false
	}
	ok, err := p.store.IsGranted(context.Background(), p.sessionID(), question)
	if err != nil || !ok {
		// A failed lookup means asking again — never assuming approval.
		return false, false
	}
	fmt.Fprintf(p.out, "· already approved for this session: %s\n", question)
	return true, true
}

// remember records an answer.
//
// Both answers are memoised for the rest of the process: a turn that
// throws is re-run by the loop, and asking the same question again on
// every retry is how a person gets trained to hit y without reading.
//
// Only an APPROVAL is persisted. A refusal that outlived the process
// would silently block a capability the user might well allow next
// time, with nothing on screen to explain why — so the next invocation
// asks again.
func (p *terminalPrompter) remember(question string, answer bool) {
	p.mu.Lock()
	p.remembered[question] = answer
	p.mu.Unlock()

	if !answer || p.store == nil {
		return
	}
	if err := p.store.Grant(context.Background(), p.sessionID(), question); err != nil {
		fmt.Fprintf(p.out, "· could not record the approval (it will be asked again): %s\n", err)
	}
}

var _ sandbox.InputRequester = (*terminalPrompter)(nil)
var _ sandbox.InputRequester = questioner{}
