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
# The first file read from the folder, and the first run of the installed hub.exe
# (the installer never runs it to learn its version: review R-27).
own = s.find("/setowner *S-1-5-32-544")
uses = [s.find('FileOpen $R1 "$DataDir\\version"'), s.find("'\"$HubExe\" ")]
sys.exit(0 if own >= 0 and all(own < u for u in uses) and min(uses) >= 0 else 1)
PY
check "the per-user move reads the user's own folder" grep -q 'UserAppDataLocal' <<<"$STR"
# Review GLM5-F1: the user's own folder is theirs to change, so its hub.exe must not
# reach the admin-only folder, where the elevated installer would run it and the
# service would start it.
check "the per-user move leaves the user's hub.exe behind" python3 - "$HERE/installer.nsi" <<'PY'
import re, sys
moves = [l for l in open(sys.argv[1]) if "robocopy" in l and "UserAppDataLocal" in l]
sys.exit(0 if moves and all(re.search(r'/XD\s+"?bin"?(\s|$|\')', l) for l in moves) else 1)
PY
check "the installer waits for the service to stop" grep -q 'service stop' <<<"$STR"
check "install.ini and ca.crt stay readable (public; the CI check reads them)" grep -q 'install.ini" /grant "\*S-1-5-32-545:R"' <<<"$STR"
check "the installer records what it ran (install.log)" grep -q 'FileOpen $LogFile "$DataDir\\install.log"' <<<"$STR"
# The CI run of 2026-10-20: /inheritance:r with /T left every file with an empty access
# list ((OI)(CI) grants do not apply to files), so the service could not run hub.exe.
check "no recursive /inheritance:r (files would get empty access lists)" sh -c "! grep -E 'inheritance:r.*/T' <<'X'
$STR
X"
check "files under the data folder inherit its access list" grep -qF '\*" /reset /T' <<<"$STR"
check "uninstalling removes the Defender exclusion" grep -q 'Remove-MpPreference' <<<"$STR"
check "the firewall rules admit only the PC's own subnets" sh -c "! grep 'firewall add rule' <<'X' | grep -v remoteip=localsubnet
$STR
X"
check "the whole PC is the default for a user who can elevate" sh -c "! grep -q '^!define MULTIUSER_INSTALLMODE_DEFAULT_CURRENTUSER' <<'X'
$STR
X"
check "the uninstaller keeps hosts if putting the new one in place fails" grep -qF 'Rename "$6.hub-old" $6' <<<"$STR"
# Review R-27: the installed hub.exe is in a folder the service's account may change,
# so no elevated step runs it (the per-user install runs it as the user, not elevated).
check "no elevated step runs the installed hub.exe" python3 - "$HERE/installer.nsi" <<'PY'
import re, sys
bad = [l for l in open(sys.argv[1]) if re.search(r"(Run|nsExec::\w+)\s+'\"\$HubExe\"", l)]
sys.exit(1 if bad else 0)
PY
check "a kept newer hub is logged and shown in the Apps list" sh -c "grep -q 'Kept the newer hub' <<'X' && grep -qF '\"DisplayVersion\" \"\$ShownVersion\"' <<'X2'
$STR
X
$STR
X2"
echo
if [ "$FAILS" -gt 0 ]; then echo "$FAILS checks failed"; exit 1; fi
echo "all checks passed"
