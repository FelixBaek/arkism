# Published 0.1.4 -> candidate upgrade with synthetic records only. The parent
# profile, provider credentials and normal GUI are never used. Dot-sourcing is inert.
param([string]$BaselineInstallerPath, [string]$InstallerPath, [string]$StagePath,
      [string]$Version, [string]$SourceSha, [string]$EvidencePath)

function Get-UpgradeBaseline {
  return @{ Version = '0.1.4'; SourceSha = '7bbbd879633bb5833b317f1e79ae079881fe7840'
    Sha256 = '45f561595ae5fef7d236b7f913062b951c3bda139781f3540b5d033d6d3b31aa'
    Bytes = [long]77956340 }
}

function Assert-UpgradeBaseline([string]$Path) {
  Assert-InstalledSmokeFile $Path
  $pin = Get-UpgradeBaseline
  if ([IO.Path]::GetFileName($Path) -cne "Arkism-$($pin.Version)-setup.exe" -or
      ([IO.FileInfo]$Path).Length -ne $pin.Bytes -or (Get-InstalledSmokeHash $Path) -cne $pin.Sha256) {
    throw 'upgrade_baseline_hash_mismatch'
  }
  return $pin
}

function Assert-UpgradeContext($State) {
  $fresh = Get-InstalledSmokeContext $State.Context.SourceSha
  if ($fresh.SourceSha -cne $State.Context.SourceSha -or $fresh.RunnerTemp -cne $State.Context.RunnerTemp) {
    throw 'upgrade_context_changed'
  }
  Assert-InstalledSmokeOwnedRoot $State.Context $State.Root
  if ($State.Install -cne (Join-Path $State.Root 'installed Arkism') -or
      $State.Data -cne (Join-Path $State.Root 'retained data/runtime data') -or
      $State.Checks -cne (Join-Path $State.Root 'upgrade checks') -or
      $State.Tools -cne (Join-Path $State.Root 'database tools')) { throw 'upgrade_path_changed' }
  if ($State.Environment.Count -ne (New-InstalledSmokeEnvironment $State.Root).Count) { throw 'upgrade_environment_changed' }
  $expected = New-InstalledSmokeEnvironment $State.Root
  foreach ($name in $expected.Keys) {
    if ($State.Environment[$name] -cne $expected[$name]) { throw 'upgrade_environment_changed' }
  }
}

function Assert-UpgradeLoopback([string]$Server) {
  $uri = $null
  if (-not [Uri]::TryCreate($Server, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -cne 'http' -or
      $uri.Host -cne '127.0.0.1' -or $uri.Port -le 1024 -or $uri.UserInfo -or
      $uri.AbsolutePath -cne '/' -or $uri.Query -or $uri.Fragment) { throw 'upgrade_loopback_required' }
}

function ConvertFrom-UpgradeRecordJson([string]$Raw, [string]$Kind) {
  if ($Kind -cnotin @('api', 'fixture')) { throw 'upgrade_json_context_invalid' }
  try { $value = $Raw | ConvertFrom-Json -ErrorAction Stop }
  catch { throw ('upgrade_' + $Kind + '_json_invalid') }
  # PS pipelines unwrap a one-element JSON array. Check the original root token
  # as well, so an array cannot masquerade as a single record object.
  if (-not $Raw.TrimStart().StartsWith('{', [StringComparison]::Ordinal) -or $value -isnot [pscustomobject]) {
    throw ('upgrade_' + $Kind + '_json_object_required')
  }
  return $value
}

function Invoke-UpgradeRequest([string]$Server, [string]$Token, [string]$Path, $Body, [int]$ExpectedStatus, [switch]$RawResult) {
  Assert-UpgradeLoopback $Server
  if ($Path -notmatch '\A/v1/[A-Za-z0-9_/?%=&:.-]+\z') { throw 'upgrade_api_path_invalid' }
  $request = [Net.HttpWebRequest]::Create($Server.TrimEnd('/') + $Path)
  $request.Proxy = $null; $request.AllowAutoRedirect = $false; $request.Timeout = 15000; $request.ReadWriteTimeout = 15000
  $request.Headers['Authorization'] = 'Bearer ' + $Token
  if ($null -ne $Body) {
    $request.Method = 'POST'; $request.ContentType = 'application/json'
    $bytes = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 24 -Compress))
    $request.ContentLength = $bytes.Length
    $stream = $request.GetRequestStream()
    try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
  }
  $response = $null
  try {
    try { $response = $request.GetResponse() } catch { throw 'upgrade_api_request_failed' }
    if ([int]$response.StatusCode -ne $ExpectedStatus) { throw 'upgrade_api_status_failed' }
    $reader = New-Object IO.StreamReader($response.GetResponseStream(), [Text.Encoding]::UTF8)
    try {
      $buffer = New-Object char[] 262145
      $count = $reader.ReadBlock($buffer, 0, $buffer.Length)
      if ($count -ge $buffer.Length) { throw 'upgrade_api_response_limit' }
      $text = [string]::new($buffer, 0, $count)
      $value = ConvertFrom-UpgradeRecordJson $text 'api'
      if ($RawResult) { return $text }; return $value
    } finally { $reader.Dispose() }
  } finally { if ($null -ne $response) { $response.Dispose() } }
}

