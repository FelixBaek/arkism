# Offline tests run the actual wrapper with inert files and explicit effect fakes.
# No installer, runtime, registry, task, service, model or network is invoked.
$ErrorActionPreference = 'Stop'
$sourcePath = Join-Path $PSScriptRoot 'smoke-windows-installed.ps1'
$sourceHash = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash
. $sourcePath
$caseCount = 0
$originalSmokeEvidenceWriter = (Get-Command Write-InstalledSmokeEvidence).ScriptBlock
$originalSmokeAttributes = (Get-Command Get-InstalledSmokeAttributes).ScriptBlock
$originalSmokeGlobalState = (Get-Command Get-InstalledSmokeGlobalState).ScriptBlock
$originalSmokeSetAttributes = (Get-Command Set-InstalledSmokeAttributes).ScriptBlock

function Assert-SmokeTest([bool]$Condition, [string]$Message) {
  if (-not $Condition) { throw $Message }
}

function Write-SmokeTestFile([string]$Path, [string]$Text) {
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path)) | Out-Null
  [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function New-SmokeTestState {
  return @{ Registrations = @(); Shortcuts = @(); Residual = @(); Tasks = @(); Autostart = @() }
}

function New-SmokeTestFixture {
  $root = Join-Path ([IO.Path]::GetTempPath()) ('arkism installed offline ' + [Guid]::NewGuid().ToString('N'))
  Write-Host "Only owned installed offline fixture root: $root"
  $stage = Join-Path $root 'stage'
  $files = @('uninstall.exe', 'runtime/arkism-installer.exe', 'Arkism.exe', 'integration/arkism.exe', 'runtime/memoryd.exe', 'runtime/memory-principal.exe', 'runtime/roles.sql',
    'runtime/pgsql/bin/postgres.exe', 'runtime/pgsql/bin/pg_ctl.exe', 'runtime/pgsql/bin/initdb.exe',
    'runtime/pgsql/bin/psql.exe', 'runtime/pgsql/bin/pg_dump.exe', 'runtime/pgsql/bin/pg_restore.exe',
    'runtime/pgsql/lib/vector.dll', 'runtime/pgsql/share/extension/vector.control', 'runtime/pgsql/share/extension/vector--0.8.1.sql')
  foreach ($name in $files) { Write-SmokeTestFile (Join-Path $stage $name) "inert fixture $name" }
  $context = @{ RunnerTemp = (Join-Path $root 'runner temp'); Programs = (Join-Path $root 'programs'); Desktop = (Join-Path $root 'desktop'); RunId = 'synthetic-1'; SourceSha = ('a' * 40) }
  foreach ($path in @($context.RunnerTemp, $context.Programs, $context.Desktop)) { [IO.Directory]::CreateDirectory($path) | Out-Null }
  $installer = Join-Path $root 'Arkism-1.2.3-setup.exe'
  Write-SmokeTestFile $installer 'inert setup bytes, never executable'
  Write-SmokeTestFile (Join-Path $root 'neighbor.txt') 'preserve neighbor'
  return @{
    Root = $root; Stage = $stage; Installer = $installer; Context = $context; Files = $files
    Evidence = (Join-Path $root 'evidence.json'); State = (New-SmokeTestState)
    Events = [Collections.Generic.List[string]]::new(); Links = [Collections.Generic.List[string]]::new(); Mode = ''; CleanupMode = ''
    Result = @{ ok = $true; phase = 'complete'; capabilities = 200; restored_capabilities = 200; reused = $true; backup_bytes = 128; expiry = 'ok'; safety_copy = $false; server = 'http://127.0.0.1:8091'
      token_checks = @{ expiry_rotation = $true; pending_before_commit = $true; pending_after_commit = $true; pending_backup_restore = $true; pending_backup_restore_content = $true; retired_token_http_403 = $true }
      fixture_source_rows = 1; fixture_memory_rows = 1; fixture_sha256 = ('b' * 64); backup_restore_content = $true }
  }
}

function New-SmokeTestLink($Fixture, [string]$Path, [string]$Target) {
  [IO.Directory]::CreateDirectory($Target) | Out-Null
  Write-SmokeTestFile (Join-Path $Target 'probe.txt') 'owned target probe'
  if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
    Push-Location -LiteralPath $Target
    try { New-Item -ItemType Junction -Path $Path -Target '.' -ErrorAction Stop | Out-Null }
    finally { Pop-Location }
  } else { New-Item -ItemType SymbolicLink -Path $Path -Target $Target -ErrorAction Stop | Out-Null }
  $Fixture.Links.Add($Path)
  Assert-SmokeTest ([IO.File]::ReadAllText((Join-Path $Path 'probe.txt')) -ceq 'owned target probe') 'Fixture link must point to its owned target'
}

function Set-SmokeTestRegistration($Fixture, [string]$Install) {
  $Fixture.Install = $Install
  $exe = Join-Path $Install 'Arkism.exe'
  $Fixture.State.Registrations = @([pscustomobject]@{ Hive = 'CurrentUser'; View = 'Registry32'; SubKeys = @(); Values = @{
    DisplayName = 'Arkism'; DisplayVersion = '1.2.3'; Publisher = 'Arkism contributors'; DisplayIcon = $exe
    InstallLocation = $Install; UninstallString = ('"' + (Join-Path $Install 'uninstall.exe') + '"'); NoModify = 1; NoRepair = 1
  }; Kinds = @{ DisplayName = 'String'; DisplayVersion = 'String'; Publisher = 'String'; DisplayIcon = 'String'
    InstallLocation = 'String'; UninstallString = 'String'; NoModify = 'DWord'; NoRepair = 'DWord' } })
  $Fixture.State.Shortcuts = @([pscustomobject]@{ Path = (Join-Path $Fixture.Context.Programs 'Arkism.lnk'); Role = 'Programs'; Scope = 'real'; Target = $exe; Arguments = '' },
    [pscustomobject]@{ Path = (Join-Path $Fixture.Context.Desktop 'Arkism.lnk'); Role = 'Desktop'; Scope = 'real'; Target = $exe; Arguments = '' })
}

