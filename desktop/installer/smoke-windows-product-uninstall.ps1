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

function Test-ProductOrdinalMember([string]$Value, [string[]]$Allowed) {
  foreach ($candidate in $Allowed) {
    if ([string]::Equals($Value, $candidate, [StringComparison]::Ordinal)) { return $true }
  }
  return $false
}

function Get-ProductPolicyStages {
  return @('none', 'callback', 'owner', 'layout', 'environment', 'setup_hash', 'stage_payload', 'installed_payload',
    'uninstaller_hash', 'profile', 'retained_data', 'sentinel', 'global_read', 'registration', 'global_absence',
    'shortcuts', 'process_scan', 'process_validate', 'complete')
}

function Get-ProductPolicyTrace($State) {
  if ($State -is [hashtable] -and (Test-ProductPolicyTrace $State['PolicyTrace'])) { return $State['PolicyTrace'] }
}

function Invoke-ProductPolicyStage($State, [string]$Stage, [scriptblock]$Body) {
  $trace = Get-ProductPolicyTrace $State
  if ($null -ne $trace) { $trace['CurrentStage'] = $Stage }
  & $Body
  if ($null -ne $trace) { $trace['CompletedStage'] = $Stage }
}

function Invoke-ProductUninstallPolicy($State, [long]$AllowedPid, [string]$AllowedImage) {
  $trace = Get-ProductPolicyTrace $State
  if ($null -ne $trace) {
    $trace['Context'] = if ($AllowedPid -eq 0 -and [string]::IsNullOrEmpty($AllowedImage)) { 'initial' } else { 'late' }
    $trace['CallbackCount']++
    $trace['CurrentStage'] = 'callback'; $trace['CompletedStage'] = 'none'; $trace['Completed'] = $false
    $trace['ProcessObserved'] = $false; $trace['ProcessCount'] = 0; $trace['ProcessVerified'] = $false
    $State['ProcessScanTrace'] = $null
    $State['ProcessIdentityTrace'] = $null
  }
  Assert-ProductUninstallInputs $State $AllowedPid $AllowedImage
  if ($null -ne $trace) {
    $trace['CurrentStage'] = 'complete'; $trace['CompletedStage'] = 'complete'; $trace['Completed'] = $true
  }
}

function New-ProductUninstallPolicy($State) {
  $State['PolicyTrace'] = @{ Context = 'initial'; CallbackCount = 0; CurrentStage = 'none'; CompletedStage = 'none'
    ProcessObserved = $false; ProcessCount = 0; ProcessVerified = $false; Completed = $false }
  $factoryState = $State
  $factoryPolicy = ${function:Invoke-ProductUninstallPolicy}
  return [Action[long, string]]{ param($AllowedPid, $AllowedImage) & $factoryPolicy $factoryState $AllowedPid $AllowedImage }.GetNewClosure()
}

function Test-ProductPolicyTrace($Trace) {
  if ($Trace -isnot [hashtable]) { return $false }
  foreach ($name in @('Context', 'CurrentStage', 'CompletedStage')) {
    if ($Trace[$name] -isnot [string]) { return $false }
  }
  if (-not (Test-ProductOrdinalMember $Trace['Context'] @('initial', 'late'))) { return $false }
  foreach ($name in @('CurrentStage', 'CompletedStage')) {
    if (-not (Test-ProductOrdinalMember $Trace[$name] (Get-ProductPolicyStages))) { return $false }
  }
  if (-not (Test-ProductTraceCount $Trace['CallbackCount'] 2) -or -not (Test-ProductTraceCount $Trace['ProcessCount'] 1000000)) { return $false }
  foreach ($name in @('ProcessObserved', 'ProcessVerified', 'Completed')) {
    if ($Trace[$name] -isnot [bool]) { return $false }
  }
  return $true
}

function Test-ProductTraceCount($Value, [int]$Maximum) {
  return ($Value -is [int] -and $Value -ge 0 -and $Value -le $Maximum)
}

