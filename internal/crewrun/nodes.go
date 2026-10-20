package crewrun

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
	"io"
	"maps"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Node config keys (all values are strings). Templates may use {{input}} (the
// node input: a string as is, anything else as JSON) and {{in.a.b}} (one value
// of the input by path).
//
//	start, schedule, webhook  pass the trigger input through.
//	ai         An agent CLI working with its tools: model (default the workflow's, then the engine's), effort
//	           (default medium), persona (persona record id), instructions, goal (template, default "{{input}}"),
//	           fence, done, cwd (template), timeout (default 20m), retries (0-3), access (read: look only;
//	           write, the default: edit files inside cwd, which it then needs; full: the CLI's no-limits mode).
//	           Runs RUNNER run-node --json with {model, effort, instructions, task, cwd, access, timeout_s, once, skills} on stdin and reads {ok, output,
//	           model, effort, cli, duration_ms, error}; ok=false fails the node. Output {output, model, effort,
//	           cli, duration_ms}. With two or more port names on its outgoing edges (back edges too) the
//	           task asks for a last "ROUTE: <port>" line, which picks the port and leaves the output.
//	           collab=on: workers (JSON [{name, model, instructions}]), max_workers (1-16, default 5),
//	           concurrency (1-16, default 3): a one-shot plan splits the goal, workers run it, and a last
//	           call sums up (and routes); output {output, workers: [{worker, task, ok, output}]}.
//	ai_once    One question, one answer: model, effort, prompt (template), persona, instructions,
//	           timeout (default 5m); routes like ai. Always runs with access read.
//	subflow    workflow (handle), task (template, default "{{input}}"). Runs that workflow here with input
//	           {"request": task}, at most three levels deep; output is its summary. A wait inside fails it.
//	code       lang (sh|go), code. A temp dir, 30 s, only PATH and HOME in the environment (go also
//	           GOTOOLCHAIN=local), input JSON on stdin, stdout as output (JSON if it parses, else text).
//	           sh runs "sh main.sh"; go runs "go run main.go" (a full package main). ARKISM_CREW_SANDBOX
//	           (e.g. "bwrap --ro-bind / / --dev /dev --bind {dir} {dir} --unshare-net") is put in front.
//	           An output object with a bool "ok" takes port "pass" or "fail".
//	http       method (default GET, POST when body is set), url, headers ("Name: value" lines), body
//	           (templates), key (id or name in provider-keys.json, sent as Authorization: Bearer), timeout.
//	           Response ≤ 1 MB; JSON if it parses, else text; status ≥ 400 fails.
//	github     args (JSON list or space-separated, each a template), repo_dir. Runs gh with no shell, 2 min.
//	approval   question (template). Waits until a person decides (POST /v1/crew/runs/{id}/decision);
//	           port "approve" or "reject"; output {approved, note}.
//	condition  expr (see EvalCondition). Port "yes" or "no"; output is the input.
//	merge      output is an object keyed by source node id.
//	loop       path (to a list in the input; empty means the input itself), concurrency (1-16, default 1).
//	           Runs the one node directly after it once per item (ai, ai_once, code, http, github, notify or
//	           only); that node and the loop both output the list of results. Only the
//	           immediate next node repeats.
//	wait       duration (Go duration, ≤ 30 days). The run waits (kind wait) and resumes after resume_at,
//	           also across engine restarts; output is the input.
//	notify     channel (telegram|slack|email), to (destination name in notify.json, default "default"),
//	           message (template, default the input as text).
//	end        summary (template, default the input as text). Finishes the run as succeeded.
//
// Every node may set access=read to run alongside other ready read nodes.
//
// Shared state: any template (not code, expr or control keys) may also read {{trigger}} / {{trigger.a.b}} (the run's
// input) and {{nodes.<id>}} / {{nodes.<id>.a.b}} (a finished node's output), not
// only what the incoming edges hand over. A node not done yet reads as empty.
func (e *Engine) execute(ctx context.Context, x *runExec, n Node, input any) (result, error) {
	c := x.shared(n.Config)
	if out, ok := x.fake(n, input); ok {
		return result{out: out}, nil
	}
	switch n.Type {
	case "start", "schedule", "webhook":
		return result{out: input}, nil
	case "ai", "ai_once":
		return e.runAI(ctx, x, n, input)
	case "subflow":
		out, err := e.runSubflow(ctx, c, input)
		return result{out: out}, err
	case "expert":
		return e.runExpert(ctx, x, n, input)
	case "team":
		out, err := e.runTeam(ctx, c, input)
		return result{out: out}, err
	case "n8n":
		out, err := e.runN8N(ctx, c, input)
		return result{out: out}, err
	case "code":
		out, err := e.runCode(ctx, c, input)
		port := ""
		if m, ok := out.(map[string]any); ok {
			if b, ok := m["ok"].(bool); ok {
				port = map[bool]string{true: "pass", false: "fail"}[b]
			}
		}
		return result{out: out, port: port}, err
	case "http":
		out, err := e.runHTTP(ctx, c, input)
		return result{out: out}, err
	case "github":
		out, err := e.runGitHub(ctx, c, input)
		return result{out: out}, err
	case "approval":
		q := c["question"]
		if q == "" {
			q = n.Title
		}
		if q == "" {
			q = "계속할까요?"
		}
		return result{wait: &Waiting{Kind: "approval", Question: clip(render(q, input), 2000)}}, nil
	case "condition":
		ok, err := EvalCondition(c["expr"], input)
		if err != nil {
			return result{}, failf("bad_expr", "%v", err)
		}
		port := "no"
		if ok {
			port = "yes"
		}
		return result{out: input, port: port}, nil
	case "merge":
		return result{out: input}, nil
	case "loop":
		return e.runLoop(ctx, x, n, input)
	case "wait":
		d, err := time.ParseDuration(c["duration"])
		if err != nil || d > 30*24*time.Hour {
			return result{}, failf("bad_config", "duration must be a Go duration up to 720h")
		}
		if d <= 0 {
			return result{out: input}, nil
		}
		at := e.now().UTC().Add(d).Truncate(time.Millisecond)
		return result{wait: &Waiting{Kind: "wait", ResumeAt: &at}}, nil
	case "notify":
		out, err := e.notify(ctx, c, input)
		return result{out: out}, err
	case "end":
		s := summaryOf(input)
		if f := cmp.Or(c["summary"], c["message"]); f != "" { // the editor saves "message"
			s = render(f, input)
		}
		return result{out: input, end: true, summary: clip(s, 4000)}, nil
	}
	return result{}, failf("unknown_node", "node type %q", n.Type)
}

// fake: a test run (시험 실행) does not run steps that reach outside: AI,
// code that may write, HTTP, GitHub, notify and subflows hand back a
// stand-in output and a wait does not wait. Branches, loops, merges,
// approvals and shared state run as usual.
func (x *runExec) fake(n Node, input any) (any, bool) {
	if x == nil || x.run.State.Trigger != "test" {
		return nil, false
	}
	switch n.Type {
	case "code":
		if n.Config["access"] == "read" {
			return nil, false
		}
	case "ai", "ai_once", "expert", "team", "n8n", "http", "github", "notify", "subflow":
	case "wait":
		return input, true
	default:
		return nil, false
	}
	return map[string]any{"test": true, "step": cmp.Or(n.Title, n.ID), "type": n.Type, "input": input}, true
}

var sharedRe = regexp.MustCompile(`\{\{\s*((?:nodes|trigger)(?:\.[^}\s]+)?)\s*\}\}`)

// sharedSkip are the config keys shared state never fills: the condition reads only
// its input, and code or control keys must not take text from outside (a trigger
// body pasted into a sh script runs as code; code reads its input on stdin).
var sharedSkip = map[string]bool{"expr": true, "code": true, "lang": true, "access": true, "key": true, "timeout": true}

// shared fills {{trigger…}} and {{nodes.<id>…}} in every config value but sharedSkip.
func (x *runExec) shared(c map[string]string) map[string]string {
	if x == nil {
		return c
	}
	var out map[string]string
	for k, v := range c {
		if sharedSkip[k] || !sharedRe.MatchString(v) {
			continue
		}
		if out == nil {
			out = maps.Clone(c)
		}
		out[k] = sharedRe.ReplaceAllStringFunc(v, func(m string) string {
			path := splitPath(sharedRe.FindStringSubmatch(m)[1])
			x.mu.Lock()
			defer x.mu.Unlock()
			if path[0] == "trigger" {
				return textOf(walkPath(decodeStored(x.run.State.Input), path[1:]))
			}
			if len(path) < 2 {
				return ""
			}
			st := x.state(path[1])
			if st == nil || st.Status != "done" {
				return ""
			}
			return textOf(walkPath(decodeStored(st.Output), path[2:]))
		})
	}
	if out == nil {
		return c
	}
	return out
}

var tmplRe = regexp.MustCompile(`\{\{\s*(input|in(?:\.[^}\s]+)?)\s*\}\}`)

func render(tpl string, input any) string {
	return tmplRe.ReplaceAllStringFunc(tpl, func(m string) string {
		name := tmplRe.FindStringSubmatch(m)[1]
		if name == "input" || name == "in" {
			return textOf(input)
		}
		return textOf(lookupPath(input, splitPath(name)))
	})
}

// taskText fills a step's goal: written in plain words with no {{input}}, the previous step's result is added after it,
// so nobody has to place variables by hand.
func taskText(tpl string, input any) string {
	if strings.TrimSpace(tpl) == "" {
		return render("{{input}}", input)
	}
	if tmplRe.MatchString(tpl) {
		return render(tpl, input)
	}
	if prev := strings.TrimSpace(textOf(input)); prev != "" {
		return tpl + "\n\nInput from the previous step:\n" + prev
	}
	return tpl
}

func durationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

func parseOutput(b []byte) any {
	t := bytes.TrimSpace(b)
	var v any
	if len(t) > 0 && json.Unmarshal(t, &v) == nil {
		return v
	}
	return string(t)
}

// capped keeps the first n bytes and drops the rest.
type capped struct {
	buf bytes.Buffer
	n   int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.n - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

type cmdSpec struct {
	args    []string
	dir     string
	env     []string
	stdin   []byte
	timeout time.Duration
}

// runCmd runs a command without a shell, in its own process group so a
// timeout kills its children too. stdout ≤ 1 MB; stderr only feeds a short
// error message.
func runCmd(ctx context.Context, s cmdSpec) ([]byte, error) {
	if len(s.args) == 0 {
		return nil, failf("bad_config", "no command")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.args[0], s.args[1:]...)
	cmd.Dir, cmd.Env = s.dir, s.env
	setProcessGroup(cmd)
	if s.stdin != nil {
		cmd.Stdin = bytes.NewReader(s.stdin)
	}
	out, errb := &capped{n: 1 << 20}, &capped{n: 4096}
	cmd.Stdout, cmd.Stderr = out, errb
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.buf.Bytes(), failf("timeout", "stopped after %s", s.timeout)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.buf.Bytes(), failf("exit", "exit %d: %s", ee.ExitCode(), clip(strings.TrimSpace(errb.buf.String()), 200))
		}
		return out.buf.Bytes(), failf("exec", "%s could not start", filepath.Base(s.args[0]))
	}
	return out.buf.Bytes(), nil
}

