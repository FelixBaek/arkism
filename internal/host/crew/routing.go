package crew

// 라우팅 (model routing): Arkism picks the model; a failed pick hands the task to the fallbacks.

import (
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

// decideRoute is {id, model, provider, decision, classifier_ms, fallbacks} from Arkism 라우팅.
func decideRoute(task, group string) (map[string]any, error) {
	body := host.Obj{{Key: "scope", Value: scope()}, {Key: "task", Value: task}}
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
	decideFunc func(task, group string) (map[string]any, error)
	runFn      func(spec map[string]any) (map[string]any, error)
	reportFunc func(call map[string]any)
)

// workHookContext reads a saved work checkpoint as reference data (a fake in tests).
var workHookContext = func(workID string, request host.Requester, scope map[string]any) (string, error) {
	return host.WorkHookContext(workID, request, scope, 8000, nil)
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
		decide = host.DecideRoute
	}
	if run == nil {
		run = func(s map[string]any) (map[string]any, error) { return runNodeFn(s) }
	}
	if report == nil {
		report = host.ReportCall
	}
	groupV := spec["group"]
	group := ""
	if truthy(groupV) {
		if !validGroup(groupV) {
			return nil, crewErr("라우팅 그룹 ID를 확인해 주세요")
		}
		group = groupV.(string)
	}
	d, err := decide(task, group)
	if err != nil {
		return nil, err
	}
	if group != "" && d["group"] != group {
		return nil, crewErr("요청한 그룹의 라우팅 결과를 확인할 수 없어요")
	}
	if workID := spec["work_id"]; truthy(workID) {
		// Read once for this execution so every fallback receives the same checkpoint as data.
		context, err := workHookContext(pyStr(workID), requester, host.Scope())
		if err != nil {
			return nil, crewErr("선택한 작업 상태를 읽지 못해 실행하지 않았어요")
		}
		spec = withKey(spec, "task", task+"\n\n"+context)
	}
	first, ok := d["id"].(string)
	if !ok {
		return nil, crewErr("라우팅이 모델을 고르지 못했어요 (Arkism 라우팅 설정을 확인해 주세요)")
	}
	ids := []string{first}
	for _, f := range list(d["fallbacks"]) {
		ids = append(ids, pyStr(f))
	}
	var res map[string]any
	var i int
	var mid string
	for i, mid = range ids {
		provider, model, found := strings.Cut(mid, "/")
		if !found {
			return nil, crewErr("라우팅이 모델을 고르지 못했어요 (Arkism 라우팅 설정을 확인해 주세요)")
		}
		attempt := withKey(withKey(spec, "model", model), "provider", provider)
		res, err = run(attempt)
		if err != nil {
			if !isCrewErr(err) {
				return nil, err
			}
			res = result(false, "", model, "", "", time.Time{}, err.Error())
		}
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
		call := map[string]any{"model": mid, "decision": decision, "replaced": replaced, "ok": res["ok"],
			"latency_ms": res["duration_ms"], "classifier_ms": classifier, "error": code}
		if truthy(spec["origin"]) {
			call["origin"] = spec["origin"]
			addUsage(call, res)
		}
		report(call)
		if res["ok"] == true {
			break
		}
	}
	decision := d["decision"]
	if i > 0 {
		decision = "failover"
	}
	routed := host.Obj{{Key: "id", Value: mid}, {Key: "decision", Value: decision}, {Key: "tried", Value: toAny(ids[:i+1])}}
	if group != "" {
		routed = append(routed, host.KV{Key: "group", Value: group})
	}
	res["routed"] = routed
	return res, nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
