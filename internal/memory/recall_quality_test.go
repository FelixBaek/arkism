package memory

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type noAnswerEval struct {
	Memories []struct {
		Key      string   `json:"key"`
		Text     string   `json:"text"`
		Keywords []string `json:"keywords"`
	} `json:"memories"`
	Queries []struct {
		Query  string `json:"query"`
		Answer string `json:"answer"`
	} `json:"queries"`
}

type noAnswerScore struct{ negatives, negativeReturned, answers, answerHit int }

func setRecallQuality(t *testing.T, f *fixture, q SpaceRecallQuality) {
	t.Helper()
	cur, err := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: cur.Revision, RecallQuality: &q}, false); err != nil {
		t.Fatal(err)
	}
}

func runNoAnswerEval(f *fixture, set noAnswerEval, ids map[string]string) noAnswerScore {
	var s noAnswerScore
	for _, q := range set.Queries {
		out := f.search(f.human, SearchInput{Scope: scopeA, Query: q.Query, Modes: []string{"exact", "fts"}, Limit: 5})
		if q.Answer == "" {
			s.negatives++
			if len(out.Items) > 0 {
				s.negativeReturned++
			}
			continue
		}
		s.answers++
		if slices.ContainsFunc(out.Items, func(h Hit) bool { return h.ID == ids[q.Answer] }) {
			s.answerHit++
		}
	}
	return s
}

// The relevance floor measured on the fixed no-answer set, before (gate off) and after.
func TestRelevanceGateNoAnswerEval(t *testing.T) {
	raw, err := os.ReadFile("testdata/recall_no_answer_v1.json")
	var set noAnswerEval
	if err != nil || json.Unmarshal(raw, &set) != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	ids := map[string]string{}
	for _, m := range set.Memories {
		ids[m.Key] = f.activeMemory(m.Text, m.Keywords...).ID
	}
	off := false
	setRecallQuality(t, f, SpaceRecallQuality{RelevanceGate: &off})
	before := runNoAnswerEval(f, set, ids)
	setRecallQuality(t, f, SpaceRecallQuality{})
	after := runNoAnswerEval(f, set, ids)
	t.Logf("recall_no_answer_v1 before: no-answer returned %d/%d, answer hit@5 %d/%d", before.negativeReturned, before.negatives, before.answerHit, before.answers)
	t.Logf("recall_no_answer_v1 after:  no-answer returned %d/%d, answer hit@5 %d/%d", after.negativeReturned, after.negatives, after.answerHit, after.answers)
	if after.negativeReturned >= before.negativeReturned || after.answerHit != before.answerHit || after.answerHit != after.answers {
		t.Fatal("relevance floor did not cut no-answer recall without losing answers", before, after)
	}
}

func TestRelevanceGateEmptiesWithReasonAndKeepsVectorHits(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("Pull requests need one human approval before merge", "project")
	in := SearchInput{Scope: scopeA, Query: "project budget for next quarter", Modes: []string{"exact", "fts"}, Limit: 5}
	out := f.search(f.human, in)
	if len(out.Items) != 0 || out.EmptyReason != emptyBelowRelevance {
		t.Fatal("an unrelated keyword hit passed the floor", out.Items, out.EmptyReason)
	}
	if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "project", Modes: []string{"exact"}, Limit: 5}); len(got.Items) != 1 || got.EmptyReason != "" {
		t.Fatal("a whole-keyword lookup was dropped", got.Items, got.EmptyReason)
	}
	// A vector neighbour is semantic evidence; the gate keeps it.
	lists := [][]Hit{{{ID: m.ID, Collection: colMemories, Reasons: []string{reasonExactKeyword}}}, {{ID: m.ID, Collection: colMemories, Reasons: []string{reasonVector}}}}
	trace := &recallTrace{}
	if kept := relevanceGate(in, lists, trace); len(kept[0]) != 1 {
		t.Fatal("vector-backed keyword hit dropped", kept)
	}
}

