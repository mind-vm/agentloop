package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/dop251/goja"
)

// fetchPolicyConfig is the shape the fetch pack expects from
// PolicyChecker.ConfigJSON when the policy result allows. The pack
// does the URL matching locally so policy implementations stay
// tool-agnostic.
//
// `urls` is the allowlist; `allow_all` is the escape hatch (e.g. for
// internal tooling that's already gated upstream). When the policy
// supplies `URLAllowlistPolicy`, it converts to this shape with
// `urls = AllowedPrefixes`.
type fetchPolicyConfig struct {
	URLs     []string `json:"urls"`
	AllowAll bool     `json:"allow_all"`

	// Whitelist is the legacy spelling URLAllowlistPolicy emits. The
	// fetch pack accepts both so a caller can mix policies that emit
	// `urls` with URLAllowlistPolicy's own ConfigJSON.
	Whitelist []string `json:"whitelist"`
}

// effectiveURLs returns the union of URLs and Whitelist.
func (c *fetchPolicyConfig) effectiveURLs() []string {
	if len(c.URLs) > 0 {
		return c.URLs
	}
	return c.Whitelist
}

// FetchPack creates a pack with fetch(), htmlToMarkdown(), allowUrls(),
// and fetchWhitelist() primitives. The pack runs every URL through the
// installed PolicyChecker (under tool name "fetch") and surfaces the
// allowlist through fetchWhitelist() so agents can pre-check before
// looping over fetches.
//
// requester is the InputRequester used for the runtime allowUrls()
// auto-prompt — when fetch() is called against an unapproved host, the
// pack asks the user. Pass nil to skip the prompt path; fetches against
// unapproved hosts then fail with a policy-denial error.
func FetchPack(ctx context.Context, requester InputRequester) Pack {
	// Session-scoped allowed hostnames (populated by allowUrls() approvals).
	sessionAllowed := make(map[string]bool)

	// currentPolicyConfig caches the most recent fetch policy config so
	// fetchWhitelist() can return the URL list without re-querying.
	var currentPolicyConfig fetchPolicyConfig

	isSessionAllowed := func(hostname string) bool {
		if sessionAllowed[hostname] {
			return true
		}
		for domain := range sessionAllowed {
			if strings.HasSuffix(hostname, "."+domain) {
				return true
			}
		}
		return false
	}

	return fetchPackBuild(ctx, requester, sessionAllowed, isSessionAllowed, &currentPolicyConfig)
}

