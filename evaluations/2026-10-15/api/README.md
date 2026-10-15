# API / MCP 재사용 검증

기준: 공개 v0.1.0 `9379ee5f493716e1c80027c0beb4fd12283100cf`.
이 디렉터리의 기록은 **제품 코드 변경 없이** 추가 테스트로 측정했다.

## 실행

```sh
./evaluations/2026-10-15/api/run.sh /tmp/arkism-api-rerun
python3 evaluations/2026-10-15/api/summarize.py /tmp/arkism-api-rerun
```

Docker, Go, 로컬의 `supabase/postgres:17.6.1.136` 이미지가 필요하다.
스크립트는 새 컨테이너와 임의 loopback 포트를 만들고 종료 때 삭제한다.
Go 테스트는 반복마다 새 DB와 `NOSUPERUSER NOBYPASSRLS` 앱 역할을 생성·삭제한다.
A/B는 서로 다른 인증 토큰·principal·HTTP transport를 쓴다.
실제 HTTP handler, Postgres 저장소, PostgresAuthorizer, 강제 RLS를 통과한다.
실제 운영 DB/서버/설정은 사용하지 않았다. 임베딩 제공자는 없다.

## 관측 결과

`TestCrossAgentHTTPPostgresReuse`의 8개 하위 테스트 × 3회가 모두 통과했다(24/24).
`-race`를 적용했다. `results.jsonl`은 Go 원본 실행 로그,
`runs.csv`는 실행별 결과, `requests.csv`는 실제 HTTP 요청별 관측 시간,
`summary.json`은 집계다. 모든 데이터는 비식별 합성 fixture다.

| 테스트 이름 접미사 | 검증한 동작 |
| --- | --- |
| approval_provenance_scope_and_revocation | A 수집 → B 승인 전 검색 0; A/B 승인 시도 403; human-role fixture 승인 후 B 검색·컨텍스트·출처 조회; 타 프로젝트 검색 0; 무권한 범위와 잘못된 토큰 차단; 삭제 후 검색 0 |
| rejected_candidate | 거절한 후보 검색 0 |
| superseded | 교체 전 기억 검색 제외, 교체본만 검색 |
| edited_approval | 수정 승인한 본문만 검색 |
| removed_source | 출처 삭제 요청이 202로 수락된 직후 재검색 0 |
| expired | 미래 만료를 설정하고 실제 시간이 지난 뒤 검색 0 |
| conflict | 충돌 보류 중 검색 0, 둘 중 하나 유지로 해소한 뒤 유지본만 검색 |
| mcp_subprocess_capture_and_recall | 실제 빌드한 `arkism skill-router` 새 subprocess에서 remember → 승인 전 recall 0 → fixture 승인 → query/ID recall 1; codex/claude_code runtime 설정 양방향 |

끝에는 B principal을 비활성화하고 검색이 차단됨을 확인한다.
권한 없는 workspace와 비활성 principal의 요청은 503이었다. 거절 감사 기록의
RLS 쓰기도 막히는 fail-closed 경로이며, 본문/검색 결과는 노출되지 않았다.
잘못된 토큰과 에이전트의 승인 시도는 403이었다.

직접 요청한 성공 HTTP 검색 36건: 평균 26.153ms, 표본 표준편차 16.205ms,
최소 10.559ms, 최대 65.491ms. 이는 `-race` + 작은 합성 DB의 loopback HTTP
전체 시간이다. MCP 내부 검색과 임베딩 시간은 이 숫자에 포함하지 않았다.
성능 개선 또는 운영 검색 지연으로 일반화할 수 없다.

## 발견한 경계와 남은 검증

`superseded`와 `expired` 테스트에서 `/v1/memories/:id` 관리 조회는 이전 본문을
200으로 반환했다. 이것은 관리 이력 조회 동작이다. 따라서 이 API를 그대로
MCP 컨텍스트에 넣는 어댑터는 별도 유효성 검사를 해야 한다. 어댑터 수정 전후
회귀 검증은 상위 평가 보고서에서 따로 다룬다.

human-role fixture의 호출은 자동화했다. **사람의 명시적 검토·승인이 실제로
일어났다는 증거가 아니다.** MCP runtime 이름만 바꾼 subprocess 역시
Claude/Codex 모델 실행이 아니다. 이 검증은 기억 조회 가능성을 입증하며,
실제 모델의 작업 준수·품질·비용 효과를 입증하지 않는다. 모델 토큰·청구 비용·
사람 검토 시간은 미측정(null)이다. 스킬 경로는 별도 담당 검증에 있다.
출처 삭제의 검색 차단만 확인했으며 백그라운드 전체 물리 삭제 완료는 주장하지 않는다.

## 선택적 외부 클라이언트 fixture

`ARKISM_EVAL_FIXTURE_STATE=/tmp/private-state.json`와
`ARKISM_EVAL_FIXTURE_STOP=/tmp/private-stop`를 설정하면 테스트 서버가
최대 15분 유지된다. state는 0600 파일이며 loopback URL, 합성 토큰, scope만
담는다. stop 파일을 만들면 정상 종료하고 state를 제거한다. 이 파일은
공개 결과나 저장소에 넣지 않는다. 실제 에이전트를 붙이기 위한 편의 기능이며
그 자체로 실제 에이전트 실행을 증명하지 않는다.
