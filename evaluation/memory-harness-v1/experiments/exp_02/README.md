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

## 탐색용 파일럿 어댑터

`internal/memoryharness/dynamicsv2/exp02_pilot_adapter_test.go`는 EXP-01 생성 계획(사건·질문만, 정답 파일은 열지 않음)을 native 정책 엔진 입력으로 옮겨 일곱 정책 순위를 만드는 시험 코드입니다. 위의 실행계획 gate를 열지 않으며, 그 결과는 공식 EXP-02 결과나 REF winner가 아닙니다.

```sh
go test ./internal/memoryharness/dynamicsv2 -run TestEXP02 -count=1
EXP02_POOL=1 EXP02_PLAN_DIR="$PLAN_DIR" EXP02_OUT_DIR="$NEW_DIR" \
  go test ./internal/memoryharness/dynamicsv2 -run TestEXP02PilotAdapter -count=1
```

환경변수 없이 돌리면 한 스트림 인라인 계획으로 투영만 검사합니다. `EXP02_PLAN_DIR`을 주면 `rankings.jsonl`(정책별 선택·순위·선택 기억 원문)과 `manifest.json`(투영 규칙, 읽은 입력 파일 hash)을 새 폴더 `EXP02_OUT_DIR`에 씁니다. 실패한 정책 행이나 비활성 기억 선택이 하나라도 있으면 시험이 실패합니다.

`EXP02_POOL=1`은 같은 split의 스트림을 인물·주제 조합이 겹치지 않게 묶어 한 native 스트림으로 합칩니다. 합칠 때 어느 스트림이든 자기 사건 순서가 바뀌면 거절합니다.

한계: 과거 시점 질문은 cutoff 전에 무효화된 판을 되살릴 수 없어 모든 정책에서 정답에 닿지 못합니다. 계획의 미인증 outcome은 synthetic_independent 근거로 올려 투영하므로, outcome을 쓰는 정책의 차이는 근거가 인증된 경우를 가정한 값입니다. 중요도 특성은 기억 종류에 고정 가중치를 주는 단순 규칙입니다.

## 원자료 기반 계획 생성기 (부분 구현)

별도 `produce_run_plan.py`는 원본 자료·분석 일정·별도 합성 수용 입력·소유자 artifact 참조에서 질문/반복별 원래 typed RunPlan, `not_run` 원장, v2 시점 연결과 EXP03 projection 입력을 생성한다. 원래 `build_run_plan.py plan/verify-index`의 보류 gate는 그대로이며, 이 생성기가 그 gate의 수용을 대신하지 않는다. 참조 bytes/hash는 읽고 대조하지만 사람의 수용·독립성을 인증하지 않는다. 원자료 manifest의 events/queries/gold raw SHA도 직접 확인한다. 원본 analysis-source bytes를 그대로 보존하고 final-k 불일치는 덮어쓰지 않고 거부한다.

CLI는 `--request --input-root --evidence-root --output-root --native-cli --native-cli-sha256 --out`을 명시한다. out은 input-root의 새 `produced` 디렉터리만 허용하며 덮어쓰지 않는다. 선언된 argv는 고정 native run 형식으로 생성한다. CLI는 핀으로 지정한 기존 실행파일의 hash-stream 프로세스1개를 사용하며 정책 실행은0개다. 결과도 `built_not_executed_or_authorized`다. 후속 별도 실행 dispatch·실제 원장 recorder·최종 검토와 REF/확인실험 전체 인수 연결이 필요하다. 자동 제품 빌드·정책 실행·승인 기능은 없다.

## 실제 native 실행 원장 (부분 구현)

`record_native.py`는 `--run-plan --run-index --projection-plan --analysis-plan --schedule --dispatch --trusted-dispatch-sha256 --input-root --evidence-root --output-root --native-cli`를 명시해 호출한다. 확인실험은 `--final-inputs --trusted-owner-review-sha256`도 함께 필요하다. 별도 실행 배정은 정확한 계획/미실행 원장/projection/실행파일 SHA, 순서 있는 시도 목록과 nanosecond 제한을 결속하며 범위는 `offline_native_fixture_only`다. 이는 실제 사람의 권한 인증을 대신하지 않는다.

