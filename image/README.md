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
