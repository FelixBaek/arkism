# Compile the product's shared NSIS cleanup macro with an inert peer only.
# Never execute the product installer/uninstaller or use real startup keys.
param([Parameter(Mandatory)][string]$Makensis, [string]$TestRoot)
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'autostart_control_windows_required' }
if ($PSVersionTable.PSEdition -ne 'Desktop') { throw 'autostart_control_windows_powershell_required' }
if (-not $TestRoot) { $TestRoot = Join-Path ([IO.Path]::GetTempPath()) ('arkism-autostart-control-' + [guid]::NewGuid().ToString('N')) }
$root = [IO.Path]::GetFullPath($TestRoot)
if ($root -notmatch '^[A-Za-z]:\\[A-Za-z0-9 _\\.-]+$' -or (Test-Path -LiteralPath $root)) { throw 'autostart_control_fresh_literal_root_required' }
for ($ancestor = [IO.Path]::GetDirectoryName($root); $ancestor; $ancestor = [IO.Path]::GetDirectoryName($ancestor)) {
  if (Test-Path -LiteralPath $ancestor) {
    if ((Get-Item -Force -LiteralPath $ancestor).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'autostart_control_reparse_root' }
  }
}
$null = New-Item -ItemType Directory -Path $root
[IO.File]::WriteAllText((Join-Path $root 'TASK-OWNER.txt'), 'arkism-autostart-control')
Write-Output "Owned inert control root: $root"
$macro = Join-Path $PSScriptRoot 'remove-autostart.nsh'
$macroHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $macro).Hash
$peer = Join-Path $root 'inert-peer.exe'
$program = @'
using System;
using System.IO;
using System.Threading;
public static class AutostartControlPeer {
  public static int Main(string[] args) {
    if(args.Length!=1 || args[0]!="--remove-autostart") return 99;
    string marker=Environment.GetEnvironmentVariable("ARKISM_AUTOSTART_CONTROL_MARKER");
    string root=Environment.GetEnvironmentVariable("ARKISM_AUTOSTART_CONTROL_ROOT");
    if(root==null || marker==null || !Path.GetFullPath(marker).StartsWith(root+"\\",StringComparison.OrdinalIgnoreCase)) return 98;
    File.WriteAllText(marker,"--remove-autostart");
    string mode=Environment.GetEnvironmentVariable("ARKISM_AUTOSTART_CONTROL_MODE");
    if(mode=="hang") Thread.Sleep(Timeout.Infinite);
    return mode=="failure" ? 7 : 0;
  }
}
'@
Add-Type -TypeDefinition $program -OutputAssembly $peer -OutputType ConsoleApplication
$results = @()
foreach ($mode in @('success', 'failure', 'missing', 'hang')) {
  $caseRoot = Join-Path $root $mode
  $install = Join-Path $caseRoot 'installed Arkism'
  $null = New-Item -ItemType Directory -Path $install
  $sentinel = Join-Path $install 'keep-if-failed.txt'
  [IO.File]::WriteAllText($sentinel, 'owned inert fixture data')
  Write-Output "Only inert deletion target: $sentinel"
  if ($mode -ne 'missing') { Copy-Item -LiteralPath $peer -Destination (Join-Path $install 'Arkism.exe') }
  $setup = Join-Path $caseRoot 'autostart-control.exe'
  $source = Join-Path $caseRoot 'autostart-control.nsi'
  $script = @'
Unicode true
RequestExecutionLevel user
SilentInstall silent
Name "Inert autostart cleanup control"
OutFile "@OUTPUT@"
InstallDir "@INSTALL@"
!include "@MACRO@"
Section
  !insertmacro ArkismRemoveAutostart "$INSTDIR\Arkism.exe" control_failed
  Delete "$INSTDIR\keep-if-failed.txt"
  SetErrorLevel 0
  Goto control_complete
control_failed:
  SetErrorLevel 41
control_complete:
SectionEnd
'@
  $script = $script.Replace('@OUTPUT@', $setup).Replace('@INSTALL@', $install).Replace('@MACRO@', $macro)
  [IO.File]::WriteAllText($source, $script, [Text.UTF8Encoding]::new($false))
  & $Makensis /V2 $source
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $setup)) { throw 'autostart_control_compile_failed' }
  $marker = Join-Path $caseRoot 'called.txt'
  $env:ARKISM_AUTOSTART_CONTROL_ROOT = $caseRoot
  $env:ARKISM_AUTOSTART_CONTROL_MARKER = $marker
  $env:ARKISM_AUTOSTART_CONTROL_MODE = $mode
  $process = Start-Process -FilePath $setup -ArgumentList '/S' -PassThru
  if (-not $process.WaitForExit(35000)) { throw 'autostart_control_timeout' }
  $process.Refresh()
  $expected = if ($mode -eq 'success') { 0 } else { 41 }
  if ($process.ExitCode -ne $expected) { throw "autostart_control_wrong_exit: $mode" }
  if ((Test-Path -LiteralPath $sentinel) -ne ($mode -ne 'success')) { throw 'autostart_control_payload_changed_after_failure' }
  if ($mode -eq 'missing') {
    if (Test-Path -LiteralPath $marker) { throw 'autostart_control_missing_peer_called' }
  } elseif ([IO.File]::ReadAllText($marker) -cne '--remove-autostart') { throw 'autostart_control_wrong_arguments' }
  $results += [ordered]@{ mode = $mode; exit_code = $process.ExitCode; payload_retained = (Test-Path -LiteralPath $sentinel) }
}
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $macro).Hash -cne $macroHash) { throw 'autostart_control_source_changed' }
$left = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase) })
if ($left.Count) { throw 'autostart_control_owned_peer_still_running' }
[ordered]@{ ok = $true; cases = $results; macro_sha256 = $macroHash.ToLowerInvariant(); product_effects = $false } |
  ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $root 'evidence.json') -Encoding UTF8
Write-Output 'All 4 native NSIS autostart controls passed; no product effects executed.'
