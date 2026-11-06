package memory

import (
	"errors"
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

// A slow embedder spends the request's gate budget: the later claims skip the merge check
// and are still stored as ordinary candidates instead of the request running out of time.
func TestCaptureGateBudgetBoundsTheWaitOnASlowEmbedder(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	f.embedText(f.activeMemory("응답은 항상 쉬운 한국어로 짧게 쓴다"))
	f.svc.CaptureGateBudget = 60 * time.Millisecond
	f.emb.onEmbed = func() { time.Sleep(40 * time.Millisecond) }
	before := len(f.emb.calls)
	claims := []CaptureClaim{explicit("첫째 규칙은 커밋을 작게 나눈다"), explicit("둘째 규칙은 테스트를 먼저 쓴다"),
		explicit("셋째 규칙은 로그에 비밀을 남기지 않는다"), explicit("넷째 규칙은 브랜치 이름을 짧게 짓는다")}
	got := f.capture(f.human, captureInput(scopeA, "evt-slow", claims...))
	for _, item := range got {
		if item.Status != "candidate" || item.Reason != "review_required" || item.CandidateID == "" {
			t.Fatal("a claim past the budget was not kept as a candidate", got)
		}
	}
	if calls := len(f.emb.calls) - before; calls == 0 || calls >= len(claims) {
		t.Fatalf("gate embedded %d of %d claims, want the budget to stop it early", calls, len(claims))
	}
}
