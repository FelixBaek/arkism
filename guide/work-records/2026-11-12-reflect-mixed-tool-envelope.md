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

The detector targets a complete reserved envelope appended to prose; it does not execute arbitrary JSON extracted from text or relax strict tool decoding. It does not infer what a malformed or incomplete JSON fragment intended. Tests use controlled model doubles and do not establish a new benchmark score. The old native 27/32 result remains preserved. No production deployment was performed.


## Targeted native service regression

Candidate `bb244298c810bd29c513dbe3e88843278e760c19` was built only for the isolated evaluation server and tested against the single previously failed native-pilot question. The original data scope, model and service profile were retained. A separate persisted bridge enforced a hard three-generation-call total and per-question cap, with no retries; offline tests verified cap preservation across restart and that failed calls consume their reservation.

The request returned HTTP 200 and a nonempty final answer in one iteration and one successful generation call. It identified both the broken windshield and the later vehicle breakdown. No native tool was called. Usage was 5,440 input tokens and 139 output tokens (5,579 total); the bridge does not enforce the requested output-token limit. The original 32-question outputs and 27/32 result were not overwritten, and their file hash remained unchanged.

The live model did not produce a mixed prose/tool envelope on this attempt, so the conditional repair branch was **not exercised in the live run**. This selected failure-case check establishes only that the new candidate can return a valid service answer for that input; it does not prove live repair activation or establish a revised benchmark score. The bounded repair branch is covered by the deterministic service-loop tests above. No additional model calls were made to force the failure to recur.
