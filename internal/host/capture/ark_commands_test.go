package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func arkHook(t *testing.T, prompt string) (map[string]any, string) {
	t.Helper()
	in, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-ark", "cwd": t.TempDir(), "prompt": prompt})
	var out bytes.Buffer
	if code := hookMain(bytes.NewReader(in), &out); code != 0 {
		t.Fatal("hook exit", code)
	}
	line := strings.TrimSpace(strings.Split(out.String(), "\n")[0])
	if line == "" {
		return nil, ""
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		t.Fatal(err, out.String())
	}
	return v, out.String()
}

func TestArkParse(t *testing.T) {
	for _, c := range []struct {
		prompt, name, arg string
		ok                bool
	}{
		{"ark:help", "help", "", true},
		{"  ARK:Status ", "status", "", true},
		{"ark:status 가 뭐야?", "", "", false},
		{"ark:remember 배포 전에 스테이징", "remember", "배포 전에 스테이징", true},
		{"ark:remember\n여러 줄\n내용", "remember", "여러 줄\n내용", true},
		{"ark:forget mem_1", "forget", "mem_1", true},
		{"ark:wat", "wat", "", true},
		{"이건 ark:help 가 아님", "", "", false},
		{"ark: help", "", "", false},
	} {
		name, arg, ok := arkParse(c.prompt)
		if ok != c.ok || ok && (name != c.name || arg != c.arg) {
			t.Errorf("%q: got %q %q %v", c.prompt, name, arg, ok)
		}
	}
}

func TestArkHelpBlocksWithoutRecallOrCapture(t *testing.T) {
	capFake(t)
	capOnPrompt = true // even with prompt capture on, a command is not captured
	search = func(string, string, int, float64) ([]any, bool, error) { t.Fatal("recall ran for a command"); return nil, true, nil }
	v, _ := arkHook(t, "ark:help")
	if v["decision"] != "block" || !strings.Contains(str(v["reason"]), "ark:remember") || v["systemMessage"] != nil {
		t.Fatal("help output", v)
	}
	if n := countJSON(spoolDir()); n != 0 {
		t.Fatal("command was spooled for capture", n)
	}
	capOrigin = "codex"
	v, _ = arkHook(t, "ark:nope")
	if v["decision"] != "block" || v["systemMessage"] != v["reason"] || !strings.Contains(str(v["reason"]), "알 수 없는") {
		t.Fatal("codex output", v)
	}
}

func TestArkPassThroughKeepsNormalHook(t *testing.T) {
	capFake(t)
	called := false
	search = func(string, string, int, float64) ([]any, bool, error) { called = true; return []any{}, true, nil }
	if v, _ := arkHook(t, "ark:status 는 무슨 뜻이야?"); v != nil && v["decision"] != nil {
		t.Fatal("ordinary prompt was blocked", v)
	}
	if !called {
		t.Fatal("ordinary prompt skipped recall")
	}
}

func TestArkRememberCreatesCandidateOnly(t *testing.T) {
	capFake(t)
	var sent host.Obj
	capPost = func(path string, payload any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/capture" {
			t.Fatal("path", path)
		}
		sent = payload.(host.Obj)
		return map[string]any{"items": []any{map[string]any{"status": "candidate", "reason": "review_required", "candidate_id": "cand_1"}}}, nil
	}
	v, _ := arkHook(t, "ark:remember 배포 전에는 <private>내 비번 hunter2</private> 스테이징 확인")
	if !strings.Contains(str(v["reason"]), "cand_1") || !strings.Contains(str(v["reason"]), "승인") {
		t.Fatal("reply", v)
	}
	claims := field(sent, "claims").([]any)
	claim := claims[0].(host.Obj)
	text := str(field(claim, "text"))
	if strings.Contains(text, "hunter2") || !strings.Contains(text, privateMark) || field(claim, "basis") != "inferred" {
		t.Fatal("claim", claim)
	}
	if ev := field(sent, "event"); field(ev, "type") != "explicit_remember" {
		t.Fatal("event", ev)
	}
	// The server's secret verdict is shown, not hidden.
	capPost = func(string, any, time.Duration) (map[string]any, error) {
		return map[string]any{"items": []any{map[string]any{"status": "rejected", "reason": "secret_detected"}}}, nil
	}
	if v, _ := arkHook(t, "ark:remember 키는 sk-ant-xxx"); !strings.Contains(str(v["reason"]), "비밀값") {
		t.Fatal("secret reply", v)
	}
}

func TestArkFailsOpenWithMessage(t *testing.T) {
	capFake(t)
	capPost = func(string, any, time.Duration) (map[string]any, error) { return nil, errors.New("boom") }
	v, _ := arkHook(t, "ark:remember 무언가")
	if v["decision"] != "block" || !strings.Contains(str(v["reason"]), "실패") {
		t.Fatal("error reply", v)
	}
}

