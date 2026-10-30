package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// applicable zero windows persist until reset.
func TestRoutingSubscriptionExhaustion(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	zero, left := 0, 50
	reset := now.Add(time.Hour)
	base := ProviderAccount{ID: "a", Label: "a", Status: "connected", Default: true,
		UsageStatus: "ok", UsageCheckedAt: &now, Quotas: []ProviderQuota{{Window: "week", LeftPct: &zero, ResetsAt: &reset}}}
	for _, tc := range []struct {
		name    string
		edit    func(*ProviderAccount)
		blocked bool
	}{
		{"zero future reset", func(a *ProviderAccount) {}, true},
		{"unknown reset", func(a *ProviderAccount) { a.Quotas[0].ResetsAt = nil }, true},
		{"reset passed", func(a *ProviderAccount) { z := now.Add(-time.Second); a.Quotas[0].ResetsAt = &z }, false},
		{"reset now", func(a *ProviderAccount) { a.Quotas[0].ResetsAt = &now }, false},
		{"remaining", func(a *ProviderAccount) { a.Quotas[0].LeftPct = &left }, false},
		{"unknown left", func(a *ProviderAccount) { a.Quotas[0].LeftPct = nil }, false},
		{"no windows", func(a *ProviderAccount) { a.Quotas = nil }, false},
		{"legacy quota", func(a *ProviderAccount) { q := a.Quotas[0]; a.Quotas = nil; a.Quota = &q }, true},
		{"quotas supersede legacy alias", func(a *ProviderAccount) { q := a.Quotas[0]; a.Quota = &q; a.Quotas[0].LeftPct = &left }, false},
		{"model window", func(a *ProviderAccount) { a.Quotas[0].Model = "sonnet" }, true},
		{"other model window", func(a *ProviderAccount) { a.Quotas[0].Model = "opus" }, false},
		{"unknown alongside exhausted window", func(a *ProviderAccount) { a.Quotas = append(a.Quotas, ProviderQuota{Window: "5h"}) }, true},
		{"paid credits do not reset window", func(a *ProviderAccount) {
			n, yes := 20.0, true
			a.Credits = &ProviderCredits{Balance: &n, Unlimited: &yes}
		}, true},
		{"zero credits alone", func(a *ProviderAccount) {
			n := 0.0
			a.Credits = &ProviderCredits{Balance: &n}
			a.Quotas[0].LeftPct = &left
		}, false},
		{"legacy usage unknown", func(a *ProviderAccount) { a.UsageStatus = "" }, false},
		{"unavailable without confirmed sample", func(a *ProviderAccount) { a.UsageStatus = "unavailable" }, false},
		{"expired usage", func(a *ProviderAccount) { a.UsageStatus = "expired" }, false},
		{"no checked time", func(a *ProviderAccount) { a.UsageCheckedAt = nil }, false},
		{"stale", func(a *ProviderAccount) { z := now.Add(-2*time.Minute - time.Nanosecond); a.UsageCheckedAt = &z }, true},
		{"stale unknown reset", func(a *ProviderAccount) {
			z := now.Add(-3 * time.Minute)
			a.UsageCheckedAt = &z
			a.Quotas[0].ResetsAt = nil
		}, false},
		{"stale reset passed", func(a *ProviderAccount) {
			z := now.Add(-3 * time.Minute)
			a.UsageCheckedAt = &z
			a.Quotas[0].ResetsAt = &now
		}, false},
		{"failed refresh unknown reset", func(a *ProviderAccount) { a.UsageStatus = "unavailable"; a.Quotas[0].ResetsAt = nil }, false},
		{"two minute boundary", func(a *ProviderAccount) { z := now.Add(-2 * time.Minute); a.UsageCheckedAt = &z }, true},
		{"small clock skew", func(a *ProviderAccount) { z := now.Add(5 * time.Second); a.UsageCheckedAt = &z }, true},
		{"future checked invalid", func(a *ProviderAccount) { z := now.Add(5*time.Second + time.Nanosecond); a.UsageCheckedAt = &z }, false},
		{"API mode account", func(a *ProviderAccount) { a.Default = false }, false},
		{"missing login only", func(a *ProviderAccount) { a.Status = "missing" }, false},
		{"expired login only", func(a *ProviderAccount) { a.Status = "expired" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			a.Quotas = append([]ProviderQuota{}, base.Quotas...)
			tc.edit(&a)
			p := &Providers{Providers: []Provider{{ID: "anthropic", Kind: "subscription", Accounts: []ProviderAccount{a}}}}
			if got := subscriptionExhausted(p, "anthropic", "sonnet 5.5", now); got != tc.blocked {
				t.Fatalf("exhausted = %v, want %v", got, tc.blocked)
			}
		})
	}
	for _, other := range []struct {
		name           string
		status         string
		defaultAccount bool
		remaining      *int
		blocked        bool
	}{
		{"second exhausted", "connected", true, &zero, true},
		{"second usable", "connected", true, &left, false},
		{"second unknown", "connected", true, nil, false},
		{"nondefault usable", "connected", false, &left, true},
		{"missing ignored", "missing", true, &left, true},
		{"expired ignored", "expired", true, &left, true},
	} {
		t.Run(other.name, func(t *testing.T) {
			a := base
			a.ID, a.Status, a.Default = "b", other.status, other.defaultAccount
			a.Quotas = []ProviderQuota{{Window: "5h", LeftPct: other.remaining, ResetsAt: &reset}}
			p := &Providers{Providers: []Provider{{ID: "anthropic", Kind: "subscription", Accounts: []ProviderAccount{base, a}}}}
			if got := subscriptionExhausted(p, "anthropic", "sonnet 5.5", now); got != other.blocked {
				t.Fatal("multi account exhaustion", got)
			}
			p.Providers[0].Kind = "api"
			if subscriptionExhausted(p, "anthropic", "sonnet 5.5", now) {
				t.Fatal("API provider gated by subscriptions")
			}
		})
	}
}

