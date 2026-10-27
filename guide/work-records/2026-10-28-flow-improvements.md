# 2026-10-28 FLOW 실행에서 스킬 개선 이력 연결

작업 ID: ST-611 / FR-077 / API-035. 내부 통합 계획 WFI-09에 해당하며 별도 공식 Story ID를 만들지 않았다.

Task: ST-611. Research: none. 관련 대화 ID: `01a17e5f-0d81-78a1-bbdf-1ee00be8fb2c`.

기준 HEAD: `1196ccc28cf771402944b67def209a1b38e118ae`. 기존 FLOW 통합 위에 추가한 작업이며, 이전 커밋의 최종 검사를 이번 변경의 검사 결과로 승계하지 않는다.

## 이유와 변경

실행 기록과 스킬 생성·독립 비교·승인·제한 시험·적용·관찰·복구가 각각 존재해도, 하나의 개선 이력으로 추적하지 못하는 부분을 연결했다. 기존 스킬 API의 검증과 권한을 재사용하며 새 평가기나 자동화 권한을 만들지 않는다.

- 종료된 실행의 ID/revision, skill ID와 request ID를 고정한 관리 기록을 기존 jobs 저장소에 보관한다. 실행 참조는 개선을 시작한 맥락이고, 별도의 검증된 lesson 재료를 임의 실행 요약으로 대체할 수 없다.
- 생성·비교·승인·trial·관찰·적용·감시·복구를 명시적 단계로 연결한다. agent/pipeline 승인은 거부하고, service 자동화는 현재 운영 정책이 있을 때만 기존 API가 허용한다.
- 단계 의도를 먼저 저장하고 같은 action ID·본문·주체에만 영수증을 재사용한다. 처리 결과가 불명확한 단계는 새 모델 호출이나 적용으로 자동 반복하지 않는다. 결정적인 기존 기록을 확인하는 recover만 허용한다.
- 독립 비교 복구는 원래 구조화된 측정 본문과 연결된 실행·평가 기록을 확인한다. 자유 형식 성공 요약은 비교 결과로 받아들이지 않는다.
- 사람의 명시적 적용에도 현재 시험 근거를 사전 확인한다. service의 자동 적용은 기존 적용 트랜잭션 안에서 정책과 전체 시험 근거를 다시 확인한다. 다른 agent 범위의 관측은 같은 주체를 유지하면서 현재 읽기 권한을 재검사한다.
- 목록은 scope·kind를 저장소에서 먼저 제한하고 참조·상태만 돌려준다. 상세에는 기존 타입의 생성 계획과 영수증을 제공하며 독립 평가로 해석할 수 없도록 자료 권한을 표시한다.
- 에이전트 앱의 기존 skill-router에 시작·상세·목록·명시적 한 단계 진행 도구를 연결했다. 호출자가 제공한 기존 typed 근거만 전달하고 클라이언트의 scope/token을 유지한다. 도구에서 평가·승인·다음 단계를 자동 선택하거나 거부 뒤 다른 자격 증명으로 우회하지 않는다.

구현은 [관리 API](../../internal/memory/flow_improvement.go), [단계 예약·완료](../../internal/memory/flow_improvement_advance.go), [기존 서비스 연결](../../internal/memory/flow_improvement_actions.go), [시험 근거 재확인](../../internal/memory/flow_improvement_evidence.go), [목록](../../internal/memory/flow_improvement_list.go)에 있다. 사용법은 [개선 흐름 가이드](../flow-improvements.md)에 기록했다. 공통 모델·라우트 정책·OpenAPI와 UI는 통합 담당자의 별도 변경이다.

## 실행한 검증

