# Native crew contract and evidence boundaries

Task: CRW-01 (#154)
Research: E development foundation; no empirical result or independent-paper claim
Design: v8 CRW-01 native-engine contract, additive research sidecars and single-writer ownership
Relation: faithful
Deviation: Private lab contracts remain outside the public tree. This record exposes non-sensitive summaries and immutable hashes; reproducing the private contract audit requires the operator's dispatch binding.

The current Go engine already supports agent, team and workflow execution.
CRW-01 maps those paths and the separate workflow-run, team-meeting and
work-checkpoint state machines to observed fields, proposed additions and
explicit unknowns. Product source is unchanged. Existing FLOW lease, attempt,
unknown-write behavior, meeting snapshot/budget/stop and checkpoint idempotency
are reused. A final node trace with a64-event cap is not treated as a complete
per-attempt ledger. Existing token/price estimates remain estimates.

The private machine-readable contract and owner/delta records distinguish
workflow revision, approval-time run CAS and later jobs effect revision.
Human-only effect approval, exact run/effect evidence, data-only input and
OI-25 host/server exclusions remain invariant. A team decision or completed
checkpoint cannot become external-effect authority or an independent memory
outcome. The service's existing meeting-budget permission remains distinct.
Additive observation/evaluation sidecars are explicitly proposed; no database
migration or full production observability is inferred.

Private logical artifacts and SHA256:

| Private artifact under logical crew_lab/contracts | SHA256 |
|---|---|
| crew_contract_v1.json | 0bc550da0679271e216f3c718658d91491df1d8e92612d0cd866ab6068fa6d82 |
| ownership.json | 1d7657e783b45d31538981ff4990484148f08909c8ecb6cc97aab9a65cb0301b |
| current_delta.md | 87cc99afb5731e1065b06e8c234f5556e939511dcacd475ca7f0cf95b93123bc |

## Local verification

The owner audit ran29 static tests. Independent review checked six contract
groups, rejected12 document mutations, accepted seven effect-evidence
boundaries and rejected25 invalid cases. It verified53 source fingerprints,
55 exact line anchors, the three-file hash chain and downstream ownership.
These are contract-consistency checks, not executions of the product engine.

Failing-before probes exposed service effect approval and missing/mismatched
target evidence being accepted. The contract/audit were corrected and retested
while preserving historical run CAS and later effect revision distinctions.
Current source was pinned to07ed21d8eca16cb93d0f9111c683ed751325df02; native
crew evidence also matched the reviewed origin/main snapshot.

Public progress and ownership now record CRW-01 as locally verified. The
original baseline contract remains unchanged. Public-checkout check_local finished with exit0: Python412, Go
complexity/vet/full race and UI532 passed before this documentation commit.
R8's standalone build/acceptance gate remains unexecuted pending authorization.
No product source, model/API/effect call, product build, release or deployment
was performed by this card. New algorithm effectiveness and study readiness
remain unverified.

## 한국어 작업 기록

작업 ID: CRW-01 (#154)
변경 이유: 기존 자체 엔진을 재사용하고 승인·revision·관측의 사실과 제안·미확인을 구분해 후속 구현 계약을 잠근다.
실행한 검증: 정적29, 독립6묶음·문서변조12거부·효과양성7/음성25, 소스53·행55·3파일hashchain 확인. 공개체크아웃 check_local 종료0(Python412/Go 복잡도·vet·race/UI532).
미실행: 제품 Go 실행 검사, 실제 모델/API/효과 호출, 빌드·배포·본실험.
커밋 상태: 비공개 원문은 외부 lab에 유지하고 공개 요약·근거·진행 기록만 같은 단계 PR로 관리한다.
