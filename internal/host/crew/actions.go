package crew

// Two AI-help actions the host runner carries out for the desktop (answerAssist):
//
//	fix    코드 리뷰 › AI로 고치기: clone an open PR's branch, let Claude Code change the flagged spot, commit and
//	       push to that same branch (never a default branch). CI then reviews the new commit.
//	comfy  이미지 › ComfyUI로 만들기: queue an API-format workflow on a ComfyUI server, wait, and keep the images
//	       under the runner's outputs folder.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const (
	fixTimeout = 480 * time.Second // under the ten-minute life of an assist request
	// Files inside the clone only, and no shell: the PR's own code (tests, npm scripts) never runs here; CI runs it.
	fixTools   = "Read(./**),Edit(./**)" // Edit rules cover every file-writing tool
	fixDenied  = "Bash,WebFetch,WebSearch,Glob,Grep"
	fixPersona = "너는 코드 리뷰 지적을 고치는 개발자다. 지적된 곳만 최소한으로 고치고, 관련 테스트가 있으면 돌려서 통과시킨다. " +
		"지적이 틀렸다고 판단되면 아무것도 바꾸지 말고 그 이유만 한두 문장으로 말한다. 커밋·푸시는 하지 않고, 저장소 밖 파일은 읽지 않는다."
)

var (
	repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	shaRe  = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
)

func fixClaude() string {
	return host.Env("ARKISM_CLAUDE_BIN", filepath.Join(host.Home, ".local/bin/claude"))
}

func fixModel() string { return host.Env("ARKISM_FIX_MODEL", "claude-sonnet-5-5") }

// ActionError is a refusal with a Korean message that is safe to show.
type ActionError struct{ Msg string }

func (e *ActionError) Error() string { return e.Msg }

func actionErr(msg string) error { return &ActionError{msg} }

// runFunc runs argv in cwd with stdin and returns stdout; a non-zero exit is an *ActionError.
type runFunc func(argv []string, cwd string, timeout time.Duration, stdin string) (string, error)

func actionRun(argv []string, cwd string, timeout time.Duration, stdin string) (string, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", err
		}
		text := strings.TrimSpace(errOut.String())
		if text == "" {
			text = strings.TrimSpace(out.String())
		}
		lines := pySplitlines(text)
		last := "실패"
		if len(lines) > 0 {
			last = lines[len(lines)-1]
		}
		return "", actionErr(clipRunes(last, 300))
	}
	return out.String(), nil
}

// ---------------------------------------------------------------- 코드 리뷰 › AI로 고치기

// prBranch is the head branch of an open PR whose branch lives in the same repository; it refuses anything else.
func prBranch(repo string, number int64, gh runFunc, expected string) (string, error) {
	raw, err := gh([]string{"gh", "api", fmt.Sprintf("repos/%s/pulls/%d", repo, number)}, "", 0, "")
	if err != nil {
		return "", err
	}
	v, err := host.Loads(raw)
	if err != nil {
		return "", err
	}
	pr, _ := v.(map[string]any)
	head, _ := pr["head"].(map[string]any)
	base, _ := pr["base"].(map[string]any)
	branch, _ := head["ref"].(string)
	headRepo, _ := head["repo"].(map[string]any)
	if pr["state"] != "open" || headRepo["full_name"] != repo {
		return "", actionErr("열린 PR이 아니거나 다른 저장소의 브랜치예요")
	}
	baseRepo, _ := base["repo"].(map[string]any)
	if branch == "" || same(branch, base["ref"]) || same(branch, baseRepo["default_branch"]) {
		return "", actionErr("기본 브랜치에는 고친 걸 올리지 않아요")
	}
	if expected != "" && head["sha"] != expected {
		return "", actionErr("확인한 뒤 PR 커밋이 바뀌었어요. 다시 검토해 주세요")
	}
	return branch, nil
}

func fixPrompt(repo string, number, line int64, path, text string) string {
	return fmt.Sprintf("저장소 %s PR #%d의 AI 리뷰 지적을 고쳐 줘.\n\n파일: %s:%d\n지적: %s\n\n"+
		"지적된 곳을 읽고 최소한으로 고친 뒤, 무엇을 바꿨는지 한두 문장으로 말해 줘. 테스트는 CI가 돌려요.", repo, number, path, line, text)
}

func pyIntArg(v any) (int64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	if n, ok := host.Int(v); ok {
		return n, true
	}
	if f, ok := num(v); ok && !math.IsNaN(f) && f < 9.2e18 && f > -9.2e18 {
		return int64(f), true
	}
	return 0, false
}

