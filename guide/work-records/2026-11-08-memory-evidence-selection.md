# Joint evidence selection / 기억·대화 근거 집합 선택

Task: MQ-R2.
Why: allow complementary evidence outside the existing output head to compete
within the unchanged final memory and conversation budgets.
Research: local retrieval architecture review, adapted into an opt-in service experiment.
Design: select authorized candidate numbers jointly, validate the complete set,
and retain a materialized baseline on every unsuccessful selection.
Relation: adapted.
Deviation: this is a narrow service integration, not a reproduction of a research
model or an assertion of a particular retrieval-quality improvement.

## Behavior / 구현

`recall_quality.evidence_selection` is false by default. With `candidate_pool: 100`,
ordinary active memory recall can retain the fused memory candidates before its
final cut and the authorized conversation excerpts before their five-entry cut.
The configured server model sees only the question, numbered candidate text and
summaries, and output budgets. No benchmark answers, labels, oracle choices,
model-written facts or replacement summaries enter the product path.

The option requires conversation capture, source inclusion and a final memory
limit of 1–10. Cross-encoder/model reranking, prune/dedup, active native ranking,
history, relationships, frozen context, non-memory collections, whole-item mode,
conversation-context mode, preferred observations, source-fact output and token
budgets retain their existing behavior. `rerank: false` overrides this option.
These paths make zero calls to the new selector; independently configured legacy
features retain their own existing behavior.

The query and every candidate must pass authorization and model-transfer checks.
Any denied/private/restricted candidate keeps the full baseline; the model input
is not silently reduced to avoid the denial. This first version conservatively
falls back for cross-scope shared/reach candidates. A local endpoint is not a
privacy exception. The existing model-call ledger enforces a separate project cap
of 200 attempts per day. The child context lasts at most ten seconds, the complete
serialized prompt plus instruction is limited to 64,000 Unicode characters, and
the output limit is 512 tokens. No candidate is silently shortened to fit input.

Only strict JSON arrays of existing candidate numbers are accepted. Duplicate
keys/numbers, unknown fields, missing/null arrays, invalid numbers, trailing
output and excess choices are rejected. Selected memory text plus summary must
fit `max_chars`; conversation text plus summary and ordinary adjacent completion
must fit 600 characters times the selected count, up to five entries. Failure
retains the baseline, rather than clipping an over-budget set and calling it
successful. Scores, lane provenance, source snapshots, provisional results and
correction notices retain their existing meaning. Empty memory selections are
serialized as `items: []`, including through the real search handler.

Memory final fences remain in place. The eligible selector path additionally
retains selected and adjacent source snapshots and verifies source dependencies
at the final transaction, even without conversation-context output. A source or
memory change during the model call can therefore reject the request; model
fallback does not override a normal final-fence error. Dependencies are checked
only when present: original conversation sources need not reference another source.

Existing audit notes retain at most 64 transmitted memory IDs and one source
document ID, together with the existing prompt hash and usage trace. This is
**not complete lineage for all 200 candidates**. Existing optional trace-content
and retention policies remain unchanged. Full experimental candidate evidence
must be collected separately in an authorized private evaluation.

## Verification / 검증

The final focused race run passed 42 top-level tests and 42 subtests, with zero
failures/skips, in 6.327 seconds. It covers 100+100 input preservation and selection
beyond both old output cuts; disabled/unsupported paths; strict response decoding;
all-candidate transfer and private-query refusal; daily-cap/timeout/provider
fallback; whole text/summary budgets; minimum-score and lane preservation; prompt
overflow; source-only HTTP arrays; bounded audit links; and memory/source/neighbor
and source-dependency revocation. Scoped vet passed.

The additional source-erasure/privacy/transfer regression passed 18 top-level tests and 37 subtests,
with zero failures/skips, in 2.336 seconds. It checks existing behavior after the
metadata-test-double correction. API/SDK unit checks passed 12 tests in 1.348
seconds. The generated contracts match 102 management operations and their DTOs;
OpenAPI structural validation covers 435 operations. These are structural counts,
not 435 live HTTP requests. Generated SDK and diff checks passed.

The unit service cases use constructed fixtures and fake model responses. Their
HTTP case exercises the real search handler through an in-process recorder.
A separate acceptance run subsequently exercised five real HTTP requests against
an isolated PostgreSQL database with bearer authorization and forced RLS. All five
passed under the race detector (test time 3.35 seconds; including compilation
48.08 seconds): disabled behavior returned ten memories and five conversations;
the synthetic selector chose one memory and one conversation beyond the old
output heads; an invalid number retained the exact baseline; a private candidate
prevented an additional provider call; and erasing a source dependency during
selection returned exactly HTTP 403 with `access_denied` and no result.

This acceptance used an in-process synthetic provider, not semantic inference.
It made three synthetic calls and recorded three usage-ledger rows, with zero
embedding calls, actual model inference or external provider connections. Five
response hashes and all 973 source-file hashes were independently rechecked.
Including unsuccessful fixture/setup attempts, eight HTTP requests and four
synthetic calls were preserved separately; unsuccessful attempts are not passes.
The temporary child database, role and connections were removed. The executed
private-overlay command was:

```sh
go test -p 2 -race -v -overlay "$MQ_R2_PRIVATE_OVERLAY" -run '^TestPrivateEvidenceSelectionPostgresHTTP$' -count=1 -timeout=5m ./internal/memory
```

