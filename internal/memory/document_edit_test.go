//go:build linux

package memory

import (
	"errors"
	"slices"
	"testing"
)

func TestExtractDocumentListTagsAndReprocess(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim), extractAnswer(extractRuleClaim))
	f.svc.Repo = opsFakeRepo{f.repo}
	up := extractUpload(t, f, "선택 상태에는 주황 강조선을 쓰지 않는다.")
	list, err := f.svc.ListExtractDocuments(testCtx, f.human, scopeA, DocumentPage{Limit: 10})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != up.DocumentID || list.Items[0].Label != "rules.txt" || list.Items[0].Status != JobStatusDone ||
		list.Items[0].CandidatesCreated != 1 || list.Items[0].JobID != up.JobID {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := f.svc.EditDocumentTags(testCtx, f.agent, up.DocumentID, DocumentTagsInput{Scope: scopeA, Add: []string{"design"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent tag edit: %v", err)
	}
	if _, err := f.svc.EditDocumentTags(testCtx, f.human, up.DocumentID, DocumentTagsInput{Scope: scopeA, Add: []string{"token: sk-abcdefghijklmnop"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret tag: %v", err)
	}
	got, err := f.svc.EditDocumentTags(testCtx, f.human, up.DocumentID, DocumentTagsInput{Scope: scopeA, Add: []string{"design", "ui"}})
	if err != nil || got.Candidates != 1 || got.Memories != 0 || !slices.Equal(got.Tags, []string{"design", "ui"}) {
		t.Fatalf("tag edit %+v %v", got, err)
	}
	cands := f.repo.rows("memory_candidates")
	if len(cands) != 1 || !slices.Contains(cands[0].Tags, "design") {
		t.Fatalf("candidate tags %+v", cands)
	}
	if list, _ = f.svc.ListExtractDocuments(testCtx, f.human, scopeA, DocumentPage{Limit: 10, Tag: "ui"}); len(list.Items) != 1 {
		t.Fatalf("tag filter %+v", list)
	}
	if list, _ = f.svc.ListExtractDocuments(testCtx, f.human, scopeA, DocumentPage{Limit: 10, Tag: "other"}); len(list.Items) != 0 {
		t.Fatalf("other tag %+v", list)
	}
	// Once approved, the edit reaches the memory as an audited revision.
	c := cands[0]
	c.Status = "approved"
	if err = f.repo.Replace(testCtx, "memory_candidates", c, c.Revision); err != nil {
		t.Fatal(err)
	}
	f.put(colMemories, Record{ID: c.ID, Kind: c.Kind, Status: "active", Text: c.Text, Tags: c.Tags, ApprovedBy: "human-1"})
	got, err = f.svc.EditDocumentTags(testCtx, f.human, up.DocumentID, DocumentTagsInput{Scope: scopeA, Remove: []string{"ui"}})
	if err != nil || got.Memories != 1 || got.Candidates != 0 {
		t.Fatalf("memory tag edit %+v %v", got, err)
	}
	if m, _ := f.repo.Get(testCtx, colMemories, scopeA, c.ID); slices.Contains(m.Tags, "ui") || !slices.Contains(m.Tags, "design") || m.Text != c.Text {
		t.Fatalf("memory tags %+v", m)
	}
	// Reprocess reads every chunk again from the stored evidence, with the document's tags.
	if _, err := f.svc.ReprocessDocument(testCtx, f.agent, up.DocumentID, WikiScopeInput{Scope: scopeA}); err == nil {
		t.Fatal("agent reprocess of another person's document")
	}
	again, err := f.svc.ReprocessDocument(testCtx, f.human, up.DocumentID, WikiScopeInput{Scope: scopeA})
	if err != nil || again.JobID == up.JobID || again.DocumentID != up.DocumentID || again.Status != JobStatusDone || again.Delta.ChunksChanged != 1 {
		t.Fatalf("reprocess %+v %v", again, err)
	}
	job, _ := f.repo.Get(testCtx, "jobs", scopeA, again.JobID)
	if j := job.FileExtract; j.Chunks[0].Round == "" || !slices.Equal(j.Tags, []string{"design"}) || j.Calls != 1 {
		t.Fatalf("reprocess job %+v", j)
	}
	// A stored source that changed is refused, not guessed.
	src, _ := f.repo.Get(testCtx, "sources", scopeA, up.SourceID)
	src.Text += " 덧붙임"
	_ = f.repo.Replace(testCtx, "sources", src, src.Revision)
	if _, err := f.svc.ReprocessDocument(testCtx, f.human, up.DocumentID, WikiScopeInput{Scope: scopeA}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed source: %v", err)
	}
}

func TestSourceChunkText(t *testing.T) {
	a, b := FileExtractChunk{Locator: "chunk:1", Hash: ContentHash(extractSpaces("첫 문단\n\n[x] 괄호"))}, FileExtractChunk{Locator: "chunk:2", Hash: ContentHash(extractSpaces("둘째"))}
	text := "[chunk:1]\n첫 문단\n\n[x] 괄호\n\n[chunk:2]\n둘째"
	if got, ok := sourceChunkText(text, a); !ok || got != "첫 문단\n\n[x] 괄호" {
		t.Fatalf("a %q %v", got, ok)
	}
	if got, ok := sourceChunkText(text, b); !ok || got != "둘째" {
		t.Fatalf("b %q %v", got, ok)
	}
	if _, ok := sourceChunkText("[chunk:1]\n다른 글", a); ok {
		t.Fatal("hash mismatch accepted")
	}
}
