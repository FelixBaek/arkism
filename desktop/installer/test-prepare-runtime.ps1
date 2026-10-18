# Offline control-flow tests for the actual preparation script. Compatible with
# Windows PowerShell 5.1; no downloads, extraction, Visual Studio or PE execution.
$ErrorActionPreference = 'Stop'
# .NET Framework keeps ZipArchiveMode in the separate compression assembly.
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$sourcePath = Join-Path $PSScriptRoot 'prepare-runtime.ps1'
$source = [IO.File]::ReadAllText($sourcePath)
$sourceHash = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash
$testCount = 0
$programs = 'postgres.exe', 'pg_ctl.exe', 'initdb.exe', 'psql.exe', 'pg_dump.exe', 'pg_restore.exe'

function Assert-RuntimeTest([bool]$Condition, [string]$Message) {
  if (-not $Condition) { throw $Message }
}

function Get-RuntimeCommandSnapshot {
  $rows = @()
  foreach ($name in @('Invoke-WebRequest', 'Expand-Archive', 'Get-Command', 'Copy-Item', 'Move-Item', 'Remove-Item', 'cmd')) {
    $commands = @(Microsoft.PowerShell.Core\Get-Command -Name $name -All -ErrorAction SilentlyContinue)
    if ($commands.Count -eq 0) { $rows += "$name=<missing>" }
    foreach ($command in $commands) {
      $rows += "$name=$($command.CommandType)|$($command.ModuleName)|$($command.Source)|$($command.Definition)"
    }
  }
  return $rows -join "`n"
}

# Capture real command resolution before any fixture boundary can be installed.
$originalRuntimeCommands = Get-RuntimeCommandSnapshot

function Get-RuntimeDeclaration([string]$Text, [string]$Name) {
  $pattern = '(?m)^\$' + [regex]::Escape($Name) + " = '([^']+)'"
  $found = [regex]::Matches($Text, $pattern)
  Assert-RuntimeTest ($found.Count -eq 1) "Expected one declaration of $Name"
  return $found[0].Groups[1].Value
}

