package crew

// AI 도움 (crew assists). The desktop asks Arkism for help (AI로 채우기·다듬기, 대화에서 추천, 분류); this runner
// answers with a fill persona on a fixed model. Each answer is posted once.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/FelixBaek/arkism/internal/host"
)

func defaultAssistModel() string { return host.Env("ARKISM_ASSIST_MODEL", "sonnet 5.5") }

const assistPersona = "너는 Arkism의 도우미다. 사용자가 저장해 둔 전문가·스킬을 짧고 분명하게 다듬는다."

var assistGuide = map[string]string{
	"fill": "답은 JSON 객체 하나만 쓴다(설명·코드 블록 없이). 키: name(60자 이내), role(한 줄 소개, 200자 이내), category, " +
		"instructions(번호 목록 규칙, 2000자 이내), file_name(영문 소문자·숫자·하이픈), skills(아래 목록에 있는 이름만), " +
		"skill_rules([{field: category|name, value}]), tools(MCP 서버 이름). 모르면 그 키를 뺀다.",
	"polish":    "아래 글을 같은 뜻으로 더 짧고 분명하게 고친다. 고친 글만 쓴다.",
	"recommend": "답은 JSON 배열 하나만 쓴다: [{name, role, why}] 3개. why는 어떤 요청이 많았는지 한 줄.",
	"classify":  "답은 카테고리 이름 하나만 쓴다. 맞는 것이 없으면 빈 줄을 쓴다.",
	"explain":   "쉬운 한국어로 짧게 답한다. 답만 쓴다.",
	"draft":     "SKILL.md 전체만 쓴다(설명·코드 블록 표시 없이). 맨 위는 --- 로 감싼 name·description, 그 아래 번호 목록 단계 5개 이하.",
	"suggest": "답은 JSON 배열 하나만 쓴다: [{name(영문 소문자·숫자·하이픈), title(한국어 한 줄), why(어떤 요청이 몇 번 나왔는지 한 줄), " +
		"examples(사용자 요청 문장 2개), skill_md(SKILL.md 전체)}] 최대 3개. 이미 있는 스킬과 겹치면 빼고, 근거가 약하면 빈 배열을 쓴다.",
}

// Where installSkill writes a new SKILL.md, per agent (the shared ~/.agents/skills serves Codex and Gemini CLI).
const sharedSkillsDir = ".agents/skills"

var skillDirs = map[string]string{"claude_code": ".claude/skills", "codex": sharedSkillsDir, "gemini": sharedSkillsDir, "zcode": ".zcode/skills"}

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// installSkill writes a new skill folder for each chosen agent; it never overwrites one that exists. Returns the paths written.
func installSkill(spec map[string]any, home string) ([]string, error) {
	if home == "" {
		home = host.Home
	}
	name := strings.TrimSpace(sOr(spec["name"]))
	content := sOr(spec["content"])
	if !skillName.MatchString(name) || strings.TrimSpace(content) == "" || len([]rune(content)) > 20000 {
		return nil, crewErr("스킬 이름이나 내용이 올바르지 않아요")
	}
	set := map[string]bool{}
	for _, a := range list(spec["agents"]) {
		if d, ok := skillDirs[pyStr(a)]; ok {
			if _, isStr := a.(string); isStr {
				set[d] = true
			}
		}
	}
	dirs := make([]string, 0, len(set))
	for d := range set {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs { // all or nothing: a clash in one agent leaves the others untouched
		if _, err := os.Lstat(filepath.Join(home, d, name)); err == nil {
			return nil, crewErr(fmt.Sprintf("~/%s/%s이(가) 이미 있어요", d, name))
		}
	}
	written := []string{}
	for _, d := range dirs {
		folder := filepath.Join(home, d, name)
		if err := os.MkdirAll(filepath.Dir(folder), 0o777); err != nil {
			return nil, err
		}
		if err := os.Mkdir(folder, 0o777); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(strings.TrimRightFunc(content, unicode.IsSpace)+"\n"), 0o666); err != nil {
			return nil, err
		}
		written = append(written, fmt.Sprintf("~/%s/%s/SKILL.md", d, name))
	}
	if len(written) == 0 {
		return nil, crewErr("넣을 에이전트를 골라 주세요")
	}
	return written, nil
}

