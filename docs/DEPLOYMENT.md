# Deploying facility hubs at scale

How the hub in this repository could be rolled out to hundreds of facilities on Raspberry Pis (or, as an option under consideration, on an existing Windows PC): the hardware kit, the single country image, what happens at the facility, the network set-ups we will meet, how existing and new client devices are pointed at the hub, and how the fleet is run afterwards.

This is a proposal built on what the POC showed (see [FINDINGS.md](FINDINGS.md)). Items marked **(to build)** do not exist in the POC yet.

## Principles

1. **One image per country, no per-facility configuration.** Every hub is flashed with the same image. A hub learns its facility, programs and users from central when a setup user logs in (spec 6.2). A box can go to any facility, and a spare can replace any hub.
2. **Nothing to install on site.** The installer plugs in power and network and logs in once. No keyboard, screen or command line.
3. **Central stays the source of truth.** A hub can be wiped or replaced at any time without data loss once its outbox is pushed. Backups of hubs are not needed.
4. **Devices keep reaching the hub when its address changes.** IP addresses change when a router is replaced, and tablets that point at a stale IP break silently. Devices use `https://dhis2.local`, which follows the hub (section 4, "Reaching the hub securely").
5. **Fail closed.** If the hub cannot decide something, it says "needs connection" rather than guessing.
6. **No IT staff at facilities, no visits needed.** The people at a hub facility are the staff who enter data. Setting up, replacing and unlocking a hub are things they can do with an installation card; everything else is done from central.
7. **A hub only where it is needed.** A hub is for sharing data between staff and devices inside a facility while the internet is down. Most small facilities do not need one (section 0).
8. **Keep it simple.** No new national infrastructure: the software comes from GitHub releases, and the country's settings and decisions live on its central DHIS2.
9. **One image for every country.** The image and the Windows installer are the same everywhere; nothing country-specific is flashed or built in. Central's address is given at set-up, from the country's installation card.

## 0. Roles, and which facilities get a hub

### Who does what

