// Package contextscope defines the Gate 2 wire contract; it is not a store.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// ContextScopeV1 binds the data boundary to an authenticated user and a use.
// PrincipalID is the user on whose behalf an explicitly bound actor operates.
type ContextScopeV1 struct {
	Scope
	PrincipalID string `json:"principal_id"`
	Purpose     string `json:"purpose"`
	Destination string `json:"destination"`
	RequestID   string `json:"request_id"`
}

var contextIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_./@+-]{0,255}$`)

func (s Scope) Valid() bool {
	return contextIdentifier.MatchString(s.WorkspaceID) && contextIdentifier.MatchString(s.ProjectID) && contextIdentifier.MatchString(s.UserID) && (s.AgentID == "" || contextIdentifier.MatchString(s.AgentID))
}

func (s ContextScopeV1) Validate(scope Scope) error {
	if !scope.Valid() || s.Scope != scope || s.PrincipalID != scope.UserID || !contextIdentifier.MatchString(s.Purpose) || !contextIdentifier.MatchString(s.RequestID) || (s.Destination != "internal" && (!strings.HasPrefix(s.Destination, "external:") || !contextIdentifier.MatchString(strings.TrimPrefix(s.Destination, "external:")))) {
		return errors.New("invalid context scope")
	}
	return nil
}

func NewContextScope(scope Scope, purpose, destination string) ContextScopeV1 {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return ContextScopeV1{Scope: scope, PrincipalID: scope.UserID, Purpose: purpose, Destination: destination, RequestID: hex.EncodeToString(id[:])}
}
