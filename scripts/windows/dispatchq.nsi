; DispatchQ for Windows: per-user installer (no administrator rights).
; Built by scripts/build-windows.sh; not meant to be run through makensis by hand.
;
; Defines expected from the build script:
;   VERSION      product version string, e.g. dev-feature-windows-port-poc
;   STAGE        directory holding dispatchqapp.exe, dispatchqd.exe, dispatchq.exe,
;                dispatchq.ico, LICENSE, path-helper.ps1 [, webview2-bootstrapper.exe]
;   OUTFILE      installer to write
;   BOOTSTRAPPER defined when STAGE contains webview2-bootstrapper.exe

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

Name "DispatchQ"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\DispatchQ"
InstallDirRegKey HKCU "Software\DispatchQ" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
BrandingText "DispatchQ ${VERSION}"

VIProductVersion "0.0.0.0"
VIAddVersionKey "ProductName" "DispatchQ"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "DispatchQ installer"
VIAddVersionKey "LegalCopyright" "DispatchQ contributors"

!define MUI_ICON "${STAGE}\dispatchq.ico"
!define MUI_UNICON "${STAGE}\dispatchq.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\dispatchqapp.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Open DispatchQ"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${STAGE}\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

; Parameters: /PURGE (uninstaller) also deletes the data directory.
Var Purge

; Adds (ACTION=Add) or removes (ACTION=Remove) the install directory in the user's
; PATH. The directory is handed to the fixed script path-helper.ps1 through an
; environment variable and is never part of PowerShell source, so $, backticks or
; parentheses in a legal install path stay literal. A failure is reported.
!macro PathHelper ACTION
  System::Call 'Kernel32::SetEnvironmentVariableW(w "DISPATCHQ_INSTDIR", w "$INSTDIR")i.r0'
  nsExec::ExecToStack 'powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\path-helper.ps1" -Action ${ACTION}'
  Pop $0
  Pop $1
  ${If} $0 != 0
    DetailPrint "Updating the PATH failed (exit code $0): $1"
    MessageBox MB_OK|MB_ICONEXCLAMATION "DispatchQ could not update your PATH (${ACTION}): $1$\r$\nThe dispatchq command may not be available in new terminals. You can add $INSTDIR to your user PATH yourself." /SD IDOK
  ${EndIf}
!macroend

; The WebView2 runtime registers its version under this client id, per machine
; or per user.
!define WEBVIEW2_KEY "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Function StopRunning
  ; Upgrade or reinstall: the daemon holds the store lock and the dashboard port,
  ; and the window keeps the program files open.
  IfFileExists "$INSTDIR\dispatchqd.exe" 0 +2
    nsExec::Exec '"$INSTDIR\dispatchqd.exe" uninstall'
  nsExec::Exec 'taskkill /F /IM dispatchqapp.exe'
  nsExec::Exec 'taskkill /F /IM dispatchqd.exe'
  Sleep 800
FunctionEnd

Function EnsureWebView2
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\${WEBVIEW2_KEY}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "${WEBVIEW2_KEY}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
!ifdef BOOTSTRAPPER
    DetailPrint "Installing the Microsoft Edge WebView2 Runtime (needed for the DispatchQ window)"
    File "/oname=$PLUGINSDIR\webview2-bootstrapper.exe" "${STAGE}\webview2-bootstrapper.exe"
    ExecWait '"$PLUGINSDIR\webview2-bootstrapper.exe" /silent /install' $1
    DetailPrint "WebView2 installer exit code: $1"
!else
    MessageBox MB_OK|MB_ICONEXCLAMATION "DispatchQ needs the Microsoft Edge WebView2 Runtime, which was not found. Windows 11 includes it. Install it from https://developer.microsoft.com/microsoft-edge/webview2/ and start DispatchQ again."
!endif
  ${EndIf}
FunctionEnd

Section "DispatchQ" SecMain
  Call StopRunning
  SetOutPath "$INSTDIR"
  File "${STAGE}\dispatchqapp.exe"
  File "${STAGE}\dispatchqd.exe"
  File "${STAGE}\dispatchq.exe"
  File "${STAGE}\dispatchq.ico"
  File "${STAGE}\LICENSE"
  File "${STAGE}\path-helper.ps1"
  ; Earlier PoC builds installed first-start notes; drop a leftover copy.
  Delete "$INSTDIR\INSTALL-WINDOWS.txt"
  WriteUninstaller "$INSTDIR\Uninstall DispatchQ.exe"

  Call EnsureWebView2

  CreateDirectory "$SMPROGRAMS\DispatchQ"
  CreateShortcut "$SMPROGRAMS\DispatchQ\DispatchQ.lnk" "$INSTDIR\dispatchqapp.exe" "" "$INSTDIR\dispatchq.ico"
  CreateShortcut "$SMPROGRAMS\DispatchQ\Uninstall DispatchQ.lnk" "$INSTDIR\Uninstall DispatchQ.exe"

  ; Add the CLI to the user's PATH.
  !insertmacro PathHelper Add

  ; Register the daemon to start at every sign-in and start it now.
  nsExec::ExecToLog '"$INSTDIR\dispatchqd.exe" install'
  Pop $0
  ${If} $0 != 0
    DetailPrint "Registering the daemon failed (exit code $0)"
    MessageBox MB_OK|MB_ICONEXCLAMATION "DispatchQ could not register its background service for sign-in (exit code $0). Start DispatchQ from the Start menu." /SD IDOK
  ${EndIf}

  WriteRegStr HKCU "Software\DispatchQ" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "DisplayName" "DispatchQ"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "Publisher" "DispatchQ"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "DisplayIcon" "$INSTDIR\dispatchq.ico"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "UninstallString" '"$INSTDIR\Uninstall DispatchQ.exe"'
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "QuietUninstallString" '"$INSTDIR\Uninstall DispatchQ.exe" /S'
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ" "NoRepair" 1
SectionEnd

Function un.onInit
  ${GetParameters} $0
  ${GetOptions} $0 "/PURGE" $1
  ${IfNot} ${Errors}
    StrCpy $Purge "1"
  ${EndIf}
FunctionEnd

Section "Uninstall"
  ; Stops the daemon and removes its autostart entry, then closes the window.
  nsExec::Exec '"$INSTDIR\dispatchqd.exe" uninstall'
  nsExec::Exec 'taskkill /F /IM dispatchqapp.exe'
  nsExec::Exec 'taskkill /F /IM dispatchqd.exe'
  Sleep 800

  !insertmacro PathHelper Remove

  Delete "$SMPROGRAMS\DispatchQ\DispatchQ.lnk"
  Delete "$SMPROGRAMS\DispatchQ\Uninstall DispatchQ.lnk"
  RMDir "$SMPROGRAMS\DispatchQ"

  Delete "$INSTDIR\dispatchqapp.exe"
  Delete "$INSTDIR\dispatchqd.exe"
  Delete "$INSTDIR\dispatchq.exe"
  Delete "$INSTDIR\dispatchq.ico"
  Delete "$INSTDIR\LICENSE"
  ; Installed by earlier PoC builds.
  Delete "$INSTDIR\INSTALL-WINDOWS.txt"
  Delete "$INSTDIR\path-helper.ps1"
  Delete "$INSTDIR\Uninstall DispatchQ.exe"
  RMDir "$INSTDIR"

  ${If} $Purge == "1"
    ; DISPATCHQ_HOME, when set, is the user's own choice and is left alone.
    RMDir /r "$APPDATA\dispatchq"
  ${EndIf}

  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ"
  DeleteRegKey HKCU "Software\DispatchQ"
SectionEnd
