package memory

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// embedText stores the memory's vector for its own text, as the worker would.
func (f *fixture) embedText(m Record) {
	f.t.Helper()
	out, err := f.emb.Embed(testCtx, []string{m.Text}, "document")
	if err != nil {
		f.t.Fatal(err)
	}
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	key := recKey(m.Scope, m.ID)
	r := f.repo.store["memories"][key]
	out[0].ContentHash = ContentHash(recordContent(r))
	r.Embedding = &out[0]
	f.repo.store["memories"][key] = r
}

func (f *fixture) openProposal(item CaptureItem) (Record, error) {
	f.t.Helper()
	c := f.get(f.human, "memory_candidates", scopeA, item.CandidateID)
	return f.repo.Get(testCtx, colMergeProposals, scopeA, mergeProposalID(scopeA, c.ID, c.Revision))
}

func TestCaptureProposesAMergeForARewordedApprovedMemory(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	m := f.activeMemory("응답은 항상 쉬운 한국어로 짧게 쓴다")
	f.embedText(m)

	// Six of seven words shared: cosine 0.926, over the 0.92 default.
	got := f.capture(f.human, captureInput(scopeA, "evt-similar", explicit("응답은 항상 쉬운 한국어로 짧게 쓴다 알겠지")))
	if got[0].Status != "candidate" || got[0].Reason != "merge_proposed" || got[0].CandidateID == "" {
		t.Fatal("similar claim did not get a merge proposal", got)
	}
	p, err := f.openProposal(got[0])
	if err != nil || p.Status != mergeOpen || p.Merge.Proposal.Action != "merge" || len(p.Merge.Proposal.Targets) != 1 || p.Merge.Proposal.Targets[0].ID != m.ID {
		t.Fatalf("proposal %+v %v", p, err)
	}
	// Nothing is merged or deleted until a person decides.
	if f.get(f.human, "memory_candidates", scopeA, got[0].CandidateID).Status != "candidate" || f.get(f.human, "memories", scopeA, m.ID).Status != "active" {
		t.Fatal("the gate changed a record on its own")
	}
	if _, err := f.svc.DecideMergeProposal(testCtx, f.human, p.ID, MergeDecision{Scope: scopeA, ExpectedRevision: p.Revision, Decision: "accept"}); err != nil {
		t.Fatal("a person could not accept the proposal", err)
	}

	// A claim about something else stays a plain candidate.
	other := f.capture(f.human, captureInput(scopeA, "evt-other", explicit("커밋 메시지는 영어로 쓴다")))
	if other[0].Status != "candidate" || other[0].Reason != "review_required" {
		t.Fatal("unrelated claim was not a plain candidate", other)
	}
	if _, err := f.openProposal(other[0]); !errors.Is(err, ErrNotFound) {
		t.Fatal("unrelated claim got a proposal", err)
	}
}

func TestCaptureWithoutEmbeddingsKeepsTheOldPath(t *testing.T) {
	for name, down := range map[string]func(f *fixture){
		"embedder failing": func(f *fixture) { f.emb.err = errors.New("embedder down") },
		"no embedder":      func(f *fixture) { f.svc.Embedder = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.emb.semantic = true
			f.embedText(f.activeMemory("응답은 항상 쉬운 한국어로 짧게 쓴다"))
			down(f)
			got := f.capture(f.human, captureInput(scopeA, "evt-down", explicit("응답은 항상 쉬운 한국어로 짧게 쓴다 알겠지")))
			if got[0].Status != "candidate" || got[0].Reason != "review_required" {
				t.Fatal("old path not kept", got)
			}
			if _, err := f.openProposal(got[0]); !errors.Is(err, ErrNotFound) {
				t.Fatal("a proposal was stored without an embedding", err)
			}
		})
	}
}

