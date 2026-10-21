# Fresh installed-binary check for a disposable GitHub-hosted Windows runner.
# Dot-sourcing defines helpers only. Never executes the product uninstaller.
param([string]$InstallerPath, [string]$StagePath, [string]$Version,
      [string]$SourceSha, [string]$EvidencePath)

function ConvertTo-InstalledSmokePath([string]$Path) {
  if ([string]::IsNullOrWhiteSpace($Path)) { throw 'smoke_path_missing' }
  $provider = $null
  $drive = $null
  $resolved = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path, [ref]$provider, [ref]$drive)
  if ($provider.Name -ne 'FileSystem') { throw 'smoke_filesystem_required' }
  return [IO.Path]::GetFullPath($resolved)
}

function Get-InstalledSmokeAttributes([string]$Path) {
  try { return [IO.File]::GetAttributes($Path) }
  catch [IO.FileNotFoundException] { return $null }
  catch [IO.DirectoryNotFoundException] { return $null }
}

function Assert-InstalledSmokeDirectory([string]$Path, [switch]$AllowMissing) {
  $full = ConvertTo-InstalledSmokePath $Path
  $parent = [IO.Path]::GetDirectoryName($full.TrimEnd([IO.Path]::DirectorySeparatorChar))
  if ($parent -and $parent -ne $full) { Assert-InstalledSmokeDirectory $parent -AllowMissing:$AllowMissing }
  $attrs = Get-InstalledSmokeAttributes $full
  if ($null -eq $attrs) {
    if ($AllowMissing) { return }
    throw "smoke_directory_missing: $full"
  }
  if (($attrs -band [IO.FileAttributes]::ReparsePoint) -or -not ($attrs -band [IO.FileAttributes]::Directory)) {
    throw "smoke_unsafe_directory: $full"
  }
}

function Assert-InstalledSmokeFile([string]$Path) {
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($Path))
  $attrs = Get-InstalledSmokeAttributes $Path
  if ($null -eq $attrs -or ($attrs -band ([IO.FileAttributes]::ReparsePoint -bor [IO.FileAttributes]::Directory))) {
    throw "smoke_regular_file_required: $Path"
  }
}

function Test-InstalledSmokeWithin([string]$Path, [string]$Root) {
  $prefix = $Root.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
  return $Path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
}