function Copy-SmokeTestInstall($Fixture, [string]$Arguments) {
  Assert-SmokeTest ($Arguments.StartsWith('/S /NoAutoRun /D=')) 'Install arguments must preserve NoAutoRun and the raw final /D tail'
  $install = $Arguments.Substring('/S /NoAutoRun /D='.Length)
  Assert-SmokeTest ($install.Contains('installed Arkism') -and -not $install.Contains('"')) 'NSIS /D must allow spaces without quotes'
  foreach ($name in $Fixture.Files) {
    $path = Join-Path $install $name
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path)) | Out-Null
    [IO.File]::Copy((Join-Path $Fixture.Stage $name), $path)
  }
  # The exact emitted uninstaller is already copied as part of the signed payload.
  Set-SmokeTestRegistration $Fixture $install
  if ($Fixture.Mode -eq 'wrong_hash') { Write-SmokeTestFile (Join-Path $install 'Arkism.exe') 'different bytes' }
  if ($Fixture.Mode -eq 'changed_installer') { Write-SmokeTestFile $Fixture.Installer 'changed setup bytes' }
  if ($Fixture.Mode -eq 'wrong_owner') { $Fixture.State.Registrations[0].Values.InstallLocation = $Fixture.Root }
  if ($Fixture.Mode -eq 'wrong_shortcut') { $Fixture.State.Shortcuts[0].Target = $Fixture.Installer }
  if ($Fixture.Mode -eq 'wrong_kind') { $Fixture.State.Registrations[0].Kinds.DisplayName = 'ExpandString' }
  if ($Fixture.Mode -eq 'partial_install') { throw 'synthetic install failed after payload copy' }
}

function Write-SmokeTestResult($Fixture, [string]$FilePath, [string]$Arguments) {
  Assert-SmokeTest ($FilePath -ceq (Join-Path $Fixture.Install 'Arkism.exe')) 'Must run the installed binary'
  Assert-SmokeTest ($Arguments -match '^--runtime-check "([^"]+)"$') 'Data directory must be a quoted explicit argument'
  $data = $Matches[1]
  Assert-SmokeTest ([IO.Directory]::GetFileSystemEntries($data).Length -eq 0) 'Owned runtime check must receive a fresh directory'
  if ($Fixture.Mode -eq 'timeout') { throw 'synthetic runtime timeout' }
  if ($Fixture.Mode -eq 'runtime_failure') { throw 'synthetic runtime exit 1' }
  $text = $Fixture.Result | ConvertTo-Json
  if ($Fixture.ResultJson) { $text = $Fixture.ResultJson }
  if ($Fixture.Mode -eq 'bad_json') { $text = '{invalid' }
  Write-SmokeTestFile (Join-Path $data 'check.json') $text
  if ($Fixture.Mode -eq 'mutated_payload') { Write-SmokeTestFile (Join-Path $Fixture.Install 'runtime/roles.sql') 'changed after check' }
  if ($Fixture.Mode -eq 'cleanup_marker') { Write-SmokeTestFile (Join-Path ([IO.Path]::GetDirectoryName($Fixture.Install)) '.installed-smoke-owner') 'foreign marker' }
  if ($Fixture.Mode -eq 'cleanup_link') { New-SmokeTestLink $Fixture (Join-Path $Fixture.Install 'runtime/foreign') (Join-Path $Fixture.Root 'link target') }
  if ($Fixture.Mode -eq 'task_changed') { $Fixture.State.Tasks = @('synthetic newly appeared task') }
  if ($Fixture.Mode -eq 'extra_subkey') { $Fixture.State.Registrations[0].SubKeys = @('foreign-child') }
}