| 검사 | 결과와 범위 |
|---|---|
| `go test ./internal/memory -run '^TestFlowImprovement' -count=1` | 변경 단계에서 통과. 실제 Service의 종료 실행과 개선 HTTP 라우트, fake 제공자의 후보 생성, 독립 측정 비교, 명시 승인, 정책 trial, 관찰, 적용, 회귀 중단, 별도 baseline 검증 후 rollback 연결 확인 |
| 같은 개선 회귀 | 동일 요청 재사용, 동시 생성 1건, 후보 저장 후 관리 갱신 실패의 복구, 원래 실행 revision/삭제·현재 권한 검사, 목록 필터·페이지·본문 제외 확인 |
| 교차 agent scope와 정책 회귀 | 현재 읽기 허용 시 관찰·상세·목록 연결, 읽기 철회 시 차단, 운영 정책 제거 시 observe 차단, 사람의 service 가장 차단, 관측 후 생긴 독립 부정 평가가 human apply를 막는 것을 확인 |
| 복구 보완 후 개선 회귀 | 변경된 비교 원문을 독립 비교 완료로 복구하지 않는 것, 기존 서비스가 이미 승격한 trial을 관찰 대기로 되돌리지 않는 것 확인 |
| `go test -race ./internal/memory -run '^(TestFlowImprovement\|TestSkillGeneration\|TestSkillComparisonRecord\|TestSkillTrial\|TestSkillAutomation)' -count=1` | 최종 개선 코드와 선택한 기존 스킬 생성·비교·시험·자동화 회귀 통과. 실제 DB 검증을 포함하는 이름의 별도 테스트는 이 선택에 포함하지 않음 |
| `go test -race ./internal/host/skills -run 'FlowImprovement\|RouterGolden\|Workflow' -count=1` | 신규 MCP payload·scope 결속, 불필요한 다음 단계/재시도 없음, 서버 거부 유지, scope/token 덮어쓰기 거부, 기존 workflow·도구 목록 회귀 통과 |
| `ARKISM_UPDATE_GOLDEN=1 go test ./internal/host/skills -run '^TestRouterGoldenSessionAndTools$' -count=1` | 네 MCP 도구 계약에 맞춰 기존 도구 목록 fixture 갱신. 위 race 검사에서는 갱신 옵션 없이 일치 확인 |
| `go test -race ./internal/memory -run '^TestFlow(Common\|Children)' -count=1` | 실제 Handler·Client·Engine과 fake 실행기의 전문가·팀·중첩 workflow 통합 통과. 상세 경계는 아래 후속 검사 절 참조 |
| `go test -race ./internal/host/skills ./internal/host/capture -run 'FlowImprovement\|WorkflowRunList\|RouterGolden\|Workflow\|InstGuidance' -count=1` | 재접속 실행 목록 projection, limit·scope/token 경계, 기존 MCP workflow와 설치 지침의 영역 보존·멱등성 통과. 실제 앱 설치는 수행하지 않음 |
| `go test -race ./internal/host/skills -run 'FlowImprovement\|Workflow\|RouterGolden\|Crew' -count=1` | 복잡도 helper 분할 이후 신규 목록·개선과 기존 Crew/workflow·golden 동작 보존 재확인 통과 |
| `go run ./tools/complexity --report` | 최종 담당 코드에서 통과. 새 개선 함수·MCP helper의 복잡도는 20 이하이며 기존 baseline은 변경하지 않았다. 중간 통합 검사에서 공통 함수 21로 한 번 실패했고 담당자가 helper 분리 후 다시 확인 |
| `python3 tools/check_work_record.py --report` 및 문서 상대 링크 확인 | 이 작업 기록의 형식과 두 신규 문서의 상대 링크 대상 확인 |
| `git diff --check` | 공백 오류 없음 |

원시 로그는 작업 호스트의 `/tmp/arkism-flow-improvement-race.log`, `/tmp/arkism-flow-improvement-mcp-race.log`, `/tmp/arkism-flow-common-improvement-tests.log`에 보관한다. 저장소 전체 check_local 및 통합 담당자의 최종 검사는 대기 중이다.

