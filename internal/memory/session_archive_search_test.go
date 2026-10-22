package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func archiveSearchFixture(t *testing.T, repo Repository) (*fixture, *time.Time) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	} else {
		f.svc.Repo = archiveFake{f.repo}
	}
	f.svc.SessionArchiveKey = bytes.Repeat([]byte{7}, 32)
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.svc.SessionArchiveNow = func() time.Time { return now }
	return f, &now
}
func archiveSearchUpload(t *testing.T, f *fixture, now time.Time, id string, raw []byte, format string) SessionArchiveCommitOutput {
	t.Helper()
	var chunks []string
	for at := 0; at < len(raw); at += SessionArchiveChunkSize {
		part := raw[at:min(at+SessionArchiveChunkSize, len(raw))]
		out, err := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{Scope: scopeA, ParentID: id, SHA256: ContentHash(string(part)), Data: base64.StdEncoding.EncodeToString(part)})
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, out.ID)
	}
	out, err := f.svc.CommitSessionArchive(testCtx, f.human, SessionArchiveCommitInput{Scope: scopeA, ID: id, SessionID: "search-session", Device: "test", Origin: "codex", Format: format, ObservedAt: now, SHA256: ContentHash(string(raw)), Chunks: chunks, Text: "tail projection without early message"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func jsonLine(value any) []byte                     { b, _ := json.Marshal(value); return append(b, '\n') }
func TestSessionArchiveFullSafeSearch(t *testing.T) { testArchiveFullSafeSearch(t, nil) }
func TestPostgresSessionArchiveFullSafeSearch(t *testing.T) {
	testArchiveFullSafeSearch(t, isolatedPostgres(t))
}
func testArchiveFullSafeSearch(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	id := ContentHash("full-search")
	first := jsonLine(map[string]any{"message": map[string]any{"role": "user", "content": "첫날의 한국어단서"}})
	raw := append([]byte{}, first...)
	raw = append(raw, jsonLine(map[string]any{"payload": map[string]any{"role": "assistant", "content": strings.Repeat("가나다 ", 120000)}})...)
	raw = append(raw, jsonLine(map[string]any{"role": "assistant", "channel": "analysis", "content": "한국어단서 hidden"})...)
	raw = append(raw, jsonLine(map[string]any{"role": "system", "content": "한국어단서 system"})...)
	raw = append(raw, jsonLine(map[string]any{"role": "user", "content": "한국어단서 password: hunter2secret"})...)
	raw = append(raw, jsonLine(map[string]any{"role": "assistant", "content": []map[string]string{{"type": "thinking", "thinking": "한국어단서 thought"}, {"type": "tool_use", "text": "한국어단서 tool"}, {"type": "text", "text": "마지막 한국어단서 답변"}}})...)
	header := archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	search := ArchiveSearchInput{Scope: scopeA, Query: "한국어단서", Limit: 1}
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, search)
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "첫날의 한국어단서" || page.Next == "" {
		t.Fatalf("early raw message not found: %+v %v", page, err)
	}
	ref := page.Items[0].Ref
	message, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
	if err != nil || message.Text != page.Items[0].Text || message.Authority != AuthorityDataOnly || message.RawBytes != len(first) {
		t.Fatalf("original reference failed: %+v %v", message, err)
	}
	search.After = page.Next
	page, err = f.svc.SearchSessionArchiveMessages(testCtx, f.agent, search)
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "마지막 한국어단서 답변" || page.Secrets != 1 || len(page.SecretRefs) != 1 {
		t.Fatalf("unsafe or missing result: %+v %v", page, err)
	}
	stored, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || !stored.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("search extended retention", err)
	}
	forged := ref
	forged.Offset = 1
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: forged}); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-line reference accepted", err)
	}
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeB, Ref: ref}); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign source exposed", err)
	}
	f.auth.actors["no-read"] = actor("no-read", "agent", false, "write")
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, "no-read", ArchiveMessageInput{Scope: scopeA, Ref: ref}); !errors.Is(err, ErrDenied) {
		t.Fatal("denied source exposed", err)
	}
	*now = now.Add(time.Second)
	raw = append(raw, jsonLine(map[string]string{"role": "user", "content": "new message"})...)
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	legacy := ref
	legacy.MessageSHA256 = ""
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: legacy}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale legacy reference accepted", err)
	}
	if view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); err != nil || view.Text != "첫날의 한국어단서" {
		t.Fatal("unchanged message lost after append", err)
	}
	if _, err = f.svc.SearchSessionArchiveMessages(testCtx, f.agent, search); !errors.Is(err, ErrConflict) {
		t.Fatal("stale search cursor accepted", err)
	}
	*now = now.Add(SessionArchiveTTL)
	if out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "한국어단서"}); (err != nil && !errors.Is(err, ErrConflict)) || len(out.Items) != 0 {
		t.Fatal("expired raw exposed")
	}
}
func TestSessionArchiveParserDoesNotInheritOuterRole(t *testing.T) {
	for _, raw := range []string{`{"role":"user","payload":{"content":"tool internals"}}`, `{"channel":"analysis","payload":{"role":"assistant","content":"hidden"}}`, `{"role":"user","table":"messages","record":{"content":"hidden"}}`} {
		text, _, _ := archiveSafeMessage([]byte(raw))
		if text != "" {
			t.Fatal("outer fields promoted nested data")
		}
	}
}
func TestSessionArchiveOversizedPaginationAndCoverage(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := append([]byte(`{"role":"tool","content":"`), bytes.Repeat([]byte("x"), archiveSearchBudget+1024)...)
	raw = append(raw, []byte("\"}\nnot-json\n{\"role\":\"user\",\"content\":\"끝 단서\"}")...)
	archiveSearchUpload(t, f, *now, ContentHash("oversized"), raw, "jsonl")
	input := ArchiveSearchInput{Scope: scopeA, Query: "단서"}
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, input)
	if err != nil || len(page.Items) != 0 || page.ScannedBytes <= archiveSearchBudget || page.Next == "" {
		t.Fatalf("unbounded first page: %+v %v", page, err)
	}
	input.After = page.Next
	page, err = f.svc.SearchSessionArchiveMessages(testCtx, f.agent, input)
	if err != nil || len(page.Items) != 1 || page.Oversized != 0 || page.Invalid != 1 {
		t.Fatalf("oversized resume lost data: %+v %v", page, err)
	}
	// Previously issued skip cursors still skip only the old large record tail.
	input.After = encodeArchiveCursor(archiveSearchCursor{Ref: ArchiveMessageRef{ArchiveID: ContentHash("oversized"), SHA256: ContentHash(string(raw)), Chunk: archiveSearchBudget / SessionArchiveChunkSize}, Skip: true, QueryHash: ContentHash(input.Query)})
	page, err = f.svc.SearchSessionArchiveMessages(testCtx, f.agent, input)
	if err != nil || len(page.Items) != 1 || page.Oversized != 1 || page.Invalid != 1 {
		t.Fatalf("legacy skip cursor changed: %+v %v", page, err)
	}
}
func TestSessionArchiveZcodeRolesAndTextPagination(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	var raw []byte
	for _, value := range []any{
		map[string]any{"session": map[string]string{"id": "s"}},
		map[string]any{"table": "message", "record": map[string]string{"id": "m1", "data": `{"role":"assistant"}`}},
		map[string]any{"table": "message", "record": map[string]string{"id": "m2", "data": `{"role":"tool"}`}},
		map[string]any{"table": "part", "record": map[string]string{"message_id": "m2", "data": `{"type":"text","text":"숨긴 단서"}`}},
		map[string]any{"table": "part", "record": map[string]string{"message_id": "m1", "data": string(jsonLine(map[string]string{"type": "text", "text": "단서 " + strings.Repeat("긴 대화 ", 10000)}))}},
	} {
		raw = append(raw, jsonLine(value)...)
	}
	archiveSearchUpload(t, f, *now, ContentHash("zcode-search"), raw, "reconstructed_zcode")
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "단서"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Role != "assistant" {
		t.Fatalf("zcode role join failed: %+v %v", page, err)
	}
	message, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: page.Items[0].Ref})
	if err != nil || message.NextOffset == nil || len(message.Text) > archiveMessagePage {
		t.Fatal("long message missing continuation", err)
	}
	next, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: message.Ref, TextOffset: *message.NextOffset})
	if err != nil || message.Text+next.Text != "단서 "+strings.Repeat("긴 대화 ", 10000) {
		t.Fatal("message pages lost text", err)
	}
}

