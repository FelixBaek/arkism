package capture

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The layout and row shapes follow DeepSeek CLI's session-persistence-jsonl backend
// (~/.dsh/sessions/--<project>--/<session id>/session.v4.jsonl); every value here is made up.
func TestLogsCaptureDeepseekSessions(t *testing.T) {
	dir := t.TempDir()
	oldState, oldOrigin := capState, capOrigin
	t.Cleanup(func() { capState, capOrigin = oldState, oldOrigin })
	capState, capOrigin = filepath.Join(dir, "state"), "deepseek"
	settings := filepath.Join(dir, "capture.json")
	t.Setenv("ARKISM_CAPTURE_SETTINGS", settings)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	root := filepath.Join(dir, "sessions")
	session := filepath.Join(root, "--work--", "sess-1")
	os.MkdirAll(session, 0o755)
	ms := func(at time.Time) string { return itoa(at.UnixMilli()) }
	header := `{"type":"session","version":4,"id":"sess-1","createdAt":` + ms(now.Add(-6*time.Hour)) + `,"cwd":"` + filepath.ToSlash(work) + `","isSeeded":false,"delegationDepth":0}`
	user := func(id, text string, at time.Time) string {
		return `{"type":"user/message","seq":1,"time":` + ms(at) + `,"data":{"content":[{"type":"text","text":"` + text + `"}],"source":{"kind":"user"},"role":"user","id":"` + id + `"},"surfaceOp":"append"}`
	}
	harness := func(id, text string, at time.Time) string {
		return `{"type":"user/message","seq":1,"time":` + ms(at) + `,"data":{"content":[{"type":"text","text":"` + text + `"}],"source":{"kind":"runtime-context","form":"snapshot","sections":[]},"role":"user","id":"` + id + `"},"surfaceOp":"append"}`
	}
	assistant := func(text string, at time.Time) string {
		return `{"type":"assistant/message","seq":1,"time":` + ms(at) + `,"data":{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}],"source":{"kind":"model","provider":"deepseek-official","model":"deepseek-v4-flash"},"id":"a"}}}`
	}
	turnEnd := func(kind string, at time.Time) string {
		return `{"type":"turn/end","seq":1,"time":` + ms(at) + `,"data":{"turn":1,"reason":{"kind":"` + kind + `"}}}`
	}
	log := filepath.Join(session, "session.v4.jsonl")
	write := func(lines ...string) {
		os.WriteFile(log, []byte(strings.Join(append([]string{header}, lines...), "\n")+"\n"), 0o644)
		os.Chtimes(log, now, now)
	}
	old, recent := now.Add(-5*time.Hour), now.Add(-time.Minute)
	lines := []string{
		user("m1", "옛날 이야기", old), assistant("네", old), turnEnd("completed", old),
		user("m2", "앞으로 커밋 메시지는 한국어로 써줘 <private>토큰 abc</private>", recent),
		harness("m3", "Current runtime context 시스템", recent),
		assistant("확인할게요", recent), turnEnd("completed", recent),
		user("m4", "아직 답 안 받은 말", recent), assistant("도구 호출 중", recent),
	}
	write(lines...)
	// An older generation beside the current one is what dsh left behind on upgrade: not read.
	os.WriteFile(filepath.Join(session, "session.v3.jsonl"), []byte(header+"\n"+user("m9", "옛 세대", recent)+"\n"+turnEnd("completed", recent)+"\n"), 0o644)
	spool := func() []string { s, _ := filepath.Glob(filepath.Join(spoolDir(), "*.json")); return s }
	if n, err := logsCaptureFrom(root, deepseekChatFile, deepseekChat, now); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	raw, _ := os.ReadFile(spool()[0])
	job := string(raw)
	for _, bad := range []string{"옛날", "시스템", "abc", "아직", "옛 세대"} {
		if strings.Contains(job, bad) {
			t.Fatal(bad, job)
		}
	}
	if !strings.Contains(job, "커밋 메시지는 한국어로") || !strings.Contains(job, `"origin": "deepseek"`) || !strings.Contains(job, `"session_id": "sess-1"`) ||
		!strings.Contains(job, filepath.ToSlash(work)) {
		t.Fatal(job)
	}
	if n, _ := logsCaptureFrom(root, deepseekChatFile, deepseekChat, now); n != 0 {
		t.Fatal("queued twice", n)
	}
	// An aborted turn is not an answer; the pending message counts once its turn completes.
	write(append(lines, turnEnd("aborted", recent))...)
	if n, _ := logsCaptureFrom(root, deepseekChatFile, deepseekChat, now); n != 0 {
		t.Fatal("aborted turn captured", n)
	}
	lines = append(lines, turnEnd("completed", now))
	write(lines...)
	if n, _ := logsCaptureFrom(root, deepseekChatFile, deepseekChat, now); n != 1 || len(spool()) != 2 {
		t.Fatal("answered later", n)
	}
	// Paused or excluded: nothing.
	write(append(lines, user("m5", "새 말", now), assistant("응", now), turnEnd("completed", now))...)
	os.WriteFile(settings, []byte(`{"paused":true}`), 0o600)
	if n, _ := logsCaptureFrom(root, deepseekChatFile, deepseekChat, now); n != 0 {
		t.Fatal("paused captured", n)
	}
	os.WriteFile(settings, []byte(`{"exclude":["`+filepath.ToSlash(work)+`"]}`), 0o600)
	if n, _ := logsCaptureFrom(root, deepseekChatFile, deepseekChat, now); n != 0 {
		t.Fatal("excluded captured", n)
	}
	os.WriteFile(settings, []byte(`{}`), 0o600)
	if n, _ := logsCaptureFrom(root, deepseekChatFile, deepseekChat, now); n != 1 {
		t.Fatal("resumed", n)
	}
}

