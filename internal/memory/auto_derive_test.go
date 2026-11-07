//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func autoDeriveJobs(f *fixture) []Record {
	out := []Record{}
	for _, r := range f.repo.rows(colJobs) {
		if r.Kind == autoDeriveJobKind {
			out = append(out, r)
		}
	}
	return out
}

func TestAutoDeriveThresholdQueuesOneJobPerScope(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	t.Setenv("MEMORY_AUTO_DERIVE_THRESHOLD", "2")
	f, _ := wikiFixture(t)
	_ = approveCandidate(t, f, "deploys need the staging flag")
	jobs := autoDeriveJobs(f)
	if len(jobs) != 1 || jobs[0].Status != JobStatusPending || jobs[0].AutoDerive.Approved != 1 || jobs[0].RunAt == nil || time.Until(*jobs[0].RunAt) < 5*time.Hour {
		t.Fatalf("first approval waits for the interval: %+v", jobs)
	}
	_ = approveCandidate(t, f, "deploys run from the main branch")
	jobs = autoDeriveJobs(f)
	if len(jobs) != 1 || jobs[0].AutoDerive.Approved != 2 || jobs[0].RunAt.After(time.Now()) {
		t.Fatalf("threshold reached: one job, due now: %+v", jobs)
	}
}

func TestAutoDeriveOffByDefaultAndWithoutModel(t *testing.T) {
	g, _ := wikiFixture(t)
	_ = approveCandidate(t, g, "an upgrade does not start model runs")
	if n := len(autoDeriveJobs(g)); n != 0 {
		t.Fatalf("queued without the opt-in: %d", n)
	}
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	f := newFixture(t)
	_ = approveCandidate(t, f, "no server model")
	if n := len(autoDeriveJobs(f)); n != 0 {
		t.Fatalf("queued without a model: %d", n)
	}
}

// Every path that makes a memory active counts once through the one hook: a review,
// a person's approved -> active, a restore; a replayed review counts nothing.
func TestAutoDeriveCountsEveryApprovalPathOnce(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	f, _ := wikiFixture(t)
	approved := func() int {
		jobs := autoDeriveJobs(f)
		if len(jobs) != 1 {
			t.Fatalf("jobs: %+v", jobs)
		}
		return jobs[0].AutoDerive.Approved
	}
	m := f.activeMemory("deploys need the staging flag")
	if n := approved(); n != 1 {
		t.Fatalf("candidate -> approved -> active counted %d times", n)
	}
	c := f.create(f.agent, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "deploys run from main", Generator: &Generator{Model: "test-model", PromptVersion: "p1", PipelineVersion: "v1"}, Sources: refs(f.source())}))
	review := CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve", OperationID: "op-auto-derive-1"}
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, review); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, review); err != nil {
		t.Fatal("replay", err)
	}
	if n := approved(); n != 2 {
		t.Fatalf("review plus replay counted to %d", n)
	}
	gone := f.transition(f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision, Status: "rejected", ReasonCode: "wrong"})
	if _, err := f.svc.RestoreMemory(testCtx, f.human, m.ID, MemoryRestoreInput{Scope: scopeA, ExpectedRevision: gone.Revision}); err != nil {
		t.Fatal(err)
	}
	if n := approved(); n != 3 {
		t.Fatalf("restore not counted: %d", n)
	}
}

