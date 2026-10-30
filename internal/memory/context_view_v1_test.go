package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func viewMemory(t *testing.T, f *fixture, scope Scope, text string, tags ...string) Record {
	t.Helper()
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scope, Text: text}))
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scope, Kind: "user", Text: text, Tags: tags, Keywords: []string{"viewcanary"}, Sources: refs(src)}))
	m, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scope, ExpectedRevision: c.Revision, Action: "approve"})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func viewInput(scope Scope) ContextBuildInput {
	return ContextBuildInput{Scope: scope, Task: "viewcanary", Modes: []string{"exact", "fts"}, TokenEncoding: "cl100k_base"}
}
func excludeView(scope Scope, r Record) *ContextInvocationView {
	return &ContextInvocationView{Protocol: ContextViewProtocol, ContextScope: scope, Exclude: []Ref{{Collection: colMemories, ID: r.ID, Revision: r.Revision}}}
}
func buildView(t *testing.T, s *Service, token string, in ContextBuildInput) ContextPack {
	t.Helper()
	p, e := s.BuildContext(testCtx, token, in)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func viewHas(items []Hit, id string) bool {
	return slices.ContainsFunc(items, func(h Hit) bool { return h.ID == id })
}
func viewCanonicalHash(f *fixture) string {
	all := map[string]map[string]Record{}
	for _, col := range []string{colMemories, "memory_candidates", "sources", "evaluations", "crew", colJobs} {
		all[col] = map[string]Record{}
		for _, r := range f.repo.rows(col) {
			if col == colJobs && !strings.HasPrefix(r.Kind, nativeDynamicsPrefix) {
				continue
			}
			all[col][r.ID] = r
		}
	}
	return ContentHash(structuredText(all))
}

type viewReaderReceipt struct {
	Session, Phase, InputSHA string
	Refs                     []Ref
	TextSHAs                 []string
}

func viewReadPack(t *testing.T, session, phase string, p ContextPack) viewReaderReceipt {
	t.Helper()
	rendered, e := host.AssembleParts(host.Part{Full: p.ContextData, Stable: true}, host.Part{}, host.Part{}, host.ContextMaxChars)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(bytes.NewReader([]byte(rendered)))
	if e != nil {
		t.Fatal(e)
	}
	var observed ContextBundle
	if e = json.Unmarshal(raw, &observed); e != nil {
		t.Fatal("isolated same reader received unexpected rendering", e)
	}
	if observed.Authority != AuthorityDataOnly {
		t.Fatal("authority changed")
	}
	rec := viewReaderReceipt{Session: session, Phase: phase, InputSHA: ContentHash(string(raw)), Refs: []Ref{}, TextSHAs: []string{}}
	for _, h := range observed.Items {
		rec.Refs = append(rec.Refs, Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision})
		rec.TextSHAs = append(rec.TextSHAs, ContentHash(h.Text))
	}
	return rec
}
func TestContextViewCurrentNextAndNewSession(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "shadow"}[shadow], func(t *testing.T) { exerciseContextViewSessions(t, shadow) })
	}
}
func exerciseContextViewSessions(t *testing.T, shadow bool) {
	t.Helper()
	f := newFixture(t)
	f.svc.Embedder = nil
	a := viewMemory(t, f, scopeA, "viewcanary old-body")
	b := viewMemory(t, f, scopeA, "viewcanary independent-body")
	if shadow {
		f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B0")
	}
	repo := &nativeSearchCountRepo{Repository: f.repo}
	f.svc.Repo = repo
	before := viewCanonicalHash(f)
	in := viewInput(scopeA)
	in.ContextView = excludeView(scopeA, a)
	current := buildView(t, f.svc, f.human, in)
	if viewHas(current.Items, a.ID) || !viewHas(current.Items, b.ID) || repo.calls != 1 {
		t.Fatal("current exact exclusion or retrieval count")
	}
	if current.ContextView == nil || current.ContextView.PriorPathRemoved || current.ContextView.ReaderAcknowledgement != "unknown" || len(current.ContextView.ExcludedSearch) != 1 {
		t.Fatal("view observation")
	}
	journal := []viewReaderReceipt{viewReadPack(t, "one", "current", current)}
	in.ContextView = nil
	next := buildView(t, f.svc, f.human, in)
	fresh := buildView(t, f.svc, f.human, in)
	for _, p := range []ContextPack{next, fresh} {
		if !viewHas(p.Items, a.ID) || !viewHas(p.Items, b.ID) || p.ContextView != nil {
			t.Fatal("nonpersistent view retained unexpectedly")
		}
	}
	journal = append(journal, viewReadPack(t, "one", "next", next), viewReadPack(t, "new", "newsession", fresh))
	if len(journal[0].Refs) != 1 || len(journal[1].Refs) != 2 || len(journal[2].Refs) != 2 || repo.calls != 3 || before != viewCanonicalHash(f) {
		t.Fatal("phase/reader/canonical ledger")
	}
	// Server cannot observe this isolated reader: actual delivery/use/task/causal remain unknown.
	if current.ContextView.ActualUse != "unknown" || current.ContextView.TaskSuccess != "unknown" {
		t.Fatal("invented task evidence")
	}
}
func TestContextViewNilBackwardCompatibilityAndShadow(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	a := viewMemory(t, f, scopeA, "viewcanary a")
	viewMemory(t, f, scopeA, "viewcanary b")
	in := viewInput(scopeA)
	nilPack := buildView(t, f.svc, f.human, in)
	direct := f.search(f.human, SearchInput{Scope: scopeA, Query: in.Task, Modes: in.Modes, Limit: 6, BudgetTokens: 2000, TokenEncoding: in.TokenEncoding, Purpose: "answer"})
	nilPack.ContextID = ""
	direct.ContextID = ""
	if !reflect.DeepEqual(nilPack.ContextBundle, direct) || nilPack.ContextView != nil {
		t.Fatal("nil behavior changed")
	}
	in.ContextView = excludeView(scopeA, a)
	off := buildView(t, f.svc, f.human, in)
	f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B0")
	on := buildView(t, f.svc, f.human, in)
	off.ContextID = ""
	on.ContextID = ""
	if !reflect.DeepEqual(off, on) {
		t.Fatal("view shadow changed actual response")
	}
	raw, _ := json.Marshal(nilPack)
	if bytes.Contains(raw, []byte("context_view")) {
		t.Fatal("nil response wire changed")
	}
}
func TestContextViewExactScopeRevisionAndValidation(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	a := viewMemory(t, f, scopeA, "viewcanary a")
	in := viewInput(scopeA)
	in.ContextView = excludeView(scopeA, a)
	in.ContextView.Exclude[0].Revision++
	if !viewHas(buildView(t, f.svc, f.human, in).Items, a.ID) {
		t.Fatal("different revision excluded")
	}
	in.ContextView.ContextScope = scopeB
	if _, e := f.svc.BuildContext(testCtx, f.human, in); !errors.Is(e, ErrInvalid) {
		t.Fatal("foreign context scope", e)
	}
	for _, mode := range []string{"protocol", "collection", "zero_revision", "duplicate", "null", "bound"} {
		t.Run(mode, func(t *testing.T) {
			v := excludeView(scopeA, a)
			switch mode {
			case "protocol":
				v.Protocol = "other"
			case "collection":
				v.Exclude[0].Collection = "sources"
			case "zero_revision":
				v.Exclude[0].Revision = 0
			case "duplicate":
				v.Exclude = append(v.Exclude, v.Exclude[0])
			case "null":
				v.Exclude = nil
			case "bound":
				v.Exclude = make([]Ref, 129)
			}
			p := viewInput(scopeA)
			p.ContextView = v
			if _, e := f.svc.BuildContext(testCtx, f.human, p); !errors.Is(e, ErrInvalid) {
				t.Fatal("invalid view accepted", e)
			}
		})
	}
}
func TestContextViewPinsAndPriorPath(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	a := viewMemory(t, f, scopeA, "viewcanary pinned")
	viewMemory(t, f, scopeA, "viewcanary other")
	f.crew(pinAgent("view-reader", memPin(a.ID, "full", 90)))
	original := buildView(t, f.svc, f.human, viewInput(scopeA))
	cached := validationReplay(t, f, validationInput(a))
	in := viewInput(scopeA)
	in.Agent = "@view-reader"
	in.ContextView = excludeView(scopeA, a)
	pack := buildView(t, f.svc, f.human, in)
	if viewHas(pack.Items, a.ID) || len(pack.ContextView.ExcludedPins) != 1 || len(pack.ContextView.ExcludedSearch) != 1 {
		t.Fatal("pin/search reintroduction")
	}
	out, e := f.svc.ValidateContext(testCtx, f.human, cached)
	if e != nil || !out.Integrity || out.Items[0].Status != "valid" {
		t.Fatal("transient view retroactively invalidated cache", e)
	}
	if !viewHas(original.Items, a.ID) {
		t.Fatal("prior delivery fixture")
	}
	in.ContextView = nil
	if !viewHas(buildView(t, f.svc, f.human, in).Items, a.ID) {
		t.Fatal("next unmasked pins")
	}
}
func TestContextViewLaneOrderAndNoAlias(t *testing.T) {
	a := Hit{Scope: scopeA, Collection: colMemories, ID: "a", Revision: 7}
	b := a
	b.ID = "b"
	newer := a
	newer.Revision++
	foreign := a
	foreign.Scope = scopeB
	v := &ContextInvocationView{Protocol: ContextViewProtocol, ContextScope: scopeA, Exclude: []Ref{{Collection: colMemories, ID: "a", Revision: 7}}}
	frozen, e := freezeContextView(scopeA, v)
	if e != nil {
		t.Fatal(e)
	}
	v.Exclude[0].ID = "b"
	lanes := [][]Hit{{a, b, b, newer, foreign}, {b, a, b}}
	filterContextViewLanes(lanes, frozen)
	if !reflect.DeepEqual(lanes, [][]Hit{{b, b, newer, foreign}, {b, b}}) {
		t.Fatal("relative duplicates/exact scope/revision")
	}
}
func TestContextViewDoesNotBypassFinalFence(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	a := viewMemory(t, f, scopeA, "viewcanary excluded")
	b := viewMemory(t, f, scopeA, "viewcanary returned")
	f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B0")
	f.svc.Repo = &nativeSearchBeforeTxRepo{Repository: f.repo, before: func() {
		if _, e := f.svc.Transition(testCtx, f.human, colMemories, b.ID, ChangeInput{Scope: scopeA, ExpectedRevision: b.Revision, Status: "rejected"}); e != nil {
			t.Fatal(e)
		}
	}}
	in := viewInput(scopeA)
	in.ContextView = excludeView(scopeA, a)
	pack, e := f.svc.BuildContext(testCtx, f.human, in)
	if e == nil || pack.ContextData != "" {
		t.Fatal("final fence bypassed", e)
	}
	stages := f.svc.NativeSearchShadow.StagesV2()
	if len(stages) != 1 || stages[0].BaselineReturned || len(stages[0].FinalRejected) != 1 || stages[0].FinalRejected[0].ScopedRefSHA != contextScopedRefSHA(scopeA, Ref{Collection: colMemories, ID: b.ID, Revision: b.Revision}) {
		t.Fatal("final reject attribution")
	}
}
func TestContextViewCallerCancellation(t *testing.T) {
	f := newFixture(t)
	a := viewMemory(t, f, scopeA, "viewcanary")
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	in := viewInput(scopeA)
	in.ContextView = excludeView(scopeA, a)
	if _, e := f.svc.BuildContext(ctx, f.human, in); e == nil {
		t.Fatal("canceled input accepted")
	}
}

