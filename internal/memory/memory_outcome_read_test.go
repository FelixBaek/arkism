package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) MemoryOutcomes(_ context.Context, in MemoryOutcomeQuery) ([]Record, error) {
	in, after, e := normalizeMemoryOutcomeQuery(in, true)
	if e != nil {
		return nil, e
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []Record{}
	for _, r := range f.store["evaluations"] {
		if memoryOutcomeMatches(r, in) && (after == nil || r.CreatedAt.Before(after.At) || r.CreatedAt.Equal(after.At) && r.ID < after.ID) {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	if len(rows) > in.Limit+1 {
		rows = rows[:in.Limit+1]
	}
	return rows, nil
}
func (f *fakeRepo) MemoryOutcomeCounts(_ context.Context, in MemoryOutcomeQuery) ([]MemoryOutcomeCount, error) {
	in, _, e := normalizeMemoryOutcomeQuery(in, false)
	if e != nil {
		return nil, e
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	by := map[string]*MemoryOutcomeCount{}
	for _, r := range f.store["evaluations"] {
		if !memoryOutcomeMatches(r, in) {
			continue
		}
		c := by[r.Target.ID]
		if c == nil {
			c = &MemoryOutcomeCount{MemoryID: r.Target.ID}
			by[r.Target.ID] = c
		}
		c.Total++
		if r.Passed {
			c.Passed++
		}
		if r.CreatedAt.After(c.Last) {
			c.Last = r.CreatedAt
		}
	}
	out := []MemoryOutcomeCount{}
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MemoryID < out[j].MemoryID })
	return out, nil
}

// recordOutcomes stores three verdicts on one memory: work_task fail, work_task
// pass, personal_compliance pass.
func recordOutcomes(t *testing.T) (*fixture, Record, MemoryOutcomeInput) {
	f, m, in := outcomeFixture(t)
	for i, v := range []struct {
		dim    string
		passed bool
	}{{"work_task", false}, {"work_task", true}, {"personal_compliance", true}} {
		one := in
		one.EventID, one.Dimension, one.Passed = "outcome-read-"+string(rune('a'+i)), v.dim, v.passed
		if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", one); e != nil {
			t.Fatal(e)
		}
	}
	return f, m, in
}

func outcomeQuery(id string) MemoryOutcomeQuery {
	return MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{id}}
}

func TestMemoryOutcomeReadListsSummaryFiltersAndPages(t *testing.T) {
	f, m, in := recordOutcomes(t)
	page, e := f.svc.ListMemoryOutcomes(testCtx, f.human, outcomeQuery(m.ID))
	if e != nil {
		t.Fatal(e)
	}
	s := page.Summary
	if len(page.Items) != 3 || page.NextCursor != "" || !s.Available || s.Total != 3 || s.Passed != 2 || s.PassRate == nil || *s.PassRate != 2.0/3 || s.LastJudgedAt == nil || !s.LastJudgedAt.Equal(page.Items[0].JudgedAt) || page.Authority != AuthorityDataOnly {
		t.Fatalf("page: %+v", page)
	}
	if page.Memory != in.Target || page.Items[0].Trace == nil || *page.Items[0].Trace != in.Trace || page.Items[0].Memory != in.Target {
		t.Fatalf("refs: %+v", page.Items[0])
	}
	q := outcomeQuery(m.ID)
	q.Dimension = "work_task"
	if page, e = f.svc.ListMemoryOutcomes(testCtx, f.human, q); e != nil || len(page.Items) != 2 || page.Summary.Total != 2 || page.Summary.Passed != 1 {
		t.Fatalf("dimension: %+v %v", page, e)
	}
	future := time.Now().Add(time.Hour)
	q = outcomeQuery(m.ID)
	q.From = &future
	if page, e = f.svc.ListMemoryOutcomes(testCtx, f.human, q); e != nil || len(page.Items) != 0 || page.Summary.Total != 0 || page.Summary.PassRate != nil || page.Summary.LastJudgedAt != nil {
		t.Fatalf("from: %+v %v", page, e)
	}
	q = outcomeQuery(m.ID)
	q.Limit = 1
	seen := map[string]bool{}
	for i := 0; ; i++ {
		page, e = f.svc.ListMemoryOutcomes(testCtx, f.human, q)
		if e != nil || len(page.Items) != 1 || page.Summary.Total != 3 {
			t.Fatalf("page %d: %+v %v", i, page, e)
		}
		seen[page.Items[0].Evaluation.ID] = true
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatal("pages repeated or skipped", seen)
	}
	q.Dimension = "work_task" // a cursor is bound to its filters
	if _, e = f.svc.ListMemoryOutcomes(testCtx, f.human, q); !errors.Is(e, ErrInvalid) {
		t.Fatal("cursor reused across filters:", e)
	}
}