func TestArkStatusCountsThisSession(t *testing.T) {
	dir := capFake(t)
	rows := []string{
		`{"event":"recall","hook":"SessionStart","session":"sess-ark","injected":2,"ids":["m1","m2"]}`,
		`{"event":"inject","hook":"SessionStart","session":"sess-ark","chars":120}`,
		`{"event":"recall","hook":"UserPromptSubmit","session":"sess-ark","injected":1,"ids":["m2"]}`,
		`{"event":"recall","hook":"UserPromptSubmit","session":"other","injected":5,"ids":["x"]}`,
	}
	os.WriteFile(filepath.Join(dir, "metrics.jsonl"), []byte(strings.Join(rows, "\n")+"\n"), 0o600)
	v, _ := arkHook(t, "ark:status")
	reason := str(v["reason"])
	for _, want := range []string{"m1, m2", "서로 다른 기억 2개", "120자", "세션 시작 목차: 1회, 기억 2개"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("status lacks %q:\n%s", want, reason)
		}
	}
	if strings.Contains(reason, "x") && strings.Contains(reason, " x") {
		t.Fatal("other session leaked", reason)
	}
}

func TestArkSyncSpawnsBackground(t *testing.T) {
	capFake(t)
	keep := arkSpawn
	t.Cleanup(func() { arkSpawn = keep })
	var ran []string
	arkSpawn = func(args ...string) error { ran = append(ran, strings.Join(args, " ")); return nil }
	v, _ := arkHook(t, "ark:sync")
	if len(ran) != 2 || ran[0] != "sync-skills --execute" || ran[1] != "agents sync" || !strings.Contains(str(v["reason"]), "백그라운드") {
		t.Fatal(ran, v)
	}
}

func TestArkForgetOpensPrivacyDeletionRequest(t *testing.T) {
	capFake(t)
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/memories/mem_1?"):
			return map[string]any{"id": "mem_1", "revision": 3, "source_event_id": "src_b",
				"sources": []any{map[string]any{"source_id": "src_a"}, map[string]any{"source_id": "src_b"}}}, nil
		case strings.HasPrefix(path, "/v1/memory_candidates/cand_nosrc?"):
			return map[string]any{"id": "cand_nosrc", "revision": 1}, nil
		}
		return nil, &host.HTTPError{Code: 404}
	}
	var posts []host.Obj
	capPost = func(path string, payload any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/privacy/deletions" {
			t.Fatal("forget must only open a deletion request, got", path)
		}
		posts = append(posts, payload.(host.Obj))
		return map[string]any{"id": "pd_42", "privacy_deletion": map[string]any{"state": "requested"}}, nil
	}
	v, _ := arkHook(t, "ark:forget mem_1")
	if !strings.Contains(str(v["reason"]), "pd_42 (requested)") || !strings.Contains(str(v["reason"]), "근거 원본 2개") {
		t.Fatal(v)
	}
	body := posts[0]
	targets := field(body, "target_ids").([]any)
	if field(body, "target_type") != "source_event" || len(targets) != 2 || targets[0] != "src_a" || targets[1] != "src_b" ||
		!strings.HasPrefix(str(field(body, "idempotency_key")), "ark-forget-") || field(body, "reason") != arkForgetReason {
		t.Fatal("request body", body)
	}
	// The same memory revision reuses the same idempotency key, so the server returns the same request.
	arkHook(t, "ark:forget mem_1")
	if field(posts[1], "idempotency_key") != field(body, "idempotency_key") {
		t.Fatal("repeat opened a different request")
	}
	if v, _ := arkHook(t, "ark:forget cand_nosrc"); !strings.Contains(str(v["reason"]), "근거 원본 기록이 없어") || len(posts) != 2 {
		t.Fatal("no-source memory", v)
	}
	if v, _ := arkHook(t, "ark:forget nope"); !strings.Contains(str(v["reason"]), "찾지 못했") {
		t.Fatal("missing id", v)
	}
	if v, _ := arkHook(t, "ark:forget ../etc"); !strings.Contains(str(v["reason"]), "id 하나") {
		t.Fatal("bad id", v)
	}
	// Server refusal: fail-open, the session goes on with a short message.
	capPost = func(string, any, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 403} }
	if v, _ := arkHook(t, "ark:forget mem_1"); v["decision"] != "block" || !strings.Contains(str(v["reason"]), "열지 못했습니다") {
		t.Fatal("refusal", v)
	}
}

func TestArkAckForRelay(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_CAPTURE_ACK", "1")
	_, raw := arkHook(t, "ark:help")
	if !strings.Contains(raw, `"arkismDelivery": {"accepted": true}`) && !strings.Contains(raw, `"accepted":true`) {
		t.Fatal("relay receipt missing", raw)
	}
}
