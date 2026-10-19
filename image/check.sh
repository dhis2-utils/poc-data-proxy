#!/usr/bin/env bash
# Checks a built hub image without booting or mounting it (docs/superpowers/specs/
# 2026-10-19-pi-image-design.md, "Testing"). Run as root (debugfs reads the files).
#
#   sudo image/check.sh dhis2-hub-0.3.0-arm64.img.xz
set -uo pipefail
IMG=$1
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
FAILS=0
ok() { echo "ok   $*"; }
bad() { echo "FAIL $*"; FAILS=$((FAILS + 1)); }
check() { local what=$1; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }

case "$IMG" in
*.xz) xz -dc "$IMG" > "$WORK/disk.img" ;;
*) cp "$IMG" "$WORK/disk.img" ;;
esac
read -r LABEL B_START B_SIZE R_START R_SIZE < <(sfdisk -J "$WORK/disk.img" | python3 -c '
import json, sys
t = json.load(sys.stdin)["partitiontable"]
p = t["partitions"]
print(t["id"].removeprefix("0x"), p[0]["start"], p[0]["size"], p[1]["start"], p[1]["size"])')
dd if="$WORK/disk.img" of="$WORK/boot.vfat" bs=512 skip="$B_START" count="$B_SIZE" status=none
dd if="$WORK/disk.img" of="$WORK/root.ext4" bs=512 skip="$R_START" count="$R_SIZE" status=none
rm "$WORK/disk.img"
R="$WORK/root.ext4"
cat_() { debugfs -R "cat $1" "$R" 2>/dev/null; }
mode() { debugfs -R "stat $1" "$R" 2>/dev/null | sed -n 's/.*Mode: *\([0-7]*\).*/\1/p'; }
exists() { debugfs -R "stat $1" "$R" 2>/dev/null | grep -q '^Inode:'; }

# Partitions: the disk identifier the boot configuration names.
CMDLINE=$(mtype -i "$WORK/boot.vfat" ::/cmdline.txt)
check "cmdline.txt boots root=PARTUUID=$LABEL-02" grep -q "root=PARTUUID=$LABEL-02 " <<<"$CMDLINE "
FSTAB=$(cat_ /etc/fstab)
check "fstab mounts PARTUUID=$LABEL-01 and -02" sh -c "grep -q 'PARTUUID=$LABEL-01' <<'X'
$FSTAB
X
grep -q 'PARTUUID=$LABEL-02' <<'X'
$FSTAB
X"
check "root file system is clean (e2fsck)" e2fsck -fn "$R"
check "no cloud-init seed on the boot partition" sh -c "! mdir -i '$WORK/boot.vfat' ::/user-data"

# Permissions survived the rebuild.
[ "$(mode /usr/bin/sudo)" = 04755 ] && ok "sudo is setuid (04755)" || bad "sudo mode $(mode /usr/bin/sudo)"
[ "$(mode /tmp)" = 01777 ] && ok "/tmp is 01777" || bad "/tmp mode $(mode /tmp)"

# The hub.
debugfs -R "dump /usr/local/lib/hub/hub $WORK/hub" "$R" >/dev/null 2>&1
check "the hub binary is an arm64 executable" python3 -c "
import sys; b = open('$WORK/hub', 'rb').read(20)
sys.exit(0 if b[:4] == b'\x7fELF' and int.from_bytes(b[18:20], 'little') == 183 else 1)"
HUBUID=$(cat_ /etc/passwd | awk -F: '$1 == "hub" {print $3}')
HUBGID=$(cat_ /etc/passwd | awk -F: '$1 == "hub" {print $4}')
check "a hub system user exists" test -n "$HUBUID"
check "the hub binary belongs to the hub user (self-upgrade)" sh -c "debugfs -R 'stat /usr/local/lib/hub/hub' '$R' 2>/dev/null | grep -q 'User: *$HUBUID '"
# Self-upgrade writes the new binary next to the running one, then renames it: the
# folder must be the hub's, not root's /usr/local/bin.
check "the hub's binary folder is the hub's (self-upgrade)" sh -c "test -n '$HUBUID' && debugfs -R 'stat /usr/local/lib/hub' '$R' 2>/dev/null | grep -q 'User: *$HUBUID '"
check "hub.service runs the binary in that folder" sh -c "debugfs -R 'cat /etc/systemd/system/hub.service' '$R' 2>/dev/null | grep -q '^ExecStart=/usr/local/lib/hub/hub '"
check "rfkill does not block the Wi-Fi (setup Wi-Fi)" exists /etc/systemd/system/multi-user.target.wants/hub-wifi-unblock.service
check "hub.service is enabled" exists /etc/systemd/system/multi-user.target.wants/hub.service
check "hub.service may bind ports 80 and 443" sh -c "debugfs -R 'cat /etc/systemd/system/hub.service' '$R' 2>/dev/null | grep -q CAP_NET_BIND_SERVICE"
YAML=$(cat_ /etc/hub/hub.yaml)
check "hub.yaml has no central_url (one image for every country)" sh -c "! grep -q '^central_url' <<'X'
$YAML
X"
for want in 'network: networkmanager' 'listen: ":443"' 'http_listen: ":80"' 'usb_dir: /media/hub-usb'; do
  check "hub.yaml: $want" grep -qF "$want" <<<"$YAML"
done

# The system around it.
check "host name dhis2" test "$(cat_ /etc/hostname | tr -d '\n')" = dhis2
DPKG=$(cat_ /var/lib/dpkg/status)
installed() { awk -v p="$1" '$1 == "Package:" {n = $2} $1 == "Status:" && n == p && /installed/ && !/not-installed|config-files/ {f = 1} END {exit !f}' <<<"$DPKG"; }
installed openssh-server && bad "an SSH server is installed" || ok "no SSH server"
for p in network-manager avahi-daemon chrony nftables unattended-upgrades polkitd; do
  check "installed: $p" installed "$p"
done
for p in cloud-init userconf-pi; do
  installed "$p" && bad "still installed: $p" || ok "removed: $p"
done
LOGIN=$(cat_ /etc/passwd | awk -F: '$3 >= 1000 && $3 < 65000 && $7 !~ /(nologin|false)$/')
check "no login user" test -z "$LOGIN"
check "polkit lets the hub manage NetworkManager" sh -c "debugfs -R 'cat /etc/polkit-1/rules.d/50-hub-network.rules' '$R' 2>/dev/null | grep -q 'org.freedesktop.NetworkManager'"
check "nftables is enabled" exists /etc/systemd/system/sysinit.target.wants/nftables.service
check "nftables opens 80 and 443" sh -c "debugfs -R 'cat /etc/nftables.conf' '$R' 2>/dev/null | grep -q 'tcp dport { 80, 443 }'"
check "the hardware watchdog is on (RuntimeWatchdogSec)" sh -c "debugfs -R 'cat /etc/systemd/system.conf.d/watchdog.conf' '$R' 2>/dev/null | grep -q '^RuntimeWatchdogSec='"
check "the USB stick is mounted for the hub" sh -c "debugfs -R 'cat /etc/udev/rules.d/90-hub-usb.rules' '$R' 2>/dev/null | grep -q /media/hub-usb"
check "machine-id is empty (made at first boot)" test -z "$(cat_ /etc/machine-id)"
check "the setup Wi-Fi's DNS folder is writable by the hub" sh -c "test -n '$HUBGID' && debugfs -R 'stat /etc/NetworkManager/dnsmasq-shared.d' '$R' 2>/dev/null | grep -q 'Group: *$HUBGID '"

echo
if [ "$FAILS" -gt 0 ]; then echo "$FAILS checks failed"; exit 1; fi
echo "all checks passed"
