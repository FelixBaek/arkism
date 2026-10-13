package memory

import (
	"errors"
	"strings"
	"testing"
)

func TestSourceDeduplicationAndIdempotency(t *testing.T) {
	f := newFixture(t)
	in := CreateInput{Scope: scopeA, Text: "unique source", IdempotencyKey: "request-1"}
	first := f.create(f.human, "sources", sourceInput(in))
	if first.Duplicate || first.ContentHash != ContentHash(in.Text) {
		t.Fatal("invalid first digest")
	}
	if e := f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrNotFound) {
		t.Fatal("source registration queued embedding", e)
	}
	again := f.create(f.human, "sources", sourceInput(in))
	if structuredText(first) != structuredText(again) {
		t.Fatal("idempotency changed first response")
	}
	in.IdempotencyKey = "request-2"
	dup := f.create(f.human, "sources", sourceInput(in))
	if dup.ID != first.ID || !dup.Duplicate || len(f.repo.rows("sources")) != 1 {
		t.Fatal("duplicate source created")
	}
	if repeated := f.create(f.human, "sources", sourceInput(in)); structuredText(dup) != structuredText(repeated) {
		t.Fatal("duplicate receipt changed")
	}
	in.Text = "other payload"
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(in)); !errors.Is(e, ErrConflict) {
		t.Fatal("key reuse accepted")
	}
	in.Text = "unique source"
	in.Scope = scopeB
	if other := f.create(f.human, "sources", sourceInput(in)); other.ID == first.ID {
		t.Fatal("cross-scope deduplication")
	}
	if e := f.svc.Delete(testCtx, f.human, "sources", first.ID, ChangeInput{Scope: scopeA, ExpectedRevision: first.Revision}); e != nil {
		t.Fatal(e)
	}
	in.Scope = scopeA
	for _, key := range []string{"request-1", "request-2", ""} {
		in.IdempotencyKey = key
		if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(in)); !errors.Is(e, ErrConflict) {
			t.Fatal("deleted source resurrected")
		}
	}
}
func TestSourceRegistrationAtomicityAndPolicy(t *testing.T) {
	f := newFixture(t)
	in := CreateInput{Scope: scopeA, Text: "atomic source", IdempotencyKey: "atomic"}
	f.repo.failAudit = true
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(in)); e == nil {
		t.Fatal("audit failure ignored")
	}
	if len(f.repo.rows("sources")) != 0 || len(f.repo.rows("jobs")) != 0 {
		t.Fatal("partial registration persisted")
	}
	f.repo.failAudit = false
	first := f.create(f.human, "sources", sourceInput(in))
	in.IdempotencyKey = ""
	in.Sensitivity = "restricted"
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(in)); !errors.Is(e, ErrConflict) {
		t.Fatal("classification conflict overwritten")
	}
	in.Sensitivity = ""
	who := f.auth.actors[f.human]
	who.verbs["read"] = false
	f.auth.actors[f.human] = who
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(in)); !errors.Is(e, ErrDenied) {
		t.Fatal("write-only duplicate returned old source")
	}
	for _, job := range f.repo.rows("jobs") {
		if job.Text != "" || job.Summary != "" || job.Target == nil || job.Target.ID != first.ID {
			t.Fatal("invalid source receipt")
		}
	}
}

func TestSourceScopeAndIdempotencyValidation(t *testing.T) {
	f := newFixture(t)
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Text: "scope required"})); !errors.Is(e, ErrInvalid) {
		t.Fatal("missing source scope accepted")
	}
	for _, key := range []string{"key with spaces", strings.Repeat("x", 129)} {
		if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "key", IdempotencyKey: key})); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid key accepted")
		}
	}
	base := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "scope identity"}))
	for i := 0; i < 4; i++ {
		scope := scopeA
		switch i {
		case 0:
			scope.WorkspaceID = "other"
		case 1:
			scope.ProjectID = "other"
		case 2:
			scope.UserID = "other"
		case 3:
			scope.AgentID = "other"
		}
		if r := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scope, Text: "scope identity"})); r.ID == base.ID || r.Duplicate {
			t.Fatal("scope identity collision")
		}
	}
}