function Invoke-SmokeTestFixture($Fixture) {
  # These names are explicit wrapper boundaries, confined to this invocation.
  function Get-InstalledSmokeContext([string]$SourceSha) {
    if ($Fixture.Mode -eq 'host_denied') { throw 'synthetic hosted guard denied' }
    Assert-SmokeTest ($SourceSha -eq $Fixture.Context.SourceSha) 'Source identity must be forwarded'
    return $Fixture.Context
  }
  function Get-InstalledSmokeGlobalState($Context) {
    if ($Fixture.Mode -eq 'state_io') { throw 'synthetic state inspection denied' }
    return $Fixture.State
  }
  function Get-InstalledSmokeAttributes([string]$Path) {
    if ($Fixture.Mode -eq 'attributes_io' -and $Path -ceq $Fixture.Context.RunnerTemp) { throw 'synthetic attribute inspection denied' }
    & $originalSmokeAttributes $Path
  }
  function Get-InstalledSmokeProcesses([string]$InstallPath) {
    if ($Fixture.Mode -eq 'preexisting_process' -and -not $InstallPath) { return 'synthetic prior process' }
    if ($Fixture.Mode -eq 'autorun' -and $InstallPath) { return 'synthetic unexpected GUI' }
  }
  function Invoke-InstalledSmokeProcess([string]$FilePath, [string]$Arguments, $Environment, [int]$TimeoutSeconds, [string]$Phase) {
    $Fixture.Events.Add("process:$Phase")
    Assert-SmokeTest ($Phase -in @('profile_check', 'install', 'runtime_check') -and $FilePath -notlike '*uninstall.exe') 'Product uninstaller is forbidden'
    Assert-SmokeTest ($TimeoutSeconds -gt 0 -and $TimeoutSeconds -le 300) 'Every process needs a finite timeout'
    Assert-SmokeTest (-not $Environment.ContainsKey('GH_TOKEN') -and -not $Environment.ContainsKey('ARKISM_GATEWAY_TOKEN_FILE')) 'No inherited credential/config overrides'
    if ($Phase -eq 'profile_check') {
      Assert-SmokeTest ($FilePath -eq (Join-Path $Fixture.Stage 'runtime/arkism-installer.exe') -and $Arguments.StartsWith('check-folders ')) 'Preflight must use staged helper'
      if ($Fixture.Mode -eq 'profile_mismatch') { throw 'synthetic known folder mismatch' }
    }
    elseif ($Phase -eq 'install') { Copy-SmokeTestInstall $Fixture $Arguments }
    else { Write-SmokeTestResult $Fixture $FilePath $Arguments }
    return @{ Phase = $Phase; ExitCode = 0 }
  }
  function Remove-InstalledSmokeShortcut([string]$Path) {
    $Fixture.Events.Add('delete:shortcut')
    if ($Fixture.CleanupMode -eq 'shortcut_failure') { throw 'synthetic shortcut cleanup denied' }
    $Fixture.State.Shortcuts = @($Fixture.State.Shortcuts | Where-Object { $_.Path -cne $Path })
  }
  function Remove-InstalledSmokeRegistryKey($Record) {
    $Fixture.Events.Add('delete:registry')
    $Fixture.State.Registrations = @()
  }
  function Write-InstalledSmokeEvidence([string]$Path, $Report) {
    if ($Fixture.CleanupMode -eq 'evidence_failure') { throw 'synthetic evidence write denied' }
    & $originalSmokeEvidenceWriter $Path $Report
  }
  Invoke-InstalledSmoke -InstallerPath $Fixture.Installer -StagePath $Fixture.Stage -Version '1.2.3' -SourceSha $Fixture.Context.SourceSha -EvidencePath $Fixture.Evidence
}

function Invoke-SmokeTestCase([string]$Name, [scriptblock]$Body) {
  $fixture = New-SmokeTestFixture
  try {
    & $Body $fixture
    Assert-SmokeTest ([IO.File]::ReadAllText((Join-Path $fixture.Root 'neighbor.txt')) -ceq 'preserve neighbor') 'Neighbor must remain unchanged'
    $script:caseCount++
    Write-Host "PASS $Name"
  } finally {
    foreach ($link in $fixture.Links) {
      Assert-SmokeTest (Test-InstalledSmokeWithin $link $fixture.Root) 'Only fixture-owned links may be removed'
      Write-Host "Removing only owned installed offline fixture link: $link"
      [IO.Directory]::Delete($link)
    }
    Assert-InstalledSmokeTree $fixture.Root
    Write-Host "Removing only owned installed offline fixture root: $($fixture.Root)"
    [IO.Directory]::Delete($fixture.Root, $true)
  }
}

function Get-SmokeTestError($Fixture) {
  try { Invoke-SmokeTestFixture $Fixture | Out-Null; return '' }
  catch { return $_.Exception.Message }
}

Invoke-SmokeTestCase 'fresh installed binary and owned manual cleanup' {
  param($f)
  $result = Invoke-SmokeTestFixture $f
  Assert-SmokeTest ($result.ok -and $result.cleanup -eq 'owned_manual_cleanup') 'Fresh install must pass'
  Assert-SmokeTest (($f.Events -join ',') -eq 'process:profile_check,process:install,process:runtime_check,delete:shortcut,delete:shortcut,delete:registry') 'Only preflight and two allowed processes with owned registration cleanup'
  Assert-SmokeTest ([IO.Directory]::GetDirectories($f.Context.RunnerTemp).Length -eq 0) 'Owned root must be removed'
  $evidence = [IO.File]::ReadAllText($f.Evidence)
  Assert-SmokeTest ($evidence -notmatch 'owner.token|runtime.json|Bearer|password') 'Evidence must contain no runtime secrets'
}

foreach ($kind in @('Registrations', 'Shortcuts', 'Tasks', 'Autostart', 'Residual')) {
  Invoke-SmokeTestCase "preexisting $kind has zero effects" {
    param($f)
    $f.State[$kind] = @('synthetic existing state')
    Assert-SmokeTest ((Get-SmokeTestError $f).Contains('smoke_preexisting_state')) 'Existing state must fail closed'
    Assert-SmokeTest ($f.Events.Count -eq 0 -and -not [IO.File]::Exists($f.Evidence)) 'Preflight failure must not run cleanup or processes'
    Assert-SmokeTest ([IO.Directory]::GetDirectories($f.Context.RunnerTemp).Length -eq 0) 'No root may be created before state preflight'
  }
}

foreach ($mode in @('host_denied', 'state_io', 'attributes_io', 'preexisting_process')) {
  Invoke-SmokeTestCase "$mode has zero effects" {
    param($f)
    $f.Mode = $mode
    Assert-SmokeTest ((Get-SmokeTestError $f).Length -gt 0) 'Guard must fail'
    Assert-SmokeTest ($f.Events.Count -eq 0) 'Guard failure cannot mutate global state'
  }
}

