package crew

// Token use of one run-node call. The CLIs print text only, so tokens are
// counted from characters and priced at list price: always an estimate (추정).

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/FelixBaek/arkism/internal/crewrun"
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

var observationIntegerLexeme = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func usageObservationError(code, path string) error {
	return &crewrun.ObservationError{Code: code, Path: path}
}
func usageObservationInteger(value any, path string) (int64, error) {
	var number int64
	switch typed := value.(type) {
	case int64:
		number = typed
	case int:
		number = int64(typed)
	case json.Number:
		if !observationIntegerLexeme.MatchString(string(typed)) {
			return 0, usageObservationError("invalid_type", path)
		}
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, usageObservationError("invalid_value", path)
		}
		number = parsed
	default:
		return 0, usageObservationError("invalid_type", path)
	}
	if number < 0 || number > 1000000000 {
		return 0, usageObservationError("invalid_value", path)
	}
	return number, nil
}
func usageObservationCost(value any, path string) (float64, error) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case int64:
		number = float64(typed)
	case int:
		number = float64(typed)
	case json.Number:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, usageObservationError("invalid_value", path)
		}
		number = parsed
	default:
		return 0, usageObservationError("invalid_type", path)
	}
	if number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, usageObservationError("invalid_value", path)
	}
	if number == 0 {
		number = 0
	}
	return number, nil
}
func chooseUsageComponent(reported, estimated map[string]any, key string) (any, string) {
	if value, ok := reported[key]; ok && value != nil {
		return value, "provider_reported"
	}
	if value, ok := estimated[key]; ok && value != nil {
		return value, "estimated"
	}
	return nil, "unknown"
}
func usageObservationString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// NormalizeUsageObservation creates the separate usage_observation sidecar.
// Inputs must come from the indicated trusted producer boundary. An arbitrary
// string claiming "reported" is not provenance. Existing usageOf/reporting
// behavior is unchanged; callers opt in before a wrapper discards richer data.
// Counts accept exact int/int64 or JSON integer lexemes (host.Decode preserves
// json.Number). Bool, float and exponent counts are rejected, never rounded.
func NormalizeUsageObservation(reported, estimated map[string]any, providerSchema, reportedPricingRevision, estimatedPricingRevision *string) (crewrun.UsageObservation, error) {
	result := crewrun.UnknownUsageObservation()
	if flag, ok := reported["estimated"]; ok {
		isEstimate, valid := flag.(bool)
		if !valid || isEstimate {
			return result, usageObservationError("invalid_value", "$.reported.estimated")
		}
	}
	parts := []struct {
		key   string
		value **int64
		basis *string
	}{
		{"input_tokens", &result.InputTokens, &result.InputBasis}, {"output_tokens", &result.OutputTokens, &result.OutputBasis},
		{"cache_read_tokens", &result.CacheReadTokens, &result.CacheReadBasis}, {"cache_write_tokens", &result.CacheWriteTokens, &result.CacheWriteBasis},
		{"reasoning_tokens", &result.ReasoningTokens, &result.ReasoningBasis},
	}
	for _, part := range parts {
		value, basis := chooseUsageComponent(reported, estimated, part.key)
		if value == nil {
			continue
		}
		number, err := usageObservationInteger(value, "$."+part.key)
		if err != nil {
			return crewrun.UnknownUsageObservation(), err
		}
		*part.value = &number
		*part.basis = basis
	}
	result.UsageBasis = "unknown"
	if result.InputBasis != "unknown" && result.OutputBasis != "unknown" {
		result.UsageBasis = "estimated"
		if result.InputBasis == "provider_reported" && result.OutputBasis == "provider_reported" {
			result.UsageBasis = "provider_reported"
		}
	}
	value, basis := chooseUsageComponent(reported, estimated, "cost_usd")
	if value != nil {
		cost, err := usageObservationCost(value, "$.cost_usd")
		if err != nil {
			return crewrun.UnknownUsageObservation(), err
		}
		result.CostUSD = &cost
		result.CostBasis = basis
		if basis == "provider_reported" {
			result.PricingRevision = usageObservationString(reportedPricingRevision)
		} else {
			result.PricingRevision = usageObservationString(estimatedPricingRevision)
		}
	}
	result.ProviderUsageSchema = usageObservationString(providerSchema)
	if err := crewrun.ValidateUsageObservation(result); err != nil {
		return crewrun.UnknownUsageObservation(), err
	}
	return result, nil
}
