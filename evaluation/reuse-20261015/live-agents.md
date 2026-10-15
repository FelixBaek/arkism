# 실제 Claude ↔ Codex 기억 재사용

제품 기준은 v0.1.0 `9379ee5f493716e1c80027c0beb4fd12283100cf`다. 평가용 테스트만 추가한 독립 복제본에서 원본 `cmd/arkism`을 빌드했다. 운영 DB·MCP·개인 기억·훅은 연결하지 않았다. 각 실행은 새 임시 HOME, 인증 파일만 복사한 새 프로필과 새 세션을 사용했다. 임시 인증 사본은 실행 종료 때 삭제했다.

사용자가 이 채팅에서 정확한 5개 합성 사실에 대해 **“5개 테스트 기억 승인”**이라고 답한 후 실행했다. 그중 labels/cache 두 사실만 여기서 사용했다. A가 실제 `remember` MCP 도구로 후보를 제출하고, 별도 B 인증으로 HTTP 검색이 0건임을 확인한 다음, 호스트가 기록된 사용자 결정을 human-role 테스트 토큰으로 적용했다. 새 B 모델은 `recall` 도구를 직접 호출했다. 데이터베이스/HTTP/인증/RLS 및 MCP subprocess가 실제로 실행됐다. 원본 출처는 MCP 응답의 sources 필드로 전달됐다. B가 별도의 원문 출처 API를 호출한 것은 아니다.

| 방향 | 실제 도구 조회 | 규칙 적용 | 요청 JSON의 출처 ID |
| --- | --- | --- | --- |
| Codex → Claude | 성공 | `MiXeD` 유지, 공백 제거 | 실패: memory ID를 source_id에 사용 |
| Claude → Codex | 성공 | TTL=0 캐시 금지, 15 유지 | 성공 |

출처 정확도는 1/2다. Claude는 뒤의 설명에는 올바른 source ID도 썼지만 요청한 JSON 필드는 틀렸다. 이 실패를 성공으로 바꾸지 않았다. Claude는 허용되지 않은 `record_memory_usage`를 추가 호출하려 했고 권한이 거부됐다. 사용 기록이 저장됐다고 주장하지 않는다. 두 건은 가능성 확인이며 일반 성공률 추정 자료로 부족하다.

- Codex CLI 0.162.1, 요청 모델 gpt-6-astra, reasoning low.
- Claude CLI 2.1.299, claude-opus-5-5, effort low.
- `live-agents/runs.json`: 실행별 시간, 토큰, 사용자 승인 근거, 조회 응답.
- `live-agents/*-writer.jsonl`, `*-reader.jsonl`: 실제 도구 호출/응답/최종 답변. 세션 식별자·시스템 메타데이터·thinking 블록은 제외했다. 합성 기록의 ID는 근거 연결을 위해 유지했다.
- `live-agents/scores.json`, `.csv`: 독립 실행 판정. `judge_live.py`는 성공 도구 응답의 기억 ID/본문과 답안/출처를 검사한다.
- `live-agents-preflight/`: 첫 Codex 실행에서 MCP 호출 허용 설정이 빠져 도구 호출이 거부된 실패도 보존했다. 임시 프로필에 remember/recall만 명시 허용한 뒤 재실행했다. 승인 정책을 전역으로 끄지 않았다.

실제 청구 비용은 모든 실행에서 null이다. Claude의 `total_cost_usd`는 modelUsage의 costBasis=list인 목록 가격 산출값이므로 실제 과금으로 보고하지 않았다. 기억 작성/검토/승인의 사람 시간도 계측하지 않아 null이다. 이 소규모 실험은 A/B/C 비용 비교에 합산하지 않는다.

## 재현

`live_agents.py`는 기존 Codex/Claude 인증으로 모델을 실행한다. 정확한 FACTS에 사람 승인이 있어야 `--human-approved-facts`를 사용할 수 있다. 새로운 내용의 승인을 대신하지 않는다.

1. 기준 커밋의 새 복제본에 이 PR의 `internal/memory/cross_agent_http_postgres_test.go`만 복사한다. 아래의 사용자 전용 폴더를 만든 다음, 이 복제본에서 원본 제품을 빌드한다.
2. `evaluations/2026-10-15/api/run.sh`처럼 별도 loopback Postgres를 준비한다. 운영 DB를 사용하지 않는다. 아래의 DSN은 그 일회용 DB만 가리켜야 한다.
3. 권한 0700인 사용자 전용 디렉터리에서 다음을 실행한다. state에는 합성 테스트 토큰이 있으므로 공개하지 않는다.

```sh
export POSTGRES_TEST_DSN='postgres://arkism@127.0.0.1:PORT/arkism_ci?sslmode=disable'
install -d -m 700 "$HOME/arkism-reuse-evidence-20261015"
private_dir=$(mktemp -d "$HOME/arkism-reuse-evidence-20261015/run.XXXXXX")
# 기준 커밋의 독립 복제본에서 빌드:
go build -o "$private_dir/arkism-baseline" ./cmd/arkism
ARKISM_EVAL_FIXTURE_STATE="$private_dir/state.json" \
ARKISM_EVAL_FIXTURE_STOP="$private_dir/stop" \
  go test ./internal/memory -run '^TestCrossAgentHTTPPostgresReuse$' -count=1 -v > "$private_dir/server.log" 2>&1 &
# state.json 생성 이후, PR checkout에서 실행:
python3 evaluation/reuse-20261015/live_agents.py \
  --state "$private_dir/state.json" --arkism "$private_dir/arkism-baseline" \
  --output "$private_dir/results" --human-approved-facts
python3 evaluation/reuse-20261015/judge_live.py "$private_dir/results"
touch "$private_dir/stop"
# 서버 종료 후 본인이 생성한 임시 디렉터리/컨테이너를 정리한다.
```

서버는 stop 파일이 없으면 최대 15분 뒤 종료된다. 이 실험은 기억만 검증했다. 실제 모델의 스킬 수집→사람 승인→다른 모델의 실행은 아직 입증하지 않았다.

### Private reproduction paths

`live_agents.py`, `judge_live.py`, and `summarize_checks.py` accept artifacts
inside this evaluation directory or `$HOME/arkism-reuse-evidence-20261015`.
The latter directory must belong to the current user with mode `0700` and must
not be a symlink. Place the disposable fixture state and Arkism test executable
there before invoking the live harness. Arbitrary paths in a shared temporary
directory are rejected; symlinks cannot escape the selected artifact root.
