package crew

// A connection of kind "hermes" talks to Hermes Agent's API server Runs API instead of arkism.backend.v1. Checked
// against Hermes Agent v0.22.0+159.gc5fc095 (commit c5fc0958b49f415041c7f356c8bdf9f914037e89) source:
//   - GET /v1/capabilities: features run_submission, run_status, run_stop, runs_idempotency{supported,durable}
//     (gateway/platforms/api_server.py:2513-2531, _STATIC_FEATURE_FLAGS at :68).
//   - POST /v1/runs {input, instructions, model} with Idempotency-Key; 202 {run_id, status, replayed}; the same key
//     replays the same run for 24h, a different body under it is 409 (api_server_runs.py:620-757, :461-478;
//     api_server_run_idempotency.py:58). instructions become the run's ephemeral system prompt (:426, :733).
//   - GET /v1/runs/{id}: status queued|running|waiting_for_approval|stopping|completed|failed|cancelled|interrupted,
//     output, error (a string), runtime.model = the model that served it, after a provider fallback too (:181-201,
//     :937-990, :769-779).
//   - POST /v1/runs/{id}/stop: {"status": "stopping"} until the work exits, then cancelled (:1253-1274).
// Not offered per request, so reported false: tool restriction (tools come from the server's own api_server toolsets,
// api_server.py:2374), cost (usage has token counters only, and a missing counter reads as 0, api_server_runs.py:759),
// a memory section kept apart from instructions.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

const backendKindHermes = "hermes"

// hermesState maps a Hermes run status to the arkism.backend.v1 state; anything else stays unknown (never success).
var hermesState = map[string]string{"queued": "accepted", "started": "accepted", "running": "running",
	"waiting_for_approval": "waiting_approval", "stopping": "cancel_requested", "completed": "succeeded",
	"failed": "failed", "cancelled": "cancelled", "interrupted": "failed"}

type hermesRun struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	Output  string `json:"output"`
	Error   string `json:"error"`
	Runtime struct {
		Model string `json:"model"`
	} `json:"runtime"`
}

func (h hermesRun) reply() backendReply {
	r := backendReply{TaskID: h.RunID, Status: hermesState[h.Status], Model: h.Runtime.Model}
	if r.Status == "" {
		r.Status = "unknown"
	}
	if r.Status == "succeeded" {
		r.Result = &struct {
			Text string `json:"text"`
		}{h.Output}
	}
	if msg := strings.TrimSpace(h.Error); msg != "" || h.Status == "interrupted" {
		if msg == "" {
			msg = "Hermes가 실행을 중단했어요"
		}
		r.Error = &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{h.Status, msg}
	}
	return r
}

func hermesCaps(ctx context.Context, b *backend) (map[string]bool, error) {
	var page struct {
		Object   string `json:"object"`
		Features struct {
			RunSubmission bool `json:"run_submission"`
			RunStatus     bool `json:"run_status"`
			RunStop       bool `json:"run_stop"`
			Idempotency   struct {
				Supported bool `json:"supported"`
				Durable   bool `json:"durable"`
			} `json:"runs_idempotency"`
		} `json:"features"`
	}
	if _, err := backendCall(ctx, b, http.MethodGet, "/v1/capabilities", nil, "", &page); err != nil {
		return nil, err
	}
	if page.Object != "hermes.api_server.capabilities" || !page.Features.RunSubmission {
		return nil, crewErr("Hermes API 서버의 Runs API가 보이지 않아요")
	}
	f := page.Features
	caps := map[string]bool{}
	for _, k := range backendCaps {
		caps[k] = false
	}
	caps["status"], caps["cancel"] = f.RunStatus, f.RunStop
	caps["idempotency"] = f.Idempotency.Supported && f.Idempotency.Durable
	caps["instructions"], caps["model_choice"] = true, true // /v1/runs always honours instructions and model
	return caps, nil
}

// hermesBody is the Runs request for an arkism.backend.v1 start body; built once, so a retry sends the same payload.
func hermesBody(body map[string]any) map[string]any {
	instructions := strings.TrimSpace(sOr(body["instructions"]) + "\n\n" + sOr(body["skill_documents"]))
	out := map[string]any{"input": sOr(body["task"])}
	if instructions != "" {
		out["instructions"] = instructions
	}
	if m := sOr(obj(body["model"])["requested"]); m != "" {
		out["model"] = m
	}
	return out
}

// taskStart, taskStatus and taskCancel are the three task calls in the connection's own wire format.
func taskStart(ctx context.Context, b *backend, body map[string]any, key string, out *backendReply) (int, error) {
	if b.Kind != backendKindHermes {
		return backendCall(ctx, b, http.MethodPost, "/v1/tasks", body, key, out)
	}
	var h hermesRun
	code, err := backendCall(ctx, b, http.MethodPost, "/v1/runs", hermesBody(body), key, &h)
	if err == nil {
		// Admission (or a replay of it) says only that the run exists; the status call brings its result.
		*out = backendReply{TaskID: h.RunID, Status: "accepted"}
	}
	return code, err
}

func taskStatus(ctx context.Context, b *backend, id string, out *backendReply) (int, error) {
	if b.Kind != backendKindHermes {
		return backendCall(ctx, b, http.MethodGet, "/v1/tasks/"+url.PathEscape(id), nil, "", out)
	}
	var h hermesRun
	code, err := backendCall(ctx, b, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil, "", &h)
	if err == nil {
		*out = h.reply()
	}
	return code, err
}

func taskCancel(ctx context.Context, b *backend, id string, out *backendReply) error {
	if b.Kind != backendKindHermes {
		_, err := backendCall(ctx, b, http.MethodPost, "/v1/tasks/"+url.PathEscape(id)+"/cancel", map[string]any{"reason": "arkism"}, "", out)
		return err
	}
	var h hermesRun
	_, err := backendCall(ctx, b, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/stop", map[string]any{}, "", &h)
	if err == nil {
		*out = h.reply()
	}
	return err
}
