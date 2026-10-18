# Hosted-Windows inert fixtures only. This never invokes the product uninstaller.
param([string]$Makensis, [string]$SourceSha, [string]$EvidencePath)

function New-JobFixturePayload([string]$Root) {
  $program = @'
using System;
using System.Diagnostics;
using System.IO;
using System.Threading;

public static class InertJobProcess
{
    public static int Main()
    {
        string mode = Environment.GetEnvironmentVariable("ARKISM_JOB_MODE");
        string marker = Environment.GetEnvironmentVariable("ARKISM_JOB_MARKER");
        if (mode == "child" || mode == "hang-parent") {
            var start = new ProcessStartInfo(Process.GetCurrentProcess().MainModule.FileName);
            start.UseShellExecute = false; start.CreateNoWindow = true;
            start.EnvironmentVariables["ARKISM_JOB_MODE"] = mode == "child" ? "grandchild" : "hang";
            using (var child = Process.Start(start)) { if (child == null) return 91; }
            return 0;
        }
        if (mode == "grandchild") { Thread.Sleep(800); File.WriteAllText(marker, "grandchild_done"); return 0; }
        if (mode == "hang") { File.WriteAllText(marker, "hang_started"); Thread.Sleep(Timeout.Infinite); return 92; }
        if (mode == "control") {
            File.WriteAllText(marker, "control_started");
            var clock = Stopwatch.StartNew();
            string release = Environment.GetEnvironmentVariable("ARKISM_JOB_RELEASE");
            while (!File.Exists(release) && clock.ElapsedMilliseconds < 30000) Thread.Sleep(20);
            return File.Exists(release) ? 0 : 93;
        }
        return 94;
    }
}
'@
  $exe = Join-Path $Root 'inert-process.exe'
  Add-Type -TypeDefinition $program -OutputAssembly $exe -OutputType ConsoleApplication
  Assert-InstalledSmokeFile $exe
  return $exe
}

function New-JobFixtureInstaller([string]$Root, [string]$Compiler, $Environment) {
  if ($Root -notmatch '^[A-Za-z]:\\[A-Za-z0-9\\ _.-]+$') { throw 'job_fixture_nsis_path_unsupported' }
  $payload = New-JobFixturePayload $Root
  $setup = Join-Path $Root 'inert-setup.exe'
  $source = Join-Path $Root 'inert-uninstall.nsi'
  # Only this minimal generated script is compiled. No product sections are included.
  $program = @'
Unicode true
RequestExecutionLevel user
Name "Inert process completion fixture"
OutFile "@SETUP@"
InstallDir "@INSTALL@"
SilentInstall silent
SilentUnInstall silent
Section
  SetOutPath "$INSTDIR"
  File "@PAYLOAD@"
  WriteUninstaller "$INSTDIR\fixture-uninstall.exe"
SectionEnd
Section "Uninstall"
  FileOpen $0 "$INSTDIR\copy-path.txt" w
  FileWrite $0 "$EXEPATH"
  FileClose $0
  Exec '"$INSTDIR\inert-process.exe"'
  Delete "$INSTDIR\fixture-uninstall.exe"
SectionEnd
'@
  $install = Join-Path $Root 'installed'
  $program = $program.Replace('@SETUP@', $setup).Replace('@INSTALL@', $install).Replace('@PAYLOAD@', $payload)
  [IO.File]::WriteAllText($source, $program, [Text.UTF8Encoding]::new($false))
  [void](Invoke-InstalledSmokeProcess $Compiler ('/V2 "' + $source + '"') $Environment 120 'compile_inert_job')
  $setupHash = Get-InstalledSmokeHash $setup
  $payloadHash = Get-InstalledSmokeHash $payload
  [void](Invoke-InstalledSmokeProcess $setup ("/S /D=$install") $Environment 60 'install_inert_job')
  if ((Get-InstalledSmokeHash $setup) -cne $setupHash) { throw 'job_fixture_installer_changed' }
  if ((Get-InstalledSmokeHash (Join-Path $install 'inert-process.exe')) -cne $payloadHash) { throw 'job_fixture_payload_changed' }
  return [ordered]@{ Executable = Join-Path $install 'fixture-uninstall.exe'; Install = $install
    InstallerSha256 = $setupHash; ScriptSha256 = Get-InstalledSmokeHash $source; PayloadSha256 = $payloadHash
    UninstallerSha256 = Get-InstalledSmokeHash (Join-Path $install 'fixture-uninstall.exe') }
}

