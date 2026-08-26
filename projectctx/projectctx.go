// Package projectctx discovers project-level instruction files —
// AGENTS.md and friends — from a checkout on disk, and renders them
// into a section an application can fold into an agentloop run's
// system prompt.
//
// This is a layered capability, not core loop behaviour: agentloop
// works fine without it, nothing in the core imports it, and an
// application opts in by calling Load and passing the rendered result
// as RunRequest.Context:
//
//	docs, err := projectctx.Load(cwd)  // err is advisory; docs may be usable
//	result, err := loop.Run(ctx, agentloop.RunRequest{
//	    SessionID: id,
//	    Message:   msg,
//	    Context:   projectctx.Render(docs),
//	})
//
// Render returns "" for no docs and the loop skips an empty Context, so
// the call is safe to make unconditionally.
//
// "Project" here means a checkout on disk — a directory tree with a
// repository root. It is unrelated to agentloop.Scope.ProjectID, which
// is a tenant boundary; a single Scope may span many checkouts and a
// checkout knows nothing about scopes.
package projectctx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultFileName is the instruction file looked for when
// Loader.FileName is empty. AGENTS.md is the emerging cross-tool
// convention for agent-readable project guidance.
const DefaultFileName = "AGENTS.md"

// DefaultMaxBytes caps a single instruction file's contribution when
// Loader.MaxBytes is zero. These files land in every prompt of every
// run, so an oversized one is a standing cost rather than a one-off:
// past the cap the head is kept and the rest replaced by a marker.
const DefaultMaxBytes = 64 * 1024

// Doc is one discovered instruction file.
type Doc struct {
	// Path is where it was read from, as an absolute path. For the
	// application's own logging and diagnostics.
	Path string

	// Name is how the file is identified to the model: its path
	// relative to the repository root, or the bare file name for a
	// global doc. Render uses this rather than Path so the host's
	// directory layout stays out of the prompt.
	Name string

	// Content is the trimmed file body, truncated at Loader.MaxBytes.
	Content string
}

// Loader discovers instruction files. The zero value is usable and
// looks for AGENTS.md files from the repository root down to the
// working directory.
type Loader struct {
	// FileName is the instruction file to look for. Empty uses
	// DefaultFileName.
	FileName string

	// GlobalDir is an optional directory holding a user-global
	// FileName, loaded ahead of (and so overridden by) the repository's
	// own files.
	//
	// Empty means no global file is read at all. That is deliberate: a
	// library reaching into a user's home directory by default would
	// surprise the applications embedding it, so a CLI that wants the
	// convention opts in explicitly — GlobalDir:
	// filepath.Join(home, ".myagent").
	GlobalDir string

	// MaxBytes caps each file's contribution. Zero uses
	// DefaultMaxBytes; negative disables truncation.
	MaxBytes int
}

// Load discovers instruction files for a session rooted at cwd with the
// default Loader. See Loader.Load.
func Load(cwd string) ([]Doc, error) { return Loader{}.Load(cwd) }

