package ext

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/mind-vm/agentloop/sandbox"
)

// --- reading --------------------------------------------------------

func (w *workspace) readFile(name string, opts map[string]any) string {
	rel, err := w.rel(name)
	if err != nil {
		w.throw("readFile: %v", err)
	}

	info, err := w.root.Stat(rel)
	if err != nil {
		w.throw("readFile: %v", err)
	}
	if info.IsDir() {
		w.throw("readFile: %s is a directory — use listDir", rel)
	}

	data, err := w.root.ReadFile(rel)
	if err != nil {
		w.throw("readFile: %v", err)
	}

	text := string(data)
	truncated := false
	if max := w.opts.maxFileBytes(); int64(len(data)) > max {
		// Cut on a line boundary so the last line handed back is whole —
		// a half-line of code reads as real and is not.
		cut := int(max)
		if nl := strings.LastIndexByte(text[:cut], '\n'); nl > 0 {
			cut = nl + 1
		}
		text = text[:cut]
		truncated = true
	}

	offset, limit := optInt(opts, "offset"), optInt(opts, "limit")
	if offset > 0 || limit > 0 {
		text = sliceLines(text, offset, limit)
	}
	if truncated {
		text += fmt.Sprintf("\n[truncated: the file is %d bytes; pass {offset, limit} to read further]\n", len(data))
	}

	w.sb.Emit(sandbox.Event{Kind: sandbox.EventFileRead, Summary: "readFile: " + rel})
	return text
}

// sliceLines takes limit lines starting at the 1-based line offset.
// An offset past the end yields nothing rather than an error: "there is
// no line 900" is a fact about the file, not a mistake by the caller.
func sliceLines(text string, offset, limit int) string {
	lines := strings.Split(text, "\n")
	start := 0
	if offset > 0 {
		start = offset - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}

func (w *workspace) listDir(name string) []map[string]any {
	rel, err := w.rel(name)
	if err != nil {
		w.throw("listDir: %v", err)
	}

	entries, err := fs.ReadDir(w.root.FS(), rel)
	if err != nil {
		w.throw("listDir: %v", err)
	}

	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && slices.Contains(w.opts.skipDirs(), e.Name()) {
			continue
		}
		if len(out) >= w.opts.maxResults() {
			break
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		out = append(out, map[string]any{
			"name": e.Name(),
			"path": joinRel(rel, e.Name()),
			"dir":  e.IsDir(),
			"size": size,
		})
	}

	w.sb.Emit(sandbox.Event{Kind: sandbox.EventFileRead, Summary: fmt.Sprintf("listDir: %s (%d entries)", rel, len(out))})
	return out
}

func joinRel(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}

// --- mutation -------------------------------------------------------

func (w *workspace) writeFile(name string, content string) bool {
	rel, err := w.rel(name)
	if err != nil {
		w.throw("writeFile: %v", err)
	}
	if info, err := w.root.Stat(rel); err == nil && info.IsDir() {
		w.throw("writeFile: %s is a directory", rel)
	}

	w.authorize("writeFile", rel)

	if dir := path.Dir(rel); dir != "." && dir != "/" {
		if err := w.root.MkdirAll(dir, 0o755); err != nil {
			w.throw("writeFile: %v", err)
		}
	}
	if err := w.root.WriteFile(rel, []byte(content), 0o644); err != nil {
		w.throw("writeFile: %v", err)
	}

	w.sb.Emit(sandbox.Event{
		Kind:    sandbox.EventFileWrite,
		Summary: fmt.Sprintf("writeFile: %s (%d bytes)", rel, len(content)),
	})
	return true
}

func (w *workspace) editFile(name, find, replace string, opts map[string]any) int {
	rel, err := w.rel(name)
	if err != nil {
		w.throw("editFile: %v", err)
	}
	if find == "" {
		w.throw("editFile: the text to find must not be empty")
	}

	data, err := w.root.ReadFile(rel)
	if err != nil {
		w.throw("editFile: %v", err)
	}
	text := string(data)

	// Count before asking permission and before writing. A find that
	// matches nothing, or matches in more places than the caller
	// realised, is a mistaken edit — refusing it costs one turn, while
	// making it costs a wrong file the agent believes is right.
	all := optBool(opts, "all")
	count := strings.Count(text, find)
	switch {
	case count == 0:
		w.throw("editFile: %s does not contain that text", rel)
	case count > 1 && !all:
		w.throw("editFile: that text appears %d times in %s — include more surrounding context to identify one, or pass {all: true}", count, rel)
	}

	w.authorize("editFile", rel)

	replaced := 1
	if all {
		replaced = count
		text = strings.ReplaceAll(text, find, replace)
	} else {
		text = strings.Replace(text, find, replace, 1)
	}

	// Preserve the file's own mode; an edit should not quietly make a
	// script non-executable.
	perm := os.FileMode(0o644)
	if info, err := w.root.Stat(rel); err == nil {
		perm = info.Mode().Perm()
	}
	if err := w.root.WriteFile(rel, []byte(text), perm); err != nil {
		w.throw("editFile: %v", err)
	}

	w.sb.Emit(sandbox.Event{
		Kind:    sandbox.EventFileWrite,
		Summary: fmt.Sprintf("editFile: %s (%d replacement(s))", rel, replaced),
	})
	return replaced
}

// --- searching ------------------------------------------------------

func (w *workspace) globFiles(pattern string) []string {
	if strings.TrimSpace(pattern) == "" {
		w.throw("glob: a pattern is required")
	}
	matches, err := w.walk(pattern, func(string) bool { return true })
	if err != nil {
		w.throw("glob: %v", err)
	}
	w.sb.Emit(sandbox.Event{Kind: sandbox.EventFileRead, Summary: fmt.Sprintf("glob %s → %d file(s)", pattern, len(matches))})
	return matches
}

func (w *workspace) grep(pattern string, opts map[string]any) []map[string]any {
	if strings.TrimSpace(pattern) == "" {
		w.throw("grep: a pattern is required")
	}
	if optBool(opts, "ignoreCase") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		w.throw("grep: %v", err)
	}

	limit := optInt(opts, "limit")
	if limit <= 0 || limit > w.opts.maxResults() {
		limit = w.opts.maxResults()
	}
	fileGlob := optString(opts, "glob")
	if strings.TrimSpace(fileGlob) == "" {
		fileGlob = "**"
	}

	hits := make([]map[string]any, 0, 16)
	_, err = w.walk(fileGlob, func(rel string) bool {
		if len(hits) >= limit {
			return false
		}
		data, err := w.root.ReadFile(rel)
		if err != nil || int64(len(data)) > w.opts.maxFileBytes() || isBinary(data) {
			return true
		}
		for i, line := range strings.Split(string(data), "\n") {
			if len(hits) >= limit {
				return false
			}
			if re.MatchString(line) {
				hits = append(hits, map[string]any{
					"path": rel,
					"line": i + 1,
					"text": strings.TrimRight(line, "\r"),
				})
			}
		}
		return true
	})
	if err != nil {
		w.throw("grep: %v", err)
	}

	w.sb.Emit(sandbox.Event{Kind: sandbox.EventFileRead, Summary: fmt.Sprintf("grep %s → %d match(es)", pattern, len(hits))})
	return hits
}

