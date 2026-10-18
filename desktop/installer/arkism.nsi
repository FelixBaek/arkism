; Arkism per-user installer: one exe, Start menu + desktop shortcut, uninstaller.
Unicode true
ManifestDPIAware true
!include "MUI2.nsh"
!include "FileFunc.nsh"

!ifndef VERSION
  !define VERSION "0.1.0"
!endif
!ifndef STAGE_DIR
  !define STAGE_DIR "..\stage"
!endif
!ifndef OUT_FILE
  !define OUT_FILE "Arkism-${VERSION}-setup.exe"
!endif
!define APP "Arkism"
!define EXE "Arkism.exe"
!define REG_UNINSTALL "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP}"

Name "${APP} ${VERSION}"
OutFile "${OUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\${APP}"
RequestExecutionLevel user
SetCompressor /SOLID lzma
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "${APP} Setup"
VIAddVersionKey "CompanyName" "Arkism contributors"
VIAddVersionKey "LegalCopyright" "Arkism contributors"

!define MUI_ICON "${__FILEDIR__}\..\src-tauri\icons\icon.ico"
!define MUI_UNICON "${__FILEDIR__}\..\src-tauri\icons\icon.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Arkism 실행"
; Opening the installer waits for 다음 before installing; the silent in-app update skips pages.
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Korean"

Section "Install"
  ; Close everything this install replaces, in any session: the app, its built-in Postgres and
  ; server (runtime\), and the provider watcher it runs from integration\. The app stops them
  ; itself before an in-app update; this catches a crashed or force-closed app.
  ; Match by full path: image names are case-insensitive, so Arkism.exe would also hit arkism.exe.
  nsExec::Exec `powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { $$_.ExecutablePath -eq '$INSTDIR\${EXE}' -or $$_.ExecutablePath -like '$INSTDIR\runtime\*' -or $$_.ExecutablePath -like '$INSTDIR\integration\*' } | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force }"`
  Sleep 500
  ; The agent client only. The installer does not touch the agents' own settings: the user
  ; connects them from the app (Settings > Agents), and agents already connected keep using
  ; this updated client.
  SetOutPath "$INSTDIR\integration"
  File "${STAGE_DIR}\integration\*"
  SetOutPath "$INSTDIR\runtime"
  File /r "${STAGE_DIR}\runtime\*.*"
  SetOutPath "$INSTDIR"
  File "${STAGE_DIR}\${EXE}"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${EXE}"
  CreateShortcut "$DESKTOP\${APP}.lnk" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${REG_UNINSTALL}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${REG_UNINSTALL}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${REG_UNINSTALL}" "Publisher" "Arkism contributors"
  WriteRegStr HKCU "${REG_UNINSTALL}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${REG_UNINSTALL}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${REG_UNINSTALL}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKCU "${REG_UNINSTALL}" "NoModify" 1
  WriteRegDWORD HKCU "${REG_UNINSTALL}" "NoRepair" 1
  ; Silent updates restart the app unless an unattended caller explicitly opts out.
  ; /NoAutoRun is a runtime option on the same installer; interactive launch stays unchanged.
  IfSilent 0 arkism_install_complete
  ${GetParameters} $0
  ClearErrors
  ${GetOptions} $0 "/NoAutoRun" $1
  IfErrors arkism_launch_after_install
  StrCmp $1 "" arkism_install_complete
arkism_launch_after_install:
  Exec '"$INSTDIR\${EXE}"'
arkism_install_complete:
SectionEnd

Section "Uninstall"
  ; Take Arkism out of the agents' settings first (backed up), while the client still exists.
  nsExec::Exec '"$INSTDIR\integration\arkism.exe" uninstall-arkism-windows'
  nsExec::Exec `powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { $$_.ExecutablePath -eq '$INSTDIR\${EXE}' -or $$_.ExecutablePath -like '$INSTDIR\runtime\*' -or $$_.ExecutablePath -like '$INSTDIR\integration\*' } | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force }"`
  Sleep 300
  ; Only the programs; the memories in the app's data folder stay unless the user removes them.
  RMDir /r "$INSTDIR\runtime"
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\uninstall.exe"
  Delete "$INSTDIR\integration\arkism.exe"
  Delete "$INSTDIR\integration\*.ps1"
  RMDir "$INSTDIR\integration"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APP}.lnk"
  Delete "$DESKTOP\${APP}.lnk"
  DeleteRegKey HKCU "${REG_UNINSTALL}"
SectionEnd
