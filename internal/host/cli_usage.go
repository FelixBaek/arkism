package host

// What one `claude -p --output-format json` call used: the digest and merge workers send it with
// their proposals, capture records it as subscription usage, and memoryd prices it at list price.

import (
	"encoding/json"
	"sort"
)

// CLIUsage is {model, requests, input_tokens, output_tokens} from the CLI's JSON answer, or nil
// when it reported nothing. Cache reads and writes count as input (the ledger keeps one input
// figure), so the cost is an upper estimate. The model is the one that did most of the work
// (modelUsage), else requested.
func CLIUsage(res map[string]any, requested string) Obj {
	model, in, out := requested, int64(0), int64(0)
	if mu, _ := res["modelUsage"].(map[string]any); len(mu) > 0 {
		ids := make([]string, 0, len(mu))
		for id := range mu {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		best := int64(-1)
		for _, id := range ids {
			u, _ := mu[id].(map[string]any)
			i := cliTokens(u, "inputTokens") + cliTokens(u, "cacheReadInputTokens") + cliTokens(u, "cacheCreationInputTokens")
			o := cliTokens(u, "outputTokens")
			in, out = in+i, out+o
			if i+o > best {
				model, best = id, i+o
			}
		}
	} else if u, isMap := res["usage"].(map[string]any); isMap {
		in = cliTokens(u, "input_tokens") + cliTokens(u, "cache_read_input_tokens") + cliTokens(u, "cache_creation_input_tokens")
		out = cliTokens(u, "output_tokens")
	}
	if in+out == 0 || model == "" {
		return nil
	}
	return Obj{{Key: "model", Value: model}, {Key: "requests", Value: 1}, {Key: "input_tokens", Value: in}, {Key: "output_tokens", Value: out}}
}

func cliTokens(u map[string]any, key string) int64 {
	var n float64
	switch x := u[key].(type) {
	case json.Number:
		n, _ = x.Float64()
	case float64:
		n = x
	case int64:
		n = float64(x)
	case int:
		n = float64(x)
	}
	if n > 0 {
		return int64(n)
	}
	return 0
}
