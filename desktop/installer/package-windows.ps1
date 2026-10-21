# Shared packaging path. Emission writes only an uninstaller in a fresh owned
# build directory; it never installs the product or runs the emitted uninstaller.
function Assert-InstallerPackagingPath([string]$Path, [bool]$Directory) {
  if ($Path -notmatch '^[A-Za-z]:[\\/]' -or $Path -match '["\x00-\x1f]') { throw 'packaging_absolute_literal_path_required' }
  $current = [IO.Path]::GetFullPath($Path)
  $first = $true
  while ($current) {
    $entry = Get-Item -LiteralPath $current -Force -ErrorAction Stop
    if ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'packaging_reparse_path_rejected' }
    $expectedDirectory = if ($first) { $Directory } else { $true }
    if ([bool]($entry.Attributes -band [IO.FileAttributes]::Directory) -ne $expectedDirectory) { throw 'packaging_path_type_invalid' }
    $first = $false
    $current = [IO.Path]::GetDirectoryName($current)
  }
}
function Invoke-InstallerPackagingProcess([string]$Executable, [string]$Arguments, [string]$LogRoot, [string]$Name) {
  $process = Start-Process -FilePath $Executable -ArgumentList $Arguments -PassThru -Wait -WindowStyle Hidden `
    -RedirectStandardOutput (Join-Path $LogRoot ($Name + '.out.log')) `
    -RedirectStandardError (Join-Path $LogRoot ($Name + '.err.log'))
  foreach ($suffix in @('.out.log', '.err.log')) {
    $log = Join-Path $LogRoot ($Name + $suffix)
    if ((Get-Item -LiteralPath $log).Length) { Get-Content -LiteralPath $log | Out-Host }
  }
  if ($process.ExitCode -ne 0) { throw "packaging_process_failed: $Name ($($process.ExitCode))" }
}
function Invoke-WindowsInstallerPackage {
  param(
    [Parameter(Mandatory)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    [Parameter(Mandatory)][string]$Stage,
    [Parameter(Mandatory)][string]$Makensis,
    [Parameter(Mandatory)][string]$Output,
    [string]$TempRoot = [IO.Path]::GetTempPath(),
    [scriptblock]$SignUninstaller
  )
  $Stage = [IO.Path]::GetFullPath($Stage).TrimEnd('\')
  $Output = [IO.Path]::GetFullPath($Output)
  $TempRoot = [IO.Path]::GetFullPath($TempRoot)
  Assert-InstallerPackagingPath $Stage $true
  Assert-InstallerPackagingPath $TempRoot $true
  Assert-InstallerPackagingPath ([IO.Path]::GetDirectoryName($Output)) $true
  if (Test-Path -LiteralPath $Output) { throw 'packaging_fresh_output_required' }
  $uninstaller = Join-Path $Stage 'uninstall.exe'
  if (Test-Path -LiteralPath $uninstaller) { throw 'packaging_fresh_uninstaller_required' }
  $helper = Join-Path $Stage 'runtime/arkism-installer.exe'
  Assert-InstallerPackagingPath $helper $false
  $compiler = Get-Command $Makensis -CommandType Application -ErrorAction Stop | Select-Object -First 1
  Assert-InstallerPackagingPath $compiler.Source $false
  $source = Join-Path $PSScriptRoot 'arkism.nsi'
  $owner = [guid]::NewGuid().ToString('N')
  $root = Join-Path $TempRoot ('arkism-uninstaller-' + $owner)
  $null = New-Item -ItemType Directory -Path $root -ErrorAction Stop
  [IO.File]::WriteAllText((Join-Path $root 'TASK-OWNER.txt'), $owner)
  Write-Information "Owned uninstaller emission directory: $root" -InformationAction Continue
  $emitter = Join-Path $root 'emit-uninstaller.exe'
  & $compiler.Source /V2 "/DVERSION=$Version" /DARKISM_EMIT_UNINSTALLER "/DSTAGE_DIR=$Stage" "/DOUT_FILE=$emitter" $source
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $emitter -PathType Leaf)) { throw 'packaging_uninstaller_compile_failed' }
  Invoke-InstallerPackagingProcess $emitter '/S' $root 'emit'
  $generated = Join-Path $root 'uninstall.exe'
  Assert-InstallerPackagingPath $generated $false
  Copy-Item -LiteralPath $generated -Destination $uninstaller -ErrorAction Stop
  if ($SignUninstaller) { & $SignUninstaller $uninstaller | Out-Host }
  $manifest = Join-Path $root 'payload.json'
  Invoke-InstallerPackagingProcess $helper ('pack "' + $Version + '" "' + $Stage + '" "' + $manifest + '"') $root 'pack'
  Assert-InstallerPackagingPath $manifest $false
  & $compiler.Source /V2 "/DVERSION=$Version" "/DSTAGE_DIR=$Stage" "/DPACKAGE_MANIFEST=$manifest" "/DOUT_FILE=$Output" $source
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $Output -PathType Leaf)) { throw 'packaging_installer_compile_failed' }
  return [pscustomobject]@{ Installer = $Output; Uninstaller = $uninstaller; Manifest = $manifest; EmissionRoot = $root }
}
