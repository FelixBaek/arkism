//go:build linux

package memory

import (
	"errors"
	"testing"
)

func TestWikiLockToggleAndJobRead(t *testing.T) {
	f, _ := wikiFixture(t, wikiPrintAnswers()...)
	src := wikiSource(t, f, "print", wikiPrintSource)
	scope := src.Scope
	job := wikiBuild(t, f, src)
	bleed := wikiPageID(scope, "bleed")
	page, err := f.svc.GetWikiPage(testCtx, f.human, scope, bleed)
	if err != nil || page.Locked {
		t.Fatalf("page: %+v %v", page, err)
	}

	if _, err = f.svc.LockWikiPage(testCtx, f.human, "", WikiLockInput{Scope: scope, Locked: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty id: %v", err)
	}
	if _, err = f.svc.LockWikiPage(testCtx, f.human, bleed, WikiLockInput{Scope: scope, ExpectedRevision: page.Revision + 7, Locked: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err = f.svc.LockWikiPage(testCtx, f.human, wikiPageID(scope, "missing"), WikiLockInput{Scope: scope, Locked: true}); err == nil {
		t.Fatal("missing page locked")
	}
	locked, err := f.svc.LockWikiPage(testCtx, f.human, bleed, WikiLockInput{Scope: scope, ExpectedRevision: page.Revision, Locked: true})
	if err != nil || !locked.Locked || locked.Revision <= page.Revision {
		t.Fatalf("lock: %+v %v", locked, err)
	}
	// The old revision is spent; replaying it conflicts.
	if _, err = f.svc.LockWikiPage(testCtx, f.human, bleed, WikiLockInput{Scope: scope, ExpectedRevision: page.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay: %v", err)
	}
	open, err := f.svc.LockWikiPage(testCtx, f.human, bleed, WikiLockInput{Scope: scope, ExpectedRevision: locked.Revision})
	if err != nil || open.Locked {
		t.Fatalf("unlock: %+v %v", open, err)
	}

	got, err := f.svc.GetWikiJob(testCtx, f.human, scope, job.ID)
	if err != nil || got.ID != job.ID || got.Status != JobStatusDone {
		t.Fatalf("job: %+v %v", got, err)
	}
	if _, err = f.svc.GetWikiJob(testCtx, f.human, scope, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty job id: %v", err)
	}
	if _, err = f.svc.GetWikiJob(testCtx, "nobody", scope, job.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("anonymous job read: %v", err)
	}
	// A wiki page id is not a wiki job.
	if _, err = f.svc.GetWikiJob(testCtx, f.human, scope, bleed); err == nil {
		t.Fatal("page read as a job")
	}
}