function Add-ProductPolicyDiagnostic($Report, $State) {
  # Optional diagnostics never replace the primary, retention or evidence failure.
  try { Copy-ProductPolicyDiagnostic $Report $State } catch { $Report.policy_trace_unavailable = $true }
}

function Copy-ProductPolicyDiagnostic($Report, $State) {
  $trace = Get-ProductPolicyTrace $State
  if (-not (Test-ProductPolicyTrace $trace)) { return }
  $Report.policy_context = $trace['Context']; $Report.policy_callback_count = $trace['CallbackCount']
  $Report.policy_current_stage = $trace['CurrentStage']; $Report.policy_completed_stage = $trace['CompletedStage']
  $Report.policy_process_observed = $trace['ProcessObserved']; $Report.policy_process_count = $trace['ProcessCount']
  $Report.policy_process_verified = $trace['ProcessVerified']; $Report.policy_completed = $trace['Completed']
  Copy-ProductProcessScanDiagnostic $Report $State
  Copy-ProductProcessIdentityDiagnostic $Report $State
}

function Copy-ProductProcessIdentityDiagnostic($Report, $State) {
  $identity = $State['ProcessIdentityTrace']
  if ($identity -isnot [hashtable]) { return }
  foreach ($name in @('NativeImageReliedOn', 'CimImageUnavailable')) {
    if ($identity[$name] -isnot [bool] -or -not $identity[$name]) { return }
  }
  if ($identity['Source'] -isnot [string] -or
    -not [string]::Equals($identity['Source'], 'native_verified_cim_unavailable', [StringComparison]::Ordinal)) { return }
  $Report.policy_native_image_relied_on = $identity['NativeImageReliedOn']
  $Report.policy_cim_image_unavailable = $identity['CimImageUnavailable']
  $Report.policy_process_image_source = $identity['Source']
}

function New-ProductProcessScan($State) {
  if ($null -eq (Get-ProductPolicyTrace $State)) { return }
  $scan = @{ Stage = 'query'; QueryReturned = $false; QueryRows = 0; RowsVisited = 0
    ImageKind = 'unobserved'; NameKind = 'unobserved'; PidRelation = 'unobserved' }
  $State['ProcessScanTrace'] = $scan
  return $scan
}

function Set-ProductProcessScan($Scan, [string]$Name, $Value) {
  if ($Scan -is [hashtable]) { $Scan[$Name] = $Value }
}

function Get-ProductScanKind($Value) {
  if ($null -eq $Value) { return 'null' }
  if ($Value -is [string]) { return 'string' }
  if ($Value -is [array]) { return 'array' }
  return 'other'
}

function Get-ProductScanPidRelation($Row, [long]$ExpectedPid) {
  if ($ExpectedPid -le 0) { return 'not_applicable' }
  # Optional observation never changes the actual process identity assertion.
  try { $value = $Row.ProcessId } catch { return 'unavailable' }
  if (($value -isnot [int] -and $value -isnot [long] -and $value -isnot [uint32]) -or
    $value -le 0 -or $value -gt [uint32]::MaxValue) { return 'unavailable' }
  if ($value -eq $ExpectedPid) { return 'expected_pid' }
  return 'other_pid'
}

function Test-ProductProcessScan($Scan) {
  if ($Scan -isnot [hashtable]) { return $false }
  $allowed = @{
    Stage = @('query','iterate','row_image','image_check','row_name','unknown_image','path_normalize','path_containment','emit','complete')
    ImageKind = @('unobserved','null','string','array','other'); NameKind = @('unobserved','null','string','array','other')
    PidRelation = @('unobserved','not_applicable','expected_pid','other_pid','unavailable')
  }
  foreach ($name in $allowed.Keys) {
    if ($Scan[$name] -isnot [string]) { return $false }
    if (-not (Test-ProductOrdinalMember $Scan[$name] $allowed[$name])) { return $false }
  }
  if ($Scan['QueryReturned'] -isnot [bool]) { return $false }
  foreach ($name in @('QueryRows','RowsVisited')) {
    if (-not (Test-ProductTraceCount $Scan[$name] 1000000)) { return $false }
  }
  return $true
}

