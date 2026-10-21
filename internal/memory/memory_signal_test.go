package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) MemoryUseCounts(_ context.Context, scope Scope, ids []string) ([]MemoryUseCount, error) {
	if checkScope(scope) != nil || len(ids) == 0 || len(ids) > memoryGraphMapNodes {
		return nil, ErrInvalid
	}
	by := map[string]*MemoryUseCount{}
	for _, r := range f.rows("jobs") {
		if r.Scope != scope || r.Kind != "context_usage" || r.Status != "done" || r.Usage == nil {
			continue
		}
		seen := map[string]bool{}
		for _, it := range r.Usage.Items {
			id := it.Memory.ID
			if it.Memory.Collection != colMemories || seen[id] || !strings.Contains(" "+strings.Join(ids, " ")+" ", " "+id+" ") {
				continue
			}
			seen[id] = true
			c := by[id]
			if c == nil {
				c = &MemoryUseCount{MemoryID: id}
				by[id] = c
			}
			c.Uses++
			if u := (MemoryProjectUse{it.Memory.Revision, r.Usage.ProjectKey}); u.ProjectKey != "" && !slices.Contains(c.Projects, u) {
				c.Projects = append(c.Projects, u)
			}
			if r.CreatedAt.After(c.Last) {
				c.Last = r.CreatedAt
			}
		}
	}
	out := []MemoryUseCount{}
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MemoryID < out[j].MemoryID })
	return out, nil
}

func useReceipt(t *testing.T, repo interface {
	Insert(context.Context, string, Record) error
}, id string, scope Scope, memory Ref, at time.Time) {
	t.Helper()
	r := Record{ID: id, Scope: scope, Kind: "context_usage", Status: "done", Revision: 1, CreatedAt: at, UpdatedAt: at, CreatedBy: "agent-1", Usage: &ContextUsage{Items: []MemoryUse{{Memory: memory, Rank: 1}}}}
	if e := repo.Insert(testCtx, "jobs", r); e != nil {
		t.Fatal(e)
	}
}

func signalOf(t *testing.T, f *fixture, scope Scope, id string) MemorySignalView {
	t.Helper()
	page, e := f.svc.MemorySignals(testCtx, f.human, MemorySignalQuery{Scope: scope, MemoryIDs: []string{id}})
	if e != nil || len(page.Items) != 1 || page.Authority != AuthorityDataOnly {
		t.Fatal(page, e)
	}
	return page.Items[0]
}

