// Package skills discovers SKILL.md files in a project and exposes each
// one to an agentloop run as a retrievable document.
//
// The model learns a skill EXISTS from a one-line entry in the system
// prompt and from skillList(); it pulls the full instructions with
// skillGet(name) when it decides the skill applies. Bodies never sit in
// the prompt otherwise, which is the point — a project can carry a
// dozen detailed skills without any of them costing context until one
// is actually used.
//
// This differs from the CLI convention the package was ported from,
// where a skill is expanded into the turn only when the USER types
// $name or /name. Here the agent retrieves its own skills mid-run,
// which fits a loop whose whole design is pulling detail on demand
// rather than front-loading it.
//
// Like projectctx, this is layered: nothing in the core imports it. An
// application opts in by loading skills and adding the packs to its
// SandboxBuilder.
//
//	sk, err := skills.Load(cwd) // err is advisory; sk may be usable
//	caps := append(agentloop.DefaultCapabilities(client, ""), skills.Capabilities(sk)...)
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/projectctx"
	"github.com/mind-vm/agentloop/sandbox"
)

// FileName is the file that defines a skill inside its directory.
const FileName = "SKILL.md"

// DefaultDir is the project-relative directory searched for skill
// directories when Loader.Dir is empty.
const DefaultDir = ".agentloop/skills"

// DefaultMaxBytes caps one skill body when Loader.MaxBytes is zero.
// A body is only ever fetched on demand, so this is generous compared
// with projectctx's cap on files that ride along in every prompt.
const DefaultMaxBytes = 256 * 1024

// Skill is one discovered SKILL.md.
type Skill struct {
	// Name is the retrieval name: skillGet(Name). Lowercased, and
	// restricted to characters that read cleanly as an identifier.
	Name string

	// Description is the one-line summary shown in the prompt catalog
	// and in skillList(). Falls back to the body's opening line when
	// the frontmatter omits it.
	Description string

	// Body is the full instruction text, minus frontmatter.
	Body string

	// Path is the absolute source path, for the application's logs.
	Path string
}

// reserved are names a skill may not take, because the sandbox already
// binds them. The danger is not just a confusing skillList(): pack help
// entries are merged by name, so a skill called "fetch" would REPLACE
// the fetch primitive's documentation with its own, and a skill called
// "http" would collide with the module require() resolves by that name.
//
// This mirrors sandbox's built-in packs and core primitives. It is a
// static list rather than a query because skills load before a sandbox
// exists — worth re-checking when the built-in surface changes.
var reserved = map[string]bool{
	"ai": true, "aiJSON": true, "answer": true, "assert": true,
	"console": true, "fetch": true, "help": true, "htmlToMarkdown": true,
	"http": true, "log": true, "markdown": true, "parseUrl": true,
	"print": true, "require": true, "skillGet": true, "skillList": true,
	"skills": true, "text": true,
}

// validName keeps a skill name usable as an identifier the model types
// back verbatim into skillGet("...").
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Loader discovers skills. The zero value is usable and searches
// <project root>/.agentloop/skills.
type Loader struct {
	// Dir is the skills directory, relative to the project root. Empty
	// uses DefaultDir. An absolute path is used as-is, for an
	// application that keeps skills outside the checkout.
	Dir string

	// GlobalDir is an optional user-global skills directory, loaded
	// first so a project skill of the same name overrides it. Empty
	// means none — same reasoning as projectctx.Loader.GlobalDir: a
	// library should not read $HOME unless asked.
	GlobalDir string

	// MaxBytes caps one skill body. Zero uses DefaultMaxBytes;
	// negative disables truncation.
	MaxBytes int
}

// Load discovers skills for a session rooted at cwd with the default
// Loader. See Loader.Load.
func Load(cwd string) ([]Skill, error) { return Loader{}.Load(cwd) }

// Load discovers skills for a session rooted at cwd. Each immediate
// subdirectory of the skills directory that contains a FileName becomes
// one skill, named by its frontmatter `name` or, failing that, the
// directory name. Project skills override global ones of the same name,
// and the result is sorted by name.
//
// The error is advisory in the same way projectctx.Load's is: skills
// that parsed cleanly come back alongside it, so one malformed SKILL.md
// costs the run that skill rather than all of them.
func (l Loader) Load(cwd string) ([]Skill, error) {
	var loadErrs []error
	byName := map[string]Skill{}

	// Global first — a project skill of the same name replaces it.
	var dirs []string
	if l.GlobalDir != "" {
		dirs = append(dirs, l.GlobalDir)
	}
	dirs = append(dirs, l.projectDir(cwd))

	for _, dir := range dirs {
		found, errs := l.loadDir(dir)
		loadErrs = append(loadErrs, errs...)
		for _, s := range found {
			byName[s.Name] = s
		}
	}

	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errors.Join(loadErrs...)
}