function Write-RuntimeFixtureFile([string]$Path, [string]$Text) {
  $Path = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path)
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path)) | Out-Null
  [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function New-InertRuntimeZip([string]$Path, [string]$Text) {
  if (Test-Path -LiteralPath $Path) { Remove-Item -LiteralPath $Path -Force }
  $archive = [IO.Compression.ZipFile]::Open($Path, [IO.Compression.ZipArchiveMode]::Create)
  try {
    $entry = $archive.CreateEntry('fixture.txt')
    $writer = [IO.StreamWriter]::new($entry.Open(), [Text.UTF8Encoding]::new($false))
    try { $writer.Write($Text) } finally { $writer.Dispose() }
  } finally { $archive.Dispose() }
}

function New-RuntimeFixture {
  $root = Join-Path ([IO.Path]::GetTempPath()) ('arkism runtime test [fixture] ' + [Guid]::NewGuid().ToString('N'))
  [IO.Directory]::CreateDirectory($root) | Out-Null
  $pgVersion = Get-RuntimeDeclaration $source 'pgVersion'
  $vectorVersion = Get-RuntimeDeclaration $source 'vectorVersion'
  $goodPg = Join-Path $root 'expected-pg.zip'
  $goodVector = Join-Path $root 'expected-vector.zip'
  $wrong = Join-Path $root 'different.zip'
  New-InertRuntimeZip $goodPg 'synthetic PostgreSQL input'
  New-InertRuntimeZip $goodVector 'synthetic pgvector input'
  New-InertRuntimeZip $wrong 'synthetic different input'
  $copy = $source
  foreach ($binding in @(@('pgSha256', $goodPg), @('vectorSha256', $goodVector))) {
    $original = Get-RuntimeDeclaration $source $binding[0]
    Assert-RuntimeTest ([regex]::Matches($copy, [regex]::Escape($original)).Count -eq 1) 'Pin replacement must be unique'
    $fixtureHash = (Get-FileHash -LiteralPath $binding[1] -Algorithm SHA256).Hash.ToLowerInvariant()
    $copy = $copy.Replace($original, $fixtureHash)
  }
  $cache = Join-Path $root 'cache'
  $stage = Join-Path $root 'stage'
  $dir = Join-Path $cache "postgres-$pgVersion-pgvector-$vectorVersion"
  $scriptCopy = Join-Path $root 'prepare-runtime.ps1'
  # Rebind only the two pins. The original script's entire control flow runs.
  [IO.File]::WriteAllText($scriptCopy, $copy, [Text.UTF8Encoding]::new($false))
  Write-RuntimeFixtureFile (Join-Path $stage 'keep.txt') 'synthetic unrelated stage file'
  Write-RuntimeFixtureFile (Join-Path $stage 'runtime/pgsql/previous.txt') 'synthetic previous runtime'
  $visualStudio = Join-Path $root 'Visual Studio'
  Write-RuntimeFixtureFile (Join-Path $visualStudio 'VC/Auxiliary/Build/vcvars64.bat') 'synthetic toolchain placeholder'
  return @{
    Root = $root; Cache = $cache; Stage = $stage; Dir = $dir; Script = $scriptCopy
    GoodPg = $goodPg; GoodVector = $goodVector; Wrong = $wrong
    PgVersion = $pgVersion; VectorVersion = $vectorVersion; VisualStudio = $visualStudio
    PgUrl = "https://get.enterprisedb.com/postgresql/postgresql-$pgVersion-windows-x64-binaries.zip"
    VectorUrl = "https://github.com/pgvector/pgvector/archive/refs/tags/v$vectorVersion.zip"
    Events = [System.Collections.Generic.List[string]]::new(); Mode = 'ok'; StageCopies = 0
    Links = [System.Collections.Generic.List[string]]::new()
  }
}

function Assert-OwnedRuntimePath([string]$Path) {
  $root = [IO.Path]::GetFullPath($global:arkismRuntimeTest.Root) + [IO.Path]::DirectorySeparatorChar
  Assert-RuntimeTest ([IO.Path]::GetFullPath($Path).StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) 'Fixture path escaped its owned root'
}

function Invoke-RuntimeDownload {
  param([Parameter(Position=0)][string]$Uri, [switch]$UseBasicParsing, [string]$OutFile, [int]$TimeoutSec)
  $state = $global:arkismRuntimeTest
  Assert-OwnedRuntimePath $OutFile
  if ($Uri -eq $state.PgUrl) { $role = 'pg'; $zip = $state.GoodPg }
  elseif ($Uri -eq $state.VectorUrl) { $role = 'vector'; $zip = $state.GoodVector }
  else { throw 'Unexpected URL: no network request was attempted' }
  Assert-RuntimeTest ($UseBasicParsing -and $TimeoutSec -eq 300) 'Download options changed'
  $state.Events.Add("download:$role")
  if ($state.Mode -eq "wrong_$role") { $zip = $state.Wrong }
  if ($state.Mode -eq "partial_$role" -or $state.Mode -eq "download_failure_$role") {
    $bytes = [IO.File]::ReadAllBytes($zip)
    [IO.File]::WriteAllBytes($OutFile, $bytes[0..([int]($bytes.Length / 2))])
    if ($state.Mode -eq "download_failure_$role") { throw 'synthetic download failed' }
  } else { Copy-Item -LiteralPath $zip -Destination $OutFile }
}

function Invoke-RuntimeExpand {
  param([Alias('LiteralPath')][Parameter(Position=0)][string]$Path, [Parameter(Position=1)][string]$DestinationPath, [switch]$Force)
  $state = $global:arkismRuntimeTest
  Assert-OwnedRuntimePath $Path
  Assert-OwnedRuntimePath $DestinationPath
  if ((Split-Path -Leaf $Path) -eq 'pg.zip') { $role = 'pg'; $zip = $state.GoodPg }
  elseif ((Split-Path -Leaf $Path) -eq 'pgvector.zip') { $role = 'vector'; $zip = $state.GoodVector }
  else { throw 'Unexpected archive path' }
  Assert-RuntimeTest ((Get-FileHash -LiteralPath $Path).Hash -eq (Get-FileHash -LiteralPath $zip).Hash) 'Unverified bytes reached the extractor'
  $state.Events.Add("extract:$role")
  if ($state.Mode -eq 'extract_failure') { throw 'synthetic extraction failed' }
  if ($role -eq 'pg') {
    foreach ($name in $programs) { Write-RuntimeFixtureFile (Join-Path $DestinationPath "pgsql/bin/$name") "synthetic $name" }
    foreach ($relative in @('bin/libpq.dll', 'lib/fixture.dll', 'share/postgres.bki', 'share/postgresql.conf.sample', 'share/pg_hba.conf.sample')) {
      Write-RuntimeFixtureFile (Join-Path $DestinationPath "pgsql/$relative") "synthetic $relative"
    }
  } else {
    Write-RuntimeFixtureFile (Join-Path $DestinationPath "pgvector-$($state.VectorVersion)/Makefile.win") 'synthetic inert makefile'
  }
}

# Resolve the production native-command boundary to a script block. The actual
# script still checks its output, exit status and toolchain path.
function Get-RuntimeNativeCommand {
  [CmdletBinding()]
  param([string]$Name, [object]$CommandType)
  Assert-RuntimeTest ($Name.EndsWith('\vswhere.exe') -and $CommandType -eq 'Application') 'Unexpected native command lookup'
  if ($global:arkismRuntimeTest.Mode -eq 'vswhere_missing') { throw 'synthetic discovery command missing' }
  return {
    $state = $global:arkismRuntimeTest
    $state.Events.Add('vswhere')
    $global:LASTEXITCODE = 0
    if ($state.Mode -eq 'vswhere_failure') { $global:LASTEXITCODE = 31 }
    if ($state.Mode -eq 'vswhere_empty') { return '' }
    return $state.VisualStudio
  }
}

function Invoke-RuntimeBuild {
  param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Arguments)
  $state = $global:arkismRuntimeTest
  Assert-RuntimeTest ($Arguments.Count -eq 2 -and $Arguments[0] -eq '/c' -and $Arguments[1].Contains('nmake /nologo /F Makefile.win')) 'Build invocation changed'
  $state.Events.Add('build')
  $full = Join-Path $state.Dir 'full/pgsql'
  Write-RuntimeFixtureFile (Join-Path $full 'lib/vector.dll') 'synthetic built extension'
  Write-RuntimeFixtureFile (Join-Path $full 'share/extension/vector.control') 'synthetic extension control'
  if ($state.Mode -ne 'missing_runtime') {
    Write-RuntimeFixtureFile (Join-Path $full "share/extension/vector--$($state.VectorVersion).sql") 'synthetic extension SQL'
  }
  $global:LASTEXITCODE = 0
  if ($state.Mode -eq 'build_failure') {
    # An interrupted cache with vector.dll must not become a completed warm cache.
    Write-RuntimeFixtureFile (Join-Path $state.Dir 'runtime/pgsql/lib/vector.dll') 'synthetic incomplete cache'
    $global:LASTEXITCODE = 17
  }
  if ($state.Mode -eq 'stage_link_during_build') {
    $state.LinkedStageTarget = Join-Path $state.Root 'runtime target after build'
    $runtime = Join-Path $state.Stage 'runtime'
    [IO.Directory]::Move($runtime, $state.LinkedStageTarget)
    New-RuntimeFixtureLink $state $runtime $state.LinkedStageTarget
  }
}

function Copy-RuntimeFixtureItem {
  [CmdletBinding()]
  param([string]$LiteralPath, [string]$Destination, [switch]$Recurse, [switch]$Force)
  $state = $global:arkismRuntimeTest
  if ($null -ne $state -and
      [IO.Path]::GetFullPath($LiteralPath) -eq [IO.Path]::GetFullPath((Join-Path $state.Dir 'runtime/pgsql'))) {
    $state.StageCopies++
    Assert-OwnedRuntimePath $Destination
    if ($state.Mode -eq 'stage_copy_failure') {
      Write-RuntimeFixtureFile (Join-Path $Destination 'partial.txt') 'synthetic partial copy'
      throw 'synthetic staging copy failed'
    }
    if ($state.Mode -eq 'stage_missing_file') {
      Microsoft.PowerShell.Management\Copy-Item @PSBoundParameters
      Remove-Item -LiteralPath (Join-Path $Destination 'bin/psql.exe')
      return
    }
  }
  Microsoft.PowerShell.Management\Copy-Item @PSBoundParameters
}

