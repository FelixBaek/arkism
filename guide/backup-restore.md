# Backup and restore

## Desktop app (built-in mode)

Open **Settings › 고급 (Advanced) › 데이터 (Data)**.

- **지금 백업 (Back up now)** saves a full backup to `Documents/Arkism Backups`.
- The app also makes one backup a day and keeps the last seven automatic ones. Backups you make by hand are never pruned.
- Pick a backup in the list to restore it. Restoring replaces the current data.
- **모든 데이터 지우기 (Erase all data)** removes the built-in database and settings. Backups in `Documents/Arkism Backups` stay.

The backup folder sits outside the app's data folder, so erasing data or uninstalling the app does not touch it.

## Server

A server backup is a nightly job you schedule yourself (a systemd timer or cron). The repository ships the `arkism` subcommands it is built from, not the job script. It needs the recipient certificate in `ARKISM_BACKUP_CERT`. Each run:

1. pauses writers only while it pins a database snapshot and copies the file store (`arkism backup-snapshot`),
2. bundles the `pg_dump -Fc` archive and the files (`arkism backup-bundle create`),
3. encrypts the bundle with CMS into `/srv/arkism/backups/arkism-prod-pg-<time>.cms` (`arkism backup-archive encrypt`).

`arkism-backup-retention.timer` removes old archives.

### Restore

If the old server still runs, save its deletion list first: `POST /v1/deletions/export` with `{"scope": ...}` returns every deletion for that scope.

Keep every writer (API, worker, timers) stopped, and restore into a **new, empty** database. Prepare it with the same roles, `extensions` schema and `vector` extension as in [server.md](server.md#install-by-hand) step 1, but do not run `--init-db`.

1. Decrypt and unpack the archive:

   ```bash
   arkism backup-archive decrypt --cert backup-recipient.pem --key backup-recipient.key --input X.cms --output bundle.zip
   arkism backup-bundle extract --input bundle.zip --output restore
   ```

   This gives `restore/database.archive` and `restore/files`.

2. Load the database:

   ```bash
   pg_restore --exit-on-error -d "$NEW_DATABASE_URL" < restore/database.archive
   ```

3. Bind the restored files to the restored database. File storage stays locked until this runs.

   ```bash
   export MEMORY_DATABASE_URL="$NEW_DATABASE_URL" MEMORY_BLOB_DIRECTORY="$PWD/restore/files" MEMORY_WORKER_TOKEN=...
   memoryd --adopt-restored-blobs
   memoryd --check-indexes
   ```

4. Replay deletions made after the backup was taken. Start `memoryd --recovery`. It listens on loopback only, serves just the deletion and expiry endpoints, and needs the same `MEMORY_SESSION_ARCHIVE_KEY` as before. For each target in the saved list, send `POST /v1/privacy/deletions` with a stable idempotency key such as `restore-<target id>`, so a repeated replay does not create a second request. Then call `POST /v1/session-archives/expire` for each scope. `memoryd --inspect-restored-recovery` shows what is still pending.
5. The normal API refuses to start until the restore is reviewed. There is no command for this step yet. After the replay, remove the review marker with the `arkism_init` connection, then start the API and worker as usual:

   ```sql
   DELETE FROM arkism.memory_storage_metadata WHERE key = 'restore_review_pending';
   ```
