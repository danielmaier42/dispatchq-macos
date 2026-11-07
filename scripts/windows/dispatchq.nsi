; DispatchQ for Windows: per-user installer (no administrator rights).
; Built by scripts/build-windows.sh; not meant to be run through makensis by hand.
;
; Defines expected from the build script:
;   VERSION      product version string, e.g. dev-feature-windows-port-poc
;   ARCH         payload architecture: x64 or arm64
;   STAGE        directory holding dispatchqapp.exe, dispatchqd.exe, dispatchq.exe,
;                dispatchq.ico, LICENSE, path-helper.ps1 [, webview2-bootstrapper.exe]
;   OUTFILE      installer to write
;   BOOTSTRAPPER defined when STAGE contains webview2-bootstrapper.exe

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "x64.nsh"

; NSIS only builds x86 installer stubs. The stub runs natively on x64 Windows and
; under emulation on Windows on ARM; the payload it installs is native to ARCH.
!if "${ARCH}" != "x64"
!if "${ARCH}" != "arm64"
!error "ARCH must be x64 or arm64"
!endif
!endif

Name "DispatchQ"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\DispatchQ"
InstallDirRegKey HKCU "Software\DispatchQ" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
BrandingText "DispatchQ ${VERSION} (${ARCH})"

VIProductVersion "0.0.0.0"
VIAddVersionKey "ProductName" "DispatchQ"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "DispatchQ installer (Windows ${ARCH})"
VIAddVersionKey "LegalCopyright" "DispatchQ contributors"

!define MUI_ICON "${STAGE}\dispatchq.ico"
!define MUI_UNICON "${STAGE}\dispatchq.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\dispatchqapp.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Open DispatchQ"
!define MUI_ABORTWARNING_TEXT "Cancel the DispatchQ setup?"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${STAGE}\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_PAGE_CUSTOMFUNCTION_PRE SkipFinishAfterFailure
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

; Parameters: /PURGE (uninstaller) also deletes the data directory.
Var Purge
; "1" once the installation stopped without completing.
Var InstallFailed

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

; Where the WebView2 Runtime documents its version ("pv"), per machine or per
; user. Its per-machine entry is in the 32-bit registry view (stored under
; WOW6432Node on 64-bit Windows); this key is the logical path, read through
; SetRegView, never the physical WOW6432Node path Windows reserves.
!define WEBVIEW2_CLIENTS "SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

; Refuses a machine the payload cannot run on. ARM64 executables need Windows on
; ARM; x64 executables run on x64 Windows and, emulated, on Windows 11 on ARM.
Function .onInit
!if "${ARCH}" == "arm64"
  ${IfNot} ${IsNativeARM64}
    MessageBox MB_OK|MB_ICONSTOP "This DispatchQ installer is for Windows on ARM (ARM64). Use the x64 installer on this PC." /SD IDOK
    Abort
  ${EndIf}
!else
  ${IfNot} ${IsNativeAMD64}
  ${AndIfNot} ${IsNativeARM64}
    MessageBox MB_OK|MB_ICONSTOP "DispatchQ needs 64-bit Windows." /SD IDOK
    Abort
  ${EndIf}
!endif
FunctionEnd

Function StopRunning
  ; Upgrade or reinstall: the daemon holds the store lock and the dashboard port,
  ; and the window keeps the program files open. This runs before anything is
  ; changed, from a copy of the new dispatchqd in the installer's temporary
  ; folder: the installed dispatchqd may be an older build whose uninstall
  ; ends every dispatchqd by name, so it is never run. The copy stops this
  ; folder's window and the daemon that owns the data directory after
  ; checking that it runs from this folder (or the folder the installer last
  ; recorded). Nothing is ended by image name: another installation, or a
  ; daemon for another DISPATCHQ_HOME, keeps running. When the data directory
  ; is used by such a process, or its owner cannot be identified, setup
  ; stops here with the reason and changes nothing.
  StrCpy $InstallFailed ""
  InitPluginsDir
  File "/oname=$PLUGINSDIR\dispatchqd-setup.exe" "${STAGE}\dispatchqd.exe"
  retry:
  nsExec::ExecToStack '"$PLUGINSDIR\dispatchqd-setup.exe" stop-installation --dir "$INSTDIR"'
  Pop $0
  Pop $1
  ${If} $0 != 0
    DetailPrint "DispatchQ could not be stopped safely (exit code $0): $1"
    MessageBox MB_RETRYCANCEL|MB_ICONSTOP "Setup has not changed anything and cannot continue, because the running DispatchQ cannot be stopped safely:$\r$\n$\r$\n$1$\r$\nDo what the message says, then choose Retry. Cancel leaves everything as it is." /SD IDCANCEL IDRETRY retry
    StrCpy $InstallFailed "1"
  ${EndIf}
