package ext_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mind-vm/thinking-core/ext"
	"github.com/mind-vm/thinking-core/sandbox"
)

func blocksSandbox(t *testing.T) (*sandbox.Sandbox, *[]sandbox.Event) {
	t.Helper()
	s := sandbox.New(ext.BlocksPack(ext.BlocksOptions{}))
	var blocks []sandbox.Event
	s.SetOnEvent(func(e sandbox.Event) {
		if e.Kind == sandbox.EventBlock {
			blocks = append(blocks, e)
		}
	})
	return s, &blocks
}

// The payload is the renderer's contract, so the test pins it as JSON —
// the shape a client actually receives — rather than as Go values.
func payloadJSON(t *testing.T, e sandbox.Event) string {
	t.Helper()
	b, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("payload does not marshal: %v", err)
	}
	return string(b)
}

func TestBlocksEmitTheContractShapes(t *testing.T) {
	s, blocks := blocksSandbox(t)
	_, _, err := s.ExecuteTurn(`
var rows = [{region: "North", revenue: 1972.6}, {region: "West", revenue: 2285.25}];
emitMarkdown("**Revenue** by region");
emitTable({title: "Regions", columns: ["region", "revenue"], rows: rows});
emitChart({variant: "bar", data: rows, x: "region", series: ["revenue"]});
emitStats({items: [{label: "Total", value: "4,257.85", unit: "EUR", delta: 0.12}]});
`, nil)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	want := []struct{ summary, payload string }{
		{"markdown", `{"markdown":"**Revenue** by region"}`},
		{"table", `{"columns":["region","revenue"],"rows":[{"region":"North","revenue":1972.6},{"region":"West","revenue":2285.25}],"title":"Regions"}`},
		{"chart", `{"data":[{"region":"North","revenue":1972.6},{"region":"West","revenue":2285.25}],"series":["revenue"],"variant":"bar","x":"region"}`},
		{"stats", `{"items":[{"delta":0.12,"label":"Total","unit":"EUR","value":"4,257.85"}]}`},
	}
	if len(*blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d", len(*blocks), len(want))
	}
	for i, w := range want {
		e := (*blocks)[i]
		if e.Summary != w.summary || payloadJSON(t, e) != w.payload {
			t.Errorf("block %d = %s %s\nwant      %s %s", i, e.Summary, payloadJSON(t, e), w.summary, w.payload)
		}
	}
	if _, ok := s.TakeAnswer(); ok {
		t.Error("emitting a block ended the run")
	}
}

// A renderer reading value as a string shows nothing for a number, so the
// pack converts rather than letting the figure vanish.
func TestBlocksStatsConvertsNumericValues(t *testing.T) {
	s, blocks := blocksSandbox(t)
	if _, _, err := s.ExecuteTurn(`emitStats({items: [{label: "Orders", value: 7}, {label: "Revenue", value: 6137.85}]})`, nil); err != nil {
		t.Fatalf("script: %v", err)
	}
	got := payloadJSON(t, (*blocks)[0])
	if !strings.Contains(got, `"value":"7"`) || !strings.Contains(got, `"value":"6137.85"`) {
		t.Errorf("numeric values not converted to strings: %s", got)
	}
}

// Each case is a spec a renderer would draw wrong without saying so.
func TestBlocksRejectSpecsThatWouldDrawWrong(t *testing.T) {
	rows := `[{region: "North", revenue: 1}]`
	cases := []struct{ name, script, want string }{
		{"chart series not in rows", `emitChart({variant: "bar", data: ` + rows + `, x: "region", series: ["total"]})`, `series "total" is a key of no row`},
		{"chart x not in rows", `emitChart({variant: "bar", data: ` + rows + `, x: "month", series: ["revenue"]})`, `x "month" is a key of no row`},
		{"chart without data", `emitChart({variant: "bar", data: [], x: "region", series: ["revenue"]})`, "data must be a non-empty array"},
		{"table column not in rows", `emitTable({columns: ["region", "Revenue"], rows: ` + rows + `})`, `column "Revenue" is a key of no row, so it would show empty; the rows have "region", "revenue"`},
		{"table without columns", `emitTable({rows: ` + rows + `})`, "columns must be a non-empty array"},
		{"stats without label", `emitStats({items: [{value: "1"}]})`, "items[0] has no label"},
		{"stats NaN", `emitStats({items: [{label: "Avg", value: 0/0}]})`, "compute a real number first"},
		{"stats object value", `emitStats({items: [{label: "Avg", value: {n: 1}}]})`, "needs value as a formatted string"},
		{"not an object", `emitTable("a,b")`, "pass a spec object"},
		{"empty markdown", `emitMarkdown("  ")`, "pass the markdown text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, blocks := blocksSandbox(t)
			_, _, err := s.ExecuteTurn(tc.script, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "nothing was shown") {
				t.Errorf("error does not say the block was withheld: %v", err)
			}
			if len(*blocks) != 0 {
				t.Errorf("a rejected spec was still emitted")
			}
		})
	}
}

func TestBlocksSparklineNeedsNoX(t *testing.T) {
	s, blocks := blocksSandbox(t)
	if _, _, err := s.ExecuteTurn(`emitChart({variant: "sparkline", data: [{v: 1}, {v: 3}], series: ["v"]})`, nil); err != nil {
		t.Fatalf("sparkline without x rejected: %v", err)
	}
	if len(*blocks) != 1 {
		t.Fatal("sparkline not emitted")
	}
}

func TestBlocksGuidanceReplacesTheDefault(t *testing.T) {
	p := ext.BlocksPack(ext.BlocksOptions{Guidance: "// Every number comes from sql.query()."})
	if !strings.Contains(p.Prompt, "sql.query()") || strings.Contains(p.Prompt, "never invent one") {
		t.Errorf("guidance not replaced:\n%s", p.Prompt)
	}
	if !strings.Contains(p.Prompt, "declare function emitChart(") {
		t.Error("declarations lost with custom guidance")
	}
}
