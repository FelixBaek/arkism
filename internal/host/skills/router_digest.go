package skills

import (
	"errors"

	"github.com/FelixBaek/arkism/internal/host"
)

// recallDigest reads approved topic digests (topic summaries a person approved) for a query.
func recallDigest(query, limit any) (map[string]any, error) {
	q, ok := query.(string)
	if query != nil && !ok {
		return nil, errors.New("query must be a string")
	}
	n := 3
	switch v := limit.(type) {
	case nil:
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		n = int(v)
	default:
		f, err := pyFloat(v)
		if err != nil {
			return nil, err
		}
		n = int(f)
	}
	if n < 1 || n > 5 {
		return nil, errors.New("limit must be 1-5")
	}
	return routerRequest("/v1/digests/recall", map[string]any{"scope": host.Scope(), "query": runeCut(q, 1000), "limit": n})
}
