package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostgresClassifierCanOnlyClassify(t *testing.T) {
	admin := isolatedPostgres(t)
	ctx := context.Background()
	scope := Scope{WorkspaceID: "org-a", ProjectID: "project-a"}
	now := time.Now().UTC()
	cand := Record{ID: "cand", Scope: scope, Revision: 1, Status: "candidate", Kind: "semantic", Text: "proposal", CreatedAt: now, UpdatedAt: now}
	if err := admin.Insert(ctx, "memory_candidates", cand); err != nil {
		t.Fatal(err)
	}
	token := testPrincipalToken("classifier-token-that-is-never-stored-raw")
	if err := admin.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: "arkism-memory-reviewer", Role: roleClassifier, Token: token,
		OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"db:read", "db:write", "classify:memory_candidates"}}); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, admin)
	bound := ContextWithPostgresToken(ctx, token)

	classified, err := admin.Get(ctx, "memory_candidates", scope, cand.ID) // stored times, as the service would hold them
	if err != nil {
		t.Fatal(err)
	}
	classified.Revision, classified.UpdatedAt = 2, now.Add(time.Second)
	classified.Classification = &CandidateClassification{Risk: "high", By: "arkism-memory-reviewer"}
	forged := classified
	forged.Classification = &CandidateClassification{Risk: "high", By: "someone-else"}
	if err := app.Replace(bound, "memory_candidates", forged, 1); !errors.Is(err, ErrDenied) {
		t.Fatalf("classifier signed as another principal: %v", err)
	}
	if err := app.Replace(bound, "memory_candidates", classified, 1); err != nil {
		t.Fatalf("classification refused: %v", err)
	}
	event := func(by, target string) Record {
		return Record{ID: ID(), Scope: scope, Kind: "classify:memory_candidates", Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: by, Target: &Ref{ID: target}}
	}
	if err := app.Insert(bound, "audit_events", event("someone-else", cand.ID)); !errors.Is(err, ErrDenied) {
		t.Fatalf("classifier forged an audit actor: %v", err)
	}
	if err := app.Insert(bound, "audit_events", event("arkism-memory-reviewer", "other-cand")); !errors.Is(err, ErrDenied) {
		t.Fatalf("classifier audited a candidate it did not classify: %v", err)
	}
	if err := app.Insert(bound, "audit_events", event("arkism-memory-reviewer", cand.ID)); err != nil {
		t.Fatalf("classification audit refused: %v", err)
	}
	deniedRead := event("arkism-memory-reviewer", cand.ID)
	deniedRead.Kind = "read:memory_candidates"
	deniedRead.Audit = &AuditMetadata{RequestID: "synthetic-read", Decision: "allow"}
	if err := app.Insert(bound, "audit_events", deniedRead); !errors.Is(err, ErrDenied) {
		t.Fatalf("classifier without read permission inserted a read audit: %v", err)
	}
	approved := classified
	approved.Revision, approved.Status = 3, "approved"
	if err := app.Replace(bound, "memory_candidates", approved, 2); !errors.Is(err, ErrDenied) {
		t.Fatalf("classifier approved: %v", err)
	}
	edited := classified
	edited.Revision, edited.Text = 3, "rewritten"
	if err := app.Replace(bound, "memory_candidates", edited, 2); !errors.Is(err, ErrDenied) {
		t.Fatalf("classifier rewrote text: %v", err)
	}
	mem := Record{ID: "mem", Scope: scope, Revision: 1, Status: "active", Kind: "semantic", Text: "x", CreatedAt: now, UpdatedAt: now}
	if err := app.Insert(bound, "memories", mem); !errors.Is(err, ErrDenied) {
		t.Fatalf("classifier wrote a memory: %v", err)
	}
	if got, err := admin.Get(ctx, "memory_candidates", scope, cand.ID); err != nil || got.Status != "candidate" || got.Text != "proposal" || got.Classification.Risk != "high" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPostgresClassifierAuditsSingleCandidateRead(t *testing.T) {
	admin := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	migrations := schemaMigrations()
	if err := admin.migrateSchema(ctx, migrations[:11]); err != nil {
		t.Fatal(err)
	}
	scope := Scope{WorkspaceID: "org-a", ProjectID: "project-a"}
	now := time.Now().UTC()
	candidate := Record{ID: "review-only", Scope: scope, Kind: "episodic", Status: "candidate", Revision: 1, Text: "synthetic rehearsal", CreatedAt: now, UpdatedAt: now}
	source := Record{ID: "synthetic-source", Scope: scope, Kind: "document", Status: "active", Revision: 1, Text: candidate.Text, CreatedAt: now, UpdatedAt: now}
	if err := admin.Insert(ctx, "sources", source); err != nil {
		t.Fatal(err)
	}
	candidate.Sources = []SourceRef{{SourceID: source.ID, Locator: "chunk:1"}}
	if err := admin.Insert(ctx, "memory_candidates", candidate); err != nil {
		t.Fatal(err)
	}
	grant := PrincipalGrant{PrincipalID: "pc-reviewer", Role: roleClassifier, Token: testPrincipalToken("isolated single review fixture"), OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"db:read", "db:write", "read:memory_candidates", "classify:memory_candidates"}}
	if err := admin.ProvisionPrincipal(ctx, grant); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, admin)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: app, Auth: auth}
	bound := ContextWithPostgresToken(ctx, grant.Token)
	read := func() (Record, error) { return svc.Get(bound, grant.Token, "memory_candidates", scope, candidate.ID) }
	if _, err = read(); !errors.Is(err, ErrDenied) {
		t.Fatalf("before migration, single review read = %v", err)
	}
	if err = admin.migrateSchema(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	before, err := read()
	if err != nil {
		t.Fatalf("read before classification: %v", err)
	}
	if _, err = svc.ClassifyCandidate(bound, grant.Token, candidate.ID, CandidateClassifyInput{Scope: scope, ExpectedRevision: before.Revision, Risk: "high"}); err != nil {
		t.Fatal(err)
	}
	after, err := read()
	if err != nil || after.Status != "candidate" || after.Text != before.Text || after.ID != before.ID || after.Revision != before.Revision+1 || after.Classification == nil || after.Classification.By != grant.PrincipalID {
		t.Fatalf("read after classification: revision=%d status=%q err=%v", after.Revision, after.Status, err)
	}
	var count int
	if err = admin.pool.QueryRow(ctx, "SELECT count(*) FROM arkism.memory_records WHERE collection='audit_events' AND kind='read:memory_candidates' AND doc->>'created_by'=$1 AND doc->'target'->>'id'=$2", grant.PrincipalID, candidate.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("read audit count=%d err=%v", count, err)
	}
	assertForgedClassifierAuditsDenied(t, bound, app, scope, candidate, grant)
	verifyHTTPReclassification(t, admin, svc, scope, after, grant)
}