// isBinary reports whether data looks like something a text search
// should skip. A NUL byte in the first few KB is the same heuristic
// grep itself uses, and it is right far more often than reading a
// megabyte of object code into the context window.
func isBinary(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// walk visits every file matching pattern, calling visit with each
// path; visit returns false to stop early. It returns the matched paths.
//
// It walks the root's own fs.FS, so the traversal is confined by the
// same mechanism every other primitive here relies on.
func (w *workspace) walk(pattern string, visit func(rel string) bool) ([]string, error) {
	var (
		// Initialised, not nil: a nil slice crosses into JS as null, and
		// `glob(...).length` on a pattern that matched nothing should be
		// 0 rather than a TypeError.
		matches = []string{}
		seen    int
		skip    = w.opts.skipDirs()
		maxHits = w.opts.maxResults()
		maxWalk = w.opts.maxWalkFiles()
	)

	err := fs.WalkDir(w.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory should not abandon the search.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != "." && slices.Contains(skip, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}

		seen++
		if seen > maxWalk {
			return fmt.Errorf("gave up after %d files — narrow the pattern", maxWalk)
		}
		ok, err := matchPath(pattern, p)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if len(matches) < maxHits {
			matches = append(matches, p)
		}
		if !visit(p) {
			return fs.SkipAll
		}
		if len(matches) >= maxHits {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// matchPath reports whether a slash-separated path matches pattern,
// where "**" spans any number of path segments and "*" stays within
// one. path.Match alone cannot express the first, and "**/*.go" is the
// pattern anyone actually reaches for.
func matchPath(pattern, name string) (bool, error) {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, seg []string) (bool, error) {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Zero or more segments: try every split point. Patterns are
			// short, so the branching is bounded in practice.
			for i := 0; i <= len(seg); i++ {
				ok, err := matchSegments(pat[1:], seg[i:])
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(seg) == 0 {
			return false, nil
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil {
			return false, fmt.Errorf("bad pattern %q: %w", pattern(pat), err)
		}
		if !ok {
			return false, nil
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0, nil
}

func pattern(segments []string) string { return strings.Join(segments, "/") }

// --- option reading -------------------------------------------------
//
// Options arrive as a plain map rather than through a Go struct because
// goja maps JS object keys onto EXPORTED Go field names, not json tags,
// unless the runtime installs a field-name mapper. Installing one would
// change how every other pack's values cross the boundary. Reading the
// keys here keeps the documented JS interface — lowercase names — true
// without reaching into shared runtime configuration.

func optString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func optBool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}

// optInt accepts every numeric shape a JS number can arrive as. goja
// exports an integral Number as int64 and a fractional one as float64,
// and a caller may well pass "40" as a string.
func optInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
