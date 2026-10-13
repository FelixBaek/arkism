# Start Arkism on this PC: Postgres, the API and the worker (Docker Desktop).
# The first run creates .env with random passwords and tokens and prints the owner token at the end.
# Run it again any time: it keeps .env and the data, and only starts what is stopped.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Docker Desktop is required: https://docs.docker.com/desktop/' }
function Invoke-Docker { & docker @args; if ($LASTEXITCODE -ne 0) { throw "docker $($args -join ' ') failed" } }

if (-not (Test-Path .env)) {
  function New-Secret {
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    -join ($bytes | ForEach-Object { '{0:x2}' -f $_ })
  }
  @(
    "ARKISM_DB_ADMIN_PASSWORD=$(New-Secret)"
    "ARKISM_INIT_PASSWORD=$(New-Secret)"
    "ARKISM_APP_PASSWORD=$(New-Secret)"
    "ARKISM_OWNER_TOKEN=$(New-Secret)"
    "ARKISM_WORKER_TOKEN=$(New-Secret)"
    'ARKISM_API_PORT=8091'
    'ARKISM_DB_PORT=55491'
  ) | Set-Content -Encoding ascii .env
  Write-Host 'Created deploy\local\.env (keep it private).'
}
$cfg = @{}
Get-Content .env | Where-Object { $_ -match '^([A-Z_]+)=(.*)$' } | ForEach-Object { $cfg[$Matches[1]] = $Matches[2] }

Write-Host 'Building the Arkism image...'
Invoke-Docker compose --profile setup build setup
Write-Host 'Starting Postgres...'
Invoke-Docker compose up -d --wait db
Get-Content -Raw roles.sql | docker compose exec -T -e "PGPASSWORD=$($cfg.ARKISM_DB_ADMIN_PASSWORD)" db `
  psql --no-psqlrc -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -U supabase_admin -d arkism `
  -v "init_pw=$($cfg.ARKISM_INIT_PASSWORD)" -v "app_pw=$($cfg.ARKISM_APP_PASSWORD)"
if ($LASTEXITCODE -ne 0) { throw 'Could not create the database roles' }
Write-Host 'Creating the schema...'
Invoke-Docker compose --profile setup run --rm setup
function Register($token, $principal, $role) {
  $env:MEMORY_PRINCIPAL_TOKEN = $token
  try {
    Invoke-Docker compose --profile setup run --rm -e MEMORY_PRINCIPAL_TOKEN --entrypoint /memory-principal setup `
      --principal $principal --role $role --organization default --project default --actions '*'
  } finally { Remove-Item Env:MEMORY_PRINCIPAL_TOKEN }
}
Register $cfg.ARKISM_OWNER_TOKEN 'management:owner' 'human'
Register $cfg.ARKISM_WORKER_TOKEN 'worker' 'service'
Write-Host 'Starting the API and the worker...'
Invoke-Docker compose up -d api worker

$url = "http://127.0.0.1:$($cfg.ARKISM_API_PORT)"
$ready = $false
foreach ($i in 1..60) {
  try { Invoke-WebRequest -UseBasicParsing "$url/readyz" | Out-Null; $ready = $true; break } catch { Start-Sleep 1 }
}
if (-not $ready) { throw "The API did not become ready. See: docker compose -f deploy\local\compose.yaml logs api" }

$sshUser = $env:USERNAME
$sshHost = $env:COMPUTERNAME
$urlNote = ''
if ($url -ne 'http://127.0.0.1:8091') { $urlNote = "`n     The API is not on the default port: also run  setx ARKISM_URL $url" }
Write-Host ''
Write-Host 'Arkism is running.'
Write-Host "  Server address : $url"
Write-Host "  Owner token    : $($cfg.ARKISM_OWNER_TOKEN) (also in deploy\local\.env)"
Write-Host @"

Next, so the desktop app can connect with one SSH address (see docs/server.md):
  1. Turn on the OpenSSH Server on this PC (Settings > System > Optional features), so 'ssh $sshUser@$sshHost' works.
  2. Install the arkism tool (from the repository root, in PowerShell):
       go build -o arkism.exe ./cmd/arkism
       New-Item -ItemType Directory -Force "`$env:LOCALAPPDATA\Arkism\bin" | Out-Null
       Copy-Item arkism.exe "`$env:LOCALAPPDATA\Arkism\bin\arkism.exe"
       [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path','User') + ";`$env:LOCALAPPDATA\Arkism\bin", 'User')
  3. Save the owner token for your user only and point the tool at it:
       (Select-String '^ARKISM_OWNER_TOKEN=' deploy\local\.env).Line.Split('=',2)[1] | Set-Content -NoNewline "`$env:LOCALAPPDATA\Arkism\owner.token"
       icacls "`$env:LOCALAPPDATA\Arkism\owner.token" /inheritance:r /grant:r "`${env:USERNAME}:F"
       setx ARKISM_TOKEN_FILE "`$env:LOCALAPPDATA\Arkism\owner.token"$urlNote
  4. Open a new terminal and check it: arkism connect-info   (prints the connection details as JSON)
  5. On the PC with the app, make sure 'ssh $sshUser@$sshHost' works without a password prompt, then open
     Settings > 연결 (Connection), type $sshUser@$sshHost (or your ~/.ssh/config alias) and connect.
     If the app does not find arkism, enter its full path in the connection form.

Stop: docker compose -f deploy\local\compose.yaml down (data is kept).
"@