// Load discovers instruction files for a session rooted at cwd,
// returned in INCREASING priority — earlier is more general, later more
// specific, so a caller that renders them in order lets the closest
// file have the last word:
//
//   - GlobalDir/FileName, when GlobalDir is set
//   - FileName from the repository root down to cwd, outermost first
//
// The upward walk stops at the repository root, or at cwd when there is
// no repository. Repository files must resolve within that root even
// after symlinks, and are read through an os.Root so a target swapped
// between the check and the read still cannot escape. Missing and empty
// files are skipped silently.
//
// The error is advisory: any file that loaded cleanly is still
// returned alongside it, because one unreadable AGENTS.md in a
// subdirectory is a reason to warn, not a reason to deny the run its
// remaining project context. Callers that want strictness can treat a
// non-nil error as fatal themselves.
func (l Loader) Load(cwd string) ([]Doc, error) {
	fileName := l.FileName
	if fileName == "" {
		fileName = DefaultFileName
	}

	var docs []Doc
	var loadErrs []error

	// User-global, lowest priority. Read directly rather than through a
	// root: it lives outside the project by definition, and it is the
	// user's own file, explicitly opted into by setting GlobalDir.
	if l.GlobalDir != "" {
		path := filepath.Join(l.GlobalDir, fileName)
		if doc, ok, err := l.readDoc(path, fileName); err != nil {
			loadErrs = append(loadErrs, err)
		} else if ok {
			docs = append(docs, doc)
		}
	}

	root := Root(cwd)
	dirs := Ancestors(cwd)
	// Ancestors runs cwd-first and may continue past the root when cwd
	// is not inside a repository at all; trim anything above it.
	for len(dirs) > 0 && filepath.Clean(dirs[len(dirs)-1]) != filepath.Clean(root) {
		dirs = dirs[:len(dirs)-1]
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return docs, errors.Join(append(loadErrs, fmt.Errorf("projectctx: resolve project root %s: %w", root, err))...)
	}
	projectRoot, err := os.OpenRoot(realRoot)
	if err != nil {
		return docs, errors.Join(append(loadErrs, fmt.Errorf("projectctx: open project root %s: %w", root, err))...)
	}
	defer func() { _ = projectRoot.Close() }()

	// Outermost first, so the file closest to cwd ends up last and wins.
	for i := len(dirs) - 1; i >= 0; i-- {
		path := filepath.Join(dirs[i], fileName)
		resolved, err := ResolveWithin(realRoot, path)
		if err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("projectctx: validate %s: %w", path, err))
			continue
		}
		name, err := filepath.Rel(realRoot, resolved)
		if err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("projectctx: locate %s within %s: %w", path, realRoot, err))
			continue
		}
		doc, ok, err := l.readRooted(projectRoot, name, path)
		if err != nil {
			loadErrs = append(loadErrs, err)
			continue
		}
		if ok {
			docs = append(docs, doc)
		}
	}
	return docs, errors.Join(loadErrs...)
}

// readRooted reads a validated target through an os.Root. ResolveWithin
// already picked the target; the rooted read closes the gap where
// replacing that target between check and read could otherwise escape.
func (l Loader) readRooted(root *os.Root, name, sourcePath string) (Doc, bool, error) {
	b, err := root.ReadFile(name)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, false, nil
		}
		return Doc{}, false, fmt.Errorf("projectctx: read %s within %s: %w", sourcePath, root.Name(), err)
	}
	doc, ok := l.makeDoc(sourcePath, filepath.ToSlash(name), b)
	return doc, ok, nil
}

func (l Loader) readDoc(path, name string) (Doc, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, false, nil
		}
		return Doc{}, false, fmt.Errorf("projectctx: read %s: %w", path, err)
	}
	doc, ok := l.makeDoc(path, name, b)
	return doc, ok, nil
}

// makeDoc trims and truncates a file body. A whitespace-only file is
// not a doc — reporting it as one would put an empty heading in every
// prompt — so it comes back ok=false, indistinguishable from absent.
func (l Loader) makeDoc(path, name string, b []byte) (Doc, bool) {
	content := strings.TrimSpace(string(b))
	if content == "" {
		return Doc{}, false
	}
	if max := l.maxBytes(); max > 0 && len(content) > max {
		content = strings.TrimSpace(content[:max]) +
			fmt.Sprintf("\n\n[... truncated at %d bytes ...]", max)
	}
	return Doc{Path: path, Name: name, Content: content}, true
}

func (l Loader) maxBytes() int {
	if l.MaxBytes == 0 {
		return DefaultMaxBytes
	}
	return l.MaxBytes // negative disables truncation
}

// Render composes docs into the prompt section an application passes as
// agentloop.RunRequest.Context, each file under its own heading. It
// returns "" for no docs — and the loop skips an empty Context — so
// callers can apply it unconditionally.
//
// The heading level matches the sections agentloop's own system prompt
// uses, so project instructions read as one more section of it rather
// than as a document pasted onto the end.
func Render(docs []Doc) string {
	docs = nonEmpty(docs)
	if len(docs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Project instructions\n\n")
	b.WriteString("The following come from instruction files in this project. ")
	b.WriteString("Treat them as authoritative guidance for work in this repository. ")
	b.WriteString("Where two files disagree, the later one is more specific and wins.\n")
	for _, d := range docs {
		b.WriteString("\n### ")
		b.WriteString(d.Name)
		b.WriteString("\n\n")
		b.WriteString(d.Content)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// nonEmpty drops zero-value docs, so a caller that ignored Load's
// advisory error and got a partially populated slice still renders
// cleanly.
func nonEmpty(docs []Doc) []Doc {
	out := make([]Doc, 0, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.Content) != "" {
			out = append(out, d)
		}
	}
	return out
}
