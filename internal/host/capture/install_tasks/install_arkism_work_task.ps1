# Retry pending work events using the installed Go client and its existing connection settings.
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$Arkism = (Join-Path $PSScriptRoot 'arkism.exe'),
    [ValidateNotNullOrEmpty()][ValidateSet('codex', 'claude_code', 'zcode')][string[]]$Agents = @('codex', 'claude_code', 'zcode'),
    [ValidatePattern('^$|^[A-Za-z0-9][A-Za-z0-9_.@:-]*$')][string]$SshHost = '',
    [ValidateRange(1, 60)][int]$EveryMinutes = 5
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'arkism_scheduled_tasks.ps1')
$normalizedAgents = $Agents | ForEach-Object { $_.ToLowerInvariant() } | Select-Object -Unique
foreach ($agent in $normalizedAgents) {
    $taskName = "Arkism-Work-Drain-$agent"
    $arguments = "windows drain-work $agent"
    if ($SshHost) { $arguments += " --ssh-host $SshHost" }
    if ($PSCmdlet.ShouldProcess($taskName, 'Register current-user synchronization')) {
        Register-ArkismScheduledTask -TaskName $taskName -Arkism $Arkism `
            -Arguments $arguments -EveryMinutes $EveryMinutes
    }
}
