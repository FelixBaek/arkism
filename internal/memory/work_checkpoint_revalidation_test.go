package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestWorkCheckpointHTTPProgramRoundtrip(t *testing.T) {
	f := newFixture(t)
	h := Handler(f.svc)
	call := func(path, token string, payload any) *httptest.ResponseRecorder {
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/v1/work-checkpoints/"+path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	pkg := workCheckpointPackage()
	pkg.Status = "in_progress"
	w := call("save", f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "http-transfer", EventID: "evt", Package: pkg})
	if w.Code != 200 {
		t.Fatalf("save status %d", w.Code)
	}
	var saved WorkCheckpointSaveOutput
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || bytes.Contains(w.Body.Bytes(), []byte(pkg.Goal)) {
		t.Fatal("invalid save metadata")
	}
	w = call("get", f.human, WorkCheckpointGetInput{Scope: scopeA, ID: saved.ID})
	if w.Code != 200 {
		t.Fatalf("resume status %d", w.Code)
	}
	var resumed WorkCheckpointView
	if err := json.Unmarshal(w.Body.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Package.Goal != pkg.Goal || resumed.Package.Files[0].SHA256 != pkg.Files[0].SHA256 || resumed.Authority != AuthorityDataOnly {
		t.Fatal("work was not preserved across callers")
	}
	w = call("save", f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: saved.ID, EventID: "another", Package: pkg})
	if w.Code != 409 {
		t.Fatalf("stale update status %d", w.Code)
	}
}

func TestWorkCheckpointRetryBindsExpectedRevision(t *testing.T) {
	f := newFixture(t)
	pkg := workCheckpointPackage()
	if _, err := workCheckpointSave(f, f.agent, "retry-binding", "event", 0, pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := workCheckpointSave(f, f.agent, "retry-binding", "event", 1, pkg); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request revision reused an old receipt: %v", err)
	}
}

func TestWorkCheckpointReadsRecheckDeletedEvidence(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	if _, err := workCheckpointSave(f, f.agent, "evidence", "event", 0, pkg); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "evidence"}); err == nil {
		t.Fatal("checkpoint body remained readable after its evidence was removed")
	}
	if _, err := f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA}); err == nil {
		t.Fatal("checkpoint title remained readable after its evidence was removed")
	}
}
