# SYS-01 A: 동일 후보 shadow 계약과 합성 검산

Task: SYS-01.spec (#152)
Research: P1 engineering foundation; no effectiveness result
Design: Keep one native baseline input and distinguish diagnostic shadow selection from actual returned context and physical fences.
Relation: adapted
Deviation: Pure Python metadata and synthetic observations only. Existing Go Search, its transactions, authorization and packing are not executed here.

이 폴더는 **계약·합성 검산 단계(A)**다. 실제 제품 연결(B), 운영 채택, 권한 인증이나 성능 실험을 완료한 것이 아니다. 원 baseline과 DYN 계약·등록기는 변경하지 않는다.

## 입력과 출력

고정 `adapter_contract.json`의 raw SHA-256은
`82936159380c3d34720ea80822db0b8c10c3242dba8bb493876c7a8895bad5f0`다.
40개 닫힌 자료형과 3개 고정 해시 projection만 소비한다. caller schema, 임의 파일 경로, 저장소·권한·쓰기 capability를 받지 않는다.

`test_shadow_contract.py` 안의 순수 함수:

- `decode_typed(bytes, type_name)` / `validate_typed(type_name, value)`: 고정 구조·범위·해시·상호 참조를 검사한다. unknown/missing/duplicate key, 잘못된 Unicode, bool/실수/지수형 정수, 초과 int64, NaN/Inf bits, 비정규 음의0 bits를 거절한다. 오류에는 고정 code와 알려진 schema path만 포함한다.
- `canonical_bytes(type_name, value)` / `semantic_sha256(type_name, value)`: 기존 `contract_io`의 고정 문자열·배열·원시 인코딩 helper를 재사용한다. 원 `_OBJECTS`를 확장하지 않는다. SYS domain + NUL + 고정 type/projection 이름 + NUL + typed bytes를 해시한다. raw artifact SHA와 구분한다.
- `validate_snapshot(snapshot)`: 동일 scope·전체 ref·후보/lanes·예산·원 RRF window·clock·가중치 관측·두 단계 commitment를 확인한다. 실제 DB 상태나 자료 본문을 인증하지 않는다.
- `compare_shadow(request, decision, native, view_facts, advisory, losses, side_effects, cost, paired_semantics)`: 닫힌 합성 입력을 연결해 `synthetic_model` / `synthetic_only`의 `ShadowLedger`를 반환한다. ranker나 Search를 실행하지 않는다. 실패는 고정 `SYSContractError`이고 입력을 바꾸지 않는다. 성공 출력도 입력과 깊은 복사로 분리한다.

고정 codec의 비공개 helper 이름은 명시적인 버전 의존이다. 이것을 안정적인 공개 API로 취급하지 않으며 codec 변경 시 별도 대조가 필요하다. float wire bits는 문자열 모양으로 해시하지 않고 논리 binary64 값으로 인코딩한다.

## 관측과 미관측

실제 기준선은 native RRF의 내부 `3*limit` window 뒤 recency·outcome 가중치와 최종 limit를 적용한다. offline B0와 이름·의미를 섞지 않는다. 합성 snapshot은 동일한 검증 후보 C를 먼저 고정하고, 원 RRF window → 원 Now 인자 → 원 Get 결과 → recency 순서의 **선언된 관측 metadata**를 별도로 연결한다. window 밖 가중치를 추가 조회하거나 관측값1로 채우지 않는다.

`NativeOutcome`은 현재 `request.mode`의 원 Search 결과에 해당한다. 현재 mode의 witness만 그 refs/order/status/error와 정확히 연결한다. 다른 mode의 독립 관측이 같은 snapshot/scope 안에서 유효하게 다른 refs·상태·본문·영수증을 보이면 `different`로 보존한다. 다른 scope·불가능한 후보 참조·위조된 정합성은 거절한다. 양쪽 결과를 한쪽과 강제로 같게 만들어 차이를 없애지 않는다.

실제 반환과 advisory shadow는 별개다. `eligible_at_observation`은 실제 최종 권한 fence나 정책 철회의 원자적 보장이 아니다. 미관측 reader/view는 unknown이다. 단발 context 제외, 지속 정정·철회, 현재/다음 턴/새 세션, 다른 활성 tag/reader와 history를 각각 기록한다. supersession edge만으로 다른 reader의 포함을 지우지 않는다.

실제 native fence는 카운터를 쓴다. 따라서 shadow용으로 다시 호출해서 측정하면 안 된다. 합성 검산은 ref·source별 관측과 원 호출 trace를 비교한다. 합계가 같아도 다른 ref의 쓰기는 다르다. rollback으로 카운터 변화가0이어도 shadow 호출이 있으면 무호출이 아니다. `complete_watched_set`은 이미 알려진 반환·선택 후보·관련 source·anchor/run을 포함해야 한다. 이는 제한된 관측 집합의 관계 검사이며 전 DB·물리 실행·실사용 권한의 진실을 인증하지 않는다.

양쪽 반환·논리 영수증·부작용의 동일성은 각각 따로 계산한다. 생성 ContextID/usage ID/생성 시각만 고정 규칙에 따라 제외한다. bundle Authority, 순서와 중복을 보존한 Degraded, post-clip 본문·summary의 SHA, score/lanes/reasons/vector/history/token/context metadata와 영수증의 principal/scope/ref/CAS/rank를 비교한다. 예상 밖 권한·열화 문자열은 새 권한으로 해석하지 않는다. 관측 nil/빈 Degraded의 JSON 의미는 knownempty `[]`지만 미관측은 `null`이다. `_fixture_` 정규화 helper는 이 합성 관측 규칙만 확인하며 caller의 관측 선언을 인증하지 않는다.

## 현재 입력과 단계 연결

paired native clock의 관측값과 knownness는 현재 immutable snapshot의 실제 baseline clock과 일치해야 한다. 미관측 `not_recorded`는 unknown으로 남기며 알려진 clock이나 동일성으로 보충하지 않는다.

현재 mode의 알려진 token-current 집합 P_B와 history 집합 H_B는 최종 transaction 직전 `pre_final_indices`와 연결한다. 이 pre-final은 S_B pre-pack이 아니다. committed 반환은 각각 P_B/H_B의 정확한 전체 material ref와 일치해야 한다. 최종 transaction이 실패해 실제 반환이 비어도 이미 관측된 packed pre-final을 지우지 않는다. 다른 mode의 유효한 독립 관측은 이 현재 단계와 강제 동일하게 만들지 않고 기존 `different`로 보존한다.

Scope는 현재 native Search의 `validScope`/`scopeName` 규칙을 따른다. 네 필드는 각각 UTF-8 256byte 미만이고, workspace/project는 비어 있거나 dot/dotdot, slash/backslash 또는 Unicode control(Cc)을 포함할 수 없다. 평범한 Unicode·공백 label과 optional user/agent의 기존 동작은 보존한다. 더 좁은 `ContextScopeV1`의 식별자·필수 user 규칙을 Search에 덮어쓰지 않으며, 다른 `NativeID` 필드에도 Scope 전용 규칙을 넓히지 않는다. 이는 입력 정합성 검사이고 실제 actor 권한이나 현재 scope 승인을 인증하지 않는다.

## 손실 분모

다섯 stage를 정확히 한 번씩 고정 순서로 기록한다. 실패·미실행·미관측도 레코드를 빼거나 0/100%로 채우지 않는다. 분모0은 undefined다.

| 단계 | 손실 집합 / 분모 |
|---|---|
| native RRF window | C − R / \|C\| |
| native final limit | R − S_B / \|R\| |
| char packing | S_B − (C_B ∪ H_B) / \|S_B\| |
| current token packing | C_B − P_B / \|C_B\| |
| shadow policy selection | C − S_S / \|C\| |

현재 항목과 history는 서로소인 **전체 material ref**로 구분한다. root가 선택한 요약은 다음과 같다.

- `baseline_postpack_count = |P_B| + |H_B|`
- `packing_losses = |S_B| − (|P_B| + |H_B|)`
- `final_limit_losses = |R| − |S_B|`

필요한 집합이 모두 관측된 경우에만 숫자로 표시한다. 이 값은 packing 단계의 항목 수다. 나중의 최종 transaction 실패로 실제 반환이0이 된 것과 구분한다. token packing은 current Items에 적용되며 history를 손실이나 전체 token budget 보장으로 바꾸지 않는다. 합성 `TokensUsed`도 실제 제품 tokenizer 실행이나 가격·성능 측정이 아니다.

## 재현과 실제 범위

저장소 루트에서:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover -s evaluation/memory-harness-v1/system/sys_01 -p 'test_*.py' -v
```

`race_cases.json`에는 실제 typed 입력과 고정 기대값을 가진 46개 사례가 있다. 원30개와 추가9개, history 요약2개, mode 방향5개다. 각 사례는 별도 discoverable test method로 실행한다. 보충 검사는 raw JSON·정수·Unicode·고정 오류 경로·해시·입력/출력 불변·scope·관측 완전성의 음성을 확인한다. Scope의 literal typed bytes/SHA와 손계산 RRF·손실 분모는 별도 고정 oracle다. fixture의 ledger SHA는 구현 출력에서 재생성하지 않는다.

테스트 전용 `_fixture_rrf` / `_fixture_native_scores`는 수식의 제한된 합성 검산이며 실제 native 실행·packing·권한 검사로 표시하지 않는다. 실패 상태가 의도된 입력도 기대하는 `different`, `unknown` 또는 고정 오류를 실제 검사한다. 등록된 정책 구현이 없는 상태에서 합성 B2 선택 결과를 실제 B2 실행이라고 하지 않는다.

검사 중 발견한 자료형 key·ordinal 경계·scope 연결·관측 완전성 결함은 원 코드/로그를 보존한 뒤 고쳤다. 긴 case 이름의 loader 준비 오류와 history fixture 구성 오류는 제품 결함과 따로 기록했다. 작성자는 이 코드의 기능 독립 수용을 판정하지 않는다.

B에는 별도 실제 Story·DYN 정책 선행·공유 코드 배정 및 실제 Go/fake repository/격리 PostgreSQL 경쟁 검사가 필요하다. 이 A 도구는 제품 코드, API, DB, 모델, 네트워크, 빌드·배포·Claude 작업을 변경하거나 실행하지 않는다. 가짜 비용0·실사용 성공률·논문 우월성·분산 exactly-once도 주장하지 않는다.
