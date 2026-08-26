package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// --- delay arithmetic -------------------------------------------------

func TestParseRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		present bool
	}{
		{name: "seconds", value: "2", want: 2 * time.Second, present: true},
		{name: "zero seconds", value: "0", want: 0, present: true},
		{name: "padded", value: "  3 ", want: 3 * time.Second, present: true},
		{name: "http date", value: now.Add(3 * time.Second).Format(http.TimeFormat), want: 3 * time.Second, present: true},
		{name: "past http date clamps to zero", value: now.Add(-time.Hour).Format(http.TimeFormat), want: 0, present: true},
		{name: "over cap", value: "600", want: maxRetryDelay, present: true},
		{name: "empty", value: "", present: false},
		{name: "negative", value: "-5", present: false},
		{name: "garbage", value: "soon", present: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfterHeader(tt.value, now)
			if got.Present != tt.present {
				t.Fatalf("Present = %v, want %v", got.Present, tt.present)
			}
			if got.Present && got.Delay != tt.want {
				t.Fatalf("Delay = %s, want %s", got.Delay, tt.want)
			}
		})
	}
}

func TestRetryDelayPrefersRetryAfterOverBackoff(t *testing.T) {
	noJitter := func(time.Duration) time.Duration {
		t.Fatal("jitter must not be used when Retry-After is present")
		return 0
	}
	// The server's own hint wins, even when it is shorter than the
	// backoff this attempt would otherwise have used.
	got := retryDelayWithJitter(10*time.Second, 3, retryAfter{Delay: time.Second, Present: true}, noJitter)
	if got != time.Second {
		t.Fatalf("delay = %s, want 1s", got)
	}
	// A "retry now" hint means now, not the base delay.
	if got := retryDelayWithJitter(500*time.Millisecond, 0, retryAfter{Present: true}, noJitter); got != 0 {
		t.Fatalf("delay = %s, want 0", got)
	}
	// And it is still capped.
	if got := retryDelayWithJitter(500*time.Millisecond, 0, retryAfter{Delay: time.Hour, Present: true}, noJitter); got != maxRetryDelay {
		t.Fatalf("delay = %s, want %s", got, maxRetryDelay)
	}
}

func TestBackoffDelayDoublesPerAttempt(t *testing.T) {
	for attempt, want := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 2: 4 * time.Second} {
		got := backoffDelayWithJitter(time.Second, attempt, nil)
		if got != want {
			t.Fatalf("attempt %d: delay = %s, want %s", attempt, got, want)
		}
	}
}

func TestBackoffDelayAddsBoundedJitter(t *testing.T) {
	got := backoffDelayWithJitter(2*time.Second, 1, func(max time.Duration) time.Duration {
		if max != 2*time.Second { // half of the 4s delay
			t.Fatalf("jitter max = %s, want 2s", max)
		}
		return 1500 * time.Millisecond
	})
	if got != 5500*time.Millisecond {
		t.Fatalf("delay = %s, want 5.5s", got)
	}
}

func TestBackoffDelayJitterNeverExceedsCap(t *testing.T) {
	got := backoffDelayWithJitter(20*time.Second, 0, func(max time.Duration) time.Duration {
		if max != 10*time.Second {
			t.Fatalf("jitter max = %s, want 10s", max)
		}
		return max
	})
	if got != maxRetryDelay {
		t.Fatalf("delay = %s, want %s", got, maxRetryDelay)
	}
}

func TestBackoffDelayCapsRunawayAttempts(t *testing.T) {
	if got := backoffDelayWithJitter(time.Second, 40, nil); got != maxRetryDelay {
		t.Fatalf("delay = %s, want %s", got, maxRetryDelay)
	}
}

func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504}
	for _, s := range retryable {
		if !isRetryableStatus(s) {
			t.Errorf("status %d should be retryable", s)
		}
	}
	for _, s := range []int{400, 401, 403, 404, 409, 422} {
		if isRetryableStatus(s) {
			t.Errorf("status %d should not be retryable", s)
		}
	}
}

func TestSleepCtxReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx() = %v, want context.Canceled", err)
	}
}

// --- client wiring ----------------------------------------------------

const completionBody = `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`

