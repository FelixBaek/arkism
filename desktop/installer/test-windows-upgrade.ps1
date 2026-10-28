# Inert boundary regressions only: no installer, DB, credentials or registry API.
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'smoke-windows-installed.ps1')
. (Join-Path $PSScriptRoot 'smoke-windows-upgrade.ps1')
$script:UpgradePassed = 0
function Check-Upgrade([bool]$Value, [string]$Code) { if (-not $Value) { throw $Code } }
function Reject-Upgrade([scriptblock]$Body, [string]$Code) {
  $caught = $false
  try { & $Body } catch { $caught = $true }
  if (-not $caught) { throw $Code }
}
function Test-UpgradeCase([string]$Name, [scriptblock]$Body) {
  & $Body; $script:UpgradePassed++; Write-Output "PASS $Name"
}
$owner = [Guid]::NewGuid().ToString('N')
$root = Join-Path ([IO.Path]::GetTempPath()) ('arkism-upgrade-offline-' + $owner)
if ([IO.Directory]::Exists($root) -or [IO.File]::Exists($root)) { throw 'upgrade_fixture_root_exists' }
[IO.Directory]::CreateDirectory($root) | Out-Null
[IO.File]::WriteAllText((Join-Path $root '.upgrade-offline-owner'), $owner)
$script:UpgradeFixtureRoot = $root

Test-UpgradeCase 'published baseline rejects an unrelated filename' {
  $p = Join-Path $root 'unrelated.exe'; [IO.File]::WriteAllText($p, 'inert; never executed')
  Reject-Upgrade { Assert-UpgradeBaseline $p } 'unrelated_baseline_accepted'
}
Test-UpgradeCase 'published baseline rejects truncated installer bytes' {
  $p = Join-Path $root 'Arkism-0.1.4-setup.exe'; [IO.File]::WriteAllText($p, 'inert; never executed')
  Reject-Upgrade { Assert-UpgradeBaseline $p } 'truncated_baseline_accepted'
}
Test-UpgradeCase 'owner API accepts only a plain numeric loopback endpoint' {
  Assert-UpgradeLoopback 'http://127.0.0.1:34567'
  foreach ($uri in @('https://127.0.0.1:34567','http://localhost:34567','http://127.0.0.2:34567',
    'http://127.0.0.1:80','http://user:secret@127.0.0.1:34567','http://127.0.0.1:34567/path',
    'http://127.0.0.1:34567/?token=synthetic','http://127.0.0.1:34567/#fragment','http://example.invalid:34567')) {
    Reject-Upgrade { Assert-UpgradeLoopback $uri } 'non_loopback_owner_endpoint_accepted'
  }
}
Test-UpgradeCase 'unsafe API path rejects before issuing a request' {
  Reject-Upgrade { Invoke-UpgradeRequest 'http://127.0.0.1:34567' 'inert' '/v1/sources"bad' $null 200 } 'unsafe_api_path_accepted'
}