// aiSpec is what RUNNER run-node reads on stdin.
type aiSpec struct {
	Model        string   `json:"model"`
	Effort       string   `json:"effort"`
	Instructions string   `json:"instructions"`
	Task         string   `json:"task"`
	CWD          string   `json:"cwd,omitempty"`
	Access       string   `json:"access,omitempty"`
	TimeoutS     int      `json:"timeout_s"`
	Once         bool     `json:"once"`
	Skills       []string `json:"skills,omitempty"` // hub skill ids the runner puts into the instructions
	// Origin tags the runner's routing call (meetings: kind, meeting_id, team_id, agent_id, trigger).
	Origin map[string]string `json:"origin,omitempty"`
}

// aiUsage is run-node's estimated token use of one call.
type aiUsage struct {
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	Estimated    bool     `json:"estimated"`
}

// tokens is the call's input + output tokens, 0 when the runner did not say.
func (a aiAnswer) tokens() int64 {
	if a.Usage == nil {
		return 0
	}
	return a.Usage.InputTokens + a.Usage.OutputTokens
}

type aiAnswer struct {
	OK     bool     `json:"ok"`
	Output string   `json:"output"`
	Model  string   `json:"model"`
	Effort string   `json:"effort"`
	CLI    string   `json:"cli"`
	Ms     int64    `json:"duration_ms"`
	Error  string   `json:"error"`
	Usage  *aiUsage `json:"usage,omitempty"`
	Rules  string   `json:"rules,omitempty"` // short sha256 of the shared record rules the runner added
}