func TestRoutingQuotaModelBoundaries(t *testing.T) {
	for _, tc := range []struct {
		window, model string
		matches       bool
	}{
		{"", "gpt-6.1", true}, {"sonnet", "sonnet", true}, {"sonnet", "sonnet 5.5", true},
		{"sonnet", "claude-sonnet-5-5", true}, {"opus", "OPUS 5.5", true}, {"fable", "claude-fable-5-1", true},
		{"claude-sonnet", "sonnet-5-5", true}, {"gpt-6.1", "gpt-6.1-mini", true},
		{"sonnet", "sonnetish", false}, {"opus", "opusology", false}, {"fable", "fablex", false},
		{"sonnet", "not-sonnet-5-5", false}, {"sonnet", "opus 5.5", false}, {"gpt-6.1", "gpt-6.10", false},
		{"claude-", "opus 5.5", false},
	} {
		if got := quotaMatchesModel(tc.window, tc.model); got != tc.matches {
			t.Errorf("quota %q model %q = %v", tc.window, tc.model, got)
		}
	}
}

func TestRoutingUsesSubscriptionUsageOnlyForDecideCandidates(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "handoff", "route", "write")
	now, zero := time.Now().UTC(), 0
	account := ProviderAccount{ID: "a", Label: "a", Status: "connected", Default: true, UsageStatus: "ok", UsageCheckedAt: &now,
		Quotas: []ProviderQuota{{Window: "week", Model: "sonnet", LeftPct: &zero}}}
	inv := []Provider{
		{ID: "anthropic", Name: "Anthropic", Kind: "subscription", Accounts: []ProviderAccount{account}, Models: []string{"sonnet 5.5", "opus 5.5"}},
		{ID: "openai", Name: "OpenAI", Kind: "api", Accounts: []ProviderAccount{account}, Models: []string{"gpt-6.1"}},
	}
	if _, err := f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &inv}); err != nil {
		t.Fatal(err)
	}
	fixed, off := "anthropic/sonnet 5.5", false
	policy := RoutingPolicyInput{Scope: scopeA, DefaultModel: &fixed, AutoRouting: &off}
	if _, err := f.svc.SetRoutingPolicy(testCtx, f.human, policy); err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "review"})
	if err != nil || d.ID != "anthropic/opus 5.5" || d.Decision != "fixed" || len(d.Fallbacks) != 1 || d.Fallbacks[0] != "openai/gpt-6.1" {
		t.Fatal("fixed/fallback candidates", d, err)
	}
	page, err := f.svc.RoutingModels(testCtx, f.human, scopeA)
	if err != nil || len(page.Items) != 3 || !page.Items[0].Enabled {
		t.Fatal("quota changed UI enabled state", page, err)
	}
	on := true
	policy.AutoRouting, policy.ExpectedRevision = &on, 1
	if _, err = f.svc.SetRoutingPolicy(testCtx, f.human, policy); err != nil {
		t.Fatal(err)
	}
	picker := &fakePicker{choice: "openai/gpt-6.1"}
	f.svc.ModelPicker = picker
	d, err = f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "review"})
	if err != nil || d.Decision != "clef" || len(picker.seen) != 2 || picker.seen[0].ID != "anthropic/opus 5.5" {
		t.Fatal("Clef candidates", d, picker.seen, err)
	}
	inv[0].Accounts[0].Quotas[0].Model = ""
	inv[1].Accounts[0].Quotas = []ProviderQuota{{Window: "week", LeftPct: &zero}}
	if _, err = f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &inv}); err != nil {
		t.Fatal(err)
	}
	if d, err = f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "review"}); err != nil || d.ID != "openai/gpt-6.1" || len(d.Fallbacks) != 0 {
		t.Fatal("API remains available", d, err)
	}
	if _, err = f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "review", Providers: []string{"anthropic"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("all subscription candidates exhausted", err)
	}
	inv[0].Accounts[0].UsageStatus = "unavailable"
	if _, err = f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &inv}); err != nil {
		t.Fatal(err)
	}
	if d, err = f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "review", Providers: []string{"anthropic"}}); err != nil || d.ID != fixed || len(d.Fallbacks) != 1 {
		t.Fatal("failed usage report should restore unknown candidates", d, err)
	}
}

