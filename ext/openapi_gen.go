package ext

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// operation is the extracted, spec-independent shape OpenAPIPack needs
// to render one JS function + its doc entry. Pulled out of
// *openapi3.Operation so rendering doesn't have to touch the spec
// types at all.
type operation struct {
	funcName     string
	method       string
	path         string // the raw OpenAPI path template, e.g. "/pets/{petId}"
	summary      string
	pathParams   []param // always required, per the OpenAPI spec; sorted by name
	queryParams  []param
	hasBody      bool
	bodyRequired bool
}

// param is one path or query parameter, reduced to what rendering
// needs: a JS-safe identifier, whether it's required, and an OpenAPI
// schema type ("string", "integer", ...) for the generated TS
// declaration — display purposes only, no runtime validation.
type param struct {
	name     string
	required bool
	typ      string
}

// collectOperations walks every path × method in spec, keeping only
// methods in allowed, and returns them sorted by (path, method) for
// deterministic generation — map iteration order in the underlying
// spec is not stable, and stable output is what makes two runs over
// the same spec produce identical function names.
func collectOperations(spec *openapi3.T, allowed map[string]bool) []operation {
	if spec.Paths == nil {
		return nil
	}
	pathMap := spec.Paths.Map()
	paths := make([]string, 0, len(pathMap))
	for p := range pathMap {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	used := map[string]bool{}
	var ops []operation
	for _, path := range paths {
		item := pathMap[path]
		methods := make([]string, 0, len(item.Operations()))
		for m := range item.Operations() {
			methods = append(methods, m)
		}
		sort.Strings(methods)

		for _, method := range methods {
			if !allowed[method] {
				continue
			}
			op := item.Operations()[method]
			ops = append(ops, extractOperation(item, op, method, path, used))
		}
	}
	return ops
}

// extractOperation builds one operation from a path item + its
// operation for one method. used tracks already-assigned function
// names across the whole spec so a name collision (two operations that
// sanitize/derive to the same identifier) gets a numeric suffix rather
// than one silently shadowing the other in the generated module.
func extractOperation(item *openapi3.PathItem, op *openapi3.Operation, method, path string, used map[string]bool) operation {
	name := op.OperationID
	if name != "" {
		name = sanitizeIdent(name)
	} else {
		name = deriveName(method, path)
	}
	name = dedupeName(name, used)

	params := mergeParameters(item.Parameters, op.Parameters)
	var pathParams []param
	var queryParams []param
	for _, p := range params {
		switch p.In {
		case "path":
			// Path parameters are always required, per the OpenAPI spec
			// (a $ref or authoring error that says otherwise is not
			// something to trust over that rule).
			pathParams = append(pathParams, param{name: sanitizeIdent(p.Name), required: true, typ: schemaTypeOf(p.Schema)})
		case "query":
			queryParams = append(queryParams, param{name: sanitizeIdent(p.Name), required: p.Required, typ: schemaTypeOf(p.Schema)})
		}
	}
	sort.Slice(pathParams, func(i, j int) bool { return pathParams[i].name < pathParams[j].name })
	sort.Slice(queryParams, func(i, j int) bool { return queryParams[i].name < queryParams[j].name })

	hasBody := false
	bodyRequired := false
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		hasBody = true
		bodyRequired = op.RequestBody.Value.Required
	}

	return operation{
		funcName:     name,
		method:       method,
		path:         path,
		summary:      op.Summary,
		pathParams:   pathParams,
		queryParams:  queryParams,
		hasBody:      hasBody,
		bodyRequired: bodyRequired,
	}
}

// mergeParameters combines path-item-level and operation-level
// parameters, per the OpenAPI rule that an operation-level parameter
// with the same (in, name) overrides the path-level one.
func mergeParameters(pathLevel, opLevel openapi3.Parameters) []*openapi3.Parameter {
	byKey := map[string]*openapi3.Parameter{}
	var order []string
	add := func(refs openapi3.Parameters) {
		for _, ref := range refs {
			if ref == nil || ref.Value == nil {
				continue
			}
			p := ref.Value
			key := p.In + "|" + p.Name
			if _, seen := byKey[key]; !seen {
				order = append(order, key)
			}
			byKey[key] = p
		}
	}
	add(pathLevel)
	add(opLevel)

	out := make([]*openapi3.Parameter, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

func schemaTypeOf(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Value == nil || ref.Value.Type == nil || len(*ref.Value.Type) == 0 {
		return "string"
	}
	return (*ref.Value.Type)[0]
}

func specTitle(spec *openapi3.T) string {
	if spec.Info == nil {
		return ""
	}
	return spec.Info.Title
}

func specDescription(spec *openapi3.T) string {
	if spec.Info == nil {
		return ""
	}
	return spec.Info.Description
}

func specVersion(spec *openapi3.T) string {
	if spec.Info == nil {
		return ""
	}
	return spec.Info.Version
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a spec title into a require()-able skill name:
// lowercase, non-alphanumeric runs collapsed to a single hyphen,
// leading/trailing hyphens trimmed.
func slugify(s string) string {
	s = nonSlugRe.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s, "-")
}

var identInvalidRe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// sanitizeIdent turns an arbitrary OpenAPI name (operationId, parameter
// name) into a valid JS identifier: invalid characters become
// underscores, and a leading digit gets a leading underscore.
func sanitizeIdent(s string) string {
	s = identInvalidRe.ReplaceAllString(s, "_")
	if s == "" {
		return "_"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "_" + s
	}
	return s
}

// deriveName builds a function name from a method + path template when
// an operation has no operationId, e.g. "GET /pets/{petId}/photos" ->
// "getPetsByPetIdPhotos".
func deriveName(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			b.WriteString("By")
			b.WriteString(pascalCase(strings.Trim(seg, "{}")))
		} else {
			b.WriteString(pascalCase(seg))
		}
	}
	return sanitizeIdent(b.String())
}

func pascalCase(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strings.ToUpper(f[:1]))
		b.WriteString(f[1:])
	}
	return b.String()
}

// dedupeName appends a numeric suffix (2, 3, ...) until name is unused,
// then marks it used. Two operations that derive to the same identifier
// — a real possibility with derived (operationId-less) names — must not
// silently overwrite one function with another in the generated module.
func dedupeName(name string, used map[string]bool) string {
	candidate := name
	for n := 2; used[candidate]; n++ {
		candidate = fmt.Sprintf("%s%d", name, n)
	}
	used[candidate] = true
	return candidate
}
