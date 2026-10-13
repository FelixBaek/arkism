package memory

import "context"

// The UI capability contract: one advisory probe per action the product
// screens show, answered by the same policy path every other request uses.
// A hidden menu stays hidden only until the next check — every real action
// is still authorized (and can still be refused) at request time.
type Capability struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

var capabilityCatalog = []Capability{
	{"read_sources", "read:sources"},
	{"read_memories", "read:memories"},
	{"read_artifacts", "read:artifacts"},
	{"review_list", "review:memory_candidates"},
	{"review_decide", "transition:memory_candidates"},
	{"propose", "write:memory_candidates"},
	{"supersede", "transition:memories"},
	{"delete_memories", "delete:memories"},
	{"delete_sources", "delete:sources"},
	{"search", "search:memories"},
	{"context", "context:read"},
}

// Actions the service refuses for non-human principals regardless of what the
// policy answers; the advertised contract must match that enforcement.
var humanOnlyActions = map[string]bool{
	"review_list":     true,
	"review_decide":   true,
	"supersede":       true,
	"delete_memories": true,
	"delete_sources":  true,
}

type Capabilities struct {
	AssistRequestLookup bool            `json:"assist_request_lookup"`
	Scope               Scope           `json:"scope"`
	Principal           string          `json:"principal_id,omitempty"`
	Role                string          `json:"role,omitempty"`
	Actions             map[string]bool `json:"actions"`
	Advisory            bool            `json:"advisory"`
}

// Capabilities probes actions sequentially: denial audits use serializable
// transactions and concurrent probes can exhaust their own retries. Any
// policy failure fails the whole answer (fail-closed), never a partial list.
func (s *Service) Capabilities(ctx context.Context, token string, scope Scope) (Capabilities, error) {
	if s.Auth == nil || token == "" || !validScope(scope) || containsSecret(structuredText(scope)) {
		return Capabilities{}, ErrDenied
	}
	out := Capabilities{Scope: scope, Actions: make(map[string]bool, len(capabilityCatalog)), Advisory: true, AssistRequestLookup: true}
	for _, cap := range capabilityCatalog {
		d, err := s.authorize(ctx, token, cap.Action, scope, "", "")
		if err != nil {
			return Capabilities{}, err
		}
		allowed := d.Allowed
		if humanOnlyActions[cap.Key] {
			allowed = allowed && d.Role == roleHuman
		}
		out.Actions[cap.Key] = allowed
		// Identity comes only from decisions that passed verification; an
		// unverified answer never names a principal to the UI.
		if out.Principal == "" && d.Allowed && d.PrincipalID != "" && !containsSecret(d.PrincipalID) && decisionRoles[d.Role] {
			out.Principal, out.Role = d.PrincipalID, d.Role
		}
	}
	return out, nil
}
