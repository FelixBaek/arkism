package crew

// One step on an agent-runner connection (backends.go): the policy checks before anything is sent, the
// arkism.backend.v1 start/status/cancel calls, and a per-step ledger so a restart asks the runner about the task it
// already started instead of starting another. Nothing here re-runs a task: a start whose answer is lost, a runner
// that forgets a task or a timeout ends the step as failed or unknown, and the owner decides.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const backendProtocol = "arkism.backend.v1"

const (
	backendResultChars = 20000   // what an answer may carry on to Arkism, like a CLI step's output
	backendReplyBytes  = 1 << 20 // a larger reply is refused, not read
	backendStartTries  = 3       // only for a runner that dedupes by Idempotency-Key
)

var (
	backendPoll     = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second} // then the last one
	backendTimeout  = 4 * time.Hour                                                       // a step without timeout_s
	backendStartMax = 30 * time.Second
	backendNow      = time.Now
	backendSleep    = func(ctx context.Context, d time.Duration) {
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
	}
)

// backendState rank: a reply never moves a task back (a late "accepted" after "running" is ignored).
var backendRank = map[string]int{"starting": 0, "accepted": 1, "running": 2, "waiting_approval": 2, "cancel_requested": 3,
	"succeeded": 4, "failed": 4, "cancelled": 4, "unknown": 4}

func terminalState(s string) bool { return backendRank[s] == 4 }

// backendLabel is what a runner's task id or model name may look like before Arkism keeps it (the server's trace rule).
var backendLabel = regexp.MustCompile(`^[A-Za-z0-9._:@/ -]{1,128}$`)

// ---------------------------------------------------------------- wire

