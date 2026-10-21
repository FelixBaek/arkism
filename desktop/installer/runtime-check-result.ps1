# Shared by staged and installed runtime checks. Old/stale runtime binaries
# must not pass a new release gate without running the token lifecycle cases.
function Assert-RuntimeTokenEvidence($Result) {
  foreach ($name in @('expiry_rotation', 'pending_before_commit', 'pending_after_commit', 'pending_backup_restore', 'retired_token_http_403')) {
    $value = $Result.token_checks.$name
    if ($value -isnot [bool] -or -not $value) { throw "runtime_token_evidence_failed: $name" }
  }
}
