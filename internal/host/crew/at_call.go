package crew

// @handle calls: an expert answers once with its own model and instructions (an assist "try" the host runner picks up,
// the same path the desktop's 써보기 and Discord use); a team holds a meeting the engine drives. Both report like a
// workflow run, {run_id, status, summary, waiting}, so every harness calls them through run_workflow.

import (
	"fmt"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func callAt(handle, task string, wait float64, cwd string, resolver func(string) (map[string]any, error)) (map[string]any, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, crewErr("할 일(task)이 비었어요")
	}
	if resolver == nil {
		resolver = resolveDefault
	}
	h := "@" + strings.TrimLeft(strings.TrimSpace(handle), "@")
	res, err := resolver(h)
	if err != nil {
		return nil, apiErr(err)
	}
	item := obj(res["item"])
	c := obj(item["crew"])
	if cwd != "" {
		task += "\n\n작업 폴더: " + cwd
	}
	switch c["type"] {
	case "agent":
		return askExpert(h, task, wait)
	case "team":
		return holdMeeting(pyStr(item["id"]), task, wait)
	}
	return nil, crewErr(fmt.Sprintf("%s(이)라는 전문가나 팀을 찾지 못했어요", h))
}

// askExpert posts a try assist and waits for its answer; an expert that works by a workflow runs it (tryAgent).
func askExpert(handle, task string, wait float64) (map[string]any, error) {
	rec, err := apiPost("/v1/crew/assists", host.Obj{{Key: "scope", Value: scope()},
		{Key: "assist", Value: host.Obj{{Key: "kind", Value: "try"}, {Key: "expert", Value: handle}, {Key: "prompt", Value: clipRunes(task, 20000)}}}}, 15*time.Second)
	if err != nil {
		return nil, apiErr(err)
	}
	id := pyStr(rec["id"])
	return pollView(wait, rec, func() (map[string]any, error) {
		return apiGet("/v1/crew/assists/"+pyQuote(id, "")+"?"+scopeQuery(), 15*time.Second)
	}, func(r map[string]any) map[string]any {
		a := obj(r["crew_assist"])
		status := map[string]string{"done": "succeeded", "failed": "failed"}[pyStr(a["status"])]
		summary := pyStr(getOr(a, "output", ""))
		if status == "failed" {
			summary = pyStr(getOr(a, "error", ""))
		} else if status == "" {
			status = "running"
		}
		return map[string]any{"run_id": id, "status": status, "summary": summary, "waiting": nil}
	})
}

// holdMeeting asks for a meeting of the team on the task and waits for its conclusion.
func holdMeeting(teamID, task string, wait float64) (map[string]any, error) {
	title := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
	rec, err := apiPost("/v1/crew/meetings", host.Obj{{Key: "scope", Value: scope()}, {Key: "meeting", Value: host.Obj{
		{Key: "team_id", Value: teamID}, {Key: "title", Value: clipRunes(title, 120)}, {Key: "topic", Value: clipRunes(task, 20000)},
		{Key: "trigger", Value: "call"}}}}, 15*time.Second)
	if err != nil {
		return nil, apiErr(err)
	}
	id := pyStr(rec["id"])
	return pollView(wait, rec, func() (map[string]any, error) {
		return apiGet("/v1/crew/meetings/"+pyQuote(id, "")+"?"+scopeQuery(), 15*time.Second)
	}, func(r map[string]any) map[string]any { return meetingView(id, obj(r["crew_meeting"])) })
}

func meetingView(id string, m map[string]any) map[string]any {
	status := map[string]string{"concluded": "succeeded", "no_conclusion": "succeeded", "stopped": "stopped", "failed": "failed",
		"waiting": "waiting"}[pyStr(m["status"])]
	if status == "" {
		status = "running"
	}
	c := obj(m["conclusion"])
	lines := []string{}
	if s := strings.TrimSpace(pyStr(getOr(c, "summary", ""))); s != "" {
		lines = append(lines, s)
	}
	for _, part := range [][2]string{{"decisions", "결정"}, {"todos", "할 일"}, {"open_questions", "남은 질문"}} {
		for _, x := range list(c[part[0]]) {
			lines = append(lines, part[1]+": "+pyStr(x))
		}
	}
	if m["status"] == "no_conclusion" && len(lines) == 0 {
		lines = append(lines, "회의가 결론 없이 끝났어요")
	}
	if status == "waiting" {
		// a budget hold: only a person may let it go on, in the app
		lines = append(lines, "팀 회의가 예산 승인을 기다려요: Arkism 앱에서 확인해 주세요 ("+pyStr(getOr(obj(m["waiting"]), "reason", ""))+")")
	}
	if status == "failed" || status == "stopped" {
		lines = append(lines, "끝난 이유: "+pyStr(getOr(m, "ended_reason", "")))
	}
	return map[string]any{"run_id": id, "status": status, "summary": strings.Join(lines, "\n"), "waiting": nil}
}

// pollView reads the record until its view leaves running or wait seconds pass, like waitRun.
func pollView(wait float64, rec map[string]any, get func() (map[string]any, error), view func(map[string]any) map[string]any) (map[string]any, error) {
	end := runClock() + max(0, wait)
	for {
		v := view(rec)
		if v["status"] != "running" || runClock() >= end {
			return v, nil
		}
		runSleep(min(runPoll, max(0, end-runClock())))
		var err error
		if rec, err = get(); err != nil {
			return nil, apiErr(err)
		}
	}
}
