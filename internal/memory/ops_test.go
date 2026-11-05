package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// opsFakeRepo adds the operator query surface to the shared fake store.
type opsFakeRepo struct{ *fakeRepo }

func (o opsFakeRepo) OpsRecords(_ context.Context, q opsRecordQuery) ([]Record, error) {
	out := []Record{}
	for _, r := range o.rows(q.Collection) {
		if opsFakeMatch(r, q) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func opsFakeMatch(r Record, q opsRecordQuery) bool {
	target := ""
	if r.Target != nil {
		target = r.Target.ID
	}
	after := q.After == nil || r.CreatedAt.Before(q.After.At) || r.CreatedAt.Equal(q.After.At) && r.ID < q.After.ID
	return sameScope(r.Scope, q.Scope) && after && (q.Kind == "" || r.Kind == q.Kind) && strings.HasPrefix(r.Kind, q.KindPrefix) &&
		(len(q.Statuses) == 0 || slices.Contains(q.Statuses, r.Status)) && (q.Actor == "" || r.CreatedBy == q.Actor) &&
		(q.TargetID == "" || target == q.TargetID) && (q.Tag == "" || slices.Contains(r.Tags, q.Tag)) && (q.Transport == "" || r.Audit != nil && r.Audit.Transport == q.Transport) &&
		(q.From.IsZero() || !r.CreatedAt.Before(q.From)) && (q.To.IsZero() || r.CreatedAt.Before(q.To))
}

func (o opsFakeRepo) OpsCounts(_ context.Context, q opsCountQuery) ([]OpsCount, error) {
	counts := map[[2]string]int64{}
	for _, r := range o.rows(q.Collection) {
		if !sameScope(r.Scope, q.Scope) || len(q.Statuses) > 0 && !slices.Contains(q.Statuses, r.Status) {
			continue
		}
		bucket := ""
		if q.Bucket == "day" {
			bucket = r.CreatedAt.UTC().Format("2006-01-02")
		}
		tags := r.Tags
		if r.MentalModel != nil {
			tags = r.MentalModel.Refresh.Tags
		}
		if q.Bucket == "day" && q.TimeField == "occurred" && r.Occurred != nil {
			bucket = r.Occurred.From.UTC().Format("2006-01-02")
		}
		keys := map[string][]string{"": {""}, "kind": {r.Kind}, "status": {r.Status}, "tag": tags}[q.By]
		for _, k := range keys {
			counts[[2]string{bucket, k}]++
		}
	}
	out := []OpsCount{}
	for k, n := range counts {
		out = append(out, OpsCount{Bucket: k[0], Key: k[1], Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket+out[i].Key < out[j].Bucket+out[j].Key })
	return out, nil
}

func newOpsFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.svc.Repo = opsFakeRepo{f.repo}
	f.auth.actors[f.human].verbs["ops"] = true
	return f
}

func (f *fixture) put(col string, r Record) Record {
	f.t.Helper()
	if r.Scope == (Scope{}) {
		r.Scope = scopeA
	}
	if r.Revision == 0 {
		r.Revision = 1
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	r.UpdatedAt = r.CreatedAt
	if err := f.repo.Insert(testCtx, col, r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) job(id string) Record {
	f.t.Helper()
	r, err := f.repo.Get(testCtx, colJobs, scopeA, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestOpsAuditListRedactsAndPages(t *testing.T) {
	f := newOpsFixture(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	f.put("audit_events", Record{ID: "a1", Kind: "set_reach:memories", CreatedBy: "human-1", CreatedAt: base.Add(-3 * time.Second), Target: &Ref{ID: "m1"},
		Text: "private body", Audit: &AuditMetadata{Decision: "allow", ReasonCode: "dup", Reason: "free text about a person"}})
	f.put("audit_events", Record{ID: "a2", Kind: "leak sk-abcdefghijklmnop", CreatedBy: "human-1", CreatedAt: base.Add(-2 * time.Second)})
	f.put("audit_events", Record{ID: "a3", Kind: "wiki:job_created", CreatedBy: "human-2", CreatedAt: base.Add(-time.Second)})

	page, err := f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "a3" || page.Items[1].Action != opsRedacted || page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != "a1" || next.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", next, err)
	}
	raw := structuredText(next.Items[0])
	if strings.Contains(raw, "private body") || strings.Contains(raw, "free text") || next.Items[0].ReasonCode != "dup" {
		t.Fatalf("audit view leaked content: %s", raw)
	}
	// A cursor is bound to its filters.
	if _, err = f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Limit: 2, Actor: "human-1", Cursor: page.NextCursor}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rebound cursor err = %v", err)
	}
	filtered, err := f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Action: "wiki:"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != "a3" {
		t.Fatalf("action filter = %+v, %v", filtered, err)
	}
	f.put("audit_events", Record{ID: "a4", Kind: "remember:memories", CreatedBy: "human-1", CreatedAt: base.Add(-4 * time.Second), Audit: &AuditMetadata{Transport: "mcp"}})
	viaMCP, err := f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Transport: "mcp"})
	if err != nil || len(viaMCP.Items) != 1 || viaMCP.Items[0].ID != "a4" {
		t.Fatalf("transport filter = %+v, %v", viaMCP, err)
	}
	if _, err = f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Transport: "pigeon"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad transport err = %v", err)
	}
	if _, err = f.svc.ListAuditEvents(testCtx, f.agent, OpsListInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent audit read err = %v", err)
	}
	stats, err := f.svc.AuditEventStats(testCtx, f.human, scopeA, base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil || stats.Total != 4 || len(stats.ByDay) == 0 || !slices.ContainsFunc(stats.ByAction, func(c OpsCount) bool { return c.Key == opsRedacted }) {
		t.Fatalf("audit stats = %+v, %v", stats, err)
	}
}

func TestOpsCancelPendingIsSkippedByWorkers(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colJobs, Record{ID: "e1", Kind: "embedding", Status: JobStatusPending})
	v, err := f.svc.CancelOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA, ExpectedRevision: 1})
	if err != nil || v.Status != JobStatusCancelled || v.Revision != 2 {
		t.Fatalf("cancel = %+v, %v", v, err)
	}
	if _, err = f.repo.ClaimJob(testCtx, time.Now(), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("worker claimed a cancelled job: %v", err)
	}
	// The wiki runner treats cancelled as finished and never calls a model.
	f.put(colJobs, Record{ID: "w1", Kind: wikiJobKind, Status: JobStatusPending, Wiki: &WikiRecord{Job: &WikiJob{KnowledgeSourceID: "k1"}}})
	if _, err = f.svc.CancelOperation(testCtx, f.human, "w1", OperationChangeInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.RunWikiJob(testCtx, f.human, scopeA, "w1")
	if err != nil || got.Status != JobStatusCancelled || got.Attempts != 0 {
		t.Fatalf("wiki run on cancelled = %+v, %v", got, err)
	}
	if !slices.ContainsFunc(f.repo.rows("audit_events"), func(r Record) bool { return r.Kind == "cancel:jobs" }) {
		t.Fatal("cancel not audited")
	}
}

