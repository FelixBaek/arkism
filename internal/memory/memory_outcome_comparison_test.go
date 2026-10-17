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

func (f *fakeRepo) MemoryOutcomeSessionCounts(ctx context.Context, in MemoryOutcomeQuery) ([]MemoryOutcomeSessionCount, error) {
	in, _, e := normalizeMemoryOutcomeQuery(in, false)
	if e != nil || len(in.MemoryIDs) != 1 || in.Dimension == "" {
		return nil, ErrInvalid
	}
	id := in.MemoryIDs[0]
	type session struct{ ok, used, returned bool }
	by := map[string]*session{}
	for _, r := range f.rows("evaluations") {
		one := in
		if r.Target == nil {
			continue
		}
		if one.MemoryIDs = []string{r.Target.ID}; !memoryOutcomeMatches(r, one) {
			continue
		}
		for _, rel := range r.Relations {
			if rel.Collection == "execution_traces" {
				s := by[rel.ID]
				if s == nil {
					s = &session{ok: true}
					by[rel.ID] = s
				}
				s.ok, s.used = s.ok && r.Passed, s.used || r.Target.ID == id
			}
		}
	}
	runs := map[string]bool{}
	for _, r := range f.rows("jobs") {
		if r.Scope == in.Scope && r.Kind == "context_usage" && r.Status == "done" && r.Usage != nil && r.Usage.Run != nil && r.Usage.Run.Collection == "agent_runs" && usedRef(r.Usage, id) {
			runs[r.Usage.Run.ID] = true
		}
	}
	sides := map[string]*MemoryOutcomeSessionCount{}
	for trace, s := range by {
		if tr, e := f.Get(ctx, "execution_traces", in.Scope, trace); !s.used && e == nil {
			for _, rel := range tr.Relations {
				s.returned = s.returned || rel.Collection == "agent_runs" && runs[rel.ID]
			}
		}
		observation := observationUnknown
		if s.used {
			observation = observationDelivered
		} else if s.returned {
			observation = observationReturned
		}
		c := sides[observation]
		if c == nil {
			c = &MemoryOutcomeSessionCount{Used: s.used, Observation: observation}
			sides[observation] = c
		}
		c.Sessions++
		if s.ok {
			c.Passed++
		}
	}
	out := []MemoryOutcomeSessionCount{}
	for _, observation := range []string{observationDelivered, observationReturned, observationUnknown} {
		if c := sides[observation]; c != nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

// putOutcome stores one verdict row directly; the write path is ST-302's test.
func putOutcome(t *testing.T, repo Repository, scope Scope, id, trace, memory, dim string, passed bool) {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: id, Scope: scope, Kind: memoryOutcomeKind, Status: "active", Revision: 1, Text: "Independent pinned memory checker", Tags: []string{"dimension:" + dim}, Target: &Ref{Collection: colMemories, ID: memory, Revision: 1},
		Relations: []Ref{{Collection: colMemories, ID: memory, Revision: 1}, {Collection: "execution_traces", ID: trace, Revision: 1}}, Passed: passed, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}, CreatedBy: "checker", CreatedByType: roleService, CreatedAt: at, UpdatedAt: at}
	if e := repo.Insert(testCtx, "evaluations", r); e != nil {
		t.Fatal(e)
	}
}

