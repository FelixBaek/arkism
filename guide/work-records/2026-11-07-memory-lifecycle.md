# Memory lifecycle recovery and event identity

Task: MEMORY-AUDIT-M01-M03-M08-M10
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
