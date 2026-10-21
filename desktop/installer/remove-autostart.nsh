!ifndef ARKISM_REMOVE_AUTOSTART_INCLUDED
!define ARKISM_REMOVE_AUTOSTART_INCLUDED

; Same executable policy used by the settings UI. This helper exits before any
; GUI, database or agent setup. The failure target must retain the payload.
!macro ArkismRemoveAutostart Executable FailureLabel
  nsExec::Exec /TIMEOUT=15000 '"${Executable}" --remove-autostart'
  Pop $0
  StrCmp $0 "0" +2
  Goto ${FailureLabel}
!macroend

!endif
