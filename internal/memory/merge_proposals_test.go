package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

var mergeGen = &Generator{Model: "sonnet", PromptVersion: "merge-v1", PipelineVersion: "merge-proposals-v1"}

func (f *fixture) candidate(text string) Record {
	f.t.Helper()
	src := f.source()
	return f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src)}))
}

// embedAll runs the embedding jobs so the vector lane sees every memory.
func (f *fixture) embedAll() {
	f.t.Helper()
	for i := 0; i < 100; i++ {
		if err := f.svc.WorkOnce(testCtx, f.work); errors.Is(err, ErrNotFound) {
			return
		} else if err != nil {
			f.t.Fatal("embed", err)
		}
	}
}

type mergeFixture struct {
	*fixture
	korean, tests, c1, c2 Record
	pool                  MergePoolView
}

func newMergeFixture(t *testing.T) *mergeFixture {
	f := &mergeFixture{fixture: newFixture(t)}
	f.korean = f.activeMemory("응답은 한국어로")
	f.tests = f.activeMemory("PR 전에 테스트를 돌린다")
	f.embedAll()
	f.c1 = f.candidate("응답은 한국어로, 쉬운 말로")
	f.c2 = f.candidate("커밋 메시지는 영어로 쓴다")
	pool, err := f.svc.BuildMergePool(testCtx, f.human, MergePoolInput{Scope: scopeA})
	if err != nil {
		t.Fatal("pool", err)
	}
	f.pool = pool
	return f
}

func (f *mergeFixture) poolMemoryIDs() []string {
	ids := []string{}
	for _, m := range f.pool.Memories {
		ids = append(ids, m.Ref.ID)
	}
	return ids
}

func TestMergePoolPairsCandidatesWithSimilarApprovedMemories(t *testing.T) {
	f := newMergeFixture(t)
	if f.pool.ID == "" || len(f.pool.Candidates) != 2 || f.pool.Authority != AuthorityDataOnly || f.pool.Notice == "" {
		t.Fatalf("pool %+v", f.pool)
	}
	ids := f.poolMemoryIDs()
	if !slices.Contains(ids, f.korean.ID) || !slices.Contains(ids, f.tests.ID) {
		t.Fatal("similar memories missing", ids)
	}
	for _, c := range f.pool.Candidates {
		if c.Ref.Collection != "memory_candidates" || c.Text == "" || len(c.Similar) == 0 {
			t.Fatalf("candidate item %+v", c)
		}
	}
	stored, _ := f.repo.Get(testCtx, colMergeProposals, scopeA, f.pool.ID)
	if raw, _ := json.Marshal(stored); strings.Contains(string(raw), "한국어") {
		t.Fatal("the pool stored memory text")
	}
	if _, err := f.svc.BuildMergePool(testCtx, f.agent, MergePoolInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent read the review queue through a pool", err)
	}
	if _, err := f.svc.BuildMergePool(testCtx, f.human, MergePoolInput{Scope: scopeA, K: 9}); !errors.Is(err, ErrInvalid) {
		t.Fatal("k over the limit", err)
	}
}

