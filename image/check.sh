#!/usr/bin/env bash
# Checks a built hub image without booting or mounting it (docs/superpowers/specs/
# 2026-10-19-pi-image-design.md, "Testing"). Run as root (debugfs reads the files).
#
#   sudo image/check.sh dhis2-hub-0.3.0-arm64.img.xz
#   sudo LOADTEST=1 image/check.sh dhis2-hub-0.3.0-loadtest-arm64.img.xz
set -uo pipefail
IMG=$1
LOADTEST=${LOADTEST:-}
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
masked() { debugfs -R "stat /etc/systemd/system/$1" "$R" 2>/dev/null | grep -q 'Fast link dest: "/dev/null"'; }

# Partitions: the disk identifier the boot configuration names.
CMDLINE=$(mtype -i "$WORK/boot.vfat" ::/cmdline.txt)
check "cmdline.txt boots root=PARTUUID=$LABEL-02" grep -q "root=PARTUUID=$LABEL-02 " <<<"$CMDLINE "
no_resize() { ! grep -qw resize <<<"$CMDLINE"; }
check "cmdline.txt leaves out Raspberry Pi OS's first-start resize" no_resize
FSTAB=$(cat_ /etc/fstab)
check "fstab mounts PARTUUID=$LABEL-01 and -02" sh -c "grep -q 'PARTUUID=$LABEL-01' <<'X'
$FSTAB
X
grep -q 'PARTUUID=$LABEL-02' <<'X'
$FSTAB
X"
check "root file system is clean (e2fsck)" e2fsck -fn "$R"
check "no cloud-init seed on the boot partition" sh -c "! mdir -i '$WORK/boot.vfat' ::/user-data"
check "config.txt enables a TPM board on SPI" sh -c "mtype -i '$WORK/boot.vfat' ::/config.txt | grep -q '^dtoverlay=tpm-slb9670'"
check "the hub's user may use the TPM (group tss)" sh -c "debugfs -R 'cat /etc/group' '$R' 2>/dev/null | grep -q '^tss:.*hub'"
check "/dev/tpmrm0 belongs to group tss" sh -c "debugfs -R 'cat /etc/udev/rules.d/60-hub-tpm.rules' '$R' 2>/dev/null | grep -q 'GROUP=\"tss\"'"

# Permissions survived the rebuild.
[ "$(mode /usr/bin/sudo)" = 04755 ] && ok "sudo is setuid (04755)" || bad "sudo mode $(mode /usr/bin/sudo)"
[ "$(mode /tmp)" = 01777 ] && ok "/tmp is 01777" || bad "/tmp mode $(mode /tmp)"

# The hub.
debugfs -R "dump /usr/local/lib/hub/hub $WORK/hub" "$R" >/dev/null 2>&1
check "the hub binary is an arm64 executable" python3 -c "
import sys; b = open('$WORK/hub', 'rb').read(20)
sys.exit(0 if b[:4] == b'\x7fELF' and int.from_bytes(b[18:20], 'little') == 183 else 1)"
HUBUID=$(cat_ /etc/passwd | awk -F: '$1 == "hub" {print $3}')
check "a hub system user exists" test -n "$HUBUID"
check "the hub binary belongs to the hub user (self-upgrade)" sh -c "debugfs -R 'stat /usr/local/lib/hub/hub' '$R' 2>/dev/null | grep -q 'User: *$HUBUID '"
# Self-upgrade writes the new binary next to the running one, then renames it: the
# folder must be the hub's, not root's /usr/local/bin.
check "the hub's binary folder is the hub's (self-upgrade)" sh -c "test -n '$HUBUID' && debugfs -R 'stat /usr/local/lib/hub' '$R' 2>/dev/null | grep -q 'User: *$HUBUID '"
check "hub.service runs the binary in that folder" sh -c "debugfs -R 'cat /etc/systemd/system/hub.service' '$R' 2>/dev/null | grep -q '^ExecStart=/usr/local/lib/hub/hub '"
check "rfkill does not block the Wi-Fi (setup Wi-Fi)" exists /etc/systemd/system/multi-user.target.wants/hub-wifi-unblock.service
check "hub.service is enabled" exists /etc/systemd/system/multi-user.target.wants/hub.service
check "the journal is kept on the disk (hub-diag.txt shows the previous start)" sh -c "debugfs -R 'cat /etc/systemd/journald.conf.d/40-rpi-volatile-storage.conf' '$R' 2>/dev/null | grep -q '^Storage=persistent'"
check "diagnostics for support are written to the boot partition" exists /etc/systemd/system/timers.target.wants/hub-diag.timer
check "no first-boot prompt waits for a console nobody has" masked systemd-firstboot.service
check "the disk is grown at the first start" exists /etc/systemd/system/multi-user.target.wants/hub-growdisk.service
if [ "$LOADTEST" = 1 ]; then
  check "load-test mode is there, off unless the boot partition asks for it" sh -c "debugfs -R 'cat /etc/systemd/system/hub-loadtest.service' '$R' 2>/dev/null | grep -q '^ConditionPathExists=/boot/firmware/hub-loadtest.txt'"
  check "load-test mode is enabled" exists /etc/systemd/system/multi-user.target.wants/hub-loadtest.service
  check "the facility hub stays off in load-test mode" sh -c "debugfs -R 'cat /etc/systemd/system/hub.service.d/loadtest.conf' '$R' 2>/dev/null | grep -q '^ConditionPathExists=!/boot/firmware/hub-loadtest.txt'"
  check "curl is installed (load-test mode)" exists /usr/bin/curl
