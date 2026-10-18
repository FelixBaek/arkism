# Explicit fresh-product removal check for disposable hosted Windows CI only.
# Dot-sourcing defines functions; it never installs or removes a product.
param([string]$InstallerPath, [string]$StagePath, [string]$Version,
      [string]$SourceSha, [string]$EvidencePath)

function Initialize-ProductUninstallTypes {
  if ('Arkism.InstallerChecks.ProductUninstallCheck' -as [type]) { throw 'product_types_preloaded' }
  $source = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'WindowsProcessJob.cs')) + "`n" +
    [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'WindowsProductUninstallCheck.cs'))
  $options = @{}
  if ($PSVersionTable.PSVersion.Major -ge 7) { $options.CompilerOptions = '/langversion:5' }
  Add-Type -TypeDefinition $source @options
}

function Assert-ProductEnvironment($State) {
  $expected = New-InstalledSmokeEnvironment $State.Root
  if ($State.Environment.Count -ne $expected.Count) { throw 'product_environment_changed' }
  foreach ($name in $expected.Keys) {
    if ($State.Environment[$name] -cne $expected[$name]) { throw 'product_environment_changed' }
  }
}

function Assert-ProductManifest($Expected, $Actual) {
  if (@($Expected).Count -ne @($Actual).Count) { throw 'product_manifest_changed' }
  for ($index = 0; $index -lt @($Expected).Count; $index++) {
    if ($Expected[$index].Path -cne $Actual[$index].Path -or $Expected[$index].Sha256 -cne $Actual[$index].Sha256) {
      throw 'product_manifest_changed'
    }
  }
}

function Get-ProductProcessRows([string]$Install) {
  foreach ($process in Get-CimInstance Win32_Process -ErrorAction Stop) {
    if ([string]::IsNullOrWhiteSpace($process.ExecutablePath)) {
      if ($process.Name -in @('Arkism.exe', 'arkism.exe', 'uninstall.exe', 'memoryd.exe', 'memory-principal.exe', 'postgres.exe')) {
        throw 'product_process_image_unknown'
      }
      continue
    }
    $image = ConvertTo-InstalledSmokePath $process.ExecutablePath
    if (Test-InstalledSmokeWithin $image $Install) { $process }
  }
}

function Assert-ProductProcessState($State, [long]$AllowedPid = 0, [string]$AllowedImage = '') {
  $rows = @(Get-ProductProcessRows $State.Install)
  if ($AllowedPid -eq 0 -and [string]::IsNullOrEmpty($AllowedImage)) {
    if ($rows.Count) { throw 'product_unexpected_process' }
    return
  }
  $expected = ConvertTo-InstalledSmokePath (Join-Path $State.Install 'uninstall.exe')
  if ($AllowedPid -le 0 -or $AllowedPid -gt [uint32]::MaxValue -or
    [string]::IsNullOrWhiteSpace($AllowedImage) -or (ConvertTo-InstalledSmokePath $AllowedImage) -ine $expected) {
    throw 'product_process_allowance_invalid'
  }
  if ($rows.Count -ne 1) { throw 'product_process_set_invalid' }
  Assert-ProductProcessRow $rows[0] $AllowedPid $expected
}

function Assert-ProductProcessRow($Row, [long]$AllowedPid, [string]$Expected) {
  if (($Row.ProcessId -isnot [int] -and $Row.ProcessId -isnot [long] -and $Row.ProcessId -isnot [uint32]) -or
    $Row.ProcessId -le 0 -or $Row.ProcessId -ne $AllowedPid -or [string]::IsNullOrWhiteSpace($Row.ExecutablePath) -or
    (ConvertTo-InstalledSmokePath $Row.ExecutablePath) -ine $Expected) { throw 'product_process_set_invalid' }
}

function Assert-ProductGlobalState($State, [long]$AllowedPid = 0, [string]$AllowedImage = '') {
  $global = Get-InstalledSmokeGlobalState $State.Context
  Assert-InstalledSmokeRegistration $global $State.Install $State.Version -RequireComplete
  foreach ($name in @('Autostart', 'Tasks', 'Residual')) {
    if (@($global[$name]).Count) { throw 'product_global_state_changed' }
  }
  Assert-ProductProcessState $State $AllowedPid $AllowedImage
}

function Assert-ProductProfileBeforeRemoval($State) {
  Assert-ProductManifest $State.Profile @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME | Sort-Object Path)
}