FunctionEnd

; SkipFinishAfterFailure skips the finish page, and with it the offer to open
; DispatchQ, when the installation did not complete.
Function SkipFinishAfterFailure
  ${If} $InstallFailed == "1"
    Abort
  ${EndIf}
FunctionEnd

; ReadWebView2Registration leaves the WebView2 Runtime version registered in
; $R0, or "" when there is none. An empty or 0.0.0.0 entry is stale and the
; next location is read: per machine in the 32-bit view, per machine in the
; 64-bit view, per user. Only used when CheckWebView2 cannot start its check;
; a registration says nothing about whether the window can load the runtime.
Function ReadWebView2Registration
  SetRegView 32
  ReadRegStr $R0 HKLM "${WEBVIEW2_CLIENTS}" "pv"
  ${If} $R0 == ""
  ${OrIf} $R0 == "0.0.0.0"
    SetRegView 64
    ReadRegStr $R0 HKLM "${WEBVIEW2_CLIENTS}" "pv"
    SetRegView 32
  ${EndIf}
  ${If} $R0 == ""
  ${OrIf} $R0 == "0.0.0.0"
    ReadRegStr $R0 HKCU "${WEBVIEW2_CLIENTS}" "pv"
  ${EndIf}
  ${If} $R0 == "0.0.0.0"
    StrCpy $R0 ""
  ${EndIf}
FunctionEnd

; CheckWebView2 sets $R1 to "yes" when the DispatchQ window will find a usable
; WebView2 Runtime and to "no" otherwise, and leaves what was found in $R2.
; The installed dispatchqd (this build, native to the payload's architecture
; like the window) repeats the window's own lookup and loads the runtime's
; client library in a separate process it ends after 20 seconds: exit code 0
; usable, 10 not usable or not confirmed. The check is ended after 60 seconds;
; a check that did not finish or failed counts as not confirmed, never as
; usable. Only when dispatchqd cannot be started at all does the documented
; registration decide.
Function CheckWebView2
  nsExec::ExecToStack /TIMEOUT=60000 '"$INSTDIR\dispatchqd.exe" webview2-status'
  Pop $R1
  Pop $R2
  ${If} $R1 == "0"
    StrCpy $R1 "yes"
  ${ElseIf} $R1 == "10"
    StrCpy $R1 "no"
  ${ElseIf} $R1 == "error"
    DetailPrint "The WebView2 check could not be started"
    Call ReadWebView2Registration
    ${If} $R0 == ""
      StrCpy $R1 "no"
      StrCpy $R2 "No WebView2 Runtime registration was found (the window's own check could not be started)."
    ${Else}
      StrCpy $R1 "yes"
      StrCpy $R2 "WebView2 Runtime $R0 is registered (the window's own check could not be started)."
    ${EndIf}
  ${Else}
    StrCpy $R2 "The WebView2 check did not complete (result: $R1), so it is not confirmed that the window can load the runtime. $R2"
    StrCpy $R1 "no"
  ${EndIf}
  DetailPrint "$R2"
FunctionEnd

; EnsureWebView2 installs the runtime only when the window would not find a
; confirmed usable one. The installation of DispatchQ continues either way:
; the daemon and CLI do not need WebView2, only the window does. The
; bootstrapper's exit code alone decides nothing: the check runs again
; afterwards, and a runtime still not confirmed usable is reported, with the
; exit code (decimal and hex) and what the check found. The bootstrapper
; installs a missing runtime; it is not known to repair a damaged one, and the
; message does not say it does.
Function EnsureWebView2
  Call CheckWebView2
  ${If} $R1 == "yes"
    Return
  ${EndIf}
