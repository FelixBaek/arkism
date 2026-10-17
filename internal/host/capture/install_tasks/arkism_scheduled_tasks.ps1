# Shared by the two explicit, user-invoked registration scripts. Loading this file registers nothing.
function Register-ArkismScheduledTask {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$TaskName,
        [Parameter(Mandatory)][string]$Arkism,
        [Parameter(Mandatory)][string]$Arguments,
        [ValidateRange(1, 60)][int]$EveryMinutes = 5
    )
    $ErrorActionPreference = 'Stop'
    $binary = Get-Item -LiteralPath $Arkism -ErrorAction Stop
    if ($binary.PSIsContainer) { throw 'Arkism must be an executable file, not a directory' }
    $action = New-ScheduledTaskAction -Execute $binary.FullName -Argument $Arguments -WorkingDirectory $binary.DirectoryName
    # Omitting RepetitionDuration repeats indefinitely; TimeSpan.MaxValue produces invalid task XML.
    $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes $EveryMinutes)
    $user = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable `
        -ExecutionTimeLimit (New-TimeSpan -Minutes 5) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal `
        -Settings $settings -Description 'Arkism local synchronization for the current user' -Force -ErrorAction Stop | Out-Null
    [PSCustomObject]@{ TaskName = $TaskName; Registered = $true; EveryMinutes = $EveryMinutes }
}
