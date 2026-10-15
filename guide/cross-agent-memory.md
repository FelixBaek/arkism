# Cross-agent and cross-session memory

This change follows the reuse evaluation in PR #45. The baseline is
`4ef98458a53ea4b6afa47bcda9801cb47dc5678c`. Passing the earlier memory-reuse
experiment does not demonstrate autonomous handoff, lower cost, or better
results than maintained static instructions.

## Implementation contract

1. Connect actual CLI agent execution to the canonical work checkpoint API.
   Handoff carries a bounded structured goal, constraints, evidence references
   and return state. Reserve work before execution; retries must not silently
   run the same task twice. Resume and publication recheck current access and
   evidence. Stored test results and model claims are historical observations,
   never independent verification or permission to run commands.
2. Supply a bounded recent-session summary at session start, limited to the
   current repository and authorized scope. Exclude the current session and
   stale, deleted or inaccessible state. Keep standing rules and summaries
   distinguishable, and preserve the existing context budget.
3. Narrow memory retrieval by a named server-defined role profile without
   granting additional access. Unknown profiles fail closed; ordinary search
   continues to use its existing contract.
4. Extend the existing PostgreSQL relationship storage with bounded,
   evidence-backed entity traversal. Relations require explicit human writes
   and currently valid memory evidence. Deleted or replaced evidence must stop
   subsequent retrieval. A graph path is cited data, not an inferred new fact.

The architecture keeps the existing PostgreSQL authority and JSON HTTP/MCP
transport. No new message broker, graph database, autonomous approval, or
deployment is part of this change. The runtime adapter is not advertised as
an A2A standards implementation.

## Verification boundaries

Use the public HTTP/API and MCP boundaries, session-start hook, and executable
CLI handoff as the observable interfaces. Check restart, duplicate delivery,
concurrent claims, wrong scope, removed evidence, output bounds, and source
identity. Use an isolated disposable PostgreSQL database for integration
checks. Synthetic CLI subprocesses prove process integration only; distinguish
them from paid model executions and from operation of the deployed service.

## Available interfaces

| Interface | Behavior |
| --- | --- |
| MCP `handoff_work`, CLI `arkism crew handoff` | Explicit sender/recipient/session, parent checkpoint, task, model and absolute working directory; reserve a unique child ID, run, save the result |
| MCP `resume_work` | Read the saved result with current evidence and access checks |
| MCP `recent_sessions`, HTTP `/v1/work-checkpoints/recent` | Recent distinct sessions in the exact repository and authorized scope |
| MCP `recall_for_role`, HTTP `/v1/memory/role-search` | Researcher, implementer and reviewer relevance profiles |
| MCP `expand_knowledge_graph`, HTTP `/v1/knowledge-graph/expand` | Evidence-backed directed traversal |
| HTTP `/v1/knowledge-graph/relations` | Human-only assertion; an agent cannot approve its own relation |

The MCP client fixes scope from its configured identity; tool arguments cannot
replace it. API requests continue to use normal authentication and RLS.

### Handoff and recovery

`handoff_work` requires `id`, `parent_work_id`, `sender`, `recipient`,
`session_id`, `task`, `model`, and `cwd`. Optional `skills` resolves at most eight
latest-approved skills under the caller's credential. Parent and skill versions
are checked before dispatch and before publishing a result. The runner does not
switch to an owner or separate runner credential for these reads.

Dispatch also requires explicit external-model transfer permission. The server
checks the exact task/instructions hash, current parent provenance and approved
skill versions at `/v1/work-checkpoints/authorize-handoff`. A temporary CLI
profile receives only the selected provider login file. Arkism credentials and
inherited MCP configuration are excluded; Codex must pass an empty MCP-list
preflight before execution. Temporary profiles are removed on exit.

Archive message references are inherited with source IDs and the checked skill
version/source dependencies. Explicit privacy
erasure follows those references through dependent checkpoints and receipts.
Routine archive expiry still permits independent durable work summaries, in
accordance with the existing retention contract.

`access` defaults to `read`. Explicit `write` uses Codex's workspace-write
sandbox and requires the parent's absolute repository path and `cwd` to resolve
to the same directory. `full` access, automatic model selection and nested
handoffs are rejected. Read mode can inspect another explicitly selected
checkout; the stored repository label does not prove that checkout's identity.

Timeout is 120 seconds by default, bounded to 1–600 seconds. Parent context is
limited to 24 KB, skill text to 6 KB and returned result text to 8 KB. These are
size/time limits, not a provider token or currency budget.

