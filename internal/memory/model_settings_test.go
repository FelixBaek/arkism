package memory

import (
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testProviderKey = "sk-test-0123456789abcdWXYZ"

// CLI metadata is optional, bounded and credential-free.
func TestModelSettingsCLIInfoValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		info  *ProviderCLIInfo
		valid bool
	}{
		{"legacy", nil, true},
		{"unknown", &ProviderCLIInfo{}, true},
		{"version only", &ProviderCLIInfo{Version: "2.1.0"}, true},
		{"npm", &ProviderCLIInfo{Version: "2.1.0", Latest: "2.2.0", Via: "npm", Update: true}, true},
		{"brew", &ProviderCLIInfo{Version: "0.1.0-rc.1+build.02", Latest: "0.1.0", Via: "brew", Update: true}, true},
		{"self", &ProviderCLIInfo{Version: "2.1.0", Latest: "2.1.0", Via: "self"}, true},
		{"60 characters", &ProviderCLIInfo{Version: "1.0.0+" + strings.Repeat("a", 54)}, true},
		{"long version", &ProviderCLIInfo{Version: "1.0.0+" + strings.Repeat("a", 55)}, false},
		{"long latest", &ProviderCLIInfo{Latest: "1.0.0+" + strings.Repeat("a", 55)}, false},
		{"version prefix", &ProviderCLIInfo{Version: "v1.0.0"}, false},
		{"incomplete version", &ProviderCLIInfo{Version: "1.0"}, false},
		{"leading zero", &ProviderCLIInfo{Version: "01.0.0"}, false},
		{"leading zero prerelease", &ProviderCLIInfo{Latest: "1.0.0-01"}, false},
		{"shell version", &ProviderCLIInfo{Version: "1.0.0;echo"}, false},
		{"version whitespace", &ProviderCLIInfo{Version: "1.0.0\n"}, false},
		{"unknown via", &ProviderCLIInfo{Via: "pip"}, false},
		{"missing version", &ProviderCLIInfo{Latest: "2.2.0", Via: "npm", Update: true}, false},
		{"missing latest", &ProviderCLIInfo{Version: "2.1.0", Via: "npm", Update: true}, false},
		{"missing via", &ProviderCLIInfo{Version: "2.1.0", Latest: "2.2.0", Update: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := []Provider{{ID: "anthropic", Name: "Anthropic", Kind: "subscription", Accounts: []ProviderAccount{
				{ID: "claude", Label: "Claude", CLI: "claude", Status: "connected", CLIInfo: tc.info},
			}}}
			if got := validProviders(list); got != tc.valid {
				t.Fatalf("validProviders = %v, want %v", got, tc.valid)
			}
		})
	}
	f := newFixture(t)
	list := []Provider{{ID: "openai", Name: "OpenAI", Kind: "subscription", Accounts: []ProviderAccount{
		{ID: "codex", Label: "Codex", CLI: "codex", Status: "connected",
			CLIInfo: &ProviderCLIInfo{Version: "1.0.0", Latest: "1.1.0", Via: "npm", Update: true}},
	}}}
	r, err := f.svc.ReportProviders(testCtx, f.runner(), ProvidersReport{Scope: scopeA, Providers: &list})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	var got Record
	if err != nil || json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got.Providers.Providers[0].Accounts[0].CLIInfo, list[0].Accounts[0].CLIInfo) {
		t.Fatal("cli_info did not round trip", err)
	}
	list[0].Accounts[0].CLIInfo.Latest = "1.0.0-sk-AAAAAAAAAAAA"
	if _, err := f.svc.ReportProviders(testCtx, f.runner(), ProvidersReport{Scope: scopeA, Providers: &list}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("secret in semver metadata accepted", err)
	}
}

func cliUpdateFixture(t *testing.T) (*fixture, string, ProviderCLIUpdate) {
	t.Helper()
	f := newFixture(t)
	runner := f.runner()
	list := []Provider{
		{ID: "anthropic", Name: "Anthropic", Kind: "subscription", Accounts: []ProviderAccount{{ID: "claude", Label: "Claude", CLI: "claude", Status: "connected", CLIInfo: &ProviderCLIInfo{Version: "2.1.0", Latest: "2.2.0", Via: "npm", Update: true}}}},
		{ID: "openai", Name: "OpenAI", Kind: "subscription", Accounts: []ProviderAccount{{ID: "codex", Label: "Codex", CLI: "codex", Status: "connected", CLIInfo: &ProviderCLIInfo{Version: "2.1.0", Latest: "2.2.0", Via: "npm", Update: true}}}},
	}
	if _, err := f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &list}); err != nil {
		t.Fatal(err)
	}
	return f, runner, ProviderCLIUpdate{Scope: scopeA, Provider: "anthropic", CLI: "claude", RequestID: strings.Repeat("a", 32), ExpectedVersion: "2.1.0", ExpectedVia: "npm"}
}

