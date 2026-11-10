package memory

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// dreamModelSeed holds what the generative and deciding stages work on: three paraphrases (one in
// Korean), three memories that together show a preference, a user-stated memory in an open conflict
// with an inferred one, a review entry that contradicts an older memory, one that contradicts a
// memory written at the same time, a procedural entry, and a lesson learned in three runs.
type dreamModelSeed struct {
	dreamSeed
	ko, en1, en2, other, vim, code, standup, lunch Record
	asks                                           []Record
	standupNew, lunchNew, rule                     Record
	relID                                          string
	calls                                          *atomic.Int64
}

const mergedKorean = "The user wants answers in Korean"

func newDreamModelSeed(t *testing.T, pg *Postgres) dreamModelSeed {
	base := newDreamSeed(t, pg)
	f := base.f
	f.auth.actors[base.worker] = actor("dream-worker", roleService, true, append(slices.Clone(allVerbs), "generate")...)
	s := dreamModelSeed{dreamSeed: base}
	s.ko = f.activeMemory("사용자는 답변을 한국어로 받기를 원한다")
	s.en1 = f.activeMemory("The user wants every answer in Korean")
	s.en2 = f.activeMemory("Replies to the user should be written in Korean")
	s.other = f.activeMemory("The nightly backup runs at 3am")
	for _, text := range []string{"Asked for a shorter summary of the report", "Asked to cut the release notes down to five lines", "Asked for the meeting notes as three bullets"} {
		s.asks = append(s.asks, f.activeMemory(text))
	}
	stated := func(r Record, kind string) Record {
		return dreamPatch(t, f, colMemories, r.ID, func(r *Record) {
			r.MemoryContext = &MemoryContext{Domains: []string{"work"}, EvidenceKind: kind, SemanticKind: "preference"}
		})
	}
	s.vim = stated(f.activeMemory("Felix edits code in Vim"), "user_statement")
	s.code = stated(f.activeMemory("Felix edits code in VS Code"), "inference")
	out, err := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "mark", Reason: "two editors",
		Memories: []Ref{{Collection: colMemories, ID: s.vim.ID, Revision: s.vim.Revision}, {Collection: colMemories, ID: s.code.ID, Revision: s.code.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	s.relID = out.Relation.ID
	earlier := time.Now().Add(-48 * time.Hour)
	s.standup = dreamPatch(t, f, colMemories, f.activeMemory("The standup is at 9am").ID, func(r *Record) { r.EffectiveAt = &earlier })
	s.lunch = f.activeMemory("Lunch is at noon")
	src := f.source()
	candidate := func(kind, text string, c CandidateClassification) Record {
		r := f.create(f.agent, "memory_candidates", CreateInput{Scope: scopeA, Kind: kind, Text: text, Sources: refs(src), Generator: &Generator{Model: "m", PromptVersion: "v1", PipelineVersion: "v1"}})
		return dreamPatch(t, f, "memory_candidates", r.ID, func(r *Record) { r.Classification = &c })
	}
	s.standupNew = candidate("semantic", "The standup moved to 10am", CandidateClassification{Risk: "low", Relation: "contradiction", DupHint: s.standup.ID})
	s.lunchNew = candidate("semantic", "Lunch is at 1pm", CandidateClassification{Risk: "low", Relation: "contradiction", DupHint: s.lunch.ID})
	s.rule = candidate("procedural", "Run the linter before every commit", CandidateClassification{Risk: "low", Verdict: &CandidateVerdict{Action: "keep"}})
	proposalFixture(t, f) // three runs that each learned "verify the result" on task type review
	s.calls = dreamModelServer(t, f, s.answer)
	return s
}

// dreamModelServer answers the dream's model calls with answer(system, user); a skill draft gets the draft fixture.
func dreamModelServer(t *testing.T, f *fixture, answer func(system, user string) string) *atomic.Int64 {
	calls := new(atomic.Int64)
	_, draft := draftFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) != 2 {
			t.Error("invalid model request")
			return
		}
		content := answer(req.Messages[0].Content, req.Messages[1].Content)
		if content == "" {
			content = structuredText(draft)
		}
		_ = json.NewEncoder(w).Encode(draftResponse(content))
	}))
	t.Cleanup(server.Close)
	f.svc.DreamModel = LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "dream-model"}
	f.svc.CostRates = CostRates{Revision: "test", SkillLLMInputTokenPicoUSD: 2, SkillLLMOutputTokenPicoUSD: 3}
	return calls
}

