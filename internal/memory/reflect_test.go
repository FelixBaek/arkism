package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func reflectFixture(t *testing.T, answers ...string) (*fixture, *fakeWikiLLM) {
	f := newFixture(t)
	f.svc.Embedder = nil
	a := f.auth.actors[f.human]
	a.verbs["generate"] = true
	f.auth.actors[f.human] = a
	llm := &fakeWikiLLM{answers: answers}
	f.svc.Wiki = llm
	return f, llm
}

func directiveInput(title, rule, target string, priority int) DirectiveInput {
	return DirectiveInput{Scope: scopeA, Title: title, Rule: rule, AppliesTo: target, Priority: priority}
}

func TestDirectiveOnlyHumansWrite(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateDirective(testCtx, f.agent, directiveInput("t", "r", "both", 1)); !errors.Is(err, ErrDenied) {
		t.Fatal("agent created a directive", err)
	}
	d, err := f.svc.CreateDirective(testCtx, f.human, directiveInput("Units", "Always use metric units.", "both", 5))
	if err != nil || !d.Active || d.Revision != 1 || d.CreatedBy != "human-1" {
		t.Fatal(d, err)
	}
	upd := directiveInput("Units", "Use metric.", "reflect", 7)
	upd.ExpectedRevision = 1
	if _, err = f.svc.UpdateDirective(testCtx, f.agent, d.ID, upd); !errors.Is(err, ErrDenied) {
		t.Fatal("agent updated", err)
	}
	if err = f.svc.DeleteDirective(testCtx, f.agent, d.ID, DirectiveDeleteInput{Scope: scopeA, ExpectedRevision: 1}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent deleted", err)
	}
	got, err := f.svc.GetDirective(testCtx, f.agent, scopeA, d.ID)
	if err != nil || got.Rule != "Always use metric units." {
		t.Fatal("agent read", err)
	}
	u, err := f.svc.UpdateDirective(testCtx, f.human, d.ID, upd)
	if err != nil || u.Revision != 2 || u.Priority != 7 || u.AppliesTo != "reflect" {
		t.Fatal(u, err)
	}
	if _, err = f.svc.UpdateDirective(testCtx, f.human, d.ID, upd); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision", err)
	}
	for _, bad := range []DirectiveInput{directiveInput("", "r", "both", 1), directiveInput("t", "r", "all", 1), directiveInput("t", "r", "both", 101), directiveInput("t", "<private>x</private>", "both", 1)} {
		if _, err = f.svc.CreateDirective(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid accepted", bad, err)
		}
	}
	// The generic record routes never touch directives.
	if _, err = f.svc.Get(testCtx, f.human, colDirectives, Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID}, d.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("generic get", err)
	}
	if err = f.svc.DeleteDirective(testCtx, f.human, d.ID, DirectiveDeleteInput{Scope: scopeA, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if list, err := f.svc.ListDirectives(testCtx, f.human, scopeA, 0, ""); err != nil || len(list.Items) != 0 {
		t.Fatal("deleted still listed", err)
	}
}

func TestDirectiveHTTPAgentRefused(t *testing.T) {
	f := newFixture(t)
	body := structuredText(directiveInput("t", "rule", "both", 1))
	for token, want := range map[string]int{f.agent: 403, f.human: 201} {
		req := httptest.NewRequest("POST", "/v1/directives", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != want {
			t.Fatal(token, w.Code, w.Body.String())
		}
	}
}

func TestDirectiveListPages(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.svc.CreateDirective(testCtx, f.human, directiveInput("t", "rule", "both", i))
	}
	first, err := f.svc.ListDirectives(testCtx, f.human, scopeA, 3, "")
	if err != nil || len(first.Items) != 3 || first.NextCursor != "3" || first.Items[0].Priority != 4 {
		t.Fatal(first, err)
	}
	second, err := f.svc.ListDirectives(testCtx, f.human, scopeA, 3, first.NextCursor)
	if err != nil || len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatal(second, err)
	}
}

func TestReflectCitesOnlyRetrievedMemories(t *testing.T) {
	f, llm := reflectFixture(t)
	m := f.activeMemory("reflect needle: the deploy runs on Fridays", "needle")
	llm.answers = []string{"배포는 금요일입니다 [" + m.ID + "] [mem-invented]"}
	if _, err := f.svc.CreateDirective(testCtx, f.human, directiveInput("Tone", "Answer briefly.", "reflect", 3)); err != nil {
		t.Fatal(err)
	}
	ctxOnly, _ := f.svc.CreateDirective(testCtx, f.human, directiveInput("Ctx", "Only for context.", "context", 9))
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle 언제 배포?"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.CitedMemoryIDs, []string{m.ID}) || len(out.DirectivesApplied) != 1 || slices.Contains(out.DirectivesApplied, ctxOnly.ID) {
		t.Fatal("citations or directives", out.CitedMemoryIDs, out.DirectivesApplied)
	}
	if len(out.Retrieved) == 0 || out.Retrieved[0].ID != m.ID || out.Usage == nil || out.Generator == nil || out.Generator.PromptVersion != reflectPromptVersion {
		t.Fatal("retrieved/usage", out)
	}
	if len(llm.prompts) != 1 || !strings.Contains(llm.prompts[0], "Answer briefly.") || !strings.Contains(llm.prompts[0], "same language") || strings.Contains(llm.prompts[0], "Only for context.") {
		t.Fatal("prompt", llm.prompts)
	}
	// Answers are not stored as memories, and the call is in the ledger.
	if rows, _ := f.repo.ListRecords(testCtx, scopeA, colMemories, 100); len(rows) != 1 {
		t.Fatal("answer stored", len(rows))
	}
	if n, _ := f.svc.dailyModelCalls(testCtx, projectScope(scopeA), reflectOperation, time.Now()); n != 1 {
		t.Fatal("usage not recorded", n)
	}
}