func TestRecallQualitySettingsValidate(t *testing.T) {
	f := newFixture(t)
	bad := SpaceRecallQuality{ConflictPending: "hide"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{RecallQuality: &bad}, false); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown conflict_pending mode stored", err)
	}
	if q := (*SpaceConfig)(nil).recallQuality(); !q.gateOn() || q.Provisional || q.AutoCloseOnCorrection || q.ConflictPending != "" {
		t.Fatal("defaults changed: gate on, provisional and auto-close off, attach", q)
	}
}

// heldCorrection captures an approved memory, then an agent's correction a person must review.
func heldCorrection(t *testing.T, f *fixture, subject, oldText, newText string) (Record, CaptureItem) {
	t.Helper()
	claim := explicit(oldText)
	claim.Subject = subject
	old := f.captureAndReview(f.human, captureInput(scopeA, "evt-"+subject+"-old", claim))[0]
	mem := f.get(f.human, colMemories, scopeA, old.MemoryID)
	fix := explicit(newText)
	fix.Subject, fix.Basis = subject, "inferred"
	fix.Supersedes = &Ref{Collection: colMemories, ID: mem.ID, Revision: mem.Revision}
	got := f.capture(f.agent, captureInput(scopeA, "evt-"+subject+"-fix", fix))
	if got[0].Status != "candidate" || got[0].Reason != "conflict_review" {
		t.Fatal("correction not held for review", got)
	}
	return f.get(f.human, colMemories, scopeA, mem.ID), got[0]
}

