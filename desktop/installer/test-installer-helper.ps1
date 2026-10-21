# Native shared-macro controls only: never run a product installer/uninstaller.
param([Parameter(Mandatory)][string]$Makensis, [string]$TestRoot)
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT' -or $PSVersionTable.PSEdition -ne 'Desktop') { throw 'installer_control_windows_powershell_required' }
if (-not $TestRoot) { $TestRoot = Join-Path ([IO.Path]::GetTempPath()) ('arkism-helper-control-' + [guid]::NewGuid().ToString('N')) }
$root = [IO.Path]::GetFullPath($TestRoot)
if ($root -notmatch '^[A-Za-z]:\\[A-Za-z0-9 _\\.-]+$' -or (Test-Path -LiteralPath $root)) { throw 'installer_control_fresh_root_required' }
for ($parent = [IO.Path]::GetDirectoryName($root); $parent; $parent = [IO.Path]::GetDirectoryName($parent)) {
  if ((Test-Path -LiteralPath $parent) -and ((Get-Item -Force -LiteralPath $parent).Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'installer_control_reparse_root' }
}
$null = New-Item -ItemType Directory -Path $root
[IO.File]::WriteAllText((Join-Path $root 'TASK-OWNER.txt'), 'arkism-installer-helper-control')
Write-Output "Only inert helper control root: $root"
$macro = Join-Path $PSScriptRoot 'installer-helper.nsh'
$macroHash = (Get-FileHash -LiteralPath $macro -Algorithm SHA256).Hash
$peer = Join-Path $root 'inert-helper.exe'
$program = @'
using System;
using System.IO;
using System.Threading;
public static class InstallerControlPeer {
 public static int Main(string[] args) {
  string root=Environment.GetEnvironmentVariable("ARKISM_INSTALLER_CONTROL_ROOT");
  if(root==null || !File.Exists(Path.Combine(root,"CASE-OWNER"))) return 98;
  string mode=Environment.GetEnvironmentVariable("ARKISM_INSTALLER_CONTROL_MODE");
  if(args.Length<2 || args[args.Length-1]!=Path.Combine(root,"installed Arkism")) return 97;
  if(args[0]=="install") { if(args.Length!=3 || args[1]!="0.9.0") return 96; }
  else if(args[0]=="repair") { if(args.Length!=2) return 95; }
  else return 94;
  File.AppendAllText(Path.Combine(root,"events.txt"),args[0]+Environment.NewLine);
  if(args[0]=="repair") return mode=="repair_failure" ? 8:0;
  if(mode=="timeout") Thread.Sleep(Timeout.Infinite);
  if(mode=="committed_warning") Console.WriteLine("{\"Status\":\"committed_with_cleanup_warning\"}");
  return mode=="install_failure" || mode=="repair_failure" ? 7:0;
 }
}
'@
Add-Type -TypeDefinition $program -OutputAssembly $peer -OutputType ConsoleApplication
$results = @()
foreach ($mode in @('success','committed_warning','install_failure','repair_failure','timeout','missing')) {
  $case = Join-Path $root $mode
  $install = Join-Path $case 'installed Arkism'
  $null = New-Item -ItemType Directory -Path $install
  [IO.File]::WriteAllText((Join-Path $case 'CASE-OWNER'), 'arkism-helper-case')
  $sentinel = Join-Path $install 'payload-kept-on-failure.txt'
  [IO.File]::WriteAllText($sentinel, 'owned inert payload only')
  Write-Output "Only inert payload deletion target: $sentinel"
  $setup = Join-Path $case 'control.exe'; $source = Join-Path $case 'control.nsi'
  $script = @'
Unicode true
RequestExecutionLevel user
SilentInstall silent
Name "Inert installer helper control"
OutFile "@OUTPUT@"
InstallDir "@INSTALL@"
!define ARKISM_INSTALLER_HELPER_TIMEOUT_MS 1500
!include "@MACRO@"
Section
 InitPluginsDir
 SetOutPath "$PLUGINSDIR"
 File /oname=arkism-installer.exe "@PEER@"
 @MISSING@
 !insertmacro ArkismRunInstallerHelper 'install "0.9.0"' install_failed
 Delete "$INSTDIR\payload-kept-on-failure.txt"
 SetErrorLevel 0
 Goto complete
install_failed:
 !insertmacro ArkismRunInstallerHelper repair recovery_failed
 SetErrorLevel 41
 Goto complete
recovery_failed:
 SetErrorLevel 42
complete:
SectionEnd
'@
  $missing = if ($mode -eq 'missing') { 'Delete "$PLUGINSDIR\arkism-installer.exe"' } else { '' }
  $script = $script.Replace('@OUTPUT@',$setup).Replace('@INSTALL@',$install).Replace('@MACRO@',$macro).Replace('@PEER@',$peer).Replace('@MISSING@',$missing)
  [IO.File]::WriteAllText($source,$script,[Text.UTF8Encoding]::new($false))
  & $Makensis /V2 $source
  if ($LASTEXITCODE -ne 0) { throw 'installer_control_compile_failed' }
  $env:ARKISM_INSTALLER_CONTROL_ROOT=$case; $env:ARKISM_INSTALLER_CONTROL_MODE=$mode
  $process=Start-Process -FilePath $setup -ArgumentList '/S' -PassThru -WindowStyle Hidden
  if (-not $process.WaitForExit(20000)) { throw 'installer_control_unbounded_wait' }
  $process.Refresh()
  $success=$mode -in @('success','committed_warning')
  $expected = if ($success) {0} elseif ($mode -in @('repair_failure','missing')) {42} else {41}
  if ($process.ExitCode -ne $expected -or (Test-Path -LiteralPath $sentinel) -eq $success) { throw "installer_control_wrong_outcome: $mode" }
  $events = if ($mode -eq 'missing') {@()} else {@(Get-Content -LiteralPath (Join-Path $case 'events.txt'))}
  $sequence=$events -join ','
  $expectedSequence = if ($mode -eq 'missing') {''} elseif ($success) {'install'} else {'install,repair'}
  if ($sequence -cne $expectedSequence) { throw "installer_control_wrong_dispatch: $mode" }
  $results += [ordered]@{mode=$mode;exit_code=$process.ExitCode;sequence=$sequence;payload_preserved=(-not $success)}
}
if ((Get-FileHash -LiteralPath $macro -Algorithm SHA256).Hash -cne $macroHash) { throw 'installer_control_macro_changed' }
$left=@(Get-CimInstance Win32_Process | Where-Object {$_.ExecutablePath -and $_.ExecutablePath.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase)})
if ($left.Count) { throw 'installer_control_owned_peer_remaining' }
[ordered]@{ok=$true;cases=$results;product_install_executed=$false;product_uninstall_executed=$false;timeout_fixture_ms=1500;macro_sha256=$macroHash.ToLowerInvariant()} |
  ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $root 'evidence.json') -Encoding UTF8
Write-Output 'All 6 shared installer-helper controls passed; only inert files used.'
