# Native control cases for the product NSIS source with an inert payload.
# This is distinct from running the final shipping installer and its real runtime.
param(
  [Parameter(Mandatory)][string]$Makensis,
  [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{40}$')][string]$SourceSha,
  [Parameter(Mandatory)][string]$EvidencePath
)
$ErrorActionPreference = 'Stop'
$autorunSourceSha = $SourceSha
$autorunEvidencePath = $EvidencePath
. (Join-Path $PSScriptRoot 'smoke-windows-installed.ps1')
$SourceSha = $autorunSourceSha
$EvidencePath = $autorunEvidencePath

function ConvertTo-NsisCompilerPath($Path) {
  if ($Path -isnot [string] -or [string]::IsNullOrWhiteSpace($Path)) { throw 'nsis_control_compiler_path_invalid' }
  $absolute = $Path.StartsWith('/') -and -not $Path.StartsWith('//')
  if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) { $absolute = $Path -match '^[A-Za-z]:[\\/]' }
  if (-not $absolute) { throw 'nsis_control_compiler_absolute_local_path_required' }
  return ConvertTo-InstalledSmokePath $Path
}

function Assert-NsisCompilerEntry([string]$Path, [bool]$Directory) {
  $attrs = Get-InstalledSmokeAttributes $Path
  if ($null -eq $attrs -or ($attrs -band [IO.FileAttributes]::ReparsePoint)) { throw 'nsis_control_compiler_entry_unsafe' }
  if ([bool]($attrs -band [IO.FileAttributes]::Directory) -ne $Directory) { throw 'nsis_control_compiler_entry_type' }
}

function Get-NsisCompilerAliasTarget([string]$SdkRoot) {
  $item = Get-Item -LiteralPath $SdkRoot -Force -ErrorAction Stop
  if ($item.LinkType -cnotin @('Junction', 'SymbolicLink')) { throw 'nsis_control_sdk_alias_unsupported' }
  $targets = @($item.Target)
  if ($targets.Count -ne 1) { throw 'nsis_control_sdk_alias_target_ambiguous' }
  return ConvertTo-NsisCompilerPath $targets[0]
}

function Get-NsisCompilerDigest([string]$Path) {
  # The source SDK alias is the only inspected link allowed by this resolver.
  return (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
}

function Resolve-NsisCompilerAlias([string]$Source, [string]$SdkRoot) {
  $target = Get-NsisCompilerAliasTarget $SdkRoot
  $physical = Join-Path $target 'bin/go.exe'
  Assert-InstalledSmokeFile $physical
  $sourceHash = Get-NsisCompilerDigest $Source
  $targetHash = Get-NsisCompilerDigest $physical
  if ($sourceHash -cne $targetHash) { throw 'nsis_control_sdk_alias_hash_mismatch' }
  if ((Get-NsisCompilerAliasTarget $SdkRoot) -cne $target) { throw 'nsis_control_sdk_alias_changed' }
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($SdkRoot))
  Assert-NsisCompilerEntry ([IO.Path]::GetDirectoryName($Source)) $true
  Assert-NsisCompilerEntry $Source $false
  Assert-InstalledSmokeFile $physical
  return [pscustomobject]@{ Path = $physical; AliasResolved = $true; SourceSha256 = $sourceHash; TargetSha256 = $targetHash }
}

function Resolve-NsisCompiler([string]$Source) {
  $path = ConvertTo-NsisCompilerPath $Source
  $bin = [IO.Path]::GetDirectoryName($path)
  if ([IO.Path]::GetFileName($path) -ine 'go.exe' -or [IO.Path]::GetFileName($bin) -ine 'bin') { throw 'nsis_control_compiler_layout_invalid' }
  $sdk = [IO.Path]::GetDirectoryName($bin)
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($sdk))
  Assert-NsisCompilerEntry $bin $true
  Assert-NsisCompilerEntry $path $false
  $attrs = Get-InstalledSmokeAttributes $sdk
  if ($null -eq $attrs -or -not ($attrs -band [IO.FileAttributes]::Directory)) { throw 'nsis_control_sdk_directory_required' }
  if ($attrs -band [IO.FileAttributes]::ReparsePoint) { return Resolve-NsisCompilerAlias $path $sdk }
  Assert-InstalledSmokeFile $path
  return [pscustomobject]@{ Path = $path; AliasResolved = $false; SourceSha256 = $null; TargetSha256 = $null }
}

