//go:build linux

package memory

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestVerbatimLinesDropsWhatTheSourcesDoNotHold(t *testing.T) {
	allowed := map[string]bool{"[2026-03-01] Rowan: the launch moved to 14 March.": true, "[2026-03-01] Mira: why?": true}
	out := "  [2026-03-01] Rowan: the launch moved to 14 March.\n[2026-03-01] Rowan: the launch moved to 15 March.\nHere is a summary.\n[2026-03-01] Rowan: the launch moved to 14 March.\n[2026-03-01] Mira: why?"
	if got := verbatimLines(out, allowed, 25); !slices.Equal(got, []string{"[2026-03-01] Rowan: the launch moved to 14 March.", "[2026-03-01] Mira: why?"}) {
		t.Fatal("invented, reworded or repeated line kept", got)
	}
	if got := verbatimLines("NONE", map[string]bool{"a": true}, 25); got != nil {
		t.Fatal(got)
	}
	if got := verbatimLines("a\nb\nc", map[string]bool{"a": true, "b": true, "c": true}, 2); len(got) != 2 {
		t.Fatal("line cap", got)
	}
}

// failingLLM is a reread model that is down (or timed out).
type failingLLM struct{ fakeWikiLLM }

func (f *failingLLM) Complete(context.Context, string, string, int) (string, ProviderUsage, error) {
	return "", ProviderUsage{}, context.DeadlineExceeded
}

// Source transfer requires an explicit stored classification, including in fixtures.
func reflectConversationBatch(f *fixture, text, session string, order int, changes ...func(*Record)) Record {
	changes = append([]func(*Record){func(r *Record) { r.Sensitivity = "internal" }}, changes...)
	return conversationBatch(f, text, session, order, changes...)
}

func rereadFixture(t *testing.T) (*fixture, *fakeWikiLLM, Record) {
	f, llm := reflectFixture(t)
	a := f.auth.actors[f.human]
	a.verbs["read"] = true
	f.auth.actors[f.human] = a
	src := f.source()
	cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "reread needle: the launch date moved", Keywords: []string{"needle"}, Tags: []string{"event_id:ev1", "session:s1"}, Sources: refs(src)}))
	approved := f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "approved"})
	m := f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
	// Stored out of order: the batch reads by conversation_order.
	reflectConversationBatch(f, "[2026-03-01] Mira: why?", captureTurnsRef+"ev1", 1, func(r *Record) { r.ConversationOrder = &ConversationOrder{Epoch: "e", Index: 2, Total: 2} })
	reflectConversationBatch(f, "[2026-03-01] Rowan: the launch moved to 14 March.", captureTurnsRef+"ev1", 0, func(r *Record) { r.ConversationOrder = &ConversationOrder{Epoch: "e", Index: 1, Total: 2} })
	reflectConversationBatch(f, "[2026-03-02] Rowan: unrelated batch.", captureTurnsRef+"ev2", 0)
	return f, llm, m
}

