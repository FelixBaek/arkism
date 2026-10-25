# Agent runner connections

An expert (or one workflow step) can run on an agent runner you already operate instead of Arkism's own CLIs. Calls, `@name` questions in Discord and workflows stay the same: only where the step runs changes.

The connection lives on the computer that runs `arkism` (the host runner). The server stores only the connection's **name** on the expert.

## 1. Add a connection

```bash
printf '%s\n' "$RUNNER_TOKEN" | arkism crew backends add --name myrunner --url https://runner.example.com
arkism crew backends check myrunner
```

- The token is read from stdin, never from an argument. It goes to the OS keyring (service `arkism-backends`), or to `backends.json` (mode `0600`) on a machine without one. `list` and `check` show only where it is kept.
- Arkism sends the runner **only this token**. The Arkism owner or runner token never leaves the host.
- `check` reads `GET /v1/capabilities` and prints what the runner cannot do (`limits`).

Options:

| Flag | Meaning |
|---|---|
| `--local` | Allow a loopback or private address (`127.0.0.1`, `10.x`, `192.168.x`, `100.64/10`). Plain `http` is allowed only on loopback. |
| `--isolated` | You state that the runner works in its own sandbox. A read-only step may then go to a runner that cannot restrict tools itself. |
| `--own-model` | Accept that a runner without model choice uses its own model. The step records `model` as not applied. |
| `--memory` | Let approved memory go along with the task (see "Memory" below). Off by default. |

Other commands: `arkism crew backends list`, `arkism crew backends remove myrunner`.

Address rules (always on): `https` only (plain `http` only on loopback with `--local`); no user name, password, query or fragment in the URL; metadata servers, link-local, multicast and unspecified addresses are refused; every connect re-checks the resolved IP; redirects are never followed; no proxy.

## 2. Use it for an expert or a step

```bash
arkism crew backends use myrunner --expert @researcher   # back to Arkism's own path: use local
```

This saves `backend: myrunner` on the expert (the owner's change, so it is refused inside an agent Arkism started). In a workflow, an `ai` or `expert` node may set its own `backend` (`local` overrides the expert's).

What runs on a connection:

