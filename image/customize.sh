#!/bin/sh
# Runs inside the image's root file system (chroot, as root) during image/build.sh.
set -eu
export DEBIAN_FRONTEND=noninteractive LC_ALL=C

apt-get update -q
apt-get install -y -q --no-install-recommends chrony nftables unattended-upgrades polkitd
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
# The setup Wi-Fi's captive DNS answer is written here by the hub (internal/netsetup).
mkdir -p /etc/NetworkManager/dnsmasq-shared.d
chgrp hub /etc/NetworkManager/dnsmasq-shared.d
chmod 2775 /etc/NetworkManager/dnsmasq-shared.d

echo dhis2 > /etc/hostname
sed -i 's/^127\.0\.1\.1.*/127.0.1.1\tdhis2/' /etc/hosts
grep -q '^127\.0\.1\.1' /etc/hosts || printf '127.0.1.1\tdhis2\n' >> /etc/hosts

systemctl enable hub.service hub-wifi-unblock.service hub-diag.timer nftables.service chrony.service avahi-daemon.service NetworkManager.service
systemctl disable userconfig.service sshswitch.service 2>/dev/null || true
# A new machine ID at first boot, so cloned images do not share one. An empty one also
# makes systemd treat that boot as a first boot, when systemd-firstboot asks on the
# console for a keymap, time zone and root password and waits: with no screen or
# keyboard, the hub never started (two Pis, 2026-10-22). The image sets what it would
# ask for, or needs none of it.
systemctl mask systemd-firstboot.service
chmod 0755 /usr/local/sbin/hub-diag
: > /etc/machine-id
rm -f /var/lib/dbus/machine-id
