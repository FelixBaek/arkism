# Built-in Arkism for the Windows installer: portable Postgres with pgvector, prepared once per
# version in the build PC's cache, then copied into the installer stage beside the server programs.
# Needs the Visual Studio C++ build tools (for pgvector). Run by build-windows.ps1.
param([Parameter(Mandatory)][string]$Cache, [Parameter(Mandatory)][string]$Stage)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$pgVersion = '17.6-1'
$vectorVersion = '0.8.1'
$pgUrl = "https://get.enterprisedb.com/postgresql/postgresql-$pgVersion-windows-x64-binaries.zip"
$vectorUrl = "https://github.com/pgvector/pgvector/archive/refs/tags/v$vectorVersion.zip"
# Exact bytes observed at these official HTTPS origins on 2026-10-18.
# These pins are not independently published or signed upstream checksums.
$pgSha256 = 'd378882abd001a186735acd6f6ba716bca6ccd192e800412d4fd15ed25376b3e'
$vectorSha256 = '61182a6afd6fb94c0e7740c037a7d3c36a8cab1cc581ae5bd9b7b2cfd789bdfc'
$tools = 'postgres.exe', 'pg_ctl.exe', 'initdb.exe', 'psql.exe', 'pg_dump.exe', 'pg_restore.exe'
$required = @($tools | ForEach-Object { "bin/$_" }) + @(
  'lib/vector.dll', 'share/postgres.bki', 'share/postgresql.conf.sample', 'share/pg_hba.conf.sample',
  'share/extension/vector.control', "share/extension/vector--$vectorVersion.sql"
)

function Get-VerifiedRuntimeZip([string]$Uri, [string]$Path, [string]$ExpectedHash) {
  Invoke-WebRequest -UseBasicParsing $Uri -OutFile $Path -TimeoutSec 300
  $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
  if (-not [string]::Equals($actual, $ExpectedHash, [StringComparison]::OrdinalIgnoreCase)) {
    throw "checksum mismatch for $Uri"
  }
}

function Test-RuntimeFiles([string]$Root, [string[]]$Required) {
  foreach ($relative in $Required) {
    $path = Join-Path $Root $relative
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $false }
    if ((Get-Item -LiteralPath $path -ErrorAction Stop).Length -le 0) { return $false }
  }
  return $true
}

function Test-RuntimeCache([string]$Root, [string]$Completion, [string]$Fingerprint, [string[]]$Required) {
  if (-not (Test-Path -LiteralPath $Completion -PathType Leaf)) { return $false }
  try {
    # Completion metadata is small. A malformed or interrupted marker is a cache miss.
    $buffer = New-Object byte[] 4097
    $stream = [IO.File]::OpenRead($Completion)
    $length = 0
    try {
      while ($length -lt $buffer.Length) {
        $read = $stream.Read($buffer, $length, $buffer.Length - $length)
        if ($read -eq 0) { break }
        $length += $read
      }
    } finally { $stream.Dispose() }
    if ($length -eq 0 -or $length -gt 4096) { return $false }
    $utf8 = [Text.UTF8Encoding]::new($false, $true)
    $text = $utf8.GetString($buffer, 0, $length).TrimStart([char]0xfeff).Trim()
    if (-not $text.StartsWith('{')) { return $false }
    $record = ConvertFrom-Json -InputObject $text -ErrorAction Stop
    if (@($record.PSObject.Properties).Count -ne 2 -or
        ($record.schema -isnot [int] -and $record.schema -isnot [long]) -or $record.schema -ne 1 -or
        $record.input_fingerprint -isnot [string] -or
        -not [string]::Equals($record.input_fingerprint, $Fingerprint, [StringComparison]::Ordinal)) {
      return $false
    }
    return Test-RuntimeFiles -Root $Root -Required $Required
  } catch { return $false }
}

function Get-NormalizedRuntimePath([string]$Path) {
  $provider, $drive = $null, $null
  $native = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path, [ref]$provider, [ref]$drive)
  if ($provider.Name -ne 'FileSystem') { throw 'runtime paths must use the filesystem' }
  $full = [IO.Path]::GetFullPath($native)
  $root = [IO.Path]::GetPathRoot($full)
  if ([string]::Equals($full, $root, [StringComparison]::OrdinalIgnoreCase)) { return $root }
  return $full.TrimEnd([char[]]@([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar))
}

