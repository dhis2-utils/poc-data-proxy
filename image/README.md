# The hub's Raspberry Pi image

`build.sh` turns Raspberry Pi OS Lite (64-bit, trixie, pinned in `build.sh` with its SHA-256 in `base.sha256`) into the hub's image. It works without loop devices or mounts:
1. extract the root file system with `debugfs`;
2. restore the owners and modes `debugfs` drops, such as setuid (`fixmodes.py`);
3. customise it in a chroot (`customize.sh`, plus everything under `files/`);
4. rebuild it with `mkfs.ext4 -d`;
5. edit the boot partition with mtools;
6. write both into a disk with the original disk identifier.

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-X main.version=0.3.0 -X github.com/dhis2/facility-hub/internal/upgrade.ReleaseKey=$(cat release.pub)" -o hub-linux-arm64 ./cmd/hub
sudo image/build.sh hub-linux-arm64 0.3.0 out/      # root, on an arm64 host; about 10 minutes
sudo image/check.sh out/dhis2-hub-0.3.0-arm64.img.xz
```

`check.sh` reads the finished image without booting it. It checks:
- the partitions and the identifiers the boot configuration names;
- a clean file system and setuid bits;
- the hub binary, its user, its service and `hub.yaml`;
- the host name, packages, users and firewall;
- the polkit rule, the watchdog, the USB stick mount and the machine ID.

What only a Pi can show (booting, Wi-Fi, the watchdog, the clock, the stick) is in docs/DEPLOYMENT.md, section 2.

Flash the image with Raspberry Pi Imager or `xz -dc … | dd`, without the Imager's customisation: the image has no user, no SSH and no cloud-init.

**Support without a console.** 3 minutes after each start, and every 30 minutes, the hub writes `hub-diag.txt` to the boot partition (`bootfs`, which any computer can read): the hub's state and log, failed services, the network devices and Wi-Fi, whether the USB stick is mounted, free disk, and this start's warnings (`/usr/local/sbin/hub-diag`). The hub's log lines hold templates and parameter names, no patient data or passwords. A hub that does not come up: take its card or disk out and read that file.

**First boot.** The image's machine ID is empty, so each hub makes its own; systemd then treats that boot as a first boot. `systemd-firstboot`, which would ask on the console for a keymap, a time zone and a root password and wait for an answer, is masked: on 2026-10-22 two Pis without a screen waited there for good, before the hub started. `check.sh` checks it stays masked.

**Growing the disk.** `cmdline.txt` has no `resize`: with it, Raspberry Pi OS's start-up stage grows the system partition and gives the disk a new ID, and on this image those steps stopped half-way (the new ID written, `cmdline.txt` and `fstab` not yet), so the disk no longer booted (2026-10-22, a Pi 5 from SD and from a USB SSD). `hub-growdisk.service` grows the partition and its file system in the running system instead, once per disk, before the hub starts; its log is in `hub-diag.txt`. The disk keeps the image's ID, which only has to differ from another disk's when two made from this image are in one Pi at once.

**Wi-Fi on.** The base image's NetworkManager state says `WirelessEnabled=false` (Raspberry Pi OS keeps Wi-Fi off until a country is set), and NetworkManager then blocks the radio itself, whatever `hub-wifi-unblock` did before it. The image ships that state as `true`, so the setup Wi-Fi can start; the radio uses the world regulatory domain.