else
  # A facility's image has no load-test mode (review R-31): build.sh adds it only with
  # LOADTEST=1.
  for f in /etc/systemd/system/hub-loadtest.service /etc/systemd/system/hub.service.d/loadtest.conf /usr/local/sbin/hub-loadtest; do
    exists "$f" && bad "load-test mode in a facility's image: $f" || ok "no load-test mode: $f"
  done
fi
wifi_on() { cat_ /var/lib/NetworkManager/NetworkManager.state | grep -q '^WirelessEnabled=true'; }
check "NetworkManager starts with the Wi-Fi on" wifi_on
check "hub.service may bind ports 80 and 443" sh -c "debugfs -R 'cat /etc/systemd/system/hub.service' '$R' 2>/dev/null | grep -q CAP_NET_BIND_SERVICE"
UNIT=$(cat_ /etc/systemd/system/hub.service)
for want in StateDirectoryMode=0700 UMask=0077 NoNewPrivileges=yes PrivateTmp=yes LimitCORE=0 \
  MemorySwapMax=0 ProtectSystem=strict ProtectHome=yes RuntimeDirectory=hub \
  ReadWritePaths=/usr/local/lib/hub ReadWritePaths=-/media/hub-usb; do
  check "hub.service: $want" grep -qx "$want" <<<"$UNIT"
done
check "the stick's mount point is there before the hub starts (ReadWritePaths)" exists /media/hub-usb
check "setpriv is installed (hub-diag)" exists /usr/bin/setpriv
check "hub-diag runs the hub binary as the hub's user, not root" sh -c "debugfs -R 'cat /usr/local/sbin/hub-diag' '$R' 2>/dev/null | grep -F 'lib/hub/hub version' | grep -q 'setpriv --reuid=hub'"
YAML=$(cat_ /etc/hub/hub.yaml)
check "hub.yaml has no central_url (one image for every country)" sh -c "! grep -q '^central_url' <<'X'
$YAML
X"
for want in 'network: networkmanager' 'listen: ":443"' 'http_listen: ":80"' 'usb_dir: /media/hub-usb' 'data_dir: /var/lib/hub'; do
  check "hub.yaml: $want" grep -qF "$want" <<<"$YAML"
done
# Every folder hub.yaml has the hub write to is writable under ProtectSystem=strict.
check "hub.yaml's data_dir is hub.service's StateDirectory" sh -c "grep -q '^data_dir: /var/lib/hub\b' <<'X'
$YAML
X
grep -qx StateDirectory=hub <<'X'
$UNIT
X"

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
# Only the NetworkManager actions the hub's nmcli calls need (review R-26).
POLKIT=$(cat_ /etc/polkit-1/rules.d/50-hub-network.rules)
ACTIONS=$(grep -o '"org\.freedesktop\.NetworkManager\.[a-z][a-z.-]*"' <<<"$POLKIT" | tr -d '"' | sort | tr '\n' ' ')
WANT_ACTIONS="org.freedesktop.NetworkManager.network-control org.freedesktop.NetworkManager.settings.modify.system org.freedesktop.NetworkManager.wifi.scan org.freedesktop.NetworkManager.wifi.share.protected "
[ "$ACTIONS" = "$WANT_ACTIONS" ] && ok "polkit allows the hub only: $ACTIONS" || bad "polkit allows the hub: $ACTIONS"
check "polkit refuses the hub every other NetworkManager action" grep -q 'polkit.Result.NO' <<<"$POLKIT"
if command -v node >/dev/null; then
  check "the polkit rule does what it says (node)" node -e '
