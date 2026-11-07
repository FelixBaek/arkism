# Memory context delivery options / 기억 문맥 전달 선택지

Task: memory audit M03 (conversation), M06 (whole items), M09 (adjacent turns), M10 (optional lane availability)
Research: none
Why: Conversation evidence returned by search was absent from BuildContext's consumed data; character truncation could remove later independent facts before token packing; optional storage outages stopped base recall; a bounded scan of unrelated conversations hid adjacent batches.
Verified: Scoped memory integration and regression tests with an isolated temporary PostgreSQL passed in 8.403s. HTTP tests exercise all three new options and inspect consumed ContextData, exact token counts, retained facts, actual dropped reasons and partial/degraded output. Source and appended-neighbor erasure at final commit are rejected. Frozen/external exclusion, pinned-option rejection, caller cancellation, denial propagation, legacy defaults and native-ranking fallback pass. Schema migration manifest, fresh install, replay, rollback, concurrency and history protections pass. git diff --check passes.
Limits: Synthetic engineering fixtures only; no live model answer-quality claim, production build, deployment, production database, or push. A first new-test compile failed because a fixture used the wrong embedded field; corrected before the reported passes. Native ranking preserves baseline for requests using these options. Agent pinned context rejects them. Remaining limitations and API scope are below.
제목: 대화 근거 전달, 완결 항목 패킹, 선택 레인 복구와 대화 이웃 직접 조회
이유: 검색에서 찾은 대화 근거가 실제 입력 문자열에 누락되고 문자 상한에서 뒤의 독립 사실이 사라지는 경로를 선택 기능으로 보완한다. 부가 검색 장애와 대화 batch 조회 상한의 영향도 제한한다.
검증: 격리 임시 PostgreSQL을 사용한 관련 기능·회귀 시험이 8.403초에 통과했다. 실제 HTTP 입력부터 최종 문자열까지 새 옵션 세 개, 토큰 수, 제외 이유, partial 상태와 원문·이웃 삭제 경합을 확인했다. 스키마 migration 및 기존 검색·문맥·동적순위 회귀도 통과했다.
한계: 합성 기능 자료만 사용했다. 제품 빌드·모델 호출·운영 접근·게시를 하지 않았다. 긴 1위 유일정답을 잃을 수 있는 정책이며 정확도 향상을 보장하지 않는다. 기존의 같은 수집시각·정렬 한계와 지원하지 않는 경로는 아래에 명시한다.

## Behavior and expected improvements / 기능과 기대 개선

All three request booleans default to false. They are accepted by ordinary REST search and `/v1/context/build`, and direct Go service calls. Public API/SDK contract synchronization is maintained by the parent integration change. This work does not add a UI switch or claim every host/MCP caller automatically opts in.

- **M03 — `conversation_context`:** BuildContext forwards the choice and requests source inclusion. Search places matching unreviewed conversation turns in a separate `conversation` list in `context_data`, after approved and provisional entries. Every retained entry stays `data-only`; original source and appended neighbor are independently authorized and checked again in the final transaction. The exact JSON tokenizer budget covers those turns, including metadata. Their text/summary uses only remaining character space after the ordinary groups. Direct Search requires positive `budget_tokens`; BuildContext uses its existing budget default and directives reservation. This makes a fact present only in captured conversation available to a consumer of ContextData.
- **M06 — `whole_items`:** At the character stage, an oversized item is skipped whole, allowing later short independent facts through to token packing. Existing ranking among retained items stays. Explain reports `character_budget`, `character_truncated`, `summary_truncated`, `token_budget`, `provisional_token_budget`, `conversation_character_budget`, and `conversation_token_budget` where applicable; truncation counts are separate from omission counts. The test keeps two distinct short facts after an oversized first item. A second test explicitly shows the cost: the long first item can contain the only answer and be omitted. This is greedy selection, not semantic summarization or optimal packing.
- **M09 — bounded adjacent lookup:** PostgreSQL exposes an optional repository capability with two limit-one reads. It finds the immediate previous/next batch under `created_at,id` within the same scope, source reference, product and exact collection timestamp. Other conversation batches no longer consume the legacy 99-row discovery cap. The caller preserves its existing spare-excerpt budget and independently checks the returned candidates. Deleted immediate neighbors remain adjacency barriers rather than making a farther turn appear immediate. Other repository implementations retain the bounded legacy scan.
- **M10 — `best_effort_lanes`:** Only optional temporal/graph/causal/semantic storage reads returning `ErrUnavailable` may degrade while the request remains active. The failed graph source is disabled for subsequent hops. Base results and healthy lanes remain available, response `degraded` records the affected lane, and BuildContext sets `partial`. Default hard failure, explicit base modes, authorization, cleaning, usage ledger, final verification and cancellation behavior are preserved. There is no added per-lane timer; existing request deadline, depth and fanout bounds remain.

