package crew

// run-node: one AI step of a workflow node, run here with the operator's own CLIs.

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func stepTimeout() int {
	n, err := strconv.Atoi(host.Env("ARKISM_CREW_TIMEOUT", "900")) // seconds per AI step without timeout_s
	if err != nil {
		return 900
	}
	return n
}

func skillCache() string {
	return host.Env("ARKISM_SKILL_CACHE", host.Home+"/.cache/arkism-skills")
}

// result is the run-node answer {ok, output, model, effort, cli, duration_ms, error}.
func result(ok bool, output, model, effort, cli string, started time.Time, errText string) map[string]any {
	ms := int64(0)
	if !started.IsZero() {
		ms = time.Since(started).Milliseconds()
	}
	return map[string]any{"ok": ok, "output": output, "model": model, "effort": effort, "cli": cli, "duration_ms": ms, "error": errText}
}

func failed(model, errText string) map[string]any {
	return result(false, "", model, "", "", time.Time{}, errText)
}

var resultOrder = []string{"ok", "output", "model", "effort", "cli", "duration_ms", "error", "routed"}

// ---------------------------------------------------------------- skills and argument size

// runTextBytes: a run hands the instructions to the CLI as one argument; Linux caps one argument at 128 KiB.
const runTextBytes = 120_000

// argBytes is the bytes text takes as a CLI argument, counting the JSON escaping Codex's -c option adds.
func argBytes(text string) int { return len(dumpLine(text)) }

// fitText cuts text to fit one CLI argument beside task (Gemini and grok carry both in one), saying so when cut.
func fitText(text, task string) string {
	room := runTextBytes - argBytes(task)
	if argBytes(text) <= room {
		return text
	}
	for text != "" && argBytes(text) > room-200 {
		r := []rune(text)
		text = string(r[:len(r)*3/4])
	}
	return text + "\n(지시문이 너무 길어 여기까지만 넣었어요.)"
}

// resolveSkills resolves skill refs through the hub (a fake in tests).
var resolveSkills = func(refs []string) ([]map[string]any, error) {
	raw, err := os.ReadFile(host.TokenFile)
	if err != nil {
		return nil, err
	}
	return host.ResolveSkills(host.BaseURL, strings.TrimSpace(string(raw)), host.Scope(), skillCache(), 15*time.Second, refs, "", "")
}

// skillText is the SKILL.md bodies of the skills a node names, from the hub (latest approved), as one instructions
// block. A skill the hub cannot give fails the step: a node that names a skill must not run without it.
func skillText(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	refs := make([]string, len(ids))
	for i, id := range ids {
		refs[i] = id
		if !strings.Contains(id, "@") {
			refs[i] = id + "@latest-approved"
		}
	}
	items, err := resolveSkills(refs)
	if err != nil {
		var he *host.SkillHubError
		if errors.As(err, &he) {
			return "", crewErr(fmt.Sprintf("스킬을 불러오지 못했어요 (%s)", he.Error()))
		}
		return "", err
	}
	parts := []string{}
	for i, ref := range ids {
		if i >= len(items) {
			break
		}
		item := items[i]
		if truthy(item["error"]) {
			return "", crewErr(fmt.Sprintf("스킬 %s을(를) 찾지 못했어요", ref))
		}
		parts = append(parts, fmt.Sprintf("## 스킬: %s\n%s", pyStr(item["name"]), strings.TrimSpace(str(item["instructions"]))))
	}
	return "# 이 단계에서 따를 스킬\n\n" + strings.Join(parts, "\n\n"), nil
}

// joinSkills puts instructions plus the named skills in one CLI argument. Only the step's own text may be cut;
// skills that do not fit whole refuse the step instead of running it on part of a skill.
func joinSkills(own, skills, beside string) (string, error) {
	if skills == "" {
		return fitText(own, beside), nil
	}
	if argBytes(skills)+argBytes(beside) > runTextBytes-1000 {
		return "", crewErr("지정한 스킬이 너무 길어 한 번에 넣을 수 없어요. 스킬 수를 줄여 주세요")
	}
	own = fitText(own, beside+skills+"\n\n")
	if own == "" {
		return skills, nil
	}
	return own + "\n\n" + skills, nil
}

// ---------------------------------------------------------------- 직접 추가 chat