// dsh's default log is checksummed zstd frames: a header frame, one frame per append batch.
// This one (made with Node's zlib.zstdCompressSync, as dsh writes) ends in a torn frame.
const deepseekZstdLog = "KLUv/SR8VQMAgsYWHHCLOsAAotIOrkQPP4Z94TrfQSHr30bTrPrcgAOAB4JW7HODw9aqxJun79ooBDqf/0GXz1FaNAYliPEhimBRkoWQoLeQX/XG+BwFcvkG2M7w2WFL9Wr0n8baEQQATAUJF4bPhNoWMaL8dQcotS/9ZA4A3QUAQkolKpClzQGamekOPWKhSu45qtdZtJTJ3rfd7e13aZuf3XV3b+qrUUOeqE7UBce995GbH0jSZlILuRqUODmZNTruvQuQztkfWs/u43QW3NSI2rPE/VRgIQIDF8JBQkVgIdVkUQYXRV1/7N3gRrz35J60ufQOG8az1lorLApzcMzi3lk6febeGWzPzgpoLcukso3+zhBAXwoNAHhYSc3BVBaA42Aylr2dBjA+jFD+mYI5oRLWU+E6lCKYAXStDE8otS/9JKZ9BABCCB8ogMXpPwMzOKUZJSpSKVzULiayxU92kUVVtm9IpkDSZIaEHWIyBrCCAYE5z0Dg9+5dv4Ci67yLT8FPJEHyipinqitCsShJgACgYk2q66tvB/dh35E7Qs+nfevmrowxVmgYx7A4zbJn+PpK2DdYXs+JJiXDQcRS+p4Zj/4UBQANI8aPJA3BOtWoQyk="

func TestDeepseekChatZstd(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(deepseekZstdLog)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "--work-z--", "sess-z", "session.v4.jsonl.zstd")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, raw, 0o644)
	sid, cwd, msgs, err := deepseekChat(path)
	if err != nil || sid != "sess-z" || cwd != "/work/z" || len(msgs) != 2 {
		t.Fatal(sid, cwd, msgs, err)
	}
	if m := msgs[0]; m.typ != "user" || m.id != "mz1" || m.text != "압축된 로그의 말" || m.at != "2026-10-13T11:59:00Z" || msgs[1].typ != "assistant" {
		t.Fatal(msgs)
	}
	if deepseekGeneration("session.jsonl") != 0 || deepseekGeneration("session.v12.jsonl.zstd") != 12 ||
		deepseekGeneration("session.v0.jsonl") != -1 || deepseekGeneration("session.v4.jsonl.tmp") != -1 {
		t.Fatal("generation names")
	}
}
