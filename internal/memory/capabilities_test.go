package memory

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

func TestCapabilitiesProbe(t *testing.T) {
	f := newFixture(t)
	caps, err := f.svc.Capabilities(testCtx, f.human, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Actions) != len(capabilityCatalog) {
		t.Fatalf("action count = %d, want %d", len(caps.Actions), len(capabilityCatalog))
	}
	if !caps.Actions["read_sources"] || !caps.Actions["review_decide"] {
		t.Fatal("human token must read and review")
	}
	if !caps.Advisory || caps.Principal == "" || caps.Role != "human" {
		t.Fatalf("identity: %q %q advisory=%v", caps.Principal, caps.Role, caps.Advisory)
	}
	// The service refuses these for agents regardless of policy allowance.
	agentCaps, err := f.svc.Capabilities(testCtx, f.agent, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if agentCaps.Role != "agent" {
		t.Fatalf("agent role = %q", agentCaps.Role)
	}
	for _, key := range []string{"review_list", "review_decide", "supersede", "delete_memories", "delete_sources"} {
		if agentCaps.Actions[key] {
			t.Fatalf("agent must not be offered %s", key)
		}
	}
	if !agentCaps.Actions["read_sources"] {
		t.Fatal("agent read must stay visible")
	}
}

func TestCapabilitiesFailClosed(t *testing.T) {
	f := newFixture(t)
	f.svc.Auth = unavailableAuthorizer
	if _, err := f.svc.Capabilities(testCtx, callerToken, scopeA); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("capabilities = %v, want ErrUnavailable", err)
	}
	if _, err := f.svc.Capabilities(testCtx, callerToken, Scope{WorkspaceID: "ws-a"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid scope = %v, want ErrDenied", err)
	}
}

// denied probes must not contend with their own audit
// transactions and exhaust PostgreSQL serializable retries into HTTP 503.
func TestPostgresCapabilityDenialsRemainForbidden(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{
		PrincipalID: "skill-reader", Role: "human", Token: f.human,
		OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID,
		Actions: []string{"db:read", "db:write", "read:skills"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := p.pool.Config().Copy()
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(testCtx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Postgres{pool: pool}
	auth, err := NewPostgresAuthorizer(store)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Repo, f.svc.Auth = store, auth
	for i := 0; i < 20; i++ {
		ctx := ContextWithPostgresToken(context.WithValue(testCtx, auditRequestKey{}, ID()), f.human)
		caps, err := f.svc.Capabilities(ctx, f.human, scopeA)
		if !errors.Is(err, ErrDenied) || caps.Actions != nil {
			t.Fatalf("probe %d: expected denial without partial result, got %v", i, err)
		}
	}
}
