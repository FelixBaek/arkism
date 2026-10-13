package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// A nested policy denial must survive the aborted business transaction.

// Allow auditing keeps the same HTTP request ID and never copies input tokens.

func TestAuditRequest(t *testing.T) { exerciseAuditRequest(t, newFakeRepo()) }
func TestAuditDenialValidation(t *testing.T) {
	r := Authorization{Scope: scopeA, Context: NewContextScope(scopeA, "read:sources", "internal")}
	d := Decision{Scope: scopeA, Context: r.Context, PrincipalID: "person", Role: "human", DecisionID: "decision"}
	if !verifiedDenial(r, d) {
		t.Fatal("valid denial rejected")
	}
	d.Context.RequestID = "different"
	if verifiedDenial(r, d) {
		t.Fatal("unbound denial trusted")
	}
}

func TestAuditProviderErrorStoredOnce(t *testing.T) {
	repo := newFakeRepo()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Auth = unavailableAuthorizer
	for i := 0; i < 2; i++ {
		if _, err := f.svc.authorize(testCtx, f.human, "read:sources", scopeA, "src-1", ""); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("authorize: %v", err)
		}
	}
	events := denialRows(repo, scopeA, "policy:error:read:sources")
	if len(events) != 1 {
		t.Fatalf("provider error denial rows = %d, want exactly 1", len(events))
	}
	if events[0].Text != "" || events[0].Audit == nil || events[0].Audit.Decision != "deny" || events[0].Audit.PolicyDecisionID != "" {
		t.Fatal("provider error record carries unverified fields")
	}
	if events[0].CreatedBy != "unknown" || events[0].CreatedByType != "unknown" {
		t.Fatal("provider error must not claim a principal")
	}
}

func TestAuditInvalidPolicyResponse(t *testing.T) {
	repo := newFakeRepo()
	f := newFixture(t)
	f.svc.Repo = repo
	// The provider denies but its principal field carries a secret: the answer
	// fails verification, while the audit trail still records the refusal
	// without the unverified principal.
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		return Decision{Allowed: false, Scope: req.Scope, Context: req.Context, PrincipalID: "agent password=hunter2", Role: "agent", DecisionID: "deny-9", ContentSafe: true}, nil
	})
	if _, err := f.svc.authorize(testCtx, f.human, "read:sources", scopeA, "src-1", ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("authorize: %v", err)
	}
	events := denialRows(repo, scopeA, "policy:invalid:read:sources")
	if len(events) != 1 {
		t.Fatalf("invalid response denial rows = %d, want exactly 1", len(events))
	}
	if events[0].Audit == nil || events[0].Audit.PolicyDecisionID != "" || events[0].CreatedBy != "unknown" {
		t.Fatal("invalid response record kept unverified fields")
	}
	for _, event := range repo.rows("audit_events") {
		if strings.Contains(structuredText(event), "hunter2") {
			t.Fatal("unverified principal leaked into audit_events")
		}
	}
}

func TestAuditInputDenialQuarantine(t *testing.T) {
	repo := newFakeRepo()
	f := newFixture(t)
	f.svc.Repo = repo
	secret := "resource?password=hunter2"
	if _, err := f.svc.authorize(testCtx, f.human, "read:sources", scopeA, secret, ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("authorize: %v", err)
	}
	// The requested scope itself is clean, so the refusal is attributed there
	// while the secret-bearing identifier is never copied into the record.
	events := denialRows(repo, scopeA, "auth:deny:read:sources")
	if len(events) != 1 || events[0].Target != nil {
		t.Fatalf("secret-target rows = %d, want exactly 1 without target", len(events))
	}
	for _, event := range repo.rows("audit_events") {
		if strings.Contains(structuredText(event), "hunter2") {
			t.Fatal("denied input secret leaked into audit_events")
		}
	}
	quarantined := denialRows(repo, Scope{WorkspaceID: "__unparsed__", ProjectID: "__unparsed__"}, "auth:deny:read:sources")
	if len(quarantined) != 0 {
		t.Fatalf("clean scope must not land in quarantine, got %d", len(quarantined))
	}
	// An unusable scope cannot be attributed; the refusal lands in quarantine.
	if _, err := f.svc.authorize(testCtx, f.human, "read:sources", Scope{WorkspaceID: "ws-a"}, "src-1", ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("authorize: %v", err)
	}
	quarantined = denialRows(repo, Scope{WorkspaceID: "__unparsed__", ProjectID: "__unparsed__"}, "auth:deny:read:sources")
	if len(quarantined) != 1 {
		t.Fatalf("invalid scope rows = %d, want exactly 1", len(quarantined))
	}
}