// callAI runs one AI step, again up to retries times when it fails. The
// runner stops its CLI (and the CLI's children) a little before we would
// stop the runner, so nothing keeps working after the node failed.
func (e *Engine) callAI(ctx context.Context, s aiSpec, limit time.Duration, retries int) (aiAnswer, error) {
	s.TimeoutS = max(1, int((limit-5*time.Second)/time.Second))
	in, _ := json.Marshal(s)
	args := append(append([]string{}, e.Runner...), "run-node", "--json")
	var err error
	for try := 0; try <= retries && ctx.Err() == nil; try++ {
		var out []byte
		out, err = runCmd(ctx, cmdSpec{args: args, env: os.Environ(), stdin: in, timeout: limit})
		var res aiAnswer
		switch {
		case json.Unmarshal(bytes.TrimSpace(out), &res) != nil:
			if err == nil {
				err = failf("ai_output", "실행기가 JSON으로 답하지 않았어요")
			}
		case !res.OK:
			err = failf("ai_failed", "%s", clip(res.Error, 300))
		default:
			return res, nil
		}
	}
	return aiAnswer{}, err
}

func intIn(s string, lo, hi, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return max(lo, min(n, hi))
}

func joinText(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n\n")
}

// instructions: the persona's text, the node's own, its fence and done rule.
func (e *Engine) instructions(c map[string]string) (string, error) {
	persona := ""
	if id := strings.TrimSpace(c["persona"]); id != "" {
		it, ok := e.item(id)
		if !ok || it.Crew.Type != "persona" {
			return "", failf("bad_config", "페르소나 %s를 찾지 못했어요", id)
		}
		persona = it.Crew.Instructions
	}
	fence, done := "", ""
	if f := strings.TrimSpace(c["fence"]); f != "" {
		fence = "울타리: " + f
	}
	if d := strings.TrimSpace(c["done"]); d != "" {
		done = "완료 기준: " + d
	}
	return joinText(persona, c["instructions"], fence, done), nil
}