// Change the real database after decryption and before the response fence.
// This makes the losing interleaving deterministic for search and direct read.
type archiveSearchFenceMutation struct {
	*Postgres
	before func()
}

func (r *archiveSearchFenceMutation) SessionArchiveTransaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if r.before != nil {
		before := r.before
		r.before = nil
		before()
	}
	return r.Postgres.SessionArchiveTransaction(ctx, fn)
}
func TestPostgresSessionArchiveSearchResponseFence(t *testing.T) {
	for _, mode := range []string{"search", "message", "dialogue"} {
		for _, change := range []string{"append", "expire"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				p := isolatedPostgres(t)
				f, now := archiveSearchFixture(t, p)
				id := ContentHash(mode + change)
				raw := jsonLine(map[string]string{"role": "user", "content": "검색 단서"})
				archiveSearchUpload(t, f, *now, id, raw, "jsonl")
				ref := ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}
				mutator := &Service{Repo: p, Auth: f.auth, SessionArchiveKey: f.svc.SessionArchiveKey, SessionArchiveNow: f.svc.SessionArchiveNow}
				f.svc.Repo = &archiveSearchFenceMutation{Postgres: p, before: func() {
					if change == "expire" {
						*now = now.Add(SessionArchiveTTL)
						out, err := mutator.ExpireSessionArchives(testCtx, f.human, SessionArchiveExpireInput{Scope: scopeA})
						if err != nil || len(out.Expired) != 1 {
							t.Fatalf("expiry mutation failed: %v", err)
						}
					} else {
						other := *f
						other.svc = mutator
						*now = now.Add(time.Second)
						archiveSearchUpload(t, &other, *now, id, append(raw, jsonLine(map[string]string{"role": "user", "content": "later"})...), "jsonl")
					}
				}}
				if mode == "search" || mode == "dialogue" {
					input := ArchiveSearchInput{Scope: scopeA, Query: "단서"}
					if mode == "dialogue" {
						input.Query = ""
						input.ArchiveID = id
					}
					out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, input)
					if !errors.Is(err, ErrConflict) || len(out.Items) != 0 {
						t.Fatalf("search leaked after %s: %v", change, err)
					}
				} else {
					out, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
					if !errors.Is(err, ErrConflict) || out.Text != "" {
						t.Fatalf("message leaked after %s: %v", change, err)
					}
				}
			})
		}
	}
}
func TestSessionArchiveUnsupportedAndInvalidCursor(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	archiveSearchUpload(t, f, *now, ContentHash("sqlite"), []byte("SQLite format 3\x00"), "sqlite_snapshot")
	out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "SQLite"})
	if err != nil || out.Unsupported != 1 || len(out.Items) != 0 {
		t.Fatalf("missing unsupported count: %+v %v", out, err)
	}
	for _, cursor := range []string{"not-base64", encodeArchiveCursor(archiveSearchCursor{QueryHash: ContentHash("other")}), encodeArchiveCursor(archiveSearchCursor{QueryHash: ContentHash("SQLite"), Skip: true})} {
		if _, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "SQLite", After: cursor}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid cursor accepted", err)
		}
	}
}