const assistTTL = 600.0 // the server reads an unanswered request as failed after ten minutes

// assistLeft is the seconds before the request expires on the server; an unknown creation time counts as fresh.
func assistLeft(rec map[string]any, now time.Time) float64 {
	made, err := time.Parse(time.RFC3339Nano, strings.Replace(pyStr(rec["created_at"]), "Z", "+00:00", 1))
	if err != nil {
		return assistTTL
	}
	return assistTTL - now.Sub(made).Seconds()
}

func outputsDir() string {
	return host.Env("ARKISM_CREW_OUTPUTS", filepath.Join(host.DataDir, "outputs"))
}

func outputName(aid string) string { return clipRunes(unsafeSub.ReplaceAllString(aid, ""), 64) }

func failedResult(msg string) map[string]any {
	return map[string]any{"status": "failed", "error": clipRunes(msg, 500)}
}

// actionAssist runs 코드 리뷰 › AI로 고치기 and 이미지 › ComfyUI로 만들기.
func actionAssist(a map[string]any, aid string) map[string]any {
	prompt := sOr(a["prompt"])
	if prompt == "" {
		prompt = "{}"
	}
	v, err := host.Loads(prompt)
	if err != nil {
		return failedResult("ValueError")
	}
	spec := obj(v)
	if a["kind"] == "fix" {
		ok, text, err := fixFinding(spec, actionRun, nil)
		if err != nil {
			return failedResult(err.Error())
		}
		if !ok {
			return failedResult(text)
		}
		return map[string]any{"status": "done", "output": text}
	}
	paths, err := comfyGenerate(spec, filepath.Join(outputsDir(), outputName(aid)), 2*time.Second, 420*time.Second)
	if err != nil {
		return failedResult(err.Error())
	}
	return map[string]any{"status": "done", "output": "만든 파일:\n" + strings.Join(paths, "\n")}
}

// installAssist is an install_skill assist: write the skill folders, or a failed result.
func installAssist(a map[string]any) map[string]any {
	prompt := sOr(a["prompt"])
	if prompt == "" {
		prompt = "{}"
	}
	v, err := host.Loads(prompt)
	if err != nil {
		return failedResult("ValueError")
	}
	paths, err := installSkill(obj(v), "")
	if err != nil {
		msg := err.Error()
		if msg == "" {
			msg = "쓰지 못했어요"
		}
		return failedResult(msg)
	}
	return map[string]any{"status": "done", "output": dumpLine(toAny(paths))}
}

// skillRoots is the folders a draft may quote a SKILL.md from (the skill sources the hub reads).
func skillRoots() []string {
	return []string{host.SkillsRoot, filepath.Join(host.Home, ".codex/skills"), filepath.Join(host.Home, ".codex/plugins/cache"),
		filepath.Join(host.Home, ".claude/skills"), filepath.Join(host.Home, sharedSkillsDir)}
}

var skillRef = regexp.MustCompile(`\{\{SKILL\.md:([^{}\n]{1,1000})\}\}`)