function New-JobFixtureRequest($Context, [string]$Root, [string]$Executable, [string]$Marker, [string]$ExpectedHash) {
  $request = New-Object Arkism.InstallerChecks.JobRequest
  $request.Root = $Root; $request.Owner = $Context.OwnedRoots[$Root]; $request.SourceSha = $Context.SourceSha
  $request.Executable = $Executable; $request.ExpectedSha256 = $ExpectedHash
  $request.Marker = $Marker
  return $request
}

function Find-JobFixtureFailure($Failure) {
  $jobException = $Failure.Exception
  while ($null -ne $jobException) {
    if ($jobException -is [Arkism.InstallerChecks.JobFailure]) { return $jobException }
    $jobException = $jobException.InnerException
  }
  return $null
}

function Assert-JobFixtureMarker([string]$Path, [string]$Expected) {
  Assert-InstalledSmokeFile $Path
  if (([IO.FileInfo]$Path).Length -gt 1024 -or [IO.File]::ReadAllText($Path) -cne $Expected) { throw 'job_fixture_marker_invalid' }
}

function Get-JobFixtureCopy([string]$Root, [string]$Install, [string]$OriginalHash) {
  $pathFile = Join-Path $Install 'copy-path.txt'
  Assert-InstalledSmokeFile $pathFile
  if (([IO.FileInfo]$pathFile).Length -gt 32768) { throw 'job_fixture_copy_path_oversized' }
  $copy = ConvertTo-InstalledSmokePath ([IO.File]::ReadAllText($pathFile))
  if (-not (Test-InstalledSmokeWithin $copy (Join-Path $Root 'temp')) -or [IO.Path]::GetFileName($copy) -ine 'Un.exe') {
    throw 'job_fixture_copy_not_owned'
  }
  Assert-InstalledSmokeFile $copy
  $hash = Get-InstalledSmokeHash $copy
  if ($hash -cne $OriginalHash) { throw 'job_fixture_copy_mismatch' }
  return [pscustomobject]@{ Path = $copy; Directory = [IO.Path]::GetDirectoryName($copy); Sha256 = $hash }
}

function ConvertTo-JobEchoLiteral([string]$Value) {
  return '"' + $Value.Replace('\', '\\').Replace('"', '\"') + '"'
}

function Get-JobEchoSource($Context, [string]$Root, $Copy, [int]$PointerBytes) {
  $core = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'WindowsProcessJob.cs'))
  if (([regex]::Matches($core, 'namespace Arkism\.InstallerChecks')).Count -ne 1) { throw 'job_echo_namespace_invalid' }
  $isolated = $core.Replace('namespace Arkism.InstallerChecks', 'namespace Arkism.EchoFixture')
  if ($isolated.Replace('namespace Arkism.EchoFixture', 'namespace Arkism.InstallerChecks') -cne $core) { throw 'job_echo_source_changed' }
  $entry = @'
namespace Arkism.EchoFixture {
  public static class FixedEchoEntry {
    public static int Main(string[] args) {
      var request = new JobRequest { Root = @ROOT@, Owner = @OWNER@, SourceSha = @SOURCE@,
        Executable = @FILE@, ExpectedSha256 = @HASH@ };
      return BoundEchoLeaf.MainBound(args, request, @DIRECTORY@, @POINTER@);
    }
  }
}
'@
  $constants = @{ ROOT = $Root; OWNER = $Context.OwnedRoots[$Root]; SOURCE = $Context.SourceSha
    FILE = $Copy.Path; HASH = $Copy.Sha256; DIRECTORY = $Copy.Directory }
  foreach ($name in $constants.Keys) { $entry = $entry.Replace(('@' + $name + '@'), (ConvertTo-JobEchoLiteral $constants[$name])) }
  $entry = $entry.Replace('@POINTER@', [string]$PointerBytes)
  return $isolated + "`n" + $entry
}

