package ext

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/dop251/goja"

	"github.com/mind-vm/thinking-core/sandbox"
)

// BlocksOptions configures BlocksPack.
type BlocksOptions struct {
	// Guidance replaces the instruction above the declarations — where the
	// numbers in a block must come from, and how the run should end. The
	// default names no data source; an application that has one (a SQL
	// pack, a domain core) says so here, because "never invent a number"
	// lands harder when the prompt names where the real ones live.
	Guidance string
}

const defaultBlocksGuidance = `// Present results as blocks the UI renders, then finish with a short text summary.
// Every number in a block must come from data a script read or computed — never invent one.
// Prefer a chart or a table over a wall of text.`

// BlocksPack lets a script show results as typed render blocks — markdown,
// a table, a chart, a panel of figures — each a sandbox.EventBlock on the
// run's stream with the spec as its Payload and the block type as its
// Summary. agentrun carries it to a client as a sandbox_event; drawing it is
// the client's job, and this package depends on no UI.
//
// The payload shapes are process-mining's (studio-apps), which drew them
// with Mantine first: an application already rendering those blocks can
// switch to this pack without touching its frontend, and keep its domain
// blocks (a process map, a calendar) in a pack of its own.
//
// What it adds over passing the object through is a check, before emitting,
// for the specs a renderer would draw wrong without complaint: a chart whose
// x or series names a column no row has draws an empty plot, a table column
// no row carries draws an empty column, and a figure's numeric value reads
// as no value at all to a renderer that expects the pre-formatted string the
// contract promises. Each of those is a number the agent computed and the
// reader never saw, so it throws instead — one turn for the agent to fix,
// rather than a wrong answer it believes it gave. Numeric figure values are
// the exception: those are converted, since the intent is unambiguous.
//
// Blocks do not end the run. A script can emit several, across turns, and
// the user watches them arrive while the agent is still working.
func BlocksPack(opts BlocksOptions) sandbox.Pack {
	guidance := opts.Guidance
	if guidance == "" {
		guidance = defaultBlocksGuidance
	}
	return sandbox.Pack{
		Name:        "blocks",
		Description: "Show results as render blocks — emitTable, emitChart, emitStats, emitMarkdown",
		Prompt: "// --- Render blocks (rich output for the UI) ---\n" + guidance + `
declare function emitMarkdown(markdown: string): void;
/** rows are objects keyed by the column names. */
declare function emitTable(spec: { title?: string; columns: string[]; rows: Record<string, any>[] }): void;
/** A chart over rows. Common variants: bar (compare across categories),
 * line/area (trend over time), donut/pie (parts of a whole, single series).
 * x = category / x-axis column; series = value column(s); both must be keys of the rows.
 * For the rarer variants and the size/types fields, call help('charts') first. */
declare function emitChart(spec: { title?: string; variant: "bar" | "line" | "area" | "composite" | "donut" | "pie" | "radialBar" | "funnel" | "radar" | "scatter" | "bubble" | "sparkline" | "heatmap"; data: Record<string, any>[]; x: string; series: string[]; size?: string; types?: Record<string, "bar" | "line" | "area"> }): void;
/** Headline figures (KPIs) in a grid. value is the formatted string to show; unit an optional
 * suffix ("%", "EUR"); delta an optional change as a fraction (0.12 => +12%) driving an up/down badge. */
declare function emitStats(spec: { title?: string; cols?: number; items: { label: string; value: string; unit?: string; delta?: number; hint?: string }[] }): void;`,
		HelpEntries: map[string]string{
			"emitMarkdown": "emitMarkdown(md) — show a short markdown block.",
			"emitTable":    "emitTable({columns, rows, title?}) — show a table; rows are objects keyed by the column names.",
			"emitChart":    "emitChart({variant, data, x, series, title?}) — show a chart over rows. Common variants: bar/line/area/donut/pie. See help('charts') for the rest.",
			"emitStats":    "emitStats({items, title?, cols?}) — show a grid of headline figures (KPIs).",
			"charts": `charts — emitChart variants beyond bar/line/area/donut/pie. All take data (rows), x and series like the common ones, plus the noted extra fields:
  composite — mix bar/line/area in one chart. x=category column, series=value columns; types maps a series name to "bar"|"line"|"area" (unmapped series default to bar).
  radar     — one spoke per x value, compare series across them. x=axis-label column, series=value columns.
  scatter   — points on numeric x/y. x=numeric x column, series=[y column].
  bubble    — scatter with sized points. x=x column, series=[y column], size=value column for the point radius.
  radialBar — circular bars, one per category. x=label column, series=[value column].
  funnel    — stage drop-off. x=stage-label column, series=[value column]; pass rows already ordered by descending value.
  sparkline — tiny inline trend, no axes/labels. series=[value column] (x is ignored).
  heatmap   — calendar heatmap. x=date column (YYYY-MM-DD strings), series=[value column].`,
		},
		Register: func(rt *goja.Runtime, sb *sandbox.Sandbox) {
			register := func(name, blockType string, check func(map[string]any) (map[string]any, error)) {
				_ = rt.Set(name, func(call goja.FunctionCall) goja.Value {
					spec, err := blockSpec(call.Argument(0))
					if err == nil {
						spec, err = check(spec)
					}
					if err != nil {
						// One line: the loop keeps only the first line of a
						// script error.
						panic(rt.NewGoError(fmt.Errorf("%s: %w — nothing was shown", name, err)))
					}
					sb.Emit(sandbox.Event{Kind: sandbox.EventBlock, Summary: blockType, Payload: spec})
					return goja.Undefined()
				})
			}
			_ = rt.Set("emitMarkdown", func(call goja.FunctionCall) goja.Value {
				md := call.Argument(0)
				if goja.IsUndefined(md) || goja.IsNull(md) || strings.TrimSpace(md.String()) == "" {
					panic(rt.NewGoError(fmt.Errorf("emitMarkdown: pass the markdown text as a string — nothing was shown")))
				}
				sb.Emit(sandbox.Event{Kind: sandbox.EventBlock, Summary: "markdown", Payload: map[string]any{"markdown": md.String()}})
				return goja.Undefined()
			})
			register("emitTable", "table", checkTable)
			register("emitChart", "chart", checkChart)
			register("emitStats", "stats", checkStats)
		},
	}
}

