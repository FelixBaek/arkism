//go:build linux

package memory

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestReflectRecallRefreshesOriginalPassagesWithoutExtraCalls(t *testing.T) {
	f, llm := reflectFixture(t)
	f.svc.ReflectPassages = 1
	old := reflectConversationBatch(f, "Coffee meeting was on Tuesday.", captureTurnsRef+"coffee", 0)
	fresh := reflectConversationBatch(f, "Orbital nickname: the crew calls the rover Lantern.", captureTurnsRef+"orbital", 0)
	llm.answers = []string{toolCall("recall", map[string]any{"query": "orbital nickname"}), "The crew calls it Lantern."}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "coffee", MaxIterations: 2})
	if err != nil || out.Usage.Requests != 2 || len(llm.prompts) != 2 {
		t.Fatalf("followup failed or spent extra calls: %v %+v", err, out)
	}
	if !strings.Contains(llm.prompts[0], old.Text) || strings.Contains(llm.prompts[0], fresh.Text) {
		t.Fatal("initial evidence setup failed")
	}
	if !strings.Contains(llm.prompts[1], fresh.Text) || strings.Contains(llm.prompts[1], old.Text) {
		t.Fatal("bounded followup did not replace old optional passage")
	}
}

func TestReflectFollowupKeepsFirstSourceDeliveryObligations(t *testing.T) {
	for _, eraseOld := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "replaced_old"}[eraseOld], func(t *testing.T) { checkReflectFollowupErasure(t, eraseOld) })
	}
}

func checkReflectFollowupErasure(t *testing.T, eraseOld bool) {
	t.Helper()
	f, _ := reflectFixture(t)
	f.svc.ReflectPassages = 1
	old := reflectConversationBatch(f, "Coffee initial detail", captureTurnsRef+"first", 0)
	fresh := reflectConversationBatch(f, "Orbital later detail", captureTurnsRef+"next", 0)
	llm := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{toolCall("recall", map[string]any{"query": "orbital"}), "later detail"}}}
	llm.change = func() {
		if len(llm.prompts) != 2 {
			return
		}
		id := fresh.ID
		if eraseOld {
			id = old.ID
		}
		f.editRecord("sources", id, func(r *Record) { r.ContentErased = true })
	}
	f.svc.Wiki = llm
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "coffee", MaxIterations: 2})
	if len(llm.prompts) != 2 || !strings.Contains(llm.prompts[1], fresh.Text) {
		t.Fatalf("followup path not reached: %v", err)
	}
	if err == nil || out.Answer != "" {
		t.Fatal("late erased source delivered", err, out.Answer)
	}
}

func TestReflectFollowupPassagesRespectRequestAndToolFilters(t *testing.T) {
	f, _ := reflectFixture(t)
	f.svc.ReflectPassages = 10
	at := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	outerFrom, outerTo := at.AddDate(0, 0, -9), at.AddDate(0, 0, 20)
	from, to := at.AddDate(0, 0, -1), at.AddDate(0, 0, 1)
	add := func(text, by string, created time.Time, scope Scope) {
		reflectConversationBatch(f, text, captureTurnsRef+text, 0, func(r *Record) {
			r.CreatedBy = by
			r.CreatedAt = created
			r.Scope = scope
			r.Tags = []string{"team:core"}
		})
	}
	add("orbital allowed", "wanted", at, scopeA)
	add("orbital outside-tool-window", "wanted", at.AddDate(0, 0, 5), scopeA)
	add("orbital wrong-recorder", "other", at, scopeA)
	other := scopeA
	other.ProjectID = "other"
	add("orbital wrong-scope", "wanted", at, other)
	run := &reflectRun{tools: reflectToolDefaults, seen: map[string]bool{}, graph: map[string]bool{}, in: ReflectInput{Scope: scopeA, Query: "initial", CreatedBy: []string{"wanted"}, CreatedFrom: &outerFrom, CreatedTo: &outerTo, Tags: []string{"team:core"}}}
	_, err := f.svc.reflectTool(testCtx, f.human, run, reflectToolCall{Name: reflectToolRecall, Args: reflectToolArgs{Query: "orbital", From: &from, To: &to}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(run.passages, "\n")
	if !strings.Contains(got, "orbital allowed") || strings.Contains(got, "outside-tool-window") || strings.Contains(got, "wrong-") {
		t.Fatal("filter mismatch", got)
	}
	if run.in.Query != "initial" || !run.in.CreatedFrom.Equal(outerFrom) {
		t.Fatal("tool mutated original request")
	}
}

func TestReflectFollowupPassagesRemainOptInAndExcludeObservationOnly(t *testing.T) {
	for _, mode := range []string{"off", "observation_tool", "observation_only"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := reflectFixture(t)
			f.svc.ReflectPassages = 1
			reflectConversationBatch(f, "orbital optional text", captureTurnsRef+"optional", 0)
			run := &reflectRun{tools: reflectToolDefaults, seen: map[string]bool{}, graph: map[string]bool{}, in: ReflectInput{Scope: scopeA, Query: "initial"}, passages: []string{"existing"}}
			call := reflectToolCall{Name: reflectToolRecall, Args: reflectToolArgs{Query: "orbital"}}
			switch mode {
			case "off":
				f.svc.ReflectPassages = 0
			case "observation_tool":
				call.Name = reflectToolObservations
			case "observation_only":
				run.in.FactTypes = []string{"observation"}
			}
			if _, err := f.svc.reflectTool(testCtx, f.human, run, call); err != nil {
				t.Fatal(err)
			}
			if len(run.passages) != 1 || run.passages[0] != "existing" || len(run.sourceRecords) != 0 {
				t.Fatal("optional/observation behavior changed")
			}
		})
	}
}

