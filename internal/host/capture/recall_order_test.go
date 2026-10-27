package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// standingRow is a GET /v1/memories row as the API sends it (no collection, created_at RFC 3339).
func standingRow(id, kind, created, text string) map[string]any {
	return map[string]any{"id": id, "kind": kind, "revision": json.Number("1"), "text": text, "created_at": created, "status": "active",
		"scope": map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u"}}
}

// The standing rules sort user first, then oldest first, then id, whatever order the server lists.
func TestStandingOrderUserThenOldestThenID(t *testing.T) {
	rows := func() []map[string]any {
		return []map[string]any{
			standingRow("a79d597c4af56e9d17534b150a09436c", "procedural", "2026-10-13T20:22:44.942Z", "새 절차"),
			standingRow("033b6394712964a139f82bfc9edd59e8", "procedural", "2026-09-24T10:39:43.513Z", "옛 절차"),
			standingRow("ffff0000000000000000000000000000", "user", "2026-10-15T01:00:00Z", "새 선호"),
			standingRow("0000ffff000000000000000000000000", "procedural", "2026-09-24T10:39:43.513Z", "같은 시각"), // tie: id decides
			standingRow("1111000000000000000000000000000a", "procedural", "", "시각 없음"),                         // missing sorts first
			standingRow("2222000000000000000000000000000b", "user", "2026-09-24T10:39:43.5Z", "옛 선호"),          // shorter fraction parses
		}
	}
	want := []string{"2222000000000000000000000000000b", "ffff0000000000000000000000000000", "1111000000000000000000000000000a",
		"0000ffff000000000000000000000000", "033b6394712964a139f82bfc9edd59e8", "a79d597c4af56e9d17534b150a09436c"}
	if got := itemIDs(standingOrder(rows(), "")); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	rev := rows()
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if got := itemIDs(standingOrder(rev, "")); !reflect.DeepEqual(got, want) {
		t.Fatal("order depends on the input order", got)
	}
}

// When the char limit cuts, the newest how-to rule goes, not one that sorts late by id.
func TestSessionIndexCutsNewestRuleFirst(t *testing.T) {
	t.Chdir(t.TempDir()) // the index reads .arkism/contract.md from the cwd's repo; keep a local one out
	capFake(t)
	old := standingRow("ffff6394712964a139f82bfc9edd59e8", "procedural", "2026-09-24T10:39:43.513Z", "PR 본문에 자동 생성 표시 문구를 넣지 않는다.")
	newer := standingRow("0000597c4af56e9d17534b150a09436c", "procedural", "2026-10-13T20:22:44.942Z", "오래된 branch 자체를 계속 유지하지 않는다.")
	pref := standingRow("8888000000000000000000000000000c", "user", "2026-10-15T01:00:00Z", "보고는 한국어로 짧게 한다.")
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "kind=user") {
			return map[string]any{"items": []any{pref}}, nil
		}
		return map[string]any{"items": []any{newer, old}}, nil // the server lists by id
	}
	limit := len([]rune(indexHeader)) + 1 + len([]rune(indexLine(pref, ""))) + 1 + len([]rune(indexLine(old, ""))) + 1 + len([]rune(fmt.Sprintf(indexMoreLine, 999))) + 1
	idx, err := sessionIndex("s1", "", limit)
	if err != nil || !reflect.DeepEqual(idx.IDs, itemIDs([]map[string]any{pref, old})) || !strings.Contains(idx.Full, "자동 생성") || !strings.HasSuffix(idx.Full, fmt.Sprintf(indexMoreLine, 1)) {
		t.Fatal(err, idx.IDs, idx.Full)
	}
	again, _ := sessionIndex("s2", "", limit)
	if again.Full != idx.Full {
		t.Fatal("index not byte-identical across builds")
	}
}