func fetchPackBuild(
	ctx context.Context,
	requester InputRequester,
	sessionAllowed map[string]bool,
	isSessionAllowed func(string) bool,
	currentPolicyConfig *fetchPolicyConfig,
) Pack {
	// policyCheckFetch runs the policy gate for a URL and refreshes the
	// cached config on allow. Session-scoped user approvals
	// short-circuit the policy: they're a user-facing runtime mechanism
	// orthogonal to admin governance.
	policyCheckFetch := func(s *Sandbox, rawURL string) (bool, string) {
		if parsed, err := url.Parse(rawURL); err == nil {
			if isSessionAllowed(parsed.Hostname()) {
				return true, ""
			}
		}
		res, err := s.CheckPolicy(ctx, "fetch", map[string]any{"url": rawURL})
		if err != nil {
			return false, fmt.Sprintf("policy error: %v", err)
		}
		if !res.Allowed {
			return false, res.Reason
		}
		var cfg fetchPolicyConfig
		if len(res.ConfigJSON) > 0 {
			_ = json.Unmarshal(res.ConfigJSON, &cfg)
		}
		*currentPolicyConfig = cfg
		if cfg.AllowAll {
			return true, ""
		}
		if isURLAllowed(rawURL, cfg.effectiveURLs()) {
			return true, ""
		}
		// No explicit allowlist in ConfigJSON and the policy didn't
		// pre-filter the URL → it's a transparent allow. Honour it.
		if len(cfg.effectiveURLs()) == 0 {
			return true, ""
		}
		return false, fmt.Sprintf("URL %q not in allowlist", rawURL)
	}

	return Pack{
		Name:        "fetch",
		Description: "HTTP requests, web scraping, and URL access management",
		Prompt: `// --- Web & Fetch ---

/**
 * HTTP GET a URL. SYNCHRONOUS — returns the response object directly. This is the
 * sandbox's own fetch, NOT the browser Fetch API: no Promise, no await, no res.json().
 * body is the raw response text — use JSON.parse(res.body) for JSON endpoints.
 * Pass opts.headers to send request headers (e.g. Authorization for a bearer token).
 * Auto-prompts the user for access if the domain is not whitelisted.
 */
declare function fetch(url: string, opts?: { headers?: Record<string, string> }): { status: number; body: string; headers: Record<string, string> };
/** Convert an HTML string to markdown — e.g. htmlToMarkdown(fetch(url).body) for a readable page. */
declare function htmlToMarkdown(html: string): string;
/** Pre-approve domains before fetching in a loop. Not needed for single fetches — fetch() auto-prompts. */
declare function allowUrls(domains: string | string[]): boolean;
/** URL patterns currently allowed without a prompt. */
declare function fetchWhitelist(): string[];`,
		HelpEntries: map[string]string{
			"fetch":          "fetch(url, opts?) — Fetch a URL and return {status, body, headers}. Pass {headers:{Authorization:'Bearer ...'}} to send request headers. Auto-prompts for access if domain is not whitelisted.",
			"htmlToMarkdown": "htmlToMarkdown(html) — Convert an HTML string to markdown.",
			"allowUrls":      "allowUrls(domains) — Request user permission to access domains. Pass an array of domain strings. Prompts once per domain.",
			"fetchWhitelist": "fetchWhitelist() — Returns the array of allowed URL patterns.",
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			requestAccess := func(rawURL string) bool {
				hostname := rawURL
				if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
					hostname = parsed.Hostname()
				}
				if isSessionAllowed(hostname) {
					return true
				}
				if ok, _ := policyCheckFetch(s, rawURL); ok {
					return true
				}
				if requester == nil {
					return false
				}

				s.Emit(Event{Kind: EventAsk, Summary: fmt.Sprintf("allowUrls: requesting access to %s", hostname)})
				value, err := requester.Request(InputRequestData{
					InputType: "confirm",
					Message:   fmt.Sprintf("Allow the agent to access %s?", hostname),
				})
				if err != nil {
					s.Emit(Event{Kind: EventError, Summary: fmt.Sprintf("allowUrls: %s", err.Error())})
					return false
				}
				if result, ok := value.(bool); ok && result {
					sessionAllowed[hostname] = true
					s.Emit(Event{Kind: EventAskResult, Summary: fmt.Sprintf("allowUrls: %s approved", hostname)})
					return true
				}
				s.Emit(Event{Kind: EventAskResult, Summary: fmt.Sprintf("allowUrls: %s denied", hostname)})
				return false
			}

			_ = rt.Set("allowUrls", func(call goja.FunctionCall) goja.Value {
				arg := call.Argument(0)
				if goja.IsUndefined(arg) || goja.IsNull(arg) {
					panic(rt.NewGoError(fmt.Errorf("allowUrls: url or array of urls is required")))
				}
				var domains []string
				switch exported := arg.Export().(type) {
				case string:
					domains = []string{exported}
				case []any:
					for _, item := range exported {
						if s, ok := item.(string); ok && s != "" {
							domains = append(domains, s)
						}
					}
				case []string:
					domains = exported
				default:
					domains = []string{arg.String()}
				}
				if len(domains) == 0 {
					panic(rt.NewGoError(fmt.Errorf("allowUrls: url is required")))
				}
				// Deduplicate by hostname so a list of URLs sharing a
				// host produces a single prompt.
				hostnames := make(map[string]string)
				for _, rawURL := range domains {
					hostname := rawURL
					if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
						hostname = parsed.Hostname()
					}
					if _, seen := hostnames[hostname]; !seen {
						hostnames[hostname] = rawURL
					}
				}
				allApproved := true
				for _, rawURL := range hostnames {
					if !requestAccess(rawURL) {
						allApproved = false
					}
				}
				return rt.ToValue(allApproved)
			})

			// _http(url, opts?) — internal primitive the `http` module wraps.
			_ = rt.Set("_http", func(call goja.FunctionCall) goja.Value {
				rawURL := call.Argument(0).String()
				if rawURL == "" {
					panic(rt.NewGoError(fmt.Errorf("http: url is required")))
				}
				if ok, reason := policyCheckFetch(s, rawURL); !ok {
					s.Emit(Event{Kind: EventError, Summary: fmt.Sprintf("http blocked: %s", rawURL)})
					panic(rt.NewGoError(fmt.Errorf("http: URL %q not allowed (%s) — use allowUrls() to request access", rawURL, reason)))
				}
				method := "GET"
				var body string
				var headers map[string]string
				if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) {
					opts := call.Argument(1).Export()
					if m, ok := opts.(map[string]any); ok {
						if v, ok := m["method"].(string); ok {
							method = v
						}
						if v, ok := m["body"].(string); ok {
							body = v
						}
						headers = extractHeaders(m["headers"])
					}
				}
				s.Emit(Event{Kind: EventFetch, Summary: fmt.Sprintf("http.%s: %s", strings.ToLower(method), rawURL), Detail: rawURL})
				result, err := doFetchRequest(ctx, rawURL, method, body, headers)
				if err != nil {
					s.Emit(Event{Kind: EventError, Summary: "http error", Detail: err.Error()})
					panic(rt.NewGoError(fmt.Errorf("http: %w", err)))
				}
				s.Emit(Event{Kind: EventFetch, Summary: fmt.Sprintf("http.%s: %s → %d", strings.ToLower(method), rawURL, result["status"]), Result: fmt.Sprintf("%d", result["status"])})
				return rt.ToValue(result)
			})

			_ = rt.Set("fetch", func(call goja.FunctionCall) goja.Value {
				rawURL := call.Argument(0).String()
				if rawURL == "" {
					panic(rt.NewGoError(fmt.Errorf("fetch: url is required")))
				}
				if !requestAccess(rawURL) {
					s.Emit(Event{Kind: EventError, Summary: fmt.Sprintf("fetch blocked: %s", rawURL)})
					panic(rt.NewGoError(fmt.Errorf("fetch: access to %q was denied by the user", rawURL)))
				}
				var headers map[string]string
				if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) {
					if m, ok := call.Argument(1).Export().(map[string]any); ok {
						headers = extractHeaders(m["headers"])
					}
				}
				s.Emit(Event{Kind: EventFetch, Summary: fmt.Sprintf("fetch: %s", rawURL), Detail: rawURL})
				result, err := doFetchRequest(ctx, rawURL, "GET", "", headers)
				if err != nil {
					s.Emit(Event{Kind: EventError, Summary: "fetch error", Detail: err.Error()})
					panic(rt.NewGoError(fmt.Errorf("fetch: %w", err)))
				}
				s.Emit(Event{Kind: EventFetch, Summary: fmt.Sprintf("fetch: %s → %d", rawURL, result["status"]), Result: fmt.Sprintf("%d", result["status"])})
				return rt.ToValue(result)
			})

			_ = rt.Set("htmlToMarkdown", func(call goja.FunctionCall) goja.Value {
				html := call.Argument(0).String()
				if html == "" {
					return rt.ToValue("")
				}
				markdown, err := htmltomarkdown.ConvertString(html)
				if err != nil {
					panic(rt.NewGoError(fmt.Errorf("htmlToMarkdown: %w", err)))
				}
				return rt.ToValue(markdown)
			})

			// fetchWhitelist() — agents inspect the allowlist before
			// running a fetch loop. An empty URL triggers a policy
			// check that surfaces the current ConfigJSON without
			// rejecting anything.
			_ = rt.Set("fetchWhitelist", func(call goja.FunctionCall) goja.Value {
				_, _ = policyCheckFetch(s, "")
				return rt.ToValue(currentPolicyConfig.effectiveURLs())
			})
		},
	}
}