// idsOf returns the ids of the prompt lines whose text is one of texts.
func idsOf(user string, texts ...string) []string {
	out := []string{}
	for _, line := range strings.Split(user, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 && slices.Contains(texts, parts[2]) {
			out = append(out, parts[0])
		}
	}
	return out
}

// answer is a model that groups the three paraphrases (and proposes one wrong merge with a member
// already merged) and finds the preference the three requests show (and one with too little evidence).
func (s dreamModelSeed) answer(system, user string) string {
	switch {
	case strings.Contains(system, "tidy a person's long-term memory"):
		para := idsOf(user, s.ko.Text, s.en1.Text, s.en2.Text)
		wrong := append(idsOf(user, s.other.Text), para[:1]...)
		return structuredText(map[string]any{"groups": []dreamGroup{{IDs: para, Text: mergedKorean, Confidence: 0.92}, {IDs: wrong, Text: "backups and Korean", Confidence: 0.9}}})
	case strings.Contains(system, "read a person's long-term memories"):
		asks := idsOf(user, s.asks[0].Text, s.asks[1].Text, s.asks[2].Text)
		return structuredText(map[string]any{"insights": []dreamInsight{{Text: "The user prefers short summaries", Type: "preference", Evidence: asks, Confidence: 0.85}, {Text: "The user likes bullets", Type: "preference", Evidence: asks[:2], Confidence: 0.9}}})
	}
	return ""
}

func (s dreamModelSeed) changeOf(run DreamRun, op string) (DreamChange, int) {
	for i, c := range run.Changes {
		if c.Op == op {
			return c, i
		}
	}
	return DreamChange{}, -1
}

func (s dreamModelSeed) memoryWithText(t *testing.T, text string) Record {
	t.Helper()
	rows, err := s.f.svc.Repo.ListRecords(testCtx, scopeA, colMemories, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Text == text {
			return r
		}
	}
	t.Fatal("no memory", text)
	return Record{}
}

func TestDreamStagesDevelopMemoryAndUndo(t *testing.T) {
	s := newDreamModelSeed(t, nil)
	item := s.run(t)
	checkDreamStages(t, s, item)
	checkDreamStagesUndo(t, s, item)
}

