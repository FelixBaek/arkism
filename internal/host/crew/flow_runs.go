package crew

import (
	"encoding/json"
	"github.com/FelixBaek/arkism/internal/flowresult"
	"github.com/FelixBaek/arkism/internal/host"
	"strings"
	"time"
)

// Preserve full terminal node outputs; Summary is intentionally size limited.
func workflowResult(st map[string]any) string {
	raw, _ := json.Marshal(st)
	return flowresult.Text(raw)
}

func waitWorkflow(id, kind string, wait float64) (map[string]any, error) {
	if kind == "" || kind == "workflow" {
		return waitRun(id, wait, nil)
	}
	if !runIDRe.MatchString(id) || !in([]string{"assist", "meeting"}, kind) {
		return nil, crewErr("실행 종류나 id가 올바르지 않아요")
	}
	path := "/v1/crew/" + map[string]string{"assist": "assists", "meeting": "meetings"}[kind] + "/" + pyQuote(id, "") + "?" + scopeQuery()
	get := func() (map[string]any, error) { r, e := apiGet(path, 15*time.Second); return r, apiErr(e) }
	r, err := get()
	if err != nil {
		return nil, err
	}
	return pollView(wait, r, get, func(rec map[string]any) map[string]any {
		if kind == "meeting" {
			v := meetingView(id, obj(rec["crew_meeting"]))
			v["run_kind"] = kind
			v["terminal"] = in([]string{"succeeded", "failed", "stopped", "partial"}, pyStr(v["status"]))
			return v
		}
		a := obj(rec["crew_assist"])
		status := map[string]string{"done": "succeeded", "failed": "failed"}[pyStr(a["status"])]
		if status == "" {
			status = "running"
		}
		summary := pyStr(getOr(a, "output", ""))
		if status == "failed" {
			summary = pyStr(getOr(a, "error", ""))
		}
		return map[string]any{"run_id": id, "run_kind": kind, "status": status, "terminal": status != "running", "summary": summary, "result": summary, "waiting": nil}
	})
}

func cancelWorkflow(id, kind string) (map[string]any, error) {
	if kind != "" && kind != "workflow" {
		return nil, crewErr("이 실행 종류는 아직 도구 취소를 지원하지 않아요. FLOW에서 중지해 주세요")
	}
	r, err := getRun(id)
	if err != nil {
		return nil, err
	}
	st := obj(r["crew_run"])
	if in([]string{"succeeded", "failed", "stopped"}, pyStr(st["status"])) {
		return runView(r), nil
	}
	// An explicit stop endpoint checks creator/role, retains progress, and cancels
	// the engine context before another step is started.
	r, err = apiPost("/v1/crew/runs/"+pyQuote(id, "")+"/stop", host.Obj{{Key: "scope", Value: scope()}, {Key: "expected_revision", Value: r["revision"]}}, 15*time.Second)
	if err != nil {
		return nil, apiErr(err)
	}
	view := runView(r)
	view["cancel_requested"] = true
	view["summary"] = strings.TrimSpace(pyStr(view["summary"]))
	return view, nil
}