// HttpModulePack registers a built-in `http` module reachable via
// require('http'). Wraps the internal _http() primitive (registered by
// FetchPack) so the script has named verbs instead of an options bag.
//
// HttpModulePack depends on FetchPack being registered — _http needs
// to exist at require time. Register both as a pair.
func HttpModulePack() Pack {
	return Pack{
		Name:        "http",
		Description: "HTTP client for API requests",
		Prompt: `/** Synchronous HTTP client, e.g. const http = require('http'). All methods return the response directly (no Promise). body is a raw string: JSON.stringify(payload) on the way in, JSON.parse(res.body) on the way out; set Content-Type via opts.headers. Call allowUrls() first if the domain is not whitelisted. */
declare module 'http' {
  interface HttpResponse { status: number; body: string; headers: Record<string, string> }
  interface HttpOpts { headers?: Record<string, string> }
  function get(url: string, opts?: HttpOpts): HttpResponse;
  function post(url: string, body?: string, opts?: HttpOpts): HttpResponse;
  function put(url: string, body?: string, opts?: HttpOpts): HttpResponse;
  function del(url: string, opts?: HttpOpts): HttpResponse;
}`,
		HelpEntries: map[string]string{
			"http": `require('http') — HTTP client module for API requests.
  var h = require('http');
  h.get(url, opts?) — GET request
  h.post(url, body?, opts?) — POST request
  h.put(url, body?, opts?) — PUT request
  h.del(url, opts?) — DELETE request
  opts.headers sends request headers, e.g. {headers:{Authorization:'Bearer '+token}}.
  All methods return {status, body, headers}.`,
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			s.AddSkillCode("http", httpModuleJS)
		},
	}
}

