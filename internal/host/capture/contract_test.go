package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const contractFixture = "# 작업 계약\n\n## 문제\n세션 시작 때 기억을 너무 많이 넣는다.\n\n## 완료 확인\n```sh\n# 주석은 실행하지 않는다\n%s\n%s\n```\n\n## 예산\n1시간\n"

type contractEnv struct {
	t    *testing.T
	base string
	repo string
}

func newContractEnv(t *testing.T) *contractEnv {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	oldState, oldChecks := contractState, contractChecks
	contractState, contractChecks = filepath.Join(base, "state"), true
	t.Cleanup(func() { contractState, contractChecks = oldState, oldChecks })
	return &contractEnv{t, base, repo}
}

func (e *contractEnv) write(first, second string) {
	os.MkdirAll(filepath.Join(e.repo, ".arkism"), 0o777)
	if err := os.WriteFile(filepath.Join(e.repo, ".arkism/contract.md"), []byte(fmt.Sprintf(contractFixture, first, second)), 0o666); err != nil {
		e.t.Fatal(err)
	}
}

func (e *contractEnv) hook(event string) map[string]any {
	in, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": "s1", "cwd": filepath.Join(e.repo, ".arkism")})
	var out, errb bytes.Buffer
	if code := contractHook(bytes.NewReader(in), &out, &errb); code != 0 {
		e.t.Fatalf("hook exit %d: %s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) == "" {
		return nil
	}
	m, err := loadObj(out.Bytes())
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *contractEnv) results() []string {
	raw, _ := os.ReadFile(filepath.Join(contractState, "results.jsonl"))
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if m, err := loadObj([]byte(line)); err == nil {
			out = append(out, sstr(m["result"]))
		}
	}
	return out
}

func TestContractOnlyTheCodeBlockUnderTheDoneHeadingIsRun(t *testing.T) {
	text := fmt.Sprintf(contractFixture, "python3 tools/check_local.py", "go vet ./...") + "\n## 메모\n```sh\nrm -rf /\n```\n"
	if got := contractCommands(text); !reflect.DeepEqual(got, []string{"python3 tools/check_local.py", "go vet ./..."}) {
		t.Fatal(got)
	}
	if got := contractCommands("# 계약\n## 완료 확인\n없음\n## 예산\n```sh\nls\n```"); len(got) != 0 {
		t.Fatal(got)
	}
	if got := contractCommands("no contract"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestContractCommandsGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/contract_commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text     string
		Commands []string
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := contractCommands(c.Text); !reflect.DeepEqual(got, c.Commands) {
			t.Errorf("%q: got %q want %q", c.Text, got, c.Commands)
		}
	}
}

func TestContractSessionStartInjectsTheContractFromAnyFolderOfTheRepo(t *testing.T) {
	e := newContractEnv(t)
	e.write("true", "echo ok")
	ctx := sstr(obj(e.hook("SessionStart")["hookSpecificOutput"])["additionalContext"])
	if !strings.Contains(ctx, "작업 계약") || !strings.Contains(ctx, "세션 시작 때 기억을 너무 많이 넣는다.") {
		t.Fatal(ctx)
	}
}

func TestContractNoContractInjectsNothingAndNeverBlocks(t *testing.T) {
	e := newContractEnv(t)
	if e.hook("SessionStart") != nil || e.hook("Stop") != nil {
		t.Fatal("expected no output")
	}
}

func TestContractPassingChecksLetTheSessionStop(t *testing.T) {
	e := newContractEnv(t)
	e.write("true", "echo ok")
	e.hook("SessionStart")
	if e.hook("Stop") != nil {
		t.Fatal("blocked")
	}
	if got := e.results(); !reflect.DeepEqual(got, []string{"pass"}) {
		t.Fatal(got)
	}
}

func TestContractFailingCheckBlocksWithItsOutputThenGivesUp(t *testing.T) {
	e := newContractEnv(t)
	e.write("true", "echo 테스트 실패 >&2; exit 3")
	e.hook("SessionStart")
	for _, left := range []int{2, 1, 0} {
		out := e.hook("Stop")
		reason := sstr(out["reason"])
		if out["decision"] != "block" || !strings.Contains(reason, "2번째 명령") || !strings.Contains(reason, "테스트 실패") ||
			!strings.Contains(reason, fmt.Sprintf("남은 차단 %d회", left)) || !strings.Contains(reason, "exit 3\n") {
			t.Fatal(out)
		}
	}
	if e.hook("Stop") != nil { // no endless loop
		t.Fatal("still blocking")
	}
	if got := e.results(); !reflect.DeepEqual(got, []string{"fail", "fail", "fail", "gave_up"}) {
		t.Fatal(got)
	}
	raw, _ := os.ReadFile(filepath.Join(contractState, "results.jsonl"))
	if strings.Contains(string(raw), "테스트 실패") { // output never goes to the log
		t.Fatal(string(raw))
	}
}