function Get-InstalledSmokeHash([string]$Path) {
  Assert-InstalledSmokeFile $Path
  return (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
}

function Get-InstalledSmokeTree([string]$Root, [string]$Directory) {
  Assert-InstalledSmokeDirectory $Directory
  foreach ($path in [IO.Directory]::EnumerateFileSystemEntries($Directory)) {
    $attrs = Get-InstalledSmokeAttributes $path
    if ($null -eq $attrs -or ($attrs -band [IO.FileAttributes]::ReparsePoint)) { throw "smoke_tree_changed: $path" }
    if ($attrs -band [IO.FileAttributes]::Directory) { Get-InstalledSmokeTree $Root $path; continue }
    [pscustomobject]@{ Path = $path.Substring($Root.Length + 1).Replace('\', '/'); Sha256 = Get-InstalledSmokeHash $path }
  }
}

function Get-InstalledSmokePayload([string]$Stage) {
  $rows = @([pscustomobject]@{ Path = 'Arkism.exe'; Sha256 = Get-InstalledSmokeHash (Join-Path $Stage 'Arkism.exe') },
    [pscustomobject]@{ Path = 'uninstall.exe'; Sha256 = Get-InstalledSmokeHash (Join-Path $Stage 'uninstall.exe') })
  $rows += @(Get-InstalledSmokeTree $Stage (Join-Path $Stage 'integration'))
  $rows += @(Get-InstalledSmokeTree $Stage (Join-Path $Stage 'runtime'))
  $required = @('runtime/arkism-installer.exe', 'uninstall.exe', 'integration/arkism.exe', 'runtime/memoryd.exe', 'runtime/memory-principal.exe', 'runtime/roles.sql',
    'runtime/pgsql/bin/postgres.exe', 'runtime/pgsql/bin/pg_ctl.exe', 'runtime/pgsql/bin/initdb.exe',
    'runtime/pgsql/bin/psql.exe', 'runtime/pgsql/bin/pg_dump.exe', 'runtime/pgsql/bin/pg_restore.exe',
    'runtime/pgsql/lib/vector.dll', 'runtime/pgsql/share/extension/vector.control')
  foreach ($name in $required) {
    if ($name -notin $rows.Path) { throw "smoke_payload_missing: $name" }
  }
  if (@($rows | Where-Object { $_.Path -like 'integration/*/*' }).Count) { throw 'smoke_nested_integration_unsupported' }
  return @($rows | Sort-Object Path)
}

function Get-InstalledSmokeContext([string]$SourceSha) {
  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT -or
      $env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { throw 'smoke_hosted_windows_required' }
  if ($SourceSha -notmatch '^[0-9a-f]{40}$' -or $env:GITHUB_SHA -cne $SourceSha) { throw 'smoke_source_sha_mismatch' }
  $homePaths = @($env:HOME, $env:USERPROFILE, [Environment]::GetFolderPath('UserProfile')) |
    Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Unique
  return @{
    SourceSha = $SourceSha; RunId = "$($env:GITHUB_RUN_ID)-$($env:GITHUB_RUN_ATTEMPT)"
    RunnerTemp = ConvertTo-InstalledSmokePath $env:RUNNER_TEMP
    Programs = [Environment]::GetFolderPath('Programs'); Desktop = [Environment]::GetFolderPath('DesktopDirectory')
    HomePaths = @($homePaths); AppData = [Environment]::GetFolderPath('ApplicationData')
    LocalAppData = [Environment]::GetFolderPath('LocalApplicationData'); Xdg = $env:XDG_CONFIG_HOME
  }
}

function Get-InstalledSmokeRegistry {
  $keyName = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\Arkism'
  foreach ($hive in @('CurrentUser', 'LocalMachine')) {
    foreach ($view in @('Registry32', 'Registry64')) {
      $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::$hive, [Microsoft.Win32.RegistryView]::$view)
      try {
        $key = $base.OpenSubKey($keyName)
        if ($null -eq $key) { continue }
        try {
          $values = @{}
          $kinds = @{}
          foreach ($name in $key.GetValueNames()) { $values[$name] = $key.GetValue($name); $kinds[$name] = $key.GetValueKind($name).ToString() }
          [pscustomobject]@{ Hive = $hive; View = $view; Values = $values; Kinds = $kinds; SubKeys = @($key.GetSubKeyNames()) }
        } finally { $key.Dispose() }
      } finally { $base.Dispose() }
    }
  }
}

function Get-InstalledSmokeAutostart {
  foreach ($hive in @('CurrentUser', 'LocalMachine')) {
    foreach ($view in @('Registry32', 'Registry64')) { Get-InstalledSmokeAutostartView $hive $view }
  }
}

function Get-InstalledSmokeAutostartView([string]$Hive, [string]$View) {
  $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::$Hive, [Microsoft.Win32.RegistryView]::$View)
  try {
    foreach ($name in @('Software\Microsoft\Windows\CurrentVersion\Run', 'Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run')) {
      $key = $base.OpenSubKey($name)
      if ($null -eq $key) { continue }
      try { if ('Arkism' -in $key.GetValueNames()) { "$Hive/$View/$name/Arkism" } }
      finally { $key.Dispose() }
    }
  } finally { $base.Dispose() }
}

function Get-InstalledSmokeResidualPaths($Context) {
  foreach ($folder in $Context.HomePaths) {
    foreach ($name in @('.config/arkism', '.codex', '.claude', '.claude.json', '.gemini', '.grok', '.zcode', '.dsh', '.cursor', '.config/opencode')) {
      Join-Path $folder $name
    }
  }
  foreach ($folder in @($Context.AppData, $env:APPDATA) | Select-Object -Unique) {
    if ($folder) { Join-Path $folder 'Claude' }
  }
  foreach ($folder in @($Context.LocalAppData, $env:LOCALAPPDATA) | Select-Object -Unique) {
    if (-not $folder) { continue }
    Join-Path $folder 'Programs/Arkism'
    Join-Path $folder 'dev.arkism.desktop'
    $packages = Join-Path $folder 'Packages'
    Assert-InstalledSmokeDirectory $packages -AllowMissing
    if ($null -eq (Get-InstalledSmokeAttributes $packages)) { continue }
    [IO.Directory]::EnumerateDirectories($packages, 'Claude*')
  }
  if ($Context.Xdg) { Join-Path $Context.Xdg 'opencode' }
}

function Get-InstalledSmokeShortcut([string]$Path) {
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($Path)) -AllowMissing
  if ($null -eq (Get-InstalledSmokeAttributes $Path)) { return }
  Assert-InstalledSmokeFile $Path
  $shell = New-Object -ComObject WScript.Shell
  try {
    $link = $shell.CreateShortcut($Path)
    try { [pscustomobject]@{ Path = $Path; Target = $link.TargetPath; Arguments = $link.Arguments } }
    finally { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($link) }
  } finally { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) }
}

