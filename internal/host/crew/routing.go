package crew

// 라우팅 (model routing): Arkism picks the model; a failed pick hands the task to the fallbacks.

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var groupRe = regexp.MustCompile(`^group/[a-z0-9-]{1,64}$`)

func validGroup(group any) bool {
	s, ok := group.(string)
	return ok && groupRe.MatchString(s)
}

// decideRoute is {id, model, provider, decision, classifier_ms, fallbacks, view_hash?} from Arkism 라우팅.
// view is the metadata of the work state read before the choice; the server re-checks it is still current.
func decideRoute(task, group string, view host.Obj) (map[string]any, error) {
	body := host.Obj{{Key: "scope", Value: scope()}, {Key: "task", Value: task}}
	if view != nil {
		body = append(body, host.KV{Key: "view", Value: view})
	}
	if group != "" {
		if !validGroup(group) {
			return nil, crewErr("라우팅 그룹 ID를 확인해 주세요")
		}
		body = append(body, host.KV{Key: "group", Value: group})
	}
	d, err := runnerCall("/v1/routing/decide", body, 20*time.Second)
	if err != nil {
		return nil, crewErr("라우팅이 모델을 고르지 못했어요 (Arkism 라우팅 설정을 확인해 주세요)")
	}
	return d, nil
}

// reportCall is best effort: the routing screen counts the call (no prompt, no output); a failed report never
// fails the run.
func reportCall(call map[string]any) {
	_, _ = runnerCall("/v1/routing/calls", host.Obj{{Key: "scope", Value: scope()}, {Key: "call", Value: ordered(call, "model", "decision", "replaced", "ok", "latency_ms", "classifier_ms", "error")}}, 15*time.Second)
}

var routedCodeRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

func routedErrorCode(errText string) string {
	code, _, _ := strings.Cut(errText, ":")
	code = strings.ToLower(strings.TrimSpace(code))
	if routedCodeRe.MatchString(code) {
		return code
	}
	return "error"
}

type (
	decideFunc func(task, group string, view host.Obj) (map[string]any, error)
	runFn      func(spec map[string]any) (map[string]any, error)
	reportFunc func(call map[string]any)
)

// workHookContext reads a saved work checkpoint once as reference data and its metadata view (a fake in tests).
var workHookContext = func(workID string, request host.Requester, scope map[string]any) (host.Obj, string, error) {
	return host.WorkHookContextView(workID, request, scope, 8000, nil)
}

var runRoutedFn func(map[string]any, decideFunc, runFn, reportFunc) (map[string]any, error)

// runRouted runs a task on the model 라우팅 picks; when it fails, on each fallback in turn. Every attempt is reported.
// nil decide, run or report use the real ones.
func runRouted(spec map[string]any, decide decideFunc, run runFn, report reportFunc) (map[string]any, error) {
	task := strings.TrimSpace(sOr(spec["task"]))
	if task == "" {
		return nil, crewErr("할 일(task)이 비었어요")
	}
	if decide == nil {
		decide = decideRoute
	}
	if run == nil {
		run = runAttempt
	}
	if report == nil {
		report = host.ReportCall
	}
	group, err := routeGroup(spec)
	if err != nil {
		return nil, err
	}
	spec, view, err := withWorkContext(spec, task)
	if err != nil {
		return nil, err
	}
	d, err := decide(task, group, view)
	if err != nil {
		return nil, err
	}
	if group != "" && d["group"] != group {
		return nil, crewErr("요청한 그룹의 라우팅 결과를 확인할 수 없어요")
	}
	viewHash, err := confirmedView(d, view)
	if err != nil {
		return nil, err
	}
	first, ok := d["id"].(string)
	if !ok {
		return nil, crewErr("라우팅이 모델을 고르지 못했어요 (Arkism 라우팅 설정을 확인해 주세요)")
	}
	ids := []string{first}
	for _, f := range list(d["fallbacks"]) {
		ids = append(ids, pyStr(f))
	}
	res, i, stopped, err := tryRoutes(spec, d, ids, run, report)
	if err != nil {
		return nil, err
	}
	mid := ids[i]
	decision := d["decision"]
	if i > 0 {
		decision = "failover"
	}
	routed := host.Obj{{Key: "id", Value: mid}, {Key: "decision", Value: decision}, {Key: "tried", Value: toAny(ids[:i+1])}}
	if group != "" {
		routed = append(routed, host.KV{Key: "group", Value: group})
	}
	if stopped != "" { // effect_unknown: the attempt that ran may have changed things (also when it was the last candidate); budget_spent: no time left for the next one
		routed = append(routed, host.KV{Key: "stopped", Value: stopped})
	}
	res["routed"] = append(routed, viewHash...)
	return res, nil
}

