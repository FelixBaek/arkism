//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestAutoDeriveOffWithoutModelOrWhenSwitchedOff(t *testing.T) {
	f := newFixture(t)
	_ = approveCandidate(t, f, "no server model")
	if n := len(autoDeriveJobs(f)); n != 0 {
		t.Fatalf("queued without a model: %d", n)
	}
	t.Setenv("MEMORY_AUTO_DERIVE", "off")
	g, _ := wikiFixture(t)
	_ = approveCandidate(t, g, "switched off")
	if n := len(autoDeriveJobs(g)); n != 0 {
		t.Fatalf("queued while off: %d", n)
	}
}

func TestAutoDeriveWorkerRunsOnePassAndStopsOnBudget(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE_THRESHOLD", "1")
	f, llm, ms := obsFixture(t)
	f.embedAll()
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
	// A later approval starts a fresh count on the finished job.
	f.svc.noteApprovedMemory(testCtx, scopeA)
	if job = autoDeriveJobs(f)[0]; job.Status != JobStatusPending || job.AutoDerive.Pass != 0 || job.AutoDerive.Approved != 1 {
		t.Fatalf("requeue: %+v %+v", job, job.AutoDerive)
	}
	f.svc.Wiki = nil
	if outcome, err := f.svc.autoDerivePass(testCtx, f.work, scopeA, 0); err != nil || outcome != "model_not_configured" {
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
	if err != nil {
		t.Fatal(err)
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