function New-JobEchoExecutable($Context, [string]$Root, $Copy, [int]$Machine) {
  $pointer = [Arkism.InstallerChecks.PendingEchoVerifier]::PointerBytes($Machine)
  $platform = if ($pointer -eq 4) { 'x86' } else { 'x64' }
  $exe = Join-Path $Root 'pending-echo.exe'
  if ($null -ne (Get-InstalledSmokeAttributes $exe)) { throw 'job_echo_image_exists' }
  $options = New-Object CodeDom.Compiler.CompilerParameters
  $options.GenerateExecutable = $true; $options.GenerateInMemory = $false
  $options.OutputAssembly = $exe
  $options.WarningLevel = 4
  $options.CompilerOptions = "/platform:$platform /langversion:5"
  [void]$options.ReferencedAssemblies.Add('System.dll')
  # Disk output only. Do not access CompiledAssembly, which would load the x86 leaf.
  $provider = New-Object Microsoft.CSharp.CSharpCodeProvider
  try {
    $compiled = $provider.CompileAssemblyFromSource($options, [string[]]@(Get-JobEchoSource $Context $Root $Copy $pointer))
    if ($compiled.NativeCompilerReturnValue -ne 0 -or $compiled.Errors.HasErrors -or $compiled.Errors.Count -ne 0) { throw 'job_echo_compile_failed' }
  } finally { $provider.Dispose() }
  if ([Arkism.InstallerChecks.PendingEchoVerifier]::ReadMachine($exe) -ne $Machine) { throw 'job_echo_compiler_machine_mismatch' }
  return [pscustomobject]@{ Path = $exe; Sha256 = Get-InstalledSmokeHash $exe; PointerBytes = $pointer }
}

function Read-JobEchoResult([string]$Path) {
  Assert-InstalledSmokeFile $Path
  if (([IO.FileInfo]$Path).Length -gt 1024) { throw 'job_echo_result_oversized' }
  $value = [IO.File]::ReadAllText($Path) | ConvertFrom-Json -ErrorAction Stop
  $names = @($value.PSObject.Properties.Name | Sort-Object)
  if (($names -join ',') -cne 'api_calls,code,ok,pointer_bytes,win32_error') { throw 'job_echo_result_schema' }
  if ($value.ok -isnot [bool] -or $value.code -isnot [string]) { throw 'job_echo_result_schema' }
  if ($value.code -cne 'ok' -and $value.code -cnotmatch '\A(job_|leaf_)[a-z0-9_:]{1,90}\z') { throw 'job_echo_result_schema' }
  foreach ($name in @('api_calls', 'pointer_bytes', 'win32_error')) {
    if (-not (Test-InstalledSmokeInteger $value.$name)) { throw 'job_echo_result_schema' }
  }
  return $value
}

function Invoke-JobEchoLeaf($Context, [string]$Root, $Copy, $Leaf, $Environment, [string]$Kind, $Report) {
  if ($Kind -cnotin @('directory', 'file')) { throw 'job_echo_kind_invalid' }
  Assert-InstalledSmokeOwnedRoot $Context $Root
  Assert-InstalledSmokeDirectory $Copy.Directory
  if ((Get-InstalledSmokeHash $Copy.Path) -cne $Copy.Sha256 -or (Get-InstalledSmokeHash $Leaf.Path) -cne $Leaf.Sha256) { throw 'job_echo_input_changed' }
  $resultPath = Join-Path $Root "echo-$Kind.json"
  if ($null -ne (Get-InstalledSmokeAttributes $resultPath)) { throw 'job_echo_result_exists' }
  $child = @{} + $Environment
  $child['GITHUB_ACTIONS'] = 'true'; $child['RUNNER_ENVIRONMENT'] = 'github-hosted'
  $child['GITHUB_SHA'] = $Context.SourceSha; $child['RUNNER_TEMP'] = $Context.RunnerTemp
  $processError = $null; $readError = $null; $value = $null
  try { [void](Invoke-InstalledSmokeProcess $Leaf.Path $Kind $child 30 'pending_exact_echo') } catch { $processError = $_ }
  try { $value = Read-JobEchoResult $resultPath } catch { $readError = $_ }
  if ($value) { $Report.echo_last_result = $value }
  if ($processError -and $readError) { throw ($processError.Exception.Message + '; leaf_result: ' + $readError.Exception.Message) }
  if ($processError) { throw $processError }
  if ($readError) { throw $readError }
  [Arkism.InstallerChecks.PendingEchoVerifier]::ValidateResult($Leaf.PointerBytes, $value.pointer_bytes, $value.ok, $value.api_calls, $value.win32_error, 0)
  if ($value.code -cne 'ok' -or (Get-InstalledSmokeHash $Leaf.Path) -cne $Leaf.Sha256) { throw 'job_echo_output_changed' }
  # The fixed leaf has no child/process-launch path; its retained Process exit is the boundary.
}

