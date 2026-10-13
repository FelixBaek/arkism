package memory

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresCodexOpenWriterObservedTime(t *testing.T) {
	f, now := archiveSearchFixture(t, isolatedPostgres(t))
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte(f.human), 0600); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(root, "pc-sessions")
	path := filepath.Join(inbox, "codex-sessions", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	env := arkismEnv(root, server.URL, token, "ARKISM_PC_SESSIONS="+inbox, "ARKISM_SESSION_STATE="+filepath.Join(root, "state.json"))
	collect := func() (map[string]int, error) {
		t.Helper()
		out, err := arkismExec(t, env, "", "session-archive", "collect", "--execute")
		var counts map[string]int
		if e := json.Unmarshal([]byte(strings.SplitN(string(out), "\n", 2)[0]), &counts); e != nil {
			t.Fatalf("collector output: %v %s", e, out)
		}
		return counts, err
	}
	mustCollect := func() {
		t.Helper()
		if counts, err := collect(); err != nil {
			t.Fatal("collect", err, counts)
		}
	}
	old := now.Add(-2 * time.Hour).Truncate(time.Millisecond)
	newer := old.Add(time.Hour)
	row := func(at time.Time, text string) string {
		return string(jsonLine(map[string]any{"timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"), "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}}}))
	}
	write := func(content string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	get := func() SessionArchiveView {
		t.Helper()
		view, err := f.svc.GetSessionArchive(testCtx, f.human, SessionArchiveGetInput{Scope: scopeA, ID: ContentHash("pc\x00codex\x00session\x00session.jsonl")})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	first := row(old, "첫 번째 결정")
	write(first, old)
	mustCollect()
	before := get()
	// The writer keeps the file open: mtime stays old while a newer record is appended.
	write(first+row(newer, "두 번째 결정"), old)
	mustCollect()
	current := get()
	if current.Revision != before.Revision+1 || !current.ObservedAt.Equal(newer) || current.ExpiresAt == nil || !current.ExpiresAt.Equal(newer.Add(SessionArchiveTTL)) {
		t.Fatalf("observed time not from the newest record: %+v", current)
	}
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.human, ArchiveSearchInput{Scope: scopeA, ArchiveID: current.ID, Query: "두 번째 결정", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "두 번째 결정" {
		t.Fatal("appended record not searchable", err)
	}
	// Copying/touching identical bytes must neither invent observation time nor renew TTL.
	if err = os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	mustCollect()
	same := get()
	if same.Revision != current.Revision || !same.ObservedAt.Equal(current.ObservedAt) || same.SHA256 != current.SHA256 || !same.ExpiresAt.Equal(*current.ExpiresAt) {
		t.Fatal("touch renewed the archive")
	}
	// An older synced snapshot cannot replace the newer source, even with fresh mtime.
	write(first, time.Now())
	if counts, err := collect(); err == nil || counts["http_409"] != 1 {
		t.Fatal("old snapshot accepted", counts, err)
	}
	latest := get()
	if latest.SHA256 != current.SHA256 || !latest.ExpiresAt.Equal(*current.ExpiresAt) {
		t.Fatal("old snapshot replaced the archive")
	}
}