## Compatibility / 호환 경계

Conversation evidence remains excluded for frozen views, external destinations, history and relation searches; source permission and conversation capture density are still required. New flags with nonempty `agent` return invalid because the pinned-context path does not implement them. Native dynamic or legacy fast/slow serving falls back to the ordinary baseline with `unsupported_context` when one of these options is active; its separate packer cannot silently replace the requested delivery policy. Without these flags, existing native serving and ordinary truncation remain in place.

Character and token packing remain sequential. Space freed by a later token rejection does not trigger a new retrieval or refill of earlier character omissions. The conversation total token budget does not make supplemental `include` traces outside ContextData part of that string. Provisional groups retain their own preexisting selection bounds; the new source group consumes only the remaining text/summary space. There is no claim that these options make every output envelope field share a single character cap.

## Storage plan / 저장소 계획

Migration 0007 adds a partial index on conversation sources using the full scope plus hashes of source reference/product. Full string predicates are retained, so hash collisions cannot join different sessions. This also avoids unbounded index key size for long source references. The migration manifest and replay tests cover the added version; prior migration assets are unchanged.

The PostgreSQL functional fixture has 153 same-session batches and 1,200 other-session batches and checks exact preceding/following IDs. It runs `ANALYZE` and `EXPLAIN (ANALYZE, BUFFERS)` on the matching indexed predicate and asserts that the session index is used. An earlier 1,203-row fixture observed 3 shared buffers and 0.033 ms execution for one direction; this is local synthetic plan evidence, not a production latency guarantee. The added same-session rows also pass the plan assertion.

The index narrows by session. Timestamp conversion and ordering still occur within that session, so very large single sessions need separate scale measurement or a stored ordered cursor design. Same `collected_at` remains mandatory: this does not connect every capture epoch of a long conversation, and `created_at,id` is not a newly guaranteed semantic turn sequence. No old source content is rewritten.

## Checks / 실행한 검사

Final scoped command, with the temporary PostgreSQL DSN supplied in the environment:

```text
systemd-run --user --scope -q -p MemoryMax=6G timeout 900 go test ./internal/memory -run 'TestContext|TestConversation|TestRecall|TestPostgresConversationNeighbors|TestSchemaMigration|TestNativeSearch|TestSearchTokens|TestSemantic' -count=1
```

Result: PASS, 8.403s. No all-package command was run. Earlier focused new tests passed in 0.329s, 0.721s and 1.159s; those are earlier heads, not extra evidence for later code. Final source adds no provider calls in tests. UI inspection is not applicable because no UI files changed.

Additional whole-area static audit covers retrieval/ranking, graph traversal, time, context, embeddings and multilingual normalization. It records temporal prefilter starvation, hub traversal order, Unicode normalization, multi-window intent, nonfinite environment weights, vector scale, consumer exposure and explicit conversation sequence as future candidates. None is claimed implemented by this change; the original audit's four items above are kept separate from that follow-up list.