function Move-RuntimeFixtureItem {
  [CmdletBinding()]
  param([string]$LiteralPath, [string]$Destination, [switch]$Force)
  $state = $global:arkismRuntimeTest
  if ($null -eq $state) {
    Microsoft.PowerShell.Management\Move-Item @PSBoundParameters
    return
  }
  $from = Split-Path -Leaf $LiteralPath
  $to = Split-Path -Leaf $Destination
  $publishing = $from -cmatch '^pgsql-incoming-[0-9a-f]{32}$'
  $backingUp = $to -cmatch '^pgsql-backup-[0-9a-f]{32}$'
  $restoring = $from -cmatch '^pgsql-backup-[0-9a-f]{32}$'
  Assert-OwnedRuntimePath $LiteralPath
  Assert-OwnedRuntimePath $Destination
  if ($state.Mode -eq 'stage_backup_failure' -and $backingUp) { throw 'synthetic backup rename failed' }
  if ($state.Mode -eq 'stage_backup_partial_failure' -and $backingUp) {
    Microsoft.PowerShell.Management\Move-Item @PSBoundParameters
    throw 'synthetic backup rename failed after move'
  }
  if ($publishing -and $state.Mode -in @('stage_publish_failure', 'stage_rollback_failure', 'stage_publish_cleanup_failure', 'stage_rollback_cleanup_failure')) { throw 'synthetic staging publication failed' }
  if ($state.Mode -eq 'stage_partial_publish_failure' -and $publishing) {
    Microsoft.PowerShell.Management\Move-Item @PSBoundParameters
    throw 'synthetic staging publication failed after move'
  }
  if ($state.Mode -in @('stage_rollback_failure', 'stage_rollback_cleanup_failure') -and $restoring) { throw 'synthetic rollback failed' }
  Microsoft.PowerShell.Management\Move-Item @PSBoundParameters
  if ($publishing) { $state.FailCleanup = $state.Mode -eq 'stage_cleanup_guard_failure' }
}

function Remove-RuntimeFixtureItem {
  [CmdletBinding()]
  param([string[]]$LiteralPath, [switch]$Recurse, [switch]$Force)
  $state = $global:arkismRuntimeTest
  if ($null -ne $state -and $state.Mode -in @('stage_publish_cleanup_failure', 'stage_rollback_cleanup_failure') -and
      $LiteralPath.Count -eq 1 -and (Split-Path -Leaf $LiteralPath[0]) -cmatch '^pgsql-incoming-[0-9a-f]{32}$') {
    throw [IO.IOException]::new('synthetic incoming cleanup failed')
  }
  Microsoft.PowerShell.Management\Remove-Item @PSBoundParameters
}

function Get-RuntimeStageSnapshot([string]$Stage) {
  $rows = @()
  foreach ($entry in Get-ChildItem -LiteralPath $Stage -Recurse -Force | Sort-Object FullName) {
    $relative = $entry.FullName.Substring($Stage.Length + 1).Replace('\', '/')
    $bytes = if ($entry.PSIsContainer) { '<directory>' } else { [Convert]::ToBase64String([IO.File]::ReadAllBytes($entry.FullName)) }
    $rows += "$relative=$bytes"
  }
  return $rows -join "`n"
}

function New-RuntimeFixtureLink([hashtable]$Fixture, [string]$Path, [string]$Target) {
  $owned = $Fixture.Root + [IO.Path]::DirectorySeparatorChar
  foreach ($candidate in @($Path, $Target)) {
    Assert-RuntimeTest ([IO.Path]::GetFullPath($candidate).StartsWith($owned, [StringComparison]::OrdinalIgnoreCase)) 'Link fixture escaped its owned root'
  }
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path)) | Out-Null
  $callerLocation = (Get-Location).Path
  if ([IO.Path]::DirectorySeparatorChar -eq '\') {
    # Windows PowerShell resolves -Target as a wildcard. Keep the real [] path,
    # but resolve the junction's '.' target from its literal filesystem location.
    Push-Location -LiteralPath $Target
    try { New-Item -ItemType Junction -Path $Path -Target '.' | Out-Null }
    finally { Pop-Location }
  } else {
    New-Item -ItemType SymbolicLink -Path $Path -Target $Target | Out-Null
  }
  # Register before assertions so a broken link can always be unlinked safely.
  $Fixture.Links.Add($Path)
  Assert-RuntimeTest ((Get-Location).Path -ceq $callerLocation) 'Link creation changed the caller location'
  Assert-RuntimeTest (([IO.File]::GetAttributes($Path) -band [IO.FileAttributes]::ReparsePoint) -ne 0) 'Fixture did not create a real reparse point'
  $probeName = 'link-probe-' + [Guid]::NewGuid().ToString('N') + '.tmp'
  $probe = Join-Path $Target $probeName
  $probeText = 'synthetic owned link target ' + [Guid]::NewGuid().ToString('N')
  try {
    # Write only to the owned real target; read through the link to verify its binding.
    [IO.File]::WriteAllText($probe, $probeText, [Text.UTF8Encoding]::new($false))
    Assert-RuntimeTest ([IO.File]::ReadAllText((Join-Path $Path $probeName)) -ceq $probeText) 'Fixture link points to a different target'
  } finally { [IO.File]::Delete($probe) }
}

function Get-RuntimeFixtureInspectionFailure([string]$Path, [bool]$AllowMissing) {
  throw [IO.IOException]::new('synthetic runtime path inspection failed')
}

function Get-RuntimeFixtureCleanupAttributes([string]$Path, [bool]$AllowMissing) {
  if ($global:arkismRuntimeTest.FailCleanup -and (Split-Path -Leaf $Path) -cmatch '^pgsql-incoming-[0-9a-f]{32}$') {
    throw [IO.IOException]::new('synthetic cleanup inspection failed')
  }
  # Invoke the production implementation, bypassing only this local test alias.
  $original = (Get-Item -LiteralPath Function:\Get-RuntimePathAttributes).ScriptBlock
  & $original $Path $AllowMissing
}

function Invoke-RuntimeFixture([hashtable]$Fixture, [string]$Mode = 'ok',
  [string]$CachePath = $Fixture.Cache, [string]$StagePath = $Fixture.Stage) {
  # Only the called production script and its children see these test boundaries.
  Set-Alias -Name Invoke-WebRequest -Value Invoke-RuntimeDownload -Scope Local
  Set-Alias -Name Expand-Archive -Value Invoke-RuntimeExpand -Scope Local
  Set-Alias -Name Get-Command -Value Get-RuntimeNativeCommand -Scope Local
  Set-Alias -Name cmd -Value Invoke-RuntimeBuild -Scope Local
  Set-Alias -Name Copy-Item -Value Copy-RuntimeFixtureItem -Scope Local
  Set-Alias -Name Move-Item -Value Move-RuntimeFixtureItem -Scope Local
  Set-Alias -Name Remove-Item -Value Remove-RuntimeFixtureItem -Scope Local
  if ($Mode -eq 'path_inspection_failure') {
    Set-Alias -Name Get-RuntimePathAttributes -Value Get-RuntimeFixtureInspectionFailure -Scope Local
  }
  if ($Mode -eq 'stage_cleanup_guard_failure') {
    Set-Alias -Name Get-RuntimePathAttributes -Value Get-RuntimeFixtureCleanupAttributes -Scope Local
  }
  $Fixture.Mode = $Mode
  $Fixture.FailCleanup = $false
  $Fixture.Events.Clear()
  $global:arkismRuntimeTest = $Fixture
  $global:LASTEXITCODE = 0
  try {
    & $Fixture.Script -Cache $CachePath -Stage $StagePath 6>$null
    return $null
  } catch { return $_.Exception.Message }
}

