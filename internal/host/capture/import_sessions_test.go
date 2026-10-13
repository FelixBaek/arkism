package capture

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type fakeImportServer struct {
	imported map[string]bool
	posts    []host.Obj
}

func (s *fakeImportServer) post(t *testing.T) func(string, any, time.Duration) (map[string]any, error) {
	return func(path string, payload any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/import/sessions" {
			t.Fatal("unexpected post", path)
		}
		body := payload.(host.Obj)
		s.posts = append(s.posts, body)
		dry, _ := body.Get("dry_run")
		sessions, _ := body.Get("sessions")
		var out []any
		for _, raw := range sessions.([]any) {
			sess := raw.(host.Obj)
			id := str(field(sess, "id"))
			cands, _ := field(sess, "candidates").([]any)
			status := "would_import"
			items := []any{}
			switch {
			case s.imported[id]:
				status = "already_imported"
			case len(cands) == 0:
				status = "empty"
			case dry != true:
				status = "imported"
				s.imported[id] = true
			}
			for range cands {
				items = append(items, map[string]any{"status": "candidate", "reason": "review_required"})
			}
			out = append(out, map[string]any{"id": id, "status": status, "items": items})
		}
		return map[string]any{"sessions": out}, nil
	}
}

func writeClaudeSession(t *testing.T, dir, project, sid, cwd string, msgs ...string) string {
	t.Helper()
	p := filepath.Join(dir, project, sid+".jsonl")
	os.MkdirAll(filepath.Dir(p), 0o700)
	var b strings.Builder
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for i, m := range msgs {
		ts := at.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		var rec map[string]any
		if i%2 == 0 {
			rec = map[string]any{"type": "user", "timestamp": ts, "cwd": cwd, "message": map[string]any{"role": "user", "content": m}}
		} else {
			rec = map[string]any{"type": "assistant", "timestamp": ts, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": m}}}}
		}
		raw, _ := json.Marshal(rec)
		b.Write(raw)
		b.WriteByte('\n')
	}
	os.WriteFile(p, []byte(b.String()), 0o600)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(p, old, old)
	return p
}

func importFake(t *testing.T) (string, *fakeImportServer) {
	t.Helper()
	capFake(t)
	projects := t.TempDir()
	keepP, keepC, keepZ := capProjects, capCodexSessions, capZcodeDB
	t.Cleanup(func() { capProjects, capCodexSessions, capZcodeDB = keepP, keepC, keepZ })
	capProjects, capCodexSessions, capZcodeDB = projects, t.TempDir(), filepath.Join(t.TempDir(), "none.sqlite")
	capAgentOff = func(string) bool { return false }
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(t.TempDir(), "capture.json"))
	srv := &fakeImportServer{imported: map[string]bool{}}
	capPost = srv.post(t)
	return projects, srv
}

func runImport(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var out, errb bytes.Buffer
	if code := importSessionsMain(args, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err, out.String())
	}
	return v
}

func counts(v map[string]any) map[string]any { return v["counts"].(map[string]any) }

func TestImportSessionsDryRunCallsNoModel(t *testing.T) {
	projects, srv := importFake(t)
	work := t.TempDir()
	writeClaudeSession(t, projects, "-p1", "s1", work, "그거 운영에 바로 올리면 안 돼, 스테이징 먼저", "네 알겠습니다", "다음엔 꼭 확인해", "확인하겠습니다")
	ignored := t.TempDir()
	os.WriteFile(filepath.Join(ignored, ".arkism-ignore"), nil, 0o600)
	writeClaudeSession(t, projects, "-p2", "s2", ignored, "비밀 프로젝트 이야기", "응", "더 이야기", "응")
	writeClaudeSession(t, projects, "-p3", "s3", work, "한 줄만")
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("dry run called the model")
		return nil, nil
	}
	v := runImport(t, "--dry-run")
	c := counts(v)
	if c["found"] != 3.0 || c["to_read"] != 1.0 || c["ignored"] != 1.0 || c["too_short"] != 1.0 || v["mode"] != "dry_run" {
		t.Fatal(c)
	}
	for _, p := range srv.posts {
		if d, _ := p.Get("dry_run"); d != true {
			t.Fatal("dry run wrote")
		}
	}
	if _, err := os.Stat(siLedgerFile()); !os.IsNotExist(err) {
		t.Fatal("dry run wrote the ledger")
	}
}