type fakePicker struct {
	choice string
	seen   []ModelOption
	err    error
}

func (p *fakePicker) PickModel(_ context.Context, _ string, options []ModelOption) (string, ProviderUsage, error) {
	p.seen = options
	return p.choice, ProviderUsage{Provider: "cloudflare", Model: "clef-flash", Requests: 1}, p.err
}

func TestRoutingUsesEnabledModelsClefPicksAndCallsAddUp(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "handoff", "route", "write")
	inv := []Provider{
		{ID: "openai", Name: "OpenAI", Kind: "subscription", Accounts: []ProviderAccount{{ID: "a", Label: "a", Status: "connected"}}, Keys: []ProviderKey{}, Models: []string{"gpt-6.1", "gpt-6.1-mini"}},
		{ID: "xai", Name: "xAI", Kind: "subscription", Accounts: []ProviderAccount{{ID: "b", Label: "b", Status: "connected"}}, Keys: []ProviderKey{}, Models: []string{"grok-5"}},
	}
	if _, err := f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &inv}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SwitchProviderModel(testCtx, f.human, ProviderModelSwitch{Scope: scopeA, Provider: "openai", Model: "gpt-6.1-mini"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.RoutingModels(testCtx, f.human, scopeA)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "openai/gpt-6.1" || page.Items[1].Family != "XAI" || page.Items[0].Revision != 0 {
		t.Fatal("models", page, err)
	}

	when := "긴 코드 리뷰"
	in := RoutingModelInput{Scope: scopeA, RoutingModel: RoutingModel{When: when}}
	if _, err = f.svc.SetRoutingModel(testCtx, f.agent, "xai/grok-5", in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent changed routing", err)
	}
	if _, err = f.svc.SetRoutingModel(testCtx, f.human, "openai/gpt-6.1-mini", in); !errors.Is(err, ErrNotFound) {
		t.Fatal("a model turned off in 모델 설정 took routing settings", err)
	}
	if _, err = f.svc.SetRoutingModel(testCtx, f.human, "xai/grok-5", in); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.SetRoutingModel(testCtx, f.human, "xai/grok-5", in); !errors.Is(err, ErrConflict) {
		t.Fatal("a stale revision was accepted", err)
	}

	picker := &fakePicker{choice: "xai/grok-5"}
	f.svc.ModelPicker = picker
	d, err := f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "이 PR을 리뷰해 줘"})
	if err != nil || d.ID != "xai/grok-5" || d.Model != "grok-5" || d.Decision != "clef" || len(d.Fallbacks) != 1 || d.Fallbacks[0] != "openai/gpt-6.1" {
		t.Fatal("decide", d, err)
	}
	if len(picker.seen) != 2 || picker.seen[1].When != when {
		t.Fatal("Clef did not see the when notes", picker.seen)
	}
	picker.err = ErrUnavailable
	if d, _ = f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "x"}); d.Decision != "default" || d.ID != "openai/gpt-6.1" {
		t.Fatal("Clef down should fall back to the first model", d)
	}
	if d, _ = f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "x", Providers: []string{"openai"}}); d.ID != "openai/gpt-6.1" || len(d.Fallbacks) != 0 {
		t.Fatal("providers should narrow the candidates", d)
	}
	off, fixed := false, "xai/grok-5"
	if _, err = f.svc.SetRoutingPolicy(testCtx, f.human, RoutingPolicyInput{Scope: scopeA, ExpectedRevision: 1, AutoRouting: &off, DefaultModel: &fixed}); err != nil {
		t.Fatal(err)
	}
	if d, _ = f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "x"}); d.Decision != "fixed" || d.ID != fixed {
		t.Fatal("auto routing off should send everything to the fixed model", d)
	}

	cost := 0.02
	for _, c := range []RoutingCall{
		{Model: "xai/grok-5", Decision: "clef", OK: false, LatencyMS: 900, ClassifierMS: 300, Error: "exit_1"},
		{Model: "openai/gpt-6.1", Decision: "failover", Replaced: "xai/grok-5", OK: true, LatencyMS: 1200, CostUSD: &cost},
		{Model: "openai/gpt-6.1", Decision: "fixed", OK: true, LatencyMS: 400},
	} {
		if _, err = f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: c}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: RoutingCall{Model: "xai/grok-5", Decision: "guess"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("an unknown decision was recorded", err)
	}
	ov, err := f.svc.RoutingOverview(testCtx, f.human, scopeA, "1h")
	if err != nil {
		t.Fatal(err)
	}
	k := ov["kpi"].(map[string]any)
	if k["requests"] != 3 || k["failures"] != 1 || k["success_rate"] != 66.7 || k["cost_usd"] != 0.02 || k["p50_ms"] != int64(900) {
		t.Fatal("kpi", k)
	}
	fam := ov["families"].([]map[string]any)
	if fam[0]["family"] != "GPT" || fam[0]["requests"] != 2 || fam[1]["note"] != "Clef가 보낸 일 1건" {
		t.Fatal("families", fam)
	}
	pol := ov["policy"].(map[string]any)
	if pol["auto_routing"] != false || pol["revision"] != int64(2) || pol["classifier"] != "clef-flash" {
		t.Fatal("policy", pol)
	}
	if len(ov["events"].([]RoutingEvent)) < 3 || ov["series"].(map[string]any)["incident"] == nil {
		t.Fatal("events", ov["events"])
	}
	if _, err = f.svc.RoutingOverview(testCtx, f.human, scopeA, "2h"); !errors.Is(err, ErrInvalid) {
		t.Fatal("an unknown range", err)
	}
}

