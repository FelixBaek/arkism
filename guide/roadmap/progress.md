# 로드맵 진행 기록

기준: 2026-10-29. 작업 검증 완료19/63(R1~R8, CRW-01~07, DYN-01, RTE-01~02, CAP-01). #150 준비는 별도 완료이며 63개 작업 분모에는 포함하지 않는다. 제품·연구·전체 로드맵 완료가 아니다.
여기서 카드란 이슈 안의 개별 작업이다. “로컬 카드 수용”은 작업의 구현·실제 로컬 검사·독립 검토가 완료 기준을 충족했다는 과거 기록 용어이며, 앞으로 “작업 검증 완료”로 설명한다. `verified`도 이 뜻이다.
원격 검사·PR 병합·실험·제품 통합은 각각 따로 기록한다.

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
| CAP-01 현재 기능·상태 목록 | 로컬 정적 목록 수용 | 24상태·3산출물, 소유19+독립9검사,29코드/테스트·main4차이 대조. 실제 설치·인증·실행 상태는 미관측 |
| DYN-01 사건·시계·상태 수명 계약 | 작업 검증 완료 | 별도 scalar 계약·Go28자료형/20보조 구조·23닫힌 해시 projection, 중요도 sidecar 정의. 소유20방법168세부·독립16방법163세부 일반/race 통과. 실제 사건 전이·재생·정책·CLI는 다음 카드 |
| CRW-07 채택·신규성·연구 준비 판정 | 작업 검증 완료 | 비공개4보고서, 작성자315·독립262 문서/해시/수치 검사 통과. 원1620행 분모·비용·실패 보존 확인. 개발 기반 유지, A 통합 실증 미확인·E 논문 not_ready |
| DYN-02 단일 상태 전이 | scalar 검증 완료·전체 카드 부분 수용 | 작성자32방법62세부·독립31방법65세부 일반/race 및 전체 회귀 통과. 중복·revision·이력 결합과 입력 불변을 검증했다. fast/slow는 조건부 미실행이며 전체19/63은 유지 |
| SYS-01 동일 후보 비교 계약 | A 합성 검증 완료·전체 카드 진행 중 | 작성자76방법116세부와 독립 검사 통과. 고정 후보·시각·전체 결과·쓰기/호출·손실 단계를 비교한다. 실제 제품 연결 B는 미실행이며19/63을 유지한다 |
| DYN-03 순서 재생·읽기 조회 | scalar 검증 완료·전체 카드 부분 수용 | 작성자25방법78세부·독립25방법39세부 일반/race 통과. nil배열·alias 오류우선순위 실제 실패 수정. fast/slow·운영 복원은 미지원이며19/63 유지 |
| DYN-04 tag/resource 선택 확장 | 보류·현재 미선택 | scalar 대비 필요성·fast/slow/B3 파일럿 근거가 없어 구현하지 않는다. 원 선행과 비교 계획을 수용한 뒤 재검토하며 더미 성공 모듈은 만들지 않는다 |
| #152~158의 나머지 | 미착수·읽기 설계 | 선행 수용 뒤 진행. 전체63카드는 필수49/선택14이며 선택·하위 단계의 조건은 소유권 명세에 유지 |

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