function Invoke-JobExactEcho($Context, [string]$Root, $Copy, [int]$Machine, $Request, $Before, $After, $Environment, $Report) {
  $Report.nsis_observed_delta = $After.EntryCount - $Before.EntryCount
  $rows = [Arkism.InstallerChecks.PendingEchoVerifier]::Capture($Before, $After)
  $Report.echo_phase = 'compile'
  $leaf = New-JobEchoExecutable $Context $Root $Copy $Machine
  $Report.echo_image_sha256 = $leaf.Sha256; $Report.echo_pointer_bytes = $leaf.PointerBytes
  $previous = $After; $index = 0
  foreach ($kind in @('directory', 'file')) {
    $Report.phase = "echo_$kind"
    $now = Get-JobEchoSnapshot $Request
    [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($previous, $now)
    $Report.echo_phase = $kind + '_call'
    Invoke-JobEchoLeaf $Context $Root $Copy $leaf $Environment $kind $Report
    $next = Get-JobEchoSnapshot $Request
    $Report.echo_phase = $kind + '_match'
    $Report.echo_last_observed_delta = $next.EntryCount - $now.EntryCount
    [Arkism.InstallerChecks.PendingEchoVerifier]::VerifyEcho($rows, $index, $now, $next)
    $previous = $next; $index++
    $Report.echo_verified_calls = $index
  }
  $Report.echo_phase = 'complete'
  return $previous
}

function Get-JobEchoSnapshot($Request) { return [Arkism.InstallerChecks.RebootObservation]::ReadWindows($Request) }

function Get-JobCompletedCopy($Request, $Result, [string]$Root, $Fixture, $Report, [int]$Machine) {
  $Report.owned_job_assignment_verified = $true
  $Report.root_exited_while_tree_active = $Result.RootExitedWhileActive
  $Report.job_empty = $Result.EmptyConfirmed; $Report.total_processes = $Result.TotalProcesses
  if (-not $Result.RootExitedWhileActive) { throw 'job_fixture_root_early_exit_not_observed' }
  Assert-JobFixtureMarker $Request.Marker 'grandchild_done'
  $Report.completion_marker_observed = $true
  if ($null -ne (Get-InstalledSmokeAttributes $Fixture.Executable)) { throw 'job_fixture_original_not_removed' }
  $Report.original_uninstaller_removed = $true
  $copy = Get-JobFixtureCopy $Root $Fixture.Install $Request.ExpectedSha256
  $Report.observed_owned_copy_sha256 = $copy.Sha256
  if ([Arkism.InstallerChecks.PendingEchoVerifier]::ReadNativeMachine($copy.Path) -ne $Machine) { throw 'job_echo_copy_machine_mismatch' }
  return $copy
}

function Set-JobFixtureRetention($Context, [string]$Root, $Report, $Primary, [bool]$Settled) {
  Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $Context)
  Assert-InstalledSmokeOwnedRoot $Context $Root
  if ($Primary) { $Report.cleanup = 'owned_root_retained_after_failure'; return }
  if (-not $Settled) { throw 'job_fixture_root_retained_for_vm_teardown' }
  # Intentionally leave this tiny root and all FOUR reservations; never cancel the queue.
  $Report.cleanup = 'owned_root_and_four_reservations_retained_for_vm_teardown'
}

function Start-JobFixtureControl([string]$Executable, $Environment, [string]$Marker, [string]$Release, [string]$ExpectedHash) {
  $start = New-Object Diagnostics.ProcessStartInfo
  $start.FileName = $Executable; $start.UseShellExecute = $false; $start.CreateNoWindow = $true
  $start.WorkingDirectory = [IO.Path]::GetDirectoryName($Executable)
  $start.EnvironmentVariables.Clear()
  foreach ($name in $Environment.Keys) { $start.EnvironmentVariables[$name] = $Environment[$name] }
  $start.EnvironmentVariables['ARKISM_JOB_MODE'] = 'control'
  $start.EnvironmentVariables['ARKISM_JOB_MARKER'] = $Marker
  $start.EnvironmentVariables['ARKISM_JOB_RELEASE'] = $Release
  Assert-InstalledSmokeFile $Executable
  if ((Get-InstalledSmokeHash $Executable) -cne $ExpectedHash) { throw 'job_fixture_control_image_changed' }
  return [Diagnostics.Process]::Start($start)
}

