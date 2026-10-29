package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func conflictFixture(t *testing.T) (*fixture, ConflictInput) {
	f := newFixture(t)
	a := f.activeMemory("first rule")
	b := f.activeMemory("second rule")
	return f, ConflictInput{Scope: scopeA, Memories: []Ref{{Collection: "memories", ID: a.ID, Revision: a.Revision}, {Collection: "memories", ID: b.ID, Revision: b.Revision}}, Action: "mark", Reason: "different rules"}
}
func TestConflictMarkAndResolve(t *testing.T) {
	for _, tc := range []struct {
		hold, resolution string
		active           int
	}{{"both", "keep_first", 1}, {"both", "keep_second", 1}, {"both", "keep_both", 2}, {"both", "reject_both", 0}, {"first", "keep_both", 2}, {"second", "keep_both", 2}} {
		t.Run(tc.hold+"/"+tc.resolution, func(t *testing.T) {
			f, in := conflictFixture(t)
			in.Hold = tc.hold
			marked, e := f.svc.Conflict(testCtx, f.human, in)
			if e != nil {
				t.Fatal(e)
			}
			visible := f.search(f.human, SearchInput{Scope: scopeA, Query: "rule"})
			expected := 0
			if tc.hold != "both" {
				expected = 1
			}
			if len(visible.Items) != expected {
				t.Fatal("conflicted memory leaked", visible.Items)
			}
			for i, r := range marked.Memories {
				if r.ConflictID != marked.Relation.ID || r.Revision != in.Memories[i].Revision+1 {
					t.Fatal("unfenced conflict")
				}
			}
			resolve := ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "human resolution", ConflictID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Resolution: tc.resolution}
			out, e := f.svc.Conflict(testCtx, f.human, resolve)
			if e != nil {
				t.Fatal(e)
			}
			if out.Relation.Status != "resolved" || out.Relation.Text != in.Reason || out.Relation.Conflict.ResolutionReason != resolve.Reason || out.Relation.Conflict.ResolvedBy == "" {
				t.Fatal("resolution history missing")
			}
			visible = f.search(f.human, SearchInput{Scope: scopeA, Query: "rule"})
			if len(visible.Items) != tc.active {
				t.Fatal("resolved visibility", len(visible.Items))
			}
			for _, r := range out.Memories {
				if r.ConflictID != "" {
					t.Fatal("unreleased conflict")
				}
			}
			if _, e = f.svc.Conflict(testCtx, f.human, resolve); !errors.Is(e, ErrConflict) {
				t.Fatal("double resolution", e)
			}
			// #466: counted by the chosen resolution, not by the app's constant reason text; marking is not a decision.
			if d := requireDecision(t, f.repo, "conflict_resolved", tc.resolution); d.Audit.Reason != resolve.Reason {
				t.Fatalf("conflict reason = %q", d.Audit.Reason)
			}
		})
	}
}
func TestConflictRollbackPermissionsAndBypass(t *testing.T) {
	f, in := conflictFixture(t)
	if _, e := f.svc.Conflict(testCtx, f.agent, in); !errors.Is(e, ErrDenied) {
		t.Fatal("agent", e)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: in.Memories[1].ID}
	if _, e := f.svc.Conflict(testCtx, f.human, in); !errors.Is(e, ErrDenied) {
		t.Fatal("second permission", e)
	}
	f.svc.Auth = f.auth
	f.repo.failAudit = true
	if out, e := f.svc.Conflict(testCtx, f.human, in); e == nil || out.Relation.ID != "" {
		t.Fatal("mark audit fail open")
	}
	f.repo.failAudit = false
	if len(f.repo.rows("memory_relations")) != 0 {
		t.Fatal("relation escaped rollback")
	}
	marked, e := f.svc.Conflict(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	first, second := marked.Memories[0], marked.Memories[1]
	if _, e = f.svc.Transition(testCtx, f.human, "memories", first.ID, ChangeInput{Scope: scopeA, ExpectedRevision: first.Revision, Status: "rejected"}); !errors.Is(e, ErrConflict) {
		t.Fatal("transition bypass", e)
	}
	if _, e = f.svc.Supersede(testCtx, f.human, first.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: first.Revision, Reason: "bypass", ReplacementMemoryID: second.ID, ReplacementExpectedRevision: second.Revision}); !errors.Is(e, ErrConflict) {
		t.Fatal("supersede bypass", e)
	}
	resolve := ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "resolution", ConflictID: marked.Relation.ID, ExpectedRevision: 1, Resolution: "keep_first"}
	f.repo.failAudit = true
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); e == nil {
		t.Fatal("resolve audit fail open")
	}
	f.repo.failAudit = false
	r, _ := f.repo.Get(testCtx, "memories", scopeA, first.ID)
	if r.Status != "conflicted" || r.ConflictID != marked.Relation.ID {
		t.Fatal("resolution escaped rollback")
	}
	// Broken evidence cannot be reactivated, but both records can be rejected.
	src := f.repo.store["sources"][recKey(scopeA, first.Sources[0].SourceID)]
	src.Status = "deleted"
	f.repo.store["sources"][recKey(scopeA, src.ID)] = src
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); e == nil {
		t.Fatal("broken evidence reactivated")
	}
	resolve.Resolution = "reject_both"
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); e != nil {
		t.Fatal("broken evidence could not be rejected", e)
	}
}
func TestConflictHTTPAndSecretBoundary(t *testing.T) {
	f, in := conflictFixture(t)
	secret := in
	secret.Reason = "password=synthetic-secret"
	if _, e := f.svc.Conflict(testCtx, f.human, secret); !errors.Is(e, ErrSecretDetected) {
		t.Fatal("secret accepted", e)
	}
	payload, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", "/v1/memories/conflicts", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("conflict HTTP", w.Code)
	}
}