function Assert-RuntimeOutput([hashtable]$Fixture) {
  foreach ($relative in @($programs | ForEach-Object { "bin/$_" }) + @(
    'lib/vector.dll', 'share/postgres.bki', 'share/postgresql.conf.sample', 'share/pg_hba.conf.sample',
    'share/extension/vector.control', "share/extension/vector--$($Fixture.VectorVersion).sql"
  )) {
    Assert-RuntimeTest (Test-Path -LiteralPath (Join-Path $Fixture.Stage "runtime/pgsql/$relative") -PathType Leaf) "Runtime output missing $relative"
  }
  Assert-RuntimeTest ([IO.File]::ReadAllText((Join-Path $Fixture.Stage 'keep.txt')) -eq 'synthetic unrelated stage file') 'Unrelated staging file changed'
  $marker = Join-Path $Fixture.Dir 'complete.json'
  Assert-RuntimeTest (Test-Path -LiteralPath $marker -PathType Leaf) 'Cache did not complete'
  $record = ConvertFrom-Json -InputObject ([IO.File]::ReadAllText($marker))
  Assert-RuntimeTest ($record.schema -eq 1 -and $record.input_fingerprint -cmatch '^[0-9a-f]{64}$') 'Completion metadata invalid'
}

function Test-RuntimeCase([string]$Name, [scriptblock]$Body) {
  $fixture = New-RuntimeFixture
  try {
    & $Body $fixture
    $script:testCount++
    Write-Host "PASS: $Name"
  } finally {
    Remove-Variable -Name arkismRuntimeTest -Scope Global -ErrorAction SilentlyContinue
    foreach ($link in $fixture.Links) {
      try { [IO.Directory]::Delete($link) }
      catch {
        $cause = $_.Exception.GetBaseException()
        if ($cause -isnot [IO.DirectoryNotFoundException] -and $cause -isnot [IO.FileNotFoundException]) { throw }
      }
    }
    Remove-Item -LiteralPath $fixture.Root -Recurse -Force
  }
}

Test-RuntimeCase 'matching inputs prepare once and reuse completed cache' {
  param($fixture)
  $failure = Invoke-RuntimeFixture $fixture
  Assert-RuntimeTest ($null -eq $failure) "Cold preparation failed: $failure"
  Assert-RuntimeTest (($fixture.Events -join ',') -eq 'download:pg,download:vector,extract:pg,extract:vector,vswhere,build') 'Inputs were not both verified before extraction/build'
  Assert-RuntimeOutput $fixture
  $before = Get-RuntimeStageSnapshot $fixture.Stage
  $failure = Invoke-RuntimeFixture $fixture
  Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Count -eq 0) "Completed cache did not skip downloads/build: $failure"
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Stage) -eq $before) 'Warm staging changed output or nested the runtime directory'
}

foreach ($mode in @('wrong_pg', 'wrong_vector', 'partial_pg', 'partial_vector', 'download_failure_pg', 'download_failure_vector')) {
  Test-RuntimeCase "$mode stops before any extraction/build/staging" {
    param($fixture)
    $before = Get-RuntimeStageSnapshot $fixture.Stage
    $failure = Invoke-RuntimeFixture $fixture $mode
    $expected = if ($mode.StartsWith('download_failure_')) { 'synthetic download failed' } else { 'checksum mismatch for ' }
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains($expected)) "Expected input validation failure: $failure"
    Assert-RuntimeTest (@($fixture.Events | Where-Object { $_ -notlike 'download:*' }).Count -eq 0) 'Invalid input reached extraction/build'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Stage) -eq $before) 'Failed download changed staging'
    Assert-RuntimeTest (-not (Test-Path -LiteralPath (Join-Path $fixture.Dir 'complete.json'))) 'Failed input completed its cache'
  }
}

foreach ($mode in @('vswhere_missing', 'vswhere_failure', 'vswhere_empty', 'build_failure', 'missing_runtime', 'extract_failure')) {
  Test-RuntimeCase "$mode preserves staging and leaves cache incomplete" {
    param($fixture)
    $before = Get-RuntimeStageSnapshot $fixture.Stage
    $failure = Invoke-RuntimeFixture $fixture $mode
    $expected = switch ($mode) {
      'vswhere_missing' { 'synthetic discovery command missing' }
      'vswhere_failure' { 'Visual Studio discovery failed' }
      'vswhere_empty' { 'Visual Studio discovery failed' }
      'build_failure' { 'pgvector build failed' }
      'missing_runtime' { 'runtime preparation incomplete' }
      'extract_failure' { 'synthetic extraction failed' }
    }
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains($expected)) "Expected preparation failure: $failure"
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Stage) -eq $before) 'Preparation failure changed staging'
    Assert-RuntimeTest (-not (Test-Path -LiteralPath (Join-Path $fixture.Dir 'complete.json'))) 'Preparation failure completed its cache'
    if ($mode -eq 'build_failure') {
      Assert-RuntimeTest (Test-Path -LiteralPath (Join-Path $fixture.Dir 'runtime/pgsql/lib/vector.dll')) 'Failure fixture lost its incomplete vector.dll'
      $failure = Invoke-RuntimeFixture $fixture
      Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Contains('download:pg') -and $fixture.Events.Contains('build')) "Interrupted cache was reused: $failure"
      Assert-RuntimeOutput $fixture
    }
  }
}

Test-RuntimeCase 'legacy vector.dll alone cannot bypass verification' {
  param($fixture)
  Write-RuntimeFixtureFile (Join-Path $fixture.Dir 'runtime/pgsql/lib/vector.dll') 'synthetic legacy cache'
  $failure = Invoke-RuntimeFixture $fixture
  Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Contains('download:pg') -and $fixture.Events.Contains('download:vector')) "Legacy cache bypassed inputs: $failure"
  Assert-RuntimeOutput $fixture
}