// ports are the distinct port names on a node's outgoing edges, back edges too.
func (x *runExec) ports(id string) []string {
	var out []string
	for _, ed := range append(append([]Edge{}, x.out[id]...), x.back[id]...) {
		if ed.Port != "" && !slices.Contains(out, ed.Port) {
			out = append(out, ed.Port)
		}
	}
	return out
}

// routeAsk is what an AI step's task ends with when it must pick a port.
func routeAsk(ports []string) string {
	if len(ports) < 2 {
		return ""
	}
	return "\n\n마지막 줄에 `ROUTE: <이름>` 으로 다음 중 하나를 골라 적어: " + strings.Join(ports, ", ")
}

// routePick takes the answer's last "ROUTE:" line out and returns the rest
// and the port it names; a missing or unknown name takes def.
func routePick(answer string, ports []string, def string) (string, string) {
	if len(ports) < 2 {
		return answer, ""
	}
	lines := strings.Split(answer, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		name, ok := strings.CutPrefix(strings.Trim(strings.TrimSpace(lines[i]), "*_`"), "ROUTE:")
		if !ok {
			continue
		}
		rest := strings.TrimSpace(strings.Join(append(lines[:i:i], lines[i+1:]...), "\n"))
		if name = strings.Trim(strings.TrimSpace(name), "*_`'\"<>"); slices.Contains(ports, name) {
			return rest, name
		}
		return rest, def
	}
	return answer, def
}

