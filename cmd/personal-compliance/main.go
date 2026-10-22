// personal-compliance is a personal_compliance outcome checker (ST-302, FR-012). It reads one
// outcome input on stdin and the human-approved rule table named by argv[1], and prints
// {"output":{"observed","passed"},"cost":0}. It judges only machine-checkable rules against the
// trace's behavior event; no rule, no behavior or no applicable artifact is unobserved, never a pass.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
)

type Rule struct {
	Memory struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	} `json:"memory"`
	Kind      string   `json:"kind"`
	Artifacts []string `json:"artifacts,omitempty"` // forbidden_text: commits, pull_requests, replies
	Text      string   `json:"text,omitempty"`
	MinRatio  float64  `json:"min_ratio,omitempty"` // language: 0 = any Hangul at all (a translation reply still passes)
	MaxLines  int      `json:"max_lines,omitempty"` // max_length
	MinLines  int      `json:"min_lines,omitempty"` // required_tail: a reply this long is a report
	Pattern   string   `json:"pattern,omitempty"`   // required_tail
}

type Table struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

type target struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

type input struct {
	Input struct {
		Dimension string `json:"dimension"`
		Target    target `json:"target"`
		Events    []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
	} `json:"input"`
}

type reply struct {
	Text            string `json:"text"`
	Lines           int    `json:"lines"`
	DetailRequested bool   `json:"detail_requested"`
}

type behavior struct {
	Behavior     string   `json:"behavior"`
	Replies      []reply  `json:"replies"`
	Commits      []string `json:"commits"`
	PullRequests []string `json:"pull_requests"`
	Incomplete   bool     `json:"incomplete"` // the producer cut or dropped something: no pass, only a seen violation
}

var (
	inlineCodeRE = regexp.MustCompile("`[^`\n]*`")
	urlRE        = regexp.MustCompile(`https?://\S+`)
	spaceRE      = regexp.MustCompile(`\s+`)
	paragraphRE  = regexp.MustCompile(`\n\s*\n`)
	// Only a message written inline is in the trace; one from a file, an editor, -C or --fill is not.
	inlineCommitRE = regexp.MustCompile(`(^|\s)(-[a-zA-Z]*m|--message)`)
	inlinePRRE     = regexp.MustCompile(`(^|\s)(-b|--body)(\s|=)`)
	commitSegRE    = regexp.MustCompile(`^\s*(\w+=\S*\s+)*git\b.*\bcommit\b`)
	prSegRE        = regexp.MustCompile(`^\s*(\w+=\S*\s+)*gh\s+pr\s+(create|edit)\b`)
	cmdSplitRE     = regexp.MustCompile(`&&|\|\||[;|\n]`)
)

// inline reports whether every commit or PR command in s carries its message inline: a flag
// from another command in the same shell line does not count. Segments start a command, so a
// message body line that mentions git commit is not one.
func inline(s string, seg, flag *regexp.Regexp) bool {
	s = strings.ReplaceAll(s, "\\\n", " ") // backslash line continuation
	found := false
	for _, part := range cmdSplitRE.Split(s, -1) {
		if seg.MatchString(part) {
			if !flag.MatchString(part) {
				return false
			}
			found = true
		}
	}
	return found
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: personal-compliance RULES.json < input")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		os.Exit(1)
	}
	var table Table
	if err := json.Unmarshal(raw, &table); err != nil || table.Version != 1 || validate(table) != nil {
		os.Exit(1)
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(1)
	}
	observed, passed, err := check(table, stdin)
	if err != nil {
		os.Exit(1)
	}
	out := map[string]any{"observed": observed, "passed": nil}
	if observed {
		out["passed"] = passed
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"output": out, "cost": 0})
}

func validate(t Table) error {
	for _, r := range t.Rules {
		ok := r.Memory.ID != "" && r.Memory.Revision >= 1
		switch r.Kind {
		case "forbidden_text":
			ok = ok && strings.TrimSpace(r.Text) != "" && len(r.Artifacts) > 0
			for _, a := range r.Artifacts {
				ok = ok && (a == "commits" || a == "pull_requests" || a == "replies")
			}
		case "language":
			ok = ok && r.MinRatio >= 0 && r.MinRatio <= 1
		case "max_length":
			ok = ok && r.MaxLines > 0
		case "required_tail":
			_, err := regexp.Compile(r.Pattern)
			ok = ok && r.Pattern != "" && err == nil && r.MinLines > 0
		default:
			ok = false
		}
		if !ok {
			return fmt.Errorf("invalid rule for %s", r.Memory.ID)
		}
	}
	return nil
}

