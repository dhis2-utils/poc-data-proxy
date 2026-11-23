; The DHIS2 Facility Hub on a Windows PC (docs/design/2026-10-20-windows-service-design.md).
; One installer for every country. It installs for the whole PC, as a Windows service, and
; needs administrator rights. (An install for one user, without them, was dropped on
; 22 November 2026: such a hub could serve only its own PC, while the user was logged in.)
; Silent: setup.exe /S
;
; Built on Linux by windows/build.sh: makensis -DVERSION=0.3.0 -DHUBEXE=/path/hub.exe -DOUTFILE=... installer.nsi

Unicode true
!ifndef VERSION
  !define VERSION "0.0.0-dev"
!endif
!define NAME "DHIS2 Facility Hub"
!define SERVICE "DHIS2FacilityHub"
!define ACCOUNT "NT SERVICE\DHIS2FacilityHub"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\DHIS2FacilityHub"
!define HOSTSMARK "# DHIS2 Facility Hub"

Name "${NAME}"
OutFile "${OUTFILE}"
SetCompressor /SOLID lzma
RequestExecutionLevel admin
InstallDir "$PROGRAMFILES64\${NAME}"

!include MUI2.nsh
!include LogicLib.nsh
!include WordFunc.nsh
!include FileFunc.nsh
!include x64.nsh

VIProductVersion "0.0.0.0"
VIAddVersionKey "ProductName" "${NAME}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "CompanyName" "HISP / DHIS2"
VIAddVersionKey "FileDescription" "${NAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "BSD-3-Clause, University of Oslo"

Var DataDir
Var HubExe
Var TpmNote ; on the finish page: whether the hub's keys are sealed by a TPM

Var LogFile

; Run a command and record it, its exit code and its output in install.log (the data
; folder; readable by every user): what a silent install did, for support and CI.
; Sets $0 to the exit code.
!macro Run cmd
  StrCpy $R7 '${cmd}'
  FileWrite $LogFile "> $R7$\r$\n" ; before running it: a step that dies still shows
  nsExec::ExecToStack '${cmd}'
  Pop $0
  Pop $R9
  FileWrite $LogFile "  exit $0: $R9$\r$\n"
!macroend
Var Url
Var CAName
Var ShownVersion ; the version in Windows' Apps list: of the hub.exe in place

!define MUI_WELCOMEPAGE_TEXT "The facility hub keeps DHIS2 working at this facility while the internet is down. Staff devices open it at dhis2.local.$\r$\n$\r$\nIt runs as a Windows service, whether or not anyone is logged in, and needs administrator rights to install. Windows 11 is supported.$\r$\n$\r$\nChoose a PC that stays on while staff work."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TEXT_LARGE
!define MUI_FINISHPAGE_TEXT "The hub runs. Open it on this PC with the desktop shortcut to set it up, or at https://localhost.$TpmNote"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_COMPONENTS
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Function .onInit
  SetShellVarContext all
  UserInfo::GetAccountType
  Pop $0
  ${If} $0 != "Admin"
    MessageBox MB_ICONSTOP "Installing the hub needs administrator rights." /SD IDOK
    SetErrorLevel 740 ; ERROR_ELEVATION_REQUIRED
    Quit
  ${EndIf}
  ${If} ${RunningX64}
    SetRegView 64
  ${Else}
    MessageBox MB_ICONSTOP "The hub needs 64-bit Windows." /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

Function un.onInit
  SetShellVarContext all
  SetRegView 64
FunctionEnd

; The data folder, in ProgramData. The hub runs from its bin folder there, so it can
; replace itself when it upgrades.
!macro SetDataDir
  SetShellVarContext all
  StrCpy $DataDir "$APPDATA\${NAME}"
  StrCpy $HubExe "$DataDir\bin\hub.exe"
!macroend

