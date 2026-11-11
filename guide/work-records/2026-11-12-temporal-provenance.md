# Preserve source observation dates through retrieval

Task: MEMORY-QUALITY-90 / temporal evidence.
Research: none; reproduced product metadata propagation defect.

The answer prompt reads `Hit.CollectedAt` as `recorded_at`, but the common
`HitFromRecord` conversion discarded both an explicitly stored collection time
and a captured memory's `MemoryContext.ObservedAt`. The old prompt test constructed
a hit directly, so it missed the conversion gap. An occurrence interval could
survive while the observation date needed to interpret relative wording vanished.

The conversion now preserves an explicit nonzero collection time, falling back
to an explicit nonzero observation time, and copies it into the existing optional
hit field. Neither creation/ingestion time nor effective/event time substitutes
for an unknown source observation. No database migration, input schema, ordering,
permission, source fence, benchmark answer or grading threshold is changed.

The regression exercises stored record to retrieval hit to answer prompt using
an observation two years before ingestion. Another verifies an explicit
collection date's precedence and absence of an invented anchor. A service-level
fixture covers capture, approval and retrieval, retaining the host-stamped event
observation time. The unit regressions failed on the original conversion:
`recorded_at` was absent and the explicit collection date was lost.

## Verification

Initial targeted race selection passed three top-level tests (1.180 seconds).
Final combined source-time race checks passed 7 top-level tests and 2 subtests, with no failures or skips (1.346 seconds). The new capture fixture first failed validation because its required domain was missing; after adding that fixture field the same selection passed. That initial fixture failure is not counted as a pass. `git diff --check` passed. Evidence is retained in the private temporal-provenance before/after logs.

## Limits

This restores an existing known timestamp; it does not prove that imported
metadata itself identifies the original utterance date correctly. Unknown dates
remain unknown, and unstructured memories without observation metadata receive
no invented date. The tests use the actual conversion and capture/review service
with the in-memory fixture, not a live model or a new PostgreSQL evaluation.
No category accuracy improvement or 90% acceptance is claimed.

## 한국어 기록

검색 결과 변환에서 관찰 날짜가 사라져 답변 모델이 상대 날짜의 기준을 받지
못하던 문제를 재현하고 수정했다. 수집·관찰 날짜만 전달하며 저장 시각이나
사건 시각을 대신 넣지 않는다. 과거 평가 정답이나 채점 기준은 바꾸지 않았다.

## Source-date follow-up

The [source-date follow-up](2026-11-12-source-date-fallback.md) additionally
suppresses the observation-time fallback for non-original source locators, so a
file read time is not relabeled as its original utterance time. It supplies a
separate permission-checked and fenced path for missing dates from hash-bound
original messages. The initial three-test verification above predates that
follow-up; the new record reports its own final selection.
