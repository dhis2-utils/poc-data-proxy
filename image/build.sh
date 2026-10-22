#!/usr/bin/env bash
# Builds the hub's Raspberry Pi image from Raspberry Pi OS Lite, without loop devices or
# mounts (none in many CI containers): the root file system is extracted with debugfs,
# customised in a chroot and rebuilt with mkfs.ext4 -d; the boot partition is edited with
# mtools (docs/superpowers/specs/2026-10-19-pi-image-design.md). Run as root on arm64.
#
#   sudo image/build.sh <hub binary, linux/arm64> <version> <output folder>
set -euo pipefail
BIN=$(realpath "$1") VERSION=$2 OUT=$(realpath -m "$3")
HERE=$(dirname "$(realpath "$0")")
REPO=$(dirname "$HERE")

BASE_DATE=2026-10-06
BASE_NAME=$BASE_DATE-raspios-trixie-arm64-lite.img.xz
BASE_URL=https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-$BASE_DATE/$BASE_NAME
BASE_SHA256=$(cat "$HERE/base.sha256")
ROOT_SIZE=${ROOT_SIZE:-3G} # Pi OS grows it to the disk at first boot
CACHE=${CACHE:-/var/cache/hub-image}
WORK=$(mktemp -d "${TMPDIR:-/tmp}/hub-image.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ "$(uname -m)" = aarch64 ] || { echo "run on an arm64 host (the chroot runs the image's programs)" >&2; exit 1; }
for t in debugfs mkfs.ext4 sfdisk mtype mcopy mdel xz python3 curl; do
  command -v "$t" >/dev/null || { echo "missing $t (e2fsprogs, fdisk, mtools, xz-utils)" >&2; exit 1; }
done
python3 - "$BIN" <<'PY' || { echo "$BIN is not a linux/arm64 executable" >&2; exit 1; }
import sys
b = open(sys.argv[1], "rb").read(20)
sys.exit(0 if b[:4] == b"\x7fELF" and int.from_bytes(b[18:20], "little") == 183 else 1)
PY

echo "== base image"
mkdir -p "$CACHE" "$OUT"
if [ ! -f "$CACHE/$BASE_NAME" ]; then curl -fL --retry 3 -o "$CACHE/$BASE_NAME.part" "$BASE_URL" && mv "$CACHE/$BASE_NAME.part" "$CACHE/$BASE_NAME"; fi
echo "$BASE_SHA256  $CACHE/$BASE_NAME" | sha256sum -c -
xz -dc "$CACHE/$BASE_NAME" > "$WORK/base.img"
read -r LABEL B_START B_SIZE R_START < <(sfdisk -J "$WORK/base.img" | python3 -c '
import json, sys
t = json.load(sys.stdin)["partitiontable"]
p = t["partitions"]
print(t["id"], p[0]["start"], p[0]["size"], p[1]["start"])')
dd if="$WORK/base.img" of="$WORK/boot.vfat" bs=512 skip="$B_START" count="$B_SIZE" status=none
dd if="$WORK/base.img" of="$WORK/root.ext4" bs=512 skip="$R_START" status=none
rm "$WORK/base.img"

echo "== root file system"
ROOT=$WORK/rootfs
mkdir "$ROOT"
debugfs -R "rdump / $ROOT" "$WORK/root.ext4" >/dev/null 2>&1
python3 "$HERE/fixmodes.py" "$WORK/root.ext4" "$ROOT"
rm "$WORK/root.ext4"
cp -r --no-preserve=ownership "$HERE/files/." "$ROOT/"
install -D -m 0755 "$BIN" "$ROOT/usr/local/lib/hub/hub"
install -D -m 0644 "$REPO/deploy/hub.service" "$ROOT/etc/systemd/system/hub.service"
install -m 0755 "$HERE/customize.sh" "$ROOT/tmp/customize.sh"
mv "$ROOT/etc/resolv.conf" "$WORK/resolv.conf.orig" 2>/dev/null || true
cp /etc/resolv.conf "$ROOT/etc/resolv.conf"
chroot "$ROOT" /tmp/customize.sh
rm -f "$ROOT/etc/resolv.conf" "$ROOT/tmp/customize.sh"
if [ -e "$WORK/resolv.conf.orig" ] || [ -L "$WORK/resolv.conf.orig" ]; then mv "$WORK/resolv.conf.orig" "$ROOT/etc/resolv.conf"; fi
echo "$VERSION" > "$ROOT/etc/hub/image-version"
truncate -s "$ROOT_SIZE" "$WORK/root.ext4"
mkfs.ext4 -q -L rootfs -d "$ROOT" "$WORK/root.ext4"
e2fsck -fn "$WORK/root.ext4" >/dev/null

echo "== boot partition"
for f in user-data meta-data network-config; do mdel -i "$WORK/boot.vfat" "::/$f" 2>/dev/null || true; done
# A TPM board, where the kit has one (the common Infineon SLB 9670 on SPI): the hub seals
# its device key there (internal/devkey). Without a board the driver finds nothing.
mtype -i "$WORK/boot.vfat" ::/config.txt > "$WORK/config.txt"
printf '\n# The hub: a TPM board on SPI, if fitted\n[all]\ndtparam=spi=on\ndtoverlay=tpm-slb9670\n' >> "$WORK/config.txt"
mcopy -o -i "$WORK/boot.vfat" "$WORK/config.txt" ::/config.txt
# No "resize": Raspberry Pi OS's start-up stage would grow the partition and give the
# disk a new ID, steps that stopped half-way on this image and left it unbootable
# (2026-10-22); hub-growdisk.service grows the disk in the running system instead.
mtype -i "$WORK/boot.vfat" ::/cmdline.txt | sed 's/ resize\b//' > "$WORK/cmdline.txt"
mcopy -o -i "$WORK/boot.vfat" "$WORK/cmdline.txt" ::/cmdline.txt

echo "== disk"
IMG=$OUT/dhis2-hub-$VERSION-arm64.img
R_SECTORS=$(( $(stat -c %s "$WORK/root.ext4") / 512 ))
truncate -s 0 "$IMG"
truncate -s $(( (R_START + R_SECTORS) * 512 )) "$IMG"
sfdisk -q "$IMG" <<SF
label: dos
label-id: $LABEL
unit: sectors
start=$B_START, size=$B_SIZE, type=c
start=$R_START, size=$R_SECTORS, type=83
SF
dd if="$WORK/boot.vfat" of="$IMG" bs=512 seek="$B_START" conv=notrunc status=none
dd if="$WORK/root.ext4" of="$IMG" bs=4M seek=$(( R_START * 512 )) oflag=seek_bytes conv=notrunc,sparse status=none
xz -T0 -6 -f "$IMG"
(cd "$OUT" && sha256sum "$(basename "$IMG").xz" > "$(basename "$IMG").xz.sha256")
echo "== $IMG.xz"
