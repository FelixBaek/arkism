package memory

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresSessionArchiveCopyRetention(t *testing.T) {
	p := isolatedPostgres(t)
	f, initial := archiveSearchFixture(t, p)
	f.auth.actors[f.agent] = actor("copy-retention-agent", "agent", true, "read", "use")
	// The PC exporter and the server's retention check both use the real clock, so the
	// scenario ends (copy expired) at the real present.
	start := initial.Add(-14 * 24 * time.Hour)
	var clock atomic.Int64
	clock.Store(start.UnixNano())
	f.svc.SessionArchiveNow = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	sid := "copy-retention-session"
	id := ContentHash("pc\x00zcode\x00" + sid + "\x00db-session")
	raw := []byte(structuredText(map[string]any{"session": map[string]any{"id": sid, "time_updated": start.UnixMilli()}}) + "\n")
	hash := ContentHash(string(raw))
	chunk, e := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{Scope: scopeA, ParentID: id, SHA256: hash, Data: base64.StdEncoding.EncodeToString(raw)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.CommitSessionArchive(testCtx, f.human, SessionArchiveCommitInput{Scope: scopeA, ID: id, SessionID: sid, Device: "pc", Origin: "zcode", Format: "reconstructed_zcode", ObservedAt: start, SHA256: hash, Chunks: []string{chunk.ID}}); e != nil {
		t.Fatal(e)
	}
	src := f.source()
	memory := Record{ID: ID(), Scope: scopeA, Status: statusActive, Kind: "decision", Revision: 1, Text: "사용한 기억", Sources: refs(src), Tags: []string{"session:" + sid}}
	if e = p.Insert(testCtx, colMemories, memory); e != nil {
		t.Fatal(e)
	}
	clock.Store(start.Add(6 * 24 * time.Hour).UnixNano())
	if _, e = f.svc.SessionArchiveUsed(testCtx, f.agent, SessionArchiveUsedInput{Scope: scopeA, ID: id, MemoryID: memory.ID, EventID: "actual-use"}); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	root := t.TempDir()
	out := filepath.Join(root, "out")
	if e = os.Mkdir(out, 0700); e != nil {
		t.Fatal(e)
	}
	copyPath := filepath.Join(out, ContentHash(sid)+".jsonl")
	if e = os.WriteFile(copyPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	token := filepath.Join(root, "token")
	if e = os.WriteFile(token, []byte(f.human), 0600); e != nil {
		t.Fatal(e)
	}
	// PC export-zcode asks the server's archive-retention over SSH; the fake ssh runs it here.
	env := arkismEnv(root, server.URL, token, "PATH="+fakeTool(t, "ssh")+string(os.PathListSeparator)+os.Getenv("PATH"), "ARKISM_SERVER_BIN="+arkismBinary(t))
	call := func() map[string]int {
		t.Helper()
		body := arkismRun(t, env, "", "export-zcode", "--database", filepath.Join(root, "missing.sqlite"), "--output", out, "--execute")
		var counts map[string]int
		if json.Unmarshal(body, &counts) != nil {
			t.Fatalf("invalid counts %s", body)
		}
		return counts
	}
	clock.Store(start.Add(8 * 24 * time.Hour).UnixNano())
	before, e := p.Get(testCtx, colSessionArchives, scopeA, id)
	if e != nil {
		t.Fatal(e)
	}
	if call()["retention_active"] != 1 {
		t.Fatal("renewed copy not preserved")
	}
	if got, e := os.ReadFile(copyPath); e != nil || string(got) != string(raw) {
		t.Fatal("renewed copy changed", e)
	}
	after, e := p.Get(testCtx, colSessionArchives, scopeA, id)
	if e != nil || !after.ExpiresAt.Equal(*before.ExpiresAt) || after.Revision != before.Revision {
		t.Fatal("retention query renewed", e)
	}
	clock.Store(start.Add(14 * 24 * time.Hour).UnixNano())
	if call()["expired_copies"] != 1 {
		t.Fatal("expired copy not removed")
	}
	if _, e = os.Stat(copyPath); !os.IsNotExist(e) {
		t.Fatal("copy remains", e)
	}
	if _, e = p.Get(testCtx, colMemories, scopeA, memory.ID); e != nil {
		t.Fatal("memory removed", e)
	}
	// Restored identical copies do not get a new lifetime from mtime.
	if e = os.WriteFile(copyPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if call()["expired_copies"] != 1 {
		t.Fatal("restored copy revived")
	}
}