func (e *Engine) runAI(ctx context.Context, x *runExec, n Node, input any) (result, error) {
	c := n.Config
	instr, err := e.instructions(c)
	if err != nil {
		return result{}, err
	}
	s := aiSpec{Model: cmp.Or(c["model"], x.item.Crew.Model, e.DefaultModel), Effort: cmp.Or(c["effort"], "medium"), Instructions: instr,
		CWD: expandHome(render(c["cwd"], input)), Access: c["access"], Once: n.Type == "ai_once", Skills: skillList(c["skills"])}
	limit := durationOr(c["timeout"], 20*time.Minute)
	s.Task = taskText(c["goal"], input)
	if s.Once {
		limit, s.Task = durationOr(c["timeout"], 5*time.Minute), taskText(c["prompt"], input)
	}
	retries := intIn(c["retries"], 0, 3, 0)
	ports, def := x.ports(n.ID), ""
	if len(x.out[n.ID]) > 0 {
		def = x.out[n.ID][0].Port
	}
	if !s.Once && c["collab"] == "on" {
		return e.runCollab(ctx, c, s, limit, retries, ports, def)
	}
	s.Task += routeAsk(ports)
	res, err := e.callAI(ctx, s, limit, retries)
	if err != nil {
		return result{}, err
	}
	text, port := routePick(res.Output, ports, def)
	out := map[string]any{"output": text, "model": res.Model, "effort": res.Effort, "cli": res.CLI, "duration_ms": res.Ms}
	if res.Rules != "" {
		out["rules"] = res.Rules
	}
	return result{out: out, port: port, tokens: res.tokens()}, nil
}

type collabWorker struct {
	Expert       string `json:"expert,omitempty"` // a saved expert: its model and instructions are the defaults
	Name         string `json:"name"`
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
}