// retries never enqueue, execute or audit twice.
func TestModelSettingsCLIUpdateHumanHandoffReportAndIdempotency(t *testing.T) {
	f, runner, in := cliUpdateFixture(t)
	for _, role := range []string{"agent", "pipeline", "service"} {
		f.auth.actors["other"] = actor("other", role, true, "write")
		if _, err := f.svc.UpdateProviderCLI(testCtx, "other", in); !errors.Is(err, ErrDenied) {
			t.Fatal("nonhuman requested update", role, err)
		}
	}
	f.auth.actors["reader"] = actor("reader", "human", true, "read")
	if _, err := f.svc.UpdateProviderCLI(testCtx, "reader", in); !errors.Is(err, ErrDenied) {
		t.Fatal("human without write:providers requested update", err)
	}
	updateAudits := func() int {
		n := 0
		for _, row := range f.repo.store["audit_events"] {
			if row.Kind == "update:provider_cli" {
				n++
			}
		}
		return n
	}
	r, err := f.svc.UpdateProviderCLI(testCtx, f.human, in)
	if err != nil || len(r.Providers.Requests) != 1 {
		t.Fatal("update request", r, err)
	}
	q := r.Providers.Requests[0]
	if q.ID != in.RequestID || q.Kind != "cli_update" || q.CLI != in.CLI || q.Provider != in.Provider || q.ExpectedVersion != in.ExpectedVersion || q.ExpectedVia != in.ExpectedVia || q.Status != "pending" || q.RequestedBy != "human-1" || q.RequestedAt.IsZero() {
		t.Fatal("wrong request", q)
	}
	if updateAudits() != 1 {
		t.Fatal("missing human audit")
	}
	for _, row := range f.repo.store["audit_events"] {
		if row.Kind != "update:provider_cli" {
			continue
		}
		if row.Kind != "update:provider_cli" || row.CreatedBy != "human-1" || row.Target.ID != providersID(scopeA) {
			t.Fatal("wrong audit", row)
		}
	}
	if _, err := f.svc.ProviderHandoff(testCtx, f.human, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("human collected handoff", err)
	}
	for range 2 {
		page, err := f.svc.ProviderHandoff(testCtx, runner, scopeA)
		if err != nil || len(page.Items) != 1 || page.Items[0].Key != "" || !reflect.DeepEqual(page.Items[0].Request, q) {
			t.Fatal("keyless handoff", page, err)
		}
	}
	for _, status := range []string{"pending", "done", "failed"} {
		stored := f.repo.store[colProviders][recKey(scopeA, r.ID)]
		stored.Providers.Requests[0].Status = status
		stored.Providers.Providers[0].Accounts[0].CLIInfo = &ProviderCLIInfo{Version: "2.2.0", Via: "self"}
		f.repo.store[colProviders][recKey(scopeA, r.ID)] = stored
		retry, err := f.svc.UpdateProviderCLI(testCtx, f.human, in)
		if err != nil || retry.Revision != r.Revision || len(retry.Providers.Requests) != 1 || retry.Providers.Requests[0].Status != status || updateAudits() != 1 {
			t.Fatal("retry changed request or checked new installation", retry, err)
		}
		for _, change := range []func(*ProviderCLIUpdate){
			func(v *ProviderCLIUpdate) { v.ExpectedVersion = "2.2.0" },
			func(v *ProviderCLIUpdate) { v.ExpectedVia = "self" },
			func(v *ProviderCLIUpdate) { v.Provider, v.CLI = "openai", "codex" },
		} {
			other := in
			change(&other)
			if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, other); !errors.Is(err, ErrConflict) {
				t.Fatal("reused ID accepted different input", err)
			}
		}
	}
	stored := f.repo.store[colProviders][recKey(scopeA, r.ID)]
	stored.Providers.Requests[0].Status = "pending"
	f.repo.store[colProviders][recKey(scopeA, r.ID)] = stored
	result := ProvidersReport{Scope: scopeA, Results: []ProviderResult{{ID: in.RequestID, Status: "done", Note: "updated 2.2.0"}}}
	if _, err := f.svc.ReportProviders(testCtx, f.human, result); !errors.Is(err, ErrDenied) {
		t.Fatal("human reported execution", err)
	}
	done, err := f.svc.ReportProviders(testCtx, runner, result)
	if err != nil || done.Providers.Requests[0].Status != "done" || done.Providers.Requests[0].DoneAt == nil || done.Providers.Requests[0].Note != result.Results[0].Note {
		t.Fatal("report", done, err)
	}
	page, err := f.svc.ProviderHandoff(testCtx, runner, scopeA)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("completed request handed off", page, err)
	}
}

func TestModelSettingsCLIUpdateConflictsAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ProviderAccount)
	}{
		{"different CLI", func(a *ProviderAccount) { a.CLI = "codex" }},
		{"legacy report", func(a *ProviderAccount) { a.CLIInfo = nil }},
		{"version changed", func(a *ProviderAccount) { a.CLIInfo.Version = "2.2.0" }},
		{"via changed", func(a *ProviderAccount) { a.CLIInfo.Via = "brew" }},
		{"update unavailable", func(a *ProviderAccount) { a.CLIInfo.Update = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, in := cliUpdateFixture(t)
			r := f.repo.store[colProviders][recKey(scopeA, providersID(scopeA))]
			tc.edit(&r.Providers.Providers[0].Accounts[0])
			if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, in); !errors.Is(err, ErrConflict) {
				t.Fatal("outdated installation accepted", err)
			}
		})
	}
	f, runner, in := cliUpdateFixture(t)
	r, err := f.svc.UpdateProviderCLI(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	other := in
	other.RequestID = strings.Repeat("b", 32)
	if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, other); !errors.Is(err, ErrConflict) {
		t.Fatal("two pending updates of the same CLI", err)
	}
	codex := other
	codex.Provider, codex.CLI = "openai", "codex"
	if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, codex); err != nil {
		t.Fatal("independent CLI blocked", err)
	}
	stored := f.repo.store[colProviders][recKey(scopeA, r.ID)]
	stored.Providers.Requests[0].RequestedAt = time.Now().Add(-15 * time.Minute)
	f.repo.store[colProviders][recKey(scopeA, r.ID)] = stored
	got, err := f.svc.Providers(testCtx, f.human, scopeA)
	if err != nil || got.Providers.Requests[0].Status != "pending" {
		t.Fatal("CLI update expired at the key TTL", got, err)
	}
	stored.Providers.Requests[0].RequestedAt = time.Now().Add(-providerCLIUpdateTTL - time.Second)
	f.repo.store[colProviders][recKey(scopeA, r.ID)] = stored
	got, err = f.svc.Providers(testCtx, f.human, scopeA)
	if err != nil || got.Providers.Requests[0].Status != "expired" {
		t.Fatal("CLI update did not expire", got, err)
	}
	page, err := f.svc.ProviderHandoff(testCtx, runner, scopeA)
	if err != nil || len(page.Items) != 1 || page.Items[0].Request.CLI != "codex" {
		t.Fatal("expired update handed off", page, err)
	}
	other.RequestID = strings.Repeat("c", 32)
	got, err = f.svc.UpdateProviderCLI(testCtx, f.human, other)
	if err != nil || len(got.Providers.Requests) != 3 || got.Providers.Requests[0].Status != "expired" {
		t.Fatal("expired pending blocked a new request", got, err)
	}
	if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, ProviderCLIUpdate{Scope: scopeB, Provider: in.Provider, CLI: in.CLI, RequestID: in.RequestID, ExpectedVersion: in.ExpectedVersion, ExpectedVia: in.ExpectedVia}); !errors.Is(err, ErrConflict) {
		t.Fatal("installation leaked across scopes", err)
	}
}