function Set-ProductProfileBaseline($State) {
  $global = Get-InstalledSmokeGlobalState $State.Context
  Assert-InstalledSmokeRegistration $global $State.Install $State.Version -RequireComplete
  $shortcuts = @($global.Shortcuts | ForEach-Object {
    $path = ConvertTo-InstalledSmokePath $_.Path
    if ($_.Scope -cne 'owned' -or -not (Test-InstalledSmokeWithin $path $State.Environment.HOME)) {
      throw 'product_shortcut_profile_not_owned'
    }
    $path
  })
  $expected = @(@($State.Hook) + $shortcuts | ForEach-Object {
    [pscustomobject]@{
      Path = $_.Substring($State.Environment.HOME.Length + 1).Replace('\', '/')
      Sha256 = Get-InstalledSmokeHash $_
    }
  } | Sort-Object Path)
  Assert-ProductManifest $expected @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME | Sort-Object Path)
  $State.Profile = $expected
  $State.ProfileShortcuts = $shortcuts
}

function Assert-ProductUninstallInputs($State, [long]$AllowedPid = 0, [string]$AllowedImage = '') {
  Assert-InstalledSmokeOwnedRoot $State.Context $State.Root
  Assert-ProductPathLayout $State
  Assert-ProductEnvironment $State
  if ((Get-InstalledSmokeHash $State.Setup) -cne $State.SetupHash) { throw 'product_setup_changed' }
  Assert-ProductManifest $State.Manifest @(Get-InstalledSmokePayload $State.Stage)
  Assert-InstalledSmokePayload $State.Install $State.Manifest
  if ((Get-InstalledSmokeHash (Join-Path $State.Install 'uninstall.exe')) -cne $State.UninstallerHash) { throw 'product_uninstaller_changed' }
  Assert-ProductProfileBeforeRemoval $State
  Assert-ProductManifest $State.Data @(Get-InstalledSmokeTree $State.Root (Join-Path $State.Root 'retained data'))
  if ((Get-InstalledSmokeHash (Join-Path $State.Root 'unrelated.sentinel')) -cne $State.SentinelHash) { throw 'product_sentinel_changed' }
  Assert-ProductGlobalState $State $AllowedPid $AllowedImage
}

function Assert-ProductPathLayout($State) {
  if ($State.Install -cne (Join-Path $State.Root 'installed Arkism')) { throw 'product_install_path_changed' }
  if ($State.Root -ieq $State.Stage -or (Test-InstalledSmokeWithin $State.Root $State.Stage) -or
    (Test-InstalledSmokeWithin $State.Stage $State.Root) -or (Test-InstalledSmokeWithin $State.Setup $State.Root)) {
    throw 'product_artifact_path_overlap'
  }
}

function Get-ProductTempInventory([string]$Root) {
  $inventory = New-Object 'Collections.Generic.List[object]'
  $budget = @{ Entries = 0; Bytes = [long]0 }
  Read-ProductTempDirectory $Root $Root 0 $budget $inventory
  return @($inventory | Sort-Object Path)
}

