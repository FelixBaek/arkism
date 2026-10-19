# Session collection paths

All local Codex transcript readers use the same precedence:

1. `ARKISM_CODEX_SESSIONS`: an explicit transcript directory.
2. `CODEX_HOME/sessions`, when `CODEX_HOME` is set.
3. `~/.codex/sessions` under the process user's home.

This covers capture reflection, historical session import, work handoff, local
PC hook boundaries, archive collection, usage indexing and quota-log discovery.
Changing a setting does not move, delete or modify transcripts. Use absolute
paths and restart long-running host processes after changing their environment;
scheduled one-shot processes read it on their next start.

Usage titles default to `session_index.jsonl` beside the selected sessions
directory. Set `ARKISM_CODEX_SESSION_INDEX` for a different index location.
The index is optional; copying it is not a substitute for copying transcripts.

Archive and usage collection use `ARKISM_CLAUDE_PROJECTS` for local Claude
transcripts too. In server mode, the synced PC inbox remains separate:
`ARKISM_PC_SESSIONS/codex-sessions` and `claude-projects`. With
`ARKISM_PC_LOCAL=1`, local roots are used once with the existing PC label.
Device labels and relative paths used in archive identities are unchanged.

## Moving between operating-system users

First inspect the actual producer process user, HOME and CODEX_HOME, and the
collector service user and environment. A chat's working directory is not its
transcript directory. If the producer and collector already use the intended
user's directory, no migration is necessary.

If a migration is needed:

- Back up the source and destination before editing configuration. Keep those
  backups private and outside the repository.
- Stop or quiesce the affected writer before a final copy; an active transcript
  can change while being copied.
- Copy into a new staging directory with restrictive permissions. Preserve the
  original files, session IDs and relative paths. Verify counts and hashes.
- Never overwrite a different destination file with the same path. Preserve both
  versions separately and resolve conflicting session IDs before collection.
- Grant the collector access only to the selected transcripts. Do not open the
  whole root home, copy credentials, rewrite transcript IDs, or replace the
  destination's complete Codex configuration.
- Point all collection processes at the verified destination. Keep device labels,
  collection state and receipts consistent to avoid duplicate archive identities.
- Keep the original and backup until the separate acceptance checks pass.

This procedure makes transcripts available to Arkism. It does not migrate
Codex desktop chat metadata or guarantee that chats appear in the Codex sidebar.
Archive collection retains its existing seven-day time window; historical
memory extraction uses `import-sessions` and still requires human approval.

## Validation handoff

Run path precedence and archive/usage boundary regressions, then the host suite.
Also exercise the real service with a synthetic transcript in a separate
directory: default home, custom CODEX_HOME, explicit Arkism override, explicit
title index, server inbox and local PC modes. Check unreadable-path behavior
separately; this change does not add permission diagnostics or bypass access
controls. Do not use private production transcripts as public test fixtures.