function Add-InstalledSmokeOwnedFolders($Context, [string]$Root, $Environment) {
  Assert-InstalledSmokeOwnedRoot $Context $Root
  foreach ($name in @('APPDATA', 'USERPROFILE')) {
    $path = ConvertTo-InstalledSmokePath $Environment[$name]
    if (-not (Test-InstalledSmokeWithin $path $Root)) { throw 'smoke_child_profile_not_owned' }
    Assert-InstalledSmokeDirectory $path -AllowMissing
  }
  $folders = @{
    Programs = Join-Path $Environment['APPDATA'] 'Microsoft/Windows/Start Menu/Programs'
    Desktop = Join-Path $Environment['USERPROFILE'] 'Desktop'
  }
  foreach ($name in @('Programs', 'Desktop')) {
    $folders[$name] = ConvertTo-InstalledSmokePath $folders[$name]
    if (-not (Test-InstalledSmokeWithin $folders[$name] $Root)) { throw 'smoke_child_folder_not_owned' }
    Assert-InstalledSmokeDirectory $folders[$name] -AllowMissing
    [IO.Directory]::CreateDirectory($folders[$name]) | Out-Null
  }
  $Context.OwnedShortcutFolders = $folders
}

# Environment variables alone do not redirect an absolute/OneDrive Desktop.
# Ask the shipping helper to compare the same physical known folders it uses.
function Assert-InstalledSmokeIsolatedFolders($Context, [string]$Stage, $Environment) {
  $helper = Join-Path $Stage 'runtime/arkism-installer.exe'
  Assert-InstalledSmokeFile $helper
  $folders = $Context.OwnedShortcutFolders
  $arguments = 'check-folders "' + $folders.Programs + '" "' + $folders.Desktop + '"'
  Invoke-InstalledSmokeProcess $helper $arguments $Environment 30 'profile_check'
}

function Get-InstalledSmokeShortcutCandidates($Context) {
  $seen = @{}
  $locations = @([pscustomobject]@{ Scope = 'real'; Folders = $Context })
  if ($Context.ContainsKey('OwnedShortcutFolders')) {
    $locations += [pscustomobject]@{ Scope = 'owned'; Folders = $Context.OwnedShortcutFolders }
  }
  foreach ($location in $locations) {
    foreach ($role in @('Programs', 'Desktop')) {
      $path = Join-Path (ConvertTo-InstalledSmokePath $location.Folders[$role]) 'Arkism.lnk'
      if ($seen.ContainsKey($path)) {
        if ($seen[$path] -cne $role) { throw 'smoke_shortcut_role_ambiguous' }
        continue
      }
      $seen[$path] = $role
      [pscustomobject]@{ Path = $path; Role = $role; Scope = $location.Scope }
    }
  }
}

function Get-InstalledSmokeShortcuts($Context) {
  foreach ($candidate in Get-InstalledSmokeShortcutCandidates $Context) {
    $link = Get-InstalledSmokeShortcut $candidate.Path
    if ($null -ne $link) {
      [pscustomobject]@{ Path = $link.Path; Target = $link.Target; Arguments = $link.Arguments
        Role = $candidate.Role; Scope = $candidate.Scope }
    }
  }
}