func resolvePath(p string) string {
	abs, _ := filepath.Abs(p)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// expandSkillRefs turns {{SKILL.md:/path/SKILL.md}} into that file's text, only for a SKILL.md under a skill root.
func expandSkillRefs(prompt string) (string, error) {
	var b strings.Builder
	last := 0
	for _, m := range skillRef.FindAllStringSubmatchIndex(prompt, -1) {
		p := resolvePath(prompt[m[2]:m[3]])
		ok := false
		for _, r := range skillRoots() {
			ok = ok || within(p, resolvePath(r))
		}
		if filepath.Base(p) != "SKILL.md" || !ok {
			return "", crewErr("스킬 폴더 밖의 파일은 읽지 않아요")
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", crewErr(filepath.Base(filepath.Dir(p)) + "/SKILL.md를 읽지 못했어요")
		}
		b.WriteString(prompt[last:m[0]])
		b.WriteString(clipRunes(string(raw), 30000))
		last = m[1]
	}
	b.WriteString(prompt[last:])
	return b.String(), nil
}

// assistModel is the model the owner picked in 모델 설정 for AI 도움, or the default when none is set or Arkism is unreachable.
func assistModel() string {
	picked := ""
	if page, err := runnerCall("/v1/providers?"+scopeQuery(), nil, 15*time.Second); err == nil {
		picked = sOr(obj(page["providers"])["assist_model"])
	}
	if modelID.MatchString(picked) {
		return picked
	}
	return defaultAssistModel()
}

// personaRun is a fill·polish·… assist: the fixed persona on the picked model (the default if it cannot run), inside work.
func personaRun(a map[string]any, runner runFn, work string) (map[string]any, error) {
	prompt := sOr(a["prompt"])
	if a["kind"] == "draft" {
		var err error
		if prompt, err = expandSkillRefs(prompt); err != nil {
			return nil, err
		}
	}
	persona := strings.TrimSpace(sOr(a["persona"]))
	if persona == "" {
		persona = assistPersona
	}
	persona = strings.TrimSpace(persona + "\n\n" + assistGuide[pyStr(a["kind"])])
	model := pyStr(a["model"])
	if !truthy(a["model"]) || !modelID.MatchString(model) {
		model = assistModel()
	}
	spec := map[string]any{"model": model, "effort": "medium", "instructions": persona, "task": prompt, "cwd": work}
	fallback := defaultAssistModel()
	if model == fallback {
		return runner(spec)
	}
	out, err := runner(spec)
	if err == nil && truthy(out["ok"]) {
		return out, nil
	}
	if err != nil && !isCrewErr(err) {
		return nil, err
	}
	// It could not run or answer (no CLI or key on this host): the default answers instead.
	return runner(withKey(spec, "model", fallback))
}

// assistResult is the result body for a run that finished, however it finished.
func assistResult(out map[string]any) map[string]any {
	status := "failed"
	if truthy(out["ok"]) {
		status = "done"
	}
	res := map[string]any{"status": status, "output": clipRunes(sOr(out["output"]), 20000)}
	if m := sOr(out["model"]); m != "" && modelID.MatchString(m) {
		res["model"] = m
	}
	if !truthy(out["ok"]) {
		msg := sOr(out["error"])
		if msg == "" {
			msg = "실행 실패"
		}
		res["error"] = clipRunes(msg, 500)
	}
	return res
}

var assistNow = time.Now

// answerAssist turns one assist record into the result body for POST /v1/crew/assists/{id}/result.
func answerAssist(rec map[string]any, runner runFn) (map[string]any, error) {
	if runner == nil {
		runner = func(s map[string]any) (map[string]any, error) { return runNodeFn(s) }
	}
	a := obj(rec["crew_assist"])
	switch a["kind"] {
	case "try":
		return tryAgent(rec, a, runner)
	case "install_skill":
		return installAssist(a), nil
	case "fix", "comfy":
		if assistLeft(rec, assistNow()) < 540 { // the fix needs up to eight minutes before the request's ten run out
			return failedResult("요청이 너무 오래 기다려서 실행하지 않았어요. 다시 눌러 주세요."), nil
		}
		return actionAssist(a, sOr(rec["id"])), nil
	}
	work, err := os.MkdirTemp("", "arkism-assist-")
	if err != nil {
		return nil, err
	}
	out, err := personaRun(a, runner, work)
	if err != nil {
		os.RemoveAll(work)
		if isCrewErr(err) {
			return failedResult(err.Error()), nil
		}
		return nil, err
	}
	res := assistResult(out)
	if kept := keepOutputs(work, sOr(rec["id"])); kept != "" {
		res["output"] = tailRunes(sOr(res["output"])+"\n\n만든 파일: "+kept, 20000)
	}
	return res, nil
}

// keepOutputs moves what a run made (images, slides, …) to OUTPUTS/<assist id>; it returns that folder, or "" when
// the run made nothing.
func keepOutputs(work, aid string) string {
	defer os.RemoveAll(work)
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) == 0 {
		return ""
	}
	dest := filepath.Join(outputsDir(), outputName(aid))
	if err := os.MkdirAll(outputsDir(), 0o777); err != nil {
		return ""
	}
	if os.Rename(work, dest) == nil {
		return dest
	}
	// Another file system (or the folder exists): copy instead.
	if copyTree(work, dest) != nil {
		return ""
	}
	return dest
}