// runChat runs a 직접 추가 provider with only an OpenAI-style API: one chat completion, no tools.
func runChat(key map[string]any, model, effort, instructions, task string, timeout time.Duration, started time.Time) (map[string]any, error) {
	messages := []any{}
	if instructions != "" {
		messages = append(messages, host.Obj{{Key: "role", Value: "system"}, {Key: "content", Value: instructions}})
	}
	messages = append(messages, host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: task}})
	url, err := httpsOnly(strings.TrimRight(sOr(key["base_url"]), "/") + "/chat/completions")
	if err != nil {
		return nil, err
	}
	secret := str(key["key"])
	done := func(ok bool, out, errText string) map[string]any {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, "***")
		}
		return result(ok, out, model, effort, "custom", started, errText)
	}
	body := host.MustDumps(host.Obj{{Key: "model", Value: model}, {Key: "messages", Value: messages}}, host.DumpOpts{})
	// OpenCode Go refuses a request without a per-conversation id (MissingSessionID); others ignore it.
	raw, err := providerDo(http.MethodPost, url, []byte(body), map[string]string{"Authorization": bearer + secret,
		"content-type": jsonType, "x-opencode-session": randHex(16)}, timeout, 8<<20)
	if err != nil {
		var he *host.HTTPError
		if errors.As(err, &he) {
			return done(false, "", "http_"+strconv.Itoa(he.Code)), nil
		}
		return done(false, "", "unreachable"), nil
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		return done(false, "", "unreachable"), nil
	}
	choices := list(obj(v)["choices"])
	first := map[string]any{}
	if len(choices) > 0 {
		first = obj(choices[0])
	}
	text := sOr(obj(first["message"])["content"])
	if text == "" {
		return done(false, "", "empty"), nil
	}
	return done(true, text, ""), nil
}

// ---------------------------------------------------------------- the CLI process

var (
	errSpawn   = errors.New("spawn failed")
	errTimeout = errors.New("timed out")
	// stopOnTerm: the run-node and route commands own the process, so a SIGTERM to them (the graph engine
	// stopping a node) takes the CLI's group down with it.
	stopOnTerm atomic.Bool
)

// spawnCLI runs argv with stdin in its own session and returns its stdout, stderr and exit code
// (a signal is its negative number); errSpawn when it cannot start, errTimeout after timeout.
var spawnCLI = func(argv []string, stdin, cwd string, env map[string]string, timeout time.Duration, outFile string) (string, string, int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdin = cwd, envList(env), strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	newSession(cmd)
	var term chan os.Signal
	if stopOnTerm.Load() {
		term = make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		defer signal.Stop(term)
	}
	if err := cmd.Start(); err != nil {
		return "", "", 0, errSpawn
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		killGroup(cmd.Process.Pid, sigKill)
		<-done
		return "", "", 0, errTimeout
	case <-term:
		killGroup(cmd.Process.Pid, sigKill)
		unlink(outFile)
		os.Exit(143)
	}
	return newlines(out.String()), newlines(errOut.String()), exitCode(cmd.ProcessState), nil
}

