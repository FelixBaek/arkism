package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func largeZcodeRows(role, messageID, partID, text string, padding int) ([]byte, []byte) {
	data, _ := json.Marshal(map[string]string{"role": role, "padding": strings.Repeat("x", padding)})
	meta := jsonLine(map[string]any{"table": "message", "record": map[string]string{"id": messageID, "data": string(data)}})
	data, _ = json.Marshal(map[string]string{"type": "text", "text": text})
	part := jsonLine(map[string]any{"table": "part", "record": map[string]string{"id": partID, "message_id": messageID, "data": string(data)}})
	return meta, part
}
func TestSessionArchiveZcodeLargeMessage(t *testing.T) { testZcodeLargeMessage(t, nil) }
func TestPostgresSessionArchiveZcodeLargeMessage(t *testing.T) {
	testZcodeLargeMessage(t, isolatedPostgres(t))
}
func testZcodeLargeMessage(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	text := strings.Repeat("x", 9<<20) + " 큰지코드단서 " + strings.Repeat("가나다", 15000)
	metadata, part := largeZcodeRows("user", "m", "p", text, 0)
	id := ContentHash("zcode-large-message")
	header := archiveSearchUpload(t, f, *now, id, append(metadata, part...), "reconstructed_zcode")
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "큰지코드단서"})
	if err != nil || len(page.Items) != 1 || page.Oversized != 0 || page.Invalid != 0 || page.Unsupported != 0 {
		t.Fatalf("large Zcode lost: %+v %v", page, err)
	}
	ref := page.Items[0].Ref
	if ref.RecordKey != archiveRecordKey("reconstructed_zcode", part) || ref.MessageSHA256 != archiveMessageHash("reconstructed_zcode", "user", part) {
		t.Fatal("identity changed")
	}
	in := ArchiveMessageInput{Scope: scopeA, Ref: ref, TextOffset: page.Items[0].TextOffset}
	first, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, in)
	if err != nil || first.NextOffset == nil || len(first.Text) > archiveMessagePage {
		t.Fatal("large read", err)
	}
	in.TextOffset = *first.NextOffset
	second, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, in)
	if err != nil || first.Text+second.Text != text[first.TextOffset:first.TextOffset+len(first.Text)+len(second.Text)] {
		t.Fatal("continuation changed", err)
	}
	stored, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || stored.Revision != header.Revision || !stored.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("read renewed original", err)
	}
	// Metadata reconstruction moves the same large part to a new chunk.
	padding := jsonLine(map[string]string{"session": strings.Repeat("x", SessionArchiveChunkSize+40)})
	*now = now.Add(time.Second)
	header = archiveSearchUpload(t, f, *now, id, append(append(padding, metadata...), part...), "reconstructed_zcode")
	in.TextOffset = first.TextOffset
	moved, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, in)
	if err != nil || moved.Text != first.Text || moved.Ref.Chunk == ref.Chunk || moved.Ref.RecordKey != ref.RecordKey || moved.Ref.MessageSHA256 != ref.MessageSHA256 {
		t.Fatal("large relocation failed", err)
	}
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeB, Ref: moved.Ref}); err == nil {
		t.Fatal("foreign scope exposed")
	}
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	if _, err = f.svc.ReadSessionArchiveMessage(ctx, f.agent, in); err == nil {
		t.Fatal("cancelled read returned content")
	}
	*now = header.ExpiresAt.Add(1)
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, in); !errors.Is(err, ErrConflict) {
		t.Fatal("expired large original exposed", err)
	}
}
func TestSessionArchiveZcodeLargeMetadata(t *testing.T) { testZcodeLargeMetadata(t, nil) }
func TestPostgresSessionArchiveZcodeLargeMetadata(t *testing.T) {
	testZcodeLargeMetadata(t, isolatedPostgres(t))
}
func testZcodeLargeMetadata(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	// A long decoded identifier is hashed without retaining its full string.
	messageID := strings.Repeat("식별자", 1000)
	metadata, part := largeZcodeRows("assistant", messageID, "part", "큰역할정보 뒤 본문", 9<<20)
	id := ContentHash("zcode-large-role")
	archiveSearchUpload(t, f, *now, id, append(metadata, part...), "reconstructed_zcode")
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "큰역할정보"}
	for pages := 0; pages < 3; pages++ {
		out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
		if err != nil || out.Invalid != 0 || out.Unsupported != 0 || out.Oversized != 0 {
			t.Fatalf("large metadata rejected: %+v %v", out, err)
		}
		if len(out.Items) == 1 {
			if out.Items[0].Role != "assistant" {
				t.Fatal("wrong role")
			}
			view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: out.Items[0].Ref})
			if err != nil || view.Text != "큰역할정보 뒤 본문" {
				t.Fatal("metadata direct read", err)
			}
			return
		}
		if out.Next == "" {
			t.Fatal("missing continuation")
		}
		in.After = out.Next
	}
	t.Fatal("large metadata never reached body")
}
func TestArchiveZcodeStreamIDAndEnvelope(t *testing.T) {
	for _, id := range []string{"", "식별자\\\"😀", strings.Repeat("가", 3<<20)} {
		encoded, _ := json.Marshal(id)
		span := archiveJSONSpan{jsonStreamTestSource(encoded), 0, int64(len(encoded)), '"'}
		actual, err := archiveZcodeIDHash(span, "arkism-zcode-part-v1\n")
		expected := ""
		if id != "" {
			expected = ContentHash("arkism-zcode-part-v1\n" + id)
		}
		if err != nil || actual != expected {
			t.Fatal("streamed ID hash changed", err)
		}
	}
	// Missing or malformed record must be an error, never a nil-source panic.
	for _, raw := range []string{`{"table":"part"}`, `{"table":"part","record":null}`, `{"table":"part","record":42}`} {
		fields, err := archiveJSONFields(archiveJSONSpan{jsonStreamTestSource([]byte(raw)), 0, -1, '{'})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err = archiveZcodeEnvelope(fields); err == nil {
			t.Fatal("malformed record accepted")
		}
	}
}

