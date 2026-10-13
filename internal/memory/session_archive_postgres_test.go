package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresSessionArchiveLifecycle(t *testing.T) { archiveLifecycle(t, isolatedPostgres(t)) }

func TestPostgresSessionArchiveUseExpiryRace(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	f.svc.SessionArchiveKey = bytes.Repeat([]byte{9}, 32)
	f.auth.actors[f.human] = actor("owner", "human", true, "read", "write", "use", "expire")
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "archive-race", ProjectID: ID()}
	parent := ContentHash(scope.ProjectID)
	chunk, err := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{Scope: scope, ParentID: parent, SHA256: ContentHash("race"), Data: base64.StdEncoding.EncodeToString([]byte("race"))})
	if err != nil {
		t.Fatal(err)
	}
	header, err := f.svc.CommitSessionArchive(testCtx, f.human, SessionArchiveCommitInput{Scope: scope, ID: parent, SessionID: "race", Device: "server", Origin: "codex", Format: "jsonl", ObservedAt: now, SHA256: ContentHash("race"), Chunks: []string{chunk.ID}})
	if err != nil {
		t.Fatal(err)
	}
	m := Record{ID: ID(), Scope: scope, Kind: "decision", Status: "active", Revision: 1, Tags: []string{"session:race"}, CreatedAt: now, UpdatedAt: now}
	if err := p.Insert(testCtx, "memories", m); err != nil {
		t.Fatal(err)
	}
	useService := &Service{Repo: p, Auth: f.auth, SessionArchiveNow: func() time.Time { return header.ExpiresAt.Add(-time.Millisecond) }}
	expireService := &Service{Repo: p, Auth: f.auth, SessionArchiveNow: func() time.Time { return *header.ExpiresAt }}
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var used SessionArchiveUsedOutput
	var expired SessionArchiveExpireOutput
	var useErr, expireErr error
	go func() {
		defer wait.Done()
		<-start
		used, useErr = useService.SessionArchiveUsed(testCtx, f.human, SessionArchiveUsedInput{Scope: scope, ID: parent, MemoryID: m.ID, EventID: "actual"})
	}()
	go func() {
		defer wait.Done()
		<-start
		expired, expireErr = expireService.ExpireSessionArchives(testCtx, f.human, SessionArchiveExpireInput{Scope: scope})
	}()
	close(start)
	wait.Wait()
	if useErr == nil && used.Extended {
		if len(expired.Expired) > 0 {
			t.Fatal("renewed archive was erased")
		}
		if _, err := p.Get(testCtx, colSessionArchiveChunks, scope, chunk.ID); err != nil {
			t.Fatal("renewed bytes missing")
		}
	} else if expireErr == nil && len(expired.Expired) == 1 {
		if _, err := p.Get(testCtx, colSessionArchiveChunks, scope, chunk.ID); err != ErrNotFound {
			t.Fatal("expired bytes remain")
		}
	} else {
		t.Fatalf("neither operation committed: use=%v expire=%v", useErr, expireErr)
	}
}

func TestSessionArchiveLifecycle(t *testing.T) { archiveLifecycle(t, archiveFake{newFakeRepo()}) }

func TestSessionArchiveRepeatedChunksPermissionsAndOrphans(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = archiveFake{f.repo}
	f.svc.SessionArchiveKey = bytes.Repeat([]byte{1}, 32)
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.svc.SessionArchiveNow = func() time.Time { return now }
	parent := ContentHash("repeat")
	chunk, err := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{Scope: scopeA, ParentID: parent, SHA256: ContentHash("x"), Data: base64.StdEncoding.EncodeToString([]byte("x"))})
	if err != nil {
		t.Fatal(err)
	}
	input := SessionArchiveCommitInput{Scope: scopeA, ID: parent, SessionID: "repeat", Device: "server", Origin: "codex", Format: "jsonl", ObservedAt: now, SHA256: ContentHash("xx"), Chunks: []string{chunk.ID, chunk.ID}}
	if _, err := f.svc.CommitSessionArchive(testCtx, f.human, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReadSessionArchiveChunk(testCtx, f.agent, SessionArchiveReadInput{Scope: scopeA, ID: parent, ChunkID: chunk.ID}); err != ErrDenied {
		t.Fatalf("agent raw allowed: %v", err)
	}
	if _, err := f.svc.ExpireSessionArchives(testCtx, f.agent, SessionArchiveExpireInput{Scope: scopeA}); err != ErrDenied {
		t.Fatalf("agent expiry allowed: %v", err)
	}
	if _, err := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{Scope: scopeA, ParentID: ContentHash("orphan"), SHA256: ContentHash("x"), Data: base64.StdEncoding.EncodeToString([]byte("x"))}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(SessionArchiveTTL)
	out, err := f.svc.ExpireSessionArchives(testCtx, f.human, SessionArchiveExpireInput{Scope: scopeA})
	if err != nil || len(out.Expired) != 1 || out.Orphans != 1 {
		t.Fatalf("expiry=%+v err=%v", out, err)
	}
}