// newlines is Python's universal-newline text mode.
func newlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func unlink(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

var limitLine = regexp.MustCompile(`(?i)^(?:error[: ]+)?(?:HTTP(?:/\d(?:\.\d)?)?[\s:]*429\b|(?:unexpected )?(?:status|error)(?:\s+code)?[\s:=]+429\b` +
	`|429\s+Too Many Requests\b|(?:session|usage|weekly)\s+limit(?:\s+(?:has been\s+)?(?:reached|exceeded)\b|$)` +
	`|you(?:'ve| have)\s+(?:hit|reached)\s+your\s+(?:(?:session|usage|weekly)\s+)?limit\b)`)

// subscriptionLimit: CLI stderr includes task and tool transcripts; only its final diagnostic is inspected.
func subscriptionLimit(text string) bool {
	lines := pySplitlines(strings.TrimSpace(text))
	line := ""
	if len(lines) > 0 {
		line = strings.TrimSpace(lines[len(lines)-1])
	}
	return limitLine.MatchString(line)
}

func stringList(v any) []string {
	out := []string{}
	for _, x := range list(v) {
		out = append(out, pyStr(x))
	}
	return out
}

// runNode runs one AI step of a workflow node and returns {ok, output, model, effort, cli, duration_ms, error}, plus
// rules (short sha256 of the shared record rules) when a step that may change files got them.
// A *host.CrewError is for what the caller must fix (no task, an unknown effort or access, a write step without cwd,
// no CLI for the model, no working folder).
func runNode(spec map[string]any) (res map[string]any, err error) {
	rulesSum := ""
	defer func() { // the run record names which shared rules the step got
		if res != nil && rulesSum != "" {
			res["rules"] = rulesSum
		}
	}()
	selected := strings.TrimSpace(sOr(spec["model"]))
	if strings.HasPrefix(selected, "group/") {
		return runRoutedFn(withKey(spec, "group", selected), nil, nil, nil)
	}
	if strings.ToLower(selected) == "auto" {
		return runRoutedFn(spec, nil, nil, nil)
	}
	started := time.Now()
	task := strings.TrimSpace(sOr(spec["task"]))
	if task == "" {
		return nil, crewErr("할 일(task)이 비었어요")
	}
	model := selected
	effort, timeout, err := nodeLimits(spec)
	if err != nil {
		return nil, err
	}
	row, key, err := nodeCLI(model, spec)
	if err != nil {
		return nil, err
	}
	access, err := nodeAccess(spec)
	if err != nil {
		return nil, err
	}
	rules, sum, err := workerRules(access, truthy(spec["once"]))
	rulesSum = sum
	if err != nil {
		return nil, err
	}
	own := withRules(rules, strings.TrimSpace(sOr(spec["instructions"])))
	skills, err := skillText(stringList(spec["skills"]))
	if err != nil {
		return nil, err
	}
	if key != nil {
		info, err := customInfo(key, false)
		if err != nil {
			return nil, err
		}
		if info["api"] == "openai" {
			return runChat(key, model, effort, chatInstructions(own, skills), task, timeout, started)
		}
	}
	workdir, err := nodeWorkdir(access, spec)
	if err != nil {
		return nil, err
	}
	env, err := childEnv(row, nil, model, key)
	if err != nil {
		return nil, err
	}
	env, profile, reason := accountEnv(row, model, env)
	if reason == "quota_exhausted" {
		return result(false, "", model, effort, row.Name, started, reason), nil
	}
	if truthy(spec["handoff_isolated"]) {
		var cleanup func()
		env, cleanup, err = isolatedHandoffEnv(env, row.Name)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		if row.Name == "codex" {
			if err = verifyHandoffCodex(binOf(row), workdir, env); err != nil {
				return nil, err
			}
		}
	}
	return spawnNode(spec, row, model, effort, task, own, skills, access, workdir, env, profile, timeout, started)
}

// nodeLimits reads the step's effort and timeout.
func nodeLimits(spec map[string]any) (string, time.Duration, error) {
	effortV := spec["effort"]
	if !truthy(effortV) {
		effortV = "medium"
	}
	effort, _ := effortV.(string)
	if !in(efforts, effort) {
		return "", 0, crewErr("추론 수준은 low·medium·high·max 중 하나여야 해요")
	}
	seconds := int64(0)
	if truthy(spec["timeout_s"]) {
		n, ok := pyIntArg(spec["timeout_s"])
		if !ok {
			return "", 0, crewErr("timeout_s는 초 단위 숫자여야 해요")
		}
		seconds = n
	}
	if seconds == 0 {
		seconds = int64(stepTimeout())
	}
	return effort, time.Duration(seconds) * time.Second, nil
}

func nodeCLI(model string, spec map[string]any) (host.CLI, map[string]any, error) {
	row, ok := cliFor(model)
	if !ok {
		shown := model
		if shown == "" {
			shown = "(없음)"
		}
		return row, nil, crewErr(fmt.Sprintf("모델 %s을(를) 실행할 CLI를 몰라요 (ARKISM_CREW_ROUTES로 정할 수 있어요)", shown))
	}
	// 직접 추가: the key is picked once (연결 시험 names its provider) and used for the whole run
	if row.Name != host.CustomCLI.Name {
		return row, nil, nil
	}
	key, err := customKeyFor(model, nil, sOr(spec["provider"]))
	if err != nil {
		return row, nil, err
	}
	if key == nil {
		return row, nil, crewErr(model + "에 맞는 직접 추가 키가 없어요")
	}
	return row, key, nil
}

func nodeAccess(spec map[string]any) (string, error) {
	// ai_once answers one question and never needs to change anything.
	access := "read"
	if !truthy(spec["once"]) {
		access = sOr(spec["access"])
		if access == "" {
			access = "write"
		}
	}
	if !in(accessModes, access) {
		return "", crewErr("access는 read·write·full 중 하나여야 해요")
	}
	return access, nil
}

func chatInstructions(own, skills string) string {
	if own != "" && skills != "" {
		return own + "\n\n" + skills
	} else if own == "" {
		return skills
	}
	return own
}

func nodeWorkdir(access string, spec map[string]any) (string, error) {
	cwd := expandUser(sOr(spec["cwd"]))
	if access != "read" && cwd == "" {
		// Without one the step would work in the engine's own folder.
		return "", crewErr("파일을 고칠 수 있는 AI 단계는 작업 폴더(cwd)를 정해야 해요")
	}
	workdir := cwd
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	if !isDir(workdir) {
		return "", crewErr("작업 폴더가 없어요")
	}
	return workdir, nil
}

// accountEnv picks a subscription account for claude and codex outside API mode.
func accountEnv(row host.CLI, model string, env map[string]string) (map[string]string, map[string]any, string) {
	if (row.Name != "claude" && row.Name != "codex") || host.APIModeOn(row.Name) {
		return env, nil, ""
	}
	profile, reason := host.AccountsChoose(row.Name, model, binOf(row), host.Home, host.AgentsState, nil)
	if reason == "quota_exhausted" {
		return env, profile, reason
	}
	if profile != nil {
		env = host.AccountsChildEnvironment(env, profile)
	}
	return env, profile, reason
}

func spawnNode(spec map[string]any, row host.CLI, model, effort, task, own, skills, access, workdir string,
	env map[string]string, profile map[string]any, timeout time.Duration, started time.Time) (map[string]any, error) {
	outFile := ""
	if row.Name == "codex" {
		f, err := os.CreateTemp("", "arkism-crew-*.txt")
		if err != nil {
			return nil, err
		}
		outFile = f.Name()
		f.Close()
	}
	// Gemini and grok put the task into the same argument; the other CLIs read it from standard input.
	beside := ""
	if in([]string{"gemini", "grok", "agy"}, row.Name) {
		beside = task
	}
	instructions, err := joinSkills(own, skills, beside)
	if err != nil {
		unlink(outFile)
		return nil, err
	}
	argv, stdin := buildCommand(row, model, effort, instructions, task, outFile, truthy(spec["once"]), access)
	if truthy(spec["handoff_isolated"]) {
		argv = isolatedHandoffCommand(argv, row.Name)
	}
	out, errOut, code, err := spawnCLI(argv, stdin, workdir, env, timeout, outFile)
	switch err {
	case errSpawn:
		unlink(outFile)
		return result(false, "", model, effort, row.Name, started, "cli_not_found"), nil
	case errTimeout:
		unlink(outFile)
		return result(false, "", model, effort, row.Name, started, "timeout"), nil
	}
	if outFile != "" {
		if raw, err := os.ReadFile(outFile); err == nil && len(raw) > 0 {
			out = newlines(string(raw))
		}
		unlink(outFile)
	}
	if code != 0 {
		text := errOut
		if text == "" {
			text = out
		}
		if profile != nil && subscriptionLimit(text) {
			_ = host.AccountsRecordLimit(profile, host.AgentsState, model, -1)
			return result(false, "", model, effort, row.Name, started, "quota_exhausted"), nil
		}
		lines := pySplitlines(strings.TrimSpace(text))
		last := ""
		if len(lines) > 0 {
			last = lines[len(lines)-1]
		}
		return result(false, redact(strings.TrimSpace(out), env), model, effort, row.Name, started,
			fmt.Sprintf("exit_%d: ", code)+clipRunes(redact(last, env), 200)), nil
	}
	return result(true, redact(strings.TrimSpace(out), env), model, effort, row.Name, started, ""), nil
}

// runNodeFn is runNode (a fake in tests).
var runNodeFn func(map[string]any) (map[string]any, error)

func init() { runNodeFn, runRoutedFn = runNode, runRouted }
