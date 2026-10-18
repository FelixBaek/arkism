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
  Quit
invalid_output:
  SetErrorLevel 31
  Quit
SectionEnd
'@
[IO.File]::WriteAllText($script, $fixture, (New-Object Text.UTF8Encoding($false)))
& $Makensis /NOCONFIG /V2 "/DTEST_OUTPUT=$installer" "/DTEST_PAYLOAD=$payload" $script
if ($LASTEXITCODE -ne 0) { throw 'update_identity_inert_installer_build_failed' }

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
    $report.inert_nsis_self_archive -isnot [bool] -or -not $report.inert_nsis_self_archive -or
    $report.production_nsis_executed -isnot [bool] -or $report.production_nsis_executed -or
    $report.module_sha256 -cne (Get-FileHash -LiteralPath 'desktop/src-tauri/src/update_installer.rs' -Algorithm SHA256).Hash.ToLowerInvariant() -or
    $report.fixture_sha256 -cne (Get-FileHash -LiteralPath 'desktop/src-tauri/tests/windows_update_installer_identity.rs' -Algorithm SHA256).Hash.ToLowerInvariant() -or
    $report.inert_nsis_sha256 -cne $env:ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_SHA256) {
  throw 'update_identity_evidence_invalid'
}