; Before anything in ProgramData runs or is trusted: ordinary users may create folders
; there, so a folder this installer did not make (not owned by Administrators or SYSTEM)
; is moved aside unused, and the folder is taken over for Administrators and SYSTEM only.
; The service's own account is added once the service exists.
Function TakeOverDataDir
  ${If} ${FileExists} "$DataDir\*.*"
    nsExec::ExecToStack `powershell -NoProfile -NonInteractive -Command "(Get-Acl -LiteralPath '$DataDir').GetOwner([System.Security.Principal.SecurityIdentifier]).Value"`
    Pop $0
    Pop $1
    ; No owner read (PowerShell failed or crashed): stop, nothing changed. Taken for a
    ; stranger's folder, a working hub was moved aside and a new empty one made (the
    ; Windows VM, 2026-11-20). Not logged in the folder, whose owner is unknown.
    ClearErrors
    ${WordFind} "$1" "S-1-" "E+1" $2
    ${If} ${Errors}
    ${OrIf} $0 != 0
      DetailPrint "Could not read the owner of $DataDir (exit $0: $1)."
      MessageBox MB_ICONSTOP "The installer could not check the hub's data folder ($0). Nothing was changed: run it again." /SD IDOK
      Abort
    ${EndIf}
    ${WordFind} "$1" "S-1-5-32-544" "E+1" $2
    ${If} ${Errors}
      ${WordFind} "$1" "S-1-5-18" "E+1" $2
      ${If} ${Errors}
        ${GetTime} "" "L" $3 $4 $5 $6 $7 $8 $9
        DetailPrint "Moving aside a hub folder this installer did not create (owner $1)."
        Rename "$DataDir" "$DataDir.untrusted-$5$4$3$7$8$9"
      ${EndIf}
    ${EndIf}
  ${EndIf}
  CreateDirectory "$DataDir"
  nsExec::ExecToLog 'icacls "$DataDir" /setowner *S-1-5-32-544 /T /C /Q'
  Pop $0
  ; The folder's own access list, not recursive: with /T, /inheritance:r strips each
  ; file's inherited entries while (OI)(CI) grants do not apply to files, leaving them
  ; with empty access lists (CI, 2026-10-20). Everything under it inherits instead.
  nsExec::ExecToLog 'icacls "$DataDir" /inheritance:r /grant:r "*S-1-5-32-544:(OI)(CI)F" "*S-1-5-18:(OI)(CI)F" /C /Q'
  Pop $0
  nsExec::ExecToLog 'icacls "$DataDir\*" /reset /T /C /Q'
  Pop $0
FunctionEnd

Function StopHub
  ; This installer's hub.exe, not the installed one: the service's account may change
  ; that, and this runs elevated (review R-27).
  InitPluginsDir
  File "/oname=$PLUGINSDIR\hub-new.exe" "${HUBEXE}"
  nsExec::ExecToLog '"$PLUGINSDIR\hub-new.exe" service stop' ; waits until it has stopped
  Pop $0
FunctionEnd

