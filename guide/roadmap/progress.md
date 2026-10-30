# 로드맵 진행 기록

기준: 2026-10-30. 작업 검증 완료25/63(R1~R8, CRW-01~07, DYN-01, RTE-01~02, CAP-01·07, PSY-01, SYS-01~04). 필수49개 중25개 완료·24개 미완료이며 선택14개는 보류다. #150 준비는 별도 완료이며 63개 작업 분모에는 포함하지 않는다. 제품·연구·전체 로드맵 완료가 아니다.
여기서 카드란 이슈 안의 개별 작업이다. “로컬 카드 수용”은 작업의 구현·실제 로컬 검사·독립 검토가 완료 기준을 충족했다는 과거 기록 용어이며, 앞으로 “작업 검증 완료”로 설명한다. `verified`도 이 뜻이다.
원격 검사·PR 병합·실험·제품 통합은 각각 따로 기록한다.

현재 후속 상태:

- SYS-02: 원A·실제 native B의 격리 기준 충족. 현재 API 거절·시간·identity·같은Tx 영속 인계와 실제PG/HTTP·업그레이드를 독립 검증했다.
- DYN-05: zero-aux 규칙 선택을 반영한 p1 runner/CLI/compare와 원격 검사를 수용했다. 아래 초기 실패 기록은 보존하며 전체 DYN-05/capture 완료로 합산하지 않는다.
- SYS-01: 실제 Search/HTTP shadow와 호출별 제외를 실제 Hook→BuildContext→동일 독자·영수증까지 연결해 원 기준을 충족했다. 최신 공개main f86 결합 일반/race·독립 검증을 통과했다.
- SYS-03: 원자 저장·중복/복구·실제 schema4→5 근거와 같은-reader packing·표시/저장/메모리 GET/답변 연결을 수용했다. 외부 정정1회의 전체 물리행 대사로 정상 purge와 검색 부작용을 구분했다.
- SYS-04: 원metadata 모델과 실제 복원·의존성 정정/삭제·native 게시 보류를 수용했다. 실제 LOGIN·특권 검산 경계·권한 철회 fallback을 검증했고 기존6필드와 일반 복원 보류는 유지한다.

아래 표는 초기 단계 당시의 검증 이력이다. 현재 집계와 후속 상태는 위 요약 및 뒤의 단계별 후속 기록을 따른다.

