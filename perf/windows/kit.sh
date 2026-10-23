#!/usr/bin/env bash
# Builds the Windows test kit: stress.ps1 and powercut.ps1 with the three programs
# they run, and powercut-host.sh for the Mac (docs/PERFORMANCE.md, "On a Windows
# PC"). Copy the folder to the PC. The mac folder holds the Mac's half of a remote
# load test (perf/remote-load.sh with its two programs, for an Apple Silicon Mac).
#
#   perf/windows/kit.sh <output folder>
#   ARCH=arm64 perf/windows/kit.sh <output folder>   for Windows on ARM, without
#                                                     x64 emulation
set -euo pipefail
OUT=$1
HERE=$(cd "$(dirname "$0")" && pwd)
cd "$HERE/../.."
V="$(cat VERSION)-test.$(git rev-parse --short HEAD)"
mkdir -p "$OUT"
export CGO_ENABLED=0
GOOS=windows GOARCH=${ARCH:-amd64} go build -trimpath -ldflags "-H windowsgui -X main.version=$V" -o "$OUT/hub.exe" ./cmd/hub
GOOS=windows GOARCH=${ARCH:-amd64} go build -trimpath -o "$OUT/fakecentral.exe" ./cmd/fakecentral
GOOS=windows GOARCH=${ARCH:-amd64} go build -trimpath -o "$OUT/hubload.exe" ./cmd/hubload
mkdir -p "$OUT/mac"
GOOS=darwin GOARCH=arm64 go build -trimpath -o "$OUT/mac/fakecentral" ./cmd/fakecentral
GOOS=darwin GOARCH=arm64 go build -trimpath -o "$OUT/mac/hubload" ./cmd/hubload
cp "$HERE/../remote-load.sh" "$HERE/../remote-queue.sh" "$OUT/mac/"
cp "$HERE/stress.ps1" "$HERE/powercut.ps1" "$HERE/powercut-host.sh" "$OUT/"
echo "$OUT: hub $V (windows/${ARCH:-amd64})"