func TestOpsCancelRunningRevokesLease(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colJobs, Record{ID: "e1", Kind: "embedding", Status: JobStatusPending})
	claimed, err := f.repo.ClaimJob(testCtx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CancelOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	if err = f.repo.FinishJob(testCtx, claimed, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("lease holder finished a cancelled job: %v", err)
	}
	if j := f.job("e1"); j.Status != JobStatusCancelled || j.LeaseToken != "" {
		t.Fatalf("job after cancel = %+v", j)
	}
	if _, err = f.svc.CancelOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second cancel err = %v", err)
	}
}

func TestOpsRetryAndDeleteTransitions(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colJobs, Record{ID: "e1", Kind: "embedding", Status: JobStatusFailed, Attempts: maxJobAttempts})
	for i := 1; i <= maxManualJobRetries; i++ {
		v, err := f.svc.RetryOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA})
		if err != nil || v.Status != JobStatusPending || v.Attempts != 0 || v.ManualRetries != i {
			t.Fatalf("retry %d = %+v, %v", i, v, err)
		}
		if _, err = f.svc.RetryOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrConflict) {
			t.Fatalf("retry of pending err = %v", err)
		}
		if _, err = f.svc.DeleteOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrConflict) {
			t.Fatalf("delete of pending err = %v", err)
		}
		j := f.job("e1")
		j.Status = JobStatusFailed
		if err = f.repo.Replace(testCtx, colJobs, j, j.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.RetryOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry past the bound err = %v", err)
	}
	if _, err := f.svc.DeleteOperation(testCtx, f.agent, "e1", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent delete err = %v", err)
	}
	if _, err := f.svc.DeleteOperation(testCtx, f.human, "e1", OperationChangeInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Get(testCtx, colJobs, scopeA, "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted job still there: %v", err)
	}
	// Quota, receipt and erasure rows are listed but never operable.
	f.put(colJobs, Record{ID: "q1", Kind: "candidate_quota", Status: JobStatusDone})
	if _, err := f.svc.DeleteOperation(testCtx, f.human, "q1", OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete of quota row err = %v", err)
	}
	list, err := f.svc.ListOperations(testCtx, f.human, OpsListInput{Scope: scopeA, Status: JobStatusDone})
	if err != nil || len(list.Items) != 1 || list.Items[0].Operable {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestOpsTagsHumanOnlyMetadataEdit(t *testing.T) {
	f := newOpsFixture(t)
	m := f.put(colMemories, Record{ID: "m1", Kind: "decision", Status: "active", Text: "use rebase", Tags: []string{"git", "old"}, ApprovedBy: "human-1"})
	f.put(colMemories, Record{ID: "m2", Kind: "lesson", Status: "active", Text: "x", Tags: []string{"old"}})
	if _, err := f.svc.SetMemoryTags(testCtx, f.agent, "m1", MemoryTagsInput{Scope: scopeA, Add: []string{"x"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent tag edit err = %v", err)
	}
	if _, err := f.svc.SetMemoryTags(testCtx, f.human, "m1", MemoryTagsInput{Scope: scopeA, Add: []string{"token: sk-abcdefghijklmnop"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret tag err = %v", err)
	}
	got, err := f.svc.SetMemoryTags(testCtx, f.human, "m1", MemoryTagsInput{Scope: scopeA, ExpectedRevision: 1, Add: []string{"vcs"}, Remove: []string{"git"}})
	if err != nil || !slices.Equal(got.Tags, []string{"old", "vcs"}) || got.Text != m.Text || got.Status != "active" || got.ApprovedBy != "human-1" || got.Revision != 2 {
		t.Fatalf("tag edit = %+v, %v", got, err)
	}
	res, err := f.svc.RenameTag(testCtx, f.human, TagRenameInput{Scope: scopeA, From: "old", To: "legacy"})
	if err != nil || res.Renamed != 2 || res.More {
		t.Fatalf("rename = %+v, %v", res, err)
	}
	tags, err := f.svc.ListTags(testCtx, f.agent, TagListInput{Scope: scopeA})
	if err != nil || len(tags.Items) != 2 || tags.Items[0] != (TagCount{Tag: "legacy", Count: 2}) {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	audits := 0
	for _, r := range f.repo.rows("audit_events") {
		if r.Kind == "set_tags:memories" || r.Kind == "rename_tag:memories" {
			audits++
		}
	}
	if audits != 3 {
		t.Fatalf("tag audits = %d", audits)
	}
}

func TestOpsStatsAndLivez(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colMemories, Record{ID: "m1", Kind: "decision", Status: "active"})
	f.put(colMemories, Record{ID: "m2", Kind: "lesson", Status: "superseded"})
	f.put("memory_candidates", Record{ID: "c1", Kind: "lesson", Status: "candidate"})
	f.put("memory_candidates", Record{ID: "c2", Kind: "lesson", Status: "rejected"})
	st, err := f.svc.Stats(testCtx, f.human, scopeA)
	if err != nil || st.Memories.Total != 2 || st.Memories.ByKind["lesson"] != 1 || st.Memories.ByStatus["active"] != 1 || st.PendingCandidates != 1 {
		t.Fatalf("stats = %+v, %v", st, err)
	}
	// An agent reads its space's counts with read:memories alone.
	if ast, err := f.svc.Stats(testCtx, f.agent, scopeA); err != nil || ast.Memories.Total != 2 || ast.PendingCandidates != 1 {
		t.Fatalf("agent stats = %+v, %v", ast, err)
	}
	now := time.Now().UTC()
	ts, err := f.svc.StatsTimeseries(testCtx, f.human, StatsTimeseriesInput{Scope: scopeA, From: now.Add(-time.Hour), To: now.Add(time.Hour), By: "status"})
	if err != nil || ts.Bucket != "day" || len(ts.Points) != 2 {
		t.Fatalf("timeseries = %+v, %v", ts, err)
	}
	if _, err = f.svc.StatsTimeseries(testCtx, f.human, StatsTimeseriesInput{Scope: scopeA, From: now.Add(-time.Hour), To: now, Bucket: "year"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad bucket err = %v", err)
	}
	w := httptest.NewRecorder()
	Handler(&Service{}).ServeHTTP(w, httptest.NewRequest("GET", "/livez", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "alive") {
		t.Fatalf("livez = %d %s", w.Code, w.Body.String())
	}
}

func TestOpsTagEditsKeepSystemTagsAndCountSkips(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colMemories, Record{ID: "m1", Kind: "decision", Status: "active", Text: "a", Tags: []string{"session:s1", "old"}})
	f.put(colMemories, Record{ID: "m2", Kind: "decision", Status: "active", Text: "b", Tags: []string{"old"}, ConflictID: "c1"})
	for _, in := range []MemoryTagsInput{{Scope: scopeA, Add: []string{"session:s2"}}, {Scope: scopeA, Remove: []string{"session:s1"}}} {
		if _, err := f.svc.SetMemoryTags(testCtx, f.human, "m1", in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("system tag edit %+v err = %v", in, err)
		}
	}
	for _, in := range []TagRenameInput{{Scope: scopeA, From: "session:s1", To: "x"}, {Scope: scopeA, From: "old", To: "session:s9"}} {
		if _, err := f.svc.RenameTag(testCtx, f.human, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("system tag rename %+v err = %v", in, err)
		}
	}
	res, err := f.svc.RenameTag(testCtx, f.human, TagRenameInput{Scope: scopeA, From: "old", To: "legacy"})
	if err != nil || res != (TagRenameResult{Renamed: 1, Skipped: 1}) {
		t.Fatalf("rename = %+v, %v", res, err)
	}
	if m := f.repo.rows(colMemories); !slices.ContainsFunc(m, func(r Record) bool { return r.ID == "m1" && slices.Equal(r.Tags, []string{"session:s1", "legacy"}) }) {
		t.Fatalf("memories after rename = %+v", m)
	}
}

// TestOpsSQLReadsTheStoredDocForm ties every doc path the operator SQL reads to
// what encodeRecord writes into the jsonb doc: dates are {"$date": ...} objects,
// so a time filter, cursor or bucket must go through pgDate, never doc->>'created_at'.
func TestOpsSQLReadsTheStoredDocForm(t *testing.T) {
	at := time.Date(2026, 11, 5, 7, 8, 9, 123456789, time.UTC)
	_, raw, err := encodeRecord(Record{ID: "a1", Kind: "set_tags:memories", CreatedAt: at, CreatedBy: "human-1", Target: &Ref{ID: "m1"}, Tags: []string{"old"}, OriginProduct: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		CreatedAt     map[string]any `json:"created_at"`
		CreatedBy     string         `json:"created_by"`
		Target        map[string]any `json:"target"`
		Tags          []string       `json:"tags"`
		OriginProduct string         `json:"origin_product"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	stored, _ := doc.CreatedAt["$date"].(string)
	parsed, err := time.Parse(time.RFC3339, stored)
	if err != nil || !parsed.Equal(at.Truncate(time.Millisecond)) || doc.CreatedBy != "human-1" || doc.Target["id"] != "m1" || !slices.Equal(doc.Tags, []string{"old"}) || doc.OriginProduct != "cli" {
		t.Fatalf("stored doc = %s", raw)
	}
	if pgDate != `((doc->'created_at'->>'$date')::timestamptz)` {
		t.Fatalf("pgDate = %s", pgDate)
	}
	from, to := at.Add(-time.Hour), at
	w := opsRecordWhere(opsRecordQuery{Actor: "a", TargetID: "t", Tag: "x", From: from, To: to, After: &keysetCursor{At: at, ID: "z"}})
	sql := w.sql() + opsBucketExpr["day"] + opsBucketExpr["week"] + opsKeyExpr["source"]
	for _, want := range []string{pgDate + ` >= $6`, pgDate + ` < $7`, `doc->>'created_by'=$8`, `doc->'target'->>'id'=$9`, `doc->'tags' ? $10`, `(` + pgDate + ` < $11 OR (` + pgDate + ` = $12 AND id < $13))`, `date_trunc('day', ` + pgDate, `doc->>'origin_product'`} {
		if !strings.Contains(sql, want) {
			t.Fatalf("operator SQL lacks %q:\n%s", want, sql)
		}
	}
	if strings.Contains(strings.ReplaceAll(sql, pgDate, ""), "created_at") || len(w.args) != 8 {
		t.Fatalf("operator SQL reads created_at outside pgDate or binds %d args:\n%s", len(w.args), sql)
	}
}

func TestOpsTagsWildcardPagingAndMentalModels(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colMemories, Record{ID: "m1", Kind: "decision", Status: "active", Tags: []string{"user:alice", "role-admin"}})
	f.put(colMemories, Record{ID: "m2", Kind: "decision", Status: "active", Tags: []string{"user:bob", "User:Carol"}})
	f.put(colMemories, Record{ID: "m3", Kind: "decision", Status: "active", Tags: []string{"user:alice", "env-prod"}})
	f.put(colMentalModels, Record{ID: "mm1", Kind: mentalModelKind, Status: "active", MentalModel: &MentalModelData{Title: "t", Query: "q", Refresh: MentalModelRefresh{Mode: mmRefreshManual, Tags: []string{"team:core"}}}})
	got, err := f.svc.ListTags(testCtx, f.agent, TagListInput{Scope: scopeA, Q: "USER:*"})
	if err != nil || got.Total != 3 || got.Items[0] != (TagCount{Tag: "user:alice", Count: 2}) {
		t.Fatalf("wildcard = %+v, %v", got, err)
	}
	page, err := f.svc.ListTags(testCtx, f.agent, TagListInput{Scope: scopeA, Q: "user:*", Limit: 1, Offset: 1})
	if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Offset != 1 || page.Items[0].Tag == "user:alice" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	for pattern, want := range map[string]int{"*-admin": 1, "env*-prod": 1, "user:alice": 1, "user": 0, "*": 5} {
		if got, _ := f.svc.ListTags(testCtx, f.agent, TagListInput{Scope: scopeA, Q: pattern}); got.Total != want {
			t.Fatalf("pattern %q total = %d, want %d", pattern, got.Total, want)
		}
	}
	mm, err := f.svc.ListTags(testCtx, f.agent, TagListInput{Scope: scopeA, Source: "mental_models"})
	if err != nil || mm.Total != 1 || mm.Items[0].Tag != "team:core" {
		t.Fatalf("mental model tags = %+v, %v", mm, err)
	}
	for _, in := range []TagListInput{{Scope: scopeA, Source: "wiki"}, {Scope: scopeA, Limit: tagPageMax + 1}, {Scope: scopeA, Offset: -1}} {
		if _, err := f.svc.ListTags(testCtx, f.agent, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v err = %v", in, err)
		}
	}
}

func TestOpsStatsLinksAndOccurredTimeseries(t *testing.T) {
	f := newOpsFixture(t)
	old := time.Date(2020, 3, 4, 10, 0, 0, 0, time.UTC)
	f.put(colMemories, Record{ID: "m1", Kind: "decision", Status: "active", Occurred: &RecallTimeWindow{From: old, To: old.Add(time.Hour)}})
	f.put(colMemoryRelations, Record{ID: "r1", Kind: causalKind, Status: "active"})
	f.put(colMemoryRelations, Record{ID: "r2", Kind: "conflicts_with", Status: "active"})
	f.put(colMemoryRelations, Record{ID: "r3", Kind: causalKind, Status: "candidate"})
	st, err := f.svc.Stats(testCtx, f.human, scopeA)
	if err != nil || st.Links[causalKind] != 1 || st.Links["conflicts_with"] != 1 {
		t.Fatalf("links = %+v, %v", st.Links, err)
	}
	now := time.Now().UTC()
	ts, err := f.svc.StatsTimeseries(testCtx, f.human, StatsTimeseriesInput{Scope: scopeA, From: now.Add(-time.Hour), To: now.Add(time.Hour), TimeField: "occurred"})
	if err != nil || ts.TimeField != "occurred" || len(ts.Points) != 1 || ts.Points[0].Bucket != "2020-03-04" {
		t.Fatalf("occurred timeseries = %+v, %v", ts, err)
	}
	if _, err = f.svc.StatsTimeseries(testCtx, f.human, StatsTimeseriesInput{Scope: scopeA, From: now.Add(-time.Hour), To: now, TimeField: "updated"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad time field err = %v", err)
	}
}

func TestOpsTimeAxisSQL(t *testing.T) {
	for field, want := range map[string]string{"created": pgDate, "mentioned": "observed_at", "occurred": "doc->'occurred'->'from'"} {
		if !strings.Contains(opsTimeExpr[field], want) || !strings.HasSuffix(strings.TrimSuffix(opsTimeExpr[field], ")"), pgDate) && field != "created" {
			t.Fatalf("%s axis = %s", field, opsTimeExpr[field])
		}
	}
	if opsTagsPath(colMentalModels) != `doc->'mental_model'->'refresh'->'tags'` || opsTagsPath(colMemories) != `doc->'tags'` {
		t.Fatal("tag paths")
	}
}
