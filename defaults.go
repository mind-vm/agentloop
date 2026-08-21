package agentloop

import (
	"context"
	"log/slog"

	"github.com/jryannel/agentloop/llm"
	"github.com/jryannel/agentloop/sandbox"
)

// DefaultCapabilities is the general-purpose bundle most agents want:
// require() (always on), require('http'), require('markdown'), fetch() /
// htmlToMarkdown(), and — when llmClient is non-nil — ai() / aiJSON().
// model is the model passed to every ai()/aiJSON() sub-call; empty uses
// the client's own default.
//
// Passing llmClient == nil is valid: the "ai" capability's Build then
// returns (nil, nil) and the session simply has no ai()/aiJSON()
// primitive, rather than failing to start.
func DefaultCapabilities(llmClient llm.Client, model string) []Capability {
	return []Capability{
		{
			Name:        "require",
			Description: "CommonJS-style require() resolver",
			AlwaysOn:    true,
			Build: func(bc BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{sandbox.RequirePack(bc.Ctx)}, nil
			},
		},
		{
			Name:        "markdown",
			Description: "require('markdown') helpers",
			Build: func(BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{sandbox.MarkdownModulePack()}, nil
			},
		},
		{
			Name:        "http",
			Description: "require('http') verbs",
			Build: func(BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{sandbox.HttpModulePack()}, nil
			},
		},
		{
			Name:        "fetch",
			Description: "fetch() + htmlToMarkdown()",
			Build: func(bc BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{sandbox.FetchPack(bc.Ctx, nil)}, nil
			},
		},
		{
			Name:        "ai",
			Description: "ai() / aiJSON() against the configured LLM",
			Build: func(bc BuildContext) ([]sandbox.Pack, error) {
				if llmClient == nil {
					return nil, nil
				}
				return []sandbox.Pack{sandbox.AIPack(bc.Ctx, llmClient, model)}, nil
			},
		},
	}
}

// DefaultSandboxBuilder is the simplest SandboxBuilder: it composes a
// fixed Capabilities list into a fresh sandbox.Sandbox for every Run,
// filtered by EnabledCapabilities (nil = all enabled). Applications whose
// capability set varies per scope/session (e.g. a per-tenant allowlist
// pulled from a database) should implement SandboxBuilder themselves —
// its Build method is a good starting point to copy.
type DefaultSandboxBuilder struct {
	// Capabilities is the full set this builder can install; each
	// Build call filters it down via EnabledCapabilities.
	Capabilities []Capability

	// EnabledCapabilities is the allowlist passed through to every
	// capability's BuildContext. nil means "all enabled".
	EnabledCapabilities *[]string
}

// Build implements SandboxBuilder. A capability whose Build fails is
// logged and skipped — via a "warning" sandbox.Event when onEvent is
// non-nil, and always via slog — rather than aborting the whole
// session: one flaky capability shouldn't deny the user their turn.
func (b *DefaultSandboxBuilder) Build(ctx context.Context, sess Session, scope Scope, onEvent sandbox.OnEvent) (*sandbox.Sandbox, func(), error) {
	bc := BuildContext{
		Ctx:                 ctx,
		Scope:               scope,
		SessionID:           sess.ID,
		MessageID:           sess.MessageID,
		EnabledCapabilities: b.EnabledCapabilities,
	}

	var packs []sandbox.Pack
	for _, cap := range b.Capabilities {
		if !cap.AlwaysOn && !capabilityAllowed(cap.Name, b.EnabledCapabilities) {
			continue
		}
		built, err := cap.Build(bc)
		if err != nil {
			slog.WarnContext(ctx, "agentloop: capability build failed", "name", cap.Name, "error", err)
			if onEvent != nil {
				onEvent(sandbox.Event{Kind: sandbox.EventWarning, Summary: "capability unavailable this run: " + cap.Name, Detail: err.Error()})
			}
			continue
		}
		packs = append(packs, built...)
	}

	// skillList()/skillGet() introspect the union of every other pack;
	// help() reads every pack's HelpEntries, so both go last.
	skills := make([]sandbox.SkillInfo, 0, len(packs))
	for _, p := range packs {
		skills = append(skills, sandbox.SkillInfo{Name: p.Name, Description: p.Description, Type: "pack"})
	}
	packs = append(packs, sandbox.SkillDiscoveryPack(skills), sandbox.HelpPack())

	sb := sandbox.New(packs...)
	sb.SetOnEvent(onEvent)
	return sb, func() {}, nil
}

func capabilityAllowed(name string, enabled *[]string) bool {
	if enabled == nil {
		return true
	}
	for _, n := range *enabled {
		if n == name {
			return true
		}
	}
	return false
}

var _ SandboxBuilder = (*DefaultSandboxBuilder)(nil)
