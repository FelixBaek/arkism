package memory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) ListRoutingAttributions(ctx context.Context, scope Scope, after string, limit int) ([]Record, error) {
	rows, e := f.ListRecords(ctx, scope, colJobs, 1<<30)
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, r := range rows {
		if r.Kind == routingAttributionKind && r.ID > after {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func sampleAttribution(id string) RoutingAttribution {
	return RoutingAttribution{DecisionID: id, DecidedAt: time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC), Decision: "clef",
		Chosen: "xai/grok-5", Fallbacks: []string{"openai/gpt-6.1"},
		Attempts: []RoutingAttributionAttempt{{Index: 0, Model: "xai/grok-5", Error: "cli_not_found"}, {Index: 1, Model: "openai/gpt-6.1", OK: true, LatencyMS: 900}},
		Origin:   &RoutingOrigin{Kind: "workflow", WorkflowID: "wf", RunID: "run-1", NodeID: "n1"}}
}

func TestRoutingAttributionIsIdempotentPerDecision(t *testing.T) {
	f := newFixture(t)
	runner := "tok-runner"
	f.auth.actors[runner] = actor("crew-runner", "service", true, "write")
	in := RoutingAttributionInput{Scope: scopeA, Record: sampleAttribution("dec-1")}
	first, err := f.svc.RecordRoutingAttribution(testCtx, runner, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.RecordRoutingAttribution(testCtx, runner, in) // a resend after a lost reply
	if err != nil || again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatal(again, err)
	}
	rows, _ := f.repo.ListRecords(testCtx, scopeA, colJobs, 100)
	if len(rows) != 1 || rows[0].Kind != routingAttributionKind || rows[0].Status != JobStatusDone {
		t.Fatal(rows)
	}
	other := in
	other.Record.Attempts = other.Record.Attempts[:1]
	if _, err = f.svc.RecordRoutingAttribution(testCtx, runner, other); !errors.Is(err, ErrConflict) {
		t.Fatal("another record under the same decision id", err)
	}
	if _, err = f.svc.RecordRoutingAttribution(testCtx, f.work, RoutingAttributionInput{Scope: scopeA, Record: sampleAttribution("dec-2")}); !errors.Is(err, ErrDenied) {
		t.Fatal("a token without write:routing_calls", err)
	}
}

func TestRoutingAttributionRejectsMalformedRecords(t *testing.T) {
	f := newFixture(t)
	for name, edit := range map[string]func(*RoutingAttribution){
		"decision id":         func(a *RoutingAttribution) { a.DecisionID = "has space" },
		"no time":             func(a *RoutingAttribution) { a.DecidedAt = time.Time{} },
		"failover as pick":    func(a *RoutingAttribution) { a.Decision = "failover" },
		"bad model":           func(a *RoutingAttribution) { a.Chosen = "no-slash" },
		"too many fallbacks":  func(a *RoutingAttribution) { a.Fallbacks = []string{"a/b", "a/c", "a/d", "a/e"} },
		"no attempts":         func(a *RoutingAttribution) { a.Attempts = nil },
		"attempt out of turn": func(a *RoutingAttribution) { a.Attempts[1].Model = "xai/grok-5" },
		"ok then another":     func(a *RoutingAttribution) { a.Attempts[0].OK = true },
		"error text":          func(a *RoutingAttribution) { a.Attempts[0].Error = "Traceback: secret sk-123" },
		"stop reason":         func(a *RoutingAttribution) { a.Stopped = "gave_up" },
		"arm label":           func(a *RoutingAttribution) { a.Arm = "arm one" },
		"view hash":           func(a *RoutingAttribution) { a.ViewHash = "abc" },
		"group":               func(a *RoutingAttribution) { a.Group = "../x" },
	} {
		rec := sampleAttribution("dec-x")
		edit(&rec)
		if _, err := f.svc.RecordRoutingAttribution(testCtx, f.agent, RoutingAttributionInput{Scope: scopeA, Record: rec}); !errors.Is(err, ErrInvalid) {
			t.Error(name, err)
		}
	}
}

// The HTTP body refuses any field the record does not have, so prompt text cannot ride along.
func TestRoutingAttributionHTTPRefusesExtraFieldsAndPagesTheExport(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	post := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/routing/attributions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.agent)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	scope := `{"workspace_id":"` + scopeA.WorkspaceID + `","project_id":"` + scopeA.ProjectID + `","user_id":"` + scopeA.UserID + `","agent_id":"` + scopeA.AgentID + `"}`
	// The record exactly as the crew runner sends it (routing_attribution_test.go in internal/host/crew).
	rec := `{"decision_id": "dec-h", "decided_at": "2026-10-31T01:02:03Z", "decision": "clef", "chosen": "xai/grok-5", "fallbacks": ["openai/gpt-6.1"], ` +
		`"attempts": [{"index": 0, "model": "xai/grok-5", "ok": false, "latency_ms": 0, "error": "cli_not_found"}, {"index": 1, "model": "openai/gpt-6.1", "ok": true, "latency_ms": 900}], ` +
		`"group": "group/fast", "arm": "F-ADAPTIVE", "origin": {"kind": "workflow", "run_id": "run-1", "node_id": "n1"}`
	if code := post(`{"scope":` + scope + `,"record":` + rec + `,"prompt":"fix the bug"}}`); code != http.StatusBadRequest {
		t.Fatal("prompt field accepted", code)
	}
	if code := post(`{"scope":` + scope + `,"record":` + rec + `}}`); code != http.StatusOK {
		t.Fatal(code)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.svc.RecordRoutingAttribution(testCtx, f.agent, RoutingAttributionInput{Scope: scopeA, Record: sampleAttribution(fmt.Sprint("dec-", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.repo.Insert(testCtx, colJobs, Record{ID: "routing-attribution-0", Scope: scopeA, Kind: "unrelated", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	cursor := ""
	for {
		page, err := f.svc.ListRoutingAttributions(testCtx, f.human, scopeA, cursor, 2)
		if err != nil || page.Authority != AuthorityDataOnly {
			t.Fatal(page, err)
		}
		for _, it := range page.Items {
			seen = append(seen, it.Record.DecisionID)
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	sort.Strings(seen)
	if strings.Join(seen, ",") != "dec-0,dec-1,dec-2,dec-h" {
		t.Fatal(seen)
	}
	f.auth.actors["tok-write"] = actor("crew-runner", "service", true, "write")
	if _, err := f.svc.ListRoutingAttributions(testCtx, "tok-write", scopeA, "", 10); !errors.Is(err, ErrDenied) {
		t.Fatal("a token without read:routing", err)
	}
}

// The Postgres export filters by kind before paging and survives a store round trip.
func TestPostgresRoutingAttributionExport(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	for i := 0; i < 3; i++ {
		if _, err := f.svc.RecordRoutingAttribution(testCtx, f.agent, RoutingAttributionInput{Scope: scopeA, Record: sampleAttribution(fmt.Sprint("dec-", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.RecordRoutingAttribution(testCtx, f.agent, RoutingAttributionInput{Scope: scopeA, Record: sampleAttribution("dec-0")}); err != nil {
		t.Fatal("resend", err)
	}
	first, err := f.svc.ListRoutingAttributions(testCtx, f.human, scopeA, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	rest, err := f.svc.ListRoutingAttributions(testCtx, f.human, scopeA, first.NextCursor, 2)
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatal(rest, err)
	}
	got := rest.Items[0].Record
	if len(got.Attempts) != 2 || got.Attempts[1].LatencyMS != 900 || got.Origin == nil || got.Origin.RunID != "run-1" || !got.DecidedAt.Equal(sampleAttribution("x").DecidedAt) {
		t.Fatal(got)
	}
	if other, _ := f.svc.ListRoutingAttributions(testCtx, f.human, scopeB, "", 10); len(other.Items) != 0 {
		t.Fatal("another scope sees the records", other)
	}
}