foreach ($mode in @('wrong_hash', 'changed_installer', 'partial_install', 'timeout', 'runtime_failure', 'bad_json', 'mutated_payload')) {
  Invoke-SmokeTestCase "$mode reports failure and cleans only owned state" {
    param($f)
    $f.Mode = $mode
    Assert-SmokeTest ((Get-SmokeTestError $f).Length -gt 0) 'Injected failure must fail actual wrapper'
    Assert-SmokeTest (@($f.State.Registrations).Count -eq 0 -and @($f.State.Shortcuts).Count -eq 0) 'Owned registration should be cleaned'
    Assert-SmokeTest ([IO.Directory]::GetDirectories($f.Context.RunnerTemp).Length -eq 0) 'Failed check must clean its owned root'
  }
}

foreach ($mode in @('wrong_owner', 'wrong_shortcut', 'wrong_kind', 'autorun')) {
  Invoke-SmokeTestCase "$mode preserves unproven state" {
    param($f)
    $f.Mode = $mode
    $errorText = Get-SmokeTestError $f
    Assert-SmokeTest ($errorText.Length -gt 0 -and $errorText.Contains('cleanup:')) 'Primary and cleanup failure must both survive'
    Assert-SmokeTest (@($f.Events | Where-Object { $_ -like 'delete:*' }).Count -eq 0) 'Unsafe cleanup must delete nothing'
    Assert-SmokeTest ([IO.Directory]::GetDirectories($f.Context.RunnerTemp).Length -eq 1) 'Unproven root/state must be retained'
  }
}

foreach ($mode in @('cleanup_marker', 'cleanup_link', 'task_changed', 'extra_subkey')) {
  Invoke-SmokeTestCase "$mode prevents all destructive cleanup" {
    param($f)
    $f.Mode = $mode
    Assert-SmokeTest ((Get-SmokeTestError $f).Length -gt 0) 'Changed ownership/state must be reported'
    Assert-SmokeTest (@($f.Events | Where-Object { $_ -like 'delete:*' }).Count -eq 0) 'Ownership must be checked before registration cleanup'
    Assert-SmokeTest ([IO.Directory]::GetDirectories($f.Context.RunnerTemp).Length -eq 1) 'Unsafe tree must remain intact'
  }
}

Invoke-SmokeTestCase 'primary and cleanup errors remain visible' {
  param($f)
  $f.Mode = 'runtime_failure'; $f.CleanupMode = 'shortcut_failure'
  $errorText = Get-SmokeTestError $f
  Assert-SmokeTest ($errorText.Contains('synthetic runtime exit 1') -and $errorText.Contains('synthetic shortcut cleanup denied')) 'Both error causes must remain'
  Assert-SmokeTest ([IO.Directory]::GetDirectories($f.Context.RunnerTemp).Length -eq 1) 'Failed cleanup must retain its source'
}

Invoke-SmokeTestCase 'primary survives evidence publication failure' {
  param($f)
  $f.Mode = 'runtime_failure'; $f.CleanupMode = 'evidence_failure'
  $errorText = Get-SmokeTestError $f
  Assert-SmokeTest ($errorText.Contains('synthetic runtime exit 1') -and $errorText.Contains('synthetic evidence write denied')) 'Evidence failure must retain the primary cause'
}

foreach ($binding in @(@('phase', 'start'), @('expiry', 'expiry failed'), @('capabilities', 500), @('restored_capabilities', 500), @('reused', $false),
    @('backup_bytes', 0), @('safety_copy', $true), @('server', 'http://example.invalid:8091'), @('ok', 'true'), @('capabilities', 200.0), @('backup_bytes', $true))) {
  Invoke-SmokeTestCase "strict runtime result $($binding[0])=$($binding[1]) [$($binding[1].GetType().Name)]" {
    param($f)
    $f.Result[$binding[0]] = $binding[1]
    # Windows PowerShell serializes the Double 200.0 as the valid integer 200.
    # Preserve the literal JSON type that this rejection case intends to supply.
    if ($binding[0] -eq 'capabilities' -and $binding[1] -is [double]) {
      $f.ResultJson = ($f.Result | ConvertTo-Json -Depth 8 -Compress) -replace '"capabilities":200(?=[,}])', '"capabilities":200.0'
    }
    Assert-SmokeTest ((Get-SmokeTestError $f).Length -gt 0) 'False success runtime report must be rejected'
  }
}

foreach ($tokenCase in @('expiry_rotation', 'pending_before_commit', 'pending_after_commit', 'pending_backup_restore', 'pending_backup_restore_content', 'retired_token_http_403')) {
  foreach ($badTokenValue in @($false, 'true', $null)) {
    Invoke-SmokeTestCase "missing or false token evidence $tokenCase" {
      param($f)
      $f.Result.token_checks[$tokenCase] = $badTokenValue
      Assert-SmokeTest ((Get-SmokeTestError $f).Contains('runtime_token_evidence_failed')) 'Token lifecycle evidence cannot be missing, false or a string'
    }
  }
}

