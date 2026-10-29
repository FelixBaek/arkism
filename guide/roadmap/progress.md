# 로드맵 진행 기록

기준: 2026-10-29. 로컬 카드 수용9/63(R1~R7, CRW-01, RTE-01). 제품·연구·전체 로드맵 완료가 아니다.
`verified`는 해당 카드의 실제 로컬 검증과 독립 검토를 마쳤다는 뜻이다.
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
| R8 CSV·인수·인계 | 구현·검증 중 | report23·acceptance40methods/48leaf·collector168 및 독립 검증 통과. 최종 Python412/Go/UI532 통과, 기본 builtbinary 완주 미실행 |
| CRW-01 현재 엔진·연구 계약 | 로컬 계약 수용 | [PR421](https://github.com/FelixBaek/arkism/pull/421). R0 뒤 독립 착수 가능. private 계약3파일, 정적29/독립 변조12·효과 양성7/음성25, 근거53파일·55행 확인. 제품 실행 검사는 아님 |
| CRW-02 호출·사용량 관측 | 구현 중 | 소유 race96leaf·독립 일반/race84개 통과로 helper 하위 단계 수용. 공유 호출부 연결·v2 참조 관문·전체 수용은 미완료 |
| CRW-05.spec 공정 비교 자료 | 로컬 명세 하위 단계 수용 | 소유46+독립16검사,162개 literal oracle·공정 입력 대조·20파일 hash 확인. 연구용 holdout·실행 adapter·전체 CRW-05는 미완료 |
| RTE-01 라우팅 사건 계약 | 로컬 계약 수용 | 소유36+독립139검사,11모델/94필드 출처와11산출물 hash 확인. 런타임·실제 무작위성·성능은 미검증. DYN-01/SYS-02는 unknown 유지 |
| RTE-02 시도별 비용 대사 | 구현 중 | 수용된 RTE-01의 비공개 계약을 소비해 비용·누락 대사기만 작성. 제품 동작·DB·외부 호출은 변경하지 않음 |
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
standalone go build를 요구하는 마지막 인수 경로도 아직 미실행이다.
0개 검사·필수 skip·누락 hash를 완료로 처리하지 않는다.

변경 이유·검증·한계의 상세 기록:

- [준비](../work-records/2026-10-29-roadmap-preparation.md)
- [R1/R2/R4](../work-records/2026-10-29-roadmap-baseline-contract.md)
- [R3/R5/R6/R7](../work-records/2026-10-29-roadmap-baseline-pipeline.md)

진행에 맞춰 이 기록과 소유권 manifest의 상태를 함께 갱신한다.
상태 문자열 자체는 실행 증거나 승인 권한이 아니다.

- [R8 구현·검증과 미실행 인수 관문](../work-records/2026-10-29-roadmap-baseline-reporting.md)

- [CRW-01 계약·근거와 검증 범위](../work-records/2026-10-29-roadmap-crew-contract.md)

CRW-02는 소유 helper의 독립 검토·인계 관문 `CRW-02.modules`를 추가해
전체 카드 수용과 공유 호출부 연결을 분리했다. 현재 63카드/95관문이며,
INTEGRATION 단일 작성자가 인계 후 등록된 공유 파일만 연결한다.
고정 CRW-01 비공개 계약 원문은 그대로 유지하고 후속 배정 기록으로 범위를 잇는다.

- [CRW-05.spec 모의 비교 자료·평가기와 한계](../work-records/2026-10-29-roadmap-crew-evaluation-spec.md)

독립 회의와 legacy 단독 실행의 실제 기록은 각각 다른 collection에 있다.
기존 crew v1 계약을 넓히지 않고 별도 execution-reference v2 관문을 등록했다.
새 비공개 계약·검증 자료는 R0 단일 작성자가 맡고, 전체 CRW-02 수용과
회의·host 통합은 이 관문도 충족해야 한다. 기존 CRW-01과 CRW-05.spec의
v1 수용 증거는 유지한다. 확인된 자기 root는 collection/id를 명시하며
null은 미확인으로만 사용한다. 효과 승인 CAS는 계속 crew_runs로 제한한다.

CRW-02 helper의 최종 고정 소스는 Python412/Go 복잡도·vet·전체race/
UI532 및 고정 lint0issues를 통과했다. 실제 호출부는 아직 연결하지 않았다.

- [CRW-02 helper·회계 불변식과 미완료 통합](../work-records/2026-10-29-roadmap-crew-observer.md)
- [RTE-01 라우팅 사건 계약·필드 출처·검증 범위](../work-records/2026-10-29-roadmap-routing-contract.md)
