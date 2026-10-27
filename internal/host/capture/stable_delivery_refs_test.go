package capture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestStableReplayPreservesExactRevisionAndForm(t *testing.T) {
	t.Chdir(t.TempDir())
	standing := stableFake(t)
	_, posts, _ := sessionOutcomeEnv(t)
	fakeStableValidation(t, standing)
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_WORK_AGENT", "")
	t.Setenv("ARKISM_AGENT", "")
	item := memoryHit("original-memory", 7, "")
	item["kind"] = "user"
	*standing = []any{item}
	first := hookText(t, `{"hook_event_name":"SessionStart","session_id":"s"}`)
	// A completed drain removes the initial ledger. Replay must create its own exact receipt.
	timeNow = func() float64 { return float64(time.Now().Unix()) + sessionIdle + 2 }
	sessionTraces()
	if len(*posts) != 1 {
		t.Fatal(*posts)
	}
	second := hookText(t, `{"hook_event_name":"SessionStart","session_id":"s","source":"compact"}`)
	if second != first {
		t.Fatal("replay changed bytes")
	}
	sessionTraces()
	if len(*posts) != 2 {
		t.Fatalf("replay lost exact revision receipt: posts=%d", len(*posts))
	}
	payload := (*posts)[1]["events"].([]any)[0].(map[string]any)["payload"].(map[string]any)
	refs := payload["retrieved"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["revision"] != float64(7) {
		t.Fatal(refs)
	}
	deliveries, ok := payload["deliveries"].([]any)
	if !ok || len(deliveries) != 1 || deliveries[0].(map[string]any)["form"] != "full" {
		t.Fatal("delivery form missing", payload)
	}
	if payload["observation"] != "context_delivery" {
		t.Fatal(payload)
	}
}

func TestLegacyStableCacheRefusesOwnerlessTextAndDoesNotSuppressCurrentIDs(t *testing.T) {
	t.Chdir(t.TempDir())
	stableFake(t)
	sessionOutcomeEnv(t)
	t.Setenv("ARKISM_WORK_ID", "")
	_, err := sessionStore().SaveStable("legacy", host.StableBlock{Full: "legacy private context", IDs: []string{"legacy-memory"}})
	if err != nil {
		t.Fatal(err)
	}
	original := capScope
	capScope = func() map[string]any { scope := original(); scope["user_id"] = "another-user"; return scope }
	var output bytes.Buffer
	hookMain(strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"legacy"}`), &output)
	if strings.Contains(output.String(), "legacy private context") {
		t.Fatal("ownerless private text reached hook stdout", output.String())
	}
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 0 {
		t.Fatal("invented receipt", files)
	}
	stored, ok := sessionStore().Stable("legacy")
	if !ok || stored.Scope != nil {
		t.Fatal("ownerless cache was silently promoted", stored)
	}
	hit := memoryHit("legacy-memory", 1, "")
	hit["scope"].(map[string]any)["user_id"] = "another-user"
	hit["text"] = "current authorized memory"
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{hit}, true, nil }
	output.Reset()
	hookMain(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"legacy","prompt":"현재 계정에서 승인된 기억을 확인합니다."}`), &output)
	if !strings.Contains(output.String(), "current authorized memory") {
		t.Fatal("ownerless IDs suppressed current memory", output.String())
	}
}

func TestPromptCarriesOriginalDeliveredFormOnLaterTurn(t *testing.T) {
	capFake(t)
	hookEnv(t)
	sessionOutcomeEnv(t)
	hits := []any{memoryHit("m", 4, "")}
	search = func(string, string, int, float64) ([]any, bool, error) { return hits, true, nil }
	p, d, err := promptParts("이전 전달 형태를 확인합니다.", "s", "")
	if err != nil {
		t.Fatal(err)
	}
	markShown("s", p.Meta, d)
	p, d, err = promptParts("이전 전달 형태를 다시 확인합니다.", "s", "")
	if err != nil {
		t.Fatal(err)
	}
	markShown("s", p.Full, d)
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	raw, _ := os.ReadFile(files[0])
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(rows) != 2 {
		t.Fatal(string(raw))
	}
	for _, line := range rows {
		row, _ := loadsMap(line)
		deliveries, ok := row["deliveries"].([]any)
		if !ok || len(deliveries) != 1 || deliveries[0].(map[string]any)["form"] != "meta" {
			t.Fatal("metadata delivery upgraded or lost", line)
		}
	}
}