The overlay, full command environment, response bodies, failed attempts and
source hashes remain in private diagnostics; no private fixtures are added here.

After the separately documented [read-fence timestamp repair](2026-11-08-fence-timestamp-stability.md),
the identical synthetic overlay was run against the integrated tree again. All
five HTTP cases passed under the race detector (3.70 seconds; including compilation
45.36 seconds), with the same four200 responses and final403/access_denied. Five
response hashes and all974 source-file hashes were checked. This second run used
three synthetic provider calls, zero embedding calls and zero real inference.
The earlier successful run and unsuccessful setup attempts remain separate.
After all database checks finished, zero remaining child databases, test roles
and other client connections were verified. The owned temporary PostgreSQL
instance was stopped and its938,285,647-byte data copy removed; original inputs,
logs and diagnostic artifacts remain. The clean, integrated selector worktree
was removed after its source hashes had been retained.

한국어: 별도 실제 PostgreSQL·HTTP·권한 검사 5개가 race 검사와 함께 통과했다.
기존 동작, 후보 밖 선택, 잘못된 응답 복귀, 비공개 후보 차단, 선택 도중 원문
삭제 차단을 확인했다. 합성 모델을 사용했으므로 검색 정확도 검증은 아니다.

Executed commands used the already installed matching Go toolchain on PATH,
with `GOTOOLCHAIN=local`, `GOPROXY=off` and `GOMAXPROCS=2`. No tool was downloaded.
The race and contract-unit commands were wrapped in
`systemd-run --user --scope -q -p MemoryMax=6G timeout 900`.

```sh
go test -race ./internal/memory -run '^(TestEvidenceSelection|TestConversationSelection|TestConversationLane|TestConversationNeighbor|TestConversationMiddle|TestLocalQuality|TestLocalCandidatePool|TestRecallCandidateWidth)' -count=1 -timeout=850s -json
go test -race ./internal/memory -run '^(TestSourceMemoryErasure.*|TestSourceErasure.*|TestSourceRecordErasureGuards|TestSearchExcludesStaleIndexRowsAndDeletedSources|TestRunSourceFenceAtomic|TestConflictCannotCopyInvalidSource|TestExternalContextSensitivity|TestTransferPolicyReceivesDestinationAndClassification|TestRestrictedSourceCannotBeExportedViaDerivedRecord|TestExternalContextBatchesDuplicateSources|TestModelInputTransitiveArchiveAndSourceDeletion)$' -count=1 -timeout=850s -json
go vet ./internal/memory
python3 tools/update_api_contracts.py
python3 tools/generate_sdk.py
PYTHONDONTWRITEBYTECODE=1 TSC_BIN="$CACHED_TSC_BIN" python3 -m unittest tools/test_update_api_contracts.py tools/test_generate_sdk.py
python3 tools/update_api_contracts.py --check
python3 tools/check_openapi.py
python3 tools/generate_sdk.py --check
git diff --check
```

`CACHED_TSC_BIN` was the already installed TypeScript 5.9.3 compiler. This verifies
the generated contracts with that compiler; it does not claim execution under a
newly installed compiler version. The contract tests emitted an existing
ResourceWarning without a failure.

Initial failures are retained in local diagnostic logs. One transfer-denial test
initially denied configuration reads as well as the intended candidate transfer;
a no-model unit fixture omitted the required nonempty query. Both test fixtures
were corrected. A later real implementation failure showed that checking source
dependencies unconditionally rejects original conversations with no parent
source; the check was narrowed to existing dependencies, with a model-time
source-dependency deletion regression added. That new fixture also initially reused
a source reference and attempted to alter immutable source links; it now creates
an independent linked source correctly. Its execution exposed a pre-existing fake
metadata reader that discarded `ContentErased`, unlike the production payload
reader. The fake preserves that field now, retaining the original erasure assertion.
Failed runs are not counted as passes.

## Limits / 남은 한계

No actual model inference, external provider request, production build, deployment
or publication was performed. This change is an experimental
selector integration, **not evidence that any category reaches 90%**. Fixed-input
quality comparison and regression acceptance remain required before adoption.
The input cap counts characters, not a provider tokenizer's exact context window;
a provider context refusal retains the baseline. Broad/private candidate sets may
legitimately refuse selection. The synthetic database acceptance does not establish real-model latency or
representative authorization-read performance. No UI was changed.

한국어: 최종 10개 기억·5개 대화와 기존 문자 예산을 유지하면서, 허용된 전체
후보에서 보완적인 근거를 고르는 기본 비활성 실험 옵션이다. 모델이 쓴 사실을
저장하거나 응답하지 않는다. 거부·시간 초과·예산 초과·잘못된 번호는 기존
결과로 복귀하며, 원문 변경에 대한 최종 검사는 계속 요청을 거부할 수 있다.
실제 모델 품질 및 분야별 90% 달성 검증은 아직 수행하지 않았다.

Review: the final specification/code read and independent standards/security
review reported no remaining blocking findings. The source-count budget, empty
HTTP array and bounded audit-link findings were resolved before the passing run.
The local commit's work-record format check passed with the full base revision:

```sh
python3 tools/check_work_record.py --range 0f4dc305e410553e49067d0e379eb99d469c9b9b..HEAD
```