func TestMemorySignalRecomputeIsIdempotentAndLeavesMemoryAlone(t *testing.T) {
	f, m, in := recordOutcomes(t) // work_task fail+pass, personal_compliance pass
	before := f.get(f.human, colMemories, scopeA, m.ID)
	if v := signalOf(t, f, scopeA, m.ID); !v.Available || v.Signal != nil {
		t.Fatalf("signal before first run: %+v", v)
	}
	used := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	useReceipt(t, f.repo, "use-1", scopeA, in.Target, used.Add(-time.Hour))
	useReceipt(t, f.repo, "use-2", scopeA, in.Target, used)
	out, e := f.svc.RecomputeMemorySignals(testCtx, f.human, MemorySignalRecomputeInput{Scope: scopeA})
	if e != nil || out.Computed < 1 || out.Changed != out.Computed || out.NextCursor != "" {
		t.Fatal(out, e)
	}
	v := signalOf(t, f, scopeA, m.ID)
	s := v.Signal
	if s == nil || s.Memory.ID != m.ID || s.Samples != 3 || s.Passed != 2 || s.PassRate == nil || *s.PassRate != 2.0/3 || s.LastJudgedAt == nil ||
		s.PersonalCompliance != (MemorySignalCounts{1, 1}) || s.WorkTask != (MemorySignalCounts{2, 1}) || s.Uses != 2 || s.LastUsedAt == nil || !s.LastUsedAt.Equal(used) {
		t.Fatalf("signal: %+v", s)
	}
	rows := len(f.repo.rows("jobs"))
	again, e := f.svc.RecomputeMemorySignals(testCtx, f.human, MemorySignalRecomputeInput{Scope: scopeA})
	if e != nil || again.Changed != 0 || again.Computed != out.Computed || len(f.repo.rows("jobs")) != rows {
		t.Fatal("repeat run rewrote:", again, e)
	}
	if v2 := signalOf(t, f, scopeA, m.ID); structuredText(v2) != structuredText(v) {
		t.Fatalf("repeat run moved the signal: %+v", v2.Signal)
	}
	useReceipt(t, f.repo, "use-3", scopeA, in.Target, time.Now().UTC().Truncate(time.Millisecond))
	if out, e = f.svc.RecomputeMemorySignals(testCtx, f.human, MemorySignalRecomputeInput{Scope: scopeA}); e != nil || out.Changed != 1 {
		t.Fatal("new use not picked up:", out, e)
	}
	if s = signalOf(t, f, scopeA, m.ID).Signal; s.Uses != 3 || s.Samples != 3 {
		t.Fatalf("after new use: %+v", s)
	}
	after := f.get(f.human, colMemories, scopeA, m.ID)
	if after.Status != before.Status || after.Revision != before.Revision || after.UpdatedAt != before.UpdatedAt {
		t.Fatal("recompute changed the memory")
	}
	// Paging: one memory per call walks every active memory once.
	q := MemorySignalRecomputeInput{Scope: scopeA, Limit: 1}
	seen := 0
	for i := 0; i < 50; i++ {
		page, e := f.svc.RecomputeMemorySignals(testCtx, f.human, q)
		if e != nil || page.Computed > 1 {
			t.Fatal(page, e)
		}
		seen += page.Computed
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
	}
	if seen != out.Computed {
		t.Fatal("paging skipped or repeated", seen, out.Computed)
	}
	for _, bad := range []MemorySignalRecomputeInput{{Scope: scopeA, Limit: 101}, {Scope: scopeA, Cursor: "x"}, {Scope: Scope{}}} {
		if _, e = f.svc.RecomputeMemorySignals(testCtx, f.human, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad input accepted:", bad, e)
		}
	}
}

func TestMemorySignalDoesNotLeakAcrossScopes(t *testing.T) {
	f, m, in := recordOutcomes(t)
	foreign := f.repo.rows("evaluations")[0]
	foreign.ID, foreign.Scope, foreign.Passed = "foreign-outcome", scopeB, true
	if e := f.repo.Insert(testCtx, "evaluations", foreign); e != nil {
		t.Fatal(e)
	}
	other := scopeA
	other.UserID = "someone-else"
	useReceipt(t, f.repo, "use-foreign", scopeB, in.Target, time.Now().UTC())
	useReceipt(t, f.repo, "use-other-user", other, in.Target, time.Now().UTC())
	for _, scope := range []Scope{scopeA, scopeB} {
		if _, e := f.svc.RecomputeMemorySignals(testCtx, f.human, MemorySignalRecomputeInput{Scope: scope}); e != nil {
			t.Fatal(e)
		}
	}
	if s := signalOf(t, f, scopeA, m.ID).Signal; s == nil || s.Samples != 3 || s.Uses != 0 {
		t.Fatalf("foreign rows counted: %+v", s)
	}
	if v := signalOf(t, f, scopeB, m.ID); v.Available || v.Signal != nil {
		t.Fatalf("memory read through another scope: %+v", v)
	}
	signals := 0
	for _, r := range f.repo.rows("jobs") {
		if r.Kind == memorySignalKind {
			signals++
			if r.Scope != scopeA {
				t.Fatal("signal written outside the memory's scope:", r.Scope)
			}
		}
	}
	if signals == 0 {
		t.Fatal("no signal written")
	}
	f.auth.actors["far"] = fakeActor{principal: "far", role: roleHuman, verbs: map[string]bool{"read": true, "write": true}, wrongScope: true}
	f.auth.actors["mute"] = actor("mute", roleHuman, true, "write")
	f.auth.actors["reader"] = actor("reader", roleHuman, true, "read")
	for _, token := range []string{"far", "mute", "nobody", f.agent, "reader", f.work} {
		if _, e := f.svc.RecomputeMemorySignals(testCtx, token, MemorySignalRecomputeInput{Scope: scopeA}); !errors.Is(e, ErrDenied) {
			t.Fatal(token, "recomputed:", e)
		}
	}
	for _, token := range []string{"far", "mute", "nobody"} {
		if _, e := f.svc.MemorySignals(testCtx, token, MemorySignalQuery{Scope: scopeA, MemoryIDs: []string{m.ID}}); !errors.Is(e, ErrDenied) {
			t.Fatal(token, "read signals:", e)
		}
	}
}

