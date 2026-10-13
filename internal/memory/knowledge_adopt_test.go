//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const knowledgeGuideMarkdown = `Intro line before any heading.

# 인쇄 가이드

## 재단

재단선은 3mm 바깥으로 둔다. 배포절차를 따른다.

## 색상
### CMYK

인쇄용 파일은 CMYK로 저장한다.

` + "```" + `sh
# not a heading inside a fence

echo done
` + "```" + `

# Appendix
Plain appendix text.`

func TestKnowledgeChunkerFollowsHeadings(t *testing.T) {
	chunks := knowledgeChunkText(knowledgeGuideMarkdown, ".md")
	paths := []string{}
	for _, c := range chunks {
		paths = append(paths, c.SectionPath)
	}
	want := []string{"document", "인쇄 가이드 > 재단", "인쇄 가이드 > 색상 > CMYK", "Appendix"}
	if !slices.Equal(paths, want) {
		t.Fatalf("section paths %q", paths)
	}
	// The bare "# 인쇄 가이드" heading travels with the first child section.
	if !strings.HasPrefix(chunks[1].Text, "# 인쇄 가이드\n\n## 재단") {
		t.Fatalf("bare heading not carried: %q", chunks[1].Text)
	}
	if !strings.Contains(chunks[2].Text, "# not a heading inside a fence\n\necho done") || !strings.HasPrefix(chunks[2].Text, "## 색상\n### CMYK") {
		t.Fatalf("fence split or heading lost: %q", chunks[2].Text)
	}
	// Text files have no headings: everything is one "document" section.
	for _, c := range knowledgeChunkText("# not markdown\n\nsecond paragraph", ".txt") {
		if c.SectionPath != "document" {
			t.Fatal(c)
		}
	}
}

func TestKnowledgeChunkerPacksParagraphsAndHardCutsWithOverlap(t *testing.T) {
	para := strings.TrimSpace(strings.Repeat("짧은 문단입니다. ", 10))
	packed := knowledgeChunkText(strings.Join([]string{para, para, para, para, para, para}, "\n\n"), ".txt")
	for _, c := range packed {
		if utf8.RuneCountInString(c.Text) > knowledgeChunkMaxRunes {
			t.Fatalf("chunk too long: %d", utf8.RuneCountInString(c.Text))
		}
		if strings.HasPrefix(c.Text, "\n") || strings.HasSuffix(c.Text, "\n") {
			t.Fatal("paragraph boundary not trimmed")
		}
	}
	if len(packed) < 2 || !strings.Contains(packed[0].Text, para+"\n\n"+para) {
		t.Fatalf("paragraphs not packed: %d", len(packed))
	}
	long := strings.TrimSpace(strings.Repeat("word ", 400)) // 1999 runes, one paragraph
	cut := knowledgeChunkText(long, ".md")
	if len(cut) < 4 {
		t.Fatalf("hard cut: %d", len(cut))
	}
	for i := 1; i < len(cut); i++ {
		prev := []rune(cut[i-1].Text)
		tail := string(prev[len(prev)-knowledgeChunkOverlapRunes/2:])
		if !strings.Contains(cut[i].Text, strings.TrimSpace(tail)) {
			t.Fatalf("chunk %d does not overlap the previous one", i)
		}
		if utf8.RuneCountInString(cut[i].Text) > knowledgeChunkMaxRunes {
			t.Fatal("hard cut chunk too long")
		}
	}
	// No content is lost: the windows cover at least every word once.
	if strings.Count(strings.Join(chunkTexts(cut), " "), "word") < 400 {
		t.Fatal("hard cut lost content")
	}
	json := knowledgeChunkText(`{"a":"`+strings.Repeat("x", 1500)+`"}`, ".json")
	if len(json) < 3 || json[0].SectionPath != "document" {
		t.Fatal("json not hard cut")
	}
}

func chunkTexts(chunks []knowledgeChunk) []string {
	out := []string{}
	for _, c := range chunks {
		out = append(out, c.Text)
	}
	return out
}

func TestKnowledgeSearchTokensAddKoreanBigrams(t *testing.T) {
	doc := knowledgeSearchTokens("배포절차를 따른다")
	for _, want := range []string{"배포절차", "배포", "포절", "절차"} {
		if !slices.Contains(doc, want) {
			t.Fatalf("%q missing from %q", want, doc)
		}
	}
	if q := knowledgeQueryTerms("절차"); !slices.Equal(q, []string{"절차"}) {
		t.Fatal(q)
	}
	if q := knowledgeQueryTerms("Docker 배포절차 docker"); !slices.Equal(q, []string{"docker", "배포절차", "배포", "포절", "절차"}) {
		t.Fatal(q)
	}
	r := Record{Text: "본문", Knowledge: &KnowledgeRecord{SectionPath: "인쇄 > 재단"}}
	if v := knowledgeSearchVector(r); !strings.Contains(v, "'재단'") || !strings.Contains(v, "'본문'") {
		t.Fatal(v)
	}
	if pgSearchTokens(colKnowledgeChunks, r) != knowledgeSearchVector(r) {
		t.Fatal("knowledge chunks must store knowledge tokens")
	}
	ranks := knowledgeTextRankRecords([]Record{{ID: "a", Text: "배포 이야기"}, {ID: "b", Text: "배포절차 문서"}, {ID: "c", Text: "무관한 글"}}, knowledgeQueryTerms("배포절차"))
	if len(ranks) != 2 || ranks[0].ID != "b" || ranks[0].Matched <= ranks[1].Matched {
		t.Fatalf("rank order %+v", ranks)
	}
}

// knowledgeRealFixture runs a non-synthetic Markdown source through ingest,
// human review, binding and activation.
func knowledgeRealFixture(t *testing.T) (*fixture, KnowledgeIngestResult, Record, KnowledgeRetrieveInput) {
	t.Helper()
	return knowledgeRealFixtureOn(t, nil)
}

func knowledgeRealFixtureOn(t *testing.T, repo Repository) (*fixture, KnowledgeIngestResult, Record, KnowledgeRetrieveInput) {
	t.Helper()
	f := handoffFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	for _, token := range []string{f.human, f.agent, f.work} {
		a := f.auth.actors[token]
		a.verbs["retrieve"] = true
		a.verbs["read"] = true
		f.auth.actors[token] = a
	}
	in := knowledgeFixtureInput()
	in.Synthetic = false
	in.SourceKey, in.Filename = "print-guide", "print-guide.md"
	in.Content = knowledgeGuideMarkdown
	ing, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	head, err := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, ing.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: ing.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	ing.Source = head
	b, err := f.svc.CreateKnowledgeBinding(testCtx, f.human, KnowledgeBindingInput{Scope: in.Scope, BindingKey: "guide", KnowledgeSourceID: head.ID, SpecialistProfileID: "illustrator", SpecialistProfileVersionID: "v1", SkillID: "illustrator.v1", TrustRequirement: "reviewed", AccessMode: "cite_required"})
	if err != nil {
		t.Fatal(err)
	}
	b = knowledgeActivate(t, f, b)
	req := KnowledgeRetrieveInput{Scope: in.Scope, ProfileID: "illustrator", ProfileVersion: "v1", SkillID: "illustrator.v1", SkillVersion: "v1", BindingIDs: []string{b.ID}, Query: "절차", MaxTokens: 4096, Limit: 4}
	return f, ing, b, req
}

func TestKnowledgeNonSyntheticSourceEndToEnd(t *testing.T) {
	f, ing, _, req := knowledgeRealFixture(t)
	if ing.Source.Knowledge.SourceMetadata["fixture"] != "" || ing.Source.Knowledge.SourceMetadata["format"] != ".md" {
		t.Fatalf("metadata %v", ing.Source.Knowledge.SourceMetadata)
	}
	anchor, err := f.repo.Get(testCtx, "sources", req.Scope, ing.Source.SourceEventID)
	if err != nil || anchor.SourceRef != "knowledge/"+ing.Source.ID || strings.Contains(anchor.Text, "Synthetic") {
		t.Fatalf("anchor %v %+v", err, anchor.SourceRef)
	}
	if ing.Document.Knowledge.ChunkerVersion != knowledgeChunkerVersion || len(ing.Chunks) != 4 {
		t.Fatalf("chunks %d %s", len(ing.Chunks), ing.Document.Knowledge.ChunkerVersion)
	}
	// "절차" only exists inside the compound "배포절차": the bigram finds it.
	got, err := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if err != nil || len(got.Context.Items) != 1 {
		t.Fatalf("bigram retrieval: %v %+v", err, got)
	}
	item := got.Context.Items[0]
	if item.SectionPath != "인쇄 가이드 > 재단" || item.Authority != "data_only_untrusted" || !strings.Contains(item.Content, "배포절차") || got.ExclusionSummary["no_text_match"] != 3 {
		t.Fatalf("evidence %+v %+v", item, got.ExclusionSummary)
	}
	log, err := f.svc.GetKnowledgeRetrievalLog(testCtx, f.work, got.RetrievalLogID, req.Scope)
	if err != nil || log.KnowledgeRetrieval.AdapterVersion != knowledgeFTSAdapter {
		t.Fatalf("adapter %v", err)
	}
	if again, err := f.svc.RevalidateKnowledge(testCtx, f.work, knowledgeValidateRequest(got)); err != nil || again.ContextHash != got.ContextHash {
		t.Fatalf("revalidate: %v", err)
	}
	// A heading word finds the chunks of its section through section_path.
	req.Query = "CMYK 색상"
	got, err = f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if err != nil || len(got.Context.Items) == 0 || got.Context.Items[0].SectionPath != "인쇄 가이드 > 색상 > CMYK" {
		t.Fatalf("section search: %v %+v", err, got)
	}
	// The agent still never reads the original file.
	if _, err = f.svc.ReadArtifact(testCtx, f.agent, req.Scope, ing.Source.SourceEventID, ing.Document.Knowledge.StorageRef); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent read raw non-synthetic file: %v", err)
	}
}

func TestKnowledgeOldChunkerVersionIsRechunkedNotMixed(t *testing.T) {
	f, ing, _, req := knowledgeRealFixture(t)
	scope := req.Scope
	// Pretend the stored source was chunked by the previous chunker.
	oldVersion := ContentHash("text/markdown:" + ing.Document.ContentHash + ":" + knowledgeParserVersion + ":runes-512-v1")
	oldDoc := ing.Document
	oldKnowledge := *oldDoc.Knowledge
	oldDoc.Knowledge = &oldKnowledge
	oldDoc.ID = "knowledge-document-" + ContentHash(ing.Source.ID+oldVersion)
	oldDoc.Knowledge.SourceVersion, oldDoc.Knowledge.ChunkerVersion = oldVersion, "runes-512-v1"
	if err := f.repo.Insert(testCtx, colKnowledgeDocuments, oldDoc); err != nil {
		t.Fatal(err)
	}
	// The old chunker's rows had other IDs; drop the current ones to model that.
	if err := f.repo.Delete(testCtx, colKnowledgeDocuments, scope, ing.Document.ID, ing.Document.Revision); err != nil {
		t.Fatal(err)
	}
	for _, c := range ing.Chunks {
		if err := f.repo.Delete(testCtx, colKnowledgeChunks, scope, c.ID, c.Revision); err != nil {
			t.Fatal(err)
		}
	}
	head := ing.Source
	headKnowledge := *head.Knowledge
	head.Knowledge = &headKnowledge
	head.Knowledge.SourceVersion, head.Knowledge.DocumentID, head.Knowledge.DocumentIDs = oldVersion, oldDoc.ID, []string{oldDoc.ID}
	if err := f.repo.Replace(testCtx, colKnowledgeSources, head, head.Revision); err != nil {
		t.Fatal(err)
	}
	head, _ = f.repo.Get(testCtx, colKnowledgeSources, scope, head.ID)
	in := knowledgeFixtureInput()
	in.Synthetic, in.SourceKey, in.Filename, in.Content, in.ExpectedRevision = false, "print-guide", "print-guide.md", knowledgeGuideMarkdown, head.Revision
	again, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil || again.Duplicate {
		t.Fatalf("same content under an older chunker must re-chunk: %v %v", err, again.Duplicate)
	}
	if again.Source.Status != "draft" || again.Source.Knowledge.SourceVersion == oldVersion || !slices.Equal(again.Source.Knowledge.DocumentIDs, []string{oldDoc.ID, again.Document.ID}) {
		t.Fatal("re-chunked source must be reviewed again")
	}
	for _, c := range again.Chunks {
		if c.Knowledge.ChunkerVersion != knowledgeChunkerVersion || c.Knowledge.DocumentID != again.Document.ID {
			t.Fatal("mixed chunker versions in one document")
		}
	}
	// A chunk whose chunker differs from its document is refused, never ranked.
	_, err = f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, again.Source.ID, KnowledgeStateInput{Scope: scope, ExpectedRevision: again.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RetrieveKnowledge(testCtx, f.work, req); err != nil {
		t.Fatal(err)
	}
	chunk := again.Chunks[0]
	chunk.Knowledge.ChunkerVersion = "runes-512-v1"
	if err = f.repo.Replace(testCtx, colKnowledgeChunks, chunk, chunk.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RetrieveKnowledge(testCtx, f.work, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("mixed chunk accepted: %v", err)
	}
}

func knowledgeHTTP(t *testing.T, f *fixture, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func knowledgeScopeQuery(s Scope) string {
	return url.Values{"workspace_id": {s.WorkspaceID}, "project_id": {s.ProjectID}, "user_id": {s.UserID}, "agent_id": {s.AgentID}}.Encode()
}

func TestKnowledgeListEndpoints(t *testing.T) {
	f, ing, b, req := knowledgeRealFixture(t)
	code, out := knowledgeHTTP(t, f, "GET", "/v1/knowledge/sources?"+knowledgeScopeQuery(req.Scope), f.human, "")
	if code != 200 {
		t.Fatal(code, out)
	}
	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatal(out)
	}
	src := items[0].(map[string]any)
	bindings, _ := src["bindings"].([]any)
	if src["id"] != ing.Source.ID || src["trust_level"] != "reviewed" || src["synthetic"] != false || src["format"] != ".md" || src["chunk_count"].(float64) != 4 || len(bindings) != 1 || bindings[0].(map[string]any)["id"] != b.ID {
		t.Fatal(src)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "배포절차") {
		t.Fatal("source list leaks file content")
	}
	code, out = knowledgeHTTP(t, f, "GET", "/v1/knowledge/documents?"+knowledgeScopeQuery(req.Scope)+"&source="+url.QueryEscape(ing.Source.ID), f.human, "")
	if code != 200 {
		t.Fatal(code, out)
	}
	docs, _ := out["items"].([]any)
	if len(docs) != 1 {
		t.Fatal(out)
	}
	doc := docs[0].(map[string]any)
	sections, _ := doc["sections"].([]any)
	if doc["id"] != ing.Document.ID || doc["current"] != true || doc["chunker_version"] != knowledgeChunkerVersion || len(sections) != 4 || sections[1] != "인쇄 가이드 > 재단" {
		t.Fatal(doc)
	}
	raw, _ = json.Marshal(out)
	if strings.Contains(string(raw), "배포절차") {
		t.Fatal("document list leaks file content")
	}
	for _, path := range []string{
		"/v1/knowledge/sources?" + knowledgeScopeQuery(req.Scope) + "&limit=0",
		"/v1/knowledge/sources?" + knowledgeScopeQuery(req.Scope) + "&extra=1",
		"/v1/knowledge/documents?" + knowledgeScopeQuery(req.Scope),
		"/v1/knowledge/sources?workspace_id=" + req.Scope.WorkspaceID,
	} {
		if code, _ := knowledgeHTTP(t, f, "GET", path, f.human, ""); code != 400 {
			t.Fatal(path, code)
		}
	}
	other := req.Scope
	other.ProjectID = "other"
	if code, out = knowledgeHTTP(t, f, "GET", "/v1/knowledge/sources?"+knowledgeScopeQuery(other), f.human, ""); code != 200 || len(out["items"].([]any)) != 0 {
		t.Fatal("cross scope listing", code, out)
	}
	if code, _ = knowledgeHTTP(t, f, "GET", "/v1/knowledge/documents?"+knowledgeScopeQuery(other)+"&source="+url.QueryEscape(ing.Source.ID), f.human, ""); code != 404 {
		t.Fatal("cross scope documents", code)
	}
	f.svc.SyntheticHandoff = false
	if code, _ = knowledgeHTTP(t, f, "GET", "/v1/knowledge/sources?"+knowledgeScopeQuery(req.Scope), f.human, ""); code != 403 {
		t.Fatal("feature flag off still lists", code)
	}
}

func TestKnowledgeSearchResolvesActiveBindings(t *testing.T) {
	f, _, b, req := knowledgeRealFixture(t)
	body, _ := json.Marshal(map[string]any{"scope": req.Scope, "specialist_profile_id": "illustrator", "query": "절차"})
	code, out := knowledgeHTTP(t, f, "POST", "/v1/knowledge/search", f.work, string(body))
	if code != 200 || out["authority"] != "data_only_untrusted" {
		t.Fatal(code, out)
	}
	ids, _ := out["binding_ids"].([]any)
	receipt, _ := out["receipt"].(map[string]any)
	if len(ids) != 1 || ids[0] != b.ID || receipt == nil || receipt["retrieval_log_id"] == "" || len(receipt["context"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatal(out)
	}
	// Another profile has no binding: an empty answer with a reason, no log.
	body, _ = json.Marshal(map[string]any{"scope": req.Scope, "specialist_profile_id": "listing_reviewer", "query": "절차"})
	if code, out = knowledgeHTTP(t, f, "POST", "/v1/knowledge/search", f.work, string(body)); code != 200 || out["reason"] != "no_active_binding" || out["receipt"] != nil {
		t.Fatal(code, out)
	}
	// Unknown profiles stay denied by the feature contract.
	body, _ = json.Marshal(map[string]any{"scope": req.Scope, "specialist_profile_id": "someone", "query": "절차"})
	if code, _ = knowledgeHTTP(t, f, "POST", "/v1/knowledge/search", f.work, string(body)); code != 403 {
		t.Fatal(code)
	}
	// A suspended binding is no longer searched.
	if _, err := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeBindings, b.ID, KnowledgeStateInput{Scope: req.Scope, ExpectedRevision: b.Revision, Status: "suspended"}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.SearchKnowledge(testCtx, f.work, KnowledgeSearchInput{Scope: req.Scope, ProfileID: "illustrator", Query: "절차"})
	if err != nil || got.Reason != "no_active_binding" {
		t.Fatalf("suspended binding searched: %v %+v", err, got)
	}
	// The budget still applies through the search path.
	if _, err = f.svc.SearchKnowledge(testCtx, f.work, KnowledgeSearchInput{Scope: req.Scope, ProfileID: "illustrator", Query: "절차", MaxTokens: 100}); err != nil {
		t.Fatalf("no binding must short-circuit before budget validation: %v", err)
	}
}

func TestKnowledgeSearchBudgetAndOrdering(t *testing.T) {
	f, _, _, req := knowledgeRealFixture(t)
	if _, err := f.svc.SearchKnowledge(testCtx, f.work, KnowledgeSearchInput{Scope: req.Scope, ProfileID: "illustrator", Query: "절차", MaxTokens: 100}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("budget below minimum: %v", err)
	}
	got, err := f.svc.SearchKnowledge(testCtx, f.work, KnowledgeSearchInput{Scope: req.Scope, ProfileID: "illustrator", Query: "인쇄 CMYK 저장 재단", MaxTokens: 256})
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.ExclusionSummary["context_budget"] == 0 {
		t.Fatalf("tight budget must exclude: %+v", got.Receipt)
	}
	wide, err := f.svc.SearchKnowledge(testCtx, f.work, KnowledgeSearchInput{Scope: req.Scope, ProfileID: "illustrator", Query: "인쇄 CMYK 저장", Limit: 8})
	if err != nil || len(wide.Receipt.Context.Items) < 2 || wide.Receipt.Context.Items[0].SectionPath != "인쇄 가이드 > 색상 > CMYK" {
		t.Fatalf("most matched terms first: %v %+v", err, wide.Receipt)
	}
}