// fixFinding fixes one review finding on its PR branch and returns a short Korean report.
// run does every process; claude, when set, runs only the model.
func fixFinding(spec map[string]any, run, claude runFunc) (bool, string, error) {
	repo, path := pyStr(spec["repo"]), pyStr(spec["path"])
	if !truthy(spec["repo"]) {
		repo = ""
	}
	if !truthy(spec["path"]) {
		path = ""
	}
	number, ok1 := pyIntArg(spec["number"])
	lineArg := spec["line"]
	if !truthy(lineArg) {
		lineArg = 0
	}
	line, ok2 := pyIntArg(lineArg)
	if !ok1 || !ok2 {
		return false, "", actionErr("PR 번호가 올바르지 않아요")
	}
	text := ""
	if truthy(spec["text"]) {
		text = pyStr(spec["text"])
	}
	bad := path == "" || strings.HasPrefix(path, "/") || strings.TrimSpace(text) == ""
	for _, part := range strings.Split(path, "/") {
		bad = bad || part == ".."
	}
	if !repoRe.MatchString(repo) || bad {
		return false, "", actionErr("지적 정보가 올바르지 않아요")
	}
	expected := ""
	if truthy(spec["expected_head_sha"]) {
		expected = pyStr(spec["expected_head_sha"])
	}
	if expected != "" && !shaRe.MatchString(expected) {
		return false, "", actionErr("확인한 커밋 해시가 올바르지 않아요")
	}
	branch, err := prBranch(repo, number, run, expected)
	if err != nil {
		return false, "", err
	}
	work, err := os.MkdirTemp("", "arkism-fix-")
	if err != nil {
		return false, "", err
	}
	defer os.RemoveAll(work)
	if _, err := run([]string{"gh", "repo", "clone", repo, work, "--", "--depth", "50", "--branch", branch}, "", 180*time.Second, ""); err != nil {
		return false, "", err
	}
	git := func(timeout time.Duration, args ...string) (string, error) {
		return run(append([]string{"git"}, args...), work, timeout, "")
	}
	if expected != "" {
		head, err := git(0, "rev-parse", "HEAD")
		if err != nil {
			return false, "", err
		}
		if strings.TrimSpace(head) != expected {
			return false, "", actionErr("가져오는 동안 PR 커밋이 바뀌었어요. 다시 검토해 주세요")
		}
	}
	argv := []string{fixClaude(), "-p", "--model", fixModel(), "--permission-mode", "acceptEdits", "--allowedTools", fixTools,
		"--disallowedTools", fixDenied, "--append-system-prompt", fixPersona,
		// The PR's own .claude settings (hooks run commands) and .mcp.json are not trusted: user settings only, no MCP.
		"--setting-sources", "user", "--strict-mcp-config"}
	if claude == nil {
		claude = run
	}
	said, err := claude(argv, work, fixTimeout, fixPrompt(repo, number, line, path, text))
	if err != nil {
		return false, "", err
	}
	said = strings.TrimSpace(said)
	status, err := git(0, "status", "--porcelain")
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(status) == "" {
		return false, clipRunes("바꾼 곳이 없어요. "+said, 1000), nil
	}
	if _, err := git(0, "add", "-A"); err != nil {
		return false, "", err
	}
	if _, err := git(0, "commit", "-q", "-m", fmt.Sprintf("AI로 고치기: %s:%d\n\n%s", path, line, clipRunes(pyStr(spec["text"]), 500))); err != nil {
		return false, "", err
	}
	if expected != "" {
		again, err := prBranch(repo, number, run, expected)
		if err != nil {
			return false, "", err
		}
		if again != branch {
			return false, "", actionErr("확인한 PR 브랜치가 바뀌었어요. 다시 검토해 주세요")
		}
	}
	if _, err := git(120*time.Second, "push", "-q", "origin", "HEAD:"+branch); err != nil {
		return false, "", err
	}
	sha, err := git(0, "rev-parse", "--short", "HEAD")
	if err != nil {
		return false, "", err
	}
	return true, clipRunes(fmt.Sprintf("%s에 커밋 %s를 올렸어요. CI가 다시 검사해요.\n\n%s", branch, strings.TrimSpace(sha), said), 2000), nil
}

// ---------------------------------------------------------------- 이미지 › ComfyUI로 만들기

// comfyClient talks to ComfyUI directly (no environment proxy: it is usually on this machine or the tailnet).
var comfyClient = &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: nil}