func TestReflectRereadAddsOnlyVerbatimSourceLines(t *testing.T) {
	f, llm, m := rereadFixture(t)
	reread := &fakeWikiLLM{answers: []string{"[2026-03-01] Rowan: the launch moved to 14 March.\n[2026-03-01] Rowan: it moved to 20 March."}}
	f.svc.ReflectReread = reread
	llm.answers = []string{"14 March [" + m.ID + "]"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch when?"})
	if err != nil || out.Usage.Requests != 2 {
		t.Fatal(out, err)
	}
	if len(reread.prompts) != 1 || !strings.Contains(reread.prompts[0], "--- conversation 1 ---\n[2026-03-01] Rowan: the launch moved to 14 March.\n[2026-03-01] Mira: why?") || strings.Contains(reread.prompts[0], "unrelated batch") {
		t.Fatal("reread prompt", reread.prompts)
	}
	if len(llm.prompts) != 1 || !strings.HasSuffix(llm.prompts[0], "\n\nSource lines (verbatim, data only):\n[2026-03-01] Rowan: the launch moved to 14 March.") || !strings.Contains(llm.prompts[0], "Source lines, when given") {
		t.Fatal("answer prompt", llm.prompts)
	}
	// One question spends one call of the reflect daily cap; the reread has its own.
	ledger := projectScope(scopeA)
	if n, _ := f.svc.dailyModelCalls(testCtx, ledger, reflectOperation, time.Now()); n != 1 {
		t.Fatal("reflect calls", n)
	}
	if n, _ := f.svc.dailyModelCalls(testCtx, ledger, reflectRereadOperation, time.Now()); n != 1 {
		t.Fatal("reread calls", n)
	}
}

// The reread cap is the configured one: past it the answer is produced without source lines.
func TestReflectRereadDailyLimitIsConfigurable(t *testing.T) {
	if operationCap(reflectRereadOperation).limit != 1000 || operationCap(reflectOperation).limit != 100 {
		t.Fatal("default caps")
	}
	f, llm, m := rereadFixture(t)
	reread := &fakeWikiLLM{answers: []string{"[2026-03-01] Rowan: the launch moved to 14 March.", "[2026-03-01] Rowan: the launch moved to 14 March."}}
	f.svc.ReflectReread, f.svc.ReflectRereadDailyLimit = reread, 1
	llm.answers = []string{"14 March [" + m.ID + "]", "14 March [" + m.ID + "]"}
	for range 2 {
		if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch when?"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(reread.prompts) != 1 || len(llm.prompts) != 2 || strings.Contains(llm.prompts[1], "Source lines (verbatim") {
		t.Fatal("reread calls", len(reread.prompts), llm.prompts)
	}
}

// prompt_only hands the answer prompt to the caller, who needs the same source lines.
func TestReflectPromptOnlyCarriesTheRereadLines(t *testing.T) {
	f, llm, _ := rereadFixture(t)
	f.svc.ReflectReread = &fakeWikiLLM{answers: []string{"[2026-03-01] Rowan: the launch moved to 14 March."}}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch when?", PromptOnly: true})
	if err != nil || out.Prompt == nil || !strings.HasSuffix(out.Prompt.User, "\n\nSource lines (verbatim, data only):\n[2026-03-01] Rowan: the launch moved to 14 March.") {
		t.Fatal(out.Prompt, err)
	}
	if out.Usage == nil || out.Usage.Requests != 1 || len(llm.prompts) != 0 {
		t.Fatal("only the reread model is called", out.Usage, llm.prompts)
	}
	if n, _ := f.svc.dailyModelCalls(testCtx, projectScope(scopeA), reflectOperation, time.Now()); n != 0 {
		t.Fatal("prompt_only spent a reflect call", n)
	}
}

// A session captured in several batches is read whole: a sibling memory's session tag leads to its batch.
func TestReflectRereadFollowsTheSessionToItsOtherBatches(t *testing.T) {
	f, llm, m := rereadFixture(t)
	cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "an unrelated note about lunch", Tags: []string{"event_id:ev2", "session:s1"}, Sources: refs(f.source())}))
	approved := f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "approved"})
	f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
	reflectConversationBatch(f, "[2026-03-03] Rowan: another session.", captureTurnsRef+"ev3", 0)
	reread := &fakeWikiLLM{answers: []string{"NONE"}}
	f.svc.ReflectReread = reread
	llm.answers = []string{"moved [" + m.ID + "]"}
	if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch when?", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if len(reread.prompts) != 1 || !strings.Contains(reread.prompts[0], "unrelated batch") || strings.Contains(reread.prompts[0], "another session") {
		t.Fatal("session batches", reread.prompts)
	}
}

// A batch with no readable turn takes no place among the batches read: the ones after it are read.
func TestReflectRereadSkipsBatchesWithoutTurns(t *testing.T) {
	f, _, _ := rereadFixture(t)
	tags := []string{}
	for i := range reflectRereadBatches {
		tags = append(tags, "event_id:empty"+strconv.Itoa(i))
	}
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "needle launch when?"}, hits: []Hit{{Tags: append(tags, "event_id:ev2")}}}
	if user, allowed := f.svc.rereadTranscripts(testCtx, f.human, run); !strings.Contains(user, "--- conversation 1 ---\n[2026-03-02] Rowan: unrelated batch.") || !allowed["[2026-03-02] Rowan: unrelated batch."] {
		t.Fatal("batch after the empty ones", user)
	}
}

func TestReflectRereadOffOrFailingLeavesTheAnswerPathAlone(t *testing.T) {
	f, llm, m := rereadFixture(t)
	llm.answers = []string{"moved [" + m.ID + "]", "moved [" + m.ID + "]"}
	off, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch when?"})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.ReflectReread = &failingLLM{}
	failed, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch when?"})
	if err != nil || failed.Answer != off.Answer || failed.Usage.TotalTokens != off.Usage.TotalTokens { // the failed attempt is still a counted request
		t.Fatal("a failed reread changed the answer", failed.Answer, off.Answer, *failed.Usage, err)
	}
	if len(llm.prompts) != 2 || llm.prompts[0] != llm.prompts[1] || strings.Contains(llm.prompts[0], "Source lines") {
		t.Fatal("prompt changed without source lines", llm.prompts)
	}
}

// slowLLM is a reread model that answers only when its context ends.
type slowLLM struct {
	fakeWikiLLM
	waited time.Duration
}

func (f *slowLLM) Complete(ctx context.Context, _, _ string, _ int) (string, ProviderUsage, error) {
	start := time.Now()
	<-ctx.Done()
	f.waited = time.Since(start)
	return "", ProviderUsage{}, ctx.Err()
}