func checkDreamStages(t *testing.T, s dreamModelSeed, item DreamRunItem) {
	t.Helper()
	run := item.Run
	for _, c := range run.Changes {
		t.Logf("change %-13s %-56s %.2f %s", c.Op, c.Target, c.Confidence, c.Reason)
	}
	for _, k := range run.Skipped {
		t.Logf("skip   %-56s %s", k.Target, k.Reason)
	}
	if run.Outcome != "ok" || run.Model != "dream-model" || run.ModelCalls != 3 || s.calls.Load() != 3 {
		t.Fatal("outcome", run.Outcome, run.Model, run.ModelCalls, s.calls.Load())
	}
	merged := s.memoryWithText(t, mergedKorean)
	if merged.Status != statusActive || !citesAll(merged, s.ko, s.en1, s.en2) || len(merged.Relations) != 0 {
		t.Fatal("merged memory", merged.Status, len(merged.Sources))
	}
	insight := s.memoryWithText(t, "The user prefers short summaries")
	if insight.Status != statusActive || insight.Kind != "user" || insight.MemoryContext == nil || insight.MemoryContext.EvidenceKind != "inference" || !citesAll(insight, s.asks...) {
		t.Fatal("insight", insight)
	}
	s.expect(t, "after", map[string]map[string]string{
		colMemories:         {s.ko.ID: "superseded", s.en1.ID: "superseded", s.en2.ID: "superseded", s.other.ID: "active", s.vim.ID: "active", s.code.ID: "superseded", s.standup.ID: "superseded", s.standupNew.ID: "active", s.lunch.ID: "active", s.rule.ID: "active"},
		"memory_candidates": {s.lunchNew.ID: "candidate"},
		"memory_relations":  {s.relID: "resolved"},
	})
	skipped := map[string]string{}
	for _, k := range run.Skipped {
		skipped[k.Target] = k.Reason
	}
	if skipped["memory_candidates/"+s.lunchNew.ID] != "contradiction_undecidable" || !slices.Contains(slices.Collect(maps.Values(skipped)), "insight_thin") || !slices.Contains(slices.Collect(maps.Values(skipped)), "merge_invalid") {
		t.Fatal("skips", skipped)
	}
	promo, _ := s.changeOf(run, "promote_skill")
	skill, err := s.f.svc.Repo.Get(testCtx, colSkills, scopeA, "review")
	if err != nil || promo.Target != "skills/review" || skill.ActiveVersion != promo.After {
		t.Fatal("promotion", promo, skill.ActiveVersion, err)
	}
	version, err := s.f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, skill.ActiveVersion)
	if err != nil || version.Status != statusActive || checkActiveSkill(testCtx, s.f.svc.Repo, version) != nil {
		t.Fatal("promoted version is not routable", version.Status, err)
	}
	// The skill search re-reads the version's lessons through its fence; they are still candidates.
	if err = s.f.svc.Repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return checkActiveSkill(ctx, searchReadTx{Tx: tx}, version)
	}); err != nil {
		t.Fatal("the skill search refuses the promoted version", err)
	}
}

// citesAll: r cites every source of each member.
func citesAll(r Record, members ...Record) bool {
	for _, m := range members {
		for _, src := range m.Sources {
			if !slices.ContainsFunc(r.Sources, func(x SourceRef) bool { return x.SourceID == src.SourceID }) {
				return false
			}
		}
	}
	return true
}

