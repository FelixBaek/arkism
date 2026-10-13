package memory

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func zcodeRelocationRows(role, text, id string) ([]byte, []byte) {
	metadata := jsonLine(map[string]any{"table": "message", "record": map[string]string{"id": "m", "data": `{"role":"` + role + `"}`}})
	part := jsonLine(map[string]any{"table": "part", "record": map[string]string{"id": id, "message_id": "m", "data": `{"type":"text","text":"` + text + `"}`}})
	return metadata, part
}
func TestZcodeStableRecordRelocation(t *testing.T) { testZcodeStableRecordRelocation(t, nil) }
func TestPostgresZcodeStableRecordRelocation(t *testing.T) {
	testZcodeStableRecordRelocation(t, isolatedPostgres(t))
}
func testZcodeStableRecordRelocation(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	id := ContentHash("zcode-relocation")
	metadata, part := zcodeRelocationRows("user", "원문 단서", "part-one")
	archiveSearchUpload(t, f, *now, id, append(metadata, part...), "reconstructed_zcode")
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "단서"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Ref.RecordKey == "" {
		t.Fatal("record key missing", err)
	}
	ref := page.Items[0].Ref
	in := sourceInput(CreateInput{Scope: scopeA, Text: "linked summary", ArchiveMessages: []ArchiveMessageRef{ref}})
	saved, err := f.svc.Create(testCtx, f.human, "sources", in)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuilt session metadata moves the same part into another chunk.
	padding := jsonLine(map[string]string{"session": string(bytes.Repeat([]byte("x"), SessionArchiveChunkSize+50))})
	*now = now.Add(time.Second)
	header := archiveSearchUpload(t, f, *now, id, append(append(padding, metadata...), part...), "reconstructed_zcode")
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
	if err != nil || view.Text != "원문 단서" || view.Ref.Chunk == ref.Chunk || view.Ref.RecordKey != ref.RecordKey || view.Ref.MessageSHA256 != ref.MessageSHA256 {
		t.Fatalf("same record lost after relocation: %+v %v", view, err)
	}
	repeated, err := f.svc.Create(testCtx, f.human, "sources", in)
	if err != nil || !repeated.Duplicate || repeated.ID != saved.ID || repeated.ArchiveMessages[0] != saved.ArchiveMessages[0] {
		t.Fatal("relocated source dedupe changed provenance", err)
	}
	if _, _, err = f.svc.validateArchiveMessageRefs(testCtx, f.agent, scopeA, []ArchiveMessageRef{ref, view.Ref}, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("old/new positions duplicated same record", err)
	}
	legacy := ref
	legacy.RecordKey = ""
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: legacy}); err == nil {
		t.Fatal("legacy position unexpectedly relocated")
	}
	row, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || !row.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("relocation extended TTL", err)
	}
	originalAuth := f.svc.Auth
	reads := 0
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "read:"+colSessionArchives {
			reads++
			if reads == 3 {
				*now = *header.ExpiresAt
			}
		}
		return originalAuth.Authorize(ctx, token, req)
	})
	if view, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); err == nil || view.Text != "" {
		t.Fatal("relocation bypassed expiry at response fence", err)
	}
}
func TestZcodeRelocationRejectsChangedOrAmbiguousIdentity(t *testing.T) {
	for _, mode := range []string{"duplicate", "text", "role", "id", "missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			metadata, part := zcodeRelocationRows("user", "original", "part-one")
			id := ContentHash("zcode-reject-" + mode)
			archiveSearchUpload(t, f, *now, id, append(metadata, part...), "reconstructed_zcode")
			view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(append(metadata, part...))), Offset: len(metadata)}})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "duplicate":
				part = append(part, part...)
			case "text":
				metadata, part = zcodeRelocationRows("user", "modified", "part-one")
			case "role":
				metadata, part = zcodeRelocationRows("assistant", "original", "part-one")
			case "id":
				metadata, part = zcodeRelocationRows("user", "original", "part-two")
			case "missing":
				part = nil
			case "malformed":
				part = append(part, []byte("not-json\n")...)
			}
			*now = now.Add(time.Second)
			raw := append(jsonLine(map[string]string{"session": "new version"}), metadata...)
			archiveSearchUpload(t, f, *now, id, append(raw, part...), "reconstructed_zcode")
			out, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: view.Ref})
			if err == nil || out.Text != "" {
				t.Fatal("changed or ambiguous record returned", mode, err)
			}
		})
	}
}
