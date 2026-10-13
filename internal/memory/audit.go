package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type auditRequestKey struct{}

type AuditMetadata struct {
	RequestID        string `json:"request_id" bson:"request_id"`
	Decision         string `json:"decision" bson:"decision"`
	PolicyDecisionID string `json:"policy_decision_id,omitempty" bson:"policy_decision_id,omitempty"`
}

// Denial classes stored in audit_events. Verified policy denials keep the
// historical "policy:deny" kind; an unverifiable policy answer, a provider
// failure and a pre-policy input rejection each get their own class so an
// audit reader can tell them apart without trusting unverified fields.
const (
	auditClassPolicyDeny    = "policy:deny"
	auditClassPolicyInvalid = "policy:invalid"
	auditClassPolicyError   = "policy:error"
	auditClassInputDeny     = "auth:deny"
	auditPrincipalUnknown   = "unknown"
)

// Requests that never produced a storable scope still leave an audit trail
// under one literal quarantine scope that carries none of the caller's values.
var auditQuarantineScope = Scope{WorkspaceID: "__unparsed__", ProjectID: "__unparsed__"}

func auditRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(auditRequestKey{}).(string); ok && id != "" {
		return id
	}
	return ID()
}

func verifiedDenial(request Authorization, d Decision) bool {
	return !d.Allowed && d.Scope == request.Scope && d.Context == request.Context &&
		validRunLabel(d.PrincipalID, 256) && validRunLabel(d.DecisionID, 256) && decisionRoles[d.Role] &&
		!containsSecret(d.PrincipalID) && !containsSecret(d.DecisionID)
}

func auditScope(request Authorization) Scope {
	if validScope(request.Scope) && !containsSecret(structuredText(request.Scope)) {
		return request.Scope
	}
	return auditQuarantineScope
}

func auditTarget(request Authorization) *Ref {
	if request.ResourceID == "" || containsSecret(request.ResourceID) || len(request.ResourceID) > 512 {
		return nil
	}
	return &Ref{ID: request.ResourceID}
}

// One denial is one event: the ID is derived from the outcome class, the bound
// request ID, the action, the resource and the policy answer, so retrying a
// parent transaction recomputes the same ID instead of adding a second row.
// The bound request ID comes from the HTTP middleware; callers without one
// (workers) dedupe per action, resource and policy answer instead.
func auditDenialID(class, boundRequestID string, request Authorization, decisionID, principal string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{class, boundRequestID, request.Action, request.ResourceID, decisionID, principal}, "\x00")))
	return "aud" + hex.EncodeToString(sum[:])
}

// auditDenial persists one denial outside the caller's transaction. An event
// that is already stored (by an earlier attempt of the same request) is left
// untouched, so per-row denials stay exactly-once across retries.
func (s *Service) auditDenial(ctx context.Context, class string, request Authorization, boundRequestID, decisionID, principal, role, policyDecisionID string) error {
	if s.Repo == nil {
		return ErrUnavailable
	}
	if class == "" || containsSecret(class) || containsSecret(request.Action) || len(request.Action) > 256 {
		return ErrSecretDetected
	}
	if principal == "" || containsSecret(principal) || len(principal) > 256 {
		principal = auditPrincipalUnknown
		role = auditPrincipalUnknown
	}
	if role == "" || !decisionRoles[role] {
		role = auditPrincipalUnknown
	}
	if containsSecret(policyDecisionID) || len(policyDecisionID) > 256 {
		policyDecisionID = ""
		decisionID = ""
	}
	now := time.Now().UTC()
	r := Record{ID: auditDenialID(class, boundRequestID, request, decisionID, principal), Scope: auditScope(request),
		Kind: class + ":" + request.Action, Status: "active", Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: principal, CreatedByType: role,
		Target: auditTarget(request),
		Audit:  &AuditMetadata{RequestID: boundRequestID, Decision: "deny", PolicyDecisionID: policyDecisionID}}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.Get(ctx, "audit_events", r.Scope, r.ID); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		// ErrConflict is the concurrent insert of the same derived ID: stored once.
		if err := tx.Insert(ctx, "audit_events", r); err != nil && !errors.Is(err, ErrConflict) {
			return err
		}
		return nil
	})
}

// boundAuditRequestID returns the request ID the HTTP middleware bound to ctx;
// a per-call generated context ID never takes part in denial identity.
func boundAuditRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(auditRequestKey{}).(string); ok {
		return id
	}
	return ""
}

// auditDeniedInput records a rejection that never reached the policy provider:
// missing credentials, an unusable scope or a secret-bearing identifier. None
// of the rejected input is copied into the record.
func (s *Service) auditDeniedInput(ctx context.Context, action string, scope Scope, id string) {
	if s.Auth == nil {
		return
	}
	request := Authorization{Action: action, Scope: scope, ResourceID: id}
	_ = s.auditDenial(ctx, auditClassInputDeny, request, boundAuditRequestID(ctx), "", auditPrincipalUnknown, auditPrincipalUnknown, "")
}

func (s *Service) auditPolicyDenial(ctx context.Context, request Authorization, d Decision) error {
	return s.auditDenial(ctx, auditClassPolicyDeny, request, request.Context.RequestID, d.DecisionID, d.PrincipalID, d.Role, d.DecisionID)
}