func TestContractTheCheckIsTheContractAgreedAtSessionStart(t *testing.T) {
	e := newContractEnv(t)
	e.write("true", "exit 1")
	e.hook("SessionStart")
	e.write("true", "true") // the agent weakens the contract mid-session
	if e.hook("Stop")["decision"] != "block" {
		t.Fatal("not blocked")
	}
}

func TestContractResumeOrCompactKeepsTheAgreedCheckAndTheBlockCount(t *testing.T) {
	e := newContractEnv(t)
	e.write("true", "exit 1")
	e.hook("SessionStart")
	if r := sstr(e.hook("Stop")["reason"]); !strings.Contains(r, "남은 차단 2회") {
		t.Fatal(r)
	}
	e.write("true", "true")
	ctx := sstr(obj(e.hook("SessionStart")["hookSpecificOutput"])["additionalContext"])
	if !strings.Contains(ctx, "exit 1") {
		t.Fatal(ctx)
	}
	if r := sstr(e.hook("Stop")["reason"]); !strings.Contains(r, "남은 차단 1회") {
		t.Fatal(r)
	}
	// the session copy keeps Python's key order: root, contract, blocks
	path, _ := contractSessionFile("s1")
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), `{"root": `) || !strings.HasSuffix(string(raw), `, "blocks": 2}`) {
		t.Fatal(string(raw))
	}
}

func TestContractChecksRunOnlyWhenTurnedOn(t *testing.T) {
	e := newContractEnv(t)
	e.write("true", "exit 1")
	e.hook("SessionStart")
	contractChecks = false
	if e.hook("Stop") != nil {
		t.Fatal("blocked while checks are off")
	}
}

func TestContractTimeLimitCoversAllCommands(t *testing.T) {
	e := newContractEnv(t)
	ok, failed, output, err := contractRunChecks([]string{"sleep 5"}, e.repo, time.Second)
	if err != nil || ok || failed != 1 || !strings.Contains(output, "시간 초과") || !strings.Contains(output, "1초") {
		t.Fatal(ok, failed, output, err)
	}
}

func TestContractChildrenOfACheckDoNotOutliveIt(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	e := newContractEnv(t)
	pidfile := filepath.Join(e.base, "child.pid")
	ok, failed, _, err := contractRunChecks([]string{fmt.Sprintf("sleep 30 > /dev/null 2>&1 & echo $! > %s; wait", pidfile)}, e.repo, time.Second)
	if err != nil || ok || failed != 1 {
		t.Fatal(ok, failed, err)
	}
	raw, _ := os.ReadFile(pidfile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	time.Sleep(200 * time.Millisecond)
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if fields[0] != "Z" {
			t.Fatal("the background child is still running")
		}
	}
}

func TestContractSessionStartTruncatesAndCheckCommand(t *testing.T) {
	e := newContractEnv(t)
	long := strings.Repeat("가", 5000)
	e.write("true", long)
	ctx := sstr(obj(e.hook("SessionStart")["hookSpecificOutput"])["additionalContext"])
	note, body, _ := strings.Cut(ctx, "\n")
	if !strings.HasPrefix(note, "작업 계약(") || host.Len(body) != contractMaxChars {
		t.Fatal(host.Len(body))
	}
	if code := contractMain(nil, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatal(code)
	}
	var errb bytes.Buffer
	contractMain([]string{"x"}, nil, &bytes.Buffer{}, &errb)
	if !strings.HasPrefix(errb.String(), "Task contract hook:") || !strings.HasSuffix(errb.String(), "command output.\n\n") {
		t.Fatal(errb.String())
	}
}

func TestContractNonStringSessionIsLoggedAsHookError(t *testing.T) {
	newContractEnv(t)
	var out, errb bytes.Buffer
	if code := contractHook(strings.NewReader(`{"hook_event_name":"Stop","session_id":5}`), &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatal(code, out.String())
	}
	raw, _ := os.ReadFile(filepath.Join(contractState, "results.jsonl"))
	if !strings.Contains(string(raw), `"event": "hook_error", "hook": "Stop", "error": "TypeError"`) {
		t.Fatal(string(raw))
	}
	if code := contractHook(strings.NewReader("not json"), &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatal(code)
	}
}