// tryRoutes runs the task on each model in turn until one succeeds or a failed attempt may have changed things.
// The step's timeout is one budget for the whole chain: a later attempt gets only the time left, and none starts
// once it is spent. The attempt count is bounded by the candidate list. It returns the last result, its index and
// why it stopped early ("" when it did not).
func tryRoutes(spec, d map[string]any, ids []string, run runFn, report reportFunc) (map[string]any, int, string, error) {
	var res map[string]any
	budget, budgeted := chainBudget(spec)
	deadline := routeNow().Add(budget)
	for i, mid := range ids {
		provider, model, found := strings.Cut(mid, "/")
		if !found {
			return nil, 0, "", crewErr("라우팅이 모델을 고르지 못했어요 (Arkism 라우팅 설정을 확인해 주세요)")
		}
		attempt := withKey(withKey(spec, "model", model), "provider", provider)
		if i > 0 && budgeted {
			left := deadline.Sub(routeNow())
			if left < time.Second {
				return res, i - 1, "budget_spent", nil
			}
			attempt = withKey(attempt, "timeout_s", int64((left+time.Second-1)/time.Second))
		}
		var err error
		res, err = run(attempt)
		ran := err == nil // a *CrewError is refused before anything runs
		free := res[toolFree] == true
		delete(res, toolFree)
		if err != nil {
			if !isCrewErr(err) {
				return nil, 0, "", err
			}
			res = result(false, "", model, "", "", time.Time{}, err.Error())
		}
		report(routedCall(spec, d, ids, i, attempt, res))
		if res["ok"] == true {
			return res, i, "", nil
		}
		if ran && !free && mayHaveChanged(attempt, res) {
			return res, i, "effect_unknown", nil
		}
	}
	return res, len(ids) - 1, "", nil
}

var routeNow = time.Now // a test clock

// chainBudget is the step's timeout as the chain's one wall-time budget, the same default runAttempt would use.
// An invalid, zero-or-negative or overflowing timeout_s gets no budget: each attempt keeps its own limit and
// runNode refuses what it cannot run, as before.
func chainBudget(spec map[string]any) (time.Duration, bool) {
	_, budget, err := nodeLimits(spec)
	if b := strings.TrimSpace(sOr(spec["backend"])); b != "" && b != "local" && !truthy(spec["timeout_s"]) {
		budget = backendTimeout // an agent-runner step without timeout_s runs up to this long
	}
	if n, ok := pyIntArg(spec["timeout_s"]); truthy(spec["timeout_s"]) && (!ok || n <= 0 || n > (math.MaxInt64-int64(time.Second))/int64(time.Second)) { // leaves room for the round-up in tryRoutes
		return 0, false // runNode does not refuse these: a negative one just times out at once
	}
	return budget, err == nil
}

// toolFree marks a result from a call that had no tools (a chat completion); runNode and tryRoutes drop it.
const toolFree = "\x00tool_free"

// mayHaveChanged says a failed attempt may already have changed files or run commands, so no fallback may run
// the task again: a step that may write (write or full access, the default) failed after its CLI started. A CLI
// exit, a timeout or a limit hit mid-run proves nothing about tool calls already made. Only a CLI that never
// started or an account refused before the start (no start time) falls over, as does any read-only step.
func mayHaveChanged(attempt, res map[string]any) bool {
	access, _ := nodeAccess(attempt) // an invalid access was refused before the run
	notStarted := res["error"] == "cli_not_found" || res["error"] == "quota_exhausted" && neverStarted(res)
	return access != "read" && !notStarted
}

// neverStarted reads the zero duration a run without a start time reports, whatever number type carries it.
func neverStarted(res map[string]any) bool {
	ms, ok := num(res["duration_ms"])
	return ok && ms == 0
}

// withWorkContext reads the selected work once before the choice: the decision sees its metadata view and
// every fallback the same checkpoint as data. A failed read stops the run; it is never an empty history.
func withWorkContext(spec map[string]any, task string) (map[string]any, host.Obj, error) {
	workID := spec["work_id"]
	if !truthy(workID) {
		return spec, nil, nil
	}
	view, context, err := workHookContext(pyStr(workID), requester, host.Scope())
	if err != nil {
		return nil, nil, crewErr("선택한 작업 상태를 읽지 못해 실행하지 않았어요")
	}
	return withKey(spec, "task", task+"\n\n"+context), view, nil
}

// confirmedView is the decision's view_hash as a routed field; a sent view the server did not confirm stops the run.
func confirmedView(d map[string]any, view host.Obj) (host.Obj, error) {
	hash, _ := d["view_hash"].(string)
	if view != nil && hash == "" {
		return nil, crewErr("선택한 작업 상태를 라우팅이 확인하지 못해 실행하지 않았어요")
	}
	if hash == "" {
		return nil, nil
	}
	return host.Obj{{Key: "view_hash", Value: hash}}, nil
}

func routeGroup(spec map[string]any) (string, error) {
	groupV := spec["group"]
	if !truthy(groupV) {
		return "", nil
	}
	if !validGroup(groupV) {
		return "", crewErr("라우팅 그룹 ID를 확인해 주세요")
	}
	return groupV.(string), nil
}

// routedCall is the report of attempt i (ids[i]); a successful res also gets its usage.
func routedCall(spec, d map[string]any, ids []string, i int, attempt, res map[string]any) map[string]any {
	decision, replaced, classifier := d["decision"], "", int64(0)
	if i > 0 {
		decision, replaced = "failover", ids[i-1]
	} else {
		classifier, _ = pyIntArg(getOr(d, "classifier_ms", 0))
		if !truthy(d["classifier_ms"]) {
			classifier = 0
		}
	}
	code := ""
	if res["ok"] != true {
		code = routedErrorCode(sOr(res["error"]))
	} else {
		res["usage"] = usageOf(attempt, res)
	}
	call := map[string]any{"model": ids[i], "decision": decision, "replaced": replaced, "ok": res["ok"],
		"latency_ms": res["duration_ms"], "classifier_ms": classifier, "error": code}
	if truthy(spec["origin"]) {
		call["origin"] = spec["origin"]
		addUsage(call, res)
	}
	return call
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