function Get-InstalledSmokeGlobalState($Context) {
  $residual = @()
  foreach ($path in @(Get-InstalledSmokeResidualPaths $Context) | Select-Object -Unique) {
    Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($path)) -AllowMissing
    if ($null -ne (Get-InstalledSmokeAttributes $path)) { $residual += $path }
  }
  $names = @('Arkism-Work-Drain-claude_code', 'Arkism-Work-Drain-codex', 'Arkism-Work-Drain-zcode', 'Arkism-Zcode-Export')
  $tasks = @(Get-ScheduledTask -ErrorAction Stop | Where-Object { $_.TaskPath -ceq '\' -and $_.TaskName -in $names })
  return @{
    Registrations = @(Get-InstalledSmokeRegistry); Autostart = @(Get-InstalledSmokeAutostart)
    Tasks = @($tasks | ForEach-Object { $_.TaskName }); Residual = $residual
    Shortcuts = @(Get-InstalledSmokeShortcuts $Context)
  }
}

function Assert-InstalledSmokeFreshState($State) {
  foreach ($name in @('Registrations', 'Shortcuts', 'Autostart', 'Tasks', 'Residual')) {
    if (@($State[$name]).Count) { throw "smoke_preexisting_state: $name" }
  }
}

function Get-InstalledSmokeProcesses([string]$InstallPath) {
  foreach ($process in Get-CimInstance Win32_Process -ErrorAction Stop) {
    if (-not $InstallPath) {
      if ($process.Name -in @('Arkism.exe', 'arkism.exe', 'memoryd.exe', 'memory-principal.exe')) { $process }
      continue
    }
    if (-not $process.ExecutablePath -and $process.Name -in @('Arkism.exe', 'arkism.exe', 'memoryd.exe', 'memory-principal.exe', 'postgres.exe')) {
      throw 'smoke_process_image_unavailable'
    }
    if ($process.ExecutablePath -and (Test-InstalledSmokeWithin $process.ExecutablePath $InstallPath)) { $process }
  }
}

function Assert-InstalledSmokeNoProcesses([string]$InstallPath, [string]$ErrorCode) {
  if (@(Get-InstalledSmokeProcesses $InstallPath).Count) { throw $ErrorCode }
}

function New-InstalledSmokeEnvironment([string]$Root) {
  $child = @{}
  foreach ($name in @('SystemRoot', 'WINDIR', 'SystemDrive', 'ComSpec', 'PATHEXT', 'PROCESSOR_ARCHITECTURE', 'NUMBER_OF_PROCESSORS')) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($value) { $child[$name] = $value }
  }
  $child['PATH'] = "$($child['SystemRoot'])\System32;$($child['SystemRoot'])\System32\WindowsPowerShell\v1.0"
  foreach ($name in @('HOME', 'USERPROFILE')) { $child[$name] = Join-Path $Root 'profile' }
  $child['APPDATA'] = Join-Path $Root 'profile/AppData/Roaming'
  $child['LOCALAPPDATA'] = Join-Path $Root 'profile/AppData/Local'
  $child['XDG_CONFIG_HOME'] = Join-Path $Root 'profile/.config'
  foreach ($name in @('TEMP', 'TMP')) { $child[$name] = Join-Path $Root 'temp' }
  return $child
}

function Invoke-InstalledSmokeProcess([string]$FilePath, [string]$Arguments, $Environment, [int]$TimeoutSeconds, [string]$Phase) {
  Assert-InstalledSmokeFile $FilePath
  $info = New-Object Diagnostics.ProcessStartInfo
  $info.FileName = $FilePath; $info.Arguments = $Arguments; $info.UseShellExecute = $false
  $info.CreateNoWindow = $true; $info.WorkingDirectory = [IO.Path]::GetDirectoryName($FilePath)
  $info.EnvironmentVariables.Clear()
  foreach ($name in $Environment.Keys) { $info.EnvironmentVariables[$name] = $Environment[$name] }
  $process = [Diagnostics.Process]::Start($info)
  try {
    if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
      $process.Kill(); [void]$process.WaitForExit(10000)
      throw "smoke_process_timeout: $Phase"
    }
    if ($process.ExitCode -ne 0) { throw "smoke_process_failed: $Phase exit=$($process.ExitCode)" }
    return [pscustomobject]@{ Phase = $Phase; ExitCode = $process.ExitCode }
  } finally { $process.Dispose() }
}

function Assert-InstalledSmokeRegistration($State, [string]$InstallPath, [string]$Version, [switch]$RequireComplete) {
  $expected = @{
    DisplayName = 'Arkism'; DisplayVersion = $Version; Publisher = 'Arkism contributors'
    DisplayIcon = (Join-Path $InstallPath 'Arkism.exe'); InstallLocation = $InstallPath
    UninstallString = ('"' + (Join-Path $InstallPath 'uninstall.exe') + '"'); NoModify = 1; NoRepair = 1
  }
  foreach ($record in $State.Registrations) {
    if ($record.Hive -ne 'CurrentUser' -or $record.Values.Count -ne $expected.Count -or @($record.SubKeys).Count) { throw 'smoke_registration_not_owned' }
    foreach ($name in $expected.Keys) {
      Assert-InstalledSmokeRegistryValue $record.Values $record.Kinds $expected $name
    }
  }
  foreach ($shortcut in $State.Shortcuts) {
    if ($shortcut.Target -ine $expected.DisplayIcon -or $shortcut.Arguments) { throw 'smoke_shortcut_not_owned' }
  }
  if ($RequireComplete) { Assert-InstalledSmokeCompleteRegistration $State }
  Assert-InstalledSmokeStableState $State
}

