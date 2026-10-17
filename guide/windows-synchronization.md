# Optional Windows synchronization tasks

Connecting an agent installs the Go client and three embedded PowerShell helpers
in `$HOME\.config\arkism`. The helpers ship inside the binary, so a missing
source helper fails compilation and both Windows packaging paths carry the same
payload. No adjacent download or Python installation is needed.

Installation and connection do not register scheduled tasks. If you use the
Windows-to-server synchronization outbox and want periodic retries, explicitly
run the relevant helper from PowerShell. Preview first:

```powershell
& "$HOME\.config\arkism\install_arkism_work_task.ps1" -Agents codex,claude_code -WhatIf
& "$HOME\.config\arkism\install_arkism_export_task.ps1" -WhatIf
```

Omit `-WhatIf` to register. `-EveryMinutes` accepts 1–60, with 5 as the default.
`-Arkism` can select another installed executable. The work helper also accepts
`-SshHost` for an explicit SSH alias; otherwise the Go client's existing
connection settings apply. The task arguments never contain API tokens or
passwords.

The export helper invokes `arkism export-zcode --execute` and the work helper
invokes `arkism windows drain-work` for the selected supported agents. These are
outbox synchronization tools, not database backups or replacements for agent
hooks. They run only while the current user is logged on, request no elevation,
ignore overlapping runs, and stop a run after five minutes. Repetition has no
end date. Registration errors are reported rather than treated as success.

Disconnecting Arkism removes its four known task names and backs up and removes
the registration helpers. Unrelated files and tasks are preserved. Reconnecting
repairs a missing helper from the binary. This does not change existing data,
encryption, server permissions or backup support.

The Windows CI contract constructs actions, triggers, principals and settings
with native ScheduledTasks cmdlets, but replaces task registration with a test
double. It never registers tasks or runs synchronization. Installed-app checks
and an actual scheduled retry are separate acceptance steps.

The repeat behavior follows Microsoft's [task repetition duration contract](https://learn.microsoft.com/en-us/windows/win32/taskschd/taskschedulerschema-duration-repetitiontype-element).
