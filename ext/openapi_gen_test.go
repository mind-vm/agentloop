package ext

import "testing"

func TestSanitizeIdent(t *testing.T) {
	cases := map[string]string{
		"getPetById": "getPetById",
		"pet-id":     "pet_id",
		"pet.id":     "pet_id",
		"123abc":     "_123abc",
		"":           "_",
		"a b/c[d]":   "a_b_c_d_",
	}
	for in, want := range cases {
		if got := sanitizeIdent(in); got != want {
			t.Errorf("sanitizeIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveName(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{"GET", "/pets", "getPets"},
		{"GET", "/pets/{petId}", "getPetsByPetId"},
		{"GET", "/pets/{petId}/photos", "getPetsByPetIdPhotos"},
		{"POST", "/pets", "postPets"},
		{"DELETE", "/pets/{pet-id}", "deletePetsByPetId"},
	}
	for _, c := range cases {
		if got := deriveName(c.method, c.path); got != c.want {
			t.Errorf("deriveName(%q, %q) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestDedupeName(t *testing.T) {
	used := map[string]bool{}
	first := dedupeName("getPets", used)
	second := dedupeName("getPets", used)
	third := dedupeName("getPets", used)

	if first != "getPets" {
		t.Errorf("first: got %q, want getPets", first)
	}
	if second != "getPets2" {
		t.Errorf("second: got %q, want getPets2", second)
	}
	if third != "getPets3" {
		t.Errorf("third: got %q, want getPets3", third)
	}
	for _, name := range []string{first, second, third} {
		if !used[name] {
			t.Errorf("%q should be marked used", name)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Pet Store":       "pet-store",
		"Pet Store API!!": "pet-store-api",
		"  spaced  ":      "spaced",
		"":                "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTsType(t *testing.T) {
	cases := map[string]string{
		"string":  "string",
		"integer": "number",
		"number":  "number",
		"boolean": "boolean",
		"array":   "any[]",
		"object":  "any",
		"":        "any",
		"unknown": "any",
	}
	for in, want := range cases {
		if got := tsType(in); got != want {
			t.Errorf("tsType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderFunctionDecl(t *testing.T) {
	cases := []struct {
		name string
		op   operation
		want string
	}{
		{
			name: "no params",
			op:   operation{funcName: "ping", method: "GET", path: "/ping"},
			want: "/** GET /ping */\ndeclare function ping(params?: {}): " + responseTypeJS + ";\n",
		},
		{
			name: "required path param makes params required",
			op: operation{
				funcName:   "getPetById",
				method:     "GET",
				path:       "/pets/{petId}",
				summary:    "Get a pet",
				pathParams: []param{{name: "petId", required: true, typ: "string"}},
			},
			want: "/** Get a pet */\ndeclare function getPetById(params: { petId: string; }): " + responseTypeJS + ";\n",
		},
		{
			name: "optional query param",
			op: operation{
				funcName:    "listPets",
				method:      "GET",
				path:        "/pets",
				queryParams: []param{{name: "limit", required: false, typ: "integer"}},
			},
			want: "/** GET /pets */\ndeclare function listPets(params?: { limit?: number; }): " + responseTypeJS + ";\n",
		},
		{
			name: "required body makes params required",
			op: operation{
				funcName:     "createPet",
				method:       "POST",
				path:         "/pets",
				hasBody:      true,
				bodyRequired: true,
			},
			want: "/** POST /pets */\ndeclare function createPet(params: { body: any; // JSON-serialized request body }): " + responseTypeJS + ";\n",
		},
		{
			name: "optional body keeps params optional",
			op: operation{
				funcName: "search",
				method:   "GET",
				path:     "/search",
				hasBody:  true,
			},
			want: "/** GET /search */\ndeclare function search(params?: { body?: any; // JSON-serialized request body }): " + responseTypeJS + ";\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderFunctionDecl(c.op); got != c.want {
				t.Errorf("renderFunctionDecl() =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestPathExprJS(t *testing.T) {
	cases := map[string]string{
		"/pets":                 `BASE_URL + "/pets"`,
		"/pets/{petId}":         `BASE_URL + "/pets/" + encodeURIComponent(String(params.petId))`,
		"/pets/{pet-id}/photos": `BASE_URL + "/pets/" + encodeURIComponent(String(params.pet_id)) + "/photos"`,
		"/{a}/{b}":              `BASE_URL + "/" + encodeURIComponent(String(params.a)) + "/" + encodeURIComponent(String(params.b))`,
	}
	for in, want := range cases {
		if got := pathExprJS(in); got != want {
			t.Errorf("pathExprJS(%q) = %q, want %q", in, got, want)
		}
	}
}
