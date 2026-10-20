package crew

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// defaultWorkerRules is the shared record rule every step that may change files gets in its instructions. Not every
// path reads ~/.codex/AGENTS.md or ~/.claude/CLAUDE.md (the direct API and --bare CLIs do not), so it is passed here.
const defaultWorkerRules = `# 작업 기록 규칙
- 코드를 바꾸면 변경 이유, 관련 작업 ID, 실행한 검증과 결과, 남은 한계를 기록한다.
- 연구용 변경은 그 프로젝트의 연구 기록 규약을 따른다.
- 실행하지 않은 검증을 통과로 쓰지 않는다. 못 한 검증은 미실행과 이유를 쓴다.
- 작업 범위에 없는 외부 실행·게시 권한을 이 규칙에서 추정하지 않는다.
- 끝나면 보고한다: 작업 ID, 바꾼 파일, 실행한 검증 명령과 결과, 실패·미실행과 이유, 커밋 상태(SHA 또는 미커밋).`

// workerRules is the rule text for a step and its short sha256 for the run record; both empty for read-only steps,
// which must not change what an isolated evaluator or a meeting sees. ARKISM_WORKER_RULES names a file to use
// instead, or "off".
func workerRules(access string, once bool) (string, string, error) {
	if once || access == "read" {
		return "", "", nil
	}
	text := defaultWorkerRules
	switch path := strings.TrimSpace(host.Env("ARKISM_WORKER_RULES", "")); path {
	case "":
	case "off":
		return "", "", nil
	default:
		raw, err := os.ReadFile(expandUser(path))
		if err != nil {
			return "", "", crewErr("ARKISM_WORKER_RULES 파일을 읽지 못했어요: " + err.Error())
		}
		text = strings.TrimSpace(string(raw))
	}
	if text == "" {
		return "", "", nil
	}
	sum := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(sum[:])[:12], nil
}

// withRules puts the shared rules ahead of the step's own instructions.
func withRules(rules, own string) string {
	if rules == "" || own == "" {
		return rules + own
	}
	return rules + "\n\n" + own
}
