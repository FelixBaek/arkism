package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func sessionOutcomeEnv(t *testing.T) (spool string, posts *[]map[string]any, fail *int) {
	dir := t.TempDir()
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