function Write-UpgradeFixture($State, [string]$SourceJson, [string]$MemoryJson) {
  Assert-UpgradeContext $State
  foreach ($raw in @($SourceJson, $MemoryJson)) {
    if ($raw.Length -gt 262144) { throw 'upgrade_fixture_json_limit' }
    [void](ConvertFrom-UpgradeRecordJson $raw 'fixture')
  }
  $path = Join-Path $State.Checks 'legacy-fixture.json'
  Assert-InstalledSmokeDirectory $State.Checks
  # Keep original ISO8601/review strings, not JSON.NET's date reserialization.
  $bytes = [Text.UTF8Encoding]::new($false).GetBytes(('{"source":' + $SourceJson + ',"memory":' + $MemoryJson + '}'))
  $stream = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
  try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
}

function Invoke-UpgradeProcess([string]$FilePath, [string]$Arguments, $Environment, [int]$TimeoutSeconds, [string]$Phase) {
  try { return (Invoke-InstalledSmokeProcess $FilePath $Arguments $Environment $TimeoutSeconds $Phase) }
  catch { throw ('upgrade_process_failed:' + $Phase) }
}

function Get-UpgradeMetadata($State) {
  Assert-UpgradeContext $State
  $file = Join-Path $State.Data 'runtime.json'
  Assert-InstalledSmokeFile $file
  if (([IO.FileInfo]$file).Length -gt 65536) { throw 'upgrade_metadata_limit' }
  try { $value = [IO.File]::ReadAllText($file) | ConvertFrom-Json -ErrorAction Stop } catch { throw 'upgrade_metadata_json_invalid' }
  foreach ($field in @('admin', 'app', 'init', 'owner')) {
    if ($value.$field -isnot [string] -or $value.$field -notmatch '\A[a-f0-9]{32,128}\z') { throw 'upgrade_metadata_invalid' }
  }
  foreach ($field in @('pg_port', 'api_port')) {
    if (-not (Test-InstalledSmokeInteger $value.$field) -or $value.$field -le 1024 -or $value.$field -gt 65535) { throw 'upgrade_metadata_invalid' }
  }
  if ($value.archive_key -isnot [string] -or $value.archive_key -notmatch '\A[a-f0-9]{64}\z') { throw 'upgrade_metadata_invalid' }
  return $value
}