// An approval that lands while the job holds its lease is not lost: the run queues
// the next count when it ends.
func TestAutoDeriveRequeuesApprovalsDuringARun(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	f, _ := wikiFixture(t)
	_ = f.activeMemory("first approval")
	var job Record
	for job.Kind != autoDeriveJobKind { // embedding jobs come first; they stay leased
		var err error
		if job, err = f.repo.ClaimJob(testCtx, time.Now().UTC(), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	// The run's first pass began a moment ago and consumed the count so far.
	started := time.Now().UTC().Add(-time.Second)
	job.AutoDerive.Started, job.AutoDerive.Pass, job.AutoDerive.Approved = &started, 1, 0
	_ = f.activeMemory("approved while the run holds the lease")
	if cur := autoDeriveJobs(f)[0]; cur.Status != JobStatusRunning || cur.Revision != job.Revision {
		t.Fatalf("the running job was touched: %+v", cur)
	}
	f.svc.Wiki = nil // the pass ends at once ("model_not_configured"), so this is the run's end
	if err := f.svc.runAutoDerive(testCtx, f.work, job); err != nil {
		t.Fatal(err)
	}
	cur := autoDeriveJobs(f)[0]
	if cur.Status != JobStatusPending || cur.AutoDerive.Pass != 0 || cur.AutoDerive.Approved != 1 || cur.AutoDerive.Started != nil || cur.RunAt == nil || time.Until(*cur.RunAt) < 5*time.Hour {
		t.Fatalf("late approval not queued: %+v %+v", cur, cur.AutoDerive)
	}
}

// Two workers never hold the same scope job; two first approvals still count twice.
func TestAutoDeriveOneJobUnderConcurrency(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	f, _ := wikiFixture(t)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); f.svc.noteApprovedMemory(testCtx, scopeA) }()
	}
	wg.Wait()
	if jobs := autoDeriveJobs(f); len(jobs) != 1 || jobs[0].AutoDerive.Approved != 2 {
		t.Fatalf("%+v", jobs)
	}
	f.repo.mu.Lock()
	for k, j := range f.repo.store["jobs"] {
		j.RunAt = nil
		f.repo.store["jobs"][k] = j
	}
	f.repo.mu.Unlock()
	claims := make(chan error, 2)
	for range 2 {
		go func() { _, err := f.repo.ClaimJob(testCtx, time.Now().UTC(), time.Minute); claims <- err }()
	}
	won := 0
	for range 2 {
		if err := <-claims; err == nil {
			won++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("%d workers claimed the scope job", won)
	}
}

func TestAutoDeriveWorkerRunsOnePassAndStopsOnBudget(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE_THRESHOLD", "1")
	f, llm, ms := obsFixture(t)
	f.embedAll()
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	w := f.auth.actors[f.work] // the worker principal holds the grants a person would give it
	w.verbs["context"] = true
	f.auth.actors[f.work] = w
	f.svc.noteApprovedMemory(testCtx, scopeA)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	job := autoDeriveJobs(f)[0]
	if len(llm.prompts) != 1 || job.Status != JobStatusPending || job.AutoDerive.Pass != 1 || job.AutoDerive.Outcome != "ok" || job.LeaseToken != "" {
		t.Fatalf("observation pass: prompts=%d job=%+v %+v", len(llm.prompts), job, job.AutoDerive)
	}
	proposed := f.repo.rows(colObservations)
	if len(proposed) != 1 || proposed[0].Status != obsProposed {
		t.Fatalf("the pass only proposes: %+v", proposed)
	}
	for range wikiDailyCallLimit {
		r, err := f.svc.beginProviderCall(testCtx, scopeA, "wiki_write", wikiProvider, "fake-wiki")
		if err != nil {
			t.Fatal(err)
		}
		_ = f.svc.finishProviderCall(testCtx, r, ProviderUsage{Provider: wikiProvider, Model: "fake-wiki", Requests: 1}, true)
	}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	job = autoDeriveJobs(f)[0]
	if len(llm.prompts) != 1 || job.Status != JobStatusDone || job.AutoDerive.Outcome != "model_budget_exceeded" {
		t.Fatalf("budget stop: prompts=%d %+v %+v", len(llm.prompts), job, job.AutoDerive)
	}
	if v := viewOperation(job); v.Outcome != "model_budget_exceeded" || v.LastRun == nil {
		t.Fatalf("the operations view hides the last pass: %+v", v)
	}
	// A later approval starts a fresh count on the finished job.
	f.svc.noteApprovedMemory(testCtx, scopeA)
	if job = autoDeriveJobs(f)[0]; job.Status != JobStatusPending || job.AutoDerive.Pass != 0 || job.AutoDerive.Approved != 1 {
		t.Fatalf("requeue: %+v %+v", job, job.AutoDerive)
	}
	f.svc.Wiki = nil
	if outcome, err := f.svc.autoDerivePass(testCtx, f.work, scopeA, 0, nil); err != nil || outcome != "model_not_configured" {
		t.Fatal(outcome, err)
	}
}

func TestConsolidatePlanOnlyPreviewsWithoutModel(t *testing.T) {
	f, llm, ms := obsFixture(t)
	out, err := f.svc.ConsolidateObservations(testCtx, f.human, ObservationConsolidateInput{Scope: scopeA, PlanOnly: true})
	if err != nil || out.Plan == nil || !out.DryRun || len(llm.prompts) != 0 || len(out.Plan.Groups) != 1 || len(out.Plan.Groups[0]) != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, id := range out.Plan.Groups[0] {
		if id == ms[3].ID {
			t.Fatal("unrelated memory planned into the group")
		}
	}
	if rows := f.repo.rows(colObservations); len(rows) != 0 {
		t.Fatalf("plan stored %d observations", len(rows))
	}
}