foreach ($change in @('missing', 'malformed', 'fingerprint', 'schema_type', 'oversized', 'missing_file')) {
  Test-RuntimeCase "$change completion cannot bypass verification" {
    param($fixture)
    $failure = Invoke-RuntimeFixture $fixture
    Assert-RuntimeTest ($null -eq $failure) "Initial fixture failed: $failure"
    $marker = Join-Path $fixture.Dir 'complete.json'
    switch ($change) {
      'missing' { Remove-Item -LiteralPath $marker }
      'malformed' { [IO.File]::WriteAllText($marker, '{invalid json') }
      'fingerprint' { [IO.File]::WriteAllText($marker, '{"schema":1,"input_fingerprint":"old-inputs"}') }
      'schema_type' {
        $record = ConvertFrom-Json -InputObject ([IO.File]::ReadAllText($marker))
        $record.schema = '1'
        [IO.File]::WriteAllText($marker, ($record | ConvertTo-Json -Compress))
      }
      'oversized' { [IO.File]::WriteAllText($marker, ('x' * 4097)) }
      'missing_file' { Remove-Item -LiteralPath (Join-Path $fixture.Dir "runtime/pgsql/share/extension/vector--$($fixture.VectorVersion).sql") }
    }
    $failure = Invoke-RuntimeFixture $fixture
    Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Contains('download:pg') -and $fixture.Events.Contains('build')) "Invalid completed cache was reused: $failure"
    Assert-RuntimeOutput $fixture
  }
}

Test-RuntimeCase 'changing an input pin invalidates an otherwise completed cache' {
  param($fixture)
  $failure = Invoke-RuntimeFixture $fixture
  Assert-RuntimeTest ($null -eq $failure) "Initial fixture failed: $failure"
  $marker = Join-Path $fixture.Dir 'complete.json'
  $before = [IO.File]::ReadAllText($marker)
  $oldPin = (Get-FileHash -LiteralPath $fixture.GoodPg).Hash.ToLowerInvariant()
  New-InertRuntimeZip $fixture.GoodPg 'synthetic updated PostgreSQL input'
  $newPin = (Get-FileHash -LiteralPath $fixture.GoodPg).Hash.ToLowerInvariant()
  $copy = [IO.File]::ReadAllText($fixture.Script)
  Assert-RuntimeTest ([regex]::Matches($copy, [regex]::Escape($oldPin)).Count -eq 1) 'Input pin replacement must be unique'
  [IO.File]::WriteAllText($fixture.Script, $copy.Replace($oldPin, $newPin), [Text.UTF8Encoding]::new($false))
  $failure = Invoke-RuntimeFixture $fixture
  Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Contains('download:pg') -and $fixture.Events.Contains('build')) "Changed pin reused old completion: $failure"
  Assert-RuntimeTest ([IO.File]::ReadAllText($marker) -ne $before) 'Completion fingerprint did not bind the input pin'
  Assert-RuntimeOutput $fixture
}

function Add-RuntimeStageNeighbors([hashtable]$Fixture) {
  Write-RuntimeFixtureFile (Join-Path $Fixture.Stage 'runtime/unrelated-runtime.dat') 'synthetic neighbor'
  Write-RuntimeFixtureFile (Join-Path $Fixture.Stage 'runtime/pgsql-backup-neighbor/keep.txt') 'synthetic backup neighbor'
  Write-RuntimeFixtureFile (Join-Path $Fixture.Stage 'runtime/pgsql-incoming-neighbor/keep.txt') 'synthetic incoming neighbor'
  Write-RuntimeFixtureFile (Join-Path $Fixture.Stage 'runtime/pgsql/previous-only.txt') 'synthetic previous stage only'
}

function Assert-RuntimeStageNeighbors([hashtable]$Fixture) {
  Assert-RuntimeTest ([IO.File]::ReadAllText((Join-Path $Fixture.Stage 'runtime/unrelated-runtime.dat')) -eq 'synthetic neighbor') 'Runtime neighbor changed'
  Assert-RuntimeTest ([IO.File]::ReadAllText((Join-Path $Fixture.Stage 'runtime/pgsql-backup-neighbor/keep.txt')) -eq 'synthetic backup neighbor') 'Backup neighbor changed'
  Assert-RuntimeTest ([IO.File]::ReadAllText((Join-Path $Fixture.Stage 'runtime/pgsql-incoming-neighbor/keep.txt')) -eq 'synthetic incoming neighbor') 'Incoming neighbor changed'
}

foreach ($mode in @('stage_copy_failure', 'stage_missing_file', 'stage_backup_failure', 'stage_backup_partial_failure', 'stage_publish_failure', 'stage_partial_publish_failure')) {
  Test-RuntimeCase "$mode preserves the source, previous staging and neighbors" {
    param($fixture)
    Assert-RuntimeTest ($null -eq (Invoke-RuntimeFixture $fixture)) 'Initial completed cache failed'
    Add-RuntimeStageNeighbors $fixture
    $stageBefore = Get-RuntimeStageSnapshot $fixture.Stage
    $cacheBefore = Get-RuntimeStageSnapshot $fixture.Cache
    $failure = Invoke-RuntimeFixture $fixture $mode
    $expected = switch ($mode) {
      'stage_copy_failure' { 'synthetic staging copy failed' }
      'stage_missing_file' { 'runtime staging copy incomplete' }
      'stage_backup_failure' { 'synthetic backup rename failed' }
      'stage_backup_partial_failure' { 'synthetic backup rename failed after move' }
      'stage_publish_failure' { 'synthetic staging publication failed' }
      'stage_partial_publish_failure' { 'synthetic staging publication failed after move' }
    }
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains($expected)) "Expected staging failure: $failure"
    Assert-RuntimeTest ($fixture.Events.Count -eq 0) 'A staging failure unexpectedly rebuilt its verified cache'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Stage) -eq $stageBefore) 'Staging failure lost old output or left transition files'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Cache) -eq $cacheBefore) 'Staging failure changed the source cache'
    Assert-RuntimeStageNeighbors $fixture
    $failure = Invoke-RuntimeFixture $fixture
    Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Count -eq 0) "Staging retry failed: $failure"
    Assert-RuntimeOutput $fixture
    Assert-RuntimeStageNeighbors $fixture
    Assert-RuntimeTest (-not (Test-Path -LiteralPath (Join-Path $fixture.Stage 'runtime/pgsql/previous-only.txt'))) 'Successful publication retained stale runtime output'
  }
}

foreach ($mode in @('stage_copy_failure', 'stage_publish_failure', 'stage_partial_publish_failure')) {
  Test-RuntimeCase "$mode without old staging preserves the original empty destination" {
    param($fixture)
    Assert-RuntimeTest ($null -eq (Invoke-RuntimeFixture $fixture)) 'Initial completed cache failed'
    Remove-Item -LiteralPath (Join-Path $fixture.Stage 'runtime/pgsql') -Recurse -Force
    $stageBefore = Get-RuntimeStageSnapshot $fixture.Stage
    $cacheBefore = Get-RuntimeStageSnapshot $fixture.Cache
    $failure = Invoke-RuntimeFixture $fixture $mode
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('synthetic staging')) "Expected staging failure: $failure"
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Stage) -eq $stageBefore) 'Failed publication left a partial destination'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Cache) -eq $cacheBefore) 'Failed publication changed its source'
  }
}