func TestImportSessionsRunIsResumableAndIdempotent(t *testing.T) {
	projects, srv := importFake(t)
	work := t.TempDir()
	writeClaudeSession(t, projects, "-p1", "s1", work,
		"그거 운영에 바로 올리면 안 돼, 스테이징 먼저 <private>비번 hunter2</private>", "네", "앞으로 기본 브랜치는 main으로 해", "알겠습니다")
	calls := 0
	var sawLog string
	llm = func(system, user, _ string, _ time.Duration, _ string, key string) (any, error) {
		calls++
		sawLog = user
		if !strings.Contains(system, "CANDIDATES") || key != "candidates" {
			t.Fatal("prompt")
		}
		return []any{
			map[string]any{"kind": "lesson", "subject": "deploy", "text": "배포 전에는 스테이징에서 먼저 확인한다.", "message": 0, "quote": "운영에 바로 올리면 안 돼", "reason": "user objected"},
			map[string]any{"kind": "decision", "subject": "git", "text": "기본 브랜치는 main.", "message": 2, "quote": "기본 브랜치는 main으로"},
			map[string]any{"kind": "decision", "subject": "x", "text": "지어낸 것", "message": 2, "quote": "없는 문장"},
			map[string]any{"kind": "lesson", "subject": "x", "text": "범위 밖", "message": 99, "quote": "네"},
			map[string]any{"kind": "memory", "subject": "x", "text": "모르는 종류", "message": 1, "quote": "네"},
		}, nil
	}
	v := runImport(t, "--run")
	if strings.Contains(sawLog, "hunter2") || !strings.Contains(sawLog, "[#0] 사용자:") {
		t.Fatal("log sent to the model", sawLog)
	}
	sessions := v["sessions"].([]any)
	s0 := sessions[0].(map[string]any)
	if s0["candidates"] != 2.0 || s0["status"] != "imported" || s0["stored"] != 2.0 {
		t.Fatal(s0)
	}
	// The sent body: stable hashed id, original times, citations.
	last := srv.posts[len(srv.posts)-1]
	sess := field(last, "sessions").([]any)[0].(host.Obj)
	id := str(field(sess, "id"))
	if id != siSessionID("claude_code", "s1") || !strings.HasPrefix(str(field(sess, "ended_at")), "2026-03-01T10:03:00") ||
		!strings.HasPrefix(str(field(sess, "started_at")), "2026-03-01T10:00:00") {
		t.Fatal("session body", sess)
	}
	cand := field(sess, "candidates").([]any)[0].(host.Obj)
	if field(cand, "quote") != "운영에 바로 올리면 안 돼" || field(cand, "message") != 0 {
		t.Fatal("citation", cand)
	}
	info, err := os.Stat(siLedgerFile())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("ledger", err)
	}
	// Re-run: the ledger skips it, no model call.
	again := runImport(t, "--run")
	if calls != 1 || counts(again)["already_imported"] != 1.0 {
		t.Fatal("re-run read the session again", calls, counts(again))
	}
	// A lost ledger is still harmless: the server says already_imported.
	os.Remove(siLedgerFile())
	runImport(t, "--run")
	if len(srv.imported) != 1 {
		t.Fatal("duplicated on the server")
	}
}

func TestImportSessionsFailureIsRetried(t *testing.T) {
	projects, _ := importFake(t)
	writeClaudeSession(t, projects, "-p1", "s1", t.TempDir(), "하나", "둘", "셋", "넷")
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return nil, runtimeErr("extractor_exit_1")
	}
	v := runImport(t, "--run")
	if counts(v)["failed"] != 1.0 {
		t.Fatal(counts(v))
	}
	l, _ := siLoadLedger()
	if e := l.Sessions[siSessionID("claude_code", "s1")]; e.Status != "failed" || e.Attempts != 1 {
		t.Fatal(e)
	}
	llm = func(string, string, string, time.Duration, string, string) (any, error) { return []any{}, nil }
	runImport(t, "--run")
	l, _ = siLoadLedger()
	if e := l.Sessions[siSessionID("claude_code", "s1")]; e.Status != "done" || e.Attempts != 2 {
		t.Fatal("not retried", e)
	}
}

func TestImportSessionsTailSnapshot(t *testing.T) {
	keep := siMaxChars
	t.Cleanup(func() { siMaxChars = keep })
	siMaxChars = 40
	lines := []string{"사용자: 아주 오래된 첫 메시지입니다", "AI: 답", "사용자: 마지막 교정", "AI: 네"}
	kept, offset, log := siLog(lines)
	if offset == 0 || len(kept) == len(lines) || !strings.Contains(log, "생략") || !strings.Contains(log, "[#3] AI: 네") {
		t.Fatal(offset, log)
	}
	// A citation into the dropped head is refused.
	padded := append(make([]string, offset), kept...)
	got := siCheck([]any{map[string]any{"kind": "lesson", "text": "x", "message": 0, "quote": "첫 메시지"}}, padded, offset)
	if len(got) != 0 {
		t.Fatal("cited a message the model never saw", got)
	}
}

func TestImportSessionsArgs(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{{"--run", "--dry-run"}, {"--run", "--extract"}, {"--since", "어제"}, {"extra"}, {"--limit", "0"}} {
		if importSessionsMain(args, nil, &out, &errb) != 2 {
			t.Fatal("accepted", args)
		}
	}
	if _, err := siSince("2026-01-31"); err != nil {
		t.Fatal(err)
	}
}

func TestImportSessionsExtractPreviewStoresNothing(t *testing.T) {
	projects, srv := importFake(t)
	writeClaudeSession(t, projects, "-p1", "s1", t.TempDir(), "운영에 바로 올리지 마", "네", "알겠지?", "네")
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return []any{map[string]any{"kind": "lesson", "subject": "d", "text": "운영 전 스테이징.", "message": 0, "quote": "운영에 바로"}}, nil
	}
	v := runImport(t, "--dry-run", "--extract")
	s0 := v["sessions"].([]any)[0].(map[string]any)
	if v["mode"] != "dry_run_extract" || s0["status"] != "would_import" || len(s0["proposals"].([]any)) != 1 || len(srv.imported) != 0 {
		t.Fatal(v)
	}
	if _, err := os.Stat(siLedgerFile()); !os.IsNotExist(err) {
		t.Fatal("preview wrote the ledger")
	}
}
