# Deploying facility hubs at scale

How the hub in this repository could be rolled out to hundreds of facilities on Raspberry Pis (or, as an option under consideration, on an existing Windows PC): the hardware kit, the single country image, what happens at the facility, the network set-ups we will meet, how existing and new client devices are pointed at the hub, and how the fleet is run afterwards.

This is a proposal built on what the POC showed (see [FINDINGS.md](FINDINGS.md)). Items marked **(to build)** do not exist in the POC yet.

## Principles

1. **One image per country, no per-facility configuration.** Every hub is flashed with the same image. A hub learns its facility, programs and users from central when a setup user logs in (spec 6.2). A box can go to any facility, and a spare can replace any hub.
2. **Nothing to install on site.** The installer plugs in power and network and logs in once. No keyboard, screen or command line.
3. **Central stays the source of truth.** A hub can be wiped or replaced at any time without data loss once its outbox is pushed. Backups of hubs are not needed.
4. **Devices keep reaching the hub when its address changes.** IP addresses change when a router is replaced, and tablets that point at a stale IP break silently. How, and how the connection is secured, is open (section 4, "Reaching the hub securely").
5. **Fail closed.** If the hub cannot decide something, it says "needs connection" rather than guessing.

## 1. Hardware kit

One kit per facility, assembled and flashed at a central depot.

| Part | Recommendation | Why |
|---|---|---|
| Board | Raspberry Pi 5, 4 GB (a Pi 4 works for smaller facilities; Compute Module boards under "Options under consideration") | The hub itself needs a few hundred MB at most; the rest of the RAM caches the data file, which decides speed once the file outgrows it (about 2.5 GB at 100 000 people; docs/PERFORMANCE.md). |
| Storage | 128 GB NVMe SSD on an M.2 HAT (Pi 5) or a USB 3 SSD (Pi 4). Not an SD card. | The store writes on every sync. SD cards wear out and corrupt on power loss. |
| Power | Official power supply plus a small UPS (UPS HAT or a 12 V mini UPS) | Power cuts are common. A clean shutdown protects the store, and a UPS keeps the LAN up during short cuts. |
| Clock | Pi 5 RTC battery (Pi 4 has no RTC) | TLS, sync watermarks and PAT expiry need the right time after a power cut with no internet. |
| Case | Passive-cooled metal case, wall-mountable | Heat and dust. No fan to fail. |
| Network | Ethernet cable; optional USB 4G modem for facilities with no uplink | See section 4. |
| Label | Hub ID and a QR code for connecting devices (section 4, "Reaching the hub securely") | The label is what staff and support use to find the hub. A Pi has no screen to show the code on. |

Rough cost per kit: USD 150–220.

## 2. The country image

Built once per release with Raspberry Pi's image tools (`pi-gen` or `rpi-image-gen`) in CI, so every image is reproducible. It contains:

- **Raspberry Pi OS Lite, 64-bit.** No desktop.
- **The hub binary** at `/usr/local/bin/hub`, the systemd unit from [`deploy/hub.service`](deploy/hub.service), and a `hub` service user.
- **The country defaults file** `/etc/hub/hub.yaml`: central URL, setup user group, release channel and release public key (spec 6.1). This is the only country-specific part.
- **Networking:** NetworkManager; `dnsmasq`, used only in "hub provides the network" mode (section 4); `avahi-daemon` for `hub.local`.
- **Time:** `chrony` with the country's NTP servers, falling back to the RTC.
- **Firewall** (`nftables`): inbound HTTPS from the LAN only. No SSH unless it comes in over the support VPN.
- **Updates:** `unattended-upgrades` for OS security fixes. The hub binary upgrades itself from the release channel (spec 12a).
- **Watchdog:** the hardware watchdog plus `Restart=always` in systemd.
- **Support VPN (optional, to build):** an outbound-only WireGuard tunnel to a country support server, disabled until the setup flow enables it.
- **First-boot service:** regenerates SSH host keys and the machine ID, so cloned images do not share identities.

