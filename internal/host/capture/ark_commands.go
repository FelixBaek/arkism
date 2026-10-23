package capture

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// `ark:` chat commands. A prompt that is exactly one command is answered by the UserPromptSubmit
// hook itself: no model is called, the prompt never reaches the agent, and it is not captured as a
// memory. The answer goes back the way each agent shows a hook's message:
//
//	Claude Code   {"decision":"block","reason":…}: the prompt is dropped and reason is shown to the user.
//	Codex         the same, plus systemMessage, which Codex surfaces as a warning in its UI; Codex does
//	              not document whether reason itself is displayed.
//	ZCode         sent the Claude Code way; whether ZCode honors decision:block is not documented, so the
//	              prompt may still reach the model there (see docs/adopt-ui-commands.md).
//
// Agents without a UserPromptSubmit hook (Gemini CLI, Grok, OpenCode, Cursor) never see the commands.
// The hook stays fail-open: an error inside a recognized command is shown as a short code, never
// breaks the session, and an unrecognized prompt passes through untouched.

const arkHelp = `Arkism 명령 (모델을 부르지 않고 바로 처리합니다)
  ark:remember <내용>  기억 후보로 남깁니다. 검토해 승인하기 전에는 쓰이지 않습니다.
  ark:status           이번 세션에 Arkism이 넣은 기억 수와 id
  ark:sync             스킬·에이전트 동기화를 백그라운드로 시작합니다
  ark:forget <기억 id>  근거 원본의 개인정보 삭제 요청을 엽니다(앱의 잊기와 같은 절차). 훅이 직접 지우지는 않습니다.
  ark:help             이 도움말`