!ifdef BOOTSTRAPPER
  DetailPrint "Installing the Microsoft Edge WebView2 Runtime (needed for the DispatchQ window)"
  File "/oname=$PLUGINSDIR\webview2-bootstrapper.exe" "${STAGE}\webview2-bootstrapper.exe"
  ClearErrors
  ExecWait '"$PLUGINSDIR\webview2-bootstrapper.exe" /silent /install' $1
  ${If} ${Errors}
    StrCpy $1 "could not be started"
  ${Else}
    IntFmt $R3 "0x%08X" $1
    StrCpy $1 "exit code $1, $R3"
  ${EndIf}
  DetailPrint "WebView2 installer result: $1"
  Call CheckWebView2
  ${If} $R1 != "yes"
    MessageBox MB_OK|MB_ICONEXCLAMATION "The Microsoft Edge WebView2 Runtime is still not confirmed usable after its installer ran (installer result: $1), so the DispatchQ window may not open. The background service and the dispatchq command work without it.$\r$\n$\r$\nWhat setup found: $R2$\r$\n$\r$\nIf the runtime is missing, install the Evergreen runtime from https://developer.microsoft.com/microsoft-edge/webview2/ (it needs an internet connection; an organisation may have to allow it), then start DispatchQ again. Setup does not repair an installed runtime that cannot be loaded. To check again later, run dispatchqd webview2-status in a new terminal." /SD IDOK
  ${EndIf}
!else
  MessageBox MB_OK|MB_ICONEXCLAMATION "DispatchQ needs the Microsoft Edge WebView2 Runtime, which is not confirmed usable for the window:$\r$\n$\r$\n$R2$\r$\n$\r$\nWindows 11 includes it. Install it from https://developer.microsoft.com/microsoft-edge/webview2/ and start DispatchQ again." /SD IDOK
!endif
FunctionEnd

Section "DispatchQ" SecMain
  Call StopRunning
  ${If} $InstallFailed == "1"
    SetErrorLevel 2
    Abort "Setup stopped before changing anything: the running DispatchQ could not be stopped safely."
  ${EndIf}
  SetOutPath "$INSTDIR"
  File "${STAGE}\dispatchqapp.exe"
  File "${STAGE}\dispatchqd.exe"
  File "${STAGE}\dispatchq.exe"
  File "${STAGE}\dispatchq.ico"
  File "${STAGE}\LICENSE"
  File "${STAGE}\path-helper.ps1"
  ; The Linux helper for providers that run in WSL, for both processor
  ; architectures; only the one matching a distribution is ever started.
  SetOutPath "$INSTDIR\wsl"
  File "${STAGE}\wsl\dispatchq-wsl-linux-amd64"
  File "${STAGE}\wsl\dispatchq-wsl-linux-arm64"
  SetOutPath "$INSTDIR"
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
  nsExec::ExecToStack '"$INSTDIR\dispatchqd.exe" install'
  Pop $0
  Pop $1
  ${If} $0 != 0
    DetailPrint "Registering the daemon failed (exit code $0): $1"
    MessageBox MB_OK|MB_ICONSTOP "DispatchQ could not register and start its background service (exit code $0):$\r$\n$\r$\n$1$\r$\nThe installation is incomplete. Do what the message says and run setup again." /SD IDOK
    StrCpy $InstallFailed "1"
    SetErrorLevel 2
    Abort "Setup stopped: the background service could not be registered."
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
  ; Closes this installation's window, stops its daemon and then removes the
  ; autostart entry. The installed dispatchqd is the build this uninstaller
  ; came with. Nothing is ended by image name. When the data directory is
  ; used by another installation, or its owner cannot be identified, nothing
  ; is removed.
  ${If} ${FileExists} "$INSTDIR\dispatchqd.exe"
    retry:
    nsExec::ExecToStack '"$INSTDIR\dispatchqd.exe" uninstall'
    Pop $0
    Pop $1
    ${If} $0 != 0
      DetailPrint "DispatchQ could not be stopped safely (exit code $0): $1"
      MessageBox MB_RETRYCANCEL|MB_ICONSTOP "DispatchQ was not removed, because it cannot be stopped safely:$\r$\n$\r$\n$1$\r$\nDo what the message says, then choose Retry. Cancel leaves DispatchQ installed." /SD IDCANCEL IDRETRY retry
      SetErrorLevel 2
      Abort "Uninstall stopped before removing anything."
    ${EndIf}
  ${EndIf}

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
  Delete "$INSTDIR\wsl\dispatchq-wsl-linux-amd64"
  Delete "$INSTDIR\wsl\dispatchq-wsl-linux-arm64"
  RMDir "$INSTDIR\wsl"
  Delete "$INSTDIR\Uninstall DispatchQ.exe"
  RMDir "$INSTDIR"

  ${If} $Purge == "1"
    ; DISPATCHQ_HOME, when set, is the user's own choice and is left alone.
    RMDir /r "$APPDATA\dispatchq"
  ${EndIf}

  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DispatchQ"
  DeleteRegKey HKCU "Software\DispatchQ"
SectionEnd