foreach ($fixtureField in @('fixture_source_rows', 'fixture_memory_rows', 'fixture_sha256', 'backup_restore_content')) {
  Invoke-SmokeTestCase "missing durable record evidence $fixtureField" {
    param($f)
    $f.Result.Remove($fixtureField)
    Assert-SmokeTest ((Get-SmokeTestError $f).Contains('runtime_fixture_evidence_failed')) 'Missing durable record evidence must fail closed'
  }
}
foreach ($binding in @(@('fixture_source_rows', 0), @('fixture_source_rows', 2), @('fixture_source_rows', $true), @('fixture_source_rows', '1'), @('fixture_source_rows', 1.0),
    @('fixture_memory_rows', 0), @('fixture_memory_rows', $true), @('fixture_memory_rows', 1.0),
    @('fixture_sha256', ''), @('fixture_sha256', ('a' * 63)), @('fixture_sha256', ('A' * 64)), @('fixture_sha256', 123),
    @('backup_restore_content', $false), @('backup_restore_content', 'true'), @('backup_restore_content', 1))) {
  Invoke-SmokeTestCase "invalid durable record evidence $($binding[0])" {
    param($f)
    $f.Result[$binding[0]] = $binding[1]
    if ($binding[1] -is [double]) {
      $f.ResultJson = ($f.Result | ConvertTo-Json -Depth 8 -Compress) -replace ('"' + $binding[0] + '":1(?=[,}])'), ('"' + $binding[0] + '":1.0')
    }
    Assert-SmokeTest ((Get-SmokeTestError $f).Contains('runtime_fixture_evidence_failed')) 'Counts must be exact integers, hashes lower-case SHA256 and content checks Boolean true'
  }
}

Invoke-SmokeTestCase 'old runtime without token checks cannot pass' {
  param($f)
  $f.Result.Remove('token_checks')
  Assert-SmokeTest ((Get-SmokeTestError $f).Contains('runtime_token_evidence_failed')) 'Old runtime report must fail the current gate'
}

Invoke-SmokeTestCase 'child environment does not change caller secrets or paths' {
  param($f)
  $before = [Environment]::GetEnvironmentVariables()
  $child = New-InstalledSmokeEnvironment $f.Root
  $child.HOME = 'synthetic child only'
  $after = [Environment]::GetEnvironmentVariables()
  Assert-SmokeTest ($before.Count -eq $after.Count) 'Caller environment count changed'
  foreach ($name in $before.Keys) { Assert-SmokeTest ($before[$name] -ceq $after[$name]) 'Caller environment value changed' }
  Assert-SmokeTest (@($child.Keys | Where-Object { $_ -match 'TOKEN|KEY|CERT|PASSWORD|ARKISM|MEMORY|GITHUB' }).Count -eq 0) 'Child must use an allowlist'
}

Invoke-SmokeTestCase 'dot-source never starts checks and ordinary local execution is refused' {
  param($f)
  . $sourcePath
  if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT -and $env:RUNNER_ENVIRONMENT -eq 'github-hosted') { return }
  try { Get-InstalledSmokeContext ('a' * 40) | Out-Null; throw 'Expected hosted guard rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_hosted_windows_required')) 'Local invocation must be denied' }
}

Invoke-SmokeTestCase 'root marker mismatch preserves all files' {
  param($f)
  $root = New-InstalledSmokeOwnedRoot $f.Context 'test'
  Write-SmokeTestFile (Join-Path $root '.installed-smoke-owner') 'foreign marker'
  try { Remove-InstalledSmokeOwnedRoot $f.Context $root; throw 'Expected marker rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_root_not_owned')) 'Wrong ownership must fail closed' }
  Assert-SmokeTest ([IO.Directory]::Exists($root)) 'Wrong marker root must be preserved'
}

Invoke-SmokeTestCase 'file ancestor fails before effects' {
  param($f)
  $file = Join-Path $f.Root 'not a directory'
  Write-SmokeTestFile $file 'keep file'
  $f.Context.RunnerTemp = Join-Path $file 'missing tail'
  Assert-SmokeTest ((Get-SmokeTestError $f).Contains('smoke_unsafe_directory')) 'File ancestor must be rejected'
  Assert-SmokeTest ($f.Events.Count -eq 0) 'Invalid root must precede effects'
}

Invoke-SmokeTestCase 'linked runner-temp ancestor fails before effects' {
  param($f)
  $link = Join-Path $f.Root 'runner alias'
  New-SmokeTestLink $f $link $f.Context.RunnerTemp
  $f.Context.RunnerTemp = Join-Path $link 'missing tail'
  Assert-SmokeTest ((Get-SmokeTestError $f).Contains('smoke_unsafe_directory')) 'Link before missing tail must be rejected'
  Assert-SmokeTest ($f.Events.Count -eq 0) 'Linked ancestor must precede all effects'
}

Invoke-SmokeTestCase 'existing evidence is preserved before effects' {
  param($f)
  Write-SmokeTestFile $f.Evidence 'preserve existing evidence'
  Assert-SmokeTest ((Get-SmokeTestError $f).Contains('smoke_evidence_exists')) 'Existing output must not be overwritten'
  Assert-SmokeTest ($f.Events.Count -eq 0 -and [IO.File]::ReadAllText($f.Evidence) -ceq 'preserve existing evidence') 'Existing output must stay unchanged'
}

Invoke-SmokeTestCase 'uninstaller and wrong-version input names have zero effects' {
  param($f)
  foreach ($name in @('uninstall.exe', 'other.exe', 'Arkism-9.9.9-setup.exe')) {
    $f.Installer = Join-Path $f.Root $name
    Write-SmokeTestFile $f.Installer 'inert forbidden input, never execute'
    Assert-SmokeTest ((Get-SmokeTestError $f).Contains('smoke_installer_name_mismatch')) 'Only the exact same-build versioned setup input is accepted'
    Assert-SmokeTest ($f.Events.Count -eq 0 -and -not [IO.File]::Exists($f.Evidence)) 'Input mismatch must not run processes, registration cleanup or evidence publication'
  }
}


