package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// NewOpenAI builds a Client against any OpenAI-wire-compatible
// /chat/completions endpoint: OpenAI itself, OpenRouter, EdenAI,
// Groq, together.ai, a local vLLM/Ollama server, and most other hosted
// or self-hosted LLM gateways speak this same protocol.
const (
	defaultBaseURL   = "https://api.openai.com/v1"
	defaultChatModel = "gpt-4o-mini"
)

// Config configures the OpenAI-compatible client.
type Config struct {
	APIKey          string
	ChatModel       string
	BaseURL         string
	ReasoningEffort string
	// Headers carries extra request headers some gateways require or
	// recommend — e.g. OpenRouter's optional HTTP-Referer / X-Title.
	Headers map[string]string
	// HTTPClient is optional; nil falls back to a 5-minute-timeout client.
	HTTPClient *http.Client

	// MaxRetries caps how many extra attempts a /chat/completions
	// request gets after its first one fails retryably — a transport
	// error, a 429, or a 5xx. Zero uses the package default (3);
	// negative disables retrying entirely.
	//
	// Retries happen before any of the response body is read, so a
	// streaming call never replays deltas the caller already saw.
	MaxRetries int

	// RetryBaseDelay is the wait before the first retry; each further
	// attempt doubles it, plus jitter, capped at 30s. A Retry-After
	// header from the upstream overrides both. Zero uses the package
	// default (500ms).
	RetryBaseDelay time.Duration
}

// ConfigFromEnv reads OPENAI_API_KEY / OPENAI_CHAT_MODEL / OPENAI_BASE_URL /
// OPENAI_REASONING_EFFORT / OPENAI_MAX_RETRIES.
//
//	OPENAI_API_KEY          — required to actually build a client.
//	OPENAI_CHAT_MODEL       — default "gpt-4o-mini"; override to match
//	                          whatever BaseURL expects (e.g.
//	                          "openai/gpt-4o-mini" on OpenRouter,
//	                          "google/gemini-2.5-flash" on EdenAI).
//	OPENAI_BASE_URL         — default the official OpenAI API; point this
//	                          at "https://openrouter.ai/api/v1",
//	                          "https://api.edenai.run/v3", or any other
//	                          OpenAI-wire-compatible host.
//	OPENAI_REASONING_EFFORT — optional ("low"|"medium"|"high"), caps
//	                          thinking on reasoning models.
//	OPENAI_MAX_RETRIES      — optional; retries after a failed attempt,
//	                          default 3. "0" disables retrying. A value
//	                          that isn't an integer is ignored.
func ConfigFromEnv() Config {
	cfg := Config{
		APIKey:          os.Getenv("OPENAI_API_KEY"),
		ChatModel:       envOr("OPENAI_CHAT_MODEL", defaultChatModel),
		BaseURL:         envOr("OPENAI_BASE_URL", defaultBaseURL),
		ReasoningEffort: os.Getenv("OPENAI_REASONING_EFFORT"),
	}
	// "0" has to mean "no retries", but Config's zero value means
	// "use the default" — so map an explicit 0 onto the negative
	// disable sentinel.
	if n, err := strconv.Atoi(os.Getenv("OPENAI_MAX_RETRIES")); err == nil {
		if n <= 0 {
			n = -1
		}
		cfg.MaxRetries = n
	}
	return cfg
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// openAIClient implements Client against an OpenAI-wire-compatible
// POST {baseURL}/chat/completions endpoint.
type openAIClient struct {
	apiKey          string
	baseURL         string
	chatModel       string
	reasoningEffort string
	headers         map[string]string
	http            *http.Client
	maxRetries      int
	retryBaseDelay  time.Duration
}

// NewOpenAI builds a Client from cfg. Returns an error if APIKey is
// empty — the caller decides what a missing key means for the run it's
// building (e.g. skip registering the "ai" capability).
func NewOpenAI(cfg Config) (Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("llm: OPENAI_API_KEY is not set")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	maxRetries := cfg.MaxRetries
	switch {
	case maxRetries == 0:
		maxRetries = defaultMaxRetries
	case maxRetries < 0:
		maxRetries = 0
	}
	retryBaseDelay := cfg.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = defaultRetryBaseDelay
	}
	return &openAIClient{
		apiKey:          cfg.APIKey,
		baseURL:         strings.TrimRight(orDefault(cfg.BaseURL, defaultBaseURL), "/"),
		chatModel:       orDefault(cfg.ChatModel, defaultChatModel),
		reasoningEffort: cfg.ReasoningEffort,
		headers:         cfg.Headers,
		http:            hc,
		maxRetries:      maxRetries,
		retryBaseDelay:  retryBaseDelay,
	}, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (c *openAIClient) Name() string { return "openai-compatible" }

func (c *openAIClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return CompletionResponse{}, errors.New("llm: complete: messages is empty")
	}
	model := orDefault(req.Model, c.chatModel)
	wire := chatRequest{
		Model:           model,
		Messages:        buildMessages(req.Messages),
		ReasoningEffort: c.reasoningEffort,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
	}
	parsed, err := c.postChat(ctx, wire)
	if err != nil {
		return CompletionResponse{}, err
	}
	out := CompletionResponse{Model: model}
	if len(parsed.Choices) > 0 {
		out.Content = parsed.Choices[0].Message.Content
	}
	if parsed.Usage != nil {
		out.InputTokens = parsed.Usage.PromptTokens
		out.OutputTokens = parsed.Usage.CompletionTokens
	}
	return out, nil
}

