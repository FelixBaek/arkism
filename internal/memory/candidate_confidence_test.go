package memory

import (
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
)

func exerciseCandidateConfidence(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	for _, value := range []string{"0", "0.82", "1", "null", ""} {
		t.Run(value, func(t *testing.T) {
			in := candidateInput(CreateInput{Scope: scopeA, Text: "proposed statement", Generator: &Generator{Model: "test-model", PromptVersion: "p1", PipelineVersion: "v1"}, Sources: refs(src)})
			var payload map[string]any
			_ = json.Unmarshal([]byte(structuredText(in)), &payload)
			if value != "" {
				var n any
				_ = json.Unmarshal([]byte(value), &n)
				payload["confidence"] = n
			}
			req := httptest.NewRequest("POST", "/v1/memory_candidates", strings.NewReader(structuredText(payload)))
			req.Header.Set("Authorization", "Bearer "+f.agent)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			var r Record
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &r) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			var wire map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &wire)
			want, has := payload["confidence"]
			has = has && want != nil
			if got, ok := wire["confidence"]; ok != has || has && got != want {
				t.Fatal("confidence lost", value, wire)
			}
			if r.Status != "candidate" || r.Trust != 0.5 || r.ApprovedBy != "" {
				t.Fatal("self rating escalated trust", r)
			}
			stored, e := repo.Get(testCtx, "memory_candidates", scopeA, r.ID)
			if e != nil {
				t.Fatal(e)
			}
			var again map[string]any
			_ = json.Unmarshal([]byte(structuredText(stored)), &again)
			if got, ok := again["confidence"]; ok != has || has && got != want {
				t.Fatal("storage lost confidence", again)
			}
		})
	}
}
func TestCandidateConfidence(t *testing.T) { exerciseCandidateConfidence(t, newFakeRepo()) }

func TestCandidateConfidenceRejectsBeforePolicy(t *testing.T) {
	for _, value := range []float64{-0.01, 1.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		f := newFixture(t)
		called := false
		f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(Authorization, *Decision) error { called = true; return nil }}
		_, e := f.svc.Create(testCtx, f.agent, "memory_candidates", CreateInput{Scope: scopeA, Text: "proposal", Confidence: &value})
		if !errors.Is(e, ErrInvalid) || called || len(f.repo.rows("memory_candidates")) != 0 {
			t.Fatal(value, e, called)
		}
	}
	f := newFixture(t)
	n := 0.5
	_, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "source", Confidence: &n}))
	if !errors.Is(e, ErrInvalid) {
		t.Fatal("non-candidate confidence accepted", e)
	}
}

func TestCandidateConfidenceApproval(t *testing.T) {
	for _, action := range []string{"approve", "edit_approve"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			n := 0.82
			r := f.create(f.agent, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "proposal", Generator: &Generator{Model: "test-model", PromptVersion: "p1", PipelineVersion: "v1"}, Sources: refs(src), Confidence: &n}))
			n = 0
			if r.Confidence == nil || *r.Confidence != 0.82 {
				t.Fatal("caller alias", r.Confidence)
			}
			in := CandidateReviewInput{Scope: scopeA, ExpectedRevision: r.Revision, Action: action, Content: "human edited"}
			if action == "approve" {
				in.Content = ""
			}
			if _, e := f.svc.ReviewCandidate(testCtx, f.agent, r.ID, in); !errors.Is(e, ErrDenied) {
				t.Fatal("agent approved", e)
			}
			approved, e := f.svc.ReviewCandidate(testCtx, f.human, r.ID, in)
			if e != nil {
				t.Fatal(e)
			}
			m, e := f.repo.Get(testCtx, colMemories, scopeA, approved.ID)
			if e != nil || m.Confidence == nil || *m.Confidence != 0.82 || m.Trust != 1 {
				t.Fatal("approval lost original self rating", e, m)
			}
			h := HitFromRecord(colMemories, m)
			if strings.Contains(structuredText(h), "confidence") {
				t.Fatal("self rating in context")
			}
		})
	}
}