function Invoke-UpgradePg($State, [string]$Action) {
  Assert-UpgradeContext $State
  if ($Action -cnotin @('start', 'stop')) { throw 'upgrade_pg_action_invalid' }
  $exe = Join-Path $State.Tools 'bin/pg_ctl.exe'
  $row = @($State.ToolsManifest | Where-Object { $_.Path -ceq 'bin/pg_ctl.exe' })
  if ($row.Count -ne 1 -or (Get-InstalledSmokeHash $exe) -cne $row[0].Sha256) { throw 'upgrade_pg_tool_changed' }
  $pg = Join-Path $State.Data 'pg'
  $environment = $State.Environment.Clone(); $environment['PGPASSWORD'] = $State.Secrets.admin
  if ($Action -ceq 'start') {
    if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $pg 'postmaster.pid'))) { throw 'upgrade_pg_already_running' }
    $log = Join-Path $State.Checks 'legacy-postgres.log'
    [void](Invoke-UpgradeProcess $exe ('start -D "' + $pg + '" -l "' + $log + '" -o "-h 127.0.0.1 -p ' + $State.Secrets.pg_port + '" -w -t 30') $environment 60 'upgrade_legacy_pg_start')
  } else {
    [void](Invoke-UpgradeProcess $exe ('stop -D "' + $pg + '" -m fast -w -t 30') $environment 60 'upgrade_legacy_pg_stop')
  }
}

function Assert-UpgradeStopped($State) {
  Assert-UpgradeContext $State
  if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $State.Data 'pg/postmaster.pid'))) { throw 'upgrade_database_still_running' }
}