func (l Loader) projectDir(cwd string) string {
	dir := l.Dir
	if dir == "" {
		dir = DefaultDir
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(projectctx.Root(cwd), filepath.FromSlash(dir))
}

// loadDir reads one skills directory. A missing directory is not an
// error — most projects have none.
func (l Loader) loadDir(dir string) ([]Skill, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("skills: read %s: %w", dir, err)}
	}

	var out []Skill
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), FileName)
		b, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("skills: read %s: %w", path, err))
			}
			continue
		}
		s, err := l.parse(e.Name(), b, path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, s)
	}
	return out, errs
}

func (l Loader) parse(dirName string, content []byte, path string) (Skill, error) {
	meta, body := splitFrontmatter(content)

	name := strings.ToLower(strings.TrimSpace(meta["name"]))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(dirName))
	}
	if !validName.MatchString(name) {
		return Skill{}, fmt.Errorf("skills: %s: name %q must be lowercase letters, numbers, hyphens, or underscores", path, name)
	}
	if reserved[name] {
		return Skill{}, fmt.Errorf("skills: %s: name %q is reserved by the sandbox", path, name)
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return Skill{}, fmt.Errorf("skills: %s: body is empty", path)
	}
	if max := l.maxBytes(); max > 0 && len(text) > max {
		text = strings.TrimSpace(text[:max]) + fmt.Sprintf("\n\n[... truncated at %d bytes ...]", max)
	}

	description := strings.TrimSpace(meta["description"])
	if description == "" {
		description = firstLine(text)
	}
	return Skill{Name: name, Description: description, Body: text, Path: path}, nil
}

func (l Loader) maxBytes() int {
	if l.MaxBytes == 0 {
		return DefaultMaxBytes
	}
	return l.MaxBytes // negative disables truncation
}

// firstLine derives a description from the body when frontmatter gave
// none: the first line with actual prose on it, headings skipped, so
// the catalog says something more useful than "# My Skill".
func firstLine(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		const maxLen = 160
		if len(line) > maxLen {
			return strings.TrimSpace(line[:maxLen]) + "…"
		}
		return line
	}
	return ""
}

// splitFrontmatter separates optional leading `---`-fenced frontmatter
// from the body.
//
// The frontmatter reader is a minimal `key: value` scanner, NOT a YAML
// parser — enough for the `name` and `description` this package reads,
// and not worth a YAML dependency for. Nested structures, lists, and
// multi-line scalars are ignored rather than rejected, so a richer
// frontmatter aimed at some other tool still loads here.
func splitFrontmatter(content []byte) (map[string]string, []byte) {
	s := string(content)
	if !strings.HasPrefix(s, "---\n") {
		return nil, content
	}
	rest := s[len("---\n"):]
	for _, sep := range []string{"\n---\n", "\n---"} {
		if i := strings.Index(rest, sep); i >= 0 {
			return parseKeyValues(rest[:i]), []byte(strings.TrimLeft(rest[i+len(sep):], "\n"))
		}
	}
	return nil, content // unterminated fence — treat the whole file as body
}

func parseKeyValues(block string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(block, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.HasPrefix(key, "#") || strings.ContainsAny(key, " \t") {
			continue // a comment, or an indented key of some nested structure
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if value != "" {
			out[strings.ToLower(key)] = value
		}
	}
	return out
}

// Packs turns discovered skills into sandbox packs, one per skill.
//
// Each pack installs no JS functions. It contributes a single catalog
// line to the system prompt and registers its body as the help entry
// for its name, which is what makes skillGet(name) return the full
// instructions. Keeping the body out of the Prompt is the whole design:
// the model pays for a skill's detail only when it asks for it.
func Packs(sk []Skill) []sandbox.Pack {
	out := make([]sandbox.Pack, 0, len(sk))
	for _, s := range sk {
		out = append(out, sandbox.Pack{
			Name:        s.Name,
			Description: s.Description,
			SkillType:   "prompt",
			Prompt: fmt.Sprintf(
				"// skill %q — %s Call skillGet(%q) for the full instructions before using it.",
				s.Name, ensureSentence(s.Description), s.Name),
			HelpEntries: map[string]string{s.Name: s.Body},
		})
	}
	return out
}

// Capabilities wraps discovered skills as agentloop Capabilities, one
// per skill, ready to append to the slice a DefaultSandboxBuilder gets.
//
// One capability each rather than one for all of them is deliberate:
// Capability.Name is what a per-session allowlist
// (BuildContext.EnabledCapabilities) matches on, so this is what lets
// an application enable a skill for one session and not another.
func Capabilities(sk []Skill) []agentloop.Capability {
	packs := Packs(sk)
	out := make([]agentloop.Capability, 0, len(packs))
	for _, p := range packs {
		out = append(out, agentloop.Capability{
			Name:        p.Name,
			Description: p.Description,
			Build: func(agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{p}, nil
			},
		})
	}
	return out
}

// ensureSentence gives the catalog line a terminator so the sentence
// that follows it does not run on.
func ensureSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "A project skill."
	}
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}
