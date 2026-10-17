# Register only when explicitly run by the user. No Python, password or server key is needed.
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$Arkism = (Join-Path $PSScriptRoot 'arkism.exe'),
    [ValidateRange(1, 60)][int]$EveryMinutes = 5
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'arkism_scheduled_tasks.ps1')
if ($PSCmdlet.ShouldProcess('Arkism-Zcode-Export', 'Register current-user synchronization')) {
    Register-ArkismScheduledTask -TaskName 'Arkism-Zcode-Export' -Arkism $Arkism `
        -Arguments 'export-zcode --execute' -EveryMinutes $EveryMinutes
}
