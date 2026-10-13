package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionArchiveSelectedDialogue(t *testing.T) { testSelectedDialogue(t, nil) }
func TestPostgresSessionArchiveSelectedDialogue(t *testing.T) {
	testSelectedDialogue(t, isolatedPostgres(t))
}
func testSelectedDialogue(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	id, other := ContentHash("dialogue"), ContentHash("other-dialogue")
	expected := []string{"사용자 목표 " + strings.Repeat("긴 제약 ", 20000) + "끝의 제약", "답변 " + strings.Repeat("진행 내용 ", 20000) + "끝의 남은 일"}
	raw := jsonLine(map[string]string{"role": "user", "content": expected[0]})
	raw = append(raw, jsonLine(map[string]string{"role": "assistant", "channel": "analysis", "content": "hidden thought"})...)
	raw = append(raw, jsonLine(map[string]string{"role": "tool", "content": "tool secret"})...)
	raw = append(raw, jsonLine(map[string]string{"role": "user", "content": "password: hunter2secret"})...)
	raw = append(raw, jsonLine(map[string]string{"role": "assistant", "content": expected[1]})...)
	header := archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	archiveSearchUpload(t, f, *now, other, jsonLine(map[string]string{"role": "user", "content": "other session"}), "jsonl")
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Limit: 1}
	var messages []string
	firstCursor := ""
	secrets := 0
	for pages := 0; ; pages++ {
		if pages > 8 {
			t.Fatal("pagination did not terminate")
		}
		out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
		if err != nil {
			t.Fatal(err)
		}
		secrets += out.Secrets
		for _, item := range out.Items {
			if item.Ref.ArchiveID != id || item.TextOffset != 0 || item.Authority != AuthorityDataOnly {
				t.Fatalf("wrong message: %+v", item)
			}
			body := item.Text
			offset := item.NextOffset
			for offset != nil {
				part, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: item.Ref, TextOffset: *offset})
				if err != nil {
					t.Fatal(err)
				}
				body += part.Text
				offset = part.NextOffset
			}
			messages = append(messages, body)
		}
		if out.Next == "" {
			break
		}
		if firstCursor == "" {
			firstCursor = out.Next
		}
		in.After = out.Next
	}
	if len(messages) != 2 || messages[0] != expected[0] || messages[1] != expected[1] || secrets != 1 {
		t.Fatalf("dialogue lost text or leaked: count=%d secrets=%d", len(messages), secrets)
	}
	stored, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || !stored.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("reading renewed retention", err)
	}
	for _, invalid := range []ArchiveSearchInput{
		{Scope: scopeA, ArchiveID: other, After: firstCursor},
		{Scope: scopeA, Query: "x", After: firstCursor},
		{Scope: scopeA, ArchiveID: id, Query: "x", After: firstCursor},
		{Scope: scopeA},
		{Scope: scopeA, ArchiveID: "not-an-id"},
	} {
		if _, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid filter/cursor: %v", err)
		}
	}
	unfiltered, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "진행", Limit: 1})
	if err != nil || unfiltered.Next == "" {
		t.Fatal("missing unfiltered cursor", err)
	}
	if _, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "진행", After: unfiltered.Next}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unfiltered cursor accepted with filter", err)
	}
	forged := archiveSearchCursor{ArchiveID: id, QueryHash: ContentHash(""), Ref: ArchiveMessageRef{ArchiveID: other, SHA256: ContentHash("irrelevant")}}
	if _, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, After: encodeArchiveCursor(forged)}); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-archive cursor accepted", err)
	}
	filtered, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "other session"})
	if err != nil || len(filtered.Items) != 0 || filtered.Next != "" {
		t.Fatal("filter escaped selected archive", err)
	}
	// Changing the stored bytes invalidates a previously issued dialogue cursor.
	*now = now.Add(time.Second)
	archiveSearchUpload(t, f, *now, id, append(raw, jsonLine(map[string]string{"role": "user", "content": "appended"})...), "jsonl")
	if _, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, After: firstCursor}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale dialogue cursor accepted", err)
	}
}