Section "!The hub" SecHub
  SectionIn RO
  !insertmacro SetDataDir
  Call TakeOverDataDir
  FileOpen $LogFile "$DataDir\install.log" a
  FileSeek $LogFile 0 END ; "a" opens at the start: without this a run wrote over the last one
  FileWrite $LogFile "$\r$\n== ${NAME} ${VERSION}$\r$\n"
  Call StopHub

  ; Never put an older hub over a newer one: it may have migrated the store already.
  ; The installed version is read, never asked of the installed hub.exe, which would run
  ; elevated (review R-27): the file the hub writes at each start, else what the last
  ; installer recorded (a hub from before that file).
  StrCpy $1 "install"
  StrCpy $2 ""
  StrCpy $ShownVersion "${VERSION}"
  ; This installer's hub.exe, for every elevated step below: never the installed one,
  ; which the service's account may change (review R-27).
  InitPluginsDir
  File "/oname=$PLUGINSDIR\hub-new.exe" "${HUBEXE}"
  ${If} ${FileExists} "$HubExe"
    ClearErrors
    FileOpen $R1 "$DataDir\version" r
    ${IfNot} ${Errors}
      FileRead $R1 $2
      FileClose $R1
      ${WordFind} "$2" "$\n" "+1" $2
    ${EndIf}
    ${If} $2 == ""
      ReadRegStr $2 SHCTX "${UNINSTKEY}" "DisplayVersion"
    ${EndIf}
  ${EndIf}
  ${If} $2 != ""
    ; The new hub.exe compares, by release precedence: NSIS's VersionCompare reads
    ; dotted numbers only, and would take 0.2.0 for older than 0.2.0-ci.57.
    nsExec::ExecToStack '"$PLUGINSDIR\hub-new.exe" version "$2"'
    Pop $0
    Pop $3
    ${WordFind} "$3" "$\n" "+1" $3
    ${If} $3 == "older"
      ; Recorded for support, not asked of whoever runs this: there is nothing for them
      ; to do. The Apps list shows the version that runs (PENDING.md, item 34).
      DetailPrint "Keeping the newer hub $2 already installed."
      FileWrite $LogFile "Kept the newer hub $2 already installed; this installer's ${VERSION} was not put in.$\r$\n"
      StrCpy $1 "keep"
      StrCpy $ShownVersion "$2"
    ${EndIf}
  ${EndIf}

  ; Settings, ports and the hub's certificate authority (hub init keeps existing ones),
  ; by this installer's hub.exe, also where a newer one was kept. Before the program
  ; is replaced: an install that stops here leaves the hub as it was (the Windows VM,
  ; 22 November 2026, R6).
  ; hub init tries the ports by listening on them, and Windows asks whether to let a
  ; program no rule names onto the network: every fresh install showed that prompt,
  ; for this temporary copy (the same run, R7 and R10). A rule naming it, removed
  ; again after, keeps the prompt away; the hub's own rules follow below.
  !insertmacro Run 'netsh advfirewall firewall add rule name="${NAME} (installer)" dir=in action=block program="$PLUGINSDIR\hub-new.exe" profile=any'
  !insertmacro Run '"$PLUGINSDIR\hub-new.exe" init -data "$DataDir"'
  StrCpy $7 $0
  !insertmacro Run 'netsh advfirewall firewall delete rule name="${NAME} (installer)"'
  ${If} $7 != 0
    ; For example data sealed by a TPM this PC no longer opens: hub init says so.
    MessageBox MB_ICONSTOP "The hub could not prepare its data folder ($7): $DataDir\install.log says why." /SD IDOK
    !insertmacro Run 'sc start ${SERVICE}' ; as it was: its start page says what is wrong
    FileClose $LogFile
    Abort
  ${EndIf}
  SetOutPath "$DataDir\bin"
  ${If} $1 == "install"
    File "/oname=hub.exe" "${HUBEXE}"
  ${EndIf}
  ReadINIStr $Url "$DataDir\install.ini" "hub" "url"
  ReadINIStr $CAName "$DataDir\install.ini" "hub" "ca_name"
  ReadINIStr $4 "$DataDir\install.ini" "hub" "https_port"
  ReadINIStr $5 "$DataDir\install.ini" "hub" "http_port"
  ; Whether a TPM seals the hub's keys: recommended, not required (docs/DEPLOYMENT.md,
  ; "TPM"). Said where the person installing sees it, and in install.log.
  ReadINIStr $6 "$DataDir\install.ini" "hub" "tpm"
  ${If} $6 == "sealed"
    FileWrite $LogFile "TPM: the hub's keys are sealed by this PC's TPM.$\r$\n"
  ${Else}
    StrCpy $TpmNote "$\r$\n$\r$\nThis PC has no usable TPM (security chip), so the hub's keys are not sealed: if the PC is stolen, only staff passwords protect its data. If the PC has a TPM, turn it on in the firmware settings; the hub uses it from its next start."
    FileWrite $LogFile "TPM: none usable (or tpm: false in hub.yaml); the hub's keys are not sealed.$\r$\n"
  ${EndIf}

  ; Trust the hub's authority on this PC, and dhis2.local is this PC.
  !insertmacro Run 'certutil -f -addstore Root "$DataDir\ca.crt"'
  Call AddHostsLine
  ; Devices on the network reach the hub: its two ports and mDNS (dhis2.local). Any
  ; profile (a facility's network is often "Public"), but only from the PC's own
  ; subnets, never from beyond a router.
  !insertmacro Run 'netsh advfirewall firewall delete rule name="${NAME}"'
  !insertmacro Run 'netsh advfirewall firewall add rule name="${NAME}" dir=in action=allow program="$HubExe" protocol=TCP localport=$4,$5 remoteip=localsubnet profile=any'
  !insertmacro Run 'netsh advfirewall firewall add rule name="${NAME}" dir=in action=allow program="$HubExe" protocol=UDP localport=5353 remoteip=localsubnet profile=any'
  ; The service, its account's rights on the data folder (and no one else's), then start.
  !insertmacro Run 'sc query ${SERVICE}'
  ${If} $0 != 0
    !insertmacro Run '"$PLUGINSDIR\hub-new.exe" service install -config "$DataDir\hub.yaml" -exe "$HubExe" -nostart'
  ${EndIf}
  !insertmacro Run 'icacls "$DataDir" /inheritance:r /grant:r "*S-1-5-32-544:(OI)(CI)F" "*S-1-5-18:(OI)(CI)F" "${ACCOUNT}:(OI)(CI)M" /Q'
  !insertmacro Run 'icacls "$DataDir\*" /reset /T /C /Q'
  ; The settings for installers and checks, and the authority devices install, are
  ; public: readable by every user of the PC (nothing secret in either).
  !insertmacro Run 'icacls "$DataDir\install.ini" /grant "*S-1-5-32-545:R"'
  !insertmacro Run 'icacls "$DataDir\ca.crt" /grant "*S-1-5-32-545:R"'
  !insertmacro Run 'sc start ${SERVICE}'
  Sleep 3000
  !insertmacro Run 'sc query ${SERVICE}'

  ; The shortcut opens the hub at the address every device uses.
  WriteINIStr "$DESKTOP\${NAME}.url" "InternetShortcut" "URL" "$Url"

  ; Windows' Apps list.
  SetOutPath "$INSTDIR"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr SHCTX "${UNINSTKEY}" "DisplayName" "${NAME}"
  WriteRegStr SHCTX "${UNINSTKEY}" "DisplayVersion" "$ShownVersion"
  WriteRegStr SHCTX "${UNINSTKEY}" "Publisher" "HISP / DHIS2"
  WriteRegStr SHCTX "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr SHCTX "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegStr SHCTX "${UNINSTKEY}" "InstallLocation" "$DataDir"
  WriteRegStr SHCTX "${UNINSTKEY}" "CAName" "$CAName"
  WriteRegDWORD SHCTX "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD SHCTX "${UNINSTKEY}" "NoRepair" 1
  FileClose $LogFile
  nsExec::Exec 'icacls "$DataDir\install.log" /grant "*S-1-5-32-545:R"'
  Pop $0
