# FLOW에서 스킬 개선 이력 이어가기

스킬 개선 관리 흐름은 실행 근거, 개선 후보, 독립 비교, 승인, 제한 시험, 적용, 후속 관찰과 복구를 하나의 ID로 연결합니다. 기존 스킬 생성·비교·trial·적용·복구 API의 권한과 검증을 사용합니다. 관리 단계가 앞으로 이동했다는 사실만으로 스킬의 품질이 증명되는 것은 아닙니다.

새 관리 흐름은 `jobs`의 `flow_skill_improvement` 기록으로 보관됩니다. 실행 중 작업의 지침을 새 버전으로 교체하지 않습니다. 실제 적용은 기존 스킬 선택 정책을 따르는 이후 작업에 반영됩니다.

## 준비할 근거

시작 요청에는 종료된 FLOW 실행의 정확한 ID/revision, 개선할 skill ID, 고유 request ID를 함께 묶습니다. 원래 실행 참조는 개선을 시작한 맥락이며, 자유 형식의 실행 요약을 독립 평가로 바꾸지 않습니다.

생성 재료는 기존 `GenerateSkillInput`입니다. 검증된 lesson 참조, 스킬 경로와 사전에 고정한 비교 계획이 필요합니다. 기존 생성 API의 최소 실행 수, 반성·출처·독립 평가 연결, 비용·전송 정책 검사를 그대로 통과해야 합니다. 비교 기준과 예산을 후보가 자기에게 유리하게 바꾸는 입력은 받지 않습니다.

`runtime`과 `trial_ends_at`으로 시험 환경과 종료 시각도 고정합니다. 운영에 자동화 정책이 없으면 service가 승인·trial·적용·관찰·복구할 수 없습니다. 에이전트나 pipeline의 자기 승인을 허용하는 설정을 추가하지 않습니다.

## API와 진행 순서

| API | 동작 |
|---|---|
| `POST /v1/flow/improvements` | 관리 기록 생성. 필드: `scope`, `request_id`, `source_run`, `generation`, `runtime`, `trial_ends_at`. 이 요청만으로 모델을 호출하거나 스킬을 적용하지 않습니다. |
| `GET /v1/flow/improvements` | 현재 scope의 상태·참조 목록. `limit` 기본 50/최대 100, `after`로 이어 읽습니다. `next_cursor`가 있으면 빈 페이지 뒤에도 계속 읽습니다. |
| `GET /v1/flow/improvements/{id}` | 현재 권한으로 근거를 다시 확인한 뒤 저장된 상세 상태를 읽습니다. |
| `POST /v1/flow/improvements/{id}/advance` | 한 단계를 명시적으로 진행합니다. `scope`, `expected_revision`, `action_id`, `action`을 보냅니다. |

상세 응답은 `{id, revision, improvement, authority}`이며 `authority`는 `data_only_untrusted`입니다. 목록은 `{items, next_cursor?}`이고 각 항목의 `flow_improvement`에 단계와 참조를 담습니다. 목록에는 생성 재료의 경로·본문·비교 계획을 복사하지 않습니다.

| 현재 단계 | 다음 action | 실제로 수행하는 일 |
|---|---|---|
| `evidence_ready` | `generate` | 기존 생성 API로 개선 후보를 만듭니다. 모델 호출과 비용이 발생할 수 있습니다. |
| `comparison_required` | `compare` | 기존 독립 실행기가 만든 `SkillComparisonResult`를 기록합니다. 요약이나 성공 문구를 받는 단계가 아닙니다. |
| `approval_required` | `approve` | 사람의 명시적 승인 또는 현재 허용된 service 정책으로 기존 상태 전이를 요청합니다. |
| `trial_ready` | `trial` | 기존 API로 제한 시험을 시작합니다. 기본 활성 버전을 바꾸지 않습니다. |
| `trial_running` | `observe` | 실제 시험 사용 기록과 독립 평가를 확인합니다. 기록 없음과 평가 대기를 구분합니다. |
| `trial_comparison_required` | `compare` | 시험 후보의 정확한 revision에 대한 독립 비교 결과를 기록합니다. |
| `apply_ready` | `apply` | 시험 관측·비교·현재 정책을 다시 확인하고 기존 적용 API를 호출합니다. |
| `monitoring` | `monitor` | 적용 이후 기록된 회귀를 기존 감시 API로 확인합니다. 관측이 없다는 사실을 개선 성공으로 표시하지 않습니다. |
| `recovery_unavailable` | `monitor` 또는 `rollback` | 검증된 복구 대상이 없으면 그대로 남깁니다. `rollback`은 고정된 이전 버전과 기존 독립 검증을 요구합니다. |

`trial_comparison_required`와 `apply_ready`에서도 `observe`로 현재 시험 근거를 다시 확인할 수 있습니다. `monitoring`에서도 명시적 `rollback`을 요청할 수 있습니다. 실패한 비교는 `comparison_failed`, 실패·중단·만료된 시험은 `trial_failed`, 복구 완료는 `rolled_back`으로 남습니다.

이 표는 단계 순서를 설명합니다. 현재 사용자의 실행 권한을 뜻하지 않습니다. 특히 `compare`는 후보 생성자와 다른 service/pipeline의 측정 결과를 요구하며, `observe`와 `monitor`는 현재 명시적 자동화 정책을 가진 service가 수행합니다. 사람 앱이 service로 가장해 대신 호출하지 않습니다.

