package skills

import (
	"errors"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// readWiki backs the read_wiki MCP tool: reviewed wiki pages only (the server
// never returns drafts on this path), always labelled as untrusted data.
func readWiki(args map[string]any) (any, error) {
	query := strings.TrimSpace(argStr(args, "query", ""))
	page := strings.TrimSpace(argStr(args, "page", ""))
	if (query == "") == (page == "") {
		return nil, errors.New("pass either page or query")
	}
	payload := map[string]any{"scope": host.Scope(), "query": query, "page": page}
	for _, key := range []string{"depth", "limit"} {
		if v, ok := args[key]; ok && v != nil {
			if _, isBool := v.(bool); isBool {
				return nil, errors.New(key + " must be an integer")
			}
			n, ok := pyInt(v)
			if !ok {
				return nil, errors.New(key + " must be an integer")
			}
			payload[key] = int(n)
		}
	}
	res, err := routerRequest("/v1/wiki/read", payload)
	if err != nil {
		return nil, err
	}
	if res["authority"] != "data_only_untrusted" {
		return nil, errors.New("wiki read returned no data-only authority")
	}
	items, _ := res["items"].([]any)
	if items == nil {
		items = []any{}
	}
	return host.Obj{{Key: "items", Value: items}, {Key: "omitted", Value: getOr(res, "omitted", []any{})}, {Key: "tokens_used", Value: getOr(res, "tokens_used", 0)},
		{Key: "authority", Value: "data_only_untrusted"}, {Key: "note", Value: getOr(res, "note", "")}}, nil
}