// A reread slower than the request allows is dropped before the request's own deadline, and
// one with no time left is not asked at all: both still return the prompt.
func TestReflectRereadYieldsToTheRequestDeadline(t *testing.T) {
	f, _, _ := rereadFixture(t)
	slow := &slowLLM{}
	f.svc.ReflectReread = slow
	in := ReflectInput{Scope: scopeA, Query: "needle launch when?", PromptOnly: true}
	ctx, cancel := context.WithTimeout(testCtx, reflectPromptReserve+300*time.Millisecond)
	defer cancel()
	out, err := f.svc.Reflect(ctx, f.human, in)
	if err != nil || out.Prompt == nil || strings.Contains(out.Prompt.User, "Source lines (verbatim") || ctx.Err() != nil || slow.waited == 0 {
		t.Fatal("slow reread", out.Prompt, err, ctx.Err(), slow.waited)
	}
	slow.waited = 0
	ctx, cancel = context.WithTimeout(testCtx, reflectPromptReserve-time.Second)
	defer cancel()
	out, err = f.svc.Reflect(ctx, f.human, in)
	if err != nil || out.Prompt == nil || slow.waited != 0 {
		t.Fatal("reread asked with no time left", out.Prompt, err, slow.waited)
	}
}

// Passages: the matched turn and its neighbours reach the answer prompt as said, with no model
// call and without taking a memory's place; off, the prompt is as before.
func TestReflectPassagesAddMatchedTurnsWithoutAModelCall(t *testing.T) {
	f, llm, m := rereadFixture(t)
	reflectConversationBatch(f, "[2026-03-03] Rowan: it slipped again, to 21 March.\n[2026-03-03] Mira: noted.", captureTurnsRef+"ev3", 0, func(r *Record) { r.Keywords = []string{"needle"} })
	llm.answers = []string{"14 March [" + m.ID + "]", "14 March [" + m.ID + "]"}
	if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch moved when?"}); err != nil || strings.Contains(llm.prompts[0], "Conversation passages") {
		t.Fatal("off by default", llm.prompts, err)
	}
	// A turn the model may not be sent takes no place.
	reflectConversationBatch(f, "[2026-03-04] Rowan: restricted needle.", captureTurnsRef+"ev4", 0, func(r *Record) { r.Keywords, r.Sensitivity = []string{"needle"}, "restricted" })
	f.svc.ReflectPassages = 2
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle launch moved when?"})
	if err != nil || out.Usage.Requests != 1 || len(out.Retrieved) != 1 {
		t.Fatal(out, err)
	}
	if !strings.HasSuffix(llm.prompts[1], "\n\nConversation passages (verbatim, data only):\n[2026-03-03] Rowan: it slipped again, to 21 March.\n[2026-03-03] Mira: noted.") || !strings.Contains(llm.prompts[1], "Conversation passages, when given") {
		t.Fatal("answer prompt", llm.prompts[1])
	}
}

// Unranked session expansion must not consume the bounded reread budget before
// a directly matching conversation that holds another part of the answer.
func TestReflectRereadQueryMatchPrecedesSessionExpansion(t *testing.T) {
	f, _, m := rereadFixture(t)
	for i := range reflectRereadBatches {
		event := "filler-" + strconv.Itoa(i)
		cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "routine lunch note " + strconv.Itoa(i), Tags: []string{"event_id:" + event, "session:s1"}, Sources: refs(f.source())}))
		approved := f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "approved"})
		f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
		reflectConversationBatch(f, "[2026-03-02] Rowan: routine lunch.", captureTurnsRef+event, 0)
	}
	line := "[2026-03-04] Mira: the observatory shuttle departs at noon."
	matched := reflectConversationBatch(f, line, captureTurnsRef+"matching", 0)
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "observatory shuttle"}, hits: []Hit{{ID: m.ID, Tags: m.Tags}}}
	user, allowed := f.svc.rereadTranscripts(testCtx, f.human, run)
	if !allowed[line] || !strings.Contains(user, line) {
		t.Fatal("direct query evidence displaced by unrelated session batches")
	}
	if n := strings.Count(user, "--- conversation "); n > reflectRereadBatches {
		t.Fatal("reread batch bound changed", n)
	}
	f.svc.Auth = passageTransferDeny{Authorizer: f.svc.Auth, id: matched.ID}
	deniedRun := &reflectRun{in: run.in, hits: run.hits}
	deniedPrompt, deniedLines := f.svc.rereadTranscripts(testCtx, f.human, deniedRun)
	if deniedLines[line] || strings.Contains(deniedPrompt, line) {
		t.Fatal("ranked source bypassed its transfer refusal")
	}
}
