package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func nativeSearchPGFixture(t *testing.T) (*nativePGFixture, *NativeSearchShadowRuntime, SearchInput) {
	t.Helper()
	p := nativePGSetup(t)
	actions := []string{"db:read", "db:write", "read:evaluations", "read:agent_runs", "read:execution_traces", "read:sources", "read:memories", "read:jobs", "write:jobs", "search:memories", "context:read"}
	if e := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-worker", Role: roleService, Token: p.workerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}); e != nil {
		t.Fatal(e)
	}
	// Set the synthetic fixture's timeless kind before enrollment; production recency remains unchanged.
	m, e := p.admin.Get(testCtx, colMemories, scopeA, p.in.Target.ID)
	if e != nil {
		t.Fatal(e)
	}
	m.Kind = "preference"
	m.Text += " searchcanary"
	m.Keywords = append(m.Keywords, "searchcanary")
	if e = p.admin.Replace(testCtx, colMemories, m, m.Revision); e != nil {
		t.Fatal(e)
	}
	r := nativeSearchTestRuntime(t, "V3-P1-NOTIME")
	if _, e = p.worker.ActivateNativeMemoryDynamics(testCtx, p.workerToken, NativeDynamicsActivationInput{Scope: p.in.Scope, Target: p.in.Target, Dimension: p.in.Dimension, Config: nativeSearchMatchingConfig(t, r), RequestID: "search-pg"}); e != nil {
		t.Fatal(e)
	}
	in := nativeSearchTestInput()
	in.Query = "searchcanary"
	in.IncludeInactive = false
	return p, r, in
}
func nativeSearchHTTP(t *testing.T, s *Service, token string, in SearchInput) ContextBundle {
	t.Helper()
	raw, e := json.Marshal(in)
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", "/v1/search", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Handler(s).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("HTTP status", w.Code, w.Body.String())
	}
	var b ContextBundle
	if e = json.Unmarshal(w.Body.Bytes(), &b); e != nil {
		t.Fatal(e)
	}
	return b
}
func TestNativeSearchShadowPostgresHTTP(t *testing.T) {
	p, r, in := nativeSearchPGFixture(t)
	beforeStates := nativeSearchHash("states", p.rows(t, "state"))
	beforeConsumption := nativeSearchHash("consumption", p.rows(t, "consumption"))
	off := nativeSearchHTTP(t, p.worker, p.workerToken, in)
	if len(off.Items) != 1 {
		t.Fatal("missing fixture")
	}
	beforeJobs := len(pgCollection(t, p.admin, colJobs))
	p.worker.NativeSearchShadow = r
	on := nativeSearchHTTP(t, p.worker, p.workerToken, in)
	off.ContextID = ""
	on.ContextID = ""
	if !reflect.DeepEqual(off, on) {
		t.Fatal("HTTP baseline differs")
	}
	got := r.Observations()
	if len(got) != 1 || got[0].Status != "selected" || got[0].SourceCount != 1 || !got[0].BaselineReturned {
		t.Fatal(got)
	}
	if len(pgCollection(t, p.admin, colJobs)) != beforeJobs+1 || beforeStates != nativeSearchHash("states", p.rows(t, "state")) || beforeConsumption != nativeSearchHash("consumption", p.rows(t, "consumption")) {
		t.Fatal("shadow domain write")
	}
}

type nativeSearchBeforeTxRepo struct {
	Repository
	before func()
	calls  int
}

func (r *nativeSearchBeforeTxRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.calls++
	if r.calls == 1 {
		r.before()
	}
	return r.Repository.Transaction(ctx, fn)
}
func TestNativeSearchShadowPostgresFinalFence(t *testing.T) {
	for _, fault := range []string{"revision", "delete", "source", "grant"} {
		t.Run(fault, func(t *testing.T) {
			p, r, in := nativeSearchPGFixture(t)
			p.worker.NativeSearchShadow = r
			var afterMutation []string
			repo := &nativeSearchBeforeTxRepo{Repository: p.workerRepo, before: func() {
				nativeSearchInjectPGFault(t, p, in, fault)
				afterMutation = nativeSearchRawDatabaseSnapshot(t, p.admin)
			}}
			p.worker.Repo = repo
			out, e := p.worker.Search(testCtx, p.workerToken, in)
			if e == nil || out.ContextData != "" || len(out.Items) != 0 || !reflect.DeepEqual(afterMutation, nativeSearchRawDatabaseSnapshot(t, p.admin)) {
				t.Fatal("final fence bypassed", e)
			}
			got := r.Observations()
			if len(got) != 1 || got[0].BaselineReturned || got[0].Status == "selected" {
				t.Fatal(got)
			}
		})
	}
}

func nativeSearchInjectPGFault(t *testing.T, p *nativePGFixture, in SearchInput, fault string) {
	t.Helper()
	switch fault {
	case "grant":
		if e := p.admin.SetPrincipalActive(testCtx, "native-pg-worker", false); e != nil {
			t.Fatal(e)
		}
	case "revision":
		m, e := p.admin.Get(testCtx, colMemories, in.Scope, p.in.Target.ID)
		if e != nil {
			t.Fatal(e)
		}
		m.Revision++
		m.Text += " changed"
		if e = p.admin.Replace(testCtx, colMemories, m, m.Revision-1); e != nil {
			t.Fatal(e)
		}
	case "delete":
		if e := p.admin.Delete(testCtx, colMemories, in.Scope, p.in.Target.ID, p.in.Target.Revision); e != nil {
			t.Fatal(e)
		}
	case "source":
		m, e := p.admin.Get(testCtx, colMemories, in.Scope, p.in.Target.ID)
		if e != nil {
			t.Fatal(e)
		}
		source, e := p.admin.Get(testCtx, "sources", in.Scope, m.Sources[0].SourceID)
		if e != nil {
			t.Fatal(e)
		}
		if e = p.admin.Delete(testCtx, "sources", in.Scope, source.ID, source.Revision); e != nil {
			t.Fatal(e)
		}
	}
}

type nativeSearchTwoTxRepo struct {
	Repository
	first, second func()
	calls         int
}

func (r *nativeSearchTwoTxRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.calls++
	if r.calls == 1 {
		r.first()
	}
	if r.calls == 2 {
		r.second()
	}
	return r.Repository.Transaction(ctx, fn)
}
func TestNativeSearchShadowPostgresDeadlineKeepsBaseline(t *testing.T) {
	p, r, in := nativeSearchPGFixture(t)
	r.config.Deadline = 30 * time.Millisecond
	p.worker.NativeSearchShadow = r
	var release func()
	repo := &nativeSearchTwoTxRepo{Repository: p.workerRepo, first: func() {
		tx, e := p.admin.pool.Begin(testCtx)
		if e != nil {
			t.Fatal(e)
		}
		release = func() {
			if e := tx.Rollback(testCtx); e != nil {
				t.Fatal(e)
			}
		}
		var id string
		e = tx.QueryRow(testCtx, "SELECT id FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND id=$6 FOR UPDATE", colMemories, in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, p.in.Target.ID).Scan(&id)
		if e != nil {
			t.Fatal(e)
		}
	}, second: func() { release(); release = nil }}
	defer func() {
		if release != nil {
			release()
		}
	}()
	p.worker.Repo = repo
	before := len(pgCollection(t, p.admin, colJobs))
	out, e := p.worker.Search(testCtx, p.workerToken, in)
	if e != nil || len(out.Items) != 1 || repo.calls != 2 || len(pgCollection(t, p.admin, colJobs)) != before+1 {
		t.Fatal("deadline changed baseline", e)
	}
	got := r.Observations()
	if len(got) != 1 || got[0].Reason != "shadow_deadline" || got[0].Status != "fallback" || !got[0].BaselineReturned {
		t.Fatal(got)
	}
}
