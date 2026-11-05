//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func obsAnswer(t *testing.T, drafts ...observationDraft) string {
	t.Helper()
	raw, err := json.Marshal(observationAnswer{Observations: drafts})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func obsDraft(content string, ids ...string) observationDraft {
	return observationDraft{Content: content, SourceMemoryIDs: ids, Durability: "lasting", DurabilityReason: "a standing rule", Confidence: 0.8}
}

// obsFixture: three similar approved memories (one cluster) and one unrelated memory.
func obsFixture(t *testing.T) (*fixture, *fakeWikiLLM, []Record) {
	f, llm := wikiFixture(t)
	ms := []Record{
		f.activeMemory("deploy to staging requires the staging flag in the deploy script"),
		f.activeMemory("deploy to staging requires the staging flag set in the deploy script"),
		f.activeMemory("deploy to staging requires the staging flag in every deploy script"),
		f.activeMemory("lunch orders close at noon on fridays"),
	}
	return f, llm, ms
}

func (f *fixture) consolidate(token string, dryRun bool) (ObservationConsolidateResult, error) {
	return f.svc.ConsolidateObservations(testCtx, token, ObservationConsolidateInput{Scope: scopeA, DryRun: dryRun})
}

func recalledObservation(t *testing.T, f *fixture, token, query string) *Hit {
	t.Helper()
	out, err := f.svc.Search(testCtx, token, SearchInput{Scope: scopeA, Query: query, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range out.Items {
		if h.Collection == colObservations {
			return &out.Items[i]
		}
	}
	return nil
}

func TestObservationConsolidateValidatesCitations(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t,
		obsDraft("Staging deploys need the staging flag in the deploy script.", ms[0].ID, ms[1].ID, ms[2].ID),
		obsDraft("Lunch and staging are related.", ms[0].ID, ms[3].ID),
		obsDraft("One note only.", ms[0].ID),
		obsDraft("Twice the same note.", ms[0].ID, ms[0].ID),
		observationDraft{Content: "Bad durability.", SourceMemoryIDs: []string{ms[0].ID, ms[1].ID}, Durability: "forever"},
	)}
	out, err := f.consolidate(f.human, false)
	if err != nil {
		t.Fatal(err)
	}
	codes := []string{}
	for _, r := range out.Rejected {
		codes = append(codes, r.Code)
	}
	want := []string{"citation_outside_group", "too_few_citations", "duplicate_citation", "invalid_durability"}
	if len(out.Proposed) != 1 || !slices.Equal(codes, want) || out.Calls != 1 || out.Groups != 1 {
		t.Fatalf("proposed=%d codes=%v calls=%d groups=%d", len(out.Proposed), codes, out.Calls, out.Groups)
	}
	p := out.Proposed[0]
	if p.Status != obsProposed || !slices.Equal(p.SourceMemoryIDs, []string{ms[0].ID, ms[1].ID, ms[2].ID}) || !p.Derived || p.Durability != "lasting" {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(llm.prompts[0], "MEMORY_DATA") || strings.Contains(llm.prompts[0], "lunch") {
		t.Fatal("the model sees only the group's memories, fenced as data")
	}
	// A covered group is not asked about again.
	if again, err := f.consolidate(f.human, false); err != nil || again.Calls != 0 {
		t.Fatalf("%+v %v", again, err)
	}
	// The usage ledger records the call under the shared wiki cap.
	if n, _ := f.svc.wikiDailyCalls(testCtx, scopeA, p.CreatedAt); n != 1 {
		t.Fatalf("recorded calls %d", n)
	}
}

func TestObservationModelNotConfiguredAndAgentDenied(t *testing.T) {
	f := newFixture(t)
	if _, err := f.consolidate(f.human, true); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("got %v", err)
	}
	if code, status := errorResponse(ErrModelNotConfigured); code != "model_not_configured" || status != 503 {
		t.Fatal(code, status)
	}
	f, llm, ms := obsFixture(t)
	if _, err := f.consolidate(f.agent, false); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent consolidate: %v", err)
	}
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatal(err)
	}
	p := out.Proposed[0]
	if _, err := f.svc.ReviewObservation(testCtx, f.agent, p.ID, ObservationReviewInput{Scope: scopeA, ExpectedRevision: p.Revision, Action: "approve"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent approve: %v", err)
	}
	batch := ObservationReviewBatchInput{Scope: scopeA, Items: []ObservationReviewItem{{ID: p.ID, ExpectedRevision: p.Revision, Action: "approve"}}}
	if _, err := f.svc.ReviewObservations(testCtx, f.agent, batch); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent batch: %v", err)
	}
	if _, err := f.svc.Get(testCtx, f.human, colObservations, scopeA, p.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("generic record reads must not serve observations")
	}
	if _, err := f.svc.Create(testCtx, f.agent, colObservations, CreateInput{Scope: scopeA, Text: "self approved"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic create: %v", err)
	}
}

