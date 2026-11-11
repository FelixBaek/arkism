# Recover missing observation anchors from verified original message sources

Task: MEMORY-QUALITY-90 / temporal provenance follow-up.
Research: none; missing metadata evidence and source-bound retrieval repair.

Some approved memories have neither a collection nor an observation timestamp,
while their original message source retains its historical collection date.
Passing such a memory to reflect omitted the date needed to interpret relative
wording. This follow-up only fills missing dates from explicitly referenced
message/conversation sources; it never uses ingestion time or scans unrelated
records. A nonempty source span with the current exact content hash is required,
as is the `original` locator. All referenced sources must be admissible and have
the same known date. More than ten references, disagreement, a missing timestamp,
changed content, file locator, or missing hash leaves the anchor unknown.

The shared source gate retains scope, request filters, live/privacy checks,
read permission, source-dependency validation and per-source transfer approval.
Only the timestamp is added to the prompt; the source text is not copied into
the prompt or transfer payload for this metadata-only path. Selected snapshots
are verified and fenced before generation, after generation, and at prompt-only
delivery. Full-record comparisons catch timestamp changes even without a
revision change; later source erasure or transfer revocation rejects delivery.
Original passage/reread admission remains limited to conversation sources.

A separate correction avoids interpreting a file's read time as its original
utterance time: `MemoryContext.ObservedAt` is not promoted when a source reference
uses a non-original locator. Explicit collection metadata remains unchanged.

## Verification

The regression covers allowed dates, file and unbound references, ambiguous
source dates, missing sources, restricted data, recorder filters, transfer
refusal and span mismatch. Actual PostgreSQL checks cover erasure, changed
dates, transfer revocation and erasure during a synthetic model call. The latter
checks the admitted timestamp reaches the prompt and no answer is delivered
once the source is erased. Existing passage safety tests run alongside them.
Final race selection passed 9 top-level tests and 18 subtests with zero failures or skips (2.075 seconds). The repository complexity gate and `git diff --check` passed. Early test-fixture attempts were corrected: the shared source factory replaced requested dates, and the normal repository correctly refused a provenance-date update. The final date-change guard case uses explicit test-only SQL in its disposable database to simulate an out-of-band repair at the same revision. Initial failing attempts are preserved separately and are not counted as passes.

## Limits

The source's recorded collection date must itself be trustworthy as an
observation time; this change does not infer a document's authoring date or fix
bad imported timestamps. There is no live-model answer-quality score in these
regressions. Permission and generation doubles accompany the real SQL repository;
these checks do not claim production RLS or provider behavior. The optional
fallback adds bounded source reads and conservative source fences. It changes no
benchmark gold, answer labels, retrieval score threshold or operational database.

## 한국어 기록

기억 자체의 날짜가 없더라도 해시로 연결된 원본 메시지의 수집 날짜가 같고
권한 검사를 통과하면 답변에 날짜 기준을 전달한다. 파일을 나중에 읽은 시각은
원문 날짜로 사용하지 않는다. 원문 삭제·날짜 변경·전송 권한 회수 뒤에는
답변을 내보내지 않으며, 실제 정확도 점수 상승으로 간주하지 않는다.
