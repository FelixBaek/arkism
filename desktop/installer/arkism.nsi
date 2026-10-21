; Arkism per-user installer: one exe, Start menu + desktop shortcut, uninstaller.
Unicode true
ManifestDPIAware true
!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "${__FILEDIR__}\remove-autostart.nsh"
!include "${__FILEDIR__}\installer-helper.nsh"

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
InstallDirRegKey HKCU "${REG_UNINSTALL}" "InstallLocation"
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
!ifdef ARKISM_EMIT_UNINSTALLER
  ; Build-only executable: write the uninstaller next to this emitter. It has
  ; no installation page, launch action, registry or shortcut writes.
  SilentInstall silent
  !insertmacro MUI_UNPAGE_INSTFILES
  !insertmacro MUI_LANGUAGE "Korean"
  Section "Emit uninstaller"
    ClearErrors
    WriteUninstaller "$EXEDIR\uninstall.exe"
    IfErrors 0 +3
    SetErrorLevel 1
    Abort "Uninstaller emission failed"
  SectionEnd
!else
  !ifndef PACKAGE_MANIFEST
    !error "PACKAGE_MANIFEST is required; emit, sign and pack the payload first"
  !endif
  !define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
  !define MUI_FINISHPAGE_RUN_TEXT "Arkism 실행"
  ; Opening setup waits for Next; silent updates skip these pages.
  !insertmacro MUI_PAGE_WELCOME
  !insertmacro MUI_PAGE_INSTFILES
  !insertmacro MUI_PAGE_FINISH
  !insertmacro MUI_LANGUAGE "Korean"

Section "Install"
  ; Extract the entire authenticated bundle without changing the old install.
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=arkism-installer.exe "${STAGE_DIR}\runtime\arkism-installer.exe"
  File /oname=payload.json "${PACKAGE_MANIFEST}"
  SetOutPath "$PLUGINSDIR\payload\integration"
  File "${STAGE_DIR}\integration\*"
  SetOutPath "$PLUGINSDIR\payload\runtime"
  File /r "${STAGE_DIR}\runtime\*.*"
  SetOutPath "$PLUGINSDIR\payload"
  File "${STAGE_DIR}\${EXE}"
  File "${STAGE_DIR}\uninstall.exe"
  IfErrors arkism_install_failed
  ; The helper verifies every staged byte, stops verified process handles and
  ; commits directory publication, shortcuts and registration as one lifecycle.
  !insertmacro ArkismRunInstallerHelper 'install "${VERSION}"' arkism_install_failed
  SetOutPath "$INSTDIR"
  ; Silent updates restart the app unless an unattended caller opts out.
  IfSilent 0 arkism_install_complete
  ${GetParameters} $0
  ClearErrors
  ${GetOptions} $0 "/NoAutoRun" $1
  IfErrors arkism_launch_after_install
  StrCmp $1 "" arkism_install_complete
arkism_launch_after_install:
  Exec '"$INSTDIR\${EXE}"'
  Goto arkism_install_complete
arkism_install_failed:
  ; A timeout can interrupt the helper between renames. Retry its checkpoint
  ; recovery before returning a failed install; never launch a partial payload.
  !insertmacro ArkismRunInstallerHelper repair arkism_install_recovery_failed
  SetErrorLevel 1
  Abort "설치를 완료하지 못했습니다. 기존 설치를 보존했습니다."
arkism_install_recovery_failed:
  SetErrorLevel 1
  Abort "설치를 복구하지 못했습니다. 복구 기록을 보존했습니다. 설치 프로그램을 다시 실행해 주세요."
arkism_install_complete:
SectionEnd
!endif

!ifdef ARKISM_EMIT_UNINSTALLER
Section "Uninstall"
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=arkism-installer.exe "${STAGE_DIR}\runtime\arkism-installer.exe"
  !insertmacro ArkismRunInstallerHelper 'check "${VERSION}"' arkism_remove_failed
  !insertmacro ArkismRunInstallerHelper stop arkism_remove_failed
  ; Disconnect only after the app and watchers have verifiably stopped.
  nsExec::ExecToStack /TIMEOUT=120000 '"$INSTDIR\integration\arkism.exe" uninstall-arkism-windows'
  Pop $0
  Pop $1
  DetailPrint $1
  StrCmp $0 "0" +2
  Goto arkism_remove_failed
  ; The executable shares the settings command's per-user, transactional policy.
  ; Refuse removal if startup cleanup cannot be proven; keep the payload for repair.
  !insertmacro ArkismRemoveAutostart "$INSTDIR\${EXE}" arkism_autostart_failed
  Goto arkism_autostart_removed
arkism_autostart_failed:
  SetErrorLevel 1
  Abort "자동 시작 등록을 정리하지 못했습니다. Arkism을 복구한 뒤 제거를 다시 시도해 주세요."
arkism_autostart_removed:
  ; Receipt-bound file removal preserves unknown user files and directories.
  !insertmacro ArkismRunInstallerHelper 'remove-payload "${VERSION}"' arkism_remove_failed
  !insertmacro ArkismRunInstallerHelper 'remove-metadata "${VERSION}"' arkism_remove_failed
  Goto arkism_remove_complete
arkism_remove_failed:
  SetErrorLevel 1
  Abort "제거를 완료하지 못했습니다. 남은 파일과 설정을 보존했습니다."
arkism_remove_complete:
SectionEnd
!endif
