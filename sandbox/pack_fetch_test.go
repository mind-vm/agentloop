package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// askApprover is a test InputRequester: it answers a fixed way and
// records what it was asked.
type askApprover struct {
	answer bool
	err    error
	asked  []string
}

func (a *askApprover) Request(req InputRequestData) (any, error) {
	a.asked = append(a.asked, req.Message)
	if a.err != nil {
		return nil, a.err
	}
	return a.answer, nil
}

// fetchAgainst starts a server and returns a sandbox whose fetch pack
// points at it. httptest listens on 127.0.0.1, which DefaultPolicy
// treats as private — so the default policy is exactly the denial that
// exercises the approval path.
func fetchAgainst(t *testing.T, requester InputRequester, policy PolicyChecker, h http.HandlerFunc) (*Sandbox, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	s := New(FetchPack(context.Background(), requester))
	if policy != nil {
		s.SetPolicy(policy)
	}
	return s, srv.URL
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Echo-Method", r.Method)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("hello from " + r.URL.Path + "|auth=" + r.Header.Get("Authorization")))
}

func TestFetchReturnsStatusBodyAndHeaders(t *testing.T) {
	s, url := fetchAgainst(t, nil, AllowAll, echoHandler)

	// Response header keys are LOWERCASED, matching the web Fetch API's
	// case-insensitive Headers rather than Go's canonical casing. A
	// script indexing them by the name the server sent would read
	// undefined, so the normalisation is worth pinning.
	_, ret := turn(t, s, `function run(args) {
		const r = fetch(`+quote(url+"/thing")+`);
		return {
			status: r.status,
			body: r.body,
			method: r.headers["x-echo-method"],
			canonical: r.headers["X-Echo-Method"] === undefined,
		};
	}`)

	var got map[string]any
	if err := json.Unmarshal(ret, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", ret, err)
	}
	if got["status"] != float64(200) {
		t.Errorf("status = %v", got["status"])
	}
	if body, _ := got["body"].(string); !strings.Contains(body, "hello from /thing") {
		t.Errorf("body = %v", got["body"])
	}
	if got["method"] != "GET" {
		t.Errorf("response headers did not come through: %v", got)
	}
	if got["canonical"] != true {
		t.Errorf("header keys should be lowercased only, got %v", got)
	}
}

// The opts.headers path is how an agent authenticates, so it has to
// actually reach the wire.
func TestFetchSendsRequestHeaders(t *testing.T) {
	s, url := fetchAgainst(t, nil, AllowAll, echoHandler)

	_, ret := turn(t, s, `function run(args) {
		return fetch(`+quote(url)+`, { headers: { Authorization: "Bearer t0ken" } }).body;
	}`)
	if !strings.Contains(string(ret), "auth=Bearer t0ken") {
		t.Errorf("the request header did not reach the server: %s", ret)
	}
}

// --- the approval path ----------------------------------------------

// The documented "no human available" case: an unapproved host fails
// rather than hanging or silently proceeding.
func TestUnapprovedHostFailsWithNoRequester(t *testing.T) {
	s, url := fetchAgainst(t, nil, DefaultPolicy{}, echoHandler)

	_, _, err := s.ExecuteTurn(`function run(args) { return fetch(`+quote(url)+`); }`, nil)
	if err == nil {
		t.Fatal("a denied host should fail the call")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %v", err)
	}
}

func TestApprovedHostProceeds(t *testing.T) {
	approver := &askApprover{answer: true}
	s, url := fetchAgainst(t, approver, DefaultPolicy{}, echoHandler)

	_, ret := turn(t, s, `function run(args) { return fetch(`+quote(url)+`).status; }`)
	if string(ret) != "200" {
		t.Errorf("status = %s", ret)
	}
	if len(approver.asked) != 1 {
		t.Fatalf("asked %d times: %v", len(approver.asked), approver.asked)
	}
	// The host is what the user is deciding about, so it has to be in
	// the question.
	if !strings.Contains(approver.asked[0], "127.0.0.1") {
		t.Errorf("the prompt should name the host, got %q", approver.asked[0])
	}
}

func TestRefusedHostDoesNotFetch(t *testing.T) {
	var served bool
	approver := &askApprover{answer: false}
	s, url := fetchAgainst(t, approver, DefaultPolicy{}, func(w http.ResponseWriter, r *http.Request) {
		served = true
		echoHandler(w, r)
	})

	if _, _, err := s.ExecuteTurn(`function run(args) { return fetch(`+quote(url)+`); }`, nil); err == nil {
		t.Fatal("a refused fetch should fail")
	}
	if served {
		t.Error("a refused fetch reached the server anyway")
	}
}

