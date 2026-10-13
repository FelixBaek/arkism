package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestSupersedeNewMemoryAtomicHistory(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	old := f.activeMemory("old decision")
	in := SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "decision updated", NewContent: "new decision", Sources: refs(src)}
	f.repo.failAudit = true
	if failed, e := f.svc.Supersede(testCtx, f.human, old.ID, in); e == nil || failed.New.ID != "" {
		t.Fatal("new replacement audit failed open")
	}
	f.repo.failAudit = false
	if len(f.repo.rows("memories")) != 1 || len(f.repo.rows("memory_relations")) != 0 {
		t.Fatal("new replacement escaped rollback")
	}
	out, e := f.svc.Supersede(testCtx, f.human, old.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if out.New.TrustLevel != "user_verified" || out.Old.TrustLevel != old.TrustLevel || out.Old.Status != "superseded" || out.New.Status != "active" || out.New.Text != in.NewContent || out.Old.Text != old.Text || out.Old.SupersededBy.ID != out.New.ID || len(out.New.Supersedes) != 1 || out.New.Supersedes[0].ID != old.ID {
		t.Fatal("history links", out)
	}
	if out.Relation.Kind != "supersedes" || out.Relation.Relations[0].ID != out.New.ID || out.Relation.Relations[1].Revision != out.Old.Revision {
		t.Fatal("relation endpoints")
	}
	if _, e = f.svc.Supersede(testCtx, f.human, old.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("repeat supersede", e)
	}
}

type staleSupersedeSearch struct {
	Repository
	hits []Hit
}

func (r staleSupersedeSearch) SearchCollection(context.Context, string, SearchInput, []float64, string) ([][]Hit, error) {
	return [][]Hit{r.hits}, nil
}
func TestSupersedeExistingAndStaleSearch(t *testing.T) {
	f := newFixture(t)
	old := f.activeMemory("obsolete rule")
	next := f.activeMemory("current rule")
	in := SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "new rule", ReplacementMemoryID: next.ID, ReplacementExpectedRevision: next.Revision}
	f.repo.failAudit = true
	if out, e := f.svc.Supersede(testCtx, f.human, old.ID, in); e == nil || out.Old.ID != "" {
		t.Fatal("audit failed open")
	}
	f.repo.failAudit = false
	stored := f.get(f.human, "memories", scopeA, old.ID)
	if stored.Status != "active" || stored.SupersededBy != nil || len(f.repo.rows("memory_relations")) != 0 {
		t.Fatal("partial supersede escaped rollback")
	}
	out, e := f.svc.Supersede(testCtx, f.human, old.ID, in)
	if e != nil || out.New.ID != next.ID || out.New.Revision != next.Revision+1 || out.New.Text != next.Text {
		t.Fatal("existing replacement", e)
	}
	f.svc.Repo = staleSupersedeSearch{Repository: f.repo, hits: []Hit{HitFromRecord("memories", old), HitFromRecord("memories", next)}}
	results := f.search(f.human, SearchInput{Scope: scopeA, Query: "rule"})
	if len(results.Items) != 1 || results.Items[0].ID != next.ID {
		t.Fatal("stale index returned superseded memory")
	}
}
func TestSupersedeGuards(t *testing.T) {
	f := newFixture(t)
	old := f.activeMemory("old guarded rule")
	next := f.activeMemory("new guarded rule")
	in := SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "revised rule", ReplacementMemoryID: next.ID, ReplacementExpectedRevision: next.Revision}
	if _, e := f.svc.Transition(testCtx, f.human, "memories", old.ID, ChangeInput{Scope: scopeA, ExpectedRevision: old.Revision, Status: "superseded"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("bare supersede accepted", e)
	}
	if _, e := f.svc.Supersede(testCtx, f.agent, old.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("agent supersede", e)
	}
	for _, tc := range []struct {
		name   string
		change func(*SupersedeInput)
		want   error
	}{
		{"self", func(x *SupersedeInput) { x.ReplacementMemoryID = old.ID }, ErrInvalid},
		{"mixed", func(x *SupersedeInput) { x.NewContent = "extra" }, ErrInvalid},
		{"missing reason", func(x *SupersedeInput) { x.Reason = " " }, ErrInvalid},
		{"secret", func(x *SupersedeInput) { x.Reason = "password=synthetic-secret" }, ErrSecretDetected},
		{"stale target", func(x *SupersedeInput) { x.ReplacementExpectedRevision++ }, ErrConflict},
		{"scope", func(x *SupersedeInput) { x.Scope.ProjectID = "other" }, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := in
			tc.change(&v)
			if _, e := f.svc.Supersede(testCtx, f.human, old.ID, v); !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
		})
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: next.ID}
	if _, e := f.svc.Supersede(testCtx, f.human, old.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("target permission bypass", e)
	}
	f.svc.Auth = unscannedAuth{f.auth}
	if _, e := f.svc.Supersede(testCtx, f.human, old.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("policy scan bypass", e)
	}
}

func TestSupersedeHTTPAndSourceProof(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	old := f.activeMemory("previous rule")
	in := SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "corrected contact", NewContent: "Contact reviewer@example.test", Sources: refs(src)}
	in.Sources[0].Span.End = 64000
	if _, e := f.svc.Supersede(testCtx, f.human, old.ID, in); !errors.Is(e, ErrInvalid) {
		t.Fatal("invalid source proof accepted", e)
	}
	in.Sources = refs(src)
	payload, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", "/v1/memories/"+old.ID+"/supersede", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var out SupersedeOutput
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.New.Sensitivity != "private" || out.New.Inspection == nil || !out.New.Inspection.PersonalData {
		t.Fatal("HTTP private replacement", w.Code)
	}
}
func TestSupersedeCannotRetireReplacementDependency(t *testing.T) {
	f := newFixture(t)
	old := f.activeMemory("dependency rule")
	next := f.activeMemory("dependent rule")
	next.Relations = []Ref{{Collection: "memories", ID: old.ID, Revision: old.Revision}}
	f.repo.store["memories"][recKey(scopeA, next.ID)] = next
	_, e := f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "replace", ReplacementMemoryID: next.ID, ReplacementExpectedRevision: next.Revision})
	if !errors.Is(e, ErrConflict) {
		t.Fatal("replacement dependency retired", e)
	}
}
