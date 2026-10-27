# 2026-10-28 FLOW 통합과 기존 리서치 확장

작업 ID: ST-611 / FR-077 / API-035. 기존 TC-CREW 계열의 회귀를 보강한다. WFI 번호는 내부 계획의 단계 구분이며 새로운 공식 Story ID가 아니다.

Task: ST-611. Research: none. 관련 대화 ID: `01a17e5f-0d81-78a1-bbdf-1ee00be8fb2c`.

기준 HEAD: `4717e1d5f8fd4fa2483a1d04beb4a973b73df4b1`. 현재 변경은 이 기준의 작업 트리에 있다. 기록 규약은 2026-10-21 v8.1과 저장소 AGENTS를 따랐다. 연구 고정 계약과 기존 실험 결과의 귀속은 바꾸지 않았다.

## 이유와 실제 범위

사용자가 전문가·팀의 별도 메뉴를 FLOW로 합치면서 기존 기능을 유지하고, 저장된 작업을 에이전트 앱과 Discord에서 호출해 최종 결과까지 받도록 요청했다. 후속 결정에 따라 별도 딥리서치 정의를 기본 설치하는 대신 기존 리서치를 개선한다. 심층 전환 기준은 경과 시간이 아닌 근거 부족이다. Discord를 먼저 구현하고 다른 메신저는 후속으로 둔다.

- FLOW 목록에 기존 전문가·팀·워크플로우를 모으고 역할·페르소나·모델·스킬·도구·팀 구성·회의·자동화·그래프·기록 편집을 유지했다.
- 리서치는 기존 항목을 먼저 열고, 개선안을 읽기 전용으로 제시한다. 명시적 저장 시 같은 ID와 읽은 revision으로 수정한다. 기존 그래프·모델·검색 연결·승인·예산을 보존하고 실제 사용 가능한 깊은 별칭을 보여준다.
- 새 FLOW 실행의 요청 중복 방지, 정의 고정, 실행 lease와 중단 요청, MCP의 기존 실행 조회·반복 대기, Discord의 영속 전달과 부분 영수증을 추가했다. 기존 assist·meeting·legacy 호출의 지원 차이를 유지해 표시한다.
- 구조화된 전체 조사 보고서와 출처를 공통으로 전달한다. 출처 없는 최종 결과·잘못된 형식·저장 한도 초과는 완료로 표시하지 않는다. 사실 진위 검증이나 조사 품질 개선의 증명으로 확대하지 않는다.
- 기본 조사 템플릿에는 확인된 Claude CLI의 읽기·검색 도구 제한을 사용한다. 기존 다중 모델 리서치 실행기를 강제 교체하지 않는다.
- 필수 검사에서 증가한 host 복잡도는 기능별 helper로 분리했다. MCP 입력·호출·결과, 인용문 제외, 격리 환경·비밀 제거, 호출 이름 예약을 나누고 복잡도 baseline을 수정하지 않았다.

사용자 안내는 [FLOW 가이드](../flow.md)에 있다. 변경 근거 코드는 [UI](../../desktop/ui/crew.js), [리서치 제안](../../internal/memory/flow_research.go), [실행](../../internal/crewrun/flow_execution.go), [보고서](../../internal/flowresult/result.go), [MCP](../../internal/host/crew/flow_runs.go), [Discord 전달](../../internal/host/discord/delivery.go), [제한된 조사 실행](../../internal/host/crew/research_runner.go)로 나뉜다. 공개 문서에는 비공개 원대화·사용자의 그래프 원문·개인 설정을 복사하지 않았다.

## 설계 기록과 코드의 차이

2026-10-20 내부 기록에는 실행 lease, run-recovery, trace 전달 outbox와 관련 검증이 이미 있는 것으로 적혀 있었다. 이번 기준 HEAD에는 그 기록이 지목한 구현 파일과 계약 일부가 없었다. 이전 기록의 검증을 이번 브랜치에 승계하지 않았다.

이번 코드의 FLOW lease·중단 API와 Discord 전달 작업은 실제 확인한 새 구현이다. 현재 `/v1/crew/deliveries` 계약은 과거 문서의 `/v1/crew/run-deliveries` 및 trace 전달과 같은 기능으로 간주하지 않는다. 새 FLOW 실행의 보장을 기존 모든 실행 경로나 외부 시스템의 정확히 한 번 처리로 확대하지 않는다. 내부 backlog·API·추적표에 이 차이를 별도로 남긴다.