func archiveLifecycle(t *testing.T, p Repository) {
	f := newFixture(t)
	f.svc.Repo = p
	f.svc.SessionArchiveKey = bytes.Repeat([]byte{7}, 32)
	f.auth.actors[f.human] = actor("owner", "human", true, "read", "write", "use", "expire")
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.svc.SessionArchiveNow = func() time.Time { return now }
	scope := Scope{WorkspaceID: "session-archive-test", ProjectID: ID()}
	parent := ContentHash("archive-" + scope.ProjectID)
	raw := []byte("original transcript with exact bytes\n")
	chunk, err := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{
		Scope: scope, ParentID: parent, SHA256: ContentHash(string(raw)), Data: base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	in := SessionArchiveCommitInput{Scope: scope, ID: parent, SessionID: "session-one", Device: "server", Origin: "codex", Format: "jsonl", ObservedAt: now.Add(-6 * 24 * time.Hour), SHA256: ContentHash(string(raw)), Chunks: []string{chunk.ID}, Text: "safe projection"}
	created, err := f.svc.CommitSessionArchive(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.ListSessionArchives(testCtx, f.human, SessionArchiveListInput{Scope: scope, Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list count=%d err=%v", len(page.Items), err)
	}
	read, err := f.svc.ReadSessionArchiveChunk(testCtx, f.human, SessionArchiveReadInput{Scope: scope, ID: parent, ChunkID: chunk.ID})
	if err != nil || read.Data != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("raw round trip failed: %v", err)
	}
	memory := Record{ID: ID(), Scope: scope, Status: "active", Kind: "decision", Revision: 1, Text: "durable decision", Tags: []string{"session:session-one"}, CreatedAt: now, UpdatedAt: now}
	if err := p.Insert(testCtx, "memories", memory); err != nil {
		t.Fatal(err)
	}
	used := SessionArchiveUsedInput{Scope: scope, ID: parent, MemoryID: memory.ID, EventID: "actual-answer-one"}
	first, err := f.svc.SessionArchiveUsed(testCtx, f.human, used)
	if err != nil || !first.Extended || !first.ExpiresAt.After(*created.ExpiresAt) {
		t.Fatalf("use failed: %v", err)
	}
	now = now.Add(time.Hour)
	duplicate, err := f.svc.SessionArchiveUsed(testCtx, f.human, used)
	if err != nil || duplicate.Extended || !duplicate.ExpiresAt.Equal(*first.ExpiresAt) {
		t.Fatalf("duplicate extended: %v", err)
	}
	now = first.ExpiresAt.Add(time.Millisecond)
	view, err := f.svc.GetSessionArchive(testCtx, f.human, SessionArchiveGetInput{Scope: scope, ID: parent})
	if err != nil || view.State != "expired" || view.Text != "" {
		t.Fatalf("expired projection leaked before sweep: %v", err)
	}
	expired, err := f.svc.ExpireSessionArchives(testCtx, f.human, SessionArchiveExpireInput{Scope: scope, Limit: 100})
	if err != nil || len(expired.Expired) != 1 {
		t.Fatalf("expiry failed: %v", err)
	}
	if _, err := p.Get(testCtx, colSessionArchiveChunks, scope, chunk.ID); err != ErrNotFound {
		t.Fatalf("chunk retained: %v", err)
	}
	kept, err := p.Get(testCtx, "memories", scope, memory.ID)
	if err != nil || kept.Text != memory.Text {
		t.Fatalf("memory changed: %v", err)
	}
}

type archiveFake struct{ *fakeRepo }

func (f archiveFake) SessionArchiveRows(ctx context.Context, scope Scope, after string, limit int, q string) ([]Record, error) {
	rows, _ := f.ListRecords(ctx, scope, colSessionArchives, 1<<30)
	out := []Record{}
	for _, r := range rows {
		if r.ID > after && live(r) && strings.Contains(r.Text, q) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f archiveFake) SessionArchiveExpiredRows(ctx context.Context, scope Scope, now time.Time, limit int) ([]Record, error) {
	rows, _ := f.ListRecords(ctx, scope, colSessionArchives, 1<<30)
	out := []Record{}
	for _, r := range rows {
		if sessionArchiveDueForExpiry(r, now) {
			out = append(out, r)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f archiveFake) SessionArchiveOrphanRows(ctx context.Context, scope Scope, now time.Time, limit int) ([]Record, error) {
	rows, _ := f.ListRecords(ctx, scope, colSessionArchiveChunks, 1<<30)
	out := []Record{}
	for _, r := range rows {
		if r.ExpiresAt == nil || r.ExpiresAt.After(now) {
			continue
		}
		h, e := f.Get(ctx, colSessionArchives, scope, r.SessionArchive.ParentID)
		found := false
		if e == nil && h.SessionArchive != nil {
			for _, id := range h.SessionArchive.Chunks {
				if id == r.ID {
					found = true
				}
			}
		}
		if !found {
			out = append(out, r)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f archiveFake) SessionArchiveChunkExtents(ctx context.Context, scope Scope, parent string, ids []string) ([]archiveChunkExtent, error) {
	seen := map[string]bool{}
	var out []archiveChunkExtent
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := f.Get(ctx, colSessionArchiveChunks, scope, id)
		if err != nil {
			return nil, err
		}
		if !isSessionArchiveChunkFor(r, parent) {
			return nil, ErrConflict
		}
		cipher := r.SessionArchive.Cipher
		size := len(cipher)/4*3 - 28 - (len(cipher) - len(strings.TrimRight(cipher, "=")))
		out = append(out, archiveChunkExtent{id, r.ContentHash, int64(size)})
	}
	return out, nil
}