func TestMemorySignalHTTPReturnsCountsOnly(t *testing.T) {
	f, m, in := recordOutcomes(t)
	useReceipt(t, f.repo, "use-1", scopeA, in.Target, time.Now().UTC())
	h := Handler(f.svc)
	call := func(method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	scope, _ := json.Marshal(scopeA)
	code, body := call("POST", "/v1/memory-signals/recompute", `{"scope":`+string(scope)+`}`)
	if code != http.StatusOK || !strings.Contains(body, `"next_cursor":""`) {
		t.Fatal(code, body)
	}
	code, one := call("GET", "/v1/memories/"+m.ID+"/signal?workspace_id="+scopeA.WorkspaceID+"&project_id="+scopeA.ProjectID, "")
	var view MemorySignalView
	if code != http.StatusOK || json.Unmarshal([]byte(one), &view) != nil || view.Signal == nil || view.Signal.Samples != 3 || view.Signal.Uses != 1 || view.UseMeasurementBasis != "server_return" || view.ActualUseStatus != "not_measured" {
		t.Fatal(code, one)
	}
	code, batch := call("POST", "/v1/memory-signals", `{"scope":`+string(scope)+`,"memory_ids":["`+m.ID+`","missing"]}`)
	if code != http.StatusOK || !strings.Contains(batch, `"available":false`) {
		t.Fatal(code, batch)
	}
	for _, raw := range []string{body, one, batch} {
		for _, text := range []string{m.Text, "actual_test_exit", strings.Repeat("a", 64)} {
			if strings.Contains(raw, text) {
				t.Fatalf("response leaked %q: %s", text, raw)
			}
		}
	}
	for _, bad := range []string{`{"scope":` + string(scope) + `,"memory_ids":[]}`, `{"scope":` + string(scope) + `,"memory_ids":["a","a"]}`, `{"scope":` + string(scope) + `,"memory_ids":["a"],"extra":1}`} {
		if code, _ = call("POST", "/v1/memory-signals", bad); code != http.StatusBadRequest {
			t.Fatal(bad, code)
		}
	}
}

func TestPostgresMemoryUseCountsScopes(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	ref := func(id string) Ref { return Ref{Collection: colMemories, ID: id, Revision: 1} }
	put := func(id string, scope Scope, at time.Time, items ...MemoryUse) {
		r := Record{ID: id, Scope: scope, Kind: "context_usage", Status: "done", Revision: 1, CreatedAt: at, UpdatedAt: at, CreatedBy: "agent-1", Usage: &ContextUsage{Items: items}}
		if e := p.Insert(testCtx, "jobs", r); e != nil {
			t.Fatal(e)
		}
	}
	put("u1", scopeA, now.Add(-time.Hour), MemoryUse{Memory: ref("mem-1"), Rank: 1}, MemoryUse{Memory: ref("mem-2"), Rank: 2})
	put("u2", scopeA, now, MemoryUse{Memory: ref("mem-1"), Rank: 1})
	put("leak", scopeB, now.Add(time.Hour), MemoryUse{Memory: ref("mem-1"), Rank: 1})
	other := scopeA
	other.AgentID = "agent-x"
	put("leak-agent", other, now.Add(time.Hour), MemoryUse{Memory: ref("mem-1"), Rank: 1})
	counts, e := p.MemoryUseCounts(testCtx, scopeA, []string{"mem-1", "mem-2", "mem-3"})
	if e != nil || len(counts) != 2 || counts[0].MemoryID != "mem-1" || counts[0].Uses != 2 || !counts[0].Last.Equal(now) || counts[1].MemoryID != "mem-2" || counts[1].Uses != 1 {
		t.Fatalf("counts: %+v %v", counts, e)
	}
	sig := MemorySignal{Memory: ref("mem-1"), Samples: 1, Uses: 2, ComputedAt: now}
	if e = p.Insert(testCtx, "jobs", Record{ID: memorySignalID(scopeA, "mem-1"), Scope: scopeA, Kind: memorySignalKind, Status: JobStatusDone, Revision: 1, Target: &sig.Memory, MemorySignal: &sig, CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
	got, e := p.Get(testCtx, "jobs", scopeA, memorySignalID(scopeA, "mem-1"))
	if e != nil || got.MemorySignal == nil || !sameMemorySignal(*got.MemorySignal, sig) {
		t.Fatalf("signal round trip: %+v %v", got.MemorySignal, e)
	}
	if _, e = p.Get(testCtx, "jobs", scopeB, memorySignalID(scopeA, "mem-1")); !errors.Is(e, ErrNotFound) {
		t.Fatal("signal read across scope:", e)
	}
}

// deletingListRepo deletes the listed memory after the page is read, the
// window in which a recompute could otherwise write an orphan signal.
type deletingListRepo struct {
	*fakeRepo
	del func()
}

func (r deletingListRepo) ListMemories(ctx context.Context, in MemoryListInput) ([]Record, error) {
	rows, e := r.fakeRepo.ListMemories(ctx, in)
	r.del()
	return rows, e
}

func memorySignalRows(f *fixture) []Record {
	out := []Record{}
	for _, r := range f.repo.rows("jobs") {
		if r.Kind == memorySignalKind {
			out = append(out, r)
		}
	}
	return out
}

func TestMemorySignalLeavesWithItsMemory(t *testing.T) {
	f, m, _ := recordOutcomes(t)
	if _, e := f.svc.RecomputeMemorySignals(testCtx, f.human, MemorySignalRecomputeInput{Scope: scopeA}); e != nil {
		t.Fatal(e)
	}
	if len(memorySignalRows(f)) == 0 {
		t.Fatal("no signal before delete")
	}
	if e := f.svc.Delete(testCtx, f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}); e != nil {
		t.Fatal(e)
	}
	for _, r := range memorySignalRows(f) {
		if r.Target != nil && r.Target.ID == m.ID {
			t.Fatal("orphan signal kept the deleted memory id")
		}
	}
	if v := signalOf(t, f, scopeA, m.ID); v.Available || v.Signal != nil {
		t.Fatalf("deleted memory still has a signal: %+v", v)
	}
}

func TestMemorySignalRecomputeLosesRaceToDelete(t *testing.T) {
	f, m, _ := recordOutcomes(t)
	racing := &Service{Auth: f.auth, Embedder: f.emb, Repo: deletingListRepo{f.repo, func() {
		if e := f.svc.Delete(testCtx, f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}); e != nil {
			t.Fatal(e)
		}
	}}}
	if _, e := racing.RecomputeMemorySignals(testCtx, f.human, MemorySignalRecomputeInput{Scope: scopeA}); !errors.Is(e, ErrConflict) {
		t.Fatal("recompute wrote past a concurrent delete:", e)
	}
	if n := len(memorySignalRows(f)); n != 0 {
		t.Fatal("orphan signal rows:", n)
	}
}

func (f *fakeRepo) MemoryUsageWindow(_ context.Context, scope Scope, since, recent time.Time) ([]MemoryUsageCount, error) {
	if checkScope(scope) != nil || since.IsZero() || recent.Before(since) {
		return nil, ErrInvalid
	}
	live := map[string]bool{}
	for _, m := range f.rows(colMemories) {
		live[m.ID] = m.Scope == scope && !m.ContentErased && !m.PrivacyQuarantined
	}
	by := map[string]*MemoryUsageCount{}
	for _, r := range f.rows("jobs") {
		if r.Scope != scope || r.Kind != "context_usage" || r.Status != "done" || r.Usage == nil || r.CreatedAt.Before(since) {
			continue
		}
		seen := map[string]bool{}
		for _, it := range r.Usage.Items {
			id := it.Memory.ID
			if it.Memory.Collection != colMemories || seen[id] || !live[id] {
				continue
			}
			seen[id] = true
			c := by[id]
			if c == nil {
				c = &MemoryUsageCount{MemoryID: id}
				by[id] = c
			}
			c.Uses++
			if !r.CreatedAt.Before(recent) {
				c.Recent++
			}
			if r.CreatedAt.After(c.Last) {
				c.Last = r.CreatedAt
			}
		}
	}
	out := []MemoryUsageCount{}
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MemoryID < out[j].MemoryID })
	return out, nil
}