function Assert-InstalledSmokeCompleteRegistration($State) {
  if (@($State.Registrations).Count -eq 0 -or @($State.Shortcuts).Count -ne 2) { throw 'smoke_registration_incomplete' }
  foreach ($role in @('Programs', 'Desktop')) {
    if (@($State.Shortcuts | Where-Object { $_.Role -ceq $role }).Count -ne 1) { throw 'smoke_shortcut_roles_incomplete' }
  }
}

function Get-InstalledSmokeRegistrationSummary($State) {
  return [ordered]@{
    registrations = @($State.Registrations).Count; shortcuts = @($State.Shortcuts).Count
    real_shortcuts = @($State.Shortcuts | Where-Object { $_.Scope -ceq 'real' }).Count
    owned_shortcuts = @($State.Shortcuts | Where-Object { $_.Scope -ceq 'owned' }).Count
    programs_shortcuts = @($State.Shortcuts | Where-Object { $_.Role -ceq 'Programs' }).Count
    desktop_shortcuts = @($State.Shortcuts | Where-Object { $_.Role -ceq 'Desktop' }).Count
    string_registry_values = @($State.Registrations | ForEach-Object { $_.Kinds.Values } | Where-Object { $_ -ceq 'String' }).Count
    dword_registry_values = @($State.Registrations | ForEach-Object { $_.Kinds.Values } | Where-Object { $_ -ceq 'DWord' }).Count
  }
}

function Assert-InstalledSmokeStableState($State) {
  foreach ($name in @('Autostart', 'Tasks', 'Residual')) {
    if (@($State[$name]).Count) { throw "smoke_global_state_changed: $name" }
  }
}

function Assert-InstalledSmokeRegistryValue($Values, $Kinds, $Expected, [string]$Name) {
  if (-not $Values.ContainsKey($Name) -or $Values[$Name] -cne $Expected[$Name]) { throw "smoke_registration_not_owned: $Name" }
  if ($Expected[$Name] -is [string] -and $Values[$Name] -isnot [string]) { throw 'smoke_registration_type_changed' }
  if ($Expected[$Name] -is [int] -and $Values[$Name] -isnot [int]) { throw 'smoke_registration_type_changed' }
  $kind = if ($Expected[$Name] -is [int]) { 'DWord' } else { 'String' }
  if ($null -eq $Kinds -or -not $Kinds.ContainsKey($Name) -or $Kinds[$Name] -cne $kind) { throw 'smoke_registration_kind_changed' }
}

function Remove-InstalledSmokeShortcut([string]$Path) {
  Assert-InstalledSmokeFile $Path
  [IO.File]::Delete($Path)
}

function Remove-InstalledSmokeRegistryKey($Record) {
  $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::CurrentUser, [Microsoft.Win32.RegistryView]::$($Record.View))
  try { $base.DeleteSubKeyTree('Software\Microsoft\Windows\CurrentVersion\Uninstall\Arkism', $false) }
  finally { $base.Dispose() }
}

function Remove-InstalledSmokeRegistration($Context, [string]$InstallPath, [string]$Version) {
  $state = Get-InstalledSmokeGlobalState $Context
  Assert-InstalledSmokeRegistration $state $InstallPath $Version
  foreach ($link in $state.Shortcuts) { Remove-InstalledSmokeShortcut $link.Path }
  foreach ($record in $state.Registrations) { Remove-InstalledSmokeRegistryKey $record }
}

function Assert-InstalledSmokePayload([string]$InstallPath, $Manifest) {
  foreach ($row in $Manifest) {
    $path = Join-Path $InstallPath $row.Path
    if ((Get-InstalledSmokeHash $path) -cne $row.Sha256) { throw "smoke_installed_hash_mismatch: $($row.Path)" }
  }
  Assert-InstalledSmokeFile (Join-Path $InstallPath 'uninstall.exe')
  $actual = @(Get-InstalledSmokeTree $InstallPath $InstallPath)
  if ($actual.Count -ne @($Manifest).Count) { throw 'smoke_installed_unexpected_file' }
}