SectionEnd

Section "Keep this PC awake while plugged in" SecAwake
  ; The hub cannot serve devices while the PC sleeps.
  nsExec::Exec 'powercfg /change standby-timeout-ac 0'
  Pop $0
  nsExec::Exec 'powercfg /change hibernate-timeout-ac 0'
  Pop $0
SectionEnd

Section "Ask Microsoft Defender to skip the hub's data" SecDefender
  ; Scanning the database while the hub writes slows and can lock it. Best effort:
  ; other antivirus needs the same exclusion by hand (docs/WINDOWS-INSTALL.md).
  nsExec::Exec `powershell -NoProfile -NonInteractive -Command "Add-MpPreference -ExclusionPath '$DataDir'"`
  Pop $0
SectionEnd

Function AddHostsLine
  StrCpy $6 "$WINDIR\System32\drivers\etc\hosts"
  ClearErrors
  FileOpen $7 $6 r
  ${IfNot} ${Errors}
    ${Do}
      FileRead $7 $8
      ${If} ${Errors}
        ${ExitDo}
      ${EndIf}
      ${WordFind} "$8" "${HOSTSMARK}" "E+1" $9
      ${IfNot} ${Errors}
        FileClose $7
        Return
      ${EndIf}
    ${Loop}
    FileClose $7
  ${EndIf}
  FileOpen $7 $6 a
  FileSeek $7 0 END
  FileWrite $7 "$\r$\n127.0.0.1 dhis2.local ${HOSTSMARK}$\r$\n"
  FileClose $7
FunctionEnd

; ---------------------------------------------------------------- uninstall