Reservation uses the canonical checkpoint revision check. Another process with
the same child ID cannot execute it again, including after a crash. Inspect
`resume_work` before deliberately choosing a new ID. Ambiguous execution or
result-persistence failure is not automatically retried; interrupted work may
remain reserved. This prevents duplicate execution but does not promise
automatic recovery of a lost model response. `completed` reports completion of
the invocation; output remains `unverified` and requires independent checking.

### Recent sessions

SessionStart fetches summaries afresh instead of caching them with standing
rules. Defaults are the last seven days, three sessions, and at most 2,500
characters within the existing total hook budget. Settings:

- `ARKISM_RECENT_SESSIONS`: 0 disables; otherwise 1–10.
- `ARKISM_RECENT_SESSION_DAYS`: 1–30.
- `ARKISM_RECENT_SESSION_CHARS`: 500–4,000.

The current session is excluded. At most 30 candidates are inspected to fill the
requested number of valid summaries; deleted or inaccessible evidence is skipped,
while a changed checkpoint revision, invalid scope or database error fails closed.
The canonical current directory must equal the
checkpoint's repository; URLs, different checkout paths and missing directories
are not guessed to be equivalent. Summaries contain saved goals, decisions,
completed work, next steps and source references. They do not execute saved
commands or transmit whole transcripts. This reads existing work summaries; it
does not create a new model summary for every historical conversation.

For relation storage, lifecycle and role mappings, see
[knowledge graph and role retrieval](knowledge-graph-and-role-retrieval.md).

## Verification

The new tests exercise real PostgreSQL, loopback HTTP, authenticated RLS,
MCP/CLI subprocesses and an external CLI stub. They cover same-ID replay,
concurrent reservation, source/skill removal, wrong scope, role narrowing,
recent-session exclusion, graph traversal bounds and deletion of graph data.
The CLI stub is deliberately not evidence of real model quality or cost savings.

The restricted single-connection HTTP test found an authorization lookup inside
a graph transaction that waited for its own database connection. Authorization
now happens before the transaction; the final transaction still revalidates
the exact memory snapshot and its sources.

Reproduce using a disposable database configured by `POSTGRES_TEST_DSN`:

```sh
go test -race ./internal/host/crew ./internal/host/skills ./internal/host/capture
go test -race ./internal/memory -run 'Test(PostgresAgentHandoff|CollaborationHTTPPostgres|PostgresKnowledgeGraph|PostgresRecentWork|KnowledgeGraph|RoleRetrieval|RecentWork|RecentSessions|HandoffTransfer|PostgresHandoffTransfer|PostgresWorkArchivePrivacy)'
```

`tools/check_collaboration.sh` creates and removes its own disposable PostgreSQL
container and runs the deterministic integration suite.

The optional `TestLiveCrossAgentRepositoryHandoff` requires
`ARKISM_LIVE_HANDOFF=1`, an isolated `POSTGRES_TEST_DSN`, and explicit absolute
`ARKISM_LIVE_AUTH_HOME`, `ARKISM_LIVE_CLAUDE_BIN`, and `ARKISM_LIVE_CODEX_BIN`.
It uses synthetic policy, approval and repository fixtures with actual provider
CLIs. This functional smoke test is not a real-user approval or quality study.

No operational deployment or fresh real-model quality comparison is implied.
PR #45's measurements remain unchanged.

### Recorded validation (2026-10-15)

The live smoke completed both steps: Claude Opus 5.5 reviewed the synthetic
repository, then Codex gpt-6-astra applied the fix using that saved review and
the approved skill fixture. Both saved checkpoints retained and cited the
original source ID (2/2). Independent Python checks passed (2/2), and the test
file was unchanged. This is one small functional run, not a representative
production-repository benchmark. Provider charges and human approval time
were not measured.

The first live run exposed a real persistence bug: multi-line model output was
stored as one checkpoint fact, which rejects control characters. The adapter
now stores bounded nonempty lines, normalizes tabs, and validates text before
publication. A multi-line PostgreSQL regression test covers this failure.

Local validation also passed Python checks (32), UI checks (29), Go vet and
all Go race tests, plus Rust tests (26 passed, 1 intentionally ignored).
`tools/check_collaboration.sh` passed using its own fresh database and cleaned
up its container. After late handoff fixes, the affected integration/race tests
were rerun separately.