func TestMemoryOutcomeReadBatchSummaries(t *testing.T) {
	f, m, _ := recordOutcomes(t)
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: "unrelated"}))
	out, e := f.svc.SummarizeMemoryOutcomes(testCtx, f.human, MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{m.ID, "missing-memory", other.ID}})
	if e != nil || len(out.Items) != 3 {
		t.Fatal(out, e)
	}
	for _, it := range out.Items {
		switch it.MemoryID {
		case m.ID:
			if !it.Available || it.Total != 3 || it.Passed != 2 {
				t.Fatalf("summary: %+v", it)
			}
		default:
			if it.Available || it.Total != 0 || it.PassRate != nil {
				t.Fatalf("unknown memory reported: %+v", it)
			}
		}
	}
	ids := make([]string, memoryOutcomeBatchLimit+1)
	for i := range ids {
		ids[i] = "m-" + string(rune('a'+i%26)) + strings.Repeat("x", i)
	}
	for _, bad := range []MemoryOutcomeQuery{{Scope: scopeA, MemoryIDs: ids}, {Scope: scopeA, MemoryIDs: []string{m.ID, m.ID}}, {Scope: scopeA}, {Scope: scopeA, MemoryIDs: []string{m.ID}, Limit: 5}, {Scope: scopeA, MemoryIDs: []string{m.ID}, Dimension: "other"}} {
		if _, e = f.svc.SummarizeMemoryOutcomes(testCtx, f.human, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad batch accepted:", len(bad.MemoryIDs), e)
		}
	}
}

func TestMemoryOutcomeReadDoesNotLeakAcrossScopes(t *testing.T) {
	f, m, _ := recordOutcomes(t)
	// A foreign-scope verdict on the same memory id must not count in scopeA.
	foreign := f.repo.rows("evaluations")[0]
	foreign.ID, foreign.Scope, foreign.Passed = "foreign-outcome", scopeB, true
	if e := f.repo.Insert(testCtx, "evaluations", foreign); e != nil {
		t.Fatal(e)
	}
	page, e := f.svc.ListMemoryOutcomes(testCtx, f.human, outcomeQuery(m.ID))
	if e != nil || page.Summary.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("foreign row counted: %+v %v", page.Summary, e)
	}
	q := outcomeQuery(m.ID)
	q.Scope = scopeB
	if _, e = f.svc.ListMemoryOutcomes(testCtx, f.human, q); !errors.Is(e, ErrNotFound) {
		t.Fatal("memory read through another scope:", e)
	}
	out, e := f.svc.SummarizeMemoryOutcomes(testCtx, f.human, q)
	if e != nil || out.Items[0].Available || out.Items[0].Total != 0 {
		t.Fatalf("foreign summary: %+v %v", out, e)
	}
	f.auth.actors["far"] = fakeActor{principal: "far", role: roleHuman, verbs: map[string]bool{"read": true}, wrongScope: true}
	f.auth.actors["mute"] = actor("mute", roleHuman, true, "write")
	for _, token := range []string{"far", "mute", "nobody"} {
		if _, e = f.svc.ListMemoryOutcomes(testCtx, token, outcomeQuery(m.ID)); !errors.Is(e, ErrDenied) {
			t.Fatal(token, "read outcomes:", e)
		}
		if _, e = f.svc.SummarizeMemoryOutcomes(testCtx, token, outcomeQuery(m.ID)); !errors.Is(e, ErrDenied) {
			t.Fatal(token, "read summaries:", e)
		}
	}
}

