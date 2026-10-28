# Shared by staged and installed runtime checks. Old/stale runtime binaries
# must not pass a new release gate without token and durable-record checks.
function Assert-RuntimeTokenEvidence($Result) {
  foreach ($name in @('expiry_rotation', 'pending_before_commit', 'pending_after_commit', 'pending_backup_restore', 'pending_backup_restore_content', 'retired_token_http_403')) {
    $value = $Result.token_checks.$name
    if ($value -isnot [bool] -or -not $value) { throw "runtime_token_evidence_failed: $name" }
  }
  foreach ($name in @('fixture_source_rows', 'fixture_memory_rows')) {
    $value = $Result.$name
    if (($value -isnot [int] -and $value -isnot [long]) -or $value -ne 1) { throw "runtime_fixture_evidence_failed: $name" }
  }
  if ($Result.fixture_sha256 -isnot [string] -or $Result.fixture_sha256 -cnotmatch '\A[0-9a-f]{64}\z') {
    throw 'runtime_fixture_evidence_failed: fixture_sha256'
  }
  if ($Result.backup_restore_content -isnot [bool] -or -not $Result.backup_restore_content) {
    throw 'runtime_fixture_evidence_failed: backup_restore_content'
  }
}
