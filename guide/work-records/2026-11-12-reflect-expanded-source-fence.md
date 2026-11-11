# Fence source expansion through answer delivery

Task: MEMORY-QUALITY-90. Research: none; privacy correctness repair.

A native reflect tool-loop regression reproduced a pre-existing gap: both chunk
and document expansion returned an answer after its source was erased during the
answer model call. Those synthetic source hits had no entry in the source snapshot
map checked before and after generation. The initial regression failed for both
depths; this is a confirmed defect, not a hypothetical risk.

Source expansion now admits a current source snapshot with scope, live/privacy,
source sensitivity, recorder filters, read authorization, dependency checks and
per-source transfer authorization. The stored reference's span/hash is validated
against that snapshot and the excerpt is reconstructed from it. Admitted snapshots
join the existing transaction and dependency fences before/after model use, so a
source change, erasure or revoked transfer prevents answer delivery. Ordinary
conversation passage and original-date eligibility remain unchanged.

Independent review also identified that later admissions could overwrite an
existing source snapshot while earlier prompt text remained. keepSources now
preserves the first snapshot and only upgrades transfer/full-text obligations.
A regression updates source content, approves a new memory citing that version,
then expands its document while retaining the earlier passage; delivery must reject the mixed
versions rather than silently bind old text to the new source.

The source-level transfer gate checks the whole redacted source even for chunk
expansion. This is conservative: a harmless excerpt may be withheld when other
source content prevents transfer. The change adds no shell tools or network
executors, and does not broaden the tool iteration or model-call budget.

## Verification

Before fix: both chunk/document native-loop erasure cases failed by returning an
answer. Initial fixed race regression passed (1.258s). Expanded regression checks
unchanged delivery, revoked transfer, changed hash, restricted source and recorder
filter exclusion. An initial expanded test fixture used a string instead of the
CreatedBy slice and did not compile; it was corrected, not counted as passing.
Final PostgreSQL-backed race selection passed (2.854s), including both native
chunk/document deletion regressions and existing passage/date source fences.
The disposable setup initially failed its DSN-name guard and then its schema
initialization under the locale-default encoding; reinitializing only that owned
cluster explicitly as UTF-8 resolved setup. These failures are retained separately.
No evaluation dataset or production database was changed. Complexity and diff
checks passed with no added complexity debt. The PostgreSQL result predates the
final first-snapshot guard; its focused follow-up race result is recorded separately.
The first follow-up fixture attempted an immutable memory-source replacement and
was correctly rejected. It was revised to approve a new memory for the new source
version, preserving the ordinary provenance constraints. A source factory also
creates a self-reference bound to its original content, so mutating that fixture
correctly failed its dependency gate. The final mixed-version scenario uses a
standalone imported source without a self-reference; no production gate was relaxed.

## 한국어

변경 이유: 원문 확장 도구가 읽은 자료를 답변 전달 시점에 다시 검증하지 않아,
모델 호출 중 삭제된 자료에 근거한 답변이 전달되는 기존 문제를 재현했다.
수정: 실제로 읽은 원문의 해시와 범위를 검증하고 기존 권한·삭제·변경 검증에
연결했다. 원문 일부만 읽어도 전체 원문의 전송 검사가 적용되는 보수적 한계가 있다.

Final first-snapshot follow-up: focused race selection passed (1.849s), 13 top-level tests and 24 subtests, zero failures/skips. This follow-up used the in-memory repository; the earlier PostgreSQL fence results are reported separately.