// Stream runs a chat completion with stream:true over SSE, delivering
// content deltas to onChunk. Upstreams that reject the streaming request
// outright (a 4xx before any delta) fall back to Complete plus one
// trailing chunk, preserving "best-effort incremental, full response
// always" without the caller having to know which upstream it's talking
// to.
func (c *openAIClient) Stream(ctx context.Context, req CompletionRequest, onChunk func(string)) (CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return CompletionResponse{}, errors.New("llm: stream: messages is empty")
	}
	model := orDefault(req.Model, c.chatModel)
	wire := chatRequest{
		Model:           model,
		Messages:        buildMessages(req.Messages),
		ReasoningEffort: c.reasoningEffort,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		Stream:          true,
		StreamOptions:   &streamOptions{IncludeUsage: true},
	}

	out, err := c.streamChat(ctx, model, wire, onChunk)
	if err == nil {
		return out, nil
	}
	// A retryable status (429) has already exhausted its retries inside
	// doJSONPost — falling back to Complete would only spend the same
	// budget again against a limit that is still closed.
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !isRetryableStatus(apiErr.StatusCode) {
		full, cerr := c.Complete(ctx, req)
		if cerr != nil {
			return full, cerr
		}
		if onChunk != nil && full.Content != "" {
			onChunk(full.Content)
		}
		return full, nil
	}
	return CompletionResponse{}, err
}

func (c *openAIClient) streamChat(ctx context.Context, model string, wire chatRequest, onChunk func(string)) (CompletionResponse, error) {
	body, err := json.Marshal(wire)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("llm: stream marshal: %w", err)
	}
	respBody, err := c.doJSONPost(ctx, "/chat/completions", body)
	if err != nil {
		return CompletionResponse{}, err
	}
	defer func() { _ = respBody.Close() }()

	out := CompletionResponse{Model: model}
	var content strings.Builder
	sawFrame := false
	var raw strings.Builder // non-SSE body, kept in case the upstream ignored stream:true
	scanner := bufio.NewScanner(respBody)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			if !sawFrame && raw.Len() < 1024*1024 {
				raw.WriteString(line)
				raw.WriteString("\n")
			}
			continue
		}
		sawFrame = true
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // one malformed frame shouldn't kill the stream
		}
		if len(chunk.Choices) > 0 {
			if delta := chunk.Choices[0].Delta.Content; delta != "" {
				content.WriteString(delta)
				if onChunk != nil {
					onChunk(delta)
				}
			}
		}
		if chunk.Usage != nil {
			out.InputTokens = chunk.Usage.PromptTokens
			out.OutputTokens = chunk.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return CompletionResponse{}, fmt.Errorf("llm: stream read: %w", err)
	}
	if !sawFrame {
		var parsed chatResponse
		if err := json.Unmarshal([]byte(raw.String()), &parsed); err != nil {
			return CompletionResponse{}, fmt.Errorf("llm: stream: no SSE frames and body not a completion: %w", err)
		}
		if len(parsed.Choices) > 0 {
			out.Content = parsed.Choices[0].Message.Content
			if onChunk != nil && out.Content != "" {
				onChunk(out.Content)
			}
		}
		if parsed.Usage != nil {
			out.InputTokens = parsed.Usage.PromptTokens
			out.OutputTokens = parsed.Usage.CompletionTokens
		}
		return out, nil
	}
	out.Content = content.String()
	return out, nil
}

type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []chatMessage  `json:"messages"`
	Temperature     *float64       `json:"temperature,omitempty"`
	MaxTokens       *int           `json:"max_tokens,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Stream          bool           `json:"stream,omitempty"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("llm: status %d: %s", e.StatusCode, e.Body)
}

func buildMessages(in []Message) []chatMessage {
	out := make([]chatMessage, 0, len(in))
	for _, m := range in {
		out = append(out, chatMessage{Role: roleFor(m.Role), Content: m.Content})
	}
	return out
}

// roleFor maps llm's roles to OpenAI vocabulary. Unknown roles
// fall through to "user" — the upstream rejects bogus values with a
// clear error, so over-validating here gains nothing.
func roleFor(r string) string {
	switch r {
	case "system", "assistant":
		return r
	default:
		return "user"
	}
}

func (c *openAIClient) postChat(ctx context.Context, wire chatRequest) (*chatResponse, error) {
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal: %w", err)
	}
	respBody, err := c.doJSONPost(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = respBody.Close() }()
	var parsed chatResponse
	if err := json.NewDecoder(respBody).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("llm: decode: %w", err)
	}
	return &parsed, nil
}

// doJSONPost issues the request and returns the still-unread response
// body on success. Transport errors, 429s, and 5xx are retried with
// exponential backoff plus jitter, honouring a Retry-After header when
// the upstream sends one; every other status fails immediately, since
// the server will reject the identical request the same way next time.
//
// Nothing is read from a successful body here, and a failed attempt's
// body is drained and closed before the next one — so the streaming
// caller downstream always receives a fresh, fully-unconsumed stream.
func (c *openAIClient) doJSONPost(ctx context.Context, path string, body []byte) (io.ReadCloser, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			// A malformed base URL fails identically on every attempt.
			return nil, fmt.Errorf("llm: request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}

		hint := absentRetryAfter()
		resp, err := c.http.Do(req)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("llm: do: %w", err)
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return resp.Body, nil
		default:
			buf, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			hint = parseRetryAfterHeader(resp.Header.Get("Retry-After"), time.Now())
			lastErr = &apiError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(buf))}
			if !isRetryableStatus(resp.StatusCode) {
				return nil, lastErr
			}
		}

		// A cancelled Run isn't a provider failure — report it as
		// itself rather than as whatever the in-flight attempt returned.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt >= c.maxRetries {
			return nil, lastErr
		}
		if err := sleepCtx(ctx, retryDelay(c.retryBaseDelay, attempt, hint)); err != nil {
			return nil, err
		}
	}
}

var _ Client = (*openAIClient)(nil)