검증은 localhost의 fake 모델 제공자와 합성 실행·권한·정책 fixture를 사용했다. 독립 체크 API가 고정 fixture 측정 결과를 기록하며 실제 모델 성능이나 운영 독립 평가 품질을 증명하지 않는다. 테스트가 사용하는 자동화 정책은 운영에 설치하지 않았다.

## 실패·한계·미실행

초기 통합 중 공통 모델·라우트 코드가 완성되기 전 컴파일 실패와 전체 HTTP 등록의 경로 충돌이 있었다. 개선 모듈 검사는 실제 권한 mux에 담당 라우트만 등록하여 분리했다. 관찰 fixture의 빈 출처 평가 생성은 유효하지 않아 실패했고, 기존 독립 trial outcome API로 근거를 기록하도록 고쳤다. 독립 읽기 검토에서 교차 agent scope 재바인딩과 승격 상태 복구 누락을 찾아 수정하고 회귀를 추가했다. 비교 원문 변조 회귀의 기대 오류를 conflict 한 종류로 한정했던 테스트는 실제 읽기 경로가 불확실한 상태를 유지하면서 거부하는 동작까지 허용하도록 고쳤다. 비교 완료로 승격되지 않는다는 요구는 유지했다.

## 후속 공통 실행 통합 검사

같은 담당자가 [공통 실행 검사](../../internal/memory/flow_common_test.go)와 [자식 실행 검사](../../internal/memory/flow_children_test.go)를 추가했다. 실제 Handler·HTTP Client·Engine과 가짜 로컬 실행기를 사용하며 운영 호출은 없다.

일반 실행에서 전문가 역할·결과·완료 재실행 방지, 재귀 지원 범위 확인, 중첩 2단계에서 동시 실행 한도 1, 자식 시작 전 정의 변경의 pin 보존, 상위 화면의 사람 승인 전달, service/agent 승인 거부, 새 엔진에서 재개, 취소 후 모든 자식 종료, 현재 하위 읽기 권한, 자식 attempt 중복·상이 본문·lease 검사와 필수 자식 완료 전 부모 완료 거부를 확인했다. 별도 화면에서 자식의 결정을 변경했을 때 오래된 상위 승인이 자식을 덮어쓰거나 부모 일부만 변경하지 못하는 것도 확인했다.

팀 실행은 처음에 첫 회의 progress에서 invalid_request로 실패했다. 서버가 저장한 전체 snapshot과 worker의 제한된 wire 타입이 다르게 왕복하는 경계를 발견해 통합 담당자에게 전달했다. 담당자가 연결된 회의의 snapshot을 서버 정본으로 보존하도록 수정한 뒤 fake 회의의 conclusion이 저장되고 부모 결과까지 도달하는 것을 포함해 common/children 전체를 race로 재검증해 통과했다. 원시 로그는 작업 호스트의 `/tmp/arkism-flow-common-children-race.log`에 있다.

## 재접속 실행 목록

후속 요청으로 `list_workflow_runs` MCP와 설치 공통 지침을 추가했다. 기존 실행 목록 API를 현재 scope로 읽고 ID·작업 ID·상태·작업 종류·부모 ID·생성 시각·종료 결과 조회 가능 여부만 내보낸다. 입력·정의·대기 질문·출처 채널·lease·결과 본문은 목록에 노출하지 않는 회귀를 추가했다. 종료 결과 조회 가능 여부는 성공·품질·전달 완료를 뜻하지 않는다.

사용자의 현재 작업과 관련된 실행을 선택한 뒤 기존 ID로 get/wait를 이어가며, 목록 전체의 결과를 자동 주입하거나 다시 시작하지 않는다. 닫힌 대화로의 push는 여전히 미지원이다. 새 도구를 포함해 golden을 다시 갱신하고 옵션 없는 검증으로 일치를 확인했다. router의 복잡도가 21로 증가해 기존 Crew 목록 처리만 helper로 분리했으며 baseline은 바꾸지 않았다. 사용법은 [FLOW 가이드](../flow.md)에 있다. 검증 로그는 작업 호스트의 `/tmp/arkism-flow-reconnect-mcp-race.log`에 보관한다.