func TestReflectPromptOnlyMakesNoModelCall(t *testing.T) {
	f, llm := reflectFixture(t)
	f.activeMemory("prompt needle", "needle")
	out, err := f.svc.Reflect(testCtx, f.agent, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true})
	if err != nil || out.Prompt == nil || !strings.Contains(out.Prompt.User, "prompt needle") || out.Answer != "" || len(llm.prompts) != 0 {
		t.Fatal(out, err)
	}
	f.svc.Wiki = nil
	if _, err = f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true}); err != nil {
		t.Fatal("prompt preview needs no model", err)
	}
}

func TestReflectWithoutModel(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle"})
	if !errors.Is(err, ErrModelNotConfigured) {
		t.Fatal(err)
	}
	if code, status := errorResponse(err); code != "model_not_configured" || status != 503 {
		t.Fatal(code, status)
	}
}

func TestReflectBudgetExceeded(t *testing.T) {
	f, llm := reflectFixture(t, "never")
	f.activeMemory("budget needle", "needle")
	for i := 0; i < reflectDailyCallLimit; i++ {
		if _, err := f.svc.beginProviderCall(testCtx, projectScope(scopeA), reflectOperation, wikiProvider, "fake-wiki"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle"}); !errors.Is(err, ErrModelBudget) || len(llm.prompts) != 0 {
		t.Fatal(err, llm.prompts)
	}
}

func TestContextBuildIncludesDirectives(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("context needle", "needle")
	high, _ := f.svc.CreateDirective(testCtx, f.human, directiveInput("High", "Never push to main.", "context", 9))
	f.svc.CreateDirective(testCtx, f.human, directiveInput("Reflect only", "x", "reflect", 9))
	off := directiveInput("Off", "inactive", "both", 9)
	off.Active = new(bool)
	f.svc.CreateDirective(testCtx, f.human, off)
	in := ContextBuildInput{Scope: scopeA, Task: "needle", TokenEncoding: "cl100k_base", Modes: []string{"fts"}}
	pack, err := f.svc.BuildContext(testCtx, f.agent, in)
	if err != nil || pack.Directives == nil || len(pack.Directives.Items) != 1 || pack.Directives.Items[0].ID != high.ID ||
		pack.Directives.Authority != DirectiveAuthority || pack.Directives.Tokens == 0 || pack.Partial || len(pack.Items) != 1 {
		t.Fatal(pack.Directives, err)
	}
	raw, _ := json.Marshal(pack)
	if !strings.Contains(string(raw), `"directives"`) {
		t.Fatal("not serialized")
	}
	// A budget too small for the rule reports it instead of cutting it.
	f.svc.CreateDirective(testCtx, f.human, directiveInput("Long", strings.Repeat("rule words ", 300), "both", 1))
	budget := 400
	in.BudgetTokens = &budget
	pack, err = f.svc.BuildContext(testCtx, f.agent, in)
	if err != nil || len(pack.Directives.Omitted) != 1 || !pack.Partial || pack.Directives.Tokens+pack.TokensUsed > budget {
		t.Fatal(pack.Directives, pack.TokensUsed, err)
	}
}

// A memory the external-transfer policy refuses never reaches the model; the
// prompt-only preview, which stays with the caller, still shows it.
func TestReflectSendsOnlyTransferableMemories(t *testing.T) {
	f, llm := reflectFixture(t, "ok")
	f.activeMemory("open needle fact", "needle")
	r := f.activeMemory("restricted needle fact", "needle")
	r.Sensitivity = "restricted"
	f.repo.store[colMemories][recKey(r.Scope, r.ID)] = r
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle"})
	if err != nil || len(llm.prompts) != 1 || strings.Contains(llm.prompts[0], "restricted needle") || !strings.Contains(llm.prompts[0], "open needle") {
		t.Fatal("restricted memory sent", err, llm.prompts)
	}
	for _, h := range out.Retrieved {
		if h.ID == r.ID {
			t.Fatal("restricted memory listed")
		}
	}
	preview, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true})
	if err != nil || !strings.Contains(preview.Prompt.User, "restricted needle") || len(llm.prompts) != 1 {
		t.Fatal("preview", err)
	}
}

// A malformed answer still spent tokens; they count against the wiki job.
func TestWikiCallCountsTokensOfRejectedAnswer(t *testing.T) {
	f, _ := wikiFixture(t, string([]byte{0xff, 0xfe}))
	job := &WikiJob{MaxTotalTokens: 1 << 20}
	if _, err := f.svc.wikiCall(testCtx, projectScope(scopeA), job, "wiki_analyse", "s", "u"); !errors.Is(err, ErrUnavailable) || job.Tokens != 150 || job.Calls != 1 {
		t.Fatal(err, job.Tokens, job.Calls)
	}
}
