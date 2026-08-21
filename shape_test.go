package agentloop

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDataShapeTypeScriptLiteral pins the digest's TS-type-literal rendering:
// the model reads this with the same instincts as the ## Sandbox API
// declarations, so the syntax (member separators, []-suffix arrays, counts as
// comments) is load-bearing prompt surface, not just debug output.
func TestDataShapeTypeScriptLiteral(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "object of primitives",
			raw:  `{"name":"acme","total":42,"open":true,"note":null}`,
			want: `{ name: string; note: null; open: boolean; total: number }`,
		},
		{
			name: "array of objects with count",
			raw:  `{"caps":[{"customer":"a","cap_usd":1},{"customer":"b","cap_usd":2},{"customer":"c","cap_usd":3}]}`,
			want: `{ caps: { cap_usd: number; customer: string }[] /* 3 items */ }`,
		},
		{
			name: "empty array",
			raw:  `{"rows":[]}`,
			want: `{ rows: unknown[] /* empty */ }`,
		},
		{
			name: "nested array element parenthesised",
			raw:  `{"grid":[[1,2],[3,4],[5,6]]}`,
			want: `{ grid: (number[] /* 2 items */)[] /* 3 items */ }`,
		},
		{
			name: "empty object digests to nothing",
			raw:  `{}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dataShape(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Fatalf("dataShape(%s)\n got: %s\nwant: %s", tc.raw, got, tc.want)
			}
		})
	}
}

// TestDataShapeKeyCapRendersComment verifies the truncation marker is a TS
// comment, not a bare ellipsis that would break the type-literal reading.
func TestDataShapeKeyCapRendersComment(t *testing.T) {
	m := map[string]int{}
	for i := 0; i < shapeMaxKeys+3; i++ {
		m[string(rune('a'+i%26))+string(rune('0'+i/26))] = i
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got := dataShape(raw)
	want := "/* +3 more keys */"
	if !strings.Contains(got, want) {
		t.Fatalf("digest missing truncation comment %q:\n%s", want, got)
	}
}
