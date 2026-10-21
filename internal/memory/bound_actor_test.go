package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func exerciseBatchBoundActor(t *testing.T, repo Repository, field string) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	input := CandidateReviewBatchInput{Scope: scopeA}
	for _, text := range []string{"first approved", "middle candidate", "last approved"} {
		c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src)}))
		input.Items = append(input.Items, CandidateReviewBatchItem{ID: c.ID, ExpectedRevision: c.Revision, Action: "approve"})
	}
	changed := false
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
		if in.ResourceID == input.Items[1].ID && in.Action == "transition:memory_candidates" {
			changed = true
			if field == "principal" {
				d.PrincipalID = "other-human"
			}
			if field == "role" {
				d.Role = "agent"
			}
		}
		return nil
	}}
	req := httptest.NewRequest("POST", "/v1/memory_candidates/review_batch", strings.NewReader(structuredText(input)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if field != "none" {
		if w.Code != 403 || !changed || strings.Contains(w.Body.String(), "items") {
			t.Fatal("changed HTTP actor returned batch data", w.Code, changed, w.Body.String())
		}
		// Batches use separate transactions. A completed authorized item remains;
		// the changed identity and all subsequent items must leave no new memory.
		for i, item := range input.Items {
			c, err := repo.Get(testCtx, "memory_candidates", scopeA, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				if c.Status != "approved" || c.ApprovedBy != "human-1" {
					t.Fatal("authorized item lost", c)
				}
			} else {
				if c.Status != "candidate" || c.Revision != 1 || c.ApprovedBy != "" || c.Review != nil {
					t.Fatal("rejected item changed", c)
				}
				if _, err := repo.Get(testCtx, colMemories, scopeA, item.ID); !errors.Is(err, ErrNotFound) {
					t.Fatal("rejected memory published", err)
				}
			}
		}
		return
	}
	if w.Code != 200 || !changed {
		t.Fatal("batch HTTP", w.Code, changed, w.Body.String())
	}
	var out CandidateReviewBatchOutput
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 3 {
		t.Fatal("batch result", w.Body.String())
	}
	for i, item := range out.Items {
		rejected := i == 1 && field != "none"
		if rejected {
			if item.Status != 403 || item.Record != nil || item.Code != "access_denied" {
				t.Fatal("changed actor approved", item)
			}
			c, e := repo.Get(testCtx, "memory_candidates", scopeA, input.Items[i].ID)
			if e != nil || c.Status != "candidate" || c.Revision != 1 || c.ApprovedBy != "" || c.Review != nil {
				t.Fatal("failed candidate changed", c, e)
			}
			if _, e = repo.Get(testCtx, colMemories, scopeA, c.ID); !errors.Is(e, ErrNotFound) {
				t.Fatal("failed memory published", e)
			}
		} else if item.Status != 200 || item.Record == nil || item.Record.ApprovedBy != "human-1" || item.Record.Review == nil || item.Record.Review.By != "human-1" {
			t.Fatal("normal item lost", i, item)
		}
	}
}
func TestBatchBoundActor(t *testing.T) {
	for _, field := range []string{"none", "principal", "role"} {
		t.Run(field, func(t *testing.T) { exerciseBatchBoundActor(t, newFakeRepo(), field) })
	}
}