func blockSpec(v goja.Value) (map[string]any, error) {
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, fmt.Errorf("pass a spec object")
	}
	m, ok := v.Export().(map[string]any)
	if !ok {
		return nil, fmt.Errorf("pass a spec object, not %s", v.ExportType())
	}
	return m, nil
}

func checkTable(spec map[string]any) (map[string]any, error) {
	columns, ok := stringList(spec["columns"])
	if !ok || len(columns) == 0 {
		return nil, fmt.Errorf("columns must be a non-empty array of column names")
	}
	rows, ok := objectList(spec["rows"])
	if !ok {
		return nil, fmt.Errorf("rows must be an array of objects keyed by the column names")
	}
	if len(rows) > 0 {
		have := keysOf(rows)
		for _, c := range columns {
			if !have[c] {
				return nil, fmt.Errorf("column %q is a key of no row, so it would show empty; the rows have %s", c, listKeys(have))
			}
		}
	}
	return spec, nil
}

func checkChart(spec map[string]any) (map[string]any, error) {
	data, ok := objectList(spec["data"])
	if !ok || len(data) == 0 {
		return nil, fmt.Errorf("data must be a non-empty array of row objects")
	}
	series, ok := stringList(spec["series"])
	if !ok || len(series) == 0 {
		return nil, fmt.Errorf("series must be a non-empty array of value column names")
	}
	have := keysOf(data)
	x, _ := spec["x"].(string)
	// sparkline ignores x; every other variant plots against it.
	if spec["variant"] != "sparkline" {
		if x == "" {
			return nil, fmt.Errorf("x must name the category / x-axis column; the rows have %s", listKeys(have))
		}
		if !have[x] {
			return nil, fmt.Errorf("x %q is a key of no row, so the chart would draw empty; the rows have %s", x, listKeys(have))
		}
	}
	for _, s := range series {
		if !have[s] {
			return nil, fmt.Errorf("series %q is a key of no row, so the chart would draw empty; the rows have %s", s, listKeys(have))
		}
	}
	return spec, nil
}

func checkStats(spec map[string]any) (map[string]any, error) {
	items, ok := objectList(spec["items"])
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("items must be a non-empty array of {label, value}")
	}
	out := make([]any, len(items))
	for i, it := range items {
		label, _ := it["label"].(string)
		if label == "" {
			return nil, fmt.Errorf("items[%d] has no label", i)
		}
		switch v := it["value"].(type) {
		case string:
		case int64:
			it["value"] = strconv.FormatInt(v, 10)
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("items[%d] (%q) has value %v — compute a real number first", i, label, v)
			}
			it["value"] = strconv.FormatFloat(v, 'f', -1, 64)
		default:
			return nil, fmt.Errorf("items[%d] (%q) needs value as a formatted string, e.g. \"1,240.50\"", i, label)
		}
		out[i] = it
	}
	spec["items"] = out
	return spec, nil
}

// stringList and objectList accept the shapes goja's Export produces for a
// JS array — []any, or a typed slice when every element shares a type.
func stringList(v any) ([]string, bool) {
	switch l := v.(type) {
	case []string:
		return l, true
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

func objectList(v any) ([]map[string]any, bool) {
	switch l := v.(type) {
	case []map[string]any:
		return l, true
	case []any:
		out := make([]map[string]any, 0, len(l))
		for _, e := range l {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	}
	return nil, false
}

func keysOf(rows []map[string]any) map[string]bool {
	have := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			have[k] = true
		}
	}
	return have
}

func listKeys(have map[string]bool) string {
	keys := make([]string, 0, len(have))
	for k := range have {
		keys = append(keys, strconv.Quote(k))
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return "no keys at all"
	}
	return strings.Join(keys, ", ")
}