type collabResult struct {
	Worker string `json:"worker"`
	Task   string `json:"task"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// runCollab plans with one short call, runs the workers, and sums up.
func (e *Engine) runCollab(ctx context.Context, c map[string]string, s aiSpec, limit time.Duration, retries int, ports []string, def string) (result, error) {
	var workers []collabWorker
	if json.Unmarshal([]byte(c["workers"]), &workers) != nil || len(workers) == 0 {
		return result{}, failf("bad_config", "협업에는 workers(JSON 배열)가 필요해요")
	}
	most, conc := intIn(c["max_workers"], 1, 16, 5), intIn(c["concurrency"], 1, 16, 3)
	var names []string
	for i, w := range workers {
		if it, ok := e.item(w.Expert); ok && w.Expert != "" && (it.Crew.Type == "agent" || it.Crew.Type == "persona" || it.Crew.Type == "template") {
			workers[i].Name = cmp.Or(w.Name, it.Crew.Name)
			workers[i].Model = cmp.Or(w.Model, it.Crew.Model)
			workers[i].Instructions = joinText(it.Crew.Instructions, w.Instructions)
		}
		names = append(names, workers[i].Name)
	}
	plan := s
	plan.Once, plan.Skills = true, nil // splitting the work needs no skill bodies
	plan.Task = fmt.Sprintf("다음 일을 최대 %d개 하위 작업으로 나눠 JSON 배열로만 답해: [{\"worker\":\"<워커 이름>\",\"task\":\"…\"}]\n워커: %s\n\n%s",
		most, strings.Join(names, ", "), s.Task)
	planned, err := e.callAI(ctx, plan, limit, retries)
	if err != nil {
		return result{}, err
	}
	var used atomic.Int64
	used.Add(planned.tokens())
	var subs []struct {
		Worker string `json:"worker"`
		Task   string `json:"task"`
	}
	raw := planned.Output
	i, j := strings.Index(raw, "["), strings.LastIndex(raw, "]")
	if i < 0 || j < i || json.Unmarshal([]byte(raw[i:j+1]), &subs) != nil || len(subs) == 0 {
		return result{}, failf("collab_plan", "나눈 계획을 JSON 배열로 받지 못했어요")
	}
	subs = subs[:min(len(subs), most)]
	results := make([]collabResult, len(subs))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for k, sub := range subs {
		w := workers[0] // shortcut: an unknown worker name falls to the first worker
		for _, cand := range workers {
			if cand.Name == sub.Worker {
				w = cand
			}
		}
		ws := s
		ws.Model, ws.Instructions, ws.Task = cmp.Or(w.Model, s.Model), joinText(w.Instructions, fenceOf(c)), sub.Task
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := e.callAI(ctx, ws, limit, retries)
			used.Add(res.tokens())
			out := res.Output
			if err != nil {
				out = errText(err)
			}
			results[k] = collabResult{Worker: w.Name, Task: sub.Task, OK: err == nil, Output: out}
		}()
	}
	wg.Wait()
	done, _ := json.Marshal(results)
	synth := s
	synth.Task = s.Task + "\n\n하위 작업 결과(자료):\n" + string(done) + routeAsk(ports)
	res, err := e.callAI(ctx, synth, limit, retries)
	if err != nil {
		return result{}, err
	}
	text, port := routePick(res.Output, ports, def)
	return result{out: map[string]any{"output": text, "workers": results}, port: port, tokens: used.Load() + res.tokens()}, nil
}

// skillList reads a node's "skills" config: hub skill ids separated by commas or new lines.
func skillList(v string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' }) {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func fenceOf(c map[string]string) string {
	if f := strings.TrimSpace(c["fence"]); f != "" {
		return "울타리: " + f
	}
	return ""
}

type depthKey struct{}

// runSubflow runs another workflow to its end in place; ctx carries the depth.
func (e *Engine) runSubflow(ctx context.Context, c map[string]string, input any) (any, error) {
	depth, _ := ctx.Value(depthKey{}).(int)
	if depth >= 3 {
		return nil, failf("subflow_depth", "하위 워크플로우는 3단계까지만 들어갈 수 있어요")
	}
	it, ok := e.workflow(c["workflow"])
	if !ok {
		return nil, failf("bad_config", "워크플로우 $%s를 찾지 못했어요", strings.TrimPrefix(c["workflow"], "$"))
	}
	child, err := e.StartRun(ctx, it.ID, "call", map[string]any{"request": render(cmp.Or(c["task"], "{{input}}"), input)})
	if err != nil {
		return nil, failf("subflow", "하위 실행을 만들지 못했어요")
	}
	if err := e.Drive(context.WithValue(ctx, depthKey{}, depth+1), child.ID); err != nil {
		return nil, err
	}
	got, err := e.Store.GetRun(ctx, child.ID)
	if err != nil {
		return nil, err
	}
	switch got.State.Status {
	case "succeeded":
		return got.State.Summary, nil
	case "waiting":
		cx := &runExec{e: e, run: got}
		if err := cx.finish(ctx, "failed", "subflow_waiting: 다른 워크플로우가 부른 실행은 기다릴 수 없어요", ""); err != nil {
			return nil, err
		}
		return nil, failf("subflow_waiting", "%s이(가) 기다리는 단계(승인·기다리기·콜백)에서 멈췄어요", it.Crew.Name)
	}
	return nil, failf("subflow_failed", "%s: %s", it.Crew.Name, clip(got.State.Summary, 300))
}

func (e *Engine) runCode(ctx context.Context, c map[string]string, input any) (any, error) {
	lang, code := c["lang"], c["code"]
	if strings.TrimSpace(code) == "" || lang != "sh" && lang != "go" {
		return nil, failf("bad_config", "code needs lang sh|go and code")
	}
	dir, err := os.MkdirTemp("", "arkism-crew-code-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	home, _ := os.UserHomeDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	var args []string
	if lang == "sh" {
		err = os.WriteFile(filepath.Join(dir, "main.sh"), []byte(code), 0o600)
		args = []string{"sh", "main.sh"}
	} else {
		err = os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0o600)
		args = []string{"go", "run", "main.go"}
		env = append(env, "GOTOOLCHAIN=local")
	}
	if err != nil {
		return nil, err
	}
	var sandbox []string
	for _, a := range e.Sandbox {
		sandbox = append(sandbox, strings.ReplaceAll(a, "{dir}", dir))
	}
	timeout := e.CodeTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	// config.timeout (Go duration) lets a node run longer than the 30 s default, up to 2 h.
	timeout = min(durationOr(c["timeout"], timeout), 2*time.Hour)
	in, _ := json.Marshal(input)
	out, err := runCmd(ctx, cmdSpec{args: append(sandbox, args...), dir: dir, env: env, stdin: in, timeout: timeout})
	if err != nil {
		return nil, err
	}
	return parseOutput(out), nil
}

// readLimited reads at most 1 MB and fails beyond it.
func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, 1<<20+1))
	if err != nil {
		return nil, failf("read", "response read failed")
	}
	if len(b) > 1<<20 {
		return nil, failf("too_large", "response over 1 MB")
	}
	return b, nil
}

func (e *Engine) runHTTP(ctx context.Context, c map[string]string, input any) (any, error) {
	raw := render(c["url"], input)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, failf("bad_config", "url must be http(s)")
	}
	body := render(c["body"], input)
	method := strings.ToUpper(strings.TrimSpace(c["method"]))
	if method == "" {
		method = "GET"
		if body != "" {
			method = "POST"
		}
	}
	limit := durationOr(c["timeout"], 60*time.Second)
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return nil, failf("bad_config", "bad method")
	}
	for _, line := range strings.Split(render(c["headers"], input), "\n") {
		name, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		req.Header.Set(textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name)), strings.TrimSpace(val))
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if name := c["key"]; name != "" {
		k, err := lookupKey(e.KeysFile, name)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := e.httpClientFor(limit).Do(req)
	if err != nil {
		return nil, failf("http", "request to %s failed", u.Host)
	}
	defer resp.Body.Close()
	b, err := readLimited(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, failf("http_status", "%s answered %d", u.Host, resp.StatusCode)
	}
	return parseOutput(b), nil
}

func (e *Engine) runGitHub(ctx context.Context, c map[string]string, input any) (any, error) {
	var args []string
	spec := strings.TrimSpace(c["args"])
	if strings.HasPrefix(spec, "[") {
		if json.Unmarshal([]byte(spec), &args) != nil {
			return nil, failf("bad_config", "args is not a JSON list")
		}
	} else {
		args = strings.Fields(spec)
	}
	if len(args) == 0 {
		return nil, failf("bad_config", "args missing")
	}
	for i, a := range args {
		args[i] = render(a, input)
	}
	gh := e.GH
	if gh == "" {
		gh = "gh"
	}
	out, err := runCmd(ctx, cmdSpec{args: append([]string{gh}, args...), dir: expandHome(c["repo_dir"]), env: os.Environ(), timeout: 2 * time.Minute})
	if err != nil {
		return nil, err
	}
	return parseOutput(out), nil
}

var loopBodies = map[string]bool{"ai": true, "ai_once": true, "code": true, "http": true, "github": true, "notify": true}

func (e *Engine) runLoop(ctx context.Context, x *runExec, n Node, input any) (result, error) {
	next := x.out[n.ID]
	if len(next) != 1 {
		return result{}, failf("bad_config", "loop needs exactly one next node")
	}
	body := x.nodes[next[0].To]
	if !loopBodies[body.Type] {
		return result{}, failf("bad_config", "a loop cannot repeat a %s node", body.Type)
	}
	items, ok := lookupPath(input, splitPath(n.Config["path"])).([]any)
	if !ok {
		return result{}, failf("not_a_list", "nothing to loop over at %q", n.Config["path"])
	}
	if len(items) > 1000 {
		return result{}, failf("too_many", "%d items (max 1000)", len(items))
	}
	conc, _ := strconv.Atoi(n.Config["concurrency"])
	conc = max(1, min(conc, 16))
	results := make([]any, len(items))
	errs := make([]error, len(items))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := e.execute(ctx, x, body, item)
			results[i], errs[i] = r.out, err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return result{}, failf("loop_item", "item %d: %s", i, errText(err))
		}
	}
	return result{out: results, also: map[string]any{body.ID: results}}, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// readPrivateFile reads a host secret file and refuses one others can read.
func readPrivateFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// Windows keeps access in ACLs, not mode bits (every file reads as 0666 there).
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must be chmod 600", filepath.Base(path))
	}
	return os.ReadFile(path)
}

func readSecretFile(path string) ([]byte, error) { return os.ReadFile(path) }

func lookupKey(path, name string) (string, error) {
	raw, err := readPrivateFile(path)
	if err != nil {
		return "", failf("key", "provider keys unavailable (missing, or not chmod 600)")
	}
	var f struct {
		Keys []map[string]any `json:"keys"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return "", failf("key", "provider keys file unreadable")
	}
	keystore.Resolve(f.Keys) // keys kept in the OS credential store come back here
	for _, k := range f.Keys {
		id, _ := k["id"].(string)
		n, _ := k["name"].(string)
		key, _ := k["key"].(string)
		if (id == name || n == name) && key != "" {
			return key, nil
		}
	}
	return "", failf("key", "no provider key named %q", name)
}