function Read-InstalledSmokeResult([string]$Path) {
  Assert-InstalledSmokeFile $Path
  if (([IO.FileInfo]$Path).Length -gt 65536) { throw 'smoke_result_too_large' }
  $result = [IO.File]::ReadAllText($Path) | ConvertFrom-Json -ErrorAction Stop
  . (Join-Path $PSScriptRoot 'runtime-check-result.ps1')
  Assert-RuntimeTokenEvidence $result
  if ($result.phase -cne 'complete') { throw 'smoke_runtime_incomplete' }
  if ($result.ok -isnot [bool] -or -not $result.ok -or $result.reused -isnot [bool] -or -not $result.reused -or
      $result.safety_copy -isnot [bool] -or $result.safety_copy -or $result.expiry -cne 'ok') { throw 'smoke_result_failed' }
  foreach ($name in @('capabilities', 'restored_capabilities')) {
    if (-not (Test-InstalledSmokeInteger $result.$name) -or $result.$name -ne 200) { throw 'smoke_capabilities_failed' }
  }
  if (-not (Test-InstalledSmokeInteger $result.backup_bytes) -or $result.backup_bytes -le 0) { throw 'smoke_backup_failed' }
  $uri = $null
  if (-not [Uri]::TryCreate($result.server, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -cne 'http' -or
      $uri.Host -cne '127.0.0.1' -or $uri.Port -le 0 -or $uri.UserInfo -or $uri.AbsolutePath -ne '/' -or $uri.Query -or $uri.Fragment) { throw 'smoke_server_not_loopback' }
  return [ordered]@{ ok = $true; capabilities = 200; restored_capabilities = 200; reused = $true; backup_bytes = $result.backup_bytes; expiry = 'ok'; safety_copy = $false; token_checks = $result.token_checks }
}

function Test-InstalledSmokeInteger($Value) {
  return $Value -is [int] -or $Value -is [long]
}

function Assert-InstalledSmokeTree([string]$Directory) {
  Assert-InstalledSmokeDirectory $Directory
  foreach ($path in [IO.Directory]::EnumerateFileSystemEntries($Directory)) {
    $attrs = Get-InstalledSmokeAttributes $path
    if ($null -eq $attrs -or ($attrs -band [IO.FileAttributes]::ReparsePoint)) { throw "smoke_tree_changed: $path" }
    if ($attrs -band [IO.FileAttributes]::Directory) { Assert-InstalledSmokeTree $path }
  }
}

function New-InstalledSmokeOwnedRoot($Context, [string]$Kind) {
  if ($Kind -notmatch '^[a-z-]+$') { throw 'smoke_root_kind_invalid' }
  Assert-InstalledSmokeDirectory $Context.RunnerTemp
  $marker = [Guid]::NewGuid().ToString('N')
  $root = Join-Path $Context.RunnerTemp ("arkism $Kind $marker")
  if ($root -match '[\x00-\x1f''"\[\]*?]') { throw 'smoke_unsupported_install_path' }
  if ($null -ne (Get-InstalledSmokeAttributes $root)) { throw 'smoke_root_exists' }
  [IO.Directory]::CreateDirectory($root) | Out-Null
  [IO.File]::WriteAllText((Join-Path $root '.installed-smoke-owner'), $marker)
  if (-not $Context.ContainsKey('OwnedRoots')) { $Context.OwnedRoots = @{} }
  $Context.OwnedRoots[$root] = $marker
  return $root
}

function Assert-InstalledSmokeOwnedRoot($Context, [string]$Root) {
  if (-not $Context.ContainsKey('OwnedRoots') -or -not $Context.OwnedRoots.ContainsKey($Root) -or
      -not (Test-InstalledSmokeWithin $Root $Context.RunnerTemp)) { throw 'smoke_root_not_owned' }
  Assert-InstalledSmokeDirectory $Root
  $markerPath = Join-Path $Root '.installed-smoke-owner'
  Assert-InstalledSmokeFile $markerPath
  if ([IO.File]::ReadAllText($markerPath) -cne $Context.OwnedRoots[$Root]) { throw 'smoke_root_not_owned' }
  Assert-InstalledSmokeTree $Root
}

function Remove-InstalledSmokeOwnedRoot($Context, [string]$Root) {
  Assert-InstalledSmokeOwnedRoot $Context $Root
  Clear-InstalledSmokeOwnedReadOnly $Root
  Assert-InstalledSmokeOwnedRoot $Context $Root
  [IO.Directory]::Delete($Root, $true)
  $Context.OwnedRoots.Remove($Root)
}

function Clear-InstalledSmokeOwnedReadOnly([string]$Directory) {
  Assert-InstalledSmokeDirectory $Directory
  foreach ($path in [IO.Directory]::EnumerateFileSystemEntries($Directory)) {
    $attrs = Get-InstalledSmokeAttributes $path
    if ($null -eq $attrs -or ($attrs -band [IO.FileAttributes]::ReparsePoint)) { throw 'smoke_tree_changed' }
    if ($attrs -band [IO.FileAttributes]::Directory) { Clear-InstalledSmokeOwnedReadOnly $path }
    Clear-InstalledSmokeReadOnlyEntry $path ([bool]($attrs -band [IO.FileAttributes]::Directory))
  }
  Clear-InstalledSmokeReadOnlyEntry $Directory $true
}

function Clear-InstalledSmokeReadOnlyEntry([string]$Path, [bool]$Directory) {
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($Path))
  $attrs = Get-InstalledSmokeAttributes $Path
  if ($null -eq $attrs -or ($attrs -band [IO.FileAttributes]::ReparsePoint)) { throw 'smoke_tree_changed' }
  if ([bool]($attrs -band [IO.FileAttributes]::Directory) -ne $Directory) { throw 'smoke_tree_type_changed' }
  if ($attrs -band [IO.FileAttributes]::ReadOnly) {
    Set-InstalledSmokeAttributes $Path ($attrs -band (-bnot [IO.FileAttributes]::ReadOnly))
  }
}

