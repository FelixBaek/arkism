# Imported conversation reread compatibility

## Why

Task: open-domain retrieval quality investigation. The optional conversation rereader only accepted query matches whose source reference began with the capture prefix. Retrieved memory session tags also led only to capture-event siblings, leaving imported conversation batches unreachable. Accept nonempty query-matched references and exact session-tag values while retaining the existing capture path. Priority is retrieved event batches, query-matched references, exact imported session references, then unranked sibling events.

All candidates still pass per-source read, request-filter, transfer and delivery checks. Whole-batch handling means eligible text is not arbitrarily shortened; sources denied by those gates still remain excluded. Previously admitted snapshots remain binding. Empty references cannot trigger an unfiltered scan. A batch exceeding the 100-row lookup limit or mixing source origins is skipped with a degraded note; the existing whole-batch rune budget is preserved. Default rereading remains off and no model-call allowance is added.

## Verified

Before implementation, `go test ./internal/memory -run '^TestReflectRereadImportedSourceRefs$' -count=1` failed both query and session cases at the intended missing-batch assertion. After implementation, `go test -race ./internal/memory -run '^TestReflect' -count=1` passed (5.191s). Its first sandboxed attempt stopped because an existing localhost HTTP test could not bind; the permitted local-test rerun passed. After the final empty-reference guard and real-loop test, `go test -race ./internal/memory -run '^TestReflectReread' -count=1` passed (2.328s). Cognitive complexity checks on changed files passed at the limit of 20.

A separate red regression confirmed that ten broad imported sessions could crowd query-matched evidence out of the ten-batch budget. Query-matched references now precede those session fallbacks. After this final ordering change, the same `TestReflectReread` race command passed (2.293s), and cognitive complexity remained within 20.

Synthetic coverage includes read/transfer denial, recorder and scope filters, exact source references, late erasure, first-snapshot rebinding prevention, complete multiline batches, oversized/mixed-origin batches, empty references and actual reread-to-answer delivery. All model responses are local fakes. No benchmark answer or source transcript is included here.

Limits: first-stage query ranking remains bounded; unknown imported session references may simply find no batch. Ambiguous origins and batches larger than the existing limits are omitted rather than partially presented. This compatibility fix alone establishes no measured accuracy gain.

## 제목

가져온 대화 원문의 선택적 재독 연결 복구

## 이유

캡처 전용 접두사에 의존하던 연결을 확장해 검색된 원문 참조와 기억의 세션 태그가 가리키는 원문 배치를 읽는다. 범위·권한·전송·최초 스냅샷 검증은 그대로 적용하며 기본 비활성 설정을 유지한다.

## 검증

수정 전 두 경로의 실패를 확인했고 수정 후 Reflect 전체 race 회귀가 통과했다. 추가 합성 테스트로 필터·삭제·배치 한도·빈 참조를 검증한다. 실제 평가 점수 향상은 아직 검증하지 않았고 외부 모델 호출은 하지 않았다.

## Actual-store diagnosis and SQL validation

The preserved evaluation store had 4,519 memories carrying event/session tags,
and 1,359 conversation sources grouped into 196 imported batches. None of those
source references had the capture-specific prefix. Their batches contained
3–15 rows, so the existing 100-row bound was not the cause. Against the isolated
baseline API, three question pairs with rereading off/on produced identical
prompts and zero rereader attempts; an authenticated raw-source read succeeded.
The comparison used a failing local model trap, with no external model calls.

Real PostgreSQL regressions passed under race instrumentation (2.905 seconds):
complete authorized imported-session text, exact scope and transfer exclusions,
lookahead detection for a 101-row batch, and erasure after rereading that prevents
answer generation. Each test used a disposable child database; the evaluation
store was not modified. Linux, Windows and Darwin vet passed, and pinned full
Go lint reported zero issues again with the final PostgreSQL test file included.

A source audit of all 23 previously judged wrong open-domain answers found at
least six clear evidence omissions. Other cases require distinctions between
plausible alternatives, insufficiently supported labels and source/label
inconsistencies. No labels were removed or altered, and no rule forcing personal
attributes or diagnoses into answers was added. Actual accuracy is being measured
separately on the same frozen 62 questions with the same grader.