// One approval covers the host for the rest of the session — otherwise
// a loop over ten pages asks ten times.
func TestApprovalIsRememberedForTheSession(t *testing.T) {
	approver := &askApprover{answer: true}
	s, url := fetchAgainst(t, approver, DefaultPolicy{}, echoHandler)

	turn(t, s, `function run(args) {
		fetch(`+quote(url+"/one")+`);
		fetch(`+quote(url+"/two")+`);
		return 1;
	}`)
	// And across turns, since the pack outlives a single one.
	turn(t, s, `function run(args) { return fetch(`+quote(url+"/three")+`).status; }`)

	if len(approver.asked) != 1 {
		t.Errorf("asked %d times, want 1: %v", len(approver.asked), approver.asked)
	}
}

// A requester that errors is not an approval.
func TestApproverErrorDeniesTheFetch(t *testing.T) {
	approver := &askApprover{err: context.Canceled}
	s, url := fetchAgainst(t, approver, DefaultPolicy{}, echoHandler)

	if _, _, err := s.ExecuteTurn(`function run(args) { return fetch(`+quote(url)+`); }`, nil); err == nil {
		t.Fatal("a failed permission request must not read as approval")
	}
}

// allowUrls is the pre-approval path, for a script about to fetch in a
// loop rather than one URL.
func TestAllowUrlsPreApproves(t *testing.T) {
	approver := &askApprover{answer: true}
	s, url := fetchAgainst(t, approver, DefaultPolicy{}, echoHandler)

	turn(t, s, `function run(args) {
		allowUrls(["127.0.0.1"]);
		return fetch(`+quote(url)+`).status;
	}`)
	if len(approver.asked) != 1 {
		t.Errorf("asked %d times, want 1 — allowUrls should cover the later fetch: %v", len(approver.asked), approver.asked)
	}
}

// A policy that already allows the URL must not prompt at all.
func TestAnAllowedURLNeverPrompts(t *testing.T) {
	approver := &askApprover{answer: true}
	srv := httptest.NewServer(http.HandlerFunc(echoHandler))
	t.Cleanup(srv.Close)

	s := New(FetchPack(context.Background(), approver))
	s.SetPolicy(DefaultPolicy{URLAllowPrefixes: []string{srv.URL + "/"}})

	turn(t, s, `function run(args) { return fetch(`+quote(srv.URL+"/x")+`).status; }`)
	if len(approver.asked) != 0 {
		t.Errorf("an allowed URL should not prompt: %v", approver.asked)
	}
}

func TestFetchWhitelistReportsThePolicyAllowlist(t *testing.T) {
	s := New(FetchPack(context.Background(), nil))
	s.SetPolicy(DefaultPolicy{URLAllowPrefixes: []string{"https://a.example/", "https://b.example/"}})

	_, ret := turn(t, s, `function run(args) { return fetchWhitelist(); }`)
	got := string(ret)
	if !strings.Contains(got, "a.example") || !strings.Contains(got, "b.example") {
		t.Errorf("fetchWhitelist() = %s", got)
	}
}

func TestHtmlToMarkdown(t *testing.T) {
	s := New(FetchPack(context.Background(), nil))
	_, ret := turn(t, s, `function run(args) {
		return htmlToMarkdown("<h1>Title</h1><p>Some <a href='https://example.com'>link</a>.</p>");
	}`)
	got := string(ret)
	if !strings.Contains(got, "Title") || !strings.Contains(got, "example.com") {
		t.Errorf("htmlToMarkdown = %s", got)
	}
}

// --- module names ---------------------------------------------------

// require() resolves names against registered skills, so a name
// colliding with a built-in module would shadow it, and one that is not
// a valid identifier could not be required at all.
func TestValidateModuleName(t *testing.T) {
	reserved := ReservedModuleNames()
	if len(reserved) == 0 {
		t.Fatal("no reserved names — the collision check has nothing to protect")
	}
	for _, name := range reserved {
		if err := ValidateModuleName(name); err == nil {
			t.Errorf("ValidateModuleName(%q) should reject a reserved name", name)
		}
	}

	// The rule is ^[a-zA-Z_][a-zA-Z0-9_]*$ — stricter than JavaScript
	// itself, which also admits $ and non-ASCII letters. Worth recording
	// as the actual contract rather than assuming JS's.
	for _, name := range []string{"", " ", "has space", "has-dash", "1leading", "dot.name", "$dollar", "café"} {
		if err := ValidateModuleName(name); err == nil {
			t.Errorf("ValidateModuleName(%q) should reject a name that is not an identifier", name)
		}
	}

	// Case is not part of the rule — only identifier shape and the
	// reserved set are.
	for _, name := range []string{"my_skill", "mySkill", "UPPER", "_private", "n1"} {
		if err := ValidateModuleName(name); err != nil {
			t.Errorf("ValidateModuleName(%q) = %v, want accepted", name, err)
		}
	}
}

func TestValidateModuleCode(t *testing.T) {
	if err := ValidateModuleCode("ok", `module.exports = { f: function() { return 1; } };`); err != nil {
		t.Errorf("valid code rejected: %v", err)
	}
	// Catching this before registration is the whole point: a syntax
	// error found at run time would fail a turn instead of the edit.
	if err := ValidateModuleCode("bad", `function ( {{{`); err == nil {
		t.Error("a syntax error should be reported before registration")
	}
}
