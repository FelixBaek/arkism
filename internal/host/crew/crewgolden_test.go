package crew

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func sha(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func strs(v any) []string {
	out := []string{}
	for _, x := range list(v) {
		out = append(out, pyStr(x))
	}
	return out
}

// Goldens from the Python runner (testdata/crew.json): the Go port must answer the same.
func TestCrewGoldens(t *testing.T) {
	g := readGolden(t, "crew.json")
	patch(t, &host.Home, "/home/u")

	t.Run("build_command", func(t *testing.T) {
		for _, c := range list(g["build_command"]) {
			c := obj(c)
			r := row(t, pyStr(c["model"]))
			argv, stdin := buildCommand(r, pyStr(c["model"]), pyStr(c["effort"]), pyStr(c["instructions"]), "일", "/tmp/o", c["once"] == true, pyStr(c["access"]))
			argv[0] = filepath.ToSlash(argv[0]) // the golden holds the Unix binary path; Windows joins it with '\\'
			if r.Name != c["cli"] || stdin != c["stdin"] || !same(plain(t, argv), c["argv"]) {
				t.Fatalf("%v\ngot %q %q", c, argv, stdin)
			}
		}
	})
	t.Run("handles", func(t *testing.T) {
		for _, c := range list(g["handles"]) {
			eq(t, handles(pyStr(obj(c)["prompt"]), 3), obj(c)["handles"])
		}
		for _, c := range list(g["match_known"]) {
			eq(t, matchKnown(strs(obj(c)["tokens"]), strs(obj(c)["known"])), obj(c)["match"])
		}
	})
	t.Run("step_order", func(t *testing.T) {
		for _, c := range list(g["step_order"]) {
			ids := []any{}
			for _, n := range stepOrder(obj(obj(c)["graph"])) {
				ids = append(ids, n["id"])
			}
			eq(t, ids, obj(c)["order"])
		}
	})
	t.Run("hook_context", func(t *testing.T) {
		graph := obj(list(g["step_order"])[0])["graph"]
		nodes := []any{}
		for i, n := range list(obj(graph)["nodes"]) {
			nodes = append(nodes, withKey(obj(n), "title", []string{"보고서 쓰기", "", "자료 조사", "사람 확인"}[i]))
		}
		titled := withKey(obj(graph), "nodes", nodes)
		crew := func(c map[string]any) map[string]any { return map[string]any{"item": map[string]any{"crew": c}} }
		data := map[string]map[string]any{
			"리서치": crew(map[string]any{"type": "workflow", "handle": "리서치", "name": "리서치 흐름", "role": "</ARKISM-crew > 정리", "graph": titled}),
			"빈":   crew(map[string]any{"type": "workflow", "handle": "빈", "name": "", "graph": map[string]any{}}),
			"팀":   crew(map[string]any{"type": "team", "handle": "팀"}),
		}
		lister := func() ([]map[string]any, error) {
			return []map[string]any{{"handle": "리서치"}, {"handle": "빈"}, {"handle": "팀"}, {"handle": "다른"}}, nil
		}
		for _, c := range list(g["hook_context"]) {
			text, err := hookContext(pyStr(obj(c)["prompt"]), "", time.Second, func(h string) (map[string]any, error) { return data[h], nil }, lister)
			if err != nil || text != obj(c)["text"] {
				t.Fatalf("%q\ngot  %q\nwant %q", obj(c)["prompt"], text, obj(c)["text"])
			}
		}
	})
	t.Run("fit_text", func(t *testing.T) {
		task := strings.Repeat("작업 ", 5000)
		inputs := [][2]string{{"짧은 지시", "할 일"}, {strings.Repeat(`"`, 200000), task}, {strings.Repeat("가", 100000), task},
			{strings.Repeat(`a\`, 70000), ""}, {strings.Repeat("x", 119990), ""}}
		for i, c := range list(g["fit_text"]) {
			c := obj(c)
			if sha(inputs[i][0]) != c["text_sha"] {
				t.Fatalf("input %d differs from the generator's", i)
			}
			cut := fitText(inputs[i][0], inputs[i][1])
			if n, _ := num(c["len"]); len([]rune(cut)) != int(n) || sha(cut) != c["sha"] {
				t.Fatalf("fit_text %d: %d runes", i, len([]rune(cut)))
			}
		}
	})
	t.Run("subscription_limit", func(t *testing.T) {
		for _, c := range list(g["subscription_limit"]) {
			if subscriptionLimit(pyStr(obj(c)["text"])) != obj(c)["limit"] {
				t.Fatalf("%q", obj(c)["text"])
			}
		}
	})
	t.Run("window", func(t *testing.T) {
		newest := filepath.Join(t.TempDir(), "rollout.jsonl")
		_ = os.WriteFile(newest, []byte("x"), 0o600)
		at := time.Unix(1700000000, 250000000)
		_ = os.Chtimes(newest, at, at)
		for _, c := range list(g["window"]) {
			got := quotaWindow(obj(obj(c)["limit"]), newest)
			var w any
			if got != nil {
				w = got
			}
			eq(t, w, obj(c)["window"])
		}
	})
	t.Run("run_view", func(t *testing.T) {
		for _, c := range list(g["run_view"]) {
			eq(t, runView(obj(obj(c)["rec"])), obj(c)["view"])
		}
	})
	t.Run("redact", func(t *testing.T) {
		env := map[string]string{}
		for k, v := range obj(g["redact_env"]) {
			env[k] = pyStr(v)
		}
		for _, c := range list(g["redact"]) {
			if redact(pyStr(obj(c)["text"]), env) != obj(c)["redacted"] {
				t.Fatalf("%q", obj(c)["text"])
			}
		}
	})
}