func viewPendingCorrection(t *testing.T, f *fixture, old Record) (Record, Record) {
	t.Helper()
	claim := explicit("viewcanary new lasting preference")
	claim.Supersedes = &Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}
	captured, e := f.svc.Capture(testCtx, f.human, captureInput(scopeA, "sys01-v8-correction", claim))
	if e != nil || len(captured.Items) != 1 || captured.Items[0].Status != "candidate" {
		t.Fatal("capture must wait for human", e)
	}
	candidate := f.get(f.human, "memory_candidates", scopeA, captured.Items[0].CandidateID)
	current := f.get(f.human, colMemories, scopeA, old.ID)
	if candidate.Status != "conflicted" || candidate.ApprovedBy != "" || current.Status != "active" || current.Text != old.Text || candidate.ConflictID == "" {
		t.Fatal("hold/approval boundary")
	}
	if _, e = f.svc.ReviewCandidate(testCtx, f.agent, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"}); !errors.Is(e, ErrDenied) {
		t.Fatal("agent approved correction", e)
	}
	return candidate, current
}
func viewHeldPhases(t *testing.T, f *fixture, current Record) {
	t.Helper()
	in := viewInput(scopeA)
	in.ContextView = excludeView(scopeA, current)
	masked := buildView(t, f.svc, f.human, in)
	first := viewReadPack(t, "same", "current-held-mask", masked)
	if viewHas(masked.Items, current.ID) || slices.ContainsFunc(first.Refs, func(r Ref) bool { return r.ID == current.ID }) {
		t.Fatal("held current one-shot exclusion")
	}
	in.ContextView = nil
	for _, phase := range []string{"next-held", "new-session-held"} {
		p := buildView(t, f.svc, f.human, in)
		receipt := viewReadPack(t, phase, phase, p)
		if !viewHas(p.Items, current.ID) || !slices.Contains(receipt.Refs, Ref{Collection: colMemories, ID: current.ID, Revision: current.Revision}) {
			t.Fatal("held memory did not remain active", phase)
		}
	}
}
func viewConfirmCorrection(t *testing.T, f *fixture, candidate, current Record) Record {
	t.Helper()
	relation := f.get(f.human, "memory_relations", scopeA, candidate.ConflictID)
	m, e := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve", OperationID: "sys01-v8-human-confirm", Conflict: &CandidateReviewConflict{ID: relation.ID, ExpectedRevision: relation.Revision, Memory: Ref{Collection: colMemories, ID: current.ID, Revision: current.Revision}, Resolution: "replace_old", Reason: "explicit human correction"}})
	if e != nil || m.Status != "active" || m.ApprovedBy == "" || m.Text != "viewcanary new lasting preference" {
		t.Fatal("explicit correction", e)
	}
	old := f.get(f.human, colMemories, scopeA, current.ID)
	if old.Status != "superseded" || old.SupersededBy == nil || old.SupersededBy.ID != m.ID || len(m.Supersedes) != 1 || ContentHash(old.Text) != ContentHash(current.Text) {
		t.Fatal("old/new exact metadata")
	}
	return m
}
func viewCorrectedPhases(t *testing.T, f *fixture, old, current Record) {
	t.Helper()
	for _, phase := range []string{"current-corrected", "next-corrected", "new-session-corrected"} {
		p := buildView(t, f.svc, f.human, viewInput(scopeA))
		reader := viewReadPack(t, phase, phase, p)
		if viewHas(p.Items, old.ID) || !viewHas(p.Items, current.ID) || !slices.Contains(reader.Refs, Ref{Collection: colMemories, ID: current.ID, Revision: current.Revision}) {
			t.Fatal("corrected reader projection", phase)
		}
	}
	historical := f.search(f.human, SearchInput{Scope: scopeA, Query: "viewcanary", Modes: []string{"fts"}, IncludeInactive: true, Limit: 10, MaxChars: 4000, BudgetTokens: 2000, TokenEncoding: "cl100k_base", SearchCriteria: SearchCriteria{Filters: &SearchFilters{Tags: []string{"view:old"}}}})
	if len(historical.History) != 1 || historical.History[0].ID != old.ID || historical.History[0].Revision <= old.Revision || ContentHash(historical.History[0].Text) != ContentHash(old.Text) {
		t.Fatal("explicit history/old tag not diagnosed")
	}
	var contextData ContextBundle
	if json.Unmarshal([]byte(historical.ContextData), &contextData) != nil || len(contextData.Items) != 0 {
		t.Fatal("history metadata is not current context data")
	}
	alternate := viewMemory(t, f, scopeA, old.Text, "view:alternate")
	in := viewInput(scopeA)
	in.Filters = &SearchFilters{Tags: []string{"view:alternate"}}
	if !viewHas(buildView(t, f.svc, f.human, in).Items, alternate.ID) || alternate.ID == old.ID || ContentHash(alternate.Text) != ContentHash(old.Text) {
		t.Fatal("other active tag/ref content path")
	}
}
func viewWithdrawPhases(t *testing.T, f *fixture, m Record) {
	t.Helper()
	if e := f.svc.Delete(testCtx, f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}); e != nil {
		t.Fatal("canonical delete", e)
	}
	for _, phase := range []string{"current-withdrawn", "next-withdrawn", "new-session-withdrawn"} {
		p := buildView(t, f.svc, f.human, viewInput(scopeA))
		r := viewReadPack(t, phase, phase, p)
		if viewHas(p.Items, m.ID) || slices.ContainsFunc(r.Refs, func(ref Ref) bool { return ref.ID == m.ID }) {
			t.Fatal("withdrawn ref returned", phase)
		}
	}
}
func TestContextViewPersistentCorrectionWithdrawalAndHistory(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "shadow"}[shadow], func(t *testing.T) {
			f := newFixture(t)
			f.svc.Embedder = nil
			old := viewMemory(t, f, scopeA, "viewcanary old lasting preference", "view:old")
			viewMemory(t, f, scopeB, old.Text, "view:foreign")
			initial := buildView(t, f.svc, f.human, viewInput(scopeA))
			reader := viewReadPack(t, "same", "initial", initial)
			cache := validationInput(old)
			cache.BlockHash = reader.InputSHA
			sealed := validationReplay(t, f, cache)
			if _, e := f.svc.Transition(testCtx, f.human, colMemories, old.ID, ChangeInput{Scope: scopeA, ExpectedRevision: old.Revision, Status: "superseded"}); !errors.Is(e, ErrInvalid) {
				t.Fatal("direct superseded transition was accepted", e)
			}
			if shadow {
				f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B0")
			}
			candidate, held := viewPendingCorrection(t, f, old)
			viewHeldPhases(t, f, held)
			out, e := f.svc.ValidateContext(testCtx, f.human, sealed)
			assertValidationUnavailable(t, out, e)
			corrected := viewConfirmCorrection(t, f, candidate, held)
			viewCorrectedPhases(t, f, old, corrected)
			viewWithdrawPhases(t, f, corrected)
		})
	}
}

func TestContextViewExcludedHistoryNotInFreshShadowProjection(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	old := viewMemory(t, f, scopeA, "viewcanary excluded history")
	r := f.transition(f.human, colMemories, old.ID, ChangeInput{Scope: scopeA, ExpectedRevision: old.Revision, Status: "rejected"})
	f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B1")
	in := nativeSearchTestInput()
	in.Query = "viewcanary"
	in.contextView = excludeView(scopeA, r)
	out := f.search(f.human, in)
	v := f.svc.NativeSearchShadow.StagesV2()[0]
	if len(out.Items) != 0 || len(out.History) != 0 || v.Status != "selected" || v.Reason != "none" || v.FullRankedRefs == nil || len(v.FullRankedRefs) != 0 {
		t.Fatal("excluded history retained in fresh candidate projection", v.Status, v.Reason)
	}
	in.contextView = nil
	unmasked := f.search(f.human, in)
	if len(unmasked.History) != 1 || unmasked.History[0].ID != old.ID {
		t.Fatal("mask persisted into independent history path")
	}
}
