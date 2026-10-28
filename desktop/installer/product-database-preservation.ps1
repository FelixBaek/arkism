# Real PostgreSQL checks for the hosted product-removal coordinator. Dot-sourcing
# defines functions only. Every effect requires the existing hosted/owned guards.
function Assert-ProductDatabaseContext($State) {
  $context = Get-InstalledSmokeContext $State.Context.SourceSha
  if ($context.SourceSha -cne $State.Context.SourceSha -or $context.RunnerTemp -cne $State.Context.RunnerTemp) {
    throw 'product_database_context_changed'
  }
  Assert-InstalledSmokeOwnedRoot $State.Context $State.Root
  Assert-ProductPathLayout $State
  Assert-ProductEnvironment $State
  if ($State.Database.Data -cne (Join-Path $State.Root 'retained data/runtime data') -or
      $State.Database.Tools -cne (Join-Path $State.Root 'database tools') -or
      $State.Database.Checks -cne (Join-Path $State.Root 'database checks')) { throw 'product_database_path_changed' }
}

function Assert-ProductDatabaseStopped($State) {
  Assert-ProductDatabaseContext $State
  if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $State.Database.Data 'pg/postmaster.pid'))) {
    throw 'product_database_still_running'
  }
}

function Invoke-ProductDatabaseTool($State, [string]$Name, [string]$Arguments, [string]$Phase) {
  Assert-ProductDatabaseContext $State
  if ($Name -cnotin @('pg_ctl', 'psql')) { throw 'product_database_tool_invalid' }
  $relative = "bin/$Name.exe"
  $rows = @($State.Database.ToolsManifest | Where-Object { $_.Path -ceq $relative })
  $exe = Join-Path $State.Database.Tools $relative
  if ($rows.Count -ne 1 -or (Get-InstalledSmokeHash $exe) -cne $rows[0].Sha256) {
    throw 'product_database_tool_changed'
  }
  $environment = $State.Environment.Clone()
  # Generated, isolated database credential: never print it or pass it as argv.
  $environment['PGPASSWORD'] = $State.Database.Password
  [void](Invoke-InstalledSmokeProcess $exe $Arguments $environment 60 $Phase)
}

function Get-ProductDatabaseRows($State, [string]$Phase) {
  if ($Phase -cnotin @('before', 'after')) { throw 'product_database_phase_invalid' }
  Assert-ProductDatabaseStopped $State
  Assert-ProductManifest $State.Database.ToolsManifest @(Get-InstalledSmokeTree $State.Database.Tools $State.Database.Tools) 'database_tools_copy'
  $data = Join-Path $State.Database.Data 'pg'
  $port = $State.Database.Port
  $file = Join-Path $State.Database.Checks "$Phase.rows"
  $log = Join-Path $State.Database.Checks "$Phase.postgres.log"
  if ($null -ne (Get-InstalledSmokeAttributes $file) -or $null -ne (Get-InstalledSmokeAttributes $log)) {
    throw 'product_database_output_exists'
  }
  $attempted = $false
  $primary = $null; $cleanup = $null; $hash = $null
  try {
    $attempted = $true
    Invoke-ProductDatabaseTool $State 'pg_ctl' ('start -D "' + $data + '" -l "' + $log + '" -o "-h 127.0.0.1 -p ' + $port + '" -w -t 30') "database_${Phase}_start"
    # All fixture fields, including IDs/revisions/approval/source links and scope,
    # are compared privately. No record bodies are emitted to CI logs/artifacts.
    $sql = "SELECT collection||':'||doc::text FROM arkism.memory_records WHERE collection IN ('sources','memories') ORDER BY collection,id"
    Invoke-ProductDatabaseTool $State 'psql' ('-X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -p ' + $port + ' -U postgres -d arkism -c "' + $sql + '" -o "' + $file + '"') "database_${Phase}_rows"
    Assert-InstalledSmokeFile $file
    if (([IO.FileInfo]$file).Length -gt 65536) { throw 'product_database_rows_too_large' }
    $lines = @([IO.File]::ReadAllLines($file) | Where-Object { $_.Length })
    if ($lines.Count -ne 2 -or @($lines | Where-Object { $_.StartsWith('sources:') }).Count -ne 1 -or
        @($lines | Where-Object { $_.StartsWith('memories:') }).Count -ne 1) { throw 'product_database_fixture_missing' }
    $hash = Get-InstalledSmokeHash $file
  } catch { $primary = $_
  } finally {
    if ($attempted) {
      try {
        if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $data 'postmaster.pid'))) {
          Invoke-ProductDatabaseTool $State 'pg_ctl' ('stop -D "' + $data + '" -m fast -w -t 30') "database_${Phase}_stop"
        }
        Assert-ProductDatabaseStopped $State
      } catch { $cleanup = $_ }
    }
  }
  Assert-InstalledSmokeOutcome $primary $cleanup $null
  return $hash
}