function Get-SmokeTestActualState($Fixture) {
  function Get-InstalledSmokeResidualPaths($Context) {}
  function Get-InstalledSmokeRegistry { return $Fixture.State.Registrations }
  function Get-InstalledSmokeAutostart { return $Fixture.State.Autostart }
  function Invoke-SmokeTestScheduledTasks {}
  Set-Alias -Name Get-ScheduledTask -Value Invoke-SmokeTestScheduledTasks -Scope Local
  function Get-InstalledSmokeShortcut([string]$Path) {
    Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($Path)) -AllowMissing
    if (-not [IO.File]::Exists($Path)) { return }
    Assert-InstalledSmokeFile $Path
    return @($Fixture.State.Shortcuts | Where-Object { $_.Path -ieq $Path })
  }
  return & $originalSmokeGlobalState $Fixture.Context
}

function New-SmokeTestOwnedShortcutState($Fixture) {
  $owned = New-InstalledSmokeOwnedRoot $Fixture.Context 'folder-test'
  $child = New-InstalledSmokeEnvironment $owned
  Add-InstalledSmokeOwnedFolders $Fixture.Context $owned $child
  $install = Join-Path $owned 'installed'
  Set-SmokeTestRegistration $Fixture $install
  foreach ($record in $Fixture.State.Shortcuts) {
    $record.Path = Join-Path $Fixture.Context.OwnedShortcutFolders[$record.Role] 'Arkism.lnk'
    Write-SmokeTestFile $record.Path 'inert shortcut bytes'
  }
  return $owned
}

Invoke-SmokeTestCase 'actual state finds owned child shortcuts while retaining real folders' {
  param($f)
  $programs = $f.Context.Programs; $desktop = $f.Context.Desktop
  $owned = New-SmokeTestOwnedShortcutState $f
  $state = Get-SmokeTestActualState $f
  Assert-InstalledSmokeRegistration $state $f.Install '1.2.3' -RequireComplete
  $summary = Get-InstalledSmokeRegistrationSummary $state
  Assert-SmokeTest ($summary.owned_shortcuts -eq 2 -and $summary.real_shortcuts -eq 0 -and $summary.programs_shortcuts -eq 1 -and $summary.desktop_shortcuts -eq 1) 'Child folders must be observed with exact roles'
  Assert-SmokeTest ($f.Context.Programs -ceq $programs -and $f.Context.Desktop -ceq $desktop) 'Real shell folder monitoring must remain unchanged'
}

Invoke-SmokeTestCase 'actual state continues to observe preexisting real shortcuts' {
  param($f)
  $owned = New-SmokeTestOwnedShortcutState $f
  $f.State.Registrations = @(); $f.State.Shortcuts = @()
  $path = Join-Path $f.Context.Programs 'Arkism.lnk'
  Write-SmokeTestFile $path 'inert prior shortcut'
  $f.State.Shortcuts = @([pscustomobject]@{Path=$path;Target=$f.Installer;Arguments=''})
  $state = Get-SmokeTestActualState $f
  try { Assert-InstalledSmokeFreshState $state; throw 'Expected real state rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_preexisting_state')) 'Owned monitoring must not hide real prior state' }
  Assert-SmokeTest ([IO.File]::ReadAllText($path) -ceq 'inert prior shortcut' -and $f.Events.Count -eq 0) 'Real prior state must remain unchanged'
}

Invoke-SmokeTestCase 'two Programs shortcuts cannot substitute for a Desktop shortcut' {
  param($f)
  $owned = New-SmokeTestOwnedShortcutState $f
  $f.State.Shortcuts[1].Path = Join-Path $f.Context.Programs 'Arkism.lnk'
  Write-SmokeTestFile $f.State.Shortcuts[1].Path 'inert duplicate Programs shortcut'
  $state = Get-SmokeTestActualState $f
  try { Assert-InstalledSmokeRegistration $state $f.Install '1.2.3' -RequireComplete; throw 'Expected role rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_shortcut_roles_incomplete')) 'The two distinct roles are required' }
  Assert-SmokeTest ($f.Events.Count -eq 0) 'Incomplete role ownership cannot delete state'
}

Invoke-SmokeTestCase 'wrong owned shortcut targets cannot be removed' {
  param($f)
  $owned = New-SmokeTestOwnedShortcutState $f
  $f.State.Shortcuts[0].Target = $f.Installer
  $effects = @{Count=0}
  function Get-InstalledSmokeGlobalState($Context) { return Get-SmokeTestActualState $f }
  function Remove-InstalledSmokeShortcut([string]$Path) { $effects.Count++; throw 'Unexpected shortcut removal' }
  function Remove-InstalledSmokeRegistryKey($Record) { $effects.Count++; throw 'Unexpected registry removal' }
  try { Remove-InstalledSmokeRegistration $f.Context $f.Install '1.2.3'; throw 'Expected owned target rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_shortcut_not_owned')) 'Owned paths must still have the exact expected target' }
  Assert-SmokeTest ($effects.Count -eq 0 -and [IO.File]::Exists($f.State.Shortcuts[0].Path)) 'Foreign shortcut targets must be preserved'
}

