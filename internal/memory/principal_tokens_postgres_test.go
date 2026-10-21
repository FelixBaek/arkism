package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPostgresPrincipalTokenRotationAndExpiry(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	old, next := testPrincipalToken("old"), testPrincipalToken("next")
	grant := PrincipalGrant{PrincipalID: "token-owner", Role: roleHuman, Token: old, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "read:memories"}, TokenTTL: time.Hour}
	if err := p.ProvisionPrincipal(ctx, grant); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	row := Record{ID: "credential-bound-row", Scope: scopeA, Revision: 1, Status: statusActive, Kind: "fact", Text: "private row", CreatedAt: now, UpdatedAt: now}
	if err := p.Insert(ctx, colMemories, row); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, p)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	request := Authorization{Action: "read:memories", Scope: scopeA}
	assertAccess := func(token string, allowed bool) {
		t.Helper()
		d, err := auth.Authorize(ctx, token, request)
		if allowed && (err != nil || !d.Allowed) || !allowed && err == nil && d.Allowed {
			t.Fatal("unexpected service authorization", allowed, err)
		}
		_, err = app.Get(ContextWithPostgresToken(ctx, token), colMemories, scopeA, row.ID)
		if allowed && err != nil || !allowed && !errors.Is(err, ErrNotFound) {
			t.Fatal("unexpected direct RLS read", allowed, err)
		}
	}
	assertAccess(old, true)
	rotation := PrincipalTokenRotation{PrincipalID: grant.PrincipalID, PreviousToken: old, Token: next, TTL: time.Hour}
	if err := app.RotatePrincipalToken(ctx, rotation); err == nil {
		t.Fatal("runtime role rotated a credential")
	}
	if err := p.RotatePrincipalToken(ctx, rotation); err != nil {
		t.Fatal(err)
	}
	assertAccess(old, false)
	assertAccess(next, true)
	// Retired digests remain unique: an old secret can never become current again.
	if err := p.RotatePrincipalToken(ctx, PrincipalTokenRotation{PrincipalID: grant.PrincipalID, PreviousToken: next, Token: old}); !errors.Is(err, ErrConflict) {
		t.Fatal("retired token reused", err)
	}
	assertAccess(next, true)
	var issued, expires time.Time
	var predecessor []byte
	nextHash := sha256.Sum256([]byte(next))
	if err := p.q().QueryRow(ctx, `SELECT issued_at,expires_at,rotated_from FROM principal_token_history WHERE token_sha256=$1`, nextHash[:]).Scan(&issued, &expires, &predecessor); err != nil || expires.Sub(issued) != time.Hour || len(predecessor) != 32 {
		t.Fatal("missing lifecycle metadata", err)
	}
	status, err := p.PrincipalTokenStatus(ctx, grant.PrincipalID, next)
	if err != nil || status.IssuedAt == nil || !status.Active || !status.NeedsRotation || !status.ExpiresAt.Equal(expires) {
		t.Fatal("invalid lifecycle status", err)
	}
	if _, err = p.PrincipalTokenStatus(ctx, grant.PrincipalID, old); !errors.Is(err, ErrDenied) {
		t.Fatal("retired credential inspected current state", err)
	}
	if _, err = app.PrincipalTokenStatus(ctx, grant.PrincipalID, next); err == nil {
		t.Fatal("runtime role inspected private ledger")
	}
	if err = p.RotatePrincipalToken(ctx, PrincipalTokenRotation{PrincipalID: grant.PrincipalID, PreviousToken: old, Token: next, TTL: MaxPrincipalTokenTTL}); err != nil {
		t.Fatal("committed rotation replay failed", err)
	}
	status, err = p.PrincipalTokenStatus(ctx, grant.PrincipalID, next)
	if err != nil || !status.ExpiresAt.Equal(expires) {
		t.Fatal("replay extended lifetime", err)
	}
	other := grant
	other.PrincipalID = "token-thief"
	other.Token = old
	if err = p.ProvisionPrincipal(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatal("retired digest reused by another principal", err)
	}
	var retained int
	if err = p.q().QueryRow(ctx, `SELECT count(*) FROM principals WHERE id='token-thief'`).Scan(&retained); err != nil || retained != 0 {
		t.Fatal("failed provisioning retained principal", err)
	}
	grant.Token = next
	grant.TokenTTL = MaxPrincipalTokenTTL
	if err := p.ProvisionPrincipal(ctx, grant); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := p.q().QueryRow(ctx, `SELECT expires_at FROM principal_token_history WHERE token_sha256=$1`, nextHash[:]).Scan(&after); err != nil || !expires.Equal(after) {
		t.Fatal("grant update extended token lifetime", err)
	}
	if _, err := app.q().Exec(ctx, `UPDATE principal_token_history SET expires_at=statement_timestamp()+interval '1 year'`); err == nil {
		t.Fatal("runtime role edited lifecycle")
	}
	// Expire while a restricted transaction remains open; the next SQL statement
	// must see expiry rather than retaining its transaction-start timestamp.
	tx, err := app.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('arkism.token_hash',$1,true)`, hexDigest(next)); err != nil {
		t.Fatal(err)
	}
	var beforeCount, afterCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM memory_records`).Scan(&beforeCount); err != nil || beforeCount != 1 {
		t.Fatal("pre-expiry transaction read", err)
	}
	if _, err := p.q().Exec(ctx, `UPDATE principal_token_history SET issued_at=statement_timestamp()-interval '1 hour',expires_at=statement_timestamp()-interval '1 second' WHERE token_sha256=$1`, nextHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM memory_records`).Scan(&afterCount); err != nil || afterCount != 0 {
		t.Fatal("expired token retained transaction access", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertAccess(next, false)
	req := httptest.NewRequest("GET", "/v1/memories?workspace_id="+scopeA.WorkspaceID+"&project_id="+scopeA.ProjectID, nil)
	req.Header.Set("Authorization", "Bearer "+next)
	response := httptest.NewRecorder()
	Handler(&Service{Repo: app, Auth: auth}).ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("expired token HTTP response", response.Code)
	}
	row.ID = "expired-write"
	if err = app.Insert(ContextWithPostgresToken(ctx, next), colMemories, row); err == nil {
		t.Fatal("expired token wrote through RLS", err)
	}
	if _, err = p.Get(ctx, colMemories, scopeA, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired write retained data", err)
	}
}

func hexDigest(token string) string {
	// Use the same binding presented to SQL without keeping a raw credential.
	ctx := ContextWithPostgresToken(context.Background(), token)
	return ctx.Value(postgresTokenContextKey{}).(postgresTokenBinding).hash
}

func TestPostgresPrincipalConcurrentRotation(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	old := testPrincipalToken("concurrent-old")
	grant := PrincipalGrant{PrincipalID: "concurrent-owner", Role: roleHuman, Token: old, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"read:memories"}}
	if err := p.ProvisionPrincipal(ctx, grant); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, label := range []string{"concurrent-a", "concurrent-b"} {
		group.Add(1)
		go func(label string) {
			defer group.Done()
			results <- p.RotatePrincipalToken(ctx, PrincipalTokenRotation{PrincipalID: grant.PrincipalID, PreviousToken: old, Token: testPrincipalToken(label)})
		}(label)
	}
	group.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("rotation was not serialized", success, conflict)
	}
}

func TestPostgresPrincipalLegacyMigrationGrace(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	historicalSchema(t, p, 2)
	legacy := sha256.Sum256([]byte("legacy-short-token"))
	schemaExec(t, p, `INSERT INTO principals(id,role,token_sha256) VALUES('legacy-owner','human',$1)`, legacy[:])
	if err := p.Init(ctx); err != nil {
		t.Fatal(err)
	}
	var unknown bool
	var grace float64
	if err := p.q().QueryRow(ctx, `SELECT issued_at IS NULL,extract(epoch FROM expires_at-statement_timestamp()) FROM principal_token_history WHERE principal_id='legacy-owner'`).Scan(&unknown, &grace); err != nil || !unknown || grace < 29*86400 || grace > 30*86400 {
		t.Fatal("legacy issuance fabricated or grace missing", err)
	}
	if err := p.RotatePrincipalToken(ctx, PrincipalTokenRotation{PrincipalID: "legacy-owner", PreviousToken: "legacy-short-token", Token: testPrincipalToken("legacy-replacement")}); err != nil {
		t.Fatal(err)
	}
}
