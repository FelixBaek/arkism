# Follow-up conversation passages

Work ID: MEMORY-QUALITY-90; native service-path retrieval completion.

## Reason and behavior

A reformulated recall query could find memory summaries while still omitting the original conversation wording needed for an exact detail. Optional conversation passages previously used only the initial question. Ordinary follow-up recall now searches original wording by keyword when the existing passage option is enabled and source access is allowed.

Memory hits are packed first. New passages share the remaining per-tool approximate-token allowance and the existing chunk cap; a passage that does not fit is skipped before its sources are admitted. Accepted source IDs appear in the tool trace. The overall passage count, 40,000-rune limit and combined prompt budget remain fixed. Fresh relevant passages may replace older optional passages, but every earlier source snapshot remains bound to the request and is still checked before delivery. Matching turns and neighbors use the existing privacy, source-transfer and source-dependency checks. Original scope, recorder and tag filters remain; ordinary recall intersects tool date bounds with the request window. Observation-only recall retains its earlier behavior.

The default option stays off. The enabled, source-authorized path has a generic capability description and prompt version `reflect-v7`; the disabled default prompt remains unchanged. Follow-up passage search adds no generation or embedding request and grants no additional tool or iteration allowance. A separate source-expansion review fix skips a newly denied document transfer without dropping already accepted sources; a nonempty changed document still conflicts.

## Validation

- Focused real-loop tests passed: a reformulated query exposes original wording omitted from the initial prompt, replaces an older optional passage, and spends exactly the existing two generation calls.
- Late erasure of either a newly added source or a replaced older source rejects delivery.
- Recorder, scope, tag and intersected date filters, default-off and observation-only compatibility passed.
- A 64-token tool cap and 16-token chunk cap bound the combined memory and raw passage output; oversized sources are excluded from both snapshots and trace, while accepted raw source IDs are visible.
- Global Unicode-rune and passage-count bounds passed. Newly denied document transfer leaves an accepted source and its original snapshot intact.
- Enabled capability/version and source-denied descriptor tests passed. Changed-file cognitive and cyclomatic complexity checks passed.
- Race-enabled Reflect suite passed (`go test -race ./internal/memory -run '^TestReflect' -count=1`, 4.822 seconds), including the existing source, memory, mental-model and tool-format regressions.

## Limits

Tests use controlled records and model doubles. A model may answer immediately without requesting a useful reformulation; this change does not guarantee that a natural follow-up occurs or establish a new evaluation score. Follow-up matching is keyword-only and may miss semantic matches. Long raw blocks are skipped rather than rewritten to fit a small tool cap. No benchmark questions, gold answers or special-case identities are encoded in retrieval. Subsequent actual service evaluation exercised follow-up recall in a selected failure case, but it still did not retrieve the missing answer. The fixed native 32-question score also remained 27/32. See [the actual evaluation record](2026-11-12-memory-live-evaluation.md). No production deployment was performed.
