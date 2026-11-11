# Reviewing model-written content

The server model can draft four kinds of content from your memories:
observations, topic summaries (mental models), causal links and memory
candidates extracted from files. None of them reaches an agent until a person
approves it. The model never approves, and a service token cannot approve.

| Content | Model output starts as | Approve with | Readers see |
| --- | --- | --- | --- |
| Observation | `proposed` | `POST /v1/observations/{id}/review` (`approve`, `reject`, `edit`) or `review_batch` | `active` observations only |
| Topic summary | `draft` revision | `POST /v1/mental-models/{id}/review` (`approve`, `reject`) | the last approved revision |
| Causal link | `candidate` | `POST /v1/causal-links/{id}/review` (`approve`, `reject`) | approved links only |
| File extract | memory candidate | the normal candidate review (`POST /v1/memory_candidates/{id}/review`) | approved memories only |

In the desktop app the drafts appear in MEMORY's review list (확인해 주세요),
next to merge proposals. Starting a consolidation run or creating a topic
summary is API-only for now.

## What the model reads

Every model call goes through the server model and the same gates:

- Only approved memories are sent. Each one is checked for the caller's
  `context:read` permission and the `external:transfer` grant before it leaves
  the server. Private-marked, secret or restricted text stays behind.
- Calls count against the daily server-model cap and are recorded in the usage
  ledger and the model call trace.
- Without a configured server model the routes answer `503 model_not_configured`.
  See [server mode](server.md#upgrade-to-the-memory-review-features).

## Observations

A consolidation run (`POST /v1/observations/consolidate`) groups approved
memories that share a topic, a name or a repeated lesson and asks the model for
short statements that cite them. Citations outside the group are dropped. What
passes is stored as `proposed`.

An approved observation turns `stale` when a memory it cites is replaced, and on
the next run when one is rejected, expired or erased. Recall checks every cited
memory again before serving an observation. The next run proposes a refreshed
text with a diff against the old one, which again waits for review. Every change
is kept in `GET /v1/observations/{id}/history`.

## Topic summaries

A person creates a topic with a name and a query (`POST /v1/mental-models`).
A refresh recalls the query over approved memories and approved observations and
writes a cited summary as a draft. `dry_run` returns the text without storing a
draft. Readers keep seeing the previous approved revision until the new draft is
approved. Deleting or erasing a cited memory clears summary text that depends on
it.

## Causal links

A person can link two memories directly (`POST /v1/causal-links`); that link is
active at once. `POST /v1/causal-links/propose` asks the model for cause and
effect pairs among approved memories and stores them as candidates. A rejected
pair stays as a tombstone, so the model cannot propose it again. Recall with
`expand_causal` and context building follow approved links only, one step from
the top results.

## Slow model calls

Six model-backed routes can outlast one request: `POST /v1/observations/consolidate`,
`POST /v1/causal-links/propose`, `POST /v1/mental-models/{id}/refresh`,
`POST /v1/wiki/build_from_memories` (`dry_run`), `POST /v1/skills/analyze` and
`POST /v1/skills/generate`.

- A call that finishes within 25 seconds returns its ordinary result or error.
- A slower call returns `503 request_timeout` and cancels its work context. It
  also follows caller cancellation. Sensitive response bodies are not copied
  into operation rows, and these routes no longer return deferred `202` replies.
- Work committed before cancellation can remain. Inspect existing records
  before repeating a request; a timeout does not prove that nothing was saved.
- Legacy deferred operations are readable only by their creator. They return
  terminal `failed` / `result_unavailable`, never the old response body. Reading
  one attempts to clear its stored response payload; the body stays hidden even
  if cleanup fails. Unread legacy rows and backups still
  require separate cleanup; this is not a completed historical erasure sweep.
- Desktop and SDK polling compatibility remains for older servers. Background
  completion of these six sensitive routes is held until results can be tied
  to current authorization and source erasure.

## Files

`POST /v1/files/memories` uploads a file and starts an extraction job. Each
chunk becomes ordinary memory candidates with the file as their source, so they
go through the same review as captured conversations.

- Re-uploading the same document (same uploader and name) re-extracts only the
  chunks that changed. The job reports what disappeared; earlier candidates stay
  for a person to retire.
- Cancel a running job with `POST /v1/operations/{id}/cancel`. The job stops
  before its next model call. Candidates from the chunk in progress stay as
  candidates.
- A failed job is resumed by its uploader with `POST /v1/files/memories/jobs/{id}/run`.
  Operator retry is refused because the job runs under the uploader's token.
- `POST /v1/extract/preview` shows what pasted text would produce and stores
  nothing.

## Rules and answers

Directives (rules) are written by people only and are active when saved; there
is no model proposal step. `POST /v1/reflect` answers a question from approved
memories under the active directives, cites the memory ids it used and stores
nothing.
