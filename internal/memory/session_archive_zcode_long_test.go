package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func longZcodeOriginal(duplicate bool) []byte {
	var raw []byte
	// Metadata exceeds both the old 4 MiB prefix cap and one 16 MiB search page.
	for i := 0; i < 34; i++ {
		data := map[string]string{"role": "user", "padding": strings.Repeat("x", 512<<10)}
		if i == 33 {
			data["channel"] = "analysis"
		}
		raw = append(raw, jsonLine(map[string]any{"table": "message", "record": map[string]string{"id": fmt.Sprintf("m%d", i), "data": string(jsonLine(data))}})...)
	}
	if duplicate {
		raw = append(raw, jsonLine(map[string]any{"table": "message", "record": map[string]string{"id": "m0", "data": `{"role":"assistant"}`}})...)
	}
	for _, id := range []string{"m0", "m33", "missing"} {
		raw = append(raw, jsonLine(map[string]any{"table": "part", "record": map[string]string{"id": "part-" + id, "message_id": id, "data": `{"type":"text","text":"긴 메타데이터 뒤 한국어 단서"}`}})...)
	}
	return raw
}
func TestSessionArchiveZcodeLongMetadata(t *testing.T) { testArchiveZcodeLongMetadata(t, nil) }
func TestPostgresSessionArchiveZcodeLongMetadata(t *testing.T) {
	testArchiveZcodeLongMetadata(t, isolatedPostgres(t))
}
func testArchiveZcodeLongMetadata(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	id := ContentHash("long-zcode-metadata")
	header := archiveSearchUpload(t, f, *now, id, longZcodeOriginal(false), "reconstructed_zcode")
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "한국어"}
	first, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
	if err != nil || first.Next == "" || len(first.Items) != 0 || first.Unsupported != 0 {
		t.Fatalf("metadata pagination: %+v %v", first, err)
	}
	in.After = first.Next
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
	if err != nil || len(page.Items) != 1 || page.Unsupported != 0 || page.Invalid != 1 || page.ScannedBytes <= archiveSearchBudget {
		t.Fatalf("long role lookup: %+v %v", page, err)
	}
	if page.Items[0].Role != "user" || page.Items[0].Ref.RecordKey == "" || page.Items[0].Ref.MessageSHA256 == "" {
		t.Fatal("missing original identity", page.Items[0])
	}
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: page.Items[0].Ref})
	if err != nil || view.Text != "긴 메타데이터 뒤 한국어 단서" || view.Role != "user" {
		t.Fatalf("direct original read: %+v %v", view, err)
	}
	current, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || current.Revision != header.Revision || !current.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("lookup changed original lifetime", err)
	}
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeB, Ref: view.Ref}); err == nil {
		t.Fatal("foreign scope exposed original")
	}
	*now = header.ExpiresAt.Add(1)
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: view.Ref}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired original accepted", err)
	}
}
func TestSessionArchiveZcodeDistantDuplicateAndCancellation(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	id := ContentHash("duplicate-long-zcode")
	archiveSearchUpload(t, f, *now, id, longZcodeOriginal(true), "reconstructed_zcode")
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "한국어"}
	first, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
	if err != nil {
		t.Fatal(err)
	}
	in.After = first.Next
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
	if err != nil || len(page.Items) != 0 || page.Unsupported != 1 {
		t.Fatalf("distant duplicate exposed: %+v %v", page, err)
	}
	h, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	if _, scanned, err := f.svc.archiveZcodeRole(ctx, h, "m0"); !errors.Is(err, context.Canceled) || scanned != 0 {
		t.Fatal("cancelled scan continued", scanned, err)
	}
}
func TestSessionArchiveZcodeRoleCacheBounded(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := []byte{}
	for i := 0; i < 130; i++ {
		raw = append(raw, jsonLine(map[string]any{"table": "message", "record": map[string]string{"id": fmt.Sprint(i), "data": `{"role":"user"}`}})...)
	}
	part := func(i int) []byte {
		return jsonLine(map[string]any{"table": "part", "record": map[string]string{"message_id": fmt.Sprint(i), "data": `{"type":"text","text":"근거"}`}})
	}
	raw = append(raw, part(0)...)
	id := ContentHash("bounded-role-cache")
	archiveSearchUpload(t, f, *now, id, raw, "reconstructed_zcode")
	h, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]string{}
	for i := 0; i < 130; i++ {
		text, role, valid, scanned, err := f.svc.archiveSafeMessageForHeader(testCtx, h, part(i), cache)
		if err != nil || !valid || text != "근거" || role != "user" || scanned == 0 {
			t.Fatal("bounded cache lost result", i, err)
		}
	}
	if len(cache) != 128 {
		t.Fatal("unbounded cache", len(cache))
	}
	_, _, _, scanned, err := f.svc.archiveSafeMessageForHeader(testCtx, h, part(0), cache)
	if err != nil || scanned != 0 {
		t.Fatal("role cache not used", err, scanned)
	}
	// A missing encrypted chunk is a storage failure, not a missing message ID.
	missing := h
	metadata := *h.SessionArchive
	metadata.Chunks = []string{ContentHash("missing-role-chunk")}
	missing.SessionArchive = &metadata
	_, _, _, _, err = f.svc.archiveSafeMessageForHeader(testCtx, missing, part(0), map[string]string{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("missing chunk swallowed as unknown role", err)
	}
}
