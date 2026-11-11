# Query evidence before unranked session expansion

Task: MEMORY-QUALITY-90, multi-evidence retrieval.
Research: none; reproducible bounded-source selection defect.

The reread stage follows the source batches of retrieved memories, expands their
sessions, and also searches conversation text for the question. Previously it
appended every unranked session sibling before the directly query-matched batches.
With a ten-readable-batch limit, a long session could consume every slot with
unrelated text and exclude evidence for another part of the question.

The new regression uses a synthetic retrieved memory, ten unrelated session
batches, and a separate conversation containing the requested shuttle detail.
Before correction, the actual reread transcript builder omitted that detail and
the regression failed. The implementation now orders retrieved-memory source
batches first, directly query-matched batches second, and unranked session
expansion last. Deduplication, lookup and text budgets, source authorization,
transfer decisions, erasure fences and the verbatim output check are unchanged.
The regression also refuses the matching source's transfer and verifies that its
text is then excluded despite its higher selection priority.

## Verification

Pre-fix: `TestReflectRereadQueryMatchPrecedesSessionExpansion` failed in 0.04s
with “direct query evidence displaced by unrelated session batches”. The fixture
uses synthetic conversations and the real service transcript-building path.
The first race attempt could not compile while a parallel temporal change was
between its caller edit and helper creation; it is not recorded as a test pass.

The final focused race run passed all ten top-level tests in 2.156s, with zero
failures or skips. It covers the new omission and transfer-refusal regression,
existing session expansion, empty-source fallback, verbatim extraction,
model failure, deadlines, prompt-only and per-operation call budgets.
`git diff --check` passed.

```sh
go test -race ./internal/memory -run '^(TestReflectReread.*|TestReflectPromptOnlyCarriesTheRereadLines|TestVerbatimLines.*)$' -count=1 -timeout=180s -json
```

## Limits

This corrects one retrieval starvation mechanism. Retrieved-memory batches still
precede directly matched conversation batches, so an already-full set of memory
sources can consume the same fixed budget. It does not assert complete multi-hop
retrieval, real-model accuracy, or 90% achievement. No benchmark gold, scores,
query-specific aliases, time limits or batch caps were changed. No product build,
model call, production database modification or deployment was performed.

## 한국어

같은 대화의 관련 없는 원문들이 제한된 열 자리를 먼저 채워, 질문과 직접
맞는 원문이 빠지는 문제를 재현했다. 질문에 맞는 원문을 단순 대화 확장보다
먼저 읽도록 순서를 고쳤다. 권한·전송 거부와 기존 분량 제한은 유지한다.
이는 실제 모델의 90% 정확도 달성을 입증하는 결과가 아니다.
