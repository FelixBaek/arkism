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
	} else if sc := (&SpaceConsolidation{DefaultScopes: [][]string{{"design"}, {"ui"}}}).partition([]Record{m}); len(sc) != 1 || !slices.Equal(sc[0].tags, []string{"design"}) {
		// The document's tags are what observation scopes are cut from: the edit moves the memory's scope.
		t.Fatalf("observation scopes %d", len(sc))
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

func TestExtractDocumentDelete(t *testing.T) {
	for _, approved := range []bool{false, true} {
		f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
		f.svc.Repo = opsFakeRepo{f.repo}
		up := extractUpload(t, f, "선택 상태에는 주황 강조선을 쓰지 않는다.")
		// An agent lists only the documents it uploaded itself.
		if list, err := f.svc.ListExtractDocuments(testCtx, f.agent, scopeA, DocumentPage{Limit: 10}); err != nil || len(list.Items) != 0 {
			t.Fatalf("agent list %+v %v", list, err)
		}
		c := f.repo.rows("memory_candidates")[0]
		if approved {
			c.Status = "approved"
			if err := f.repo.Replace(testCtx, "memory_candidates", c, c.Revision); err != nil {
				t.Fatal(err)
			}
			f.put(colMemories, Record{ID: c.ID, Kind: c.Kind, Status: "active", Text: c.Text, ApprovedBy: "human-1"})
		}
		if _, err := f.svc.DeleteExtractDocument(testCtx, f.agent, up.DocumentID, WikiScopeInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
			t.Fatalf("agent delete: %v", err)
		}
		got, err := f.svc.DeleteExtractDocument(testCtx, f.human, up.DocumentID, WikiScopeInput{Scope: scopeA})
		if err != nil || got.Jobs != 1 || approved && (got.Kept != 1 || got.Rejected != 0) || !approved && (got.Kept != 0 || got.Rejected != 1) {
			t.Fatalf("delete %+v %v", got, err)
		}
		cand, _ := f.repo.Get(testCtx, "memory_candidates", scopeA, c.ID)
		if approved && cand.Status != "approved" || !approved && cand.Status != "rejected" {
			t.Fatalf("candidate after delete %+v", cand)
		}
		if approved {
			if m, err := f.repo.Get(testCtx, colMemories, scopeA, c.ID); err != nil || m.Status != "active" {
				t.Fatalf("approved memory must stay %+v %v", m, err)
			}
		}
		for _, id := range []string{up.DocumentID, up.JobID} {
			if _, err := f.repo.Get(testCtx, "jobs", scopeA, id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s still there: %v", id, err)
			}
		}
		if list, _ := f.svc.ListExtractDocuments(testCtx, f.human, scopeA, DocumentPage{Limit: 10}); len(list.Items) != 0 {
			t.Fatalf("list after delete %+v", list)
		}
		if _, err := f.svc.DeleteExtractDocument(testCtx, f.human, up.DocumentID, WikiScopeInput{Scope: scopeA}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete: %v", err)
		}
		if !slices.ContainsFunc(f.repo.rows("audit_events"), func(r Record) bool { return r.Kind == "delete:file_extract_document" && r.Target.ID == up.DocumentID }) {
			t.Fatal("delete not audited")
		}
	}
}
