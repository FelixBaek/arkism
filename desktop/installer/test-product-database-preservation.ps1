# Offline effects are explicit fakes: inert .exe text is never executed. No
# PostgreSQL, installer, registry, provider or real profile is opened.
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'smoke-windows-installed.ps1')
. (Join-Path $PSScriptRoot 'smoke-windows-product-uninstall.ps1')
. (Join-Path $PSScriptRoot 'product-database-preservation.ps1')
$suite = Join-Path ([IO.Path]::GetTempPath()) ('arkism database offline ' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($suite) | Out-Null
Write-Host "Only owned database preservation offline fixture root: $suite"
$script:cases = 0

function Assert-DatabaseTest([bool]$Value, [string]$Message) {
  if (-not $Value) { throw $Message }
}
function Write-DatabaseTestFile([string]$Path, [string]$Text) {
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path)) | Out-Null
  [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}
function New-DatabaseTestFixture {
  $parent = Join-Path $suite ([guid]::NewGuid().ToString('N'))
  $root = Join-Path $parent 'owned'
  [IO.Directory]::CreateDirectory($root) | Out-Null
  $owner = [guid]::NewGuid().ToString('N')
  Write-DatabaseTestFile (Join-Path $root '.installed-smoke-owner') $owner
  $context = @{ RunnerTemp = $parent; SourceSha = ('a' * 40); OwnedRoots = @{} }
  $context.OwnedRoots[$root] = $owner
  $state = @{ Context = $context; Root = $root; Install = (Join-Path $root 'installed Arkism')
    Stage = (Join-Path $parent 'stage'); Setup = (Join-Path $parent 'Arkism-0.1.6-setup.exe')
    Environment = (New-InstalledSmokeEnvironment $root); Calls = @(); Mode = '' }
  foreach ($name in @('postgres', 'pg_ctl', 'psql')) {
    Write-DatabaseTestFile (Join-Path $state.Stage "runtime/pgsql/bin/$name.exe") "inert $name fixture"
  }
  Write-DatabaseTestFile (Join-Path $state.Install 'Arkism.exe') 'inert app fixture'
  Write-DatabaseTestFile (Join-Path $root 'retained data/closed-memory.fixture') 'closed owned fixture'
  $state.Manifest = @([pscustomobject]@{ Path = 'Arkism.exe'; Sha256 = Get-InstalledSmokeHash (Join-Path $state.Install 'Arkism.exe') })
  $state.Manifest += @(Get-InstalledSmokeTree $state.Stage (Join-Path $state.Stage 'runtime'))
  return $state
}

