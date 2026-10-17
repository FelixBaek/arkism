# Windows CI contract: use native task-object factories, but NEVER register a real task.
$ErrorActionPreference = 'Stop'
Import-Module ScheduledTasks -ErrorAction Stop
$global:ArkismTaskRegistrations = [Collections.Generic.List[object]]::new()
$global:ArkismTaskRegistrationShouldFail = $false
function global:Register-ScheduledTask {
    [CmdletBinding()]
    param($TaskName, $Action, $Trigger, $Principal, $Settings, $Description, [switch]$Force)
    if ($global:ArkismTaskRegistrationShouldFail) { throw 'simulated registration failure' }
    $global:ArkismTaskRegistrations.Add([PSCustomObject]@{
        TaskName = $TaskName; Action = $Action; Trigger = $Trigger; Principal = $Principal; Settings = $Settings
    })
}
function Assert-TaskContract([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}
function Assert-TaskThrows([scriptblock]$Action) {
    $thrown = $false
    try { & $Action | Out-Null } catch { $thrown = $true }
    Assert-TaskContract $thrown 'Expected invalid input or registration failure to be reported'
}
$root = Join-Path ([IO.Path]::GetTempPath()) "arkism task contract [$([guid]::NewGuid())]"
[IO.Directory]::CreateDirectory($root) | Out-Null
try {
    $binary = Join-Path $root 'arkism.exe'
    [IO.File]::WriteAllText($binary, 'fake executable; never run')
    $scripts = Join-Path $PSScriptRoot '..\internal\host\capture\install_tasks'
    $export = Join-Path $scripts 'install_arkism_export_task.ps1'
    $work = Join-Path $scripts 'install_arkism_work_task.ps1'
    Assert-TaskContract ((Get-Command Register-ScheduledTask).CommandType -eq 'Function') 'Registration mock is not active'
    & $export -Arkism $binary -Confirm:$false | Out-Null
    & $work -Arkism $binary -Agents codex,claude_code,zcode,codex -SshHost 'sync-host' -Confirm:$false | Out-Null
    Assert-TaskContract ($global:ArkismTaskRegistrations.Count -eq 4) 'Expected export and three distinct drain tasks'
    $expected = @{
        'Arkism-Zcode-Export' = 'export-zcode --execute'
        'Arkism-Work-Drain-codex' = 'windows drain-work codex --ssh-host sync-host'
        'Arkism-Work-Drain-claude_code' = 'windows drain-work claude_code --ssh-host sync-host'
        'Arkism-Work-Drain-zcode' = 'windows drain-work zcode --ssh-host sync-host'
    }
    $user = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    foreach ($task in $global:ArkismTaskRegistrations) {
        Assert-TaskContract ($task.Action.Execute -eq $binary) 'Executable path was not preserved'
        Assert-TaskContract ($task.Action.WorkingDirectory -eq $root) 'Working directory was not preserved'
        Assert-TaskContract ($task.Action.Arguments -eq $expected[$task.TaskName]) 'Task uses the wrong CLI contract'
        Assert-TaskContract ($task.Principal.UserId -eq $user) 'Task does not belong to the current user'
        Assert-TaskContract ([int]$task.Principal.RunLevel -eq 0) 'Task must not request elevation'
        Assert-TaskContract ([int]$task.Principal.LogonType -eq 3) 'Task must use the interactive user token'
        Assert-TaskContract ($task.Trigger.Repetition.Interval -eq 'PT5M') 'Wrong repetition interval'
        Assert-TaskContract ([string]::IsNullOrEmpty($task.Trigger.Repetition.Duration)) 'Repetition unexpectedly expires'
        Assert-TaskContract ([int]$task.Settings.MultipleInstances -eq 2) 'Overlapping tasks must be ignored'
        Assert-TaskContract ($task.Settings.ExecutionTimeLimit -eq 'PT5M') 'Task runtime must be bounded'
        Assert-TaskContract ([bool]$task.Settings.StartWhenAvailable) 'Missed triggers should resume'
    }
    & $export -Arkism $binary -WhatIf | Out-Null
    & $work -Arkism $binary -WhatIf | Out-Null
    Assert-TaskContract ($global:ArkismTaskRegistrations.Count -eq 4) 'WhatIf registered a task'
    Assert-TaskThrows { & $work -Arkism $binary -Agents gemini -Confirm:$false }
    Assert-TaskThrows { & $work -Arkism $binary -SshHost 'bad host' -Confirm:$false }
    Assert-TaskThrows { & $work -Arkism $binary -SshHost '-oProxyCommand=bad' -Confirm:$false }
    Assert-TaskThrows { & $work -Arkism $binary -EveryMinutes 0 -Confirm:$false }
    Assert-TaskThrows { & $export -Arkism (Join-Path $root 'missing.exe') -Confirm:$false }
    Assert-TaskThrows { & $export -Arkism $root -Confirm:$false }
    $global:ArkismTaskRegistrationShouldFail = $true
    Assert-TaskThrows { & $export -Arkism $binary -Confirm:$false }
    Assert-TaskContract ($global:ArkismTaskRegistrations.Count -eq 4) 'Rejected input registered a task'
    Write-Host 'Windows scheduled task contract passed; no real tasks were registered.'
} finally {
    Remove-Item Function:\Register-ScheduledTask
    Remove-Variable ArkismTaskRegistrations, ArkismTaskRegistrationShouldFail -Scope Global
    Remove-Item -LiteralPath $root -Recurse -Force
}