func TestMemoryOutcomeReadHTTPReturnsReferencesOnly(t *testing.T) {
	f, m, _ := recordOutcomes(t)
	h := Handler(f.svc)
	q := "?workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID
	call := func(method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	code, body := call("GET", "/v1/memories/"+m.ID+"/outcomes"+q+"&dimension=work_task&limit=1", "")
	var page MemoryOutcomePage
	if code != http.StatusOK || json.Unmarshal([]byte(body), &page) != nil || len(page.Items) != 1 || page.NextCursor == "" || page.Summary.Total != 2 {
		t.Fatal(code, body)
	}
	scope, _ := json.Marshal(scopeA)
	code, sum := call("POST", "/v1/memory-outcomes/summary", `{"scope":`+string(scope)+`,"memory_ids":["`+m.ID+`"]}`)
	if code != http.StatusOK || !strings.Contains(sum, `"total":3`) {
		t.Fatal(code, sum)
	}
	for _, raw := range []string{body, sum} {
		for _, text := range []string{m.Text, "actual_test_exit", "Independent pinned memory checker", strings.Repeat("a", 64)} {
			if strings.Contains(raw, text) {
				t.Fatalf("response leaked %q: %s", text, raw)
			}
		}
	}
	for _, bad := range []string{q + "&dimension=", q + "&from=yesterday", q + "&limit=101", q + "&extra=1", "?workspace_id=ws-a"} {
		if code, _ = call("GET", "/v1/memories/"+m.ID+"/outcomes"+bad, ""); code != http.StatusBadRequest {
			t.Fatal(bad, code)
		}
	}
	if code, _ = call("POST", "/v1/memory-outcomes/summary", `{"scope":`+string(scope)+`,"memory_ids":["`+m.ID+`"],"cursor":"x"}`); code != http.StatusBadRequest {
		t.Fatal("batch cursor accepted", code)
	}
}

func TestPostgresMemoryOutcomeReadScopesAndPages(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	put := func(id string, scope Scope, memory, dim string, passed bool, at time.Time) {
		r := Record{ID: id, Scope: scope, Kind: memoryOutcomeKind, Status: "active", Revision: 1, Text: "checker", Tags: []string{"dimension:" + dim}, Target: &Ref{Collection: colMemories, ID: memory, Revision: 1},
			Relations: []Ref{{Collection: colMemories, ID: memory, Revision: 1}, {Collection: "execution_traces", ID: "t-" + id, Revision: 1}}, Passed: passed, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}, CreatedBy: "checker", CreatedByType: roleService, CreatedAt: at, UpdatedAt: at}
		if e := p.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
	}
	put("o1", scopeA, "mem-1", "work_task", true, now.Add(-3*time.Minute))
	put("o2", scopeA, "mem-1", "work_task", false, now.Add(-2*time.Minute))
	put("o3", scopeA, "mem-1", "personal_compliance", true, now.Add(-time.Minute))
	put("o4", scopeA, "mem-2", "work_task", true, now)
	put("leak", scopeB, "mem-1", "work_task", true, now)
	other := scopeA
	other.UserID = "someone-else"
	put("leak-user", other, "mem-1", "work_task", true, now)
	counts, e := p.MemoryOutcomeCounts(testCtx, MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"mem-1", "mem-2", "mem-3"}})
	if e != nil || len(counts) != 2 || counts[0] != (MemoryOutcomeCount{MemoryID: "mem-1", Total: 3, Passed: 2, Last: counts[0].Last}) || !counts[0].Last.Equal(now.Add(-time.Minute)) || counts[1].MemoryID != "mem-2" || counts[1].Total != 1 {
		t.Fatalf("counts: %+v %v", counts, e)
	}
	from := now.Add(-150 * time.Second)
	counts, e = p.MemoryOutcomeCounts(testCtx, MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"mem-1"}, Dimension: "work_task", From: &from})
	if e != nil || len(counts) != 1 || counts[0].Total != 1 || counts[0].Passed != 0 {
		t.Fatalf("filtered counts: %+v %v", counts, e)
	}
	q := MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"mem-1"}, Limit: 2}
	rows, e := p.MemoryOutcomes(testCtx, q)
	if e != nil || len(rows) != 3 || rows[0].ID != "o3" || rows[1].ID != "o2" {
		t.Fatalf("page: %v %v", outcomeIDs(rows), e)
	}
	q.Cursor = memoryOutcomeCursorFor(q, rows[1])
	if rows, e = p.MemoryOutcomes(testCtx, q); e != nil || len(rows) != 1 || rows[0].ID != "o1" {
		t.Fatalf("next page: %v %v", outcomeIDs(rows), e)
	}
}

func outcomeIDs(rows []Record) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