# Explicit effect boundary fake, not GITHUB_* environment spoofing.
function Get-InstalledSmokeContext([string]$SourceSha) {
  if ($script:fixture.Mode -ceq 'host_denied') { throw 'smoke_hosted_windows_required' }
  Assert-DatabaseTest ($SourceSha -ceq $script:fixture.Context.SourceSha) 'fixture source mismatch'
  return $script:fixture.Context
}
function Invoke-InstalledSmokeProcess([string]$FilePath, [string]$Arguments, $Environment, [int]$TimeoutSeconds, [string]$Phase) {
  Assert-DatabaseTest (Test-InstalledSmokeWithin $FilePath $script:fixture.Root) 'fake executable must be owned'
  Assert-DatabaseTest ([IO.File]::ReadAllText($FilePath).StartsWith('inert ')) 'only inert tools may reach the fake runner'
  Assert-DatabaseTest ($Environment.HOME -ceq $script:fixture.Environment.HOME) 'isolated child environment changed'
  Assert-DatabaseTest ($TimeoutSeconds -ge 1) 'bounded process required'
  $script:fixture.Calls += $Phase
  if ($Phase -ceq 'database_fresh_runtime') {
    Assert-DatabaseTest ($Arguments -match '^--runtime-check "([^"]+)"$') 'explicit quoted fresh directory required'
    $data = $Matches[1]
    Assert-DatabaseTest (Test-InstalledSmokeWithin $data $script:fixture.Root) 'fresh fixture data must be owned'
    $checks = @{ expiry_rotation = $true; pending_before_commit = $true; pending_after_commit = $true
      pending_backup_restore = $true; pending_backup_restore_content = $true; retired_token_http_403 = $true }
    $result = @{ ok = $true; phase = 'complete'; capabilities = 200; restored_capabilities = 200; reused = $true
      backup_bytes = 128; expiry = 'ok'; safety_copy = $false; server = 'http://127.0.0.1:8091'; token_checks = $checks
      fixture_source_rows = 1; fixture_memory_rows = 1; fixture_sha256 = ('b' * 64); backup_restore_content = $true }
    if ($script:fixture.Mode -ceq 'runtime_fixture_missing') { $result.backup_restore_content = $false }
    Write-DatabaseTestFile (Join-Path $data 'check.json') ($result | ConvertTo-Json -Depth 5)
    Write-DatabaseTestFile (Join-Path $data '.runtime-check-owned') 'arkism-fresh-runtime-check-v2'
    $metadata = '{"pg_port":55491,"admin":"synthetic-isolated-password"}'
    if ($script:fixture.Mode -ceq 'metadata_json_private') {
      $metadata = '{"admin":{"synthetic-private-value-never-log":'
    }
    Write-DatabaseTestFile (Join-Path $data 'runtime.json') $metadata
    Write-DatabaseTestFile (Join-Path $data 'pg/PG_VERSION') '17'
  } elseif ($Phase -like '*_start') {
    Write-DatabaseTestFile (Join-Path $script:fixture.Database.Data 'pg/postmaster.pid') 'inert process marker'
  } elseif ($Phase -like '*_stop') {
    if ($script:fixture.Mode -ceq 'stop_failed') { throw 'synthetic_stop_failed' }
    $marker = Join-Path $script:fixture.Database.Data 'pg/postmaster.pid'
    Assert-DatabaseTest (Test-InstalledSmokeWithin $marker $script:fixture.Root) 'Only the inert owned marker may be removed'
    Write-Host "Removing only inert database process marker: $marker"
    [IO.File]::Delete($marker)
  } elseif ($Phase -like '*_rows') {
    Assert-DatabaseTest ($Environment.PGPASSWORD -ceq 'synthetic-isolated-password') 'DB credential belongs only in child env'
    Assert-DatabaseTest (-not $Arguments.Contains($Environment.PGPASSWORD)) 'DB password must not appear in argv'
    if ($script:fixture.Mode -cin @('query_failed', 'query_and_stop_failed')) {
      if ($script:fixture.Mode -ceq 'query_and_stop_failed') { $script:fixture.Mode = 'stop_failed' }
      throw 'synthetic_query_failed'
    }
    Assert-DatabaseTest ($Arguments -match '-o "([^"]+)"$') 'private output file required'
    $output = $Matches[1]
    $rows = "memories:synthetic-memory`nsources:synthetic-source`n"
    if ($script:fixture.Mode -ceq 'missing_rows') { $rows = "sources:synthetic-source`n" }
    if ($script:fixture.Mode -ceq 'changed_rows' -and $Phase -ceq 'database_after_rows') {
      $rows = "memories:changed-memory`nsources:synthetic-source`n"
    }
    Write-DatabaseTestFile $output $rows
  } else { throw "unexpected fake phase: $Phase" }
  return [pscustomobject]@{ Phase = $Phase; ExitCode = 0 }
}
function Test-DatabaseCase([string]$Name, [scriptblock]$Body) {
  $script:fixture = New-DatabaseTestFixture
  & $Body
  $script:cases++
  Write-Host "database preservation offline: $Name"
}
function Reject-DatabaseTest([scriptblock]$Body, [string]$Message) {
  $failed = $false; $observed = 'no failure'
  try { & $Body } catch { $observed = $_.Exception.Message; $failed = $observed.Contains($Message) }
  Assert-DatabaseTest $failed "expected failure: $Message; observed: $observed"
}
Test-DatabaseCase 'success requires a fresh fixture, stopped byte equality and logical rows' {
  $report = @{ database_preservation_tested = $false; product_uninstaller_tested = $true; product_postconditions = $true }
  New-ProductDatabaseFixture $script:fixture $report
  Assert-DatabaseTest (-not $report.database_preservation_tested) 'creating a fixture is not preservation evidence'
  Assert-ProductDatabasePreserved $script:fixture $report
  Assert-DatabaseTest ($report.database_preservation_tested -and $report.database_logical_rows_preserved -and $report.database_closed_bytes_preserved) 'all real postconditions must precede positive evidence'
  Assert-DatabaseTest (($script:fixture.Calls -join ',') -ceq 'database_fresh_runtime,database_before_start,database_before_rows,database_before_stop,database_after_start,database_after_rows,database_after_stop') 'query lifecycle must stop both clusters'
}
Test-DatabaseCase 'a fixture alone cannot claim actual uninstall preservation' {
  $report = @{ database_preservation_tested = $false }
  New-ProductDatabaseFixture $script:fixture $report
  Reject-DatabaseTest { Assert-ProductDatabasePreserved $script:fixture $report } 'product_database_removal_not_verified'
  Assert-DatabaseTest (-not $report.database_preservation_tested) 'uninstaller evidence must precede preservation evidence'
}
Test-DatabaseCase 'host rejection has zero effects' {
  $script:fixture.Mode = 'host_denied'
  Reject-DatabaseTest { New-ProductDatabaseFixture $script:fixture @{} } 'smoke_hosted_windows_required'
  Assert-DatabaseTest ($script:fixture.Calls.Count -eq 0) 'denied host must not reach a process'
}
Test-DatabaseCase 'a changed staged tool cannot replace the frozen payload manifest' {
  Write-DatabaseTestFile (Join-Path $script:fixture.Stage 'runtime/pgsql/bin/postgres.exe') 'inert changed staged tool'
  Reject-DatabaseTest { New-ProductDatabaseFixture $script:fixture @{} } 'product_manifest_changed'
  Assert-DatabaseTest ($script:fixture.Calls.Count -eq 0) 'unverified staged programs must not execute'
}
Test-DatabaseCase 'existing private row output is preserved before server start' {
  $report = @{ database_preservation_tested = $false; product_uninstaller_tested = $true; product_postconditions = $true }
  New-ProductDatabaseFixture $script:fixture $report
  $output = Join-Path $script:fixture.Database.Checks 'after.rows'
  Write-DatabaseTestFile $output 'preserve prior owned output'
  $before = $script:fixture.Calls.Count
  Reject-DatabaseTest { Assert-ProductDatabasePreserved $script:fixture $report } 'product_database_output_exists'
  Assert-DatabaseTest ($script:fixture.Calls.Count -eq $before -and [IO.File]::ReadAllText($output) -ceq 'preserve prior owned output') 'existing output must fail before any server or overwrite'
  Assert-DatabaseTest (-not $report.database_preservation_tested) 'blocked output must not produce preservation evidence'
}
Test-DatabaseCase 'missing durable runtime evidence fails before DB tools' {
  $script:fixture.Mode = 'runtime_fixture_missing'
  Reject-DatabaseTest { New-ProductDatabaseFixture $script:fixture @{} } 'runtime_fixture_evidence_failed'
  Assert-DatabaseTest ($script:fixture.Calls.Count -eq 1) 'missing seed must stop before PG start'
}
Test-DatabaseCase 'malformed private metadata emits only a fixed error code' {
  $script:fixture.Mode = 'metadata_json_private'
  $message = ''
  try { New-ProductDatabaseFixture $script:fixture @{} } catch { $message = $_.Exception.Message }
  Assert-DatabaseTest ($message -ceq 'product_database_metadata_json_invalid') 'Private JSON parser details must be replaced with a fixed code'
  Assert-DatabaseTest (-not $message.Contains('synthetic-private-value-never-log')) 'Private metadata must not appear in the propagated error'
  Assert-DatabaseTest ($script:fixture.Calls.Count -eq 1) 'Invalid metadata must stop before PG tools execute'
}
foreach ($mode in @('query_failed', 'missing_rows')) {
  Test-DatabaseCase "$mode still stops the owned server" {
    $script:fixture.Mode = $mode
    $message = if ($mode -ceq 'query_failed') { 'synthetic_query_failed' } else { 'product_database_fixture_missing' }
    Reject-DatabaseTest { New-ProductDatabaseFixture $script:fixture @{} } $message
    Assert-DatabaseTest ($script:fixture.Calls[-1] -ceq 'database_before_stop') 'query failure must stop owned PG'
  }
}
Test-DatabaseCase 'query and stop failures retain both causes' {
  $script:fixture.Mode = 'query_and_stop_failed'
  try { New-ProductDatabaseFixture $script:fixture @{}; throw 'expected failure' }
  catch {
    Assert-DatabaseTest ($_.Exception.Message.Contains('synthetic_query_failed') -and $_.Exception.Message.Contains('synthetic_stop_failed')) 'cleanup must not hide query failure'
  }
}
foreach ($mode in @('tool_changed', 'bytes_changed', 'changed_rows', 'running', 'path_changed')) {
  Test-DatabaseCase "$mode cannot produce preservation evidence" {
    $report = @{ database_preservation_tested = $false; product_uninstaller_tested = $true; product_postconditions = $true }
    New-ProductDatabaseFixture $script:fixture $report
    switch ($mode) {
      tool_changed { Write-DatabaseTestFile (Join-Path $script:fixture.Database.Tools 'bin/pg_ctl.exe') 'inert changed tool' }
      bytes_changed { Write-DatabaseTestFile (Join-Path $script:fixture.Database.Data 'pg/PG_VERSION') 'changed cluster' }
      changed_rows { $script:fixture.Mode = 'changed_rows' }
      running { Write-DatabaseTestFile (Join-Path $script:fixture.Database.Data 'pg/postmaster.pid') 'inert running marker' }
      path_changed { $script:fixture.Database.Data = Join-Path $script:fixture.Root 'unowned data' }
    }
    $message = @{ tool_changed = 'product_manifest_changed'; bytes_changed = 'product_manifest_changed'
      changed_rows = 'product_database_rows_changed'; running = 'product_database_still_running'; path_changed = 'product_database_path_changed' }[$mode]
    Reject-DatabaseTest { Assert-ProductDatabasePreserved $script:fixture $report } $message
    Assert-DatabaseTest (-not $report.database_preservation_tested) 'failed check must leave tested=false'
  }
}
# The printed root is unique, owned inert test output. Inspect before deletion;
# failures retain it for diagnosis instead of removing an unverified tree.
foreach ($child in [IO.Directory]::EnumerateDirectories($suite)) {
  $owned = Join-Path $child 'owned'
  Assert-InstalledSmokeDirectory $owned
  Assert-InstalledSmokeFile (Join-Path $owned '.installed-smoke-owner')
  Assert-InstalledSmokeTree $child
}
Write-Host "Removing only owned database preservation offline fixture root: $suite"
[IO.Directory]::Delete($suite, $true)
Write-Host "$script:cases database preservation offline cases passed"
