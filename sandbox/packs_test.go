package sandbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// --- discovery ------------------------------------------------------

// The prompt promises {name, description, type}. goja maps a Go struct
// by FIELD name, not json tag, so the pack round-trips through
// encoding/json to get the documented keys. This pins that: replacing
// the round-trip with a direct rt.ToValue(skills) would silently start
// handing the model {Name, Description, Type} and every script written
// against the documented shape would read undefined.
func TestSkillListUsesTheDocumentedKeys(t *testing.T) {
	s := New(SkillDiscoveryPack([]SkillInfo{
		{Name: "fetch", Description: "fetch things", Type: "pack"},
	}))

	_, ret := turn(t, s, `function run(args) { return skillList(); }`)

	var got []map[string]any
	if err := json.Unmarshal(ret, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", ret, err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills", len(got))
	}
	for _, key := range []string{"name", "description", "type"} {
		if _, ok := got[0][key]; !ok {
			t.Errorf("missing key %q — the model is told to expect it. Got %v", key, got[0])
		}
	}
}

// Agents call skillList().forEach(...), and null would crash the run.
func TestSkillListIsAnArrayWhenEmpty(t *testing.T) {
	s := New(SkillDiscoveryPack(nil))
	_, ret := turn(t, s, `function run(args) { return { n: skillList().length }; }`)
	if !strings.Contains(string(ret), `"n":0`) {
		t.Errorf("ret = %s, want an empty array", ret)
	}
}

func TestSkillGet(t *testing.T) {
	s := New(SkillDiscoveryPack([]SkillInfo{
		{Name: "fetch", Description: "the short description", Type: "pack"},
	}))

	_, ret := turn(t, s, `function run(args) { return skillGet("fetch"); }`)
	if !strings.Contains(string(ret), "the short description") {
		t.Errorf("ret = %s", ret)
	}

	// An unknown name answers with something actionable rather than
	// throwing — the model can recover by listing.
	_, ret = turn(t, s, `function run(args) { return skillGet("nope"); }`)
	if !strings.Contains(string(ret), "skillList") {
		t.Errorf("an unknown skill should point at skillList, got %s", ret)
	}
}

// Detailed help registered by a pack wins over the one-line summary.
func TestSkillGetPrefersDetailedHelp(t *testing.T) {
	documented := Pack{
		Name:        "documented",
		Description: "one line",
		HelpEntries: map[string]string{"documented": "the long form, with an example"},
	}
	s := New(documented, SkillDiscoveryPack([]SkillInfo{
		{Name: "documented", Description: "one line", Type: "pack"},
	}))

	_, ret := turn(t, s, `function run(args) { return skillGet("documented"); }`)
	if !strings.Contains(string(ret), "the long form") {
		t.Errorf("ret = %s, want the detailed entry", ret)
	}
}

// --- help -----------------------------------------------------------

func TestHelp(t *testing.T) {
	s := New(Pack{
		Name:        "thing",
		HelpEntries: map[string]string{"doThing": "doThing(x) — does the thing.\n  More detail here."},
	}, HelpPack())

	_, ret := turn(t, s, `function run(args) { return help("doThing"); }`)
	if !strings.Contains(string(ret), "does the thing") {
		t.Errorf("ret = %s", ret)
	}

	// The listing shows only the first line of each entry, so one
	// verbose primitive cannot bury the rest.
	_, ret = turn(t, s, `function run(args) { return help(); }`)
	listing := string(ret)
	if !strings.Contains(listing, "doThing") {
		t.Errorf("listing = %s", listing)
	}
	if strings.Contains(listing, "More detail here") {
		t.Errorf("the listing should carry first lines only, got %s", listing)
	}
}

func TestHelpForAnUnknownName(t *testing.T) {
	s := New(HelpPack())
	_, ret := turn(t, s, `function run(args) { return help("nosuchthing"); }`)
	if !strings.Contains(string(ret), "help()") {
		t.Errorf("an unknown name should point back at help(), got %s", ret)
	}
}

// --- core primitives ------------------------------------------------

func TestLogCapturesEveryForm(t *testing.T) {
	s := New()
	logs, _ := turn(t, s, `
		log("a string");
		log({ k: "v" });
		log(1, "two");
		print("printed");
		console.log("consoled");
	`)
	for _, want := range []string{"a string", `"k"`, "two", "printed", "consoled"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q\ngot:\n%s", want, logs)
		}
	}
}

// assert exists so the model can turn its own logic error into a
// recoverable signal rather than computing on bad data.
func TestAssert(t *testing.T) {
	s := New()
	if _, _, err := s.ExecuteTurn(`function run(args) { assert(true, "fine"); return 1; }`, nil); err != nil {
		t.Errorf("a passing assertion should not fail the run: %v", err)
	}

	_, _, err := s.ExecuteTurn(`function run(args) { assert(false, "the invariant broke"); }`, nil)
	if err == nil {
		t.Fatal("a failed assertion should fail the run")
	}
	if !strings.Contains(err.Error(), "the invariant broke") {
		t.Errorf("the message should reach the model, got %v", err)
	}
}

