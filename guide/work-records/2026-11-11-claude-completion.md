# Conversation passage completion / 원문 검색 인계 작업

Task: P33-CHUNKS, PR551-REVIEW, P33-ACCEPTANCE-AUDIT.
Research: none; implementation review and audit of existing local experiments.
Why: finish an interrupted passage-search implementation without treating an
unreported evaluation or an unsafe data path as accepted product behavior.

## Implementation / 구현

The interrupted implementation was committed as `03e94757`, based on `4773569b`.
It adds question-matched conversation batches and their adjacent batches to
reflect's prompt, under a separate 40,000-character bound. The server setting
`ARKISM_REFLECT_PASSAGES` remains off by default, with range0–50. This is additional
evidence alongside the existing memories, not a replacement of the memory list.
Passage selection makes no generation call, but can make an additional metered
query-embedding call. This distinction corrects the earlier "no model call" wording.

The completion fixes three reproduced safety failures: recorder filters were
bypassed; a source-specific transfer refusal did not prevent transmission; and
erasing a source during answer generation did not prevent delivery. The shared
passage/reread admission now applies request filters, source-dependency checks and
per-source external-transfer authorization. Matched and adjacent records retain
snapshots. Transactional source/dependency fences run before model use, after it,
and before prompt-only delivery. A revocation rejects delivery; it is not treated
as an optional retrieval failure. Optional lookup timeouts retain the baseline.

Whole optional blocks are removed when the complete prompt exceeds its budget.
The previous prompt version and rules remain when no passages are present. The
new rules are tied to actual passage input. Configuration boundary tests cover
unset, zero, valid bounds, malformed values and overflow.

Three pending PR review findings are also addressed: unrelated Latin prefixes
such as java/javascript remain distinct search concepts; dream reports sort by
creation time before pagination, so undo does not reorder history; and open
maintenance proposals arriving during a dream run count as new work. Existing
plural aliases and Korean query expansion remain. This last fix does not claim
to remove every scheduler race between the activity check and final job update.

한국어: 중단된 원문 검색을 이어받아 필터·전송 권한·삭제 검증을 보완했다.
기본값은 계속 꺼짐이다. 추가 임베딩 비용은 있을 수 있다. 원문이 바뀌면
답변을 거부하며, 단순 조회 실패와 권한 회수를 구분한다. 검색어 합치기와
드림 목록·새 정리 요청 감지에 남았던 리뷰 지적도 수정했다.

## Recovered evaluation / 기존 평가 검산

The saved full run actually completed:1,085 unique prompts and answers, no missing
keys or prompt errors. Questions, categories and gold match the prior run. No new
model inference was performed to recover these results. These are historical
final-run observations from the pre-review code, not scores of the safety repair.

| Category | Prior correct | Passage run correct | Same-answer regrade |
| --- | ---: | ---: | ---: |
| Temporal |201/233|203/233 (87.12%)|206/233|
| Multi-hop |162/188|162/188 (86.17%)|161/188|
| Open-domain |45/62|45/62 (72.58%)|45/62|
| Single-hop |575/602|581/602 (96.51%)|581/602|
| Total |983/1085 (90.60%)|991/1085 (91.34%)|993/1085 (91.52%)|

The paired final runs gained37 and lost29 questions. Only single-hop exceeds90%.
The judge explicitly accepts generous topic/entity agreement: the earlier claim
that this was strict grading is incorrect. Regrading uses the same stored answers
and is not another answer-generation replication. The old run also enabled
rereading while the new one used ten passages without rereading, so the difference
does not isolate a single code change.

The206-question development sample was selected using prior correct/incorrect
labels and is contained in the full1,085. Several configurations were inspected.
It is therefore not an untouched held-out estimate. The answer-call script sends
only the stored system/user prompts; the audit found no evidence of directly
passing gold answers to generation. Gold was used for grading and diagnostics.

For178 sample questions, full-run prompts were byte-identical but answers were
generated again. Thus the original pipeline did not establish the requested
first-completed-result policy across stages. Substituting the earlier saved sample
answers gives994/1085 only as a sensitivity analysis, not a replacement score.
Per-attempt completion times, effective model configuration, raw judge responses
and complete cost records are absent. Label parsing could fall back to a text
substring; its historical use cannot be reconstructed. No formal acceptance or
all-category90% completion is claimed.

한국어: 보고만 누락됐고 전체 평가는 끝나 있었다. 관측 총점은983→991이지만
세 분야는90% 미달이다. 채점은 관대했고, 개발 표본을 다시 썼으며, 먼저 끝난
결과만 쓴다는 정책도 전체 단계에서 입증하지 못했다. 기존 숫자는 보존하되
정식 인수 성적이나 새 안전성 수정의 효과로 사용하지 않는다.

## Verification / 검증

Before correction, three focused safety regressions failed. Independently, the
same PostgreSQL tests over the original commit reproduced transfer-policy bypass
and delivery after either the selected source or its parent was erased. They
passed after correction. These database tests use real SQL/search/neighbor/fence
behavior, a synthetic generator and a test authorizer; they are not a live-model,
production-RLS or HTTP deployment claim.