function Test-RuntimePathOverlap([string]$Left, [string]$Right) {
  $leftPath, $rightPath = (Get-NormalizedRuntimePath $Left), (Get-NormalizedRuntimePath $Right)
  $comparison = [StringComparison]::Ordinal
  if ([IO.Path]::DirectorySeparatorChar -eq '\') { $comparison = [StringComparison]::OrdinalIgnoreCase }
  $separator = [IO.Path]::DirectorySeparatorChar
  $leftPrefix = $leftPath.TrimEnd([char]$separator) + $separator
  $rightPrefix = $rightPath.TrimEnd([char]$separator) + $separator
  return [string]::Equals($leftPath, $rightPath, $comparison) -or
    $leftPath.StartsWith($rightPrefix, $comparison) -or
    $rightPath.StartsWith($leftPrefix, $comparison)
}

function Get-RuntimePathAttributes([string]$Path, [bool]$AllowMissing) {
  try { return [IO.File]::GetAttributes($Path) }
  catch {
    $cause = $_.Exception.GetBaseException()
    if ($AllowMissing -and ($cause -is [IO.FileNotFoundException] -or $cause -is [IO.DirectoryNotFoundException])) {
      return $null
    }
    throw
  }
}

function Assert-RuntimePathComponent([string]$Path, [IO.FileAttributes]$Attributes, [bool]$File) {
  if (($Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "runtime path contains a reparse point: $Path"
  }
  $directory = ($Attributes -band [IO.FileAttributes]::Directory) -ne 0
  if ($File) {
    if ($directory) { throw "runtime file path is a directory: $Path" }
  } elseif (-not $directory) { throw "runtime directory path is not a directory: $Path" }
}

function Assert-RuntimePath([string]$Path, [switch]$File) {
  $full = Get-NormalizedRuntimePath $Path
  $components = [System.Collections.Generic.Stack[string]]::new()
  $component = $full
  while (-not [string]::IsNullOrEmpty($component)) {
    $components.Push($component)
    $component = [IO.Path]::GetDirectoryName($component)
  }
  # Inspect parents first. Only a genuinely missing tail below a verified root is allowed.
  $allowMissing = $false
  foreach ($component in $components) {
    $attributes = Get-RuntimePathAttributes $component $allowMissing
    if ($null -eq $attributes) { return }
    Assert-RuntimePathComponent $component $attributes ($File -and $component -ceq $full)
    $allowMissing = $true
  }
}

function Assert-RuntimeDirectories([string[]]$Paths) {
  foreach ($path in $Paths) { Assert-RuntimePath $path }
}

# Use one filesystem location for both PowerShell cmdlets and .NET file operations.
$Cache = Get-NormalizedRuntimePath $Cache
$Stage = Get-NormalizedRuntimePath $Stage

# The fingerprint records the inputs and recipe, not a signature of cached output files.
$inputs = @('arkism-windows-runtime-v1', 'windows-x86_64', $pgVersion, $pgUrl, $pgSha256,
  $vectorVersion, $vectorUrl, $vectorSha256) -join "`n"
$sha = [Security.Cryptography.SHA256]::Create()
try {
  $fingerprint = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($inputs + "`n"))).Replace('-', '').ToLowerInvariant()
} finally { $sha.Dispose() }
$dir = Join-Path $Cache "postgres-$pgVersion-pgvector-$vectorVersion"
$pg = Join-Path $dir 'runtime\pgsql'
$target = Join-Path $Stage 'runtime'
$staged = Join-Path $target 'pgsql'
# Reject lexical overlap and existing linked components before reading or cleaning the cache.
# Rechecks limit exposure during a build; they do not coordinate concurrent path swaps or all device aliases.
if (Test-RuntimePathOverlap $dir $staged) { throw 'cache and staging runtime paths overlap' }
$completion = Join-Path $dir 'complete.json'
Assert-RuntimeDirectories @($dir, $pg, $staged)
Assert-RuntimePath $completion -File
if (-not (Test-RuntimeCache -Root $pg -Completion $completion -Fingerprint $fingerprint -Required $required)) {
  Assert-RuntimeDirectories @($dir, $pg, $staged)
  if (Test-Path -LiteralPath $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
  New-Item -ItemType Directory -Force $dir | Out-Null
  $full = Join-Path $dir 'full'
  # Verify both inputs before either archive is extracted or any build tool runs.
  Get-VerifiedRuntimeZip -Uri $pgUrl -Path (Join-Path $dir 'pg.zip') -ExpectedHash $pgSha256
  Get-VerifiedRuntimeZip -Uri $vectorUrl -Path (Join-Path $dir 'pgvector.zip') -ExpectedHash $vectorSha256
  Expand-Archive -LiteralPath (Join-Path $dir 'pg.zip') -DestinationPath $full -Force
  Expand-Archive -LiteralPath (Join-Path $dir 'pgvector.zip') -DestinationPath $dir -Force
  $vswhere = Get-Command -Name "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe" -CommandType Application -ErrorAction Stop
  $vs = & $vswhere -latest -products * -property installationPath
  if ($LASTEXITCODE -ne 0 -or $vs -isnot [string] -or [string]::IsNullOrWhiteSpace($vs)) {
    throw 'Visual Studio discovery failed'
  }
  $vcvars = Join-Path $vs 'VC\Auxiliary\Build\vcvars64.bat'
  if (-not (Test-Path -LiteralPath $vcvars -PathType Leaf)) { throw 'Visual Studio C++ build tools missing' }
  $src = Join-Path $dir "pgvector-$vectorVersion"
  cmd /c "`"$vcvars`" >nul && set `"PGROOT=$full\pgsql`" && cd /d `"$src`" && nmake /nologo /F Makefile.win && nmake /nologo /F Makefile.win install"
  if ($LASTEXITCODE -ne 0) { throw 'pgvector build failed' }
  Assert-RuntimeDirectories @($dir, $pg, $staged, $full, $src)
  # Only what the app runs: the server and its setup tools, every DLL, and share/.
  # No pgAdmin, StackBuilder, docs, headers or static libraries.
  New-Item -ItemType Directory -Force (Join-Path $pg 'bin'), (Join-Path $pg 'lib') | Out-Null
  Get-ChildItem -LiteralPath (Join-Path $full 'pgsql\bin') -File | Where-Object { $_.Extension -eq '.dll' -or $tools -contains $_.Name } |
    ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $pg 'bin') }
  Get-ChildItem -LiteralPath (Join-Path $full 'pgsql\lib') -File -Filter *.dll |
    ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $pg 'lib') }
  Copy-Item -LiteralPath (Join-Path $full 'pgsql\share') -Destination (Join-Path $pg 'share') -Recurse
  Assert-RuntimeDirectories @($dir, $pg, $staged, $full, $src)
  Assert-RuntimePath (Join-Path $dir 'pg.zip') -File
  Assert-RuntimePath (Join-Path $dir 'pgvector.zip') -File
  Remove-Item -LiteralPath @($full, (Join-Path $dir 'pg.zip'), (Join-Path $dir 'pgvector.zip'), $src) -Recurse -Force
  if (-not (Test-RuntimeFiles -Root $pg -Required $required)) { throw 'runtime preparation incomplete' }
  # Only a fully prepared cache can bypass input verification on the next build.
  $record = [ordered]@{schema = 1; input_fingerprint = $fingerprint} | ConvertTo-Json -Compress
  $temporary = Join-Path $dir ('complete-' + [Guid]::NewGuid().ToString('N') + '.tmp')
  try {
    Assert-RuntimeDirectories @($dir, $pg, $staged)
    Assert-RuntimePath $completion -File
    Assert-RuntimePath $temporary -File
    [IO.File]::WriteAllText($temporary, $record, [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temporary -Destination $completion -Force
  } finally {
    Assert-RuntimePath $temporary -File
    if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
  }
}
Assert-RuntimeDirectories @($dir, $pg, $staged)
New-Item -ItemType Directory -Force $target | Out-Null
$transaction = [Guid]::NewGuid().ToString('N')
$incoming = Join-Path $target "pgsql-incoming-$transaction"
$backup = Join-Path $target "pgsql-backup-$transaction"
if ((Test-Path -LiteralPath $incoming) -or (Test-Path -LiteralPath $backup)) { throw 'runtime staging temporary path already exists' }
$previous = Test-Path -LiteralPath $staged
$publishing, $published = $false, $false
try {
  Assert-RuntimeDirectories @($pg, $staged, $incoming, $backup)
  Copy-Item -LiteralPath $pg -Destination $incoming -Recurse -Force
  if (-not (Test-RuntimeFiles -Root $incoming -Required $required)) { throw 'runtime staging copy incomplete' }
  Assert-RuntimeDirectories @($pg, $staged, $incoming, $backup)
  if ($previous) { Move-Item -LiteralPath $staged -Destination $backup }
  Assert-RuntimeDirectories @($staged, $incoming, $backup)
  $publishing = $true
  Move-Item -LiteralPath $incoming -Destination $staged
  $published = $true
  Assert-RuntimePath $backup
  if (Test-Path -LiteralPath $backup) { Remove-Item -LiteralPath $backup -Recurse -Force }
} catch {
  $failure = $_
  if (-not $published) {
    if (Test-Path -LiteralPath $backup) {
      try {
        Assert-RuntimeDirectories @($staged, $backup)
        if (Test-Path -LiteralPath $staged) { Remove-Item -LiteralPath $staged -Recurse -Force }
        Move-Item -LiteralPath $backup -Destination $staged
      } catch {
        # Keep the owned backup for recovery if restoring the previous output fails.
        throw "runtime staging failed; previous output recovery requires $backup : $($_.Exception.Message)"
      }
    } elseif ($publishing -and -not $previous -and (Test-Path -LiteralPath $staged)) {
      Assert-RuntimePath $staged
      Remove-Item -LiteralPath $staged -Recurse -Force
    }
  }
  throw $failure
} finally {
  Assert-RuntimePath $incoming
  if (Test-Path -LiteralPath $incoming) { Remove-Item -LiteralPath $incoming -Recurse -Force }
}
$mb = [int]((Get-ChildItem -LiteralPath $target -Recurse -File | Measure-Object Length -Sum).Sum / 1MB)
Write-Host "built-in runtime staged: $mb MB"
