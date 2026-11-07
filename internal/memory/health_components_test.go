package memory

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type componentProbeRepo struct {
	Repository
	calls int
}

func (p *componentProbeRepo) componentHealth(context.Context) ComponentHealth {
	p.calls++
	return ComponentHealth{Database: "ready", Schema: "unavailable", Search: "unavailable", WriteGate: "paused"}
}

func TestComponentHealthRequiresOperatorAndKeepsPublicReadinessOpaque(t *testing.T) {
	f := newFixture(t)
	p := &componentProbeRepo{Repository: f.repo}
	f.svc.Repo = p
	f.auth.actors[f.human] = actor("operator", "human", false, "ops")
	f.auth.actors[f.agent] = actor("agent", "agent", false, "ops")
	h := Handler(f.svc)
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/metrics/health?workspace_id=ws-a&project_id=proj-a", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(""); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request(f.agent); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if p.calls != 0 {
		t.Fatal("unauthorized probe")
	}
	w := request(f.human)
	var got ComponentHealth
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Schema != "unavailable" || got.WriteGate != "paused" || got.Blobs != "disabled" {
		t.Fatal(w.Code, w.Body)
	}
	for _, secret := range []string{"ws-a", "proj-a", "operator", f.human} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("identifier leaked")
		}
	}
	public := httptest.NewRecorder()
	h.ServeHTTP(public, httptest.NewRequest("GET", "/readyz", nil))
	if public.Code != 200 || strings.Contains(public.Body.String(), "schema") {
		t.Fatal(public.Code, public.Body)
	}
}

func TestPostgresComponentHealthDetectsSchemaAndClosedDatabase(t *testing.T) {
	p := isolatedPostgres(t)
	if got := p.componentHealth(testCtx); got.Database != "ready" || got.Schema != "ready" || got.Search != "ready" || got.WriteGate != "ready" {
		t.Fatal(got)
	}
	schemaExec(t, p, "DROP INDEX arkism.memory_records_fts")
	if got := p.componentHealth(testCtx); got.Database != "ready" || got.Schema != "ready" || got.Search != "unavailable" {
		t.Fatal(got)
	}
	p.pool.Close()
	if got := p.componentHealth(testCtx); got.Database != "unavailable" {
		t.Fatal(got)
	}
}
