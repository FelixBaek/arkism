# Capture privacy boundaries

`<private>…</private>` removes marked text from extraction input. Nested spans stay hidden until the outer span closes. An unfinished span hides the remainder of the message. User, assistant and tool text, prior state and queued extraction input follow the same boundary.

| Path | Before sending or saving |
| --- | --- |
| Hooks and extraction | Redact text before search and model authorization. Refuse marked identifiers and object keys rather than changing their identity. |
| Model requests and responses | Inspect the final request and response. The authorized structured request is exactly the request sent to the model. |
| Archive and evidence | Inspect a stable copy before the first upload. Skip an entire marked archive, including its hashes and evidence locators. Keep accepted archives byte for byte. |
| Execution history | Redact assistant replies and tool commands before recording behavior. Mark obscured behavior as incomplete so it cannot be treated as fully observed evidence. |
| Prepared delivery retries | Reuse the exact safe request or wait. Never rewrite an already prepared event under the same event ID. |

Pause, folder exclusions and `.arkism-ignore` apply to the source folder and explicit project paths recorded in supported session metadata. They are checked again before archive requests and retries, and before queued extraction or checkpoint delivery. Windows drive and UNC exclusions also work for imported metadata on another operating system. Local symbolic links are checked against their resolved folders.

A missing capture settings file keeps the default behavior. Invalid JSON, invalid setting types, unreadable settings or settings larger than 1 MiB pause capture. Inspection errors stop archive transmission. Waiting does not approve a memory, change its scope or advance its durable processing cursor.

## Archive inspection limits

Inspection recognizes plain UTF-8 JSON/JSONL, JSON strings nested inside records, JSON Unicode escapes and UTF-16 SQLite text. SQLite logical TEXT and BLOB values are checked as well as raw bytes, because a value can span storage pages. Opaque compressed or encrypted payloads are not decoded.

- Archives and reconstructed output: at most 2 GiB, matching the existing 2,048 chunk limit.
- Structured metadata: at most 8 MiB per record, 64 nesting levels and 4,096 explicit paths.
- SQLite logical inspection: at most 1,024 tables, 1,000,000 rows, 64 MiB per value and 30 seconds per inspection. Snapshot backup also has a 30 second limit.

An archive outside these inspection limits waits instead of sending unchecked bytes. Large unstructured records retain their original bytes and are still scanned for private markers.

These boundaries apply to new capture and retries. They do not erase existing backups, archives or memories. Use the app's deletion controls to remove previously saved data. Legacy marked archive text is refused as a source for new automatic checkpoint evidence.

## Verification

The capture tests use synthetic input and fake transports. They check zero requests for marked input across JSONL, reconstructed exports and SQLite overflow pages; all dialogue roles and prior state; old spool and prepared retries; policy changes during extraction or upload; source mutations; staging cleanup; behavior traces and receipt replay; and unchanged bytes, hashes and delivery behavior for ordinary sessions. Actual queue tests pause repeatedly without spending retry attempts, then resume the original delivery. Windows CI also runs the privacy boundary tests. No real provider or user session is needed.
