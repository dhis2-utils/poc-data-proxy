#!/bin/sh
# Runs inside the image's root file system (chroot, as root) during image/build.sh.
set -eu
export DEBIAN_FRONTEND=noninteractive LC_ALL=C

apt-get update -q
apt-get install -y -q --no-install-recommends chrony nftables unattended-upgrades polkitd curl
# No SSH (support works from central), no first-boot user prompt or cloud-init: a hub
# has no console user, and set-up happens on the hub's own page.
apt-get purge -y -q openssh-server openssh-sftp-server userconf-pi cloud-init || true
apt-get autoremove -y -q --purge
apt-get clean
rm -rf /var/lib/apt/lists/* /etc/cloud
if id pi >/dev/null 2>&1; then userdel -r pi 2>/dev/null || userdel pi; fi

# The hub's user: owns its binary's folder (self-upgrade writes the new binary there and
# renames it into place) and its data.
useradd --system --home-dir /var/lib/hub --no-create-home --shell /usr/sbin/nologin hub
usermod -aG netdev hub
# The TPM, where the kit has one (60-hub-tpm.rules gives /dev/tpmrm0 to this group).
groupadd -f --system tss
usermod -aG tss hub
chown -R hub:hub /usr/local/lib/hub
chmod 0755 /usr/local/lib/hub /usr/local/lib/hub/hub
ln -sf ../lib/hub/hub /usr/local/bin/hub
# The setup Wi-Fi's captive DNS answer: root's, written by hub-captive on the hub's
# flag. NetworkManager runs dnsmasq as root, so the hub may not write here (review R-26).
mkdir -p /etc/NetworkManager/dnsmasq-shared.d
chmod 0755 /etc/NetworkManager/dnsmasq-shared.d
# The USB stick's mount point (90-hub-usb.rules): there before the hub starts, so its
# ReadWritePaths= keeps it writable (deploy/hub.service).
mkdir -p /media/hub-usb
# The boot partition is not needed to run the hub: a FAT file system that a power cut
# left damaged (hub-diag writes there) must not stop the start in emergency mode, with
# no console to answer it (review R-31).
awk '$2 == "/boot/firmware" && $4 !~ /(^|,)nofail(,|$)/ {$4 = $4 ",nofail"} {print}' /etc/fstab > /etc/fstab.new
mv /etc/fstab.new /etc/fstab

echo dhis2 > /etc/hostname
sed -i 's/^127\.0\.1\.1.*/127.0.1.1\tdhis2/' /etc/hosts
grep -q '^127\.0\.1\.1' /etc/hosts || printf '127.0.1.1\tdhis2\n' >> /etc/hosts

systemctl enable hub.service hub-wifi-unblock.service hub-diag.timer hub-growdisk.service hub-captive.path hub-captive.service hub-clock.service nftables.service chrony.service avahi-daemon.service NetworkManager.service
# Load-test mode, only in an image built with LOADTEST=1 (image/README.md).
if [ -e /etc/systemd/system/hub-loadtest.service ]; then
	systemctl enable hub-loadtest.service
	chmod 0755 /usr/local/sbin/hub-loadtest
fi
systemctl disable userconfig.service sshswitch.service 2>/dev/null || true
# A new machine ID at first boot, so cloned images do not share one. An empty one also
# makes systemd treat that boot as a first boot, when systemd-firstboot asks on the
# console for a keymap, time zone and root password and waits: with no screen or
# keyboard, the hub never started (two Pis, 2026-10-22). The image sets what it would
# ask for, or needs none of it.
systemctl mask systemd-firstboot.service
chmod 0755 /usr/local/sbin/hub-diag /usr/local/sbin/hub-growdisk /usr/local/sbin/hub-captive
# NetworkManager keeps the Wi-Fi off, and blocks the radio, while its saved state says
# so: the base image ships WirelessEnabled=false (Pi OS, until a country is set), which
# overrode hub-wifi-unblock and kept the setup Wi-Fi from starting (rpi5, 2026-10-22).
mkdir -p /var/lib/NetworkManager
printf '[main]\nWirelessEnabled=true\n' > /var/lib/NetworkManager/NetworkManager.state
: > /etc/machine-id
rm -f /var/lib/dbus/machine-id
