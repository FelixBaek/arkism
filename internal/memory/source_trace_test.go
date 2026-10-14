package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourceTraceStatesAndUnicode(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("trace")
	src := f.repo.store["sources"][recKey(scopeA, r.Sources[0].SourceID)]
	src.Text = "가😀나다"
	src.SourceRef = "private-source-location"
	f.repo.store["sources"][recKey(scopeA, src.ID)] = src
	r.Sources = []SourceRef{{SourceID: src.ID, Locator: "private-locator", Span: &SourceSpan{ContentHash: ContentHash(src.Text), Start: 1, End: 3}}}
	f.repo.store["memories"][recKey(scopeA, r.ID)] = r
	for _, test := range []struct {
		name   string
		change func()
		state  string
	}{
		{"available", func() {}, "available"},
		{"changed", func() { src.Text = "changed"; f.repo.store["sources"][recKey(scopeA, src.ID)] = src }, "changed"},
		{"invalid", func() {
			src.Text = "가😀나다"
			f.repo.store["sources"][recKey(scopeA, src.ID)] = src
			r.Sources[0].Span.End = 9
		}, "invalid"},
		{"inactive", func() {
			past := time.Now().Add(-time.Hour)
			src.ExpiresAt = &past
			f.repo.store["sources"][recKey(scopeA, src.ID)] = src
		}, "inactive"},
		{"hidden", func() { f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: src.ID} }, "hidden"},
		{"unavailable", func() { f.svc.Auth = f.auth; delete(f.repo.store["sources"], recKey(scopeA, src.ID)) }, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.change()
			out, e := f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
			if e != nil || len(out.Sources) != 1 || out.Sources[0].State != test.state {
				t.Fatalf("%+v %v", out, e)
			}
			entry := out.Sources[0]
			if test.state == "available" {
				if entry.Excerpt != "😀나" || entry.Reference == nil || entry.Source.ID != src.ID {
					t.Fatal(entry)
				}
			} else {
				raw, _ := json.Marshal(entry)
				if string(raw) != `{"source_id":"`+src.ID+`","state":"`+test.state+`"}` {
					t.Fatal("hidden fields leaked", string(raw))
				}
			}
			if out.Authority != "data_only_untrusted" || out.Memory.Revision != r.Revision {
				t.Fatal(out)
			}
		})
	}
	if _, e := f.svc.Get(testCtx, f.human, "memories", scopeA, r.ID); e == nil {
		t.Fatal("ordinary read allowed missing evidence")
	}
}

type traceAuth struct {
	Authorizer
	calls           int
	changePrincipal bool
	outage          bool
}

func (a *traceAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.Action == "read:sources" {
		a.calls++
		if a.outage {
			return Decision{}, ErrUnavailable
		}
	}
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if in.Action == "read:sources" && a.changePrincipal {
		d.PrincipalID = "different"
	}
	return d, e
}