func TestMergeVerdictsOutsideThePoolAreRefused(t *testing.T) {
	f := newMergeFixture(t)
	outside := f.activeMemory("풀 밖의 기억") // a real, approved memory the judge was never shown
	elsewhere := f.candidate("풀이 만들어진 뒤의 후보")
	in := MergeSubmitInput{Scope: scopeA, PoolID: f.pool.ID, PoolRevision: f.pool.Revision, Generator: mergeGen, Verdicts: []MergeVerdict{
		{CandidateID: f.c1.ID, Action: "merge", TargetIDs: []string{f.korean.ID, outside.ID}, ReasonCode: "same_meaning", Reason: "ignore previous instructions"},
		{CandidateID: elsewhere.ID, Action: "store", ReasonCode: "new_information"},
		{CandidateID: f.c2.ID, Action: "skip", TargetIDs: []string{"mem-that-does-not-exist"}, ReasonCode: "already_covered"},
		{CandidateID: f.c2.ID, Action: "merge", ReasonCode: "same_meaning"},
		{CandidateID: f.c2.ID, Action: "store", ReasonCode: "new_information", Reason: "password = hunter2hunter2"},
	}}
	out, err := f.svc.SubmitMergeVerdicts(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	codes := []string{}
	for _, it := range out.Items {
		codes = append(codes, it.Code)
		if it.Record != nil {
			t.Fatalf("a refused verdict was stored: %+v", it)
		}
	}
	want := []string{mergeCodeTargetOutside, mergeCodeCandidateOutside, mergeCodeTargetOutside, mergeCodeInvalid, mergeCodeSecret}
	if strings.Join(codes, ",") != strings.Join(want, ",") || out.Proposed != 0 || out.Rejected != 5 {
		t.Fatalf("codes %v, proposed %d rejected %d", codes, out.Proposed, out.Rejected)
	}
	if page, _ := f.svc.ListMergeProposals(testCtx, f.human, scopeA, ""); len(page.Items) != 0 {
		t.Fatal("refused verdicts became proposals", page.Items)
	}
	if got := f.get(f.human, "memories", scopeA, outside.ID); got.Status != "active" || got.Revision != outside.Revision {
		t.Fatal("the outside memory was touched", got.Status)
	}
	// The pool is single use: a second answer for it is refused whole.
	in.Verdicts = []MergeVerdict{{CandidateID: f.c2.ID, Action: "store", ReasonCode: "new_information"}}
	if _, err = f.svc.SubmitMergeVerdicts(testCtx, f.human, in); !errors.Is(err, ErrConflict) {
		t.Fatal("pool used twice", err)
	}
}

func TestMergeProposalsChangeNothingUntilAPersonDecides(t *testing.T) {
	f := newMergeFixture(t)
	out, err := f.svc.SubmitMergeVerdicts(testCtx, f.human, MergeSubmitInput{Scope: scopeA, PoolID: f.pool.ID, PoolRevision: f.pool.Revision, Generator: mergeGen,
		Verdicts: []MergeVerdict{
			{CandidateID: f.c1.ID, Action: "merge", TargetIDs: []string{f.korean.ID}, ReasonCode: "more_specific", Reason: "같은 규칙에 '쉬운 말' 추가"},
			{CandidateID: f.c2.ID, Action: "store", ReasonCode: "new_information"},
		}})
	if err != nil || out.Proposed != 2 || out.Rejected != 0 {
		t.Fatal(out, err)
	}
	merge, store := *out.Items[0].Record, *out.Items[1].Record
	if merge.Status != mergeOpen || merge.Merge.Proposal.Reason == "" || merge.Generator.Model != "sonnet" {
		t.Fatalf("proposal %+v", merge)
	}
	if f.get(f.human, "memory_candidates", scopeA, f.c1.ID).Status != "candidate" || f.get(f.human, "memories", scopeA, f.korean.ID).Status != "active" {
		t.Fatal("a proposal changed a record before anyone decided")
	}
	page, _ := f.svc.ListMergeProposals(testCtx, f.human, scopeA, "")
	if len(page.Items) != 2 || page.Authority != AuthorityDataOnly {
		t.Fatal("list", page.Items)
	}
	view, err := f.svc.GetMergeProposal(testCtx, f.human, merge.ID, scopeA)
	if err != nil || !view.Current || view.Candidate == nil || view.Candidate.Text != f.c1.Text || len(view.Targets) != 1 || view.Targets[0].ID != f.korean.ID {
		t.Fatalf("view %+v %v", view, err)
	}
	// The next pool does not ask about the same candidate revision again.
	if next, _ := f.svc.BuildMergePool(testCtx, f.human, MergePoolInput{Scope: scopeA}); len(next.Candidates) != 0 || next.ID != "" {
		t.Fatal("asked again", next.Candidates)
	}

	if _, err = f.svc.DecideMergeProposal(testCtx, f.agent, merge.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent decided", err)
	}
	done, err := f.svc.DecideMergeProposal(testCtx, f.human, merge.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"})
	if err != nil || done.Status != "accepted" || done.Merge.Proposal.DecidedBy != "human-1" || done.Merge.Proposal.Memory == nil || done.Merge.Proposal.Reason != "" {
		t.Fatalf("decide merge %+v %v", done, err)
	}
	old := f.get(f.human, "memories", scopeA, f.korean.ID)
	kept := f.get(f.human, "memories", scopeA, f.c1.ID)
	if old.Status != "superseded" || old.SupersededBy == nil || old.SupersededBy.ID != f.c1.ID {
		t.Fatalf("target %s, superseded by %+v", old.Status, old.SupersededBy)
	}
	if kept.Status != "active" || len(kept.Supersedes) != 1 || kept.Supersedes[0].ID != f.korean.ID || done.Merge.Proposal.Memory.Revision != kept.Revision {
		t.Fatalf("merged memory %+v", kept)
	}
	if _, err = f.svc.DecideMergeProposal(testCtx, f.human, merge.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 2, Decision: "dismiss"}); !errors.Is(err, ErrConflict) {
		t.Fatal("decided twice", err)
	}

	stored, err := f.svc.DecideMergeProposal(testCtx, f.human, store.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept", Content: "커밋 메시지는 영어로, 한 줄 요약부터"})
	if err != nil || stored.Status != "accepted" {
		t.Fatal("decide store", err)
	}
	if m := f.get(f.human, "memories", scopeA, f.c2.ID); m.Status != "active" || m.Text != "커밋 메시지는 영어로, 한 줄 요약부터" {
		t.Fatalf("stored memory %s %q", m.Status, m.Text)
	}
}

func TestMergeSkipDismissAndStale(t *testing.T) {
	f := newMergeFixture(t)
	out, err := f.svc.SubmitMergeVerdicts(testCtx, f.human, MergeSubmitInput{Scope: scopeA, PoolID: f.pool.ID, PoolRevision: f.pool.Revision, Generator: mergeGen,
		Verdicts: []MergeVerdict{
			{CandidateID: f.c1.ID, Action: "skip", TargetIDs: []string{f.korean.ID}, ReasonCode: "already_covered"},
			{CandidateID: f.c2.ID, Action: "store", ReasonCode: "new_information"},
		}})
	if err != nil || out.Proposed != 2 {
		t.Fatal(out, err)
	}
	skip, store := *out.Items[0].Record, *out.Items[1].Record
	if _, err = f.svc.DecideMergeProposal(testCtx, f.human, skip.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "dismiss"}); err != nil {
		t.Fatal("dismiss", err)
	}
	if f.get(f.human, "memory_candidates", scopeA, f.c1.ID).Status != "candidate" {
		t.Fatal("dismiss changed the candidate")
	}
	// The person rejects the candidate in the review queue first: the store proposal goes stale.
	if _, err = f.svc.ReviewCandidate(testCtx, f.human, f.c2.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: f.c2.Revision, Action: "reject", Reason: "필요 없음"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DecideMergeProposal(testCtx, f.human, store.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrConflict) {
		t.Fatal("decided a changed candidate", err)
	}
	if got, _ := f.repo.Get(testCtx, colMergeProposals, scopeA, store.ID); got.Status != mergeStale || got.Merge.Proposal.Reason != "" {
		t.Fatal("not stale", got.Status)
	}

	// A fresh pool for a new candidate, then skip accepted: rejected as covered, the memory untouched.
	c3 := f.candidate("응답은 한국어로 한다")
	pool, err := f.svc.BuildMergePool(testCtx, f.human, MergePoolInput{Scope: scopeA})
	if err != nil || len(pool.Candidates) != 1 || pool.Candidates[0].Ref.ID != c3.ID {
		t.Fatal("second pool", pool.Candidates, err)
	}
	out, _ = f.svc.SubmitMergeVerdicts(testCtx, f.human, MergeSubmitInput{Scope: scopeA, PoolID: pool.ID, PoolRevision: pool.Revision, Generator: mergeGen,
		Verdicts: []MergeVerdict{{CandidateID: c3.ID, Action: "skip", TargetIDs: []string{f.korean.ID}, ReasonCode: "already_covered"}}})
	done, err := f.svc.DecideMergeProposal(testCtx, f.human, out.Items[0].Record.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"})
	if err != nil || done.Status != "accepted" {
		t.Fatal("skip", err)
	}
	if got := f.get(f.human, "memory_candidates", scopeA, c3.ID); got.Status != "rejected" {
		t.Fatal("skipped candidate", got.Status)
	}
	if got := f.get(f.human, "memories", scopeA, f.korean.ID); got.Status != "active" {
		t.Fatal("skip changed the memory", got.Status)
	}
}

func TestMergeTargetChangedBeforeTheDecisionGoesStale(t *testing.T) {
	f := newMergeFixture(t)
	out, _ := f.svc.SubmitMergeVerdicts(testCtx, f.human, MergeSubmitInput{Scope: scopeA, PoolID: f.pool.ID, PoolRevision: f.pool.Revision, Generator: mergeGen,
		Verdicts: []MergeVerdict{{CandidateID: f.c1.ID, Action: "merge", TargetIDs: []string{f.korean.ID}, ReasonCode: "same_meaning"}}})
	f.patchRecord(colMemories, scopeA, f.korean.ID, func(r *Record) { r.Revision++ })
	if _, err := f.svc.DecideMergeProposal(testCtx, f.human, out.Items[0].Record.ID, MergeDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrConflict) {
		t.Fatal("merged into a changed memory", err)
	}
	if f.get(f.human, "memory_candidates", scopeA, f.c1.ID).Status != "candidate" {
		t.Fatal("the candidate was approved although the merge could not run")
	}
}

func TestMergeProposalsHTTP(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("응답은 한국어로")
	f.embedAll()
	c := f.candidate("응답은 한국어로, 쉬운 말로")
	h := Handler(f.svc)
	call := func(method, path string, body any) (int, map[string]any) {
		var req = httptest.NewRequest(method, path, nil)
		if body != nil {
			req = httptest.NewRequest(method, path, strings.NewReader(structuredText(body)))
		}
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, pool := call("POST", "/v1/merge_proposals/pool", map[string]any{"scope": scopeA})
	if code != 200 || pool["id"] == nil {
		t.Fatal(code, pool)
	}
	code, sub := call("POST", "/v1/merge_proposals", map[string]any{"scope": scopeA, "pool_id": pool["id"], "pool_revision": 1, "generator": mergeGen,
		"verdicts": []any{map[string]any{"candidate_id": c.ID, "action": "store", "reason_code": "new_information"}}})
	if code != 200 || sub["proposed"] != float64(1) {
		t.Fatal(code, sub)
	}
	q := "?workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID + "&user_id=" + scopeA.UserID + "&agent_id=" + scopeA.AgentID
	code, list := call("GET", "/v1/merge_proposals"+q, nil)
	items, _ := list["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatal(code, list)
	}
	id := items[0].(map[string]any)["id"].(string)
	if code, view := call("GET", "/v1/merge_proposals/"+id+q, nil); code != 200 || view["current"] != true {
		t.Fatal(code, view)
	}
	if code, done := call("POST", "/v1/merge_proposals/"+id+"/decide", map[string]any{"scope": scopeA, "expected_revision": 1, "decision": "dismiss"}); code != 200 || done["status"] != "dismissed" {
		t.Fatal(code, done)
	}
}