The source-stable reflect/reread race selection passed42 top-level tests, with
zero failures/skips (3.076s). The independent final PostgreSQL run passed2
top-level tests and2 subtests, with zero failures/skips (1.580s). Source-file
hashes were unchanged during that final run. The environment parser race check
passed2 top-level tests and11 subtests (1.211s).

The three PR review regression tests failed before correction. The dream portion
subsequently passed28 top-level tests and7 subtests; that combined run still failed
one lexical-alias assertion. After correcting alias deduplication, the complete
focused lexical selection passed8 top-level tests and26 subtests (1.379s), with
zero failures/skips. The independent crash-test correction passed2 top-level
PostgreSQL tests and5 subtests; its separate record preserves timing evidence. Initial failed checks
are retained: one expected exact plural spelling overlooked an existing alias;
one sandboxed run could not open its loopback test server; an alias-group ordering
regression was found and corrected. None is counted as a pass.

Scoped vet for memory and daemon packages passed. OpenAPI structural validation
passed437 operations (zero live requests). Complexity validation passed while
reporting three pre-existing functions above the threshold.

Commands use the installed matching Go toolchain, `GOTOOLCHAIN=local`,
`GOPROXY=off`, `GOMAXPROCS=2`; no tool download or product build is required.

```sh
go test -race ./internal/memory -run '^(TestReflect.*|TestVerbatimLines.*)$' -count=1 -timeout=300s -json
go test -race ./cmd/memoryd -run '^(TestReflectPassagesFromEnv|TestRereadLimitFromEnv)$' -count=1 -timeout=180s -json
go test -race ./internal/memory -run '^(TestSearchConcepts.*|TestSearchQuery.*|TestDream.*|TestSkillQuery.*)$' -count=1 -timeout=300s -json
go test -race ./internal/memory -run '^(TestSearchConcepts.*|TestSearchQuery.*|TestSkillQuery.*)$' -count=1 -timeout=180s -json
go vet ./internal/memory ./cmd/memoryd
go run ./tools/complexity --report "$PRIVATE_COMPLEXITY_REPORT"
python3 tools/check_openapi.py
git diff --check
```

Two new dream test files now use a synthetic person consistently in both
languages; the focused post-sanitization dream race run passed12 top-level tests
and4 subtests, with zero failures/skips (20.749s). Original implementation
branches remain intact; publication must use a clean integration history so the
old fixture identifiers are not introduced through intermediate commits.

The independent standards/specification reads found the source safety fixes
consistent with the intended boundary. The audit keeps private fixtures, original
answers, prompt hashes, failed attempts and logs outside the repository. No raw
benchmark conversation or gold data is added by this completion.

## Publication fixture review / 공개용 테스트 자료 검토

For this completion, the new public integration worktree replaces identifying
values in the existing email-redaction and no-answer fixtures with fictional
names, reserved test domains and a generic account label. Matching assertions
change together, retaining masking, parsing and abstention behavior. Eight other
changed files replace old comment-only work identifiers or synthetic person
names with generic descriptions or a consistent fictional name. Three generated
requirements provenance comments lose local scratch-directory paths; every
noncomment dependency line remains byte-identical. Original implementation
branches remain unchanged.

The focused memory race selection passed six top-level tests (3.508s). The capture
race selection passed five top-level tests and four subtests (1.169s). Both had
zero failures or skips. `git diff --check` passed. The independent app API check
`node desktop/ui/app-api.test.js` passed five tests with zero failures; that
change concerns deferred-operation polling and does not alter a rendered screen.

A private publication review records exact public-base blob hashes, paths, match
line numbers and nonprivate categories, without copying matching personal data
into this document. Existing explicit exceptions remain separate from the
reviewed product labels, work identifiers, copyright attribution and constructed
test data. This review does not grant new private-data exceptions or claim that
publication checks have passed before the final integration commit is checked.

이번 공개 통합 작업에서는 기존 테스트의 식별 값을 가상 인물, 테스트 전용
도메인과 일반 계정명으로 바꾸고 관련 검증문도 함께 맞췄다. 요구사항 파일의
생성 경로 주석 세 곳만 일반화했으며 의존성 고정 값은 그대로다. 메모리 및
캡처 회귀 검사는 모두 통과했고 원래 개발 브랜치는 변경하지 않았다.

## Limits / 남은 한계

The corrected implementation has not been re-evaluated with a real model. Source
snapshots are conservative: a change to an optional block dropped for budget can
still reject delivery. Existing citation output names memory IDs, not passage
IDs. Combined prompt size uses the existing character-based estimate rather than
a provider tokenizer. The passage feature stays experimental and off by default.
The dedicated temporary PostgreSQL instance was stopped after both passage and
crash checks; child databases, roles and other client connections were zero, and
its approximately65.6MB data copy was removed. Original evaluation inputs and logs
remain. Material-insufficient research tracks remain on hold. This record does not mark
the whole roadmap or all-category quality objective complete.