// A memory an earlier turn showed is not shown again in the session unless revised; the trace
// ledger still lists it every turn, and another session sees it fresh.
func TestPromptRecallSkipsEarlierTurnsUnlessRevised(t *testing.T) {
	capFake(t)
	hookEnv(t)
	sessionOutcomeEnv(t)
	hits := []any{memoryHit("c8b788c964a0350e97d8635111de6824", 2, ""), memoryHit("d9243bd5ef20da4ea2369e120fb20962", 1, "")}
	search = func(string, string, int, float64) ([]any, bool, error) { return hits, true, nil }
	turn := func(sid string) string {
		t.Helper()
		p, revs, err := promptParts("기억 승인 권한을 다시 설명해 줘", sid, "")
		if err != nil {
			t.Fatal(err)
		}
		markShown(sid, p.Full, revs)
		return p.Full
	}
	if first := turn("s1"); strings.Count(first, "\n- [") != 2 {
		t.Fatal(first)
	}
	if second := turn("s1"); second != "" {
		t.Fatal("repeated in the same session", second)
	}
	hits[0] = memoryHit("c8b788c964a0350e97d8635111de6824", 3, "") // revised
	third := turn("s1")
	if strings.Count(third, "\n- [") != 1 {
		t.Fatal("revision not shown once", third)
	}
	if turn("s1") != "" {
		t.Fatal("revision repeated")
	}
	if other := turn("s2"); strings.Count(other, "\n- [") != 2 {
		t.Fatal("another session lost the memories", other)
	}
	if got := sessionStore().Delivered("s1"); !reflect.DeepEqual(got, map[string]int64{"c8b788c964a0350e97d8635111de6824": 3, "d9243bd5ef20da4ea2369e120fb20962": 1}) {
		t.Fatal(got)
	}
	// The budget stub carries no memory, so nothing is recorded; compaction forgets what was shown.
	_, revs, _ := promptParts("기억 승인 권한을 다시 설명해 줘", "s3", "")
	markShown("s3", host.MemoryOmitted, revs)
	if sessionStore().Delivered("s3")["d9243bd5ef20da4ea2369e120fb20962"] != 0 || turn("s3") == "" {
		t.Fatal("stub output recorded as shown")
	}
	sessionStore().ResetDelivered("s1")
	if again := turn("s1"); strings.Count(again, "\n- [") != 2 {
		t.Fatal("compaction did not bring the memories back", again)
	}
	ledgers, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	rows := 0
	for _, l := range ledgers {
		raw, _ := os.ReadFile(l)
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if row, _ := loadsMap(line); row["session"] == "s1" {
				rows++
			}
		}
	}
	if rows != 5 {
		t.Fatal("ledger rows for s1", rows)
	}
}