func TestObservationApprovalRequiredBeforeRecall(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID, ms[2].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatal(err)
	}
	p := out.Proposed[0]
	if h := recalledObservation(t, f, f.agent, "staging flag"); h != nil {
		t.Fatal("a proposal must not be recalled")
	}
	res, err := f.svc.ReviewObservations(testCtx, f.human, ObservationReviewBatchInput{Scope: scopeA, Items: []ObservationReviewItem{
		{ID: p.ID, ExpectedRevision: p.Revision, Action: "approve"},
		{ID: "ob_missing", ExpectedRevision: 1, Action: "approve"},
	}})
	if err != nil || res.Reviewed != 1 || res.Failed != 1 || res.Items[1].Code != "not_found" {
		t.Fatalf("%+v %v", res, err)
	}
	h := recalledObservation(t, f, f.agent, "staging flag")
	if h == nil || !h.Derived || h.Kind != observationKind || h.Origin != observationKind || !slices.Equal(h.SourceIDs, []string{ms[0].ID, ms[1].ID, ms[2].ID}) {
		t.Fatalf("approved observation not recalled: %+v", h)
	}
	if !slices.ContainsFunc(f.embeddingJobs(), func(j Record) bool { return j.Target != nil && j.Target.ID == p.ID }) {
		t.Fatal("approval queues the embedding")
	}
	// Rejecting retires it from recall again.
	cur, _ := f.svc.GetObservation(testCtx, f.human, scopeA, p.ID)
	if _, err := f.svc.ReviewObservation(testCtx, f.human, p.ID, ObservationReviewInput{Scope: scopeA, ExpectedRevision: cur.Revision, Action: "reject"}); err != nil {
		t.Fatal(err)
	}
	if recalledObservation(t, f, f.agent, "staging flag") != nil {
		t.Fatal("a rejected observation must not be recalled")
	}
}

