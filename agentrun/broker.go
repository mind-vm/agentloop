package agentrun

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/mind-vm/thinking-core/sandbox"
)

// broker.go is the human-in-the-loop transport for the sandbox's ask().
//
// The session id it keys on is opaque to it, and so is the tenant id it
// compares; both come from whatever the application already resolved for the
// request.
//
// The run's SSE stream is one-way, so the loop's blocking Request has to meet
// the operator's reply arriving on a separate HTTP call:
//
//	sandbox ask() ──▶ broker.request ──▶ an `input_request` event on the
//	                     │                run's SSE stream, with an id
//	                     └── blocks ◀── a reply on another request
//	                                    {requestId, value} resolves it
//
// It is process-local on purpose. An ask() can only be answered while its run
// is live, and a run lives on one process for its whole window; a durable
// queue would buy nothing and would have to be reaped.
//
// Registration carries the run's tenant, so a reply from another tenant
// cannot resolve someone else's prompt — and reports "no live run" rather than
// "not yours", which would confirm the session exists.

// AskTimeout bounds how long ask() blocks. It sits under the loop's own run
// timeout so an unanswered prompt fails the ask() — the agent sees the error
// and can plan around the silence — before the whole run is killed.
const AskTimeout = 4 * time.Minute

// Broker connects a live run's ask() calls to replies from a person.
type Broker struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*brokerRun
}

type brokerRun struct {
	orgID   uuid.UUID
	emit    func(Event)
	pending map[string]chan any
	done    chan struct{}
}

// NewBroker returns a broker with no live runs.
func NewBroker() *Broker { return &Broker{runs: map[uuid.UUID]*brokerRun{}} }

// Begin registers a live run's event sink under its session. The returned func
// drops the registration and unblocks any still-pending ask() with an error.
// A second Begin for the same session replaces the first: the newest stream
// owns the prompts.
func (b *Broker) Begin(sessionID, orgID uuid.UUID, emit func(Event)) (end func()) {
	run := &brokerRun{orgID: orgID, emit: emit, pending: map[string]chan any{}, done: make(chan struct{})}
	b.mu.Lock()
	b.runs[sessionID] = run
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if b.runs[sessionID] == run {
			delete(b.runs, sessionID)
		}
		b.mu.Unlock()
		close(run.done)
	}
}

// Requester is the sandbox.InputRequester to hand sandbox.InputPack for the
// run on sessionID.
func (b *Broker) Requester(sessionID uuid.UUID) sandbox.InputRequester {
	return &sessionRequester{broker: b, sessionID: sessionID}
}

type sessionRequester struct {
	broker    *Broker
	sessionID uuid.UUID
}

func (r *sessionRequester) Request(req sandbox.InputRequestData) (any, error) {
	return r.broker.request(r.sessionID, req)
}

func (b *Broker) request(sessionID uuid.UUID, req sandbox.InputRequestData) (any, error) {
	b.mu.Lock()
	run, ok := b.runs[sessionID]
	if !ok {
		b.mu.Unlock()
		return nil, fmt.Errorf("this run has no live connection to ask")
	}
	id := req.RequestID
	if id == "" {
		id = uuid.NewString()
	}
	ch := make(chan any, 1)
	run.pending[id] = ch
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(run.pending, id)
		b.mu.Unlock()
	}()

	run.emit(Event{
		Type:    "input_request",
		Content: req.Message,
		Args: map[string]any{
			"requestId":   id,
			"inputType":   req.InputType,
			"options":     req.Options,
			"placeholder": req.Placeholder,
			// Questions carries its own json tags; a nil slice for the
			// other input types marshals as null, which a client reads as
			// "not a questions prompt".
			"questions": req.Questions,
		},
	})

	select {
	case v := <-ch:
		return v, nil
	case <-run.done:
		return nil, fmt.Errorf("the run ended before anyone replied")
	case <-time.After(AskTimeout):
		return nil, fmt.Errorf("no reply within %s", AskTimeout)
	}
}

// Reply resolves a pending ask(). orgID must match the tenant the run
// registered under.
func (b *Broker) Reply(sessionID, orgID uuid.UUID, requestID string, value any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	run, ok := b.runs[sessionID]
	if !ok || run.orgID != orgID {
		return fmt.Errorf("no live run is waiting for input on this session")
	}
	ch, ok := run.pending[requestID]
	if !ok {
		return fmt.Errorf("no pending input request %q — it may have timed out", requestID)
	}
	delete(run.pending, requestID)
	ch <- value // buffered; request() owns the drain
	return nil
}
