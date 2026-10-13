package capture

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

func winFixture(t *testing.T) string {
	dir := t.TempDir()
	state, run, send, spawn := winState, winRun, winSendHook, winSpawnExport
	t.Cleanup(func() { winState, winRun, winSendHook, winSpawnExport = state, run, send, spawn })
	winState = dir
	winSpawnExport = func() {}
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_WORK_SELECTION_STATE", filepath.Join(dir, "selections"))
	// No app connection file: an Arkism app installed on the test machine must not pick the SSH host.
	t.Setenv("ARKISM_AGENT_CONNECTION", filepath.Join(dir, "no-agents.json"))
	return dir
}

func winJSONFiles(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

type winCall struct {
	argv  []string
	input []byte
}

func winResults(calls *[]winCall, outputs ...string) func([]string, []byte, time.Duration) (int, []byte, error) {
	return func(argv []string, input []byte, _ time.Duration) (int, []byte, error) {
		*calls = append(*calls, winCall{argv, input})
		out := outputs[0]
		if len(outputs) > 1 {
			outputs = outputs[1:]
		}
		return 0, []byte(out), nil
	}
}

func TestWinZcodeMissingSessionKeepsContextAndNextHookBinds(t *testing.T) {
	root := winFixture(t)
	database := filepath.Join(root, "db.sqlite")
	db, err := sqlite.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE session(id TEXT, directory TEXT, parent_id TEXT, time_updated INTEGER);" +
		"CREATE TABLE message(id TEXT, session_id TEXT, data TEXT);CREATE TABLE part(id TEXT, message_id TEXT, data TEXT);"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	roots := pcHookRoots
	t.Cleanup(func() { pcHookRoots = roots })
	pcHookRoots = map[string]string{"zcode": database}
	winState = filepath.Join(root, "state")
	sends := 0
	winSendHook = func(string, []byte) ([]byte, error) { sends++; return []byte("current context"), nil }
	winRun = func([]string, []byte, time.Duration) (int, []byte, error) { return 1, nil, nil }
	event := host.Obj{{Key: "hook_event_name", Value: "SessionStart"}, {Key: "session_id", Value: "new"}, {Key: "cwd", Value: "C:/repo"}, {Key: "arkism_work_id", Value: "work"}}
	out, err := winHook("zcode", []byte(host.MustDumps(event, host.DumpOpts{})), io.Discard)
	if err != nil || string(out) != "current context" || sends != 1 {
		t.Fatal(string(out), err, sends)
	}
	workRoot := winWorkRoot("zcode")
	sessions, _ := filepath.Glob(filepath.Join(workRoot, "session-*.json"))
	if len(winWorkFiles(workRoot)) != 0 || len(sessions) != 0 {
		t.Fatal("recorded a missing session")
	}
	db, _ = sqlite.Open(database)
	db.Exec("INSERT INTO session VALUES ('new','C:/repo',NULL,1000)")
	db.Close()
	event.Set("hook_event_name", "UserPromptSubmit")
	event.Set("prompt", "continue")
	if out, err := winHook("zcode", []byte(host.MustDumps(event, host.DumpOpts{})), io.Discard); err != nil || string(out) != "current context" {
		t.Fatal(string(out), err)
	}
	signals := winWorkFiles(workRoot)
	if len(signals) != 1 {
		t.Fatal(signals)
	}
	raw, _ := os.ReadFile(signals[0])
	signal, _ := loadObjBytes(raw)
	b := signal["boundary"].(map[string]any)
	if signal["action"] != "bind" || signal["work_id"] != "work" || b["session_id"] != "new" || len(b["records"].(map[string]any)) != 0 {
		t.Fatal(signal)
	}
	winHook("zcode", []byte(host.MustDumps(event, host.DumpOpts{})), io.Discard)
	if len(winWorkFiles(workRoot)) != 1 {
		t.Fatal(winWorkFiles(workRoot))
	}
}

func TestWinZeroExitWithoutStorageReceiptKeepsNewAndRetriedPrompts(t *testing.T) {
	dir := winFixture(t)
	prompt := []byte(`{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "durable preference"}`)
	startup := []byte(`{"hook_event_name": "SessionStart", "session_id": "s"}`)
	context := `{"hookSpecificOutput":{"additionalContext":"current"}}` + "\n"
	failedAck := `{"arkismDelivery":{"accepted":false}}` + "\n"
	successAck := `{"arkismDelivery":{"accepted":true}}` + "\n"
	var calls []winCall
	winRun = winResults(&calls, context)
	if _, err := winHook("codex", prompt, io.Discard); err == nil {
		t.Fatal("no error")
	}
	if len(winJSONFiles(dir)) != 1 {
		t.Fatal(winJSONFiles(dir))
	}
	winRun = winResults(&calls, context, failedAck)
	if out, err := winHook("codex", startup, io.Discard); err != nil || string(out) != context {
		t.Fatal(string(out), err)
	}
	if len(winJSONFiles(dir)) != 1 {
		t.Fatal(winJSONFiles(dir))
	}
	winRun = winResults(&calls, context, successAck)
	if out, err := winHook("codex", startup, io.Discard); err != nil || string(out) != context {
		t.Fatal(string(out), err)
	}
	if len(winJSONFiles(dir)) != 0 {
		t.Fatal(winJSONFiles(dir))
	}
	winRun = winResults(&calls, context+successAck)
	if out, err := winSendHook("codex", prompt); err != nil || string(out) != strings.TrimSuffix(context, "\n") {
		t.Fatal(string(out), err)
	}
}

func TestWinOfflinePromptSurvivesThenRetriesWithoutReturningOldContext(t *testing.T) {
	dir := winFixture(t)
	first := []byte(host.MustDumps(host.Obj{{Key: "hook_event_name", Value: "UserPromptSubmit"}, {Key: "session_id", Value: "s"}, {Key: "prompt", Value: "앞으로 짧게 답해"}}, host.DumpOpts{}))
	startup := []byte(`{"hook_event_name": "SessionStart", "session_id": "s"}`)
	var stored []byte
	winSendHook = func(_ string, data []byte) ([]byte, error) { stored = data; return nil, errTimeoutExpired }
	if _, err := winHook("codex", first, io.Discard); !errors.Is(err, errTimeoutExpired) {
		t.Fatal(err)
	}
	if len(winJSONFiles(dir)) != 1 || len(winJSONFiles(filepath.Join(dir, "spool", "zcode"))) != 0 {
		t.Fatal(winJSONFiles(dir))
	}
	var sent [][]byte
	replies := []string{"new context", "old context"}
	winSendHook = func(origin string, data []byte) ([]byte, error) {
		sent = append(sent, data)
		r := replies[0]
		replies = replies[1:]
		return []byte(r), nil
	}
	if out, err := winHook("codex", startup, io.Discard); err != nil || string(out) != "new context" {
		t.Fatal(string(out), err)
	}
	if len(sent) != 2 || string(sent[1]) != string(stored) {
		t.Fatal(sent)
	}
	body, _ := loadObjBytes(stored)
	if _, isFloat := body[pcTime]; !isFloat || !strings.Contains(string(body[pcTime].(json.Number)), ".") {
		t.Fatal(body)
	}
	delete(body, pcTime)
	want, _ := loadObjBytes(first)
	if !eq(body, want) || len(winJSONFiles(dir)) != 0 {
		t.Fatal(body)
	}
	hook, _ := winSSHCommand("zcode", "hook")
	mcp, _ := winSSHCommand("claude_code", "mcp")
	if !strings.Contains(hook[len(hook)-1], "arkism-capture-pc-zcode") || !strings.Contains(mcp[len(mcp)-1], "ARKISM_RUNTIME=claude_code") ||
		strings.Contains(mcp[len(mcp)-1], "ARKISM_SKILLS_RECEIVE_ONLY") {
		t.Fatal(hook, mcp)
	}
	if _, err := winSSHCommand("codex; echo unsafe", "hook"); err == nil {
		t.Fatal("unsafe origin accepted")
	}
}

func TestWinSSHCommandsLaunchTheServerBinary(t *testing.T) {
	t.Setenv("ARKISM_AGENT_CONNECTION", filepath.Join(t.TempDir(), "none.json"))
	t.Setenv("ARKISM_SERVER_BIN", "")
	os.Unsetenv("ARKISM_SERVER_BIN")
	t.Setenv("ARKISM_SERVER_STATE", "/state")
	t.Setenv("ARKISM_SSH_HOST", "host")
	want := map[string]string{
		"hook":       "env ARKISM_ORIGIN=codex ARKISM_CAPTURE_ON_PROMPT=1 ARKISM_CAPTURE_ACK=1 ARKISM_CLIENT=pc ARKISM_CAPTURE_STATE=/state/arkism-capture-pc-codex /srv/arkism/bin/arkism capture hook",
		"work-event": "env ARKISM_ORIGIN=codex ARKISM_CLIENT=pc ARKISM_CAPTURE_STATE=/state/arkism-capture-pc-codex /srv/arkism/bin/arkism work-pc-receiver",
		"mcp":        "env ARKISM_RUNTIME=codex ARKISM_CLIENT=pc /srv/arkism/bin/arkism skill-router",
	}
	for mode, remote := range want {
		argv, err := winSSHCommand("codex", mode)
		if err != nil || argv[len(argv)-1] != remote || argv[len(argv)-2] != "host" {
			t.Fatal(argv, err)
		}
	}
	argv, _ := winSSHCommand("zcode", "archive-retention")
	if argv[len(argv)-1] != "/srv/arkism/bin/arkism archive-retention" {
		t.Fatal(argv)
	}
	for _, origin := range []string{"gemini", "grok", "deepseek"} {
		argv, _ := winSSHCommand(origin, "mcp")
		if !strings.Contains(argv[len(argv)-1], "ARKISM_RUNTIME="+origin) || !strings.Contains(argv[len(argv)-1], "ARKISM_CLIENT=pc") {
			t.Fatal(argv)
		}
	}
}

// Ported from the receiver tests: exit 0 without a receipt is not an acknowledgement.
func TestWinSSHSenderPreservesBytesAndDoesNotTreatExitZeroAsReceipt(t *testing.T) {
	f := newReceiverFixture(t)
	winFixture(t)
	var calls []winCall
	winRun = winResults(&calls, "{}")
	_, err := outboxDrain(f.client, func(raw []byte) (any, error) { return winSendWorkEvent("codex", raw) }, 1)
	boundaryWantErr(t, err, "unacknowledged")
	if len(calls) != 1 || string(calls[0].input) != string(f.raw) || !strings.HasSuffix(calls[0].argv[len(calls[0].argv)-1], " work-pc-receiver") {
		t.Fatal(calls)
	}
	if len(outboxFiles(f.client)) != 1 {
		t.Fatal("event dropped")
	}
}

func TestWinDrainWorkReportsShape(t *testing.T) {
	winFixture(t)
	t.Setenv("ARKISM_SSH_HOST", "x")
	var out strings.Builder
	if code := winMain([]string{"drain-work", "codex", "--ssh-host", "h"}, strings.NewReader(""), &out, io.Discard); code != 0 ||
		out.String() != `{"acknowledged": 0, "expired": 0, "remaining": 0, "failed": false, "expired_prompts": 0}`+"\n" {
		t.Fatal(code, out.String())
	}
	if code := winMain([]string{"drain-work", "gemini"}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatal(code)
	}
}

// With the app's built-in Arkism running, agents talk to it in this program instead of over SSH;
// a connection file that points anywhere but loopback is ignored.
func TestWinLocalConnectionRunsHereInsteadOfSSH(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "owner.token")
	if err := os.WriteFile(token, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	conn := filepath.Join(dir, "agents.json")
	t.Setenv("ARKISM_AGENT_CONNECTION", conn)
	t.Setenv("ARKISM_SSH_HOST", "host")
	for _, k := range []string{"ARKISM_PC_LOCAL", "ARKISM_URL", "ARKISM_TOKEN_FILE", "ARKISM_ORIGIN", "ARKISM_CLIENT", "ARKISM_CAPTURE_ON_PROMPT", "ARKISM_CAPTURE_ACK", "ARKISM_CAPTURE_STATE", "ARKISM_RUNTIME"} {
		t.Setenv(k, "")
	}
	write := func(v string) {
		if err := os.WriteFile(conn, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"mode":"local","url":"http://127.0.0.1:8091","token_file":` + strconv.Quote(token) + `}`)
	argv, err := winSSHCommand("codex", "hook")
	if err != nil || len(argv) != 3 || argv[1] != "capture" || argv[2] != "hook" {
		t.Fatal(argv, err)
	}
	if os.Getenv("ARKISM_URL") != "http://127.0.0.1:8091" || os.Getenv("ARKISM_TOKEN_FILE") != token || os.Getenv("ARKISM_ORIGIN") != "codex" || os.Getenv("ARKISM_CLIENT") != "pc" || os.Getenv("ARKISM_PC_LOCAL") != "1" {
		t.Fatal("local settings not passed to the child")
	}
	if argv, err := winSSHCommand("claude_code", "mcp"); err != nil || argv[1] != "skill-router" || os.Getenv("ARKISM_RUNTIME") != "claude_code" {
		t.Fatal(argv, err)
	}
	if _, err := winSSHCommand("codex", "work-event"); err == nil {
		t.Fatal("work hand-off needs a server")
	}
	for _, bad := range []string{
		`{"mode":"local","url":"http://evil.example:8091","token_file":` + strconv.Quote(token) + `}`,
		`{"mode":"server","url":"http://127.0.0.1:8091","token_file":` + strconv.Quote(token) + `}`,
		`{"mode":"local","url":"http://127.0.0.1:8091","token_file":"` + filepath.ToSlash(filepath.Join(dir, "missing")) + `"}`,
		`not json`,
	} {
		write(bad)
		if argv, _ := winSSHCommand("codex", "hook"); argv[len(argv)-2] != "host" {
			t.Fatal("expected ssh for", bad, argv)
		}
	}
}

// On the PC with the built-in Arkism, a "pc" transcript is read from the agent's own folder.
func TestArchiveRootsUseThePCsOwnFoldersInLocalMode(t *testing.T) {
	root := func() string {
		for _, r := range archiveRoots() {
			if r.origin == "claude_code" && r.device == "pc" {
				return r.root
			}
		}
		return ""
	}
	t.Setenv("ARKISM_PC_LOCAL", "")
	if root() != filepath.Join(archivePC, "claude-projects") {
		t.Fatal(root())
	}
	t.Setenv("ARKISM_PC_LOCAL", "1")
	if root() != filepath.Join(host.Home, ".claude/projects") {
		t.Fatal(root())
	}
	for _, r := range archiveRoots() {
		if r.device == "server" && (r.origin == "claude_code" || r.origin == "codex") {
			t.Fatal("the same folder listed twice:", r)
		}
	}
}