// The screen's save goes through HTTP with the id in the body.
func TestRoutingModelSaveOverHTTP(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("POST", "/v1/routing/models", strings.NewReader(`{"scope":{"workspace_id":"w","project_id":"p","user_id":"u","agent_id":"a"},"id":"xai/grok-5","expected_revision":0,"when":"x"}`))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not_found") {
		t.Fatal("a model not in 모델 설정 should be not_found", w.Code, w.Body.String())
	}
}

// Clef gets m0, m1, … as choice keys (it refuses "/") and the answer maps back to the model id.
func TestClefPickModelSendsPlainKeys(t *testing.T) {
	var got map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"result":{"model":"clef-flash","answers":{"model":{"type":"choice","choice":"m1","probabilities":{"m0":0.2,"m1":0.8},"confidence":0.6}},"usage":{"input_tokens":120}}}`))
	}))
	defer srv.Close()
	j := NewClefJudge("acct", "tok")
	j.Endpoint = srv.URL
	old := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = old }()
	id, usage, err := j.PickModel(testCtx, "리뷰", []ModelOption{{ID: "openai/gpt-6.1", Name: "GPT"}, {ID: "xai/grok-5", Name: "Grok", When: "뉴스"}})
	if err != nil || id != "xai/grok-5" || usage.InputTokens != 120 {
		t.Fatal(id, usage, err)
	}
	crit := got["questions"].(map[string]any)["model"].(map[string]any)["criteria"].(map[string]any)
	if crit["m0"] != "GPT" || crit["m1"] != "Grok: 뉴스" {
		t.Fatal("criteria", crit)
	}
}

func TestRoutingPrechoiceViewIsRecheckedAndHashedWithoutBodies(t *testing.T) {
	f, runner, src, pkg := prechoiceRoutingFixture(t, nil, "work-one")
	read, err := f.svc.GetWorkCheckpoint(testCtx, runner, WorkCheckpointGetInput{Scope: scopeA, ID: "work-one"})
	if err != nil {
		t.Fatal(err)
	}
	view := RoutingPrechoiceView{Checkpoint: RoutingViewCheckpoint{ID: read.ID, Revision: read.Revision, UpdatedAt: read.UpdatedAt},
		Refs: read.Package.SourceRefs, History: "known_empty"}
	decide := func(scope Scope, v RoutingPrechoiceView) (RoutingDecision, error) {
		return f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scope, Task: "review", View: &v})
	}
	// The same legal snapshot replays to the same hash; without a view nothing is claimed.
	a, err := decide(scopeA, view)
	if err != nil || !strings.HasPrefix(a.ViewHash, "sha256:") || len(a.ViewHash) != 71 {
		t.Fatal(a, err)
	}
	if b, err := decide(scopeA, view); err != nil || b.ViewHash != a.ViewHash {
		t.Fatal("replay", b, err)
	}
	if d, err := f.svc.DecideRoute(testCtx, runner, RoutingDecideInput{Scope: scopeA, Task: "review"}); err != nil || d.ViewHash != "" {
		t.Fatal("no view", d, err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), pkg.Title) || strings.Contains(string(raw), pkg.Goal) {
		t.Fatal("view carries a body", string(raw))
	}

	// A wrong history claim, an unknown history and a mismatched ref are not the stored work.
	expectViewErrors(t, decide, ErrConflict, map[string]RoutingPrechoiceView{
		"observed": func() RoutingPrechoiceView { w := view; w.History, w.HistoryCount = "observed", 1; return w }(),
		"ref": func() RoutingPrechoiceView {
			w := view
			w.Refs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision + 1}}
			return w
		}(),
		"no_refs": func() RoutingPrechoiceView { w := view; w.Refs = nil; return w }(),
		"updated_at": func() RoutingPrechoiceView {
			w := view
			w.Checkpoint.UpdatedAt = w.Checkpoint.UpdatedAt.Add(time.Millisecond)
			return w
		}(),
	})
	expectViewErrors(t, decide, ErrInvalid, map[string]RoutingPrechoiceView{
		"unknown":     func() RoutingPrechoiceView { w := view; w.History = "unknown"; return w }(),
		"empty_count": func() RoutingPrechoiceView { w := view; w.HistoryCount = 1; return w }(),
		"revision0":   func() RoutingPrechoiceView { w := view; w.Checkpoint.Revision = 0; return w }(),
		"no_time":     func() RoutingPrechoiceView { w := view; w.Checkpoint.UpdatedAt = time.Time{}; return w }(),
		"bad_id":      func() RoutingPrechoiceView { w := view; w.Checkpoint.ID = "../x"; return w }(),
	})
	// Other scope keeps the existing privacy-preserving not-found fence.
	if _, err := decide(scopeB, view); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign", err)
	}
	// A revision change after the read is stale, never retroactively accepted.
	pkg.NextSteps = []string{"색인 다시 확인"}
	if _, err := workCheckpointSave(f, f.agent, "work-one", "evt-2", 1, pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := decide(scopeA, view); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision", err)
	}
	// Deleted evidence keeps the checkpoint read fence: no hash for a view built on it.
	fresh, err := f.svc.GetWorkCheckpoint(testCtx, runner, WorkCheckpointGetInput{Scope: scopeA, ID: "work-one"})
	if err != nil {
		t.Fatal(err)
	}
	view.Checkpoint.Revision, view.Checkpoint.UpdatedAt = fresh.Revision, fresh.UpdatedAt
	if _, err := decide(scopeA, view); err != nil {
		t.Fatal("fresh view", err)
	}
	if err := f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision); err != nil {
		t.Fatal(err)
	}
	if d, err := decide(scopeA, view); err == nil || d.ViewHash != "" {
		t.Fatal("deleted evidence", d, err)
	}
	// A token that may route but not read work is denied, never handed an unchecked hash.
	f.auth.actors["tok-route-only"] = actor("router-2", "service", true, "route")
	if _, err := f.svc.DecideRoute(testCtx, "tok-route-only", RoutingDecideInput{Scope: scopeA, Task: "review", View: &view}); !errors.Is(err, ErrDenied) {
		t.Fatal("route-only token", err)
	}
}

func expectViewErrors(t *testing.T, decide func(Scope, RoutingPrechoiceView) (RoutingDecision, error), want error, cases map[string]RoutingPrechoiceView) {
	t.Helper()
	for name, v := range cases {
		if _, err := decide(scopeA, v); !errors.Is(err, want) {
			t.Fatal(name, err)
		}
	}
}

// prechoiceRoutingFixture saves work id with one live source ref; both scopes can route, so only the work
// fences refuse a foreign view. A nil repo keeps the offline store.
func prechoiceRoutingFixture(t *testing.T, repo Repository, id string) (*fixture, string, Record, WorkCheckpointPackage) {
	t.Helper()
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	runner := f.runner()
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "handoff", "route", "write")
	inv := []Provider{{ID: "openai", Name: "OpenAI", Kind: "api", Models: []string{"gpt-6.1", "gpt-6.1-mini"}}}
	for _, scope := range []Scope{scopeA, scopeB} {
		if _, err := f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scope, Providers: &inv}); err != nil {
			t.Fatal(err)
		}
	}
	src := f.source()
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	if _, err := workCheckpointSave(f, f.agent, id, "evt-1", 0, pkg); err != nil {
		t.Fatal(err)
	}
	return f, runner, src, pkg
}