function Read-ProductTempDirectory([string]$Root, [string]$Directory, [int]$Depth, $Budget, $Rows) {
  if ($Depth -gt 12) { throw 'product_temp_inventory_limit' }
  Assert-InstalledSmokeDirectory $Directory
  foreach ($path in [IO.Directory]::EnumerateFileSystemEntries($Directory)) {
    $Budget.Entries++
    if ($Budget.Entries -gt 4096) { throw 'product_temp_inventory_limit' }
    $attrs = Get-InstalledSmokeAttributes $path
    if ($null -eq $attrs -or ($attrs -band [IO.FileAttributes]::ReparsePoint)) { throw 'product_temp_unsafe' }
    $relative = $path.Substring($Root.Length + 1).Replace('\', '/')
    if ($attrs -band [IO.FileAttributes]::Directory) {
      $Rows.Add([pscustomobject]@{ Path = $relative; Kind = 'directory'; Sha256 = ''; Bytes = [long]0 })
      Read-ProductTempDirectory $Root $path ($Depth + 1) $Budget $Rows
      continue
    }
    $bytes = ([IO.FileInfo]$path).Length
    if ($bytes -gt (64 * 1024 * 1024 - $Budget.Bytes)) { throw 'product_temp_inventory_limit' }
    $Budget.Bytes += $bytes
    $hash = Get-InstalledSmokeHash $path
    if (([IO.FileInfo]$path).Length -ne $bytes) { throw 'product_temp_changed' }
    $Rows.Add([pscustomobject]@{ Path = $relative; Kind = 'file'; Sha256 = $hash; Bytes = $bytes })
  }
}

function Get-ProductUninstallerCopy($Before, $After, [string]$Temp, [string]$Hash) {
  $existing = @{}; $current = @{}
  foreach ($row in @($Before)) { $existing[$row.Path] = $row }
  foreach ($row in @($After)) { $current[$row.Path] = $row }
  foreach ($name in $existing.Keys) {
    if (-not $current.ContainsKey($name) -or $current[$name].Kind -cne $existing[$name].Kind -or
      $current[$name].Sha256 -cne $existing[$name].Sha256) { throw 'product_temp_existing_changed' }
  }
  $copies = @($After | Where-Object { -not $existing.ContainsKey($_.Path) -and $_.Kind -ceq 'file' -and
    [IO.Path]::GetFileName($_.Path) -ieq 'Un.exe' })
  if ($copies.Count -ne 1 -or $copies[0].Sha256 -cne $Hash) { throw 'product_copy_ambiguous' }
  $path = Join-Path $Temp $copies[0].Path
  $path = ConvertTo-InstalledSmokePath $path
  if (-not (Test-InstalledSmokeWithin $path $Temp)) { throw 'product_copy_not_owned' }
  Assert-InstalledSmokeFile $path
  if ((Get-InstalledSmokeHash $path) -cne $Hash) { throw 'product_copy_changed' }
  return [pscustomobject]@{ Path = $path; Directory = [IO.Path]::GetDirectoryName($path); Sha256 = $Hash }
}

function Assert-ProductHookBackup($State) {
  if ($null -ne (Get-InstalledSmokeAttributes $State.Hook)) { throw 'product_helper_effect_missing' }
  $backupRoot = Join-Path $State.Environment.HOME '.config/arkism/backups'
  Assert-InstalledSmokeDirectory $backupRoot
  $dirs = @([IO.Directory]::EnumerateDirectories($backupRoot))
  if ($dirs.Count -ne 1 -or -not $dirs[0].EndsWith('-remove', [StringComparison]::Ordinal)) { throw 'product_helper_backup_invalid' }
  $manifest = Join-Path $dirs[0] 'manifest.json'; Assert-InstalledSmokeFile $manifest
  if (([IO.FileInfo]$manifest).Length -gt 65536) { throw 'product_helper_backup_invalid' }
  $value = [IO.File]::ReadAllText($manifest) | ConvertFrom-Json -ErrorAction Stop
  $fields = @($value.PSObject.Properties)
  $saved = Join-Path $dirs[0] '0'
  if ($fields.Count -ne 1 -or $fields[0].Name -cne $State.Hook -or $fields[0].Value -cne $saved -or
    (Get-InstalledSmokeHash $saved) -cne $State.HookHash) { throw 'product_helper_backup_invalid' }
  $files = @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME)
  if ($files.Count -ne 2) { throw 'product_profile_after_removal_changed' }
}

function Assert-ProductRemoved($State, $Result) {
  if (-not $Result.EmptyConfirmed) { throw 'product_job_not_empty' }
  foreach ($row in $State.Manifest) {
    if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $State.Install $row.Path))) { throw 'product_program_remains' }
  }
  if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $State.Install 'uninstall.exe'))) { throw 'product_uninstaller_remains' }
  if ($null -ne (Get-InstalledSmokeAttributes $State.Install)) {
    if (@(Get-InstalledSmokeTree $State.Install $State.Install).Count) { throw 'product_unexpected_file_remains' }
  }
  Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $State.Context)
  Assert-InstalledSmokeNoProcesses $State.Install 'product_process_remains'
  Assert-ProductHookBackup $State
  Assert-ProductManifest $State.Data @(Get-InstalledSmokeTree $State.Root (Join-Path $State.Root 'retained data'))
  if ((Get-InstalledSmokeHash (Join-Path $State.Root 'unrelated.sentinel')) -cne $State.SentinelHash) { throw 'product_sentinel_changed' }
}

function Get-ProductRebootSnapshot($Request) {
  return [Arkism.InstallerChecks.RebootObservation]::ReadWindows($Request)
}

function Invoke-ProductBoundRemoval($State) {
  $binding = New-Object Arkism.InstallerChecks.ProductUninstallBinding($State.Root, $State.Context.OwnedRoots[$State.Root],
    $State.Context.SourceSha, $State.Install, $State.Setup, $State.SetupHash, $State.UninstallerHash)
  $policy = [Action[long, string]]{ param($AllowedPid, $AllowedImage) Assert-ProductUninstallInputs $State $AllowedPid $AllowedImage }
  return [Arkism.InstallerChecks.ProductUninstallCheck]::RunWindows($binding, $policy)
}

