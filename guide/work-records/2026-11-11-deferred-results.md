# Deferred response disclosure repair / 지연 응답 공개 차단

Related work: PR #552 review of sensitive model responses in operation rows.

The previous implementation copied arbitrary successful route responses into
`jobs`. Those rows did not carry complete source provenance, survived source
erasure and could be read by another human or service with `read:jobs`. Merely
restricting the creator or adding a TTL would still expose stale responses after
the original authority changed or a source was erased.

The six model-backed routes now answer within their original request, bounded by
25 seconds and any earlier caller deadline. Successful fast responses and normal
errors keep their previous shape. Timeout returns HTTP 503 `request_timeout`;
caller cancellation returns HTTP 503 `request_cancelled`. Both propagate context
cancellation. They create no new operation row and persist no response body.
Detached asynchronous completion and new HTTP 202 acceptance are disabled until
there is a complete provenance, reauthorization and erasure contract. The legacy
DTOs remain decodable for compatibility; they are not newly emitted.

Legacy deferred metadata is creator-only for humans, services and agents; rows
without a creator are invisible. GET and LIST never return the stored body,
including when cleanup fails. Their terminal projection is `failed` with
`result_unavailable`, so an old client stops polling. This denotes unavailable
response delivery, not proof that the original operation failed to commit. An
owner read attempts to remove the body from the live database row while
preserving its original execution status. Legacy rows never read, ownerless
rows, backups and storage-level historical copies are not swept by this change.
No claim is made that those existing copies have all been erased.

Cancellation is cooperative and cannot roll back an already committed side
effect; a timeout must not trigger blind automatic retries. No actual model,
external provider or product/release build was used for these tests. Slow-call
background completion remains unavailable, rather than being reported as a
finished feature.

The synthetic regression first reproduced disclosure to other human/service
principals, stale-body return after changed authority or erased-source state,
and nonterminal legacy polling. All three top-level tests and three subtests
failed before the repair. The erased-source fixture tests unconditional legacy
body withholding; old rows have no lineage that could support selective source
reauthorization. PostgreSQL verification separately checks both serialized
payload and searchable document fields for removal of the synthetic body.

The portable operations fixture was moved unchanged out of the Windows-excluded
test file. The integration owner ran `GOOS=windows go vet ./internal/memory`
successfully; that is a cross-platform compile/vet check, not execution of
Windows tests.

Final focused race verification passed 25 top-level tests and three subtests
(1.436s), with zero failures or skips. This includes two real PostgreSQL 18 tests:
legacy body removal from both live representations without changing execution
status, and zero jobs rows after a cancelled slow call. Other checks cover
creator-only GET/LIST, permission/source-state changes, ownerless rows, terminal
polling, cleanup failure, fast result/error behavior, panic containment and
request/deadline cancellation without new jobs. The independent final review
reported no blocking findings.

```sh
go test -race ./internal/memory -run '^(TestPostgres(DeferredLegacyResultIsScrubbed|RespondLaterTimeoutStoresNoResponse)|TestDeferredLegacy.*|TestRespondLater.*|TestOps.*|TestAgent.*Job.*)$' -count=1 -timeout=180s -json
```

The final run used the cached Go 1.27.4 toolchain, `GOTOOLCHAIN=local`,
`GOPROXY=off`, `GOMAXPROCS=2` and disposable loopback PostgreSQL child databases.
An earlier invocation with Go 1.27.3 was rejected before tests; the first
sandboxed PostgreSQL invocation failed because loopback sockets were denied.
Neither is counted as passing. The permitted local rerun produced the final
results above. No full-suite or live-model quality claim follows from this
focused verification.
