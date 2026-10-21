# Memory extraction and retrieval contracts

Implementation contracts retained from the development guide. For layout, checks and debugging, see [development](../development.md).

## Skill improvement generation

New memory extraction uses `capture-extract-v5` and `harness-session-v6`.
One-off permissions, budgets and assignment corrections belong in session state;
durable candidates retain the named project and every condition. Session
user-statement context fields must be supported by verbatim quotes from the same
original user message. Separate messages cannot be combined to justify a condition.
Host-created fragments may share that origin only at the same revision, including
authorized prior evidence; absent or malformed fragment metadata keeps them separate.
Inferred scope diagnostics belong in state uncertainties, not in quoted condition fields.
This literal check prevents invented metadata wording; it does not prove semantic
completeness. Older prepared requests retain their original delivery contract.
An older session quality gate does not enable the new prompt's fast model.
Inherited scope stays in the statement, with exact original citations. Context
fields copy the actual subject or condition phrase; they do not concatenate a
name and a subject from different clauses, replace the scope with an exception,
or invent a translated name. Previously prepared v4/v5 context still requires
literal evidence.

The full-text lane omits English question scaffolding from query terms while
preserving negation, quantities and temporal operators. Queries with at least
three distinct topic concepts need two distinct matches, including descriptive
memory context. This check runs before the candidate limit and also applies to
rows awaiting backfill. Short lookups remain permissive; exact keyword,
semantic and relation retrieval keep their independent contracts. These rules
reduce lexical noise; they are not a semantic relevance guarantee.
Korean backup/copy and restore/recovery wording is expanded as a single concept
per pair. The alternative spellings cannot count as two independent matches.

On Windows, state-file publications to the same destination may wait up to two
seconds for their turn. Transient replacement retries retain their separate
500 ms budget. These bound additional waits and attempts, not kernel or scheduler
latency; neither bound is a promise of absolute write completion time.

`POST /v1/skills/generate` reuses verified lessons and the pinned baseline. The
configured chat and Anthropic adapters request localized changes to an existing
skill instead of asking the model to rewrite the full document. Each of up to
eight replacements names a literal `old_text`, its `new_text`, and a zero-based
`lesson_index` into the supplied verified lessons. These are internal provider
fields; the HTTP request and canonical `SkillDraft` format are unchanged.

Every anchor must occur exactly once in the original baseline. Missing,
ambiguous, overlapping, chained, empty, and ineffective changes are rejected.
Replacements are composed against original byte locations, preserving all
untouched text and line endings. New skills continue to use full content.
The resulting full document still passes the existing capability, size, source,
comparison, and approval checks. A patch identifies a proposed change; it does
not establish its semantic correctness or performance benefit.

Candidate generation leaves the active baseline unchanged. The Anthropic
prepared-result path stores the composed document, so a retry after candidate
storage failure can recover it without repeating the provider call. Provider
prompt versions distinguish this format from older whole-document generation.

## Memory conditions in retrieval

Full-text retrieval includes descriptive applicability, uncertainty and valid-from
phrases alongside the body, summary and keywords. Bounded condition phrases
precede body tokens so long records cannot exhaust the index budget before
the conditions are included. The same Korean normalizer is
used for new writes, replacements and bounded index backfill. Domain, evidence
and semantic-kind labels do not become generic search terms. Descriptive dates
do not change lifecycle dates or make an unapproved memory eligible for recall.

The HTTP/PostgreSQL regression captures a tool observation, requires human
approval, then retrieves it using only condition terms. It checks returned
context, scope, unrelated queries and deletion. The schema upgrade regression
checks old indexes are refreshed without changing canonical records, and that
paused recovery remains paused. See [database migrations](database-migrations.md)
for upgrade and rollback. These deterministic tests do not measure extraction
completeness or semantic retrieval accuracy on live conversations.

### Memory storage and management evidence

Storage/management is separate from recall ranking. Raw session archives, capture
transport copies, work checkpoints, pending candidates and active memories retain
their existing independent lifecycles. A successful citation validator checks literal
support; it does not establish semantic completeness or an actual human decision.

The pending review screen groups similar proposals for display only. Four-part scope,
kind, reach, repository, applicability, exceptions, effective date and polarity
must agree; legacy proposals without applicability only group by normalized exact text.
Each proposal keeps its own revision, sources and review action. A repeated statement
never approves a proposal or raises trust. Expanding a group checks at most32 proposals,
two requests at a time. The evidence endpoint reads at most64 original message references
and counts distinct canonical user turns only where the candidate's literal citation
occurs. Conversation-window membership alone does not count. Expired, reconstructed,
unreadable, inferred and unmatched evidence stays unknown. Candidate source tracing
also provides the existing privacy-fenced original-message reader before approval.

New explicit review decisions store a server receipt id, authenticated actor role and
channel. These identify a human-role account request, not physical human presence.
Historical role labels, trust1, user-speech tags or old `person` flags are not upgraded
into this evidence. Existing memories and trust values are preserved. Recall exposes
`approval_origin` as provenance without changing relevance or usage ranking.

Human review reminders (`review_due_at`) are independent of expiry. Scheduling/clearing
uses the current revision, source checks and an atomic audit, and refuses a pending
correction. An overdue reminder appears in the review list; it never approves, deletes,
expires or renews original archives. Correction-pending warnings reuse the existing
conflict guard. Server delivery and answer-text overlap are displayed separately from
actual answer use; a zero overlap is not proof of uselessness.

Capture copy expiry leaves bodyless, mode0600 local receipts for seven days, capped at256.
They record copy removal and whether extraction was prepared; successful delivery stays
unverified. Raw paths, utterances, session ids and model responses are excluded. Failed
receipt writes do not extend raw-copy retention. Session extraction records bodyless
withholding reason/window diagnostics only while collection policy permits persistence.
`capture stats` reports expired transport copies, receipt failures and validation holds.
Neither these counters nor successful literal checks claim complete collection or
semantic extraction coverage.

Deletion displays distinguish request acceptance, server stage reports and unverified
external copies. The desktop keeps at most32 bodyless receipt ids per server/scope for
seven days; it reads status on demand. The optional archive audit has a25-row/2second
bound, exact scope and human authorization. A full page is partial; errors/timeouts
return unknown without a residue count. It never attests external backups or client
caches. Restore receipt-ledger and publication-review gates remain in force.

The approval-origin review list surfaces existing active memories whose new explicit
review receipt is missing. Listing them never changes their active status or trust;
the historical attribution remains unproven until a separately authorized review flow.

Desktop runtime launches omit inherited Windows shell pseudo variables (for example
`=C:` drive-directory state) from their explicit application environment. Programs
and working directories are already absolute. Ordinary inherited variables, command
overrides/removals and replay snapshots remain intact. Explicit malformed names still
reach the existing native rejection boundary; the native validator is unchanged.
