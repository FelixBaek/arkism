# Routing event contract and observed field provenance

Task: RTE-01 (#155)
Research: F development foundation; no performance, causal or statistical result
Design: Separate routing-v2 sidecars over the existing Go routing paths
Relation: faithful
Deviation: Runtime collection, API changes and authenticated evidence resolvers remain proposals. Unavailable DYN-01/SYS-02 interfaces are represented only as unknown.

The private contract separates decisions, attempts and outcomes without adding
fields to baseline v1. Its11 closed models and94 fields distinguish16 existing
observations,64 fields requiring new collection and14 unobserved fields. The
mapping follows current Go crew, gateway and workflow routing, including work
context attached after selection and best-effort per-attempt reports.

Decision inputs require source availability and observation cutoffs. Historical
outcome features must join the actual prior decision/outcome, scope, hash and
clock evidence. Batch validation tolerates arrival permutations while rejecting
missing joins, parent cycles, conflicting IDs and foreign scope. Exact replay
is idempotent. Provider acceptance and transport success remain distinct from
an independently evaluated task result.

Deterministic selection carries its actual support without a numeric propensity.
Randomized examples validate distribution/reference structure only; they do not
prove a real randomized mechanism. Unknown cost stays null, and an undispatched
attempt cannot carry positive observed spend. Existing crew-to-personal-memory
outcome exclusions remain intact. No runtime collector or new execution engine
is introduced.

## Verification and private evidence

The owner suite passed36 tests. Independent review reran that suite and passed
139 additional tests, including all94 missing-field cases,22 unknown-field
cases, strict numeric/Unicode boundaries, future inputs, cross-scope joins,
six arrival permutations, duplicate/conflicting IDs and independent typed
canonical hashes for the three literal examples. All11 frozen artifact pins
matched. The native source pins also matched the reviewed base.

One ownership progress-file pin records an intermediate author read while the
root writer was updating the plan. It is preserved as a capture checkpoint,
not presented as bytes committed in the base or as an unchanged current file.
Test authoring corrections are separated from implementation findings.

Actual failing-before evidence exposed future observed features accepted when
availability was unknown, unknown attempts supporting transport success, and
positive observed cost on a not-attempted record. These were corrected and
retested. Valid observed zero, estimated reservation and unknown-null cost
remain distinct.

Private logical artifact pins:

| Artifact | SHA256 |
|---|---|
| routing_v2.schema.json | 044b466ba9db692b98ab324cc268890de04ed1b0d1b5e49a4ba7f48f57698f9c |
| validator.py | 1822fda84190d0866f6b583760983cd3cf042e3425c940d6732dd622ad0b97dd |
| test_validator.py | 15e2c398e7fd3e5e35f3e8e829dfce9c08a3b8b61b68f1c627372dbd05fe20e8 |
| integrity.json | ef35281d295db08c29523812bc13a76287fb632ef8a7ac024dfd4ed027ab5a31 |

The raw contract/source artifacts remain private. A well-formed hash or checker
reference is not authentication, authorization, real randomization or proof
that a producer omitted no calls. Actual API/runtime integration, independent
task evaluation, sampling and performance remain unexecuted. The fixed baseline
contract is unchanged. No model/provider/API/effect call, product build,
release or deployment was performed by this card.

## 한국어 작업 기록

작업 ID: RTE-01 (#155)
변경 이유: 기존 라우팅 결정·시도·결과를 별도 연구 계약으로 연결하고 실제 관측·새 수집·미확인 필드를 구분한다.
실행한 검증: 소유36·독립139검사,11모델/94필드 출처·11아티팩트 hash·고정 canonical3벡터 대조 통과.
미실행: 런타임/API 통합, 실제 인증·무작위 기구·작업 품질·성능 검증, 모델·외부 효과 호출, 빌드·배포.
현재 한계: 로컬 schema·필드 대응 카드 범위만 수용했다. 원문은 비공개로 유지하며 미수용 DYN/SYS 인터페이스를 구현됐다고 표시하지 않는다.
커밋 상태: 비공개 원문을 공개하지 않고 검증 범위·해시·진행 기록을 단계 PR에 기록한다.