| Who | What |
|---|---|
| **National DHIS2 team** | Plans the rollout and chooses hub facilities with the districts. On central DHIS2: installs the Facility Hub app, checks user accounts, protects the hub's dataStore namespaces, sets retention, chooses the hub version, watches every hub, follows up refused changes in bulk, checks the hubs before upgrading central or tightening metadata. Trains district officers and facility focal points. |
| **MoH IT** | Buys and prepares Pi kits (flashing the supplier's image at a depot, spares), installs the Windows service on chosen PCs. |
| **District** | Proposes hub facilities, delivers kits, helps facilities by phone, follows up refused changes from the Facility Hub app, sends spares. Not needed on site. |
| **Facility** | A facility focal point (assigned to the facility, in the hub owners group) sets up the hub with the installation card; every staff member logs in once online and moves their devices to the hub; staff keep the hub powered and log in after a restart. |
| **Software supplier** | Publishes signed releases on GitHub (binaries, the Pi image, the Windows installer) and the Facility Hub app, tested against the DHIS2 versions they support. |

### Which facilities get a hub

Give a facility a hub when patients pass between staff on different devices during a visit (reception, lab, pharmacy, maternity), when staff work in the browser (Capture web, the Aggregate Data Entry app) and must keep working during outages, when outages are frequent or long, or when it is big enough that registering a patient twice is a risk. Leave it without one where one or two staff each capture their own patients on their own tablet: the Capture app works offline on its own and syncs when it can. The national team decides with the districts and revisits it yearly.

## 1. Hardware kit

One kit per facility, assembled and flashed at a central depot.

| Part | Recommendation | Why |
|---|---|---|
| Board | Raspberry Pi 5, 4 GB (a Pi 4 works for smaller facilities; Compute Module boards under "Options under consideration") | The hub itself needs a few hundred MB at most; the rest of the RAM caches the data file, which decides speed once the file outgrows it (about 2.5 GB at 100 000 people; docs/PERFORMANCE.md). |
| Storage | 128 GB NVMe SSD on an M.2 HAT (Pi 5) or a USB 3 SSD (Pi 4). Not an SD card. | The store writes on every sync. SD cards wear out and corrupt on power loss. A Pi 4 on its SD card ran 72 hours offline in session B (PERFORMANCE.md), which says nothing yet about wear over years. |
| Power | Official power supply, connected to a UPS. Budget one where the facility has none: no small UPS for the kit has proven good enough yet (below) | Power cuts are common. A clean shutdown protects the store, and a UPS keeps the LAN up during short cuts. |
| Clock | Required: the Pi 5's RTC with its battery; a Pi 4 has no RTC and needs an add-on RTC module | TLS and PAT expiry need the right time after a power cut with no internet, and offline the hub's clock decides which aggregate period is current and when a period expires: a board restarting at its last saved time would answer a new month's form as locked until it reaches a time server. (The hub's pulls follow central's clock, not its own.) Without one, the Pi image starts at its base image's date after a power cut with no network, not at its last saved time: NO-RTC.md. |
| USB stick | An industrial USB stick, in the box | Two jobs, one stick (built; `usb_dir`): before set-up it carries the hub's setup code (below); after set-up it holds what a restore needs: every pending save, the key file and the hub's state, copied each minute (RECOVERY.md, option C2), so a dead disk loses at most a minute of saves; the cached data comes back from central. |
| Case | Wall-mountable; being tested: a passive-cooled metal case for the Pi 4, a fan (active cooler or fan case) for the Pi 5 | Heat and dust. A fanless case has no fan to fail; the Pi 5 may need one to avoid throttling. Chosen after the tests (HARDWARE-TESTING.md). |
| Network | Ethernet cable; optional USB 4G modem for facilities with no uplink | See section 4. |
| Label | Hub ID and `dhis2.local` | The label is what staff and support use to find the hub. Nothing depends on it: the stick has the same (`hub.txt`, section 4). |

Rough cost per kit: USD 150–220.

## 2. The hub image and releases

One generic image per release (built 2026-10-19: `image/`, see image/README.md). The supplier's CI builds it from Raspberry Pi OS Lite and publishes it, with a signed checksum, in the GitHub release next to the hub binaries (`.github/workflows/release.yml`). The build needs no loop devices or mounts: it extracts the root file system, customises it in a chroot and rebuilds it (FINDINGS.md, "Building the image without mounts"). Nothing in the image is country-specific, so no country maintains its own image across hub and DHIS2 releases. `image/check.sh` checks every built image without booting it. The image contains:

- **Raspberry Pi OS Lite, 64-bit** (trixie). No desktop, no SSH, no console user, no cloud-init: set-up happens on the hub's own page.
- **The hub binary** at `/usr/local/lib/hub/hub`, in a folder the `hub` service user owns so it can upgrade itself (linked from `/usr/local/bin/hub`); a Wi-Fi unblocked at boot (Pi OS blocks it until a country is set; the world regulatory domain applies); and the systemd unit from [`deploy/hub.service`](../deploy/hub.service).
- **No country settings.** Central's address is given at set-up (section 3), from the country's installation card. The release source and key are the supplier's, built into the binary. `hub.yaml` has only the ports, folders and `network: networkmanager`.
- **Networking:**
  - NetworkManager for every connection, with a polkit rule that lets the hub manage it;
  - its built-in DHCP and DNS for the hub's own Wi-Fi (section 4);
  - `avahi-daemon` for `dhis2.local`, with the host name `dhis2`.
- **Time:** `chrony` with public NTP pools, falling back to the RTC.
- **Firewall** (`nftables`): from private addresses only, ports 80 and 443 and mDNS, plus DHCP and DNS on the hub's own Wi-Fi.
- **Updates:** `unattended-upgrades` for OS security fixes, rebooting at 03:00 when needed. The hub binary upgrades itself from the supplier's GitHub releases, to the version the country chose on central (section 6, "Upgrades").
- **Watchdog:** the hardware watchdog (`RuntimeWatchdogSec=15`), plus `Restart=always` in systemd.
- **The USB stick** is mounted for the hub at `/media/hub-usb` when plugged in (FAT or exFAT; a udev rule). Any stick is mounted, but once set up the hub copies only to the box's own: the stick in the box before set-up, or one an admin took on later on the hub's page (RECOVERY.md, C2, "Only the box's own stick").
- **No remote access.** A support VPN was considered and is out of scope for now: it needs a server, and it would add little, since the hub's data stays encrypted until a staff member logs in. Support works from central (section 6). A hub that does not come up at all writes what support needs to `hub-diag.txt` on its boot partition, readable on any computer (image/README.md).
- **A machine ID made at first boot**, so cloned images do not share one.

**Release key rotation.** Every hub binary embeds the supplier's release public key (`upgrade.ReleaseKey`, set at build); a hub runs only a release whose manifest (`v<version>/release.json`) verifies under that key, and a manifest that does not verify is ignored and not fetched again until the country's release choice changes (FINDINGS.md, "Found by adversarial testing" 29). To rotate the key, the supplier publishes one intermediate release whose manifest is signed with **both** the old and the new key and whose binary trusts both, then a later release that drops the old key. Hubs take the intermediate release through the normal channel and are ready for the new key; a hub that somehow missed it stays on its current release (it cannot run anything unsigned) until a release signed by a key it trusts is chosen. Never re-sign an existing version with a new key: hubs that already refused or ran that version will not look at it again.

**Verified on hardware later** (local builds of the image have run on a Pi 5 and a Pi 4 since late October, and the USB stick was tested in session B, 4–9 November; FINDINGS.md):
- booting a release image, and the root file system growing to the disk;
- NetworkManager's access-point mode with the captive DNS, joining a Wi-Fi, and case C with real devices;
- USB modems;
- the watchdog, the RTC with `chrony`, and the USB stick mount;
- an upgrade on a Pi.

The image holds **no secrets**. Each hub gets its own data key, hub ID and signing key pair at first boot and setup. Patient data on the SSD is encrypted by the hub itself: it is in a data file encrypted with AES-256-XTS under the data key, so full-disk encryption is optional.

The outbox keeps each offline save after central accepted it, for 30 days from when it was sent (`store.SentOutboxKept`), so it can still be sent again. The sync cycle then deletes it. Refused, partly refused and waiting saves stay until they are resolved. Before 2026-10-28 the hub deleted no outbox entry, so it kept a copy of every offline save past the retention a country set. A save sent before that date counts from when it was made.

**What is not encrypted** (security review 2026-10-20, S1), all in the data folder and readable only by the hub's account (and root or administrators): on a Pi, `deploy/hub.service` makes the folder 0700 and what the hub writes 0600 (`StateDirectoryMode`, `UMask`; before review R-26 the folder was 0755 and the store 0644, readable by any local account; a hub keeps the unit of the image it was flashed with, since self-upgrade replaces only the binary); on Windows the installer limits the folder to the service account, administrators and SYSTEM; on a Mac it is 0700. On a machine with a TPM 2.0 (a TPM board on the Pi, most PCs), the hub seals a device key in the TPM and the items marked *sealed* are encrypted with it (`internal/devkey`, built 2026-10-20):

- **The key file:** hub ID, mode, central URL, the proxy, and each user's username and key slot. Each slot lets someone holding the disk test password guesses offline; with a TPM, the slots on the disk are bound to the device key (*sealed*), and only the USB stick's copy opens with a password alone.
- **The TLS keys** (*sealed*):
  - the server's `tls.key`, which the hub needs before anyone logs in, to serve its login page over HTTPS;
  - the authority's `ca.key`, which renews it.
- **Before set-up:** the setup code (`setup-token`, *sealed*).
- **On a Pi:** the setup Wi-Fi's password (`setup-wifi`), the password of the hub's own facility Wi-Fi (`facility-wifi`, case C) and the chosen Wi-Fi's name (`network.json`), all *sealed*. NetworkManager keeps a copy of each Wi-Fi password, readable by root only.
- **The logs:** templates and parameter names only, no values.

The hub's own account password, the move key and the users' tokens are in the encrypted state.

**What a stolen disk or stick gives, then:**
- **Offline password guessing** against the key slots. The weakest staff password sets the bar.
- **Impersonation of this hub** (`dhis2.local`, its names, private addresses) **to the devices that trust its authority.** That's enough to collect the passwords staff type at their next login there.

**With a TPM**, a disk or data folder taken *without* its TPM gives neither: the slots and keys on it need the device key, which only that TPM opens. Three limits:
- **A stolen whole device** (board and TPM together) is not protected: no measured boot ties the key to the hub's own software (the Pi has none; on Windows a firmware update would lock the hub out), so whoever boots it can unseal the key.
- **The USB stick** keeps slots with the password alone, so the hub can be restored on new hardware: a stolen stick still allows guessing, and gives the unsent saves and the tokens behind a guessed password, never the patient cache.
- **A dead TPM or a disk moved to another machine** cannot be opened; the hub says so on its start page and is restored from its stick onto an empty disk, as for a dead disk. Without a stick, its unsent saves are lost.

The device key never unlocks the hub: that still takes a staff login (RECOVERY.md, option I, decided against 2026-10-20). Without a TPM, or with `tpm: false` in hub.yaml, a stolen hub is handled as a password leak: its users change their passwords, its tokens are revoked in the Facility Hub app, and its authority is removed from the devices.

## Options under consideration (not decided)

Ideas for hardware and the image, recorded with what they would imply for the hub. None is decided.

- **Storage: industrial pSLC microSD instead of an SSD.** Far higher write endurance than consumer cards, often with power-loss protection, no USB bridge or cable, low power. Capacity is a third of the raw flash (typically 8–64 GB), so the data must fit: about 6.5 GB at a busy HC IV with retention by programme, 15–16.5 GB with five years kept (docs/PERFORMANCE.md), plus the OS, the write-ahead log and a backup copy. With microSD, retention and a smaller data file become requirements. Event programs are held in full unless retention is set for them (`facility-hub-config/retention`): set it for large registers such as OPD. Random writes are SD cards' weak point: measure the card with `perf/pi.sh bench` on a real Pi, set the `pi4` profile's disk limits from it, and compare the card's rated total bytes written with the bytes the hub writes per day. On a Pi 5, an M.2 NVMe board is the alternative to a USB SSD.
- **Board: a Compute Module (CM4 or CM5) on an IO board instead of a Pi 4 or 5.** A flat board with mounting holes, easier to build into an enclosure than a Pi with a cable and a 2.5" drive. The official CM5 IO board has an M.2 slot for NVMe (no HAT, USB bridge or cable); the CM4 IO board has a full-size PCIe slot that needs an NVMe adapter card; third-party carrier boards (Waveshare and others) often have M.2 slots and cost less, but vary in quality. The official boards also have an RTC with a battery slot, Gigabit Ethernet and a 12 V barrel input, so a router-style 12 V mini DC UPS can power router and hub together. Raspberry Pi commits to producing the CM4 until at least 2034 and the CM5 for longer, which matters for spares over years. Modules come with 8–32 GB of soldered eMMC or without ("Lite"): eMMC is sturdier and faster than an SD card, but has no power-loss protection, cannot be swapped when worn (the module is scrap), and 32 GB gets tight with five years kept (15 GB) plus the OS and a backup copy. Imaging eMMC goes over USB (`rpiboot`), slower per unit at the depot than writing NVMe drives in bulk. Candidate kit: CM5 with 4 GB on an IO board with an M.2 NVMe drive, the Pi 5 + NVMe set-up on one board; compare complete kit prices. The `pi5` perf profile approximates it, and the `pi4` profile with `IO=0` a CM4 with NVMe.
- **Storage at scale: budget drives, chosen by test.** At 1000 units, a known-brand SSD costs perhaps USD 15–30 more per unit than a budget one (USD 15–30k), against field trips of perhaps USD 50–200 per dead drive. The bigger risk with budget drives is parts changing between batches. So: fix one exact part number with the supplier; shortlist two or three cheap candidates (tried for the POC: Intenso High Performance and Verbatim Vi550 S3, both 2.5" SATA, on a Pi 4 through StarTech's USB 3.0 SATA cable, whose ASMedia bridge is widely reported to work with UAS on the Pi 4); put 3–5 units of each through `perf/pi.sh bench`, the paced run (bytes written per day) and 100 power cuts under load; buy the cheapest that passes. Notes from the search: M.2 *SATA* drives do not work in the Pi 5's NVMe HATs; portable NVMe-based USB SSDs can draw more current than a Pi 4's ports supply; on the Pi, check `lsusb -t` shows `Driver=uas` and `dmesg` shows no resets under load (`usb-storage.quirks=<vid>:<pid>:u` falls back to the slower mode).
- **Storage for pilots: endurance microSD cards.** Rated for years of continuous video (sequential writes, so the rating overstates the margin for the hub's small synced writes); a year of a pilot is very likely within it. They are no better than other cards at power cuts and keep SD speed: 30 k people worked well with 1 GB in the ladder, 60 k was borderline, 100 k slow (docs/PERFORMANCE.md); 4 GB boards help, since the data file then fits in memory. Conditions: genuine 64–128 GB cards from a reputable supplier, a spare per site or district, a router UPS where possible, facilities sized to the board.
- **Power banks as a UPS: not recommended.** Many cut their output for a moment when mains comes or goes, which reboots the Pi; they sit at full charge in heat (wear, swelling); they give no power-fail signal, so the cut is only delayed; some do not switch back on after running flat. A router-style mini DC UPS (12 V and 5 V outputs, made to stay in line) does the job for about USD 20–40, and keeps the router up too.
- **Pi Zero 2 W for very small facilities: possible, not preferred.** 512 MB (cache for the data of about 5–10 k people), a CPU two to three times slower than a Pi 4's, SD storage unless an SSD is added over USB 2 through an adapter, 2.4 GHz Wi-Fi only (one radio: join the facility Wi-Fi or run an access point, not both well), Ethernet only through a USB adapter. It saves USD 20–40 against a Pi 4 1 GB or Pi 5 2 GB in a kit dominated by storage, power and case, and adds a second board family to support.
- **Read-only OS, logs in RAM.** Only the data partition takes writes, so a power cut cannot damage the system. Implications: OS fixes become image updates instead of `unattended-upgrades` (see A/B below); without an RTC the saved clock must live on the data partition (the Pi 5 has an RTC with a battery connector); keep the last crash or a small rotating log on the data partition, since logs in RAM are lost exactly when they are needed.
- **A small UPS with a shutdown signal.** The hub stops cleanly on SIGTERM, so a UPS that triggers a normal shutdown is enough. Requirements: a power-fail signal (GPIO or I2C), automatic power-on when power returns (essential unattended), a chemistry that tolerates heat, and enough current for a Pi 5. Kinds to evaluate (availability not checked): supercapacitor modules (shutdown only, nothing to wear out or swell), LiFePO4 boards (for example LiFePO4wered/Pi+), 18650 Li-ion HATs (for example the Geekworm X728 and X1200 series, Waveshare's UPS HAT; ride through outages, but Li-ion ages in heat), integrated controllers (PiJuice, UPS PIco).
- **Two system partitions (A/B).** Boots the previous system if an update fails, and is how a read-only OS gets updated. The hub's own upgrade (spec 12a) already migrates a copy of the store and keeps the old one. The catch: an older hub refuses a store migrated to a newer schema, so a rollback after the new version has written data must either switch back to the pre-upgrade store copy (losing what was written since) or schema changes must stay readable by the previous release. To settle before the first release.
- **Locking the device against changes.** In layers, cheapest first:
  - *Network:* beyond the firewall above (HTTPS from the LAN only), ship no SSH server; lock the root and default accounts. Hub settings change only through the hub UI, for staff assigned to the facility.
  - *Console:* disable the login prompts on screen and serial console, and remove the serial console from the boot settings.
  - *Physical access to the card or disk* is the remaining gap: anyone can edit the system on another computer, for example to record passwords at the next login and so get the data key (patient data itself stays encrypted). Secure boot closes it: a public key burned into the board's one-time-programmable memory, after which only signed images boot, with a verified read-only system partition (dm-verity) checked by the signed image. Pi 4 and Pi 5 are believed to support this; to be checked against Raspberry Pi's current documentation. It fits with the read-only OS and A/B partitions above, which update through signed images anyway. Costs: burning the key is irreversible, the signing key becomes critical infrastructure (lost: no new images; leaked: no protection), and the boot order must be locked too.
  - *Out of reach:* theft, unplugging or wiping. With the above, a modified or foreign system does not boot and the data stays encrypted; what remains is continuity, covered in [RECOVERY.md](RECOVERY.md).
  - Likely more than a first deployment needs; recorded for later discussion.
- **Operating system: gokrazy instead of Raspberry Pi OS.** gokrazy is a minimal Linux for Raspberry Pis in which every program on the device is a Go binary: no shell, no C programs, no package manager, a read-only root on A/B partitions, updated by writing a new image over the network. The hub is one static Go binary with pure-Go SQLite, so it would run unchanged, and Linux's drivers for USB and NVMe storage, Ethernet and the watchdog stay. It reaches most of "Locking the device against changes" above in one step. To check: Pi 5 support and the SSDs on both boards; how the hub's own self-upgrade fits gokrazy's image updates (one mechanism should own updates); the network set-ups that need more than a client (the hub's own access point in case C, mDNS, a UPS power-fail signal on GPIO), which would have to come from Go programs too; and how support staff look at a device without a shell. Considered and not preferred: unikernels (MirageOS, Unikraft, Nanos). They run on a hypervisor rather than directly on a Pi, so Linux stays underneath; MirageOS would mean rewriting the hub in OCaml; and the risks that matter most for the hub (a stolen disk, weak passwords, the hub's own access rules) are not in the operating system.
- **No hardware: the hub as a Windows service on an existing facility PC** (built 2026-10-20, see "The hub on a Windows PC" below; run on a Windows VM, not yet on a facility PC). The alternative to a kit. Same hub, same store, one PC. The notes below are what was known before it was built.
  - *Code:* the hub cross-compiles for Windows except in two places: free disk space for the status page (`statfs`) and the self-upgrade, which replaces the running process and hands it the data key (`internal/upgrade`). Windows cannot replace a running process or overwrite a running `.exe`, so the upgrade needs its own flow: Windows restarts the hub service on the new version (through its Service Control Manager; no person involved), with the data key handed over so the hub does not stay locked. The rest is small: Windows paths (`C:\ProgramData\...`) instead of `/var/lib/hub` and `/etc/hub`, access rules on the data folder instead of `0600`, and answering stop and shutdown from the service manager. File locking on Windows was checked on the VM, 9 November: Windows refuses to rename a store file that is still open (a request still running, or a virus scanner), which Linux allows, and an upgrade failed at its store swap; the swap now retries for up to a minute (FINDINGS.md, E4). Not checked yet: flushes on Windows, and the test suite on a Windows runner.
  - *The PC:* must not sleep or hibernate (power plan); Windows Update reboots it; antivirus must exclude the data folder, or it locks and scans the database and its write-ahead log; it is someone's working PC, with full disks, other software and people switching it off. A desktop has no battery, so a power cut is a hard stop. Resources are not a concern: an old i3 with 4 GB and an SSD is faster than a Pi 5, with no SD card wear.
  - *After a restart* the hub is locked until a user with a key slot logs in through it (RECOVERY.md, notes). With Windows Update this happens more often than on a Pi. Windows offers what option I in RECOVERY.md would need, a secret held by the machine for a service at boot (DPAPI, the TPM); option I was decided against (2026-10-20): the first login after a restart unlocks the hub.
  - *Network:* the installer adds a firewall rule for the hub's port; a network marked "Public" may still block it. There is no "hub provides the network" mode (section 4, case C): Windows' mobile hotspot takes about 8 devices and expects an uplink to share. A facility needs a working router. A laptop that leaves the building takes the hub with it, so a desktop that stays put is better.
  - *Confidentiality:* data at rest stays encrypted, but anyone with Windows admin rights on the PC, or malware, can read the running hub's memory, and users of the PC can open the hub UI. Run the service under its own restricted account, restrict the data folder to it, and prefer a PC not used for email and browsing.
  - *Rollout:* an installer with admin rights (service, firewall rule, antivirus exclusion, power plan, data folder), signed with the supplier's code-signing certificate and published on GitHub, or SmartScreen and antivirus warn about it. MoH IT installs it through the ministry's software deployment, or the PC's owner does with a USB stick and a one-page guide. Support is harder, because every PC differs.
  - Considered and not preferred:
    - *Docker on Windows.* It needs the WSL2 virtual machine: Docker Desktop starts only when a Windows user logs in (a reboot then stops everything until someone does), may need a paid licence for large organisations, needs virtualisation enabled in the BIOS and 1–2 GB of extra memory, and puts the container behind its own address translation. It keeps the Linux image and self-upgrade, but fixes none of the PC issues above and hides DPAPI and the TPM.
    - *Several PCs sharing the hub, one acting as master.* Electing a master safely needs at least three machines switched on; otherwise two can each believe they are master. Accepting writes on more than one machine needs conflict merging, which the single-writer design (one outbox, central decides access) does not have. Devices also need one address that follows the master. A standby that only receives copies of the encrypted data file, and is promoted by hand, gives most of the benefit (RECOVERY.md, option C).

## The hub on a Windows PC (built 2026-10-20; run on a Windows VM in sessions A and B, FINDINGS.md)

For facilities without a Pi kit: the same hub on a PC they already have. That can be a records-room desktop or laptop staff use for their work, or a small server already there for another programme (OpenMRS for ART, say). The design is docs/superpowers/specs/2026-10-20-windows-service-design.md; the guide for the person at the PC is [WINDOWS-INSTALL.md](WINDOWS-INSTALL.md).

- **Expectations.** Choose a PC that stays on while staff work. When it is off anyway, staff work online, from the apps' own offline cache, or on paper: degraded service, as for a Pi without power.
- **One signed installer for every country** (`windows/`, NSIS, built and signed on Linux), from GitHub with the release or carried on a USB stick. It installs either way:
  - **for the whole PC** (administrator rights): a service under its own virtual account, started at boot;
  - **for one user** (no rights needed): it runs while that user is logged in.
- **What the installer does:**
  - picks free ports (443 and 80, else 8443 and 8080);
  - creates the hub's certificate authority and adds it to the PC's trusted roots;
  - maps `dhis2.local` to the PC itself;
  - opens the firewall for the hub;
  - optionally keeps the PC awake on mains power and asks Microsoft Defender to skip the hub's data;
  - adds a desktop shortcut to `https://dhis2.local`.
- **Devices use `dhis2.local`, as on the Pi.** The hub answers it itself on the network (`internal/mdns`), and stays silent if another device already does. The PC's name and IP are the fallback; the hub's page on the PC shows them.
- **Set-up only on the PC itself:** being at the PC proves presence at the facility, so no setup code is needed.
- **From one user to the whole PC:** installing for the whole PC over a hub installed for one user moves that hub's data, approval and certificate authority along, so devices notice nothing; its program folder stays behind, and the installer puts its own `hub.exe` in place. **Accepted (2026-10-27):** a certificate authority key that the user (or software running as them) put in their folder becomes the whole PC's hub authority and a trusted root of the PC. Its reach is that of the authority itself (section 4, "The certificate"): `dhis2.local`, the hub's names and private addresses, not public sites.
- **After a restart**, including Windows Update, the first staff login through the hub unlocks it, offline too. Until then saves wait. Nothing unlocks it without a person.
- **USB stick.** The hub keeps its copy on a stick a staff member chose on the admin page (`usb_dir: auto`), found again by its label and serial number, or its folder, whatever its drive letter. It never picks one by itself. Choosing marks the stick as on the Pi (RECOVERY.md, C2, "Only the box's own stick"): a drive with the same label and serial but without the mark gets nothing, and the admin page asks again.
- **Upgrades.** The same releases and staging as the Pi. The hub runs from its data folder, replaces its own `hub.exe` (the running one is renamed), and the service manager starts the new one. The data key is handed over in a file sealed to the hub's account (DPAPI), read once and refused after 2 minutes.
- **Not checked until a Windows machine runs it:** everything in docs/TESTING.md, "Windows".

## The hub on a Mac (for the DHIS2 team)

Not for facilities. `macos/install.sh` runs the same hub on a Mac, so the core team can try it against a test DHIS2, with the same app, staging and upgrades: as a launchd agent for the logged-in user, answering `dhis2.local` itself, its authority trusted in the System keychain. Releases carry `hub-darwin-arm64` and `hub-darwin-amd64`; macos/README.md.

## 3. From depot to facility

### On central, before the first hub

1. The national team installs the Facility Hub app on central.
2. A superuser names, in the app's Settings, the hub owners group, the hub accounts group and its role, and the hub managers group (section 6, "Protect the data store namespaces" and "The hub's own account"). A hub refuses set-up, and take-over, until a hub owners group is named: "A superuser must name the hub owners group in the Facility Hub app (Settings) first." It is left as it was.
3. The national team or the districts add the facility focal points, and whoever else may set a hub up or take one over, to the hub owners group.
4. Then the facilities set their hubs up (below).

### At the depot (bulk)

1. Flash the supplier's image, unchanged, onto the SSDs, for example with a USB duplicator or `rpi-imager --cli` in a loop.
2. Assemble the kits and power each one on once on the depot network. This runs first boot, sets the clock, creates the hub ID and checks that it can reach central.
3. Print and stick the label: hub ID and the QR code for connecting devices. The setup code is not printed: at first boot, while not set up, the hub writes the code to the USB stick in the box, in `facility-hub/hub.txt` (the line `Setup code:`, with the hub ID and the setup Wi-Fi's name and password) (built). A disk flashed at a district therefore carries its code on the stick already in the kit, with no inventory record needed. Any stick plugged in while the hub is in set-up mode also gets it, in case the kit's stick is missing; once set up, the hub removes the line and never writes it again, and a used code no longer works. The code in plain text next to the encrypted backup weakens nothing: it only allows set-up of an unconfigured disk. (Settled: the hub keeps the same code across reboots until set-up uses it; then it removes the line. A `setup-code.txt` that hubs before 2026-10-28 wrote is removed.) With a generic image, a fresh hub does not know central yet, and the assignment check runs on whatever server it is pointed at: without the code, anyone on the facility Wi-Fi could open the set-up page first and point the hub at their own server. The code means only someone holding the box can set it up. (A Windows hub needs none: its set-up opens only on the PC itself.) The depot list of hub IDs becomes the inventory; it matches the `facility-hub-registry` namespace on central.
4. Pack it with a one-page installation card.

No facility is assigned at the depot. Any box can go anywhere.

### At the facility (about an hour, by facility staff)

No one from the district needs to come. Kits arrive by courier or with a routine district visit; Windows PCs get the hub from MoH IT beforehand.

**What the person setting up needs, besides the kit:** a device that can read the USB stick in the box: a laptop, or a phone or tablet with a USB adapter (USB-C or OTG). The setup code is only on the stick (`facility-hub/hub.txt`, not printed on the box), and every Pi hub asks for it; Ethernet does not change that. Without Ethernet the same file also gives the setup Wi-Fi's name and password. A facility with neither a laptop nor an adapter needs one sent with the kit: the depot keeps no record of the codes.

1. Mount the hub near the router and plug in power and Ethernet, as on the installation card. The card covers the case with no router (section 4, case C).
2. On any laptop or phone on the facility network, open the address on the label (on the Windows PC, the hub's page opens on the PC itself, and set-up only opens there). The hub shows its setup page. Scan the QR code on the installation card to give it central's address, and enter the setup code from `facility-hub/hub.txt` on the USB stick in the box (read on a laptop, or a phone with a USB adapter). The hub accepts only `https` with a publicly trusted certificate, and shows the server's name before anyone logs in (built). Scanning works in browsers with the BarcodeDetector API on a secure page (Chrome on Android); elsewhere the address is typed.
3. The facility focal point (assigned to the facility, and in the hub owners group) logs in with their own DHIS2 account. The hub then:
   - takes the facility from the user's org units, asking which one if they have several: anyone assigned to the facility may do this, and the hub serves the org unit chosen, at whatever level (built). Choose the facility itself, not a parent: generated values whose pattern has `ORG_UNIT_CODE` are made with the chosen unit's code, so a hub set up on a unit with children would hand the children its code offline (FINDINGS.md, decided limitations, T1);
   - reads the facility's programmes;
   - registers itself on central, offering to retire an old hub for the same facility (section "Replacing or moving a hub");
   - creates its hub token (section 6, "The hub's own account").

   No patient data is downloaded at this point, unless the focal point's own account can see some: every pull runs with one user's own token.
4. Every staff member logs in once through the hub while it is online. The hub then downloads what that person may see on central, with their access, and they can log in offline from then on. Data reaches the hub only through the people who use it. Still online, on the device they will use, each staff member then opens Capture (each of their programmes' working lists) and the Data Entry app (each of their data sets' forms) once. The browser keeps the apps, and the hub keeps each screen's settings and metadata for that person. Since 2026-10-29 the hub also loads, at each online login, the screens anyone at the facility opened online in the last 14 days, for that person and with their access (FINDINGS.md, "Shared metadata cache"). So what the first staff member opens reaches the others at their next online login, a minute or two after it. A screen nobody has opened online through the hub is still not available offline.
5. Point the client devices at the hub (section 5).
6. The offline check: unplug the uplink, register a test patient on one tablet, sync, and find them on another.

If something fails, the focal point calls the district, which sees on central (the Facility Hub app) whether the hub registered and what it last reported.

### Replacing or moving a hub

- **First find out what broke.** If the disk is fine and only the board (Pi) or PC failed, move the disk or the data folder to the replacement: it starts as the same hub with all its unsent data, and needs no setup. A dead disk is restored from the USB mirror stick (RECOVERY.md, option C2), losing at most a minute of saves (built: RECOVERY.md, C2). The new disk's setup page offers to restore the hub from the stick; a staff member of that hub confirms with their own password, and it restarts as the same hub, with the same ID in the registry (the key slots are in the copy). Its state (present, last copy and snapshot, failing, free space) shows on the hub's page and in the heartbeat (`usb`). Facility staff only plug the stick in and keep it when a disk is replaced. Only a lost hub is replaced by a new one.
- **Replacement:** a spare arrives by courier, the same day if need be; a staff member assigned to the facility sets it up and confirms "replace the old hub". Allowed at any time: patient data is not at stake, since a new hub gets data only through each user's own login, and the retired one stops serving data. Central records who replaced which hub and when: the old registry entry gets `retiredBy` (the new hub), `retiredByUser` and `retiredAt`, the new one `replaces` (built). The Facility Hub app shows it and the national team can undo a mistaken replacement there **(to build)**. The old one is marked retired in the registry; if it is still running and online, it stops serving patient data and only sends what it has not pushed, refused changes included, so plug it into any connection before collecting it. Anything an old hub that is lost had not pushed is gone, apart from what the mirror stick holds.
- **Decommissioning:** use "Reset hub" in the admin page, open to staff assigned to the facility. (Someone moved away from the facility on central keeps the admin page until the hub next refreshes their access: hourly by default, or at their next online login after a time offline.) It pushes the outbox, marks the hub's registry entry retired, deletes on central the tokens it holds (its own and its users', each as its owner), deletes its folder on its own USB stick, wipes the store and returns to setup mode (`TestResetDropsTokensAndStickFolder`). The registry entry keeps the list of its tokens, so any the hub could not delete can still be revoked with "Revoke all tokens of this hub" in the Facility Hub app.
- **Theft:** the store is unreadable without a staff password, because the data key is only in memory. It is only as strong as the weakest password among the users with a key slot, though: whoever holds the SSD can test password guesses offline against each slot, and a cracked password is also that user's central DHIS2 password. Treat a stolen hub as a possible password leak: ask its users to change their passwords. Then revoke its tokens: "Revoke all tokens of this hub" on its page in the Facility Hub app. On DHIS2 2.42, disabling a user or changing their password does not stop their tokens, and only the owner or the hub managers can delete them (FINDINGS.md, "Tokens outlive their user"). A hub shares the tokens it makes with the hub managers, and lists them in its registry entry; tokens made before this (2026-10-20) are covered at their next renewal. A revoked hub that comes online again drops what it still holds when it next makes a token (a staff login, or its own token's renewal) and serves nothing offline, until it is reset or its entry no longer says "revoked". It takes "revoked" in its registry entry as real only once central refuses its own token, so a forged entry does not disable it (FINDINGS.md, "Found by adversarial testing" 14). A token refused for another reason (expired during a long outage, its owner disabled) can still let a forged entry pass; the hub then drops its tokens as for a real revocation. That is undone as a forged "retired" is: set the entry's `status` back to `active` on central, then a staff login makes tokens again and "Use my account" gives the hub a token of its own (`TestForgedRevokedUndoneWhenEntryIsActive`). Unsent saves are kept; their authors log in online again so they can be sent. How long tokens live is the country's setting in the app (7–365 days, else `pat_expiry_days`, 90).
- **An insider with the disk or the stick:** every key slot opens the same data key, and users' tokens are sealed with that key, as are the central passwords kept for users who cannot make a token. So one staff member's password, together with the hub's disk or its USB stick, opens every registered user's token or kept password, including those of district or national accounts that logged in at this hub once (review R-25). Treat such a case as a leak of all of them: revoke the hub's tokens and ask its users to change their passwords.
- **Accounts that manage users work through the hub online only, by design.** A superuser (`ALL`), or an account with an authority to create, change, delete, replicate or impersonate users or to change user roles (`F_USER_ADD`, `F_USER_DELETE`, their `_WITHIN_MANAGED_GROUP` forms, `F_USERROLE_PUBLIC_ADD`, `F_USERROLE_PRIVATE_ADD`, `F_REPLICATE_USER`, `F_IMPERSONATE_USER`), gets no key slot and no stored token on any hub (review R-25). Such a token, taken from a hub as above, would let its holder make or take over any account on central. These users are passed through to central online, as users from outside the facility are, and cannot log in offline: without a token, saves made offline could never be sent. A user who gains such an authority loses the token the hub held at their next online login there; the hub deletes it on central. Facility staff, and the accounts they use on the hub, should not hold these authorities. The hub's own token is not covered: a hub set up by such an account holds that account's token until the hub's own account is approved or staff take it over (section 6, "The hub's own account"), so set hubs up with a facility account. "Use my account" refuses such accounts.

## 4. Network set-ups

Facilities differ. The image supports four cases without per-facility configuration files; the right mode is picked during setup.

### Case A: existing router with Wi-Fi and an internet uplink (most common)

```
 tablets, laptops ──Wi-Fi──► facility router ──► internet ──► central DHIS2
                                   │
                                hub (Ethernet, DHCP client)
```

- The hub is an ordinary DHCP client on the LAN.
- **No DHCP reservation needed:** devices find the hub by name ("Reaching the hub securely" below).
- **Check for client isolation.** Some routers isolate Wi-Fi clients from each other and from the wired LAN ("AP isolation", "guest network"). Tablets then cannot reach the hub.
- **Check that `dhis2.local` resolves** from a tablet; some routers filter multicast. The hub's IP works as a fallback.
- **Both checks, from a tablet** (built): open `dhis2.local` from a tablet. The admin page then says "Devices reach this hub by dhis2.local", or that they reach it only by IP address, or that no other device has opened it yet, which suggests client isolation.

### Case B: a 4G MiFi or mobile router as the only network

These small devices often have no Ethernet port and frequently isolate clients.

- The hub joins the MiFi's Wi-Fi as a client, chosen on the setup Wi-Fi ("Setup Wi-Fi" below; built, untested on hardware).
- If the MiFi isolates clients, use case C instead and connect the MiFi to the hub as its uplink.

### Case C: no usable local network; the hub provides it

```
 tablets, laptops ──Wi-Fi──► hub (access point, DHCP, DNS) ──Ethernet / USB 4G──► internet
```

- The hub runs its own Wi-Fi access point, DHCP and DNS (NetworkManager's shared mode). Its uplink is the Ethernet port (to a modem) or a USB 4G modem.
- The Pi's built-in Wi-Fi handles a small facility, about 10–15 devices. Beyond that, add a cheap external access point on the hub's Ethernet.
- This is the most robust case, because the hub controls DHCP, DNS and client isolation. It is also the simplest to support remotely, because every facility in this mode looks the same.
- Chosen on the setup Wi-Fi ("Setup Wi-Fi" below; built, untested on hardware): the setup network becomes the facility's network. It keeps the hub's name (`Hub-Zvc7xMnFcVg`), with a password the hub makes (decided 2026-10-27): 4 groups of 4 lowercase letters and digits without look-alikes (no 0/o, 1/l/i), as `k7mp-x2qd-9wtr-b4nz`, about 80 bits. The page that makes the Wi-Fi shows it, then the hub's admin page and `hub.txt` on the USB stick ("Facility Wi-Fi password:"). An admin can make a new password on the admin page ("New password"): the Wi-Fi starts again with it, and every device has to join again. A hub that joined another network and is then made the facility's Wi-Fi again uses the password it made before, so devices that knew it join again by themselves; "New password" is the way to cut them off.
- Hubs made the facility's Wi-Fi before 2026-10-27, under a name and password an admin chose, keep them until an admin makes a new password; the Wi-Fi then takes the hub's name too. Their password is in neither the admin page nor `hub.txt`.
- Its uplink: the Ethernet port to a modem or router, or a USB 4G stick of the one kind the hub supports ("USB modems" below).

### Setup Wi-Fi: a hub with no screen or keyboard (built 2026-10-19, untested on hardware)

A hub with no network cannot be configured over one, and has no screen. So it provides one for the purpose:

1. **When:** at boot, if the hub has no Ethernet link with DHCP and no saved Wi-Fi it can join, it starts its own setup Wi-Fi.
2. **Its name and password:** "Hub-" and the hub ID without its "hub-" (`Hub-Zvc7xMnFcVg` for `hub-Zvc7xMnFcVg`), with a password the hub makes at its first boot at the depot: 8 digits, the least Wi-Fi allows, typed on a phone's number pad. Not one password for every kit. The setup code is 8 digits: both only prove that someone holds the box, and wrong setup codes are throttled (50 per 15 minutes from every device together, then locked; at most about 4,700 guesses a day, so an even chance at the code takes about 29 years). A hub that already wrote a 6-digit code keeps it until set-up uses it. Both are on the USB stick in the box, in `facility-hub/hub.txt`, which the hub writes and keeps current: the hub ID, the setup code until set-up, the setup Wi-Fi's name and password, the hub's address on the facility network, and in case C the facility Wi-Fi's name and password. Only the setup code's line goes after set-up; the rest stays, since the setup Wi-Fi is needed again when the facility's network changes. The label may repeat them, but nothing depends on a label or a sheet surviving: whoever holds the box has the stick. (Plain text on the stick weakens nothing: the setup Wi-Fi only leads to the setup page, which needs the setup code or a staff login, for set-up and for changing the hub's network alike. On an image built for one central or a list of them, set-up needs no code, so before set-up the setup Wi-Fi's password is then the only gate on the network forms.)
3. **What the admin does:** connect a phone to that Wi-Fi; the phone opens the hub's setup page by itself, as a hotel Wi-Fi does (the hub answers every name with its own address). The page offers:
   - **Join an existing Wi-Fi** (case B, or case A without a cable): pick a network from a scan, enter its password;
   - **Make this hub the facility's Wi-Fi** (case C): the setup network becomes the facility's, under the same name, with a longer password the hub makes and shows (case C above). The setup Wi-Fi keeps its 8 digits: it only leads to the setup page.
4. **Switching:** the Pi has one Wi-Fi radio, and running its own network while joining another is unreliable. So the hub scans first, keeps the choice, turns its own network off and joins. If that fails within about a minute, the setup Wi-Fi comes back with what went wrong ("wrong password", "network not found"). Before switching, the page tells the admin to put the phone back on the facility's Wi-Fi, and shows the hub's address there.
5. **Afterwards:** if the joined network is gone for 15 minutes, the setup Wi-Fi returns, so a hub is never out of reach because a router changed its password. The setup Wi-Fi runs only while the hub has no network.
6. **Security:** besides the per-kit password, the setup page needs the setup code before set-up (also to join a Wi-Fi or make the facility's) and a staff login after it, as on any network.
7. **Built in `internal/netsetup`**, through NetworkManager's `nmcli`. The connections are `hub-setup`, `hub-wifi` and `hub-facility`:
   - The setup Wi-Fi starts 60 seconds after boot without a network, and 15 minutes after the network is lost.
   - A cable or USB modem stops the setup Wi-Fi, never the facility's Wi-Fi.
   - While the setup Wi-Fi is on, the hub tries a Wi-Fi it joined before every 15 minutes, so a router that restarted is rejoined without anyone coming back. NetworkManager does not retry while the radio is an access point.
   - The captive answer is a dnsmasq line (`address=/#/10.42.0.1`) that the hub writes only while the setup Wi-Fi is on. The hub's plain-HTTP page sends phones to `http://10.42.0.1/`. Phones open it in their small sign-in window, which cannot download the hub's certificate: the page then says to open `http://10.42.0.1` in Safari or Chrome. The hub renews its certificate within 30 seconds of a new address (the setup Wi-Fi's, or a network it joined), so `https://10.42.0.1` is covered.
   - The setup and admin pages show the network section. After set-up, changing the network needs a staff login.
   - `hub.txt` carries the facility Wi-Fi's password, since the hub chose it (`facility-wifi`, sealed in the data folder, made once and again only by "New password"). A password an admin chose before 2026-10-27 is left out.

The hub's IP changes when it moves from its own network to the facility's; devices use `dhis2.local`, so that does not matter ("Reaching the hub securely" below).

### USB modems: one kind only (built 2026-10-19, untested on hardware)

USB 4G sticks come in three kinds:

1. **Router in a stick** (often sold as "HiLink" or Ethernet mode): the stick shows up as a network card and hands out an address by DHCP. The hub needs no settings; it is one more wired uplink. The SIM PIN and the operator's APN live in the stick's own web page, and most sticks find the APN themselves.
2. **Modems** (QMI, MBIM or serial): they need modem software on the hub, the APN, the PIN, and sometimes a mode switch, all varying by model and operator.
3. **Sticks that first show up as a CD drive**, and must be switched before they work.

The hub supports the first kind only. Generic support for the others is an endless list of models and operators that nobody on site could debug. In practice:

- One tested model of the first kind per country, in the kit, prepared at the depot: SIM PIN off, APN checked.
- The setup page and the admin page show whether the uplink works, nothing to configure: a stick of this kind shows as "USB modem" (drivers `cdc_ether`, `rndis_host`, `cdc_ncm`; phone tethering too).
- Where that does not work out, a MiFi: the hub joins it over the setup Wi-Fi like any network, and it is often the simpler kit anyway.

### Case D: managed networks (hospital IT, NGO-managed sites)

- Static IP or reserved DHCP, a firewall exception for outbound HTTPS to central and to GitHub (releases), and inbound HTTPS from client subnets.
- Proxy servers: the hub needs outbound HTTPS to central's URL and GitHub (releases). The admin page has a proxy field (built 2026-10-19), used for every outbound connection; empty uses the standard `HTTPS_PROXY` environment variable.

### What the hub needs from any network

| Direction | What | Why |
|---|---|---|
| Hub → central | HTTPS (443) to the central DHIS2 URL | Sync, login, metadata |
| Hub → GitHub releases | HTTPS (443) to github.com and its download host | Self-upgrade |
| Hub → NTP | UDP 123 to the country's or a public NTP pool | Correct time |
| Clients → hub | HTTPS (443 or 8443) on the LAN | Capture apps |

All connections to central are outgoing. Nothing on the internet needs to reach the hub, so it works behind NAT and carrier-grade NAT on 4G. A captive portal or hotel login in front of the internet is taken for central unreachable: the ping must answer "pong", and an `/api/` answer that is an HTML page where JSON was asked for, or a redirect to another host, takes the hub offline at once. Staff keep working offline until someone logs the uplink in.

### Reaching the hub securely (decided 2026-10-19)

The hardest part of deployment. Two requirements:

1. **Security.** Every connection to the hub is encrypted and authenticated, on the facility LAN too. No plain HTTP, and no certificate warnings that staff learn to click through: a warning clicked through stops a listener on the Wi-Fi, not someone actively impersonating the hub (a fake access point, a spoofing device), and it teaches staff to ignore warnings everywhere. Browsers also refuse service workers, which offline apps need, on a certificate they do not trust.
2. **Simplicity for end users.** One address everywhere, which does not change when the hub's IP does, and no router configuration: small facilities have nobody who can set a DHCP reservation or a DNS server.

**Decision: `https://dhis2.local`, with the hub's own certificate authority installed once on each device (option C below).** Browsers are the clients: the Capture web app and the other DHIS2 web apps, installed as apps from the hub's address where wanted. The Android Capture app is on hold for deployment (it does not trust certificate authorities installed by the user, see option B); tests keep covering it where they can. The cost is the installation on each device, about a minute with a guide, and hardest on personal phones; a custom app that would avoid it is not on the table now.

#### How it works

Built 2026-10-19 (`internal/hubtls`, `internal/ui/start.go`, the stick's `tls.rec`), except where marked. Tested in Chromium (trusted: moves to HTTPS; not trusted: stays and shows the steps); a run on the tablet is still to do.

- **The name.** The hub announces `dhis2.local` on the LAN by mDNS, whatever its IP, so nothing needs a reservation: the image names the device `dhis2`, and `avahi` announces `<host name>.local`. Tested on Android 14 (system resolver and Chrome) and iOS (FINDINGS.md, "Devices trusting the hub's certificate"). A second device called `dhis2` on the same network makes avahi rename one of them `dhis2-2.local`, which the hub's certificate does not cover: use the IP there.
- **Where `.local` fails, the IP address.** Expected on some networks; how often is for the pilot to measure:
  - access points that filter multicast (common on mesh systems and some business access points);
  - wired and Wi-Fi on separate subnets or VLANs (mostly case D);
  - older Android versions (Android 14 is tested; versions before about 12 may not resolve `.local`, to check);
  - client isolation, which breaks the IP too and has to be fixed anyway (case A).

  Then devices use `https://<the hub's IP>`, with the same certificate: the hub's certificate lists its current addresses. The IP is on the hub's admin page, in `hub.txt` on the USB stick, and in its heartbeat (`addresses`), so the Facility Hub app's hub page shows it to a district officer on the phone. Two costs: the IP can change, so such a facility needs a DHCP reservation on its router after all; and a browser keeps a site's offline data and installed app per address, so a device stays with one address, name or IP, and does not switch between them. A case C hub is its own network, so its address never changes.
- **The certificate.** The hub's certificate authority is limited to web servers (no code signing, mail or client certificates) and by name constraints to the hub's names (`dhis2.local`, `localhost`, its host name, the names in `hub_hosts`) and private addresses, so installing it cannot expose any public site. It lasts ten years, so a device installs it once. The hub keeps the authority's key and issues its server certificate again when fewer than 60 days are left and when its addresses change (checked every few minutes, without a restart). Server certificates last 397 days, inside Apple's 825-day limit for any TLS certificate. The authority's key sits next to the server's key, readable only by the hub's service user, not in the encrypted store: the hub needs to issue certificates without anyone logging in. Its files are written synced, so a power cut leaves the old file or the new one; a key that cannot be read stops the hub rather than being replaced, and a corrupt one is kept aside (`ca.key.bad`) and replaced. **What a stolen key allows** (from the disk; on the stick it is sealed): toward the devices that installed this hub's authority, impersonating `dhis2.local` (at any facility those devices visit, since every hub uses that name), the hub's other names, and HTTPS on any private address, such as a router's admin page. Not public sites, not software or mail. Accepted, as for a stolen server key; the hub is retired and its devices remove its authority and install the new hub's (section 3, "Replacing or moving a hub"). Narrowing the addresses to the hub's own subnet was considered: the subnet changes with the router, and the authority cannot be changed without every device installing again. A hub upgraded from a version that threw the key away makes a new authority once, and its devices install it again.
- **Backup.** The authority, its key and the server's certificate are on the USB stick, sealed with the data key like the hub's state (RECOVERY.md, C2). A disk restored from the stick keeps them, so devices notice nothing. A new kit without the old stick has a new authority, and every device installs it (about 10 to 30 per facility).
- **Getting the authority.** From the hub itself, at `http://dhis2.local/hub/ca.crt` (decided 2026-10-19). Someone on the facility Wi-Fi at the moment a device installs could hand it their own file instead and then impersonate the hub to that device: a one-time window per device, for this hub's names only, accepted. A cautious installer compares the fingerprint the start page shows with the one in the Facility Hub app on central (the hub's page there), which comes from the hub's heartbeat (`caFingerprint`) over central's HTTPS; the hub's admin page shows it too. Getting the file from central itself was considered: staff would have to open a page on central by themselves (a link on the hub's page proves nothing), which needs an app or page every staff member can open; it stays an option if a country's risk assessment asks for it.
- **The start page.** The hub also listens on plain HTTP (port 80), and answers every request there with a start page, except `/hub/ca.crt`. The page tries `https://` at the same name and path in the background: if the device trusts the hub, the page moves there at once, so people only ever type `dhis2.local`; if not, it shows the steps for that device (Android, iPhone and iPad, Windows, macOS), the file and its fingerprint. HTTPS answers carry HSTS for seven days, so a device used weekly goes straight to HTTPS and a fake plain-HTTP page cannot intercept it. Seven days, not a year: after a hub is replaced without its stick, devices that remember HSTS cannot open `dhis2.local` until it lapses, and meanwhile reach the start page at `http://<the hub's IP>` (HSTS never applies to IP addresses). **(To check: whether browsers keep HSTS for `.local` names.)**
- **Ports.** `https://dhis2.local` and `http://dhis2.local` need ports 443 and 80. The Pi's service runs as its own user with permission to bind them (`CAP_NET_BIND_SERVICE` in `deploy/hub.service`); `listen: ":443"` and `http_listen: ":80"` in the image's `hub.yaml`.
- **Installing, per platform** (measured on a Samsung tablet with Android 14 and on an iPhone):
  - *Android:* download the file, then Settings › Security and privacy › More security settings › Install from device storage › CA certificate; Android warns "Your privacy is at risk", then "Install anyway", the screen lock, and the file. Android requires a screen lock (PIN, pattern or password) for any installed certificate. Opening the file in Chrome only says it must be installed in Settings.
  - *iPhone and iPad:* open the file in Safari and allow the profile; Settings › General › VPN & Device Management › install; then Settings › General › About › Certificate Trust Settings › switch on full trust. Without the last step Safari keeps warning.
  - *Windows:* double-click the file, "Install Certificate", and pick "Trusted Root Certification Authorities" by hand. *macOS:* Keychain Access and "Always Trust". Firefox may need its own import; managed fleets push it with Group Policy, Intune or an MDM, which also covers managed tablets.
- **On the hub PC itself** (Windows option), `https://localhost` needs no installation.

#### Considered and not chosen

**Option A: a public name and a Let's Encrypt certificate per hub.** Each hub gets a random public name (for example `h-4f7k2q.hubs.hmis.example.org`) pointing to its LAN address, and a publicly trusted certificate through the DNS-01 challenge, run by a name service the country or HISP would operate. Every device would trust it with nothing installed. Not chosen because **offline, the name does not resolve**: devices ask the router, the router asks the internet. It works only where the hub is the facility's DNS server, and making the hub the staff Wi-Fi everywhere, or configuring routers, is not doable. Also against it: a new service to run, rate limits (about 50 new certificates per domain per week), certificate lifetimes heading to 47 days so that a hub offline longer loses its certificate, routers that drop public names with private addresses (DNS rebinding protection), and no `dhis2.local`: Let's Encrypt only issues for public names, and one name and certificate shared by all hubs would put the same key on every hub.

**Option B: the Capture app pins the hub's key** (on hold, with the Android Capture app). The hub keeps its own key; the app stores its fingerprint from a QR code or from central and accepts that key and no other, by IP, offline, with nothing installed. Needs a trust check per server in the DHIS2 Android SDK's HTTP client (OkHttp's own pinning still requires a publicly trusted chain), which is the Android team's work and security review. Kept in mind for when Android Capture comes back.

#### Ruled out

- **Plain HTTP**, even on the facility LAN. Anyone on the Wi-Fi reads passwords and patient data. Test set-ups only.
- **Certificate warnings clicked through** (requirement 1).
- **A USB stick to carry the authority to devices.**

## 5. Pointing client devices at the hub

### New devices

- **Every device, once:** install the hub's certificate authority, guided by `http://dhis2.local` (section 4, "Reaching the hub securely"). Then open `https://dhis2.local` and log in; install the Capture web app from there as an app where wanted.
- **Android Capture app:** on hold for deployment (it does not trust the hub's authority).
- **With an MDM** (Android Enterprise, for example Intune, Headwind or Google's), push the hub URL with the device. The Capture app's managed-configuration support needs checking before relying on this **(to check)**.
- **Laptops and desktops:** a browser bookmark or a desktop shortcut to the hub's address. Installing the Capture web app as a PWA from the hub's address gives an app-like icon.

### Existing devices already pointing at central

A device keeps one server URL per account, so moving it to the hub is a one-time step per device:

1. **Before switching, sync everything to central** while online. Unsynced data on a device must not be stranded under the old server. Capture keeps one database per account and does not warn about unsynced data when staff log out or add an account; the hub's account then looks complete without those records, and staff may register the people again (adversarial I3c, 28 October). Logging in to the old account again where central is reachable and syncing recovers them.
2. **Android:** log out and log in again with the hub's address. The Capture app supports multiple accounts, so the central account can stay for fallback, but staff should use only one. Users must not end up entering data in both.
3. **Browsers:** replace the central bookmark with the hub's.
4. **Training point:** "At this facility, always open DHIS2 through the hub address." Using central directly still works while online, but that data does not appear on the hub until the hub's next pull, so other users will not see it if the internet then drops.

### Devices that move between facilities

A hub only serves its own facility. A community health worker's tablet that visits several facilities should keep central as its server. This is a limitation of hub-as-server, which the long-term client fallback in section 7 removes.

### Ongoing

- Android syncs with the hub every 30 minutes or by hand (spec 5.1). For quick hand-over between a tablet and another user, staff press "Sync" after finishing a patient. This needs training.
- Web users see each other's work immediately, because every web request goes to the hub.

### Images and files

The hub keeps no images or files (IMAGE and FILE_RESOURCE values; FINDINGS.md, "Images and files"). Online they work through the hub as on central, up to central's size limit (`max.file_upload_size`, 10 MB by default). Offline:

- **Capture web** cannot add an image or file. It shows "Async field update failed" and saves the form without it. No image shows offline, not even one seen online.
- **The Android app** keeps an image or file added offline on the tablet. The record it belongs to syncs once the hub is online again, and until then other devices do not see that record.
- **Training point:** "Photos and documents need the internet. Add them when the hub is online, or add them later to the record."

Where a programme relies on photos or documents (for example a scanned birth certificate), tell the country before rollout.

## 6. Running the fleet

### Monitoring

- Every hub writes a heartbeat to `facility-hub-status/<hub_id>` on central once an hour, at the end of a sync cycle, and at its first cycle after a start. It holds:
  - the latest status: version, uptime, last push and pull, pending count, issue count, free disk and last upgrade result;
  - a history (built), one summary per day for the last 90 days:
    - sync cycles;
    - outages and minutes offline, and the longest outage;
    - the peak of pending changes;
    - the lowest free disk;
    - saves passed straight to central while online (`savedOnline`), and saves stored on the hub and sent later from the outbox (`savedOffline`): tracker imports and aggregate edits, one per request (built 2026-10-28, `TestSavesCountedInHeartbeat`, `TestAggregateSavesCounted`).

  The history shows how much time a hub spends online and offline, and how much staff work through it offline. It lives in the same key, about 14 KB at 90 days, so the namespace still holds one key per hub. The two save counts add at most 3.4 KB at 90 days (four-digit counts every day), less on days without saves, which leave them out.
- **The Facility Hub app** (first version built 2026-10-18, `apps/facility-hub`, [FACILITY-HUB-APP.md](FACILITY-HUB-APP.md)), a DHIS2 app the national team installs on central in App Management. It lists hubs from `facility-hub-registry` with their latest heartbeat, approves hubs' own accounts, gives out move codes, chooses and stages the hub version (2026-10-19), holds the country's other hub settings (retention, token lifetime) and resends refused saves (`facility-hub-admin/retries`). It flags:
  - no heartbeat for more than 2 days;
  - pending data older than 1 day;
  - rejected objects (issues);
  - low disk;
  - failed upgrades;
  - "hub needs upgrade" (central version not supported);
  - a hub token that no longer works (section "The hub's own account").
  - dataStore entries the hub acts on that any user may write (`unprotected`, section "Protect the data store namespaces").
  - instructions on central the hub ignored for want of a valid code (`unsigned`, same section).
- **Refused changes** land in `facility-hub-issues`, one key per change, with central's error codes and its messages masked: every value the hub sent is replaced by `***`, so central staff see which data element or attribute caused it and nothing that was entered (spec 11.2). They exist only on the hub until resolved: the client was told they were saved. Districts follow up single ones. Once resolved (sent again and accepted, or replaced by a newer version on central), the hub deletes the key, so the namespace holds what is stuck now, not every refusal ever. Keys of hubs that were reset or replaced stay; removing them after a grace period is for the Facility Hub app **(to build)**.
  - Answers from the way to central (a redirect, 408, 429, 5xx, an answer cut off) are no refusal: the save waits for the next cycle, without limit. An answer to a tracker save that is no import report (a proxy's 413 page, a firewall's 403 page) also keeps it waiting, but after a day of such answers the hub refuses the entry with that answer, so it does not hold up the saves after it.
  - A change central refuses because it has deleted the record (E1082, E1113, E1114, E4017, "already deleted") closes on the hub after the push, with what it blocked. DHIS2 cannot undo a deletion, so nobody could fix it. The hub keeps a tombstone, so Android's next offline download drops the record, and keeps the outbox entry for 30 days (built 2026-11-04, `TestMootRefusalClosesItself`). The hub log has a line for each.
  - The saves of a user central stopped accepting (disabled, deleted, token expired) wait for that user's online login; after 7 days the hub refuses them itself, under the code `NO_LOGIN` (RECOVERY.md, "What already helps").
- **Refused in bulk** (for example hundreds of events after a metadata change):
  - the heartbeat carries counts by kind, programme and error code (`refused`), so the app shows the pattern, not hundreds of rows (built);
  - the national team fixes central (restores the option, relaxes the rule), then presses "Resend" on that group (kind, programme, error code) on the Facility Hub app's Refusals page. Each hub sends its refused saves of that group, and what they blocked, again as their original authors, once, at its next sync (built 2026-10-20: `facility-hub-admin/retries`, `TestRetryResendsRefusedGroup`, `TestAggregateRetryAfterFix`). Central's new answer stands: a save refused again is a fresh issue. On a `NO_LOGIN` group, Resend can name another user of the hub to send the saves as (`user` in the retry); a hub that does not know that user, or cannot send as them until they log in online there, applies nothing and says why in its heartbeat (`retriesNotApplied`);
  - when central was changed on purpose and the waiting saves do not fit (a new rule, a closed period, a duplicate), a hub manager presses "Discard" on that group, or on one change on the hub's page (built 2026-11-04: `action: "discard"` in the retry, `issue` for one change; `TestDiscardGroup`, `TestDiscardRefusedRegistration`). Each hub, once, throws its refused saves of that group away, with what they blocked. For each record it keeps central's version, read as the author, or a tombstone of a record central never had, so Android's next offline download drops it. A record central had but does not show to the author stays refused (central's 404 does not tell a deletion from no access). The values entered are not sent. The hub keeps the discarded outbox entry for 30 days, then prunes it. A hub that holds no token for the author keeps those saves refused and says why in its heartbeat. Ideas considered and dropped for repairing such saves: exporting them encrypted to a national key (a golden key), per-export codes sent by SMS (a person on site relays them), a remote repair session (needs remote access).
- **Not available offline:** each "needs connection" a hub answers to a read while offline (mostly a screen nobody had opened online) is counted per endpoint; the heartbeat carries the 10 most frequent since the hub started and their total (`offlineMisses`, `offlineMissesTotal`; endpoints with `{uid}`, parameter names without values), and the hub's page in the Facility Hub app lists them (built 2026-10-22, `TestOfflineMissesInHeartbeat`). It measured how often staff met a screen they never opened online, which decided building the load of each user's screens at their online login (2026-10-29, FINDINGS, "Shared metadata cache"). Since then, a miss is a screen nobody at the facility opened online in the last 14 days, or a staff member who went offline within a minute or two of logging in. The hub's log says what it loaded for whom (`replay after login user=… loaded=… kB=… s=…`). The hub's own `/hub/ui/endpoints` lists every miss.
- **Before tightening metadata** (a new compulsory field, a removed option, a closed stage), check what hubs and tablets have captured offline under the old rules: those changes will be refused.
- **dataStore volume.** Measured on 2.42 (docs/FINDINGS.md): single-key reads and writes stay at 0.1 s up to a million entries in a namespace. Browsing slows as a namespace grows, because filters have no index on the values and deep pages read all rows before them: 0.3–0.6 s for a filter that matches little, 1.7 s for a deep page at a million. Only the issues namespace could grow without bound, hence the clean-up above.
- **Heartbeat volume:** one key per hub, overwritten hourly, with its 90-day history inside, so the namespace does not grow. A heartbeat is about 14 KB with that history (20 KB with 100 refusal groups; the offline misses add under 1 KB): sent every cycle it was 1.3 MB a day per hub, much of it over 4G, and 650 MB a day into central for 500 hubs; hourly it is a quarter of that (measured 2026-10-21, docs/reviews/KISS-2026-10-20.md). dataStore writes are not audited (measured on 2.42 and 2.43: 5 500 heartbeat writes from 500 hubs left every audit table unchanged). 500 hubs need 0.14 writes per second; reading all their heartbeats takes about 0.1 s (docs/FINDINGS.md). No archiving is needed.

### Answering staff from the hub while online (built 2026-11-06)

Where the uplink is slow or unreliable, the national team can have hubs answer staff from their own copy even while central is reachable: Facility Hub app, Settings, "Answer from the hub while online", a list of programmes. Hubs read it with their other settings, within an hour. Off by default: with no programme listed, everything passes to central while online, as before. Measurements and the reasoning: docs/PERFORMANCE.md ("A slow uplink"), docs/CONNECTIVITY.md.

With programmes listed, a hub answers from its copy:

- **Patient data of the listed programmes:** working lists, searches within the facility, a person, their enrollments, events and relationships, once the hub has finished a full pull of the programme for that user. Searches across the district stay with central, unless a user's search scope is their facility alone. Whatever the hub cannot answer goes on to central.
- **Metadata and settings** the user's apps asked for before (forms, rules, option sets, the user's own settings), from the copies the hub keeps for offline use.

Saves, reserved IDs and validation always go to central while online.

**How staff get changes.** A change made elsewhere (another facility, a district officer) reaches staff at the hub's next sync, 15 minutes by default. For metadata, a hub answers from a copy only if it is newer than the last change it knows of, and learns of changes:

- hourly, asking central what metadata changed, as one member of each group of staff with the same access;
- when the national team presses "Refresh metadata on all hubs" (Settings): every online hub within 15 minutes, an offline hub when it reconnects;
- when someone at the facility presses "Refresh metadata now" on the hub page;
- when a setting or data store entry is changed through the hub;
- for one user, when their access changes.

The hourly check does not see deletions (central lists them only to superusers), changes to which org units a programme or data set is assigned to (they change no `lastUpdated` on central), or changes to settings and app configuration; those reach staff at their next online login within a day, or at once with either refresh button. **After deleting metadata, reassigning forms, or changing settings that staff should see at once, press "Refresh metadata on all hubs".** Offline nothing changes: the hub serves the copies it has.

### Upgrades

- **Hub software** (built 2026-10-19): the supplier publishes signed releases on GitHub (`dhis2-utils/poc-data-proxy`, tag `v<version>`; the source and the public key are built into the binary). Each country chooses which version its hubs run in the Facility Hub app's "Hub version" page, which writes `facility-hub-admin/release`, and stages it:
  1. 5 pilot hubs, listed by hub ID;
  2. 10 %;
  3. 50 %;
  4. 100 %.

  Each hub checks after its sync cycle and downloads the chosen version from GitHub in the background, so a slow link never holds up syncing. Once the download matches the signed release, the next cycle pushes its outbox, migrates a copy of its store, swaps, health-checks and rolls back on failure. The health check tests the hub itself (its store and its status page), not central: a slow or unreachable central does not roll a good release back. Until the check has passed, the new release takes no saves of its own and pushes nothing. Clients get "the hub is upgrading" and retry, and a rollback has nothing to lose. An upgrade cut short (a power cut, a crash) is finished or undone at the next start, before the store is opened: the hub runs the old release on its old store, or the new release on the migrated one. It never moves to an older version. A release that keeps exiting before its health check is rolled back at its fourth start: each start counts itself in the upgrade marker before anything else, so no outside watchdog is needed, on the Pi or on Windows. A version that failed its health check, its migration or its starts is not tried again. A version whose upgrade was cut short, or whose migration ran out of time, is tried once more, and skipped after a second time. Results show up in the heartbeat, and the page counts them per stage.
- **Upgrade the Facility Hub app before the hubs** (the first hub release after 0.1.5 checks the codes). A hub on such a release ignores instructions the app has not signed for it. After upgrading the app, save once what is in force: the release choice (Hub version), each setting (Settings), the accounts settings (a superuser, Approvals) and any planned move. Until then approved hubs keep their last settings, follow no planned move, apply no resend or discard and take no new release.
- **Migrations on full hubs.** SQLite keeps sorts in memory, inside a 4 GB limit. A migration that built an index over existing rows ran out of it at 4.6 million objects, the busy HC IV kept for five years. A hub that cannot migrate rolls back and stays on its old release; once central moves to a DHIS2 version only the new release supports, it serves as pass-through only, and with hundreds of hubs a rollout stage could stall.
  - Fixed (2026-10-17): migrations sort through temporary files on the hub's disk, encrypted like the store, so their size is bounded by free disk, not memory. The same rebuild then took 26 s at 4.6 million objects (docs/PERFORMANCE.md, "Upgrading a large hub").
  - Still: a migration that touches existing rows is timed at that size before a release (AGENTS.md), and the first stage of a rollout includes some of the largest hubs, found by the heartbeat's disk use.
- **Stores from before the first release.** Until a hub is deployed, the store's base schema is edited in place instead of migrated; the 30 October tie-order change did this to the list indexes. A store created by an earlier build keeps the old indexes and is never rebuilt: it pays a sort per page and the plan tests' assumptions no longer hold. A test hub still holding such a store is reset, not upgraded; its data returns from central at the next pulls.
- **Before a DHIS2 server upgrade:** run the live suite against the new version (docs/TESTING.md); if the hub needs changes, roll out that hub release first. A hub release only supports the DHIS2 versions it was tested against (spec 12a): new majors come about a year apart, with API changes nobody can plan for in advance. Hubs on an unsupported version stay usable online, as pass-through only, and show "hub needs upgrade". Their queued saves stay in the store: the upgrade to a release that supports central carries them over unpushed, and that release pushes them.
- **Operating system:** security updates install themselves (`unattended-upgrades`, with automatic reboot at night). Re-image only for major OS changes. That can be done by shipping a pre-flashed SSD, because the hub holds nothing that central does not.

### Support

- **From central.** The district and the national team read the Facility Hub app: last heartbeat, pending and refused changes, disk, token. The hub's own status page only opens on the facility network, so it is not the first line.
- **On site, by the staff:** restart the hub, log in to unlock it after a restart, plug in a replacement.
- **No remote access** to hubs for now (section 2).
- Most faults are fixed by moving the disk to a new board or sending a spare (section 3). Keep a spare per district.
- Faults in the hub software go from the national team to the supplier.

### Central's time zone

Set central's server time zone to the facilities' own. In a country with several zones, use the easternmost, so that central's date is never behind a facility's. Central refuses an enrollment or incident date "in the future" (E1020, E1021) by its own date, while staff date records by their devices'. With central in a zone to the west, every registration dated today is refused between local midnight and central's: in the Pi 5 soak, with central on UTC and the clients on UTC+1, 398 saves in one hour each night (FINDINGS.md, "Open", central's time zone). Online the staff member sees central's refusal at once. Offline, or while the hub still has a backlog to push, the hub refuses the same dates itself, by its own clock in central's time zone, with central's code and text. So the staff member sees the refusal at the save in both cases. A hub whose clock is behind the newest time central has shown it (a Pi 4 without an RTC after a power cut with no network) leaves these dates to central, which judges them at the push, and its page says its clock is behind. A hub whose clock is ahead lets such a date through, and central refuses it at push. The required RTC keeps this rare (NO-RTC.md).

A device whose clock or time zone is ahead of central's dates its records in central's future. The hub's page lists each user whose device clock ran more than half an hour ahead of central's at their latest upload, and the heartbeat counts them (`clientClocksAhead`). Set the date, time and time zone of those devices to central's. The hub reads the device time that Android Capture sends with each record (`createdAtClient`, `updatedAtClient`). Capture web sends no device time, so the hub cannot check a browser's clock. A clock behind is not listed: it looks the same as records saved earlier and sent late. The list is kept in memory, so it is empty after a restart until each device sends again.

Allowing future enrollment and incident dates in a program also avoids E1020, but it changes what the program allows. It is a workaround, not the fix.

### Security operations

- **Protect the data store namespaces** on central (`facility-hub-*`) before the first hub goes out. By default any user can write them (FINDINGS.md): a forged registry entry can retire a hub (it then stops serving offline, though it keeps its data) or block setup of a facility.
  - **The "hub owners" group.** The hub writes these namespaces with the token of a staff member: whoever set it up, or took it over. So share write access on them with one user group, the hub owners, and nobody else. The national team or the districts add the facility focal points (and whoever else may run a hub) to it. Membership is then the list of the people who own hubs.
  - Set-up and take-over check it: someone outside the group is refused with "ask the national team or the district to add you to the hub owners group". The hub is left as it was, with no half-done set-up and no lost token.
  - Weaker than "admins only": any group member could write any hub's entries. A dedicated hub account per hub would restore "admins only" (below).
  - **Signed instructions** (built 2026-11-11, review R-07; [the design](superpowers/specs/2026-11-11-signed-instructions.md)). Sharing cannot tell the app's writes from those of a user who created a missing entry first and shared it read-only for everyone else, which passes a check of public access (measured on 2.42). So the Facility Hub app adds to each instruction it writes a code per approved hub, made with that hub's move key, and an approved hub acts on `central-move`, `retries`, `release`, `accounts`, the `facility-hub-config` settings and the revocation in its registry entry only with a valid code for itself, no older than the last one it took of that kind. It ignores the rest, and names them in its heartbeat (`unsigned`), on its admin page and as a flag in the app (`TestHeartbeatReportsUnsignedEntries`, `TestSignedInstructions`). A hub not yet approved has no key: it ignores `central-move` and `retries`, and reads `release`, `accounts` and the settings on their sharing, as before. Retention and pool sizes from `facility-hub-config` are kept within the app's limits.
  - Each hub still checks, with its heartbeat, the sharing of the entries it acts on (`facility-hub-admin/accounts`, `central-move`, `release`, `retries`, and its own registry entry). Those any user may write are listed in the heartbeat (`unprotected`), on the hub's admin page and as a flag in the Facility Hub app (`TestHeartbeatReportsUnprotectedEntries`): "retired" in the registry, and what a hub reads before its approval, still rest on sharing. A hub shares its own registry entry, which it creates and owns, as readable by all and writable by the hub owners, hub accounts and hub managers groups named in the app's Settings, at set-up (or at its next heartbeat, should the entry be open again). Set-up and take-over are refused until a hub owners group is named (`TestHubProtectsItsRegistryEntry`).
- **The hub's own account.** For its housekeeping (heartbeat, issue reports, registry, programme list, retention settings, reserving generated IDs, upgrade checks) the hub uses a personal access token of whoever set it up, renewed every `pat_expiry_days` with itself. If that person is disabled on central, the hub goes silent, and offline registrations that need generated IDs fail once its pool runs out; if they move elsewhere, the hub keeps their credentials unnoticed. Patient data is not affected: it goes with each user's own token.
  - *Interim* (built): any member of the hub owners group assigned to the facility can take the hub over on its admin page ("Use my account", with their password): the hub swaps to a token of theirs and deletes the old one. On a hub with its own account, a take-over is refused while central still accepts that account ("This hub has its own account on central, which still works. A hub manager must revoke it first"). Once a hub manager has revoked it (Revoke disables the hub's user), a take-over sets the account aside (the hub forgets its password; it keeps its move key) and the hub asks for an account again, which the national team approves as below; the page says so (`TestTakeOverSetsTheOwnAccountAside`). The heartbeat reports whose token the hub uses and when it expires (`hubTokenOwner`, `hubTokenExpires`), so the Facility Hub app can list the hubs that depend on a user being disabled; a hub whose token no longer works stops sending heartbeats, which the app flags after 2 days.
  - *The hub's own account* (built 2026-10-17, `internal/hubaccount`; approved in the Facility Hub app, see [FACILITY-HUB-APP.md](FACILITY-HUB-APP.md)): an account per hub, so its housekeeping depends on no staff member.
    1. At set-up the hub asks for one in its registry entry (`accountRequested`), with a public key only it can decrypt with (`boxKey`). Its admin page shows a 16-character *account code* made from that key, its ID and its facility.
    2. A hub manager (a member of the hub managers group, not necessarily a superuser; [FACILITY-HUB-APP.md](FACILITY-HUB-APP.md), "Hub managers") approves waiting hubs in the Facility Hub app, for hubs in their own org units. Anyone in the hub owners group can write the registry, so an entry proves nothing by itself: the approver calls the facility on a number they already have (never a code from someone who calls them), asks for the code on the hub's page and enters it; the app refuses a code that does not match the key and facility in the entry. No other key or facility can be made to match the code on the hub's screen. For each hub approved, the app:
       - creates the user `<hub ID>`, assigned to the hub's facility only (capture, data view and search), so anything public on central is still limited to that facility, or gives the existing user a new password (a hub approved again);
       - gives it a role with no authorities and puts it in the hub accounts group;
       - makes a new *move key* and keeps it in `facility-hub-keys/<hub ID>`, created afresh (so its owner is the approver) and shared with the hub managers group only: not the hub's account, not other staff. Every hub manager can therefore make move codes for every hub (accepted 2026-10-19; FACILITY-HUB-APP.md);
       - hands the credentials and the move key over in the registry entry, encrypted to the hub's key.
    3. At its next catalog refresh the hub checks the account: named for its own ID, assigned to its facility only for capture, data view and search, with no authorities. It creates its own token and proves that token can write its records, then saves it all and deletes the staff member's token. At once it gives the account a new password and a new token, deletes the account's other tokens, and removes the credentials from the registry: whoever saw them on the way is shut out. The new password is saved before central is asked, so an answer lost on the way loses nothing. Measured on 2.42: the account changes its own password with its token (`PUT /api/me/changePassword`), and lists and deletes its own tokens.
    - A hub on its own account shows no code, so an entry someone else marks as asking again cannot be approved. The hub asks again itself whenever its entry holds another key, no request and no account, or an account it cannot open or refuses.
    - It keeps the account's password and the move key, sealed, to make a token again if its token lapsed while it was offline, for typed-in moves, and to check the codes on central's instructions. The USB stick's copy of the hub's state holds them too, sealed with the data key like everything on the stick. "Revoke all tokens of this hub" in the app also disables the user `<hub ID>` (or gives it a new random password when disabling is refused), so the password the hub holds opens nothing; its tokens go with the revocation, since on 2.42 they outlive a disabled user. A hub manager may do either within their org units (checked on 2.42.7.1 and 2.43.2.1, 11 November 2026). Approving the hub again needs the user enabled first, in the Users app.
    - No golden key, no extra service: the person approving acts in the open, and nothing secret is in the image.
    - *Once, before the first hub is set up* (section 3, "On central, before the first hub"), a superuser creates the hub owners group, the hub accounts group (write sharing on the `facility-hub-*` namespaces except `facility-hub-admin`, read on programme metadata) and its role, and the hub managers group and its role (FACILITY-HUB-APP.md, "Hub managers"), and names them in the app's Settings. No hub owner need be in the hub accounts group.
- **PAT hygiene:**
  - PATs live as long as the country's setting in the Facility Hub app (7–365 days; else `pat_expiry_days`, default 90) and renew while the hub is online.
  - Users who leave are disabled on central, and the hub refuses them within minutes. Their PATs, though, keep working against central on 2.42 (FINDINGS.md, "Tokens outlive their user"): revoke a lost hub's tokens in the app.
  - Hubs show users central refuses as "needs online login".
- **Physical access:** data at rest is encrypted, and the key is only in memory after a staff login. A stolen hub reveals no patient data without a staff password, but a powered-on hub that is moved keeps its key until it loses power.
- **Passwords protect the disk.** Each key slot can be attacked offline by whoever holds the SSD (Argon2id slows this down; it does not stop weak passwords). Enforce central's password policy for hub users. The hub removes slots of users who have not logged in for `key_slot_max_age`.
- **TPM (recommended for a national rollout).** A TPM add-on board (about USD 25 for the Pi; the image enables the Infineon SLB 9670 on SPI) holds the hub's device key, mixed into every key slot on the disk and sealing the TLS keys, so a disk taken on its own is useless (built 2026-10-20, `internal/devkey`; see section 2, "What is not encrypted", for its limits). Used wherever present; a hub without one works as before. Not yet tried on a TPM board or a Windows PC.
- **What the encryption does not cover.** AES-XTS keeps the data file confidential but does not detect tampering outside the sealed rows; someone with write access to the disk can corrupt it. And once a staff member logs in, the whole facility's data is decrypted in the hub's memory: a running hub is a more valuable target than its disk.
- **Central server address.** Given once at set-up (above). Afterwards it changes only when central moves, and a hub must never be stranded by that, nor follow a server that is not the same DHIS2.
  - A rule in an earlier draft (a hub sends nothing to a new server before it returns a secret from the hub's registry entry) cannot work: reading the dataStore needs a login, so the check itself would send credentials to the unproven server.
  - Instead the current central vouches for the new address. The hub switches when the current address no longer answers as central and the new one holds this hub's registry entry (built, `internal/core/move.go`):
    1. *Announced:* the national team writes `facility-hub-admin/central-move` = `{"url": "https://new.example.org", "from": "2026-11-01T06:00:00Z"}` on the current central before the move, in the Facility Hub app (which creates the entry readable by all and writable by administrators only). Hubs read it with their token at each catalog refresh. `facility-hub-admin` must be writable by administrators only: the hub owners group writes the other namespaces, and must not be able to move every hub. An announcement without a valid code for the hub is not used, and a hub not yet approved uses none: the check of the new address sends the hub's token and compares the hub's ID, which is public, so only the code keeps hubs from a server that echoes it (`TestCentralMoveSendsNothingToAForgedAddress`). `from` is optional; before it, the address is not used.
    2. *Redirect:* the current address, reached over https (verified TLS), answers the hub's probes with a permanent redirect (301 or 308) to another host, three times in a row. A redirect from a plain-http address (a captive portal, say) never counts, and a probe that reaches central clears it.
    3. *Typed in*, for an unplanned move (built): while the current address does not answer, a staff member enters central's new address on the hub's page with a 10-character *move code* the national team gives them. A hub manager has the Facility Hub app make one code per hub for the new address (from each hub's move key, [FACILITY-HUB-APP.md](FACILITY-HUB-APP.md)), as a list to send to facilities. The hub checks the code itself and sends nothing for it: it moves only to the address the code was made for, so a code is no use for any other server. A code stays valid for its address until the hub is approved again. Wrong codes are throttled like logins. Only an approved hub has a move key. The admin page's "Simulate uplink cut" appears only with `uplink_switch: true` in `hub.yaml`, for demonstrations, and does not count as central not answering.
  - For an announced or redirected move, the current address must have stopped answering for an hour, counted from the hub's start if it started during the outage, so a brief outage during a planned move does not move hubs early. A hub whose `hub.yaml` pins `central_url` never moves; one limited to `central_hosts` moves only within them.
  - The new address is kept in the store, so it survives restarts. A hub that misses all of these keeps working offline, with its data, waiting for an address.
- **Signing.** Hub binaries and the release manifest are signed with the supplier's ed25519 release key, which hubs check before upgrading. The Windows installer and `hub.exe` are also Authenticode-signed with a code-signing certificate issued to the supplier's organisation; its key must sit in hardware, so CI signs through a cloud signing service (for example Azure Trusted Signing, about USD 10 a month, or DigiCert KeyLocker) with `signtool`, or `jsign` from Linux. SmartScreen warns until the certificate has built up download reputation.
- **Host names:** if devices reach the hub by a name other than its host name, `dhis2.local` or its IP (for example a router DNS name), list it in `hub_hosts`; the hub refuses unknown `Host` headers. A name for the certificate must name one host: at least three labels (`hub.clinic.example.org`), or two under `.local`, `.lan`, `.internal` or `.home.arpa`; wider names (`example.org`, `co.uk`) would let the authority vouch for a whole domain, and are left out with a warning. Set it before the hub's first boot: the hub's authority permits the names in `hub_hosts` when it is made, so a name added later is not in the certificate (the hub logs a warning) until the hub makes a new authority, which every device installs again.

## 7. Rollout plan

| Phase | Hubs | Goal |
|---|---|---|
| National preparation | none, or one test facility | Central DHIS2 configured (Facility Hub app installed, user accounts checked, namespaces protected, retention set), kits and installer ready, trainers trained. A test hub reaches central and works offline. |
| Pilot | 5–10 facilities in one district, chosen for poor connectivity | Facility staff set up hubs with the installation card alone. Validate network cases A–C, staff training and the offline workflows with real patients. Measure sync volumes and power issues; answer support questions from central. |
| Expansion | 50 facilities across 3–5 districts | Validate depot logistics and delivery, the chosen way of reaching the hub securely, the Facility Hub app and staged upgrades; refused changes stay few. |
| National | Hundreds | District by district: facilities chosen, kits flashed in batches and delivered, staff set up their hubs; the national team watches every hub from central. |

### Per-facility checklist

- [ ] Facility chosen for a hub (section 0)
- [ ] Power socket with UPS; hub mounted, cooled, labelled; USB stick in (setup code, then backup)
- [ ] Network case identified (A/B/C/D); client isolation checked (and DNS rebinding, with option A)
- [ ] Hub set up by a staff member assigned to the facility
- [ ] Every staff member logged in once through the hub (this is when their data downloads), and opened Capture's working lists and the Data Entry app's forms once online
- [ ] Every tablet and browser switched to the hub address (synced first)
- [ ] Offline test: uplink unplugged, a registration on one device seen on another
- [ ] Heartbeat visible in the Facility Hub app on central

### Possible future enhancement: wards or departments as org units

Not planned for now. Some countries put wards or departments below the facility org unit, with staff assigned to the facility or to a ward, and search scope usually the whole facility. The hub does not support this today: a user assigned only to a ward cannot log in through it (it accepts the facility or above), its pulls take only data owned by the facility org unit itself, and set-up offers the lowest-level org units, which would be the wards. Supporting it would take: a facility level set by the country in the Facility Hub app; the hub serves the facility and everything below it; pulls with the facility's descendants, still per user, so central decides what each user sees; login for anyone assigned at or below the facility; data written offline readable by users whose capture or search scope covers its org unit, and writable only to org units in the user's capture scope; searches on the subtree, or the ward an app selects.

### Longer term

The client-side fallback in the shared app-runtime would remove most of section 5. Devices would keep central as their server and use the facility's hub automatically when central is unreachable (spec 5.1 and open question in spec 14). Discovery could use the hub's address and key, published in the registry for the user's facility. With that in place, tablets that move between facilities would also work.
