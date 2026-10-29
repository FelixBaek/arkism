# Reconcile supplied routing cost evidence without inventing joins

Task: RTE-02 (#155)
Research: F local accounting foundation; no actual billing or performance result
Design: Accepted routing-v2 records plus exact scoped native refs and explicit charge expectations
Relation: adapted
Deviation: Research output only. Native routing records lack exact decision/attempt correlation, so unmatched records remain unallocated rather than joined by model or time. Product dispatch and error behavior are unchanged.

The private reconciler keeps router, generator and verifier charges separate.
Cache, retry and failure are nonadditive tags on those charges. Provider-reported
cost, configured-rate estimate, list-price estimate, subscription allocation,
invoice evidence and unknown have distinct provenance. Token-only estimates
require price revision evidence and remain estimates. An observed provider value
does not itself prove an actual invoice.

Exact receipt replay counts once. Conflicting payloads at one native version and
source CAS regressions cannot replace earlier paid evidence. Legitimate
estimate-to-observed refinement and a later actual source revision are distinct.
Missing schedules, unknown cost, ledger failure and incomplete streams preserve
coverage limits. Totals remain null when incomplete; exact known subtotals are
reported separately, including amounts too large for a finite aggregate.

Unjoinable paid evidence is retained in a sanitized, nonadditive diagnostic view.
It is not assigned a made-up role or target, added to totals, or presented as an
authenticated charge. Caller-provided schedules and receipts cannot prove that
no runtime calls were omitted. This is not a monetary reservation facility.

## Verification

Owner63 methods and independent28 methods/41 leaf checks passed. Independent
review replayed the owner suite, verified six frozen files,11 native source pins
and the accepted numeric-parser revision, and checked24 arrival permutations,
duplicate/conflicting receipts, late cancellation settlement, failed charges,
unknown roles, separate cost views, finite309-digit values plus tiny amounts,
overflow and typed binary64 interpretation.

Actual failing-before cases included unknown role attributed to generator,
conflicting amounts at the same exact native version, source CAS regression,
and loss of unallocated paid evidence. Those were corrected and retested.
Test authoring errors and an observation caught before an independent failing
assertion are recorded separately from implementation RED evidence.

| Private logical artifact | SHA256 |
|---|---|
| reconcile.py | 17ea8b88250d4880abf6c95f4629e1b62c4d833f4fe02112b379867f1aa4e968 |
| test_reconcile.py | 202baeacef8b01af8202c877e5d910ad02dae9d06db880e0b29d8deecfacacdf |
| integrity.json | 09bef640a393a168f480078338b0bf73568d7c09c2b49eb9059371ae5ec8c2a3 |

The raw research files remain private. No native provider_usage/routing_calls,
database, policy, model call, product build or deployment was changed or run.
Actual product reconciliation and authenticated runtime completeness remain
unaccepted. The inspected source base and later8c14279d observation are recorded
separately; the accepted crew helper changed while native cost code stayed equal.

## 한국어 작업 기록

작업 ID: RTE-02 (#155)
변경 이유: 실패·미확인·누락·중복 비용을 지우지 않고 실제 근거별로 연구용 대사를 수행한다.
실행한 검증: 소유63·독립28methods/41leaf,6파일 SHA·11소스 핀·24순열·수치/이력/미귀속 근거 보존 경계 통과.
미실행: 실제 장부·DB·제공자·제품 연결·인증·성능·빌드·배포.
현재 한계: 제공된 자료의 대사이며 runtime 전체 누락 없음이나 실제 청구 완료를 증명하지 않는다.
커밋 상태: 비공개 원문 대신 변경 이유·검증 범위·해시·진행 기록만 후속 단계 PR에 포함한다.
