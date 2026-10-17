package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func sessionOutcomeEnv(t *testing.T) (spool string, posts *[]map[string]any, fail *int) {
	dir := t.TempDir()
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(dir, "capture.json"))
	oldState, oldOrigin, oldScope, oldPost, oldNow := capState, capOrigin, capScope, capPost, timeNow
	t.Cleanup(func() {
		capState, capOrigin, capScope, capPost, timeNow = oldState, oldOrigin, oldScope, oldPost, oldNow
	})
	capState, capOrigin, spool = filepath.Join(dir, "state"), "codex", filepath.Join(dir, "receipts")
	capScope = func() map[string]any {
		return map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u", "agent_id": ""}
	}
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	t.Setenv("ARKISM_MEMORY_OUTCOME_SPOOL", spool)
	posts, fail = &[]map[string]any{}, new(int)
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/execution_traces/completed" {
			t.Fatal(path)
		}
		if *fail != 0 {
			return nil, &host.HTTPError{Code: *fail}
		}
		var b map[string]any
		raw, _ := json.Marshal(body)
		json.Unmarshal(raw, &b)
		*posts = append(*posts, b)
		return map[string]any{"trace": map[string]any{"collection": "execution_traces", "id": b["trace_id"], "revision": json.Number("1")}}, nil
	}
	return spool, posts, fail
}

// memoryHit is a search hit as the API sends it: an empty agent_id is omitted from the scope.
func memoryHit(id string, rev int, agent string) map[string]any {
	scope := map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u"}
	if agent != "" {
		scope["agent_id"] = agent
	}
	return map[string]any{"collection": "memories", "id": id, "revision": json.Number(fmt.Sprint(rev)), "text": "t", "scope": scope}
}

func TestSessionTraceRecordsDeliveredSharedMemoriesOnceIdle(t *testing.T) {
	spool, posts, fail := sessionOutcomeEnv(t)
	var items []map[string]any
	for i := 0; i < 40; i++ {
		items = append(items, memoryHit(fmt.Sprint("m", i), 2, ""))
	}
	noText := memoryHit("x", 1, "")
	delete(noText, "text")
	listed := memoryHit("m39", 2, "") // a GET /v1/memories row (standing rules) has no collection
	delete(listed, "collection")
	knowledge := memoryHit("k", 1, "")
	knowledge["collection"] = "knowledge"
	items = append(items[:39], listed, memoryHit("agent-only", 1, "crew"), memoryHit("unreviewed", 0, ""), noText, knowledge)
	logRecall("SessionStart", "s1", items)
	logRecall("UserPromptSubmit", "s1", items[:3]) // a repeat delivery is one target

	now := float64(time.Now().Unix())
	timeNow = func() float64 { return now }
	sessionTraces()
	if len(*posts) != 0 {
		t.Fatal("an active session was recorded", *posts)
	}
	timeNow = func() float64 { return now + sessionIdle + 1 }
	*fail = 503
	sessionTraces()
	if len(*posts) != 0 {
		t.Fatal(*posts)
	}
	*fail = 0
	sessionTraces() // the claimed ledger is retried with the same bodies
	if len(*posts) != 2 {
		t.Fatalf("want 2 traces of <=32 refs, got %d", len(*posts))
	}
	total := 0
	for _, b := range *posts {
		run, scope := b["agent_run"].(map[string]any), b["scope"].(map[string]any)
		retrieved := b["events"].([]any)[0].(map[string]any)["payload"].(map[string]any)["retrieved"].([]any)
		if scope["agent_id"] != "" || run["agent_id"] != "codex" || run["task_type"] != "agent_session" || b["thread_id"] != "s1" || len(retrieved) > 32 {
			t.Fatal(b)
		}
		for _, r := range retrieved {
			if id := r.(map[string]any)["id"]; id == "agent-only" || id == "unreviewed" || id == "x" || id == "k" {
				t.Fatal("undeliverable target in trace", id)
			}
		}
		total += len(retrieved)
	}
	if total != 40 {
		t.Fatal(total)
	}
	receipts, _ := filepath.Glob(filepath.Join(spool, "*.json"))
	left, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*"))
	if len(receipts) != 2 || len(left) != 0 {
		t.Fatal(receipts, left)
	}
	var r map[string]any
	raw, _ := os.ReadFile(receipts[0])
	if json.Unmarshal(raw, &r) != nil || r["scope"].(map[string]any)["agent_id"] != "" {
		t.Fatal(string(raw))
	}
}