Test-RuntimeCase 'rollback failure retains the previous runtime in its owned backup' {
  param($fixture)
  Assert-RuntimeTest ($null -eq (Invoke-RuntimeFixture $fixture)) 'Initial completed cache failed'
  Add-RuntimeStageNeighbors $fixture
  $staged = Join-Path $fixture.Stage 'runtime/pgsql'
  $oldOutput = Get-RuntimeStageSnapshot $staged
  $cacheBefore = Get-RuntimeStageSnapshot $fixture.Cache
  $failure = Invoke-RuntimeFixture $fixture 'stage_rollback_failure'
  Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('previous output recovery requires') -and $failure.Contains('synthetic rollback failed') -and $failure.Contains('synthetic staging publication failed')) "Rollback failure was hidden: $failure"
  $backups = @(Get-ChildItem -LiteralPath (Join-Path $fixture.Stage 'runtime') -Directory |
    Where-Object { $_.Name -cmatch '^pgsql-backup-[0-9a-f]{32}$' })
  Assert-RuntimeTest ($backups.Count -eq 1) 'Rollback failure deleted the recoverable backup'
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $backups[0].FullName) -eq $oldOutput) 'Backup lost the previous runtime bytes'
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Cache) -eq $cacheBefore) 'Rollback failure changed its source cache'
  Assert-RuntimeStageNeighbors $fixture
  $incoming = @(Get-ChildItem -LiteralPath (Join-Path $fixture.Stage 'runtime') -Directory |
    Where-Object { $_.Name -cmatch '^pgsql-incoming-[0-9a-f]{32}$' })
  Assert-RuntimeTest ($incoming.Count -eq 0) 'Rollback failure leaked its owned incoming copy'
}

foreach ($overlap in @('warm_same', 'cold_same', 'normalized_same', 'destination_inside_cache', 'cache_inside_destination')) {
  Test-RuntimeCase "$overlap is rejected before cache or stage changes" {
    param($fixture)
    Assert-RuntimeTest ($null -eq (Invoke-RuntimeFixture $fixture)) 'Initial completed cache failed'
    switch ($overlap) {
      'warm_same' { $fixture.Stage = $fixture.Dir }
      'cold_same' {
        Remove-Item -LiteralPath (Join-Path $fixture.Dir 'complete.json')
        $fixture.Stage = $fixture.Dir
      }
      'normalized_same' { $fixture.Stage = Join-Path $fixture.Dir 'unused/..' }
      'destination_inside_cache' { $fixture.Stage = Join-Path $fixture.Dir 'nested' }
      'cache_inside_destination' {
        $fixture.Cache = Join-Path $fixture.Stage 'runtime/pgsql/cache'
        $fixture.Dir = Join-Path $fixture.Cache "postgres-$($fixture.PgVersion)-pgvector-$($fixture.VectorVersion)"
      }
    }
    $before = Get-RuntimeStageSnapshot $fixture.Root
    $failure = Invoke-RuntimeFixture $fixture
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('cache and staging runtime paths overlap')) "Overlapping paths were accepted: $failure"
    Assert-RuntimeTest ($fixture.Events.Count -eq 0) 'Overlapping paths reached download/extraction/build'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Root) -eq $before) 'Overlap rejection changed the cache or staging originals'
  }
}

Test-RuntimeCase 'a neighboring path prefix remains a valid staging destination' {
  param($fixture)
  Assert-RuntimeTest ($null -eq (Invoke-RuntimeFixture $fixture)) 'Initial completed cache failed'
  $cacheBefore = Get-RuntimeStageSnapshot $fixture.Dir
  $fixture.Stage = $fixture.Dir + '-neighbor'
  Write-RuntimeFixtureFile (Join-Path $fixture.Stage 'keep.txt') 'synthetic unrelated stage file'
  $failure = Invoke-RuntimeFixture $fixture
  Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Count -eq 0) "Distinct neighboring paths were rejected: $failure"
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Dir) -eq $cacheBefore) 'Neighbor staging changed the source cache version'
  Assert-RuntimeOutput $fixture
}

Test-RuntimeCase 'relative paths use the PowerShell location when the process location differs' {
  param($fixture)
  $previousLocation = Get-Location
  $previousProcessLocation = [Environment]::CurrentDirectory
  $processLocation = Join-Path $fixture.Root 'different process location'
  [IO.Directory]::CreateDirectory($processLocation) | Out-Null
  Write-RuntimeFixtureFile (Join-Path $processLocation 'keep.txt') 'synthetic process location neighbor'
  $processBefore = Get-RuntimeStageSnapshot $processLocation
  try {
    Set-Location -LiteralPath $fixture.Root
    [Environment]::CurrentDirectory = $processLocation
    $failure = Invoke-RuntimeFixture $fixture -CachePath 'cache' -StagePath 'stage'
    Assert-RuntimeTest ($null -eq $failure) "Relative cold preparation failed: $failure"
    Assert-RuntimeTest (($fixture.Events -join ',') -eq 'download:pg,download:vector,extract:pg,extract:vector,vswhere,build') 'Relative inputs did not follow the normal verified preparation'
    Assert-RuntimeOutput $fixture
    $stageBefore = Get-RuntimeStageSnapshot $fixture.Stage
    $cacheBefore = Get-RuntimeStageSnapshot $fixture.Cache
    $failure = Invoke-RuntimeFixture $fixture -CachePath './cache' -StagePath './stage'
    Assert-RuntimeTest ($null -eq $failure -and $fixture.Events.Count -eq 0) "Relative warm preparation failed: $failure"
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Stage) -eq $stageBefore) 'Relative warm staging changed output'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Cache) -eq $cacheBefore) 'Relative warm staging changed its source cache'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $processLocation) -eq $processBefore) 'Runtime preparation wrote into the process working location'
  } finally {
    Set-Location -LiteralPath $previousLocation.Path
    [Environment]::CurrentDirectory = $previousProcessLocation
  }
}