The image holds **no secrets**. Each hub gets its own data key, hub ID and signing key pair at first boot and setup. Patient data on the SSD is encrypted by the hub itself: everything except a small key file (hub ID, mode, central URL, and each user's username and key slot) is in a data file encrypted with AES-256-XTS under the data key, so full-disk encryption is optional. If it is used, it must unlock without a person present, for example with a key held in TPM-like storage, which the Pi does not have. This is why the POC relies on application-level encryption.

## Options under consideration (not decided)

Ideas for hardware and the image, recorded with what they would imply for the hub. None is decided.

- **Storage: industrial pSLC microSD instead of an SSD.** Far higher write endurance than consumer cards, often with power-loss protection, no USB bridge or cable, low power. Capacity is a third of the raw flash (typically 8–64 GB), so the data must fit: about 6.5 GB at a busy HC IV with retention by programme, 15–16.5 GB with five years kept (docs/PERFORMANCE.md), plus the OS, the write-ahead log and a backup copy. With microSD, retention and a smaller data file become requirements. Random writes are SD cards' weak point: measure the card with `perf/pi.sh bench` on a real Pi, set the `pi4` profile's disk limits from it, and compare the card's rated total bytes written with the bytes the hub writes per day. On a Pi 5, an M.2 NVMe board is the alternative to a USB SSD.
- **Board: a Compute Module (CM4 or CM5) on an IO board instead of a Pi 4 or 5.** A flat board with mounting holes, easier to build into an enclosure than a Pi with a cable and a 2.5" drive. The official CM5 IO board has an M.2 slot for NVMe (no HAT, USB bridge or cable); the CM4 IO board has a full-size PCIe slot that needs an NVMe adapter card; third-party carrier boards (Waveshare and others) often have M.2 slots and cost less, but vary in quality. The official boards also have an RTC with a battery slot, Gigabit Ethernet and a 12 V barrel input, so a router-style 12 V mini DC UPS can power router and hub together. Raspberry Pi commits to producing the CM4 until at least 2034 and the CM5 for longer, which matters for spares over years. Modules come with 8–32 GB of soldered eMMC or without ("Lite"): eMMC is sturdier and faster than an SD card, but has no power-loss protection, cannot be swapped when worn (the module is scrap), and 32 GB gets tight with five years kept (15 GB) plus the OS and a backup copy. Imaging eMMC goes over USB (`rpiboot`), slower per unit at the depot than writing NVMe drives in bulk. Candidate kit: CM5 with 4 GB on an IO board with an M.2 NVMe drive, the Pi 5 + NVMe set-up on one board; compare complete kit prices. The `pi5` perf profile approximates it, and the `pi4` profile with `IO=0` a CM4 with NVMe.
- **Storage at scale: budget drives, chosen by test.** At 1000 units, a known-brand SSD costs perhaps USD 15–30 more per unit than a budget one (USD 15–30k), against field trips of perhaps USD 50–200 per dead drive. The bigger risk with budget drives is parts changing between batches. So: fix one exact part number with the supplier; shortlist two or three cheap candidates (tried for the POC: Intenso High Performance and Verbatim Vi550 S3, both 2.5" SATA, on a Pi 4 through StarTech's USB 3.0 SATA cable, whose ASMedia bridge is widely reported to work with UAS on the Pi 4); put 3–5 units of each through `perf/pi.sh bench`, the paced run (bytes written per day) and 100 power cuts under load; buy the cheapest that passes. Notes from the search: M.2 *SATA* drives do not work in the Pi 5's NVMe HATs; portable NVMe-based USB SSDs can draw more current than a Pi 4's ports supply; on the Pi, check `lsusb -t` shows `Driver=uas` and `dmesg` shows no resets under load (`usb-storage.quirks=<vid>:<pid>:u` falls back to the slower mode).
- **Storage for pilots: endurance microSD cards.** Rated for years of continuous video (sequential writes, so the rating overstates the margin for the hub's small synced writes); a year of a pilot is very likely within it. They are no better than other cards at power cuts and keep SD speed: 30 k people worked well with 1 GB in the ladder, 60 k was borderline, 100 k slow (docs/PERFORMANCE.md); 4 GB boards help, since the data file then fits in memory. Conditions: genuine 64–128 GB cards from a reputable supplier, a spare per site or district, a router UPS where possible, facilities sized to the board.
- **Power banks as a UPS: not recommended.** Many cut their output for a moment when mains comes or goes, which reboots the Pi; they sit at full charge in heat (wear, swelling); they give no power-fail signal, so the cut is only delayed; some do not switch back on after running flat. A router-style mini DC UPS (12 V and 5 V outputs, made to stay in line) does the job for about USD 20–40, and keeps the router up too.
- **Pi Zero 2 W for very small facilities: possible, not preferred.** 512 MB (cache for the data of about 5–10 k people), a CPU two to three times slower than a Pi 4's, SD storage unless an SSD is added over USB 2 through an adapter, 2.4 GHz Wi-Fi only (one radio: join the facility Wi-Fi or run an access point, not both well), Ethernet only through a USB adapter. It saves USD 20–40 against a Pi 4 1 GB or Pi 5 2 GB in a kit dominated by storage, power and case, and adds a second board family to support.
- **Read-only OS, logs in RAM.** Only the data partition takes writes, so a power cut cannot damage the system. Implications: OS fixes become image updates instead of `unattended-upgrades` (see A/B below); without an RTC the saved clock must live on the data partition (the Pi 5 has an RTC with a battery connector); keep the last crash or a small rotating log on the data partition, since logs in RAM are lost exactly when they are needed.
- **A small UPS with a shutdown signal.** The hub stops cleanly on SIGTERM, so a UPS that triggers a normal shutdown is enough. Requirements: a power-fail signal (GPIO or I2C), automatic power-on when power returns (essential unattended), a chemistry that tolerates heat, and enough current for a Pi 5. Kinds to evaluate (availability not checked): supercapacitor modules (shutdown only, nothing to wear out or swell), LiFePO4 boards (for example LiFePO4wered/Pi+), 18650 Li-ion HATs (for example the Geekworm X728 and X1200 series, Waveshare's UPS HAT; ride through outages, but Li-ion ages in heat), integrated controllers (PiJuice, UPS PIco).
- **Two system partitions (A/B).** Boots the previous system if an update fails, and is how a read-only OS gets updated. The hub's own upgrade (spec 12a) already migrates a copy of the store and keeps the old one. The catch: an older hub refuses a store migrated to a newer schema, so a rollback after the new version has written data must either switch back to the pre-upgrade store copy (losing what was written since) or schema changes must stay readable by the previous release. To settle before the first release.
- **Locking the device against changes.** In layers, cheapest first:
  - *Network:* beyond the firewall above (HTTPS from the LAN only), ship no SSH server, or one that accepts keys only and only over the support VPN; lock the root and default accounts. Hub settings change only through the hub UI, for the setup user group.
  - *Console:* disable the login prompts on screen and serial console, and remove the serial console from the boot settings.
  - *Physical access to the card or disk* is the remaining gap: anyone can edit the system on another computer, for example to record passwords at the next login and so get the data key (patient data itself stays encrypted). Secure boot closes it: a public key burned into the board's one-time-programmable memory, after which only signed images boot, with a verified read-only system partition (dm-verity) checked by the signed image. Pi 4 and Pi 5 are believed to support this; to be checked against Raspberry Pi's current documentation. It fits with the read-only OS and A/B partitions above, which update through signed images anyway. Costs: burning the key is irreversible, the signing key becomes critical infrastructure (lost: no new images; leaked: no protection), and the boot order must be locked too.
  - *Out of reach:* theft, unplugging or wiping. With the above, a modified or foreign system does not boot and the data stays encrypted; what remains is continuity, covered in [RECOVERY.md](RECOVERY.md).
  - Likely more than a first deployment needs; recorded for later discussion.
- **No hardware: the hub as a Windows service on an existing facility PC.** The alternative to a kit. Same hub, same store, one PC.
  - *Code:* the hub cross-compiles for Windows except in two places: free disk space for the status page (`statfs`) and the self-upgrade, which replaces the running process and hands it the data key (`internal/upgrade`). Windows cannot replace a running process or overwrite a running `.exe`, so the upgrade needs its own flow: the service starts the new binary, or the service manager restarts it, with the data key handed over so the hub does not stay locked. The rest is small: Windows paths (`C:\ProgramData\...`) instead of `/var/lib/hub` and `/etc/hub`, access rules on the data folder instead of `0600`, and answering stop and shutdown from the service manager. Not checked yet: the SQLite driver and the encrypted file layer on Windows (file locking, flushes), and the test suite on a Windows runner.
  - *The PC:* must not sleep or hibernate (power plan); Windows Update reboots it; antivirus must exclude the data folder, or it locks and scans the database and its write-ahead log; it is someone's working PC, with full disks, other software and people switching it off. A desktop has no battery, so a power cut is a hard stop. Resources are not a concern: an old i3 with 4 GB and an SSD is faster than a Pi 5, with no SD card wear.
  - *After a restart* the hub is locked until a user with a key slot logs in through it (RECOVERY.md, notes). With Windows Update this happens more often than on a Pi. Windows offers what option I in RECOVERY.md needs, a secret held by the machine for a service at boot (DPAPI, the TPM), so pushing after a reboot without a login is easier here than on a Pi.
  - *Network:* the installer adds a firewall rule for the hub's port; a network marked "Public" may still block it. There is no "hub provides the network" mode (section 4, case C): Windows' mobile hotspot takes about 8 devices and expects an uplink to share. A facility needs a working router. A laptop that leaves the building takes the hub with it, so a desktop that stays put is better.
  - *Confidentiality:* data at rest stays encrypted, but anyone with Windows admin rights on the PC, or malware, can read the running hub's memory, and users of the PC can open the hub UI. Run the service under its own restricted account, restrict the data folder to it, and prefer a PC not used for email and browsing.
  - *Rollout:* an installer with admin rights (service, firewall rule, antivirus exclusion, power plan, data folder); a code-signing certificate, or SmartScreen and antivirus warn about the binary; support is harder, because every PC differs.
  - Considered and not preferred:
    - *Docker on Windows.* It needs the WSL2 virtual machine: Docker Desktop starts only when a Windows user logs in (a reboot then stops everything until someone does), may need a paid licence for large organisations, needs virtualisation enabled in the BIOS and 1–2 GB of extra memory, and puts the container behind its own address translation. It keeps the Linux image and self-upgrade, but fixes none of the PC issues above and hides DPAPI and the TPM.
    - *Several PCs sharing the hub, one acting as master.* Electing a master safely needs at least three machines switched on; otherwise two can each believe they are master. Accepting writes on more than one machine needs conflict merging, which the single-writer design (one outbox, central decides access) does not have. Devices also need one address that follows the master. A standby that only receives copies of the encrypted data file, and is promoted by hand, gives most of the benefit (RECOVERY.md, option C).

## 3. From depot to facility

### At the depot (bulk)

1. Flash the image onto the SSDs, for example with a USB duplicator or `rpi-imager --cli` in a loop.
2. Assemble the kits and power each one on once on the depot network. This runs first boot, sets the clock, creates the hub ID and checks that it can reach central.
3. Print and stick the label (hub ID, the QR code for connecting devices, and the setup token from `data_dir/setup-token` if the image names no central server). The depot list of hub IDs becomes the inventory; it matches the `facility-hub-registry` namespace on central.
4. Pack it with a one-page installation card.

No facility is assigned at the depot. Any box can go anywhere.

### At the facility (15 minutes)

1. Mount the hub near the router and plug in power and Ethernet. The installation card covers the case with no router (section 4, case C).
2. On any laptop or phone on the facility network, open the address on the label. The hub shows its setup page.
3. A member of the setup user group logs in with their DHIS2 account. That is typically the district HMIS officer, possibly remotely by phone with a facility staff member typing. The hub then:
   - finds the facility from the user's org units, showing a picker if the user covers several;
   - finds the tracker programs;
   - registers itself on central, offering to retire an old hub for the same facility;
   - creates its hub token;
   - starts the first full pull.
4. Every staff member logs in once through the hub while it is online. This is required for offline login later.
5. Point the client devices at the hub (section 5).
6. Optional check: open the hub's status page on a phone, then unplug the uplink and confirm a tablet can still sync to the hub.

### Replacing or moving a hub

- **Replacement:** set up a spare at the facility and confirm "replace the old hub". The old one is marked retired in the registry; if it is still running and online, it stops serving patient data and only sends what it has not pushed. Then collect it and reset it. Anything an old hub that is dead had not pushed is lost, which is why the UPS and SSD matter.
- **Decommissioning:** use "Reset hub" in the admin page. It pushes the outbox, wipes the store and returns to setup mode. Then revoke its PATs centrally.
- **Theft:** the store is unreadable without a staff password, because the data key is only in memory. It is only as strong as the weakest password among the users with a key slot, though: whoever holds the SSD can test password guesses offline against each slot, and a cracked password is also that user's central DHIS2 password. Treat a stolen hub as a possible password leak: ask its users to change their passwords. Revoke the hub's and users' PATs on central **(to build: a "revoke all tokens of this hub" action)**.

## 4. Network set-ups

Facilities differ. The image supports four cases without per-facility configuration files; the right mode is picked during setup.

### Case A: existing router with Wi-Fi and an internet uplink (most common)

```
 tablets, laptops ──Wi-Fi──► facility router ──► internet ──► central DHIS2
                                   │
                                hub (Ethernet, DHCP client)
```

- The hub is an ordinary DHCP client on the LAN.
- **Ask for a DHCP reservation** on the router so the hub keeps its address, where someone can configure the router. Whether it is required depends on how devices find the hub ("Reaching the hub securely" below).
- **Check for client isolation.** Some routers isolate Wi-Fi clients from each other and from the wired LAN ("AP isolation", "guest network"). Tablets then cannot reach the hub. The setup page should test this **(to build: the setup page asks the installer to open the status page from a tablet)**.
- **Check for DNS rebinding protection** if devices reach the hub by a public name (section 4, "Reaching the hub securely", option A).

### Case B: a 4G MiFi or mobile router as the only network

These small devices often have no Ethernet port and frequently isolate clients.

- The hub joins the MiFi's Wi-Fi as a client. The Wi-Fi password is entered on the setup page **(to build: a Wi-Fi client setup step on the setup page, while the hub is reachable on its own temporary access point)**.
- If the MiFi isolates clients, use case C instead and connect the MiFi to the hub as its uplink.

### Case C: no usable local network; the hub provides it

```
 tablets, laptops ──Wi-Fi──► hub (access point, DHCP, DNS) ──Ethernet / USB 4G──► internet
```

- The hub runs its own Wi-Fi access point (`hostapd`), DHCP and DNS (`dnsmasq`). Its uplink is the Ethernet port (to a modem) or a USB 4G modem.
- The Pi's built-in Wi-Fi handles a small facility, about 10–15 devices. Beyond that, add a cheap external access point on the hub's Ethernet.
- This is the most robust case, because the hub controls DHCP, DNS and client isolation. It is also the simplest to support remotely, because every facility in this mode looks the same.
- Mode switch **(to build)**: if the hub finds no DHCP server on Ethernet at first boot, it starts its own access point with a fixed SSID and password printed on the label.

### Case D: managed networks (hospital IT, NGO-managed sites)

- Static IP or reserved DHCP, a firewall exception for outbound HTTPS to central and to the release channel, and inbound HTTPS from client subnets.
- Proxy servers: the hub needs outbound HTTPS to central's URL and the release channel. An `https_proxy` setting **(to build)** would cover sites that force a web proxy.

### What the hub needs from any network

| Direction | What | Why |
|---|---|---|
| Hub → central | HTTPS (443) to the central DHIS2 URL | Sync, login, metadata |
| Hub → release channel | HTTPS (443) | Self-upgrade |
| Hub → NTP | UDP 123 to the country's or a public NTP pool | Correct time |
| Hub → support VPN (optional) | UDP 51820 (WireGuard) | Remote support |
| Clients → hub | HTTPS (443 or 8443) on the LAN | Capture apps |

All connections to central are outgoing. Nothing on the internet needs to reach the hub, so it works behind NAT and carrier-grade NAT on 4G.

### Reaching the hub securely (options, not decided)

The hardest part of deployment. Two requirements:

1. **Security.** Every connection to the hub is encrypted and authenticated, on the facility LAN too. No plain HTTP, and no certificate warnings that staff learn to click through: a warning clicked through stops a listener on the Wi-Fi, not someone actively impersonating the hub (a fake access point, a spoofing device), and it teaches staff to ignore warnings everywhere.
2. **Simplicity for end users.** Connecting a device is one step that needs no technical knowledge, and nothing breaks when the hub's address changes.

The constraints, from the POC and from how TLS works:

- **The Android Capture app does not trust user-installed certificate authorities.** A self-signed or private-CA certificate only works on rooted or fully managed devices.
- **Android apps do not reliably resolve `.local` names** (mDNS); Windows and macOS do. Wi-Fi isolation, multicast filtering and separate subnets break mDNS for everyone.
- **While the internet is down, devices find the hub only by IP address or through a DNS server the hub controls.** A public DNS name is looked up through the provider's DNS; phones cache answers for minutes and many routers not at all.
- **No public certificate can be issued for a private IP address.**

#### Option A: public DNS name and publicly trusted certificate

Each hub gets a name under a country zone (for example `h-4f7k2q.hubs.hmis.example.org`) pointing to its LAN address, and a Let's Encrypt certificate for it. Stock Android and every browser trust it with nothing installed.

What it takes:

- *Central:* nothing new in DHIS2. The hub adds its LAN addresses to its heartbeat; the service below uses an account that can read the `facility-hub-*` namespaces and write certificates back.
- *A name service (new, run by the country or HISP):* a small periodic job that updates each hub's DNS record from its heartbeat and gets certificates with the DNS-01 challenge. It holds the DNS API credentials; hubs never do. Better than an earlier draft of this design, which sent hubs their key: the hub puts a certificate signing request in the datastore and picks up the certificate, so its key never leaves it.
- *The hub:* report its LAN addresses (on Windows, the adapter on the default route, not VPN or virtual adapters), generate the key and signing request, reload the certificate without a restart, renew in time, accept its own name as a host, serve on 443.
- *Administration:* a domain with a DNS provider that has an API, and a decision on who owns it. Let's Encrypt limits new certificates to about 50 per registered domain per week (renewals mostly exempt), so 500 hubs take about 10 weeks unless the limit is raised. Certificate lifetimes are getting shorter, so a hub offline for longer than one loses its certificate. Every certificate is published in public logs: use random names, not facility names.
- *Each facility:* routers and resolvers with DNS rebinding protection (OpenWrt and Pi-hole by default, some ISP routers) drop public answers that hold private addresses; the zone must be allowed there.

The gap: **offline, the name does not resolve** unless the hub is the facility's DNS server (case C, or the router hands out the hub as DNS server, which needs router configuration and a fixed hub address). Without that, the option works online and fails exactly when the hub is needed. With its moving parts, it is the most complex option to run.

#### Option B: the Capture app pins the hub's key

The hub keeps its own key and self-signed certificate, as now. The Capture app stores the fingerprint of the hub's public key with the account, and for that server accepts that key and no other. A key that does not match is a hard error ("This is not your facility's hub"), with no "connect anyway".

- *Connecting a device:* join the facility Wi-Fi; in Capture choose "Add account" and "Scan hub code"; scan the QR code, which holds the hub's address, the key fingerprint and the hub ID; the app connects, checks the key and shows the hub's name ("Kibuye HC IV hub, connect?"); log in with the normal DHIS2 account. A user's first login on a hub must be online. A short code to type is the fallback for a broken camera.
- *Where the code comes from, and why it can be trusted* (the fingerprint is not secret; what matters is that it comes from the real hub):
  - The hub's dashboard on the hub PC itself (Windows option), opened on `localhost`: nothing on the network to intercept, and showing a false code needs control of the PC, which would compromise the hub anyway. No labels to print.
  - A printed label (a Pi has no screen). Anyone walking past can stick another code over it.
  - A DHIS2 app on central: the hub publishes its address and fingerprint in its registry entry, and the app shows "your facility's hub" to its users. Central's public certificate and DHIS2's own accounts carry the trust. Requires that only the hub (with its hub token) or an admin can write that entry: protect the namespace (section 6, "Security operations"). Needs the internet when the device is connected.
  - Further: Capture asks central for its facility's hub while online, and pins the key it gets, with no code to scan. The same trust anchor, more Android work.
  - The hub's dashboard opened from another device's browser is only as trustworthy as that browser's connection to the hub. The dashboard should say so when it is not opened on `localhost`.
- *Lifecycle:* the key, not the address, is the identity. The hub can announce itself on the LAN (DNS-SD), and the app accepts any address that presents the pinned key: the announcement needs no protection, so a changed IP needs no rescan and no DHCP reservation. Certificates are renewed with the same key, so nothing expires for devices. A hub restored on another machine keeps working for devices if the backup holds its TLS key (`data_dir/tls.key`); otherwise each device rescans (about 10–30 per facility). A stolen key means a new key and every device rescanning.
- *Work:*
  - In the DHIS2 Android SDK, not only the app screens: a trust check per server in its OkHttp client, which also loads images and files. OkHttp's own pinning is not enough, because it still requires a publicly trusted chain. A contained change in our estimate; it is the Android team's roadmap and needs their security review.
  - In the hub: the QR code and fingerprint on the dashboard, the registry entry, optionally the LAN announcement, and the TLS key in backups.
  - Nothing else on central; no domain, no name service.
- Works offline, by IP, fully encrypted; stricter than a public certificate, since one key is accepted instead of any of about 150 public authorities.

#### Option C: the hub's own CA, installed on the device

Exists now (`tls: true`): at first boot the hub makes a CA limited by name constraints to its own names and private addresses, issues its certificate (for `hub.local`, the host name, `<host>.local`, `localhost` and its LAN addresses), and destroys the CA key. Devices install `ca.crt` from `/hub/ca.crt`. Installing it cannot expose other sites to interception.

- *Android:* ignored by Capture unless the device is fully managed, so not a general answer for tablets.
- *Laptops and desktops:* the way browsers get trust, since a browser cannot pin a key. Installation is the burden: on Windows, double-click and pick "Trusted Root Certification Authorities" by hand; on macOS, Keychain Access and "Always Trust"; Firefox may need its own import; locked-down laptops may forbid it. Managed fleets push it with Group Policy or Intune. The hub could serve a one-click installer.
- *Address:* use `https://<pc-name>.local`. The certificate lists the LAN addresses the hub had at first boot and the CA key is gone, so a changed IP breaks it, while the name stays valid.
- *On the hub PC itself* (Windows option), `https://localhost` needs no installation at all.

#### Ruled out

- **Plain HTTP**, even on the facility LAN. Anyone on the Wi-Fi reads passwords and patient data. Test set-ups only.
- **Self-signed certificates with warnings clicked through** (requirement 1).
- **`.local` names alone for Android** (constraints above).

#### Leading combination (not decided)

Option B for Android, with the code shown on the hub PC's dashboard (Windows) or from central's registry through a DHIS2 app, and option C for the few laptops, or `localhost` on the hub PC. It meets both requirements without infrastructure, but depends on the Android team. Until it ships, encryption for Android exists only on managed devices with the hub's CA installed. Option A remains the alternative if browsers become the main clients and installing the CA proves unworkable, provided the offline lookup gap is closed.

## 5. Pointing client devices at the hub

### New devices

- **Android tablets:** in the Capture app's login screen, scan the hub's QR code (from the hub's dashboard, its label or central; section 4, "Reaching the hub securely"). Then log in. The tablet syncs metadata and data from the hub.
- **With an MDM** (Android Enterprise, for example Intune, Headwind or Google's), push the hub URL with the device. The Capture app's managed-configuration support needs checking before relying on this **(to check)**.
- **Laptops and desktops:** a browser bookmark or a desktop shortcut to the hub's address. Installing the Capture web app as a PWA from the hub's address gives an app-like icon.

### Existing devices already pointing at central

A device keeps one server URL per account, so moving it to the hub is a one-time step per device:

1. **Before switching, sync everything to central** while online. Unsynced data on a device must not be stranded under the old server.
2. **Android:** log out and log in again with the hub's address. The Capture app supports multiple accounts, so the central account can stay for fallback, but staff should use only one. Users must not end up entering data in both.
3. **Browsers:** replace the central bookmark with the hub's.
4. **Training point:** "At this facility, always open DHIS2 through the hub address." Using central directly still works while online, but that data does not appear on the hub until the hub's next pull, so other users will not see it if the internet then drops.

### Devices that move between facilities

A hub only serves its own facility. A community health worker's tablet that visits several facilities should keep central as its server. This is a limitation of hub-as-server, which the long-term client fallback in section 7 removes.

### Ongoing

- Android syncs with the hub every 30 minutes or by hand (spec 5.1). For quick hand-over between a tablet and another user, staff press "Sync" after finishing a patient. This needs training.
- Web users see each other's work immediately, because every web request goes to the hub.

## 6. Running the fleet

### Monitoring

- Every hub writes a heartbeat to `facility-hub-status/<hub_id>` on central each sync. It holds version, uptime, last push and pull, pending count, issue count, free disk and last upgrade result.
- **A small DHIS2 app (to build)** lists hubs from `facility-hub-registry` with their latest heartbeat. It flags:
  - no heartbeat for more than 2 days;
  - pending data older than 1 day;
  - rejected objects (issues);
  - low disk;
  - failed upgrades;
  - "hub needs upgrade" (central version not supported).
- Issues (rejected data) land in `facility-hub-issues`. They can be followed up by district staff, or turned into a DHIS2 message to the user who entered the data **(to build)**.

### Upgrades

- **Hub software:** publish a signed release to the country release channel. Stage it:
  1. 5 pilot hubs, listed by hub ID;
  2. 10 %;
  3. 50 %;
  4. 100 %.

  Each hub checks after its sync cycle, pushes its outbox, migrates a copy of its store, swaps, health-checks and rolls back on failure. Results show up in the heartbeat.
- **Before a DHIS2 server upgrade:** roll out a hub release that supports the new version first. Each hub release supports the current and the next DHIS2 major (spec 12a). Hubs on an unsupported version stay usable online, as pass-through only, and show "hub needs upgrade".
- **Operating system:** security updates install themselves (`unattended-upgrades`, with automatic reboot at night). Re-image only for major OS changes. That can be done by shipping a pre-flashed SSD, because the hub holds nothing that central does not.

### Support

- First line: the status page (`/hub/status`) and the admin page (`/hub/ui`), opened by the district officer from the facility network.
- Second line: the support VPN, if enabled. It is outbound only; the support server reaches the hub through the tunnel.
- Most faults are fixed by replacing the hub with a spare and setting it up again. Keep a stock of spares at district level.

### Security operations

- **Protect the data store namespaces** on central (`facility-hub-*`) before the first hub goes out. By default any user can write them (FINDINGS.md): a forged registry entry can retire a hub (it then stops serving offline, though it keeps its data) or block setup of a facility.
- **Hub token:** use a dedicated low-privilege "hub system" user per country, not the setup user's personal token (FINDINGS.md).
- **PAT hygiene:**
  - PATs expire after `pat_expiry_days` (default 90) and renew at online login.
  - Users who leave are disabled on central, so their PATs stop working.
  - Hubs show those users as "needs online login".
- **Physical access:** data at rest is encrypted, and the key is only in memory after a staff login. A stolen hub reveals no patient data without a staff password, but a powered-on hub that is moved keeps its key until it loses power.
- **Passwords protect the disk.** Each key slot can be attacked offline by whoever holds the SSD (Argon2id slows this down; it does not stop weak passwords). Enforce central's password policy for hub users. The hub removes slots of users who have not logged in for `key_slot_max_age`.
- **TPM (a requirement for a national rollout).** A TPM add-on board (about USD 25 for the Pi) could hold a device secret mixed into every key slot, so an SSD on its own is useless and the TPM limits how fast passwords can be guessed **(to build)**.
- **What the encryption does not cover.** AES-XTS keeps the data file confidential but does not detect tampering outside the sealed rows; someone with write access to the disk can corrupt it. And once a staff member logs in, the whole facility's data is decrypted in the hub's memory: a running hub is a more valuable target than its disk.
- **Central server address:** set `central_url` (and `central_hosts` for alternatives) in the image so nobody at a hub can choose another server. Domain migrations go through a signed release (`hub release -central-url`), not new images.
- **Host names:** if devices reach the hub by a name other than its host name, `hub.local` or its IP (for example a router DNS name), list it in `hub_hosts`; the hub refuses unknown `Host` headers.

## 7. Rollout plan

| Phase | Hubs | Goal |
|---|---|---|
| Pilot | 5–10 facilities in one district, chosen for poor connectivity | Validate network cases A–C, the installation card, staff training and the offline workflows with real patients. Measure sync volumes and power issues. |
| Expansion | 50 facilities across 3–5 districts | Validate depot logistics, the chosen way of reaching the hub securely, the monitoring app and staged upgrades. |
| National | Hundreds | Depot flashing in batches; district officers install and set up; central monitoring. |

### Per-facility checklist

- [ ] Power socket with UPS; hub mounted, cooled, labelled
- [ ] Network case identified (A/B/C/D); client isolation checked (and DNS rebinding, with option A)
- [ ] Hub set up by a setup-group user; first pull completed
- [ ] Every staff member logged in once through the hub
- [ ] Every tablet and browser switched to the hub address (synced first)
- [ ] Offline test: uplink unplugged, a registration on one device seen on another
- [ ] Heartbeat visible in the country monitoring app

### Longer term

The client-side fallback in the shared app-runtime would remove most of section 5. Devices would keep central as their server and use the facility's hub automatically when central is unreachable (spec 5.1 and open question in spec 14). Discovery could use the hub's address and key, published in the registry for the user's facility. With that in place, tablets that move between facilities would also work.
