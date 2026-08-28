package projectctx

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/sandbox"
)

// PackName is the sandbox pack (and agentloop Capability) the retrieval
// mode installs. One pack for all discovered docs rather than one each:
// project instructions are a single authority the model consults, and
// "some files retrievable, others not" is not a state worth being able
// to configure.
const PackName = "project_instructions"

// Packs returns the sandbox pack backing RenderCatalog: it installs
// projectGet(name) and projectList(), with every doc's FULL body held
// server-side and reachable by name.
//
// Returns nil for no docs, so an application can append the result
// unconditionally — a project with no AGENTS.md gets neither the
// primitives nor the declarations that document them.
func Packs(docs []Doc) []sandbox.Pack {
	docs = nonEmpty(docs)
	if len(docs) == 0 {
		return nil
	}
	return []sandbox.Pack{discoveryPack(docs)}
}

// Capabilities wraps Packs as agentloop Capabilities, ready to append
// to the slice a DefaultSandboxBuilder gets.
func Capabilities(docs []Doc) []agentloop.Capability {
	packs := Packs(docs)
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

// projectDoc is one entry returned by projectList().
type projectDoc struct {
	Name string `json:"name"`
	// Bytes is the full body's size, so the model can weigh a
	// projectGet against what it would cost.
	Bytes int `json:"bytes"`
	// Complete reports that the system prompt already carries the whole
	// file — the signal that says a projectGet would return nothing new.
	Complete bool `json:"complete"`
}

func discoveryPack(docs []Doc) sandbox.Pack {
	bodies := make(map[string]string, len(docs))
	list := make([]projectDoc, 0, len(docs))
	for _, d := range docs {
		bodies[d.Name] = d.Content
		list = append(list, projectDoc{Name: d.Name, Bytes: len(d.Content), Complete: d.Complete()})
	}

	// help(name) reaches the same bodies, so a model that goes looking
	// through the general help surface finds them too.
	helpEntries := make(map[string]string, len(bodies)+2)
	for name, body := range bodies {
		helpEntries[name] = body
	}
	helpEntries["projectList"] = "projectList() — List this project's instruction files. Returns [{name, bytes, complete}]."
	helpEntries["projectGet"] = "projectGet(name) — Return one project instruction file in full, by the name projectList() reports."

	return sandbox.Pack{
		Name:        PackName,
		Description: "Project instruction files, retrievable in full",
		SkillType:   "prompt",
		Prompt: `// --- Project instructions ---

interface ProjectDoc { name: string; bytes: number; complete: boolean }
/** List this project's instruction files. complete=true means the system prompt already carries the whole file, so projectGet would add nothing. */
declare function projectList(): ProjectDoc[];
/** Read one project instruction file IN FULL. The "## Project instructions" section shows only each file's opening; call this before acting in an area an excerpted file covers. */
declare function projectGet(name: string): string;`,
		HelpEntries: helpEntries,
		Register: func(rt *goja.Runtime, _ *sandbox.Sandbox) {
			_ = rt.Set("projectList", func(goja.FunctionCall) goja.Value {
				b, _ := json.Marshal(list)
				var out any
				_ = json.Unmarshal(b, &out)
				return rt.ToValue(out)
			})
			_ = rt.Set("projectGet", func(call goja.FunctionCall) goja.Value {
				name := call.Argument(0).String()
				if name == "" {
					panic(rt.NewGoError(fmt.Errorf("projectGet: name is required")))
				}
				if body, ok := resolveDoc(bodies, name); ok {
					return rt.ToValue(body)
				}
				return rt.ToValue(fmt.Sprintf(
					"No project instruction file named %q. Call projectList() to see the names.", name))
			})
		},
	}
}

// resolveDoc looks a doc up by the name projectList reports, then by
// bare file name.
//
// The fallback exists because the catalog names a doc by its path from
// the repository root ("docs/AGENTS.md") while a model reading prose
// about "the AGENTS.md file" will reach for the bare name. It applies
// only when exactly one doc matches — with several AGENTS.md files in
// play, which one was meant is precisely the thing that cannot be
// guessed, and a wrong file answered confidently is worse than a miss
// the model can correct with projectList().
func resolveDoc(bodies map[string]string, name string) (string, bool) {
	if body, ok := bodies[name]; ok {
		return body, true
	}
	var found string
	matches := 0
	for docName, body := range bodies {
		if path.Base(docName) == name {
			found = body
			matches++
		}
	}
	if matches == 1 {
		return found, true
	}
	return "", false
}

// RenderCatalog composes docs into the prompt section for the retrieval
// mode: each file's opening section inline, and a pointer to
// projectGet(name) for whatever was left out.
//
// It is the alternative to Render, not a companion to it — the two
// produce the same heading and would duplicate each other. Wire
// Capabilities alongside it, or the pointers it writes name a function
// the sandbox does not have.
//
// Returns "" for no docs, like Render, so the call is safe to make
// unconditionally.
func RenderCatalog(docs []Doc) string {
	docs = nonEmpty(docs)
	if len(docs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Project instructions\n\n")
	b.WriteString("The following come from instruction files in this project. ")
	b.WriteString("Treat them as authoritative guidance for work in this repository. ")
	b.WriteString("Where two files disagree, the later one is more specific and wins.\n")
	if excerpted(docs) {
		// Named explicitly rather than left to the model's judgement:
		// an excerpt that reads like a complete set of rules is exactly
		// the case where it would not think to ask for more.
		b.WriteString("\nA file marked as excerpted is shown only down to where the excerpt ends. ")
		b.WriteString("Its remaining sections are real instructions you have not read — call projectGet(\"<name>\") ")
		b.WriteString("for the full text before acting in an area it covers, rather than assuming the excerpt is the whole rule.\n")
	}
	for _, d := range docs {
		b.WriteString("\n### ")
		b.WriteString(d.Name)
		b.WriteString("\n\n")
		b.WriteString(d.InlineText())
		b.WriteString("\n")
		if !d.Complete() {
			fmt.Fprintf(&b, "\n[excerpt ends here — %s is %d bytes in full; call projectGet(%q) to read the rest]\n",
				d.Name, len(d.Content), d.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func excerpted(docs []Doc) bool {
	for _, d := range docs {
		if !d.Complete() {
			return true
		}
	}
	return false
}
