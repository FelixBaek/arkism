package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUsageSessionsUpsertAndSummaryCountsByDay(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "route", "write")
	seoul := time.FixedZone("KST", 9*3600)
	today := time.Now().In(seoul).Format("2006-01-02")
	old := time.Now().In(seoul).AddDate(0, 0, -40).Format("2006-01-02")
	c := 1.5
	hours := make([]int, 168)
	hours[3] = 4
	s := UsageSession{Agent: "claude_code", Device: "server", SessionID: "s1", Title: "사용량 화면", Project: "root",
		StartedAt: time.Now().Add(-41 * 24 * time.Hour), EndedAt: time.Now(), Messages: 20, ActiveSeconds: 600, Hours: hours,
		Days: map[string]map[string]UsageTokens{
			today: {"claude-opus-5-5": {Input: 10, Output: 20, CacheRead: 70, Requests: 2, CostUSD: &c}},
			old:   {"claude-opus-5-5": {Input: 1000, Output: 1000, Requests: 1}},
		},
		Tools: map[string]int{"Bash": 3}, Skills: map[string]int{"figma:figma-use": 1}}
	in := UsageSessionsInput{Scope: scopeA, Items: []UsageSession{s}}
	if _, err := f.svc.RecordUsageSessions(testCtx, runner, in); err != nil {
		t.Fatal(err)
	}
	// the collector re-reports a growing session: same row, not a second one
	s.Messages = 21
	if _, err := f.svc.RecordUsageSessions(testCtx, runner, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{s}}); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.UsageSummary(testCtx, f.human, scopeA, "7d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	k := out["kpi"].(map[string]any)
	if k["sessions"] != 1 || k["tokens"] != int64(30) || k["cost_usd"] != 1.5 || k["cache_hit_pct"] != 87.5 {
		t.Fatal("7d kpi", k)
	}
	if len(out["by_day"].([]map[string]any)) != 7 || out["hours"].([]int)[3] != 4 || out["shape"].([]int)[2] != 1 {
		t.Fatal("series", out["by_day"], out["shape"])
	}
	all, _ := f.svc.UsageSummary(testCtx, f.human, scopeA, "all", "", nil)
	if all["kpi"].(map[string]any)["tokens"] != int64(2030) {
		t.Fatal("all", all["kpi"])
	}
	none, _ := f.svc.UsageSummary(testCtx, f.human, scopeA, "all", "codex", nil)
	if none["kpi"].(map[string]any)["sessions"] != 0 || len(none["agents"].([]map[string]any)) != 1 {
		t.Fatal("filter", none["kpi"], none["agents"])
	}

	list, err := f.svc.UsageSessionList(testCtx, f.human, scopeA, "all", "", "사용량", 10)
	if err != nil || list["total"] != 1 {
		t.Fatal("list", list, err)
	}
	item := list["items"].([]map[string]any)[0]
	if item["tokens"] != int64(2030) || item["tools"].([]map[string]any)[0]["name"] != "Bash" || len(item["models"].([]map[string]any)) != 1 {
		t.Fatal("item", item)
	}
	if miss, _ := f.svc.UsageSessionList(testCtx, f.human, scopeA, "all", "", "없는 제목", 10); miss["total"] != 0 {
		t.Fatal("search", miss)
	}

	bad := s
	bad.Title = "sk-ant-api03-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bad.SessionID = "s2"
	if _, err := f.svc.RecordUsageSessions(testCtx, runner, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{bad}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.Repo.Get(testCtx, colUsageSessions, scopeA, usageSessionID(bad)); got.UsageSession.Title != "" || got.Text != "" {
		t.Fatal("secret title kept", got.UsageSession.Title)
	}
	if _, err := f.svc.UsageSummary(testCtx, f.human, scopeA, "1y", "", nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad range", err)
	}
}

func TestUsageSummaryToolHealthFromPairedMCPCalls(t *testing.T) {
	f := newFixture(t)
	runner := f.runner()
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "route", "write")
	now := time.Now()
	day := now.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02")
	calls := []UsageCall{{Tool: "mcp__figma__use_figma", At: now.Add(-3 * time.Hour), MS: 400},
		{Tool: "mcp__figma__use_figma", At: now.Add(-2 * time.Hour), MS: 30000, Err: "timeout"},
		{Tool: "mcp__github__list_issues", At: now.Add(-time.Hour), MS: 200},
		{Tool: "mcp__github__list_issues", At: now.Add(-60 * 24 * time.Hour), MS: 9000, Err: "auth"}} // outside 7d
	s := UsageSession{Agent: "claude_code", Device: "server", SessionID: "s1", StartedAt: now.Add(-61 * 24 * time.Hour), EndedAt: now, Messages: 3,
		Days: map[string]map[string]UsageTokens{day: {"m": {Input: 1, Requests: 1}}}, Tools: map[string]int{"mcp__figma__use_figma": 2}, Calls: calls}
	before := UsageSession{Agent: "claude_code", Device: "server", SessionID: "s0", StartedAt: now.Add(-10 * 24 * time.Hour), EndedAt: now.Add(-10 * 24 * time.Hour), Messages: 1,
		Tools: map[string]int{"mcp__figma__use_figma": 1}} // the 7 days before the window
	if _, err := f.svc.RecordUsageSessions(testCtx, runner, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{s, before}}); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.UsageSummary(testCtx, f.human, scopeA, "7d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := out["tool_health"].(map[string]any)
	if h["calls"] != 3 || h["failed"] != 1 || h["p95_ms"] != int64(30000) || h["p50_ms"] != int64(400) || h["avg_ms"] != int64(10200) {
		t.Fatal("kpi", h)
	}
	servers := h["servers"].([]map[string]any)
	if len(servers) != 2 || servers[0]["name"] != "figma" || servers[0]["failed"] != 1 || servers[1]["p95_ms"] != int64(200) {
		t.Fatal("servers", servers)
	}
	fails := h["fails"].([]map[string]any)
	if len(fails) != 1 || fails[0]["err"] != "timeout" || fails[0]["agent"] != "claude_code" {
		t.Fatal("fails", fails)
	}
	days := h["by_day"].([]map[string]any)
	if len(days) != 7 || days[6]["calls"].(int)+days[5]["calls"].(int) != 3 || days[0]["p95_ms"] != nil {
		t.Fatal("days", days)
	}
	var perServer []map[string]any
	for _, d := range days[5:] {
		perServer = append(perServer, d["servers"].([]map[string]any)...)
	}
	if n := len(perServer); n < 2 || n > 3 || out["tool_calls"] != 2 || out["tool_calls_change_pct"] != 100 {
		t.Fatal("per server per day / change", perServer, out["tool_calls"], out["tool_calls_change_pct"])
	}
	if all, _ := f.svc.UsageSummary(testCtx, f.human, scopeA, "all", "", nil); all["tool_calls_change_pct"] != nil {
		t.Fatal("전체 has no window before it", all["tool_calls_change_pct"])
	}
	bad := s
	bad.Calls = []UsageCall{{Tool: "Bash", At: now, MS: 1}} // only MCP calls are kept
	if _, err := f.svc.RecordUsageSessions(testCtx, runner, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{bad}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-MCP call accepted", err)
	}
	bad.Calls = []UsageCall{{Tool: "mcp__a__b", At: now, MS: 1, Err: "free text"}}
	if _, err := f.svc.RecordUsageSessions(testCtx, runner, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{bad}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("free error text accepted", err)
	}
}

// UsageSessionRows lets the summary read every fake row, like the Postgres reader does.
func (f *fakeRepo) UsageSessionRows(ctx context.Context, scope Scope) ([]Record, error) {
	return collectCatalogRecords(ctx, f, scope, colUsageSessions)
}