function Copy-ProductProcessScanDiagnostic($Report, $State) {
  $scan = $State['ProcessScanTrace']
  if (-not (Test-ProductProcessScan $scan)) { return }
  $Report.policy_scan_stage = $scan['Stage']; $Report.policy_scan_query_returned = $scan['QueryReturned']
  $Report.policy_scan_query_rows = $scan['QueryRows']; $Report.policy_scan_rows_visited = $scan['RowsVisited']
  $Report.policy_scan_image_kind = $scan['ImageKind']; $Report.policy_scan_name_kind = $scan['NameKind']
  $Report.policy_scan_pid_relation = $scan['PidRelation']
}

function Get-ProductSortedManifest($Rows) {
  $ordered = New-Object 'Collections.Generic.List[object]'
  foreach ($row in @($Rows)) { $ordered.Add($row) }
  $ordered.Sort([Comparison[object]]{ param($left, $right) [StringComparer]::Ordinal.Compare($left.Path, $right.Path) })
  return $ordered.ToArray()
}

function New-ProductManifestFailure($Expected, $Actual, [string]$Check, [int]$Index = -1) {
  $checks = @('unspecified', 'profile_initial', 'profile_before_removal', 'profile_after_removal', 'stage_payload', 'data_before_removal', 'data_after_removal')
  if (-not (Test-ProductOrdinalMember $Check $checks)) { throw 'product_manifest_check_invalid' }
  $diagnostic = @{ Check = $Check; Kind = 'count'; ExpectedFiles = @($Expected).Count; ActualFiles = @($Actual).Count
    FirstIndex = $Index; PathCaseEqual = $false; PathSeparatorEqual = $false }
  if ($Index -ge 0) {
    $pathChanged = -not [string]::Equals($Expected[$Index].Path, $Actual[$Index].Path, [StringComparison]::Ordinal)
    $hashChanged = -not [string]::Equals($Expected[$Index].Sha256, $Actual[$Index].Sha256, [StringComparison]::Ordinal)
    $diagnostic.Kind = if ($pathChanged -and $hashChanged) { 'path_and_hash' } elseif ($pathChanged) { 'path' } else { 'hash' }
    if ($Expected[$Index].Path -is [string] -and $Actual[$Index].Path -is [string]) {
      $diagnostic.PathCaseEqual = [string]::Equals($Expected[$Index].Path, $Actual[$Index].Path, [StringComparison]::OrdinalIgnoreCase)
      $diagnostic.PathSeparatorEqual = [string]::Equals($Expected[$Index].Path.Replace('\', '/'), $Actual[$Index].Path.Replace('\', '/'), [StringComparison]::Ordinal)
    }
  }
  $failure = New-Object IO.InvalidDataException('product_manifest_changed')
  $failure.Data['ArkismProductManifest'] = $diagnostic
  return $failure
}

function Assert-ProductManifest($Expected, $Actual, [string]$Check = 'unspecified') {
  if (@($Expected).Count -ne @($Actual).Count) { throw (New-ProductManifestFailure $Expected $Actual $Check) }
  $Expected = @(Get-ProductSortedManifest $Expected)
  $Actual = @(Get-ProductSortedManifest $Actual)
  for ($index = 0; $index -lt @($Expected).Count; $index++) {
    if (-not [string]::Equals($Expected[$index].Path, $Actual[$index].Path, [StringComparison]::Ordinal) -or
      -not [string]::Equals($Expected[$index].Sha256, $Actual[$index].Sha256, [StringComparison]::Ordinal)) {
      throw (New-ProductManifestFailure $Expected $Actual $Check $index)
    }
  }
}

function Get-ProductManifestDiagnostic($Failure) {
  $exception = $Failure.Exception
  for ($depth = 0; $depth -lt 8 -and $null -ne $exception; $depth++) {
    $diagnostic = $exception.Data['ArkismProductManifest']
    if ($exception.Message -ceq 'product_manifest_changed' -and $diagnostic -is [hashtable]) { return $diagnostic }
    $exception = $exception.InnerException
  }
}

function Add-ProductManifestDiagnostic($Report, $Failure) {
  $diagnostic = Get-ProductManifestDiagnostic $Failure
  if ($null -eq $diagnostic) { return }
  if ($diagnostic['Check'] -isnot [string] -or $diagnostic['Kind'] -isnot [string]) { return }
  if (-not (Test-ProductOrdinalMember $diagnostic['Check'] @('unspecified', 'profile_initial', 'profile_before_removal', 'profile_after_removal', 'stage_payload', 'data_before_removal', 'data_after_removal')) -or
    -not (Test-ProductOrdinalMember $diagnostic['Kind'] @('count', 'path', 'hash', 'path_and_hash'))) { return }
  foreach ($name in @('ExpectedFiles', 'ActualFiles', 'FirstIndex')) {
    if ($diagnostic[$name] -isnot [int] -or $diagnostic[$name] -lt -1) { return }
  }
  foreach ($name in @('PathCaseEqual', 'PathSeparatorEqual')) {
    if ($diagnostic[$name] -isnot [bool]) { return }
  }
  $Report.manifest_check = $diagnostic['Check']; $Report.manifest_mismatch_kind = $diagnostic['Kind']
  $Report.manifest_expected_files = $diagnostic['ExpectedFiles']; $Report.manifest_actual_files = $diagnostic['ActualFiles']
  $Report.manifest_first_index = $diagnostic['FirstIndex']
  $Report.manifest_path_case_equal = $diagnostic['PathCaseEqual']; $Report.manifest_path_separator_equal = $diagnostic['PathSeparatorEqual']
}

function New-ProductProcessAllowance([string]$Install, $AllowedPid, $AllowedImage) {
  if (($AllowedPid -isnot [int] -and $AllowedPid -isnot [long] -and $AllowedPid -isnot [uint32]) -or
    $AllowedPid -lt 0 -or $AllowedPid -gt [uint32]::MaxValue -or $AllowedImage -isnot [string]) {
    throw 'product_process_allowance_invalid'
  }
  if ($AllowedPid -eq 0 -and $AllowedImage.Length -eq 0) { return }
  $expected = ConvertTo-InstalledSmokePath (Join-Path $Install 'uninstall.exe')
  if ($AllowedPid -eq 0 -or [string]::IsNullOrWhiteSpace($AllowedImage) -or
    -not [string]::Equals((ConvertTo-InstalledSmokePath $AllowedImage), $expected, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'product_process_allowance_invalid'
  }
  return @{ ProcessId = $AllowedPid; Image = $expected }
}

function Test-ProductNativeCimUnavailable($Row, $ImageValue, $NameValue, $Allowance) {
  if ($null -eq $Allowance -or $null -ne $ImageValue -or $NameValue -isnot [string] -or
    -not [string]::Equals($NameValue, 'uninstall.exe', [StringComparison]::OrdinalIgnoreCase)) { return $false }
  $rowPid = $Row.ProcessId
  return (($rowPid -is [int] -or $rowPid -is [long] -or $rowPid -is [uint32]) -and
    $rowPid -gt 0 -and $rowPid -le [uint32]::MaxValue -and $rowPid -eq $Allowance['ProcessId'])
}

function Get-ProductScanAllowance([string]$Install, $ExpectedPid, $NativeImage) {
  # A PID supplied only for scan diagnostics cannot authorize an image exception.
  if ($NativeImage -is [string] -and $NativeImage.Length -eq 0) { return }
  return New-ProductProcessAllowance $Install $ExpectedPid $NativeImage
}

function Assert-ProductCimImageType($ImageValue) {
  if ($null -ne $ImageValue -and $ImageValue -isnot [string]) { throw 'product_process_image_invalid' }
}

function Get-ProductProcessRows([string]$Install, $ScanState, $ExpectedPid = 0, $NativeImage = '') {
  $allowance = Get-ProductScanAllowance $Install $ExpectedPid $NativeImage
  $scan = New-ProductProcessScan $ScanState
  $processes = @(Get-CimInstance Win32_Process -ErrorAction Stop)
  Set-ProductProcessScan $scan 'QueryReturned' $true
  Set-ProductProcessScan $scan 'QueryRows' $processes.Count
  Set-ProductProcessScan $scan 'Stage' 'iterate'
  foreach ($process in $processes) {
    if ($null -ne $scan) { $scan['RowsVisited']++ }
    Set-ProductProcessScan $scan 'ImageKind' 'unobserved'
    Set-ProductProcessScan $scan 'NameKind' 'unobserved'
    Set-ProductProcessScan $scan 'PidRelation' (Get-ProductScanPidRelation $process $ExpectedPid)
    Set-ProductProcessScan $scan 'Stage' 'row_image'
    $imageValue = $process.ExecutablePath
    Set-ProductProcessScan $scan 'ImageKind' (Get-ProductScanKind $imageValue)
    Set-ProductProcessScan $scan 'Stage' 'image_check'
    Assert-ProductCimImageType $imageValue
    if ([string]::IsNullOrWhiteSpace($imageValue)) {
      Set-ProductProcessScan $scan 'Stage' 'row_name'
      $nameValue = $process.Name
      Set-ProductProcessScan $scan 'NameKind' (Get-ProductScanKind $nameValue)
      if (Test-ProductNativeCimUnavailable $process $imageValue $nameValue $allowance) {
        # Preserve the observed CIM row; the separate native receipt supplies its image identity.
        $process
        continue
      }
      if ($nameValue -in @('Arkism.exe', 'arkism.exe', 'uninstall.exe', 'memoryd.exe', 'memory-principal.exe', 'postgres.exe')) {
        Set-ProductProcessScan $scan 'Stage' 'unknown_image'
        throw 'product_process_image_unknown'
      }
      continue
    }
    Set-ProductProcessScan $scan 'Stage' 'path_normalize'
    $image = ConvertTo-InstalledSmokePath $imageValue
    Set-ProductProcessScan $scan 'Stage' 'path_containment'
    if (Test-InstalledSmokeWithin $image $Install) {
      Set-ProductProcessScan $scan 'Stage' 'emit'
      $process
    }
  }
  Set-ProductProcessScan $scan 'Stage' 'complete'
}

function Assert-ProductProcessState($State, $AllowedPid = 0, $AllowedImage = '') {
  $allowance = New-ProductProcessAllowance $State.Install $AllowedPid $AllowedImage
  $rows = @(Invoke-ProductPolicyStage $State 'process_scan' { Get-ProductProcessRows $State.Install $State $AllowedPid $AllowedImage })
  $trace = Get-ProductPolicyTrace $State
  if ($null -ne $trace) { $trace['ProcessObserved'] = $true; $trace['ProcessCount'] = $rows.Count }
  Invoke-ProductPolicyStage $State 'process_validate' {
    if ($AllowedPid -eq 0 -and [string]::IsNullOrEmpty($AllowedImage)) {
      if ($rows.Count) { throw 'product_unexpected_process' }
      return
    }
    if ($rows.Count -ne 1) { throw 'product_process_set_invalid' }
    Assert-ProductProcessRow $rows[0] $AllowedPid $allowance['Image'] $allowance
    if ($null -eq $rows[0].ExecutablePath) {
      $State['ProcessIdentityTrace'] = @{ NativeImageReliedOn = $true; CimImageUnavailable = $true
        Source = 'native_verified_cim_unavailable' }
    }
  }
  if ($null -ne $trace) { $trace['ProcessVerified'] = $true }
}

function Assert-ProductProcessRow($Row, [long]$AllowedPid, [string]$Expected, $Allowance) {
  $imageValue = $Row.ExecutablePath
  if (Test-ProductNativeCimUnavailable $Row $imageValue $Row.Name $Allowance) { return }
  if ($imageValue -isnot [string]) { throw 'product_process_set_invalid' }
  if (($Row.ProcessId -isnot [int] -and $Row.ProcessId -isnot [long] -and $Row.ProcessId -isnot [uint32]) -or
    $Row.ProcessId -le 0 -or $Row.ProcessId -ne $AllowedPid -or [string]::IsNullOrWhiteSpace($imageValue) -or
    (ConvertTo-InstalledSmokePath $imageValue) -ine $Expected) { throw 'product_process_set_invalid' }
}

function Assert-ProductGlobalState($State, [long]$AllowedPid = 0, [string]$AllowedImage = '') {
  $global = Invoke-ProductPolicyStage $State 'global_read' { Get-InstalledSmokeGlobalState $State.Context }
  Invoke-ProductPolicyStage $State 'registration' { Assert-InstalledSmokeRegistration $global $State.Install $State.Version -RequireComplete }
  Invoke-ProductPolicyStage $State 'global_absence' {
    foreach ($name in @('Autostart', 'Tasks', 'Residual')) {
      if (@($global[$name]).Count) { throw 'product_global_state_changed' }
    }
  }
  Invoke-ProductPolicyStage $State 'shortcuts' {
    if ($State.ContainsKey('ProfileShortcutRecords')) { Assert-ProductShortcutBindings $State $global }
  }
  Assert-ProductProcessState $State $AllowedPid $AllowedImage
}

function Assert-ProductProfileBeforeRemoval($State) {
  Assert-ProductManifest $State.Profile @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME | Sort-Object Path) 'profile_before_removal'
}

function Assert-ProductProfileBeforeSetup($State) {
  Assert-InstalledSmokeOwnedRoot $State.Context $State.Root
  Assert-ProductPathLayout $State
  Assert-ProductEnvironment $State
  if (@(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME).Count) { throw 'product_profile_not_empty' }
}

function Get-ProductProfileShortcutRecords($State, $Global, $ProfileTree) {
  Assert-InstalledSmokeRegistration $Global $State.Install $State.Version -RequireComplete
  $seen = @{}
  foreach ($shortcut in @($Global.Shortcuts | Sort-Object Role)) {
    $path = ConvertTo-InstalledSmokePath $shortcut.Path
    if ($shortcut.Scope -cne 'owned' -or -not (Test-InstalledSmokeWithin $path $State.Environment.HOME)) {
      throw 'product_shortcut_profile_not_owned'
    }
    $relative = $path.Substring($State.Environment.HOME.Length + 1).Replace('\', '/')
    $rows = @($ProfileTree | Where-Object { [string]::Equals($_.Path, $relative, [StringComparison]::OrdinalIgnoreCase) })
    if ($rows.Count -ne 1 -or $seen.ContainsKey($rows[0].Path)) { throw 'product_shortcut_profile_ambiguous' }
    $seen[$rows[0].Path] = $true
    [pscustomobject]@{ Role = $shortcut.Role; Path = $rows[0].Path; Sha256 = $rows[0].Sha256 }
  }
}

function Assert-ProductShortcutBindings($State, $Global) {
  $actual = @(Get-ProductProfileShortcutRecords $State $Global $State.SetupProfile)
  Assert-ProductManifest $State.ProfileShortcutRecords $actual
  for ($index = 0; $index -lt $actual.Count; $index++) {
    if (-not [string]::Equals($State.ProfileShortcutRecords[$index].Role, $actual[$index].Role, [StringComparison]::Ordinal) -or
      -not [string]::Equals($State.ProfileShortcutRecords[$index].Path, $actual[$index].Path, [StringComparison]::Ordinal)) {
      throw 'product_shortcut_role_changed'
    }
  }
}

function Initialize-ProductProfileBaseline($State) {
  if ($State.ContainsKey('SetupProfile')) { throw 'product_profile_already_captured' }
  Assert-InstalledSmokeOwnedRoot $State.Context $State.Root
  Assert-ProductPathLayout $State
  Assert-ProductEnvironment $State
  if ((Get-InstalledSmokeHash $State.Setup) -cne $State.SetupHash) { throw 'product_setup_changed' }
  Assert-InstalledSmokePayload $State.Install $State.Manifest
  if ((Get-InstalledSmokeHash (Join-Path $State.Install 'uninstall.exe')) -cne $State.UninstallerHash) { throw 'product_uninstaller_changed' }
  Assert-ProductGlobalState $State
  $profileTree = @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME | Sort-Object Path)
  $shortcuts = @(Get-ProductProfileShortcutRecords $State (Get-InstalledSmokeGlobalState $State.Context) $profileTree)
  $State.SetupProfile = $profileTree
  $State.ProfileShortcutRecords = $shortcuts
  $State.ProfileShortcuts = @($shortcuts | ForEach-Object { ConvertTo-InstalledSmokePath (Join-Path $State.Environment.HOME $_.Path) })
}

function Set-ProductProfileBaseline($State) {
  if (-not $State.ContainsKey('SetupProfile')) { throw 'product_profile_not_captured' }
  Assert-ProductShortcutBindings $State (Get-InstalledSmokeGlobalState $State.Context)
  $hook = [pscustomobject]@{ Path = $State.Hook.Substring($State.Environment.HOME.Length + 1).Replace('\', '/'); Sha256 = $State.HookHash }
  $expected = @(@($State.SetupProfile) + @($hook) | Sort-Object Path)
  Assert-ProductManifest $expected @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME | Sort-Object Path) 'profile_initial'
  $State.Profile = $expected
}

function Assert-ProductUninstallInputs($State, [long]$AllowedPid = 0, [string]$AllowedImage = '') {
  Invoke-ProductPolicyStage $State 'owner' { Assert-InstalledSmokeOwnedRoot $State.Context $State.Root }
  Invoke-ProductPolicyStage $State 'layout' { Assert-ProductPathLayout $State }
  Invoke-ProductPolicyStage $State 'environment' { Assert-ProductEnvironment $State }
  Invoke-ProductPolicyStage $State 'setup_hash' {
    if ((Get-InstalledSmokeHash $State.Setup) -cne $State.SetupHash) { throw 'product_setup_changed' }
  }
  Invoke-ProductPolicyStage $State 'stage_payload' { Assert-ProductManifest $State.Manifest @(Get-InstalledSmokePayload $State.Stage) 'stage_payload' }
  Invoke-ProductPolicyStage $State 'installed_payload' { Assert-InstalledSmokePayload $State.Install $State.Manifest }
  Invoke-ProductPolicyStage $State 'uninstaller_hash' {
    if ((Get-InstalledSmokeHash (Join-Path $State.Install 'uninstall.exe')) -cne $State.UninstallerHash) { throw 'product_uninstaller_changed' }
  }
  Invoke-ProductPolicyStage $State 'profile' { Assert-ProductProfileBeforeRemoval $State }
  Invoke-ProductPolicyStage $State 'retained_data' { Assert-ProductManifest $State.Data @(Get-InstalledSmokeTree $State.Root (Join-Path $State.Root 'retained data')) 'data_before_removal' }
  Invoke-ProductPolicyStage $State 'sentinel' {
    if ((Get-InstalledSmokeHash (Join-Path $State.Root 'unrelated.sentinel')) -cne $State.SentinelHash) { throw 'product_sentinel_changed' }
  }
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
  if ($fields.Count -ne 1 -or -not [string]::Equals($fields[0].Name, $State.Hook, [StringComparison]::Ordinal) -or
    -not [string]::Equals($fields[0].Value, $saved, [StringComparison]::Ordinal) -or
    -not [string]::Equals((Get-InstalledSmokeHash $saved), $State.HookHash, [StringComparison]::Ordinal)) { throw 'product_helper_backup_invalid' }
  $shortcuts = New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
  foreach ($row in $State.ProfileShortcutRecords) { [void]$shortcuts.Add($row.Path) }
  $retained = @($State.SetupProfile | Where-Object { -not $shortcuts.Contains($_.Path) })
  $backups = @(@($manifest, $saved) | ForEach-Object {
    [pscustomobject]@{ Path = $_.Substring($State.Environment.HOME.Length + 1).Replace('\', '/'); Sha256 = Get-InstalledSmokeHash $_ }
  })
  Assert-ProductManifest @(@($retained) + $backups | Sort-Object Path) @(Get-InstalledSmokeTree $State.Environment.HOME $State.Environment.HOME | Sort-Object Path) 'profile_after_removal'
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
  Assert-ProductManifest $State.Data @(Get-InstalledSmokeTree $State.Root (Join-Path $State.Root 'retained data')) 'data_after_removal'
  if ((Get-InstalledSmokeHash (Join-Path $State.Root 'unrelated.sentinel')) -cne $State.SentinelHash) { throw 'product_sentinel_changed' }
}

function Get-ProductRebootSnapshot($Request) {
  return [Arkism.InstallerChecks.RebootObservation]::ReadWindows($Request)
}

function Invoke-ProductBoundRemoval($State) {
  $binding = New-Object Arkism.InstallerChecks.ProductUninstallBinding($State.Root, $State.Context.OwnedRoots[$State.Root],
    $State.Context.SourceSha, $State.Install, $State.Setup, $State.SetupHash, $State.UninstallerHash)
  $policy = New-ProductUninstallPolicy $State
  return [Arkism.InstallerChecks.ProductUninstallCheck]::RunWindows($binding, $policy)
}

function New-ProductSyntheticData($State) {
  if (-not $State.ContainsKey('SetupProfile')) { throw 'product_profile_not_captured' }
  $State.Hook = ConvertTo-InstalledSmokePath (Join-Path $State.Environment.HOME '.config/arkism/hook-codex.cmd')
  if ($null -ne (Get-InstalledSmokeAttributes $State.Hook)) { throw 'product_hook_already_exists' }
  if ($null -ne (Get-InstalledSmokeAttributes (Join-Path $State.Environment.HOME '.config/arkism/backups'))) {
    throw 'product_backup_namespace_exists'
  }
  $data = Join-Path $State.Root 'retained data'; [IO.Directory]::CreateDirectory($data) | Out-Null
  [IO.File]::WriteAllText((Join-Path $data 'closed-memory.fixture'), 'Synthetic closed-file preservation only.')
  [IO.File]::WriteAllText((Join-Path $State.Root 'unrelated.sentinel'), 'Unrelated owned file.')
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($State.Hook)) | Out-Null
  $stream = [IO.File]::Open($State.Hook, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
  try {
    $bytes = [Text.Encoding]::UTF8.GetBytes('Inert text; never executed or registered.')
    $stream.Write($bytes, 0, $bytes.Length)
  } finally { $stream.Dispose() }
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
    $report.profile_check = Assert-InstalledSmokeIsolatedFolders $context $stage $state.Environment
    Assert-ProductProfileBeforeSetup $state
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
    Initialize-ProductProfileBaseline $state
    $report.profile_files_after_setup = @($state.SetupProfile).Count
    New-ProductSyntheticData $state
    Invoke-ProductRemovalCheck $state $request $report
    $report.ok = $true; $report.phase = 'complete'
  } catch {
    $primary = $_; $report.error = 'product_check_failed'; $report.error_type = $_.Exception.GetType().FullName
    Add-ProductManifestDiagnostic $report $_
    $failure = Find-JobFixtureFailure $_
    if ($null -ne $failure) { Add-ProductJobResult $report $failure.Result; $report.job_errors = @($failure.Result.Errors) }
  }
  finally {
    try { Set-ProductRetention $state $report } catch { $cleanup = $_ }
    Add-ProductPolicyDiagnostic $report $state
    try { Write-InstalledSmokeEvidence $evidence $report } catch { $evidenceError = $_ }
  }
  Assert-InstalledSmokeOutcome $primary $cleanup $evidenceError
  return $report
}

if ($MyInvocation.InvocationName -ne '.') {
  Invoke-WindowsProductUninstall $InstallerPath $StagePath $Version $SourceSha $EvidencePath | Out-Null
}
