package ext_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/mind-vm/agentloop/ext"
	"github.com/mind-vm/agentloop/sandbox"
)

const petStoreSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "Pet Store", "version": "1.0.0"},
  "servers": [{"url": "https://api.petstore.example"}],
  "paths": {
    "/pets/{petId}": {
      "get": {
        "operationId": "getPetById",
        "summary": "Get a pet by ID",
        "parameters": [
          {"name": "petId", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "verbose", "in": "query", "required": false, "schema": {"type": "boolean"}}
        ],
        "responses": {"200": {"description": "ok"}}
      }
    },
    "/pets": {
      "post": {
        "operationId": "createPet",
        "summary": "Create a pet",
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"type": "object"}}}
        },
        "responses": {"201": {"description": "created"}}
      }
    }
  }
}`

func mustLoadSpec(t *testing.T, doc string) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte(doc))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return spec
}

func TestOpenAPIPack_NameAndDescriptionDefaults(t *testing.T) {
	spec := mustLoadSpec(t, petStoreSpec)
	pack, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{})
	if err != nil {
		t.Fatalf("OpenAPIPack: %v", err)
	}
	if pack.Name != "pet-store" {
		t.Errorf("Name: got %q, want %q", pack.Name, "pet-store")
	}
	if pack.Description != "Pet Store" {
		t.Errorf("Description: got %q, want %q", pack.Description, "Pet Store")
	}
	// The Prompt is a short always-visible pointer, not the full API —
	// it must name the skill and how to get more, without inlining every
	// operation's signature into the system prompt.
	for _, want := range []string{"Pet Store", `skillGet("pet-store")`, `require("pet-store")`} {
		if !strings.Contains(pack.Prompt, want) {
			t.Errorf("Prompt missing %q:\n%s", want, pack.Prompt)
		}
	}
	if strings.Contains(pack.Prompt, "declare function") {
		t.Errorf("Prompt should not contain full declarations — those belong behind skillGet(), got:\n%s", pack.Prompt)
	}

	// skillGet() docs are TypeScript ambient declarations, one per
	// operation, with an intro naming the API and its base URL.
	docs, ok := pack.HelpEntries["pet-store"]
	if !ok {
		t.Fatalf("HelpEntries[%q] missing", "pet-store")
	}
	for _, want := range []string{
		"Pet Store (v1.0.0)",
		"declare function getPetById(params: { petId: string; verbose?: boolean; }): { status: number; body: string; headers: Record<string, string> };",
		"declare function createPet(params: { body: any", // requestBody.required = true in the fixture
	} {
		if !strings.Contains(docs, want) {
			t.Errorf("docs missing %q:\n%s", want, docs)
		}
	}
}

func TestOpenAPIPack_ConfigOverridesDefaults(t *testing.T) {
	spec := mustLoadSpec(t, petStoreSpec)
	pack, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{
		Name:        "petstore",
		Description: "custom description",
		BaseURL:     "https://override.example",
	})
	if err != nil {
		t.Fatalf("OpenAPIPack: %v", err)
	}
	if pack.Name != "petstore" || pack.Description != "custom description" {
		t.Errorf("got Name=%q Description=%q", pack.Name, pack.Description)
	}
}

func TestOpenAPIPack_NoBaseURLNoServers_Errors(t *testing.T) {
	spec := mustLoadSpec(t, `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{"/x":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	if _, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{}); err == nil {
		t.Fatalf("expected an error when neither BaseURL nor spec servers are set")
	}
}

func TestOpenAPIPack_MethodFilterExcludingEverything_Errors(t *testing.T) {
	spec := mustLoadSpec(t, petStoreSpec)
	_, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{Methods: []string{http.MethodDelete}})
	if err == nil {
		t.Fatalf("expected an error when Methods filters out every operation")
	}
}