func TestSessionArchiveSearchRechecksPolicyAfterDecrypt(t *testing.T) {
	for _, mode := range []string{"search", "message", "dialogue"} {
		t.Run(mode, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			raw := jsonLine(map[string]string{"role": "user", "content": "단서"})
			id := ContentHash("policy-" + mode)
			archiveSearchUpload(t, f, *now, id, raw, "jsonl")
			original := f.svc.Auth
			reads := 0
			f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
				if req.Action == "read:"+colSessionArchives {
					reads++
					if reads == 3 {
						return Decision{}, ErrDenied
					}
				}
				return original.Authorize(ctx, token, req)
			})
			if mode == "search" || mode == "dialogue" {
				input := ArchiveSearchInput{Scope: scopeA, Query: "단서"}
				if mode == "dialogue" {
					input.Query = ""
					input.ArchiveID = id
				}
				out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, input)
				if !errors.Is(err, ErrDenied) || len(out.Items) != 0 {
					t.Fatal("policy change exposed search", err)
				}
			} else {
				out, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}})
				if !errors.Is(err, ErrDenied) || out.Text != "" {
					t.Fatal("policy change exposed message", err)
				}
			}
		})
	}
}

func TestSessionArchiveNestedHiddenEnvelopes(t *testing.T) {
	for _, raw := range []string{
		`{"channel":"analysis","table":"messages","record":{"role":"user","content":"hidden"}}`,
		`{"type":"session_meta","table":"messages","record":{"role":"user","content":"hidden"}}`,
		`{"table":"messages","record":{"channel":"analysis","data":"{\"role\":\"assistant\",\"content\":\"hidden\"}"}}`,
		`{"table":"messages","record":{"data":"{\"channel\":\"analysis\",\"role\":\"assistant\",\"content\":\"hidden\"}"}}`,
		`{"payload":{"type":"session_meta","role":"user","content":"hidden"}}`,
	} {
		text, _, valid := archiveSafeMessage([]byte(raw))
		if !valid || text != "" {
			t.Fatalf("hidden wrapper exposed: %s", raw)
		}
	}
	cursor := encodeArchiveCursor(archiveSearchCursor{QueryHash: ContentHash("q"), Skip: true, Ref: ArchiveMessageRef{ArchiveID: ContentHash("a"), SHA256: ContentHash("s")}})
	if _, err := decodeArchiveCursor(ArchiveSearchInput{Query: "q", After: cursor}); !errors.Is(err, ErrInvalid) {
		t.Fatal("impossible skip cursor accepted", err)
	}
}

func TestSessionArchiveZcodeHiddenAndMalformed(t *testing.T) {
	roles := map[string]string{"m": "assistant"}
	for _, raw := range []string{
		`{"channel":"analysis","table":"part","record":{"message_id":"m","data":"{\"type\":\"text\",\"text\":\"hidden\"}"}}`,
		`{"table":"part","record":{"channel":"analysis","message_id":"m","data":"{\"type\":\"text\",\"text\":\"hidden\"}"}}`,
		`{"table":"part","record":{"message_id":"m","data":"{\"channel\":\"analysis\",\"type\":\"text\",\"text\":\"hidden\"}"}}`,
	} {
		text, _, valid := archiveSafeMessageWithRoles([]byte(raw), roles)
		if !valid || text != "" {
			t.Fatal("zcode hidden envelope exposed")
		}
	}
	if _, _, valid := archiveSafeMessageWithRoles([]byte{0xff}, roles); valid {
		t.Fatal("malformed UTF-8 accepted")
	}
}
