# Deploying facility hubs at scale

How the hub in this repository could be rolled out to hundreds of facilities on Raspberry Pis: the hardware kit, the single country image, what happens at the facility, the network set-ups we will meet, how existing and new client devices are pointed at the hub, and how the fleet is run afterwards.

This is a proposal built on what the POC showed (see [FINDINGS.md](FINDINGS.md)). Items marked **(to build)** do not exist in the POC yet.

## Principles

1. **One image per country, no per-facility configuration.** Every hub is flashed with the same image. A hub learns its facility, programs and users from central when a setup user logs in (spec 6.2). A box can go to any facility, and a spare can replace any hub.
2. **Nothing to install on site.** The installer plugs in power and network and logs in once. No keyboard, screen or command line.
3. **Central stays the source of truth.** A hub can be wiped or replaced at any time without data loss once its outbox is pushed. Backups of hubs are not needed.
4. **The hub is reached by name, not by IP address.** IP addresses change when a router is replaced, and tablets that point at a stale IP break silently.
5. **Fail closed.** If the hub cannot decide something, it says "needs connection" rather than guessing.

## 1. Hardware kit

One kit per facility, assembled and flashed at a central depot.

| Part | Recommendation | Why |
|---|---|---|
| Board | Raspberry Pi 5, 4 GB (Pi 4, 4 GB also works) | The hub needs well under 1 GB of RAM at facility scale. 4 GB leaves room for the in-memory index. |
| Storage | 128 GB NVMe SSD on an M.2 HAT (Pi 5) or a USB 3 SSD (Pi 4). Not an SD card. | The store writes on every sync. SD cards wear out and corrupt on power loss. |
| Power | Official power supply plus a small UPS (UPS HAT or a 12 V mini UPS) | Power cuts are common. A clean shutdown protects the store, and a UPS keeps the LAN up during short cuts. |
| Clock | Pi 5 RTC battery (Pi 4 has no RTC) | TLS, sync watermarks and PAT expiry need the right time after a power cut with no internet. |
| Case | Passive-cooled metal case, wall-mountable | Heat and dust. No fan to fail. |
| Network | Ethernet cable; optional USB 4G modem for facilities with no uplink | See section 4. |
| Label | Hub ID, its address (`https://<hubid>.hubs.example.org`) and a QR code of that address | The label is what staff and support use to find the hub. |

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
- **Read-only OS, logs in RAM.** Only the data partition takes writes, so a power cut cannot damage the system. Implications: OS fixes become image updates instead of `unattended-upgrades` (see A/B below); without an RTC the saved clock must live on the data partition (the Pi 5 has an RTC with a battery connector); keep the last crash or a small rotating log on the data partition, since logs in RAM are lost exactly when they are needed.
- **A small UPS with a shutdown signal.** The hub stops cleanly on SIGTERM, so a UPS that triggers a normal shutdown is enough. Requirements: a power-fail signal (GPIO or I2C), automatic power-on when power returns (essential unattended), a chemistry that tolerates heat, and enough current for a Pi 5. Kinds to evaluate (availability not checked): supercapacitor modules (shutdown only, nothing to wear out or swell), LiFePO4 boards (for example LiFePO4wered/Pi+), 18650 Li-ion HATs (for example the Geekworm X728 and X1200 series, Waveshare's UPS HAT; ride through outages, but Li-ion ages in heat), integrated controllers (PiJuice, UPS PIco).
- **Two system partitions (A/B).** Boots the previous system if an update fails, and is how a read-only OS gets updated. The hub's own upgrade (spec 12a) already migrates a copy of the store and keeps the old one. The catch: an older hub refuses a store migrated to a newer schema, so a rollback after the new version has written data must either switch back to the pre-upgrade store copy (losing what was written since) or schema changes must stay readable by the previous release. To settle before the first release.

## 3. From depot to facility

### At the depot (bulk)

1. Flash the image onto the SSDs, for example with a USB duplicator or `rpi-imager --cli` in a loop.
2. Assemble the kits and power each one on once on the depot network. This runs first boot, sets the clock, creates the hub ID and checks that it can reach central.
3. Print and stick the label (hub ID, address, QR code, and the setup token from `data_dir/setup-token` if the image names no central server). The depot list of hub IDs becomes the inventory; it matches the `facility-hub-registry` namespace on central.
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
- **Ask for a DHCP reservation** on the router so the hub keeps its address, where someone can configure the router. If not, see "Reaching the hub by name" below. A reservation is a convenience, not a requirement.
- **Check for client isolation.** Some routers isolate Wi-Fi clients from each other and from the wired LAN ("AP isolation", "guest network"). Tablets then cannot reach the hub. The setup page should test this **(to build: the setup page asks the installer to open the status page from a tablet)**.
- **Check for DNS rebinding protection** (see below).

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

### Reaching the hub by name, and its certificate

This is the hardest part, because of two findings from the POC:

- **The Android Capture app does not trust user-installed certificate authorities.** A self-signed or private-CA certificate only works on rooted or fully managed devices. A **publicly trusted certificate** (Let's Encrypt) works on every stock device.
- **Android apps do not reliably resolve `hub.local`** (mDNS). Tablets need a real DNS name or an IP address.

Proposed design **(to build)**:

1. **Each hub gets a public DNS name** under a country zone, for example `h-4f7k2q.hubs.hmis.example.org`. It is derived from the hub ID and printed on the label.
2. **The name points to the hub's LAN address.** A public DNS record with a private IP (for example `192.168.1.20`) is allowed. It resolves anywhere, but is only reachable on that facility's LAN.
3. **The hub keeps the record current.** Each heartbeat reports the hub's LAN IP, and a small country service updates the DNS record when it changes. A router swap or a new DHCP lease does not break the tablets.
4. **Certificates come from a country certificate service.** The hub cannot answer an HTTP challenge from the internet, so the service gets a Let's Encrypt certificate for the hub's name using a DNS-01 challenge. The service holds the DNS API credentials; the hub never does. The hub proves its identity with the signing key pair it registered at setup, and fetches its certificate and key over HTTPS. Renewal every 60–90 days happens on a normal sync.
5. **Watch for DNS rebinding protection.** Some routers and DNS resolvers drop public DNS answers that contain private IP addresses. Where this happens:
   - case C: the hub is the DNS server, so this cannot happen;
   - case A: whitelist the hub zone on the router, or set the hub as the facility's DNS server;
   - last resort: devices use the IP address on the label, over plain HTTP.

Until this exists, the POC's working set-up is plain HTTP on the LAN by IP address, which the Android app accepts. With `tls: true`, managed devices can install the hub's CA from `/hub/ca.crt`; the CA is limited to the hub's own names and private addresses, and its key is discarded after signing. That is acceptable for a pilot on a closed facility network, but not for a national rollout.

## 5. Pointing client devices at the hub

### New devices

- **Android tablets:** in the Capture app's login screen, scan the QR code on the hub's label to fill in the server URL, or type the hub's name. Then log in. The tablet syncs metadata and data from the hub.
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
| Expansion | 50 facilities across 3–5 districts | Validate depot logistics, the certificate service, the monitoring app and staged upgrades. |
| National | Hundreds | Depot flashing in batches; district officers install and set up; central monitoring. |

### Per-facility checklist

- [ ] Power socket with UPS; hub mounted, cooled, labelled
- [ ] Network case identified (A/B/C/D); client isolation and DNS rebinding checked
- [ ] Hub set up by a setup-group user; first pull completed
- [ ] Every staff member logged in once through the hub
- [ ] Every tablet and browser switched to the hub address (synced first)
- [ ] Offline test: uplink unplugged, a registration on one device seen on another
- [ ] Heartbeat visible in the country monitoring app

### Longer term

The client-side fallback in the shared app-runtime would remove most of section 5. Devices would keep central as their server and use the facility's hub automatically when central is unreachable (spec 5.1 and open question in spec 14). Discovery could use the same DNS name, published in the registry for the user's facility. With that in place, tablets that move between facilities would also work.
