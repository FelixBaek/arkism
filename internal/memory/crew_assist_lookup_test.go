package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCrewAssistRequestLookup(t *testing.T) {
	f, _ := crewRunFixture(t)
	key := strings.Repeat("a", 32)
	in := CrewAssistInput{Scope: scopeA, RequestID: key, Assist: CrewAssist{Kind: "install_skill", Prompt: `{"name":"test","content":"# draft","agents":["codex"]}`}}
	r, err := f.svc.CreateCrewAssist(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CreateCrewAssist(testCtx, f.human, in); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate creation", err)
	}
	changed := in
	changed.Assist.Prompt = "changed"
	if _, err = f.svc.CreateCrewAssist(testCtx, f.human, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed duplicate", err)
	}
	for _, state := range []string{"pending", "done", "failed", "expired"} {
		clone := r
		a := *r.CrewAssist
		clone.CrewAssist = &a
		if state == "expired" {
			clone.CreatedAt = time.Now().Add(-crewAssistTTL - time.Minute)
		} else {
			a.Status = state
		}
		f.repo.mu.Lock()
		f.repo.store[colCrewAssists][recKey(scopeA, r.ID)] = clone
		f.repo.mu.Unlock()
		got, e := f.svc.GetCrewAssistByRequest(testCtx, f.human, scopeA, key)
		want := state
		if want == "expired" {
			want = "failed"
		}
		if e != nil || got.ID != r.ID || got.CrewAssist.Status != want {
			t.Fatal("lookup state", state, got, e)
		}
	}
	if page, e := f.svc.PendingCrewAssists(testCtx, tokEngine, scopeA, false); e != nil || len(page.Items) != 0 {
		t.Fatal("completed/expired still queued", e)
	}
	if _, e := f.svc.GetCrewAssistByRequest(testCtx, f.human2, scopeA, key); !errors.Is(e, ErrNotFound) {
		t.Fatal("other principal lookup", e)
	}
	if _, e := f.svc.GetCrewAssistByRequest(testCtx, f.human, scopeB, key); !errors.Is(e, ErrNotFound) {
		t.Fatal("other scope lookup", e)
	}
	if _, e := f.svc.GetCrewAssistByRequest(testCtx, f.agent, scopeA, key); !errors.Is(e, ErrDenied) {
		t.Fatal("agent lookup", e)
	}
	other, e := f.svc.CreateCrewAssist(testCtx, f.human2, in)
	if e != nil || other.ID == r.ID {
		t.Fatal("same client key must be principal scoped", e)
	}
	scoped := in
	scoped.Scope = scopeB
	other, e = f.svc.CreateCrewAssist(testCtx, f.human, scoped)
	if e != nil || other.ID == r.ID {
		t.Fatal("same client key must be scope scoped", e)
	}
	for _, bad := range []string{"", strings.Repeat("A", 32), strings.Repeat("a", 31), strings.Repeat("a", 33), key + "\n"} {
		if _, e = f.svc.GetCrewAssistByRequest(testCtx, f.human, scopeA, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad lookup key accepted", e)
		}
		if bad != "" {
			v := in
			v.RequestID = bad
			if _, e = f.svc.CreateCrewAssist(testCtx, f.human, v); !errors.Is(e, ErrInvalid) {
				t.Fatal("bad create key accepted", e)
			}
		}
	}
	h := Handler(f.svc)
	req := httptest.NewRequest("GET", "/v1/crew/assists/by-request/"+key+"?workspace_id="+scopeA.WorkspaceID+"&project_id="+scopeA.ProjectID+"&user_id="+scopeA.UserID+"&agent_id="+scopeA.AgentID, nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	var got Record
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &got) != nil || got.ID != r.ID {
		t.Fatal("HTTP lookup", out.Code, out.Body.String())
	}
	caps, e := f.svc.Capabilities(testCtx, f.human, scopeA)
	if e != nil || !caps.AssistRequestLookup {
		t.Fatal("lookup capability", e)
	}
}

func TestPostgresCrewAssistRequestConcurrent(t *testing.T) {
	p := isolatedPostgres(t)
	f, _ := crewRunFixture(t)
	f.svc.Repo = p
	in := CrewAssistInput{Scope: scopeA, RequestID: strings.Repeat("b", 32), Assist: CrewAssist{Kind: "draft", Prompt: "synthetic"}}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.svc.CreateCrewAssist(testCtx, f.human, in); results <- e }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	rows, err := p.ListRecords(testCtx, scopeA, colCrewAssists, 20)
	if err != nil {
		t.Fatal(err)
	}
	if successes != 1 || len(rows) != 1 {
		t.Fatal("concurrent requests created multiple jobs", successes)
	}
}
