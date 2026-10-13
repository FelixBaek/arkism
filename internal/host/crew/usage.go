package crew

// Token use of one run-node call. The CLIs print text only, so tokens are
// counted from characters and priced at list price: always an estimate (추정).

import (
	"math"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/prices"
)

// usageOf is {input_tokens, output_tokens, cost_usd?, estimated} for an answered spec.
func usageOf(spec, res map[string]any) map[string]any {
	in := prices.EstimateTokens(sOr(spec["instructions"]) + "\n" + sOr(spec["task"]))
	out := prices.EstimateTokens(sOr(res["output"]))
	u := map[string]any{"input_tokens": in, "output_tokens": out, "estimated": true}
	model := sOr(res["model"])
	if model == "" {
		model = sOr(spec["model"])
	}
	if c, ok := prices.Cost(model, in, out); ok {
		u["cost_usd"] = math.Round(c*1e6) / 1e6
	}
	return u
}

// cliProvider is the provider id 라우팅 files a CLI's calls under.
var cliProvider = map[string]string{"claude": "anthropic", "codex": "openai", "glm": "zai", "gemini": "google", "agy": "google",
	"grok": "xai", "custom": "custom"}

// providerOf is the provider of a fixed-model call: the spec's own, else its CLI's.
func providerOf(spec map[string]any, model string) string {
	if p := strings.TrimSpace(sOr(spec["provider"])); p != "" {
		return p
	}
	if row, ok := cliFor(model); ok {
		if p := cliProvider[row.Name]; p != "" {
			return p
		}
		return row.Name
	}
	return "unknown"
}

// reportFixed counts one fixed-model call on the routing screen with what it worked for (origin). Best effort.
func reportFixed(spec, res map[string]any) {
	model := strings.TrimSpace(sOr(res["model"]))
	if model == "" {
		model = strings.TrimSpace(sOr(spec["model"]))
	}
	if model == "" {
		return
	}
	call := map[string]any{"model": providerOf(spec, model) + "/" + model, "decision": "fixed", "ok": res["ok"] == true,
		"latency_ms": getOr(res, "duration_ms", 0), "origin": spec["origin"]}
	if res["ok"] != true {
		call["error"] = routedErrorCode(sOr(res["error"]))
	}
	addUsage(call, res)
	host.ReportCall(call)
}

// addUsage copies a result's estimated tokens and cost into a routing call.
func addUsage(call, res map[string]any) {
	u, ok := res["usage"].(map[string]any)
	if !ok {
		return
	}
	for _, k := range []string{"input_tokens", "output_tokens", "cost_usd", "estimated"} {
		if v, ok := u[k]; ok {
			call[k] = v
		}
	}
}
