; The DHIS2 Facility Hub on a Windows PC (docs/superpowers/specs/2026-10-20-windows-service-design.md).
; One installer for every country. It installs either for the whole PC (a Windows service;
; needs administrator rights) or for the current user (runs while they are logged in).
; Silent: setup.exe /S /AllUsers   or   setup.exe /S /CurrentUser
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
!define RUNKEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define HOSTSMARK "# DHIS2 Facility Hub"

Name "${NAME}"
OutFile "${OUTFILE}"
SetCompressor /SOLID lzma
RequestExecutionLevel user ; MultiUser asks for administrator rights for the whole-PC install

!define MULTIUSER_EXECUTIONLEVEL Highest
!define MULTIUSER_MUI
!define MULTIUSER_INSTALLMODE_COMMANDLINE
!define MULTIUSER_USE_PROGRAMFILES64
!define MULTIUSER_INSTALLMODE_INSTDIR "${NAME}"
!define MULTIUSER_INSTALLMODE_DEFAULT_CURRENTUSER ; without rights the choice is made for them
!include MultiUser.nsh
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
Var UserAppDataLocal
Var Url
Var CAName

!define MUI_WELCOMEPAGE_TEXT "The facility hub keeps DHIS2 working at this facility while the internet is down. Staff devices open it at dhis2.local.$\r$\n$\r$\nInstall it for the whole PC (recommended: it runs whether or not anyone is logged in; needs administrator rights) or for yourself only (it runs while you are logged in).$\r$\n$\r$\nChoose a PC that stays on while staff work."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MULTIUSER_PAGE_INSTALLMODE
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TEXT "The hub runs. Open it on this PC with the desktop shortcut to set it up, or at https://localhost."
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_COMPONENTS
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Function .onInit
  !insertmacro MULTIUSER_INIT
  ${If} ${RunningX64}
    SetRegView 64
  ${Else}
    MessageBox MB_ICONSTOP "The hub needs 64-bit Windows." /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

Function un.onInit
  !insertmacro MULTIUSER_UNINIT
  SetRegView 64
FunctionEnd

; The data folder: ProgramData for the whole PC, the user's local folder otherwise. The
; hub runs from its bin folder there, so it can replace itself when it upgrades.
!macro SetDataDir
  ${If} $MultiUser.InstallMode == "AllUsers"
    SetShellVarContext all
    StrCpy $DataDir "$APPDATA\${NAME}"
  ${Else}
    SetShellVarContext current
    StrCpy $DataDir "$LOCALAPPDATA\${NAME}"
  ${EndIf}
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
  nsExec::ExecToLog 'icacls "$DataDir" /inheritance:r /grant:r "*S-1-5-32-544:(OI)(CI)F" "*S-1-5-18:(OI)(CI)F" /T /C /Q'
  Pop $0
FunctionEnd

Function StopHub
  ${If} $MultiUser.InstallMode == "AllUsers"
    ${If} ${FileExists} "$HubExe"
      nsExec::ExecToLog '"$HubExe" service stop' ; waits until it has stopped
      Pop $0
    ${EndIf}
  ${Else}
    ReadEnvStr $R0 USERNAME
    nsExec::Exec 'taskkill /F /FI "USERNAME eq $R0" /IM hub.exe'
    Pop $0
  ${EndIf}
FunctionEnd