function Stop-JobFixtureControl($Process, [string]$Release) {
  if ($null -eq $Process) { return }
  try {
    [IO.File]::WriteAllText($Release, 'release')
    if (-not $Process.WaitForExit(5000)) {
      $Process.Kill()
      if (-not $Process.WaitForExit(5000)) { throw 'job_fixture_control_not_reaped' }
      throw 'job_fixture_control_needed_termination'
    }
    if ($Process.ExitCode -ne 0) { throw 'job_fixture_control_exit_failed' }
  } finally { $Process.Dispose() }
}

function Invoke-JobFixtureTimeout($Context, [string]$Root, [string]$Executable, $Environment, [string]$ExpectedHash) {
  $marker = Join-Path $Root 'hang.marker'
  $controlMarker = Join-Path $Root 'control.marker'
  $release = Join-Path $Root 'control.release'
  $control = $null; $primary = $null
  try {
    Assert-InstalledSmokeOwnedRoot $Context $Root
    $control = Start-JobFixtureControl $Executable $Environment $controlMarker $release $ExpectedHash
    $request = New-JobFixtureRequest $Context $Root $Executable $marker $ExpectedHash
    $request.Arguments = ''; $request.Mode = 'hang-parent'; $request.TimeoutMilliseconds = 3000
    $failure = $null
    try { [void][Arkism.InstallerChecks.WindowsProcessJob]::RunWindows($request) }
    catch { $failure = Find-JobFixtureFailure $_; if ($null -eq $failure) { throw } }
    if ($null -eq $failure -or -not $failure.Result.EmptyConfirmed -or
        $failure.Result.Errors.Count -ne 1 -or $failure.Result.Errors[0] -cne 'primary:job_timeout') { throw 'job_fixture_timeout_not_confirmed' }
    Assert-JobFixtureMarker $marker 'hang_started'
    Assert-JobFixtureMarker $controlMarker 'control_started'
    if ($control.HasExited) { throw 'job_fixture_unrelated_control_terminated' }
    return [ordered]@{ timed_out = $true; own_job_empty = $true; separate_control_alive = $true
      total_processes = $failure.Result.TotalProcesses }
  } catch { $primary = $_; throw }
  finally {
    try { Stop-JobFixtureControl $control $release }
    catch {
      if ($null -ne $primary) { throw ($primary.Exception.Message + '; control_cleanup: ' + $_.Exception.Message) }
      throw
    }
  }
}

