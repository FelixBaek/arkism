package memory

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func docGet(t *testing.T, f *fixture, path, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func docScopeQuery(s Scope) string {
	return "workspace_id=" + s.WorkspaceID + "&project_id=" + s.ProjectID + "&user_id=" + s.UserID + "&agent_id=" + s.AgentID
}

func TestDocumentChunksAndHistoryRoutes(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim), extractAnswer(), extractAnswer())
	filler := strings.Repeat(" 배포 전에 테스트를 실행한다.", 200)
	p1, p2, p3 := "선택 상태에는 주황 강조선을 쓰지 않는다."+filler, "둘째 문단."+filler, "셋째 문단."+filler
	first := extractUpload(t, f, p1, p2)
	extractUpload(t, f, p1, p2)
	last := extractUpload(t, f, p3, p2)
	if first.DocumentID == "" || last.DocumentID != first.DocumentID {
		t.Fatalf("document ids %q %q", first.DocumentID, last.DocumentID)
	}
	base := "/v1/files/memories/documents/" + first.DocumentID
	q := "?" + docScopeQuery(scopeA)
	code, page := docGet(t, f, base+"/chunks"+q+"&limit=1", f.human)
	items, _ := page["items"].([]any)
	if code != 200 || len(items) != 1 || page["total"] != float64(2) || page["next_cursor"] != "1" || page["job_id"] != last.JobID {
		t.Fatalf("chunks page 1: %d %v", code, page)
	}
	kept := items[0].(map[string]any)
	if kept["status"] != "unchanged" || kept["locator"] == "" || kept["hash"] == "" || kept["text"] != nil {
		t.Fatalf("kept chunk %v", kept)
	}
	code, page = docGet(t, f, base+"/chunks"+q+"&limit=1&cursor=1", f.human)
	if items, _ = page["items"].([]any); code != 200 || len(items) != 1 || page["next_cursor"] != "" || items[0].(map[string]any)["status"] != "extracted" {
		t.Fatalf("chunks page 2: %d %v", code, page)
	}
	// History: the first upload's chunk:1 produced the one candidate.
	code, page = docGet(t, f, base+"/history"+q+"&limit=2", f.human)
	items, _ = page["items"].([]any)
	if code != 200 || len(items) != 2 || items[0].(map[string]any)["job_id"] != last.JobID || page["next_cursor"] != first.JobID {
		t.Fatalf("history page 1: %d %v", code, page)
	}
	if delta := items[0].(map[string]any)["delta"].(map[string]any); delta["chunks_removed"] != float64(1) || delta["chunks_unchanged"] != float64(1) {
		t.Fatalf("delta %v", delta)
	}
	code, page = docGet(t, f, base+"/history"+q+"&cursor="+first.JobID, f.human)
	if items, _ = page["items"].([]any); code != 200 || len(items) != 1 || page["next_cursor"] != "" || items[0].(map[string]any)["candidates_created"] != float64(1) {
		t.Fatalf("history page 2: %d %v", code, page)
	}
	for _, bad := range []string{base + "/chunks" + q + "&limit=0", base + "/chunks" + q + "&cursor=x", base + "/history" + q + "&limit=999", base + "/chunks?" + "evil=1"} {
		if code, _ := docGet(t, f, bad, f.human); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	if code, _ := docGet(t, f, base+"/chunks"+q, "not-a-token"); code == 200 {
		t.Fatal("unauthenticated read")
	}
	if code, _ := docGet(t, f, base+"/chunks?"+docScopeQuery(Scope{WorkspaceID: "ws-b", ProjectID: "proj-b"}), f.human); code != 404 && code != 403 {
		t.Fatalf("other scope: %d", code)
	}
	if code, _ := docGet(t, f, "/v1/files/memories/documents/"+first.JobID+"/chunks"+q, f.human); code != 404 {
		t.Fatalf("a job is not a document: %d", code)
	}
}

func TestDocumentChunksShowCandidateState(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
	out := extractUpload(t, f, "선택 상태에는 주황 강조선을 쓰지 않는다.")
	got, err := f.svc.DocumentChunks(testCtx, f.human, scopeA, out.DocumentID, DocumentPage{Limit: 10})
	if err != nil || len(got.Items) != 1 || len(got.Items[0].Candidates) != 1 || got.Items[0].Candidates[0].Status != "candidate" {
		t.Fatalf("chunks %+v %v", got, err)
	}
	cand, _ := f.repo.Get(testCtx, "memory_candidates", scopeA, got.Items[0].Candidates[0].ID)
	cand.Status = "approved"
	if err = f.repo.Replace(testCtx, "memory_candidates", cand, cand.Revision); err != nil {
		t.Fatal(err)
	}
	got, _ = f.svc.DocumentChunks(testCtx, f.human, scopeA, out.DocumentID, DocumentPage{Limit: 10})
	if c := got.Items[0].Candidates[0]; c.Status != "approved" || c.MemoryID != cand.ID {
		t.Fatalf("approved candidate %+v", c)
	}
}

func TestKnowledgeChunksAndHistoryRoutes(t *testing.T) {
	f, _ := wikiFixture(t)
	src := wikiSource(t, f, "print", knowledgeFixtureInput().Content)
	in := knowledgeFixtureInput()
	in.SourceKey, in.Filename, in.Content, in.ExpectedRevision = "print", "print.md", in.Content+"\n\n## 추가\n새 문단.", src.Revision
	if _, err := f.svc.IngestKnowledge(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	q := "?" + docScopeQuery(src.Scope)
	code, page := docGet(t, f, "/v1/knowledge/sources/"+src.ID+"/chunks"+q+"&limit=1", f.human)
	items, _ := page["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["hash"] == "" || items[0].(map[string]any)["text"] == "" {
		t.Fatalf("knowledge chunks: %d %v", code, page)
	}
	code, page = docGet(t, f, "/v1/knowledge/sources/"+src.ID+"/history"+q, f.human)
	items, _ = page["items"].([]any)
	if code != 200 || len(items) != 2 || items[0].(map[string]any)["current"] != true || items[1].(map[string]any)["current"] != false {
		t.Fatalf("knowledge history: %d %v", code, page)
	}
	if code, _ := docGet(t, f, "/v1/knowledge/sources/"+src.ID+"/history"+q, "not-a-token"); code == 200 {
		t.Fatal("history readable without a token")
	}
}
