package ext

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/dop251/goja"
	"github.com/getkin/kin-openapi/openapi3"

	"github.com/jryannel/agentloop/sandbox"
)

// OpenAPIConfig configures OpenAPIPack.
type OpenAPIConfig struct {
	// Name is the skill's name — the string passed to require(name) and
	// shown in skillList(). Defaults to a slug of spec.Info.Title, or
	// "api" if the spec has no title.
	Name string

	// Description is the skillList() one-liner. Defaults to
	// spec.Info.Title, or a generic fallback if the spec has none.
	Description string

	// BaseURL overrides the spec's first `servers` entry. Required if
	// the spec declares no servers.
	BaseURL string

	// Headers are applied to every request this skill's functions
	// make — the place to put a static API key or bearer token.
	// OpenAPI's securitySchemes are not interpreted; this is the whole
	// auth story OpenAPIPack has.
	Headers map[string]string

	// Methods restricts which HTTP methods become functions. Nil
	// generates GET, POST, PUT, PATCH, DELETE (the common REST verbs);
	// HEAD/OPTIONS/TRACE/CONNECT are never generated regardless.
	Methods []string
}

var defaultOpenAPIMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// OpenAPIPack turns an OpenAPI 3 document into a require()-able skill:
// one JS function per operation (path + method), named after the
// operation's operationId (sanitized) or derived from the method and
// path when it has none.
//
// Each generated function calls the sandbox's own internal _http
// primitive — the same one require('http') wraps (see pack_fetch.go in
// the sandbox package) — rather than a new HTTP client. That's the
// whole design: OpenAPIPack only generates JavaScript source (a
// string), so every call a generated function makes is still
// policy-gated exactly like fetch()/require('http') already are, with
// no new Go-side networking code to audit. _http is used directly
// rather than require('http')'s get/post/put/del because it uniformly
// supports every method via its `method` option, including PATCH,
// which the http module has no wrapper for.
//
// Following the skill-lazy-loading pattern the rest of agentloop's
// skill mechanism uses (see sandbox.SkillDiscoveryPack's doc comment),
// the returned Pack's Prompt is empty — a spec can have far more
// operations than are worth inlining into every system prompt. Only
// Name and Description reach skillList(); the full per-operation
// documentation is registered as a HelpEntry under Name, so
// skillGet(name) returns it on demand.
//
// Parameters are passed as a single options object rather than
// positionally — `functionName(params)`, with params.<name> for each
// path/query parameter and params.body for the request body — since
// OpenAPI operations vary too much in parameter count and optionality
// for a positional signature to stay readable. Required-ness is
// documented but not enforced client-side; the server is still free to
// reject a missing required parameter.
//
// Returns an error if the spec resolves to zero generated operations
// (no BaseURL and no servers, no paths, or every operation filtered out
// by Methods) — a skill with nothing callable is a config mistake, not
// something to silently install.
func OpenAPIPack(spec *openapi3.T, cfg OpenAPIConfig) (sandbox.Pack, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" && len(spec.Servers) > 0 {
		baseURL = spec.Servers[0].URL
	}
	if baseURL == "" {
		return sandbox.Pack{}, fmt.Errorf("ext: OpenAPIPack: no BaseURL configured and the spec declares no servers")
	}

	name := cfg.Name
	if name == "" {
		name = slugify(specTitle(spec))
	}
	if name == "" {
		name = "api"
	}

	description := cfg.Description
	if description == "" {
		description = specTitle(spec)
	}
	if description == "" {
		description = "REST API generated from an OpenAPI spec"
	}

	methods := cfg.Methods
	if len(methods) == 0 {
		methods = defaultOpenAPIMethods
	}
	allowed := make(map[string]bool, len(methods))
	for _, m := range methods {
		allowed[strings.ToUpper(m)] = true
	}

	ops := collectOperations(spec, allowed)
	if len(ops) == 0 {
		return sandbox.Pack{}, fmt.Errorf("ext: OpenAPIPack: spec %q produced no operations (check Methods and that the spec has paths)", name)
	}

	moduleJS, docs := renderSkill(baseURL, cfg.Headers, ops)

	return sandbox.Pack{
		Name:        name,
		Description: description,
		HelpEntries: map[string]string{name: docs},
		Register: func(_ *goja.Runtime, sb *sandbox.Sandbox) {
			sb.AddSkillCode(name, moduleJS)
		},
	}, nil
}