var (
	arkCommandRE = regexp.MustCompile(`(?is)^ark:([a-z]+)(?:\s+(.*))?$`)
	arkMemoryID  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
	// arkSpawn starts a detached `arkism ARGS…` (tests replace it).
	arkSpawn = func(args ...string) error {
		cmd := exec.Command(host.Executable(), args...)
		cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1")
		detach(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
		go cmd.Wait()
		return nil
	}
)

// arkParse returns the command name and its argument, or ok false for an ordinary prompt. A command
// that takes no argument but has text after it is an ordinary prompt ("ark:status 가 뭐야?").
func arkParse(prompt string) (name, arg string, ok bool) {
	m := arkCommandRE.FindStringSubmatch(strings.TrimSpace(prompt))
	if m == nil {
		return "", "", false
	}
	name, arg = strings.ToLower(m[1]), strings.TrimSpace(m[2])
	switch name {
	case "status", "sync", "help":
		return name, "", arg == ""
	}
	return name, arg, true // remember/forget, and unknown names (answered with the help text)
}

// arkHandle answers an `ark:` prompt; false means the prompt is not a command.
func arkHandle(data map[string]any, stdout io.Writer) bool {
	prompt, _ := data["prompt"].(string)
	name, arg, ok := arkParse(prompt)
	if !ok {
		return false
	}
	var msg string
	err := func() (err error) {
		defer recovered(&err)
		msg, err = arkRun(name, arg, data)
		return err
	}()
	if err != nil {
		metric(kv("event", "hook_error"), kv("hook", "ark:"+name), kv("error", pyType(err)))
		msg = fmt.Sprintf("Arkism: ark:%s 실패 (%s). 다시 시도하거나 앱에서 확인하세요.", name, arkErrCode(err))
	}
	metric(kv("event", "ark_command"), kv("command", name), kv("session", str(or(data["session_id"], ""))), kv("ok", err == nil))
	fmt.Fprintln(stdout, arkOutput(capOrigin, msg))
	return true
}

func arkErrCode(err error) string {
	if c := host.HTTPCode(err); c != 0 {
		return fmt.Sprintf("HTTP %d", c)
	}
	if networkish(err) {
		return "서버 연결 안 됨"
	}
	return pyType(err)
}

// arkOutput is the hook answer that drops the prompt and shows msg.
func arkOutput(origin, msg string) string {
	out := host.Obj{{Key: "decision", Value: "block"}, {Key: "reason", Value: msg}}
	if origin == "codex" {
		out = append(out, host.KV{Key: "systemMessage", Value: msg})
	}
	return host.MustDumps(out, host.DumpOpts{NoASCII: true})
}

func arkRun(name, arg string, data map[string]any) (string, error) {
	sid := str(or(data["session_id"], ""))
	cwd := str(or(data["cwd"], ""))
	switch name {
	case "remember":
		return arkRemember(arg, sid, cwd)
	case "status":
		return arkStatus(sid)
	case "sync":
		return arkSync()
	case "forget":
		return arkForget(arg, sid, cwd)
	case "help":
		return arkHelp, nil
	}
	return "알 수 없는 명령: ark:" + name + "\n\n" + arkHelp, nil
}

// arkRemember sends the text as one inferred claim through the capture gate, the same path the MCP
// remember tool uses: secrets are rejected there and nothing is approved here.
func arkRemember(arg, sid, cwd string) (string, error) {
	if captureSkipped(cwd) {
		return "Arkism 수집이 멈췄거나 이 폴더는 제외되어 저장하지 않았습니다.", nil
	}
	text := clean(arg) // <private> spans never leave the hook
	if text == "" {
		return "남길 내용이 없습니다. 예: ark:remember 배포 전에는 스테이징에서 먼저 확인한다", nil
	}
	if len(text) > 4000 {
		return "너무 깁니다 (4000바이트까지). 짧게 나눠 남겨 주세요.", nil
	}
	origin := capOrigin
	res, err := capPost("/v1/capture", host.Obj{
		{Key: "scope", Value: capScope()},
		{Key: "generator", Value: host.Obj{{Key: "model", Value: "user-command"}, {Key: "prompt_version", Value: "ark-remember-v1"}, {Key: "pipeline_version", Value: "ark-command-v1"}}},
		{Key: "event", Value: host.Obj{{Key: "id", Value: "ark:" + sha256Hex([]byte(origin + "\x00" + sid + "\x00" + text))[:32]}, {Key: "type", Value: "explicit_remember"},
			{Key: "origin_product", Value: origin}, {Key: "session_id", Value: head(sid, 120)}, {Key: "occurred_at", Value: host.NowISO()}}},
		{Key: "claims", Value: []any{host.Obj{{Key: "subject", Value: "memory"}, {Key: "text", Value: text}, {Key: "kind", Value: "decision"},
			{Key: "basis", Value: "inferred"}, {Key: "reason", Value: "ark:remember로 사용자가 남긴 기억. 사람의 검토가 필요합니다."}}}},
	}, 8*time.Second)
	if err != nil {
		return "", err
	}
	items := dicts(listOf(res["items"]))
	if len(items) == 0 {
		return "", runtimeErr("capture_no_items")
	}
	item := items[0]
	switch reason := str(item["reason"]); {
	case reason == "secret_detected":
		return "비밀값(키·토큰·비밀번호)처럼 보여 저장하지 않았습니다.", nil
	case item["status"] == "rejected":
		return "저장하지 못했습니다 (" + reason + ").", nil
	case item["status"] == "merged":
		return "이미 같은 기억이나 후보가 있습니다" + arkID(item) + ".", nil
	}
	return "기억 후보로 남겼습니다" + arkID(item) + ". 앱의 검토 화면에서 승인해야 쓰입니다.", nil
}

func arkID(item map[string]any) string {
	if id := str(or(item["candidate_id"], item["memory_id"], "")); id != "" {
		return " (id: " + id + ")"
	}
	return ""
}

// arkStatus is what the hooks injected into this session, from the local metrics log (ids only).
func arkStatus(sid string) (string, error) {
	rows, err := metricRows()
	if err != nil {
		return "", err
	}
	type hookUse struct {
		times, items int
		ids          []string
	}
	uses := map[string]*hookUse{}
	seen := map[string]bool{}
	chars := 0
	for _, r := range rows {
		if sid == "" || str(r["session"]) != sid {
			continue
		}
		switch r["event"] {
		case "recall":
			h := str(r["hook"])
			u := uses[h]
			if u == nil {
				u = &hookUse{}
				uses[h] = u
			}
			n, _ := host.Int(r["injected"])
			u.times++
			u.items += int(n)
			ids, _ := r["ids"].([]any)
			for _, id := range ids {
				if s := str(id); s != "" && !seen[h+"\x00"+s] {
					seen[h+"\x00"+s] = true
					u.ids = append(u.ids, s)
				}
			}
		case "inject":
			if n, ok := host.Int(r["chars"]); ok && n > 0 {
				chars += int(n)
			}
		}
	}
	var b strings.Builder
	b.WriteString("Arkism이 이번 세션에 넣은 것")
	if sid != "" {
		b.WriteString(" (세션 " + head(sid, 12) + ")")
	}
	b.WriteString(":\n")
	if len(uses) == 0 {
		b.WriteString("  아직 없습니다.\n")
	}
	hooks := make([]string, 0, len(uses))
	for h := range uses {
		hooks = append(hooks, h)
	}
	sort.Strings(hooks)
	labels := map[string]string{"SessionStart": "세션 시작 목차", "SessionStart:contract": "작업 계약 관련", "UserPromptSubmit": "프롬프트 검색"}
	total := map[string]bool{}
	for _, h := range hooks {
		u := uses[h]
		label := labels[h]
		if label == "" {
			label = h
		}
		fmt.Fprintf(&b, "  %s: %d회, 기억 %d개 (서로 다른 id %d개)\n", label, u.times, u.items, len(u.ids))
		for _, id := range u.ids {
			total[id] = true
		}
		arkStatusIDs(&b, u.ids)
	}
	fmt.Fprintf(&b, "  합계: 서로 다른 기억 %d개, 넣은 글자 %d자", len(total), chars)
	return b.String(), nil
}

// arkStatusIDs writes up to 20 of a hook's ids on one line, then how many were left out.
func arkStatusIDs(b *strings.Builder, ids []string) {
	if len(ids) > 0 {
		shown := ids
		if len(shown) > 20 {
			shown = shown[:20]
		}
		b.WriteString("    " + strings.Join(shown, ", "))
		if len(ids) > len(shown) {
			fmt.Fprintf(b, " 외 %d개", len(ids)-len(shown))
		}
		b.WriteString("\n")
	}
}

// arkSync starts the skill and agent sync in the background; it never waits on them.
func arkSync() (string, error) {
	var failed []string
	for _, args := range [][]string{{"sync-skills", "--execute"}, {"agents", "sync"}} {
		if err := arkSpawn(args...); err != nil {
			failed = append(failed, strings.Join(args, " "))
		}
	}
	if len(failed) == 2 {
		return "", runtimeErr("sync_spawn_failed")
	}
	msg := "스킬·에이전트 동기화를 백그라운드에서 시작했습니다. 바뀐 스킬은 검토 화면에서 승인해야 쓰입니다."
	if len(failed) > 0 {
		msg += " (시작 못 함: " + strings.Join(failed, ", ") + ")"
	}
	return msg, nil
}

// ---------------------------------------------------------------- forget requests

// ark:forget opens a privacy deletion REQUEST (POST /v1/privacy/deletions, target_type
// source_event) for the source records the memory came from, the same saga the app's 잊기 uses.
// The server's authorization, hold and purge rules decide what happens next; the hook never
// deletes anything itself, and a memory without a source record is left to the app.

const (
	arkForgetReason = "user_erasure_request:ark_forget"
	arkForgetBatch  = 64 // the server's target_ids limit per request
)

// arkForget finds the memory (or candidate) and asks the server to erase its source records.
func arkForget(arg, sid, cwd string) (string, error) {
	id := strings.TrimSpace(arg)
	if !arkMemoryID.MatchString(id) {
		return "기억 id 하나를 적어 주세요. 예: ark:forget mem_0123… (id는 ark:status나 앱에서 볼 수 있습니다)", nil
	}
	if captureSkipped(cwd) {
		return "Arkism 수집이 멈췄거나 이 폴더는 제외되어 요청을 열지 않았습니다.", nil
	}
	var record map[string]any
	for _, col := range []string{"memories", "memory_candidates"} {
		res, err := capGet("/v1/"+col+"/"+url.PathEscape(id)+"?"+scopeQuery(false), 8*time.Second)
		if host.HTTPCode(err) == 404 {
			continue
		}
		if err != nil {
			return arkForgetFailed(err), nil
		}
		record = res
		break
	}
	if record == nil {
		return "그 id의 기억을 찾지 못했습니다: " + id, nil
	}
	seen := map[string]bool{}
	var sources []string
	add := func(v any) {
		if s := str(or(v, "")); s != "" && !seen[s] {
			seen[s] = true
			sources = append(sources, s)
		}
	}
	for _, ref := range dicts(listOf(record["sources"])) {
		add(ref["source_id"])
	}
	add(record["source_event_id"])
	if len(sources) == 0 {
		return "이 기억에는 근거 원본 기록이 없어 삭제 요청을 열 수 없습니다. 앱의 기억 화면에서 '잊기'로 지워 주세요: " + id, nil
	}
	sort.Strings(sources)
	rev, _ := host.Int(record["revision"])
	var ids []string
	for i := 0; i < len(sources); i += arkForgetBatch {
		batch := sources[i:min(i+arkForgetBatch, len(sources))]
		// The same memory revision always opens the same request: a repeated ark:forget returns it.
		key := fmt.Sprintf("ark-forget-%s-%d", sha256Hex([]byte(id + "\x00" + strings.Join(batch, "\x00")))[:32], rev)
		res, err := capPost("/v1/privacy/deletions", host.Obj{{Key: "scope", Value: capScope()}, {Key: "idempotency_key", Value: key},
			{Key: "target_type", Value: "source_event"}, {Key: "target_ids", Value: toAny(batch)}, {Key: "reason", Value: arkForgetReason}}, 15*time.Second)
		if err != nil {
			return arkForgetFailed(err), nil
		}
		reqID := str(or(res["id"], ""))
		if pd, ok := res["privacy_deletion"].(map[string]any); ok && str(pd["state"]) != "" {
			reqID += " (" + str(pd["state"]) + ")"
		}
		ids = append(ids, reqID)
	}
	metric(kv("event", "forget_requested"), kv("memory_id", id), kv("sources", len(sources)), kv("session", head(sid, 120)))
	return fmt.Sprintf("삭제 요청을 열었습니다: %s\n대상: 기억 %s의 근거 원본 %d개 (같은 원본에서 나온 다른 기억도 함께 지워집니다). 서버의 승인·보류 규칙에 따라 처리됩니다.",
		strings.Join(ids, ", "), id, len(sources)), nil
}

func toAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// arkForgetFailed is the fail-open answer: the request was not opened, and why in one code.
func arkForgetFailed(err error) string {
	metric(kv("event", "hook_error"), kv("hook", "ark:forget"), kv("error", pyType(err)))
	code := arkErrCode(err)
	switch host.HTTPCode(err) {
	case 403:
		code = "이 계정은 삭제 요청 권한이 없습니다"
	case 409:
		code = "다른 삭제 요청이 진행 중이거나 그새 바뀌었습니다"
	}
	return "삭제 요청을 열지 못했습니다 (" + code + "). 앱의 기억 화면에서 '잊기'를 써 주세요."
}
