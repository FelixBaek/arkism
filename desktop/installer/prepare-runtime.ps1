# Built-in Arkism for the Windows installer: portable Postgres with pgvector, prepared once per
# version in the build PC's cache, then copied into the installer stage beside the server programs.
# Needs the Visual Studio C++ build tools (for pgvector). Run by build-windows.sh on the build PC.
param([Parameter(Mandatory)][string]$Cache, [Parameter(Mandatory)][string]$Stage)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$pgVersion = '17.6-1'
$vectorVersion = '0.8.1'
$dir = Join-Path $Cache "postgres-$pgVersion-pgvector-$vectorVersion"
$pg = Join-Path $dir 'runtime\pgsql'
if (-not (Test-Path (Join-Path $pg 'lib\vector.dll'))) {
  if (Test-Path $dir) { Remove-Item -Recurse -Force $dir }
  New-Item -ItemType Directory -Force $dir | Out-Null
  $full = Join-Path $dir 'full'
  Invoke-WebRequest -UseBasicParsing "https://get.enterprisedb.com/postgresql/postgresql-$pgVersion-windows-x64-binaries.zip" -OutFile (Join-Path $dir 'pg.zip')
  Expand-Archive (Join-Path $dir 'pg.zip') $full -Force
  Invoke-WebRequest -UseBasicParsing "https://github.com/pgvector/pgvector/archive/refs/tags/v$vectorVersion.zip" -OutFile (Join-Path $dir 'pgvector.zip')
  Expand-Archive (Join-Path $dir 'pgvector.zip') $dir -Force
  $vs = & "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe" -latest -products * -property installationPath
  $vcvars = Join-Path $vs 'VC\Auxiliary\Build\vcvars64.bat'
  $src = Join-Path $dir "pgvector-$vectorVersion"
  cmd /c "`"$vcvars`" >nul && set `"PGROOT=$full\pgsql`" && cd /d `"$src`" && nmake /nologo /F Makefile.win && nmake /nologo /F Makefile.win install"
  if ($LASTEXITCODE -ne 0) { throw 'pgvector build failed' }
  # Only what the app runs: the server and its setup tools, every DLL, and share/.
  # No pgAdmin, StackBuilder, docs, headers or static libraries.
  New-Item -ItemType Directory -Force (Join-Path $pg 'bin'), (Join-Path $pg 'lib') | Out-Null
  $tools = 'postgres.exe', 'pg_ctl.exe', 'initdb.exe', 'psql.exe', 'pg_dump.exe', 'pg_restore.exe'
  Get-ChildItem (Join-Path $full 'pgsql\bin') -File | Where-Object { $_.Extension -eq '.dll' -or $tools -contains $_.Name } | Copy-Item -Destination (Join-Path $pg 'bin')
  Get-ChildItem (Join-Path $full 'pgsql\lib') -File -Filter *.dll | Copy-Item -Destination (Join-Path $pg 'lib')
  Copy-Item -Recurse (Join-Path $full 'pgsql\share') (Join-Path $pg 'share')
  Remove-Item -Recurse -Force $full, (Join-Path $dir 'pg.zip'), (Join-Path $dir 'pgvector.zip'), $src
}
$target = Join-Path $Stage 'runtime'
New-Item -ItemType Directory -Force $target | Out-Null
Copy-Item -Recurse -Force $pg (Join-Path $target 'pgsql')
$mb = [int]((Get-ChildItem -Recurse -File $target | Measure-Object Length -Sum).Sum / 1MB)
Write-Host "built-in runtime staged: $mb MB"
