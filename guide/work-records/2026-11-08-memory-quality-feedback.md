# Cross-execution outcome identity / 여러 실행의 동일 작업 평가

Task: memory quality item 3, additional improvement after F1.
Research: none
Why: different trace IDs for retries could satisfy the independent sample threshold.
Verified: scoped actual ingestion/outcome/PostgreSQL checks passed (2.204s); related run, trace, outcome and signal-policy regression checks passed (2.900s).
Limits: opt-in declared task identity is not proof of causal independence or producer honesty.

제목: 서로 다른 실행의 같은 작업 평가 중복 제거
이유: 같은 작업의 재시도가 독립 표본 수를 부풀리는 것을 막는다.
검증: 실제 수집·평가·DB 집계 검사 2.204초, 실행·trace·평가·신호 정책 회귀 검사 2.900초 통과.
한계: 생산자가 같은 작업에 같은 키를 붙여야 하며, 실제 작업의 동일성을 추측하지 않는다.
연구: 해당 없음

The optional `task_key` belongs to the authenticated completed-run ingestion and
ordinary agent-run creation contracts. The independent checker cannot supply or
replace it. The server derives an outcome identity from scope, authenticated run
creator role/ID, task type and explicit task key, while checking the exact trace/run
revision, sources, terminal completion, deletion and quarantine. Multiple run
bindings are rejected as ambiguous. Different creators or scopes do not merge.

The existing opt-in recent evidence policy now selects the first completed run
among recorded eligible judgments per task identity, memory content and dimension, then the first recorded judgment
for ties. An earlier failed completed run stays a failed sample even if a later
success was judged first. Time filtering follows selection, so later duplicates
cannot refresh an expired first judgment. Absent task identity preserves the old
exact-trace behavior; no old rows are guessed or backfilled. The API mode label
`recent_first_trace` remains for existing consumers, with task-bound traces now
collapsing into a shared sample. Legacy event-based weighting remains unchanged.

All evaluations, failure results, source events and usage/cost records remain
stored. Only the independent policy's sample selection changes. A task key is
caller-declared provenance within the authenticated producer's namespace; using
different keys for the same work remains possible. Finished timestamps likewise
come from the existing validated ingestion contract, not a new external clock
attestation. Separate attempts for genuinely new work need a new task key.
This does not change native dynamics outcome accounting or claim causal benefit.

An earlier completed run without any independent outcome cannot contribute a
judgment. If its outcome arrives later, recomputation selects it using its earlier
completion time. This selection is deterministic over currently stored eligible
evidence, not a claim that an unobserved attempt was evaluated.


## Verification details / 검증 근거

The real PostgreSQL scenario ingests seven complete traces and records seven
independent judgments. Three attempts share one task: a later-completed success
is judged before an earlier-completed failure. The five eligible samples preserve
that failure and four successes; all seven raw evaluations remain. Another
producer using the same key is separate, a distinct task is separate, and two
unkeyed traces stay separate. Ambiguous run bindings, missing creator provenance
and invalid task keys are rejected. Existing dimension/content/window parity,
legacy history, denial, source erasure and completed-session tests also passed.

The first test command failed to compile because a fixture helper name was wrong.
The next attempts exposed an invalid disposable DSN, source-less synthetic runs,
missing shared-scope agent identity and an attempt to change immutable fixture
creator provenance. These fixtures were corrected; no production checks were
removed. Automatic review rejected a proposal to make the newly added run-source
check conditional. The final code retains the unconditional check and supplies
valid lineage in synthetic runs. Existing stored runs lacking valid source
provenance now fail closed rather than contribute an outcome.

No provider calls, release build, deployment, push or production access occurred.
Schema/SDK synchronization and full integration testing are handled by the root
integration task; the focused results do not claim the complete suite passed.