function Invoke-NsisUninstallJob([string]$Makensis, [string]$SourceSha, [string]$EvidencePath) {
  $ErrorActionPreference = 'Stop'
  # The shared script has its own param block; preserve caller inputs across dot-source.
  $jobCompiler = $Makensis; $jobSource = $SourceSha; $jobEvidence = $EvidencePath
  . (Join-Path $PSScriptRoot 'smoke-windows-installed.ps1')
  $context = Get-InstalledSmokeContext $jobSource
  $jobEvidence = ConvertTo-InstalledSmokePath $jobEvidence
  if (-not (Test-InstalledSmokeWithin $jobEvidence $context.RunnerTemp)) { throw 'job_fixture_evidence_not_owned' }
  Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
  Assert-InstalledSmokeFile $jobCompiler
  Assert-InstalledSmokeDirectory ([IO.Path]::GetDirectoryName($jobEvidence))
  if ($null -ne (Get-InstalledSmokeAttributes $jobEvidence)) { throw 'job_fixture_evidence_exists' }
  Add-Type -Path (Join-Path $PSScriptRoot 'WindowsProcessJob.cs')
  $sizes = [Arkism.InstallerChecks.WindowsProcessJob]::LayoutSizes()
  Assert-JobFixtureLayout $sizes
  $root = New-InstalledSmokeOwnedRoot $context 'process-job'
  $report = [ordered]@{ ok = $false; source_sha = $jobSource; product_uninstaller_tested = $false
    every_child_image_verified = $false; phase = 'prepare'; owned_job_assignment_verified = $false
    runner_sha256 = Get-InstalledSmokeHash (Join-Path $PSScriptRoot 'WindowsProcessJob.cs')
    compiler_sha256 = Get-InstalledSmokeHash $jobCompiler }
  $primary = $null; $cleanup = $null; $evidenceError = $null
  $safeToRemove = $true; $pendingSafe = $false
  try {
    foreach ($relative in @('temp', 'profile', 'profile/AppData/Roaming', 'profile/AppData/Local')) {
      [IO.Directory]::CreateDirectory((Join-Path $root $relative)) | Out-Null
    }
    $environment = New-InstalledSmokeEnvironment $root
    $fixture = New-JobFixtureInstaller $root $jobCompiler $environment
    $marker = Join-Path $root 'grandchild.marker'
    $request = New-JobFixtureRequest $context $root $fixture.Executable $marker $fixture.UninstallerSha256
    $report.installer_sha256 = $fixture.InstallerSha256; $report.fixture_script_sha256 = $fixture.ScriptSha256
    $report.installed_uninstaller_sha256 = $request.ExpectedSha256
    $machine = [Arkism.InstallerChecks.PendingEchoVerifier]::ReadNativeMachine($fixture.Executable)
    $before = [Arkism.InstallerChecks.RebootObservation]::ReadWindows($request)
    $report.phase = 'default_self_copy'; $safeToRemove = $false
    try { $result = [Arkism.InstallerChecks.WindowsProcessJob]::RunWindows($request) }
    catch {
      $jobFailure = Find-JobFixtureFailure $_
      if ($null -ne $jobFailure) {
        $safeToRemove = $jobFailure.Result.EmptyConfirmed -or $jobFailure.Result.RootReaped
        $report.job_errors = @($jobFailure.Result.Errors)
      }
      throw
    }
    $safeToRemove = $result.EmptyConfirmed
    $copy = Get-JobCompletedCopy $request $result $root $fixture $report $machine
    $report.phase = 'pending_reboot_observation'
    $after = [Arkism.InstallerChecks.RebootObservation]::ReadWindows($request)
    # This inert-only echo adds TWO global reservations; it does not teach the generic parser a namespace.
    $finalEcho = Invoke-JobExactEcho $context $root $copy $machine $request $before $after $environment $report
    $pendingSafe = $true
    $report.same_run_exact_api_pair_correspondence = $true
    $report.pending_existing_rows_preserved = $true; $report.pending_raw_unchanged = $false
    $report.nsis_pending_rows = 2; $report.api_echo_added_reservations = 2; $report.total_new_pending_rows = 4
    $report.generic_opaque_namespace_supported = $false; $report.boot_deletion_verified = $false
    $report.legacy_reboot_unchanged = $true
    $report.phase = 'timeout_tree'; $safeToRemove = $false
    $report.timeout = Invoke-JobFixtureTimeout $context $root (Join-Path $root 'inert-process.exe') $environment $fixture.PayloadSha256
    $safeToRemove = $true
    $final = Get-JobEchoSnapshot $request
    [Arkism.InstallerChecks.PendingEchoVerifier]::AssertUnchanged($finalEcho, $final)
    $report.queue_unchanged_after_timeout_control = $true
    Assert-InstalledSmokeFreshState (Get-InstalledSmokeGlobalState $context)
    $report.ok = $true; $report.phase = 'complete'
  } catch {
    $primary = $_; $report.error = 'job_fixture_failed'
    $report.error_type = $_.Exception.GetType().FullName
  } finally {
    try {
      Set-JobFixtureRetention $context $root $report $primary ($safeToRemove -and $pendingSafe)
    } catch { $cleanup = $_; $report.cleanup = 'owned_root_retained'; $report.cleanup_error = 'job_fixture_cleanup_failed' }
  }
  try { Write-InstalledSmokeEvidence $jobEvidence $report } catch { $evidenceError = $_ }
  Assert-InstalledSmokeOutcome $primary $cleanup $evidenceError
  if (-not $report.ok) { throw 'job_fixture_not_successful' }
}

function Assert-JobFixtureLayout($Sizes) {
  $expected = @{ pointer = 8; basic = 64; extended = 144; accounting = 48; startup = 104; process = 24
    basic_affinity = 48; startup_flags = 60; accounting_active = 40 }
  foreach ($name in $expected.Keys) {
    if ($Sizes[$name] -ne $expected[$name]) { throw 'job_fixture_abi_unsupported' }
  }
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-NsisUninstallJob $Makensis $SourceSha $EvidencePath }
