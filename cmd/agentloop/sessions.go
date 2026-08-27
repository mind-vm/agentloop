package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopsql"
)

// cmdSessions inspects and prunes the session database: what is stored,
// what one session contains, and how to get rid of one.
func cmdSessions(argv []string) int {
	action, rest := "ls", argv
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		action, rest = argv[0], argv[1:]
	}

	var o options
	fs := flag.NewFlagSet("agentloop sessions "+action, flag.ContinueOnError)
	o.bind(fs)
	limit := fs.Int("limit", 20, "maximum sessions to list (0 for all)")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if o.ephemeral {
		fmt.Fprintln(os.Stderr, "agentloop: --ephemeral has no sessions to inspect")
		return exitUsage
	}
	// The session-selection flags belong to a run, not to a listing.
	o.session, o.resume = "", false
	if err := o.resolve(); err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitUsage
	}

	store, err := openStore(&o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitUsage
	}
	defer store.Close()

	ctx := context.Background()
	switch action {
	case "ls", "list":
		return sessionsList(ctx, store, &o, *limit)
	case "show":
		return sessionsShow(ctx, store, &o, fs.Args())
	case "rm", "remove", "delete":
		return sessionsRemove(ctx, store, fs.Args())
	case "revoke":
		return sessionsRevoke(ctx, store, fs.Args())
	default:
		fmt.Fprintf(os.Stderr, "agentloop: unknown sessions command %q — try ls, show, rm, or revoke.\n", action)
		return exitUsage
	}
}

func sessionsList(ctx context.Context, store *agentloopsql.Store, o *options, limit int) int {
	rows, err := store.List(ctx, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitError
	}

	if o.asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, r := range rows {
			if err := enc.Encode(sessionJSON(r)); err != nil {
				fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
				return exitError
			}
		}
		return exitOK
	}

	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "No sessions in %s.\n", o.db)
		return exitOK
	}
	for _, r := range rows {
		status := r.Status
		if status == "" {
			status = "-"
		}
		fmt.Printf("%s  %-14s  %3d steps  %-19s  %s\n",
			r.ID, status, r.Steps, r.UpdatedAt.Local().Format("2006-01-02 15:04:05"),
			truncate(r.Opening, 48))
	}
	return exitOK
}

func sessionsShow(ctx context.Context, store *agentloopsql.Store, o *options, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "agentloop: sessions show takes exactly one session id")
		return exitUsage
	}
	id := args[0]

	exists, err := store.Exists(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitError
	}
	if !exists {
		fmt.Fprintf(os.Stderr, "agentloop: no such session: %s\n", id)
		return exitError
	}

	steps, err := store.Steps(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitError
	}

	if o.asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, s := range steps {
			if err := enc.Encode(stepJSON(s)); err != nil {
				fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
				return exitError
			}
		}
		return exitOK
	}

	for _, s := range steps {
		fmt.Printf("\n--- %s (step %d, %s) ---\n%s\n",
			s.StepType, s.StepIndex, s.CreatedAt.Local().Format("15:04:05"),
			strings.TrimRight(s.Content, "\n"))
	}

	// What this session has been permitted is part of what it is — and
	// the only way to notice an approval you would rather take back.
	grants, err := store.Grants(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitError
	}
	if len(grants) > 0 {
		fmt.Printf("\n--- approved (%d) ---\n", len(grants))
		for _, g := range grants {
			fmt.Printf("  %s\n", g)
		}
		fmt.Fprintf(os.Stderr, "\nRun `agentloop sessions revoke %s` to forget these.\n", id)
	}
	return exitOK
}

func sessionsRevoke(ctx context.Context, store *agentloopsql.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "agentloop: sessions revoke takes one or more session ids")
		return exitUsage
	}
	code := exitOK
	for _, id := range args {
		grants, err := store.Grants(ctx, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
			code = exitError
			continue
		}
		if err := store.Revoke(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
			code = exitError
			continue
		}
		fmt.Fprintf(os.Stderr, "· %s: forgot %d approval(s)\n", id, len(grants))
	}
	return code
}

func sessionsRemove(ctx context.Context, store *agentloopsql.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "agentloop: sessions rm takes one or more session ids")
		return exitUsage
	}
	code := exitOK
	for _, id := range args {
		switch err := store.Delete(ctx, id); {
		case errors.Is(err, agentloopsql.ErrNoSession):
			fmt.Fprintf(os.Stderr, "agentloop: no such session: %s\n", id)
			code = exitError
		case err != nil:
			fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
			code = exitError
		default:
			fmt.Fprintf(os.Stderr, "· removed %s\n", id)
		}
	}
	return code
}

// The JSON shapes below are declared rather than derived, for the same
// reason jsonResult is: the store's Go types carry no JSON tags, and an
// output contract another program depends on should be stated in one
// visible place.

type sessionLine struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	Status    string     `json:"status,omitempty"`
	Model     string     `json:"model,omitempty"`
	Steps     int32      `json:"steps"`
	Tokens    jsonTokens `json:"tokens"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	Opening   string     `json:"opening,omitempty"`
}

func sessionJSON(r agentloopsql.SessionInfo) sessionLine {
	return sessionLine{
		Type: "session", ID: r.ID, Status: r.Status, Model: r.Model, Steps: r.Steps,
		Tokens:    jsonTokens{Prompt: r.Tokens.Prompt, Completion: r.Tokens.Completion},
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
		Opening:   r.Opening,
	}
}

type stepLine struct {
	Type      string `json:"type"`
	StepIndex int32  `json:"step_index"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"created_at"`
}

func stepJSON(s agentloop.RunStep) stepLine {
	return stepLine{
		Type: s.StepType, StepIndex: s.StepIndex, Content: s.Content,
		CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// truncate shortens s to n characters for the listing column. It counts
// runes, not bytes, so a multi-byte character is never cut in half.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
