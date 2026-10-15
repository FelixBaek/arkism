# Arkism v0.1.0 재사용·효과 검증

검증 기준: 공개 릴리스 v0.1.0, `9379ee5f493716e1c80027c0beb4fd12283100cf`.
실행일: 2026-10-15, Asia/Seoul. 운영 데이터·서비스에는 쓰지 않았다. 이 PR은 팀 기능을 늘리지 않으며 main 병합이나 배포를 수행하지 않는다.

## 1. 검증된 재사용 범위

**기억은 실제 Claude↔Codex 양방향으로 전달되어 간단한 작업에 사용됐다. 그러나 출처 정확도와 승인본만의 전달은 별도 조건이 필요하다.**

| 검증 수준 | 실제 실행 | 결과·근거 |
| --- | --- | --- |
| 기준 서비스 계약 | 독립 서비스 인스턴스, 저장소/인증 fixture | 기존 `TestCrossAgentMemoryReuseLifecycle` 통과. 실제 모델 증거와 구분 |
| 기억 API | 다른 A/B 토큰·principal·HTTP transport, 실제 TCP HTTP, PostgresAuthorizer, 제한 DB 역할과 강제 RLS | 8개 하위 검사 ×3회 = 24/24. [실행별 JSON/CSV와 명령](../../evaluations/2026-10-15/api/README.md) |
| MCP 어댑터 | 원본 `arkism skill-router` subprocess, 매번 새 HOME/token/cache, 두 runtime 설정 | remember→승인 전 recall 없음→fixture 승인→다른 인증 recall 성공. runtime 이름 변경은 실제 모델 검증으로 계산하지 않음 |
| 실제 모델 | 실제 Codex A의 remember→사용자 승인 적용→새 Claude B recall/작업; 반대 방향도 실행 | 작업 적용 2/2, 요청 JSON 출처 정확도 1/2. [실제 도구 로그·판정](live-agents.md) |
| 스킬 API | TCP HTTP + 독립 A/B 인증 fixture, propose/review/apply/resolve | 기본 승인본 조회는 승인 후 다른 클라이언트에 본문·출처 전달. 실제 모델 실행이나 실제 사람의 스킬 승인은 아님. [독립 스킬 보고서](skills-review.md) |

기억 API는 승인 전 검색·컨텍스트 미노출, 에이전트 승인 시도 거부, 승인 후 본문·출처 조회, 수정 승인·거절·교체·삭제·시계 만료·충돌 보류/해소를 검사했다. 다른 프로젝트 검색은 0건, 무권한 scope/잘못된 토큰/비활성화한 principal은 내용을 반환하지 않았다. 일부 거부는 감사 기록 RLS 때문에 403 대신 503이었다. 이 응답 형식을 정상적인 권한 오류라고 일반화하지 않는다.

사람 승인 구분: 광범위 경계 검사는 자동 human-role fixture다. 실제 모델 전달과 A/B/C 실험은 이 채팅에서 사용자가 **정확한 합성 사실 5개를 명시 승인**한 후, 그 결정을 테스트 토큰으로 적용했다. 사람 시간은 계측하지 않았다. 실제 모델 B는 MCP에서 출처 참조를 받았으며, 별도 원문 출처 API까지 열지는 않았다. API 검사에서는 원문 출처 GET도 확인했다.