func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case !d.Type().IsRegular():
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

var (
	assistMu   sync.Mutex
	assistBusy = map[string]bool{}
	// assistDone keeps answers whose report failed: the next round reports them again instead of running the work twice.
	assistDone = map[string]host.Obj{}
)

var assistOrder = []string{"status", "output", "model", "error"}

func assistJob(rec map[string]any, answer func(map[string]any) (map[string]any, error)) {
	if answer == nil {
		answer = func(r map[string]any) (map[string]any, error) { return answerAssist(r, nil) }
	}
	id := pyStr(rec["id"])
	defer func() {
		assistMu.Lock()
		delete(assistBusy, id)
		assistMu.Unlock()
	}()
	assistMu.Lock()
	body := assistDone[id]
	assistMu.Unlock()
	if body == nil {
		res, err := answer(rec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "AI 도움 응답 실패: %s\n", errName(err))
			return
		}
		body = append(host.Obj{{Key: "scope", Value: scope()}}, ordered(res, assistOrder...)...)
		assistMu.Lock()
		assistDone[id] = body
		assistMu.Unlock()
	}
	if _, err := runnerCall("/v1/crew/assists/"+pyQuote(id, "/")+"/result", body, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "AI 도움 응답 실패: %s\n", errName(err))
		return
	}
	assistMu.Lock()
	delete(assistDone, id)
	assistMu.Unlock()
}

// assistsRound starts every waiting assist this runner is not already working on.
func assistsRound() (int, error) {
	page, err := runnerCall("/v1/crew/assists?"+scopeQuery(), nil, 15*time.Second)
	if err != nil {
		return 0, err
	}
	started := 0
	for _, r := range list(page["items"]) {
		rec := obj(r)
		id := pyStr(rec["id"])
		assistMu.Lock()
		busy := assistBusy[id]
		assistBusy[id] = true
		assistMu.Unlock()
		if busy {
			continue
		}
		submit(func() { assistJob(rec, nil) })
		started++
	}
	return started, nil
}

// ---------------------------------------------------------------- old expert agent files
// Experts used to be installed as agent files (Claude Code subagents, Codex profiles, ZCode agents). Experts became
// workflows, so no such file is wanted any more: the ones this runner wrote (listed in the manifest) are removed,
// unless the owner changed them.

// syncInstalls removes the agent files this runner once wrote for experts; it returns how many were removed.
func syncInstalls(manifest string) (int, error) {
	if manifest == "" {
		manifest = stateFile("crew-installed.json")
	}
	mine, _, ok := readJSONObject(manifest)
	if !ok || len(mine) == 0 {
		return 0, nil
	}
	removed := 0
	// shortcut: entries kept after a failed read are rewritten in sorted order, not file order; only matters to a reader of the file.
	for _, path := range sortedKeys(mine) {
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil && digest(raw) == pyStr(mine[path]) {
			if os.Remove(path) != nil {
				continue
			}
			removed++
		}
		delete(mine, path)
	}
	if err := os.WriteFile(manifest, []byte(dumpIndent(mine)), 0o666); err != nil {
		return removed, err
	}
	return removed, nil
}