# Isolate only the metadata parser below. The real hosted guard is not spoofed,
# and none of the orchestration/process functions is invoked by these cases.
function Assert-UpgradeContext($State) {
  if ($State.Data -cne (Join-Path $script:UpgradeFixtureRoot 'data') -or
      [IO.File]::ReadAllText((Join-Path $script:UpgradeFixtureRoot '.upgrade-offline-owner')) -cne $owner) {
    throw 'upgrade_inert_fixture_not_owned'
  }
}
$state = @{Data=(Join-Path $root 'data')}; [IO.Directory]::CreateDirectory($state.Data)|Out-Null
$metadata = @{admin=('a'*64);app=('b'*64);init=('c'*64);owner=('d'*64);archive_key=('e'*64);pg_port=34567;api_port=34568}
$path = Join-Path $state.Data 'runtime.json'
function Write-UpgradeMetadata($Value) { [IO.File]::WriteAllText($path,($Value|ConvertTo-Json -Compress)) }
Test-UpgradeCase 'synthetic owned metadata is read without exposing credentials' {
  Write-UpgradeMetadata $metadata
  $value = Get-UpgradeMetadata $state
  Check-Upgrade ($value.pg_port -eq 34567 -and $value.api_port -eq 34568) 'metadata_not_read'
}
Test-UpgradeCase 'malformed or missing database credentials are rejected' {
  foreach ($field in @('admin','app','init','owner','archive_key')) {
    $bad=$metadata.Clone();$bad[$field]='not-a-secret-and-not-valid'
    Write-UpgradeMetadata $bad
    Reject-Upgrade { Get-UpgradeMetadata $state } 'malformed_metadata_accepted'
  }
}
Test-UpgradeCase 'malformed private metadata never leaks JSON credential contents in an error' {
  $private = 'never-log-this-private-fixture-value'
  [IO.File]::WriteAllText($path, ('{"owner":"' + $private + '","broken":'))
  $errorText = ''
  try { Get-UpgradeMetadata $state } catch { $errorText = $_.Exception.Message }
  Check-Upgrade ($errorText -ceq 'upgrade_metadata_json_invalid' -and -not $errorText.Contains($private)) 'metadata_error_exposed_private_body'
}
Test-UpgradeCase 'invalid port kinds and ranges cannot reach a listener' {
  foreach ($port in @('34567',0,80,65536)) {
    $bad=$metadata.Clone();$bad.pg_port=$port;Write-UpgradeMetadata $bad
    Reject-Upgrade { Get-UpgradeMetadata $state } 'unsafe_port_accepted'
  }
}
Test-UpgradeCase 'unowned metadata directory is rejected before file access' {
  Reject-Upgrade { Get-UpgradeMetadata @{Data=(Join-Path $root 'other')} } 'unowned_metadata_accepted'
}
Test-UpgradeCase 'raw legacy timestamps and review JSON survive fixture capture byte-for-byte' {
  $state.Checks = Join-Path $root 'upgrade checks'; [IO.Directory]::CreateDirectory($state.Checks)|Out-Null
  $source = '{"id":"source-fixture","collected_at":"2026-10-22T04:14:00Z"}'
  $memory = '{"id":"memory-fixture","review":{"at":"2026-10-22T04:14:00.1234567+00:00","person":true}}'
  Write-UpgradeFixture $state $source $memory
  $raw = [IO.File]::ReadAllText((Join-Path $state.Checks 'legacy-fixture.json'))
  Check-Upgrade ($raw -ceq ('{"source":' + $source + ',"memory":' + $memory + '}')) 'fixture_json_reserialized'
  $bytes = [IO.File]::ReadAllBytes((Join-Path $state.Checks 'legacy-fixture.json'))
  Check-Upgrade ($bytes[0] -eq 123) 'fixture_json_has_bom'
}
Test-UpgradeCase 'fixture capture refuses preexisting data without modifying it' {
  $file = Join-Path $state.Checks 'legacy-fixture.json'; $before = [IO.File]::ReadAllText($file)
  Reject-Upgrade { Write-UpgradeFixture $state '{"changed":true}' '{"changed":true}' } 'fixture_overwrite_accepted'
  Check-Upgrade ([IO.File]::ReadAllText($file) -ceq $before) 'existing_fixture_modified'
}
Test-UpgradeCase 'fixture capture rejects malformed or non-object API responses' {
  foreach ($raw in @('not json','null','[]','1','"text"')) {
    Reject-Upgrade { Write-UpgradeFixture $state $raw '{}' } 'malformed_fixture_accepted'
  }
}
Test-UpgradeCase 'private API and fixture JSON errors contain only fixed codes' {
  $private = 'private-record-body-must-not-appear'
  foreach ($kind in @('api','fixture')) {
    foreach ($raw in @(('{"text":"' + $private + '","broken":'), ('"' + $private + '"'), ('[{"text":"' + $private + '"}]'))) {
      $message = ''
      try { ConvertFrom-UpgradeRecordJson $raw $kind | Out-Null } catch { $message = $_.Exception.Message }
      Check-Upgrade ($message -cin @(('upgrade_' + $kind + '_json_invalid'),('upgrade_' + $kind + '_json_object_required')) -and
        -not $message.Contains($private)) 'private_record_body_exposed'
    }
  }
}
Test-UpgradeCase 'actual fixture validation does not expose malformed private input' {
  $private = 'private-fixture-source-must-not-appear'
  $message = ''
  try { Write-UpgradeFixture $state ('{"text":"' + $private + '","broken":') '{}' }
  catch { $message = $_.Exception.Message }
  Check-Upgrade ($message -ceq 'upgrade_fixture_json_invalid' -and -not $message.Contains($private)) 'fixture_parse_body_exposed'
}
Write-Output "All $script:UpgradePassed upgrade boundary cases passed; native installer/DB/registry calls: 0. Owned inert fixtures retained."
