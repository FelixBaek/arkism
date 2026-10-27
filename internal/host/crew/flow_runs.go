package crew

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/flowresult"
	"github.com/FelixBaek/arkism/internal/host"
)

func startRunNodes(c map[string]any, handle string, flow bool) (map[string]any, error) {
	if flow && !in([]string{"workflow", "agent", "team"}, pyStr(c["type"])) {
		return nil, crewErr(fmt.Sprintf("%s(이)라는 FLOW 작업을 찾지 못했어요", strings.TrimSpace(handle)))
	}
	if !flow && c["type"] != "workflow" {
		return nil, crewErr(fmt.Sprintf("$%s(이)라는 워크플로우를 찾지 못했어요", strings.TrimLeft(strings.TrimSpace(handle), "$")))
	}
	nodes := map[string]any{}
	for _, n := range list(obj(c["graph"])["nodes"]) {
		if id := obj(n)["id"]; truthy(id) {
			nodes[pyStr(id)] = map[string]any{"status": "pending"}
		}
	}
	if c["type"] == "workflow" && len(nodes) == 0 {
		return nil, crewErr("이 워크플로우에는 아직 단계가 없어요")
	}
	return nodes, nil
}

// Capabilities describe the current server/compiler boundary. A failed or old
// capability endpoint must not silently start through a less constrained path.
func requireFlowCapabilities(id string) error {
	if !runIDRe.MatchString(id) {
		return crewErr("FLOW 작업 id가 올바르지 않아요")
	}
	capability, err := apiGet("/v1/flow/capabilities/"+pyQuote(id, "")+"?"+scopeQuery(), 15*time.Second)
	if err != nil {
		return apiErr(err)
	}
	if capability["supported"] == true {
		return nil
	}
	unsupported := stringList(capability["unsupported"])
	if len(unsupported) > 0 {
		return crewErr(fmt.Sprintf("이 FLOW 실행 경로에서 지원하지 않는 설정: %s. 기존 설정은 보존했어요", strings.Join(unsupported, ", ")))
	}
	return crewErr("이 작업은 아직 공통 FLOW 실행을 지원하지 않아요. 기존 설정은 보존했어요")
}

// Child waits carry the durable child link and observed decision revision.
// Keep the parent node identity for the server's checked decision relay.
func runDecisionWait(st map[string]any) any {
	if st["status"] != "waiting" {
		return nil
	}
	w := obj(st["waiting"])
	if w["kind"] == "child" {
		if w["decision_kind"] != "approval" && w["decision_kind"] != "budget" {
			return nil
		}
		out := host.Obj{}
		for _, key := range []string{"node_id", "question", "kind", "child_run_id", "child_kind", "decision_kind"} {
			out = append(out, host.KV{Key: key, Value: getOr(w, key, "")})
		}
		if revision, present := w["child_revision"]; present {
			out = append(out, host.KV{Key: "child_revision", Value: revision})
		}
		return out
	}
	if w["kind"] != "approval" && w["kind"] != "budget" {
		return nil
	}
	if id, ok := w["node_id"].(string); ok {
		if _, decided := obj(st["decisions"])[id]; decided {
			return nil
		}
	}
	return host.Obj{{Key: "node_id", Value: getOr(w, "node_id", "")}, {Key: "question", Value: getOr(w, "question", "")}}
}

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