// A slow embedder spends the request's gate budget: the later claims are still stored as
// ordinary candidates instead of the request running out of time, and their merge check is
// queued once per candidate for the worker, which opens the proposal a person decides.
func TestCaptureGateBudgetDefersTheCheckToTheWorker(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	f.mergeWorker()
	m := f.activeMemory("응답은 항상 쉬운 한국어로 짧게 쓴다")
	f.embedText(m)
	f.svc.CaptureGateBudget = 60 * time.Millisecond
	f.emb.onEmbed = func() { time.Sleep(40 * time.Millisecond) }
	before := len(f.emb.calls)
	claims := []CaptureClaim{explicit("첫째 규칙은 커밋을 작게 나눈다"), explicit("둘째 규칙은 테스트를 먼저 쓴다"),
		explicit("셋째 규칙은 로그에 비밀을 남기지 않는다"), explicit("응답은 항상 쉬운 한국어로 짧게 쓴다 알겠지")}
	got := f.capture(f.human, captureInput(scopeA, "evt-slow", claims...))
	for _, item := range got {
		if item.Status != "candidate" || item.Reason != "review_required" || item.CandidateID == "" {
			t.Fatal("a claim past the budget was not kept as a candidate", got)
		}
	}
	if calls := len(f.emb.calls) - before; calls == 0 || calls >= len(claims) {
		t.Fatalf("gate embedded %d of %d claims, want the budget to stop it early", calls, len(claims))
	}
	deferred := f.mergeGateJobs()
	if len(deferred) == 0 || len(deferred) >= len(claims) || !slices.ContainsFunc(deferred, func(j Record) bool { return j.Target.ID == got[3].CandidateID }) {
		t.Fatalf("deferred %+v", deferred)
	}
	// Queueing a revision again (a retried request) keeps one job.
	last := f.get(f.human, "memory_candidates", scopeA, got[3].CandidateID)
	f.svc.deferMergeGate(testCtx, scopeA, last, *last.Generator)
	if again := f.mergeGateJobs(); len(again) != len(deferred) {
		t.Fatal("a second deferral queued a second job", len(again), len(deferred))
	}
	if _, err := f.openProposal(got[3]); !errors.Is(err, ErrNotFound) {
		t.Fatal("the deferred claim already had a proposal", err)
	}
	f.emb.onEmbed = nil
	f.drainWorker()
	p, err := f.openProposal(got[3])
	if err != nil || p.Status != mergeOpen || len(p.Merge.Proposal.Targets) != 1 || p.Merge.Proposal.Targets[0].ID != m.ID {
		t.Fatalf("worker proposal %+v %v", p, err)
	}
	for _, j := range f.mergeGateJobs() {
		if j.Status != JobStatusDone {
			t.Fatalf("job not done %+v", j)
		}
	}
	// The worker changes nothing a person decides.
	if f.get(f.human, "memory_candidates", scopeA, got[3].CandidateID).Status != "candidate" || f.get(f.human, "memories", scopeA, m.ID).Status != "active" {
		t.Fatal("the deferred check changed a record on its own")
	}
}

// A candidate a person decided before the worker ran, or one that is gone, ends its job
// without a proposal; an embedder still down keeps the job for a retry.
func TestDeferredMergeGateSkipsDecidedCandidatesAndRetriesTheEmbedder(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	f.mergeWorker()
	f.embedText(f.activeMemory("응답은 항상 쉬운 한국어로 짧게 쓴다"))
	got := f.capture(f.human, captureInput(scopeA, "evt-later", explicit("응답은 항상 쉬운 한국어로 짧게 쓴다 알겠지")))
	cand := f.get(f.human, "memory_candidates", scopeA, got[0].CandidateID)
	if err := f.repo.Delete(testCtx, colMergeProposals, scopeA, mergeProposalID(scopeA, cand.ID, cand.Revision), 1); err != nil {
		t.Fatal(err)
	}
	job := func() Record {
		f.svc.deferMergeGate(testCtx, scopeA, cand, *cand.Generator)
		return f.mergeGateJobs()[0]
	}
	f.emb.err = errors.New("embedder down")
	if err := f.svc.runDeferredMergeGate(testCtx, f.work, job()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("an unavailable embedder did not keep the job for a retry", err)
	}
	f.emb.err = nil
	stale := job()
	stale.Target.Revision++
	if err := f.svc.runDeferredMergeGate(testCtx, f.work, stale); err != nil {
		t.Fatal(err)
	}
	gone := job()
	gone.Target.ID = "missing"
	if err := f.svc.runDeferredMergeGate(testCtx, f.work, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := f.openProposal(got[0]); !errors.Is(err, ErrNotFound) {
		t.Fatal("a stale or missing candidate got a proposal", err)
	}
	// A proposal stored by an earlier attempt ends the job.
	if err := f.svc.runDeferredMergeGate(testCtx, f.work, job()); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.runDeferredMergeGate(testCtx, f.work, job()); err != nil {
		t.Fatal("a repeated check failed on the stored proposal", err)
	}
}

// mergeWorker gives the worker what a deferred merge check needs (guide/server.md).
func (f *fixture) mergeWorker() {
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "embed", "search", "context")
}

func (f *fixture) mergeGateJobs() []Record {
	f.t.Helper()
	out := []Record{}
	for _, j := range f.repo.rows("jobs") {
		if j.Kind == captureMergeGateKind {
			out = append(out, j)
		}
	}
	return out
}

func (f *fixture) drainWorker() {
	f.t.Helper()
	for range 50 {
		if err := f.svc.WorkOnce(testCtx, f.work); errors.Is(err, ErrNotFound) {
			return
		}
	}
	f.t.Fatal("the worker did not drain its queue")
}