Section "!The hub" SecHub
  SectionIn RO
  !insertmacro SetDataDir
  ${If} $MultiUser.InstallMode == "AllUsers"
    Call TakeOverDataDir
    ; The installing user's own local folder (in this mode $LOCALAPPDATA is ProgramData).
    SetShellVarContext current
    StrCpy $UserAppDataLocal "$LOCALAPPDATA"
    SetShellVarContext all
  ${EndIf}
  Call StopHub

  ; From a per-user hub to the whole PC: the installing user's hub moves along, so it
  ; keeps its ID, approval and certificate authority (devices notice nothing).
  ${If} $MultiUser.InstallMode == "AllUsers"
  ${AndIf} ${FileExists} "$UserAppDataLocal\${NAME}\ca.key"
  ${AndIfNot} ${FileExists} "$DataDir\ca.key"
    MessageBox MB_YESNO "Your own hub on this PC (installed for you only) can move to the whole-PC install, keeping its set-up. Move it?" /SD IDYES IDNO nomove
      ReadEnvStr $R0 USERNAME
    nsExec::Exec 'taskkill /F /FI "USERNAME eq $R0" /IM hub.exe'
      Pop $0
      DeleteRegValue HKCU "${RUNKEY}" "${NAME}"
      CreateDirectory "$DataDir"
      Delete "$UserAppDataLocal\${NAME}\hub.yaml" ; written again for the new place
      nsExec::Exec 'robocopy "$UserAppDataLocal\${NAME}" "$DataDir" /E /MOVE /NFL /NDL /NJH /NJS'
      Pop $0
    nomove:
  ${EndIf}

  ; Never put an older hub over a newer one: it may have migrated the store already.
  StrCpy $1 "install"
  ${If} ${FileExists} "$HubExe"
    nsExec::ExecToStack '"$HubExe" version'
    Pop $0
    Pop $2
    ${WordFind} "$2" "$\n" "+1" $2
    ${VersionCompare} "$2" "${VERSION}" $3
    ${If} $3 == 1
      DetailPrint "Keeping the newer hub $2 already installed."
      StrCpy $1 "keep"
    ${EndIf}
  ${EndIf}
  SetOutPath "$DataDir\bin"
  ${If} $1 == "install"
    File "/oname=hub.exe" "${HUBEXE}"
  ${EndIf}

  ; Settings, ports and the hub's certificate authority (hub init keeps existing ones).
  ${If} $MultiUser.InstallMode == "AllUsers"
    nsExec::ExecToLog '"$HubExe" init -data "$DataDir"'
  ${Else}
    nsExec::ExecToLog '"$HubExe" init -data "$DataDir" -per-user'
  ${EndIf}
  Pop $0
  ${If} $0 != 0
    MessageBox MB_ICONSTOP "The hub could not prepare its data folder ($0)." /SD IDOK
    Abort
  ${EndIf}
  ReadINIStr $Url "$DataDir\install.ini" "hub" "url"
  ReadINIStr $CAName "$DataDir\install.ini" "hub" "ca_name"
  ReadINIStr $4 "$DataDir\install.ini" "hub" "https_port"
  ReadINIStr $5 "$DataDir\install.ini" "hub" "http_port"

  ${If} $MultiUser.InstallMode == "AllUsers"
    ; Trust the hub's authority on this PC, and dhis2.local is this PC.
    nsExec::ExecToLog 'certutil -f -addstore Root "$DataDir\ca.crt"'
    Pop $0
    Call AddHostsLine
    ; Devices on the network reach the hub: its two ports and mDNS (dhis2.local).
    nsExec::Exec 'netsh advfirewall firewall delete rule name="${NAME}"'
    Pop $0
    nsExec::ExecToLog 'netsh advfirewall firewall add rule name="${NAME}" dir=in action=allow program="$HubExe" protocol=TCP localport=$4,$5 profile=any'
    Pop $0
    nsExec::ExecToLog 'netsh advfirewall firewall add rule name="${NAME}" dir=in action=allow program="$HubExe" protocol=UDP localport=5353 profile=any'
    Pop $0
    ; The service, its account's rights on the data folder (and no one else's), then start.
    nsExec::Exec 'sc query ${SERVICE}'
    Pop $0
    ${If} $0 != 0
      nsExec::ExecToLog '"$HubExe" service install -config "$DataDir\hub.yaml" -exe "$HubExe" -nostart'
      Pop $0
    ${EndIf}
    nsExec::ExecToLog 'icacls "$DataDir" /inheritance:r /grant:r "*S-1-5-32-544:(OI)(CI)F" "*S-1-5-18:(OI)(CI)F" "${ACCOUNT}:(OI)(CI)M" /T /Q'
    Pop $0
    ; The settings for installers and checks, and the authority devices install, are
    ; public: readable by every user of the PC (nothing secret in either).
    nsExec::ExecToLog 'icacls "$DataDir\install.ini" /grant "*S-1-5-32-545:R"'
    Pop $0
    nsExec::ExecToLog 'icacls "$DataDir\ca.crt" /grant "*S-1-5-32-545:R"'
    Pop $0
    nsExec::ExecToLog 'sc start ${SERVICE}'
    Pop $0
  ${Else}
    ; Trust the authority for this user (Windows asks once), and start with their login.
    nsExec::ExecToLog 'certutil -user -f -addstore Root "$DataDir\ca.crt"'
    Pop $0
    WriteRegStr HKCU "${RUNKEY}" "${NAME}" '"$HubExe" serve -config "$DataDir\hub.yaml"'
    Exec '"$HubExe" serve -config "$DataDir\hub.yaml"'
  ${EndIf}

  ; The shortcut opens the hub at the address every device uses.
  WriteINIStr "$DESKTOP\${NAME}.url" "InternetShortcut" "URL" "$Url"

  ; Windows' Apps list.
  SetOutPath "$INSTDIR"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr SHCTX "${UNINSTKEY}" "DisplayName" "${NAME}"
  WriteRegStr SHCTX "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr SHCTX "${UNINSTKEY}" "Publisher" "HISP / DHIS2"
  WriteRegStr SHCTX "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe" /$MultiUser.InstallMode'
  WriteRegStr SHCTX "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /$MultiUser.InstallMode /S'
  WriteRegStr SHCTX "${UNINSTKEY}" "InstallLocation" "$DataDir"
  WriteRegStr SHCTX "${UNINSTKEY}" "CAName" "$CAName"
  WriteRegDWORD SHCTX "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD SHCTX "${UNINSTKEY}" "NoRepair" 1
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
  ${If} $MultiUser.InstallMode == "AllUsers"
    nsExec::Exec `powershell -NoProfile -NonInteractive -Command "Add-MpPreference -ExclusionPath '$DataDir'"`
    Pop $0
  ${EndIf}
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
  ${If} $MultiUser.InstallMode == "AllUsers"
    ; Stopping ends the hub cleanly; saves still waiting stay in its data folder
    ; (kept unless removed below) and go to central with the next hub installed here.
    nsExec::ExecToLog '"$HubExe" service uninstall'
    Pop $0
    nsExec::Exec 'netsh advfirewall firewall delete rule name="${NAME}"'
    Pop $0
    ; Else any user could recreate the folder and hide files where Defender does not look.
    nsExec::Exec `powershell -NoProfile -NonInteractive -Command "Remove-MpPreference -ExclusionPath '$DataDir'"`
    Pop $0
    nsExec::Exec 'certutil -delstore Root "$CAName"'
    Pop $0
    Call un.RemoveHostsLine
  ${Else}
    Call un.StopHub
    DeleteRegValue HKCU "${RUNKEY}" "${NAME}"
    nsExec::Exec 'certutil -user -delstore Root "$CAName"'
    Pop $0
  ${EndIf}
  Delete "$DESKTOP\${NAME}.url"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey SHCTX "${UNINSTKEY}"
SectionEnd

Section /o "un.Remove the hub's data (unsent saves are lost)" UnSecData
  !insertmacro SetDataDir
  RMDir /r "$DataDir"
SectionEnd

Function un.StopHub
  ReadEnvStr $R0 USERNAME
  nsExec::Exec 'taskkill /F /FI "USERNAME eq $R0" /IM hub.exe'
  Pop $0
FunctionEnd

Function un.RemoveHostsLine
  StrCpy $6 "$WINDIR\System32\drivers\etc\hosts"
  ClearErrors
  FileOpen $7 $6 r
  ${If} ${Errors}
    Return
  ${EndIf}
  FileOpen $5 "$6.hub-tmp" w
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
  Delete $6
  Rename "$6.hub-tmp" $6
FunctionEnd
