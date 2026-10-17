package crew

// Workflow runs ($이름 호출). A call starts a run the way the desktop does, with the owner token: the host engine
// picks it up within ten seconds, and a person's decision on an approval goes through the same API.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// apiGet and apiPost call Arkism with the owner token (fakes in tests).
var (
	apiGet  = host.Get
	apiPost = host.Post
	// runSleep and runClock pace waitRun (fakes in tests); runClock is seconds on a monotonic clock.
	runSleep = func(seconds float64) { time.Sleep(time.Duration(seconds * float64(time.Second))) }
	runClock = func() float64 { return float64(time.Since(processStart)) / float64(time.Second) }
)

var processStart = time.Now()

const runPoll = 2.0

var handleKey = regexp.MustCompile(`^[\p{L}\p{N}_-]{1,40}$`)

// resolve is {item, members, leader_models} for $handle, or nil when no live workflow has it.
func resolve(handle string, timeout time.Duration) (map[string]any, error) {
	key := strings.ReplaceAll(strings.TrimLeft(strings.TrimSpace(handle), "$"), " ", "")
	if !handleKey.MatchString(key) {
		return nil, nil
	}
	res, err := apiGet("/v1/crew/resolve?"+scopeQuery("handle", key), timeout)
	if code := host.HTTPCode(err); code == 400 || code == 404 {
		return nil, nil
	}
	return res, err
}

func resolveDefault(handle string) (map[string]any, error) { return resolve(handle, 8*time.Second) }

// listCrew is the workflows that can be called by $handle.
func listCrew(timeout time.Duration) ([]map[string]any, error) {
	page, err := apiGet("/v1/crew?"+scopeQuery(), timeout)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	owned := map[string]bool{} // workflows an agent works by are called through that agent, not by $
	for _, r := range list(getOr(page, "items", []any{})) {
		if c := obj(obj(r)["crew"]); c["type"] == "agent" && truthy(c["workflow"]) {
			owned[strings.TrimPrefix(pyStr(c["workflow"]), "$")] = true
		}
	}
	for _, r := range list(getOr(page, "items", []any{})) {
		c := obj(obj(r)["crew"])
		if c["type"] == "workflow" && truthy(c["handle"]) && !owned[pyStr(c["handle"])] {
			out = append(out, map[string]any{"handle": c["handle"], "type": "workflow", "name": getOr(c, "name", ""),
				"role": getOr(c, "role", ""), "category": getOr(c, "category", ""), "trigger": getOr(obj(c["trigger"]), "kind", ""),
				"enabled": truthy(c["enabled"]), "steps": len(list(obj(c["graph"])["nodes"]))})
		}
	}
	return out, nil
}

var crewOrder = []string{"handle", "type", "name", "role", "category", "trigger", "enabled", "steps"}

var apiRefused = map[int]string{400: "요청이 올바르지 않아요", 403: "권한이 없어요", 404: "찾지 못했어요", 409: "그사이 실행이 바뀌었어요. 다시 해 주세요"}

// apiErr turns an Arkism failure into a Korean refusal.
func apiErr(err error) error {
	if err == nil {
		return nil
	}
	if code := host.HTTPCode(err); code != 0 {
		if msg, ok := apiRefused[code]; ok {
			return crewErr(msg)
		}
		return crewErr(fmt.Sprintf("Arkism이 거절했어요 (http %d)", code))
	}
	return crewErr("Arkism에 연결하지 못했어요")
}

// runView is {run_id, status, summary, waiting}: waiting is {node_id, question} while an approval waits for a person.
func runView(rec map[string]any) map[string]any {
	st := obj(rec["crew_run"])
	w := obj(st["waiting"])
	_, decided := obj(st["decisions"])[pyStr(w["node_id"])]
	if _, isStr := w["node_id"].(string); !isStr {
		decided = false
	}
	asks := st["status"] == "waiting" && w["kind"] == "approval" && !decided
	var waiting any
	if asks {
		waiting = host.Obj{{Key: "node_id", Value: getOr(w, "node_id", "")}, {Key: "question", Value: getOr(w, "question", "")}}
	}
	return map[string]any{"run_id": getOr(rec, "id", ""), "status": getOr(st, "status", ""), "summary": getOr(st, "summary", ""), "waiting": waiting}
}

var viewOrder = []string{"run_id", "status", "summary", "waiting"}

var runIDRe = regexp.MustCompile(`^[\p{L}\p{N}_.:-]{1,256}$`)

