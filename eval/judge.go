package eval

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/jryannel/agentloop/llm"
)

// judgeResponse asks the judge to rate response on a 0-10 integer
// scale against the case's Criteria, parsing the score out of "Score:
// <n>" in the reply.
func judgeResponse(ctx context.Context, judge llm.Client, model string, c Case, response string) (int, string, error) {
	resp, err := judge.Complete(ctx, llm.CompletionRequest{
		Model: model,
		Messages: []llm.Message{
			{Role: "system", Content: "You are a careful evaluator. Score the response 0–10 against the criteria, with 10 = perfect. Reply with two lines: 'Score: <n>' followed by 'Rationale: <one sentence>'."},
			{Role: "user", Content: buildJudgePrompt(c, response)},
		},
	})
	if err != nil {
		return 0, "", err
	}
	score, rationale := parseJudgeReply(resp.Content)
	return score, rationale, nil
}

// judgeCriterion runs a single judge call scoped to one rubric item.
// Same Score:/Rationale: parse contract as judgeResponse — the system
// prompt focuses the judge on the one assertion, so the rationale
// comes back tightly scoped to that criterion.
func judgeCriterion(ctx context.Context, judge llm.Client, model string, c Case, response string, item CriterionItem) (int, string, error) {
	resp, err := judge.Complete(ctx, llm.CompletionRequest{
		Model: model,
		Messages: []llm.Message{
			{Role: "system", Content: "You are a careful evaluator scoring one specific criterion. Score the response 0–10 against the criterion, with 10 = perfect. Reply with two lines: 'Score: <n>' followed by 'Rationale: <one sentence>'."},
			{Role: "user", Content: buildCriterionPrompt(c, response, item)},
		},
	})
	if err != nil {
		return 0, "", err
	}
	score, rationale := parseJudgeReply(resp.Content)
	return score, rationale, nil
}

func buildCriterionPrompt(c Case, response string, item CriterionItem) string {
	var b strings.Builder
	b.WriteString("Input:\n")
	b.WriteString(c.Input)
	b.WriteString("\n\nCriterion:\n")
	b.WriteString(item.Label)
	b.WriteString("\n\nResponse:\n")
	b.WriteString(response)
	return b.String()
}

func buildJudgePrompt(c Case, response string) string {
	var b strings.Builder
	b.WriteString("Input:\n")
	b.WriteString(c.Input)
	b.WriteString("\n\nCriteria:\n")
	if c.Criteria != "" {
		b.WriteString(c.Criteria)
	} else {
		b.WriteString("(none specified — rate on correctness, helpfulness, and conciseness)")
	}
	b.WriteString("\n\nResponse:\n")
	b.WriteString(response)
	return b.String()
}

var scoreRe = regexp.MustCompile(`(?i)score\s*:\s*(\d+)`)

// parseJudgeReply extracts the numeric score (clamped to 0..10) and
// the rationale line from the judge's reply text.
func parseJudgeReply(text string) (score int, rationale string) {
	if m := scoreRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 10 {
			n = 10
		}
		score = n
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "score") {
			continue
		}
		rationale = strings.TrimPrefix(strings.TrimPrefix(line, "Rationale:"), "rationale:")
		rationale = strings.TrimSpace(rationale)
		break
	}
	return score, rationale
}
