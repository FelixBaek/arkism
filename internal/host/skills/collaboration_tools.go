package skills

import (
	"errors"
	"github.com/FelixBaek/arkism/internal/host"
)

// Never accept caller-supplied scope or transport paths. Server validation is
// authoritative; this allowlist also prevents silently ignored tool arguments.
func collaborationRead(name string, args map[string]any) (map[string]any, error) {
	type endpoint struct {
		path string
		keys []string
	}
	endpoints := map[string]endpoint{
		"recent_sessions":        {"/v1/work-checkpoints/recent", []string{"repository", "session_id", "limit", "max_age_days"}},
		"recall_for_role":        {"/v1/memory/role-search", []string{"role", "query", "limit", "max_chars"}},
		"expand_knowledge_graph": {"/v1/knowledge-graph/expand", []string{"start", "max_depth", "limit"}},
	}
	e, ok := endpoints[name]
	if !ok {
		return nil, errors.New("unknown collaboration tool")
	}
	payload := map[string]any{"scope": host.Scope()}
	for key, value := range args {
		allowed := false
		for _, candidate := range e.keys {
			if key == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errors.New("unsupported collaboration argument")
		}
		payload[key] = value
	}
	return routerRequest(e.path, payload)
}