현재 원래 실행·lesson·참조의 삭제나 권한 철회는 보수적으로 상세/진행을 차단한다. 원래 실행은 생성 lesson의 자동 출처 변환이 아니라 시작 맥락이다. 실제 trial 사용 기록이나 별도 비교 결과가 없으면 단계가 대기하며 완료를 만들어내지 않는다. 독립 baseline 검증이 없으면 복구 대상을 임의로 만들지 않는다.

미실행: 실제 운영 실행에서 후보 생성, 실제 모델·독립 평가 실행기 호출, 운영 자동화 정책 설치·승인, 운영 스킬 적용·복구, 배포, Discord 외부 송신, 실제 운영 PostgreSQL/RLS 검증. 이번 담당 범위는 새 API와 fixture 검증이며 UI 렌더링은 별도 담당 범위다.

연구 protocol/run ID: 없음. 연구 고정 계약·실험 결과·observed commit 귀속은 변경하지 않았다. 공개 문서에 원대화·개인 설정·비공개 프롬프트를 복사하지 않았다.

## 커밋 상태

커밋 상태: 기준 커밋 `1196ccc28cf771402944b67def209a1b38e118ae` 위의 미커밋 작업 트리 변경. 이 담당자는 commit/push하지 않았으며 통합 담당자가 최종 검사와 커밋을 처리한다.

## 독립 검토 후 수정 (이어받기)

이전 담당자의 사용량 한도로 끊긴 검토를 다시 수행했다. 읽기 전용 검토에서 권한 우회는 찾지 못했고, 다음 네 가지를 고쳤다.

- 지워지거나 격리된 하위 실행·회의가 부모의 종료와 취소를 영구히 막던 문제: 해당 자식은 끝났지만 성공하지 않은 것으로 보고 나머지 자식 취소를 계속한다. 필수 자식이면 부모 `succeeded`는 여전히 거부한다. 회귀 `TestFlowChildrenQuarantinedChildEndsWithoutSuccess`.
- 결과 불명(`unknown`) 호출이 루트 전체의 동시 실행 한도를 영구 점유하던 문제: 진행 중인 예약만 한도에 센다. 재전송 차단과 예산 계산은 유지한다. 회귀 `TestFlowAttemptUnknownOutcomeDoesNotHoldConcurrency`.
- 이 변경 전에 만들어진 FLOW 실행(root·policy 없음)이 무한 재시도하던 문제: `flow_legacy_run` 정책 거부로 실패 처리한다. 회귀 `TestFlowAttemptLegacyRunFailsClosed`.
- 전달 복구의 권한 감사 대상 ID를 실행 ID에서 작업 ID로 바로잡고, 하위 실행 기록 갱신을 잠금 안에서 한다.

같은 이어받기에서 바뀐 도구 목록·지운 결과의 404 응답에 맞춰 테스트 기대값을 갱신하고, 설치 지침 golden과 영어 문구를 보강했으며, 복잡도 21인 세 함수를 helper로 나눴다.

| 검사 | 결과 |
|---|---|
| `go vet ./...`, `go run ./tools/complexity` | 통과 |
| `go test -race ./internal/memory/... ./internal/crewrun/... ./internal/host/...` | 통과 |
| `node --test desktop/ui/*.test.js` | 512/512 통과 |
| `update_api_contracts.py --check`, `check_openapi.py --cases` | 80 operation 일치, 299 operation·실제 응답 228건 검증 |
| 임시 PostgreSQL 18의 FLOW 테스트 | 3건 통과 |

한계: lease를 잃어 정산되지 않은 예약은 여전히 동시 실행 슬롯을 차지한다(만료 처리 지점이 아직 없음). 운영 배포·실제 모델·Discord 발송은 하지 않았다.