var polkit = {Result: {YES: "yes", NO: "no", NOT_HANDLED: null}, addRule: function (f) { rule = f; }}, rule;
eval(require("fs").readFileSync(0, "utf8"));
var NM = "org.freedesktop.NetworkManager.";
var cases = [[NM + "network-control", "hub", "yes"], [NM + "wifi.share.protected", "hub", "yes"],
  [NM + "wifi.scan", "hub", "yes"], [NM + "settings.modify.system", "hub", "yes"],
  [NM + "reload", "hub", "no"], [NM + "settings.modify.hostname", "hub", "no"],
  [NM + "wifi.share.open", "hub", "no"], [NM + "network-control", "nobody", null],
  ["org.freedesktop.login1.reboot", "hub", null]];
process.exit(cases.every(function (c) { return rule({id: c[0]}, {user: c[1]}) === c[2]; }) ? 0 : 1);' <<<"$POLKIT"
fi
# The hub sets the Pi's time zone to central's, so the 03:00 update reboot is at night.
TZRULE=$(cat_ /etc/polkit-1/rules.d/51-hub-timezone.rules)
check "polkit lets the hub set only the time zone" bash -c 'r=$(cat); grep -q "timedate1.set-timezone" <<<"$r" && ! grep -qE "set-time\"|set-ntp|set-local-rtc" <<<"$r"' <<<"$TZRULE"
check "unattended upgrades reboot at 03:00 local time" sh -c "debugfs -R 'cat /etc/apt/apt.conf.d/20auto-upgrades' '$R' 2>/dev/null | grep -q 'Automatic-Reboot-Time \"03:00\"'"
check "nftables is enabled" exists /etc/systemd/system/sysinit.target.wants/nftables.service
check "nftables opens 80 and 443" sh -c "debugfs -R 'cat /etc/nftables.conf' '$R' 2>/dev/null | grep -q 'tcp dport { 80, 443 }'"
check "nftables keeps NetworkManager's sharing (no flush ruleset)" sh -c "! debugfs -R 'cat /etc/nftables.conf' '$R' 2>/dev/null | grep -q '^flush ruleset' && debugfs -R 'cat /etc/systemd/system/nftables.service.d/hub.conf' '$R' 2>/dev/null | grep -q '^ExecStop=-/usr/sbin/nft delete table inet hub'"
check "nftables counts 100.64.0.0/10 (CGNAT) as local" sh -c "debugfs -R 'cat /etc/nftables.conf' '$R' 2>/dev/null | grep -q '100.64.0.0/10'"
check "the hardware watchdog is on (RuntimeWatchdogSec)" sh -c "debugfs -R 'cat /etc/systemd/system.conf.d/watchdog.conf' '$R' 2>/dev/null | grep -q '^RuntimeWatchdogSec='"
USBRULE=$(cat_ /etc/udev/rules.d/90-hub-usb.rules)
check "the USB stick is mounted for the hub" grep -q /media/hub-usb <<<"$USBRULE"
check "a USB stick without a partition table is mounted too" grep -qF 'ENV{DEVTYPE}!="partition|disk", GOTO="hub_usb_end"' <<<"$USBRULE"
check "the stick's rule leaves out this disk's partitions ($LABEL-*: a Pi started from USB)" grep -qxF "ENV{ID_PART_ENTRY_UUID}==\"$LABEL-*\", GOTO=\"hub_usb_end\"" <<<"$USBRULE"
check "the stick's rule gives vfat its flush option" grep -qF 'ENV{ID_FS_TYPE}=="vfat", RUN{program}+="/usr/bin/systemd-mount --no-block --collect --owner=hub --options=flush,umask=0077 ' <<<"$USBRULE"
check "the stick's rule gives exFAT no vfat option" grep -qF 'ENV{ID_FS_TYPE}=="exfat", RUN{program}+="/usr/bin/systemd-mount --no-block --collect --owner=hub --options=umask=0077 ' <<<"$USBRULE"
if udevadm verify --help >/dev/null 2>&1; then
  printf '%s\n' "$USBRULE" > "$WORK/90-hub-usb.rules"
  check "udevadm verify: the stick's rule" udevadm verify --no-style "$WORK/90-hub-usb.rules"