Invoke-SmokeTestCase 'owned folders reject escape, ambiguous roles and links' {
  param($f)
  $owned = New-InstalledSmokeOwnedRoot $f.Context 'folder-test'
  $child = New-InstalledSmokeEnvironment $owned
  $child.APPDATA = $f.Root
  try { Add-InstalledSmokeOwnedFolders $f.Context $owned $child; throw 'Expected child escape rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_child_profile_not_owned')) 'Child profile must be owned' }
  $child = New-InstalledSmokeEnvironment $owned
  $target = Join-Path $f.Root 'owned link target'
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($child.APPDATA)) | Out-Null
  New-SmokeTestLink $f $child.APPDATA $target
  try { Add-InstalledSmokeOwnedFolders $f.Context $owned $child; throw 'Expected linked child rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_tree_changed')) 'Linked child profiles must fail closed' }
  $f.Context.Desktop = $f.Context.Programs
  try { Get-InstalledSmokeShortcutCandidates $f.Context | Out-Null; throw 'Expected ambiguous role rejection' }
  catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_shortcut_role_ambiguous')) 'Same path cannot represent two roles' }
  Assert-SmokeTest ($f.Events.Count -eq 0) 'Rejected child profiles have no process/registration effects'
}

Invoke-SmokeTestCase 'shortcut candidates deduplicate normalized same-role paths' {
  param($f)
  $f.Context.OwnedShortcutFolders = @{Programs=(Join-Path $f.Context.Programs '.');Desktop=(Join-Path $f.Context.Desktop '.')}
  $candidates = @(Get-InstalledSmokeShortcutCandidates $f.Context)
  Assert-SmokeTest ($candidates.Count -eq 2) 'Same normalized role path must be observed once'
}

Invoke-SmokeTestCase 'owned readonly cleanup clears only the observed bit' {
  param($f)
  $owned = New-InstalledSmokeOwnedRoot $f.Context 'readonly-test'
  $directory = Join-Path $owned 'readonly directory'
  $file = Join-Path $directory 'owned.txt'
  Write-SmokeTestFile $file 'owned readonly bytes'
  foreach ($path in @($file,$directory)) {
    [IO.File]::SetAttributes($path, ([IO.File]::GetAttributes($path) -bor [IO.FileAttributes]::ReadOnly))
  }
  $changes = [Collections.Generic.List[object]]::new()
  function Set-InstalledSmokeAttributes([string]$Path,[IO.FileAttributes]$Attributes) {
    $before = [IO.File]::GetAttributes($Path)
    Assert-SmokeTest ($Attributes -eq ($before -band (-bnot [IO.FileAttributes]::ReadOnly))) 'Only the ReadOnly bit may change'
    $changes.Add(@{Before=$before;After=$Attributes})
    & $originalSmokeSetAttributes $Path $Attributes
  }
  $summary = Get-InstalledSmokeOwnedAttributes $f.Context $owned
  Assert-SmokeTest ($summary.readonly_entries -eq 2) 'Safe attributes must count the observed readonly entries'
  Remove-InstalledSmokeOwnedRoot $f.Context $owned
  Assert-SmokeTest ($changes.Count -eq 2 -and -not [IO.Directory]::Exists($owned)) 'Only guarded readonly entries should be normalized and removed'
}

Invoke-SmokeTestCase 'marker failure precedes every readonly attribute mutation' {
  param($f)
  $owned = New-InstalledSmokeOwnedRoot $f.Context 'readonly-test'
  $file = Join-Path $owned 'owned.txt'; Write-SmokeTestFile $file 'preserve readonly bytes'
  $normal = [IO.File]::GetAttributes($file)
  [IO.File]::SetAttributes($file, ($normal -bor [IO.FileAttributes]::ReadOnly))
  Write-SmokeTestFile (Join-Path $owned '.installed-smoke-owner') 'wrong owner'
  $effects = @{Count=0}
  function Set-InstalledSmokeAttributes([string]$Path,[IO.FileAttributes]$Attributes) { $effects.Count++; throw 'Unexpected attribute mutation' }
  try {
    try { Remove-InstalledSmokeOwnedRoot $f.Context $owned; throw 'Expected owner rejection' }
    catch { Assert-SmokeTest ($_.Exception.Message.Contains('smoke_root_not_owned')) 'Ownership must be checked before normalization' }
    Assert-SmokeTest ($effects.Count -eq 0 -and ([IO.File]::GetAttributes($file) -band [IO.FileAttributes]::ReadOnly)) 'Readonly and owner bytes must survive rejection'
  } finally { [IO.File]::SetAttributes($file,$normal) }
}

Invoke-SmokeTestCase 'readonly attribute IO failures remain visible and preserve owned files' {
  param($f)
  $owned = New-InstalledSmokeOwnedRoot $f.Context 'readonly-test'
  $file = Join-Path $owned 'owned.txt'; Write-SmokeTestFile $file 'preserve readonly bytes'
  $normal = [IO.File]::GetAttributes($file)
  [IO.File]::SetAttributes($file,($normal -bor [IO.FileAttributes]::ReadOnly))
  function Set-InstalledSmokeAttributes([string]$Path,[IO.FileAttributes]$Attributes) { throw 'synthetic attribute clear denied' }
  try {
    try { Remove-InstalledSmokeOwnedRoot $f.Context $owned; throw 'Expected attribute IO failure' }
    catch { Assert-SmokeTest ($_.Exception.Message.Contains('synthetic attribute clear denied')) 'Attribute IO failure must propagate' }
    Assert-SmokeTest ([IO.File]::ReadAllText($file) -ceq 'preserve readonly bytes') 'IO failure must preserve source bytes'
  } finally { [IO.File]::SetAttributes($file,$normal) }
}