// the prompt and SessionStart contract hooks drop hits below the same score floor and
// kind budget the MCP recall tool uses, so an unrelated prompt injects nothing.
func TestHookRecallAppliesSharedBudget(t *testing.T) {
	capFake(t)
	hookEnv(t)
	sessionOutcomeEnv(t)
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0.025")
	t.Setenv("ARKISM_RECALL_BUDGET", "procedural=3:1500")
	low := memoryHit("c8b788c964a0350e97d8635111de6824", 1, "")
	low["score"] = json.Number("0.016393")
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{low}, true, nil }
	if p, _, err := promptParts("관련 없는 질문을 하나 해 볼게요", "s1", ""); err != nil || p.Full != "" {
		t.Fatal(err, p.Full)
	}
	if got := recallRelated("작업 계약 본문", "", "SessionStart:contract"); len(got) != 0 {
		t.Fatal(got)
	}
	var procedural []any
	for i := range 4 {
		h := memoryHit(fmt.Sprintf("%032x", i+1), 1, "")
		h["kind"], h["score"] = "procedural", json.Number("0.05")
		procedural = append(procedural, h)
	}
	search = func(string, string, int, float64) ([]any, bool, error) { return procedural, true, nil }
	if p, _, err := promptParts("기억 승인 권한을 다시 설명해 줘", "s2", ""); err != nil || strings.Count(p.Full, "\n- [") != 3 {
		t.Fatal(err, p.Full)
	}
	// The kind cap runs on the injected list: hits the stable block already shows use no slot.
	ids := []string{}
	for _, h := range procedural[:3] {
		ids = append(ids, h.(map[string]any)["id"].(string))
	}
	cache := host.StableBlock{IDs: ids, Scope: stableScope(), Renderer: stableRenderer, WorkAgent: pinAgent(), Fingerprint: strings.Repeat("a", 64)}
	for _, id := range ids {
		cache.Refs = append(cache.Refs, host.StableMemoryRef{Collection: "memories", ID: id, Revision: 1, Digest: sha256Hex([]byte("t\n")), Fingerprint: strings.Repeat("b", 64)})
	}
	cache.BlockHash = stableBlockHash(cache)
	if _, err := sessionStore().SaveStable("s3", cache); err != nil {
		t.Fatal(err)
	}
	p, revs, err := promptParts("기억 승인 권한을 다시 설명해 줘", "s3", "")
	if _, fourth := revs.Revisions[procedural[3].(map[string]any)["id"].(string)]; err != nil || strings.Count(p.Full, "\n- [") != 1 || len(revs.Revisions) != 1 || !fourth {
		t.Fatal(err, p.Full, revs)
	}
}

// contract-related memories the stable block pins use no kind slot; the budget runs after
// they are skipped, so a third decision still shows under decision=2.
func TestSessionIndexContractBudgetAfterSkip(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	os.MkdirAll(filepath.Join(repo, ".arkism"), 0o755)
	os.WriteFile(filepath.Join(repo, ".arkism", "contract.md"), []byte("작업 계약: 기억 승인 경계를 지킨다."), 0o644)
	capFake(t)
	t.Setenv("ARKISM_RECALL_BUDGET", "decision=2:1500")
	capGet = func(string, time.Duration) (map[string]any, error) { return map[string]any{"items": []any{}}, nil }
	var hits []any
	for i := range 3 {
		hits = append(hits, map[string]any{"id": fmt.Sprintf("d%d", i), "kind": "decision", "text": fmt.Sprintf("결정 %d", i), "score": 0.05})
	}
	search = func(string, string, int, float64) ([]any, bool, error) { return hits, true, nil }
	idx, err := sessionIndexSkipping("s1", repo, 4000, map[string]bool{"d0": true, "d1": true})
	if err != nil || !strings.Contains(idx.Full, "결정 2") {
		t.Fatal(err, idx.Full)
	}
}

