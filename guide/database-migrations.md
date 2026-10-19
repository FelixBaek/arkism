# Database migrations

`memoryd --init-db` applies the schema versions bundled with that binary. It
records each version, name, SHA-256 checksum and application time in
`arkism.schema_migrations`. Readiness requires the complete, matching history.
The SQL assets live in `internal/memory/migrations/`; changing a recorded asset
blocks startup. Checksums use UTF-8 with LF line endings, including on Windows.

Version one freezes the previous initializer's schema, indexes, functions,
policies and triggers. An existing database without migration history runs that
idempotent baseline once. Records, approvals, the storage identity and an
existing paused recovery write gate survive. Later startups verify history and
apply only pending versions. Search-token backfill remains bounded and resumable
after the schema transaction commits; it is skipped while recovery writes are
paused.

Version two refreshes full-text indexes for descriptive memory conditions:
`memory_context.applicability`, `uncertainty` and `valid_from`. It adds a derived
index marker and a pending-row index without rewriting canonical records during
the schema transaction. The bounded backfill refreshes pre-context token vectors;
rows without descriptive context keep their existing vectors. New inserts and
replacements index the qualifiers immediately. Pending rows retain the existing
body index and use PostgreSQL's simple parser for context until normalized
backfill finishes; Korean particle matching on those qualifiers requires that
backfill. A paused recovery gate keeps backfill paused. Resume through the normal
recovery procedure, then rerun initialization to finish pending index work.
Approvals, source references, revisions and embeddings are unchanged.

## Upgrade

1. Keep a tested backup and its matching binary. Stop the API, worker, timers and
   desktop processes that use the database. Review the target version's changes.
2. Use a direct database connection as `arkism_init` or the database/schema owner
   with the DDL privileges described in [server.md](server.md). Extension setup
   still requires the privileges specified there. Use the **new** binary with
   `memoryd --init-db`; never give the application account migration privileges.
3. Run `memoryd --check-indexes` using the application connection, then start the
   matching API and worker. The built-in desktop runtime performs initialization
   with its separate initializer connection before starting the application.

Concurrent initializers serialize through a database-local, transaction-scoped
advisory lock. Lock waits are limited to five seconds; the whole schema operation
is limited to two minutes and also respects the caller's earlier deadline. A
timeout or statement failure rolls back **all** pending DDL and history rows in
that attempt. Resolve the cause and retry with the same binary. The tool reports
the failed phase/version and a safe SQLSTATE code for failed migration statements
or lock acquisition, without printing SQL, database credentials or data.

Application sessions may read history but cannot insert, update, delete or
truncate it. Missing entries, altered checksums/names, gaps and unknown newer
versions fail readiness and initialization. An empty existing history table is
an error. Do not repair these errors by deleting rows or rewriting checksums.
The migration owner is trusted and can alter the database; this ledger does not
protect against a malicious database administrator.

## Restore and rollback

Follow [backup-restore.md](backup-restore.md): restore the dump into a new empty
database **before** running initialization. A versioned dump carries its migration
history. After restoration, initialize it with a matching or newer compatible
binary, then check indexes. An older unversioned dump is adopted through the
baseline. Initialization preserves the recovery write pause; it does not approve
restored data, replay later deletions, adopt files or reopen writers.

There are no automatic down migrations. Restore the tested pre-upgrade backup
into another empty database and use its matching binary for rollback. Binaries
that predate migration tracking cannot retroactively enforce the new history
contract; do not point them at an upgraded database.

## Add a migration

Add a numbered SQL asset, embed it, and append its consecutive version/name to
`schemaMigrations()`. Keep all previously applied assets unchanged. Every batch
runs in the outer PostgreSQL transaction: do not add transaction control,
`CREATE INDEX CONCURRENTLY`, external commands or operations that require leaving
that transaction. A long or destructive upgrade needs an explicit staged design
and backup/restore rehearsal before it ships.

Regression tests use disposable loopback databases: empty bootstrap, an
independent unversioned fixture, no replay, data/pause preservation, concurrent
initialization, cancelled lock/retry, partial-failure rollback, incompatible
history and application privileges. The authenticated backup roundtrip also
restores the history and verifies readiness before applying recovery actions.
These tests do not constitute an upgrade of a user's installed database.

The SQL dialect is PostgreSQL/PLpgSQL. Sonar's Oracle PL/SQL analyzer is mapped
to Oracle-specific suffixes, rather than its default `.sql` mapping; see
[Sonar's language-mapping documentation](https://docs.sonarsource.com/sonarqube-server/analyzing-source-code/languages/t-sql).
PostgreSQL executes the migration, function, RLS and privilege regression tests;
the Go, JavaScript, Python and other configured Sonar analyses still run.

PostgreSQL documents [transaction-scoped advisory locks](https://www.postgresql.org/docs/18/explicit-locking.html#ADVISORY-LOCKS)
and [object privileges](https://www.postgresql.org/docs/18/ddl-priv.html).
