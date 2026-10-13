package memory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type interruptedReflectionPages struct{ Repository }

func (r interruptedReflectionPages) ListCandidates(ctx context.Context, in ReviewInput) ([]Record, error) {
	if in.Cursor != "" {
		return nil, ErrUnavailable
	}
	return r.Repository.ListCandidates(ctx, in)
}

func TestReflectionPastFirstPage(t *testing.T) { checkReflectionPastFirstPage(t, newFixture(t)) }
func TestPostgresReflectionPastFirstPage(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkReflectionPastFirstPage(t, f)
}
func checkReflectionPastFirstPage(t *testing.T, f *fixture) {
	t.Helper()
	for i := 0; i < 103; i++ {
		subject, text := "other-task", "Other lesson"
		if i >= 100 {
			subject, text = "wanted-task", "Keep independent evidence"
		}
		r := Record{ID: fmt.Sprintf("reflection-page-%03d", i), Scope: scopeA, Kind: "lesson", Status: "candidate", Revision: 1, Text: text, Trust: float64(103-i) / 104, CreatedAt: time.Now().UTC().Truncate(time.Millisecond), Tags: []string{learningTag(subject)}, Relations: []Ref{{Collection: "agent_runs", ID: fmt.Sprintf("run-%03d", i), Revision: 1}}}
		if err := f.svc.Repo.Insert(testCtx, "memory_candidates", r); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.svc.ListReflections(testCtx, f.human, scopeA, "wanted-task")
	if err != nil || len(page.Items) != 1 || page.Items[0].DistinctRuns != 3 || page.Items[0].Confidence != .875 || len(page.Items[0].CandidateRefs) != 3 {
		t.Fatalf("later-page lessons missing or undercounted: groups=%d err=%v", len(page.Items), err)
	}
	for _, ref := range page.Items[0].CandidateRefs {
		r, err := f.svc.Repo.Get(testCtx, "memory_candidates", scopeA, ref.ID)
		if err != nil || ref.Collection != "memory_candidates" || ref.Revision != r.Revision || r.Text != "Keep independent evidence" {
			t.Fatal("incorrect grouped candidate reference", err)
		}
	}
	f.svc.Repo = interruptedReflectionPages{f.svc.Repo}
	page, err = f.svc.ListReflections(testCtx, f.human, scopeA, "other-task")
	if err != ErrUnavailable || len(page.Items) != 0 {
		t.Fatal("partial reflection returned after later-page failure", err)
	}
}
