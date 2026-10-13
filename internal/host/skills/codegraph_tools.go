package skills

import (
	"errors"
	"strconv"

	"github.com/FelixBaek/arkism/internal/host"
)

// Code graph tools: read-only questions to /v1/code_graph/*. The server packs
// the answer under a token cap; this side only checks argument types and keeps
// the answer marked as data.
var codeGraphTools = map[string]string{
	"code_search_symbols": "search", "code_callers": "callers", "code_callees": "callees", "code_impact": "impact",
}

func callCodeGraph(name string, args map[string]any) (any, error) {
	op := codeGraphTools[name]
	payload := map[string]any{"scope": host.Scope()}
	text := func(key string, required bool, max int) error {
		v, ok := args[key]
		if !ok || v == nil {
			if required {
				return errors.New(key + " is required")
			}
			return nil
		}
		s, ok := v.(string)
		if !ok || required && pyStrip(s) == "" || host.Len(s) > max {
			return errors.New(key + " must be a nonempty string of at most " + strconv.Itoa(max) + " characters")
		}
		if s != "" {
			payload[key] = s
		}
		return nil
	}
	number := func(key string, lo, hi int) error {
		v, ok := args[key]
		if !ok || v == nil {
			return nil
		}
		n, isInt := host.Int(v)
		if !isInt || n < int64(lo) || n > int64(hi) {
			return errors.New(key + " must be an integer from " + strconv.Itoa(lo) + " to " + strconv.Itoa(hi))
		}
		payload[key] = n
		return nil
	}
	var err error
	if op == "search" {
		err = text("query", true, 200)
	} else {
		err = text("symbol", true, 1000)
	}
	for _, e := range []error{err, text("repo", false, 200), number("limit", 1, 200), number("max_tokens", 200, 8000)} {
		if e != nil {
			return nil, e
		}
	}
	if op == "impact" {
		if err := number("depth", 1, 8); err != nil {
			return nil, err
		}
	}
	res, err := routerRequest("/v1/code_graph/"+op, payload)
	if err != nil {
		return nil, err
	}
	res["authority"] = "data_only_untrusted"
	return res, nil
}