function New-NsisAutorunPayload([string]$Root, $Environment, $Diagnostics = $null) {
  $stage = Join-Path $Root 'inert stage'
  foreach ($directory in @($stage, (Join-Path $stage 'integration'), (Join-Path $stage 'runtime'))) {
    [IO.Directory]::CreateDirectory($directory) | Out-Null
  }
  $source = Join-Path $Root 'autorun.go'
  $program = @'
package main

import "os"

func main() {
    if len(os.Args) != 1 { os.Exit(91) }
    marker := os.Getenv("ARKISM_NSIS_AUTORUN_MARKER")
    if marker == "" { os.Exit(92) }
    executable, err := os.Executable()
    if err != nil { os.Exit(93) }
    if os.WriteFile(marker, []byte(executable), 0600) != nil { os.Exit(94) }
}
'@
  [IO.File]::WriteAllText($source, $program, [Text.UTF8Encoding]::new($false))
  $buildEnv = @{} + $Environment
  $buildEnv['GO111MODULE'] = 'off'; $buildEnv['GOWORK'] = 'off'; $buildEnv['GOENV'] = 'off'
  $buildEnv['GOTOOLCHAIN'] = 'local'; $buildEnv['GOPROXY'] = 'off'; $buildEnv['GOSUMDB'] = 'off'
  $buildEnv['CGO_ENABLED'] = '0'; $buildEnv['GOOS'] = 'windows'; $buildEnv['GOARCH'] = 'amd64'
  $buildEnv['GOCACHE'] = Join-Path $Root 'go-cache'
  $buildEnv['GOPATH'] = Join-Path $Root 'go'
  $commands = @(Get-Command go.exe -CommandType Application -ErrorAction Stop)
  if ($null -ne $Diagnostics) { $Diagnostics.go_search = [ordered]@{ result_count = $commands.Count } }
  $selected = $commands | Select-Object -First 1
  if ($null -eq $selected) { throw 'nsis_control_go_not_found' }
  $go = $selected.Source
  if ($null -ne $Diagnostics) {
    $Diagnostics.go_search.selected_source_type = $go.GetType().FullName
    $Diagnostics.go_search.absolute_path_boolean = [IO.Path]::IsPathRooted($go)
    $Diagnostics.go_search.drive_prefix_count = ([regex]::Matches($go, '[A-Za-z]:[\\/]')).Count
  }
  $compiler = Resolve-NsisCompiler $go
  if ($null -ne $Diagnostics) {
    $Diagnostics.go_search.sdk_alias_resolved = $compiler.AliasResolved
    $Diagnostics.go_search.source_sha256 = $compiler.SourceSha256
    $Diagnostics.go_search.target_sha256 = $compiler.TargetSha256
  }
  $arguments = 'build -trimpath -o "' + (Join-Path $stage 'Arkism.exe') + '" "' + $source + '"'
  [void](Invoke-InstalledSmokeProcess $compiler.Path $arguments $buildEnv 120 'build_inert_control')
  # These are data, not agent/server executables. The product uninstaller is never invoked.
  [IO.File]::WriteAllText((Join-Path $stage 'integration/inert-client.txt'), 'inert client placeholder')
  [IO.File]::WriteAllText((Join-Path $stage 'runtime/inert-runtime.txt'), 'inert runtime placeholder')
  return $stage
}

function Get-NsisAutorunErrorOrigin($Failure) {
  return [ordered]@{
    exception_type = $Failure.Exception.GetType().FullName
    script_leaf = ($Failure.InvocationInfo.ScriptName -split '[\\/]')[-1]
    line_number = $Failure.InvocationInfo.ScriptLineNumber
  }
}

function Wait-NsisAutorunMarker([string]$Marker, [string]$InstallPath, [bool]$Expected) {
  if ($Expected) {
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ($null -eq (Get-InstalledSmokeAttributes $Marker) -and [DateTime]::UtcNow -lt $deadline) {
      Start-Sleep -Milliseconds 100
    }
    Assert-InstalledSmokeFile $Marker
    if (([IO.FileInfo]$Marker).Length -gt 32768) { throw 'nsis_control_marker_too_large' }
    $executable = ConvertTo-InstalledSmokePath ([IO.File]::ReadAllText($Marker))
    if ($executable -ine (Join-Path $InstallPath 'Arkism.exe')) { throw 'nsis_control_wrong_executable' }
  } else {
    Start-Sleep -Milliseconds 1000
    if ($null -ne (Get-InstalledSmokeAttributes $Marker)) { throw 'nsis_control_unexpected_autorun' }
  }
  $deadline = [DateTime]::UtcNow.AddSeconds(15)
  while (@(Get-InstalledSmokeProcesses $InstallPath).Count -and [DateTime]::UtcNow -lt $deadline) {
    Start-Sleep -Milliseconds 100
  }
  if (@(Get-InstalledSmokeProcesses $InstallPath).Count) { throw 'nsis_control_process_did_not_exit' }
}