초기 통합 계획의 독립 딥리서치 기본 설치안은 기존 리서치의 명시적 개선안 저장 방식으로 대체됐다. 호환용 독립 템플릿은 유지하지만 기존 리서치를 자동 교체하지 않는다.

## 실행한 검증

아래는 담당자가 실제 실행한 검사다. 서로 다른 수정 단계의 결과를 한 최종판의 전체 검사 결과로 합산하지 않는다. 테스트 수는 생략한다.

| 단계·명령 | 확인 결과와 범위 |
|---|---|
| 초기 FLOW 통합: `node --test desktop/ui/*.test.js` | 당시 UI 전체 검사 통과. 이후 리서치 우선·개선안 UI 수정이 있으므로 최종 전체 검사 근거로 사용하지 않음 |
| 리서치 UI 수정 후: `node --test`로 [flow-catalog](../../desktop/ui/flow-catalog.test.js), i18n, crew-screens 테스트 실행 | 통과. 기존 정의 우선, 저장 전 미리보기, 별칭·revision 처리 및 문구 회귀 |
| UI 수정 후: frontend의 `lint`, `typecheck`, `build`, `check`에 해당하는 고정 도구 실행 | ESLint·TypeScript·esbuild와 산출물 검사 통과. 로컬에 이미 설치된 고정 의존성 사용 |
| UI 수정 후: `node`로 [FLOW 브라우저 검사](../../desktop/tests/flow.browser.cjs) 실행 | 소스와 번들에서 통과. 기존 리서치 열기, 개선안 미리보기의 무변경, 역할/그래프 이동, 입력 유지, Enter·Tab·Esc, 테마와 좁은 화면 확인 |
| 초기 FLOW 통합: `node desktop/tests/ui-structure.browser.cjs` | 당시 기존 화면·사용자 문자열·키보드 회귀 통과. 실제 Tauri/Windows 패키지 검증과 구별 |
| Better Design 로컬 검사: FLOW CSS | 지적 없음. 이후 해당 CSS는 변경하지 않음. 렌더링 검증은 위 브라우저 검사로 별도 수행 |
| 제한된 연구 실행: `go test ./internal/host/crew -run Research -count=1` | fake CLI로 도구 인자·환경·미지원 실행기·출력 비밀 제거 검사 통과 |
| 연구 실행 최종 보완: `go test ./internal/host/crew -run 'Research\|SpecReaches\|Access\|Handoff\|OneShot\|KeyReaches\|SelectedLogin' -count=1` | 선택한 기존 실행·모델·권한 회귀와 연구 실행 검사 통과 |
| host 리팩터 후: `go test -race ./internal/host/crew ./internal/host/skills -run 'Workflow\|Mentions\|SpecReaches\|Access\|Research\|OneShot\|Handoff\|KeyReaches\|SelectedLogin\|CrewGoldens\|CrewMain' -count=1` | 선택한 기존 회귀를 race 포함 통과 |
| host 리팩터 후: `go test ./internal/host/crew ./internal/host/skills -count=1` | 로컬 테스트 서버를 사용할 수 있는 환경에서 두 패키지 전체 통과 |
| host 리팩터 후: `go run ./tools/complexity --report`로 inventory 생성 | 담당 네 함수가 각각 43→5, 30→9, 30→17, 22→16. 새 helper도 20 이하. 실행 시 다른 담당 영역의 위반은 남아 있어 전체 명령은 실패; 전체 통과로 표시하지 않음 |
| 변경 파일의 `git diff --check` | 당시 공백 오류 없음 |
| 문서 작성 후: `python3 tools/check_work_record.py --report`로 이 기록 검사 | 형식 검사 통과. 공개 문서의 상대 링크 대상 존재 확인도 완료. 기록 형식 검사는 구현 성공의 증명이 아님 |

브라우저는 로컬 fixture를 사용했고 Claude CLI는 버전과 도움말만 조회했다. 실제 모델 호출이나 운영 데이터 변경은 이 검증에 포함하지 않는다. 원시 로그·화면 캡처는 작업 호스트의 임시 근거로 보관하며 공개 저장소에 원대화·자격 증명을 포함하지 않는다.

## 실패·미확인·미실행

초기 UI 검사에서 이전 메뉴·문구 기대값이 실패해 수정 후 다시 확인했다. sandbox의 로컬 소켓 제한으로 실패한 검사는 로컬 포트가 가능한 실행 환경에서 다시 확인했다.

