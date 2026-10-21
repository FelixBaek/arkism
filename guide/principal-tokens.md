# Principal token lifecycle

These commands are for an operator holding the `arkism_init` database connection.
They are not HTTP endpoints. The application role cannot inspect or modify token
history. Never place an init connection in an agent configuration.

## Generate and provision

Use `memory-principal --generate-token-file PATH` to create a new token file.
The command generates 32 random bytes, writes base64url without padding, and
never prints the token. It does not connect to a database or overwrite an
existing file. Unix files use 0600; Windows files are created with a protected
DACL granting the current user access before secret bytes are written.

New credentials must encode 32 bytes as 43-character unpadded base64url or
64-character hex. Inputs with fewer than 16 distinct decoded bytes are rejected.
This rejects obvious repeated inputs, but cannot prove the entropy of an
externally supplied value. Do not use a password, a password hash, or a public
test fixture as a token. Use the generator or an equivalent cryptographic RNG.

For example, in PowerShell, after setting `MEMORY_DATABASE_URL` to the init
connection through your normal secret-management process:

```powershell
$adminDir = Join-Path $env:LOCALAPPDATA 'ArkismAdmin'
New-Item -ItemType Directory -Force $adminDir | Out-Null
$tokenFile = Join-Path $adminDir 'owner-token'
memory-principal.exe --generate-token-file $tokenFile
$env:MEMORY_PRINCIPAL_TOKEN = (Get-Content -Raw -LiteralPath $tokenFile).Trim()
memory-principal.exe --principal management:owner --role human `
  --organization default --project default --actions '*' --expires-in 2160h
Remove-Item Env:MEMORY_PRINCIPAL_TOKEN
```

The default and maximum lifetime is 2160 hours (90 × 24 hours); the minimum is
one minute. An existing principal must use its current token when updating
project permissions. This does not rotate or extend its credential, even when
`--expires-in` is supplied. Use explicit rotation to replace a token.

## Inspect and rotate

Set `MEMORY_PRINCIPAL_TOKEN` to the current token and run:

```powershell
memory-principal.exe --principal management:owner --token-status
```

The JSON contains `principal_id`, `issued_at`, `expires_at`, `active`,
`rotate_before` and `needs_rotation`; it contains neither a secret nor a digest.
New tokens request rotation with 720 hours remaining. Already expired tokens
can still be inspected by this offline administrator command.

Prepare the new token and client configuration before the cutover. Stop workers
that use the old credential, then perform the rotation:

```powershell
$nextFile = Join-Path $adminDir 'owner-token.next'
memory-principal.exe --generate-token-file $nextFile
$env:MEMORY_PREVIOUS_PRINCIPAL_TOKEN = (Get-Content -Raw -LiteralPath $tokenFile).Trim()
$env:MEMORY_PRINCIPAL_TOKEN = (Get-Content -Raw -LiteralPath $nextFile).Trim()
memory-principal.exe --principal management:owner --rotate-token --expires-in 2160h
Remove-Item Env:MEMORY_PREVIOUS_PRINCIPAL_TOKEN, Env:MEMORY_PRINCIPAL_TOKEN
```

After success, switch clients and their private configuration to the new file,
then restart them. The old token stops working immediately; there is no grace
overlap. A rotation preserves all project grants and the principal role.
Concurrent rotations with the same predecessor cannot both win. Retrying an
already committed rotation with exactly the same old/new pair succeeds without
extending expiry. Retired digests still in history cannot be reused, including
under another principal. History stores SHA-256 digests and metadata, not tokens.

An expired predecessor can be rotated by the administrator. A revoked principal
cannot be rotated or automatically reactivated. Revocation remains:

```powershell
memory-principal.exe --principal management:owner --revoke
```

Keep both files until a failed/uncertain cutover is resolved. If a command reports
failure after a possible commit, retry the exact pair or inspect the new token;
do not generate a succession of replacements and discard the pending secret.

## Upgrade and managed desktop tokens

Migration 0003 preserves legacy digests and gives them 720 hours from migration
to rotate. Their original issuance time is unknown, so `issued_at` is null rather
than fabricated. They request immediate rotation. Re-running initialization
does not refresh this grace period. Existing DB migrations remain immutable.

Both service authorization and forced RLS consult the current, active,
unexpired credential. Expiry is checked at each SQL statement, including inside
a transaction that began earlier. SQL clocks and constraints use fixed hours
to avoid daylight-saving changes extending or shortening a lifetime.

For the built-in desktop database, startup inspects the managed owner and worker
tokens. When rotation is due it first saves a pending secret privately, stops
the local consumers, commits the database change, and records completion. A
restart can replay either side of a crash. Agent connection files are refreshed
before consumers resume. External/self-hosted principals require operator
rotation; the desktop does not possess their administrative connection.

Backups now use format 2 and preserve pending token state as well as current
tokens. Format 1 backups remain readable. Older apps reject format 2 rather
than restoring a snapshot whose current credential they cannot recover.
No operating database or installed application is changed merely by editing
this source. See the [Windows runtime validation record](work-records/2026-10-21-managed-token-runtime-check.md)
for actual packaging and validation status.