func checkDreamStagesUndo(t *testing.T, s dreamModelSeed, item DreamRunItem) {
	t.Helper()
	merged := s.memoryWithText(t, mergedKorean)
	insight := s.memoryWithText(t, "The user prefers short summaries")
	promo, _ := s.changeOf(item.Run, "promote_skill")
	out, err := s.f.svc.UndoDream(testCtx, s.f.human, item.ID, DreamUndoInput{Scope: scopeA})
	if err != nil || len(out.Conflicts) != 0 || len(out.Undone) != len(item.Run.Changes) {
		t.Fatal("undo", out, err)
	}
	s.expect(t, "after undo", map[string]map[string]string{
		colMemories:        {s.ko.ID: "active", s.en1.ID: "active", s.en2.ID: "active", merged.ID: "rejected", insight.ID: "rejected", s.vim.ID: "conflicted", s.code.ID: "conflicted", s.standup.ID: "active", s.standupNew.ID: "rejected", s.rule.ID: "rejected"},
		"memory_relations": {s.relID: "active"},
		colSkillVersions:   {promo.After: "rejected"},
	})
	skill, _ := s.f.svc.Repo.Get(testCtx, colSkills, scopeA, "review")
	if skill.ActiveVersion != "" {
		t.Fatal("the skill keeps the undone version", skill.ActiveVersion)
	}
	// The reopened conflict is a person's to resolve again.
	rel, _ := s.f.svc.Repo.Get(testCtx, "memory_relations", scopeA, s.relID)
	if _, err = s.f.svc.Conflict(testCtx, s.f.human, ConflictInput{Scope: scopeA, Action: "resolve", Reason: "both hold", ConflictID: rel.ID, ExpectedRevision: rel.Revision, Resolution: "keep_both", Memories: rel.Relations}); err != nil {
		t.Fatal("reopened conflict", err)
	}
	// The next dream redoes nothing a person took back and drafts no skill twice.
	before := s.calls.Load()
	next, err := s.f.svc.dream(testCtx, s.worker, scopeA, dreamConfig(), &DreamRun{StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range next.Changes {
		t.Errorf("redid an undone change: %+v", c)
	}
	if s.calls.Load()-before != int64(next.ModelCalls) || next.ModelCalls > 2 {
		t.Fatal("model calls after undo", next.ModelCalls)
	}
}

func TestDreamWithoutModelDecidesButDoesNotGenerate(t *testing.T) {
	s := newDreamModelSeed(t, nil)
	s.f.svc.DreamModel = LLMConfig{}
	run := s.run(t).Run
	if run.Model != "" || run.ModelCalls != 0 || s.calls.Load() != 0 || run.Outcome != "ok" {
		t.Fatal("no model", run.Model, run.ModelCalls, run.Outcome)
	}
	for _, c := range run.Changes {
		if c.Op == "merge" || c.Op == "insight" || c.Op == "promote_skill" {
			t.Fatal("generated without a model", c)
		}
	}
	s.expect(t, "no model", map[string]map[string]string{
		colMemories: {s.ko.ID: "active", s.code.ID: "superseded", s.standupNew.ID: "active", s.rule.ID: "active"},
	})
}

func TestDreamModelBudgetStopsTheRun(t *testing.T) {
	t.Setenv("MEMORY_DREAM_MODEL_CALLS", "1")
	s := newDreamModelSeed(t, nil)
	run := s.run(t).Run
	if run.ModelCalls != 1 || s.calls.Load() != 1 || run.Outcome != "model_limit" {
		t.Fatal("budget", run.ModelCalls, s.calls.Load(), run.Outcome)
	}
	job, _ := s.f.svc.Repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA))
	if job.Dream.Since != nil || job.Status != JobStatusPending {
		t.Fatal("a run cut short must look at the same memories again", job.Dream.Since, job.Status)
	}
	// The next run does not ask about the batch the model already answered: its one call goes further.
	if err := s.f.svc.runDream(testCtx, s.worker, job); err != nil {
		t.Fatal(err)
	}
	page, _ := s.f.svc.ListDreamRuns(testCtx, s.f.human, scopeA, 5)
	if s.calls.Load() != 2 || len(page.Items) != 2 || !slices.ContainsFunc(page.Items[0].Run.Changes, func(c DreamChange) bool { return c.Op == "insight" }) {
		t.Fatal("second run repeated the answered batch", s.calls.Load(), page.Items[0].Run.Changes)
	}
}

func TestDreamCurrentOf(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration, kind string) Record {
		r := Record{CreatedAt: now.Add(d)}
		if kind != "" {
			r.MemoryContext = &MemoryContext{EvidenceKind: kind}
		}
		return r
	}
	for name, tc := range map[string]struct {
		a, b Record
		want int
		rule string
	}{
		"user beats newer inference": {at(-time.Hour, "user_statement"), at(0, "inference"), 0, "user_stated"},
		"newer wins":                 {at(-time.Hour, ""), at(0, ""), 1, "newer"},
		"same minute":                {at(-time.Second, ""), at(0, ""), -1, ""},
		"both stated, newer":         {at(0, "user_statement"), at(-2*time.Hour, "user_statement"), 0, "newer"},
	} {
		if w, rule, _ := currentOf(tc.a, tc.b); w != tc.want || rule != tc.rule {
			t.Errorf("%s: %d %s", name, w, rule)
		}
	}
	eff := now.Add(-72 * time.Hour)
	late := at(0, "")
	late.EffectiveAt = &eff
	if w, _, _ := currentOf(late, at(-time.Hour, "")); w != 1 {
		t.Error("the time a memory takes effect counts, not when it was written")
	}
	future := now.Add(48 * time.Hour)
	planned := at(-time.Hour, "")
	planned.EffectiveAt = &future
	if w, _, _ := currentOf(planned, at(-2*time.Hour, "")); w != -1 {
		t.Error("a memory that takes effect in the future must not win on time")
	}
}

