package memory

import (
	"errors"
	"testing"
)

func TestDeletingAMemoryClearsDigestsAtOnce(t *testing.T) {
	f := newFixture(t)
	a, b := f.digestMemory("decision", "키 보관은 금고"), f.digestMemory("decision", "키는 금고에 보관")
	active := f.approveDigest("topic", f.proposeTopic("키 보관", "키는 금고에 보관한다.", memRefs(a, b)))
	other := f.proposeTopic("금고", "금고에 둔다.", memRefs(a, b)) // an open candidate holds the same text
	if err := f.svc.Delete(testCtx, f.human, "memories", b.ID, ChangeInput{Scope: scopeA, ExpectedRevision: b.Revision}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{active.ID, other.ID} {
		r := f.repo.store[colTopicDigests][recKey(scopeA, id)]
		if r.Text != "" || r.Summary != "" || r.Digest.Title != "" || r.Status != "stale" || !r.ContentErased {
			t.Fatalf("digest %s kept text after its memory was deleted: %+v", id, r)
		}
	}
	if rec, _ := f.svc.RecallDigests(testCtx, f.agent, DigestRecallInput{Scope: scopeA, Query: "금고"}); len(rec.Items) != 0 {
		t.Fatal("recall after erasure")
	}
}

func TestErasingASourceDeletesItsDigests(t *testing.T) {
	f := newFixture(t)
	a, b := f.digestMemory("decision", "배포 창은 금요일 금지"), f.digestMemory("decision", "금요일 배포 금지")
	v := f.approveDigest("topic", f.proposeTopic("배포 창", "금요일에는 배포하지 않는다.", memRefs(a, b)))
	src := f.repo.store[colTopicDigests][recKey(scopeA, v.ID)].Sources[0]
	srcRec := f.repo.store["sources"][recKey(scopeA, src.SourceID)]
	if err := f.svc.Delete(testCtx, f.human, "sources", srcRec.ID, ChangeInput{Scope: scopeA, ExpectedRevision: srcRec.Revision}); err != nil {
		t.Fatal(err)
	}
	token := erasureWorker(f)
	f.auth.actors[token] = actor("eraser-1", "service", true, "erase", "embed", "read")
	for i := 0; i < 50; i++ {
		// Embedding jobs of the erased memories fail on their own; only an empty queue ends the loop.
		if err := f.svc.WorkOnce(testCtx, token); errors.Is(err, ErrNotFound) {
			break
		}
	}
	if _, err := f.repo.Get(testCtx, colTopicDigests, scopeA, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("digest survived the erasure of its source", err)
	}
}
