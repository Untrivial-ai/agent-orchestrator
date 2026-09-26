!include "LogicLib.nsh"
!include "WinMessages.nsh"

!define AO_CLI_PATH "$INSTDIR\resources\daemon"
!define AO_MACHINE_ENV_KEY "SYSTEM\CurrentControlSet\Control\Session Manager\Environment"
!define AO_USER_ENV_KEY "Environment"

; Append AO's bundled CLI directory to one persisted PATH value. Token-by-token
; comparison avoids treating C:\ao as already present merely because
; C:\ao-tools exists. StrCmp is case-insensitive, matching Windows path rules.
!macro aoAddCliPath ROOT_KEY ENV_KEY LABEL_PREFIX
  StrCpy $0 ""
  ReadRegStr $0 ${ROOT_KEY} "${ENV_KEY}" "Path"
  StrCpy $1 "${AO_CLI_PATH}"
  StrCpy $2 $0

  ${LABEL_PREFIX}_next:
    StrCpy $3 0
  ${LABEL_PREFIX}_scan:
    StrCpy $4 $2 1 $3
    StrCmp $4 "" ${LABEL_PREFIX}_last
    StrCmp $4 ";" ${LABEL_PREFIX}_token
    IntOp $3 $3 + 1
    Goto ${LABEL_PREFIX}_scan

  ${LABEL_PREFIX}_token:
    StrCpy $4 $2 $3
    StrCmp $4 $1 ${LABEL_PREFIX}_done
    IntOp $3 $3 + 1
    StrCpy $2 $2 "" $3
    Goto ${LABEL_PREFIX}_next

  ${LABEL_PREFIX}_last:
    StrCmp $2 $1 ${LABEL_PREFIX}_done
    StrCmp $0 "" ${LABEL_PREFIX}_write_only
    StrLen $3 $0
    IntOp $3 $3 - 1
    StrCpy $4 $0 1 $3
    StrCmp $4 ";" ${LABEL_PREFIX}_write_joined
    StrCpy $0 "$0;"
  ${LABEL_PREFIX}_write_joined:
    StrCpy $0 "$0$1"
    Goto ${LABEL_PREFIX}_write
  ${LABEL_PREFIX}_write_only:
    StrCpy $0 $1
  ${LABEL_PREFIX}_write:
    WriteRegExpandStr ${ROOT_KEY} "${ENV_KEY}" "Path" $0
  ${LABEL_PREFIX}_done:
!macroend

; Remove only AO's exact CLI directory, preserving every other PATH token and
; its order. Updates are safe: the old uninstaller removes its entry and the
; new install hook adds the current $INSTDIR entry once.
!macro aoRemoveCliPath ROOT_KEY ENV_KEY LABEL_PREFIX
  StrCpy $0 ""
  ReadRegStr $0 ${ROOT_KEY} "${ENV_KEY}" "Path"
  StrCpy $1 "${AO_CLI_PATH}"
  StrCpy $2 0
  StrCpy $3 0
  ${LABEL_PREFIX}_scan:
    StrCpy $4 $0 1 $3
    StrCmp $4 "" ${LABEL_PREFIX}_last
    StrCmp $4 ";" ${LABEL_PREFIX}_token
    IntOp $3 $3 + 1
    Goto ${LABEL_PREFIX}_scan

  ${LABEL_PREFIX}_token:
    IntOp $5 $3 - $2
    StrCpy $4 $0 $5 $2
    StrCmp $4 $1 ${LABEL_PREFIX}_remove_middle
    IntOp $3 $3 + 1
    StrCpy $2 $3
    Goto ${LABEL_PREFIX}_scan

  ${LABEL_PREFIX}_last:
    IntOp $5 $3 - $2
    StrCpy $4 $0 $5 $2
    StrCmp $4 $1 ${LABEL_PREFIX}_remove_last ${LABEL_PREFIX}_unchanged

  ; For a middle token, remove it and its following semicolon. For the final
  ; token, remove its preceding semicolon. This preserves every unrelated PATH
  ; byte, including intentional empty entries and variable references.
  ${LABEL_PREFIX}_remove_middle:
    StrCpy $4 $0 $2
    IntOp $3 $3 + 1
    StrCpy $5 $0 "" $3
    StrCpy $0 "$4$5"
    Goto ${LABEL_PREFIX}_write
  ${LABEL_PREFIX}_remove_last:
    StrCmp $2 0 ${LABEL_PREFIX}_remove_only
    IntOp $2 $2 - 1
    StrCpy $0 $0 $2
    Goto ${LABEL_PREFIX}_write
  ${LABEL_PREFIX}_remove_only:
    StrCpy $0 ""
  ${LABEL_PREFIX}_write:
    ${If} $0 == ""
      DeleteRegValue ${ROOT_KEY} "${ENV_KEY}" "Path"
    ${Else}
      WriteRegExpandStr ${ROOT_KEY} "${ENV_KEY}" "Path" $0
    ${EndIf}
  ${LABEL_PREFIX}_unchanged:
!macroend

!macro customInstall
  ${If} $installMode == "all"
    !insertmacro aoAddCliPath HKLM "${AO_MACHINE_ENV_KEY}" ao_add_machine
  ${Else}
    !insertmacro aoAddCliPath HKCU "${AO_USER_ENV_KEY}" ao_add_user
  ${EndIf}
  SendMessage ${HWND_BROADCAST} ${WM_SETTINGCHANGE} 0 "STR:Environment" /TIMEOUT=5000
!macroend

!macro customUnInstall
  ${If} $installMode == "all"
    !insertmacro aoRemoveCliPath HKLM "${AO_MACHINE_ENV_KEY}" ao_remove_machine
  ${Else}
    !insertmacro aoRemoveCliPath HKCU "${AO_USER_ENV_KEY}" ao_remove_user
  ${EndIf}
  SendMessage ${HWND_BROADCAST} ${WM_SETTINGCHANGE} 0 "STR:Environment" /TIMEOUT=5000
!macroend
