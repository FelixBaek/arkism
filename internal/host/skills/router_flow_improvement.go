package skills

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// This adapter forwards caller-supplied evidence only. It never chooses an
// evaluator, changes a policy, advances automatically, or switches credentials.
func callFlowImprovement(name string, args map[string]any) (map[string]any, error) {
	path, payload, err := flowImprovementRequest(name, args)
	if err != nil {
		return nil, err
	}
	out, err := routerRequest(path, payload)
	if err != nil {
		return nil, err // uncertain writes are inspected with get, never repeated here
	}
	if out == nil {
		return nil, errors.New("FLOW improvement response is unavailable")
	}
	out["authority"] = "data_only_untrusted"
	return out, nil
}

func flowImprovementRequest(name string, args map[string]any) (string, any, error) {
	keys := map[string][]string{
		"start_flow_improvement":   {"request_id", "source_run", "generation", "runtime", "trial_ends_at"},
		"get_flow_improvement":     {"id"},
		"list_flow_improvements":   {"after", "limit"},
		"advance_flow_improvement": {"id", "expected_revision", "action_id", "action", "recover", "comparison", "application"},
	}[name]
	if keys == nil {
		return "", nil, errors.New("unknown FLOW improvement tool")
	}
	for key := range args {
		if !slices.Contains(keys, key) {
			return "", nil, fmt.Errorf("unsupported FLOW improvement argument: %s", key)
		}
	}
	base := "/v1/flow/improvements"
	if name == "list_flow_improvements" {
		path, err := flowImprovementListPath(base, args)
		return path, nil, err
	}
	if name == "start_flow_improvement" {
		payload, err := flowImprovementPayload(name, args)
		return base, payload, err
	}
	id, ok := args["id"].(string)
	if !ok || strings.TrimSpace(id) == "" || len(id) > 128 {
		return "", nil, errors.New("id is required")
	}
	path := base + "/" + pyQuote(id)
	if name == "get_flow_improvement" {
		return path + "?" + scopeQueryAll(), nil, nil
	}
	payload, err := flowImprovementPayload(name, args)
	return path + "/advance", payload, err
}

func flowImprovementListPath(base string, args map[string]any) (string, error) {
	limit := int64(50)
	if raw, exists := args["limit"]; exists {
		var ok bool
		limit, ok = host.Int(raw)
		if !ok || limit < 1 || limit > 100 {
			return "", errors.New("limit must be an integer from 1 to 100")
		}
	}
	path := base + "?" + scopeQueryAll() + "&limit=" + strconv.FormatInt(limit, 10)
	if raw, exists := args["after"]; exists {
		after, ok := raw.(string)
		if !ok || len(after) > 128 {
			return "", errors.New("after must be a saved next_cursor")
		}
		path += "&after=" + pyQuote(after)
	}
	return path, nil
}

func flowImprovementPayload(name string, args map[string]any) (map[string]any, error) {
	payload := make(map[string]any, len(args)+1)
	for key, value := range args {
		if key != "id" {
			payload[key] = value
		}
	}
	payload["scope"] = host.Scope()
	if name == "start_flow_improvement" {
		generation, err := flowImprovementScopedPayload(args["generation"])
		if err != nil {
			return nil, err
		}
		payload["generation"] = generation
		return payload, nil
	}
	if !slices.Contains([]string{"generate", "compare", "approve", "trial", "observe", "apply", "monitor", "rollback"}, argStr(args, "action", "")) {
		return nil, errors.New("a supported explicit action is required")
	}
	if application, exists := args["application"]; exists {
		bound, err := flowImprovementScopedPayload(application)
		if err != nil {
			return nil, err
		}
		payload["application"] = bound
	}
	return payload, nil
}

func flowImprovementScopedPayload(value any) (map[string]any, error) {
	input, ok := value.(map[string]any)
	if !ok || input == nil {
		return nil, errors.New("generation/application must be the existing typed API payload")
	}
	if _, exists := input["scope"]; exists {
		return nil, errors.New("FLOW improvement scope comes from the configured client")
	}
	out := make(map[string]any, len(input)+1)
	for key, value := range input {
		out[key] = value
	}
	out["scope"] = host.Scope()
	return out, nil
}