func TestSessionTraceDropsRefusedLedgerAndStaysOffByDefault(t *testing.T) {
	_, posts, fail := sessionOutcomeEnv(t)
	logRecall("SessionStart", "s2", []map[string]any{memoryHit("m", 1, "")})
	if open, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl")); len(open) != 1 {
		t.Fatal("ledger not written", open)
	}
	now := float64(time.Now().Unix())
	timeNow = func() float64 { return now + sessionIdle + 1 }
	*fail = 400
	sessionTraces()
	if left, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*")); len(left) != 0 {
		t.Fatal("refused ledger kept", left)
	}
	t.Setenv("ARKISM_MEMORY_OUTCOME_SPOOL", "")
	logRecall("SessionStart", "s3", []map[string]any{memoryHit("m", 1, "")})
	*fail = 0
	sessionTraces()
	if left, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*")); len(left) != 0 || len(*posts) != 0 {
		t.Fatal("off by default", left, *posts)
	}
}

func TestSessionTraceCarriesWindowBehaviorAndFallsBackWithoutIt(t *testing.T) {
	_, posts, _ := sessionOutcomeEnv(t)
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	line := func(v map[string]any) string { raw, _ := json.Marshal(v); return string(raw) + "\n" }
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(transcript, []byte(
		line(map[string]any{"type": "user", "timestamp": at(-2 * time.Hour), "message": map[string]any{"content": "옛 질문"}})+
			line(map[string]any{"type": "assistant", "timestamp": at(-2 * time.Hour), "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "out of window"}}}})+
			line(map[string]any{"type": "user", "timestamp": at(0), "message": map[string]any{"content": "아니, 자세히 설명해"}})+
			line(map[string]any{"type": "assistant", "timestamp": at(time.Second), "message": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "중간 보고"},
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "git commit -m 'capture window fix'"}}}}})+
			line(map[string]any{"type": "assistant", "timestamp": at(2 * time.Second), "message": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "결론입니다.\n```go\nGenerated with Claude Code\n```\n핵심: 끝"}}}})), 0o600)
	hookTranscript = transcript
	t.Cleanup(func() { hookTranscript = "" })
	logRecall("UserPromptSubmit", "s4", []map[string]any{memoryHit("m", 1, "")})

	refused := 0
	ok := capPost
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if len(body.(map[string]any)["events"].([]any)) > 1 && refused == 0 {
			refused++
			return nil, &host.HTTPError{Code: 400}
		}
		return ok(path, body, d)
	}
	// Follow each fresh ledger write; a captured whole second can leave s5 non-idle.
	timeNow = func() float64 { return float64(time.Now().UnixNano())/1e9 + sessionIdle + 1 }
	sessionTraces() // the server refuses the behavior event once: the delivery record still lands
	if refused != 1 || len(*posts) != 1 || len((*posts)[0]["events"].([]any)) != 1 {
		t.Fatal(refused, *posts)
	}

	*posts = nil
	logRecall("UserPromptSubmit", "s5", []map[string]any{memoryHit("m", 1, "")})
	sessionTraces()
	if len(*posts) != 1 {
		t.Fatal(*posts)
	}
	events := (*posts)[0]["events"].([]any)
	b := events[1].(map[string]any)
	p := b["payload"].(map[string]any)
	replies := p["replies"].([]any)
	if b["kind"] != "behavior" || len(replies) != 1 || len(p["commits"].([]any)) != 1 || p["user_corrections"] != float64(1) {
		t.Fatal(p)
	}
	r := replies[0].(map[string]any)
	if r["text"] != "결론입니다.\n\n핵심: 끝" || r["code_blocks"] != float64(1) || r["lines"] != float64(5) || r["detail_requested"] != true {
		t.Fatal(r)
	}
}

func TestSessionBehaviorCountsOnlyRealCommitCommands(t *testing.T) {
	for cmd, want := range map[string]bool{
		"git commit -m x":                                                true,
		"cd /r && git -C /r commit -m x":                                 true,
		"git add a; git commit -F msg":                                   true,
		"cat > t.go <<'EOF'\n\"git commit -m x\"":                        false,
		"echo 'gh pr create' >> notes":                                   false,
		"git log --oneline":                                              false,
		"echo 'note; git commit -m x'":                                   false,
		"echo \"a && gh pr create\" | tee n":                             false,
		"cat >> t.go <<'EOF'\n\tx := \"cd /r && git -C /r commit\"\nEOF": false,
	} {
		if shell := shellText(cmd); (commitCmdRE.MatchString(shell) || prCmdRE.MatchString(shell)) != want {
			t.Error(cmd)
		}
	}
}

func TestHeadTailKeepsFootersAndSummaries(t *testing.T) {
	s := "시작" + strings.Repeat("가", 5000) + "\n\n핵심: 끝"
	got := headTail(s, 100)
	if r := []rune(got); len(r) != 100 || !strings.HasPrefix(got, "시작") || !strings.HasSuffix(got, "핵심: 끝") {
		t.Fatal(len(r), got)
	}
	if headTail("짧다", 100) != "짧다" {
		t.Fatal("short text changed")
	}
}