function New-ProductDatabaseFixture($State, $Report) {
  if ($State.ContainsKey('Database')) { throw 'product_database_already_prepared' }
  $State.Database = @{ Data = (Join-Path $State.Root 'retained data/runtime data')
    Tools = (Join-Path $State.Root 'database tools'); Checks = (Join-Path $State.Root 'database checks') }
  Assert-ProductDatabaseContext $State
  foreach ($path in @($State.Database.Data, $State.Database.Tools, $State.Database.Checks)) {
    if ($null -ne (Get-InstalledSmokeAttributes $path)) { throw 'product_database_path_exists' }
  }
  [IO.Directory]::CreateDirectory($State.Database.Checks) | Out-Null
  $source = Join-Path $State.Stage 'runtime/pgsql'
  Assert-InstalledSmokeTree $source
  $expected = @(Get-InstalledSmokeTree $source $source | Sort-Object Path)
  $prefix = 'runtime/pgsql/'
  $frozen = @($State.Manifest | Where-Object { $_.Path.StartsWith($prefix, [StringComparison]::Ordinal) } |
    ForEach-Object { [pscustomobject]@{ Path = $_.Path.Substring($prefix.Length); Sha256 = $_.Sha256 } })
  Assert-ProductManifest $frozen $expected 'database_tools_copy'
  Copy-Item -LiteralPath $source -Destination $State.Database.Tools -Recurse
  $State.Database.ToolsManifest = @(Get-InstalledSmokeTree $State.Database.Tools $State.Database.Tools | Sort-Object Path)
  Assert-ProductManifest $expected $State.Database.ToolsManifest 'database_tools_copy'
  foreach ($name in @('postgres', 'pg_ctl', 'psql')) {
    if ("bin/$name.exe" -cnotin $State.Database.ToolsManifest.Path) { throw 'product_database_tool_missing' }
  }
  $app = Join-Path $State.Install 'Arkism.exe'
  $appRow = @($State.Manifest | Where-Object { $_.Path -ceq 'Arkism.exe' })
  if ($appRow.Count -ne 1 -or (Get-InstalledSmokeHash $app) -cne $appRow[0].Sha256) { throw 'product_database_app_changed' }
  [void](Invoke-InstalledSmokeProcess $app ('--runtime-check "' + $State.Database.Data + '"') $State.Environment 300 'database_fresh_runtime')
  $resultPath = Join-Path $State.Database.Data 'check.json'
  [void](Read-InstalledSmokeResult $resultPath)
  $marker = Join-Path $State.Database.Data '.runtime-check-owned'
  Assert-InstalledSmokeFile $marker
  if ([IO.File]::ReadAllText($marker) -cne 'arkism-fresh-runtime-check-v2') { throw 'product_database_marker_invalid' }
  $secretsPath = Join-Path $State.Database.Data 'runtime.json'
  Assert-InstalledSmokeFile $secretsPath
  if (([IO.FileInfo]$secretsPath).Length -gt 65536) { throw 'product_database_metadata_too_large' }
  try {
    $secrets = [IO.File]::ReadAllText($secretsPath) | ConvertFrom-Json -ErrorAction Stop
  } catch {
    throw 'product_database_metadata_json_invalid'
  }
  if ($secrets.pg_port -isnot [int] -or $secrets.pg_port -le 1024 -or $secrets.pg_port -gt 65535 -or
      $secrets.admin -isnot [string] -or $secrets.admin.Length -lt 16) { throw 'product_database_metadata_invalid' }
  $State.Database.Port = $secrets.pg_port; $State.Database.Password = $secrets.admin
  $State.Database.RowsHash = Get-ProductDatabaseRows $State 'before'
  # PostgreSQL has stopped before taking a byte-level preservation snapshot.
  $State.Data = @(Get-InstalledSmokeTree $State.Root (Join-Path $State.Root 'retained data'))
  $Report.database_fixture_source_rows = 1; $Report.database_fixture_memory_rows = 1
  $Report.database_rows_sha256 = $State.Database.RowsHash
  $Report.database_fixture_created = $true
}

