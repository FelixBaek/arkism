# Run on the PC after its session outbox has been mapped to the server inbox.
param(
    [string]$ArkismPath = (Join-Path $PSScriptRoot 'arkism.exe'),
    [Parameter(Mandatory=$true)][string]$Outbox
)
$ErrorActionPreference = 'Stop'
if (!(Test-Path -LiteralPath $ArkismPath -PathType Leaf)) { throw 'Missing arkism.exe' }
$arkism = (Resolve-Path -LiteralPath $ArkismPath).Path
New-Item -ItemType Directory -Force -Path $Outbox | Out-Null
$resolved = (Resolve-Path -LiteralPath $Outbox).Path
# The exporter reads session-export.json from next to arkism.exe.
$config = Join-Path (Split-Path -Parent $arkism) 'session-export.json'
$temp = $config + '.new'
[IO.File]::WriteAllText($temp, (@{outbox=$resolved} | ConvertTo-Json), (New-Object Text.UTF8Encoding($false)))
Move-Item -LiteralPath $temp -Destination $config -Force
$action = New-ScheduledTaskAction -Execute $arkism -Argument 'export-zcode --execute'
$trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 1)
$settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Minutes 30)
$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
$principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName 'Arkism-Zcode-Export' -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
Write-Output 'Arkism Zcode export scheduled. Folder synchronization is configured separately.'
