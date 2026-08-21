package sandbox

import (
	"bytes"
	"strings"

	"github.com/dop251/goja"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownModulePack creates a built-in module available via require('markdown').
// It provides Go-backed markdown parsing functions for extracting structured data
// from markdown text without LLM calls.
func MarkdownModulePack() Pack {
	return Pack{
		Name:        "markdown",
		Description: "Parse markdown into structured data (links, headings, sections)",
		Prompt: `/** Parse markdown into structured data. No LLM call needed. */
declare module 'markdown' {
  interface Link { text: string; url: string }
  interface Heading { text: string; level: number }
  interface ListItem { text: string; links: Link[] }
  interface Section { heading: string; level: number; content: string; links: Link[] }

  /** Extract all links from markdown. */
  function links(md: string): Link[];
  /** Extract all headings from markdown. */
  function headings(md: string): Heading[];
  /** Extract all list items from markdown. */
  function items(md: string): ListItem[];
  /** Split markdown into sections by heading. Each section includes its content and links. */
  function sections(md: string): Section[];
  /** Strip markdown formatting and return plain text. */
  function plain(md: string): string;
}`,
		HelpEntries: map[string]string{
			"markdown": `require('markdown') — Parse markdown into structured data.
  var m = require('markdown');
  m.links(md) — Extract all links as [{text, url}]
  m.headings(md) — Extract all headings as [{text, level}]
  m.items(md) — Extract all list items as [{text, links}]
  m.sections(md) — Split by heading into [{heading, level, content, links}]
  m.plain(md) — Strip formatting, return plain text`,
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			// Register Go-backed parsing functions
			_ = rt.Set("_md_links", func(call goja.FunctionCall) goja.Value {
				md := call.Argument(0).String()
				return rt.ToValue(mdLinks([]byte(md)))
			})

			_ = rt.Set("_md_headings", func(call goja.FunctionCall) goja.Value {
				md := call.Argument(0).String()
				return rt.ToValue(mdHeadings([]byte(md)))
			})

			_ = rt.Set("_md_items", func(call goja.FunctionCall) goja.Value {
				md := call.Argument(0).String()
				return rt.ToValue(mdItems([]byte(md)))
			})

			_ = rt.Set("_md_sections", func(call goja.FunctionCall) goja.Value {
				md := call.Argument(0).String()
				return rt.ToValue(mdSections([]byte(md)))
			})

			_ = rt.Set("_md_plain", func(call goja.FunctionCall) goja.Value {
				md := call.Argument(0).String()
				return rt.ToValue(mdPlain([]byte(md)))
			})

			// Register JS wrapper module for require('markdown')
			s.AddSkillCode("markdown", markdownModuleJS)
		},
	}
}

var markdownModuleJS = strings.TrimSpace(`
exports.links = function(md) { return _md_links(md); };
exports.headings = function(md) { return _md_headings(md); };
exports.items = function(md) { return _md_items(md); };
exports.sections = function(md) { return _md_sections(md); };
exports.plain = function(md) { return _md_plain(md); };
`)

// parseMarkdown parses markdown source into a goldmark AST.
func parseMarkdown(source []byte) ast.Node {
	md := goldmark.New()
	reader := text.NewReader(source)
	return md.Parser().Parse(reader)
}

// nodeText extracts the text content of an AST node and its children.
func nodeText(n ast.Node, source []byte) string {
	var buf bytes.Buffer
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if c.Kind() == ast.KindText {
			segment := c.(*ast.Text).Segment
			buf.Write(segment.Value(source))
		} else if c.Kind() == ast.KindCodeSpan {
			// Extract text from code spans
			for cc := c.FirstChild(); cc != nil; cc = cc.NextSibling() {
				if cc.Kind() == ast.KindText {
					segment := cc.(*ast.Text).Segment
					buf.Write(segment.Value(source))
				}
			}
		} else {
			// Recurse into other inline nodes
			buf.WriteString(nodeText(c, source))
		}
	}
	return buf.String()
}

func mdLinks(source []byte) []map[string]any {
	doc := parseMarkdown(source)
	var links []map[string]any

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			links = append(links, map[string]any{
				"text": nodeText(v, source),
				"url":  string(v.Destination),
			})
			return ast.WalkSkipChildren, nil
		case *ast.AutoLink:
			url := string(v.URL(source))
			links = append(links, map[string]any{
				"text": url,
				"url":  url,
			})
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})

	if links == nil {
		links = []map[string]any{}
	}
	return links
}

func mdHeadings(source []byte) []map[string]any {
	doc := parseMarkdown(source)
	var headings []map[string]any

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if h, ok := n.(*ast.Heading); ok {
			headings = append(headings, map[string]any{
				"text":  nodeText(h, source),
				"level": h.Level,
			})
		}
		return ast.WalkContinue, nil
	})

	if headings == nil {
		headings = []map[string]any{}
	}
	return headings
}