| 범위 | 상태 | 실제 검증·후속 PR |
|---|---|---|
| #150 준비 | 로컬·원격 검사 통과, 검토 가능 | [PR417](https://github.com/FelixBaek/arkism/pull/417). Python244/Go/UI532, 준비 회귀65. 원격 Checks/Coverage/Sonar/Go·JS·Rust Lint 성공 |
| R1 Go DTO·엄격 검증·해시 | 로컬 카드 수용 | 27자료형/150필드, race183leaf, 원본8vector. [PR418](https://github.com/FelixBaek/arkism/pull/418) |
| R2 Python I/O·manifest | 로컬 카드 수용 | 42검사, 독립 교차언어1077사례 차이0. 깊이·309자리 수·오차 경계 수정·재검증. PR418 |
| R3 합성 자료·정답 분리 | 로컬 카드 수용 | 24검사+독립20, 5seed/8파일/7query 실제 순위 대조. [PR419](https://github.com/FelixBaek/arkism/pull/419) |
| R4 정확 지수 감쇠 | 로컬 카드 수용 | 독립46leaf, 함수coverage100%, 오류변형3종 감지. PR418 |
| R5 RRF·합성 예산 선택 | 로컬 카드 수용 | 70leaf/3API coverage100%, 원본16golden·제품 산식의 제한된300대조. PR419 |
| R6 예정 분모·실패 보존 지표 | 로컬 카드 수용 | 39methods/64leaf+독립30. stream macro0.625/query weighted0.5, null·실패·음성 분모 유지. PR419 |
| R7 파일 전용 CLI | 로컬 카드 수용 | 17주검사/16하위+독립13, race·Win/Darwin vet·고정lint 통과. Go test에서 run() 직접 검증. PR419 |
| R8 CSV·인수·인계 | 로컬 카드 수용 | 기존 구현·독립 검토 뒤 사용자 검산 CLI 전용 허용으로 실제 standalone14check 통과. Go328leaf/Python128methods·7query/CSV·30artifact hash 독립 대조. 제품/연구 실험 준비와 구분. 실제 인수 기록 [PR428](https://github.com/FelixBaek/arkism/pull/428) |
| CRW-01 현재 엔진·연구 계약 | 로컬 계약 수용 | [PR421](https://github.com/FelixBaek/arkism/pull/421). R0 뒤 독립 착수 가능. private 계약3파일, 정적29/독립 변조12·효과 양성7/음성25, 근거53파일·55행 확인. 제품 실행 검사는 아님 |
| CRW-02 호출·사용량 관측 | 로컬 카드 수용 | 기존 helper 검증 뒤 실제 호출·회의·host 연결. 독립 core11/host19 일반·race, 회의20/하위10 race 및 최종 Python412/Go/UI532 통과. 실제 provider·전체 청구 증명은 미수용 |
| CRW-03 공유 문맥의 현재성 | 작업 검증 완료 | 실제 Client·회의 연결과 독립 검토, 전체 회귀·고정 lint 통과. [PR425](https://github.com/FelixBaek/arkism/pull/425)·[PR426](https://github.com/FelixBaek/arkism/pull/426). 자동 refresh·read/use 원자성은 미수용 |
| CRW-04 중단·복구·효과 경계 | 작업 검증 완료 | 실제19개 자식 프로세스 중단·복원 및 독립 판정·root 재실행 대조, 전체 회귀 통과. [PR427](https://github.com/FelixBaek/arkism/pull/427). 실제 provider 원자성은 미검증 |
| CRW-05 실행 비교·판정 | 작업 검증 완료 | 수정 전 실행을 보존하고 새 고정 버전으로1620행 전체 재실행·독립 검산 통과. 작업 완료1080/의도한 안전 중단540, 평가 오류0. 실제 모델·가격·효능 비교는 미실행 |
| CRW-06 실패 분석·책임 추정 분리 | 작업 검증 완료 | 작성자24방법/71세부·독립34방법/74세부 및 전체 회귀 통과. 실제 자료 연결·주석 일치도·미확인/실패 분모·격리 후보를 검증했다. 실제 개입·사람 수집·장기 기억 수정은 미실행 |
| CRW-05.spec 공정 비교 자료 | 로컬 명세 하위 단계 수용 | 소유46+독립16검사,162개 literal oracle·공정 입력 대조·20파일 hash 확인. 후속 실행 adapter·전체 CRW-05는 위 행에서 별도 검증 완료. 연구용 holdout은 미실행 |
| RTE-01 라우팅 사건 계약 | 로컬 계약 수용 | 최초36+139검사 뒤 finite309 후속38+152methods/157leaf 검증.11모델/94필드 출처·11산출물 hash 확인. 런타임·실제 무작위성·성능은 미검증. DYN-01/SYS-02는 unknown 유지 |
| RTE-02 시도별 비용 대사 | 로컬 연구 대사기 수용 | 소유63+독립28methods/41leaf·6파일 hash 확인. unknown·실패 비용·비가산 미귀속 근거 보존. 제품 연결·실제 장부 실행은 미수용 |
| RTE-03 선택 전 기억 메타데이터 | 격리 구현 중 | source/API 순서대조와순수featurebuilder 구현. 현재제품의모델선택순서는변경하지않으며reader/fence미수용근거는unknown 유지 |
| CAP-01 현재 기능·상태 목록 | 로컬 정적 목록 수용 | 24상태·3산출물, 소유19+독립9검사,29코드/테스트·main4차이 대조. 실제 설치·인증·실행 상태는 미관측 |
| DYN-01 사건·시계·상태 수명 계약 | 작업 검증 완료 | 별도 scalar 계약·Go28자료형/20보조 구조·23닫힌 해시 projection, 중요도 sidecar 정의. 소유20방법168세부·독립16방법163세부 일반/race 통과. 실제 사건 전이·재생·정책·CLI는 다음 카드 |
| CRW-07 채택·신규성·연구 준비 판정 | 작업 검증 완료 | 비공개4보고서, 작성자315·독립262 문서/해시/수치 검사 통과. 원1620행 분모·비용·실패 보존 확인. 개발 기반 유지, A 통합 실증 미확인·E 논문 not_ready |
| DYN-02 단일 상태 전이 | scalar 검증 완료·전체 카드 부분 수용 | 작성자32방법62세부·독립31방법65세부 일반/race 및 전체 회귀 통과. 중복·revision·이력 결합과 입력 불변을 검증했다. fast/slow는 조건부 미실행이며 전체21/63은 유지 |
| SYS-01 동일 후보 비교 계약 | A 합성 검증 완료·전체 카드 진행 중 | 작성자76방법116세부와 독립 검사 통과. 고정 후보·시각·전체 결과·쓰기/호출·손실 단계를 비교한다. 실제 제품 연결 B는 미실행이며21/63을 유지한다 |
| DYN-03 순서 재생·읽기 조회 | scalar 검증 완료·전체 카드 부분 수용 | 작성자25방법78세부·독립25방법39세부 일반/race 통과. nil배열·alias 오류우선순위 실제 실패 수정. fast/slow·운영 복원은 미지원이며21/63 유지 |
| DYN-04 tag/resource 선택 확장 | 보류·현재 미선택 | scalar 대비 필요성·fast/slow/B3 파일럿 근거가 없어 구현하지 않는다. 원 선행과 비교 계획을 수용한 뒤 재검토하며 더미 성공 모듈은 만들지 않는다 |
| DYN-05 일곱 정책·runner·CLI·compare | 구현 중·규칙 충돌1검사 미통과 | pure5파일과독립17메서드는검증했으나전체167메서드일반/race에는등록된극소가중치의B0동등성1FAIL이남아있다. 원자료보존·규칙선택대기,임의기준축소없이원runner/CLI/compare까지완료해야수용 |
| EXP-01 자료·분할·잠금 | 합성 draft 하위 단계 수용 | 보안 후속 작성자36메서드/63leaf·독립33/78 통과. 기본 수집EXP36개 정확1회, 최종Python524·Go·UI532 통과. 원격6검사·Codecov·Sonar 통과. 실제 정책 실행·골드 의미 검토·최종 잠금은 남아 전체21/63 유지 |
| EXP-02 비교군·통제·실행 명세 | spec 하위 단계 검증 | 작성자43메서드79leaf·독립96계약검사·수집323(새43개정확1회) 통과. 최종Python567·Go·UI532 통과. 실제adapter/실행계획게시/index최종수용은 남아 전체카드진행중이며21/63 유지 |
| CAP-02 독립 관측 schema·개발 fixture | 공학 구현 검증·전체 카드 대기 | 12서로다른과제×4조건=48입력과분리checker. 작성자26·독립127사례·표준schema97문서·99파일재생성 통과. 사람oracle/옛버전/stop검토는pending, 실제native근거 아님 |
| CAP-07 MCP 통신 호환 fixture | 작업 검증 완료 | 별도 가짜 서버와 현재 실제 inventory경로11조건/race, 작성자·root재실행Python9 및 root독립5검사 통과. 67개 원소스·CAP01 3artifact 불변. 현행 검증 누락4건을 재현했으며 제품 수정·현대프로토콜 지원·실제서버 검증은 별도 |
| PSY-01 기억 정정 연구 측정·윤리 준비 | 작업 검증 완료 | 별도9산출물, 작성자19메서드62leaf·독립11/50 통과. 원자료15·산출물9핀 불변. 기본3측정·가설·선행비교·권한/윤리 준비만 수용하며 실제신규성·척도타당성·기관승인·사람연구완료와 구분 |
| PSY-02 비교 표시·배정·재생 | 합성 구현 검증·전체 카드 대기 | 별도11파일, 작성자43메서드157leaf·독립20/85 통과. 유효 ID 변경의 참조 단절을 수정·재검증. 원nativeY는null/expected_only, B0source-only·연구자/기관후속은 남아21/63 유지 |
| PSY-03 모의 분석·준비 | 구현 중 | PSY01뒤 허용된 병렬 모의 분석. 실제 연구·통계 선택·표본 확정·모집은 하지 않음 |
| #152~158의 나머지 | 선행에 따라 병렬 진행 | EXP02 비교 명세는 별도 소스 검증 중. 전체63카드는 필수49/선택14이며 선택·하위 단계 조건을 유지 |

단계 전체 검사: R1/R2/R4는 Python286, R3/R5/R6/R7은 Python349,
각각 Go 복잡도·vet·전체 race와 UI532가 통과했다. R8 최종 수정본도 Python412/Go/UI532가 통과했다. 이전403/409검사 뒤 추가 발견한 저장·리소스 결함은 수정·재검증했다. PostgreSQL 통합은
로컬에서 미활성으로, 이 결과를 DB 수용으로 표시하지 않는다.

PR417~421의 최신 검증 head는 각각 Checks/Coverage/Sonar/Go·JS·RustLint
6개 워크플로를 통과했다. 후속 보안 지적도 규칙 변경 없이 재분석을 통과했다.
PR420의 변경하지 않은 Windows Rust 검사에서 최초 timeout이 발생했고, 동일 소스 재검사에서 통과했다. 원격 Checks의 Go+PostgreSQL 통합도 통과했으며 로컬 DB 미실행과 구분한다.
PR421의 기존 UI 3검사도 최초 실패 후 같은 소스 재검사에서 통과했다. 기대값과 제품 UI는 변경하지 않았다.
PR417~421은 병합하지 않았다. 고정 계약1개는 사용자가 명시적으로
공개 예외를 승인했으며 바이트와 SHA256은 유지했다.

실제 제품/설치 빌드·릴리스·배포·유료 호출·참여자 수집은 실행하지 않았다.
사용자의 검산 CLI 전용 빌드·인수 검사 예외 허용 뒤 마지막 standalone 경로도 실제 통과했다. 제품 빌드 금지는 유지한다.
0개 검사·필수 skip·누락 hash를 완료로 처리하지 않는다.

변경 이유·검증·한계의 상세 기록:

- [준비](../work-records/2026-10-29-roadmap-preparation.md)
- [R1/R2/R4](../work-records/2026-10-29-roadmap-baseline-contract.md)
- [R3/R5/R6/R7](../work-records/2026-10-29-roadmap-baseline-pipeline.md)

진행에 맞춰 이 기록과 소유권 manifest의 상태를 함께 갱신한다.
상태 문자열 자체는 실행 증거나 승인 권한이 아니다.

- [R8 구현 당시 검증 기록(standalone 미실행)](../work-records/2026-10-29-roadmap-baseline-reporting.md)

- [CRW-01 계약·근거와 검증 범위](../work-records/2026-10-29-roadmap-crew-contract.md)

CRW-02는 소유 helper의 독립 검토·인계 관문 `CRW-02.modules`를 추가해
전체 카드 수용과 공유 호출부 연결을 분리했다. 이전 관측 단계는 63카드/95관문이며,
INTEGRATION 담당자가 인계 후 등록된 공유 파일을 연결한다.
병렬 작업은 실제 파일별 단독 작성자를 먼저 배정하며 서로 겹치지 않는다.
고정 CRW-01 비공개 계약 원문은 그대로 유지하고 후속 배정 기록으로 범위를 잇는다.

- [CRW-05.spec 모의 비교 자료·평가기와 한계](../work-records/2026-10-29-roadmap-crew-evaluation-spec.md)

독립 회의와 legacy 단독 실행의 실제 기록은 각각 다른 collection에 있다.
기존 crew v1 계약을 넓히지 않고 별도 execution-reference v2 관문을 등록했다.
새 비공개 계약·검증 자료는 R0 단일 작성자가 맡고, 전체 CRW-02 수용과
회의·host 통합은 이 관문도 충족해야 한다. 기존 CRW-01과 CRW-05.spec의
v1 수용 증거는 유지한다. 확인된 자기 root는 collection/id를 명시하며
null은 미확인으로만 사용한다. 효과 승인 CAS는 계속 crew_runs로 제한한다.

CRW-02 helper의 최종 고정 소스는 Python412/Go 복잡도·vet·전체race/
UI532 및 고정 lint0issues를 통과했다. 현재 별도 후속 브랜치에서 실제 호출부를 연결 중이며,
전체 CRW-02 수용 전 독립 검토와 회귀 검증이 남아 있다.

- [CRW-02 helper·회계 불변식과 미완료 통합](../work-records/2026-10-29-roadmap-crew-observer.md)
- [RTE-01 라우팅 사건 계약·필드 출처·검증 범위](../work-records/2026-10-29-roadmap-routing-contract.md)

후속 단계 검증 기록:

- [실행 참조 v2 계약](../work-records/2026-10-29-roadmap-crew-execution-refs-v2.md)
- [Go observer v2 helper](../work-records/2026-10-29-roadmap-crew-observer-v2.md)
- [라우팅 숫자 파서 후속 보정](../work-records/2026-10-29-roadmap-routing-numeric-followup.md)
- [연구용 비용 대사기](../work-records/2026-10-29-roadmap-routing-cost-reconciliation.md)
- [기존 기능·권한 상태 목록](../work-records/2026-10-29-roadmap-capability-inventory.md)

PR422의 고정 head8c14279d는 원격6워크플로를 통과해 검토 가능 상태다.
후속 변경은 별도 v2 브랜치에서 진행하며 PR422의 9/63 스냅샷을 재작성하지 않는다.

PR423의 고정 v2 helper head도 원격 6워크플로를 통과했다.
실제 호출부 연결은 별도 브랜치에서 진행 중이다. 호출 실패·재시도·대체 모델·
회의와 host journal을 검사하며, 이 단계의 최종 검증·독립 검토 뒤 전체 카드 수용 수는 12/63이다.

CRW-02 실제 연결 단계는 최종 21개 Go 소스 핀에서 전체 check_local·고정 lint를 통과했다.
보고서는 동봉한 snapshot과 로컬 경계에 한정하며 run wall은 자동 수집하지 않아
caller-supplied/unknown으로 남는다. 실제 모델·청구·빌드·배포를 검증한 것은 아니다.

- [CRW-02 실제 호출 연결·회귀·검증 한계](../work-records/2026-10-29-roadmap-crew-callers.md)

CRW-03을 현재 코드 기준으로 착수했다. 원형 자료형·판단 API·fixture를 독립 검토하는
CRW-03.modules 관문을 먼저 두고, 이후 기존 meeting/store 경로를 연결한다.
현재 63카드/96관문이며 로컬 완료12/63은 유지한다.

CRW-03.modules는 자료형·판단 보조 모듈과 비공개 합성 fixture 범위에서 수용했다.
각14필드, 독립 일반/race169leaf, fixture32결정·4계보 및 최종
Python412/Go/UI532·고정lint0을 확인했다. 실제 Client/회의 연결은 다음 단계이며
전체 CRW-03과 전체 완료12/63은 바꾸지 않는다.

- [CRW-03 보조 모듈·독립 검증·실행 한계](../work-records/2026-10-29-roadmap-crew-context-module.md)

PR424의 고정 a48d3ff4는 원격6워크플로를 통과해 검토 가능 상태다. 병합하지 않았다.

CRW-03 실제 Client·회의 연결도 로컬 카드 수용했다. 최종 Python412/Go 복잡도·
vet·전체race/UI532, 고정lint0issues 및 고정 소스 독립 검토를 통과했다.
Client 소유44leaf·독립49leaf, 회의 소유14leaf·독립22leaf를 확인했다.
정확한 ref·현재 팀 정책을 다시 확인하며 삭제·철회 뒤 후속 입력/대체 호출/후보 저장을
차단한다. 원본·실제 Task·저장 요약 revision을 구분한다. 기본 off와 Discord 범위는 유지한다.
로컬 완료는13/63이며, read/use 원자성·실제 provider 회수·자동 의미정정/refresh는 수용하지 않았다.
PR425 보조 모듈은 원격6워크플로를 통과해 검토 가능 상태이며 병합하지 않았다.

- [CRW-03 실제 연결·회귀·검증 한계](../work-records/2026-10-29-roadmap-crew-context-integration.md)

CRW-04는 기존 엔진을 변경하지 않고 원래5기준을 로컬 fault fixture 범위에서 수용했다.
실제19개 자식 프로세스 중단·복원과 독립 판정, 별도 root 재실행 결과가 바이트까지 같다.
원본 world·체크포인트 실파일과 출력 상태도 대조했다. 모듈 소유일반/race119leaf·독립race80leaf,
native24methods/29raceleaf, adapter독립일반/race9leaf, 판정기21methods/46leaf 및
최종Python412/Go복잡도·vet·전체race/UI532·고정lint0issues가 통과했다.
18개는 기대한 안전 경계를 유지했고 target-only 정책 경쟁1개는 의도한 실패 반례로
별도 기록했다. 이 수치는 모델 성능이나 업무 성공률이 아니다. 최초 규약 교정 전 소스는
SHA·실패로그만 남아 그 최초 판정의 완전 재실행이 불가하며, 최종11소스는 바이트를 보관했다.
위 CRW-04 수용 시점은 로컬14/63이고 R8 standalone은 미실행이었다. 아래 R8 후속 검증으로15/63에 갱신했다. 실제 provider 원자성·인증·운영DB·제품 빌드·배포는 미검증이다.
PR426도 원격6워크플로 통과·검토 가능 상태이며 병합하지 않았다.

- [CRW-04 복구·효과 경계·실제 중단 시험](../work-records/2026-10-29-roadmap-crew-recovery.md)

사용자가 검산 CLI `./cmd/memory-harness-baseline`의 빌드·인수 검사만 별도로 허용했다.
기존 고정07ed 소스에서 실제 기본 인수 경로14check가 통과했고,
Go328leaf/Python128methods·음성CLI4검사(exit2)·7query/순위·선택·지표·CSV를
독립 산술로 대조했다. 실제 ELF·bundle·30산출물의 hash와 source clean을 확인했다.
산출물의 requires_independent_review 표시는 수정하지 않고 별도 상위 수용 기록을 남겼다.
R8까지 로컬15/63이며, 강한 선행 대조·새 holdout·실제 서비스/DB 동등성·연구 본실험 준비는
수용하지 않았다. 제품/설치 빌드·릴리스·배포·Claude 수정은 실행하지 않았다.
CRW-05 실제 fake 비교 실행기·독립 평가기 구현은 계속 진행 중이다.
PR427의6d571859는 원격6워크플로를 통과해 검토 가능 상태이며 병합하지 않았다.

- [R8 실제 검산 CLI·인수 경로와 독립 수용](../work-records/2026-10-29-roadmap-baseline-runtime-acceptance.md)


CRW-05의 전체 모의 실행1620행과 최초 판정 실행이 종료했다. 독립 검토에서
실제 산출물70,848개와 실제 계획 호출6,732개의 입력·비용 근거를 대조했고,
파일·상태·중단 결과는 정답1620행과 일치했다. 최초 판정은1512행 정상,
108행 평가 오류다. 이108행은 E-C4가 작업 전 안전하게 중단해 실행 관문이
없는 경우를 판정기가 잘못 거부한 것이다. 평가기 수정과 독립 재검증은 아직 진행 중이다.
원 실행·소스·판정 결과는 보존하며, CRW-05를 완료로 올리지 않는다. 로컬15/63을 유지한다.
PR428도 고정0da883d1에서 원격6워크플로를 통과해 검토 가능 상태이며 병합하지 않았다.

- [CRW-05 전체 실행·평가 오류·남은 검증](../work-records/2026-10-29-roadmap-crew-runtime-evaluation.md)


CRW-05 최종 고정 버전은 전체1620행을 새로 실행해 원래5완료 기준의 독립 검토를
통과했다. 작업 완료1080건·의도한 안전 중단540건이며 평가 오류0건이다.
산출물70,848개·실제 계획 호출6,732개·효과360건·소스72파일을 대조했다.
합성 비용6.732는 실제 청구가 아니며 transport 집계와 이중 합산하지 않았다.
초기108평가 오류와 원 실행은 보존하고 좁은 교정의 독립121leaf 검사를 남겼다.
공개 작업트리의 최종 Python412/Go 복잡도·vet·전체race/UI532 검사도 통과했다.
이 단계로 작업 검증 완료16/63이며, 모델 성능·동일USD 비교·연구 holdout·운영 권한·
OS sandbox·제품 빌드/배포는 수용하지 않았다. CRW-06은 분석 설계를 준비 중이다.

PR442의 고정dd430ffe는 원격6워크플로를 모두 통과해 검토 가능 상태다. 병합하지 않았다. 다음 변경은 별도 CRW-06 분석 브랜치에서 진행한다.


CRW-06은 원래5완료 기준을 분석 기반 범위에서 검증 완료했다. 작성자24방법/71세부,
독립34방법/74세부 검사에 실패·오류·skip0이다. 실제 r02 자료의 엄격한 입력·결과·평가
연결, native fault의 블라인드 관측, 역할·단계·scope, 원투표와 일치도, 반사실 실패·미확인
분모 및 파일 읽기 경계를 대조했다. 수정 전 바이트와 실제 실패를9단계로 보관했다.
누락된 주석과 미확인 응답은 구분하며, 선언된 반사실 성공은 검증된 인과 결과가 아니다.
실제 보정 확률은 미지원으로 거부하고 주관적 신뢰도·사람 선언·미확인을 분리한다.
기존 OI25 보호와 원r01/r02는 변경하지 않았다. 전체 작업 검증 완료17/63이다.

- [CRW-06 실패 분석·독립 검증·실행 한계](../work-records/2026-10-29-roadmap-crew-failure-analysis.md)


DYN-01은 합성 accepted-time·revision·상태 수명·P1 출처/가용시점 계약과
자료형·엄격 검증기를 검증 완료했다. 원 v1 bytes/등록기는 바꾸지 않았으며
새28자료형·20보조 구조·23해시 projection과기존 budget alias의72고정객체만 지원한다.
작성자20방법/168세부·독립16방법/163세부 일반/race, vet·고정lint0issues와
새 작업 공간 전체 Python412/Go복잡도·vet·전체race/UI532를 통과했다.
원문 정규식의0수집을 통과로 처리하지 않고 기존20함수명만 정합시켜 정확 명령도 통과했다.
작업 검증 완료18/63이며, ApplyEvent/Replay/ReadAt/정책/CLI 본체 실행이나
실제 모델·효능·운영 권한·제품 빌드/배포를 수용한 것은 아니다.

- [DYN-01 계약·Go 검증기·독립 검사와 한계](../work-records/2026-10-29-roadmap-dynamics-contract.md)

CRW-07은 실행·평가 저자와 분리한 보고 담당자 및 독립 검토자가 원5완료 기준을 확인했다.
구현/계획/관측/신규성을 구분하고 40개 주장·89개 출처·7개 직접 선행의 대조를 기록했다.
원1620행은 완료1080/의도한 안전 중단540이며 기대일치1620을 업무 성공률로 바꾸지 않았다.
합성 비용6.732의 leaf/aggregate는 같은 관측을 중복 합산하지 않으며 실제 청구는 미확인이다.
4명/5호출의 문구 오류를 수정하고 이전 보고서와 검산 준비 오류도 보존했다.
작성자315·독립262개는 문서/해시/수치 검사이며 새 런타임·실모델 실험 수가 아니다.
개발 기반은 검증 범위에서 유지하되 A 통합 실증은 미확인, 독립 E 논문은 not_ready로 남긴다.
비공개 원보고서·전문은 공개하지 않았으며 제품빌드/배포·Claude 수정은 없다.
현재 작업 검증 완료19/63이다.

- [CRW-07 채택 보고·검산과 연구 준비 한계](../work-records/2026-10-29-roadmap-crew-adoption.md)

DYN-01의 후속 전이 구현 준비 중 늦게 도착한 원사건 재시도가
단독 DTO의 arrival 상한 검사에서 먼저 거절되는 문제를 발견했다.
원 검증본과 실제 RED를 보존하고, transport arrival와 원 accepted 좌표를 분리했다.
새 사건의 arrival 상한은 EventFile 첫 ID와 후속 ApplyEvent의 신규 사건 경계에서 유지한다.
이 변경은 기존 DYN-01 카드의 좁은 후속 수정이며 완료 수19/63을 늘리지 않는다.

- [DYN-01 transport arrival 후속 수정](../work-records/2026-10-29-roadmap-transport-arrival.md)

DYN-02.scalar는 원v4/v8완료 기준을 별도 독립 검토와 원로그로 확인했다.
기존 R4감쇠와 DYN-01검증기/해시를 재사용하고 상태·영수증·cursor를 순수값으로 함께 반환한다.
작성자32방법/62세부·독립31방법/65세부 일반/race 및 전체Python412/Go/UI532가 통과했다.
I01~I05의 실제 실패와 수정 전 소스, 별도fixture·집계·복잡도·문서해시 준비 오류도 보존했다.
전체DYN-02는부분수용이며 fast/slow·실제모델·전체수치재생·권한/DB원자성은미검증이다.
이것은필수scalar하위단계수용으로 전체완료수19/63을늘리지않는다. DYN-03.scalar 선행은충족했다.

- [DYN-02.scalar 구현·독립 검증·한계](../work-records/2026-10-29-roadmap-scalar-transition.md)

SYS-01.spec A의 합성 계약과46가상 사례를 독립 검증했다.
시각·최종 결과 결합·Scope 경계의 실제 실패를 수정하고 이전 소스/로그를 보존했다.
잘못된 독립 검사 준비와 보고는 실제 소스 결함과 구분해 후속 정정했다.
R8 기본 수집기에 명시적으로 연결하여266검사를 실행했다. 수집기 독립 검토와 전체 Python488·Go복잡도/vet/race·UI532를 통과했다.
실제 제품 연결 B는 미구현이며 전체 완료19/63은 변하지 않는다.

- [SYS-01 합성 비교 계약·검증·남은 제품 연결](../work-records/2026-10-29-roadmap-shadow-contract.md)

DYN-03.scalar의 순수 재생·조회 구현은 원 전이/감쇠를 재사용하고 원자료형/계약을 유지한다.
작성자25방법78세부·독립25방법39세부 일반/race, 고정 literal4종과100회 조회 불변을 확인했다.
독립 I01 nil배열·I02 비활성화 사건 alias 오류우선순위의 실제 실패/원소스를 보존하고 수정했다.
최종 전체 Python488·Go복잡도/vet/race·UI532를 통과해 scalar 하위 단계만 수용한다. DYN-05.p1의 다른 원 선행(EXP-01.draft/EXP-02.spec)은 별도로 충족해야 한다.
DYN-04는 원 선택 조건의 근거가 없어 보류, fast/slow·실제 모델·운영 복원은 미검증이다.
전체 작업 검증 완료19/63은 유지한다.

- [DYN-03.scalar 재생·조회·독립 검증과 선택 확장 보류](../work-records/2026-10-29-roadmap-scalar-replay-probe.md)

EXP-01.draft는 기존 파싱·안전 쓰기를 재사용해 템플릿·원문·시각으로 합성 자료를 만들고 검증한다.
평가자 전용 gold/분할 정보와 실제 정책 입력을 분리하며, 미정 연구값은 null이고 최종 잠금은 거절한다.
작성자35메서드/58leaf·독립28메서드/57leaf, 수집기279메서드/574leaf와 실패 차단8종을 통과했다.
전체 evaluation301은 수집기279와 별도22의 합이며 최종 전체42812 종료0: Python523·Go복잡도/vet/race·UI532.
공개 설명2곳의 차단 규칙 충돌은 의미동일 재서술과 계약 해시 상수 갱신으로 해결했다. 원실패·검산 준비 오류는 별도로 보존했다.
합성 draft 하위 단계만 수용한다. 실제 label-free native 실행·골드 의미의 독립 검토·최종 연구 잠금은 남아 전체19/63을 유지한다.

- [EXP-01 draft 자료 생성·누출 방지·잠금 작업 기록](../work-records/2026-10-29-roadmap-experiment-draft.md)

보안 후속: 원격 검사에서 private 예약 함수의 경로 흐름2건을 지적하여 basename 경계를 함수 안에서도 검사한다. 공개 caller는 기존에도 단일 경로명을 검증하므로 공개 취약성이 입증됐다는 뜻은 아니다. 실제 잘못된 이름5종의 수정 전 실패를 보존한 뒤 작성자36/63leaf·독립33/78leaf·수집280검사 및 전체94043(Python524·Go·UI532)을 통과했다. 원격 재판정은 별도로 확인한다.

## 병렬 연구 준비 후속

CAP-07은 실제 현재 stdio inventory를 격리된 exact-source shadow와 합성 subprocess로 검증했다. legacy/modern/dual-era/timeout-error를 구분하고 빈 성공·실패·미확인을 분리했다. 초기화 오류/잘못된 결과/형식 오류/부분목록의 현행 검증 누락4건은 관측 기록이며 수정 완료가 아니다. 실제 연결 필요가 확인된 경우 별도 좁은 통합 설계를 따른다.

PSY-01은 같은 정보·backend 상태의 표현과 실제 입력 포함 예측을 분리하는 측정·윤리·문헌 준비9산출물이다. Brier/행동/시간을 기본으로 하고 자기보고는 미선택 제안으로 뒀다. 누락 부록·척도 문항을 복원하지 않았고, 문헌 접근과 독립신규성/권한/타당성/윤리승인을 구분했다. PSY02의 실제 oracle/배정/비교 화면과 기관·연구자 결정은 후속이다.

- [MCP 호환 fixture·현행 관측·검증 기록](../work-records/2026-10-30-roadmap-capability-protocol-fixtures.md)
- [기억 정정 연구 측정·윤리 준비 검증 기록](../work-records/2026-10-30-roadmap-correction-study-preparation.md)

PR482 후속55e5db8a는 원격6워크플로·실제check-runs·Codecov/patch·Sonar gate를 통과해 검토 가능 상태다. 보안 취약점0이며 유지보수 지적66건은 별도 남아 있다.

EXP-02.spec은 필수7정책과 비교통제를 검증하는 실제 parser/CLI다. 명세 성공은 미완료gate8개·plan:null·processes0을 명시한다. 외부owner/native근거 없이실행계획을 성공으로 만들지 않는다. 작성자43메서드79leaf·독립96계약검사, 기본수집323 및8음성/1양성제어, 최종91121 종료0(Python567·Go복잡도/vet/race·UI532)을 통과했다. 원검증은55e5db8a에서 수행했고 코드가바뀌지않는 연구준비기록57a246f0만ff통합한 뒤 source pin과소유권을재확인했다.

- [일곱 정책 비교 명세·실행 차단 검증 기록](../work-records/2026-10-30-roadmap-policy-comparison-spec.md)

## CAP02·PSY02 공학 구현 검증 후속

CAP02는48개입력과체커의기술구현을검증했으며원카드필수사람oracle/허용옛버전/stop조건수용은아직pending이다. load단계의부분관측은뒤의독립실행관측과분리하며unknown이성공/권한으로바뀌지않는다. 실제다른task/outcome귀속변조는거부한다. outcomeID namespace와순번라벨shortcut은한계로기록하고이자료로실제효능을검증했다고주장하지않는다.

PSY02는등록된실제caseID로양세션계획과재생을연결하도록고쳤다. 원일반ID도메인을줄이지않았고원실패와새양성회귀를보존했다. B1/B2정보·행동일치,기본3상태와진단3상태분리,표현48개/가상배정/재생·코드·실제브라우저검사범위를확인했다. 실제nativeY·B0화면·심리혼입심사·연구자/기관절차와사람자료수집은남아있다.

- [호환성 관측 schema·48개 개발 사례 검증](../work-records/2026-10-30-roadmap-capability-evidence-fixtures.md)
- [합성 비교 화면·배정·ID 연결 수정 검증](../work-records/2026-10-30-roadmap-correction-stimulus-harness.md)

이번공개기록의parent f5aebd07과문서상태는전체53294 종료0(Python567·Go복잡도/vet/race·UI532)이다. 다른작업트리의미발행DYN05소스는이검사에포함하지않았으며,그별도전체167메서드의규칙충돌1FAIL을통과로바꾸지않는다.


## RTE03·PSY03 격리 구현 독립 후속

RTE03은 결정 시점과 receipt 가용 시각, 정확한 ref/revision/content hash, history 누락과 알려진 빈값, 후선택 packing 대사를 검증한 격리 메타데이터 구현이다. FIFO 입력이 파일 검사 전에 멈추는 실제 결함을 고쳐 작성52/89세부·독립37/99를 통과했다. 실제 호스트/API·native reader/fence 연결은 남아 있어 전체 카드 완료는 아니다.

PSY03은 세션2의 상태→참여자→집단 Brier, 결측/탈락 및 군·상태별 행동·시간 분석의 합성 구현이다. 군별 행동·완료시간을 바꿔도 같은 출력이 나오는 실제 결함2건을 수정해 작성45/107세부·독립30/63를 통과했다. 연구 선택과 실제 정답/사람 자료는 미확정이며 pilot 준비 완료로 표시하지 않는다.

- [선택 전 메타데이터·FIFO 수정 검증](../work-records/2026-10-30-roadmap-routing-feature-preparation.md)
- [Brier·군별 행동/시간 모의분석 검증](../work-records/2026-10-30-roadmap-correction-simulation.md)

두 하위 구현만 수용했으며 전체21/63은 유지한다. 원 실패와 수정 전 소스는 별도로 보존했다. 공개 기록은 전체55678 종료0(Python567·Go복잡도/vet/race·UI532)과 공개 검사를 통과했다. 최초 샌드박스 소켓 오류는 원본 보존 후 동일 검사를 재실행했다. 별도 DYN05 작업트리의 수치 규범 충돌1FAIL은 미해결로 유지한다. SYS02는 기존 수락 API와 변환 계약을 대조해 다음 격리 구현을 준비 중이다. 빌드·배포·Claude 변경 없음.


## SYS02 수락·변환 계약과 기본 회귀 연결

SYS02 A는 현행 독립 평가·현재 참조·checker·trace 경계에 맞춘 구조 계약과63개 합성 입력이다. 실제 native 메타데이터 계획과 별도 synthetic DYN 사건을 구분하며, 원 서버 시각과 전송/근거 identity를 혼합하지 않는다. 가용 순서가 미래인 근거는 사건 자체를 버리지 않고 학습 자격만 보류하도록 수정했다. 원 실패·수정 전 소스와 기존35검사·63사례 기대값은 보존했다.

작성38메서드157세부·독립65사례/16메서드40세부·기본 수집361메서드815세부를 통과했다. 전체36655 종료0(Python605·Go복잡도/vet/race·UI532). 별도 수집기 실패 차단8종/정상1종도 통과했다. 실제 acceptedRef/Go 어댑터·native 자격/사건·SYS03 영속 인계는 후속이며 전체21/63을 유지한다.

- [독립 결과 변환 계약·미학습 보존 검증](../work-records/2026-10-30-roadmap-outcome-conversion-contract.md)

앞선 기록은 공개 메타데이터를 정리한 대체 PR487로 이어진다. 새47deb175의 파일 트리는 기존2fe18ddf와 정확히 같고, 기존6워크플로/15체크 성공은 같은 트리의 증거로 보존했다. 새 PR의 원격 결과는 별도로 추적하며 기존 PR486은 대체로 닫았다. 공유 이력은 보존했다.

## SYS02 실제 수락 입력 확인·준비 단계

실제 Service에서 저장된 평가·원 요청·현재 권한·정확한 참조와 독립 평가자를 다시 확인하는 Go resolver를 구현했다. generic 생성 위장·변경된 근거·권한/역할 변경을 거절하고 원 서버 시각과 명시적 false를 보존한다. 출력은 준비된 미커밋 intent이며 실제 학습 사건이나 인과 기여가 아니다.

공개 소스 독립37메서드173세부 일반/race·1311보호핀·16증거와 전체 Python605·Go복잡도/vet/race·UI532의 성공을 확인했다. 원 복잡도 실패와 수정 전 소스는 보존했다. 실제 native 사건/명시적 등록·설정·같은 트랜잭션 영속화·HTTP/격리PG는 남아 전체21/63을 유지한다. 제품 빌드·배포·Claude 변경 없음.

- [실제 수락 입력 확인·미커밋 intent 검증](../work-records/2026-10-30-roadmap-native-outcome-resolver.md)

## SYS03 원자성 모델·외부 원장과 관측 주기

수락·영속 영수증·상태/근거 소비의 원자적 확정·최신 게시를 분리한 합성 모델과 외부 원장 검산기를 구현했다. 같은 사건·근거 중복, 부분 확정, 응답 유실, 동시 writer·옛 게시와 삭제/권한 무효화를 검증한다. 과거의 reader 사실은 보존하되 이번 관측 주기로 상속하지 않으며, 거절된 관측은 다음 정상 관측의 주기를 예약하지 않는다. 원 실패·후보·24사례 기대값은 보존했다.

작성68메서드108세부·독립32메서드85세부, 기본수집429메서드923세부(기존361 유지·신규68정확1회), 신규 경로 누락/빈 모음/필수skip의3음성·정상1 제어를 통과했다. 변경 없는 기존 수집기 로직의 앞선8종 검증은 원문/증거 동일성을 확인해 재사용했다. 전체44392 종료0(Python673·Go복잡도/vet/race·UI532). 최초 임시 서명 도구 누락8준비오류는 원로그 보존 후 기존 설치 도구로 동일 소스를 재검사했다.

A 모델 단계만 수용하며 실제 numeric state·동일Tx 영속화·격리PG fault/upgrade는 B 구현으로 이어 간다. 포괄 수집핀 스냅샷은 독립 실행 시작 후 수집한 한계를 기록했다. 전체21/63 유지, 제품 빌드·배포·Claude 변경 없음.

- [원자성 모델·외부 원장·관측 주기 검증](../work-records/2026-10-30-roadmap-atomicity-ledger-model.md)


## DYN05 p1 실제 실행·비교와 순위 예외 수용

필수7정책의 순수 adapter·registry·prefix replay/read 기반 runner·Go CLI와 Python compare_runs를 구현했다. 실제 CLI가 게시한7파일을 그대로 비교했으며, 실패·대체·빈 일정과 각 비용 분모를 보존한다. snapshot은 수치 seed가 아닌 consistency witness로만 읽고 state는 원 사건열로 재생한다.

사용자가 기존순위 우선 규칙을 선택해6개 non-B0 정책을 p1-zeroaux-order-v1으로 구분했다. 모든 보조 가중치가0이면 원 상태/feature 검증과 binary64 점수를 유지한 채 B0 순서·포장을 보존한다. 원 버전의 등록 반례와 실패는 실제 재현해 보관했다. 새 버전은 같은 minsubnormal/score0 반례에서 B0와 일치했다.

독립 포함208메서드894세부 일반/race·실제7정책/Python비교 통과. 고정42자산·9source·69입력 검증. 최신 clean 부모a3cd67de에 소스9개를 그대로 통합하고 기본수집380/857·전체92315 종료0(Python624·Go복잡도/vet/race·UI532)을 확인했다.

- [실제 정책 실행기·비교·수치 예외 검증](../work-records/2026-10-30-roadmap-native-policy-runner.md)

원 v4 정의상 p1은 전체 DYN05/capture 완료가 아니다. 선택적 fastslow/capture를 자동 필수화하지 않았으며 전체21/63을 유지한다. 실제 서비스·native producer·실험과 채택은 후속이다.

## 정책 비교 선언 중복 제거와 원격 품질 후속

최초 PR491 원격 검사는 중복8.4%와 테스트 파일 쓰기의 보안 흐름 지적으로 실패했다. 원 결과와 소스를 보존하고, 복제된 모델 선언을 기존 고정 계약 SHA 확인 후 재사용하도록 줄였다. 기존25모델·enum·primitive·필드 순서·required 값은 정확히 같다. 상수 경로 쓰기는 목적지와 데이터를 분리해 표현했으며 실제 제품 경로 조작 취약성을 입증한 것으로 과장하지 않는다.

독립12메서드62세부·작성22/45세부·실제 cold-loader 파일/오류 경계와 root51핀·7증거를 확인했다. 전체94001 종료0(Python627·Go복잡도/vet/race·UI532). 원격 새 판정은 아직 대기하며 품질 기준·제외·고정 계약은 바꾸지 않았다. 전체21/63 유지, 제품 빌드·배포·Claude 변경 없음.

- [고정 정책 schema 재사용·원격 품질 후속](../work-records/2026-10-30-roadmap-policy-schema-reuse.md)

## 실제 상태 통합의 결합 기준 검증

PR491의 정책 실행기·schema 후속과 PR492/493의 실제 수락 입력 확인·원자성 모델을 하나의 선행으로 결합했다. 수용된16소스와 고정42계약/증거 핀은 그대로이며 진행 기록 충돌은 양쪽 문단을 모두 보존했다. 기본 수집451과 전체55904 종료0(Python695·Go복잡도/vet/race·UI532)을 통과했다.

이 결합은 새 native 상태 저장 구현 자체가 아니다. 명시적 등록/설정·동일Tx capture·numeric state/seen/consumption/receipt/checkpoint·전체 근거 재검증·삭제 후 무효화는 별도 private 후보에서 실제 서비스 검사와 자체 격리PG 검사로 이어 간다. 원격 공유 이력은 재작성하지 않았고 전체21/63을 유지한다. 제품 빌드·배포·Claude 변경 없음.

## 선택 범위와 현재 개발 수 정정

원 카드·최초 계획을 다시 대조해 전체63개가 필수49개·선택14개임을 확인했다. 이전 감사의 필수50·선택13에는 원래 선택인 DYN04 분류가 빠졌다. 원 감사는 이력으로 보존하고 현재 수만 정정한다. 필수21개 완료·28개 미완료이며 하위 구현 수용은 전체 완료 수에 더하지 않는다.

선택14개는 각자의 선행·별도 질문·검토된 계약·비교 자료가 미충족하여 보류한다. 기존 기능은 삭제하지 않고 자료 부재를 무효과나 기각으로 취급하지 않는다. 재검토 조건과 FLOW fixture 중복 구현을 지금 선택하지 않은 이유를 별도로 기록했다.

- [선택14개 결정·재검토 조건](optional-decisions.md)
- [선택 범위 감사 작업 기록](../work-records/2026-10-30-roadmap-conditional-selection.md)


## SYS-02 실제 native 수락·영속 인계 수용

기존 수락 API의 권한·독립 검사자·trace·현재 target/evidence·재시도 검증을 재사용해 명시 설정/등록과 같은Tx durable capture를 연결했다. state·관측seen·적격 positive 소비·event·checkpoint·capture 결과를 한Tx에 확정하고 현재 근거·숨긴 hash·삭제 marker를 다시 검사한다. 고정 합성DYN의 Task/Trial을 위조하지 않고 arkism-native-dynamics-v1의 별도 native identity/설정/시계를 사용한다. 연구 효과나 기억의 인과 기여를 주장하지 않는다.

작성자 일반/race 각각270메서드·1189세부사례와 신선한 독립21/93이 실패·skip0으로 통과했다. 실제 제한 역할/DB/HTTP·중복·유실·원자성·삭제·시계·추가Eval참조거절을 대조했고 실제7e Init4+구버전 Service의 legacy 자료에서 최종 Init5로 올려 bytes/시각/hash/unknown 보존과 no-backfill·새 사건1회효과를 확인했다. 실제 연결 종료는새소스의독립4+저자4 PID를bool/closed/FATAL로 대사했다. 원 self-kill의권한오류를실제종료로세지않은과거정정도보존했다.

root는125런타임증거·2734실행소스·6고정A/DYN핀·173이전증거를재검산했다. 선택회귀Python699·UI581·전체Go vet·실제CI범위새코드린트0을 확인했다. 공개사본의상속주석6줄내부식별자만정리하고Go parser/scanner2558실행토큰동일을확인했으며원동결소스/검사는바꾸지않았다.

SYS02의원A·현재v8거절/멱등/unknown·B5기준을정확한격리native범위에서충족해**전체22/63,필수49중22완료·27미완료**로갱신한다. SYS03의실제같은reader packing포함영수증·표시/저장/읽기/답변전체연결과SYS01 Search/HTTPshadow·SYS04실제복원은별도남아있다. a389 고정기준이며별도관측된669main전체통합/출시수용으로재명명하지않는다. 제품CLI를빌드하는일부기존검사때문에전체check_local/모든Go검사통과로표시하지않았다. 빌드·배포·main병합·Claude변경없음.

- [native 수락·저장 범위](native-state.md)
- [작업 기록](../work-records/2026-10-30-roadmap-native-service.md)


## SYS-04 A 수명·현재 Eval metadata 모델과 기본 수집

원 source/memory 프로필과 signed64 clock 범위를 보존하고, 별도 metadata-v2에서 sources/memories/evaluations/eval_tasks/eval_runs/eval_trials/judge_calibrations의 exact scope/ref/revision/hash를 대조한다. Sources/Target/Relations와 Eval3참조·trial task/run은 필수 AND 근거이며, 누락·foreign·stale·unknown·cycle과 불완전 coverage/readback을 거절·보류한다. 여섯 복원 필드와 일곱 삭제 facet을 유지하고, 같은 여섯 hash만으로 불완전 graph의 복원을 허용하지 않는다. 승인된 기억과 ordinaryEval currentness·학습 자격은 서로 구분한다.

새 프로필의 정상 Unicode/129~256byte/Cf ID를 두 삭제 입력 경로가 기존 ASCII label로 거절한 독립8반례를 보존했다. 새 후보에서 그 두 경로만 v2 Ref-ID 규칙을 쓰도록 고쳐 작성자59메서드·515세부사례 및 독립19/281이 실패·skip0으로 통과했다. 기존54 fixture 전체 plan 출력과JSON3원문을 보존했으며, v1 strict label·v2 fact-withdrawal/history 미지원은 그대로다. 현재 Go의문자열조건119경계대사는별도격리검사이며기본Python수집과구분한다.

R8 기본 수집에 정확한 모듈 한 개만 등록해 기존451개ID와새59개ID를각각1회수집했다(510메서드·1483세부사례). 기존8음성+1양성·외부inventory음성2대조를 통과했다. 후자2개를기존생산guard가영구감지한다고주장하지않는다. root가5모델소스·28독립증거와6통합소스·31R8증거를재검산했고Python758(173+536+49)·메타데이터63카드96게이트를통과했다.

이 단계는 A의 합성 모델·필요경계 검사다. 실제 DB/백업 복원·삭제 완료·최종 게시 B와 SYS01/03 선행 통합 수용은 별도다. 전체22/63·필수27개 미완료를 유지하며 빌드·배포·원격main·Claude 변경은 없다.

- [모델 계약·검사 설명](../../evaluation/memory-harness-v1/system/sys_04/README.md)
- [작업 기록](../work-records/2026-10-30-roadmap-lifecycle-metadata.md)

## Native Search shadow and invocation-view component

SYS-01 now has an isolated actual Search/HTTP read-only adapter plus optional BuildContext invocation exclusions and separate v2 stage observations. Author normal/race61/160 and independent normal/race6/23 passed; original315 JSON bytes are unchanged. Final-return races after native read publication reject stale revision/deletion/source/grant changes. Root publication-copy integration15/23 passed, source21 disclosure checks passed, and inherited comment cleanup preserved7155 Go tokens.

This is component acceptance, not whole SYS-01: production host requests still use Search while the invocation view is a BuildContext input. Connecting that actual request to the final-render/transport/reader acknowledgement chain remains required, alongside SYS-03/SYS-04 integration. No fixture-only combination counts as that connection. Whole22/63, required27 remaining and optional14 held stay unchanged. Default off/shadow baseline behavior is preserved; no UI/product build/deploy/main merge. See [native Search scope](native-search-shadow.md) and [work record](../work-records/2026-10-30-roadmap-native-search-shadow.md).

## Native context delivery and lifecycle integration accepted locally

Current whole-card count is25/63:25 required verified,24 required remaining and14 conditional held. SYS-01, SYS-03 and SYS-04 now meet their original local-service criteria; the original58 requirement/22-gate audit, exact source lineage and earlier failed experiments remain preserved.

The final candidate combines verified public main f86b95f with the native changes. One invocation per mode ran387methods1374leaf in normal/race with0failure/skip; changed-Go39/39 normal/race, UI source591, full-module vet/lint and unchanged complexity gates passed. The active-native/view/reader/recovery checks are included, while independent1/2 and genuine-LOGIN reader6/64 retain separate counts. A new shared external-mutation test compares every physical database column without filtering purges, timestamps or fences.

The incoming public-main Go12/UI17 files are preserved inheritance. API semantics retain the independent6 native changes and4 main changes. No UI or daemon feature enablement is introduced by this native implementation. Legacy standalone-product helpers, full check_local and unrestricted all-Go testing remain explicitly unexecuted. This is local integration, not release/deployment or scientific performance validation. Source protection and latest-main compatibility checks are complete locally; PR browser/remote checks are tracked separately.

See [delivery and recovery boundaries](native-delivery-lifecycle.md) and the [integration work record](../work-records/2026-10-30-roadmap-native-system-integration.md). SYS-05 A workload/summary preparation has separate passing evidence; its B service/load implementation and SYS-06 mode/fallback integration remain unfinished.
