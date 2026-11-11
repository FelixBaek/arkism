# Reflect mixed tool-envelope recovery

Work ID: MEMORY-QUALITY-90; native service-path functional evaluation.

## Reason and behavior

The native service pilot returned an explanatory sentence followed by a complete reserved tool-call JSON object as its final answer. Strict whole-response decoding correctly refused to execute that mixed response, but the loop then treated it as an ordinary answer. The original failed evaluation remains unchanged.

Reflect now recognizes a well-formed reserved tool envelope appended to prose as a format error. Recognition never executes or returns extracted calls. When another iteration is already available, the next prompt explains that no tool ran and asks for a strict tool envelope or an answer without an embedded request. The malformed response spends its existing iteration. A final-iteration mixture returns the existing model-answer error with no answer, and creates no additional model-call allowance. Whole-response tool parsing and ordinary answers retain their existing behavior. The default system prompt is unchanged: the additional repair instruction is conditional on a prior mixed-envelope failure in the same request, and is cleared after a valid reply.

## Validation

- Focused real-loop tests passed for mixed response, then strict recall, then answer in exactly three recorded calls; only the subsequent strict recall was executed.
- Final-iteration mixed response was rejected with no answer, no tool execution and exactly one model call.
- Plain answers, ordinary JSON answers, text merely naming the reserved key, and existing strict/fenced tool envelopes remain accepted by their existing classification.
- Existing tool iteration-cap and parallel-tool tests passed with the focused run.
- Cognitive-complexity checks passed for the changed loop and new files after extracting tool execution and prompt-memory-ID helpers.
- Race-enabled Reflect suite passed (`go test -race ./internal/memory -run '^TestReflect' -count=1`, 4.736 seconds). The initial sandbox attempt could not open an existing localhost test-server socket; the authorized local-socket rerun passed.

## Limits

The detector targets a complete reserved envelope appended to prose; it does not execute arbitrary JSON extracted from text or relax strict tool decoding. It does not infer what a malformed or incomplete JSON fragment intended. Tests use controlled model doubles and do not establish a new benchmark score. The old native 27/32 result remains preserved. No external model requests or production deployment were performed for this change.
