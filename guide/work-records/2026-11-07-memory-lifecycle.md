# Memory lifecycle recovery and event identity

Task: MEMORY-AUDIT-M01-M02-M08-M11
Research: none

## Why / 이유

Approved temporary rules could keep reaching context and reflection after their original memory expired or was replaced. Identical event sentences with different dates shared both claim and generated-source identities. Immediate embedding unavailability skipped the existing deferred merge gate, and an approved explicit entity/relation could lose its best-effort graph update without durable recovery.

임시 규칙의 원본 만료·대체를 적용 시점에 확인하고, 날짜가 다른 같은 문장의 사건을 별도 보존한다. 승인된 명시적 엔티티·관계는 기존 작업 큐로 복구하며, 즉시 임베딩 장애도 기존 제한된 재검사를 사용한다.

## Changes / 변경

- Automatically approved directives bind original scope, rule kind and proposal revision. Reads check the original memory and its source validity; expiry, replacement, quarantine, erased content, changed rule/text and invalid revision stop application. Manual directives retain their behavior.
- List/Get expose optional `source_state`: `active`, `inactive`, `unavailable`, `needs_review`. Linked legacy rows without source binding become inactive and remain visible as `needs_review`; they are not deleted and their original scope is never guessed. A human saving the directive establishes an independent manual rule while preserving immutable proposal provenance. This explicitly ends future source-lifecycle coupling for that manually reviewed rule.
- Episodic occurrence intervals bind both the versioned claim key and generated source text metadata. Source excerpts still bound the original claim text. Same-date retries and matching legacy pointers deduplicate; different dates remain independent.
- Approval/activation/restore enqueue `approved_entities` in the same transaction as the active explicit memory. Existing WorkOnce claim, lease, retry limits, restore publication gate and privacy fairness remain in use. Deterministic extraction and relation resolution are reused; exact source/revision fences prevent stale publication. Replays retain the original approval/restore actor to avoid changing graph authorship.
- Worker permission requirements remain explicit: existing job permissions plus read:memories and write:entities, with a service role. Permission failures do not silently complete recovery; shared bounded retries expose them. No authorization was widened.
- Immediately unavailable merge checks use the existing capture_merge_gate queue. Denied/disabled/unsafe transfers retain their existing gates; no memory is merged without review.

## Verified / 검증

- Original code service regressions failed for original-rule expiry/replacement, distinct event dates, and immediate unavailable embedding recovery. An intermediate fix also exposed generated-source deduplication, which was corrected.
- An overlay of original production code failed the new approved relation recovery test with `approval did not durably queue recovery`; the final implementation passes outage→retry→actual graph retrieval and superseded-source rejection.
- Scoped memory tests passed for capture, extraction, directives, entities, entity extraction, restore, temporal-window recall and actual PostgreSQL evidence lifecycle (2.780s). Temporary disposable PostgreSQL verifies atomic approval/outbox persistence, real ClaimJob dispatch, durable retry, successful completion, author-preserving replay, one relation, and source deletion protection.
- Further focused tests cover legacy rule review, same-text always/never changes, quarantined/removed sources, future proposal revisions, context exclusion, dated event recall and dated legacy pointer compatibility. The final focused run passed (0.415s), including the real database test.
- Each command used a 6 GiB systemd scope and 900-second timeout, limited to internal/memory with a related test-name selection. Tests use local doubles and a disposable loopback database; no live model calls occurred.

## Limits / 한계

Legacy automatic rules require explicit review and stop applying until then. API state is added here; companion UI/OpenAPI/SDK integration is coordinated separately. Source validity is checked within a read transaction, but an already assembled context or a model call cannot be revoked by a subsequent change; this is not end-to-end snapshot isolation.

The recovery outbox covers explicitly supplied Entities/EntityRelations, not all free-form inferred names, model summaries, or historical approvals predating this change. A superseding revision invalidates an old queued job. Disabled entity types and unresolved endpoints follow existing extraction policies. Conflicting graph assertions are not overwritten and may exhaust retry attempts. The worker must have the documented permissions. Read-time inactive directives still consume the persisted active-row cap until reviewed or removed.

No product build, all-package suite, operating environment access, live model quality evaluation, push or publication was performed. Additional lifecycle audit candidates are reported separately; they were not silently added to implementation scope.

## Follow-up: entity transaction lock order

The relation publication transaction now acquires the scope entity advisory lock before fencing memory/source rows, matching applyEntityExtraction. The previous reversed order could form a wait cycle between extraction and outbox publication; existing database deadlock handling is not a substitute for a consistent order. Only this ordering changes: evidence, authorization, actor provenance and shared job retry checks remain intact.

Focused tests cover real database outbox claim/retry/replay/source deletion and service recovery. This verifies the unchanged functional contract after the ordering fix; it is not a claim that every concurrent interleaving was stress-tested or that a deadlock was reproduced at runtime. The follow-up commit records the actual test result. The task IDs above follow the integrated audit ledger: M01 rules, M02 dated events, M08 approval recovery, M11 transient retry.

## Follow-up: reindex and extraction compatibility

Independent review found three regressions introduced by the recovery/fencing changes. Actual disposable database tests reproduced them before correction: reindexing an approved explicit memory failed with revision_conflict for both pending and completed outboxes; entity dry-run advanced both memory and source fence counters from 0 to 2; one expired/future memory caused a mixed extraction batch to fail with revision_conflict.

Reindex now calls the embedding-only queue helper and preserves existing approval jobs exactly. Entity preview validates through the original transaction reader without persistent fence updates, while real extraction retains source/memory fences. ExtractEntities filters time-ineligible rows before forming the batch; a concurrent change to a selected row still fails its write fence rather than publishing stale evidence. Focused database tests verify new embedding jobs, unchanged pending/completed outboxes, no preview fence/graph writes, and successful extraction of the current memory from a mixed batch.

검토에서 발견한 재색인 충돌, 미리보기의 내부 fence 쓰기, 만료·미래 기억에 의한 전체 배치 중단을 실제 임시 DB에서 먼저 재현했다. 임베딩 재색인과 승인 부수 작업을 분리하고, 미리보기는 쓰기 없는 검증을 사용하며, 실제 추출 대상의 시간 유효성을 선별한다. 새 공개 API나 운영 권한은 추가하지 않았고 추가 감사 후보 5개는 구현하지 않았다.

## Follow-up: inventory read fixture boundary

The integrated suite's TestPostgresMemoryListChangedDuringPolicy compared the memory returned by activation with the row after a later inventory read. The response is formed before afterMemoryApproval runs deterministic extraction; its internal MemoryReferenceFence therefore predates completed approval-hook fence writes. That response was not a valid snapshot of the row immediately before ListMemories.

The fixture now reads the stored row after approval and immediately before listing, then compares the entire Record with the stored row after listing. This preserves and strengthens the original assertion that listing does not rewrite memory data, while retaining all concurrent body/source/deletion/quarantine rejection cases. The test logs only the three internal fence counters to distinguish completed creation work from read effects. No product code changed in this follow-up; the focused database result is recorded in its commit.

통합 검사 실패는 생성 후 후처리 이전에 만든 응답을 목록 조회 직전 DB 상태로 간주한 시험 기준 오류였다. 조회 직전·직후 실제 레코드 전체를 비교하도록 바꿔 목록 조회의 불변성 검증은 유지하고 강화했다. 제품 동작이나 권한은 변경하지 않았다.
