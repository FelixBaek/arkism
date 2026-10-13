package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The answer and judge models run through the local Claude Code CLI exactly as capture
// extraction does (host.ClaudeBin, -p --model M --output-format json), so no API key is used.

type cliResult struct {
	Text        string
	Structured  map[string]any
	CostUSD     float64
	InputTokens int
	Attempts    int
}

type cliRunner struct {
	bin     string
	dir     string
	timeout time.Duration
	retries int
}

var errCLI = errors.New("claude cli")

func (c cliRunner) once(model, system, user, schema string) (cliResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	args := []string{"-p", "--model", model, "--output-format", "json", "--system-prompt", system,
		"--tools", "", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence"}
	if schema != "" {
		args = append(args, "--json-schema", schema)
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1")
	cmd.Dir = c.dir
	cmd.Stdin = strings.NewReader(user)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if ctx.Err() != nil {
		return cliResult{}, fmt.Errorf("%w: timeout after %s", errCLI, c.timeout)
	}
	if err != nil {
		return cliResult{}, fmt.Errorf("%w: %v", errCLI, err)
	}
	var out struct {
		IsError    bool           `json:"is_error"`
		Result     string         `json:"result"`
		Cost       float64        `json:"total_cost_usd"`
		Structured map[string]any `json:"structured_output"`
		Usage      struct {
			Input       int `json:"input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return cliResult{}, fmt.Errorf("%w: unreadable output", errCLI)
	}
	if out.IsError {
		return cliResult{}, fmt.Errorf("%w: is_error", errCLI)
	}
	return cliResult{Text: strings.TrimSpace(out.Result), Structured: out.Structured, CostUSD: out.Cost,
		InputTokens: out.Usage.Input + out.Usage.CacheRead + out.Usage.CacheCreate}, nil
}

// call retries a failed or timed-out CLI run with backoff.
func (c cliRunner) call(model, system, user, schema string) (cliResult, error) {
	var last error
	for attempt := 1; attempt <= c.retries+1; attempt++ {
		r, err := c.once(model, system, user, schema)
		if err == nil {
			r.Attempts = attempt
			return r, nil
		}
		last = err
		if attempt <= c.retries {
			time.Sleep(time.Duration(attempt*attempt) * 2 * time.Second)
		}
	}
	return cliResult{Attempts: c.retries + 1}, last
}

func defaultClaudeBin() string { return host.ClaudeBin }

const answerSystem = `You answer a question about a long conversation between two people.
You are given only MEMORIES: short notes a memory system kept from that conversation, each with the date it was recorded.
Rules:
- Use only the memories. Do not use outside knowledge about these people.
- Resolve relative time ("last year", "yesterday") against the memory's date and answer with an absolute date or year.
- Answer in English in as few words as possible (under 10 words), no explanation.
- If the memories do not contain the answer, reply exactly: I don't know`

const fullContextSystem = `You answer a question about a conversation between two people.
You are given the whole CONVERSATION, session by session, with each session's date.
Rules:
- Use only the conversation.
- Resolve relative time ("last year", "yesterday") against the session date and answer with an absolute date or year.
- Answer in English in as few words as possible (under 10 words), no explanation.
- If the conversation does not contain the answer, reply exactly: I don't know`

// judgeSystem follows mem0's LoCoMo ACCURACY_PROMPT: binary, lenient on wording and date format.
const judgeSystem = `Your task is to label an answer to a question as CORRECT or WRONG.
You get a question, a gold (ground truth) answer and a generated answer.
The gold answer is usually short. Be generous: if the generated answer touches the same topic and
refers to the same thing as the gold answer, it is CORRECT, even if it is longer or worded differently.
For time questions, a different format of the same date or period (for example "May 7th" vs "7 May")
is CORRECT. "I don't know" or an answer about something else is WRONG.
Return JSON: {"reasoning": "one short sentence", "label": "CORRECT" or "WRONG"}.`

const judgeSchema = `{"type":"object","additionalProperties":false,"required":["reasoning","label"],"properties":{"reasoning":{"type":"string"},"label":{"type":"string","enum":["CORRECT","WRONG"]}}}`

func judgeUser(question, gold, answer string) string {
	return fmt.Sprintf("Question: %s\nGold answer: %s\nGenerated answer: %s", question, gold, answer)
}

var labelRE = regexp.MustCompile(`(?i)"label"\s*:\s*"(CORRECT|WRONG)"`)
var wordRE = regexp.MustCompile(`\b(CORRECT|WRONG)\b`)

// ParseJudge turns the judge's output into 1 (CORRECT) or 0 (WRONG). It prefers the
// structured output, then a "label" field in the text, then the last bare CORRECT/WRONG.
func ParseJudge(structured map[string]any, text string) (int, error) {
	if l, ok := structured["label"].(string); ok {
		switch strings.ToUpper(strings.TrimSpace(l)) {
		case "CORRECT":
			return 1, nil
		case "WRONG":
			return 0, nil
		}
	}
	if m := labelRE.FindAllStringSubmatch(text, -1); len(m) > 0 {
		return labelScore(m[len(m)-1][1]), nil
	}
	if m := wordRE.FindAllString(text, -1); len(m) > 0 {
		return labelScore(m[len(m)-1]), nil
	}
	return 0, fmt.Errorf("judge output has no label")
}

func labelScore(s string) int {
	if strings.EqualFold(s, "CORRECT") {
		return 1
	}
	return 0
}