func TestReflectFollowupPassageGlobalBudget(t *testing.T) {
	fresh := strings.Repeat("한", reflectPassageRunes-10)
	old := strings.Repeat("나", 20)
	got := mergeReflectPassages([]string{fresh}, []string{old, fresh, "short"}, 2)
	if len(got) != 2 || got[0] != fresh || got[1] != "short" || utf8.RuneCountInString(strings.Join(got, "")) > reflectPassageRunes {
		t.Fatal("global passage budget changed")
	}
	if len(mergeReflectPassages([]string{"new"}, []string{"old"}, 1)) != 1 {
		t.Fatal("global count exceeded")
	}
}

func TestReflectFollowupSharesToolAndChunkBudgetAndTracesSources(t *testing.T) {
	f, _ := reflectFixture(t)
	f.svc.ReflectPassages = 10
	memory := f.activeMemory("orbital brief summary", "orbital")
	short := reflectConversationBatch(f, "orbital nickname Lantern", captureTurnsRef+"short", 0)
	long := reflectConversationBatch(f, "orbital "+strings.Repeat("x", 300), captureTurnsRef+"long", 0)
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "initial"}, tools: reflectToolDefaults, seen: map[string]bool{}, graph: map[string]bool{}}
	call := reflectToolCall{Name: reflectToolRecall, Args: reflectToolArgs{Query: "orbital", MaxTokens: 64, MaxChunkTokens: 16}}
	step, err := f.svc.reflectTool(testCtx, f.human, run, call)
	if err != nil || step.Error != "" {
		t.Fatal(err, step.Error)
	}
	cost := 0
	for _, hit := range run.hits {
		cost += approxTokens(hit.Text)
	}
	for _, block := range run.passages {
		cost += approxTokens(block)
		if approxTokens(block) > 16 {
			t.Fatal("chunk cap bypass")
		}
	}
	if cost > 64 || !slices.Contains(step.IDs, memory.ID) || !slices.Contains(step.IDs, short.ID) || slices.Contains(step.IDs, long.ID) {
		t.Fatal("shared cap/source trace mismatch", cost, step.IDs)
	}
	if _, ok := run.sourceRecords[long.ID]; ok {
		t.Fatal("rejected large source admitted")
	}
}

type refuseSecondDocumentTransfer struct {
	Authorizer
	id    string
	calls int
}

func (a *refuseSecondDocumentTransfer) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.ResourceID == a.id && strings.HasPrefix(in.Context.Destination, "external:") {
		a.calls++
		if a.calls == 2 {
			return Decision{}, ErrDenied
		}
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

func TestReflectDocumentNewTransferDenialSkipsOnlySource(t *testing.T) {
	f, _ := reflectFixture(t)
	mem := f.activeMemory("summary")
	accepted, err := f.repo.Get(testCtx, "sources", scopeA, mem.Sources[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	denied := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "second distinct original message"}))
	mem.Sources = append(mem.Sources, refs(denied)...)
	reflectMemoryRawPut(f, mem)
	auth := &refuseSecondDocumentTransfer{Authorizer: f.svc.Auth, id: denied.ID}
	f.svc.Auth = auth
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "summary"}}
	run.keepSources([]Record{accepted}, false)
	hits, err := f.svc.reflectSources(testCtx, f.human, run, mem.ID, true)
	if err != nil || len(hits) != 1 || auth.calls != 2 {
		t.Fatal("new denial removed allowed source", err, len(hits), auth.calls)
	}
	if _, ok := run.sourceRecords[denied.ID]; ok {
		t.Fatal("denied source admitted")
	}
	if run.sourceRecords[accepted.ID].record.ID != accepted.ID {
		t.Fatal("accepted snapshot lost")
	}
}

func TestReflectConversationCapabilityIsConditionalAndVersioned(t *testing.T) {
	f, _ := reflectFixture(t)
	run := &reflectRun{in: ReflectInput{Scope: scopeA}}
	f.svc.reflectPassages(testCtx, f.human, run, nil)
	if strings.Contains(reflectToolsText(run), "original conversation") || reflectEvidencePromptVersion(run) != reflectPromptVersion {
		t.Fatal("disabled default changed")
	}
	f.svc.ReflectPassages = 1
	f.svc.reflectPassages(testCtx, f.human, run, nil)
	if !strings.Contains(reflectToolsText(run), "original conversation") || reflectEvidencePromptVersion(run) != reflectConversationPromptVersion {
		t.Fatal("enabled capability missing")
	}
	actor := f.auth.actors[f.human]
	actor.verbs["read"] = false
	f.auth.actors[f.human] = actor
	f.svc.reflectPassages(testCtx, f.human, run, nil)
	if strings.Contains(reflectToolsText(run), "original conversation") {
		t.Fatal("denied capability advertised")
	}
}