func TestMentalModelRefreshDecodesOldAndStructuredForms(t *testing.T) {
	var in MentalModelInput
	if err := json.Unmarshal([]byte(`{"refresh":"daily"}`), &in); err != nil || in.Refresh.Mode != mmRefreshDaily {
		t.Fatal(in, err)
	}
	if err := json.Unmarshal([]byte(`{"refresh":{"mode":"manual","cron":"0 3 * * *","min_interval":"6h","kinds":["decision"],"max_tokens":4000}}`), &in); err != nil || in.Refresh.Cron != "0 3 * * *" || !validMentalModelRefresh(in.Refresh) {
		t.Fatal(in, err)
	}
	if err := json.Unmarshal([]byte(`{"refresh":{"mode":"manual","when":"x"}}`), &in); err == nil {
		t.Fatal("unknown refresh field accepted")
	}
	var stored Record
	if err := storeUnmarshal([]byte(`{"mental_model":{"title":"t","query":"q","refresh":"on_change"}}`), &stored); err != nil || stored.MentalModel.Refresh.Mode != mmRefreshChange {
		t.Fatal("stored string form", stored.MentalModel, err)
	}
	b, err := storeMarshal(stored)
	if err != nil || !strings.Contains(string(b), `"refresh":{"mode":"on_change"`) {
		t.Fatal("written back in the structured form", string(b), err)
	}
	var back Record
	if err := storeUnmarshal(b, &back); err != nil || back.MentalModel.Refresh.Mode != mmRefreshChange {
		t.Fatal("round trip", err)
	}
	for _, bad := range []MentalModelRefresh{{Mode: "hourly"}, {Mode: "manual", Cron: "61 * * * *"}, {Mode: "manual", MinInterval: "-1h"}, {Mode: "manual", MinInterval: "800h"}, {Mode: "manual", MaxTokens: 10}} {
		if validMentalModelRefresh(bad) {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestMentalModelDueTriggers(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	last := now.Add(-3 * time.Hour)
	newer, older := now.Add(-time.Hour), now.Add(-5*time.Hour)
	due := func(r MentalModelRefresh, latest, latestObs time.Time) bool {
		return mentalModelDue(&MentalModelData{Refresh: r, LastRefreshAt: &last}, latest, latestObs, now)
	}
	cases := []struct {
		name              string
		r                 MentalModelRefresh
		latest, latestObs time.Time
		want              bool
	}{
		{"cron tick with new memories", MentalModelRefresh{Mode: mmRefreshManual, Cron: "0 11 * * *"}, newer, older, true},
		{"cron tick, nothing new", MentalModelRefresh{Mode: mmRefreshManual, Cron: "0 11 * * *"}, older, older, false},
		{"cron not ticked", MentalModelRefresh{Mode: mmRefreshManual, Cron: "0 3 * * *"}, newer, older, false},
		{"after consolidation", MentalModelRefresh{Mode: mmRefreshManual, AfterConsolidation: true}, older, newer, true},
		{"min interval holds", MentalModelRefresh{Mode: mmRefreshChange, MinInterval: "4h"}, newer, older, false},
		{"on change", MentalModelRefresh{Mode: mmRefreshChange}, newer, older, true},
		{"manual", MentalModelRefresh{Mode: mmRefreshManual}, newer, newer, false},
	}
	for _, c := range cases {
		if got := due(c.r, c.latest, c.latestObs); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if !mentalModelDue(&MentalModelData{Refresh: MentalModelRefresh{Mode: mmRefreshManual, Cron: "0 3 * * *"}}, older, older, now) {
		t.Error("a never refreshed scheduled model is due")
	}
}

func TestMentalModelStaleMarks(t *testing.T) {
	f, _, ms := obsFixture(t)
	hourAgo, inHour := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	fresh := Record{Scope: scopeA, MentalModel: &MentalModelData{LastRefreshAt: &inHour, Active: &DerivedRevision{Citations: []Ref{{Collection: colMemories, ID: ms[0].ID}}}}}
	if stale, why := f.svc.mentalModelStale(testCtx, fresh); stale {
		t.Fatal("fresh model marked stale", why)
	}
	gone := Record{Scope: scopeA, MentalModel: &MentalModelData{LastRefreshAt: &inHour, Active: &DerivedRevision{Citations: []Ref{{Collection: colMemories, ID: "missing"}}}}}
	if stale, why := f.svc.mentalModelStale(testCtx, gone); !stale || why != "sources_changed" {
		t.Fatal(stale, why)
	}
	older := Record{Scope: scopeA, MentalModel: &MentalModelData{LastRefreshAt: &hourAgo}}
	if stale, why := f.svc.mentalModelStale(testCtx, older); !stale || why != "new_memories" {
		t.Fatal(stale, why)
	}
}

func TestEntitySummaryDraftNeedsHumanApproval(t *testing.T) {
	f, repo := entityFixture(t)
	for _, token := range []string{f.human, f.agent} {
		a := f.auth.actors[token]
		a.verbs["generate"] = true
		f.auth.actors[token] = a
	}
	llm := &fakeWikiLLM{}
	f.svc.Wiki = llm
	m := f.activeMemory("mina owns the harbor deploy pipeline")
	repo.ents["e1"] = NamedEntity{ID: "e1", Name: "mina", Type: "person", Aliases: []string{}}
	if _, err := f.svc.DraftEntitySummary(testCtx, f.agent, "e1", EntitySummaryInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent drafted a summary", err)
	}
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Mina owns the harbor deploy pipeline.", Citations: []string{m.ID}})
	llm.answers = []string{string(answer)}
	e, err := f.svc.DraftEntitySummary(testCtx, f.human, "e1", EntitySummaryInput{Scope: scopeA})
	if err != nil || e.SummaryDraft == nil || e.Summary != nil || repo.ents["e1"].SummaryDraft == nil {
		t.Fatalf("%+v %v", e, err)
	}
	if !strings.Contains(llm.prompts[0], "mina owns the harbor") {
		t.Fatal("prompt lacks the recalled memory")
	}
	if _, err := f.svc.PatchEntity(testCtx, f.agent, "e1", EntityPatchInput{Scope: scopeA, SummaryReview: "approve"}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent approved", err)
	}
	e, err = f.svc.PatchEntity(testCtx, f.human, "e1", EntityPatchInput{Scope: scopeA, SummaryReview: "approve"})
	if err != nil || e.Summary == nil || e.Summary.Status != statusActive || e.SummaryDraft != nil {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := f.svc.PatchEntity(testCtx, f.human, "e1", EntityPatchInput{Scope: scopeA, SummaryReview: "approve"}); !errors.Is(err, ErrConflict) {
		t.Fatal("approved without a draft", err)
	}
	if _, err := f.svc.PatchEntity(testCtx, f.human, "e1", EntityPatchInput{Scope: scopeA, SummaryReview: "publish"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown review action", err)
	}
}

// A service may ask for a draft but never approve it; one draft waits at a time; an
// approval whose citations no longer stand is refused.
func TestEntitySummaryServiceDraftsOnceAndCannotPublish(t *testing.T) {
	f, repo := entityFixture(t)
	w := f.auth.actors[f.work]
	for _, verb := range []string{"write", "search", "context", "generate"} {
		w.verbs[verb] = true
	}
	f.auth.actors[f.work] = w
	llm := &fakeWikiLLM{}
	f.svc.Wiki = llm
	m := f.activeMemory("mina owns the harbor deploy pipeline")
	repo.ents["e1"] = NamedEntity{ID: "e1", Name: "mina", Type: "person", Aliases: []string{}}
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Mina owns the harbor deploy pipeline.", Citations: []string{m.ID}})
	llm.answers = []string{string(answer), string(answer)}
	if _, err := f.svc.DraftEntitySummary(testCtx, f.work, "e1", EntitySummaryInput{Scope: scopeA}); err != nil {
		t.Fatal("service draft", err)
	}
	if _, err := f.svc.DraftEntitySummary(testCtx, f.work, "e1", EntitySummaryInput{Scope: scopeA}); !errors.Is(err, ErrConflict) || len(llm.prompts) != 1 {
		t.Fatal("second draft while one waits", err, len(llm.prompts))
	}
	if _, err := f.svc.PatchEntity(testCtx, f.work, "e1", EntityPatchInput{Scope: scopeA, SummaryReview: "approve"}); !errors.Is(err, ErrDenied) {
		t.Fatal("service approved", err)
	}
	f.transition(f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision, Status: "rejected", ReasonCode: "wrong"})
	if _, err := f.svc.PatchEntity(testCtx, f.human, "e1", EntityPatchInput{Scope: scopeA, SummaryReview: "approve"}); !errors.Is(err, ErrConflict) {
		t.Fatal("approved over a withdrawn source", err)
	}
	if e, err := f.svc.PatchEntity(testCtx, f.human, "e1", EntityPatchInput{Scope: scopeA, SummaryReview: "reject"}); err != nil || e.SummaryDraft != nil || e.Summary != nil {
		t.Fatal("reject", e, err)
	}
}

// A space's auto_derive block wins over the server environment, field by field.
func TestAutoDerivePerSpaceSetting(t *testing.T) {
	f, _ := wikiFixture(t)
	on, off := true, false
	putSettings(t, f, scopeA, SpaceConfigInput{AutoDerive: &SpaceAutoDerive{Enabled: &on, Threshold: 1}})
	view, err := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if err != nil || view.Sources["auto_derive.enabled"] != spaceSourceSpace || view.Sources["auto_derive.threshold"] != spaceSourceSpace ||
		view.Sources["auto_derive.interval"] != spaceSourceServer || view.Config.AutoDerive.Interval != "6h0m0s" {
		t.Fatalf("effective = %+v %v %v", view.Config.AutoDerive, view.Sources, err)
	}
	_ = approveCandidate(t, f, "the space opted in although the server is off")
	if jobs := autoDeriveJobs(f); len(jobs) != 1 || jobs[0].RunAt.After(time.Now()) {
		t.Fatalf("space opt-in with threshold 1 is due now: %+v", jobs)
	}

	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	g, _ := wikiFixture(t)
	putSettings(t, g, scopeA, SpaceConfigInput{AutoDerive: &SpaceAutoDerive{Enabled: &off}})
	_ = approveCandidate(t, g, "the space opted out although the server is on")
	if n := len(autoDeriveJobs(g)); n != 0 {
		t.Fatalf("space opt-out queued %d jobs", n)
	}
	for _, bad := range []SpaceAutoDerive{{Threshold: 1001}, {Threshold: -1}, {Interval: "30s"}, {Interval: "soon"}} {
		if _, err := g.svc.PutSpaceConfig(testCtx, g.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, AutoDerive: &bad}, false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
}

// Approving a fact or relation about a person queues a summary draft for them on the scope's
// auto_derive job; its summaries pass drafts it for a person to review.
func TestAutoDeriveDraftsSummariesOfApprovedPeople(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	f, repo := entityFixture(t)
	w := f.auth.actors[f.work]
	for _, verb := range []string{"write", "search", "context", "generate"} {
		w.verbs[verb] = true
	}
	f.auth.actors[f.work] = w
	llm := &fakeWikiLLM{}
	f.svc.Wiki = llm
	m := f.activeMemory("mina owns the harbor deploy pipeline")
	for _, e := range []NamedEntity{{ID: "e1", Name: "mina", Type: "person", Aliases: []string{}}, {ID: "e2", Name: "harbor", Type: "project", Aliases: []string{}}} {
		e.Keys = entityKeys(e)
		repo.ents[e.ID] = e
	}
	f.svc.queueEntitySummaries(testCtx, Record{ID: "x", Scope: scopeA, ApprovedBy: "human", Entities: []EntityInput{{Name: "harbor", Type: "project"}}})
	if got := autoDeriveJobs(f)[0].AutoDerive.Entities; len(got) != 0 {
		t.Fatalf("a project is not a person: %v", got)
	}
	approved := m
	approved.EntityRelations = []EntityRelationInput{{Source: "Mina", Relation: "works_on", Target: "harbor"}}
	f.repo.store[colMemories][recKey(scopeA, m.ID)] = approved
	f.svc.queueEntitySummaries(testCtx, approved)
	f.svc.queueEntitySummaries(testCtx, approved)
	var job Record
	for job.Kind != autoDeriveJobKind {
		var err error
		if job, err = f.repo.ClaimJob(testCtx, time.Now().UTC().Add(7*time.Hour), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if got := job.AutoDerive.Entities; len(got) != 1 || got[0] != "e1" {
		t.Fatalf("queued people: %v", got)
	}
	started := time.Now().UTC()
	job.AutoDerive.Pass, job.AutoDerive.Started = len(autoDerivePasses)-1, &started
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Mina owns the harbor deploy pipeline.", Citations: []string{m.ID}})
	llm.answers = []string{string(answer)}
	if err := f.svc.runAutoDerive(testCtx, f.work, job); err != nil {
		t.Fatal(err)
	}
	if repo.ents["e1"].SummaryDraft == nil || repo.ents["e1"].Summary != nil {
		t.Fatalf("draft only, never published: %+v", repo.ents["e1"])
	}
	if cur := autoDeriveJobs(f)[0]; len(cur.AutoDerive.Entities) != 0 || cur.AutoDerive.Outcome != "ok" {
		t.Fatalf("drafted people leave the queue: %+v", cur.AutoDerive)
	}
}