`compare` 요청의 `comparison`은 기존 `SkillComparisonResult` 전체입니다. 고정 데이터셋·환경 hash, 정확한 후보·기존판, paired quality/regression/safety 사례, 비용·시간과 집계 결과가 일치해야 합니다. 시험 이후 비교에는 관리 흐름의 trial ID를 서버가 결속합니다.

`apply`와 `rollback`은 선택적으로 기존 `ChangeInput` 형태의 `application`을 받습니다. 대상 버전·trial·revision을 다른 대상으로 바꿀 수 없습니다. 복구 대상에 기존 검증이 없으면 `application.validation_id`에 별도로 수행한 독립 검증을 지정합니다. 시험 적용의 근거는 저장된 실제 관측에서 구성하며, 임의 evidence 목록으로 바꾸지 않습니다.

사람의 적용 요청에도 저장된 시험 근거의 최신 revision·부정 평가·전체 관측 집합을 사전 확인합니다. service의 자동 적용은 기존 적용 트랜잭션 안에서도 현재 운영 정책과 전체 근거를 다시 검사합니다. 사람의 명시적 적용은 기존 사람 승인 정책을 사용하며, 이 관리 흐름이 별도의 자동 적용 권한을 만들지는 않습니다.

다른 에이전트 범위의 시험 기록은 기존 정책이 허용한 범위에서 관측합니다. 상세·목록을 다시 열 때도 같은 사용자 신원을 유지하며 각 출처·평가의 현재 읽기 권한을 확인합니다. 저장된 참조가 있다는 이유로 읽기 권한이 생기지 않습니다.

## 중복 요청과 중단 복구

동일한 `request_id`는 같은 scope·주체·원래 실행·스킬·생성 계획에만 재사용합니다. 각 단계에도 고유 `action_id`를 사용합니다. 같은 ID에 다른 내용이나 다른 실행 주체를 넣으면 충돌합니다. 정상 완료한 같은 요청을 재시도해도 생성·승인·적용을 다시 실행하지 않습니다.

단계 시작 전에 의도를 저장합니다. `pending.state=running`은 단계가 처리 중이거나 처리 도중 연결이 끊겼을 수 있다는 뜻입니다. `unknown`은 결과를 확정하지 못한 상태입니다. 새 모델 호출이나 적용으로 자동 재시도하지 않습니다.

이미 기존 API가 결과를 저장했지만 관리 기록 갱신만 실패했다면, 최신 revision을 읽고 같은 action ID와 같은 payload에 `recover:true`를 붙입니다. 복구는 결정적으로 연결된 후보·비교·승인·trial·적용 기록을 확인합니다. 새 후보를 만들거나 승인·적용을 다시 수행하지 않습니다. 근거가 없으면 불확실성을 유지합니다.

관측 단계의 중단 복구는 저장된 trial 상태를 확인하고 필요한 경우 다시 관측 대기로 돌립니다. 이를 독립 평가 완료로 표시하지 않습니다. 실패 상태나 현재 권한이 바뀌었다면 먼저 기존 스킬/trial 관리 경로에서 원인을 확인해야 합니다.

이 흐름 자체가 평가 실행기를 새로 만들거나 운영 자동화 정책을 설치하지 않습니다. 실험실의 fake 제공자·fixture 검증과 운영 모델 실행·정책 활성화·스킬 배포는 별도입니다. 실제 운영 상태는 [작업 기록](work-records/2026-10-28-flow-improvements.md)의 확인 범위를 참고하세요.

## 에이전트 앱의 MCP 도구

기존 `skill-router` MCP에 다음 도구를 제공한다. API와 같은 현재 권한 검사를 통과해야 하며, 앱에서 사람이 허용했다고 해서 agent 토큰이 human 토큰으로 바뀌지 않는다.

| 도구 | 입력과 범위 |
|---|---|
| `start_flow_improvement` | `request_id`, 정확한 `source_run`, 기존 `generation` 재료, `runtime`, `trial_ends_at`으로 관리 기록만 시작한다. 후보 생성은 별도 action이다. |
| `get_flow_improvement` | `id`로 상세·pending·영수증을 조회한다. 응답을 놓쳤다면 먼저 이 도구로 저장 상태를 확인한다. |
| `list_flow_improvements` | `limit`, `after`로 현재 범위의 목록을 읽는다. 반환된 `next_cursor`를 그대로 이어 쓴다. |
| `advance_flow_improvement` | `id`, `expected_revision`, `action_id`, 명시적 `action`과 필요한 기존 `comparison` 또는 `application` payload 하나만 전달한다. 필요할 때 같은 요청의 `recover:true`를 지정한다. |

범위와 자격 증명은 설정된 MCP 클라이언트에서 가져온다. `generation`·`application` 안에서도 scope를 임의 지정하지 않는다. 도구는 모델·평가기·스킬·정책·승인자를 대신 선택하거나 다음 단계를 자동 호출하지 않는다. 서버가 거부하면 다른 토큰으로 우회하지 않는다.

생성 요청의 연결 종료나 응답 시간 제한은 생성 실패·완료의 증거가 아니다. `get_flow_improvement`로 최신 상태를 확인하고, 동일한 불확실한 action에 대한 정본 기록이 있을 때만 명시적 복구를 요청한다. 모든 도구 응답은 `data_only_untrusted`이며 비교 품질의 증명이나 실행 권한으로 사용하지 않는다.