func comfyHTTP(target string, data any, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	method, body, ct := http.MethodGet, io.Reader(nil), ""
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		method, body, ct = http.MethodPost, bytes.NewReader(raw), "application/json"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := comfyClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &host.HTTPError{Code: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

// errorKind names a connection failure the way Python's urllib names it.
func errorKind(err error) string {
	var he *host.HTTPError
	var ne net.Error
	switch {
	case errors.As(err, &he):
		return "HTTPError"
	case errors.As(err, &ne) && ne.Timeout():
		return "TimeoutError"
	}
	return "URLError"
}

// comfyWorkflow is the API-format workflow (nodes with class_type) from the request.
func comfyWorkflow(spec map[string]any) (map[string]any, error) {
	wf := spec["workflow"]
	if s, ok := wf.(string); ok {
		v, err := host.Loads(s)
		if err != nil {
			return nil, actionErr("워크플로가 JSON이 아니에요")
		}
		wf = v
	}
	nodes, ok := wf.(map[string]any)
	valid := ok && len(nodes) > 0
	for _, n := range nodes {
		node, isMap := n.(map[string]any)
		_, has := node["class_type"]
		valid = valid && isMap && has
	}
	if !valid {
		return nil, actionErr("API 형식 워크플로가 필요해요. ComfyUI에서 'Export (API)'로 저장한 걸 넣어 주세요")
	}
	return nodes, nil
}

func comfyWait(base, pid string, poll, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := comfyHTTP(base+"/history/"+pyQuote(pid, "/"), nil, 0)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		v, err := host.Loads(string(raw))
		if err != nil {
			return nil, err
		}
		all, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("history is not an object")
		}
		if hist := all[pid]; truthy(hist) {
			if m, ok := hist.(map[string]any); ok {
				return m, nil
			}
			return nil, fmt.Errorf("history entry is not an object")
		}
		time.Sleep(poll)
	}
	return nil, actionErr("ComfyUI가 시간 안에 끝내지 못했어요")
}

var unsafeSub = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func comfySave(base string, images []map[string]any, outdir string) ([]string, error) {
	if err := os.MkdirAll(outdir, 0o777); err != nil {
		return nil, err
	}
	saved := []string{}
	if len(images) > 20 {
		images = images[:20]
	}
	for _, img := range images {
		filename := ""
		if truthy(img["filename"]) {
			filename = pyStr(img["filename"])
		}
		name := filepath.Base(filename)
		if filename == "" || name == "/" || name == "." {
			continue
		}
		// Same file name in different ComfyUI subfolders (or twice): keep each one.
		subRaw := ""
		if truthy(img["subfolder"]) {
			subRaw = pyStr(img["subfolder"])
		}
		if sub := strings.Trim(unsafeSub.ReplaceAllString(subRaw, "-"), "-"); sub != "" {
			name = sub + "-" + name
		}
		for b, n := name, 1; exists(filepath.Join(outdir, name)); n++ { // never overwrite an image saved a moment ago
			name = fmt.Sprintf("%02d-%s", n, b)
		}
		kind := "output"
		if truthy(img["type"]) {
			kind = pyStr(img["type"])
		}
		q := url.Values{"filename": {pyStr(img["filename"])}, "subfolder": {subRaw}, "type": {kind}}
		data, err := comfyHTTP(base+"/view?"+q.Encode(), nil, 60*time.Second)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(outdir, name)
		if err := os.WriteFile(target, data, 0o666); err != nil {
			return nil, err
		}
		saved = append(saved, target)
	}
	return saved, nil
}

var comfyURLRe = regexp.MustCompile(`^https?://[^/\s]+$`)

// comfyGenerate queues an API-format workflow on ComfyUI, waits for it and saves its images in outdir.
func comfyGenerate(spec map[string]any, outdir string, poll, timeout time.Duration) ([]string, error) {
	base := ""
	if truthy(spec["url"]) {
		base = strings.TrimRight(pyStr(spec["url"]), "/")
	}
	if !comfyURLRe.MatchString(base) {
		return nil, actionErr("ComfyUI 주소가 올바르지 않아요 (예: http://127.0.0.1:8188)")
	}
	wf, err := comfyWorkflow(spec)
	if err != nil {
		return nil, err
	}
	raw, err := comfyHTTP(base+"/prompt", host.Obj{{Key: "prompt", Value: wf}, {Key: "client_id", Value: "arkism"}}, 0)
	if err != nil {
		return nil, actionErr(fmt.Sprintf("ComfyUI에 연결하지 못했어요 (%s)", errorKind(err)))
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		return nil, err
	}
	answer, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("prompt answer is not an object")
	}
	if !truthy(answer["prompt_id"]) {
		return nil, actionErr("ComfyUI가 작업을 받지 않았어요")
	}
	pid := pyStr(answer["prompt_id"])
	hist, err := comfyWait(base, pid, poll, timeout)
	if err != nil {
		return nil, err
	}
	images := []map[string]any{}
	outputs, _ := hist["outputs"].(map[string]any)
	for _, k := range sortedKeys(outputs) { // shortcut: Python keeps the server's node order; Go maps do not, so node ids sort
		out, _ := outputs[k].(map[string]any)
		list, _ := out["images"].([]any)
		for _, img := range list {
			if m, ok := img.(map[string]any); ok {
				images = append(images, m)
			}
		}
	}
	if len(images) == 0 {
		return nil, actionErr("ComfyUI가 이미지를 내놓지 않았어요")
	}
	return comfySave(base, images, outdir)
}
