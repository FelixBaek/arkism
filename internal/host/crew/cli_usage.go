package crew

// What one `claude -p --output-format json` call used, for the provider_usage
// ledger: the digest and merge workers send it with their proposals and
// memoryd prices it at list price (internal/prices), so 사용량 shows money.

import (
	"sort"

	"github.com/FelixBaek/arkism/internal/host"
)

// cliUsage is {model, requests, input_tokens, output_tokens} from the CLI's
// JSON answer, or nil when it reported nothing. Cache reads and writes count as
// input (the ledger keeps one input figure), so the cost is an upper estimate.
// The model is the one that did most of the work (modelUsage), else requested.
func cliUsage(res map[string]any, requested string) host.Obj {
	model, in, out := requested, int64(0), int64(0)
	if mu := obj(res["modelUsage"]); len(mu) > 0 {
		ids := make([]string, 0, len(mu))
		for id := range mu {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		best := int64(-1)
		for _, id := range ids {
			u := obj(mu[id])
			i := tokens(u, "inputTokens") + tokens(u, "cacheReadInputTokens") + tokens(u, "cacheCreationInputTokens")
			o := tokens(u, "outputTokens")
			in, out = in+i, out+o
			if i+o > best {
				model, best = id, i+o
			}
		}
	} else if u := obj(res["usage"]); u != nil {
		in = tokens(u, "input_tokens") + tokens(u, "cache_read_input_tokens") + tokens(u, "cache_creation_input_tokens")
		out = tokens(u, "output_tokens")
	}
	if in+out == 0 || model == "" {
		return nil
	}
	return host.Obj{{Key: "model", Value: model}, {Key: "requests", Value: 1}, {Key: "input_tokens", Value: in}, {Key: "output_tokens", Value: out}}
}

func tokens(u map[string]any, key string) int64 {
	if n, ok := num(u[key]); ok && n > 0 {
		return int64(n)
	}
	return 0
}

// withUsage appends a "usage" field to payload when the call reported any.
func withUsage(payload, usage host.Obj) host.Obj {
	if usage == nil {
		return payload
	}
	return append(payload, host.KV{Key: "usage", Value: usage})
}