Invoke-SmokeTestCase 'tree inspection failure precedes every readonly attribute mutation' {
  param($f)
  $owned = New-InstalledSmokeOwnedRoot $f.Context 'readonly-test'
  $file = Join-Path $owned 'owned.txt'; Write-SmokeTestFile $file 'preserve unreadable-tree bytes'
  $normal = [IO.File]::GetAttributes($file)
  [IO.File]::SetAttributes($file,($normal -bor [IO.FileAttributes]::ReadOnly))
  $effects=@{Count=0}
  function Get-InstalledSmokeAttributes([string]$Path) {
    if ($Path -ceq $file) { throw 'synthetic tree inspection denied' }
    & $originalSmokeAttributes $Path
  }
  function Set-InstalledSmokeAttributes([string]$Path,[IO.FileAttributes]$Attributes) { $effects.Count++; throw 'Unexpected attribute mutation' }
  try {
    try { Remove-InstalledSmokeOwnedRoot $f.Context $owned; throw 'Expected inspection failure' }
    catch { Assert-SmokeTest ($_.Exception.Message.Contains('synthetic tree inspection denied')) 'Inspection IO must propagate before normalization' }
    Assert-SmokeTest ($effects.Count -eq 0 -and [IO.File]::ReadAllText($file) -ceq 'preserve unreadable-tree bytes') 'Uninspected trees cannot be modified'
  } finally { [IO.File]::SetAttributes($file,$normal) }
}

# Load definitions only; the native control's hosted preflight and main body never execute here.
$nsisSourcePath = Join-Path $PSScriptRoot 'test-nsis-autorun.ps1'
$nsisSourceHash = (Get-FileHash -LiteralPath $nsisSourcePath -Algorithm SHA256).Hash
$nsisTokens=$null; $nsisErrors=$null
$nsisAst = [Management.Automation.Language.Parser]::ParseFile($nsisSourcePath,[ref]$nsisTokens,[ref]$nsisErrors)
Assert-SmokeTest ($nsisErrors.Count -eq 0) 'Native control source must parse'
foreach ($definition in $nsisAst.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst]},$true)) {
  . ([scriptblock]::Create($definition.Extent.Text))
}

function Invoke-SmokeTestAutorunWait($Fixture,[string]$Mode) {
  $install = Join-Path $Fixture.Root 'inert writer'
  $marker = Join-Path $Fixture.Root 'writer.marker'
  $clock = @{Tick=0;Queries=0}
  if ($Mode -eq 'partial_writer') { Write-SmokeTestFile $marker '' }
  function Get-NsisAutorunNow { return [DateTime]::MinValue.AddDays(1).AddMilliseconds($clock.Tick*100) }
  function Wait-NsisAutorunPoll {
    $clock.Tick++
    if ($Mode -eq 'partial_writer' -and $clock.Tick -eq 2) { Write-SmokeTestFile $marker (Join-Path $install 'Arkism.exe') }
  }
  function Get-InstalledSmokeProcesses([string]$InstallPath) {
    $clock.Queries++
    if ($Mode -eq 'partial_writer' -and $clock.Tick -lt 2) { return 'synthetic writer still active' }
    if (($Mode -eq 'late_marker' -and $clock.Queries -eq 1) -or ($Mode -eq 'final_probe_marker' -and $clock.Tick -eq 20)) { Write-SmokeTestFile $marker (Join-Path $install 'Arkism.exe') }
    if ($Mode -eq 'late_process' -and $clock.Tick -ge 1) { return 'synthetic late autorun' }
  }
  $errorText=''
  try { Wait-NsisAutorunMarker $marker $install ($Mode -eq 'partial_writer') }
  catch { $errorText=$_.Exception.Message }
  return @{Error=$errorText;Tick=$clock.Tick}
}

Invoke-SmokeTestCase 'autorun positive waits for complete marker after child exit' {
  param($f)
  $result=Invoke-SmokeTestAutorunWait $f 'partial_writer'
  Assert-SmokeTest (-not $result.Error -and $result.Tick -eq 2) 'An existing partial marker must not be read before writer exit'
}

foreach ($mode in @('late_marker','late_process','final_probe_marker')) {
  Invoke-SmokeTestCase "noautorun rejects $mode during its observation" {
    param($f)
    $result=Invoke-SmokeTestAutorunWait $f $mode
    Assert-SmokeTest ($result.Error.Contains('nsis_control_unexpected_autorun')) 'Late autorun evidence must be rejected'
  }
}

Invoke-SmokeTestCase 'noautorun observes its complete bounded interval without real sleeps' {
  param($f)
  $result=Invoke-SmokeTestAutorunWait $f 'clean'
  Assert-SmokeTest (-not $result.Error -and $result.Tick -eq 20) 'The full bounded observation and final check must run'
}

Assert-SmokeTest ((Get-FileHash -LiteralPath $nsisSourcePath -Algorithm SHA256).Hash -ceq $nsisSourceHash) 'Native control source changed during offline tests'

Assert-SmokeTest ((Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash -ceq $sourceHash) 'Wrapper source changed during offline tests'

Invoke-SmokeTestCase 'unsafe known folders stop before installer' {
  param($f)
  $f.Mode = 'profile_mismatch'
  Assert-SmokeTest ((Get-SmokeTestError $f) -match 'known folder mismatch') 'Folder mismatch must fail'
  Assert-SmokeTest (($f.Events -join ',') -eq 'process:profile_check') 'Unsafe folder must prevent installation and runtime effects'
}

Write-Host "All $caseCount offline installed-runtime smoke cases passed. No native product effects executed."