// comparisonRows: memory m is judged in 9 sessions (6 pass; u0 also fails on
// another memory), and handed out by a receipt bound to run r-run, whose trace
// has only another memory's passing verdict. 9 sessions have unknown exposure (5 pass).
func comparisonRows(t *testing.T, repo Repository, scope Scope, m string) {
	t.Helper()
	for i := 0; i < 9; i++ {
		trace := "u" + string(rune('0'+i))
		putOutcome(t, repo, scope, "ou"+trace, trace, m, "work_task", i < 6)
		putOutcome(t, repo, scope, "on"+string(rune('0'+i)), "n"+string(rune('0'+i)), "other-mem", "work_task", i < 5)
	}
	putOutcome(t, repo, scope, "ou0-other", "u0", "other-mem", "work_task", false)
	putOutcome(t, repo, scope, "ou1-compliance", "u1", m, "personal_compliance", false)
	putOutcome(t, repo, scope, "or", "r-trace", "other-mem", "work_task", true)
	if e := repo.Insert(testCtx, "execution_traces", Record{ID: "r-trace", Scope: scope, Status: "active", Revision: 1, Relations: []Ref{{Collection: "agent_runs", ID: "r-run", Revision: 1}}}); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	if e := repo.Insert(testCtx, "jobs", Record{ID: "r-receipt", Scope: scope, Kind: "context_usage", Status: "done", Revision: 1, CreatedAt: now, UpdatedAt: now, Usage: &ContextUsage{Run: &Ref{Collection: "agent_runs", ID: "r-run", Revision: 1}, Items: []MemoryUse{{Memory: Ref{Collection: colMemories, ID: m, Revision: 1}, Rank: 1}}}}); e != nil {
		t.Fatal(e)
	}
}

func comparisonQuery(id string) MemoryOutcomeQuery {
	return MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{id}, Dimension: "work_task"}
}

func TestMemoryOutcomeComparisonSplitsSessionsAndHidesSmallSamples(t *testing.T) {
	f, m, _ := outcomeFixture(t)
	comparisonRows(t, f.repo, scopeA, m.ID)
	putOutcome(t, f.repo, scopeB, "leak", "n-leak", "other-mem", "work_task", true)
	out, e := f.svc.CompareMemoryOutcomes(testCtx, f.human, comparisonQuery(m.ID))
	if e != nil {
		t.Fatal(e)
	}
	// Delivered u0..u8; the server-only return is separate, and no
	// observation for another judged session does not prove non-use.
	if out.Delivered.Sessions != 9 || out.Used != out.Delivered || out.Delivered.Status != "insufficient_data" || out.ReturnedOnly.Sessions != 1 || out.Unknown.Sessions != 9 || out.MinSessions != 10 || out.Memory.ID != m.ID || out.Authority != AuthorityDataOnly {
		t.Fatalf("observations: %+v", out)
	}
	if out.NotUsed.Sessions != 0 || out.NotUsed.PassRate != nil || out.Difference != nil || out.ActualUseStatus != "not_measured" {
		t.Fatalf("unobserved data reported as non-use: %+v", out)
	}
	putOutcome(t, f.repo, scopeA, "on9", "n9", "other-mem", "work_task", true)
	if out, e = f.svc.CompareMemoryOutcomes(testCtx, f.human, comparisonQuery(m.ID)); e != nil || out.Unknown.Status != "ok" || *out.Unknown.PassRate != 0.6 || out.Difference != nil {
		t.Fatalf("ten unknown sessions: %+v %v", out, e)
	}
	q := comparisonQuery(m.ID)
	q.Dimension = "personal_compliance"
	if out, e = f.svc.CompareMemoryOutcomes(testCtx, f.human, q); e != nil || out.Used.Sessions != 1 || out.Used.Status != "insufficient_data" || out.NotUsed.Sessions != 0 || out.NotUsed.PassRate != nil {
		t.Fatalf("dimension: %+v %v", out, e)
	}
	for _, bad := range []MemoryOutcomeQuery{{Scope: scopeA, MemoryIDs: []string{m.ID}}, {Scope: scopeA, MemoryIDs: []string{m.ID, "x"}, Dimension: "work_task"}, {Scope: scopeA, MemoryIDs: []string{m.ID}, Dimension: "work_task", Limit: 1}} {
		if _, e = f.svc.CompareMemoryOutcomes(testCtx, f.human, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad query accepted:", bad, e)
		}
	}
}

type denyAction struct {
	Authorizer
	action string
}

func (d denyAction) Authorize(ctx context.Context, token string, req Authorization) (Decision, error) {
	out, e := d.Authorizer.Authorize(ctx, token, req)
	if req.Action == d.action {
		out.Allowed = false
	}
	return out, e
}

