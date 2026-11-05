//go:build linux

package memory

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// Text with an image in place: the image model reads the image where it stood.
func TestFileBlocksReadImagesInPlace(t *testing.T) {
	f, llm := mediaFixture(t, extractAnswer())
	v := &fakeVisionLLM{fakeWikiLLM: llm, reads: []string{"화이트보드: 금요일 배포 금지"}}
	f.svc.Wiki = v
	png := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	in := FileMemoriesInput{Scope: scopeA, Filename: "note.md", Blocks: []FileBlock{{Type: "text", Text: "회의 메모 앞부분"}, {Type: "image", MIME: "image/png", ContentBase64: png}, {Type: "text", Text: "뒷부분"}}}
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, in); err != nil || len(v.images) != 1 {
		t.Fatalf("blocks: %v %d", err, len(v.images))
	}
	p := llm.prompts[len(llm.prompts)-1]
	if a, b, c := strings.Index(p, "회의 메모 앞부분"), strings.Index(p, "[image 2]\n화이트보드: 금요일 배포 금지"), strings.Index(p, "뒷부분"); a < 0 || b < a || c < b {
		t.Fatalf("image not in place: %q", p)
	}
	for _, bad := range []FileMemoriesInput{
		{Scope: scopeA, Filename: "note.md", ContentBase64: png, Blocks: in.Blocks},
		{Scope: scopeA, Filename: "note.md", Blocks: []FileBlock{{Type: "video", ContentBase64: png}}},
		{Scope: scopeA, Filename: "note.md", Blocks: []FileBlock{{Type: "image", MIME: "text/html", ContentBase64: png}}},
		{Scope: scopeA, Filename: "note.md", Blocks: []FileBlock{{Type: "text", Text: "x", ContentBase64: png}}},
	} {
		if _, err := f.svc.ExtractFileMemories(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad blocks %+v: %v", bad.Blocks, err)
		}
	}
}

// An uploaded file kept as an attachment shows up with recall and reflect hits
// and downloads through the source artifact route.
func TestUploadedOriginalAttachedToRecall(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
	f.svc.Files.KeepOriginals = true
	raw := "선택 상태에는 주황 강조선을 쓰지 않는다."
	out := extractUpload(t, f, raw)
	for _, s := range out.Skipped {
		if strings.HasPrefix(s.Reason, "original_not_kept") {
			t.Fatalf("not kept: %+v", out.Skipped)
		}
	}
	list, err := f.svc.ListSourceArtifacts(testCtx, f.human, out.SourceID, ArtifactListInput{Scope: scopeA})
	if err != nil || len(list.Items) != 1 || list.Items[0].Kind != "document" || list.Items[0].MIME != "text/plain" {
		t.Fatalf("artifacts %+v %v", list, err)
	}
	got, err := f.svc.ReadArtifact(testCtx, f.human, scopeA, out.SourceID, list.Items[0].ID)
	if err != nil || string(got) != raw {
		t.Fatalf("download %q %v", got, err)
	}
	c := f.repo.rows("memory_candidates")[0]
	m := c
	m.Status, m.ApprovedBy = "active", "human-1"
	f.put(colMemories, m)
	h := Hit{ID: m.ID, Collection: colMemories, Origin: "memory"}
	if err = f.svc.attachFiles(testCtx, f.human, scopeA, &h); err != nil || len(h.Attachments) != 1 || h.Attachments[0].SourceID != out.SourceID || h.Attachments[0].Bytes != int64(len(raw)) {
		t.Fatalf("hit attachments %+v %v (sources %+v)", h.Attachments, err, m.Sources)
	}
	h = Hit{ID: m.ID, Collection: colMemories, Origin: "memory", Sensitivity: "restricted"}
	if err = f.svc.attachFiles(testCtx, f.human, scopeA, &h); err != nil || len(h.Attachments) != 0 {
		t.Fatalf("restricted hit lists files: %+v", h.Attachments)
	}
	if !validInclude([]string{"sources", "entities", "attachments"}, nil) || validInclude([]string{"attachments", "attachments"}, nil) {
		t.Fatal("include validation")
	}
	r := ReflectInput{Scope: scopeA, Query: "q", Include: []string{"sources"}}
	if validReflectInput(&r) {
		t.Fatal("reflect takes only attachments")
	}
	// A redacted upload keeps no original: the file still holds what was removed.
	f2, _ := wikiFixture(t, extractAnswer())
	f2.svc.Files.KeepOriginals = true
	out = extractUpload(t, f2, "배포 규칙 <private>집 주소</private>")
	if len(out.Skipped) == 0 || out.Skipped[len(out.Skipped)-1].Reason != "original_not_kept:redacted" {
		t.Fatalf("redacted kept: %+v", out.Skipped)
	}
}
