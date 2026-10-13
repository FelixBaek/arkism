package capture

import (
	"os"
	"path/filepath"
	"testing"

	sqlite "github.com/ncruces/go-sqlite3"
)

func TestUsageReadsGeminiChats(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj", "chats")
	os.MkdirAll(dir, 0o755)
	jsonl := filepath.Join(dir, "session-2026-10-13T01-00-abcd.jsonl")
	os.WriteFile(jsonl, []byte(`{"sessionId":"g1","projectHash":"h","startTime":"2026-10-13T01:00:00Z","lastUpdated":"2026-10-13T01:02:00Z","directories":["/home/u/work/arkism"]}
{"id":"m1","timestamp":"2026-10-13T01:00:01Z","type":"user","content":[{"text":"hi"}]}
{"id":"m2","timestamp":"2026-10-13T01:00:05Z","type":"gemini","content":[],"model":"gemini-3-pro"}
{"id":"m2","timestamp":"2026-10-13T01:00:06Z","type":"gemini","content":[],"model":"gemini-3-pro","toolCalls":[{"id":"t","name":"read_file","args":{},"status":"success","timestamp":"2026-10-13T01:00:06Z"}],"tokens":{"input":1000,"output":50,"cached":400,"thoughts":10,"total":1060}}
{"$set":{"summary":"x"}}
{"id":"m3","timestamp":"2026-10-13T01:01:00Z","type":"user","content":[{"text":"again"}]}
{"id":"m4","timestamp":"2026-10-13T01:01:00Z","type":"info","content":"note"}
`), 0o644)
	f, err := usageParse("gemini", "pc", jsonl)
	if err != nil || len(f.Sessions) != 1 {
		t.Fatal(err, f.Sessions)
	}
	s := f.Sessions[0]
	if s.Sid != "g1" || s.Project != "arkism" || s.Messages != 2 || len(s.Tools) != 1 || s.Tools[0].K != "read_file" {
		t.Fatalf("%+v", s)
	}
	if len(s.Days) != 1 || len(s.Days[0].Models) != 1 {
		t.Fatalf("%+v", s.Days)
	}
	m := s.Days[0].Models[0]
	if m.Model != "gemini-3-pro" || m.T != (usageTok{Input: 600, CacheRead: 400, Output: 60, Requests: 1}) {
		t.Fatalf("%+v", m)
	}

	legacy := filepath.Join(dir, "session-old.json")
	os.WriteFile(legacy, []byte(`{
  "sessionId": "g0", "projectHash": "h", "startTime": "2026-10-01T00:00:00Z", "lastUpdated": "2026-10-01T00:01:00Z",
  "messages": [
    {"id": "a", "timestamp": "2026-10-01T00:00:01Z", "type": "user", "content": "hello"},
    {"id": "b", "timestamp": "2026-10-01T00:00:02Z", "type": "gemini", "content": "", "model": "gemini-2.5-pro", "tokens": {"input": 10, "output": 5, "cached": 0, "total": 15}}
  ]
}`), 0o644)
	f, err = usageParse("gemini", "pc", legacy)
	if err != nil || len(f.Sessions) != 1 || f.Sessions[0].Sid != "g0" || f.Sessions[0].Messages != 1 || len(f.Sessions[0].Days) != 1 {
		t.Fatal(err, f.Sessions)
	}
}

func TestUsageReadsZcodeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE session (id TEXT, title TEXT, directory TEXT, time_created INTEGER, time_archived INTEGER)`,
		`CREATE TABLE message (id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT, session_id TEXT, data TEXT)`,
		`INSERT INTO session VALUES ('s1', '바탕화면 정리', 'C:\Users\u\work\site', 1, NULL), ('s0', 'old', '/x', 0, 5)`,
		`INSERT INTO message VALUES ('m1', 's1', 1, '{"role":"user","time":{"created":1791302936235}}'),
			('m2', 's1', 2, '{"role":"assistant","time":{"created":1791302940000},"modelId":"glm-5","tokens":{"input":100,"output":20,"reasoning":5,"cache":{"read":300,"write":0}}}'),
			('m3', 's1', 3, '{"role":"user","synthetic":true,"time":{"created":1791302950000}}'),
			('m4', 's0', 1, '{"role":"user","time":{"created":1}}')`,
		`INSERT INTO part VALUES ('p1', 's1', '{"type":"tool","tool":"WebSearch"}'), ('p2', 's1', '{"type":"text","text":"hi"}')`,
	} {
		if err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	f, err := usageParse("zcode", "pc", path)
	if err != nil || len(f.Sessions) != 1 {
		t.Fatal(err, f.Sessions)
	}
	s := f.Sessions[0]
	if s.Sid != "s1" || s.Title != "바탕화면 정리" || s.Project != "site" || s.Messages != 1 || len(s.Tools) != 1 || s.Tools[0].K != "WebSearch" {
		t.Fatalf("%+v", s)
	}
	if len(s.Days) != 1 || s.Days[0].Models[0].Model != "glm-5" || s.Days[0].Models[0].T != (usageTok{Input: 100, Output: 25, CacheRead: 300, Requests: 1}) {
		t.Fatalf("%+v", s.Days)
	}
}
