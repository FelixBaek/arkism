package memory

import (
	"errors"
	"testing"
)

func TestRestoreMemoryBringsBackADeletedMemory(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("restorable note")
	gone, err := f.svc.Transition(testCtx, f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision, Status: "rejected", ReasonCode: "wrong"})
	if err != nil || gone.Removal == nil {
		t.Fatal(gone, err)
	}
	page, err := f.svc.QueryMemoryCatalog(testCtx, f.human, CatalogQueryInput{Scope: scopeA, State: "removed"}, false)
	if err != nil || page.Total != 1 || page.Items[0].ID != m.ID {
		t.Fatal("removed list", page.Total, err)
	}
	in := MemoryRestoreInput{Scope: scopeA, ExpectedRevision: gone.Revision}
	if _, err = f.svc.RestoreMemory(testCtx, f.agent, m.ID, in); !errors.Is(err, ErrDenied) {
		t.Fatal("agent restored", err)
	}
	if _, err = f.svc.RestoreMemory(testCtx, f.human, m.ID, MemoryRestoreInput{Scope: scopeA, ExpectedRevision: m.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision", err)
	}
	back, err := f.svc.RestoreMemory(testCtx, f.human, m.ID, in)
	if err != nil || !live(back) || back.Removal != nil || back.ApprovedBy != m.ApprovedBy || back.Revision != gone.Revision+1 {
		t.Fatal("restore", back, err)
	}
	if _, err = f.svc.RestoreMemory(testCtx, f.human, m.ID, MemoryRestoreInput{Scope: scopeA, ExpectedRevision: back.Revision}); !errors.Is(err, ErrInvalid) {
		t.Fatal("restored an active memory", err)
	}
	audited := false
	for _, row := range f.repo.rows("audit_events") {
		if row.Kind == "transition:memories:restored" && row.Target != nil && row.Target.ID == m.ID {
			audited = true
		}
	}
	if !audited {
		t.Fatal("restore not audited")
	}
	if _, err = f.svc.RestoreMemory(testCtx, f.human, "missing", MemoryRestoreInput{Scope: scopeA, ExpectedRevision: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing memory", err)
	}
}
