package memory

import (
	"errors"
	"testing"
	"time"
)

func TestRunSourceFenceAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f := newFixture(t)
		src := f.source()
		f.repo.failAudit = fail
		in := completedRunInput(CreateInput{Scope: scopeA, Text: "source-linked task result", Sources: append(refs(src), refs(src)...)})
		_, e := f.svc.Create(testCtx, f.human, "agent_runs", in)
		want := int64(1)
		if fail {
			want = 0
			if !errors.Is(e, ErrUnavailable) {
				t.Fatal(e)
			}
		} else if e != nil {
			t.Fatal(e)
		}
		saved, e := f.repo.Get(testCtx, "sources", scopeA, src.ID)
		if e != nil || saved.MemoryReferenceFence != want || saved.Revision != src.Revision {
			t.Fatalf("source fence=%d want=%d revision=%d: %v", saved.MemoryReferenceFence, want, saved.Revision, e)
		}
	}
}

func TestConflictCannotCopyInvalidSource(t *testing.T) {
	for _, state := range []string{"deleted", "expired", "inactive"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			a, b := f.activeMemory("first decision"), f.activeMemory("second decision")
			src, e := f.repo.Get(testCtx, "sources", scopeA, a.Sources[0].SourceID)
			if e != nil {
				t.Fatal(e)
			}
			if state == "deleted" {
				e = f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision})
			} else {
				if state == "expired" {
					past := time.Now().Add(-time.Minute)
					src.ExpiresAt = &past
				} else {
					src.Status = "expired"
				}
				e = f.repo.Replace(testCtx, "sources", src, src.Revision)
			}
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "mark", Reason: "incompatible decisions", Memories: []Ref{{Collection: "memories", ID: a.ID, Revision: a.Revision}, {Collection: "memories", ID: b.ID, Revision: b.Revision}}})
			if !errors.Is(e, ErrDenied) {
				t.Fatalf("deleted source copied into relation: %v", e)
			}
			for _, before := range []Record{a, b} {
				after, e := f.repo.Get(testCtx, "memories", scopeA, before.ID)
				if e != nil || after.Revision != before.Revision || after.Status != before.Status || after.ConflictID != "" {
					t.Fatalf("failed mark changed member %+v %v", after, e)
				}
			}
			if len(f.repo.rows("memory_relations")) != 0 {
				t.Fatal("failed mark left relation")
			}
		})
	}
}