function New-UpgradeFixture($State) {
  Assert-UpgradeStopped $State
  $program = Join-Path $State.Install 'runtime/memoryd.exe'
  $row = @($State.LegacyManifest | Where-Object { $_.Path -ceq 'runtime/memoryd.exe' })
  if ($row.Count -ne 1 -or (Get-InstalledSmokeHash $program) -cne $row[0].Sha256) { throw 'upgrade_legacy_server_changed' }
  $environment = $State.Environment.Clone()
  $environment['MEMORY_STORE'] = 'postgres'
  $environment['MEMORY_DATABASE_URL'] = 'postgres://arkism_app:' + [Uri]::EscapeDataString($State.Secrets.app) + '@127.0.0.1:' + $State.Secrets.pg_port + '/arkism?sslmode=disable'
  $environment['MEMORY_EMBEDDER'] = 'none'; $environment['MEMORY_LOCAL_STANDALONE'] = 'true'
  $environment['MEMORY_PRIVACY_NO_AGENT_RUNTIME'] = 'true'; $environment['MEMORY_SESSION_ARCHIVE_KEY'] = $State.Secrets.archive_key
  $listen = '127.0.0.1:' + $State.Secrets.api_port
  $server = 'http://' + $listen
  $process = $null; $pgAttempted = $false; $primary = $null; $cleanup = $null
  try {
    $pgAttempted = $true; Invoke-UpgradePg $State 'start'
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $program; $info.Arguments = '--listen ' + $listen; $info.UseShellExecute = $false
    $info.CreateNoWindow = $true; $info.WorkingDirectory = $State.Install
    $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true; $info.EnvironmentVariables.Clear()
    foreach ($name in $environment.Keys) { $info.EnvironmentVariables[$name] = $environment[$name] }
    $process = [Diagnostics.Process]::Start($info)
    $stdout = $process.StandardOutput.ReadToEndAsync(); $stderr = $process.StandardError.ReadToEndAsync()
    $ready = $false; $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
      if ($process.HasExited) { throw 'upgrade_legacy_server_exited' }
      try {
        $request = [Net.HttpWebRequest]::Create($server + '/readyz'); $request.Proxy = $null; $request.AllowAutoRedirect = $false; $request.Timeout = 1000
        $response = $request.GetResponse()
        try { $ready = ([int]$response.StatusCode -eq 200) } finally { $response.Dispose() }
      } catch { $ready = $false }
      if ($ready) { break }; Start-Sleep -Milliseconds 200
    }
    if (-not $ready) { throw 'upgrade_legacy_not_ready' }
    $scope = @{workspace_id='default';project_id='default';user_id='management:owner'}
    $source = Invoke-UpgradeRequest $server $State.Secrets.owner '/v1/sources' @{scope=$scope;kind='document';text='Synthetic runtime source for backup verification.';origin_product='brain';source_ref='runtime-check/durable-fixture';collected_at='2026-01-01T00:00:00Z';trust_level='user_authored'} 201
    if ($source.id -notmatch '\A[A-Za-z0-9_-]{1,128}\z' -or @($source.sources).Count -ne 1 -or $source.sources[0].source_id -cne $source.id) { throw 'upgrade_fixture_source_invalid' }
    $candidate = Invoke-UpgradeRequest $server $State.Secrets.owner '/v1/memory_candidates' @{scope=$scope;kind='semantic';text='Synthetic runtime memory survives both backup restores.';sources=@($source.sources[0])} 201
    if ($candidate.id -notmatch '\A[A-Za-z0-9_-]{1,128}\z' -or -not (Test-InstalledSmokeInteger $candidate.revision) -or $candidate.revision -lt 1) { throw 'upgrade_fixture_candidate_invalid' }
    $approved = Invoke-UpgradeRequest $server $State.Secrets.owner ("/v1/memory_candidates/$($candidate.id)/review") @{scope=$scope;expected_revision=$candidate.revision;action='approve'} 200
    if ($approved.id -notmatch '\A[A-Za-z0-9_-]{1,128}\z' -or $approved.review.person -isnot [bool] -or -not $approved.review.person) { throw 'upgrade_fixture_approval_invalid' }
    $query = '?workspace_id=default&project_id=default&user_id=management%3Aowner'
    $source = Invoke-UpgradeRequest $server $State.Secrets.owner ("/v1/sources/$($source.id)$query") $null 200 -RawResult
    $memory = Invoke-UpgradeRequest $server $State.Secrets.owner ("/v1/memories/$($approved.id)$query") $null 200 -RawResult
    Write-UpgradeFixture $State $source $memory
  } catch { $primary = $_
  } finally {
    if ($null -ne $process) {
      try {
        if (-not $process.HasExited) { $process.Kill(); [void]$process.WaitForExit(10000) }
        if (-not $process.HasExited) { throw 'upgrade_legacy_server_stop_failed' }
      } catch { $cleanup = $_
      } finally { $process.Dispose() }
    }
    if ($pgAttempted) {
      try {
        if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $State.Data 'pg/postmaster.pid'))) { Invoke-UpgradePg $State 'stop' }
      } catch { if ($null -eq $cleanup) { $cleanup = $_ } }
    }
  }
  Assert-InstalledSmokeOutcome $primary $cleanup $null
  Assert-UpgradeStopped $State
}

