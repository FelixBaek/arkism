package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestPromptLedgerRecordsOnlyDeliveredBudgetItems(t *testing.T) {
	capFake(t)
	hookEnv(t)
	_, posts, _ := sessionOutcomeEnv(t)
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0")
	t.Setenv("ARKISM_RECALL_BUDGET", "user=2:1500")
	var hits []any
	for _, id := range []string{"audit-a", "audit-b", "audit-c"} {
		m := memoryHit(id, 1, "")
		m["kind"], m["text"] = "user", id
		hits = append(hits, m)
	}
	search = func(string, string, int, float64) ([]any, bool, error) { return hits, true, nil }
	p, delivery, err := promptParts("기억 전달과 예산 경계를 검사합니다.", "audit-session", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Full, "audit-c") || !strings.Contains(p.Full, "audit-a") {
		t.Fatal(p.Full)
	}
	markShown("audit-session", p.Full, delivery)
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	raw, _ := os.ReadFile(files[0])
	if strings.Contains(string(raw), "audit-c") {
		t.Fatal("budget-dropped memory recorded as delivered", string(raw))
	}
	timeNow = func() float64 { return float64(time.Now().Unix()) + sessionIdle + 2 }
	sessionTraces()
	if len(*posts) != 1 {
		t.Fatal("trace posts", len(*posts))
	}
	refs := (*posts)[0]["events"].([]any)[0].(map[string]any)["payload"].(map[string]any)["retrieved"].([]any)
	if len(refs) != 2 {
		t.Fatal(refs)
	}
}

func TestPromptLedgerOmitsStubAndPreservesPreviouslyDelivered(t *testing.T) {
	capFake(t)
	hookEnv(t)
	sessionOutcomeEnv(t)
	hits := []any{memoryHit("previous", 1, "")}
	search = func(string, string, int, float64) ([]any, bool, error) { return hits, true, nil }
	p, d, err := promptParts("기억의 전달을 검사합니다.", "s", "")
	if err != nil {
		t.Fatal(err)
	}
	markShown("s", p.Full, d)
	hits = append(hits, memoryHit("new", 1, ""))
	_, d, err = promptParts("기억의 전달을 검사합니다.", "s", "")
	if err != nil {
		t.Fatal(err)
	}
	markShown("s", host.MemoryOmitted, d)
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	raw, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || strings.Contains(string(raw), `"new"`) {
		t.Fatal("stub must retain only previous delivery", string(raw))
	}
	_, d, err = promptParts("기억의 전달을 검사합니다.", "fresh", "")
	if err != nil {
		t.Fatal(err)
	}
	markShown("fresh", host.MemoryOmitted, d)
	files, _ = filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 1 {
		t.Fatal("stub created ledger", files)
	}
}

func TestPromptLedgerMetaAndOmittedRevision(t *testing.T) {
	capFake(t)
	hookEnv(t)
	sessionOutcomeEnv(t)
	hits := []any{memoryHit("revised", 1, "")}
	search = func(string, string, int, float64) ([]any, bool, error) { return hits, true, nil }
	p, d, err := promptParts("기억의 전달을 검사합니다.", "s", "")
	if err != nil {
		t.Fatal(err)
	}
	// Real assembler falls back to metadata when the complete block does not fit.
	p.Full = strings.Repeat("긴 본문", 1100)
	d.Part = p
	output, err := host.AssembleParts(p, host.Part{Full: strings.Repeat("작업", 4000)}, host.Part{}, host.ContextMaxChars)
	if err != nil || !strings.HasPrefix(output, p.Meta) {
		t.Fatal(err, output)
	}
	markShown("s", output, d)
	if sessionStore().Delivered("s")["revised"] != 1 {
		t.Fatal("metadata delivery not recorded")
	}
	hits = []any{memoryHit("revised", 2, "")}
	_, d, err = promptParts("기억의 전달을 검사합니다.", "s", "")
	if err != nil {
		t.Fatal(err)
	}
	markShown("s", host.MemoryOmitted, d)
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	raw, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatal("previous revision tracking lost", string(raw))
	}
	row, err := loadsMap(lines[1])
	if err != nil {
		t.Fatal(err)
	}
	refs := row["refs"].([]any)
	if len(refs) != 1 || str(refs[0].(map[string]any)["revision"]) != "1" {
		t.Fatal("undelivered revision in ledger", row)
	}
}

type refusedContextWriter struct{}

func (refusedContextWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestPromptOutputFailureDoesNotRecordDelivery(t *testing.T) {
	capFake(t)
	hookEnv(t)
	sessionOutcomeEnv(t)
	t.Setenv("ARKISM_WORK_ID", "")
	search = func(string, string, int, float64) ([]any, bool, error) {
		return []any{memoryHit("not-written", 1, "")}, true, nil
	}
	hookMain(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"기억의 전달을 검사합니다."}`), refusedContextWriter{})
	files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(files) != 0 || len(sessionStore().Delivered("s")) != 0 {
		t.Fatal("failed output recorded", files, sessionStore().Delivered("s"))
	}
}

func TestStableLedgerCommitsOnlySuccessfulFinalOutput(t *testing.T) {
	for _, mode := range []string{"full", "meta", "stub", "write_failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			standing := stableFake(t)
			sessionOutcomeEnv(t)
			fakeStableValidation(t, standing)
			t.Setenv("ARKISM_WORK_ID", "")
			t.Setenv("ARKISM_WORK_AGENT", "")
			t.Setenv("ARKISM_AGENT", "")
			oldLimit := sessionMaxChars
			t.Cleanup(func() { sessionMaxChars = oldLimit })
			// the meta and stub forms need an index far past the 10-line default
			t.Setenv("ARKISM_SESSION_INDEX_LINES", "1000")
			t.Setenv("ARKISM_SESSION_INDEX_CHARS", "100000")
			*standing = []any{}
			switch mode {
			case "meta":
				sessionMaxChars = 20000
				for i := 0; i < 50; i++ {
					m := memoryHit(fmt.Sprintf("memory-%d", i), 1, "")
					m["kind"], m["text"] = "user", strings.Repeat("기억", 80)
					*standing = append(*standing, m)
				}
			case "stub":
				sessionMaxChars = 20000
				for i := 0; i < 80; i++ {
					m := memoryHit(fmt.Sprintf("%080d", i), 1, "")
					m["kind"], m["text"] = "user", strings.Repeat("기억", 80)
					*standing = append(*standing, m)
				}
			default:
				m := memoryHit("stable-memory", 1, "")
				m["kind"] = "user"
				*standing = append(*standing, m)
			}
			event := `{"hook_event_name":"SessionStart","session_id":"s"}`
			output := ""
			if mode == "write_failure" {
				hookMain(strings.NewReader(event), refusedContextWriter{})
			} else {
				output = hookText(t, event)
			}
			files, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
			_, saved := sessionStore().Stable("s")
			if mode == "stub" || mode == "write_failure" {
				if len(files) != 0 || saved {
					t.Fatal("undelivered stable block recorded", files, saved)
				}
				if mode == "stub" && !strings.HasPrefix(output, host.MemoryOmitted) {
					t.Fatal("expected stub", host.Len(output))
				}
			} else {
				if len(files) != 1 || !saved {
					t.Fatal("successful output missing receipt", files, saved)
				}
				if mode == "meta" && (!strings.Contains(output, "(id: memory-") || strings.Contains(output, strings.Repeat("기억", 20))) {
					t.Fatal("expected metadata")
				}
			}
		})
	}
}