func mdItems(source []byte) []map[string]any {
	doc := parseMarkdown(source)
	var items []map[string]any

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if li, ok := n.(*ast.ListItem); ok {
			// Extract text and links from the list item
			var itemLinks []map[string]any
			itemText := nodeText(li, source)

			_ = ast.Walk(li, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering {
					return ast.WalkContinue, nil
				}
				if link, ok := child.(*ast.Link); ok {
					itemLinks = append(itemLinks, map[string]any{
						"text": nodeText(link, source),
						"url":  string(link.Destination),
					})
				}
				return ast.WalkContinue, nil
			})

			if itemLinks == nil {
				itemLinks = []map[string]any{}
			}
			items = append(items, map[string]any{
				"text":  strings.TrimSpace(itemText),
				"links": itemLinks,
			})
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})

	if items == nil {
		items = []map[string]any{}
	}
	return items
}

func mdSections(source []byte) []map[string]any {
	doc := parseMarkdown(source)
	var sections []map[string]any

	type sectionBuilder struct {
		heading string
		level   int
		start   int // byte offset where content starts (after heading)
		links   []map[string]any
	}

	var current *sectionBuilder
	var lastEnd int // end byte offset of the previous node

	// Collect heading positions and content between them
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		if h, ok := child.(*ast.Heading); ok {
			// Flush the current section
			if current != nil {
				content := strings.TrimSpace(string(source[current.start:nodeStartOffset(child, source)]))
				if current.links == nil {
					current.links = []map[string]any{}
				}
				sections = append(sections, map[string]any{
					"heading": current.heading,
					"level":   current.level,
					"content": content,
					"links":   current.links,
				})
			}

			current = &sectionBuilder{
				heading: nodeText(h, source),
				level:   h.Level,
				start:   nodeEndOffset(h, source),
			}
			// Collect links within the section as we go
		} else if current != nil {
			// Collect links from non-heading blocks
			_ = ast.Walk(child, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering {
					return ast.WalkContinue, nil
				}
				if link, ok := n.(*ast.Link); ok {
					current.links = append(current.links, map[string]any{
						"text": nodeText(link, source),
						"url":  string(link.Destination),
					})
				}
				return ast.WalkContinue, nil
			})
		}
		lastEnd = nodeEndOffset(child, source)
	}
	_ = lastEnd

	// Flush final section
	if current != nil {
		content := strings.TrimSpace(string(source[current.start:]))
		if current.links == nil {
			current.links = []map[string]any{}
		}
		sections = append(sections, map[string]any{
			"heading": current.heading,
			"level":   current.level,
			"content": content,
			"links":   current.links,
		})
	}

	if sections == nil {
		sections = []map[string]any{}
	}
	return sections
}

func mdPlain(source []byte) string {
	doc := parseMarkdown(source)
	var buf bytes.Buffer

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Text:
			buf.Write(v.Segment.Value(source))
			if v.SoftLineBreak() || v.HardLineBreak() {
				buf.WriteByte('\n')
			}
		case *ast.CodeSpan:
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					buf.Write(t.Segment.Value(source))
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.Paragraph:
			if buf.Len() > 0 && buf.Bytes()[buf.Len()-1] != '\n' {
				buf.WriteByte('\n')
			}
		case *ast.Heading:
			if buf.Len() > 0 && buf.Bytes()[buf.Len()-1] != '\n' {
				buf.WriteByte('\n')
			}
		case *ast.ListItem:
			// no-op, text nodes handle content
		case *ast.FencedCodeBlock:
			lines := v.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				buf.Write(seg.Value(source))
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			lines := v.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				buf.Write(seg.Value(source))
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})

	return strings.TrimSpace(buf.String())
}

// nodeStartOffset returns the byte offset where a block node starts in the source.
func nodeStartOffset(n ast.Node, source []byte) int {
	if n.Lines().Len() > 0 {
		return n.Lines().At(0).Start
	}
	// For headings and other nodes, check first child
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			return t.Segment.Start
		}
	}
	return 0
}

// nodeEndOffset returns the byte offset where a block node ends in the source.
func nodeEndOffset(n ast.Node, source []byte) int {
	lines := n.Lines()
	if lines.Len() > 0 {
		return lines.At(lines.Len() - 1).Stop
	}
	// Walk to find the last text segment
	var end int
	_ = ast.Walk(n, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if t, ok := child.(*ast.Text); ok {
			if t.Segment.Stop > end {
				end = t.Segment.Stop
			}
		}
		return ast.WalkContinue, nil
	})
	return end
}