func TestAuditVerifiedDenialExactlyOnce(t *testing.T) {
	repo := newFakeRepo()
	f := newFixture(t)
	f.svc.Repo = repo
	var captured Authorization
	f.svc.Auth = policyDecision(Decision{PrincipalID: "human-a", Role: "human", ContentSafe: true, DecisionID: "deny-1"}, &captured)
	request := Authorization{Action: "read:sources", Scope: scopeA, ResourceID: "src-1", Context: NewContextScope(scopeA, "read:sources", "internal")}
	request.Context.RequestID = "req-1"
	d, err := f.svc.Auth.Authorize(testCtx, callerToken, request)
	if err != nil {
		t.Fatal(err)
	}
	if !(!d.Allowed && verifiedDenial(request, d)) {
		t.Fatal("expected a verified denial")
	}
	for i := 0; i < 3; i++ {
		if err := f.svc.auditPolicyDenial(testCtx, request, d); err != nil {
			t.Fatal(err)
		}
	}
	events := denialRows(repo, scopeA, "policy:deny:read:sources")
	if len(events) != 1 {
		t.Fatalf("verified denial rows after 3 attempts = %d, want exactly 1", len(events))
	}
	if events[0].Audit == nil || events[0].Audit.PolicyDecisionID != "deny-1" || events[0].CreatedBy != "human-a" || strings.HasPrefix(events[0].ID, "aud") == false {
		t.Fatal("verified denial record fields")
	}
}

type authorizerFunc func(context.Context, string, Authorization) (Decision, error)

func (f authorizerFunc) Authorize(ctx context.Context, token string, req Authorization) (Decision, error) {
	return f(ctx, token, req)
}

var unavailableAuthorizer = authorizerFunc(func(context.Context, string, Authorization) (Decision, error) {
	return Decision{}, ErrUnavailable
})

// policyDecision answers every request with d bound to the request scope and
// context, recording the request in captured when it is set.
func policyDecision(d Decision, captured *Authorization) Authorizer {
	return authorizerFunc(func(_ context.Context, _ string, req Authorization) (Decision, error) {
		if captured != nil {
			*captured = req
		}
		d.Scope, d.Context = req.Scope, req.Context
		return d, nil
	})
}

func denialRows(repo Repository, scope Scope, kind string) []Record {
	var events []Record
	if fake, ok := repo.(*fakeRepo); ok {
		for _, event := range fake.rows("audit_events") {
			if sameScope(event.Scope, scope) && event.Kind == kind {
				events = append(events, event)
			}
		}
		return events
	}
	return events
}
func exerciseAuditRequest(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	source := f.source()
	var captured Authorization
	f.svc.Auth = policyDecision(Decision{PrincipalID: "human-a", Role: "human", ContentSafe: true, DecisionID: "deny-1"}, &captured)
	req := httptest.NewRequest("GET", "/v1/sources/"+source.ID+"?workspace_id="+scopeA.WorkspaceID+"&project_id="+scopeA.ProjectID+"&user_id="+scopeA.UserID+"&agent_id="+scopeA.AgentID, nil)
	req.Header.Set("Authorization", "Bearer "+callerToken)
	req.Header.Set("X-Request-ID", "caller-supplied")
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("deny: %d %s", w.Code, w.Body.String())
	}
	id := w.Header().Get("X-Request-ID")
	if id == "" || id == "caller-supplied" || id != captured.Context.RequestID {
		t.Fatal("request identity not server-bound")
	}
	// A nested policy denial must survive the aborted business transaction.
	err := repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		_, e := f.svc.authorize(ctx, callerToken, "read:sources", scopeA, source.ID, "")
		return e
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	var events []Record
	if fake, ok := repo.(*fakeRepo); ok {
		events = fake.rows("audit_events")
	} else if p, ok := repo.(*Postgres); ok {
		events = pgCollection(t, p, "audit_events")
	}
	found := false
	denials := 0
	for _, event := range events {
		if event.Audit != nil && event.Audit.Decision == "deny" {
			denials++
			if event.Audit.RequestID == id {
				found = true
				if event.Audit.PolicyDecisionID != "deny-1" || event.Text != "" || event.CreatedBy != "human-a" {
					t.Fatal("invalid denial record")
				}
			}
		}
	}
	if !found || denials != 2 {
		t.Fatalf("missing committed denials: %v %d", found, denials)
	}
	// Allow auditing keeps the same HTTP request ID and never copies input tokens.
	f.svc.Auth = f.auth
	body, _ := json.Marshal(sourceInput(CreateInput{Scope: scopeA, Text: "a separate audited source"}))
	req = httptest.NewRequest("POST", "/v1/sources", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w = httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	if fake, ok := repo.(*fakeRepo); ok {
		found = false
		for _, event := range fake.rows("audit_events") {
			if event.Audit != nil && event.Audit.RequestID == w.Header().Get("X-Request-ID") {
				found = true
				if event.Audit.Decision != "allow" {
					t.Fatal("allow decision")
				}
			}
		}
		if !found {
			t.Fatal("allow request ID missing")
		}
	}
}