func TestMemoryOutcomeComparisonNeedsUsageAndMemoryRead(t *testing.T) {
	f, m, _ := outcomeFixture(t)
	comparisonRows(t, f.repo, scopeA, m.ID)
	f.svc.Auth = denyAction{f.auth, "read:usage"}
	if _, e := f.svc.CompareMemoryOutcomes(testCtx, f.human, comparisonQuery(m.ID)); !errors.Is(e, ErrDenied) {
		t.Fatal("compared without read:usage:", e)
	}
	f.svc.Auth = f.auth
	q := comparisonQuery(m.ID)
	q.Scope = scopeB
	if _, e := f.svc.CompareMemoryOutcomes(testCtx, f.human, q); !errors.Is(e, ErrNotFound) {
		t.Fatal("memory compared through another scope:", e)
	}
	f.auth.actors["mute"] = actor("mute", roleHuman, true, "write")
	if _, e := f.svc.CompareMemoryOutcomes(testCtx, "mute", comparisonQuery(m.ID)); !errors.Is(e, ErrDenied) {
		t.Fatal("compared without read:", e)
	}
}

func TestMemoryOutcomeComparisonHTTPReturnsCountsOnly(t *testing.T) {
	f, m, _ := outcomeFixture(t)
	comparisonRows(t, f.repo, scopeA, m.ID)
	h := Handler(f.svc)
	q := "?workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID
	call := func(query string) (int, string) {
		r := httptest.NewRequest("GET", "/v1/memories/"+m.ID+"/outcome-comparison"+query, nil)
		r.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	code, body := call(q + "&dimension=work_task")
	var out MemoryOutcomeComparison
	if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil || out.Delivered.Sessions != 9 || out.Unknown.Sessions != 9 || !strings.Contains(body, `"not_used":{"sessions":0,"status":"insufficient_data","passed":null,"pass_rate":null}`) {
		t.Fatal(code, body)
	}
	for _, text := range []string{m.Text, "Independent pinned memory checker", "r-trace", "other-mem"} {
		if strings.Contains(body, text) {
			t.Fatalf("response leaked %q: %s", text, body)
		}
	}
	for _, bad := range []string{q, q + "&dimension=other", q + "&dimension=work_task&limit=5", q + "&dimension=work_task&cursor=x"} {
		if code, _ = call(bad); code != http.StatusBadRequest {
			t.Fatal(bad, code)
		}
	}
}

func TestPostgresMemoryOutcomeSessionCounts(t *testing.T) {
	p := isolatedPostgres(t)
	comparisonRows(t, p, scopeA, "mem-1")
	putOutcome(t, p, scopeB, "leak", "n-leak", "other-mem", "work_task", true)
	other := scopeA
	other.UserID = "someone-else"
	putOutcome(t, p, other, "leak-user", "u-leak", "mem-1", "work_task", true)
	counts, e := p.MemoryOutcomeSessionCounts(testCtx, comparisonQuery("mem-1"))
	want := []MemoryOutcomeSessionCount{{Used: true, Observation: observationDelivered, Sessions: 9, Passed: 5}, {Observation: observationReturned, Sessions: 1, Passed: 1}, {Observation: observationUnknown, Sessions: 9, Passed: 5}}
	if e != nil || len(counts) != 3 || counts[0] != want[0] || counts[1] != want[1] || counts[2] != want[2] {
		t.Fatalf("counts: %+v %v", counts, e)
	}
	q := comparisonQuery("mem-1")
	q.Dimension = "personal_compliance"
	if counts, e = p.MemoryOutcomeSessionCounts(testCtx, q); e != nil || len(counts) != 1 || counts[0] != (MemoryOutcomeSessionCount{Used: true, Sessions: 1, Observation: observationDelivered}) {
		t.Fatalf("compliance: %+v %v", counts, e)
	}
	future := time.Now().Add(time.Hour)
	q = comparisonQuery("mem-1")
	q.From = &future
	if counts, e = p.MemoryOutcomeSessionCounts(testCtx, q); e != nil || len(counts) != 0 {
		t.Fatalf("from: %+v %v", counts, e)
	}
	if _, e = p.MemoryOutcomeSessionCounts(testCtx, MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"mem-1"}}); !errors.Is(e, ErrInvalid) {
		t.Fatal("no dimension:", e)
	}
}