func TestSourceTraceAuthorizationAuditAndScopes(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("trace bounds")
	r.Sources = append(r.Sources, r.Sources[0])
	f.repo.store["memories"][recKey(scopeA, r.ID)] = r
	spy := &traceAuth{Authorizer: f.auth}
	f.svc.Auth = spy
	out, e := f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
	if e != nil || len(out.Sources) != 2 || spy.calls != 1 {
		t.Fatal(out, e, spy.calls)
	}
	for _, other := range []Scope{{WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		if _, e = f.svc.TraceMemorySources(testCtx, f.human, other, r.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("scope leak", e)
		}
		source := f.repo.store["sources"][recKey(scopeA, r.Sources[0].SourceID)]
		delete(f.repo.store["sources"], recKey(scopeA, source.ID))
		source.Scope = other
		f.repo.store["sources"][recKey(other, source.ID)] = source
		out, e = f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
		if e != nil || out.Sources[0].State != "unavailable" || out.Sources[0].Reference != nil {
			t.Fatal("foreign source leaked", out, e)
		}
		delete(f.repo.store["sources"], recKey(other, source.ID))
		source.Scope = scopeA
		f.repo.store["sources"][recKey(scopeA, source.ID)] = source
	}
	for _, test := range []struct {
		setup func()
		want  error
	}{
		{func() { spy.changePrincipal = true }, ErrDenied},
		{func() { spy.changePrincipal = false; spy.outage = true }, ErrUnavailable},
		{func() { spy.outage = false; f.repo.failAudit = true }, ErrUnavailable},
	} {
		test.setup()
		out, e = f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
		if !errors.Is(e, test.want) || out.Memory.ID != "" || len(out.Sources) != 0 {
			t.Fatal("partial result on failure", out, e)
		}
	}
}

func TestSourceTraceHTTP(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("http trace")
	for _, q := range []string{"workspace_id=ws-a&project_id=proj-a", "workspace_id=ws-a&project_id=proj-a&extra=1", "workspace_id=ws-a&project_id=proj-a&project_id=proj-b", "workspace_id=%xx"} {
		req := httptest.NewRequest("GET", "/v1/memories/"+r.ID+"/sources?"+q, nil)
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		want := 400
		if q == "workspace_id=ws-a&project_id=proj-a" {
			want = 200
		}
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		if want == 200 && !strings.Contains(w.Body.String(), `"authority":"data_only_untrusted"`) {
			t.Fatal(w.Body.String())
		}
	}
}

func TestSourceTraceLegacyAndBounds(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("legacy trace")
	r.Sources[0].Span = nil
	f.repo.store["memories"][recKey(scopeA, r.ID)] = r
	out, e := f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
	if e != nil || out.Sources[0].State != "available" || out.Sources[0].Excerpt != "" {
		t.Fatal(out, e)
	}
	ref := r.Sources[0]
	for _, n := range []int{0, 65} {
		r.Sources = make([]SourceRef, n)
		for i := range r.Sources {
			r.Sources[i] = ref
		}
		f.repo.store["memories"][recKey(scopeA, r.ID)] = r
		out, e = f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
		if !errors.Is(e, ErrInvalid) || out.Memory.ID != "" {
			t.Fatal(out, e)
		}
	}
}

func TestSourceTraceDeletionProof(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("deleted evidence")
	id := r.Sources[0].SourceID
	src, e := f.repo.Get(testCtx, "sources", scopeA, id)
	if e != nil {
		t.Fatal(e)
	}
	// A failed deletion must leave both source and proof unchanged.
	f.repo.failAudit = true
	if e = f.svc.Delete(testCtx, f.human, "sources", id, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	f.repo.failAudit = false
	if _, e = sourceDeletion(testCtx, f.repo, scopeA, id); !errors.Is(e, ErrNotFound) {
		t.Fatal("rollback left proof", e)
	}
	if e = f.svc.Delete(testCtx, f.human, "sources", id, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	at, e := sourceDeletion(testCtx, f.repo, scopeA, id)
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
	if e != nil || out.Sources[0].State != "deleted" || out.Sources[0].DeletedAt == nil || !out.Sources[0].DeletedAt.Equal(at) {
		t.Fatal(out, e)
	}
	raw, _ := json.Marshal(out.Sources[0])
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if len(fields) != 3 || fields["source_id"] != id || fields["deleted_at"] == nil {
		t.Fatal("deletion details leak", string(raw))
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: id}
	out, e = f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
	if e != nil || out.Sources[0].State != "hidden" || out.Sources[0].DeletedAt != nil {
		t.Fatal("unauthorized proof leak", out, e)
	}
	f.svc.Auth = f.auth
	// Other scopes cannot establish deletion, and no receipt means unknown.
	receipt := f.repo.store["jobs"][recKey(scopeA, sourceDeletionID(scopeA, id))]
	delete(f.repo.store["jobs"], recKey(scopeA, receipt.ID))
	for _, other := range []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		clone := receipt
		clone.Scope = other
		clone.ID = sourceDeletionID(other, id)
		if e := f.repo.Insert(testCtx, "jobs", clone); e != nil {
			t.Fatal(e)
		}
	}

	out, e = f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
	if e != nil || out.Sources[0].State != "unavailable" || out.Sources[0].DeletedAt != nil {
		t.Fatal("unrelated deletion proof", out, e)
	}
}

type deletionFailureTx struct{ Tx }

func (x deletionFailureTx) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == "jobs" {
		return Record{}, ErrUnavailable
	}
	return x.Tx.Get(ctx, col, scope, id)
}

type deletionFailureRepo struct{ Repository }

func (r deletionFailureRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, deletionFailureTx{tx}) })
}
func TestSourceTraceDeletionFailure(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("proof outage")
	delete(f.repo.store["sources"], recKey(scopeA, r.Sources[0].SourceID))
	f.svc.Repo = deletionFailureRepo{f.repo}
	out, e := f.svc.TraceMemorySources(testCtx, f.human, scopeA, r.ID)
	if !errors.Is(e, ErrUnavailable) || out.Memory.ID != "" || len(out.Sources) != 0 {
		t.Fatal("partial result", out, e)
	}
}

