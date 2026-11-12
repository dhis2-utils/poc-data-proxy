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

`LOADTEST=1` before both commands builds and checks an image with load-test mode, `dhis2-hub-0.3.0-loadtest-arm64.img.xz` (below).

`check.sh` reads the finished image without booting it. It checks:
- the partitions and the identifiers the boot configuration names;
- a clean file system and setuid bits;
- the hub binary, its user, its service and `hub.yaml`;
- the host name, packages, users and firewall;
- the polkit rule, the watchdog, the USB stick mount and the machine ID;
- the hub's sandbox (`ProtectSystem=strict`, its writable folders, no swap), the captive DNS answer written by root, the boot partition as `nofail`, the saved clock, the polkit rule for the time zone, the Raspberry Pi archive in unattended upgrades, and no load-test mode.

What only a Pi can show (booting, Wi-Fi, the watchdog, the clock, the stick) is in docs/DEPLOYMENT.md, section 2, and in "Checks on a Pi after an image change" below.

Flash the image with Raspberry Pi Imager or `xz -dc … | dd`, without the Imager's customisation: the image has no user, no SSH and no cloud-init.

**Support without a console.** 3 minutes after each start, and every 30 minutes, the hub writes `hub-diag.txt` to the boot partition (`bootfs`, which any computer can read): the hub's state and log, failed services, the network devices and Wi-Fi, whether the USB stick is mounted, free disk, memory and swap, this start's warnings, and the last lines of the previous start with any out-of-memory or hang reported by the kernel (`/usr/local/sbin/hub-diag`). The journal is kept on the disk, at most 50 MB: Raspberry Pi OS keeps it in memory only, so the log of a start that hung or lost power was gone (session B, 10 November 2026). The hub's log lines hold templates and parameter names, no patient data or passwords. A hub that does not come up: take its card or disk out and read that file.

**First boot.** The image's machine ID is empty, so each hub makes its own; systemd then treats that boot as a first boot. `systemd-firstboot`, which would ask on the console for a keymap, a time zone and a root password and wait for an answer, is masked: on 2026-10-22 two Pis without a screen waited there for good, before the hub started. `check.sh` checks it stays masked.

**Growing the disk.** `cmdline.txt` has no `resize`: with it, Raspberry Pi OS's start-up stage grows the system partition and gives the disk a new ID, and on this image those steps stopped half-way (the new ID written, `cmdline.txt` and `fstab` not yet), so the disk no longer booted (2026-10-22, a Pi 5 from SD and from a USB SSD). `hub-growdisk.service` grows the partition and its file system in the running system instead, once per disk, before the hub starts; its log is in `hub-diag.txt`. The disk keeps the image's ID, which only has to differ from another disk's when two made from this image are in one Pi at once.

**Wi-Fi on.** The base image's NetworkManager state says `WirelessEnabled=false` (Raspberry Pi OS keeps Wi-Fi off until a country is set), and NetworkManager then blocks the radio itself, whatever `hub-wifi-unblock` did before it. The image ships that state as `true`, so the setup Wi-Fi can start; the radio uses the world regulatory domain.

**Load-test mode.** Only in an image built with `LOADTEST=1` (`gh workflow run image.yml -f loadtest=true`), never in a facility's: it adds `image/loadtest/`. A file `hub-loadtest.txt` on the boot partition then switches the Pi into a test machine for `perf/remote-queue.sh` on a Mac: `hub.service` stays off (`hub.service.d/loadtest.conf`) and `hub-loadtest.service` runs `/usr/local/sbin/hub-loadtest` instead. It waits for the Mac's fake central (`central=` in the file), starts a fresh hub for each run on plain http, samples memory, CPU, the disk's counters and the temperature every 10 seconds, and when the run's fake central goes away it writes the results to `loadtest/<run>/` on the boot partition and deletes the run's data. `name=` sets the host name (the Mac reaches the Pi at `<name>.local`); `wifi_ssid=` and `wifi_password=` join a Wi-Fi without a cable. Flash a facility image to get the facility hub back.

## Checks on a Pi after an image change