fi
check "machine-id is empty (made at first boot)" test -z "$(cat_ /etc/machine-id)"
# The setup Wi-Fi's captive DNS answer: written by root on the hub's flag, never by
# the hub, since dnsmasq runs as root (review R-26).
check "the dnsmasq folder is root's (User 0, Group 0)" sh -c "debugfs -R 'stat /etc/NetworkManager/dnsmasq-shared.d' '$R' 2>/dev/null | grep -q 'User: *0 *Group: *0 '"
[ "$(mode /etc/NetworkManager/dnsmasq-shared.d)" = 0755 ] && ok "the dnsmasq folder is 0755" || bad "the dnsmasq folder's mode $(mode /etc/NetworkManager/dnsmasq-shared.d)"
check "no captive line in the image (written at run time)" sh -c "! debugfs -R 'stat /etc/NetworkManager/dnsmasq-shared.d/hub-captive.conf' '$R' 2>/dev/null | grep -q '^Inode:'"
CAPTIVE=$(cat_ /usr/local/sbin/hub-captive)
check "hub-captive writes the one fixed line" grep -qF 'address=/#/10.42.0.1' <<<"$CAPTIVE"
check "hub-captive follows the flag in the hub's runtime folder" grep -qF '[ -e /run/hub/captive ]' <<<"$CAPTIVE"
[ "$(mode /usr/local/sbin/hub-captive)" = 0755 ] && ok "hub-captive is 0755" || bad "hub-captive mode $(mode /usr/local/sbin/hub-captive)"
check "hub-captive is root's" sh -c "debugfs -R 'stat /usr/local/sbin/hub-captive' '$R' 2>/dev/null | grep -q 'User: *0 '"
check "hub-captive.path watches the flag" sh -c "debugfs -R 'cat /etc/systemd/system/hub-captive.path' '$R' 2>/dev/null | grep -qx 'PathChanged=/run/hub/captive'"
check "hub-captive.path is enabled" exists /etc/systemd/system/paths.target.wants/hub-captive.path
check "hub-captive.service clears a stale line before NetworkManager" sh -c "debugfs -R 'cat /etc/systemd/system/hub-captive.service' '$R' 2>/dev/null | grep -qx 'Before=NetworkManager.service'"
check "hub-captive.service runs at each start" exists /etc/systemd/system/multi-user.target.wants/hub-captive.service

# A damaged boot partition does not stop the start (review R-31).
check "fstab: the boot partition is nofail" sh -c "awk '\$2 == \"/boot/firmware\" && \$4 ~ /(^|,)nofail(,|\$)/ {f = 1} END {exit !f}' <<'X'
$FSTAB
X"
debugfs -R "rdump /etc/systemd/system $WORK" "$R" >/dev/null 2>&1
# Only load-test mode's units, in an image built with LOADTEST=1, may need it.
needs_boot() {
  grep -rlE '^(RequiresMountsFor=.*/boot/firmware|Requires=.*boot-firmware\.mount|BindsTo=.*boot-firmware\.mount)' "$WORK/system" |
    if [ "$LOADTEST" = 1 ]; then grep -vE '/(hub-loadtest\.service|loadtest\.conf)$'; else cat; fi
}
check "units read from the image" test -f "$WORK/system/hub.service"
NEEDS=$(needs_boot | xargs -r -n1 basename | tr '\n' ' ')
[ -z "$NEEDS" ] && ok "no unit the hub needs requires the boot partition" || bad "units that require the boot partition: $NEEDS"
DIAG=$(cat_ /usr/local/sbin/hub-diag)
check "hub-diag writes only to a mounted boot partition" grep -qF 'mountpoint -q /boot/firmware' <<<"$DIAG"
check "hub-diag syncs its new file before the rename" grep -qF 'sync "$out.tmp"' <<<"$DIAG"

# The clock for a Pi without RTC (docs/NO-RTC.md).
check "hub-clock.service is enabled" exists /etc/systemd/system/multi-user.target.wants/hub-clock.service
check "hub-clock.service touches systemd's saved time" sh -c "debugfs -R 'cat /etc/systemd/system/hub-clock.service' '$R' 2>/dev/null | grep -q '^ExecStop=/usr/bin/touch /var/lib/systemd/timesync/clock'"
installed systemd-timesyncd && bad "systemd-timesyncd is installed: hub-clock is not needed" || ok "systemd-timesyncd is not installed (chrony): hub-clock keeps the saved time"

# Unattended upgrades cover Raspberry Pi's archive (kernel, firmware).
check "unattended upgrades: Raspberry Pi's archive" sh -c "debugfs -R 'cat /etc/apt/apt.conf.d/52hub-raspberrypi-origin' '$R' 2>/dev/null | grep -q 'origin=Raspberry Pi Foundation,codename=\${distro_codename},label=Raspberry Pi Foundation'"

echo
if [ "$FAILS" -gt 0 ]; then echo "$FAILS checks failed"; exit 1; fi
echo "all checks passed"
