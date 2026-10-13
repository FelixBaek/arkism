# Register only after the PC relay and server receiver versions are deployed together.
param(
    [string]$ArkismPath = (Join-Path $PSScriptRoot 'arkism.exe'),
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9_.@-]*$')][string]$SshHost = $(if ($env:ARKISM_SSH_HOST) { $env:ARKISM_SSH_HOST } else { 'arkism-server' })
)
$ErrorActionPreference = 'Stop'
if (!(Test-Path -LiteralPath $ArkismPath -PathType Leaf)) { throw 'Missing arkism.exe' }
$arkism = (Resolve-Path -LiteralPath $ArkismPath).Path
$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
$principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
foreach ($origin in @('codex', 'claude_code', 'zcode')) {
    $arguments = 'windows drain-work ' + $origin + ' --ssh-host ' + $SshHost
    $action = New-ScheduledTaskAction -Execute $arkism -Argument $arguments
    $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 1)
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Minutes 10)
    Register-ScheduledTask -TaskName ('Arkism-Work-Drain-' + $origin) -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
}
Write-Output 'Arkism work signal retry tasks registered for the current user.'