func TestObservationStalenessAndRefreshHistory(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID, ms[2].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatal(err)
	}
	p := out.Proposed[0]
	if _, err := f.svc.ReviewObservation(testCtx, f.human, p.ID, ObservationReviewInput{Scope: scopeA, ExpectedRevision: p.Revision, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.Supersede(testCtx, f.human, ms[0].ID, SupersedeInput{Scope: scopeA, ExpectedRevision: ms[0].Revision, Reason: "flag renamed",
		NewContent: "deploy to staging requires the stage flag in the deploy script", Sources: ms[0].Sources})
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := f.svc.GetObservation(testCtx, f.human, scopeA, p.ID)
	if stale.Status != obsStale || stale.StaleMark == nil || stale.StaleMark.Memory.ID != ms[0].ID || stale.StaleMark.ReplacedBy.ID != next.New.ID {
		t.Fatalf("supersede must stale the observation: %+v", stale)
	}
	if recalledObservation(t, f, f.agent, "staging flag") != nil {
		t.Fatal("a stale observation must not be recalled")
	}
	llm.answers = append(llm.answers, obsAnswer(t, obsDraft("Staging deploys need the stage flag.", ms[1].ID, ms[2].ID, next.New.ID)))
	out, err = f.consolidate(f.human, false)
	if err != nil || len(out.Refreshed) != 1 || out.Refreshed[0].ID != p.ID || out.Refreshed[0].Status != obsProposed {
		t.Fatalf("%+v %v", out, err)
	}
	if !strings.Contains(llm.prompts[1], "PREVIOUS") || !strings.Contains(llm.prompts[1], next.New.ID) || strings.Contains(llm.prompts[1], "id: "+ms[0].ID) {
		t.Fatal("the refresh prompt carries the previous text and the replacement, not the replaced memory")
	}
	diff := out.Refreshed[0].Diff
	if !slices.Contains(diff, DigestDiffLine{Op: "-", Text: "Staging deploys need the staging flag."}) || !slices.Contains(diff, DigestDiffLine{Op: "+", Text: "Staging deploys need the stage flag."}) {
		t.Fatalf("refresh diff against the approved text: %+v", diff)
	}
	refreshed := out.Refreshed[0]
	if _, err := f.svc.ReviewObservation(testCtx, f.human, p.ID, ObservationReviewInput{Scope: scopeA, ExpectedRevision: refreshed.Revision, Action: "edit", Content: "Staging deploys need the stage flag set."}); err != nil {
		t.Fatal(err)
	}
	hist, err := f.svc.ObservationHistory(testCtx, f.human, scopeA, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, r := range hist.Revisions {
		actions = append(actions, r.Action)
	}
	if !slices.Equal(actions, []string{"propose", "approve", "stale", "refresh", "edit"}) || hist.Revisions[4].Version != 5 {
		t.Fatalf("history %v", actions)
	}
	last := hist.Revisions[4].Diff
	if !slices.Contains(last, DigestDiffLine{Op: "+", Text: "Staging deploys need the stage flag set."}) || !slices.Contains(last, DigestDiffLine{Op: "-", Text: "Staging deploys need the stage flag."}) {
		t.Fatalf("edit diff %+v", last)
	}
	// A source rejected later is caught by the next run's sweep.
	cur := f.repo.rows(colMemories)
	m2 := cur[slices.IndexFunc(cur, func(r Record) bool { return r.ID == ms[1].ID })]
	f.transition(f.human, colMemories, m2.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m2.Revision, Status: "rejected"})
	// The refresh (and the new group of the remaining and replacement memories) get unusable answers.
	llm.answers = append(llm.answers, "not json", "not json")
	out, err = f.consolidate(f.human, false)
	if err != nil || out.StaleMarked != 1 || len(out.Rejected) != 2 || out.Rejected[0].Code != "invalid_model_output" || out.Calls != 2 {
		t.Fatalf("%+v %v", out, err)
	}
	if v, _ := f.svc.GetObservation(testCtx, f.human, scopeA, p.ID); v.Status != obsStale {
		t.Fatalf("status %s", v.Status)
	}
}

func TestObservationAndMentalModelDryRunPersistNothing(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	out, err := f.consolidate(f.human, true)
	if err != nil || len(out.Proposed) != 1 || !out.DryRun {
		t.Fatalf("%+v %v", out, err)
	}
	if rows := f.repo.rows(colObservations); len(rows) != 0 {
		t.Fatalf("dry run stored %d observations", len(rows))
	}
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshManual}})
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Staging deploys need the staging flag.", Citations: []string{ms[0].ID}})
	llm.answers = append(llm.answers, string(answer))
	v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA, DryRun: true})
	if err != nil || !v.DryRun || v.Draft == nil || v.Draft.Text != "Staging deploys need the staging flag." {
		t.Fatalf("%+v %v", v, err)
	}
	after, _ := f.svc.GetMentalModel(testCtx, f.human, scopeA, mm.ID)
	if after.Revision != mm.Revision || after.Draft != nil || after.LastRefreshAt != nil {
		t.Fatalf("dry run changed the mental model: %+v", after)
	}
}