func TestModelSettingsCLIUpdatePreservesPendingAndAuditRollback(t *testing.T) {
	f, _, in := cliUpdateFixture(t)
	stored := f.repo.store[colProviders][recKey(scopeA, providersID(scopeA))]
	for range providerRequestsKept {
		stored.Providers.Requests = append(stored.Providers.Requests, ProviderRequest{ID: ID(), Kind: "key_remove", Status: "pending"})
	}
	f.repo.store[colProviders][recKey(scopeA, stored.ID)] = stored
	if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, in); !errors.Is(err, ErrConflict) {
		t.Fatal("full pending queue accepted new work", err)
	}
	if len(f.repo.store["audit_events"]) != 0 || len(f.repo.store[colProviders][recKey(scopeA, stored.ID)].Providers.Requests) != providerRequestsKept {
		t.Fatal("rejected request changed queue or audit")
	}
	first := stored.Providers.Requests[0]
	stored.Providers.Requests[1].Status = "done"
	f.repo.store[colProviders][recKey(scopeA, stored.ID)] = stored
	r, err := f.svc.UpdateProviderCLI(testCtx, f.human, in)
	if err != nil || len(r.Providers.Requests) != providerRequestsKept || !reflect.DeepEqual(r.Providers.Requests[0], first) || r.Providers.Requests[providerRequestsKept-1].ID != in.RequestID {
		t.Fatal("pending request evicted instead of completed request", r, err)
	}
	if retry, err := f.svc.UpdateProviderCLI(testCtx, f.human, in); err != nil || retry.Revision != r.Revision {
		t.Fatal("full queue rejected idempotent retry", err)
	}
	if _, err := f.svc.RemoveProviderKey(testCtx, f.human, ProviderKeyRemoval{Scope: scopeA, Provider: "openai", KeyID: "another"}); !errors.Is(err, ErrConflict) {
		t.Fatal("old key action evicted pending CLI update", err)
	}
	f, _, in = cliUpdateFixture(t)
	f.repo.failAudit = true
	if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, in); !errors.Is(err, ErrUnavailable) {
		t.Fatal("audit failure accepted update", err)
	}
	if len(f.repo.store[colProviders][recKey(scopeA, providersID(scopeA))].Providers.Requests) != 0 {
		t.Fatal("audit rollback left request")
	}
}