// This repository's rules (project reach or repo tag) go first whatever their kind, the index stops
// at the line budget, and a closing line counts what recall can still fetch.
func TestSessionIndexRepoRulesFirstWithinLineBudget(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/o/a.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	t.Chdir(repo)
	capFake(t)
	t.Setenv("ARKISM_REPO", "o/a")
	hookTranscript = ""
	var general []any
	for i := range 12 {
		general = append(general, standingRow(fmt.Sprintf("%031x0", i), "procedural", "2026-09-24T10:00:00Z", fmt.Sprintf("일반 규칙 %d", i)))
	}
	mine := standingRow("aaaa0000000000000000000000000001", "procedural", "2026-10-20T00:00:00Z", "이 저장소 규칙")
	mine["tags"] = []any{"repo:o/a"}
	other := standingRow("bbbb0000000000000000000000000002", "procedural", "2026-10-20T00:00:00Z", "다른 저장소 규칙")
	other["tags"] = []any{"repo:o/b"}
	proj := standingRow("cccc0000000000000000000000000003", "procedural", "2026-10-21T00:00:00Z", "프로젝트 규칙")
	proj["reach"] = "project"
	pref := standingRow("dddd0000000000000000000000000004", "user", "2026-09-01T00:00:00Z", "보고는 짧게")
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "kind=user") {
			return map[string]any{"items": []any{pref}}, nil
		}
		return map[string]any{"items": append(append([]any{}, general...), other, mine, proj)}, nil
	}
	idx, err := sessionIndex("s1", repo, 4000)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(idx.Full, "\n")[2:] // header and recall guide
	if len(lines) != 11 || !strings.HasPrefix(lines[0], "- [procedural] 이 저장소 규칙") || !strings.HasPrefix(lines[1], "- [procedural, 이 프로젝트 전용") ||
		!strings.HasPrefix(lines[2], "- [user] 보고는 짧게") || lines[10] != fmt.Sprintf(indexMoreLine, 5) || strings.Contains(idx.Full, "다른 저장소") {
		t.Fatal(idx.Full)
	}
	if len(idx.IDs) != 10 || idx.IDs[0] != "aaaa0000000000000000000000000001" || idx.IDs[1] != "cccc0000000000000000000000000003" {
		t.Fatal(idx.IDs)
	}
	t.Setenv("ARKISM_SESSION_INDEX_LINES", "3")
	if short, _ := sessionIndex("s2", repo, 4000); len(short.IDs) != 3 || !strings.HasSuffix(short.Full, fmt.Sprintf(indexMoreLine, 12)) {
		t.Fatal(short.Full)
	}
}

// A resumed session's first typed question picks the related memories; without one (a new session
// has no transcript yet) the task contract does, and with neither there is no related section.
func TestSessionIndexFirstQuestionThenContract(t *testing.T) {
	t.Chdir(t.TempDir())
	capFake(t)
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(transcript, []byte(`{"type":"user","isMeta":true,"message":{"role":"user","content":"meta"}}`+"\n"+
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"회상 예산을 어떻게 정하나요"}]}}`+"\n"+
		`{"type":"user","message":{"role":"user","content":"두 번째 질문"}}`+"\n"), 0o644)
	hookTranscript = transcript
	t.Cleanup(func() { hookTranscript = "" })
	var queries []string
	search = func(q string, _ string, _ int, _ float64) ([]any, bool, error) {
		queries = append(queries, q)
		hit := memoryHit("c8b788c964a0350e97d8635111de6824", 1, "")
		hit["kind"], hit["text"] = "decision", "예산 규칙"
		return []any{hit}, true, nil
	}
	capGet = func(string, time.Duration) (map[string]any, error) { return map[string]any{"items": []any{}}, nil }
	idx, err := sessionIndex("s1", "", 4000)
	if err != nil || !reflect.DeepEqual(queries, []string{"회상 예산을 어떻게 정하나요"}) || !strings.HasPrefix(idx.Full, questionHeader+"\n- [decision] 예산 규칙") {
		t.Fatal(err, queries, idx.Full)
	}
	hookTranscript, queries = filepath.Join(t.TempDir(), "none.jsonl"), nil
	if idx, err = sessionIndex("s2", "", 4000); err != nil || len(queries) != 0 || idx.Full != "" {
		t.Fatal(err, queries, idx.Full)
	}
}

// A zero line or char budget injects nothing and makes no server call.
func TestSessionIndexBudgetZeroInjectsNothing(t *testing.T) {
	capFake(t)
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		t.Fatal("fetched at budget 0", path)
		return nil, nil
	}
	for _, env := range [][2]string{{"ARKISM_SESSION_INDEX_LINES", "0"}, {"ARKISM_SESSION_INDEX_CHARS", "0"}} {
		t.Setenv("ARKISM_SESSION_INDEX_LINES", "10")
		t.Setenv(env[0], env[1])
		if idx, err := sessionIndex("s", "", 4000); err != nil || idx.Full != "" || idx.Meta != "" || len(idx.IDs) != 0 {
			t.Fatal(env, err, idx)
		}
	}
}
