package crew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteStepsGetTheSharedRecordRules(t *testing.T) {
	out, calls, err := runWith(t, map[string]any{"model": "opus", "instructions": "너는 편집자다."}, "결과", "", 0)
	if err != nil || out["ok"] != true {
		t.Fatal(out, err)
	}
	argv := strings.Join(calls[0].argv, "\n")
	if !strings.Contains(argv, defaultWorkerRules+"\n\n너는 편집자다.") {
		t.Fatal(argv)
	}
	if sum, _ := out["rules"].(string); len(sum) != 12 {
		t.Fatal(out["rules"])
	}
}

func TestReadOnlyStepsKeepTheirInstructionsAsIs(t *testing.T) {
	for _, spec := range []map[string]any{{"once": true}, {"access": "read"}} {
		spec["model"], spec["instructions"] = "opus", "너는 평가자다."
		out, calls, err := runWith(t, spec, "결과", "", 0)
		if err != nil || strings.Contains(strings.Join(calls[0].argv, "\n"), "작업 기록 규칙") || out["rules"] != nil {
			t.Fatal(spec, out, err)
		}
	}
}

func TestWorkerRulesFileOrOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.md")
	if err := os.WriteFile(path, []byte("  # 우리 규칙\n- 검증을 적는다\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_WORKER_RULES", path)
	text, sum, err := workerRules("write", false)
	if err != nil || text != "# 우리 규칙\n- 검증을 적는다" || len(sum) != 12 {
		t.Fatal(text, sum, err)
	}
	t.Setenv("ARKISM_WORKER_RULES", "off")
	if text, sum, err := workerRules("full", false); text != "" || sum != "" || err != nil {
		t.Fatal(text, sum, err)
	}
	t.Setenv("ARKISM_WORKER_RULES", filepath.Join(t.TempDir(), "missing.md"))
	if _, _, err := workerRules("write", false); err == nil {
		t.Fatal("a missing rules file must stop the run, not drop the rules silently")
	}
}