function Set-InstalledSmokeAttributes([string]$Path, [IO.FileAttributes]$Attributes) {
  [IO.File]::SetAttributes($Path, $Attributes)
}

function Get-InstalledSmokeOwnedAttributes($Context, [string]$Root) {
  Assert-InstalledSmokeOwnedRoot $Context $Root
  $entries = @(Get-InstalledSmokeTreeAttributes $Root)
  return [ordered]@{ entries = $entries.Count
    readonly_entries = @($entries | Where-Object { $_ -band [IO.FileAttributes]::ReadOnly }).Count }
}

function Get-InstalledSmokeTreeAttributes([string]$Directory) {
  Assert-InstalledSmokeDirectory $Directory
  Get-InstalledSmokeAttributes $Directory
  foreach ($path in [IO.Directory]::EnumerateFileSystemEntries($Directory)) {
    $attrs = Get-InstalledSmokeAttributes $path
    if ($null -eq $attrs -or ($attrs -band [IO.FileAttributes]::ReparsePoint)) { throw 'smoke_tree_changed' }
    if ($attrs -band [IO.FileAttributes]::Directory) { Get-InstalledSmokeTreeAttributes $path }
    else { $attrs }
  }
}

function Write-InstalledSmokeEvidence([string]$Path, $Report) {
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($Path))
  $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
  try {
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes(($Report | ConvertTo-Json -Depth 8))
    $stream.Write($bytes, 0, $bytes.Length)
  } finally { $stream.Dispose() }
}

function Assert-InstalledSmokeOutcome($Primary, $Cleanup, $EvidenceError) {
  $errors = @()
  if ($Primary) { $errors += $Primary.Exception.Message }
  if ($Cleanup) { $errors += "cleanup: $($Cleanup.Exception.Message)" }
  if ($EvidenceError) { $errors += "evidence: $($EvidenceError.Exception.Message)" }
  if ($errors.Count) { throw ($errors -join '; ') }
}

