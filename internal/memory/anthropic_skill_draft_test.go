package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnthropicSkillGenerationUsesCanonicalBudgetAndLeavesCandidate(t *testing.T) {
	checkAnthropicPrepared(t, newFixture(t), "")
}
func TestAnthropicLocalizedSkillDraftPreservesBaselineAndPreparedRecovery(t *testing.T) {
	checkAnthropicPrepared(t, newFixture(t), "patch")
}
func TestAnthropicPreparedCandidatePrivacy(t *testing.T) {
	for _, mode := range []string{"erased", "revision", "withdrawn"} {
		t.Run(mode, func(t *testing.T) { checkAnthropicPrepared(t, newFixture(t), mode) })
	}
}
func checkAnthropicPrepared(t *testing.T, f *fixture, mode string) {
	ctx := testCtx
	in, _ := generationFixture(t, f)
	patchMode := mode == "patch"
	var baseline Record
	const baselineContent = "# Review\r\n\r\nCheck results.\r\n\r\nNever publish or widen permissions.\r\n"
	if patchMode {
		mode = ""
		imported, err := f.svc.ImportSkill(ctx, f.human, ImportSkillInput{Scope: scopeA, SkillID: in.SkillID, Path: in.Path, Content: baselineContent, Analysis: detailedSkillAnalysis()})
		if err != nil {
			t.Fatal(err)
		}
		baseline = imported.Record
		in.ComparisonPlan.BaselineVersion = baseline.ID
	}
	f.auth.actors[testPrincipalToken("drafter")] = actor("drafter", roleService, true, append(allVerbs, "reserve", "settle", "priority", "transfer")...)
	f.svc.ModelBudgetPolicy = &ModelBudgetPolicy{Scope: scopeA, Principals: []string{"drafter"}, MonthlyPicoUSD: 100 * picoDollar, GeneralPicoUSD: 90 * picoDollar, DailyPicoUSD: 3 * picoDollar, ExpiresAt: time.Now().Add(time.Hour)}
	if strings.HasPrefix(mode, "pg:") {
		p := f.svc.Repo.(*Postgres)
		if err := p.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: "drafter", Role: roleService, Token: testPrincipalToken("drafter"), OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); err != nil {
			t.Fatal(err)
		}
		app := restrictedPostgres(t, p)
		var err error
		f.svc.Auth, err = NewPostgresAuthorizer(app)
		if err != nil {
			t.Fatal(err)
		}
		f.svc.Repo = app
		ctx = ContextWithPostgresToken(ctx, testPrincipalToken("drafter"))
		mode = strings.TrimPrefix(mode, "pg:")
	}
	_, answer := draftFixture()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-workspace-id") != "workspace" || r.Header.Get("x-api-key") != "test" {
			t.Error("missing provider headers")
		}
		if r.URL.Path == "/models" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "claude-sonnet-5-5"}}})
			return
		}
		calls.Add(1)
		var payload map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Fatal("payload")
		}
		if payload["tools"] != nil || payload["temperature"] != nil || payload["output_config"] == nil {
			t.Error("invalid role contract")
		}
		wire := any(answer)
		if patchMode {
			var config map[string]any
			if json.Unmarshal(payload["output_config"], &config) != nil {
				t.Error("invalid output schema")
			}
			schema := config["format"].(map[string]any)["schema"].(map[string]any)
			props := schema["properties"].(map[string]any)
			index := props["edits"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["lesson_index"].(map[string]any)
			if props["content"] != nil || index["type"] != "integer" || !strings.Contains(string(payload["system"]), "ORIGINAL baseline") {
				t.Error("missing patch contract")
			}
			wire = skillDraftPatch{Edits: []skillDraftEdit{{OldText: "Check results.", NewText: "Check results before reporting completion.", LessonIndex: 0}}, Analysis: answer.Analysis}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "claude-sonnet-5-5", "stop_reason": "end_turn", "content": []map[string]string{{"type": "text", "text": structuredText(wire)}}, "usage": map[string]int{"input_tokens": 100, "output_tokens": 200}})
	}))
	defer server.Close()
	f.svc.Drafter = &AnthropicSkillDrafter{Service: f.svc, APIKey: "test", WorkspaceID: "workspace", Endpoint: server.URL, BudgetScope: scopeA}
	store := f.svc.Repo
	f.svc.Repo = preparedFailureRepo{store}
	if _, err := f.svc.GenerateSkill(ctx, testPrincipalToken("drafter"), in); err == nil {
		t.Fatal("candidate persist failure not injected")
	}
	if calls.Load() != 1 {
		t.Fatal("draft was not prepared")
	}
	f.svc.Repo = store
	preparedID := preparedDraftID(scopeA, "drafter", in.EventID)
	prepared, err := f.svc.Get(ctx, testPrincipalToken("drafter"), "sources", scopeA, preparedID)
	if err != nil {
		t.Fatal("prepared source unavailable", err)
	}
	if len(proposedLessons(prepared)) != len(in.Lessons) {
		t.Fatal("candidate lineage dropped")
	}
	if mode != "" {
		lesson, err := store.Get(ctx, "memory_candidates", scopeA, in.Lessons[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		old := lesson.Revision
		switch mode {
		case "erased":
			lesson.ContentErased = true
			lesson.Text = ""
		case "revision":
			lesson.Revision++
		case "withdrawn":
			lesson.Status = "rejected"
		}
		if err = store.Replace(ctx, "memory_candidates", lesson, old); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.Get(ctx, testPrincipalToken("drafter"), "sources", scopeA, preparedID); err == nil {
			t.Fatal("generic read exposed invalid derivative")
		}
		if _, err = f.svc.GenerateSkill(ctx, testPrincipalToken("drafter"), in); err == nil {
			t.Fatal("withdrawn evidence reused")
		}
		if calls.Load() != 1 {
			t.Fatal("invalid prepared result caused paid retry")
		}
		return
	}
	out, e := f.svc.GenerateSkill(ctx, testPrincipalToken("drafter"), in)
	if e != nil {
		t.Fatal(e, "calls", calls.Load())
	}
	if out.Record.Status != "candidate" || out.Record.Generator.Model != "claude-sonnet-5-5" {
		t.Fatal("draft gained authority", out)
	}
	if patchMode {
		want := strings.Replace(baselineContent, "Check results.", "Check results before reporting completion.", 1)
		current, err := f.svc.Get(ctx, testPrincipalToken("drafter"), colSkills, scopeA, in.SkillID)
		if err != nil || current.ActiveVersion != baseline.ID || out.Record.BaseVersion != baseline.ID || out.Record.Analysis.ContentHash != ContentHash(want) {
			t.Fatal("baseline changed or candidate lost unrelated detail", err)
		}
		var saved SkillDraft
		if json.Unmarshal([]byte(prepared.Text), &saved) != nil || saved.Content != want || strings.Contains(prepared.Text, `"edits"`) {
			t.Fatal("prepared recovery did not preserve canonical content")
		}
	}
	replay, e := f.svc.GenerateSkill(ctx, testPrincipalToken("drafter"), in)
	if e != nil || replay.Record.ID != out.Record.ID || calls.Load() != 1 {
		t.Fatal("replay", e, calls.Load())
	}
	status, e := f.svc.ModelBudgetStatus(ctx, testPrincipalToken("drafter"), scopeA)
	if e != nil || status.Outstanding != 0 || status.MonthlyCommitted != 100*2000000+200*10000000 {
		t.Fatal(status, e)
	}
}
func TestAnthropicSkillDraftCannotBypassAuthorizedEntry(t *testing.T) {
	a := &AnthropicSkillDrafter{}
	if _, _, e := a.Draft(testCtx, SkillDraftInput{}); e == nil {
		t.Fatal("unauthorized draft")
	}
}

func TestAnthropicSkillDraftFailureAccountingAndTransferFence(t *testing.T) {
	for _, mode := range []string{"transfer-denied", "truncated", "unknown-usage", "missing-model", "http-error", "cache-usage", "rewrite-baseline"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			in, _ := generationFixture(t, f)
			if mode == "rewrite-baseline" {
				base, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: in.SkillID, Path: in.Path, Content: "# Original procedure", Analysis: detailedSkillAnalysis()})
				if err != nil {
					t.Fatal(err)
				}
				in.ComparisonPlan.BaselineVersion = base.Record.ID
			}
			verbs := append(append([]string{}, allVerbs...), "reserve", "settle", "read")
			if mode != "transfer-denied" {
				verbs = append(verbs, "transfer")
			}
			f.auth.actors[testPrincipalToken("drafter")] = actor("drafter", roleService, true, verbs...)
			f.svc.ModelBudgetPolicy = &ModelBudgetPolicy{Scope: scopeA, Principals: []string{"drafter"}, MonthlyPicoUSD: 100 * picoDollar, GeneralPicoUSD: 90 * picoDollar, DailyPicoUSD: 3 * picoDollar, ExpiresAt: time.Now().Add(time.Hour)}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					if mode == "missing-model" {
						w.Write([]byte(`{"data":[]}`))
					} else {
						w.Write([]byte(`{"data":[{"id":"claude-sonnet-5-5"}]}`))
					}
					return
				}
				calls.Add(1)
				switch mode {
				case "http-error":
					w.WriteHeader(http.StatusInternalServerError)
					return
				case "cache-usage":
					w.Write([]byte(`{"model":"claude-sonnet-5-5","stop_reason":"end_turn","content":[],"usage":{"input_tokens":100,"output_tokens":200,"cache_read_input_tokens":5}}`))
					return
				case "rewrite-baseline":
					_, answer := draftFixture()
					_ = json.NewEncoder(w).Encode(map[string]any{"model": "claude-sonnet-5-5", "stop_reason": "end_turn", "content": []map[string]string{{"type": "text", "text": structuredText(answer)}}, "usage": map[string]int{"input_tokens": 100, "output_tokens": 200}})
					return
				}
				if mode == "unknown-usage" {
					w.Write([]byte(`{"model":"claude-sonnet-5-5","stop_reason":"end_turn","content":[]}`))
					return
				}
				w.Write([]byte(`{"model":"claude-sonnet-5-5","stop_reason":"max_tokens","content":[],"usage":{"input_tokens":100,"output_tokens":200}}`))
			}))
			defer server.Close()
			f.svc.Drafter = &AnthropicSkillDrafter{Service: f.svc, APIKey: "test", WorkspaceID: "workspace", Endpoint: server.URL, BudgetScope: scopeA}
			if _, e := f.svc.GenerateSkill(testCtx, testPrincipalToken("drafter"), in); e == nil {
				t.Fatal("unsafe output accepted")
			}
			status, e := f.svc.ModelBudgetStatus(testCtx, testPrincipalToken("drafter"), scopeA)
			if e != nil {
				t.Fatal(e)
			}
			reservations, e := f.svc.Repo.ListRecords(testCtx, scopeA, "jobs", 100)
			if e != nil {
				t.Fatal(e)
			}
			for _, row := range reservations {
				if row.Kind == "model_budget_reservation" && row.Status == JobStatusPending {
					t.Fatal("failed dispatch left reservation pending")
				}
			}
			if (mode == "unknown-usage" || mode == "http-error" || mode == "cache-usage") && status.Outstanding == 0 {
				t.Fatal("uncertain spend released")
			}
			if (mode == "truncated" || mode == "rewrite-baseline") && (status.Outstanding != 0 || status.MonthlyCommitted == 0) {
				t.Fatal("known truncated spend lost")
			}
			if mode == "rewrite-baseline" {
				for _, row := range f.repo.rows(colSkillVersions) {
					if row.Kind == proposedSkillKind {
						t.Fatal("rewrite persisted as a candidate")
					}
				}
			}
			if (mode == "transfer-denied" || mode == "missing-model") && (calls.Load() != 0 || status.MonthlyCommitted != 0) {
				t.Fatal("dispatch before authorization/discovery")
			}
			f.svc.GenerateSkill(testCtx, testPrincipalToken("drafter"), in)
			if calls.Load() > 1 {
				t.Fatal("failure retry dispatched again")
			}
		})
	}
}

// Candidate transaction fails after the separate canonical prepared source commit.
type preparedFailureRepo struct{ Repository }
type preparedFailureTx struct{ Tx }

func (r preparedFailureRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, preparedFailureTx{tx}) })
}
func (tx preparedFailureTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "skill_versions" {
		return ErrUnavailable
	}
	return tx.Tx.Insert(ctx, col, r)
}

func TestPostgresAnthropicPreparedPrivacyAppRole(t *testing.T) {
	for _, mode := range []string{"", "erased", "revision", "withdrawn"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = isolatedPostgres(t)
			checkAnthropicPrepared(t, f, "pg:"+mode)
		})
	}
}
