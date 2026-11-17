# Pi image, hub networking, releases from GitHub, version staging (design)

Status: agreed in conversation 2026-10-19. Hardware is not available yet: everything that can be built and checked without a Pi is built now; what needs a Pi is listed under "Verified on hardware later". The Windows service follows this work.

## Purpose

A Raspberry Pi kit that a facility can set up with no screen, no keyboard and no IT staff, on any of the network set-ups in DEPLOYMENT.md section 4, running one image for every country, upgraded to the version each country chooses on central. Success: flashing the published image on a kit's disk gives a hub that reaches set-up on a cable, on a facility Wi-Fi joined from a phone, or as the facility's own Wi-Fi; and a country moves its hubs to a new release in stages from the Facility Hub app.

## 1. The image

**Build method** (measured in the sandbox, 2026-10-19; works without loop devices or mounts, so also in CI containers):

1. Download the pinned Raspberry Pi OS Lite 64-bit image (trixie, `2026-10-06`) and check its published SHA-256.
2. Cut out the boot (FAT) and root (ext4) partitions with `dd` at the offsets `sfdisk -d` reports.
3. Extract the root file system with `debugfs rdump`, then restore mode, owner and group of every entry from the ext4 itself (`debugfs ls -p` per directory; rdump drops setuid and owners). Checked: `sudo` and `passwd` 4755, `/tmp` 1777.
4. Customise in a `chroot` (as root; the host's `resolv.conf` copied in for the duration). `apt-get install` works there without `/proc`.
5. Rebuild the root file system with `mkfs.ext4 -d`, edit the boot partition with mtools (`mcopy`), and write both back into a new image with the original disk identifier, so the `PARTUUID`s in `cmdline.txt` and `fstab` still match. Compress with `xz`.

Files: `image/build.sh` (the steps, run as root, arm64 host), `image/fixmodes.py` (step 3), `image/customize.sh` (step 4, run inside the chroot), `image/files/` (configuration copied in), `.github/workflows/image.yml` (the same on an arm64 runner, on release tags).

**What the image contains** (DEPLOYMENT.md section 2, made exact):

- The hub binary at `/usr/local/bin/hub`, owned by the `hub` user (self-upgrade replaces it), `deploy/hub.service`, and `/etc/hub/hub.yaml` with no country settings: `listen: ":443"`, `http_listen: ":80"`, `data_dir: /var/lib/hub`, `usb_dir: /media/hub-usb`.
- Host name `dhis2`; `avahi-daemon` (already in Pi OS).
- NetworkManager (already in Pi OS) for every connection, a polkit rule that lets the `hub` user manage it, and its own DNS for shared (access-point) connections.
- `chrony` with public NTP pools, falling back to the RTC; `nftables`: inbound 80 and 443 and DHCP/DNS on the hub's own Wi-Fi, mDNS, nothing else; `unattended-upgrades` for security fixes; the hardware watchdog (`RuntimeWatchdogSec` in systemd).
- The USB stick mounted at `/media/hub-usb` by a udev rule and systemd mount for the first USB storage partition (FAT or exFAT), owned by `hub`.
- No SSH server, no console login user: Pi OS's first-boot user setup is disabled and the default user removed. A first-boot unit regenerates the machine ID.

**Output:** `dhis2-hub-<version>-arm64.img.xz`, listed with its SHA-256 in the release's signed `release.json` (section 3).

## 2. Hub networking (`internal/netsetup`)

The hub drives NetworkManager through `nmcli`, behind a small interface so tests use a fake. Windows hubs have none of this (the PC's own network is used); the part is off unless the image's `hub.yaml` sets `network: networkmanager`.

**Connections it manages** (all others, such as the cable's default DHCP connection, are left to NetworkManager):

| Name | Kind | When |
|---|---|---|
| `hub-setup` | Wi-Fi access point "Hub-<hub ID>", WPA2, shared (the hub hands out addresses), every DNS name answered with the hub's own address | while the hub has no network |
| `hub-wifi` | Wi-Fi client, the network the admin chose | case A without a cable, case B |
| `hub-facility` | Wi-Fi access point, the name and password the admin chose, shared, internet through the cable or USB modem | case C |

**Behaviour:**

- At boot, and while running: if no cable or USB modem has an address and no `hub-wifi` connects within 60 seconds, start `hub-setup`. When a cable later gets an address, stop it. If a joined `hub-wifi` is gone for 15 minutes, `hub-setup` returns.
- The setup Wi-Fi's password is made at first boot (12 characters, no look-alike letters), kept in the data folder.
- Before starting `hub-setup`, the hub scans and keeps the list (one radio: scanning while it is an access point drops phones).
- The setup page (and the admin page after set-up, for staff of the facility) gets a "Network" section: the current connection (cable, USB modem, Wi-Fi "<name>", the hub's own Wi-Fi "<name>", or setup Wi-Fi), its addresses, and while the setup Wi-Fi is on:
  - **Join a Wi-Fi:** the scanned networks, a password field. The page tells the admin to switch the phone to that network and open `http://dhis2.local`, then the hub switches. If joining fails within a minute, `hub-setup` returns with the reason (wrong password, not found), shown at the next visit.
  - **Make this hub the facility's Wi-Fi** (needs a cable or USB modem with internet, or warns that there is none): a name and a password (at least 8 characters). `hub-setup` stops, `hub-facility` starts.
  - **Use the cable** is automatic: plugging one in is enough.
- **`facility-hub/hub.txt` on the USB stick**, rewritten when anything in it changes: hub ID, `http://dhis2.local`, the current addresses, the setup Wi-Fi's name and password, and the hub's own Wi-Fi's name if case C (not its password, which the facility chose). Plain text: the setup Wi-Fi only leads to the setup page, which needs the setup code or a staff login.
- **Proxy:** the admin page has a proxy field (`http://host:port`), kept in the store, used for every outbound connection (central, GitHub). Empty uses the environment's `HTTPS_PROXY`, as now.
- **Reached from a device:** the hub notes the last request from another device on the LAN by `dhis2.local` and by IP address (from the `Host` header, either listener). The admin page shows "Devices reach this hub by dhis2.local (last 10:42)" or "only by IP address: set a DHCP reservation", the set-up check DEPLOYMENT.md asks for.
- **USB modems:** nothing to configure; a modem of the supported kind is a wired connection. The network section names it ("USB modem") by its driver (`cdc_ether`, `rndis_host`, `cdc_ncm`).

## 3. Releases from GitHub

- **Built in:** the release source (`https://github.com/dhis2-utils/poc-data-proxy/releases/download`) and the supplier's ed25519 public key are set when the binary is built (`-ldflags -X`; the default is the current repository, `dhis2-utils/poc-data-proxy`, changed in one place when it moves), not in `hub.yaml`. `release_channel` and `release_public_key` are removed. Tests point the upgrader at a local server.
- **A release on GitHub** (tag `v<version>`) holds the binaries (`hub-linux-arm64`, `hub-linux-amd64`, `hub-windows-amd64.exe`), the image, and `release.json` with `release.json.sig`: `{"version", "files": {"linux/arm64": "hub-linux-arm64", ...}, "sha256": {...}, "image": {"file", "sha256"}}`. No stages and no `centralUrl` (central moves use move codes and the planned move). `hub release` writes these files into a folder for upload; `.github/workflows/release.yml` builds, signs (the key in a repository secret) and uploads them.
- **The country's choice** is `facility-hub-admin/release` on central: `{"version": "0.3.0", "pilotHubs": ["hub-…"], "percent": 10}`, written by the app (section 4), shared like the other admin entries (public read, hub managers read-write).
- **A hub, after each sync cycle:** reads that entry; if its version is newer than its own and the hub is in the stage (its ID in `pilotHubs`, or its hash under `percent`, as `InStage` does now), fetches `v<version>/release.json` and its signature from the release source, checks them, and upgrades as now (push, migrate a copy, swap, health check, roll back). It never moves to an older version; a version that failed its health check on this hub is not tried again. No entry: no upgrade.

## 4. Version staging in the Facility Hub app

- **A "Hub version" page** for superusers and hub managers:
  - the version: picked from the supplier's GitHub releases (`api.github.com/repos/dhis2-utils/poc-data-proxy/releases`, readable from the browser) or typed;
  - the stage: pilot hubs only (picked from the hub list), 10 %, 50 %, 100 %, with "Next stage" as one button;
  - progress, from the heartbeats: hubs in the stage, on the version, failed or rolled back (with the reason), not yet reporting.
- The hub list gets the version as a filter.
- The app computes "in the stage" with the same hash as the hub (`InStage`), with a shared test table as for the codes, so the counts match what the hubs do.

## Testing

- **Unit:** `internal/netsetup` against a fake `nmcli` (the state machine: boot without network, join, join failing, case C, cable plugged in, joined network lost for 15 minutes, `hub.txt` contents); the `Host` tracking; the proxy setting; the upgrader against a local release server (newer, older, not in stage, bad signature, failed version not retried); `InStage` in Go and TypeScript against the same table.
- **e2e:** a hub upgrading from a fake central's `facility-hub-admin/release` and a local release source.
- **The image:** `image/check.sh` inspects a built image without booting it: partitions and `PARTUUID`s, setuid bits, the hub binary and unit, `hub.yaml`, host name, no SSH, no login user, enabled units, polkit rule, nftables rules, `e2fsck` clean.
- **The app:** the staging page's logic in jest; `TestFacilityHubApp` extended to set a stage and read the progress.
- **Verified on hardware later:** booting, NetworkManager's access-point mode and captive DNS, joining Wi-Fi, case C with real clients, USB modems, the watchdog, the RTC, the USB stick mount, upgrades on a Pi.

## Docs

DEPLOYMENT.md sections 2, 4 and 6 ("Upgrades") updated from "plan" to built, with what is still unverified on hardware; README (build the image, release); FACILITY-HUB-APP.md (the version page and `facility-hub-admin/release`); TESTING.md (image check).