// tryAgent is 써보기 (and Discord's @이름 question): the work agent called by its handle answers once, read-only, on its
// model (auto: 라우팅 picks; its fallback model when the first fails). The call is counted for the agent.
func tryAgent(rec, a map[string]any, runner runFn) (map[string]any, error) {
	handle := strings.TrimPrefix(strings.TrimSpace(sOr(a["expert"])), "@")
	res, err := runnerCall("/v1/crew/resolve?"+scopeQuery("handle", "@"+handle), nil, 15*time.Second)
	item := obj(res["item"])
	c := obj(item["crew"])
	if err != nil || c["type"] != "agent" {
		return failedResult("@" + handle + " 작업 에이전트를 찾지 못했어요"), nil
	}
	if wf := strings.TrimPrefix(strings.TrimSpace(sOr(c["workflow"])), "$"); wf != "" {
		return agentWorkflow(wf, sOr(a["prompt"])), nil
	}
	model := strings.TrimSpace(sOr(c["model"]))
	if c["model_mode"] == "auto" || model == "" {
		model = "auto"
	}
	effort := sOr(c["effort"])
	if !in(efforts, effort) {
		effort = "medium"
	}
	instructions := strings.TrimSpace(sOr(c["role"]) + "\n\n" + sOr(c["instructions"]))
	if f := sOr(c["output_format"]); f == "json" || f == "markdown" {
		instructions += "\n\n답은 " + f + " 형식으로 써."
	}
	trigger := "manual"
	if rec["created_by_type"] == "service" {
		trigger = "discord"
	}
	spec := map[string]any{"model": model, "effort": effort, "instructions": instructions, "task": sOr(a["prompt"]), "once": true,
		"access": "read", "origin": map[string]any{"kind": "assist", "agent_id": sOr(item["id"]), "trigger": trigger}}
	out, err := runAgentSpec(spec, runner)
	if (err != nil || out["ok"] != true) && truthy(c["fallback_model"]) {
		out, err = runAgentSpec(withKey(spec, "model", sOr(c["fallback_model"])), runner)
	}
	if err != nil {
		if isCrewErr(err) {
			return failedResult(err.Error()), nil
		}
		return nil, err
	}
	return assistResult(out), nil
}

// agentWorkflow runs the workflow an agent works by ($handle) with the caller's prompt, and reports it like an answer.
// A run that outlasts the wait is left running; the result says so with its id.
func agentWorkflow(handle, prompt string) map[string]any {
	view, err := startWorkflow(handle, prompt, 120, "", nil)
	if err != nil {
		return failedResult(err.Error())
	}
	switch status := pyStr(view["status"]); status {
	case "succeeded":
		return map[string]any{"status": "done", "output": clipRunes(pyStr(view["summary"]), 20000)}
	case "failed", "stopped":
		return failedResult("$" + handle + " 워크플로우가 " + map[string]string{"failed": "실패했어요", "stopped": "멈췄어요"}[status])
	}
	return map[string]any{"status": "done", "output": "$" + handle + " 워크플로우가 아직 도는 중이에요 (실행 " + pyStr(view["run_id"]) + ")"}
}

// runAgentSpec runs one agent call and counts it (routed calls count themselves).
func runAgentSpec(spec map[string]any, runner runFn) (map[string]any, error) {
	out, err := runner(spec)
	if err != nil {
		if isCrewErr(err) {
			reportFixed(spec, failed(sOr(spec["model"]), err.Error()))
		}
		return nil, err
	}
	if out["ok"] == true && out["usage"] == nil {
		out["usage"] = usageOf(spec, out)
	}
	if out["routed"] == nil {
		reportFixed(spec, out)
	}
	return out, nil
}
