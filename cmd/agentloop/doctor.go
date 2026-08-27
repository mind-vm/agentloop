package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/projectctx"
	"github.com/mind-vm/agentloop/skills"
)

// cmdDoctor reports what this invocation would be configured with, and
// whether the endpoint actually answers. It exits non-zero if any check
// fails, so it doubles as a readiness probe in a setup script.
func cmdDoctor(argv []string) int {
	var o options
	fs := flag.NewFlagSet("agentloop doctor", flag.ContinueOnError)
	o.bind(fs)
	offline := fs.Bool("offline", false, "skip the live request to the configured endpoint")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	if err := o.resolve(); err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitUsage
	}

	d := &report{out: os.Stdout}
	cfg := llm.ConfigFromEnv()

	d.section("Endpoint")
	if cfg.APIKey == "" {
		d.fail("OPENAI_API_KEY", "not set")
	} else {
		d.ok("OPENAI_API_KEY", "set (%d chars)", len(cfg.APIKey))
	}
	d.info("base URL", "%s", cfg.BaseURL)
	d.info("model", "%s", modelOr(o.model, cfg.ChatModel))

	// A missing key already failed above; reporting NewOpenAI's refusal
	// as a second failure says the same thing twice.
	if cfg.APIKey != "" {
		client, err := llm.NewOpenAI(cfg)
		switch {
		case err != nil:
			d.fail("client", "%s", err)
		case *offline:
			d.info("reachability", "skipped (--offline)")
		default:
			d.probe(client, modelOr(o.model, cfg.ChatModel))
		}
	}

	d.section("Workspace")
	d.info("root", "%s", o.cwd)
	if _, err := os.OpenRoot(o.cwd); err != nil {
		d.fail("root", "%s", err)
	} else {
		d.ok("file access", "readFile, writeFile, editFile, listDir, glob, grep")
	}
	if o.noNetwork {
		d.info("network", "off (--no-network): fetch() and require('http') are not installed")
	} else {
		d.info("network", "on — fetch() asks before reaching an unapproved domain")
	}

	docs, err := projectctx.Load(o.cwd)
	if err != nil {
		d.warn("project instructions", "%s", err)
	}
	if len(docs) == 0 {
		d.info("project instructions", "none found")
	} else {
		for _, doc := range docs {
			d.ok("AGENTS.md", "%s (%d bytes)", doc.Name, len(doc.Content))
		}
	}

	sk, err := skills.Load(o.cwd)
	if err != nil {
		d.warn("skills", "%s", err)
	}
	if len(sk) == 0 {
		d.info("skills", "none found")
	} else {
		for _, s := range sk {
			d.ok("SKILL.md", "%s — %s", s.Name, s.Description)
		}
	}

	d.section("Permissions")
	d.info("approval mode", "%s", o.approval)
	switch o.approval {
	case approvalDeny:
		d.info("", "capabilities that need permission will refuse — no terminal to ask at")
	case approvalAuto:
		d.warn("approval mode", "every permission request is granted without asking")
	}

	d.section("Sessions")
	d.info("database", "%s", o.db)
	if store, err := openStore(&o); err != nil {
		d.fail("database", "%s", err)
	} else {
		defer store.Close()
		if rows, err := store.List(context.Background(), 0); err != nil {
			d.fail("database", "%s", err)
		} else {
			d.ok("stored", "%d session(s)", len(rows))
		}
	}

	if d.failed {
		return exitError
	}
	return exitOK
}

// probe makes the smallest real request the endpoint will accept, which
// is the only check that distinguishes a plausible configuration from a
// working one.
func (r *report) probe(client llm.Client, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	maxTokens := 1
	start := time.Now()
	_, err := client.Complete(ctx, llm.CompletionRequest{
		Model:     model,
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		r.fail("reachability", "%s", err)
		return
	}
	r.ok("reachability", "answered in %s", time.Since(start).Round(time.Millisecond))
}

func modelOr(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// report accumulates a human-readable check list and remembers whether
// anything failed.
type report struct {
	out    *os.File
	failed bool
}

func (r *report) section(name string) { fmt.Fprintf(r.out, "\n%s\n", name) }

func (r *report) ok(label, format string, args ...any) {
	fmt.Fprintf(r.out, "  ok    %-22s %s\n", label, fmt.Sprintf(format, args...))
}

func (r *report) info(label, format string, args ...any) {
	fmt.Fprintf(r.out, "        %-22s %s\n", label, fmt.Sprintf(format, args...))
}

func (r *report) warn(label, format string, args ...any) {
	fmt.Fprintf(r.out, "  warn  %-22s %s\n", label, fmt.Sprintf(format, args...))
}

func (r *report) fail(label, format string, args ...any) {
	r.failed = true
	fmt.Fprintf(r.out, "  FAIL  %-22s %s\n", label, fmt.Sprintf(format, args...))
}