func TestStableMetadataReplayRetainsFormAndCacheRefs(t *testing.T) {
	t.Chdir(t.TempDir())
	standing := stableFake(t)
	_, posts, _ := sessionOutcomeEnv(t)
	fakeStableValidation(t, standing)
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_WORK_AGENT", "")
	t.Setenv("ARKISM_AGENT", "")
	old := sessionMaxChars
	sessionMaxChars = 20000
	t.Cleanup(func() { sessionMaxChars = old })
	// the meta and stub forms need an index far past the 10-line default
	t.Setenv("ARKISM_SESSION_INDEX_LINES", "1000")
	t.Setenv("ARKISM_SESSION_INDEX_CHARS", "100000")
	*standing = nil
	for i := 0; i < 50; i++ {
		m := memoryHit(fmt.Sprintf("m%d", i), 3, "")
		m["kind"], m["text"] = "user", strings.Repeat("기억", 80)
		*standing = append(*standing, m)
	}
	first := hookText(t, `{"hook_event_name":"SessionStart","session_id":"s"}`)
	cache, ok := sessionStore().Stable("s")
	if !ok || cache.DeliveryForm != "meta" || len(cache.Memories) != 50 {
		t.Fatal(cache)
	}
	timeNow = func() float64 { return float64(time.Now().Unix()) + sessionIdle + 2 }
	sessionTraces()
	second := hookText(t, `{"hook_event_name":"SessionStart","session_id":"s","source":"compact"}`)
	if first != second {
		t.Fatal("metadata replay changed")
	}
	sessionTraces()
	if len(*posts) != 4 {
		t.Fatal("missing chunk or replay", len(*posts))
	}
	for _, post := range *posts {
		payload := post["events"].([]any)[0].(map[string]any)["payload"].(map[string]any)
		for _, d := range payload["deliveries"].([]any) {
			if d.(map[string]any)["form"] != "meta" {
				t.Fatal("metadata upgraded", d)
			}
		}
	}
}

