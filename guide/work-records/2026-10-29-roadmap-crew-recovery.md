# Preserve effect identity across interrupted recovery

Task: CRW-04 (#154)
Research: E engineering validation; no effectiveness or novelty result
Design: Reuse native run/node recovery, scoped human effect approval, durable claim/receipt and lease/cancellation guards. Add an advisory typed contract and isolated synthetic fault fixtures.
Relation: adapted
Deviation: Adapter capabilities describe the private local fixture only. No generic provider reconciliation, new approval API, server schema or replacement checkpoint engine is introduced.

The recovery contract binds the exact scope, intent run snapshot, node attempt,
intent/effect/adapter, target version, checked premise version and arguments
digest. Approval must match that whole identity. Current-use context inputs
are re-evaluated in enforcement mode; observe/off diagnostics cannot grant
recovery. Missing context differs from a known empty list.

Decisions are advisory data_only_untrusted: reuse, reconcile, retry the same
effect, unknown or block. They cannot authorize claims, dispatch, settlement,
node completion or a canonical ledger rewrite. Native claimed/unknown effects
retain the existing no-redispatch rule. A succeeded label without an exact bound
committed receipt is insufficient even for advisory reuse. Existing read-node
recovery remains independent from an unprepared effect's advisory block.

Nineteen private deterministic schedules execute the test binary as a child,
write separate world and driver checkpoints, exit at an injected barrier and
restore them for recovery. The outside-world ledger survives a restored older
driver checkpoint. Supported lookup/idempotency paths preserve one commit after
response loss; unsupported paths retain unknown. Deleted/denied source cases
actually attempt and fail the old approval's claim guard. Lease transfer and
cancellation attempt and refuse late node saves. Cancellation precedes delivery
of the bound receipt; historical paid-effect evidence remains while the run
stays cancelled. Native interrupted reads retry; general writes do not repeat.

The target-only negative control checks the original premise, changes only the
policy version, then commits a stale-premise effect. A locally atomic target
and premise comparison blocks the same race. That counterexample is reported
as an unsafe result, not successful business recovery. A point-in-time check
alone does not close the remote check/use window.

Verification:

- Public module: author normal/race119leaf each; independent race80leaf.
- Existing native regressions:24methods/29leaf under race; no engine changes.
- Private adapter: root race10leaf and independent normal/race9leaf each,
  including concurrent same-key calls, durable restoration and capability binding.
- Fault runner: normal/race27 reported leaf each, including one parent harness
  entrypoint;26 meaningful parent checks and19 actual fault schedules. Both
  exports and a root replay are byte-identical. Root compared physical saved
  world/checkpoint state with exported ledger/events/status for all19 cases.
- Independent evaluator:21methods/46leaf, all19 actual rows passed.18 retain
  their expected safe boundary; one reproduces the unsafe target-only control.
  These are not model success rates. Late lease/cancel tests also forbid any
  later dispatch/commit and require the actual refusal event order.
- Final check_local: Python412, Go complexity/vet/full race, UI532 passed.
  Pinned golangci-lint v2.14.0 returned0issues. Private overlay vet passed;
  equivalent source snapshot lint returned0issues where lint cannot read Go
  virtual overlay files. Those tool setup errors are not functional defects.
- Eleven final public/private source artifacts have byte snapshots and hashes.
  Native context, engine, approval and both OI-25 guards remain unchanged.

Actual negative tests exposed missing receipt evidence, adapter capability
binding, event sequence binding and driver receipt/approval/cancel handling;
corrections were replayed. Initial evaluator cross-check failures also exposed
missing output protocol definitions and incorrect oracle assumptions. Those
are recorded separately from functional defects. Their failure diagnostics and
original hashes remain, but exact pre-correction evaluator/protocol source
bytes were not archived, so that initial assessment cannot be fully replayed.
The corrected sources are archived and the final evaluation is reproducible.

Independent whole-card review confirmed all five original criteria within the
local fake/fault scope with no unresolved concrete findings. CRW-04 is locally
accepted; the roadmap count is14/63. This does not accept live execution or
research effectiveness.

The recovery check does not establish a new recovery theory. Fixture target/premise versions
are distinct from product run, source and summary revisions.

Limits: inputs are caller-supplied evidence, not authentication or proof of
human presence. Local idempotency assumes one world owner and sequential
process restoration; the mutex/file snapshots do not establish shared-world
multiprocess atomicity. Native lease/CAS tests are separate evidence. No
provider-level atomic commitment, real provider/model, operational database,
payment, mail, product build, release, deployment, whole rollback or R8
standalone gate ran.

## 한국어 작업 기록

작업 ID: CRW-04 (#154)
변경 이유: 기존 복구·승인 경계를 재사용하고, 중단 후 동일 효과와 현재 사용 가능 근거를 구분하는 시험 계약을 만든다.
실행한 검증: 모듈 소유일반/race119leaf·독립race80leaf, 기존native24methods/29leaf, adapter소유race10leaf·독립일반/race9leaf, 실제19중단사례일반/race·root재실행동일, 독립판정21methods/46leaf와19행 통과. 최종Python412/Go복잡도·vet·전체race/UI532·고정lint0issues 통과.11최종소스바이트 보관.
현재 상태: 원본5완료 기준의 독립 검토와 최종 핀 대조를 마쳐 로컬 fake/fault 범위에서 카드 수용. 로컬 완료14/63.
한계: 초기 판정 규약 원본 바이트 미보관으로 그 최초 판정 재실행은 불가하며 SHA·실패기록만 보존했다. 최종 교정본은 보관·재검증했다. 단일 소유 local world·순차 복원 한정이며 실제 provider 원자성·인증·실제 모델/운영DB/효과·제품 빌드·배포·R8 standalone은 미검증이다.