func TestModelSettingsCLIUpdateValidationAndHTTPMapping(t *testing.T) {
	f, _, in := cliUpdateFixture(t)
	for _, change := range []func(*ProviderCLIUpdate){
		func(v *ProviderCLIUpdate) { v.Scope = Scope{} },
		func(v *ProviderCLIUpdate) { v.Provider = "gemini" },
		func(v *ProviderCLIUpdate) { v.CLI = "codex" },
		func(v *ProviderCLIUpdate) { v.RequestID = strings.Repeat("A", 32) },
		func(v *ProviderCLIUpdate) { v.RequestID = strings.Repeat("a", 31) },
		func(v *ProviderCLIUpdate) { v.RequestID = strings.Repeat("a", 33) },
		func(v *ProviderCLIUpdate) { v.ExpectedVersion = "" },
		func(v *ProviderCLIUpdate) { v.ExpectedVersion = "2.1" },
		func(v *ProviderCLIUpdate) { v.ExpectedVersion = "1.0.0+" + strings.Repeat("a", 55) },
		func(v *ProviderCLIUpdate) { v.ExpectedVia = "" },
		func(v *ProviderCLIUpdate) { v.ExpectedVia = "pip" },
	} {
		bad := in
		change(&bad)
		if _, err := f.svc.UpdateProviderCLI(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad request accepted", bad, err)
		}
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token, body, code string
		status                  int
	}{
		{"agent", f.agent, string(body), "access_denied", 403},
		{"unknown command", f.human, strings.TrimSuffix(string(body), "}") + `,"command":"npm install"}`, "invalid_request", 400},
		{"unknown scope field", f.human, strings.Replace(string(body), `"workspace_id":`, `"unknown":true,"workspace_id":`, 1), "invalid_request", 400},
		{"secret semver", f.human, strings.Replace(string(body), "2.1.0", "1.0.0-sk-AAAAAAAAAAAA", 1), "secret_detected", 422},
		{"changed version", f.human, strings.Replace(string(body), "2.1.0", "2.0.0", 1), "revision_conflict", 409},
		{"enqueue", f.human, string(body), "", 200},
		{"retry", f.human, string(body), "", 200},
		{"reused ID", f.human, strings.Replace(string(body), "2.1.0", "2.2.0", 1), "revision_conflict", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/providers/cli_update", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d: %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatal("wrong error mapping", w.Body.String())
			}
		})
	}
	runner := f.runner()
	for _, fields := range []string{`"update":"true"`, `"version":12`, `"latest":true`, `"via":12`, `"unknown":true`} {
		report := `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"providers":[{"id":"anthropic","name":"Anthropic","kind":"subscription","accounts":[{"id":"a","label":"a","status":"connected","cli_info":{` + fields + `}}]}]}`
		req := httptest.NewRequest("POST", "/v1/providers", strings.NewReader(report))
		req.Header.Set("Authorization", "Bearer "+runner)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("accepted malformed cli_info %s: HTTP %d", fields, w.Code)
		}
	}
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]struct {
			Post struct {
				Responses   map[string]json.RawMessage `json:"responses"`
				RequestBody struct {
					Content map[string]struct {
						Schema struct {
							Properties           map[string]json.RawMessage `json:"properties"`
							Required             []string                   `json:"required"`
							AdditionalProperties bool                       `json:"additionalProperties"`
						} `json:"schema"`
					} `json:"content"`
				} `json:"requestBody"`
			} `json:"post"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	api := spec.Paths["/v1/providers/cli_update"].Post
	for _, status := range []string{"200", "400", "403", "409", "422"} {
		if len(api.Responses[status]) == 0 {
			t.Fatal("OpenAPI missing response", status)
		}
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatal(err)
	}
	schema := api.RequestBody.Content["application/json"].Schema
	if schema.AdditionalProperties || len(schema.Properties) != len(input) || len(schema.Required) != len(input) {
		t.Fatal("OpenAPI input shape differs from API")
	}
	for _, field := range schema.Required {
		if len(input[field]) == 0 || len(schema.Properties[field]) == 0 {
			t.Fatal("OpenAPI field differs from API", field)
		}
	}
}

// old reports and optional usage values share the same boundary.
func TestModelSettingsSubscriptionUsageValidation(t *testing.T) {
	now := time.Now().UTC()
	zero, unlimited, balance := 0, false, 0.0
	quota := ProviderQuota{Window: "week", Model: "sonnet", LeftPct: &zero}
	base := ProviderAccount{ID: "claude", Label: "Claude", Status: "connected", Default: true,
		Plan: "Max", UsageStatus: "ok", UsageCheckedAt: &now, Credits: &ProviderCredits{Balance: &balance, Unlimited: &unlimited}, Quotas: []ProviderQuota{quota}}
	for _, tc := range []struct {
		name  string
		edit  func(*ProviderAccount)
		valid bool
	}{
		{"full", func(a *ProviderAccount) {}, true},
		{"legacy", func(a *ProviderAccount) {
			*a = ProviderAccount{ID: "claude", Label: "Claude", Status: "connected", Quota: &ProviderQuota{Window: "5h"}}
		}, true},
		{"unavailable", func(a *ProviderAccount) { a.UsageStatus = "unavailable" }, true},
		{"expired", func(a *ProviderAccount) { a.UsageStatus = "expired" }, true},
		{"empty credits", func(a *ProviderAccount) { a.Credits = &ProviderCredits{} }, true},
		{"unlimited", func(a *ProviderAccount) { yes := true; a.Credits = &ProviderCredits{Unlimited: &yes} }, true},
		{"positive balance", func(a *ProviderAccount) { n := 12.5; a.Credits = &ProviderCredits{Balance: &n} }, true},
		{"plan 60 characters", func(a *ProviderAccount) { a.Plan = strings.Repeat("가", 60) }, true},
		{"plan too long", func(a *ProviderAccount) { a.Plan = strings.Repeat("가", 61) }, false},
		{"plan control", func(a *ProviderAccount) { a.Plan = "Max\nPlan" }, false},
		{"plan whitespace", func(a *ProviderAccount) { a.Plan = " Max" }, false},
		{"unknown usage status", func(a *ProviderAccount) { a.UsageStatus = "failed" }, false},
		{"usage status whitespace", func(a *ProviderAccount) { a.UsageStatus = "ok " }, false},
		{"zero checked time", func(a *ProviderAccount) { z := time.Time{}; a.UsageCheckedAt = &z }, false},
		{"non UTC checked time", func(a *ProviderAccount) { z := now.In(time.FixedZone("KST", 9*3600)); a.UsageCheckedAt = &z }, false},
		{"checked year overflow", func(a *ProviderAccount) { z := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC); a.UsageCheckedAt = &z }, false},
		{"negative balance", func(a *ProviderAccount) { n := -1.0; a.Credits = &ProviderCredits{Balance: &n} }, false},
		{"NaN balance", func(a *ProviderAccount) { n := math.NaN(); a.Credits = &ProviderCredits{Balance: &n} }, false},
		{"infinite balance", func(a *ProviderAccount) { n := math.Inf(1); a.Credits = &ProviderCredits{Balance: &n} }, false},
		{"negative infinite balance", func(a *ProviderAccount) { n := math.Inf(-1); a.Credits = &ProviderCredits{Balance: &n} }, false},
		{"model 40 characters", func(a *ProviderAccount) { a.Quotas[0].Model = strings.Repeat("m", 40) }, true},
		{"model too long", func(a *ProviderAccount) { a.Quotas[0].Model = strings.Repeat("m", 41) }, false},
		{"model control", func(a *ProviderAccount) { a.Quotas[0].Model = "sonnet\n" }, false},
		{"model syntax", func(a *ProviderAccount) { a.Quotas[0].Model = "sonnet;" }, false},
		{"legacy model invalid", func(a *ProviderAccount) { q := quota; q.Model = " opus"; a.Quota = &q }, false},
		{"12 windows and legacy alias", func(a *ProviderAccount) {
			a.Quotas = make([]ProviderQuota, 12)
			for i := range a.Quotas {
				a.Quotas[i] = quota
			}
			a.Quota = &quota
		}, true},
		{"13 windows", func(a *ProviderAccount) {
			a.Quotas = make([]ProviderQuota, 13)
			for i := range a.Quotas {
				a.Quotas[i] = quota
			}
		}, false},
		{"invalid percent", func(a *ProviderAccount) { n := 101; a.Quotas[0].LeftPct = &n }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			a.Quotas = append([]ProviderQuota{}, base.Quotas...)
			tc.edit(&a)
			p := []Provider{{ID: "anthropic", Name: "Anthropic", Kind: "subscription", Accounts: []ProviderAccount{a}}}
			if got := validProviders(p); got != tc.valid {
				t.Fatalf("validProviders = %v, want %v", got, tc.valid)
			}
		})
	}
	p := []Provider{{ID: "anthropic", Name: "Anthropic", Kind: "subscription", Accounts: []ProviderAccount{base, base}}}
	if validProviders(p) {
		t.Fatal("duplicate account ID accepted")
	}
	p[0].Accounts = p[0].Accounts[:1]
	p = append(p, Provider{ID: "other", Name: "Other", Kind: "api", Accounts: []ProviderAccount{base}})
	if !validProviders(p) {
		t.Fatal("account IDs must be unique within a provider")
	}

	f := newFixture(t)
	runner := f.runner()
	r, err := f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &p})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Record
	if err = json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded.Providers.Providers[0].Accounts[0], base) {
		t.Fatal("optional usage fields did not round trip", err)
	}
	p[0].Accounts[0].Plan = "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err = f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &p}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("secret in the new plan field accepted", err)
	}
}

func TestModelSettingsSubscriptionUsageRejectsMalformedJSON(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	for _, fields := range []string{
		`"plan":12`, `"usage_status":true`, `"usage_checked_at":"yesterday"`,
		`"credits":{"balance":"0"}`, `"credits":{"unlimited":1}`, `"credits":{"unknown":true}`,
		`"quotas":[{"window":"week","model":12}]`,
	} {
		body := `{"scope":{"workspace_id":"w","project_id":"p","user_id":"u","agent_id":"a"},"providers":[{"id":"anthropic","name":"Anthropic","kind":"subscription","accounts":[{"id":"a","label":"a","status":"connected",` + fields + `}]}]}`
		req := httptest.NewRequest("POST", "/v1/providers", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+runner)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("accepted %s: HTTP %d", fields, w.Code)
		}
	}
}

func (f *fixture) runner() string {
	f.auth.actors["tok-runner"] = actor("crew-runner", "service", true, "read", "handoff")
	return "tok-runner"
}

func TestModelSettingsKeyNeverStoredAndHandedOutOnce(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	in := ProviderKeyInput{Scope: scopeA, Provider: "openai", Name: "회사 키", Key: testProviderKey}
	if _, err := f.svc.AddProviderKey(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent added a key", err)
	}
	r, err := f.svc.AddProviderKey(testCtx, f.human, in)
	if err != nil {
		t.Fatal("add", err)
	}
	q := r.Providers.Requests[0]
	if q.Kind != "key_add" || q.Last4 != "WXYZ" || q.KeyID == "" || q.Status != "pending" {
		t.Fatal("request", q)
	}
	// The key is in no record: not the document, not the audit trail.
	for col, rows := range f.repo.store {
		for _, row := range rows {
			raw, _ := json.Marshal(row)
			if strings.Contains(string(raw), testProviderKey) {
				t.Fatal("key stored in", col)
			}
		}
	}
	if _, err = f.svc.ProviderHandoff(testCtx, f.human, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("the owner's token collected keys", err)
	}
	page, err := f.svc.ProviderHandoff(testCtx, runner, scopeA)
	if err != nil || len(page.Items) != 1 || page.Items[0].Key != testProviderKey || page.Items[0].Request.ID != q.ID {
		t.Fatal("handoff", page, err)
	}
	if page, _ = f.svc.ProviderHandoff(testCtx, runner, scopeA); len(page.Items) != 0 {
		t.Fatal("a key was handed out twice")
	}
	inv := []Provider{{ID: "openai", Name: "OpenAI", Kind: "api", Accounts: []ProviderAccount{}, Models: []string{"gpt-6.1-sol"},
		Keys: []ProviderKey{{ID: q.KeyID, Name: "회사 키", Last4: "WXYZ", CreatedAt: time.Now()}}}}
	if _, err = f.svc.ReportProviders(testCtx, f.human, ProvidersReport{Scope: scopeA, Providers: &inv}); !errors.Is(err, ErrDenied) {
		t.Fatal("the owner's token reported inventory", err)
	}
	r, err = f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &inv, Results: []ProviderResult{{ID: q.ID, Status: "done"}}})
	if err != nil || r.Providers.Requests[0].Status != "done" || r.Providers.Providers[0].Keys[0].Last4 != "WXYZ" {
		t.Fatal("report", r.Providers, err)
	}
	// A report carrying a whole key is refused.
	inv[0].Keys[0].Name = "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err = f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Providers: &inv}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a key in a report was stored", err)
	}
}

func TestModelSettingsTestRemoveAndExpiry(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	r, err := f.svc.AddProviderKey(testCtx, f.human, ProviderKeyInput{Scope: scopeA, Provider: "zai", Key: testProviderKey, BaseURL: "https://api.z.ai/api/paas/v4", TestOnly: true})
	if err != nil || r.Providers.Requests[0].Kind != "key_test" || r.Providers.Requests[0].KeyID != "" {
		t.Fatal("test request", r.Providers, err)
	}
	id := r.Providers.Requests[0].ID
	if _, err = f.svc.ProviderHandoff(testCtx, runner, scopeA); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Results: []ProviderResult{{ID: id, Status: "done", Models: []string{"glm-5.1", "glm-5-air"}}}})
	if err != nil || len(r.Providers.Requests[0].Models) != 2 || r.Providers.CollectedAt != nil {
		t.Fatal("test result", r.Providers, err)
	}
	r, err = f.svc.RemoveProviderKey(testCtx, f.human, ProviderKeyRemoval{Scope: scopeA, Provider: "zai", KeyID: "key_1"})
	if err != nil || r.Providers.Requests[1].Kind != "key_remove" {
		t.Fatal("remove", err)
	}
	page, _ := f.svc.ProviderHandoff(testCtx, runner, scopeA)
	if len(page.Items) != 1 || page.Items[0].Key != "" || page.Items[0].Request.KeyID != "key_1" {
		t.Fatal("remove handoff", page)
	}
	// A key the runner never collected is gone after the TTL and shows as expired.
	r, _ = f.svc.AddProviderKey(testCtx, f.human, ProviderKeyInput{Scope: scopeA, Provider: "openai", Name: "늦은 키", Key: testProviderKey})
	late := r.Providers.Requests[len(r.Providers.Requests)-1].ID
	f.svc.providerKeys.mu.Lock()
	hk := f.svc.providerKeys.keys[late]
	hk.until = time.Now().Add(-time.Second)
	f.svc.providerKeys.keys[late] = hk
	f.svc.providerKeys.mu.Unlock()
	if page, _ = f.svc.ProviderHandoff(testCtx, runner, scopeA); len(page.Items) != 1 || page.Items[0].Request.Kind != "key_remove" {
		t.Fatal("an expired key was handed out", page)
	}
	stored := f.repo.store[colProviders][recKey(scopeA, providersID(scopeA))]
	stored.Providers.Requests[len(stored.Providers.Requests)-1].RequestedAt = time.Now().Add(-providerKeyTTL - time.Minute)
	f.repo.store[colProviders][recKey(scopeA, providersID(scopeA))] = stored
	got, _ := f.svc.Providers(testCtx, f.human, scopeA)
	if got.Providers.Requests[len(got.Providers.Requests)-1].Status != "expired" {
		t.Fatal("a lost key still shows as waiting")
	}
}

func TestModelSettingsRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	for i, in := range []ProviderKeyInput{
		{Scope: scopeA, Provider: "openai", Name: "", Key: testProviderKey},
		{Scope: scopeA, Provider: "Open AI", Name: "x", Key: testProviderKey},
		{Scope: scopeA, Provider: "openai", Name: "x", Key: "short"},
		{Scope: scopeA, Provider: "openai", Name: "x", Key: "has space 0123456789"},
		{Scope: scopeA, Provider: "openai", Name: "x", Key: testProviderKey, BaseURL: "ftp://x"},
		{Scope: scopeA, Provider: "zai", Name: "x", Key: testProviderKey, BaseURL: "http://api.z.ai/api/anthropic"},
	} {
		if _, err := f.svc.AddProviderKey(testCtx, f.human, in); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad key input accepted", i, err)
		}
	}
	if _, err := f.svc.AddProviderKey(testCtx, f.human, ProviderKeyInput{Scope: scopeA, Provider: "openai", Name: "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Key: testProviderKey}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a key as a name accepted", err)
	}
	if _, err := f.svc.ReportProviders(testCtx, f.runner(), ProvidersReport{Scope: scopeA, Providers: &[]Provider{{ID: "x", Name: "X", Kind: "magic"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad provider kind accepted", err)
	}
}

func TestModelSettingsSwitchesSurviveReports(t *testing.T) {
	f := newFixture(t)
	sw := ProviderModelSwitch{Scope: scopeA, Provider: "anthropic", Model: "haiku 4.5", Enabled: false}
	if _, err := f.svc.SwitchProviderModel(testCtx, f.agent, sw); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent switched a model", err)
	}
	if _, err := f.svc.SwitchProviderModel(testCtx, f.human, sw); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SwitchProviderModel(testCtx, f.human, sw); err != nil {
		t.Fatal(err)
	}
	inv := []Provider{{ID: "anthropic", Name: "Anthropic", Kind: "subscription", Models: []string{"opus 5.5", "haiku 4.5"},
		Accounts: []ProviderAccount{{ID: "claude", Label: "Claude Max", CLI: "claude", Status: "connected", Default: true, Quota: &ProviderQuota{Window: "5h"}}}}}
	r, err := f.svc.ReportProviders(testCtx, f.runner(), ProvidersReport{Scope: scopeA, Providers: &inv})
	if err != nil || len(r.Providers.Disabled) != 1 || r.Providers.Disabled[0] != "anthropic/haiku 4.5" {
		t.Fatal("switch lost or doubled", r.Providers, err)
	}
	sw.Enabled = true
	if r, _ = f.svc.SwitchProviderModel(testCtx, f.human, sw); len(r.Providers.Disabled) != 0 {
		t.Fatal("switch back", r.Providers.Disabled)
	}
}

// Record IDs are unique per collection in PostgreSQL, so each scope needs its
// own model settings ID or one scope's write would collide with another's.
func TestModelSettingsRecordIDPerScope(t *testing.T) {
	f := newFixture(t)
	agentScope := scopeA
	agentScope.AgentID = "other"
	if providersID(scopeA) == providersID(scopeB) || providersID(scopeA) == providersID(agentScope) || providersID(scopeA) != providersID(Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: scopeA.UserID, AgentID: scopeA.AgentID}) {
		t.Fatal("ids", providersID(scopeA), providersID(scopeB), providersID(agentScope))
	}
	r, err := f.svc.SwitchProviderModel(testCtx, f.human, ProviderModelSwitch{Scope: scopeA, Provider: "openai", Model: "gpt-6.1-sol", Enabled: false})
	if err != nil || r.ID != providersID(scopeA) {
		t.Fatal("switch", r.ID, err)
	}
	if got, err := f.svc.Providers(testCtx, f.human, scopeA); err != nil || got.ID != r.ID || len(got.Providers.Disabled) != 1 {
		t.Fatal("read back", got, err)
	}
}

func TestProviderModelAddAndRemove(t *testing.T) {
	f := newFixture(t)
	add := ProviderModelSwitch{Scope: scopeA, Provider: "zai", Model: "glm-5.3", Enabled: true, Add: true}
	r, err := f.svc.SwitchProviderModel(testCtx, f.human, add)
	if err != nil || len(r.Providers.Extra) != 1 || r.Providers.Extra[0] != "zai/glm-5.3" {
		t.Fatal("add", r.Providers, err)
	}
	off := add
	off.Add, off.Enabled = false, false
	if r, _ = f.svc.SwitchProviderModel(testCtx, f.human, off); len(r.Providers.Disabled) != 1 || len(r.Providers.Extra) != 1 {
		t.Fatal("switching off a hand-added model forgot it", r.Providers)
	}
	add.Enabled = true
	f.svc.SwitchProviderModel(testCtx, f.human, add)
	rm := add
	rm.Add, rm.Remove = false, true
	if r, _ = f.svc.SwitchProviderModel(testCtx, f.human, rm); len(r.Providers.Extra) != 0 || len(r.Providers.Disabled) != 0 {
		t.Fatal("remove", r.Providers)
	}
}

func TestProviderModelTestGoesToTheRunnerWithoutAKey(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	in := ProviderModelTest{Scope: scopeA, Provider: "openai", Model: "gpt-6.1-sol"}
	if _, err := f.svc.TestProviderModel(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent asked for a test", err)
	}
	r, err := f.svc.TestProviderModel(testCtx, f.human, in)
	if err != nil || r.Providers.Requests[0].Kind != "model_test" || r.Providers.Requests[0].Model != "gpt-6.1-sol" {
		t.Fatal("test request", r.Providers, err)
	}
	page, err := f.svc.ProviderHandoff(testCtx, runner, scopeA)
	if err != nil || len(page.Items) != 1 || page.Items[0].Key != "" {
		t.Fatal("handoff", page, err)
	}
	done, err := f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Results: []ProviderResult{{ID: page.Items[0].Request.ID, Status: "done", Note: "응답 1.2초"}}})
	if err != nil || done.Providers.Requests[0].Note != "응답 1.2초" {
		t.Fatal("report", done.Providers, err)
	}
}

func TestProviderModelsRefreshQueuesOneRequestAtATime(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	in := ProviderModelsRefresh{Scope: scopeA}
	if _, err := f.svc.RefreshProviderModels(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent asked for a refresh", err)
	}
	r, err := f.svc.RefreshProviderModels(testCtx, f.human, in)
	if err != nil || len(r.Providers.Requests) != 1 || r.Providers.Requests[0].Kind != "models_refresh" {
		t.Fatal("refresh request", r.Providers, err)
	}
	if _, err := f.svc.RefreshProviderModels(testCtx, f.human, in); !errors.Is(err, ErrConflict) {
		t.Fatal("a second refresh while one waits", err)
	}
	page, err := f.svc.ProviderHandoff(testCtx, runner, scopeA)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("handoff", page, err)
	}
	if _, err := f.svc.ReportProviders(testCtx, runner, ProvidersReport{Scope: scopeA, Results: []ProviderResult{{ID: page.Items[0].Request.ID, Status: "done"}}}); err != nil {
		t.Fatal("report", err)
	}
	r, err = f.svc.RefreshProviderModels(testCtx, f.human, in)
	if err != nil {
		t.Fatal("a refresh after the last one finished", err)
	}
	// A refresh the runner never picked up expires: a new one may be queued and only it is handed off.
	stored := f.repo.store[colProviders][recKey(scopeA, r.ID)]
	stored.Providers.Requests[1].RequestedAt = time.Now().Add(-providerKeyTTL - time.Minute)
	f.repo.store[colProviders][recKey(scopeA, r.ID)] = stored
	if page, _ = f.svc.ProviderHandoff(testCtx, runner, scopeA); len(page.Items) != 0 {
		t.Fatal("an expired refresh was handed off", page)
	}
	r, err = f.svc.RefreshProviderModels(testCtx, f.human, in)
	if err != nil || r.Providers.Requests[1].Status != "expired" {
		t.Fatal("expired refresh blocked a new one", r.Providers, err)
	}
	if page, _ = f.svc.ProviderHandoff(testCtx, runner, scopeA); len(page.Items) != 1 || page.Items[0].Request.ID != r.Providers.Requests[2].ID {
		t.Fatal("handoff after expiry", page)
	}
}

func TestProviderLabelsSetAndClear(t *testing.T) {
	f := newFixture(t)
	in := ProviderLabel{Scope: scopeA, Provider: "openai", Kind: "account", ID: "codex", Label: "회사 Codex"}
	if _, err := f.svc.LabelProvider(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent renamed an account", err)
	}
	r, err := f.svc.LabelProvider(testCtx, f.human, in)
	if err != nil || r.Providers.Labels["account:openai/codex"] != "회사 Codex" {
		t.Fatal("label", r.Providers, err)
	}
	m := ProviderLabel{Scope: scopeA, Provider: "openai", Kind: "model", ID: "gpt-6.1-sol", Label: "GPT-6.1-Sol"}
	if r, _ = f.svc.LabelProvider(testCtx, f.human, m); len(r.Providers.Labels) != 2 {
		t.Fatal("model label", r.Providers.Labels)
	}
	in.Label = ""
	if r, _ = f.svc.LabelProvider(testCtx, f.human, in); len(r.Providers.Labels) != 1 {
		t.Fatal("clear", r.Providers.Labels)
	}
	for _, bad := range []ProviderLabel{{Scope: scopeA, Provider: "openai", Kind: "key", ID: "x", Label: "y"}, {Scope: scopeA, Provider: "openai", Kind: "model", ID: "gpt", Label: strings.Repeat("가", 41)}} {
		if _, err := f.svc.LabelProvider(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad label accepted", bad, err)
		}
	}
}

func TestProviderAssistModelSetReadAndClear(t *testing.T) {
	f := newFixture(t)
	in := ProviderAssistModel{Scope: scopeA, Model: "haiku 4.5"}
	if _, err := f.svc.SetAssistModel(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent picked the assist model", err)
	}
	if _, err := f.svc.SetAssistModel(testCtx, f.human, ProviderAssistModel{Scope: scopeA, Model: "bad;model"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad model accepted", err)
	}
	if _, err := f.svc.SetAssistModel(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	audited := false
	for _, row := range f.repo.store["audit_events"] {
		audited = audited || row.Kind == "set:assist_model"
	}
	if !audited {
		t.Fatal("no audit event")
	}
	// A runner report keeps the owner's pick, and the read returns it.
	if _, err := f.svc.ReportProviders(testCtx, f.runner(), ProvidersReport{Scope: scopeA, Providers: &[]Provider{}}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.svc.Providers(testCtx, f.runner(), scopeA); err != nil || got.Providers.AssistModel != "haiku 4.5" {
		t.Fatal("read back", got.Providers, err)
	}
	// The provider it was picked under is kept with it; a provider without a model or a bad id is refused.
	for _, bad := range []ProviderAssistModel{{Scope: scopeA, Provider: "zai"}, {Scope: scopeA, Model: "glm-4.6", Provider: "Bad Id"}} {
		if _, err := f.svc.SetAssistModel(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad provider accepted", bad, err)
		}
	}
	if r, err := f.svc.SetAssistModel(testCtx, f.human, ProviderAssistModel{Scope: scopeA, Model: "glm-4.6", Provider: "zai"}); err != nil || r.Providers.AssistProvider != "zai" {
		t.Fatal("provider", r.Providers, err)
	}
	in.Model = ""
	if r, err := f.svc.SetAssistModel(testCtx, f.human, in); err != nil || r.Providers.AssistModel != "" || r.Providers.AssistProvider != "" {
		t.Fatal("clear", r.Providers, err)
	}
}

// Stale pending requests the read view already shows as expired (lost keys, unanswered model tests) must not fill the
// queue: with 100 of them stored as pending, 목록 새로고침 answered revision_conflict and nothing was queued.
func TestProviderModelsRefreshIsNotBlockedByStalePendingRequests(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.RefreshProviderModels(testCtx, f.human, ProviderModelsRefresh{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	stored := f.repo.store[colProviders][recKey(scopeA, providersID(scopeA))]
	stale := time.Now().Add(-providerKeyTTL - time.Minute)
	stored.Providers.Requests = nil
	for i := range providerRequestsKept {
		kind := []string{"model_test", "key_add", "key_test"}[i%3]
		stored.Providers.Requests = append(stored.Providers.Requests, ProviderRequest{ID: ID(), Kind: kind, Provider: "openai", Model: "gpt-5", Status: "pending", RequestedAt: stale})
	}
	f.repo.store[colProviders][recKey(scopeA, stored.ID)] = stored
	if page, _ := f.svc.ProviderHandoff(testCtx, f.runner(), scopeA); len(page.Items) != 0 {
		t.Fatal("expired requests handed off", page)
	}
	r, err := f.svc.RefreshProviderModels(testCtx, f.human, ProviderModelsRefresh{Scope: scopeA})
	if err != nil {
		t.Fatal("stale pending requests blocked a refresh", err)
	}
	if last := r.Providers.Requests[len(r.Providers.Requests)-1]; last.Kind != "models_refresh" || last.Status != "pending" || len(r.Providers.Requests) != providerRequestsKept {
		t.Fatal("refresh not queued", r.Providers.Requests)
	}
}