func TestParseUrl(t *testing.T) {
	s := New()
	_, ret := turn(t, s, `function run(args) {
		const u = parseUrl("https://example.com:8443/a/b?q=1");
		return { host: u.hostname, path: u.pathname };
	}`)
	if !strings.Contains(string(ret), "example.com") || !strings.Contains(string(ret), "/a/b") {
		t.Errorf("ret = %s", ret)
	}

	// Anything without a host comes back NULL rather than throwing, so a
	// script can branch on it instead of wrapping every parse in a
	// try/catch. The host check is what does the real work here:
	// ParseRequestURI alone accepts an absolute path, which would
	// otherwise yield a hostless object a caller would go on to use.
	for _, rejected := range []string{
		"just/a/path",       // a genuinely relative reference
		"example.com/api",   // no scheme, so no host is parsed
		"/an/absolute/path", // a valid request URI, but hostless
		"",
	} {
		logs, ret, err := s.ExecuteTurn(`function run(args) { return parseUrl(`+quote(rejected)+`) === null; }`, nil)
		if err != nil {
			t.Errorf("parseUrl(%q) threw instead of returning null: %v (%s)", rejected, err, logs)
			continue
		}
		if string(ret) != "true" {
			t.Errorf("parseUrl(%q) = %s, want null", rejected, ret)
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- require --------------------------------------------------------

func TestRequireLoadsARegisteredSkill(t *testing.T) {
	s := New(RequirePack(context.Background()))
	s.AddSkillCode("greeter", `module.exports = { hello: function(n) { return "hello " + n; } };`)

	_, ret := turn(t, s, `function run(args) { return require("greeter").hello("world"); }`)
	if string(ret) != `"hello world"` {
		t.Errorf("ret = %s", ret)
	}
}

func TestRequireOfSomethingUnknown(t *testing.T) {
	s := New(RequirePack(context.Background()))
	if _, _, err := s.ExecuteTurn(`function run(args) { return require("nope"); }`, nil); err == nil {
		t.Error("requiring an unregistered module should fail")
	}
}

// The module is evaluated once and the result cached; a module with a
// side effect must not run it twice in a turn.
func TestRequireCachesTheModule(t *testing.T) {
	s := New(RequirePack(context.Background()))
	s.AddSkillCode("counter", `
		globalThis.__loads = (globalThis.__loads || 0) + 1;
		module.exports = { loads: function() { return globalThis.__loads; } };
	`)

	_, ret := turn(t, s, `function run(args) {
		require("counter");
		return require("counter").loads();
	}`)
	if string(ret) != "1" {
		t.Errorf("the module was evaluated %s times, want 1", ret)
	}
}

// --- markdown -------------------------------------------------------

func TestMarkdownModule(t *testing.T) {
	s := New(RequirePack(context.Background()), MarkdownModulePack())
	const doc = "# Title\\n\\nSome [a link](https://example.com) here.\\n\\n## Section\\n\\n- first item\\n- second item\\n"

	t.Run("links", func(t *testing.T) {
		_, ret := turn(t, s, `function run(args) {
			return require('markdown').links("`+doc+`").map(function(l) { return l.url; });
		}`)
		if !strings.Contains(string(ret), "https://example.com") {
			t.Errorf("ret = %s", ret)
		}
	})

	t.Run("headings", func(t *testing.T) {
		_, ret := turn(t, s, `function run(args) {
			return require('markdown').headings("`+doc+`").map(function(h) { return h.level + ":" + h.text; });
		}`)
		got := string(ret)
		if !strings.Contains(got, "1:Title") || !strings.Contains(got, "2:Section") {
			t.Errorf("ret = %s", got)
		}
	})

	t.Run("items", func(t *testing.T) {
		_, ret := turn(t, s, `function run(args) {
			return { n: require('markdown').items("`+doc+`").length };
		}`)
		if !strings.Contains(string(ret), `"n":2`) {
			t.Errorf("ret = %s", ret)
		}
	})
}

// --- system prompt --------------------------------------------------

// A pack's Prompt is the model's entire interface to it. If the prompt
// were dropped, the primitive would exist and be unreachable.
func TestSystemPromptCarriesEveryPacksPrompt(t *testing.T) {
	s := New(
		Pack{Name: "alpha", Prompt: "declare function alpha(): void;"},
		Pack{Name: "beta", Prompt: "declare function beta(): void;"},
		Pack{Name: "silent"}, // no prompt — contributes nothing, breaks nothing
	)

	prompt := s.SystemPrompt()
	for _, want := range []string{"alpha()", "beta()"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the system prompt is missing %q\ngot:\n%s", want, prompt)
		}
	}
}

func TestPacksAreReported(t *testing.T) {
	s := New(Pack{Name: "alpha"}, Pack{Name: "beta"})
	names := make([]string, 0, 2)
	for _, p := range s.Packs() {
		names = append(names, p.Name)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("Packs() = %v", names)
	}
}