`check.sh` reads the files of an image; these steps show that a Pi runs it. The image has no SSH and no console, so every check reads one of three things:
- `hub-diag.txt` on the boot partition (`bootfs`). Take the disk out and read it on any computer. It is written 3 minutes after each start and every 30 minutes after; its sections are named below in quotes.
- The hub's pages, at `http://dhis2.local` from a device on the same network.
- The disk itself, on a Linux computer (one step only).

What you need: a Pi 5 that starts from a USB SSD; a Pi 4 without an RTC on a microSD card; a FAT USB stick and an exFAT one; an Ethernet cable to a network with internet; a phone; the Facility Hub app on a central where you may roll out releases.

1. **Build the image.** Run `gh workflow run image.yml --ref <branch>` (any branch pushed to GitHub, such as `pilot-prep`). `gh run watch` follows it; the step "Check the image" must end with "all checks passed". Download it with `gh run download <run id> -n dhis2-hub-<version>-arm64.img`, or from the run's page on GitHub, under "Artifacts". It is kept 3 days. The download holds `dhis2-hub-<version>-arm64.img.xz`.
2. **Flash it** to the SSD and to the card: Raspberry Pi Imager, "Use custom", with no OS customisation; or `xz -dc dhis2-hub-<version>-arm64.img.xz | sudo dd of=/dev/<disk> bs=4M conv=fsync`.
3. **First start.** Put the FAT stick in the Pi 5, connect the cable, start it from the SSD, and wait 5 minutes. `http://dhis2.local` shows the hub's start page. Set the hub up against central (DEPLOYMENT.md, section 3), as a test facility. Then switch it off, take out the SSD, and open `hub-diag.txt`. These hold for every later reading:
   - "failed units" lists none.
   - "hub log, this start" and "warnings and errors" hold no "Read-only file system", "permission denied" or "Not authorized".
4. **The sandbox** (`deploy/hub.service`). In "hub sandbox":
   - `ProtectSystem=strict`, `ProtectHome=yes`, `RuntimeDirectory=hub`, `ReadWritePaths=/usr/local/lib/hub -/media/hub-usb`, `MemorySwapMax=0`.
   - `memory.swap.max 0` and `memory.swap.current 0`. "No such file" there means the kernel has no swap control for the hub: note it.
   - Under "as the hub sees them", `/` and `/etc` start with `ro`; `/usr/local/lib/hub`, `/var/lib/hub`, `/run/hub` and `/media/hub-usb` (with the stick in) start with `rw`.
5. **Self-upgrade.** On central, in the Facility Hub app, roll a release newer than the image's version out to this hub. Within one sync cycle (15 minutes), plus the download, the hub's admin page shows the new version, and so does the first line of `hub-diag.txt` ("== hub"). Its log has "upgrading" and no "binary swap failed" or "saving previous binary". The binary folder is the one the sandbox must leave writable.

   Without the project's release key (docs/PILOT.md, item 9), test it with a throwaway key and a release served from a laptop on the Pi's network. Build the image's binary and a newer one with the same two flags, `-X …/internal/upgrade.ReleaseKey=$(cat release.pub) -X …/internal/upgrade.ReleaseBase=http://<laptop IP>:8000` (an IP address: the static binary cannot resolve `.local` names), after `go run ./cmd/hub keygen -out release`. Sign the newer one with `go run ./cmd/hub release -key release.key -dir srv/v<newer> -version <newer> linux/arm64=<newer binary>`, serve `srv` with `python3 -m http.server 8000`, and type `<newer>` on the app's version page (the GitHub list is empty then). Never give such an image to a facility: it trusts the throwaway key and upgrades from the laptop.
6. **The stick: copy and restore.** On the hub's admin page the stick shows a recent copy. Then restore it as in RECOVERY.md, C2: a second SSD flashed with this image, the same stick. The restored hub shows the facility and its data.
7. **The stick rule** (90-hub-usb.rules). In "USB stick", on the Pi 5 started from its SSD:
   - The SSD's first partition (FAT, the boot partition) is mounted at `/boot/firmware` only, never at `/media/hub-usb`; its `ID_PART_ENTRY_UUID` matches the one in the rule's line printed below.
   - The stick's partition is mounted at `/media/hub-usb`.
   - Swap in the exFAT stick and restart: it is mounted at `/media/hub-usb`, with `umask=0077` among its options, and "warnings and errors" has no `exfat` line about an unknown option `flush`.
