# Build the Arkism Windows installer on a Windows machine (GitHub Actions windows runner, or a PC
# with Go, Rust MSVC, Visual Studio C++ build tools and NSIS). build-windows.sh does the same from
# a Linux PC over SSH; this is the self-contained version the release workflow runs.
#
#   pwsh desktop/installer/build-windows.ps1 -Version 0.3.0
#
# Values compiled into the app come from the environment (ARKISM_UPDATE_FEED, ARKISM_DEFAULT_SERVER).
# When WINDOWS_CERTIFICATE (a base64 .pfx) and WINDOWS_CERTIFICATE_PASSWORD are set, the app,
# the bundled programs and the installer are signed; otherwise they are not.
#
# Output: desktop/dist/Arkism-<version>-setup.exe (+ .sha256)
param([Parameter(Mandatory)][string]$Version, [string]$Makensis = 'makensis')
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw '-Version x.y.z is required' }
$desktop = Split-Path -Parent $PSScriptRoot
$repo = Split-Path -Parent $desktop
$stage = Join-Path $desktop 'stage'
$cache = if ($env:ARKISM_BUILD_CACHE) { $env:ARKISM_BUILD_CACHE } else { Join-Path $desktop '.build-cache' }
$dist = Join-Path $desktop 'dist'
$tmp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
New-Item -ItemType Directory -Force (Join-Path $stage 'integration'), (Join-Path $stage 'runtime'), $dist | Out-Null

function Invoke-Checked([scriptblock]$Block, [string]$What) {
  & $Block
  if ($LASTEXITCODE -ne 0) { throw "$What failed" }
}

# Build the agent client (including embedded task helpers), the server programs and database roles;
# then prepare Postgres with pgvector (prepare-runtime.ps1).
$env:CGO_ENABLED = '0'
Push-Location $repo
Invoke-Checked { go build -trimpath -o (Join-Path $stage 'integration\arkism.exe') ./cmd/arkism } 'go build arkism'
Invoke-Checked { go build -trimpath -o (Join-Path $stage 'runtime\memoryd.exe') ./cmd/memoryd } 'go build memoryd'
Invoke-Checked { go build -trimpath -o (Join-Path $stage 'runtime\memory-principal.exe') ./cmd/memory-principal } 'go build memory-principal'
Pop-Location
Copy-Item (Join-Path $repo 'tools\roles.sql') (Join-Path $stage 'runtime')
& (Join-Path $PSScriptRoot 'prepare-runtime.ps1') -Cache $cache -Stage $stage

$env:TAURI_CONFIG = '{"version":"' + $Version + '"}'
# Keep the build machine's paths (user name, folders) out of the binary's panic locations.
$env:RUSTFLAGS = "--remap-path-prefix=$($env:USERPROFILE)=~ --remap-path-prefix=$repo=build"
Push-Location (Join-Path $desktop 'src-tauri')
Invoke-Checked { cargo build --release --locked } 'cargo build'
Invoke-Checked { cargo test --release --locked } 'cargo test'
Pop-Location
Copy-Item (Join-Path $desktop 'src-tauri\target\release\arkism-desktop.exe') (Join-Path $stage 'Arkism.exe') -Force

$signing = $env:WINDOWS_CERTIFICATE -and $env:WINDOWS_CERTIFICATE_PASSWORD
$pfx = Join-Path $tmp 'arkism-signing.pfx'
function Sign-Files([string[]]$Files) {
  if (-not $signing) { return }
  $signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" | Sort-Object FullName | Select-Object -Last 1
  Invoke-Checked { & $signtool.FullName sign /f $pfx /p $env:WINDOWS_CERTIFICATE_PASSWORD /fd sha256 /tr http://timestamp.digicert.com /td sha256 @Files } 'signtool'
}
try {
  if ($signing) { [IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:WINDOWS_CERTIFICATE)) }
  Sign-Files @((Join-Path $stage 'Arkism.exe'), (Join-Path $stage 'integration\arkism.exe'), (Join-Path $stage 'runtime\memoryd.exe'), (Join-Path $stage 'runtime\memory-principal.exe'))
  $out = Join-Path $dist "Arkism-$Version-setup.exe"
  Invoke-Checked { & $Makensis /V2 "/DVERSION=$Version" "/DSTAGE_DIR=$stage" "/DOUT_FILE=$out" (Join-Path $PSScriptRoot 'arkism.nsi') } 'makensis'
  Sign-Files @($out)
} finally {
  if (Test-Path $pfx) { Remove-Item -Force $pfx }
}

# The installed app must start its built-in Arkism, back it up, erase and restore it.
$check = Join-Path $tmp "arkism-runtime-check-$PID"
$p = Start-Process (Join-Path $stage 'Arkism.exe') -ArgumentList '--runtime-check', $check -Wait -PassThru
Get-Content (Join-Path $check 'check.json')
if ($p.ExitCode -ne 0) { throw 'runtime check failed' }

$hash = (Get-FileHash $out -Algorithm SHA256).Hash.ToLower()
"$hash  $(Split-Path -Leaf $out)" | Set-Content -NoNewline -Encoding ascii "$out.sha256"
Write-Host "$out $hash"