func TestMentalModelRefreshReviewAndHistory(t *testing.T) {
	f, llm, ms := obsFixture(t)
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshDaily}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := f.svc.CreateMentalModel(testCtx, f.agent, MentalModelInput{Scope: scopeA, Title: "x", Query: "y", Refresh: MentalModelRefresh{Mode: mmRefreshManual}}); err != nil || p.Status != obsProposed {
		t.Fatalf("agent create must be a proposal: %+v %v", p, err)
	}
	if _, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "x", Query: "<private>y</private>", Refresh: MentalModelRefresh{Mode: mmRefreshManual}}); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("private query: %v", err)
	}
	outside, _ := json.Marshal(mentalModelAnswer{Summary: "Invented.", Citations: []string{"mem_not_recalled"}})
	first, _ := json.Marshal(mentalModelAnswer{Summary: "Staging needs the flag.", Citations: []string{ms[0].ID, ms[1].ID}})
	second, _ := json.Marshal(mentalModelAnswer{Summary: "Staging needs the flag.\nEvery deploy script sets it.", Citations: []string{ms[2].ID}})
	llm.answers = []string{string(outside), string(first), string(second)}
	if _, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA}); !errors.Is(err, errModelAnswer) {
		t.Fatalf("a citation outside the recall must be refused: %v", err)
	}
	if _, err := f.svc.RefreshMentalModel(testCtx, f.agent, mm.ID, MentalModelRefreshInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent refresh: %v", err)
	}
	approve := func(want int) MentalModelView {
		t.Helper()
		v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
		if err != nil || v.Draft == nil {
			t.Fatalf("%+v %v", v, err)
		}
		if _, err := f.svc.ReviewMentalModel(testCtx, f.agent, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewApprove}); !errors.Is(err, ErrDenied) {
			t.Fatalf("agent approve: %v", err)
		}
		v, err = f.svc.ReviewMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewApprove})
		if err != nil || v.Active == nil || v.Draft != nil || v.Active.Version != want {
			t.Fatalf("%+v %v", v, err)
		}
		return v
	}
	approve(2)
	v := approve(5)
	if v.Active.Text != "Staging needs the flag.\nEvery deploy script sets it." {
		t.Fatal(v.Active.Text)
	}
	hist, err := f.svc.MentalModelHistory(testCtx, f.human, scopeA, mm.ID)
	if err != nil {
		t.Fatal(err)
	}
	statuses := []string{}
	for _, r := range hist.Revisions {
		statuses = append(statuses, r.Status)
	}
	if !slices.Equal(statuses, []string{mmDraft, statusActive, mmDraft, mmSuperseded, statusActive}) {
		t.Fatalf("history %v", statuses)
	}
	if !slices.Contains(hist.Revisions[4].Diff, DigestDiffLine{Op: "+", Text: "Every deploy script sets it."}) {
		t.Fatalf("diff %+v", hist.Revisions[4].Diff)
	}
	// Deleting a cited memory clears the summary text that came from it.
	m := f.repo.rows(colMemories)[slices.IndexFunc(f.repo.rows(colMemories), func(r Record) bool { return r.ID == ms[2].ID })]
	if err := f.svc.Delete(testCtx, f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}); err != nil {
		t.Fatal(err)
	}
	after, _ := f.svc.GetMentalModel(testCtx, f.human, scopeA, mm.ID)
	if after.Active != nil {
		t.Fatalf("the active summary cited the erased memory: %+v", after.Active)
	}
	if err := f.svc.DeleteMentalModel(testCtx, f.human, scopeA, mm.ID, after.Revision); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) editRecord(col, id string, change func(*Record)) Record {
	f.t.Helper()
	r, err := f.repo.Get(testCtx, col, scopeA, id)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&r)
	r.Revision++
	if err := f.repo.Replace(testCtx, col, r, r.Revision-1); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestObservationTransferGateIsPerMemory(t *testing.T) {
	f, llm := wikiFixture(t)
	text := "deploy to staging requires the staging flag in the deploy script"
	ms := []Record{f.activeMemory(text), f.activeMemory(text + " always"), f.activeMemory(text + " too"), f.activeMemory(text + " again")}
	own := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "a restricted message"}))
	cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text + " still", Sources: refs(own)}))
	cand = f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "approved"})
	ms = append(ms, f.transition(f.human, "memories", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "active"}))
	f.editRecord(colMemories, ms[2].ID, func(r *Record) { r.Sensitivity = "restricted" })
	f.editRecord(colMemories, ms[3].ID, func(r *Record) { r.Sensitivity = "" })
	f.editRecord("sources", own.ID, func(r *Record) { r.Sensitivity = "restricted" })
	all := []Record{}
	for _, m := range ms {
		cur, _ := f.repo.Get(testCtx, colMemories, scopeA, m.ID)
		all = append(all, cur)
	}
	bare := all[0]
	bare.ID, bare.Sources = "mem-no-sources", nil // skipped like a denial, not a failed run
	kept, err := f.svc.transferableMemories(testCtx, f.human, scopeA, append(all, bare))
	if err != nil || len(kept) != 2 || kept[0].ID != ms[0].ID || kept[1].ID != ms[1].ID {
		t.Fatalf("restricted, unclassified and restricted-source memories must not pass: %v %v", refIDs(memRefs(kept...)), err)
	}
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 || len(llm.prompts) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, m := range ms[2:] {
		if strings.Contains(llm.prompts[0], m.ID) {
			t.Fatalf("memory %s reached the model", m.ID)
		}
	}
	if got := f.repo.store[colObservations][recKey(scopeA, out.Proposed[0].ID)].Sensitivity; got != ms[0].Sensitivity || got == "" {
		t.Fatalf("observation sensitivity %q", got)
	}
	// A mental-model refresh recalls for the model, so the same gate filters its recall.
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshManual}})
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Staging needs the flag.", Citations: []string{ms[0].ID}})
	llm.answers = append(llm.answers, string(answer))
	if _, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[2:] {
		if strings.Contains(llm.prompts[1], m.ID) {
			t.Fatalf("memory %s reached the mental-model prompt", m.ID)
		}
	}
}