type backendReply struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Result *struct {
		Text string `json:"text"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Model string `json:"model"`
	Usage *struct {
		InputTokens  *int64   `json:"input_tokens"`
		OutputTokens *int64   `json:"output_tokens"`
		CostUSD      *float64 `json:"cost_usd"`
	} `json:"usage"`
}

// backendHTTPError is a runner's non-2xx answer; Code 0 means no answer (network, timeout, unreadable reply).
type backendHTTPError struct {
	Code int
	Msg  string
}

func (e *backendHTTPError) Error() string {
	if e.Code == 0 {
		return e.Msg
	}
	return fmt.Sprintf("러너가 %d로 답했어요", e.Code)
}

// backendCall sends one request with the connection's own token (never Arkism's) and decodes a 2xx JSON reply.
func backendCall(ctx context.Context, b *backend, method, path string, body any, idemKey string, out any) (int, error) {
	u, err := checkBackendURL(b.URL, b.Local)
	if err != nil {
		return 0, err
	}
	token, err := b.token()
	if err != nil {
		return 0, err
	}
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String()+path, reader)
	if err != nil {
		return 0, crewErr("연결 주소가 올바르지 않아요")
	}
	req.Header.Set("User-Agent", providerAgent)
	req.Header.Set("Accept", jsonType)
	if body != nil {
		req.Header.Set("Content-Type", jsonType)
	}
	if token != "" {
		req.Header.Set("Authorization", bearer+token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := backendClient(b.Local).Do(req)
	if err != nil {
		var ce *host.CrewError
		if errors.As(err, &ce) { // the dialler refused the address
			return 0, ce
		}
		return 0, &backendHTTPError{Msg: "러너에 닿지 못했어요"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, backendReplyBytes+1))
	if err != nil {
		return resp.StatusCode, &backendHTTPError{Msg: "러너의 답을 끝까지 읽지 못했어요"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &backendHTTPError{Code: resp.StatusCode}
	}
	if len(raw) > backendReplyBytes {
		return resp.StatusCode, &backendHTTPError{Msg: "러너의 답이 1 MiB를 넘어요"}
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return resp.StatusCode, &backendHTTPError{Msg: "러너의 답이 JSON이 아니에요"}
	}
	return resp.StatusCode, nil
}

// ---------------------------------------------------------------- ledger

// taskEntry links an Arkism step (key) to the runner's task; one 0600 file per key, written before every call that
// could start or change the task. It holds ids and states only, never the task text or its answer.
type taskEntry struct {
	Key     string `json:"key"`
	Backend string `json:"backend"`
	TaskID  string `json:"task_id,omitempty"`
	State   string `json:"state"`
	Started string `json:"started_at"`
	Updated string `json:"updated_at"`
}

func ledgerPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(stateFile("backend-tasks"), hex.EncodeToString(sum[:16])+".json")
}

func readEntry(key string) *taskEntry {
	raw, err := os.ReadFile(ledgerPath(key))
	if err != nil {
		return nil
	}
	var e taskEntry
	if json.Unmarshal(raw, &e) != nil || e.Key != key {
		return nil
	}
	return &e
}

func (e *taskEntry) save() error {
	e.Updated = backendNow().UTC().Format(time.RFC3339)
	raw, _ := json.Marshal(e)
	return writePrivate(ledgerPath(e.Key), raw)
}

// ---------------------------------------------------------------- one step

// backendStep is one step's request and what is learned about it on the way.
type backendStep struct {
	b          *backend
	caps       map[string]bool
	key        string
	body       map[string]any
	trace      map[string]any
	entry      *taskEntry
	deadline   time.Time
	effort     string
	requested  string
	started    time.Time
	notApplied []string
	onAccept   func(map[string]any)
}

// runBackend is runNode for a step whose spec names a connection (spec.backend). Policy refusals are crew errors
// (nothing was sent); everything after the start call is a result, ok or not, carrying the trace under "backend".
func runBackend(spec map[string]any) (map[string]any, error) {
	st, err := prepareBackend(spec)
	if err != nil {
		return nil, err
	}
	run := obj(spec["run"])
	st.entry = readEntry(st.key)
	switch {
	case st.entry != nil && st.entry.TaskID != "":
		return st.follow(), nil // a restart or a resumed step: ask about the task already started
	case st.entry != nil && terminalState(st.entry.State):
		return st.end(st.entry.State, "", "이 단계는 이미 끝났어요 ("+st.entry.State+"): 다시 보내지 않았어요"), nil
	case st.entry != nil && !st.caps["idempotency"]:
		return st.end("unknown", "", "이전 시작 요청의 답을 받지 못했어요: 작업이 시작됐을 수 있어 다시 보내지 않았어요"), nil
	case st.entry == nil && sOr(run["task_id"]) != "":
		st.entry = &taskEntry{Key: st.key, Backend: st.b.Name, TaskID: sOr(run["task_id"]), State: "accepted", Started: nowZ()}
		return st.follow(), nil // the server's record knows the task this host's ledger lost
	case st.entry == nil && run["resume"] == "1":
		return st.end("unknown", "", "재시작 뒤 이 작업을 확인할 수 없어요: 다시 돌리지 않았어요"), nil
	}
	if st.entry == nil {
		st.entry = &taskEntry{Key: st.key, Backend: st.b.Name, State: "starting", Started: nowZ()}
	}
	if err := st.entry.save(); err != nil {
		return nil, err // without the ledger a restart could start it twice: do not start
	}
	return st.start(), nil
}

// prepareBackend loads the connection, asks for its capabilities and checks the step against them.
func prepareBackend(spec map[string]any) (*backendStep, error) {
	name := strings.TrimSpace(sOr(spec["backend"]))
	all, err := loadBackends()
	if err != nil {
		return nil, err
	}
	b := all[name]
	if b == nil {
		return nil, crewErr("외부 연결 " + name + "이(가) 이 컴퓨터에 없어요 (arkism crew backends list)")
	}
	task := strings.TrimSpace(sOr(spec["task"]))
	if task == "" {
		return nil, crewErr("할 일(task)이 비었어요")
	}
	effort, timeout, err := nodeLimits(spec)
	if err != nil {
		return nil, err
	}
	if !truthy(spec["timeout_s"]) {
		timeout = backendTimeout
	}
	access, err := nodeAccess(spec)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendStartMax)
	defer cancel()
	caps, err := fetchCaps(ctx, b)
	if err != nil {
		return nil, crewErr("외부 연결 " + name + "을(를) 확인하지 못해 보내지 않았어요: " + err.Error())
	}
	st := &backendStep{b: b, caps: caps, effort: effort, started: backendNow(), deadline: backendNow().Add(timeout)}
	if err := st.policy(spec, access); err != nil {
		return nil, err
	}
	return st, st.request(spec, task, access, timeout)
}

// request builds the start body and the trace; it refuses instructions or skills a runner cannot take apart from the task.
func (st *backendStep) request(spec map[string]any, task, access string, timeout time.Duration) error {
	name, caps := st.b.Name, st.caps
	instructions := strings.TrimSpace(sOr(spec["instructions"]))
	skillIDs := stringList(spec["skills"])
	skills, err := skillText(skillIDs)
	if err != nil {
		return err
	}
	if (instructions != "" || skills != "") && !caps["instructions"] {
		return crewErr("외부 연결 " + name + "은(는) 지시를 따로 받지 못해요: 지시나 스킬이 있는 단계는 보내지 않아요")
	}
	context := []string{}
	if instructions != "" {
		context = append(context, "instructions")
	}
	for _, id := range skillIDs {
		context = append(context, "skill:"+clipRunes(id, 100))
	}
	run := obj(spec["run"])
	st.key = sOr(run["key"])
	if st.key == "" {
		st.key = "adhoc/" + randHex(16) // no run to resume: a fresh key each call
	}
	st.onAccept, _ = spec["on_accept"].(func(map[string]any))
	format := "text"
	if f := sOr(spec["output_format"]); f == "json" || f == "markdown" {
		format = f
	}
	st.body = map[string]any{"protocol": backendProtocol,
		"run": map[string]any{"id": sOr(run["id"]), "key": st.key, "kind": sOr(run["kind"]), "expert_id": sOr(run["expert_id"]),
			"workflow_id": sOr(run["workflow_id"]), "step_id": sOr(run["step_id"])},
		"task": task, "instructions": instructions, "skill_documents": skills,
		"model":   map[string]any{"requested": st.requested, "must_apply": st.requested != "" && caps["model_choice"]},
		"access":  map[string]any{"mode": access, "enforce": caps["tool_restriction"]},
		"limits":  map[string]any{"timeout_s": int(timeout.Seconds())},
		"result":  map[string]any{"format": format, "max_chars": backendResultChars},
		"session": "none", "approval": "none"}
	usage := "unknown"
	if caps["usage"] {
		usage = "reported"
	}
	st.trace = map[string]any{"backend": name, "state": "starting", "requested_model": st.requested, "not_applied": st.notApplied,
		"context": context, "usage": usage, "fallback": false}
	return nil
}

// policy refuses what the runner cannot honour before anything is sent; settings it may skip are listed.
func (st *backendStep) policy(spec map[string]any, access string) error {
	name := st.b.Name
	if access != "read" {
		return crewErr("외부 연결 " + name + "에는 읽기 전용 단계만 보내요 (이 컴퓨터의 작업 폴더를 고칠 수 없어요)")
	}
	if !st.caps["tool_restriction"] && !st.b.Isolated {
		return crewErr("외부 연결 " + name + "은(는) 읽기 전용을 지키지 못해요: 격리된 러너면 --isolated로 다시 만들어 주세요")
	}
	if !st.caps["tool_restriction"] {
		st.notApplied = append(st.notApplied, "tool_restriction")
	}
	model := strings.TrimSpace(sOr(spec["model"]))
	switch {
	case model == "" || strings.EqualFold(model, "auto") || strings.HasPrefix(model, "group/"):
		if model != "" {
			st.notApplied = append(st.notApplied, "auto_routing") // Clef does not pick for an external runner
		}
	case st.caps["model_choice"]:
		st.requested = model
	case st.b.OwnModel:
		st.requested = model
		st.notApplied = append(st.notApplied, "model")
	default:
		return crewErr("외부 연결 " + name + "은(는) 모델을 고를 수 없어요: 전문가의 모델(" + model + ")을 지울지, 연결을 --own-model로 만들지 정해 주세요")
	}
	if strings.TrimSpace(sOr(spec["cwd"])) != "" {
		st.notApplied = append(st.notApplied, "cwd")
	}
	return nil
}

// start sends the task once (again only to a runner that dedupes by the key) and follows it when accepted.
func (st *backendStep) start() map[string]any {
	var reply backendReply
	var err error
	code := 0
	tries := 1
	if st.caps["idempotency"] {
		tries = backendStartTries
	}
	for try := 0; try < tries; try++ {
		ctx, cancel := context.WithTimeout(context.Background(), min(backendStartMax, time.Until(st.deadline)))
		code, err = backendCall(ctx, st.b, http.MethodPost, "/v1/tasks", st.body, st.key, &reply)
		cancel()
		var he *backendHTTPError
		if err == nil || !errors.As(err, &he) || he.Code != 0 && he.Code < 500 || backendNow().After(st.deadline) {
			break
		}
		backendSleep(context.Background(), time.Duration(try+1)*time.Second)
	}
	if err != nil {
		var he *backendHTTPError
		switch {
		case !errors.As(err, &he):
			return st.end("failed", "", err.Error()) // refused before it was sent (address policy, token)
		case code == 401 || code == 403:
			return st.end("failed", "auth", "외부 연결이 인증을 거절했어요 (토큰을 확인해 주세요)")
		case code == 409:
			return st.end("unknown", "", "러너가 같은 키의 다른 요청이라며 거절했어요: 다시 보내지 않았어요")
		case code >= 400 && code < 500:
			return st.end("failed", "rejected", fmt.Sprintf("러너가 작업을 받지 않았어요 (%d)", code))
		}
		return st.end("unknown", "", "시작 요청의 답을 받지 못했어요: 작업이 시작됐을 수 있어 다시 보내지 않았어요")
	}
	return st.apply(reply, true)
}

// follow asks the runner about the task until it ends, the step's time runs out, or the step is stopped.
func (st *backendStep) follow() map[string]any {
	st.trace["task_id"] = st.entry.TaskID
	st.trace["state"] = st.entry.State
	if !st.caps["status"] {
		return st.end("unknown", "", "러너가 상태 조회를 못 해서 결과를 확인할 수 없어요: 다시 돌리지 않았어요")
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stopped := make(chan struct{})
	if stopOnTerm.Load() { // the engine stopping this node: ask the runner to stop too
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		defer signal.Stop(term)
		go func() {
			select {
			case <-term:
				close(stopped)
				stop()
			case <-ctx.Done():
			}
		}()
	}
	failures := 0
	for i := 0; ; i++ {
		if backendNow().After(st.deadline) {
			return st.cancel("시간 안에 끝나지 않았어요")
		}
		backendSleep(ctx, backendPoll[min(i, len(backendPoll)-1)])
		select {
		case <-stopped:
			out := st.cancel("실행을 멈췄어요")
			fmt.Println(dumpLine(ordered(out, resultOrder...)))
			os.Exit(143)
		default:
		}
		var reply backendReply
		rctx, cancel := context.WithTimeout(ctx, backendStartMax)
		code, err := backendCall(rctx, st.b, http.MethodGet, "/v1/tasks/"+url.PathEscape(st.entry.TaskID), nil, "", &reply)
		cancel()
		if code == 404 {
			return st.end("unknown", "", "러너가 이 작업을 모른다고 해요: 다시 돌리지 않았어요")
		}
		if err != nil { // a failed status query is not a failed task: ask again until the time runs out
			failures++
			st.trace["status_failures"] = failures
			continue
		}
		if reply.TaskID == "" {
			reply.TaskID = st.entry.TaskID
		}
		if out := st.apply(reply, false); out != nil {
			return out
		}
	}
}

// apply takes one reply: the end result, or nil while the task goes on (start: follows it).
func (st *backendStep) apply(r backendReply, first bool) map[string]any {
	if out := st.unusable(&r, first); out != nil {
		return out
	}
	if !first && r.TaskID != st.entry.TaskID || backendRank[r.Status] < backendRank[st.entry.State] {
		return nil // another task's answer, or out of order (a task never goes back): ask again
	}
	st.entry.TaskID = r.TaskID
	st.trace["task_id"] = r.TaskID
	if r.Model != "" {
		st.trace["model"] = clipRunes(r.Model, 80)
	}
	if out := st.ended(r); out != nil {
		return out
	}
	prev := st.entry.State
	st.entry.State = r.Status
	st.trace["state"] = r.Status
	_ = st.entry.save()
	if first || prev == "starting" {
		if st.onAccept != nil {
			st.onAccept(st.trace)
		}
		return st.follow()
	}
	return nil
}

// unusable ends the step as unknown on a reply Arkism cannot read as a state; it drops a model name it cannot keep.
func (st *backendStep) unusable(r *backendReply, first bool) map[string]any {
	if _, known := backendRank[r.Status]; !known || r.Status == "starting" || r.Status == "unknown" {
		if first && backendLabel.MatchString(r.TaskID) { // keep the id, so the owner can look the task up
			st.entry.TaskID = r.TaskID
			st.trace["task_id"] = r.TaskID
		}
		return st.end("unknown", "", "러너의 답에 알 수 있는 상태가 없어요: 성공으로 보지 않았어요")
	}
	if r.TaskID != "" && !backendLabel.MatchString(r.TaskID) {
		return st.end("unknown", "", "러너가 보낸 작업 번호의 모양이 이상해요: 결과를 믿지 않았어요")
	}
	if first && r.TaskID == "" && !terminalState(r.Status) {
		return st.end("unknown", "", "러너가 작업 번호 없이 접수했어요: 결과를 확인할 수 없어요")
	}
	if !backendLabel.MatchString(r.Model) {
		r.Model = ""
	}
	return nil
}

// ended is the step's result for a final state (and for an approval wait, which Arkism never answers); nil otherwise.
func (st *backendStep) ended(r backendReply) map[string]any {
	switch r.Status {
	case "succeeded":
		return st.finish(r)
	case "failed", "cancelled":
		msg, code := "외부 작업이 실패했어요", "failed"
		if r.Status == "cancelled" {
			msg, code = "외부 작업이 취소됐어요", "cancelled"
		}
		if r.Error != nil && strings.TrimSpace(r.Error.Message) != "" {
			msg += ": " + clipRunes(r.Error.Message, 200)
		}
		return st.end(r.Status, code, msg)
	case "waiting_approval":
		// The request said approval "none": Arkism does not relay approvals yet, and never approves for a person.
		return st.cancel("러너가 사람의 승인을 기다려요. Arkism은 아직 외부 승인을 전달하지 않아요")
	}
	return nil
}

// cancel asks the runner to stop when it can, and ends the step as failed either way.
func (st *backendStep) cancel(why string) map[string]any {
	if !st.caps["cancel"] || st.entry.TaskID == "" {
		return st.end("unknown", "timeout", why+": 러너가 취소를 못 해서 그쪽 작업과 비용이 계속될 수 있어요")
	}
	st.entry.State = "cancel_requested"
	_ = st.entry.save()
	var reply backendReply
	ctx, cancel := context.WithTimeout(context.Background(), backendStartMax)
	defer cancel()
	_, err := backendCall(ctx, st.b, http.MethodPost, "/v1/tasks/"+url.PathEscape(st.entry.TaskID)+"/cancel", map[string]any{"reason": "arkism"}, "", &reply)
	if err == nil && reply.Status == "cancelled" {
		return st.end("cancelled", "timeout", why+": 러너가 취소를 확인했어요")
	}
	return st.end("cancel_requested", "timeout", why+": 취소를 요청했지만 확인은 못 받았어요 (비용이 더 들 수 있어요)")
}

// finish is a succeeded task: its text (clipped, the connection's token masked) and what it reported using.
func (st *backendStep) finish(r backendReply) map[string]any {
	text := ""
	if r.Result != nil {
		text = strings.TrimSpace(r.Result.Text)
	}
	if text == "" {
		return st.end("unknown", "", "러너가 성공이라면서 결과를 보내지 않았어요")
	}
	if tok, _ := st.b.token(); tok != "" {
		text = strings.ReplaceAll(text, tok, "***")
	}
	if len([]rune(text)) > backendResultChars {
		text = clipRunes(text, backendResultChars-40) + "\n(결과가 길어 여기까지만 받았어요.)"
	}
	out := st.end("succeeded", "", "")
	out["ok"], out["output"], out["error"] = true, text, ""
	if u := r.Usage; u != nil && st.caps["usage"] && (u.InputTokens != nil || u.OutputTokens != nil || u.CostUSD != nil) {
		usage := map[string]any{"estimated": false, "source": "backend"}
		if u.InputTokens != nil {
			usage["input_tokens"] = *u.InputTokens
		}
		if u.OutputTokens != nil {
			usage["output_tokens"] = *u.OutputTokens
		}
		if u.CostUSD != nil {
			usage["cost_usd"] = *u.CostUSD
		}
		out["usage"] = usage
	} else {
		st.trace["usage"] = "unknown"
	}
	return out
}

// end records the step's last state and returns the run-node result for it; never ok (finish sets ok).
func (st *backendStep) end(state, code, msg string) map[string]any {
	if st.entry != nil {
		st.entry.State = state
		_ = st.entry.save()
	}
	st.trace["state"] = state
	errText := ""
	if msg != "" {
		errText = "backend_" + state + ": " + msg
		if code != "" {
			errText = "backend_" + code + ": " + msg
		}
	}
	model, _ := st.trace["model"].(string)
	if model == "" {
		model = st.requested
	}
	out := result(false, "", model, st.effort, "backend:"+st.b.Name, st.started, clipRunes(errText, 300))
	out["backend"] = st.trace
	return out
}
