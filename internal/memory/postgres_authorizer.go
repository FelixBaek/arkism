package memory

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

type PostgresAuthorizer struct{ store *Postgres }

func NewPostgresAuthorizer(store *Postgres) (*PostgresAuthorizer, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalid
	}
	return &PostgresAuthorizer{store: store}, nil
}

// ScopeOnly: the decision reads only the token, scope and action, never the resource id.
func (a *PostgresAuthorizer) ScopeOnly() bool { return true }

func (a *PostgresAuthorizer) Authorize(ctx context.Context, token string, request Authorization) (Decision, error) {
	if a == nil || a.store == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(request.Action) == "" || !validScope(request.Scope) {
		return Decision{}, ErrDenied
	}
	ctx = ContextWithPostgresToken(ctx, token)
	var principal, role string
	var allowed, transferAllowed bool
	if err := a.store.q().QueryRow(ctx, `SELECT principal_id,principal_role,allowed,
CASE WHEN $4 LIKE 'external:%' THEN has_permission($1,$2,'external:transfer') ELSE true END
FROM authorize_principal($1,$2,$3)`, request.Scope.WorkspaceID, request.Scope.ProjectID, request.Action, request.Context.Destination).Scan(&principal, &role, &allowed, &transferAllowed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Decision{}, ErrDenied
		}
		return Decision{}, ErrUnavailable
	}
	decisionID := ContentHash(strings.Join([]string{principal, request.Action, request.Scope.WorkspaceID, request.Scope.ProjectID}, "\x00"))
	return Decision{
		Context: request.Context, DecisionID: decisionID, Allowed: allowed,
		PrincipalID: principal, Role: role, Scope: request.Scope,
		ExternalTransfer: allowed && transferAllowed, ContentSafe: allowed && !containsSecret(request.Content),
	}, nil
}

var _ Authorizer = (*PostgresAuthorizer)(nil)