// testClient points a client at srv with retries fast enough for a unit
// test. maxRetries of 0 means "use the package default"; pass -1 to
// disable retrying, matching Config.MaxRetries.
func testClient(t *testing.T, srv *httptest.Server, maxRetries int) Client {
	t.Helper()
	c, err := NewOpenAI(Config{
		APIKey:         "test-key",
		BaseURL:        srv.URL,
		HTTPClient:     srv.Client(),
		MaxRetries:     maxRetries,
		RetryBaseDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return c
}

func chat(t *testing.T, c Client) (CompletionResponse, error) {
	t.Helper()
	return c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
}

func TestCompleteRetriesUntilSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			fmt.Fprint(w, completionBody)
		}
	}))
	defer srv.Close()

	out, err := chat(t, testClient(t, srv, 0))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out.Content != "ok" {
		t.Fatalf("Content = %q, want %q", out.Content, "ok")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestCompleteRetriesTransportErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Drops the connection without a response, the way a
			// closed keep-alive or a mid-flight network blip does.
			panic(http.ErrAbortHandler)
		}
		fmt.Fprint(w, completionBody)
	}))
	defer srv.Close()

	if _, err := chat(t, testClient(t, srv, 0)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestCompleteDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad model"}`)
	}))
	defer srv.Close()

	_, err := chat(t, testClient(t, srv, 0))
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *apiError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (400 is not retryable)", got)
	}
}

func TestCompleteSurfacesLastErrorAfterRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"slow down"}`)
	}))
	defer srv.Close()

	_, err := chat(t, testClient(t, srv, 2))
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *apiError", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", apiErr.StatusCode)
	}
	if got := calls.Load(); got != 3 { // initial + 2 retries
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestCompleteRetryDisabled(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := chat(t, testClient(t, srv, -1)); err == nil {
		t.Fatal("Complete: want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (retries disabled)", got)
	}
}

// A Retry-After of 30s is far longer than the 1ms base delay, so a run
// whose deadline fires first proves the hint — not the backoff — is what
// the client waited on.
func TestCompleteHonorsRetryAfterHeaderOverBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := testClient(t, srv, 0).Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %s — the 30s hint should have been cut short by the deadline", elapsed)
	}
}

func TestCompleteReportsCancellationNotProviderError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel() // the Run is abandoned while this attempt is in flight
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := testClient(t, srv, 0).Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// --- streaming --------------------------------------------------------

func writeSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestStreamRetriesAndDeliversOneCleanStream(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSSE(w)
	}))
	defer srv.Close()

	var chunks []string
	out, err := testClient(t, srv, 0).Stream(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(c string) { chunks = append(chunks, c) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if out.Content != "ok" {
		t.Fatalf("Content = %q, want %q", out.Content, "ok")
	}
	// The failed attempt must not have leaked a delta to the caller.
	if len(chunks) != 1 || chunks[0] != "ok" {
		t.Fatalf("chunks = %q, want exactly one %q", chunks, "ok")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

// Stream falls back to Complete when an upstream rejects streaming
// outright, but a 429 is rate limiting, not a streaming rejection —
// retrying it a second time through Complete would double the budget
// spent against a window that is still closed.
func TestStreamDoesNotFallBackToCompleteOnRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := testClient(t, srv, 1).Stream(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("Stream: want error")
	}
	if got := calls.Load(); got != 2 { // initial + 1 retry, no Complete fallback
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestStreamStillFallsBackToCompleteOnUnsupported(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest) // "stream is not supported"
			return
		}
		fmt.Fprint(w, completionBody)
	}))
	defer srv.Close()

	var chunks []string
	out, err := testClient(t, srv, 0).Stream(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(c string) { chunks = append(chunks, c) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if out.Content != "ok" || len(chunks) != 1 {
		t.Fatalf("Content = %q, chunks = %q; want %q and one chunk", out.Content, chunks, "ok")
	}
}

// --- configuration ----------------------------------------------------

func TestConfigFromEnvMaxRetries(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{value: "", want: 0},     // unset: NewOpenAI applies the default
		{value: "5", want: 5},    //
		{value: "0", want: -1},   // explicit "no retries" → disable sentinel
		{value: "-2", want: -1},  // nonsense negative also means disable
		{value: "lots", want: 0}, // unparseable: fall back to the default
	}
	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			t.Setenv("OPENAI_MAX_RETRIES", tt.value)
			if got := ConfigFromEnv().MaxRetries; got != tt.want {
				t.Fatalf("MaxRetries = %d, want %d", got, tt.want)
			}
		})
	}
}