읽기 전용 preflight 이후 새 output-root/recording journal을 만들고 실제 고정 native run을 기록한다. native 출력의 부모 디렉터리는 미리 준비되어 있어야 한다. 고정 bytes의 private image를 실행하며 원래 argv와 실행 image SHA를 함께 기록한다. raw stdout/stderr·시간·exit·실패·미측정 비용과 원본 재검산 근거를 보존하고, 기존 produced/index.json 대신 recording/index.json과 projection.json을 새로 남긴다. 기존 output/journal은 덮어쓰지 않는다. 실제 프로세스 시간과 검증 호출, REF 재검산의 미측정 프로세스 수를 구분한다.

시간초과와 입력 거부는 실패로 보존한다. 제어 가능한 취소는 자식 종료/회수와 현재 시도 저장 후 다음 실행을 중단하고 CLI130으로 끝낸다. 호스트 종료·예상 밖 치명적 중단의 journal은 완성 영수증이 아니며 복구가 필요할 수 있다. v1/v2 실행 배정은 job당1회를 지원한다. 다중 시도는 아래 v3에서 별도로 연결한다. 실제 연구 결과·사람의 검토와 전체 카드 수용은 별도다.

## 단계별 산출물의 v2 이름

`exp02-native-plan-request-v2`는 기존 필드에 `production_namespace`(단일 ID)와 `task_contract`(ArtifactRef 또는 null)를 추가한다. 생성 위치는 input-root의 해당 새 디렉터리이며, task가 있으면 전체 원자료4종을 같은 bytes로 결속하고 projection v2로 연결한다. `exp02-native-execution-dispatch-v2`는 `artifact_namespace`를 추가해 원장을 output-root의 새 단일 디렉터리에 저장한다. v1은 계속 produced/recording을 쓴다. 이 기능은 검증/최종 단계 공존을 위한 것이며 job당 여러 재시도의 출력 연결을 대신하지 않는다.

## 실제 다중 시도와 출력 보존 v3

`exp02-native-plan-request-v3`는 v2 필드에 `attempt_order`(고유 ID 1~16개)와 `artifact_namespace`를 추가한다. 고정 v1 RunPlan/RunIndex 자료형은 바꾸지 않는다. 한 job의 모든 시도는 계획의 동일 argv를 실제로 실행한다. 출력은 실행기가 새로 예약한 namespace/work/job 디렉터리에서 만들며, 각 시도가 끝나면 namespace 안의 결정적인 별도 디렉터리로 옮긴다. Linux의 원자적 RENAME_NOREPLACE를 사용해 기존 디렉터리를 덮어쓰지 않는다. Linux/libc 진입점이 없는 호스트는 실행 전에 거부한다. 실제 파일 시스템에서 이동이 실패하면 출력을 보존하고 추가 실행을 중단한다.

`exp02-native-execution-dispatch-v3`는 각 attempts 항목에 `timeout_ns`를 추가한다. 전체 제한도 함께 적용한다. `exp03-native-projection-plan-v3`는 nullable task_contract와 `output_archive`(protocol: exp02-native-attempt-archive-v1, artifact_namespace)를 결속한다. native 검증은 각 시도의 정확한 archive 경로만 읽으며 다른 시도의 출력으로 바꿔치기하면 거부한다. 원래 CLI의 argv·stdout·출력 파일 bytes는 고치지 않는다.

완료뿐 아니라 시간초과 후 남은 부분 출력도 보존한다. 부분 출력은 성공으로 인정하지 않는다. 주 분석에는 같은 시계에서 먼저 완료된 결과만 쓰고 정확히 같은 완료 시각이면 사전 순서를 쓴다. 이후 중복 결과는 주 분석에 더하지 않는다. 모든 시도의 실패·실행시간·비용·미측정 항목은 남는다. 실제 검증 사례는 시간초과 1회와 완료 2회에서 주 분석 7행, 시도 3개의 비용 보존이다. 합성 fixture 실행이며 실제 연구 결과 수용이나 제품 실행 권한을 뜻하지 않는다.