function New-ProductSyntheticData($State) {
  $data = Join-Path $State.Root 'retained data'; [IO.Directory]::CreateDirectory($data) | Out-Null
  [IO.File]::WriteAllText((Join-Path $data 'closed-memory.fixture'), 'Synthetic closed-file preservation only.')
  [IO.File]::WriteAllText((Join-Path $State.Root 'unrelated.sentinel'), 'Unrelated owned file.')
  $State.Hook = ConvertTo-InstalledSmokePath (Join-Path $State.Environment.HOME '.config/arkism/hook-codex.cmd')
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($State.Hook)) | Out-Null
  [IO.File]::WriteAllText($State.Hook, 'Inert text; never executed or registered.')
  $State.HookHash = Get-InstalledSmokeHash $State.Hook
  $State.SentinelHash = Get-InstalledSmokeHash (Join-Path $State.Root 'unrelated.sentinel')
  $State.Data = @(Get-InstalledSmokeTree $State.Root $data)
  Set-ProductProfileBaseline $State
}

function Invoke-ProductRemovalCheck($State, $Request, $Report) {
  Assert-ProductUninstallInputs $State
  $Report.profile_files_before_removal = @($State.Profile).Count
  $Report.owned_shortcut_files_before_removal = @($State.ProfileShortcuts).Count
  $temp = Join-Path $State.Root 'temp'
  $inventory = @(Get-ProductTempInventory $temp)
  $before = Get-ProductRebootSnapshot $Request
  $Report.pending_baseline_entries = $before.EntryCount
  $Report.phase = 'default_product_uninstall'
  $Report.product_uninstaller_execution_attempted = $true
  $result = Invoke-ProductBoundRemoval $State
  Add-ProductJobResult $Report $result
  $Report.product_uninstaller_tested = $true
  Assert-ProductRemoved $State $result
  $afterInventory = @(Get-ProductTempInventory $temp)
  $copy = Get-ProductUninstallerCopy $inventory $afterInventory $temp $State.UninstallerHash
  $machine = Get-ProductCopyMachine $copy.Path
  $after = Get-ProductRebootSnapshot $Request
  $finalEcho = Invoke-JobExactEcho $State.Context $State.Root $copy $machine $Request $before $after $State.Environment $Report
  Assert-ProductRemoved $State $result
  [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($finalEcho, (Get-ProductRebootSnapshot $Request))
  $Report.job_empty = $result.EmptyConfirmed; $Report.root_exit_code = $result.RootExitCode
  $Report.product_postconditions = $true; $Report.closed_synthetic_data_preserved = $true
  $Report.helper_required_effects_observed = $true; $Report.helper_exit_code_observed = $false
  $Report.copy_sha256 = $copy.Sha256; $Report.same_run_exact_api_pair_correspondence = $true
  $Report.new_product_rows = 2; $Report.new_api_echo_rows = 2
  $Report.pending_final_entries = $finalEcho.EntryCount; $Report.existing_opaque_rows_preserved = $true
}

function Get-ProductCopyMachine([string]$Path) {
  return [Arkism.InstallerChecks.PendingEchoVerifier]::ReadNativeMachine($Path)
}

function Add-ProductJobResult($Report, $Result) {
  $phases = @('validate', 'create_job', 'create_suspended', 'assign', 'verify_image', 'resume', 'wait_tree', 'complete')
  $states = @('not_attempted', 'attempted', 'api_failed', 'already_running', 'still_suspended', 'resumed')
  $Report.job_phase = if ($Result.Phase -cin $phases) { $Result.Phase } else { 'unknown' }
  $Report.job_resume_state = if ($Result.ResumeState -cin $states) { $Result.ResumeState } else { 'unknown' }
  $Report.job_may_have_run = [bool]$Result.MayHaveRun
  $Report.job_empty = [bool]$Result.EmptyConfirmed; $Report.job_total_processes = [long]$Result.TotalProcesses
}

function Set-ProductRetention($State, $Report) {
  try {
    Assert-InstalledSmokeOwnedRoot $State.Context $State.Root
    $Report.cleanup = if ($Report.ok) { 'owned_root_and_all_reservations_retained_for_vm_teardown' } else { 'owned_root_retained_after_failure' }
  } catch {
    $Report.ok = $false; $Report.cleanup = 'unconfirmed_root_retained'
    throw
  }
}

function Invoke-WindowsProductUninstall([string]$InstallerPath, [string]$StagePath, [string]$Version, [string]$SourceSha, [string]$EvidencePath) {
  $ErrorActionPreference = 'Stop'
  $setupInput = $InstallerPath; $stageInput = $StagePath; $sourceInput = $SourceSha; $evidenceInput = $EvidencePath; $versionInput = $Version
  . (Join-Path $PSScriptRoot 'smoke-windows-installed.ps1')
  . (Join-Path $PSScriptRoot 'test-nsis-uninstall-job.ps1')
  $Version = $versionInput
  if ($Version -notmatch '\A\d+\.\d+\.\d+\z') { throw 'product_version_invalid' }
  $context = Get-InstalledSmokeContext $sourceInput
  $setup = ConvertTo-InstalledSmokePath $setupInput; $stage = ConvertTo-InstalledSmokePath $stageInput
  $evidence = ConvertTo-InstalledSmokePath $evidenceInput
  if ([IO.Path]::GetFileName($setup) -cne "Arkism-$Version-setup.exe") { throw 'product_setup_name_invalid' }
  if (-not (Test-InstalledSmokeWithin $evidence $context.RunnerTemp)) { throw 'product_evidence_not_owned' }
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($evidence))
  if ($null -ne (Get-InstalledSmokeAttributes $evidence)) { throw 'product_evidence_exists' }
  $manifest = @(Get-InstalledSmokePayload $stage); $setupHash = Get-InstalledSmokeHash $setup
  Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
  Assert-InstalledSmokeNoProcesses '' 'product_preexisting_process'
  Initialize-ProductUninstallTypes
  $root = New-InstalledSmokeOwnedRoot $context 'product-removal'
  $state = @{ Context = $context; Root = $root; Install = Join-Path $root 'installed Arkism'; Version = $Version
    Setup = $setup; SetupHash = $setupHash; Stage = $stage; Manifest = $manifest; Environment = New-InstalledSmokeEnvironment $root }
  $report = [ordered]@{ ok = $false; source_sha = $sourceInput; setup_sha256 = $setupHash; phase = 'prepare'
    product_uninstaller_tested = $false; helper_exit_code_observed = $false; database_preservation_tested = $false
    version = $Version; manifest_file_count = $manifest.Count
    common_runner_sha256 = Get-InstalledSmokeHash (Join-Path $PSScriptRoot 'WindowsProcessJob.cs')
    product_runner_sha256 = Get-InstalledSmokeHash (Join-Path $PSScriptRoot 'WindowsProductUninstallCheck.cs') }
  $primary = $null; $cleanup = $null; $evidenceError = $null
  try {
    [Arkism.InstallerChecks.ProductUninstallCheck]::RequireLiteralInstallPath($state.Install)
    Assert-ProductPathLayout $state
    foreach ($path in @($state.Environment.HOME, $state.Environment.APPDATA, $state.Environment.LOCALAPPDATA,
      $state.Environment.XDG_CONFIG_HOME, $state.Environment.TEMP)) { [IO.Directory]::CreateDirectory($path) | Out-Null }
    Add-InstalledSmokeOwnedFolders $context $root $state.Environment
    Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
    $request = New-JobFixtureRequest $context $root (Join-Path $state.Install 'uninstall.exe') '' ('0' * 64)
    $beforeSetup = Get-ProductRebootSnapshot $request
    $report.phase = 'fresh_install'
    [void](Invoke-InstalledSmokeProcess $setup ("/S /NoAutoRun /D=$($state.Install)") $state.Environment 180 'product_fresh_install')
    [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($beforeSetup, (Get-ProductRebootSnapshot $request))
    $report.setup_queue_unchanged = $true
    Assert-InstalledSmokePayload $state.Install $manifest
    $state.UninstallerHash = Get-InstalledSmokeHash (Join-Path $state.Install 'uninstall.exe')
    $request.ExpectedSha256 = $state.UninstallerHash
    New-ProductSyntheticData $state
    Invoke-ProductRemovalCheck $state $request $report
    $report.ok = $true; $report.phase = 'complete'
  } catch {
    $primary = $_; $report.error = 'product_check_failed'; $report.error_type = $_.Exception.GetType().FullName
    $failure = Find-JobFixtureFailure $_
    if ($null -ne $failure) { Add-ProductJobResult $report $failure.Result; $report.job_errors = @($failure.Result.Errors) }
  }
  finally {
    try { Set-ProductRetention $state $report } catch { $cleanup = $_ }
    try { Write-InstalledSmokeEvidence $evidence $report } catch { $evidenceError = $_ }
  }
  Assert-InstalledSmokeOutcome $primary $cleanup $evidenceError
  return $report
}

if ($MyInvocation.InvocationName -ne '.') {
  Invoke-WindowsProductUninstall $InstallerPath $StagePath $Version $SourceSha $EvidencePath | Out-Null
}
