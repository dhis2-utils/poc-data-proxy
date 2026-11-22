#!/usr/bin/env bash
# Checks a built Windows installer on Linux: its signature and that of hub.exe inside,
# that it carries hub.exe for windows/amd64, and that its script does what the design
# says (the service, the firewall, the trust store, dhis2.local). Running it is for a
# Windows machine (docs/TESTING.md, "Windows").
#
#   windows/check.sh out/dhis2-hub-0.3.0-setup.exe [ca-file]
set -uo pipefail
SETUP=$1
CA=${2:-${SIGN_CA:-$HOME/.cache/hub-signing/test-ca.crt}}
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
FAILS=0
ok() { echo "ok   $*"; }
bad() { echo "FAIL $*"; FAILS=$((FAILS + 1)); }
check() { local what=$1; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }

check "the installer exists" test -s "$SETUP"
check "the installer is signed" osslsigncode verify -CAfile "$CA" -in "$SETUP"
SEVENZ=$(command -v 7z || command -v 7zz || true) # p7zip on Linux, Homebrew's sevenzip on macOS
"$SEVENZ" x -y -o"$WORK/x" "$SETUP" >/dev/null 2>&1
HUB=$(find "$WORK/x" -iname hub.exe | head -1)
check "it carries hub.exe" test -n "$HUB"
check "hub.exe is signed" osslsigncode verify -CAfile "$CA" -in "$HUB"
check "hub.exe is a 64-bit Windows program" python3 -c "
import struct, sys
b = open('$HUB', 'rb').read(4096)
pe = struct.unpack_from('<I', b, 0x3c)[0]
sys.exit(0 if b[:2] == b'MZ' and b[pe:pe+4] == b'PE\0\0' and struct.unpack_from('<H', b, pe + 4)[0] == 0x8664 else 1)"
check "hub.exe has no console window (GUI subsystem)" python3 -c "
import struct, sys
b = open('$HUB', 'rb').read(4096)
pe = struct.unpack_from('<I', b, 0x3c)[0]
sys.exit(0 if struct.unpack_from('<H', b, pe + 24 + 68)[0] == 2 else 1)"
# What the installer does: its script, installer.nsi next to this file. NSIS compresses
# the compiled script, and 7-Zip versions differ in what they show of it, so the checks
# read the source (the build above compiled exactly this file).
STR=$(cat "$HERE/installer.nsi")
for want in 'WriteUninstaller' 'DHIS2FacilityHub' 'service install' 'netsh advfirewall firewall add rule' 'certutil' 'dhis2.local' \
  'icacls' 'hub init' 'powercfg' 'Add-MpPreference' 'install.ini' 'CurrentVersion\Uninstall'; do
  check "the installer script: $want" grep -qF -- "$want" <<<"$STR"
done
# Review fixes: an elevated installer must not run or trust anything an ordinary user
# could have put in ProgramData first.
check "the data folder is taken over before anything in it runs" python3 - "$HERE/installer.nsi" <<'PY'
import sys
s = open(sys.argv[1]).read()
# The first file read from the folder (the installed hub.exe is never run: below).
own = s.find("/setowner *S-1-5-32-544")
use = s.find('FileOpen $R1 "$DataDir\\version"')
sys.exit(0 if 0 <= own < use else 1)
PY
check "the installer waits for the service to stop" grep -q 'service stop' <<<"$STR"
check "install.ini and ca.crt stay readable (public; the CI check reads them)" grep -q 'install.ini" /grant "\*S-1-5-32-545:R"' <<<"$STR"
check "the installer records what it ran (install.log)" grep -q 'FileOpen $LogFile "$DataDir\\install.log"' <<<"$STR"
# The CI run of 2026-10-20: /inheritance:r with /T left every file with an empty access
# list ((OI)(CI) grants do not apply to files), so the service could not run hub.exe.
no_recursive_reset() { ! grep -E 'inheritance:r.*/T' <<<"$STR"; }
check "no recursive /inheritance:r (files would get empty access lists)" no_recursive_reset
check "files under the data folder inherit its access list" grep -qF '\*" /reset /T' <<<"$STR"
check "uninstalling removes the Defender exclusion" grep -q 'Remove-MpPreference' <<<"$STR"
local_subnets_only() { ! grep 'firewall add rule' <<<"$STR" | grep action=allow | grep -v remoteip=localsubnet; }
check "the firewall rules admit only the PC's own subnets" local_subnets_only
# Only for the whole PC (22 November 2026): a hub for one user served only its own PC.
whole_pc_only() { grep -q '^RequestExecutionLevel admin' <<<"$STR" && ! grep -qE 'MultiUser|HKCU|LOCALAPPDATA' <<<"$STR"; }
check "the installer installs for the whole PC only" whole_pc_only
check "the uninstaller keeps hosts if putting the new one in place fails" grep -qF 'Rename "$6.hub-old" $6' <<<"$STR"
# Review R-27: the installed hub.exe is in a folder the service's account may change,
# so no elevated step runs it.
check "no elevated step runs the installed hub.exe" python3 - "$HERE/installer.nsi" <<'PY'
import re, sys
bad = [l for l in open(sys.argv[1]) if re.search(r"(Run|nsExec::\w+)\s+'\"\$HubExe\"", l)]
sys.exit(1 if bad else 0)
PY
kept_newer_shown() { grep -q 'Kept the newer hub' <<<"$STR" && grep -qF '"DisplayVersion" "$ShownVersion"' <<<"$STR"; }
check "a kept newer hub is logged and shown in the Apps list" kept_newer_shown
# The Windows VM, 2026-11-20 and 21.
check "an owner the installer cannot read stops it" grep -q 'Could not read the owner of' <<<"$STR"
check "uninstalling checks that the Defender exclusion is gone" grep -qF 'ExclusionPath -contains' <<<"$STR"
tpm_said() { grep -q 'ReadINIStr $6 "$DataDir\\install.ini" "hub" "tpm"' <<<"$STR" && grep -q 'MUI_FINISHPAGE_TEXT .*$TpmNote' <<<"$STR"; }
check "the installer says whether a TPM seals the hub's keys" tpm_said
# The retest in the VM, 2026-11-22.
appends_log() { [ "$(grep -c 'install.log" a$' <<<"$STR")" = "$(grep -A1 'install.log" a$' <<<"$STR" | grep -c 'FileSeek .* 0 END')" ]; }
check "install.log is appended to, not written over" appends_log
check "hub init runs before the program is replaced" python3 - "$HERE/installer.nsi" <<'PY'
import sys
s = open(sys.argv[1]).read()
init, put = s.find('hub-new.exe" init -data'), s.find('File "/oname=hub.exe"')
sys.exit(0 if 0 <= init < put else 1)
PY
no_prompt() { grep -q 'add rule name="${NAME} (installer)".*hub-new.exe' <<<"$STR" && grep -q 'delete rule name="${NAME} (installer)"' <<<"$STR"; }
check "hub init runs under a firewall rule (no prompt)" no_prompt
echo
if [ "$FAILS" -gt 0 ]; then echo "$FAILS checks failed"; exit 1; fi
echo "all checks passed"
