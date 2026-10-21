# Runs only inert executables in a disposable hosted Windows job.
param([Parameter(Mandatory)][string]$Makensis)

$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -cne 'true' -or $env:RUNNER_OS -cne 'Windows' -or
    $env:RUNNER_ENVIRONMENT -cne 'github-hosted' -or $env:GITHUB_SHA -cnotmatch '\A[0-9a-f]{40}\z') {
  throw 'update_identity_hosted_job_required'
}
$source = git rev-parse HEAD
if ($LASTEXITCODE -ne 0 -or $source -cne $env:GITHUB_SHA) { throw 'update_identity_source_mismatch' }
if (-not [IO.Path]::IsPathRooted($env:RUNNER_TEMP) -or
    -not (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container)) { throw 'update_identity_temp_missing' }
if ($env:RUNNER_TEMP.IndexOfAny([char[]]@(0, 10, 13, 34, 36)) -ge 0) { throw 'update_identity_temp_syntax_unsupported' }
if (-not (Test-Path -LiteralPath $Makensis -PathType Leaf)) { throw 'update_identity_compiler_missing' }

$suffix = [guid]::NewGuid().ToString('N')
$root = Join-Path $env:RUNNER_TEMP ('arkism-inert-installer-' + $suffix)
$null = New-Item -ItemType Directory -Path $root -ErrorAction Stop
$evidence = Join-Path $env:RUNNER_TEMP ('arkism-update-identity-' + $suffix + '.json')
if ($evidence.Contains("`r") -or $evidence.Contains("`n")) { throw 'update_identity_evidence_path_invalid' }
"ARKISM_UPDATE_IDENTITY_EVIDENCE=$evidence" | Out-File -FilePath $env:GITHUB_ENV -Encoding utf8 -Append

$payload = Join-Path $root 'embedded.fixture'
$script = Join-Path $root 'inert-installer.nsi'
$installer = Join-Path $root 'inert-installer.exe'
[IO.File]::WriteAllBytes($payload, [Text.Encoding]::UTF8.GetBytes("Arkism inert installer fixture.`n"))
$fixture = @'
Unicode true
Name "Arkism inert installer identity fixture"
OutFile "${TEST_OUTPUT}"
RequestExecutionLevel user
SilentInstall silent
AutoCloseWindow true
Section
  ReadEnvStr $0 "ARKISM_INSTALLER_FIXTURE_OUTPUT"
  StrCmp $0 "" invalid_output
  SetOutPath "$0"
  File /oname=embedded.fixture "${TEST_PAYLOAD}"
  Return
invalid_output:
  SetErrorLevel 31
  Quit
SectionEnd
'@
[IO.File]::WriteAllText($script, $fixture, (New-Object Text.UTF8Encoding($false)))
& $Makensis /NOCONFIG /V2 "/DTEST_OUTPUT=$installer" "/DTEST_PAYLOAD=$payload" $script
if ($LASTEXITCODE -ne 0) { throw 'update_identity_inert_installer_build_failed' }