**스킬의 제한:** `@latest-approved`와 기본 라우팅은 승인본을 고르지만, 명시적 `id@version`은 미승인·폐기 버전도 검토/재생 목적으로 반환한다. 자동 승격 정책을 허용한 service principal 경로도 있다. 따라서 “어떤 경로에서도 미승인 스킬이 전달되지 않는다”는 주장은 v0.1.0에서 거짓이다. 기존 세션에 이미 전달한 내용과 연결이 끊긴 캐시는 즉시 회수할 수 있다는 보장이 없다. [해당 테스트·코드 근거](skills-review.md#contract-limits-not-silently-relabeled-as-defects)

## 2. 발견한 결함과 이번 수정

| 결함 | 기준 버전 재현 | 이번 변경 |
| --- | --- | --- |
| ID 기반 MCP recall이 이력 조회 본문을 현재 기억으로 전달 | superseded/expired 관리 GET의 실제 DB 응답 확인 + `TestRecallByIDWithholdsUnavailableMemory` 9개 사례 실패 [로그](checks/baseline-recall-regression.log) | active·승인·유효 시간·삭제/격리 상태를 확인 후 전달. 이력 관리 GET 자체는 유지. malformed 승인/격리 사례도 보강 |
| 스킬 resolve가 개별 항목 읽기 거부를 우회 | 스킬·버전·출처 deny 3종에서 일반 GET은 거부되지만 resolve는 본문 반환 [로그](skills-baseline.log) | resolve 결과마다 동일 호출자의 개별 읽기 권한 재검사. [회귀 검사](../../internal/memory/cross_agent_skill_http_test.go) |
| 실제 모델의 출처 혼동 | Claude가 source_id 자리에 memory ID 기입 | JSON 필드 판정은 실패 유지. 설명에 정답 출처가 있어도 성공으로 고치지 않음. 모델 출력 오류이며 제품 코드 수정으로 해결됐다고 주장하지 않음 |

스킬 권한 결함은 실제 HTTP handler와 항목별 거부 정책을 가진 authorizer fixture로 재현했다. 현재 Postgres의 scope 중심 권한 설정에서 실제 운영 계정 유출이 관측됐다는 뜻은 아니다.

수정 과정의 실패도 보존했다. 첫 스킬 권한 수정이 DB transaction 안에서 별도 연결을 요구해, 연결 1개 환경의 기존 `TestPostgresSkillAvailability`에서 대기했다. 전체 PostgreSQL 검사에서 발견했으며, 해당 수정은 검토 후 바로잡았다. 기준 릴리스의 결함 수에 이 개발 중 회귀를 더하지 않는다. 최종 검사 결과는 `checks/final-go-summary.json`에 기록한다.

## 3. 조건별 실제 측정

5개 합성 Python 작업 ×3조건 ×3회, **실제 Codex 새 세션 45회**. 같은 모델/설정/시작 코드, 전체 순서 섞기, 매번 새 프로필과 C의 새 DB를 사용했다. 기억에는 도메인 사실만 넣었고 독립 실행 평가 기준은 숨겼다. 정적 문서와 C의 모델 입력 정보는 동일했다.

| 조건 | 성공 | 규칙 검사 실패 | 시간 평균±표준편차 | 입력 / 출력 토큰 합계 |
| --- | ---: | ---: | ---: | ---: |
| A 기억 없음 | 5/15 | 10 | 10.882±2.365초 | 213,288 / 2,074 |
| B 정적 문서 | 15/15 | 0 | 7.023±1.615초 | 213,408 / 645 |
| C Arkism 승인 기억 | 15/15 | 0 | 7.073±1.132초 | 213,396 / 592 |

C 검색 15회: 평균 12.800ms, 표준편차 2.100ms, 범위 10.345–17.911ms. 별도 race API 경계 검사의 검색 36회는 평균 26.153ms/표준편차 16.205ms이며 실험 설정이 달라 합치지 않았다.

시간은 CLI 시작/추론이며 검색·DB 준비·판정 실행을 제외한다. C의 기계 준비는 별도로 평균 0.266초다. 실제 청구 비용, 사람의 기억 작성·검토·승인/문서 유지 시간은 **미측정(null)**이다. 재작업 0회는 한 번만 답하게 한 설계다. 실패 복구 비용이 0이라는 뜻이 아니다.

[실행별 45행·과제별 결과·실패 사례·편차](../reuse-v010/benchmark/REPORT.ko.md), [JSON](../reuse-v010/benchmark/results/results.json), [CSV](../reuse-v010/benchmark/results/results.csv), [고정 과제와 실행 순서](../reuse-v010/benchmark/results/frozen-spec.json), [환경·명령·한계](../reuse-v010/benchmark/README.md)를 함께 남겼다.

## 4. 아직 입증하지 못한 효과

**정적 문서보다 작업 품질이나 비용이 좋아졌다는 증거는 없다.** 두 조건 모두 15/15였고, 사전에 정한 “정적 문서보다 성공 3건 이상 추가”를 충족하지 못했다. A의 실패는 전달하지 않은 임의의 프로젝트 정책을 몰랐기 때문일 수 있다. 정보 제공 효과를 Arkism 고유 효과라고 해석하지 않는다.

- C는 호스트가 실제 API에서 검색해 모델에 전달했다. 45회에서 모델이 자율적으로 MCP/스킬을 선택한 것은 아니다. 실제 MCP 양방향 2건은 별도다.
- 짧은 코드 생성 과제다. 실제 저장소 수정·빌드·장기 인계·복잡한 규칙 준수나 5개 서로 다른 대형 작업에 대한 효과는 검증하지 않았다.
- 본문이 같은 정적/C 입력은 검색 출처/판단 비용을 통제하지만, 다수 기억 검색·관련성/충돌 판단·출처 활용의 장점도 측정하지 못한다.
- 스킬을 실제 다른 모델이 실행했을 때의 효과, 실제 사용자 자료의 자동 수집 품질, 데스크톱 캡처, 벡터 검색, 규모 증가 시 성능, 사람 검토 부담을 포함한 총비용은 미검증이다.
- 모델 seed/temperature는 제공되지 않았고, 요청한 출력 상한은 CLI가 지원하지 않았다. 동일 기본 설정으로 실행했다. 제공자 prefix cache는 남아 있어 시간 비교에 영향을 줄 수 있다. [환경 기록](../reuse-v010/benchmark/environment.json)

## 5. 다음 우선순위

1. 이번 권한·현재 기억 조회 수정부터 검토한다. 명시적 스킬 버전 조회를 역사 재생과 현재 작업 도구로 구분할지 제품 정책을 정한다.
2. 실제 저장소 과제를 고정하고 같은 정보의 유지된 정적 문서와 비교한다. 변경/삭제/충돌 이후의 새 세션과 스킬 실행까지 포함한다.
3. 작성·검토·승인·문서 유지 시간을 실제 사람 기준으로 계측하고, 제공자가 실제 비용을 제공하는 계정으로 비용을 비교한다. 원문 출처를 정확히 인용하는지 독립 판정한다.

## 버전·환경·재현 기록

처음에는 공개 HEAD와 v0.1.0 태그가 모두 기준 커밋이었다. 작업 도중 공개 main에 [PR #43](https://github.com/FelixBaek/arkism/pull/43)이 병합되어 `edc6f934101c12a218a02bef8a9c686f962863a6`이 됐다. 이는 캡처 감사 문서/도구 및 패치 파일 변경이다. 위 릴리스 측정에 섞지 않았다. 이번 PR의 제품 수정은 기준 버전 **이후 변경**이다.

공개 기준 버전에 AGENTS.md는 없었다. 사용자가 제공한 Git 규칙과 `guide/memory-evaluation.md`, 기존 재사용 테스트·연결 설정을 읽었다. 세 병렬 담당자는 각각 독립 clone/DB/세션을 썼다. 루트는 통합·전체 검사·실제 양방향 모델 실행을 맡았다. [독립 측정 검토](independent-measurement-review.md)

기준 버전 전체 Go race+integration 검사: 최상위 1,980개 통과, 3개 skip; 하위 검사를 포함하면 6,255개 통과. 3개 skip을 성공으로 계산하지 않았다. `go vet ./...` 통과, UI 29/29 통과. [기준 요약](checks/baseline-go-summary.json), [원본 압축 JSONL](checks/baseline-go.jsonl.gz), [UI 로그](checks/baseline-ui.log). 최종 수정 검사와 공개 전 검사 결과는 [검사 목록](checks/validation.json)에 별도 기록한다.

재현 시작점:

```sh
# 독립 API/MCP 반복 검사: 새 컨테이너/DB를 만들고 삭제한다.
./evaluations/2026-10-15/api/run.sh /tmp/arkism-reuse-rerun
python3 evaluations/2026-10-15/api/summarize.py /tmp/arkism-reuse-rerun
# 직접 ID recall / 스킬 권한 회귀 검사
go test -race ./internal/host/skills -run '^TestRecall' -count=1
go test -race ./internal/memory -run '^Test(CrossAgentSkillHTTP|SkillResolve)' -count=1
# 실제 모델 45회 및 실제 양방향은 해당 README의 독립 DB/인증 설정을 따른다.
```

모든 공개 로그는 합성 데이터만 담는다. 인증 파일·운영 데이터는 포함하지 않았다. `.gitignore`, `.dockerignore`, `.env.example`을 추가/변경하지 않았고, 무시 규칙은 `.git/info/exclude`에만 뒀다.
