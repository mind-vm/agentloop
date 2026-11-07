// Package view draws ext.BlocksPack's render blocks — and the rest of a run
// a person watches — as Tabler HTML on the server, with tabler-templ.
//
// It is the second of two ways to draw the same blocks. examples/blocks ships
// the JSON payloads to a React page that draws them with Mantine; this one
// renders each block to an HTML fragment the moment it is emitted and sends
// that, so the browser needs Tabler's CSS and JS from a CDN and a hundred
// lines of script, and no build. The payload contract is the same: an
// application can switch renderers without touching the pack or the agent.
package view

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/a-h/templ"
	"github.com/mind-vm/tabler-templ/chart"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Block is one render block as the page draws it.
type Block struct {
	Type    string         // the sandbox.Event Summary: markdown, table, chart, stats
	Payload map[string]any // the spec, normalised by Normalize
	// DOMID is the element the block occupies. A block emitted with an id
	// keeps the same DOMID across emits, which is how the page finds the
	// copy to replace.
	DOMID string
	// ChartID names the chart element. It is fresh on every emit, unlike
	// DOMID: the replaced copy's chart is still being torn down when the
	// new one starts, and two charts under one ApexCharts id fight.
	ChartID string
	// Revised marks a block that replaces one from an earlier turn, so the
	// reader knows the figure up the page they remember is gone.
	Revised bool
}

// Normalize turns a payload into the shape a remote client would see —
// JSON's float64, []any and map[string]any. In process the payload is what
// goja exported: int64 here, []map[string]any there, a typed slice when
// every element shares a type. Rendering one shape is simpler than
// anticipating every one goja can produce.
func Normalize(payload any) map[string]any {
	b, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// Render writes a component to a string as a fragment of an already loaded
// page.
//
// chart.Chart renders its start-up script once per render, and a fragment is
// a render of its own, so every streamed chart would carry the whole script
// again — inert after innerHTML, but kilobytes per block. Rendering the
// script into the void first, under the same context, uses up the once.
func Render(ctx context.Context, c templ.Component) (string, error) {
	ctx = templ.InitializeContext(ctx)
	if err := chart.Script().Render(ctx, io.Discard); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := c.Render(ctx, &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// md renders the agent's markdown. goldmark without WithUnsafe drops raw
// HTML and neutralises javascript: links, which is what makes it safe to
// put the model's text into the page unescaped.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func markdownHTML(s string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(s), &buf); err != nil {
		return templ.EscapeString(s)
	}
	return buf.String()
}

func str(p map[string]any, k string) string {
	s, _ := p[k].(string)
	return s
}

func strs(p map[string]any, k string) []string {
	l, _ := p[k].([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func rows(p map[string]any, k string) []map[string]any {
	l, _ := p[k].([]any)
	out := make([]map[string]any, 0, len(l))
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// numeric reports a column of numbers, right-aligned in a table. Numeric
// strings count, as they do for charts: a model that parses CSV in a script
// usually leaves "2210.00" a string, and it is still an amount.
func numeric(rs []map[string]any, col string) bool {
	seen := false
	for _, r := range rs {
		if r[col] == nil {
			continue
		}
		if _, ok := number(r[col]); !ok {
			return false
		}
		seen = true
	}
	return seen
}

func pretty(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

func cellText(v any) string {
	switch v.(type) {
	case map[string]any, []any:
		b, _ := json.Marshal(v)
		return string(b)
	}
	return cell(v)
}

// joinAnswer is an answer as one line: a multi-select's labels comma-joined.
func joinAnswer(v any) string {
	switch a := v.(type) {
	case []any:
		parts := make([]string, 0, len(a))
		for _, e := range a {
			parts = append(parts, cell(e))
		}
		return strings.Join(parts, ", ")
	case bool:
		if a {
			return "Yes"
		}
		return "No"
	}
	return cell(v)
}
