package memory

import (
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// #524: the reviewer's reach goes on the memory and the review; no reach keeps the kind default.
func TestReviewReachStoredOnApproval(t *testing.T) {
	f := newFixture(t)
	approve := func(text string, in CandidateReviewInput) (Record, Record) {
		t.Helper()
		c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(f.source())}))
		if c.ReachSuggestion == nil || c.ReachSuggestion.Reach != ReachGeneral {
			t.Fatal("candidate suggestion", c.ReachSuggestion)
		}
		in.Scope, in.ExpectedRevision = scopeA, c.Revision
		if in.Action == "" {
			in.Action = "approve"
		}
		m, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in)
		if e != nil {
			t.Fatal(e)
		}
		return m, f.get(f.human, "memory_candidates", scopeA, c.ID)
	}

	m, c := approve("결론부터 쓴다 default", CandidateReviewInput{})
	if m.Reach != "" || m.ReachOrigin != "" || c.Review.Reach != "" || c.Review.ReachOverridden || memoryReach(m) != ReachGeneral {
		t.Fatal("no reach must keep today's default", m.Reach, c.Review)
	}
	m, c = approve("결론부터 쓴다 same", CandidateReviewInput{Reach: ReachGeneral})
	if m.Reach != ReachGeneral || m.ReachOrigin != reachOriginHuman || c.Review.Reach != ReachGeneral || c.Review.ReachOverridden || m.Review.Reach != ReachGeneral {
		t.Fatal("suggested reach", m.Reach, m.ReachOrigin, c.Review)
	}
	m, c = approve("결론부터 쓴다 personal", CandidateReviewInput{Reach: ReachPersonal})
	if m.Reach != ReachPersonal || !c.Review.ReachOverridden || !m.Review.ReachOverridden {
		t.Fatal("override", m.Reach, c.Review)
	}
	m, c = approve("결론부터 쓴다 project", CandidateReviewInput{Action: "edit_approve", Content: "결론부터 쓴다 edited", Reach: ReachProject, ReachProject: " FelixBaek/Arkism "})
	if m.Reach != ReachProject || m.ReachProject != "felixbaek/arkism" || m.Text != "결론부터 쓴다 edited" || c.Review.Reach != ReachProject || !c.Review.ReachOverridden {
		t.Fatal("edit_approve project", m.Reach, m.ReachProject, c.Review)
	}
}

func TestReviewReachRejectsInvalid(t *testing.T) {
	f := newFixture(t)
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "invalid reach", Sources: refs(f.source())}))
	for _, in := range []CandidateReviewInput{
		{Action: "approve", Reach: "everywhere"},
		{Action: "approve", Reach: ReachProject},
		{Action: "approve", Reach: ReachGeneral, ReachProject: "o/n"},
		{Action: "approve", ReachProject: "o/n"},
		{Action: "reject", Reason: "no", Reach: ReachPersonal},
	} {
		in.Scope, in.ExpectedRevision = scopeA, c.Revision
		if _, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in); !errors.Is(e, ErrInvalid) {
			t.Fatalf("%+v: %v", in, e)
		}
	}
	h := Handler(f.svc)
	post := func(body string) int {
		r := httptest.NewRequest("POST", "/v1/memory_candidates/"+c.ID+"/review", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	scope := `"scope":{"workspace_id":"` + scopeA.WorkspaceID + `","project_id":"` + scopeA.ProjectID + `","user_id":"` + scopeA.UserID + `","agent_id":"` + scopeA.AgentID + `"}`
	rev := strconv.FormatInt(c.Revision, 10)
	if code := post(`{` + scope + `,"expected_revision":` + rev + `,"action":"approve","reach":"bogus"}`); code != 400 {
		t.Fatal("http invalid reach", code)
	}
	if code := post(`{` + scope + `,"expected_revision":` + rev + `,"action":"approve","reach":"personal"}`); code != 200 {
		t.Fatal("http reach", code)
	}
	if m := f.get(f.human, "memory_candidates", scopeA, c.ID); m.Review == nil || m.Review.Reach != ReachPersonal || !m.Review.ReachOverridden {
		t.Fatal("http review", m.Review)
	}
}