- `@name` calls (써보기, Discord) and workflow `ai` / `expert` steps with `access: read`.
- Not: steps with `write` or `full` access, `collab` or `retries` on the step, team meetings (the member's turn fails with a reason). Arkism never falls back to another model or to its own CLIs for such a step.

## 3. HTTP contract `arkism.backend.v1`

All calls carry `Authorization: Bearer <connection token>`. Replies are JSON, at most 1 MiB.

### `GET /v1/capabilities`

```json
{"protocol": "arkism.backend.v1",
 "capabilities": {"status": true, "cancel": true, "idempotency": true, "tool_restriction": true,
                  "instructions": true, "model_choice": false, "usage": true}}
```

A capability that is missing is false. Arkism reads `model_choice`, `instructions`, `status`, `cancel`, `idempotency`, `tool_restriction`, `usage`, `memory`; it lists `approval`, `events`, `session_reuse`, `attachments`, `structured_result` but does not use them yet.

### `POST /v1/tasks` (header `Idempotency-Key: <run.key>`)

```json
{"protocol": "arkism.backend.v1",
 "run": {"id": "assist-…", "key": "assist/assist-…", "kind": "assist", "expert_id": "…", "workflow_id": "", "step_id": ""},
 "task": "Summarise this paper",
 "instructions": "role and instructions of the expert",
 "skill_documents": "approved skill text, if any",
 "model": {"requested": "", "must_apply": false},
 "access": {"mode": "read", "enforce": true},
 "limits": {"timeout_s": 14400},
 "result": {"format": "text", "max_chars": 20000},
 "session": "none", "approval": "none"}
```

Reply within 30 seconds with the task's state: `accepted` or `running` for a task that takes longer (Arkism then asks `GET /v1/tasks/{task_id}`), or the final state when it is already done. A start left unanswered after 30 seconds is **unknown**:

```json
{"task_id": "t-123", "status": "succeeded", "result": {"text": "…"}, "model": "runner-model",
 "usage": {"input_tokens": 1200, "output_tokens": 300, "cost_usd": 0.01}}
```

`status` is one of `accepted`, `running`, `waiting_approval`, `succeeded`, `failed`, `cancel_requested`, `cancelled`. `error: {code, message}` explains a failure. `task_id` matches `[A-Za-z0-9._:@/ -]{1,128}`.

### `GET /v1/tasks/{task_id}` and `POST /v1/tasks/{task_id}/cancel`

Same reply shape. Cancel takes `{"reason": "arkism"}`.

The trace says what was sent (`context`) and what was not applied (`not_applied`).

### Memory

Memory goes along only when **both** hold: the connection was added with `--memory`, and the runner reports `memory: true` (it keeps the section apart from instructions). Then Arkism asks its own search for the task with destination `external:backend.<name>`, so every hit passes the server's approval, scope and transfer policy (restricted or unsafe content never leaves). Only approved memories go, at most 5, each at most 1,200 characters:

```json
"memory": {"authority": "data_only_untrusted", "items": [{"id": "m1", "text": "…"}]}
```

The runner must treat it as data, never as instructions. The trace lists each id as `memory:<id>`. When the flag is on but the runner lacks the capability, or the search fails, nothing is sent and `memory` shows under `not_applied`; the step still runs. Arkism never stores a runner's answer as memory.

## 4. What Arkism does with the answers

| Situation | Result |
|---|---|
| `401` / `403` on start | failed (`backend_auth`); nothing started |
| other `4xx` on start | failed (`backend_rejected`) |
| `5xx`, timeout or no answer on start | **unknown**: the task may have started. Sent again (same key) only to a runner with `idempotency` |
| reply without a known `status`, or `succeeded` without text | **unknown**, never success |
| status query fails | asked again until the step's time runs out; not a task failure |
| `404` on status | unknown; never re-run |
| a state older than the last one (late `accepted`) | ignored |
| `waiting_approval` | Arkism does not relay approvals and never approves: cancel (if supported), then failed |
| time runs out or the step is stopped | cancel if supported: `cancelled` when the runner confirms, `cancel_requested` when not. Without cancel the result says the task and its cost may continue |
| answer over 20,000 characters | clipped with a note; the connection token is masked |

- Each step keeps a small ledger (`backend-tasks/` in the host state, `0600`): the Arkism key, the runner's task id and the state, never the task text or the answer. It is written **before** the start call.
- After a restart, a step with a task id is asked about again; a step whose start went unanswered is not resent (unless the runner dedupes); one with neither is marked unknown. Nothing is run on another runner.
- `@name` calls: when a runner accepts, the request becomes `running` with the trace (`backend`, `task_id`, `state`, models, `not_applied`, `context`, `usage`, `fallback: false`). Discord posts "외부 실행기가 받았어요" once and the answer later. A running request with no report for 24 hours reads as failed.
- Workflow runs: each step on a connection keeps the same trace on its node in the run record (`nodes.<id>.backend`), done or failed. A step that starts shows `state: starting` until the host runner's trace replaces it.
- Discord: an answer split into parts resumes from the part that failed; a thread Discord reports gone (`403`/`404`) ends the job, and the answer stays in Arkism. A workflow run with a step running on a connection posts "외부 실행기에서 도는 단계가 있어요" once and waits as long as an `@name` call.
- Several host runners: a new request is taken only by a host that has the connection; a running one only by the host that started it (its ledger). Another host never fails it or runs it again. A request no host takes expires after 10 minutes.
- Cost: reported usage is stored as reported (`estimated: false`). A runner without `usage` leaves cost **unknown** (no number), never 0. Arkism's own price estimate is not applied.
- Answers are untrusted data, like any model output: they never change the step's access, approvals or connection.

## 5. Limits

- Only the generic HTTP contract above. OpenClaw and Hermes adapters are not implemented.
- No streaming events, attachments, session reuse or approval relay.
- The settings screen does not show connections yet; use the CLI.
- If the host that started a request loses its state folder, nobody asks about that task again; the request reads as failed after 24 hours.