func assertForgedClassifierAuditsDenied(t *testing.T, bound context.Context, app *Postgres, scope Scope, candidate Record, grant PrincipalGrant) {
	t.Helper()
	event := Record{Scope: scope, Kind: "read:memory_candidates", Status: "active", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), CreatedBy: grant.PrincipalID, Target: &Ref{ID: candidate.ID}, Audit: &AuditMetadata{RequestID: "synthetic-read", Decision: "allow"}}
	// Reject a different signer, a missing target, another user scope, and a different audit action.
	for _, mutate := range []func(*Record){
		func(r *Record) { r.CreatedBy = "other" },
		func(r *Record) { r.Target = &Ref{ID: "absent"} },
		func(r *Record) { r.Scope.UserID = "other" },
		func(r *Record) { r.Kind = "read:memories" },
		func(r *Record) { r.Text = "unexpected audit content" },
	} {
		attempt := event
		attempt.ID = ID()
		mutate(&attempt)
		if err := app.Insert(bound, "audit_events", attempt); !errors.Is(err, ErrDenied) {
			t.Fatalf("forged read audit = %v", err)
		}
	}
}

func verifyHTTPReclassification(t *testing.T, admin *Postgres, svc *Service, scope Scope, after Record, grant PrincipalGrant) {
	t.Helper()
	human := PrincipalGrant{PrincipalID: "qa-human-reviewer", Role: "human", Token: testPrincipalToken("synthetic HTTP reclassification reviewer"), OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"db:read", "db:write", "review:memory_candidates", "read:memory_candidates"}}
	if err := admin.ProvisionPrincipal(context.Background(), human); err != nil {
		t.Fatal(err)
	}
	h := Handler(svc)
	call := func(path, token string, body any, want int) Record {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/v1/memory_candidates/"+after.ID+"/"+path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s status=%d: %s", path, w.Code, w.Body.String())
		}
		var rec Record
		if want == 200 {
			if e := json.Unmarshal(w.Body.Bytes(), &rec); e != nil {
				t.Fatal(e)
			}
		}
		return rec
	}
	input := ReclassifyInput{Scope: scope, ExpectedRevision: after.Revision}

	reset := call("reclassify", human.Token, input, 200)
	if reset.ID != after.ID || reset.Revision != after.Revision+1 || reset.Status != "candidate" || reset.Classification == nil || reset.Classification.By != "" {
		t.Fatalf("failed to clear classification: %+v", reset)
	}
	call("reclassify", human.Token, input, 409)
	reviewed := call("classify", grant.Token, CandidateClassifyInput{Scope: scope, ExpectedRevision: reset.Revision, Risk: "low"}, 200)
	if reviewed.Revision != reset.Revision+1 || reviewed.Status != "candidate" || reviewed.ID != after.ID || reviewed.Text != after.Text || reviewed.Classification.Risk != "low" || reviewed.Classification.By != grant.PrincipalID {
		t.Fatalf("failed classifier replay: %+v", reviewed)
	}
	call("reclassify", grant.Token, input, 403)
	t.Log("HTTP reclassify classifier denied (403 explicit database guard) human=200 stale=409; classifier replay=200; same candidate r4")
}

func TestClassifierWriteRefusalIsNotStorageOutage(t *testing.T) {
	refusal := classifierWriteRefusal{}
	if refusal.Error() != ErrDenied.Error() {
		t.Fatalf("public classifier refusal = %q, want %q", refusal.Error(), ErrDenied.Error())
	}
	if !errors.Is(refusal, ErrDenied) || denialAuditFailed(refusal) || !denialAuditFailed(ErrUnavailable) || !denialAuditFailed(ErrDenied) {
		t.Fatal("classifier guard, unspecified denial, or storage outage incorrectly mapped")
	}
}