# The two GUI controls share every input except the requested PE architecture.
# The native test also measures the actual PE fields and embedded RT_MANIFEST/1.
function New-IdentityArchitectureControls([string]$BuildRoot) {
  $compiler = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
  if (-not [IO.File]::Exists($compiler)) { throw 'identity_comparison_compiler_missing' }
  $controlSource = Join-Path $BuildRoot 'comparison.cs'
  $controlManifest = Join-Path $BuildRoot 'comparison.manifest'
  $text = @'
using System;
using System.IO;
using System.Text;

internal static class InertArchitectureProbe
{
    private static bool Hosted()
    {
        return String.Equals(Environment.GetEnvironmentVariable("GITHUB_ACTIONS"), "true", StringComparison.Ordinal)
            && String.Equals(Environment.GetEnvironmentVariable("RUNNER_OS"), "Windows", StringComparison.Ordinal)
            && String.Equals(Environment.GetEnvironmentVariable("RUNNER_ENVIRONMENT"), "github-hosted", StringComparison.Ordinal)
            && String.Equals(Environment.GetEnvironmentVariable("ARKISM_WINDOWS_UPDATE_IDENTITY_TEST"), "1", StringComparison.Ordinal);
    }

    private static bool LocalAbsolute(string path)
    {
        return !String.IsNullOrEmpty(path) && path.Length >= 3
            && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z'))
            && path[1] == ':' && path[2] == '\\';
    }

    private static bool OrdinaryOwnedOutput(string output, string temp)
    {
        DirectoryInfo directory = new DirectoryInfo(output);
        DirectoryInfo owner = directory.Parent;
        const string prefix = "arkism identity [fixture] ";
        if (!String.Equals(directory.Name, "inert NSIS output [owned]", StringComparison.Ordinal)
            || owner == null || owner.Parent == null
            || !String.Equals(owner.Parent.FullName.TrimEnd('\\'), temp.TrimEnd('\\'), StringComparison.OrdinalIgnoreCase)
            || !owner.Name.StartsWith(prefix, StringComparison.Ordinal)) return false;
        string nonce = owner.Name.Substring(prefix.Length);
        if (nonce.Length != 32) return false;
        foreach (char character in nonce)
            if (!((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f'))) return false;
        for (DirectoryInfo current = directory; current != null; current = current.Parent)
            if ((File.GetAttributes(current.FullName) & FileAttributes.ReparsePoint) != 0) return false;
        return directory.Exists && Directory.GetFileSystemEntries(output).Length == 0;
    }

    private static int Main(string[] arguments)
    {
        try
        {
            if (arguments.Length != 1 || !String.Equals(arguments[0], "/S", StringComparison.Ordinal)) return 64;
            if (!Hosted()) return 65;
            string rawOutput = Environment.GetEnvironmentVariable("ARKISM_INSTALLER_FIXTURE_OUTPUT");
            string rawTemp = Environment.GetEnvironmentVariable("RUNNER_TEMP");
            if (!LocalAbsolute(rawOutput) || !LocalAbsolute(rawTemp)) return 66;
            string output = Path.GetFullPath(rawOutput);
            string temp = Path.GetFullPath(rawTemp);
            if (new DriveInfo(Path.GetPathRoot(output)).DriveType != DriveType.Fixed
                || !OrdinaryOwnedOutput(output, temp)) return 66;
            byte[] payload = Encoding.ASCII.GetBytes("Arkism inert installer fixture.\n");
            using (FileStream stream = new FileStream(Path.Combine(output, "embedded.fixture"), FileMode.CreateNew, FileAccess.Write, FileShare.Read))
            {
                stream.Write(payload, 0, payload.Length);
                stream.Flush(true);
            }
            return 0;
        }
        catch (Exception)
        {
            return 70;
        }
    }
}
'@
  $manifestText = @'
<?xml version="1.0" encoding="utf-8"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity name="Arkism.InertArchitectureProbe" version="1.0.0.0" processorArchitecture="*" type="win32" />
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges>
      <requestedExecutionLevel level="asInvoker" uiAccess="false" />
    </requestedPrivileges></security>
  </trustInfo>
</assembly>
'@
  [IO.File]::WriteAllText($controlSource, $text, (New-Object Text.UTF8Encoding($false)))
  [IO.File]::WriteAllText($controlManifest, $manifestText, (New-Object Text.UTF8Encoding($false)))
  $document = New-Object Xml.XmlDocument
  $document.Load($controlManifest)
  $levels = $document.GetElementsByTagName('requestedExecutionLevel', 'urn:schemas-microsoft-com:asm.v3')
  if ($levels.Count -ne 1 -or $levels[0].GetAttribute('level') -cne 'asInvoker' -or
      $levels[0].GetAttribute('uiAccess') -cne 'false') { throw 'identity_comparison_manifest_invalid' }
  foreach ($platform in @('x86', 'x64')) {
    $directory = Join-Path $BuildRoot $platform
    $null = New-Item -ItemType Directory -Path $directory -ErrorAction Stop
    $image = Join-Path $directory 'comparison.exe'
    $arguments = @('/nologo', '/noconfig', '/optimize+', '/target:winexe',
      ('/platform:' + $platform), ('/win32manifest:' + $controlManifest), ('/out:' + $image), $controlSource)
    try { $null = @(& $compiler @arguments 2>&1) }
    catch { throw 'identity_comparison_compile_failed' }
    if ($LASTEXITCODE -ne 0 -or -not [IO.File]::Exists($image)) { throw 'identity_comparison_compile_failed' }
    [Environment]::SetEnvironmentVariable(('ARKISM_WINDOWS_INSTALLER_IDENTITY_MANAGED_' + $platform.ToUpperInvariant() + '_EXE'), $image, 'Process')
    [Environment]::SetEnvironmentVariable(('ARKISM_WINDOWS_INSTALLER_IDENTITY_MANAGED_' + $platform.ToUpperInvariant() + '_SHA256'), (Get-FileHash -LiteralPath $image -Algorithm SHA256).Hash.ToLowerInvariant(), 'Process')
  }
  $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_SOURCE_SHA256 = (Get-FileHash -LiteralPath $controlSource -Algorithm SHA256).Hash.ToLowerInvariant()
  $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_MANIFEST_SHA256 = (Get-FileHash -LiteralPath $controlManifest -Algorithm SHA256).Hash.ToLowerInvariant()
  $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_COMPILER_SHA256 = (Get-FileHash -LiteralPath $compiler -Algorithm SHA256).Hash.ToLowerInvariant()
}
New-IdentityArchitectureControls $root

$env:ARKISM_WINDOWS_UPDATE_IDENTITY_TEST = '1'
$env:ARKISM_WINDOWS_INSTALLER_IDENTITY_SOURCE_SHA = $source
$env:ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_EXE = $installer
$env:ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_SHA256 = (Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash.ToLowerInvariant()
$env:ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_COMPILER_SHA256 = (Get-FileHash -LiteralPath $Makensis -Algorithm SHA256).Hash.ToLowerInvariant()
$env:ARKISM_UPDATE_IDENTITY_EVIDENCE = $evidence
$listed = & cargo test --locked --manifest-path desktop/src-tauri/Cargo.toml --test windows_update_installer_identity windows_update_installer_identity -- --ignored --exact --list
if ($LASTEXITCODE -ne 0 -or @($listed | Where-Object { $_ -ceq 'windows_update_installer_identity: test' }).Count -ne 1) {
  throw 'update_identity_exact_test_missing'
}
cargo test --locked --manifest-path desktop/src-tauri/Cargo.toml --test windows_update_installer_identity windows_update_installer_identity -- --ignored --exact --test-threads=1
if ($LASTEXITCODE -ne 0) { throw 'update_identity_native_checks_failed' }
if (-not (Test-Path -LiteralPath $evidence -PathType Leaf)) { throw 'update_identity_evidence_missing' }
$report = Get-Content -LiteralPath $evidence -Raw | ConvertFrom-Json
if ($report.ok -isnot [bool] -or -not $report.ok -or $report.source_sha -cne $source -or
    $report.scope -cne 'inert_images_only' -or $report.native_windows -isnot [bool] -or -not $report.native_windows -or
    $report.inert_nsis_self_archive -isnot [bool] -or
    $report.production_nsis_executed -isnot [bool] -or $report.production_nsis_executed -or
    $report.module_sha256 -cne (Get-FileHash -LiteralPath 'desktop/src-tauri/src/update_installer.rs' -Algorithm SHA256).Hash.ToLowerInvariant() -or
    $report.process_ffi_sha256 -cne (Get-FileHash -LiteralPath 'desktop/src-tauri/src/windows_process.rs' -Algorithm SHA256).Hash.ToLowerInvariant() -or
    $report.fixture_sha256 -cne (Get-FileHash -LiteralPath 'desktop/src-tauri/tests/windows_update_installer_identity.rs' -Algorithm SHA256).Hash.ToLowerInvariant() -or
    $report.inert_nsis_sha256 -cne $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_SHA256 -or
    $report.comparison_source_sha256 -cne $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_SOURCE_SHA256 -or
    $report.comparison_manifest_sha256 -cne $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_MANIFEST_SHA256 -or
    $report.comparison_compiler_sha256 -cne $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_COMPILER_SHA256 -or
    $report.comparison_all_required_passed -isnot [bool] -or
    @($report.comparison_cases).Count -ne 4 -or
    $report.production_required_pass -isnot [bool] -or -not $report.production_required_pass -or
    @($report.production_bound_cases).Count -ne 3 -or
    $report.dos_device_mapping_atomicity_proven -isnot [bool] -or $report.dos_device_mapping_atomicity_proven) {
  throw 'update_identity_evidence_invalid'
}

function Assert-IdentityTerminal($Case) {
  if ($Case.created -isnot [bool] -or -not $Case.created -or
      $Case.terminal -isnot [bool] -or -not $Case.terminal) { throw 'identity_terminal_receipt_invalid' }
}
function Assert-IdentityPayloadSuccess($Case) {
  if ($Case.payload_verified -isnot [bool] -or -not $Case.payload_verified -or
      $Case.payload_bytes_match -isnot [bool] -or -not $Case.payload_bytes_match -or
      $Case.output_single_file -isnot [bool] -or -not $Case.output_single_file) {
    throw 'identity_payload_receipt_invalid'
  }
}
function Assert-IdentitySuccess($Case) {
  Assert-IdentityTerminal $Case
  Assert-IdentityPayloadSuccess $Case
  if ($Case.exit_code -ne 0) { throw 'identity_success_exit_invalid' }
}
function Assert-IdentityFailedCreate($Case) {
  if ($Case.created -isnot [bool] -or $Case.created -or
      $Case.terminal -isnot [bool] -or $Case.terminal -or $Case.operation -cne 'spawn' -or
      $null -eq $Case.os_code -or $null -ne $Case.payload_verified -or
      $null -ne $Case.payload_bytes_match -or $null -ne $Case.output_single_file) {
    throw 'identity_failed_create_receipt_invalid'
  }
}
function Assert-IdentityFailedExit($Case) {
  Assert-IdentityTerminal $Case
  if ($null -eq $Case.exit_code -or $Case.exit_code -eq 0 -or
      $Case.payload_verified -isnot [bool] -or $Case.payload_bytes_match -isnot [bool] -or
      $Case.output_single_file -isnot [bool]) { throw 'identity_failed_exit_receipt_invalid' }
}
function Assert-IdentityLegacyCases($Report) {
  $targets = @('managed_x86', 'managed_x64', 'inert_nsis', 'nsis_dos')
  for ($index = 0; $index -lt $targets.Count; $index++) {
    $case = $Report.comparison_cases[$index]
    if ($case.target -cne $targets[$index]) { throw 'identity_legacy_case_order_invalid' }
    switch -CaseSensitive ($case.outcome) {
      'passed' { Assert-IdentitySuccess $case }
      'create_failed' { Assert-IdentityFailedCreate $case }
      'exit_failed' { Assert-IdentityFailedExit $case }
      'payload_failed' {
        Assert-IdentityTerminal $case
        if ($case.exit_code -ne 0 -or $case.payload_verified -isnot [bool] -or $case.payload_verified) {
          throw 'identity_failed_payload_receipt_invalid'
        }
      }
      default { throw 'identity_unknown_legacy_owner_or_phase' }
    }
  }
}
function Assert-IdentityBoundPositive($Bound) {
  Assert-IdentitySuccess $Bound.case
  if ($Bound.native_name_match -isnot [bool] -or -not $Bound.native_name_match -or
      $Bound.original_reverified -isnot [bool] -or -not $Bound.original_reverified -or
      $Bound.resume_calls -ne 1 -or $Bound.resume_observation -ne 1 -or
      $Bound.abort_attempted -isnot [bool] -or $Bound.abort_attempted -or
      $Bound.abort_signaled -isnot [bool] -or $Bound.abort_signaled) {
    throw 'identity_production_binding_not_committed'
  }
}
function Assert-IdentityBoundRejection($Bound) {
  Assert-IdentityTerminal $Bound.case
  if ($Bound.native_name_match -isnot [bool] -or $Bound.native_name_match -or
      $Bound.resume_calls -ne 0 -or $null -ne $Bound.resume_observation -or
      $Bound.abort_attempted -isnot [bool] -or -not $Bound.abort_attempted -or
      $Bound.abort_signaled -isnot [bool] -or -not $Bound.abort_signaled) {
    throw 'identity_production_rejection_not_confirmed'
  }
  if ($Bound.marker_absent -isnot [bool] -or -not $Bound.marker_absent -or
      $Bound.cleanup_confirmed -isnot [bool] -or -not $Bound.cleanup_confirmed -or
      $Bound.case.operation -cne 'native_image_name_mismatch') {
    throw 'identity_production_rejection_effects_invalid'
  }
}
function Assert-IdentityProductionCases($Report) {
  $targets = @('bound_positive', 'bound_wrong_image', 'bound_case_only')
  for ($index = 0; $index -lt $targets.Count; $index++) {
    $bound = $Report.production_bound_cases[$index]
    if ($bound.case.target -cne $targets[$index] -or $bound.case.outcome -cne 'passed') {
      throw 'identity_production_case_failed'
    }
    if ($index -eq 0) { Assert-IdentityBoundPositive $bound }
    else { Assert-IdentityBoundRejection $bound }
  }
}
Assert-IdentityLegacyCases $report
Assert-IdentityProductionCases $report
