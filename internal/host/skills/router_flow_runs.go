package skills

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/FelixBaek/arkism/internal/host"
)

func listWorkflowRuns(args map[string]any) (map[string]any, error) {
	path, err := workflowRunsPath(args)
	if err != nil {
		return nil, err
	}
	response, err := routerRequest(path, nil)
	if err != nil {
		return nil, err
	}
	rows, ok := response["items"].([]any)
	if !ok {
		return nil, errors.New("workflow run list is unavailable")
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		item, err := workflowRunSummary(asMap(row))
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return map[string]any{"items": items, "authority": "data_only_untrusted"}, nil
}

func workflowRunsPath(args map[string]any) (string, error) {
	for key := range args {
		if key != "limit" && key != "status" {
			return "", fmt.Errorf("unsupported workflow list argument: %s", key)
		}
	}
	limit := int64(50)
	if raw, exists := args["limit"]; exists {
		var ok bool
		limit, ok = host.Int(raw)
		if !ok || limit < 1 || limit > 200 {
			return "", errors.New("limit must be an integer from 1 to 200")
		}
	}
	path := "/v1/crew/runs?" + scopeQueryAll() + "&brief=1&limit=" + strconv.FormatInt(limit, 10)
	if raw, exists := args["status"]; exists {
		status, ok := raw.(string)
		if !ok || !slices.Contains([]string{"running", "waiting", "succeeded", "failed", "stopped"}, status) {
			return "", errors.New("status must be a workflow execution status")
		}
		path += "&status=" + pyQuote(status)
	}
	return path, nil
}

func workflowRunSummary(record map[string]any) (map[string]any, error) {
	run := asMap(record["crew_run"])
	id, crewID, status := argStr(record, "id", ""), argStr(run, "crew_id", ""), argStr(run, "status", "")
	if id == "" || crewID == "" || !slices.Contains([]string{"running", "waiting", "succeeded", "failed", "stopped"}, status) {
		return nil, errors.New("workflow run metadata is incomplete")
	}
	// Availability means a terminal result can be fetched separately. It does
	// not certify success, quality, delivery, or the presence of a nonempty report.
	return map[string]any{
		"id": id, "crew_id": crewID, "status": status,
		"source_type": argStr(run, "source_type", ""), "parent_id": argStr(run, "parent_id", ""),
		"created_at": argStr(record, "created_at", ""), "result_available": slices.Contains([]string{"succeeded", "failed", "stopped"}, status),
	}, nil
}