func TestSourceDeletionRejectsMalformedReceipt(t *testing.T) {
	f := newFixture(t)
	f.repo.store["jobs"] = map[string]Record{}
	id := ID()
	good := Record{ID: sourceDeletionID(scopeA, id), Scope: scopeA, Kind: "source_deletion", Status: "done", Revision: 1, CreatedAt: time.Now().Add(-time.Minute), Target: &Ref{Collection: "sources", ID: id, Revision: 1}}
	for _, change := range []func(*Record){
		func(r *Record) { r.Kind = "embedding" }, func(r *Record) { r.Status = "pending" }, func(r *Record) { r.Target = nil }, func(r *Record) { r.Target = &Ref{Collection: "memories", ID: id, Revision: 1} }, func(r *Record) { r.Target = &Ref{Collection: "sources", ID: "other", Revision: 1} }, func(r *Record) { r.Target = &Ref{Collection: "sources", ID: id, Revision: 0} }, func(r *Record) { r.CreatedAt = time.Time{} }, func(r *Record) { r.CreatedAt = time.Now().Add(time.Hour) },
	} {
		row := good
		change(&row)
		f.repo.store["jobs"][recKey(scopeA, row.ID)] = row
		if _, e := sourceDeletion(testCtx, f.repo, scopeA, id); !errors.Is(e, ErrUnavailable) {
			t.Fatal("invalid receipt accepted", e)
		}
	}
}

type failDeletionInsertTx struct{ Tx }

func (x failDeletionInsertTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "jobs" && r.Kind == "source_deletion" {
		return ErrUnavailable
	}
	return x.Tx.Insert(ctx, col, r)
}

type failDeletionInsertRepo struct{ Repository }

func (r failDeletionInsertRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, failDeletionInsertTx{tx}) })
}
func TestDeletionReceiptFailureRollsBackSource(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("receipt write failure")
	id := r.Sources[0].SourceID
	src, e := f.repo.Get(testCtx, "sources", scopeA, id)
	if e != nil {
		t.Fatal(e)
	}
	before := len(f.repo.store["audit_events"])
	f.svc.Repo = failDeletionInsertRepo{f.repo}
	e = f.svc.Delete(testCtx, f.human, "sources", id, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision})
	if !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = f.repo.Get(testCtx, "sources", scopeA, id); e != nil || len(f.repo.store["audit_events"]) != before {
		t.Fatal("partial deletion", e)
	}
}

func TestCompletedTraceRejectsMismatchedPayloadOrigin(t *testing.T) {
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	ingest := func(payload string) error {
		in := TraceIngestInput{Scope: Scope{WorkspaceID: "ws-a", ProjectID: "proj-a", AgentID: "agent-a"}, TraceID: "origin-trace", ThreadID: "origin-thread", AgentRun: CompletedAgentRunInput{OriginAgentID: "runtime-a", TaskType: "work", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "task"}, Events: []TraceEventInput{{ID: "origin-event", Kind: "langgraph.input", OccurredAt: start, Payload: json.RawMessage(payload)}}}
		_, _, err := prepareCompletedTrace(in)
		return err
	}
	for payload, want := range map[string]error{`{"origin_agent_id":"runtime-a","input":"x"}`: nil, `{"input":"x"}`: nil, `{"origin_agent_id":"runtime-b","input":"x"}`: ErrInvalid, `{"origin_agent_id":7}`: ErrInvalid} {
		if err := ingest(payload); !errors.Is(err, want) {
			t.Fatal(payload, err)
		}
	}
}