Test-RuntimeCase 'normalization preserves the filesystem root and its overlap boundary' {
  param($fixture)
  # Load only the actual pure path helpers; never prepare files at a filesystem root.
  $helpers = @([scriptblock]::Create($source).Ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
      $node.Name -in @('Get-NormalizedRuntimePath', 'Test-RuntimePathOverlap')
  }, $false))
  Assert-RuntimeTest ($helpers.Count -eq 2) 'Expected both production path helpers'
  foreach ($helper in $helpers) { . ([scriptblock]::Create($helper.Extent.Text)) }
  $root = [IO.Path]::GetPathRoot($fixture.Root)
  Assert-RuntimeTest ((Get-NormalizedRuntimePath $root) -ceq $root) 'Normalization removed the filesystem root separator'
  Assert-RuntimeTest ((Get-NormalizedRuntimePath (Join-Path $root '.')) -ceq $root) 'Root dot normalization lost the filesystem root'
  Assert-RuntimeTest (Test-RuntimePathOverlap $root $fixture.Root) 'Root did not overlap its child path'
  Assert-RuntimeTest (Test-RuntimePathOverlap $fixture.Root $root) 'Root overlap depended on argument order'
}

Test-RuntimeCase 'mock aliases preserve caller commands after success and failure' {
  param($fixture)
  Assert-RuntimeTest ((Get-RuntimeCommandSnapshot) -ceq $originalRuntimeCommands) 'Fixture replaced a caller command outside runtime preparation'
  & {
    # This caller alias must survive the production script's temporary mock binding.
    Set-Alias -Name Invoke-WebRequest -Value Write-Output -Scope Local
    $callerCommands = Get-RuntimeCommandSnapshot
    $failure = Invoke-RuntimeFixture $fixture
    Assert-RuntimeTest ($null -eq $failure) "Scoped preparation failed: $failure"
    Assert-RuntimeTest ((Get-RuntimeCommandSnapshot) -ceq $callerCommands) 'Successful preparation changed caller commands or aliases'
    $failure = Invoke-RuntimeFixture $fixture 'stage_copy_failure'
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('synthetic staging copy failed')) "Scoped failure injection changed contract: $failure"
    Assert-RuntimeTest ((Get-RuntimeCommandSnapshot) -ceq $callerCommands) 'Failed preparation changed caller commands or aliases'
  }
  Assert-RuntimeTest ((Get-RuntimeCommandSnapshot) -ceq $originalRuntimeCommands) 'A child caller alias leaked into the parent scope'
}

foreach ($shape in @('stage_ancestor', 'cache_ancestor', 'cache_leaf', 'cache_runtime', 'stage_runtime', 'stage_leaf', 'stage_missing_tail')) {
  Test-RuntimeCase "$shape rejects linked components before cleanup or preparation" {
    param($fixture)
    $originalStage = $fixture.Stage
    $originalCache = $fixture.Cache
    $realTarget = Join-Path $fixture.Root 'link target'
    [IO.Directory]::CreateDirectory($realTarget) | Out-Null
    switch ($shape) {
      'stage_ancestor' {
        $repository = Join-Path $fixture.Dir 'repository'
        $realTarget = Join-Path $repository 'desktop/stage'
        Write-RuntimeFixtureFile (Join-Path $realTarget 'runtime/pgsql/previous.txt') 'synthetic alias stage output'
        $callerLocation = (Get-Location).Path
        $inputBefore = (Get-FileHash -LiteralPath $fixture.GoodPg).Hash
        $setupFailure = $null
        try { New-RuntimeFixtureLink $fixture $fixture.GoodPg $repository }
        catch { $setupFailure = $_ }
        Assert-RuntimeTest ($null -ne $setupFailure) 'Link creation replaced an existing fixture input file'
        Assert-RuntimeTest ((Get-Location).Path -ceq $callerLocation) 'Failed link creation changed the caller location'
        Assert-RuntimeTest ((Get-FileHash -LiteralPath $fixture.GoodPg).Hash -ceq $inputBefore) 'Failed link creation changed fixture input bytes'
        $alias = Join-Path $fixture.Root 'repository alias'
        New-RuntimeFixtureLink $fixture $alias $repository
        $fixture.Stage = Join-Path $alias 'desktop/stage'
      }
      'cache_ancestor' {
        [IO.Directory]::CreateDirectory($originalCache) | Out-Null
        $fixture.Cache = Join-Path $fixture.Root 'cache alias'
        New-RuntimeFixtureLink $fixture $fixture.Cache $originalCache
        $fixture.Dir = Join-Path $fixture.Cache "postgres-$($fixture.PgVersion)-pgvector-$($fixture.VectorVersion)"
      }
      'cache_leaf' { New-RuntimeFixtureLink $fixture $fixture.Dir $realTarget }
      'cache_runtime' { New-RuntimeFixtureLink $fixture (Join-Path $fixture.Dir 'runtime') $realTarget }
      'stage_runtime' {
        [IO.Directory]::Delete($realTarget)
        [IO.Directory]::Move((Join-Path $fixture.Stage 'runtime'), $realTarget)
        New-RuntimeFixtureLink $fixture (Join-Path $fixture.Stage 'runtime') $realTarget
      }
      'stage_leaf' {
        [IO.Directory]::Delete($realTarget)
        [IO.Directory]::Move((Join-Path $fixture.Stage 'runtime/pgsql'), $realTarget)
        New-RuntimeFixtureLink $fixture (Join-Path $fixture.Stage 'runtime/pgsql') $realTarget
      }
      'stage_missing_tail' {
        $alias = Join-Path $fixture.Root 'stage alias'
        New-RuntimeFixtureLink $fixture $alias $realTarget
        $fixture.Stage = Join-Path $alias 'missing/stage'
      }
    }
    $sentinel = Join-Path $realTarget 'keep.txt'
    Write-RuntimeFixtureFile $sentinel 'synthetic linked target sentinel'
    $targetBefore = Get-RuntimeStageSnapshot $realTarget
    $stageBefore = Get-RuntimeStageSnapshot $originalStage
    $failure = Invoke-RuntimeFixture $fixture
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('reparse')) "Linked path was accepted: $failure"
    Assert-RuntimeTest ($fixture.Events.Count -eq 0 -and $fixture.StageCopies -eq 0) 'Linked path reached preparation or staging'
    Assert-RuntimeTest ([IO.File]::ReadAllText($sentinel) -eq 'synthetic linked target sentinel') 'Linked path rejection lost the target sentinel'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $realTarget) -ceq $targetBefore) 'Linked path rejection changed target bytes'
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $originalStage) -ceq $stageBefore) 'Linked path rejection changed existing staging'
    Assert-RuntimeTest (-not (Test-Path -LiteralPath (Join-Path $fixture.Dir 'complete.json'))) 'Linked path completed its cache'
  }
}