연구 실행 수정 시점의 `go test ./internal/host/crew -count=1`에서는 동시에 바뀌던 실행 응답에 새 필드가 추가되어 기존 실행 모양 및 golden 기대값 두 곳이 실패했다. 이 결과를 당시 전체 패키지 성공으로 기록하지 않았다. 통합 담당자의 이후 수정·최종 검사 결과는 별도로 갱신해야 한다.

이후 host 리팩터 시점의 두 패키지 전체 검사는 sandbox 로컬 소켓 제한 때문에 먼저 실패했다. 같은 명령을 로컬 테스트 서버가 허용된 환경에서 다시 실행해 두 패키지 모두 통과했다. 이때 이전 응답 모양/golden 실패도 재발하지 않았다. 저장소 전체 최종 검사의 결과는 여전히 별도다.

최종 통합 검사 결과:

- `MINISIGN=/tmp/arkism-flow-tools/minisign python3 tools/check_local.py` 통과. Python 도구·평가 검사, 작업 기록, 변경 함수 복잡도, Go vet·전체 race, UI **502/502** 검사를 포함한다. UI skip/fail은 0이다.
- 실제 일회용 PostgreSQL 18에서 `POSTGRES_TEST_DSN`을 지정한 FLOW HTTP·저장소 회귀와 `TestFlowPostgres` race 통과. 동시 동일 요청 4건의 실행 1개 생성, 실행/전달 원자성, lease, 페이지 분리, 프로세스 재시작 뒤 부분 영수증 회복을 확인했다. 이 fixture는 관리자 DB 연결과 합성 권한 판정을 사용하며 실제 운영 FORCE RLS 검증을 대신하지 않는다. 테스트 서버는 종료했다.
- `python3 tools/check_openapi.py` 통과: 285개 operation과 참조·스키마 유효. 이 명령 자체의 실제 HTTP case 입력은 0이며 HTTP 동작은 별도 Go 통합 테스트에서 검증했다.
- `go build`로 arkism, arkism-crew, memoryd 최종 코드 빌드 통과. 앞선 UI source/bundle 브라우저·키보드 및 Better Design 검사 이후 UI 변경 없음.
- 독립 검토에서 Discord 승인 대기 안내 누락과 격리된 실행의 재조회/lease 문제를 발견해 수정했다. 기존 MCP stdio의 지연 완료·동일 실행 재접속, Discord 대기 안내·부분 전송·실패·권한 철회 회귀를 확인했다.

첫 전체 검사에서는 minisign 미설치로 서명 테스트가 실패했다. 저장소의 공식 고정 다운로드/해시 검사로 임시 설치 후 통과했다. 이어 복잡도 초과는 baseline을 바꾸지 않고 책임별 함수 분할로 해소했다. Go vet의 테스트용 Service 잠금 복사 경고는 독립 Service 인스턴스 생성으로 수정했다. 최종 전체 검사는 모두 재실행해 통과했다.

최종 원시 로그는 작업 호스트의 `/tmp/arkism-flow-check-local.log`, `/tmp/arkism-flow-postgres-final.log`에 보관한다. 로컬 코드·fixture 수용 범위는 통과했다.

운영 수용은 아래 미실행 항목과 구분한다.

미실행: 실제 저장된 리서치 개선안의 운영 저장, 새 코드 배포, 실제 LLM 조사, Discord 외부 송신, Windows/Tauri 새 설치 검증, 다른 메신저 구현·운영 검증. 닫힌 에이전트 앱 대화로의 push와 네이티브 `@` 선택창은 지원하지 않는다. 단위·fixture 검사를 실제 연구 성능이나 운영 전달 성공의 증거로 사용하지 않는다.

관련 연구 protocol/run ID: 없음. 이 작업은 기능 구현과 회귀 검증이며 신규 연구 실험·사용자 연구·성능 우월성 주장을 만들지 않았다.

## 인수인계와 커밋 상태

전체 FLOW 관리 → 앱/Discord 호출 → 진행·승인 → 최종 결과 전달을 이번 구현 범위로 고정했다. 기존 연구 helper HTTP 입력 계약을 바꾸는 추가 개선은 포함하지 않았다. 신규 검색 서비스·엔진·메신저나 운영 변경을 추가하지 않았다.

커밋 상태: 이 기록을 구현 변경과 함께 로컬 커밋으로 보존한다. 원격 push·PR·운영 배포는 미실행이다. 비공개 기준 문서의 보충 기록은 기본 checkout의 ignored `.private/docs`에 따로 보존하며 공개 커밋에 강제로 넣지 않는다.