8. **Setup Wi-Fi and the captive answer** (hub-captive). Start the Pi without the cable. After a minute a phone sees the Wi-Fi `Hub-…`. Join it with the 8-digit password from `facility-hub/hub.txt` on the stick: the phone's sign-in window opens the hub's page. Before you join another network, read `hub-diag.txt` (switch off, read, start again):
   - Under "setup Wi-Fi's captive DNS answer", `/run/hub/` holds `captive`, `dnsmasq-shared.d/` holds `hub-captive.conf` owned by root, and the unit's log says "captive DNS answer on".
   - The hub log has no "did not follow the hub within".
9. **Joining a Wi-Fi under the polkit rule.** From the setup page, join a Wi-Fi with internet. The hub joins it and the setup Wi-Fi goes away. In `hub-diag.txt`:
   - "NetworkManager actions the hub may take" says `yes` for `network-control`, `wifi.share.protected`, `wifi.scan` and `settings.modify.system`, and `no` for every other line.
   - Under "rules mentioning NetworkManager or netdev", only `50-hub-network.rules` and Debian's `org.freedesktop.NetworkManager.rules` (for logged-in users) are listed. Report any other.
   - The captive answer is off: no `captive`, no `hub-captive.conf`, "captive DNS answer off".
10. **The facility's Wi-Fi** (case C). Start without the cable again, join the setup Wi-Fi and choose "Make this hub the facility's Wi-Fi". A phone that joins it opens `https://dhis2.local`, and any other name, such as `example.org`, does not lead to the hub's page. `hub-captive.conf` is gone.
11. **The boot partition is optional.** In "boot partition", the `/boot/firmware` line of fstab has `nofail`, and `boot-firmware.mount` has an empty `RequiredBy=`; "hub sandbox" shows an empty `RequiresMountsFor=` or one without `/boot/firmware`. To see it work, on a Linux computer change that fstab line's `PARTUUID` on the SSD's system partition to one that does not exist (`…-09`), start the Pi with the cable, and wait 3 minutes: `http://dhis2.local` answers (with no `hub-diag.txt` written, since the partition is not mounted). Put the line back.
12. **No load-test mode.** `hub-diag.txt` has no "load-test mode" section. Write an empty `hub-loadtest.txt` to the boot partition and start: the hub's page still answers. Delete the file.
13. **The saved clock** (hub-clock, docs/NO-RTC.md). On the Pi 4, start with the cable and leave it 30 minutes. In "clock", `saved` is within 15 minutes of `now`, and `hub-clock` is `active`. Switch off at a noted time, unplug the cable, wait an hour, start without the cable and wait 5 minutes, then switch off and read `hub-diag.txt`: `now` in the "==" first line is close to the noted time plus 5 minutes, not the image's base date (6 October 2026). If systemd logged that it moved the clock forward, that line is under "clock". On the Pi 5 set up in step 3, `time zone` in "clock" is central's (`serverTimeZoneId`, e.g. `Africa/Kampala`), not `Etc/UTC`, unless central itself runs on UTC; after an update that needs a reboot, `reboot scheduled` names 03:00 in that zone.
14. **Unattended upgrades from Raspberry Pi's archive.** With the cable, after a day (or a restart past 06:00, when apt's daily timer runs), in "unattended upgrades":
    - `Unattended-Upgrade::Origins-Pattern` has the Raspberry Pi line.
    - Among the `release` lines, the one for archive.raspberrypi.com has `o=` and `l=` equal to the drop-in's `origin=` and `label=` (`Raspberry Pi Foundation`, to be confirmed). If they differ, correct `files/etc/apt/apt.conf.d/52hub-raspberrypi-origin`.
    - The log's "Allowed origins are:" line includes it.
