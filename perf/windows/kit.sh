#!/usr/bin/env bash
# Builds the Windows test kit: stress.ps1 and powercut.ps1 with the three programs
# they run, and powercut-host.sh for the Mac (docs/PERFORMANCE.md, "On a Windows
# PC"). Copy the folder to the PC.
#
#   perf/windows/kit.sh <output folder>
set -euo pipefail
OUT=$1
HERE=$(cd "$(dirname "$0")" && pwd)
cd "$HERE/../.."
V="$(cat VERSION)-test.$(git rev-parse --short HEAD)"
mkdir -p "$OUT"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
go build -trimpath -ldflags "-H windowsgui -X main.version=$V" -o "$OUT/hub.exe" ./cmd/hub
go build -trimpath -o "$OUT/fakecentral.exe" ./cmd/fakecentral
go build -trimpath -o "$OUT/hubload.exe" ./cmd/hubload
cp "$HERE/stress.ps1" "$HERE/powercut.ps1" "$HERE/powercut-host.sh" "$OUT/"
echo "$OUT: hub $V"