func TestObservationPromptDatesNotesOldestFirst(t *testing.T) {
	f, llm := wikiFixture(t)
	newer := f.activeMemory("the staging deploy port changed to 9090 in the deploy script")
	older := f.activeMemory("the staging deploy port is 8080 in the deploy script")
	jan, mar := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	f.editRecord(colMemories, older.ID, func(r *Record) { r.EffectiveAt = &jan })
	f.editRecord(colMemories, newer.ID, func(r *Record) { r.EffectiveAt = &mar })
	llm.answers = []string{obsAnswer(t)}
	if _, err := f.consolidate(f.human, false); err != nil || len(llm.prompts) != 1 {
		t.Fatal(err, len(llm.prompts))
	}
	p := llm.prompts[0]
	i, j := strings.Index(p, "id: "+older.ID+"\n  date: 2026-01-05"), strings.Index(p, "id: "+newer.ID+"\n  date: 2026-03-09")
	if i < 0 || j < 0 || i > j || !strings.Contains(p, "changed or ended") {
		t.Fatalf("notes must carry server dates, oldest first, with the change rule:\n%s", p)
	}
}

func TestSourceErasureClearsMentalModelText(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID, ms[2].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatal(out, err)
	}
	obs, err := f.svc.ReviewObservation(testCtx, f.human, out.Proposed[0].ID, ObservationReviewInput{Scope: scopeA, ExpectedRevision: out.Proposed[0].Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	approved := func(title string, cites ...string) MentalModelView {
		t.Helper()
		mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: title, Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshManual}})
		if err != nil {
			t.Fatal(err)
		}
		answer, _ := json.Marshal(mentalModelAnswer{Summary: title + " needs the flag.", Citations: cites})
		llm.answers = append(llm.answers, string(answer))
		v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
		if err != nil {
			t.Fatal(err)
		}
		if v, err = f.svc.ReviewMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewApprove}); err != nil || v.Active == nil {
			t.Fatal(v, err)
		}
		return v
	}
	viaObservation, viaMemory := approved("Observed", obs.ID), approved("Direct", ms[0].ID)
	// Quarantine is not erasure: the text is withheld from readers at once.
	f.editRecord(colMemories, ms[0].ID, func(r *Record) { r.PrivacyQuarantined = true })
	if v, _ := f.svc.GetMentalModel(testCtx, f.human, scopeA, viaMemory.ID); v.Active == nil || v.Active.Text != "" || !v.Active.Withheld {
		t.Fatalf("quarantined citation: %+v", v.Active)
	}
	f.editRecord(colMemories, ms[0].ID, func(r *Record) { r.PrivacyQuarantined = false })
	src := f.repo.store["sources"][recKey(scopeA, ms[0].Sources[0].SourceID)]
	if err := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); err != nil {
		t.Fatal(err)
	}
	token := erasureWorker(f)
	f.auth.actors[token] = actor("eraser-1", "service", true, "erase", "embed", "read")
	for i := 0; i < 50; i++ {
		if err := f.svc.WorkOnce(testCtx, token); errors.Is(err, ErrNotFound) {
			break
		}
	}
	if _, err := f.repo.Get(testCtx, colObservations, scopeA, obs.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the observation survived the erasure of its source", err)
	}
	for _, id := range []string{viaObservation.ID, viaMemory.ID} {
		m := f.repo.store[colMentalModels][recKey(scopeA, id)].MentalModel
		if m.Active != nil {
			t.Fatalf("mental model %s kept text citing erased data: %+v", id, m.Active)
		}
		for _, rev := range m.History {
			if rev.Text != "" {
				t.Fatalf("mental model %s history kept text: %+v", id, rev)
			}
		}
	}
}