function Invoke-UpgradeCandidateRuntime($State, [string]$Version) {
  Assert-UpgradeStopped $State
  $repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
  $cargo = (Get-Command cargo.exe -ErrorAction Stop).Source
  Assert-InstalledSmokeFile $cargo
  $environment = $State.Environment.Clone()
  $environment['PATH'] = $env:PATH
  foreach ($name in @('CARGO_HOME','RUSTUP_HOME','CARGO_TARGET_DIR','RUSTUP_TOOLCHAIN','GITHUB_ACTIONS','GITHUB_REPOSITORY','GITHUB_SHA','GITHUB_RUN_ID','GITHUB_RUN_ATTEMPT','RUNNER_ENVIRONMENT','RUNNER_OS','RUNNER_TEMP')) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($value) { $environment[$name] = $value }
  }
  if (-not $environment.ContainsKey('CARGO_HOME')) { $environment['CARGO_HOME'] = Join-Path $env:USERPROFILE '.cargo' }
  if (-not $environment.ContainsKey('RUSTUP_HOME')) { $environment['RUSTUP_HOME'] = Join-Path $env:USERPROFILE '.rustup' }
  # The shared process runner starts in the executable directory. Select the
  # repository's pinned compiler explicitly rather than relying on cwd lookup.
  $toolchain = Join-Path $repo 'rust-toolchain.toml'; Assert-InstalledSmokeFile $toolchain
  if (([IO.FileInfo]$toolchain).Length -gt 4096) { throw 'upgrade_toolchain_limit' }
  $channels = [regex]::Matches([IO.File]::ReadAllText($toolchain), '(?m)^\s*channel\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"\s*$')
  if ($channels.Count -ne 1) { throw 'upgrade_toolchain_pin_invalid' }
  $environment['RUSTUP_TOOLCHAIN'] = $channels[0].Groups[1].Value
  $environment['RUSTFLAGS'] = "--remap-path-prefix=$($env:USERPROFILE)=~ --remap-path-prefix=$repo=build"
  $environment['TAURI_CONFIG'] = @{version=$Version;build=@{frontendDist='../ui-dist'}} | ConvertTo-Json -Depth 3 -Compress
  $environment['ARKISM_UPGRADE_ROOT'] = $State.Root
  $environment['ARKISM_UPGRADE_OWNER'] = $State.Context.OwnedRoots[$State.Root]
  $environment['ARKISM_UPGRADE_SOURCE_SHA'] = $State.Context.SourceSha
  $result = Join-Path $State.Checks 'candidate-result.json'
  if ($null -ne (Get-InstalledSmokeAttributes $result)) { throw 'upgrade_candidate_output_exists' }
  $manifest = Join-Path $repo 'desktop/src-tauri/Cargo.toml'
  [void](Invoke-UpgradeProcess $cargo ('test --release --locked --manifest-path "' + $manifest + '" existing_published_profile_upgrade -- --ignored --nocapture') $environment 600 'upgrade_candidate_runtime')
  Assert-InstalledSmokeFile $result
  if (([IO.FileInfo]$result).Length -gt 65536) { throw 'upgrade_candidate_result_limit' }
  try { $value = [IO.File]::ReadAllText($result) | ConvertFrom-Json -ErrorAction Stop } catch { throw 'upgrade_candidate_json_invalid' }
  if ($value.ok -isnot [bool] -or -not $value.ok -or $value.existing_profile_upgrade -isnot [bool] -or -not $value.existing_profile_upgrade -or
      $value.source_sha -cne $State.Context.SourceSha -or $value.fixture_source_rows -ne 1 -or $value.fixture_memory_rows -ne 1 -or
      $value.fixture_sha256 -notmatch '\A[a-f0-9]{64}\z') { throw 'upgrade_candidate_result_invalid' }
  Assert-UpgradeStopped $State
  return $value
}