function Invoke-InstalledSmoke([string]$InstallerPath, [string]$StagePath, [string]$Version, [string]$SourceSha, [string]$EvidencePath) {
  $ErrorActionPreference = 'Stop'
  if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw 'smoke_version_invalid' }
  $context = Get-InstalledSmokeContext $SourceSha
  $installer = ConvertTo-InstalledSmokePath $InstallerPath
  # Constrain the caller's same-build artifact input; a filename is not a signature.
  if ([IO.Path]::GetFileName($installer) -cne "Arkism-$Version-setup.exe") { throw 'smoke_installer_name_mismatch' }
  $stage = ConvertTo-InstalledSmokePath $StagePath
  $evidence = ConvertTo-InstalledSmokePath $EvidencePath
  Assert-InstalledSmokeDirectory $context.RunnerTemp
  Assert-InstalledSmokeDirectory $context.Programs
  Assert-InstalledSmokeDirectory $context.Desktop
  $manifest = @(Get-InstalledSmokePayload $stage)
  $installerHash = Get-InstalledSmokeHash $installer
  Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
  Assert-InstalledSmokeNoProcesses '' 'smoke_preexisting_process'
  if ((Test-InstalledSmokeWithin $context.RunnerTemp $stage) -or $context.RunnerTemp -ieq $stage) { throw 'smoke_path_overlap' }
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($evidence))
  if ($null -ne (Get-InstalledSmokeAttributes $evidence)) { throw 'smoke_evidence_exists' }
  $root = New-InstalledSmokeOwnedRoot $context 'installed-smoke'
  $install = Join-Path $root 'installed Arkism'
  $data = Join-Path $root 'runtime data'
  $childEnv = New-InstalledSmokeEnvironment $root
  $report = [ordered]@{ source_sha = $SourceSha; version = $Version; run_id = $context.RunId; installer_sha256 = $installerHash; installer_bytes = ([IO.FileInfo]$installer).Length; payload = $manifest; ok = $false; cleanup = 'not_started' }
  $primary = $null; $cleanup = $null; $evidenceError = $null
  try {
    foreach ($path in @($data, $childEnv['HOME'], $childEnv['APPDATA'], $childEnv['LOCALAPPDATA'], $childEnv['XDG_CONFIG_HOME'], $childEnv['TEMP'])) { [IO.Directory]::CreateDirectory($path) | Out-Null }
    Add-InstalledSmokeOwnedFolders $context $root $childEnv
    Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
    $report.profile_check = Assert-InstalledSmokeIsolatedFolders $context $stage $childEnv
    $report.install = Invoke-InstalledSmokeProcess $installer "/S /NoAutoRun /D=$install" $childEnv 180 'install'
    if ((Get-InstalledSmokeHash $installer) -cne $installerHash) { throw 'smoke_installer_changed' }
    Assert-InstalledSmokePayload $install $manifest
    $state = Get-InstalledSmokeGlobalState $context
    $report.registration = Get-InstalledSmokeRegistrationSummary $state
    Assert-InstalledSmokeRegistration $state $install $Version -RequireComplete
    Assert-InstalledSmokeNoProcesses $install 'smoke_unexpected_autorun'
    $report.runtime = Invoke-InstalledSmokeProcess (Join-Path $install 'Arkism.exe') ('--runtime-check "' + $data + '"') $childEnv 300 'runtime_check'
    $report.result = Read-InstalledSmokeResult (Join-Path $data 'check.json')
    Assert-InstalledSmokePayload $install $manifest
    if ((Get-InstalledSmokeHash $installer) -cne $installerHash) { throw 'smoke_installer_changed' }
    $report.ok = $true
  } catch { $primary = $_ }
  finally {
    try {
      Assert-InstalledSmokeNoProcesses $install 'smoke_owned_process_still_running'
      Assert-InstalledSmokeOwnedRoot $context $root
      $report.owned_attributes = Get-InstalledSmokeOwnedAttributes $context $root
      Remove-InstalledSmokeRegistration $context $install $Version
      Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
      Remove-InstalledSmokeOwnedRoot $context $root
      $report.cleanup = 'owned_manual_cleanup'
    } catch { $cleanup = $_; $report.cleanup = 'failed' }
    if ($primary) { $report.error = $primary.Exception.Message }
    if ($cleanup) { $report.cleanup_error = $cleanup.Exception.Message; $report.ok = $false }
    try { Write-InstalledSmokeEvidence $evidence $report } catch { $evidenceError = $_ }
  }
  Assert-InstalledSmokeOutcome $primary $cleanup $evidenceError
  return $report
}

if ($MyInvocation.InvocationName -ne '.') {
  Invoke-InstalledSmoke -InstallerPath $InstallerPath -StagePath $StagePath -Version $Version -SourceSha $SourceSha -EvidencePath $EvidencePath | Out-Null
}
