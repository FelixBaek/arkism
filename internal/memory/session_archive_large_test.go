// Megabyte archives exceed the production search deadline under the race detector; the plain test run covers them.
//go:build !race

package memory

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSessionArchiveLargeText(t *testing.T)         { testArchiveLargeText(t, nil) }
func TestPostgresSessionArchiveLargeText(t *testing.T) { testArchiveLargeText(t, isolatedPostgres(t)) }
func testArchiveLargeText(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	text := strings.Repeat("x", 9<<20) + " 큰원문한국어단서 " + strings.Repeat("가나다", 15000)
	messageRaw := jsonLine(map[string]any{"role": "user", "content": text})
	prefix := []byte(`{"role":"tool","content":""}`)
	prefix = append(prefix, bytes.Repeat([]byte(" "), SessionArchiveChunkSize-len(prefix)-1)...)
	prefix = append(prefix, '\n')
	raw := append(prefix, messageRaw...)
	id := ContentHash("large-safe-original")
	header := archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "큰원문한국어단서"})
	if err != nil || len(page.Items) != 1 || page.Oversized != 0 || !strings.Contains(page.Items[0].Text, "큰원문한국어단서") {
		t.Fatalf("large message not found: %+v %v", page, err)
	}
	ref := page.Items[0].Ref
	if ref.Chunk != 0 || ref.Offset != SessionArchiveChunkSize {
		t.Fatal("chunk-end reference changed", ref)
	}
	if ref.MessageSHA256 != archiveMessageHash("jsonl", "user", messageRaw) || page.Items[0].RawBytes != len(messageRaw) {
		t.Fatal("original fingerprint changed")
	}
	input := ArchiveMessageInput{Scope: scopeA, Ref: ref, TextOffset: page.Items[0].TextOffset}
	first, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, input)
	if err != nil || first.NextOffset == nil || len(first.Text) > archiveMessagePage {
		t.Fatal("large direct page", err)
	}
	input.TextOffset = *first.NextOffset
	second, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, input)
	if err != nil || first.Text+second.Text != text[first.TextOffset:first.TextOffset+len(first.Text)+len(second.Text)] {
		t.Fatal("large continuation changed bytes", err)
	}
	stored, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || stored.Revision != header.Revision || !stored.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("read renewed original", err)
	}
	forged := ref
	forged.MessageSHA256 = ContentHash("different")
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: forged}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed message accepted", err)
	}
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeB, Ref: ref}); err == nil {
		t.Fatal("foreign original exposed")
	}
	*now = header.ExpiresAt.Add(1)
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired original exposed", err)
	}
}
func TestSessionArchiveLargeLateSecretAndEnvelope(t *testing.T) {
	for _, mode := range []string{"secret", "hidden", "role", "invalid", "array-secret"} {
		t.Run(mode, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			text := "찾을단서 " + strings.Repeat("x", (8<<20)+100)
			raw := []byte(`{"role":"user","content":"` + text + `"`)
			switch mode {
			case "secret":
				raw = jsonLine(map[string]string{"role": "user", "content": text + " password: hidden"})
			case "array-secret":
				raw = jsonLine(map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": "찾을단서 password: hidden"}, {"type": "text", "text": text}}})
			case "hidden":
				raw = append(raw, []byte(`,"channel":"analysis"}`)...)
			case "role":
				raw = append(raw, []byte(`,"role":"tool"}`)...)
			case "invalid":
				raw = append(raw, []byte(`,"late":[1,]}`)...)
			}
			id := ContentHash("large-" + mode)
			archiveSearchUpload(t, f, *now, id, raw, "jsonl")
			out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "찾을단서"})
			if err != nil || len(out.Items) != 0 || out.Oversized != 0 {
				t.Fatal("unsafe large message exposed", mode, err)
			}
			if (mode == "secret" || mode == "array-secret") && (out.Secrets != 1 || len(out.SecretRefs) != 1 || out.SecretRefs[0].ArchiveID != id) {
				t.Fatal("missing secret result")
			}
			if mode == "invalid" && out.Invalid != 1 {
				t.Fatal("missing invalid result")
			}
		})
	}
}
func TestSessionArchiveLargeEmbeddedAndArray(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		t.Run(map[bool]string{false: "jsonl", true: "agentsview"}[embedded], func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			text := strings.Repeat("x", (8<<20)+100) + " 배열끝단서"
			message := map[string]any{"role": "assistant", "content": []map[string]string{{"type": "thinking", "text": "HIDDEN"}, {"type": "text", "text": text}, {"type": "output_text", "text": "다음"}}}
			raw := jsonLine(message)
			format := "jsonl"
			if embedded {
				raw = jsonLine(map[string]any{"table": "messages", "record": map[string]string{"data": string(raw)}})
				format = "reconstructed_agentsview"
			}
			id := ContentHash("large-" + format)
			archiveSearchUpload(t, f, *now, id, raw, format)
			out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "배열끝단서"})
			if err != nil || len(out.Items) != 1 || strings.Contains(out.Items[0].Text, "HIDDEN") || !strings.HasSuffix(out.Items[0].Text, "\n다음") {
				t.Fatalf("array read failed: %+v %v", out, err)
			}
		})
	}
}

// Fail while the large-record parser reopens authenticated chunks, after its
// fingerprint pass. Dependency failures must not become a skipped invalid row.
type archiveLargeReadFailure struct {
	archiveFake
	remaining int
	fail      error
	cancel    context.CancelFunc
}

func (r *archiveLargeReadFailure) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == colSessionArchiveChunks {
		r.remaining--
		if r.remaining == 0 {
			if r.cancel != nil {
				r.cancel()
			}
			return Record{}, r.fail
		}
	}
	return r.archiveFake.Get(ctx, col, scope, id)
}
func TestSessionArchiveLargeReadFailures(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := jsonLine(map[string]string{"role": "user", "content": strings.Repeat("x", 9<<20)})
	id := ContentHash("large-read-failure")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	for _, failure := range []error{ErrUnavailable, context.Canceled} {
		ctx, cancel := context.WithCancel(testCtx)
		wrapper := &archiveLargeReadFailure{archiveFake: archiveFake{f.repo}, remaining: 21, fail: failure}
		if failure == context.Canceled {
			wrapper.cancel = cancel
		}
		f.svc.Repo = wrapper
		out, err := f.svc.SearchSessionArchiveMessages(ctx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id})
		cancel()
		if !errors.Is(err, failure) || len(out.Items) != 0 || out.Invalid != 0 {
			t.Fatalf("read failure hidden: expected=%v got=%v page=%+v", failure, err, out)
		}
	}
}