// TestOpenAPIPack_GeneratedFunctionsCallTheRealAPI is the end-to-end
// test: parse a spec, generate a skill, require() it inside a real
// sandbox, and confirm the generated functions produce the right HTTP
// requests against a real (test) server — path param substitution,
// query params, static headers, and a JSON body.
func TestOpenAPIPack_GeneratedFunctionsCallTheRealAPI(t *testing.T) {
	type recorded struct {
		method  string
		path    string
		query   string
		body    string
		headers http.Header
	}
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, recorded{
			method:  r.Method,
			path:    r.URL.Path,
			query:   r.URL.RawQuery,
			body:    string(body),
			headers: r.Header,
		})
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"42","name":"Rex"}`))
		case http.MethodPost:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"99"}`))
		}
	}))
	defer srv.Close()

	spec := mustLoadSpec(t, petStoreSpec)
	pack, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{
		BaseURL: srv.URL,
		Headers: map[string]string{"X-Api-Key": "secret"},
	})
	if err != nil {
		t.Fatalf("OpenAPIPack: %v", err)
	}

	ctx := context.Background()
	s := sandbox.New(sandbox.RequirePack(ctx), sandbox.FetchPack(ctx, nil), pack)

	out, err := s.Execute(`
		var api = require("pet-store");
		var res = api.getPetById({petId: "42", verbose: true});
		JSON.stringify({status: res.status, body: JSON.parse(res.body)});
	`)
	if err != nil {
		t.Fatalf("getPetById: %v", err)
	}
	var getResult struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &getResult); err != nil {
		t.Fatalf("decode result %q: %v", out, err)
	}
	if getResult.Status != 200 {
		t.Errorf("status: got %d, want 200", getResult.Status)
	}

	if len(got) != 1 {
		t.Fatalf("expected 1 request, got %d", len(got))
	}
	if got[0].method != http.MethodGet {
		t.Errorf("method: got %q, want GET", got[0].method)
	}
	if got[0].path != "/pets/42" {
		t.Errorf("path: got %q, want /pets/42", got[0].path)
	}
	if got[0].query != "verbose=true" {
		t.Errorf("query: got %q, want verbose=true", got[0].query)
	}
	if got[0].headers.Get("X-Api-Key") != "secret" {
		t.Errorf("X-Api-Key header: got %q, want secret", got[0].headers.Get("X-Api-Key"))
	}

	_, err = s.Execute(`
		var api = require("pet-store");
		api.createPet({body: {name: "Rex"}});
	`)
	if err != nil {
		t.Fatalf("createPet: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 requests total, got %d", len(got))
	}
	if got[1].method != http.MethodPost {
		t.Errorf("method: got %q, want POST", got[1].method)
	}
	if got[1].path != "/pets" {
		t.Errorf("path: got %q, want /pets", got[1].path)
	}
	if got[1].body != `{"name":"Rex"}` {
		t.Errorf("body: got %q, want {\"name\":\"Rex\"}", got[1].body)
	}
	if got[1].headers.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type header: got %q, want application/json", got[1].headers.Get("Content-Type"))
	}
	if got[1].headers.Get("X-Api-Key") != "secret" {
		t.Errorf("X-Api-Key header on POST: got %q, want secret (static headers must survive the JSON-body merge)", got[1].headers.Get("X-Api-Key"))
	}
}

// TestOpenAPIPack_RequireIsPolicyGatedByName confirms require("pet-store")
// goes through the same per-module PolicyChecker gate as any other
// skill — DefaultPolicy denies nothing by name by default, so a policy
// that explicitly denies it must block the require() call.
func TestOpenAPIPack_RequireIsPolicyGatedByName(t *testing.T) {
	spec := mustLoadSpec(t, petStoreSpec)
	pack, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{BaseURL: "https://example.com"})
	if err != nil {
		t.Fatalf("OpenAPIPack: %v", err)
	}

	ctx := context.Background()
	s := sandbox.New(sandbox.RequirePack(ctx), sandbox.FetchPack(ctx, nil), pack)
	s.SetPolicy(denyPolicy{tool: "pet-store"})

	_, err = s.Execute(`require("pet-store")`)
	if err == nil {
		t.Fatalf("expected require(\"pet-store\") to be denied by policy")
	}
}

type denyPolicy struct{ tool string }

func (p denyPolicy) Check(_ context.Context, toolName string, _ map[string]any) (sandbox.PolicyCheckResult, error) {
	if toolName == p.tool {
		return sandbox.PolicyCheckResult{Allowed: false, Reason: "denied for test"}, nil
	}
	return sandbox.PolicyCheckResult{Allowed: true}, nil
}
