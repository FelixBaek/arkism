//go:build linux

package memory

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func uploadNamed(t *testing.T, f *fixture, name, text string) FileMemoriesResult {
	t.Helper()
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: name, ContentBase64: base64.StdEncoding.EncodeToString([]byte(text))})
	if err != nil || out.DocumentID == "" {
		t.Fatalf("upload %s: %+v %v", name, out, err)
	}
	return out
}

// A person reads a document's text, edits it, and only the changed chunk goes to the model again.
func TestDocumentTextEditExtractsChangedChunks(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim), extractAnswer(), extractAnswer(extractRuleClaim))
	filler := strings.Repeat(" 배포 전에 테스트를 실행한다.", 200) // one paragraph fills most of a chunk
	p1, p2 := "첫 문단."+filler, "둘째 문단."+filler
	out := extractUpload(t, f, p1, p2)
	text, err := f.svc.DocumentText(testCtx, f.human, scopeA, out.DocumentID)
	if err != nil || text.Chunks != 2 || !strings.Contains(text.Text, "둘째 문단.") {
		t.Fatalf("text %+v %v", text, err)
	}
	if _, err = f.svc.DocumentText(testCtx, f.agent, scopeA, out.DocumentID); err == nil {
		t.Fatal("an agent read the document text")
	}
	if _, err = f.svc.EditDocumentText(testCtx, f.agent, out.DocumentID, DocumentTextInput{Scope: scopeA, Text: "x"}); err == nil {
		t.Fatal("an agent edited the document text")
	}
	calls := len(llm.prompts)
	edited, err := f.svc.EditDocumentText(testCtx, f.human, out.DocumentID, DocumentTextInput{Scope: scopeA, Text: p1 + "\n\n고친 문단." + filler})
	if err != nil || edited.DocumentID != out.DocumentID || edited.Delta.ChunksChanged != 1 || edited.Delta.ChunksUnchanged != 1 || len(llm.prompts) != calls+1 ||
		!strings.Contains(llm.prompts[calls], "고친 문단.") {
		t.Fatalf("edit %+v %+v %v (calls %d -> %d)", edited, edited.Delta, err, calls, len(llm.prompts))
	}
	if _, err = f.svc.EditDocumentText(testCtx, f.human, out.DocumentID, DocumentTextInput{Scope: scopeA, Text: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty text: %v", err)
	}
}

// Documents travel as a zip; the conflict policy decides what a document of the same name does.
func TestDocumentsZipRoundTripAndConflicts(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim), extractAnswer(extractRuleClaim))
	a := uploadNamed(t, f, "a.txt", "가 문서의 규칙.")
	b := uploadNamed(t, f, "b.txt", "나 문서의 규칙.")
	zipped, err := f.svc.ExportDocuments(testCtx, f.human, DocumentsExportInput{Scope: scopeA, DocumentIDs: []string{b.DocumentID, "missing"}})
	if err != nil || !strings.HasSuffix(zipped.Filename, ".zip") {
		t.Fatalf("export %v", err)
	}
	docs, err := readDocumentZip(zipped.ContentBase64)
	if err != nil || len(docs) != 1 || docs[0].ID != b.DocumentID || !strings.Contains(docs[0].Chunks[0].Text, "나 문서") {
		t.Fatalf("zip %+v %v", docs, err)
	}
	if _, err = f.svc.ExportDocuments(testCtx, f.agent, DocumentsExportInput{Scope: scopeA, DocumentIDs: []string{a.DocumentID}}); err == nil {
		t.Fatal("an agent exported documents")
	}
	imp := func(conflict string) SpaceReport {
		t.Helper()
		r, err := f.svc.ImportDocuments(testCtx, f.human, DocumentsImportInput{Scope: scopeB, ZipBase64: zipped.ContentBase64, Conflict: conflict})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	docCount := func() int {
		n := 0
		for _, r := range scopeRecords(f, "jobs", scopeB) {
			if r.Kind == fileExtractDocKind {
				n++
			}
		}
		return n
	}
	if r := imp(""); r.Imported != 1 || docCount() != 1 {
		t.Fatalf("first import %+v", r)
	}
	if r := imp(docConflictSkip); r.Imported != 0 || !hasSkip(r, spaceSectionDocuments, "exists") {
		t.Fatalf("skip %+v", r)
	}
	if r := imp(docConflictReplace); r.Merged != 1 || docCount() != 1 {
		t.Fatalf("replace %+v", r)
	}
	if r := imp(docConflictNewID); r.Imported != 1 || docCount() != 2 {
		t.Fatalf("new_id %+v (%d documents)", r, docCount())
	}
	if _, err = f.svc.ImportDocuments(testCtx, f.human, DocumentsImportInput{Scope: scopeB, ZipBase64: zipped.ContentBase64, Conflict: "merge"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown conflict: %v", err)
	}

	// A space export can carry only the picked documents.
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Parts: []string{spaceSectionDocuments}, DocumentIDs: []string{a.DocumentID}})
	if !strings.Contains(bundle, "가 문서") || strings.Contains(bundle, "나 문서") {
		t.Fatalf("picked export:\n%s", bundle)
	}
	if validSpaceExport(SpaceExportInput{Parts: []string{"data"}, DocumentIDs: []string{a.DocumentID}}) {
		t.Fatal("a pick without the documents part")
	}
}

