package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func exerciseTrustLevels(t *testing.T, repo Repository) {
	f := newFixture(t)
	f.svc.Repo = repo
	external := f.source()
	if external.TrustLevel != "external_untrusted" {
		t.Fatal("unsafe source default", external.TrustLevel)
	}
	in := sourceInput(CreateInput{Scope: scopeA, Text: "human authored evidence", TrustLevel: "user_authored"})
	for _, role := range []string{"agent", "pipeline", "service"} {
		f.auth.actors[role] = actor(role, role, true, allVerbs...)
		if _, e := f.svc.Create(testCtx, role, "sources", in); !errors.Is(e, ErrDenied) {
			t.Fatal("nonhuman authorship", role, e)
		}
	}
	authored := f.create(f.human, "sources", in)
	changed := authored
	changed.TrustLevel = "external_untrusted"
	if e := repo.Replace(testCtx, "sources", changed, authored.Revision); !errors.Is(e, ErrConflict) {
		t.Fatal("source classification mutable", e)
	}
	in.TrustLevel = "external_untrusted"
	if _, e := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(e, ErrConflict) {
		t.Fatal("dedup changed provenance", e)
	}
	gen := &Generator{Model: "test-model", PromptVersion: "p1", PipelineVersion: "v1"}
	in.Text = "generated source"
	in.TrustLevel = "user_authored"
	in.Generator = gen
	if _, e := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(e, ErrDenied) {
		t.Fatal("model authorship", e)
	}
	for _, tc := range []struct {
		name, token, want string
		sources           []SourceRef
		generator         *Generator
	}{
		{"external", f.agent, "external_untrusted", refs(external), gen},
		{"mixed", f.human, "external_untrusted", append(refs(authored), refs(external)...), nil},
		{"human", f.human, "user_authored", refs(authored), nil},
		{"agent", f.agent, "agent_proposed", refs(authored), gen},
		{"model", f.human, "model_inferred", refs(authored), gen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := f.create(tc.token, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "proposal " + tc.name, Sources: tc.sources, Generator: tc.generator}))
			stored, e := repo.Get(testCtx, "memory_candidates", scopeA, c.ID)
			if e != nil || stored.TrustLevel != tc.want || stored.Trust != .5 {
				t.Fatal("candidate classification", stored, e)
			}
			for _, action := range []string{"approve", "edit_approve", "transition"} {
				proposal := c
				if action != "approve" {
					proposal = f.create(tc.token, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "proposal " + action + tc.name, Sources: tc.sources, Generator: tc.generator}))
				}
				var m Record
				if action == "transition" {
					_, e = f.svc.Transition(testCtx, f.human, "memory_candidates", proposal.ID, ChangeInput{Scope: scopeA, ExpectedRevision: proposal.Revision, Status: "approved"})
					if e == nil {
						m, e = repo.Get(testCtx, "memories", scopeA, proposal.ID)
					}
				} else {
					review := CandidateReviewInput{Scope: scopeA, ExpectedRevision: proposal.Revision, Action: action}
					if action == "edit_approve" {
						review.Content = "edited proposal"
					}
					m, e = f.svc.ReviewCandidate(testCtx, f.human, proposal.ID, review)
				}
				wantScore := 1.0
				if action == "transition" {
					wantScore = .5
				}
				if e != nil || m.TrustLevel != "user_verified" || m.Trust != wantScore {
					t.Fatal("approval classification/score", m, e)
				}
				original, e := repo.Get(testCtx, "memory_candidates", scopeA, proposal.ID)
				if e != nil || original.TrustLevel != tc.want {
					t.Fatal("original proposal classification changed", e)
				}
				hit := HitFromRecord("memories", m)
				if hit.TrustLevel != "user_verified" || hit.Authority != AuthorityDataOnly {
					t.Fatal("context label/authority", hit)
				}
			}
		})
	}
	// Candidates cannot self-assign a review level through the HTTP API.
	request := candidateInput(CreateInput{Scope: scopeA, Text: "self approval", Sources: refs(external), TrustLevel: "user_verified"})
	req := httptest.NewRequest("POST", "/v1/memory_candidates", strings.NewReader(structuredText(request)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("self classification accepted", w.Code)
	}
	var wire map[string]any
	if e := json.Unmarshal([]byte(structuredText(Record{})), &wire); e != nil {
		t.Fatal(e)
	}
	if _, ok := wire["trust_level"]; ok {
		t.Fatal("legacy level invented")
	}
	if candidateTrustLevel(Record{CreatedByType: "human"}, []Record{{}}) != "external_untrusted" {
		t.Fatal("legacy evidence promoted")
	}
}
func TestTrustLevels(t *testing.T) { exerciseTrustLevels(t, newFakeRepo()) }
