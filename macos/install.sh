#!/usr/bin/env bash
# The facility hub on a Mac, for the DHIS2 team to try it: it runs as the logged-in user
# (a launchd agent, started at login and restarted if it stops), from a folder of its
# own, and answers https://dhis2.local like a hub in the field. Not for facilities: a
# Mac is not a hub kit (docs/DEPLOYMENT.md).
#
#   macos/install.sh                     the latest release from GitHub
#   macos/install.sh --hub ./hub         a hub binary you built (GOOS=darwin)
#   macos/install.sh --version 0.3.0     that release
#   macos/install.sh --uninstall [--remove-data]
#
# It asks for your password once, to trust the hub's certificate authority in the
# System keychain (removed again by --uninstall).
set -euo pipefail

REPO=dhis2-utils/poc-data-proxy
LABEL=org.dhis2.facility-hub
DATA="$HOME/Library/Application Support/DHIS2 Facility Hub"
AGENT="$HOME/Library/LaunchAgents/$LABEL.plist"
KEYCHAIN=/Library/Keychains/System.keychain
HOSTSMARK="# DHIS2 Facility Hub"
HUB="" VERSION="" UNINSTALL=0 REMOVE_DATA=0

while [ $# -gt 0 ]; do
  case "$1" in
  --hub) HUB=$2; shift 2 ;;
  --version) VERSION=${2#v}; shift 2 ;;
  --uninstall) UNINSTALL=1; shift ;;
  --remove-data) REMOVE_DATA=1; shift ;;
  -h | --help) sed -n '2,15p' "$0"; exit 0 ;;
  *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done
[ "$(uname -s)" = Darwin ] || { echo "this script is for macOS" >&2; exit 1; }

ini() { sed -n "s/^$1=//p" "$DATA/install.ini" 2>/dev/null | tr -d '\r'; }

stop_agent() {
  launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
}

if [ "$UNINSTALL" = 1 ]; then
  stop_agent
  rm -f "$AGENT"
  CA_NAME=$(ini ca_name)
  if [ -n "$CA_NAME" ]; then
    sudo security delete-certificate -c "$CA_NAME" "$KEYCHAIN" 2>/dev/null || true
  fi
  if grep -q "$HOSTSMARK" /etc/hosts; then
    sudo sed -i '' "/$HOSTSMARK/d" /etc/hosts
  fi
  if [ "$REMOVE_DATA" = 1 ]; then
    rm -rf "$DATA"
    echo "Removed the hub and its data (saves not yet sent are lost)."
  else
    echo "Removed the hub. Its data stays in: $DATA"
  fi
  exit 0
fi

# The binary: one you built, or a release's, checked against the release's checksums.
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
case "$(uname -m)" in
arm64) PLAT=darwin/arm64 ;;
x86_64) PLAT=darwin/amd64 ;;
*) echo "unsupported Mac: $(uname -m)" >&2; exit 1 ;;
esac
if [ -z "$HUB" ]; then
  if [ -z "$VERSION" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p' | head -1)
    [ -n "$VERSION" ] || { echo "no release found on GitHub; use --hub" >&2; exit 1; }
  fi
  BASE="https://github.com/$REPO/releases/download/v$VERSION"
  NAME="hub-${PLAT%/*}-${PLAT#*/}"
  curl -fsSL -o "$WORK/release.json" "$BASE/release.json"
  curl -fsSL -o "$WORK/hub" "$BASE/$NAME"
  WANT=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['sha256'][sys.argv[2]])" "$WORK/release.json" "$PLAT")
  GOT=$(shasum -a 256 "$WORK/hub" | cut -d' ' -f1)
  [ "$WANT" = "$GOT" ] || { echo "the download does not match the release's checksum" >&2; exit 1; }
  HUB=$WORK/hub
fi
[ -f "$HUB" ] || { echo "no hub binary at $HUB" >&2; exit 1; }

# The hub runs from its own folder, so it can replace itself when it upgrades. Never put
# an older version over a newer one: it may have migrated the store already.
mkdir -p "$DATA/bin"
chmod 700 "$DATA"
NEW=$(cp "$HUB" "$WORK/hub-new" && chmod 755 "$WORK/hub-new" && xattr -c "$WORK/hub-new" 2>/dev/null; "$WORK/hub-new" version)
if [ -x "$DATA/bin/hub" ]; then
  OLD=$("$DATA/bin/hub" version 2>/dev/null || echo 0)
  if [ "$(printf '%s\n%s\n' "$OLD" "$NEW" | sort -V | tail -1)" != "$NEW" ]; then
    echo "Keeping the newer hub $OLD already installed (this one is $NEW)."
    NEW=$OLD
  else
    stop_agent
    cp "$WORK/hub-new" "$DATA/bin/hub"
  fi
else
  cp "$WORK/hub-new" "$DATA/bin/hub"
fi

# Settings (free ports, dhis2.local answered by the hub itself, set-up only on this Mac)
# and the hub's certificate authority; an existing hub keeps its own.
"$DATA/bin/hub" init -data "$DATA" -per-user >/dev/null
URL=$(ini url)
CA_NAME=$(ini ca_name)

# Trust the hub's authority on this Mac (limited to the hub's own names and local
# addresses, so it can vouch for nothing else), and make dhis2.local this Mac for its
# own browser, as the Windows installer does; other devices find it through the hub's
# own mDNS responder.
if ! security find-certificate -c "$CA_NAME" "$KEYCHAIN" >/dev/null 2>&1 || ! grep -q "$HOSTSMARK" /etc/hosts; then
  echo "Your password is needed once, to trust the hub's certificate and name on this Mac."
fi
if ! security find-certificate -c "$CA_NAME" "$KEYCHAIN" >/dev/null 2>&1; then
  sudo security add-trusted-cert -d -r trustRoot -k "$KEYCHAIN" "$DATA/ca.crt"
fi
if ! grep -q "$HOSTSMARK" /etc/hosts; then
  printf '127.0.0.1 dhis2.local %s\n' "$HOSTSMARK" | sudo tee -a /etc/hosts >/dev/null
  sudo dscacheutil -flushcache 2>/dev/null || true
fi

# Start at login, restart if it stops.
mkdir -p "$(dirname "$AGENT")"
cat >"$AGENT" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$DATA/bin/hub</string><string>serve</string>
    <string>-config</string><string>$DATA/hub.yaml</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardErrorPath</key><string>$DATA/launchd.log</string>
</dict>
</plist>
EOF
stop_agent
launchctl bootstrap "gui/$(id -u)" "$AGENT"

echo "The hub $NEW runs. Set it up on this Mac: open $URL"
echo "Data and logs: $DATA"
echo "Remove it with: $0 --uninstall"