Section "un.The hub" UnSecHub
  SectionIn RO
  !insertmacro SetDataDir
  ReadRegStr $CAName SHCTX "${UNINSTKEY}" "CAName"
  ; Stopping ends the hub cleanly; saves still waiting stay in its data folder
  ; (kept unless removed below) and go to central with the next hub installed here.
  ; Windows' own tools, not the installed hub.exe, which the service's account may
  ; change while this runs elevated (review R-27). Stop-Service waits for the stop.
  nsExec::ExecToLog `powershell -NoProfile -NonInteractive -Command "Stop-Service -Name '${SERVICE}' -ErrorAction SilentlyContinue"`
  Pop $0
  nsExec::ExecToLog 'sc delete ${SERVICE}'
  Pop $0
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${NAME}"'
  Pop $0
  ; Else any user could recreate the folder and hide files where Defender does not look.
  ; Checked, and tried once more: Defender may be starting or have crashed (the Windows
  ; VM, 2026-11-20, left the exclusion with nothing said).
  StrCpy $R0 `powershell -NoProfile -NonInteractive -Command "$$x = '$DataDir'; if ((Get-MpPreference -ErrorAction Stop).ExclusionPath -contains $$x) { Remove-MpPreference -ExclusionPath $$x -ErrorAction Stop; if ((Get-MpPreference -ErrorAction Stop).ExclusionPath -contains $$x) { exit 1 } }"`
  nsExec::Exec $R0
  Pop $0
  ${If} $0 != 0
    Sleep 10000
    nsExec::Exec $R0
    Pop $0
  ${EndIf}
  ${If} $0 != 0
    ; Also where another antivirus has turned Defender off: then there is nothing to remove.
    DetailPrint "Could not confirm that Microsoft Defender no longer skips $DataDir (exit $0)."
    ${If} ${FileExists} "$DataDir\install.log"
      FileOpen $R1 "$DataDir\install.log" a
      FileSeek $R1 0 END
      FileWrite $R1 "Uninstall: could not confirm that Microsoft Defender's exclusion of $DataDir is gone (exit $0); check Windows Security.$\r$\n"
      FileClose $R1
    ${EndIf}
    MessageBox MB_ICONEXCLAMATION "Could not confirm that Microsoft Defender no longer skips $DataDir. If Defender is on, remove that exclusion in Windows Security › Virus & threat protection › Exclusions." /SD IDOK
  ${EndIf}
  nsExec::Exec 'certutil -delstore Root "$CAName"'
  Pop $0
  Call un.RemoveHostsLine
  Delete "$DESKTOP\${NAME}.url"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey SHCTX "${UNINSTKEY}"
SectionEnd

Section /o "un.Remove the hub's data (unsent saves are lost)" UnSecData
  !insertmacro SetDataDir
  RMDir /r "$DataDir"
SectionEnd

; hosts without the hub's line, written to a temporary file first: the original is
; moved aside, the new one put in its place, and the original put back if that fails.
Function un.RemoveHostsLine
  StrCpy $6 "$WINDIR\System32\drivers\etc\hosts"
  ClearErrors
  FileOpen $7 $6 r
  ${If} ${Errors}
    Return
  ${EndIf}
  FileOpen $5 "$6.hub-tmp" w
  ${If} ${Errors}
    FileClose $7
    Return
  ${EndIf}
  ${Do}
    FileRead $7 $8
    ${If} ${Errors}
      ${ExitDo}
    ${EndIf}
    ${WordFind} "$8" "${HOSTSMARK}" "E+1" $9
    ${If} ${Errors}
      FileWrite $5 $8
    ${EndIf}
  ${Loop}
  FileClose $7
  FileClose $5
  Delete "$6.hub-old"
  ClearErrors
  Rename $6 "$6.hub-old"
  ${If} ${Errors}
    Delete "$6.hub-tmp"
    Return
  ${EndIf}
  Rename "$6.hub-tmp" $6
  ${If} ${Errors}
    Rename "$6.hub-old" $6
    Delete "$6.hub-tmp"
  ${Else}
    Delete "$6.hub-old"
  ${EndIf}
FunctionEnd