// The search SQL reads doc fields as stored: approved_by as a top-level string,
// content_erased dropped when false (COALESCE), never a typed date cast.
func TestObservationSearchSQLMatchesStoredDoc(t *testing.T) {
	now := time.Now().UTC()
	raw, err := storeMarshal(Record{ID: "ob_1", Scope: scopeA, Status: obsActive, ApprovedBy: "human-1", Text: "x", UpdatedAt: now, Observation: &ObservationData{Durability: "lasting"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["approved_by"] != "human-1" || doc["status"] != obsActive {
		t.Fatalf("stored doc %v", doc)
	}
	if _, ok := doc["content_erased"]; ok {
		t.Fatal("content_erased is stored when false; the SQL relies on COALESCE for its absence")
	}
	w, _ := pgSearchWhere(SearchCriteria{}, colObservations, now, "", nil)
	for _, want := range []string{"COALESCE((doc->>'content_erased')::boolean,false)=false", "NULLIF(doc->>'approved_by','') IS NOT NULL", "(doc->'effective_at'->>'$date')::timestamptz"} {
		if !strings.Contains(w, want) {
			t.Fatalf("missing %q in %s", want, w)
		}
	}
	if strings.Contains(w, "(doc->>'effective_at')::timestamptz") || strings.Contains(w, "(doc->>'updated_at')::timestamptz") {
		t.Fatal("a stored date is an object; it must be read through $date")
	}
}

func TestNativeServingKeepsBaselineWithObservations(t *testing.T) {
	a := &nativeSearchAttempt{runtime: &NativeSearchShadowRuntime{config: NativeSearchShadowConfig{Mode: "on"}}, served: &nativeBridgeResult{}}
	a.record.Status = "selected"
	base := ContextBundle{Items: []Hit{{ID: "m1", Collection: colMemories}, {ID: "ob_1", Collection: colObservations}}}
	if got := a.serve(base, map[Ref]string{}); len(got.Items) != 2 || a.serving {
		t.Fatalf("served %+v", got)
	}
}

func TestRecallPreferObservationsDropsTheirSourceMemories(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatal(err)
	}
	p := out.Proposed[0]
	if _, err := f.svc.ReviewObservation(testCtx, f.human, p.ID, ObservationReviewInput{Scope: scopeA, ExpectedRevision: p.Revision, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	search := func(prefer bool) []string {
		res, err := f.svc.Search(testCtx, f.agent, SearchInput{Scope: scopeA, Query: "staging flag", Limit: 20, PreferObservations: &prefer})
		if err != nil {
			t.Fatal(err)
		}
		return ids(res.Items)
	}
	all, preferred := search(false), search(true)
	if !slices.Contains(all, ms[0].ID) || !slices.Contains(all, p.ID) {
		t.Fatal(all)
	}
	// The observation stays, the two memories it was written from go, the uncited one stays.
	if !slices.Contains(preferred, p.ID) || slices.Contains(preferred, ms[0].ID) || slices.Contains(preferred, ms[1].ID) || !slices.Contains(preferred, ms[2].ID) {
		t.Fatal(preferred)
	}
}