function Invoke-NsisAutorunCase($Context, [string]$Root, [string]$Installer, [string]$InstallerHash,
                               [string]$PayloadHash, $Environment, [bool]$NoAutoRun) {
  $name = if ($NoAutoRun) { 'silent-no-autorun' } else { 'silent-default' }
  $install = Join-Path $Root $name
  $marker = Join-Path $Root "$name.marker"
  Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $Context)
  if ($null -ne (Get-InstalledSmokeAttributes $install)) { throw 'nsis_control_install_exists' }
  $childEnv = @{} + $Environment
  $childEnv['ARKISM_NSIS_AUTORUN_MARKER'] = $marker
  $arguments = if ($NoAutoRun) { "/S /NoAutoRun /D=$install" } else { "/S /D=$install" }
  $primary = $null
  try {
    [void](Invoke-InstalledSmokeProcess $Installer $arguments $childEnv 180 $name)
    if ((Get-InstalledSmokeHash $Installer) -cne $InstallerHash) { throw 'nsis_control_installer_changed' }
    if ((Get-InstalledSmokeHash (Join-Path $install 'Arkism.exe')) -cne $PayloadHash) { throw 'nsis_control_payload_changed' }
    Assert-InstalledSmokeRegistration (Get-InstalledSmokeGlobalState $Context) $install '0.0.0' -RequireComplete
    Wait-NsisAutorunMarker $marker $install (-not $NoAutoRun)
    return [ordered]@{ case = $name; exit_code = 0; marker_present = (-not $NoAutoRun); installed_payload_matches = $true }
  } catch {
    $primary = $_
    throw
  } finally {
    try {
      Remove-InstalledSmokeRegistration $Context $install '0.0.0'
      Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $Context)
    } catch {
      if ($null -ne $primary) { throw "NSIS control failed: $($primary.Exception.Message); registration cleanup failed: $($_.Exception.Message)" }
      throw
    }
  }
}

$context = Get-InstalledSmokeContext $SourceSha
$compiler = ConvertTo-InstalledSmokePath $Makensis
$evidence = ConvertTo-InstalledSmokePath $EvidencePath
Assert-InstalledSmokeFile $compiler
Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($evidence))
if (-not (Test-InstalledSmokeWithin $evidence $context.RunnerTemp) -or
    $null -ne (Get-InstalledSmokeAttributes $evidence)) { throw 'nsis_control_evidence_not_fresh_owned_path' }
Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
if (@(Get-InstalledSmokeProcesses '').Count) { throw 'nsis_control_preexisting_process' }
$root = New-InstalledSmokeOwnedRoot $context 'nsis-autorun'
$primary = $null; $cleanup = $null; $evidenceError = $null
$report = [ordered]@{ source_sha = $SourceSha; run_id = $context.RunId; kind = 'product-nsis-inert-control';
  shipping_payload_tested = $false; uninstaller_executed = $false; ok = $false; cleanup = 'not_started' }
try {
  $environment = New-InstalledSmokeEnvironment $root
  foreach ($path in @($environment['HOME'], $environment['APPDATA'], $environment['LOCALAPPDATA'],
                      $environment['XDG_CONFIG_HOME'], $environment['TEMP'])) {
    [IO.Directory]::CreateDirectory($path) | Out-Null
  }
  $report.phase = 'build_inert'
  $stage = New-NsisAutorunPayload $root $environment $report
  $report.phase = 'resolve_source'
  $source = Join-Path $PSScriptRoot 'arkism.nsi'
  $sourceHash = Get-InstalledSmokeHash $source
  $installer = Join-Path $root 'inert-control-setup.exe'
  $arguments = '/V2 /DVERSION=0.0.0 "/DSTAGE_DIR=' + $stage + '" "/DOUT_FILE=' + $installer + '" "' + $source + '"'
  $report.phase = 'compile_fixture'
  [void](Invoke-InstalledSmokeProcess $compiler $arguments $environment 120 'compile_product_nsis_inert_control')
  $report.phase = 'hash_fixture'
  $installerHash = Get-InstalledSmokeHash $installer
  $payloadHash = Get-InstalledSmokeHash (Join-Path $stage 'Arkism.exe')
  $report.nsis_source_sha256 = $sourceHash; $report.fixture_installer_sha256 = $installerHash
  $report.inert_payload_sha256 = $payloadHash
  $report.phase = 'default_case'
  $defaultCase = Invoke-NsisAutorunCase $context $root $installer $installerHash $payloadHash $environment $false
  $report.phase = 'noautorun_case'
  $noAutorunCase = Invoke-NsisAutorunCase $context $root $installer $installerHash $payloadHash $environment $true
  $report.cases = @($defaultCase, $noAutorunCase)
  $report.phase = 'verify_source'
  if ((Get-InstalledSmokeHash $source) -cne $sourceHash) { throw 'nsis_control_source_changed' }
  $report.ok = $true
  $report.phase = 'complete'
} catch {
  $primary = $_
  $report.primary_origin = Get-NsisAutorunErrorOrigin $_
} finally {
  try {
    if (@(Get-InstalledSmokeProcesses $root).Count) { throw 'nsis_control_process_still_running' }
    Remove-InstalledSmokeOwnedRoot $context $root
    Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
    $report.cleanup = 'owned_files_and_registrations_removed'
  } catch {
    $cleanup = $_
    $report.cleanup_origin = Get-NsisAutorunErrorOrigin $_
    $report.cleanup = 'failed'
  }
}
if ($null -ne $primary) { $report.ok = $false; $report.error = $primary.Exception.Message }
if ($null -ne $cleanup) { $report.ok = $false; $report.cleanup_error = $cleanup.Exception.Message }
try { Write-InstalledSmokeEvidence $evidence $report }
catch { $evidenceError = $_ }
Assert-InstalledSmokeOutcome $primary $cleanup $evidenceError
Write-Host 'NSIS inert controls passed: default silent launch retained; explicit no-autorun suppressed launch.'
