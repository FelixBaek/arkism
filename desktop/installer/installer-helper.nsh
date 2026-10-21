!ifndef ARKISM_INSTALLER_HELPER_INCLUDED
!define ARKISM_INSTALLER_HELPER_INCLUDED
!ifndef ARKISM_INSTALLER_HELPER_TIMEOUT_MS
!define ARKISM_INSTALLER_HELPER_TIMEOUT_MS 300000
!endif

; Always execute the authenticated helper extracted outside the live payload.
; Success includes a durable commit whose optional housekeeping needs a retry.
!macro ArkismRunInstallerHelper Command FailureLabel
  nsExec::ExecToStack /TIMEOUT=${ARKISM_INSTALLER_HELPER_TIMEOUT_MS} '"$PLUGINSDIR\arkism-installer.exe" ${Command} "$INSTDIR"'
  Pop $0
  Pop $1
  DetailPrint $1
  StrCmp $0 "0" +2
  Goto ${FailureLabel}
!macroend

!endif