func TestConflictDeletedMemberCanBeResolved(t *testing.T) {
	f, in := conflictFixture(t)
	marked, e := f.svc.Conflict(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.svc.Delete(testCtx, f.human, "memory_relations", marked.Relation.ID, ChangeInput{Scope: scopeA, ExpectedRevision: marked.Relation.Revision}); !errors.Is(e, ErrConflict) {
		t.Fatal("open relation deleted", e)
	}
	a := marked.Memories[0]
	if e = f.svc.Delete(testCtx, f.human, "memories", a.ID, ChangeInput{Scope: scopeA, ExpectedRevision: a.Revision}); e != nil {
		t.Fatal("memory deletion blocked", e)
	}
	resolve := ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "first memory deleted", ConflictID: marked.Relation.ID, ExpectedRevision: 1, Resolution: "keep_first"}
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); !errors.Is(e, ErrConflict) {
		t.Fatal("deleted member resurrected", e)
	}
	resolve.Resolution = "keep_second"
	out, e := f.svc.Conflict(testCtx, f.human, resolve)
	if e != nil || len(out.Memories) != 1 || out.Memories[0].Status != "active" || out.Memories[0].ConflictID != "" || len(out.Relation.Conflict.Missing) != 1 {
		t.Fatal("survivor stranded", out, e)
	}
	if _, e = f.repo.Get(testCtx, "memories", scopeA, a.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("deleted memory recreated")
	}
}

func TestCandidateConflictDoesNotApproveOrEmbed(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	in := ConflictInput{Scope: scopeA, Action: "mark", Reason: "candidate disagreement"}
	for _, text := range []string{"first candidate rule", "second candidate rule"} {
		r := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src)}))
		in.Memories = append(in.Memories, Ref{Collection: "memory_candidates", ID: r.ID, Revision: r.Revision})
	}
	marked, e := f.svc.Conflict(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	first := marked.Memories[0]
	if _, e = f.svc.ReviewCandidate(testCtx, f.human, first.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: first.Revision, Action: "approve"}); !errors.Is(e, ErrConflict) {
		t.Fatal("pending conflict approved", e)
	}
	out, e := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "first proposal retained", ConflictID: marked.Relation.ID, ExpectedRevision: 1, Resolution: "keep_first"})
	if e != nil {
		t.Fatal(e)
	}
	if out.Memories[0].Status != "candidate" || out.Memories[0].ApprovedBy != "" || out.Memories[1].Status != "rejected" || len(f.repo.rows("memories")) != 0 || len(f.embeddingJobs()) != 0 {
		t.Fatal("conflict resolution bypassed candidate approval")
	}
	if len(f.search(f.human, SearchInput{Scope: scopeA, Query: "rule"}).Items) != 0 {
		t.Fatal("candidate leaked into answer")
	}
	selected := out.Memories[0]
	approved, e := f.svc.ReviewCandidate(testCtx, f.human, selected.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: selected.Revision, Action: "approve"})
	if e != nil || approved.Status != "active" {
		t.Fatal("explicit approval failed", e)
	}
}

type candidateConflictAuth struct {
	Authorizer
	denyCandidate bool
	actions       []string
}