Test-RuntimeCase 'a non-directory cache component is preserved before cleanup' {
  param($fixture)
  Write-RuntimeFixtureFile $fixture.Dir 'synthetic regular file at a directory path'
  $stageBefore = Get-RuntimeStageSnapshot $fixture.Stage
  $failure = Invoke-RuntimeFixture $fixture
  Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('directory')) "Non-directory cache was accepted: $failure"
  Assert-RuntimeTest ($fixture.Events.Count -eq 0 -and $fixture.StageCopies -eq 0) 'Non-directory path reached preparation or staging'
  Assert-RuntimeTest ([IO.File]::ReadAllText($fixture.Dir) -eq 'synthetic regular file at a directory path') 'Non-directory rejection deleted the original file'
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Stage) -ceq $stageBefore) 'Non-directory rejection changed staging'
}

Test-RuntimeCase 'path inspection I/O errors stop before preparation' {
  param($fixture)
  $before = Get-RuntimeStageSnapshot $fixture.Root
  $failure = Invoke-RuntimeFixture $fixture 'path_inspection_failure'
  Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('synthetic runtime path inspection failed')) "Inspection failure was treated as a missing tail: $failure"
  Assert-RuntimeTest ($fixture.Events.Count -eq 0 -and $fixture.StageCopies -eq 0) 'Inspection error reached preparation or staging'
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Root) -ceq $before) 'Inspection error changed owned input or output bytes'
}

Test-RuntimeCase 'a link introduced during build stops before stage copy or publication' {
  param($fixture)
  Add-RuntimeStageNeighbors $fixture
  $before = Get-RuntimeStageSnapshot (Join-Path $fixture.Stage 'runtime')
  $failure = Invoke-RuntimeFixture $fixture 'stage_link_during_build'
  Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('reparse')) "Post-build linked staging was accepted: $failure"
  Assert-RuntimeTest ($fixture.Events.Contains('build') -and $fixture.StageCopies -eq 0) 'Post-build inspection did not stop staging copy'
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.LinkedStageTarget) -ceq $before) 'Post-build rejection changed the previous runtime or neighbors'
  Assert-RuntimeTest (-not (Test-Path -LiteralPath (Join-Path $fixture.Dir 'complete.json'))) 'Post-build linked path completed its cache'
}

foreach ($mode in @('stage_publish_cleanup_failure', 'stage_rollback_cleanup_failure')) {
  Test-RuntimeCase "$mode retains every cause and the previous runtime" {
    param($fixture)
    Assert-RuntimeTest ($null -eq (Invoke-RuntimeFixture $fixture)) 'Initial completed cache failed'
    Add-RuntimeStageNeighbors $fixture
    $staged = Join-Path $fixture.Stage 'runtime/pgsql'
    $oldOutput = Get-RuntimeStageSnapshot $staged
    $cacheBefore = Get-RuntimeStageSnapshot $fixture.Cache
    $failure = Invoke-RuntimeFixture $fixture $mode
    Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('synthetic incoming cleanup failed')) "Cleanup failure was hidden: $failure"
    Assert-RuntimeTest ($failure.Contains('synthetic staging publication failed')) "Cleanup failure replaced the original publication error: $failure"
    $backups = @(Get-ChildItem -LiteralPath (Join-Path $fixture.Stage 'runtime') -Directory |
      Where-Object { $_.Name -cmatch '^pgsql-backup-[0-9a-f]{32}$' })
    if ($mode -eq 'stage_rollback_cleanup_failure') {
      Assert-RuntimeTest ($failure.Contains('synthetic rollback failed') -and $failure.Contains('previous output recovery requires')) "Cleanup failure replaced rollback recovery information: $failure"
      Assert-RuntimeTest ($backups.Count -eq 1) 'Cleanup failure deleted the recoverable backup'
      Assert-RuntimeTest ($failure.Contains($backups[0].FullName)) 'Cleanup failure lost the exact recovery path'
      Assert-RuntimeTest ((Get-RuntimeStageSnapshot $backups[0].FullName) -ceq $oldOutput) 'Cleanup failure changed the recoverable previous runtime'
    } else {
      Assert-RuntimeTest ($backups.Count -eq 0) 'Successful rollback left its backup behind'
      Assert-RuntimeTest ((Get-RuntimeStageSnapshot $staged) -ceq $oldOutput) 'Cleanup failure lost the restored previous runtime'
    }
    Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Cache) -ceq $cacheBefore) 'Cleanup failure changed its source cache'
    Assert-RuntimeTest ($fixture.Events.Count -eq 0) 'Warm cleanup failure unexpectedly rebuilt its source'
    Assert-RuntimeStageNeighbors $fixture
    $incoming = @(Get-ChildItem -LiteralPath (Join-Path $fixture.Stage 'runtime') -Directory |
      Where-Object { $_.Name -cmatch '^pgsql-incoming-[0-9a-f]{32}$' })
    Assert-RuntimeTest ($incoming.Count -eq 1) 'Cleanup failure fixture did not retain the owned incoming copy'
  }
}

Test-RuntimeCase 'a cleanup inspection error after successful publication reports only cleanup' {
  param($fixture)
  Assert-RuntimeTest ($null -eq (Invoke-RuntimeFixture $fixture)) 'Initial completed cache failed'
  Add-RuntimeStageNeighbors $fixture
  $cacheBefore = Get-RuntimeStageSnapshot $fixture.Cache
  $failure = Invoke-RuntimeFixture $fixture 'stage_cleanup_guard_failure'
  Assert-RuntimeTest ($null -ne $failure -and $failure.Contains('synthetic cleanup inspection failed')) "Cleanup-only error was hidden: $failure"
  Assert-RuntimeTest (-not $failure.Contains('synthetic staging publication failed') -and -not $failure.Contains('synthetic rollback failed')) 'Cleanup-only error invented a publication or rollback cause'
  Assert-RuntimeOutput $fixture
  Assert-RuntimeStageNeighbors $fixture
  Assert-RuntimeTest ((Get-RuntimeStageSnapshot $fixture.Cache) -ceq $cacheBefore -and $fixture.Events.Count -eq 0) 'Cleanup-only failure changed or rebuilt its source cache'
  $leftovers = @(Get-ChildItem -LiteralPath (Join-Path $fixture.Stage 'runtime') -Directory |
    Where-Object { $_.Name -cmatch '^pgsql-(incoming|backup)-[0-9a-f]{32}$' })
  Assert-RuntimeTest ($leftovers.Count -eq 0) 'Successful publication left a transition directory'
}

Assert-RuntimeTest ((Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash -eq $sourceHash) 'Tests changed the original preparation script'
Write-Host "All $testCount offline runtime preparation cases passed."
