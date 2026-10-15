package memory

import (
	"context"
	"errors"
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
	const token = "classifier-token-that-is-never-stored-raw"
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
