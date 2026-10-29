# EXP-02.spec — 비교군·원인 분리 명세

이 단계는 오프라인 개발용 비교 명세와 검증기입니다. 실제 정책 실행, REF 선택, 연구 효과 측정, 제품 통합 또는 전체 EXP-02 수용을 뜻하지 않습니다. 연구값·winner·확인 연구 final_lock은 미정입니다.

`policy_matrix.json`은 B0/B1/B2/V3-P1-FD/V3-P1-IR/V3-P1-NOTIME/V3-P1-NOOUT 일곱 정책을 고정 순서로 선언합니다. FD는 독립 고정 감쇠 대조, NOTIME/NOOUT은 선택된 B2의 해당 스위치만 제거하는 비교입니다. 실제 adapter·설정의 검증은 DYN-05.p1의 후속 책임입니다. fallback은 error만 허용합니다. 선택하지 않은 optional 정책은 핵심 일곱 정책 명세를 막지 않으며, 미구현 정책을 결과로 채우지 않습니다.

`controls.json`은 시간/독립 outcome 제거, 사건 도착·중복·unknown, 지연과 수명, 시간과 간섭, 신호 출처, 원래 목표와 현재 목표를 구별합니다. exposure 대체·matched-write·fast/slow 계열은 현재 고정 native 계약에서 지원하지 않는 후속 비교로 남습니다. matched-write는 의미 있는 강화 적용 수이며 DB flush 수가 아닙니다.

`build_run_plan.py`는 고정 경로와 SHA로 EXP01 validator를 읽고, 그 모듈의 `V.G`를 단일 generator로 사용합니다. EXP01 공개 계약·template의 실제 raw bytes, 중첩 REF semantic hash, 배정된 root 결정·EXP01 로컬 수용 기록의 정확 bytes를 결속합니다. 로컬 수용은 합성 draft 단계에 한정됩니다. 메타데이터 문자열은 검토자 신원·독립성·실행 권한을 인증하지 않습니다. 보안 후속 helper의 독립 수용은 별도 추적합니다.

입력은 UTF8 JSON, 파일당 64MiB, 깊이32, 문자열4096 UTF8 bytes, 배열당100000개, 전체 배열 항목200000개 상한입니다. 중복 key·잘못된 Unicode·비유한 수·범위를 넘은 정수·알 수 없는 필드를 거절합니다. 정수는 int64이며 Count는 음수를 거절하고 기록된 process exit는 음수를 보존합니다. raw hash는 읽은 원문 bytes를 사용합니다. EXP 모델을 원 v1 registry에 추가하지 않습니다.

실제 spec 검사는 두 root를 명시해야 합니다. `INPUT_ROOT`는 저장소/자료의 허용 root이고 `EVIDENCE_ROOT`는 배정자가 제공한 정확 결정·수용 기록 root입니다. 환경변수는 아래 셸 예시의 자리표시자이며 코드가 자동으로 읽지 않습니다.

```sh
python3 -B evaluation/memory-harness-v1/experiments/exp_02/build_run_plan.py spec \
  --matrix "$MATRIX_ABSOLUTE_FILE" --controls "$CONTROLS_ABSOLUTE_FILE" \
  --input-root "$INPUT_ROOT" --evidence-root "$EVIDENCE_ROOT"
```

성공 결과는 `spec_valid_pending_owner_bindings`, `plan:null`, `processes_executed:0`이며 미완료 gate 목록을 함께 반환합니다. exit0은 자체 명세 검사가 유효하다는 뜻입니다. 실행 가능한 상태로 표시하지 않습니다. 실제 REF 규칙은 score/tie/공통튜닝예산 등을 별도 확정해야 하며 현재 template의 실행 허용값은 false입니다. 첫 validation 자료 수집은 winner null일 수 있지만 다른 실행 전제는 모두 필요합니다.

`plan` 모드는 `--matrix --controls --readiness --operating-plan --input-root --evidence-root --stage --out`을 각각 한 번 요구합니다. stage는 early_pilot/full_pilot/confirmatory입니다. 아직 배정되지 않은 OperatingPlan·신뢰 색인·materializer·native validation·선택 기록 schema 및 실제 DYN05 수용이 없으므로 현재 실행계획은 반드시 `external_contract_pending`으로 거절하며 output을 만들지 않습니다. 허용되지 않은 외부 payload를 임의 해석하거나 성공 stub으로 게시하지 않습니다. 후속 owner 구현·수용과 별도 배정이 이 gate를 열어야 합니다.

`verify-index` 모드는 `--index --plan --input-root --evidence-root --run-output-root`을 요구하며 읽기 전용입니다. 닫힌 자체 wire·실패 분모·행 수·재시도·실제 plan raw hash·공통 입력/argv·비용 단위를 검사할 수 있지만, 미배정 외부 schema·native 근거가 없는 최종 수용은 거절합니다. 실제 비용과 추정/누락을 구별하고 누락을 0으로 만들지 않습니다. 분석·task-success·REF winner 계산은 EXP03/선택 기록 소유자의 책임입니다.

계획 전 planning dispatch는 기존 입력·자료·허용 범위만 결속합니다. 완성 plan의 raw SHA는 사후 별도 execution dispatch가 결속하고 실제 실행 index가 참조합니다. 이 코드에는 실행기·native CLI 흉내·공유 DTO 변경·상태엔진이 없습니다. 실제 계획 게시와 실행은 미배정 schema가 수용된 뒤 별도로 구현·검증해야 하며 현재 안전 게시 성공을 주장하지 않습니다.

모든 오류는 고정 code/path JSON만 출력합니다. 적용 flags는 생략·중복·약어·환경 fallback을 허용하지 않습니다. bootstrap 불일치는 dependency_mismatch, 예상하지 못한 내부 오류는 internal_error/$로 구별하며 원문·traceback·비공개 경로를 출력하지 않습니다. artifact/hash 문자열은 권한을 인증하지 않습니다.

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover \
  -s evaluation/memory-harness-v1/experiments/exp_02 -p 'test_*.py' -v
```

공개 검사는 명시적으로 synthetic trust fixture의 pin을 test-local로 바꾸어 parser/결속 경계를 검사합니다. production pin을 유지하는 별도 subprocess는 이 합성 근거를 반드시 거절합니다. 실제 배정된 bytes의 spec CLI 검사는 별도 비공개 author 로그에 기록합니다. 실제 독립 수용이나 native 실행을 합성 fixture로 대체하지 않습니다. outer 방식 import 검사는 수집 가능성만 확인하며 R8 실제 연결 검사는 통합 담당의 책임입니다.

작성자 검사에서는 43 methods / 49 subtest events / 79 leaf test units가 실행됐고 실패·오류·skip은 0입니다. 13개 subtest 부모를 중복 합산하지 않습니다. 독립 검토·전체 회귀·실제 R8 연결은 별도 단계입니다.