// The document list filters by when a document was added.
func TestDocumentListTimeFilter(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
	f.svc.Repo = opsFakeRepo{f.repo}
	extractUpload(t, f, "규칙 문단.")
	list := func(q string) int {
		t.Helper()
		scope, page, err := parseDocumentQuery("workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID + q)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.svc.ListExtractDocuments(testCtx, f.human, scope, page)
		if err != nil {
			t.Fatal(err)
		}
		return len(out.Items)
	}
	past, future := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if list("&from="+past) != 1 || list("&from="+future) != 0 || list("&to="+past) != 0 {
		t.Fatal("time filter")
	}
	if _, _, err := parseDocumentQuery("workspace_id=w&project_id=p&from=" + future + "&to=" + past); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reversed range: %v", err)
	}
}

// store_document_text off keeps no text at rest; null resets it to the inherited default.
func TestStoreDocumentTextOff(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
	var off SpaceConfigInput
	if err := json.Unmarshal([]byte(`{"expected_revision":-1,"store_document_text":false}`), &off); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, off, false); err != nil {
		t.Fatal(err)
	}
	out := extractUpload(t, f, "비밀이 아닌 규칙 문단.")
	src, err := f.repo.Get(testCtx, "sources", scopeA, out.SourceID)
	if err != nil || strings.Contains(src.Text, "규칙 문단") {
		t.Fatalf("source kept the text: %q %v", src.Text, err)
	}
	if _, err = f.svc.DocumentText(testCtx, f.human, scopeA, out.DocumentID); !errors.Is(err, ErrConflict) {
		t.Fatalf("text of a document without text: %v", err)
	}
	var reset SpaceConfigInput
	if err = json.Unmarshal([]byte(`{"expected_revision":-1,"store_document_text":null}`), &reset); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, reset, false); err != nil {
		t.Fatal(err)
	}
	if settings, err := f.svc.spaceSettings(testCtx, scopeA); err != nil || !settings.keepsDocumentText() {
		t.Fatalf("reset %+v %v", settings.StoreDocumentText, err)
	}
}

// time_field=updated filters by when a document last changed instead of when it was added.
func TestDocumentListUpdatedTimeFilter(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
	f.svc.Repo = opsFakeRepo{f.repo}
	up := extractUpload(t, f, "규칙 문단.")
	later := time.Now().Add(2 * time.Hour).UTC()
	f.editRecord(colJobs, up.DocumentID, func(r *Record) { r.UpdatedAt = later })
	list := func(q string) int {
		t.Helper()
		scope, page, err := parseDocumentQuery("workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID + q)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.svc.ListExtractDocuments(testCtx, f.human, scope, page)
		if err != nil {
			t.Fatal(err)
		}
		return len(out.Items)
	}
	soon, after := time.Now().Add(time.Hour).UTC().Format(time.RFC3339), later.Add(time.Hour).Format(time.RFC3339)
	if list("&from="+soon) != 0 || list("&from="+soon+"&time_field=updated") != 1 || list("&to="+soon+"&time_field=updated") != 0 || list("&to="+after+"&time_field=created") != 1 {
		t.Fatal("updated time filter")
	}
	if _, _, err := parseDocumentQuery("workspace_id=w&project_id=p&time_field=deleted"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown time field: %v", err)
	}
}