func (a *candidateConflictAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	a.actions = append(a.actions, in.Action)
	if a.denyCandidate && in.Action == "transition:memory_candidates" {
		return Decision{}, nil
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
func TestMixedConflictCollectionsPermissionsAndDeletion(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("established rule")
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "proposed rule", Sources: refs(src)}))
	in := ConflictInput{Scope: scopeA, Memories: []Ref{{Collection: "memories", ID: m.ID, Revision: m.Revision}, {Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}}, Action: "mark", Hold: "second", Reason: "check proposal"}
	auth := &candidateConflictAuth{Authorizer: f.auth, denyCandidate: true}
	f.svc.Auth = auth
	if _, e := f.svc.Conflict(testCtx, f.human, in); !errors.Is(e, ErrDenied) {
		t.Fatal("candidate permission bypass", e)
	}
	if len(auth.actions) != 2 || auth.actions[0] != "transition:memories" || auth.actions[1] != "transition:memory_candidates" {
		t.Fatal("wrong collection policy", auth.actions)
	}
	if len(f.repo.rows("memory_relations")) != 0 {
		t.Fatal("denial wrote conflict")
	}
	auth.denyCandidate = false
	marked, e := f.svc.Conflict(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	if marked.Relation.Relations[1].Collection != "memory_candidates" || marked.Memories[0].Status != "active" || marked.Memories[1].Status != "conflicted" {
		t.Fatal("mixed collection state")
	}
	resolve := ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "both possible", ConflictID: marked.Relation.ID, ExpectedRevision: 1, Resolution: "keep_both"}
	swapped := resolve
	swapped.Memories = []Ref{resolve.Memories[1], resolve.Memories[0]}
	if _, e = f.svc.Conflict(testCtx, f.human, swapped); !errors.Is(e, ErrConflict) {
		t.Fatal("endpoint order lost", e)
	}
	result, e := f.svc.Conflict(testCtx, f.human, resolve)
	if e != nil {
		t.Fatal(e)
	}
	if result.Memories[0].Status != "active" || result.Memories[1].Status != "candidate" || len(f.repo.rows("memories")) != 1 {
		t.Fatal("candidate activated implicitly")
	}
	for _, job := range f.embeddingJobs() {
		if job.Target.Collection != "memories" || job.Target.ID == c.ID {
			t.Fatal("candidate embedded")
		}
	}
	in.Memories = result.Relation.Relations
	marked, e = f.svc.Conflict(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.svc.Delete(testCtx, f.human, "memory_candidates", c.ID, ChangeInput{Scope: scopeA, ExpectedRevision: marked.Memories[1].Revision}); e != nil {
		t.Fatal(e)
	}
	resolve.Memories = marked.Relation.Relations
	resolve.ConflictID = marked.Relation.ID
	resolve.Resolution = "keep_first"
	result, e = f.svc.Conflict(testCtx, f.human, resolve)
	if e != nil || len(result.Memories) != 1 || len(result.Relation.Conflict.Missing) != 1 || result.Relation.Conflict.Missing[0].Collection != "memory_candidates" {
		t.Fatal("missing candidate repair", e)
	}
}

func TestConflictResolveBySupersession(t *testing.T) {
	for _, tc := range []struct {
		resolution string
		winner     int
	}{{"first_supersedes_second", 0}, {"second_supersedes_first", 1}} {
		t.Run(tc.resolution, func(t *testing.T) {
			f, in := conflictFixture(t)
			marked, e := f.svc.Conflict(testCtx, f.human, in)
			if e != nil {
				t.Fatal(e)
			}
			resolve := ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "new decision replaces old", ConflictID: marked.Relation.ID, ExpectedRevision: 1, Resolution: tc.resolution}
			out, e := f.svc.Conflict(testCtx, f.human, resolve)
			if e != nil {
				t.Fatal(e)
			}
			next, old := out.Memories[tc.winner], out.Memories[1-tc.winner]
			if next.Status != "active" || old.Status != "superseded" || next.ConflictID != "" || old.ConflictID != "" || old.SupersededBy == nil || old.SupersededBy.ID != next.ID || old.SupersededBy.Revision != next.Revision || len(next.Supersedes) != 1 || next.Supersedes[0].Revision != old.Revision {
				t.Fatal("supersession link/state")
			}
			if out.Replacement == nil || out.Replacement.Kind != "supersedes" || out.Replacement.Text != resolve.Reason || out.Relation.Conflict.Replacement == nil || out.Relation.Conflict.Replacement.ID != out.Replacement.ID {
				t.Fatal("replacement history missing")
			}
			found := f.search(f.human, SearchInput{Scope: scopeA, Query: "rule"})
			if len(found.Items) != 1 || found.Items[0].ID != next.ID {
				t.Fatal("superseded member searchable")
			}
		})
	}
}