function Invoke-WindowsPublishedUpgrade([string]$BaselineInstallerPath, [string]$InstallerPath, [string]$StagePath,
    [string]$Version, [string]$SourceSha, [string]$EvidencePath) {
  $ErrorActionPreference = 'Stop'
  $baselineInput=$BaselineInstallerPath; $setupInput=$InstallerPath; $stageInput=$StagePath
  $versionInput=$Version; $sourceInput=$SourceSha; $evidenceInput=$EvidencePath
  . (Join-Path $PSScriptRoot 'smoke-windows-installed.ps1')
  . (Join-Path $PSScriptRoot 'smoke-windows-product-uninstall.ps1')
  . (Join-Path $PSScriptRoot 'test-nsis-uninstall-job.ps1')
  $context = Get-InstalledSmokeContext $sourceInput
  $baseline = ConvertTo-InstalledSmokePath $baselineInput; $pin = Assert-UpgradeBaseline $baseline
  if ($versionInput -notmatch '\A\d+\.\d+\.\d+\z' -or [Version]$versionInput -le [Version]$pin.Version) { throw 'upgrade_candidate_version_invalid' }
  $setup = ConvertTo-InstalledSmokePath $setupInput; $stage = ConvertTo-InstalledSmokePath $stageInput
  $evidence = ConvertTo-InstalledSmokePath $evidenceInput
  $candidateHash = Get-InstalledSmokeHash $setup; $manifest = @(Get-InstalledSmokePayload $stage)
  Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
  Assert-InstalledSmokeNoProcesses '' 'upgrade_preexisting_process'
  Initialize-ProductUninstallTypes
  $root = New-InstalledSmokeOwnedRoot $context 'version-upgrade'
  $state = @{Context=$context;Root=$root;Install=(Join-Path $root 'installed Arkism');Data=(Join-Path $root 'retained data/runtime data')
    Checks=(Join-Path $root 'upgrade checks');Tools=(Join-Path $root 'database tools');Environment=(New-InstalledSmokeEnvironment $root)}
  $report = [ordered]@{ok=$false;source_sha=$sourceInput;baseline_version=$pin.Version;baseline_source_sha=$pin.SourceSha
    baseline_source_binding='published_release_and_git_tag_metadata_not_build_attestation';baseline_installer_sha256=$pin.Sha256
    candidate_version=$versionInput;candidate_installer_sha256=$candidateHash;phase='prepare';cross_version_upgrade_tested=$false}
  $primary=$null;$cleanup=$null;$evidenceError=$null;$installedVersion=$null
  try {
    Assert-UpgradeContext $state
    foreach ($path in @($state.Environment.HOME,$state.Environment.APPDATA,$state.Environment.LOCALAPPDATA,$state.Environment.XDG_CONFIG_HOME,$state.Environment.TEMP,$state.Checks)) { [IO.Directory]::CreateDirectory($path)|Out-Null }
    Add-InstalledSmokeOwnedFolders $context $root $state.Environment
    $report.profile_check = Assert-InstalledSmokeIsolatedFolders $context $stage $state.Environment
    $pendingRequest = New-JobFixtureRequest $context $root (Join-Path $state.Install 'uninstall.exe') '' ('0' * 64)
    $pendingBefore = Get-ProductRebootSnapshot $pendingRequest
    [void](Assert-UpgradeBaseline $baseline)
    $report.phase='legacy_install'
    [void](Invoke-UpgradeProcess $baseline ("/S /NoAutoRun /D=$($state.Install)") $state.Environment 180 'upgrade_legacy_install')
    $installedVersion=$pin.Version
    [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($pendingBefore, (Get-ProductRebootSnapshot $pendingRequest))
    Assert-InstalledSmokeRegistration (Get-InstalledSmokeGlobalState $context) $state.Install $pin.Version -RequireComplete
    $state.LegacyManifest = @(Get-InstalledSmokeTree $state.Install $state.Install | Sort-Object Path)
    $legacyApp = Join-Path $state.Install 'Arkism.exe'
    $legacyAppRow = @($state.LegacyManifest | Where-Object { $_.Path -ceq 'Arkism.exe' })
    if ($legacyAppRow.Count -ne 1 -or (Get-InstalledSmokeHash $legacyApp) -cne $legacyAppRow[0].Sha256) { throw 'upgrade_legacy_app_changed' }
    $report.phase='legacy_fresh_runtime'
    [void](Invoke-UpgradeProcess $legacyApp ('--runtime-check "' + $state.Data + '"') $state.Environment 300 'upgrade_legacy_runtime')
    Assert-UpgradeStopped $state
    Assert-InstalledSmokeNoProcesses $state.Install 'upgrade_legacy_process_remains'
    $oldReport = Join-Path $state.Data 'check.json'; Assert-InstalledSmokeFile $oldReport
    if (([IO.FileInfo]$oldReport).Length -gt 65536) { throw 'upgrade_legacy_report_limit' }
    try { $old = [IO.File]::ReadAllText($oldReport)|ConvertFrom-Json -ErrorAction Stop } catch { throw 'upgrade_legacy_report_invalid' }
    if ($old.ok -isnot [bool] -or -not $old.ok -or $old.phase -cne 'complete' -or $old.capabilities -ne 200 -or $old.restored_capabilities -ne 200) { throw 'upgrade_legacy_runtime_failed' }
    $state.Secrets = Get-UpgradeMetadata $state
    $source = Join-Path $state.Install 'runtime/pgsql'
    $expected = @(Get-InstalledSmokeTree $source $source|Sort-Object Path)
    Copy-Item -LiteralPath $source -Destination $state.Tools -Recurse
    $state.ToolsManifest = @(Get-InstalledSmokeTree $state.Tools $state.Tools|Sort-Object Path)
    Assert-ProductManifest $expected $state.ToolsManifest 'upgrade_pg_tools_copy'
    $report.phase='legacy_data_seed'
    New-UpgradeFixture $state
    Assert-InstalledSmokeNoProcesses $state.Install 'upgrade_seed_process_remains'
    $beforeData = @(Get-InstalledSmokeTree $state.Data $state.Data|Sort-Object Path)
    $settingsHash = Get-InstalledSmokeHash (Join-Path $state.Data 'models.json')
    $report.phase='candidate_install'
    if ((Get-InstalledSmokeHash $setup) -cne $candidateHash) { throw 'upgrade_candidate_setup_changed' }
    Assert-ProductManifest $manifest @(Get-InstalledSmokePayload $stage) 'upgrade_candidate_stage'
    [void](Invoke-UpgradeProcess $setup ("/S /NoAutoRun /D=$($state.Install)") $state.Environment 180 'upgrade_candidate_install')
    $installedVersion=$versionInput
    [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($pendingBefore, (Get-ProductRebootSnapshot $pendingRequest))
    Assert-InstalledSmokePayload $state.Install $manifest
    Assert-InstalledSmokeRegistration (Get-InstalledSmokeGlobalState $context) $state.Install $versionInput -RequireComplete
    Assert-ProductManifest $beforeData @(Get-InstalledSmokeTree $state.Data $state.Data|Sort-Object Path) 'upgrade_database_after_install'
    $report.database_bytes_preserved_by_installer=$true
    $report.phase='candidate_existing_profile'
    $verified = Invoke-UpgradeCandidateRuntime $state $versionInput
    if ((Get-InstalledSmokeHash (Join-Path $state.Data 'models.json')) -cne $settingsHash) { throw 'upgrade_model_settings_changed' }
    $report.synthetic_model_settings_preserved=$true
    [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($pendingBefore, (Get-ProductRebootSnapshot $pendingRequest))
    $report.existing_opaque_pending_queue_preserved=$true
    $report.fixture_source_rows=1;$report.fixture_memory_rows=1;$report.fixture_sha256=$verified.fixture_sha256
    $report.existing_profile_rows_preserved=$true;$report.candidate_runtime_migration_tested=$true
    $report.cross_version_upgrade_tested=$true;$report.ok=$true;$report.phase='complete'
  } catch {
    $primary=$_;$report.error='upgrade_check_failed';$report.error_type=$_.Exception.GetType().FullName
  } finally {
    try {
      Assert-InstalledSmokeOwnedRoot $context $root
      if ($null -ne $installedVersion) { Remove-InstalledSmokeRegistration $context $state.Install $installedVersion }
      Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
      $report.cleanup='owned_registration_removed_root_retained_for_vm_teardown'
    } catch { $cleanup=$_;$report.ok=$false;$report.cleanup='owned_root_retained_after_unconfirmed_cleanup' }
    try { Write-InstalledSmokeEvidence $evidence $report } catch { $evidenceError=$_ }
  }
  Assert-InstalledSmokeOutcome $primary $cleanup $evidenceError
  return $report
}

if ($MyInvocation.InvocationName -ne '.') {
  Invoke-WindowsPublishedUpgrade $BaselineInstallerPath $InstallerPath $StagePath $Version $SourceSha $EvidencePath | Out-Null
}