func getRun(runID string) (map[string]any, error) {
	if !runIDRe.MatchString(runID) {
		return nil, crewErr("실행 id가 올바르지 않아요")
	}
	rec, err := apiGet("/v1/crew/runs/"+pyQuote(runID, "/")+"?"+scopeQuery(), 15*time.Second)
	return rec, apiErr(err)
}

// waitRun polls until the run ends, an approval waits for a person, or wait seconds pass; it returns runView.
func waitRun(runID string, wait float64, rec map[string]any) (map[string]any, error) {
	end := runClock() + max(0, wait)
	if rec == nil {
		var err error
		if rec, err = getRun(runID); err != nil {
			return nil, err
		}
	}
	for {
		view := runView(rec)
		if in([]string{"succeeded", "failed", "stopped"}, pyStr(view["status"])) || view["waiting"] != nil || runClock() >= end {
			return view, nil
		}
		runSleep(min(runPoll, max(0, end-runClock())))
		var err error
		if rec, err = getRun(runID); err != nil {
			return nil, err
		}
	}
}

// startWorkflow starts the workflow $handle with input {"request": task} (and "cwd" when given) and waits up to wait seconds.
func startWorkflow(handle, task string, wait float64, cwd string, resolver func(string) (map[string]any, error)) (map[string]any, error) {
	return startWorkflowAs("call", handle, task, wait, cwd, resolver)
}

// startWorkflowAs is startWorkflow with the run's trigger: "agent" when the agent that owns the workflow starts it.
func startWorkflowAs(trigger, handle, task string, wait float64, cwd string, resolver func(string) (map[string]any, error)) (map[string]any, error) {
	if strings.TrimSpace(task) == "" {
		return nil, crewErr("할 일(task)이 비었어요")
	}
	if resolver == nil {
		resolver = resolveDefault
	}
	res, err := resolver(handle)
	if err != nil {
		return nil, err
	}
	item := obj(res["item"])
	c := obj(item["crew"])
	if c["type"] != "workflow" {
		return nil, crewErr(fmt.Sprintf("$%s(이)라는 워크플로우를 찾지 못했어요", strings.TrimLeft(strings.TrimSpace(handle), "$")))
	}
	nodes := map[string]any{}
	for _, n := range list(obj(c["graph"])["nodes"]) {
		if id := obj(n)["id"]; truthy(id) {
			nodes[pyStr(id)] = map[string]any{"status": "pending"}
		}
	}
	if len(nodes) == 0 {
		return nil, crewErr("이 워크플로우에는 아직 단계가 없어요")
	}
	input := host.Obj{{Key: "request", Value: strings.TrimSpace(task)}}
	if cwd != "" {
		input = append(input, host.KV{Key: "cwd", Value: cwd})
	}
	run := host.Obj{{Key: "crew_id", Value: item["id"]}, {Key: "crew_revision", Value: item["revision"]}, {Key: "trigger", Value: trigger},
		{Key: "status", Value: "running"}, {Key: "input", Value: dumpLine(input)}, {Key: "nodes", Value: nodes}}
	rec, err := apiPost("/v1/crew/runs", host.Obj{{Key: "scope", Value: scope()}, {Key: "run", Value: run}}, 15*time.Second)
	if err != nil {
		return nil, apiErr(err)
	}
	return waitRun(pyStr(rec["id"]), wait, rec)
}

// decideRun answers the approval the run waits on, then waits like startWorkflow.
func decideRun(runID string, approve bool, note string, wait float64) (map[string]any, error) {
	rec, err := getRun(runID)
	if err != nil {
		return nil, err
	}
	asked, ok := runView(rec)["waiting"].(host.Obj)
	if !ok {
		return nil, crewErr("이 실행은 지금 승인을 기다리지 않아요")
	}
	nodeID, _ := asked.Get("node_id")
	body := host.Obj{{Key: "scope", Value: scope()}, {Key: "expected_revision", Value: rec["revision"]}, {Key: "node_id", Value: nodeID},
		{Key: "approve", Value: approve}}
	if note != "" {
		body = append(body, host.KV{Key: "note", Value: note})
	}
	rec, err = apiPost("/v1/crew/runs/"+pyQuote(runID, "/")+"/decision", body, 15*time.Second)
	if err != nil {
		return nil, apiErr(err)
	}
	return waitRun(runID, wait, rec)
}
