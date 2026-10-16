package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
			standingRow("2222000000000000000000000000000b", "user", "2026-09-24T10:39:43.5Z", "옛 선호"),             // shorter fraction parses
		}
	}
	want := []string{"2222000000000000000000000000000b", "ffff0000000000000000000000000000", "1111000000000000000000000000000a",
		"0000ffff000000000000000000000000", "033b6394712964a139f82bfc9edd59e8", "a79d597c4af56e9d17534b150a09436c"}
	if got := itemIDs(standingOrder(rows())); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	rev := rows()
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if got := itemIDs(standingOrder(rev)); !reflect.DeepEqual(got, want) {
		t.Fatal("order depends on the input order", got)
	}
}

// When the char limit cuts, the newest how-to rule goes, not one that sorts late by id.
func TestSessionIndexCutsNewestRuleFirst(t *testing.T) {
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
	limit := len([]rune(indexHeader)) + 1 + len([]rune(indexLine(pref))) + 1 + len([]rune(indexLine(old))) + 1
	idx, err := sessionIndex("s1", "", limit)
	if err != nil || !reflect.DeepEqual(idx.IDs, itemIDs([]map[string]any{pref, old})) || !strings.Contains(idx.Full, "Generated") {
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
	search = func(string, int, float64) ([]any, error) { return hits, nil }
	turn := func(sid string) string {
		t.Helper()
		p, err := promptParts("기억 승인 권한을 다시 설명해 줘", sid, "")
		if err != nil {
			t.Fatal(err)
		}
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
	if rows != 4 {
		t.Fatal("ledger rows for s1", rows)
	}
}