// The model stages end to end on a scratch Postgres database, claimed by the worker loop. With
// DREAM_E2E_GATEWAY (a loopback gateway address) and DREAM_E2E_TOKEN_FILE set, the stages ask that
// gateway's DREAM_E2E_MODEL instead of the fake model, and the test reports what it decided instead of
// checking the fake model's answers; undo must take back every change either way.
func TestPostgresDreamModelEndToEnd(t *testing.T) {
	s := newDreamModelSeed(t, isolatedPostgres(t))
	gateway := os.Getenv("DREAM_E2E_GATEWAY")
	if gateway != "" {
		token, err := os.ReadFile(os.Getenv("DREAM_E2E_TOKEN_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		config, err := ApplyLLMPreset(LLMConfig{APIKey: strings.TrimSpace(string(token)), Model: os.Getenv("DREAM_E2E_MODEL")}, "gateway", "", gateway)
		if err != nil {
			t.Fatal(err)
		}
		s.f.svc.DreamModel = config
		t.Setenv("MEMORY_DREAM_TIME", "80s")
	}
	s.logModelTable(t, "before")
	item := s.workUntilDreamed(t)
	s.logModelTable(t, "after")
	if gateway == "" {
		checkDreamStages(t, s, item)
		checkDreamStagesUndo(t, s, item)
		return
	}
	for _, c := range item.Run.Changes {
		t.Logf("change %-13s %-56s %.2f %s", c.Op, c.Target, c.Confidence, c.Reason)
	}
	for _, k := range item.Run.Skipped {
		t.Logf("skip   %-56s %s", k.Target, k.Reason)
	}
	t.Logf("model %s calls %d outcome %s", item.Run.Model, item.Run.ModelCalls, item.Run.Outcome)
	out, err := s.f.svc.UndoDream(testCtx, s.f.human, item.ID, DreamUndoInput{Scope: scopeA})
	if err != nil || len(out.Conflicts) != 0 || len(out.Undone) != len(item.Run.Changes) {
		t.Fatal("undo", out, err)
	}
	s.logModelTable(t, "undone")
	s.expect(t, "after undo", map[string]map[string]string{
		colMemories: {s.ko.ID: "active", s.en1.ID: "active", s.en2.ID: "active", s.other.ID: "active", s.asks[0].ID: "active", s.standup.ID: "active", s.lunch.ID: "active", s.vim.ID: "conflicted", s.code.ID: "conflicted"},
	})
}

// A dream without a model on Postgres: it decides contradictions and review entries and generates nothing.
func TestPostgresDreamWithoutModel(t *testing.T) {
	s := newDreamModelSeed(t, isolatedPostgres(t))
	s.f.svc.DreamModel = LLMConfig{}
	item := s.workUntilDreamed(t)
	s.logModelTable(t, "no model")
	if item.Run.ModelCalls != 0 || s.calls.Load() != 0 || slices.ContainsFunc(item.Run.Changes, func(c DreamChange) bool { return c.Op == "merge" || c.Op == "insight" || c.Op == "promote_skill" }) {
		t.Fatal("generated without a model", item.Run)
	}
	s.expect(t, "no model", map[string]map[string]string{colMemories: {s.ko.ID: "active", s.code.ID: "superseded", s.standupNew.ID: "active", s.rule.ID: "active"}})
}

// logModelTable logs every seeded row and every memory or skill version the dream wrote, by status.
func (s dreamModelSeed) logModelTable(t *testing.T, when string) {
	for _, col := range []string{colMemories, "memory_candidates", colSkillVersions} {
		rows, err := s.f.svc.Repo.ListRecords(testCtx, scopeA, col, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			t.Logf("%-8s %-18s %-11s %-10s %q", when, col, r.Status, r.Kind, r.Text)
		}
	}
}
