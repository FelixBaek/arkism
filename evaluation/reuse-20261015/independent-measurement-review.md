# Independent review of live-agent and benchmark evidence

Reviewed read-only during the 2026-10-15 run. This review did not alter prompts, task facts, evaluator checks, run order, or completed answers.

## Live bidirectional smoke

Inspected `live_agents.py`, `judge_live.py`, `live-agents/runs.json`, both `*-reader.jsonl` files, and `scores.json` in the integration checkout.

- Codex → Claude: the Claude `mcp__arkism__recall` call has a matching successful `tool_result` containing the approved label rule and original source reference. Final `transformed_label` is `MiXeD`, correct. The JSON `source_id` is the memory ID, not the source ID: strict source attribution fails. The surrounding prose does separately mention the correct original source ID; this does not change the requested field's failure.
- Claude → Codex: the completed `mcp_tool_call` has no error and contains the approved TTL rule and source reference. Final `cache_zero=false`, `positive_ttl=15`, and `source_id` all match the rule/provenance.
- The observed task application score of 2/2 and strict source-field score of 1/2 are supported by the raw events, rather than model self-reports.
- Claude attempted `record_memory_usage`, beyond the reader prompt's requested recall-only path. The tool was denied and no usage recording is evidenced. Do not claim the model stayed within the requested tool subset without mentioning this attempted call.
- The generic scorer marks Claude recall on a `tool_use` alone; future runs should require the matching successful result, and Codex scoring should check `error` and returned content as well as `status=completed`. The actual two results were manually verified here, so this parser limitation does not invalidate these observations.
- These one-task-per-direction outcomes demonstrate an actual model consuming cross-agent MCP memory. They do not establish general productivity or reliable provenance attribution.

## A/B/C benchmark fairness (30 completed runs inspected)

Inspected `benchmark/run.py`, `README.md`, `validate_judges.py`, `benchmark_http_export_test.go`, and completed run records/events/generated module shapes in the independent benchmark checkout.

- Model prompts contain only the task, starting module and condition-appropriate project fact. Hidden executable checks are not included in memory or prompts. The five facts are general project policies, not the input/output pairs from the hidden tests.
- Static and Arkism inputs are explicitly checked for identical rule text. Arkism uses exact HTTP lookup by the host; this isolates information delivery, not autonomous retrieval/search quality, skill use, or metadata reasoning. The README states this distinction clearly.
- New temporary profiles and ephemeral sessions prevent transcript carry-over; provider prefix-cache effects remain and are disclosed. At inspection time all 30 completed CLI processes exited 0. No command/MCP tool events were observed; repeated error-type events were solely the documented ignored output-cap warning.
- Actual cost, human preparation/review time and unsupported provider settings stay null. The benchmark measures short code generation, not repository-wide tasks. No repair rounds means zero by design, not a measured reduction in rework.
- Reference/mutant checker validation covers each of the five checks. No observed false success was found in the inspected run artifacts. Generated code is executed in a separate network-disabled unprivileged container.

Harness hardening suggestions sent to the benchmark author, without changing the frozen experiment:

1. Refuse to score a nonzero CLI return code as success even if a final answer is present.
2. Distinguish evaluator/container timeout from model timeout; currently the outer `TimeoutExpired` catch can classify either as a model timeout.
3. Report token measurement coverage instead of treating null tokens as zero when aggregating.

The chosen short, arbitrary-policy tasks intentionally make missing information consequential. They can show value from supplying the right information, but do not by themselves favor Arkism over a maintained static document or establish long-repository productivity. The README appropriately limits these claims.
