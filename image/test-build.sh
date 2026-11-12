#!/usr/bin/env bash
# Builds a test image for the checks in image/README.md, "Checks on a Pi after an image
# change", on a Mac with Apple silicon and Docker (or arm64 Linux with Docker). It makes
# a throwaway release key and a newer release signed with it, served from this computer
# for step 5 (self-upgrade). Never give this image to a facility: it trusts the throwaway
# key and upgrades from this computer.
#
#   image/test-build.sh [this computer's IP address on the Pi's network]
#
# Everything lands in image/out/test/ (ignored by git).
set -euo pipefail
cd "$(dirname "$0")/.."
IP=${1:-$(ipconfig getifaddr en0 2>/dev/null || true)}
[ -n "$IP" ] || { echo "usage: $0 <this computer's IP address on the Pi's network>" >&2; exit 1; }
BASE=$(cat VERSION)
V1=$BASE-local.1 # in the image
V2=$BASE         # the release to upgrade to (step 5); the app takes only x.y.z
T=image/out/test
mkdir -p "$T/srv"

echo "== throwaway release key, release address http://$IP:8000"
[ -f "$T/release.key" ] || go run ./cmd/hub keygen -out "$T/release"
PKG=github.com/dhis2/facility-hub/internal/upgrade
LDF="-X $PKG.ReleaseKey=$(cat "$T/release.pub") -X $PKG.ReleaseBase=http://$IP:8000"
build() { GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$1 $LDF" -o "$2" ./cmd/hub; }

echo "== hub $V1 (the image's) and $V2 (the release)"
build "$V1" "$T/hub-linux-arm64"
build "$V2" "$T/hub-new"
rm -rf "$T/srv/v$V2"
go run ./cmd/hub release -key "$T/release.key" -dir "$T/srv/v$V2" -version "$V2" "linux/arm64=$T/hub-new"

echo "== image (about 10-15 minutes; the base image is cached in the Docker volume hub-image-cache)"
docker run --rm --privileged --platform linux/arm64 -v "$PWD":/src -v hub-image-cache:/var/cache/hub-image -w /src debian:trixie bash -c "
  set -e
  apt-get update -q && apt-get install -y -q e2fsprogs mtools fdisk xz-utils python3 curl ca-certificates >/dev/null
  image/build.sh $T/hub-linux-arm64 $V1 $T
  image/check.sh $T/dhis2-hub-$V1-arm64.img.xz
  chown -R $(id -u):$(id -g) $T"

cat <<EOF

Done. Flash:  $T/dhis2-hub-$V1-arm64.img.xz
For step 5:   (cd $T/srv && python3 -m http.server 8000), then type $V2 on the app's version page.
Keep this computer at $IP until step 5, or run this script again with its new address and re-flash.
EOF