func TestConflictSupersessionRollbackAndGuards(t *testing.T) {
	f, in := conflictFixture(t)
	marked, e := f.svc.Conflict(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	resolve := ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "replace obsolete rule", ConflictID: marked.Relation.ID, ExpectedRevision: 1, Resolution: "first_supersedes_second"}
	jobs := len(f.embeddingJobs())
	f.repo.failAudit = true
	if out, e := f.svc.Conflict(testCtx, f.human, resolve); e == nil || out.Replacement != nil || out.Relation.ID != "" {
		t.Fatal("failed audit returned supersession")
	}
	f.repo.failAudit = false
	if len(f.repo.rows("memory_relations")) != 1 || len(f.embeddingJobs()) != jobs {
		t.Fatal("replacement relation/job escaped rollback")
	}
	for _, ref := range in.Memories {
		r, _ := f.repo.Get(testCtx, "memories", scopeA, ref.ID)
		if r.Status != "conflicted" || r.SupersededBy != nil || len(r.Supersedes) != 0 {
			t.Fatal("supersession escaped rollback")
		}
	}
	candidate := resolve
	candidate.Memories = append([]Ref(nil), resolve.Memories...)
	candidate.Memories[1].Collection = "memory_candidates"
	if _, e = f.svc.Conflict(testCtx, f.human, candidate); !errors.Is(e, ErrInvalid) {
		t.Fatal("candidate supersession accepted", e)
	}
	winner := marked.Memories[0]
	winner.Supersedes = make([]Ref, 64)
	f.repo.store["memories"][recKey(scopeA, winner.ID)] = winner
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); !errors.Is(e, ErrInvalid) {
		t.Fatal("supersession cap ignored", e)
	}
	winner.Supersedes = nil
	f.repo.store["memories"][recKey(scopeA, winner.ID)] = winner
	loser := marked.Memories[1]
	if e = f.svc.Delete(testCtx, f.human, "memories", loser.ID, ChangeInput{Scope: scopeA, ExpectedRevision: loser.Revision}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); !errors.Is(e, ErrConflict) {
		t.Fatal("missing member superseded", e)
	}
	resolve.Resolution = "keep_first"
	if _, e = f.svc.Conflict(testCtx, f.human, resolve); e != nil {
		t.Fatal("ordinary missing-member resolution regressed", e)
	}
}

// The desktop app's "새 내용으로 바꾸기" on a conflicted candidate: resolve keep_both,
// approve the candidate, then supersede the old memory so it stays as history.
func TestConflictedCandidateCanReplaceTheOldMemory(t *testing.T) {
	f := newFixture(t)
	old := f.activeMemory("deploy on Fridays")
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "never deploy on Fridays", Sources: refs(f.source())}))
	marked, e := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "mark", Reason: "user correction",
		Memories: []Ref{{Collection: "memories", ID: old.ID, Revision: old.Revision}, {Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: marked.Memories[1].Revision, Action: "approve"}); !errors.Is(e, ErrConflict) {
		t.Fatal("a conflicted candidate was reviewed directly", e)
	}
	kept, e := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "resolve", Reason: "app", Memories: marked.Relation.Relations,
		ConflictID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Resolution: "keep_both"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: kept.Memories[1].Revision, Action: "approve"}); e != nil {
		t.Fatal("approve after keep_both", e)
	}
	oldNow, _ := f.svc.Get(testCtx, f.human, "memories", scopeA, old.ID)
	fresh, e := f.svc.Get(testCtx, f.human, "memories", scopeA, c.ID)
	if e != nil {
		t.Fatal("approved candidate is not a memory", e)
	}
	again, e := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "mark", Reason: "app",
		Memories: []Ref{{Collection: "memories", ID: old.ID, Revision: oldNow.Revision}, {Collection: "memories", ID: fresh.ID, Revision: fresh.Revision}}})
	if e != nil {
		t.Fatal(e)
	}
	done, e := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "resolve", Reason: "app", Memories: again.Relation.Relations,
		ConflictID: again.Relation.ID, ExpectedRevision: again.Relation.Revision, Resolution: "second_supersedes_first"})
	if e != nil || done.Memories[0].Status != "superseded" || done.Memories[1].Status != "active" {
		t.Fatal("old memory not kept as superseded history", done.Memories, e)
	}
}