func TestSessionTraceMixedOldAndNewDeliveryForms(t *testing.T) {
	capFake(t)
	_, posts, _ := sessionOutcomeEnv(t)
	dir := filepath.Join(capState, sessionLedgerName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sdigest([]any{capOrigin, "s"})[:32]+".jsonl")
	legacy := map[string]any{"origin": capOrigin, "session": "s", "ts": host.NowISO(), "refs": []any{map[string]any{"collection": "memories", "id": "legacy", "revision": 1}}}
	if err := os.WriteFile(path, []byte(dumps(legacy)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ledgerDeliveries("s", memoryDeliveries([]map[string]any{memoryHit("known", 2, "")}, "meta"))
	timeNow = func() float64 { return float64(time.Now().Unix()) + sessionIdle + 2 }
	sessionTraces()
	if len(*posts) != 1 {
		t.Fatal(*posts)
	}
	payload := (*posts)[0]["events"].([]any)[0].(map[string]any)["payload"].(map[string]any)
	if payload["observation"] != "context_delivery_mixed" {
		t.Fatal(payload)
	}
	for _, raw := range payload["deliveries"].([]any) {
		d := raw.(map[string]any)
		want := "meta"
		if d["id"] == "legacy" {
			want = "unknown"
		}
		if d["form"] != want {
			t.Fatal(d)
		}
	}
}

func TestCachedDeliveryCannotBeRelabeledToAnotherScope(t *testing.T) {
	capFake(t)
	sessionOutcomeEnv(t)
	refs := memoryDeliveries([]map[string]any{memoryHit("m", 7, "")}, "full")
	original := capScope
	capScope = func() map[string]any { s := original(); s["user_id"] = "someone-else"; return s }
	ledgerDeliveries("s", refs)
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 0 {
		t.Fatal("scope relabeled", files)
	}
}

func TestLegacyTraceReplayPreservesOriginalIdentity(t *testing.T) {
	capFake(t)
	spool, posts, _ := sessionOutcomeEnv(t)
	refs := []any{map[string]any{"collection": "memories", "id": "m", "revision": 1}}
	stamp := host.NowISO()
	row := map[string]any{"origin": capOrigin, "session": "s", "ts": stamp, "refs": refs}
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := os.WriteFile(path, []byte(dumps(row)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := recordSession(path, spool); err != nil {
		t.Fatal(err)
	}
	expected := "session-trace-" + sdigest([]any{capOrigin, "s", stamp, stamp, refs})[:32]
	if (*posts)[0]["trace_id"] != expected {
		t.Fatal("legacy retry identity changed", *posts)
	}
	payload := (*posts)[0]["events"].([]any)[0].(map[string]any)["payload"].(map[string]any)
	if _, ok := payload["observation"]; ok {
		t.Fatal("legacy retrieval promoted to confirmed delivery", payload)
	}
}

func TestPromptLegacyRevisionDoesNotInventDeliveryMetadata(t *testing.T) {
	capFake(t)
	hookEnv(t)
	sessionOutcomeEnv(t)
	if err := sessionStore().MarkDelivered("s", map[string]int64{"m": 4}); err != nil {
		t.Fatal(err)
	}
	hit := memoryHit("m", 4, "")
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{hit}, true, nil }
	p, d, err := promptParts("이전 기억의 판 정보를 확인합니다.", "s", "")
	if err != nil {
		t.Fatal(err)
	}
	markShown("s", p.Full, d)
	if p.Full != "" {
		t.Fatal("legacy repeat unexpectedly reinjected")
	}
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 0 {
		t.Fatal("old revision reconstructed from current search", files)
	}
}

func TestDeliveryDrainDefersScopeChangeWithoutRelabelingOrDropping(t *testing.T) {
	capFake(t)
	_, posts, _ := sessionOutcomeEnv(t)
	ledgerDeliveries("s", memoryDeliveries([]map[string]any{memoryHit("m", 1, "")}, "full"))
	original := capScope
	capScope = func() map[string]any { s := original(); s["user_id"] = "someone-else"; return s }
	timeNow = func() float64 { return float64(time.Now().Unix()) + sessionIdle + 2 }
	sessionTraces()
	claimed, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.claimed"))
	if len(*posts) != 0 || len(claimed) != 1 {
		t.Fatal("scope-changed receipt posted or dropped", *posts, claimed)
	}
	capScope = original
	sessionTraces()
	if len(*posts) != 1 {
		t.Fatal("original-scope receipt not retried", *posts)
	}
}

func TestNewStableCacheRefusesTextReplayAcrossScope(t *testing.T) {
	t.Chdir(t.TempDir())
	standing := stableFake(t)
	sessionOutcomeEnv(t)
	fakeStableValidation(t, standing)
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_WORK_AGENT", "")
	t.Setenv("ARKISM_AGENT", "")
	item := memoryHit("private-memory", 7, "")
	item["kind"], item["text"] = "user", "original private memory"
	*standing = []any{item}
	hookText(t, `{"hook_event_name":"SessionStart","session_id":"s"}`)
	original := capScope
	capScope = func() map[string]any { s := original(); s["user_id"] = "another-user"; return s }
	part, _, err := stableBlock("s", "")
	if err == nil || part.Full != "" {
		t.Fatal("different scope received cached text", err, part.Full)
	}
	var output bytes.Buffer
	hookMain(strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"s"}`), &output)
	if strings.Contains(output.String(), "original private memory") {
		t.Fatal("scope mismatch reached hook stdout")
	}
	hit := memoryHit("private-memory", 7, "")
	hit["scope"].(map[string]any)["user_id"] = "another-user"
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{hit}, true, nil }
	p, _, err := promptParts("현재 계정의 관련 기억을 다시 확인합니다.", "s", "")
	if err != nil || p.Full == "" {
		t.Fatal("foreign stable IDs suppressed current scope", err)
	}
}