func TestSessionArchiveZcodeLargeRejectsUnsafe(t *testing.T) {
	filler := strings.Repeat("x", (8<<20)+100)
	for _, mode := range []string{"secret", "channel", "synthetic", "type", "bad-synthetic", "bad-data", "outer-channel"} {
		t.Run(mode, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			metadata, _ := largeZcodeRows("user", "m", "p", "", 0)
			text := "찾을단서 " + filler
			if mode == "secret" {
				text += " password: hidden"
			}
			encoded, _ := json.Marshal(text)
			data := `{"type":"text","text":` + string(encoded)
			switch mode {
			case "channel":
				data += `,"channel":"analysis"`
			case "synthetic":
				data += `,"synthetic":true`
			case "type":
				data += `,"type":"reasoning"`
			case "bad-synthetic":
				data += `,"synthetic":"false"`
			case "bad-data":
				data += `,"late":[1,]`
			}
			data += "}"
			record := map[string]string{"id": "p", "message_id": "m", "data": data}
			row := map[string]any{"table": "part", "record": record}
			if mode == "outer-channel" {
				row["channel"] = "analysis"
			}
			id := ContentHash("zcode-large-unsafe-" + mode)
			archiveSearchUpload(t, f, *now, id, append(metadata, jsonLine(row)...), "reconstructed_zcode")
			out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "찾을단서"})
			if err != nil || len(out.Items) != 0 || out.Oversized != 0 {
				t.Fatalf("unsafe Zcode: %+v %v", out, err)
			}
			if mode == "secret" && out.Secrets != 1 {
				t.Fatal("secret not counted")
			}
			if (mode == "bad-data" || mode == "bad-synthetic") && out.Invalid != 1 {
				t.Fatal("invalid not counted")
			}
		})
	}
}
func TestSessionArchiveZcodeLargeRejectsChangedIdentity(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	metadata, part := largeZcodeRows("user", "m", "p", strings.Repeat("x", (8<<20)+100)+" 원문", 0)
	id := ContentHash("zcode-large-identity")
	raw := append(append([]byte(nil), metadata...), part...)
	header := archiveSearchUpload(t, f, *now, id, raw, "reconstructed_zcode")
	first, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: header.SHA256, Offset: len(metadata)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"duplicate", "text", "role", "id"} {
		var changed []byte
		switch mode {
		case "duplicate":
			changed = append(append([]byte(nil), raw...), part...)
		case "text":
			changed = bytes.Replace(raw, []byte("원문"), []byte("변경"), 1)
		case "role":
			changed = bytes.Replace(raw, []byte("user"), []byte("assistant"), 1)
		case "id":
			changed = bytes.Replace(raw, []byte(`"id":"p"`), []byte(`"id":"other"`), 1)
		}
		*now = now.Add(time.Second)
		archiveSearchUpload(t, f, *now, id, changed, "reconstructed_zcode")
		out, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: first.Ref})
		if !errors.Is(err, ErrConflict) || out.Text != "" {
			t.Fatalf("changed identity exposed %s: %v", mode, err)
		}
	}
}
func TestSessionArchiveZcodeLargeDuplicateRole(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	metadata, part := largeZcodeRows("user", "m", "p", "금지된본문", 9<<20)
	duplicate, _ := largeZcodeRows("assistant", "m", "unused", "", 0)
	id := ContentHash("zcode-large-duplicate-role")
	archiveSearchUpload(t, f, *now, id, append(append(metadata, duplicate...), part...), "reconstructed_zcode")
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "금지된본문"}
	for page := 0; page < 4; page++ {
		out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
		if err != nil || len(out.Items) != 0 {
			t.Fatal("ambiguous role exposed", err)
		}
		if out.Unsupported == 1 {
			return
		}
		if out.Next == "" {
			t.Fatal("duplicate not reported")
		}
		in.After = out.Next
	}
	t.Fatal("duplicate role not reached")
}

func TestSessionArchiveZcodeCombinedLargeRecords(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	metadata, part := largeZcodeRows("user", "m", "p", strings.Repeat("x", 9<<20)+" 결합원문단서", 9<<20)
	id := ContentHash("zcode-both-records-large")
	archiveSearchUpload(t, f, *now, id, append(metadata, part...), "reconstructed_zcode")
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "결합원문단서"}
	for page := 0; page < 3; page++ {
		out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
		if err != nil || out.Invalid != 0 || out.Unsupported != 0 || out.Oversized != 0 {
			t.Fatalf("combined large records: %+v %v", out, err)
		}
		if len(out.Items) == 1 {
			view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: out.Items[0].Ref, TextOffset: out.Items[0].TextOffset})
			if err != nil || !strings.Contains(view.Text, "결합원문단서") {
				t.Fatal("combined direct read", err)
			}
			return
		}
		if out.Next == "" {
			t.Fatal("combined original lost")
		}
		in.After = out.Next
	}
	t.Fatal("combined body never reached")
}
