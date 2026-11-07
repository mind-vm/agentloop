package view

import (
	"context"
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/mind-vm/thinking-core/sandbox"
)

func render(t *testing.T, b Block) string {
	t.Helper()
	out, err := Render(context.Background(), BlockView(b))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// chartConfig pulls the ApexCharts config out of a rendered chart block —
// the thing ApexCharts will actually draw, which is what has to be right.
func chartConfig(t *testing.T, out string) map[string]any {
	t.Helper()
	m := regexp.MustCompile(`data-tabler-chart="([^"]*)"`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no chart in %s", out)
	}
	var cfg struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Config
}

func payload(t *testing.T, js string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A fragment must not carry the chart start-up script: the page loaded it
// once, and a copy per streamed chart is kilobytes of dead text.
func TestFragmentOmitsChartScript(t *testing.T) {
	out := render(t, Block{Type: "chart", DOMID: "b", ChartID: "c1", Payload: payload(t, `{"variant":"bar","x":"m","series":["v"],"data":[{"m":"Jan","v":1}]}`)})
	if strings.Contains(out, "<script") {
		t.Fatalf("a chart fragment carries a script:\n%s", out)
	}
	if !strings.Contains(out, `id="chart-c1"`) {
		t.Fatalf("chart element not named by ChartID:\n%s", out)
	}
}

func TestBarChartSeriesAndCategories(t *testing.T) {
	cfg := chartConfig(t, render(t, Block{Type: "chart", ChartID: "c", Payload: payload(t,
		`{"variant":"bar","x":"month","series":["revenue","cost"],"data":[{"month":"Jan","revenue":10,"cost":"4"},{"month":"Feb","revenue":12.5,"cost":5}]}`)}))
	cats := cfg["xaxis"].(map[string]any)["categories"].([]any)
	if len(cats) != 2 || cats[0] != "Jan" {
		t.Fatalf("categories = %v", cats)
	}
	series := cfg["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("series = %v", series)
	}
	cost := series[1].(map[string]any)
	// A numeric string is a number: CSV parsed in a script stays text.
	if d := cost["data"].([]any); d[0] != 4.0 || d[1] != 5.0 {
		t.Fatalf("cost data = %v", d)
	}
}

func TestDonutIsOneSlicePerRow(t *testing.T) {
	cfg := chartConfig(t, render(t, Block{Type: "chart", ChartID: "c", Payload: payload(t,
		`{"variant":"donut","x":"product","series":["revenue"],"data":[{"product":"A","revenue":3},{"product":"B","revenue":7}]}`)}))
	if cfg["chart"].(map[string]any)["type"] != "donut" {
		t.Fatalf("type = %v", cfg["chart"])
	}
	if l := cfg["labels"].([]any); len(l) != 2 || l[1] != "B" {
		t.Fatalf("labels = %v", l)
	}
	if s := cfg["series"].([]any); s[1] != 7.0 {
		t.Fatalf("series = %v", s)
	}
}

func TestCompositeTypesEachSerie(t *testing.T) {
	cfg := chartConfig(t, render(t, Block{Type: "chart", ChartID: "c", Payload: payload(t,
		`{"variant":"composite","x":"m","series":["orders","revenue"],"types":{"revenue":"line"},"data":[{"m":"Jan","orders":3,"revenue":10}]}`)}))
	series := cfg["series"].([]any)
	if series[0].(map[string]any)["type"] != "column" || series[1].(map[string]any)["type"] != "line" {
		t.Fatalf("series = %v", series)
	}
}

func TestScatterDropsPointsWithoutCoordinates(t *testing.T) {
	cfg := chartConfig(t, render(t, Block{Type: "chart", ChartID: "c", Payload: payload(t,
		`{"variant":"scatter","x":"a","series":["b"],"data":[{"a":1,"b":2},{"a":"n/a","b":3},{"a":4,"b":5}]}`)}))
	pts := cfg["series"].([]any)[0].(map[string]any)["data"].([]any)
	if len(pts) != 2 {
		t.Fatalf("points = %v, want the two with both coordinates", pts)
	}
}

func TestHeatmapIsWeeksByWeekday(t *testing.T) {
	// 2026-01-05 is a Monday, 2026-01-14 a Wednesday of the next week.
	cfg := chartConfig(t, render(t, Block{Type: "chart", ChartID: "c", Payload: payload(t,
		`{"variant":"heatmap","x":"day","series":["n"],"data":[{"day":"2026-01-05","n":2},{"day":"2026-01-14","n":5}]}`)}))
	series := cfg["series"].([]any)
	if len(series) != 7 {
		t.Fatalf("%d series, want one per weekday", len(series))
	}
	byName := map[string][]any{}
	for _, s := range series {
		m := s.(map[string]any)
		byName[m["name"].(string)] = m["data"].([]any)
	}
	mon, wed := byName["Mon"], byName["Wed"]
	if len(mon) != 2 || mon[0].(map[string]any)["y"] != 2.0 || mon[1].(map[string]any)["y"] != nil {
		t.Fatalf("Mon = %v", mon)
	}
	if wed[1].(map[string]any)["y"] != 5.0 || wed[1].(map[string]any)["x"] != "2026-01-12" {
		t.Fatalf("Wed = %v", wed)
	}
}

// An unknown variant still draws its numbers.
func TestUnknownVariantDrawsBars(t *testing.T) {
	cfg := chartConfig(t, render(t, Block{Type: "chart", ChartID: "c", Payload: payload(t,
		`{"variant":"treemapish","x":"k","series":["v"],"data":[{"k":"a","v":1}]}`)}))
	if cfg["chart"].(map[string]any)["type"] != "bar" {
		t.Fatalf("type = %v", cfg["chart"])
	}
}

func TestTableCellsAndTruncation(t *testing.T) {
	out := render(t, Block{Type: "table", DOMID: "block-t", Payload: payload(t,
		`{"title":"Overdue","columns":["order","amount"],"rows":[{"order":"A-1","amount":1240.5},{"order":"<b>x</b>","amount":3}],"truncated":true,"totalRows":812}`)})
	for _, want := range []string{`id="block-t"`, "Overdue", "1240.5", "&lt;b&gt;x&lt;/b&gt;", "Showing 2 of 812 rows.", `class="text-end font-monospace"`} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}

func TestNumericStringsAlignAsNumbers(t *testing.T) {
	rs := rows(payload(t, `{"rows":[{"a":"2210.00","b":"West"},{"a":"75.25","b":"North"}]}`), "rows")
	if !numeric(rs, "a") || numeric(rs, "b") {
		t.Fatalf("numeric(a)=%v numeric(b)=%v", numeric(rs, "a"), numeric(rs, "b"))
	}
}

func TestStatsShowsUnitTrendAndHint(t *testing.T) {
	out := render(t, Block{Type: "stats", Payload: payload(t,
		`{"items":[{"label":"Revenue","value":"48,200","unit":"EUR","delta":0.12,"hint":"vs. H2 2025"},{"label":"Orders","value":"24","delta":-0.05}]}`)})
	for _, want := range []string{"Revenue", "48,200 EUR", "12", "ti-arrow-up", "ti-arrow-down", "vs. H2 2025", "col-lg-6"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats lack %q:\n%s", want, out)
		}
	}
}

// The model's markdown reaches the page unescaped, so the renderer must be
// the one that removes what a page must not run.
func TestMarkdownDropsRawHTMLAndScriptLinks(t *testing.T) {
	out := render(t, Block{Type: "markdown", Payload: payload(t,
		`{"markdown":"**bold** <script>alert(1)</script> [x](javascript:alert(1))\n\n| a | b |\n|---|---|\n| 1 | 2 |"}`)})
	if strings.Contains(out, "<script>") || strings.Contains(out, "javascript:") {
		t.Fatalf("markdown let active content through:\n%s", out)
	}
	if !strings.Contains(out, "<strong>bold</strong>") || !strings.Contains(out, "<table>") {
		t.Fatalf("markdown not rendered:\n%s", out)
	}
}

func TestUnknownBlockShowsItsJSON(t *testing.T) {
	out := render(t, Block{Type: "processMap", Payload: payload(t, `{"nodes":[1,2]}`)})
	if !strings.Contains(out, "Unrecognised block: processMap") || !strings.Contains(out, "&#34;nodes&#34;") {
		t.Fatalf("unknown block:\n%s", out)
	}
}

func TestRevisedBadge(t *testing.T) {
	if out := render(t, Block{Type: "markdown", Revised: true, Payload: payload(t, `{"markdown":"x"}`)}); !strings.Contains(out, "revised") {
		t.Fatalf("no revised badge:\n%s", out)
	}
}

// goja hands the pack int64s and typed slices; the renderer sees JSON's
// shapes whichever arrived.
func TestNormalizeFlattensGojaShapes(t *testing.T) {
	p := Normalize(map[string]any{"rows": []map[string]any{{"n": int64(3)}}, "columns": []string{"n"}})
	rs := rows(p, "rows")
	if len(rs) != 1 || rs[0]["n"] != 3.0 || len(strs(p, "columns")) != 1 {
		t.Fatalf("normalised = %#v", p)
	}
}

func TestAskFromInProcessQuestions(t *testing.T) {
	r, err := AskFromEvent("Pick", map[string]any{
		"requestId": "r1", "inputType": "questions",
		"questions": []AskQuestion{{ID: "period", Header: "Period", Question: "Which?", Options: []sandbox.QuestionOption{{Label: "Q1"}, {Label: "Q2"}}, AllowOther: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Render(context.Background(), Ask(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-ask="r1"`, `data-question="period"`, `value="Q2"`, `data-other="true"`, `name="other-period"`, "Pick"} {
		if !strings.Contains(out, want) {
			t.Errorf("ask form lacks %q:\n%s", want, out)
		}
	}
	ans, _ := Render(context.Background(), Answered(r, map[string]any{"period": "Q2"}))
	if !strings.Contains(ans, "Period:") || !strings.Contains(ans, "Q2") || !strings.Contains(ans, `id="ask-r1"`) {
		t.Fatalf("answered:\n%s", ans)
	}
}