function Invoke-ProductDatabaseReinstall($State, $Request, $Report) {
  Assert-ProductDatabaseStopped $State
  Assert-ProductUninstallInputs $State
  $before = Get-ProductRebootSnapshot $Request
  [void](Invoke-InstalledSmokeProcess $State.Setup ("/S /NoAutoRun /D=$($State.Install)") $State.Environment 180 'database_same_candidate_reinstall')
  [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($before, (Get-ProductRebootSnapshot $Request))
  Assert-InstalledSmokePayload $State.Install $State.Manifest
  if ((Get-InstalledSmokeHash (Join-Path $State.Install 'uninstall.exe')) -cne $State.UninstallerHash) { throw 'product_database_uninstaller_changed' }
  Assert-ProductManifest $State.Data @(Get-InstalledSmokeTree $State.Root (Join-Path $State.Root 'retained data')) 'database_after_reinstall'
  # Re-created shortcut bytes may differ. Rebind only those known shortcuts;
  # every other existing profile file must remain identical.
  $actual = @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME | Sort-Object Path)
  $shortcuts = @(Get-ProductProfileShortcutRecords $State (Get-InstalledSmokeGlobalState $State.Context) $actual)
  $names = @($State.ProfileShortcutRecords | ForEach-Object { $_.Path })
  Assert-ProductManifest @($State.Profile | Where-Object { $_.Path -cnotin $names }) @($actual | Where-Object { $_.Path -cnotin $names }) 'database_profile_after_reinstall'
  $updated = @($State.SetupProfile | Where-Object { $_.Path -cnotin $names }) + @($actual | Where-Object { $_.Path -cin $names })
  $State.SetupProfile = @($updated | Sort-Object Path); $State.ProfileShortcutRecords = $shortcuts
  Set-ProductProfileBaseline $State
  Assert-ProductUninstallInputs $State
  $Report.same_candidate_reinstall_tested = $true
  $Report.cross_version_upgrade_tested = $false
}

function Assert-ProductDatabasePreserved($State, $Report) {
  if ($Report.product_uninstaller_tested -isnot [bool] -or -not $Report.product_uninstaller_tested -or
      $Report.product_postconditions -isnot [bool] -or -not $Report.product_postconditions) {
    throw 'product_database_removal_not_verified'
  }
  Assert-ProductDatabaseStopped $State
  # The removal coordinator has already checked the closed files. Repeat before
  # opening the cluster: starting PostgreSQL legitimately changes WAL/control files.
  Assert-ProductManifest $State.Data @(Get-InstalledSmokeTree $State.Root (Join-Path $State.Root 'retained data')) 'database_after_uninstall'
  $Report.database_closed_bytes_preserved = $true
  if ((Get-ProductDatabaseRows $State 'after') -cne $State.Database.RowsHash) { throw 'product_database_rows_changed' }
  $Report.database_logical_rows_preserved = $true
  $Report.database_preservation_tested = $true
  $Report.database_scope = 'synthetic_arkism_source_and_memory_in_owned_diagnostic_directory'
}