// check returns (observed, passed). A revised memory no longer matches its rule: unobserved until a
// person re-approves the table.
func check(t Table, stdin []byte) (bool, bool, error) {
	var in input
	if err := json.Unmarshal(stdin, &in); err != nil {
		return false, false, err
	}
	if in.Input.Dimension != "personal_compliance" {
		return false, false, nil
	}
	var rule *Rule
	for i, r := range t.Rules {
		if r.Memory.ID == in.Input.Target.ID && r.Memory.Revision == in.Input.Target.Revision {
			rule = &t.Rules[i]
		}
	}
	var b *behavior
	for _, e := range in.Input.Events {
		var v behavior
		if json.Unmarshal(e.Payload, &v) == nil && v.Behavior == "agent_session" {
			b = &v
		}
	}
	if rule == nil || b == nil {
		return false, false, nil
	}
	observed, passed := judge(*rule, *b)
	if b.Incomplete && passed {
		return false, false, nil
	}
	return observed, passed, nil
}

func judge(r Rule, b behavior) (observed, passed bool) {
	passed = true
	unseen := false // an artifact whose text is elsewhere: a clean result there is no pass
	switch r.Kind {
	case "forbidden_text":
		observed, passed, unseen = judgeForbiddenText(r, b)
	case "language":
		observed, passed = judgeLanguage(r, b)
	case "max_length":
		observed, passed = judgeMaxLength(r, b)
	case "required_tail":
		observed, passed = judgeRequiredTail(r, b)
	}
	if unseen && passed {
		return false, false
	}
	return observed, observed && passed
}

func judgeForbiddenText(r Rule, b behavior) (observed, passed, unseen bool) {
	passed = true
	needle := normalize(r.Text)
	for _, a := range r.Artifacts {
		var texts []string
		switch a {
		case "commits":
			texts = b.Commits
		case "pull_requests":
			texts = b.PullRequests
		case "replies":
			for _, rp := range b.Replies {
				texts = append(texts, rp.Text)
			}
		}
		for _, s := range texts {
			observed = true
			passed = passed && !strings.Contains(normalize(s), needle)
			unseen = unseen || a == "commits" && !inline(s, commitSegRE, inlineCommitRE) || a == "pull_requests" && !inline(s, prSegRE, inlinePRRE)
		}
	}
	return observed, passed, unseen
}

func judgeLanguage(r Rule, b behavior) (observed, passed bool) {
	passed = true
	for _, rp := range b.Replies {
		if hangul, latin, letters := letterCounts(rp.Text); letters >= 20 { // too short to call a language
			observed = true
			englishOnly := hangul == 0 && latin == letters
			passed = passed && !englishOnly && (hangul == 0 || float64(hangul)/float64(letters) >= r.MinRatio)
		}
	}
	return observed, passed
}

func judgeMaxLength(r Rule, b behavior) (observed, passed bool) {
	passed = true
	for _, rp := range b.Replies {
		if !rp.DetailRequested {
			observed = true
			passed = passed && rp.Lines <= r.MaxLines
		}
	}
	return observed, passed
}

func judgeRequiredTail(r Rule, b behavior) (observed, passed bool) {
	passed = true
	re := regexp.MustCompile(r.Pattern)
	for _, rp := range b.Replies {
		if rp.Lines >= r.MinLines {
			parts := paragraphRE.Split(strings.TrimSpace(rp.Text), -1)
			observed = true
			passed = passed && re.MatchString(strings.TrimSpace(parts[len(parts)-1]))
		}
	}
	return observed, passed
}

// normalize folds case and whitespace so "generated  with claude\ncode" still matches.
func normalize(s string) string {
	return spaceRE.ReplaceAllString(strings.ToLower(s), " ")
}

// letterCounts counts Hangul, Latin and all letters outside inline code and URLs.
func letterCounts(s string) (hangul, latin, letters int) {
	s = urlRE.ReplaceAllString(inlineCodeRE.ReplaceAllString(s, ""), "")
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) {
			hangul++
			letters++
		} else if unicode.IsLetter(r) {
			letters++
			if unicode.Is(unicode.Latin, r) {
				latin++
			}
		}
	}
	return
}
