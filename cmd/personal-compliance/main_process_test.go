package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// With PERSONAL_COMPLIANCE_TEST_ARGS set, the test binary runs main() with those
// arguments (a JSON array), so exit codes and stdout are checked as the real binary.
func TestMain(m *testing.M) {
	if raw, ok := os.LookupEnv("PERSONAL_COMPLIANCE_TEST_ARGS"); ok {
		var args []string
		if json.Unmarshal([]byte(raw), &args) != nil {
			os.Exit(99)
		}
		os.Args = append([]string{"personal-compliance"}, args...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runMain(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	raw, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "PERSONAL_COMPLIANCE_TEST_ARGS="+string(raw))
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), 0
}

func sessionInput(id string, rev int, b map[string]any) string {
	b["behavior"] = "agent_session"
	raw, _ := json.Marshal(map[string]any{"input": map[string]any{"dimension": "personal_compliance",
		"target": map[string]any{"id": id, "revision": rev}, "events": []any{map[string]any{"payload": b}}}})
	return string(raw)
}

func TestBinaryPrintsVerdictAndRefusesBadInput(t *testing.T) {
	rules := filepath.Join("testdata", "rules.json")
	out, code := runMain(t, sessionInput(lenRule, 2, map[string]any{"replies": []any{replyOf("짧은 답", 3, false)}}), rules)
	if code != 0 || strings.TrimSpace(out) != `{"cost":0,"output":{"observed":true,"passed":true}}` {
		t.Fatalf("pass verdict: %d %q", code, out)
	}
	out, code = runMain(t, sessionInput(lenRule, 2, map[string]any{"replies": []any{replyOf("자세히", 9, true)}}), rules)
	if code != 0 || strings.TrimSpace(out) != `{"cost":0,"output":{"observed":false,"passed":null}}` {
		t.Fatalf("unobserved verdict: %d %q", code, out)
	}

	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name  string
		stdin string
		args  []string
		code  int
	}{
		{"no rules argument", "", nil, 2},
		{"two arguments", "", []string{rules, rules}, 2},
		{"missing rules file", "", []string{filepath.Join(dir, "absent.json")}, 1},
		{"rules not JSON", "", []string{write("bad.json", "{")}, 1},
		{"wrong table version", "", []string{write("v2.json", `{"version":2,"rules":[]}`)}, 1},
		{"invalid rule", "", []string{write("rule.json", `{"version":1,"rules":[{"memory":{"id":"r","revision":1},"kind":"max_length"}]}`)}, 1},
		{"input not JSON", "{", []string{rules}, 1},
	}
	for _, c := range cases {
		if _, code := runMain(t, c.stdin, c.args...); code != c.code {
			t.Errorf("%s: exit %d, want %d", c.name, code, c.code)
		}
	}
}

func TestValidateRefusesIncompleteRules(t *testing.T) {
	rule := func(kind string, edit func(*Rule)) Table {
		r := Rule{Kind: kind}
		r.Memory.ID, r.Memory.Revision = "m", 1
		edit(&r)
		return Table{Version: 1, Rules: []Rule{r}}
	}
	bad := map[string]Table{
		"no memory revision":  rule("max_length", func(r *Rule) { r.MaxLines, r.Memory.Revision = 3, 0 }),
		"forbidden no text":   rule("forbidden_text", func(r *Rule) { r.Artifacts = []string{"commits"} }),
		"forbidden no target": rule("forbidden_text", func(r *Rule) { r.Text = "x" }),
		"forbidden unknown":   rule("forbidden_text", func(r *Rule) { r.Text, r.Artifacts = "x", []string{"issues"} }),
		"language ratio":      rule("language", func(r *Rule) { r.MinRatio = 1.5 }),
		"tail bad pattern":    rule("required_tail", func(r *Rule) { r.Pattern, r.MinLines = "(", 1 }),
		"tail no min lines":   rule("required_tail", func(r *Rule) { r.Pattern = "x" }),
		"unknown kind":        rule("tone", func(*Rule) {}),
	}
	for name, table := range bad {
		if validate(table) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	good := rule("forbidden_text", func(r *Rule) { r.Text, r.Artifacts = "x", []string{"commits", "pull_requests", "replies"} })
	if err := validate(good); err != nil {
		t.Fatal(err)
	}
}

func TestCheckIgnoresOtherDimensionsAndReplyArtifacts(t *testing.T) {
	var table Table
	raw, _ := os.ReadFile(filepath.Join("testdata", "rules.json"))
	if json.Unmarshal(raw, &table) != nil {
		t.Fatal("testdata/rules.json invalid")
	}
	other, _ := json.Marshal(map[string]any{"input": map[string]any{"dimension": "usefulness"}})
	if observed, passed, err := check(table, other); observed || passed || err != nil {
		t.Fatal("another dimension was judged", observed, passed, err)
	}
	r := Rule{Kind: "forbidden_text", Text: "Generated", Artifacts: []string{"replies"}}
	if observed, passed := judge(r, behavior{Replies: []reply{{Text: "generated here"}}}); !observed || passed {
		t.Fatal("reply violation missed", observed, passed)
	}
	if observed, passed := judge(r, behavior{Replies: []reply{{Text: "clean"}}}); !observed || !passed {
		t.Fatal("clean reply failed", observed, passed)
	}
}
