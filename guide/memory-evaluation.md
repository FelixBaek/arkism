# Memory usefulness evaluation

This protocol compares **no memory**, **static project instructions**, and
**Arkism** for one person's coding workflow. It is a measurement plan, not a
claim that Arkism improves model performance. Team features are out of scope.

## 1. Run the deterministic contract first

From the repository root:

```sh
go test -race ./internal/memory -run '^TestCrossAgentMemoryReuseLifecycle$' -count=1
```

The test uses the existing in-memory repository and authorization fakes. One
agent proposes a sourced rule; a distinct agent reads it through a separate
service instance. It checks that:

- unapproved proposals are absent from context and neither agent can approve;
- human-approved, active memory is returned with source and trust labels;
- another project does not receive that memory;
- deleting the source stops subsequent reuse even with a stale index hit.

This runs in the existing Go CI suite without network access or model keys.
It does **not** validate desktop capture, MCP adapters, Postgres, semantic
retrieval quality, or whether a real agent follows the returned rule. The
service-level human gate also does not certify every host automation path;
review capture/reflect policy separately before release.

## 2. Freeze the experiment before collecting results

Record the Arkism commit, repository fixture commit, agent/adapter versions,
model identifiers, system prompts, temperature/seed when supported, tool
permissions, task deadline, maximum output tokens, context budget, embedding
configuration, and machine configuration. Keep these fixed within each paired
comparison. Save task prompts and expected checks before running any condition.

Use synthetic repository fixtures and rules. Do not publish personal session
transcripts, credentials, or unrelated source data. Record unavailable provider
fields as unknown rather than guessing them.

Use at least five scenario types, with multiple independently worded tasks:

| Scenario | Setup and expected behavior |
| --- | --- |
| Persistent rule | Agent A learns a project test command; fresh agent B uses it. |
| Conditional exception | A temporary exception applies to one task only; B follows the ordinary rule outside that condition. |
| Changed rule | An old command is replaced; B uses the current command and never the retired one. |
| Removed evidence | A previously used source is removed; a fresh retrieval does not return its memory. Already delivered context cannot be recalled from an old chat. |
| Irrelevant project | Similar vocabulary exists in another project; B does not receive or apply that project's rule. |

For each task specify the correct change, executable success check, relevant
rule IDs, forbidden/stale rule IDs, and applicability conditions. Freeze an
evaluation set that is not used to tune extraction or search.

## 3. Compare the three conditions fairly

Run each task in a clean checkout and fresh agent session. Do not let outputs
or transcripts from one condition enter another. Use a dedicated empty Arkism
profile/database for each replicate and restore only the declared fixture.
Disable other persistent memories, automatic instruction files, and history
reuse in all conditions except the explicit static-document baseline.

1. **No memory:** B receives the task and normal repository files, with no
   previous correction or memory injection.
2. **Static document:** B receives the same approved, current facts in its
   supported project instruction file (for example `AGENTS.md`). Apply the same
   changes/removals used in the Arkism fixture; record the human maintenance
   time. Do not deliberately leave this baseline stale.
3. **Arkism:** A supplies the same evidence, a human reviews the candidate,
   and B retrieves the approved memory. Record extraction, review and retrieval
   costs, including candidates rejected or corrected by the human.

Keep total model context and task budgets equal, including injected memory or
static instructions. Measure initial preparation separately from repeated task
cost. Measure maintenance after changed/removed evidence in both memory
conditions. Rotate condition order across tasks and perform at least three
fresh replicates per task; report every attempt, including timeouts.

Run both A-to-B and B-to-A directions for the actual adapters supported by the
release. Record any manual intervention. A service test passing is not an
adapter integration result.

## 4. Record observations before judging quality

Use one row per task, condition, direction and replicate. Save this CSV header
with the run artifacts:

```csv
experiment_id,commit,fixture_commit,task_id,scenario,condition,direction,replicate,model,adapter_versions,success,timeout,relevant_rule_ids,returned_rule_ids,stale_or_forbidden_rule_ids,rule_followed,context_tokens,input_tokens,output_tokens,provider_cost_usd,setup_seconds,maintenance_seconds,review_seconds,retrieval_ms,task_seconds,manual_interventions,evidence_path
```

Use executable checks or a human reviewer unaware of the condition for task
success. Treat model self-reports as observations, not independent pass/fail
evidence. Distinguish “retrieved the right rule” from “followed the right rule.”

Report denominators and raw counts alongside:

- task success and timeout rates, paired by task;
- retrieval precision (relevant returned rules / all returned rules), and
  recall (relevant returned rules / expected relevant rules); use N/A for
  zero denominators and report no-result cases separately;
- forbidden/stale retrievals and wrong-scope disclosures, as separate counts;
- human setup, maintenance and review time, including rejected candidates;
- task time and retrieval latency distributions, tokens and actual provider
  charges; never infer charges from an unverified price;
- repeated-rule explanation and manual intervention counts.

Summarize per scenario and adapter direction before aggregating. Report the
spread across replicates and uncertainty; a small fixture is not evidence of
general superiority. Keep deterministic contract failures separate from
stochastic model failures.

## 5. Use the result to choose the next change

First require zero forbidden approval, wrong-scope publication, and reuse
after evidence removal in the deterministic contract and adapter smoke runs.
Record failures as release blockers for those contracts rather than averaging
them into a quality score.

For usefulness, predeclare what improvement would justify the review burden
for this workflow. Compare Arkism with the maintained static document, not
only with no memory. If the evidence is inconclusive, report that.

Choose one demonstrated bottleneck (extraction, review burden, retrieval,
duplicates, applicability, or stale-memory handling), change it, and rerun the
frozen set plus the full affected test suite. Keep exploratory examples
separate from the frozen evaluation. Do not expand feature scope to compensate
for missing evidence.
