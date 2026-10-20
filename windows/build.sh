#!/usr/bin/env bash
# Builds the Windows installer on Linux: hub.exe for windows/amd64 (no console window),
# signed; the NSIS installer around it, signed (windows/sign.sh).
#
#   windows/build.sh <version> <output folder> [hub.exe]
# Without a hub.exe it builds one here; RELEASE_PUBLIC_KEY (base64) goes into it as
# the release key, as for the Linux binaries.
set -euo pipefail
# Works on Linux and macOS (Homebrew: makensis, osslsigncode).
VERSION=$1 HUB=${3:-}
mkdir -p "$2" && OUT=$(cd "$2" && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT"
if [ -z "$HUB" ]; then
  HUB=$WORK/hub.exe
  (cd "$HERE/.." && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
    -ldflags "-H windowsgui -X main.version=$VERSION -X github.com/dhis2/facility-hub/internal/upgrade.ReleaseKey=${RELEASE_PUBLIC_KEY:-}" \
    -o "$HUB" ./cmd/hub)
else
  cp "$HUB" "$WORK/hub.exe" && HUB=$WORK/hub.exe
fi
"$HERE/sign.sh" "$HUB"
SETUP=$OUT/dhis2-hub-$VERSION-setup.exe
makensis -V2 -DVERSION="$VERSION" -DHUBEXE="$HUB" -DOUTFILE="$SETUP" "$HERE/installer.nsi"
"$HERE/sign.sh" "$SETUP"
cp "$HUB" "$OUT/hub-windows-amd64.exe"
echo "$SETUP"