var httpModuleJS = strings.TrimSpace(`
// HTTP module — wraps the internal _http() function with explicit methods.
// Usage: var h = require('http'); h.get(url); h.post(url, body);
//        h.get(url, {headers: {Authorization: "Bearer " + token}});

function _headers(opts) {
  return (opts && opts.headers) ? opts.headers : undefined;
}

exports.get = function(url, opts) {
  return _http(url, {method: "GET", headers: _headers(opts)});
};

exports.post = function(url, body, opts) {
  return _http(url, {method: "POST", body: body || "", headers: _headers(opts)});
};

exports.put = function(url, body, opts) {
  return _http(url, {method: "PUT", body: body || "", headers: _headers(opts)});
};

exports.del = function(url, opts) {
  return _http(url, {method: "DELETE", headers: _headers(opts)});
};
`)

// extractHeaders coerces a JS headers object (the exported form of
// {Authorization: "Bearer ...", "Content-Type": "application/json"})
// into a Go string map. Non-string values are stringified so an agent
// that passes a number doesn't silently drop the header. Returns nil
// for a missing or non-object value so callers can range over it safely.
func extractHeaders(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		switch s := val.(type) {
		case string:
			out[k] = s
		default:
			out[k] = fmt.Sprintf("%v", val)
		}
	}
	return out
}

// isURLAllowed reports whether rawURL matches any pattern in whitelist.
// Patterns support: bare host ("example.com"), wildcard subdomain
// ("*.example.com"), path prefix ("api.example.com/v1"), and the
// global wildcard ("*"). Stripping a leading scheme is supported so
// operators can paste a URL straight into the allowlist.
func isURLAllowed(rawURL string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return false
	}
	for _, entry := range whitelist {
		if entry == "*" {
			return true
		}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	for _, entry := range whitelist {
		cleaned := entry
		if idx := strings.Index(cleaned, "://"); idx != -1 {
			cleaned = cleaned[idx+3:]
		}
		cleaned = strings.TrimSuffix(cleaned, "*")
		cleaned = strings.TrimSuffix(cleaned, "/")

		entryHost, entryPath, _ := strings.Cut(cleaned, "/")
		if entryPath != "" {
			entryPath = "/" + entryPath
		}
		hostMatch := false
		if strings.HasPrefix(entryHost, "*.") {
			suffix := entryHost[1:]
			hostMatch = strings.HasSuffix(host, suffix) || host == entryHost[2:]
		} else {
			hostMatch = host == entryHost
		}
		if hostMatch {
			if entryPath == "" || strings.HasPrefix(parsed.Path, entryPath) {
				return true
			}
		}
	}
	return false
}

// doFetchRequest issues the HTTP call and shapes the response into the
// {status, body, headers} object the JS side returns. Body is capped
// at 2 MiB so a runaway agent fetching a 100 MB binary doesn't OOM
// the host process.
//
// headers are caller-supplied request headers (e.g. Authorization for a
// bearer token, Content-Type for a JSON body). They're applied after the
// default User-Agent so a caller can override it if it really wants to.
func doFetchRequest(ctx context.Context, rawURL, method, body string, headers map[string]string) (map[string]any, error) {
	if method == "" {
		method = "GET"
	}
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "agentloop/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	respHeaders := make(map[string]string)
	for k := range resp.Header {
		respHeaders[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return map[string]any{
		"status":  resp.StatusCode,
		"body":    string(respBody),
		"headers": respHeaders,
	}, nil
}