func reviewCorrection(t *testing.T, f *fixture, held Record, item CaptureItem, resolution string) Record {
	t.Helper()
	held = f.get(f.human, colMemories, scopeA, held.ID)
	rel := f.get(f.human, "memory_relations", scopeA, held.ConflictID)
	cand := f.get(f.human, "memory_candidates", scopeA, item.CandidateID)
	m, err := f.svc.ReviewCandidate(testCtx, f.human, cand.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: cand.Revision, Action: "approve", OperationID: "fix-" + held.ID,
		Conflict: &CandidateReviewConflict{ID: rel.ID, ExpectedRevision: rel.Revision, Memory: Ref{colMemories, held.ID, held.Revision}, Resolution: resolution, Reason: "person reviewed"}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func hitByID(items []Hit, id string) *Hit {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

// 수정·정정: capture → classify (held as conflict_review) → store (old stays, labelled) →
// recall (correction attached or excluded) → update (person replaces) → forget (person rejects).
func TestCorrectionCategoryWalk(t *testing.T) {
	f := newFixture(t)
	limited := testPrincipalToken("tok-agent-limited")
	f.auth.actors[limited] = actor("agent-2", "agent", true, allVerbs...)
	f.auth.actors[limited].verbs["!read:memory_candidates"] = true
	held, item := heldCorrection(t, f, "demo-ui", "기본 테마는 다크", "기본 테마는 paper 라이트로 바꿨다")
	query := SearchInput{Scope: scopeA, Query: "기본 테마", Modes: []string{"exact", "fts"}, Limit: 5}

	h := hitByID(f.search(f.agent, query).Items, held.ID)
	if h == nil || h.PendingCorrection == nil || h.PendingCorrection.CandidateID != item.CandidateID || !strings.Contains(h.PendingCorrection.Text, "paper") || h.PendingCorrection.Notice == "" {
		t.Fatal("old memory recalled without its pending correction", h)
	}
	h = hitByID(f.search(limited, query).Items, held.ID)
	if h == nil || h.PendingCorrection == nil || h.PendingCorrection.Text != "" || h.PendingCorrection.Notice == "" {
		t.Fatal("candidate text reached a caller without read:memory_candidates", h)
	}
	setRecallQuality(t, f, SpaceRecallQuality{ConflictPending: conflictPendingExclude})
	if hitByID(f.search(f.agent, query).Items, held.ID) != nil {
		t.Fatal("exclude mode still recalled the disputed memory")
	}
	setRecallQuality(t, f, SpaceRecallQuality{})

	fixed := reviewCorrection(t, f, held, item, "replace_old")
	items := f.search(f.agent, query).Items
	if hitByID(items, held.ID) != nil || hitByID(items, fixed.ID) == nil || hitByID(items, fixed.ID).PendingCorrection != nil {
		t.Fatal("after the person's decision recall still served the old fact", items)
	}
	f.transition(f.human, colMemories, fixed.ID, ChangeInput{Scope: scopeA, ExpectedRevision: fixed.Revision, Status: "rejected"})
	if out := f.search(f.agent, query); hitByID(out.Items, fixed.ID) != nil {
		t.Fatal("a forgotten correction was recalled", out.Items)
	}
}

// 시간에 따라 바뀌는 사실: a new fact kept next to the old one proposes the old one's end; a
// person accepts it (or the space closes it), and valid-time and record-time queries both answer.
func TestChangingFactCategoryWalk(t *testing.T) {
	f := newFixture(t)
	held, item := heldCorrection(t, f, "felix-home", "Felix lives in Seoul", "Felix moved to Busan in November")
	beforeMove := time.Now().UTC()
	newer := reviewCorrection(t, f, held, item, "keep_both")
	old := f.get(f.human, colMemories, scopeA, held.ID)
	if old.ExpiresAt != nil || old.ValidToProposal == nil || old.ValidToProposal.By.ID != newer.ID {
		t.Fatal("approving the new fact did not propose ending the old one", old.ExpiresAt, old.ValidToProposal)
	}
	query := SearchInput{Scope: scopeA, Query: "where does Felix live", Modes: []string{"exact", "fts"}, Limit: 5}
	if h := hitByID(f.search(f.agent, query).Items, old.ID); h != nil && h.ValidToProposal == nil {
		t.Fatal("recall hid the valid_to proposal", h)
	}
	if _, err := f.svc.SetMemoryExpiry(testCtx, f.agent, old.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: old.Revision, ExpiresAt: &old.ValidToProposal.At}); err == nil {
		t.Fatal("an agent closed a fact")
	}
	other := old.ValidToProposal.At.Add(-time.Hour)
	if _, err := f.svc.SetMemoryExpiry(testCtx, f.human, old.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: old.Revision, ExpiresAt: &other}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a past end other than the proposal was accepted", err)
	}
	closed, err := f.svc.SetMemoryExpiry(testCtx, f.human, old.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: old.Revision, ExpiresAt: &old.ValidToProposal.At})
	if err != nil || closed.ExpiresAt == nil || closed.ValidToProposal != nil {
		t.Fatal("accepting the proposal failed", err, closed.ValidToProposal)
	}
	time.Sleep(2 * time.Millisecond)
	if hitByID(f.search(f.agent, query).Items, old.ID) != nil {
		t.Fatal("a closed fact is still recalled as current")
	}
	// Bitemporal: valid time picks the fact true then; record time picks what was known then.
	now := time.Now().UTC()
	at := func(asOf, validAt time.Time) []string {
		page, err := f.svc.MemoriesAt(testCtx, f.human, MemoryAtInput{Scope: scopeA, AsOf: asOf, ValidAt: &validAt, Query: "Felix", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, h := range page.Items {
			ids = append(ids, h.ID)
		}
		return ids
	}
	if got := at(now, beforeMove); !slices.Contains(got, old.ID) {
		t.Fatal("valid time before the move lost the old fact", got)
	}
	if got := at(now, now); slices.Contains(got, old.ID) || !slices.Contains(got, newer.ID) {
		t.Fatal("valid time now still holds the old fact", got)
	}
	if got := at(beforeMove, beforeMove); slices.Contains(got, newer.ID) || !slices.Contains(got, old.ID) {
		t.Fatal("record time before approval knew the new fact", got)
	}

	// The space opts in: approving the next change closes the old fact at once.
	setRecallQuality(t, f, SpaceRecallQuality{AutoCloseOnCorrection: true})
	held2, item2 := heldCorrection(t, f, "deploy-day", "배포는 금요일에 한다", "이제 배포는 화요일에 한다")
	reviewCorrection(t, f, held2, item2, "keep_both")
	if auto := f.get(f.human, colMemories, scopeA, held2.ID); auto.ExpiresAt == nil || auto.ValidToProposal != nil {
		t.Fatal("auto-close did not end the old fact", auto.ExpiresAt, auto.ValidToProposal)
	}
}

// Provisional recall is opt-in, separate from approved memory, labelled and permission-gated.
func TestProvisionalRecallOptIn(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("배포는 매주 금요일 오후에 한다", "배포")
	claim := explicit("스테이징 배포는 목요일 저녁에 한다")
	claim.Basis = "inferred"
	pending := f.capture(f.agent, captureInput(scopeA, "evt-prov", claim))[0]
	query := SearchInput{Scope: scopeA, Query: "스테이징 배포", Modes: []string{"exact", "fts"}, Limit: 5}
	if out := f.search(f.agent, query); len(out.Provisional) != 0 {
		t.Fatal("provisional candidates served without the space opting in", out.Provisional)
	}
	setRecallQuality(t, f, SpaceRecallQuality{Provisional: true})
	out := f.search(f.agent, query)
	if len(out.Provisional) != 1 || out.Provisional[0].ID != pending.CandidateID || out.Provisional[0].Origin != "provisional" || out.ProvisionalNotice == "" {
		t.Fatal("opted-in provisional candidate missing or unlabelled", out.Provisional, out.ProvisionalNotice)
	}
	if hitByID(out.Items, pending.CandidateID) != nil {
		t.Fatal("a candidate was mixed into approved items")
	}
	limited := testPrincipalToken("tok-agent-limited")
	f.auth.actors[limited] = actor("agent-2", "agent", true, allVerbs...)
	f.auth.actors[limited].verbs["!read:memory_candidates"] = true
	if out = f.search(limited, query); len(out.Provisional) != 0 {
		t.Fatal("provisional reached a caller without read:memory_candidates")
	}
	if out = f.search(f.agent, SearchInput{Scope: scopeA, Query: "quarterly budget", Modes: []string{"exact", "fts"}, Limit: 5}); len(out.Provisional) != 0 {
		t.Fatal("an unrelated candidate came back provisional", out.Provisional)
	}
}

// A realistic mixed session: Korean and English captures, a correction, a moved fact and
// questions the space cannot answer.
func TestRecallQualityLiveStyleScenario(t *testing.T) {
	f := newFixture(t)
	held, fix := heldCorrection(t, f, "release", "릴리스 노트는 영어로만 쓴다", "릴리스 노트는 영어 다음에 한국어도 쓴다")
	moved, change := heldCorrection(t, f, "ci-runner", "CI runs on the self-hosted runner", "CI moved to hosted runners in October")
	f.activeMemory("Felix prefers short answers in Korean", "answer", "style", "korean")

	ask := func(q string) ContextBundle {
		return f.search(f.agent, SearchInput{Scope: scopeA, Query: q, Modes: []string{"exact", "fts"}, Limit: 5})
	}
	if h := hitByID(ask("릴리스 노트").Items, held.ID); h == nil || h.PendingCorrection == nil || !strings.Contains(h.PendingCorrection.Text, "한국어") {
		t.Fatal("the pending correction did not travel with the old rule", h)
	}
	for _, q := range []string{"korean food recipe for dinner", "style of the wedding dress", "CI 비용 청구서 금액"} {
		if out := ask(q); len(out.Items) != 0 {
			t.Fatal("no-answer question recalled memory", q, out.Items)
		}
	}
	reviewCorrection(t, f, held, fix, "replace_old")
	newer := reviewCorrection(t, f, moved, change, "keep_both")
	out := ask("self-hosted runner")
	if h := hitByID(out.Items, moved.ID); h == nil || h.ValidToProposal == nil || h.ValidToProposal.By.ID != newer.ID {
		t.Fatal("the moved fact did not carry its end proposal", out.Items)
	}
}
