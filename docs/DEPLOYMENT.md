# Deploying facility hubs at scale

How the hub in this repository could be rolled out to hundreds of facilities on Raspberry Pis, or on an existing Windows PC: the hardware kit, the single image, what happens at the facility, the network set-ups we will meet, and how client devices are pointed at the hub.

This is a proposal built on what the POC showed (see [FINDINGS.md](FINDINGS.md)). Items marked **(to build)** do not exist in the POC yet. How the fleet is run afterwards is in [OPERATIONS.md](OPERATIONS.md). Ideas not decided, and options considered and not chosen, are in [design/deployment-options.md](design/deployment-options.md).

## In short

- **Who gets a hub** (section 0): facilities where patients pass between staff on different devices, staff work in the browser, and outages are frequent or long. Most small facilities do not need one.
- **The kit** (section 1): a Raspberry Pi 5 with 4 GB, an SSD (not an SD card), its real-time clock with a battery, a USB stick that stays in the box, on a UPS. Or the facility's own Windows PC, with the installer.
- **One image for every country** (section 2): no user, no SSH, nothing country-specific. It updates its own program from GitHub releases, signed, at the version each country chooses on central.
- **Set-up by facility staff** (section 3): flash, plug in, open `http://dhis2.local`, type central's address and the setup code from the USB stick, log in. About an hour; no IT visit.
- **Networks** (section 4): on the facility's router (most common), on a MiFi, or the hub as the facility's Wi-Fi. Devices reach it at `https://dhis2.local` and trust its own certificate authority, installed once per device.
- **Running the fleet** (section 6, and [OPERATIONS.md](OPERATIONS.md)): the Facility Hub app on central shows each hub's hourly report, its flags and the changes central refused, approves each hub's own account and chooses the version.
- **Rollout** (section 7): national preparation, a pilot of 5–10 facilities in one district, then about 50 across a few districts, then district by district.

## Principles

1. **One image for every country, no per-facility configuration.** Every Pi is flashed with the same image, and every PC gets the same installer; nothing country-specific is flashed or built in. Central's address is given at set-up, from the country's installation card. A hub learns its facility, programs and users from central when a setup user logs in (spec 6.2). A box can go to any facility, and a spare can replace any hub.
2. **Nothing to install on site.** The installer plugs in power and network and logs in once. No keyboard, screen or command line.
3. **Central stays the source of truth.** A hub can be wiped or replaced at any time without data loss once its outbox is pushed. Backups of hubs are not needed.
4. **Devices keep reaching the hub when its address changes.** IP addresses change when a router is replaced, and tablets that point at a stale IP break silently. Devices use `https://dhis2.local`, which follows the hub (section 4, "Reaching the hub securely").
5. **Fail closed.** If the hub cannot decide something, it says "needs connection" rather than guessing.
6. **No IT staff at facilities, no visits needed.** The people at a hub facility are the staff who enter data. Setting up, replacing and unlocking a hub are things they can do with an installation card; everything else is done from central.
7. **A hub only where it is needed.** A hub is for sharing data between staff and devices inside a facility while the internet is down. Most small facilities do not need one (section 0).
8. **Keep it simple.** No new national infrastructure: the software comes from GitHub releases, and the country's settings and decisions live on its central DHIS2.

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
| Board | Raspberry Pi 5, 4 GB (a Pi 4 works for smaller facilities) | The hub itself needs a few hundred MB at most; the rest of the RAM caches the data file, which decides speed once the file outgrows it (about 2.5 GB at 100 000 people; [PERFORMANCE.md](PERFORMANCE.md)). |
| Storage | 128 GB NVMe SSD on an M.2 HAT (Pi 5) or a USB 3 SSD (Pi 4). Not an SD card. | The store writes on every sync. SD cards wear out and corrupt on power loss. A Pi 4 on its SD card ran 72 hours offline in testing, which says nothing yet about wear over years. |
| Power | Official power supply, connected to a UPS. Budget one where the facility has none: no small UPS for the kit has proven good enough yet | Power cuts are common. A clean shutdown protects the store, and a UPS keeps the LAN up during short cuts. |
| Clock | Required: the Pi 5's RTC with its battery; a Pi 4 has no RTC and needs an add-on RTC module | TLS and token expiry need the right time after a power cut with no internet, and offline the hub's clock decides which aggregate period is current and when a period expires. Without an RTC, the Pi starts at its image's date after a power cut with no network ([design/2026-11-04-no-rtc.md](design/2026-11-04-no-rtc.md)). |
| USB stick | An industrial USB stick, in the box | Before set-up it carries the hub's setup code; after set-up it holds what a restore needs: every pending save, the key file and the hub's state, copied each minute ([RECOVERY.md](RECOVERY.md), option C2). A dead disk then loses at most a minute of saves; the cached data comes back from central. |
| Case | Wall-mountable; being tested: a passive-cooled metal case for the Pi 4, a fan (active cooler or fan case) for the Pi 5 | Heat and dust. A fanless case has no fan to fail; the Pi 5 may need one to avoid throttling. Chosen after the tests ([HARDWARE-TESTING.md](HARDWARE-TESTING.md)). |
| TPM | Recommended, not required: an add-on TPM board (about USD 25, Infineon SLB 9670) | Makes a disk taken on its own useless to a thief ([SECURITY-DESIGN.md](SECURITY-DESIGN.md), "The TPM"). |
| Network | Ethernet cable; optional USB 4G modem for facilities with no uplink | See section 4. |
| Label | Hub ID and `dhis2.local` | What staff and support use to find the hub. Nothing depends on it: the stick has the same (`hub.txt`, section 4). |

Rough cost per kit: USD 150–220. Other boards, storage and power options are in [design/deployment-options.md](design/deployment-options.md).

## 2. The hub image and releases

One generic image per release (`image/`, see image/README.md). The supplier's CI builds it from Raspberry Pi OS Lite and publishes it, with a signed checksum, in the GitHub release next to the hub binaries. Nothing in the image is country-specific, so no country maintains its own image across hub and DHIS2 releases. `image/check.sh` checks every built image without booting it. The image contains:

- **Raspberry Pi OS Lite, 64-bit** (trixie). No desktop, no SSH, no console user, no cloud-init: set-up happens on the hub's own page.
- **The hub binary**, in a folder the `hub` service user owns so it can upgrade itself, and the systemd unit from [`deploy/hub.service`](../deploy/hub.service).
- **No country settings.** Central's address is given at set-up (section 3). The release source and key are the supplier's, built into the binary.
- **Networking:** NetworkManager for every connection, with its own DHCP and DNS for the hub's own Wi-Fi (section 4), and `avahi-daemon` for `dhis2.local`.
- **Time:** `chrony` with public NTP pools, falling back to the RTC. Without an RTC, `hub-clock.service` saves the time every 15 minutes and at shutdown, and the next start begins there.
- **Firewall** (`nftables`): from private addresses only, ports 80 and 443 and mDNS, plus DHCP and DNS on the hub's own Wi-Fi.
- **Updates:** `unattended-upgrades` for OS fixes, rebooting at 03:00 in central's time zone when needed. The hub binary upgrades itself to the version the country chose on central ([OPERATIONS.md](OPERATIONS.md), "Upgrades").
- **Watchdog:** the hardware watchdog, plus `Restart=always` in systemd.
- **The USB stick** is mounted for the hub when plugged in (FAT or exFAT). Once set up, the hub copies only to the box's own stick ([RECOVERY.md](RECOVERY.md), C2, "Only the box's own stick").
- **No remote access.** A support VPN would need a server, and would add little, since the hub's data stays encrypted until a staff member logs in. Support works from central. A hub that does not come up writes what support needs to `hub-diag.txt` on its boot partition, readable on any computer.
- **A machine ID made at first boot**, so cloned images do not share one.
- **The hub's service is sandboxed**: its own user, no core dumps, and a read-only file system except its own folders, the USB stick and `/run/hub` ([SECURITY-DESIGN.md](SECURITY-DESIGN.md), "Other defences").

The image holds **no secrets**. Each hub gets its own data key, hub ID and signing key pair at first boot and set-up. Patient data on the disk is encrypted by the hub itself, so full-disk encryption is optional. What stays unencrypted on the disk, and what a TPM adds, is in [SECURITY-DESIGN.md](SECURITY-DESIGN.md), "Encryption at rest".

Local builds of the image have run on a Pi 5 and a Pi 4. The checks on a Pi after an image change (image/README.md) are open ([PENDING.md](PENDING.md), item 6).

## The hub on a Windows PC

For facilities without a Pi kit: the same hub on a PC they already have. That can be a records-room desktop or laptop staff use for their work, or a small server already there for another programme. The guide for the person at the PC is [WINDOWS-INSTALL.md](WINDOWS-INSTALL.md); the design is [design/2026-10-20-windows-service-design.md](design/2026-10-20-windows-service-design.md). So far it has run on Windows VMs only.

- **Choose a PC that stays on while staff work.** When it is off anyway, staff work online, from the apps' own offline cache, or on paper: degraded service, as for a Pi without power. Avoid a laptop that leaves the building, and prefer a PC not used for email and browsing: anyone with administrator rights on it can read the running hub's memory.
- **One signed installer for every country**, from GitHub with the release or carried on a USB stick. It installs for the whole PC, by an administrator: a service under its own virtual account, started at boot. The installer:
  - picks free ports (443 and 80, else 8443 and 8080);
  - creates the hub's certificate authority and adds it to the PC's trusted roots;
  - maps `dhis2.local` to the PC itself;
  - opens the firewall for the hub;
  - optionally keeps the PC awake on mains power and asks Microsoft Defender to skip the hub's data;
  - adds a desktop shortcut to `https://dhis2.local`.
- **Devices use `dhis2.local`, as on the Pi.** The hub answers it itself on the network, and stays silent if another device already does. The PC's name and IP are the fallback.
- **The facility needs a working router.** There is no "hub provides the network" mode on Windows (section 4, case C).
- **Set-up only on the PC itself:** being at the PC proves presence at the facility, so no setup code is needed.
- **Windows 11** is supported; Windows 10 is untested.
- **TPM:** the installer's last page and `install.log` say when the PC has no usable TPM.
- **After a restart**, including Windows Update, the first staff login through the hub unlocks it, offline too. Until then saves wait.
- **USB stick.** A staff member chooses the stick on the admin page. The hub finds it again by its label and serial number, whatever its drive letter, and never picks one by itself.
- **Upgrades** use the same releases and staging as the Pi.
- **Code signing.** No code-signing certificate exists yet, so releases carry no installer ([OPERATIONS.md](OPERATIONS.md), "Signing releases").

## The hub on a Mac (for the DHIS2 team)

Not for facilities. `macos/install.sh` runs the same hub on a Mac, so the core team can try it against a test DHIS2, with the same app, staging and upgrades: as a launchd agent for the logged-in user, answering `dhis2.local` itself, its authority trusted in the System keychain. See macos/README.md.

## 3. From depot to facility

### On central, before the first hub

1. The national team installs the Facility Hub app on central.
2. A superuser names, in the app's Settings, the hub owners group, the hub accounts group and its role, and the hub managers group ([OPERATIONS.md](OPERATIONS.md), "Protect the data store namespaces"). A hub refuses set-up, and take-over, until a hub owners group is named. Saving the groups also gives the hub owners and hub managers groups read access to the hub accounts group: a hub shares its registry entry with that group as whoever set it up, and DHIS2 silently drops a group the sender cannot see. A hub whose sharing lost a group says so on its page and in its heartbeat (`registryDropped`), and shares the entry again at each heartbeat until the group is kept.
3. The national team or the districts add the facility focal points, and whoever else may set a hub up or take one over, to the hub owners group.
4. Check the generated ID patterns of the programmes hubs will serve. Each hub keeps a pool of reserved IDs per generated attribute (500 by default, set per attribute in the app), and reserved IDs stay taken for 60 days. A short pattern has few values: `ORG_UNIT_CODE + "/" + RANDOM(###)` allows 1000 per facility, shared by the hub and every tablet there. Set such an attribute's pool well below that, about a week of the facility's registrations. When central has too few left, the hub takes what remains and says so on its page.
5. **Agree what staff details reach central.** Each hub writes to the `facility-hub-*` namespaces, for the Facility Hub app: each refused change with its record's ID and its author's DHIS2 username, and, while the overwrite count is on, the recent offline saves that replaced a newer change on central, with the record's ID, kind, programme, author and four times ([OPERATIONS.md](OPERATIONS.md), "Offline edits and newer changes on central"). No values. The app shows a user only the hubs under their org units, but the entries are not shared by org unit: whoever can read the namespaces reads every hub's. Before a national rollout, the country's data protection lead signs off on this, and on who may read the namespaces. The overwrite count can be turned off (`facility-hub-config/overwrites` = `{"off": true}`).
6. Then the facilities set their hubs up (below).

### At the depot (bulk)

1. Flash the supplier's image, unchanged, onto the SSDs, for example with a USB duplicator or `rpi-imager --cli` in a loop.
2. Assemble the kits and power each one on once on the depot network. This runs first boot, sets the clock, creates the hub ID and checks that it can reach central.
3. Print and stick the label: hub ID and `dhis2.local`. Do not print the setup code. At first boot the hub writes it to the USB stick in the box, in `facility-hub/hub.txt` (the line `Setup code:`, with the hub ID and the setup Wi-Fi's name and password). A disk flashed at a district therefore carries its code on the stick already in the kit, with no inventory record needed. Any stick plugged in while the hub is in set-up mode also gets it, in case the kit's stick is missing. Once set up, the hub removes the line, and a used code no longer works. The depot list of hub IDs becomes the inventory; it matches the `facility-hub-registry` namespace on central.
4. Pack it with a one-page installation card.

No facility is assigned at the depot. Any box can go anywhere.

Why a setup code: with a generic image, a fresh hub does not know central yet. Without the code, anyone on the facility Wi-Fi could open the set-up page first and point the hub at their own server. The code means only someone holding the box can set it up. The code in plain text next to the encrypted backup weakens nothing: it only allows set-up of an unconfigured disk. (A Windows hub needs none: its set-up opens only on the PC itself.)

### At the facility (about an hour, by facility staff)

No one from the district needs to come. Kits arrive by courier or with a routine district visit; Windows PCs get the hub from MoH IT beforehand.

**What the person setting up needs, besides the kit:** a device that can read the USB stick in the box: a laptop, or a phone or tablet with a USB adapter (USB-C or OTG). The setup code is only on the stick, and every Pi hub asks for it; Ethernet does not change that. Without Ethernet the same file also gives the setup Wi-Fi's name and password. A facility with neither a laptop nor an adapter needs one sent with the kit: the depot keeps no record of the codes.

1. Mount the hub near the router and plug in power and Ethernet, as on the installation card. The card covers the case with no router (section 4, case C).
2. On any laptop or phone on the facility network, open the address on the label (on a Windows PC, the hub's page opens on the PC itself, and set-up only opens there). Type central's address from the installation card, and enter the setup code from `facility-hub/hub.txt` on the USB stick. The hub accepts only `https` with a publicly trusted certificate, and shows the server's name before anyone logs in.
3. The facility focal point (assigned to the facility, and in the hub owners group) logs in with their own DHIS2 account. The hub then:
   - takes the facility from the user's org units, asking which one if they have several. Choose the facility itself, not a parent: generated values whose pattern has `ORG_UNIT_CODE` are made with the chosen unit's code, so a hub set up on a unit with children would hand the children its code offline;
   - reads the facility's programmes;
   - registers itself on central, offering to retire an old hub for the same facility ("Replacing or moving a hub" below);
   - asks for its own account ([OPERATIONS.md](OPERATIONS.md), "The hub's own account").

   No patient data is downloaded at this point, unless the focal point's own account can see some: every pull runs with one user's own token.
4. Every staff member logs in once through the hub while it is online. The hub then downloads what that person may see on central, with their access, and they can log in offline from then on. Data reaches the hub only through the people who use it. Still online, on the device they will use, each staff member then opens Capture (each of their programmes' working lists) and the Data Entry app (each of their data sets' forms) once. The browser keeps the apps, and the hub keeps each screen's settings and metadata for that person. At each online login, the hub also loads the screens anyone at the facility opened online in the last 14 days, for that person and with their access. A screen nobody has opened online through the hub is not available offline.
5. Point the client devices at the hub (section 5).
6. The offline check: unplug the uplink, register a test patient on one tablet, sync, and find them on another.

If something fails, the focal point calls the district, which sees on central (the Facility Hub app) whether the hub registered and what it last reported.

### Replacing or moving a hub

- **First find out what broke.** If the disk is fine and only the board (Pi) or PC failed, move the disk or the data folder (and the TPM board, where there is one) to the replacement: it starts as the same hub with all its unsent data, and needs no set-up. A dead disk is restored from the USB stick, losing at most a minute of saves ([RECOVERY.md](RECOVERY.md), C2). The new disk's setup page offers to restore the hub from the stick; a staff member of that hub confirms with their own password, and it restarts as the same hub, with the same ID. Facility staff only plug the stick in and keep it when a disk is replaced. Only a lost hub is replaced by a new one.
- **Replacement:** a spare arrives by courier, the same day if need be; a staff member assigned to the facility sets it up and confirms "replace the old hub". This is allowed at any time: patient data is not at stake, since a new hub gets data only through each user's own login, and the retired one stops serving data. Central records who replaced which hub and when, and the national team can undo a mistaken replacement in the Facility Hub app ([FACILITY-HUB-APP.md](FACILITY-HUB-APP.md)). If the old hub is still running and online, it stops serving patient data and only sends what it has not pushed, refused changes included, so plug it into any connection before collecting it. Anything a lost hub had not pushed is gone, apart from what its stick holds. A new hub without the old stick has a new certificate authority, and every device installs it.
- **Decommissioning:** use "Reset hub" on the admin page, open to staff assigned to the facility. It pushes the outbox, marks the hub's registry entry retired, deletes on central the tokens it holds, deletes its folder on its own USB stick, wipes the store and returns to setup mode. Any token it could not delete can still be revoked with "Revoke all tokens of this hub" in the Facility Hub app.
- **Theft,** and a staff member who takes the disk or stick: see [OPERATIONS.md](OPERATIONS.md), "A stolen hub, an insider, and accounts that manage users".

## 4. Network set-ups

Facilities differ. The image supports four cases without per-facility configuration files; the right mode is picked during set-up.

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
- **Both checks, from a tablet:** open `dhis2.local` from a tablet. The admin page then says "Devices reach this hub by dhis2.local", or that they reach it only by IP address, or that no other device has opened it yet, which suggests client isolation.

### Case B: a 4G MiFi or mobile router as the only network

These small devices often have no Ethernet port and frequently isolate clients.

- The hub joins the MiFi's Wi-Fi as a client, chosen on the setup Wi-Fi (below). Not yet tested on hardware.
- If the MiFi isolates clients, use case C instead and connect the MiFi to the hub as its uplink.

### Case C: no usable local network; the hub provides it

```
 tablets, laptops ──Wi-Fi──► hub (access point, DHCP, DNS) ──Ethernet / USB 4G──► internet
```

- The hub runs its own Wi-Fi access point, DHCP and DNS. Its uplink is the Ethernet port (to a modem or router) or a USB 4G modem of the one kind the hub supports (below).
- The Pi's built-in Wi-Fi handles a small facility, about 10–15 devices. Beyond that, add a cheap external access point on the hub's Ethernet.
- This is the most robust case, because the hub controls DHCP, DNS and client isolation. It is also the simplest to support remotely, because every facility in this mode looks the same.
- Chosen on the setup Wi-Fi (below): the setup network becomes the facility's network. It keeps the hub's name (`Hub-Zvc7xMnFcVg`), with a password the hub makes: 4 groups of 4 lowercase letters and digits without look-alikes, as `k7mp-x2qd-9wtr-b4nz`. The page that makes the Wi-Fi shows it, then the hub's admin page and `hub.txt` on the USB stick ("Facility Wi-Fi password:"). "New password" on the admin page starts the Wi-Fi again with a new one, and every device has to join again. A hub that joined another network and is then made the facility's Wi-Fi again uses the password it made before, so devices that knew it join again by themselves.

### Setup Wi-Fi: a hub with no screen or keyboard

A hub with no network cannot be configured over one, and has no screen. So it provides one for the purpose. Set-up through it has run on a Pi 4.

1. **When:** at boot, if the hub has no Ethernet link with DHCP and no saved Wi-Fi it can join, it starts its own setup Wi-Fi, 60 seconds after boot. It also starts 15 minutes after the network is lost, so a hub is never out of reach because a router changed its password. A cable or USB modem stops it.
2. **Its name and password:** "Hub-" and the hub ID (`Hub-Zvc7xMnFcVg`), with an 8-digit password the hub makes at its first boot, typed on a phone's number pad. Not one password for every kit. Both are on the USB stick in the box, in `facility-hub/hub.txt`, which the hub keeps current: the hub ID, the setup code until set-up, the setup Wi-Fi's name and password, the hub's address on the facility network, and in case C the facility Wi-Fi's name and password. The setup Wi-Fi only leads to the setup page, which needs the setup code or a staff login ([SECURITY-DESIGN.md](SECURITY-DESIGN.md), "Setting up a hub").
3. **What the admin does:** connect a phone to that Wi-Fi; the phone opens the hub's setup page by itself, as a hotel Wi-Fi does. The page offers:
   - **Join an existing Wi-Fi** (case B, or case A without a cable): pick a network from a scan, enter its password;
   - **Make this hub the facility's Wi-Fi** (case C).
   
   Phones open the page in a small sign-in window, which cannot download the hub's certificate: the page then says to open `http://10.42.0.1` in Safari or Chrome.
4. **Switching:** the Pi has one Wi-Fi radio. So the hub scans first, keeps the choice, turns its own network off and joins. If that fails within about a minute, the setup Wi-Fi comes back with what went wrong ("wrong password", "network not found"). Before switching, the page tells the admin to put the phone back on the facility's Wi-Fi, and shows the hub's address there.
5. **Afterwards:** while the setup Wi-Fi is on, the hub tries a Wi-Fi it joined before every 15 minutes, so a router that restarted is rejoined without anyone coming back. After set-up, changing the network needs a staff login.

The hub's IP changes when it moves from its own network to the facility's; devices use `dhis2.local`, so that does not matter. How this is built is in [ARCHITECTURE.md](ARCHITECTURE.md), "Network set-up on a Pi".

### USB modems: one kind only

USB 4G sticks come in three kinds:

1. **Router in a stick** (often sold as "HiLink" or Ethernet mode): the stick shows up as a network card and hands out an address by DHCP. The hub needs no settings; it is one more wired uplink. The SIM PIN and the operator's APN live in the stick's own web page, and most sticks find the APN themselves.
2. **Modems** (QMI, MBIM or serial): they need modem software on the hub, the APN, the PIN, and sometimes a mode switch, all varying by model and operator.
3. **Sticks that first show up as a CD drive**, and must be switched before they work.

The hub supports the first kind only. Generic support for the others is an endless list of models and operators that nobody on site could debug. In practice:

- One tested model of the first kind per country, in the kit, prepared at the depot: SIM PIN off, APN checked.
- The setup page and the admin page show whether the uplink works, with nothing to configure: a stick of this kind shows as "USB modem" (phone tethering too).
- Where that does not work out, a MiFi: the hub joins it over the setup Wi-Fi like any network, and it is often the simpler kit anyway.

Not yet tested on hardware.

### Case D: managed networks (hospital IT, NGO-managed sites)

- Static IP or reserved DHCP, a firewall exception for outbound HTTPS to central and to GitHub (releases), and inbound HTTPS from client subnets.
- Proxy servers: the admin page has a proxy field, used for every outbound connection; empty uses the standard `HTTPS_PROXY` environment variable.

### What the hub needs from any network

| Direction | What | Why |
|---|---|---|
| Hub → central | HTTPS (443) to the central DHIS2 URL | Sync, login, metadata |
| Hub → GitHub releases | HTTPS (443) to github.com and its download host | Self-upgrade |
| Hub → NTP | UDP 123 to the country's or a public NTP pool | Correct time |
| Clients → hub | HTTPS (443 or 8443) on the LAN | Capture apps |

All connections to central are outgoing. Nothing on the internet needs to reach the hub, so it works behind NAT and carrier-grade NAT on 4G. A captive portal or hotel login in front of the internet counts as central unreachable: staff keep working offline until someone logs the uplink in.

### Reaching the hub securely

The hardest part of deployment. Two requirements:

1. **Security.** Every connection to the hub is encrypted and authenticated, on the facility LAN too. No plain HTTP, and no certificate warnings that staff learn to click through: a warning clicked through stops a listener on the Wi-Fi, not someone actively impersonating the hub, and it teaches staff to ignore warnings everywhere. Browsers also refuse service workers, which offline apps need, on a certificate they do not trust.
2. **Simplicity for end users.** One address everywhere, which does not change when the hub's IP does, and no router configuration: small facilities have nobody who can set a DHCP reservation or a DNS server.

**Decision (19 October 2026): `https://dhis2.local`, with the hub's own certificate authority installed once on each device.** Browsers are the clients: the Capture web app and the other DHIS2 web apps, installed as apps from the hub's address where wanted. The Android Capture app is on hold for deployment, because it does not trust certificate authorities installed by the user. The cost is the installation on each device, about a minute with a guide, and hardest on personal phones. The options not chosen are in [design/deployment-options.md](design/deployment-options.md).

How it works for a deployer:

- **The name.** The hub announces `dhis2.local` on the LAN by mDNS, whatever its IP, so nothing needs a reservation. Tested on Android 14 and iOS. A second device called `dhis2` on the same network makes one of them `dhis2-2.local`, which the hub's certificate does not cover: use the IP there.
- **Where `.local` fails, the IP address.** Expected on some networks; how often is for the pilot to measure:
  - access points that filter multicast (common on mesh systems and some business access points);
  - wired and Wi-Fi on separate subnets or VLANs (mostly case D);
  - older Android versions (Android 14 is tested; versions before about 12 may not resolve `.local`, to check);
  - client isolation, which breaks the IP too and has to be fixed anyway (case A).

  Devices then use `https://<the hub's IP>`, with the same certificate. The IP is on the hub's admin page, in `hub.txt` on the USB stick, and in the Facility Hub app, so a district officer can read it out on the phone. Two costs: the IP can change, so such a facility needs a DHCP reservation on its router after all; and a browser keeps a site's offline data and installed app per address, so a device stays with one address, name or IP, and does not switch between them. A case C hub is its own network, so its address never changes.
- **The certificate authority** is limited to the hub's own names and private addresses, so installing it cannot expose any public site. It lasts ten years, so a device installs it once. It is backed up on the USB stick, so a disk restored from the stick keeps it and devices notice nothing. A new kit without the old stick has a new authority, and every device installs it again (about 10 to 30 per facility). The details and what a stolen key allows are in [SECURITY-DESIGN.md](SECURITY-DESIGN.md), "The hub's certificate authority".
- **The start page.** Staff only ever type `dhis2.local`. On plain HTTP the hub answers with a start page that moves to HTTPS at once if the device trusts the hub, and otherwise shows the steps for that device, the file (`http://dhis2.local/hub/ca.crt`) and its fingerprint. A cautious installer compares the fingerprint with the one in the Facility Hub app. HTTPS answers carry HSTS for seven days. After a hub is replaced without its stick, devices that remember HSTS cannot open `dhis2.local` until it lapses, and meanwhile reach the start page at `http://<the hub's IP>`. **(To check: whether browsers keep HSTS for `.local` names.)**
- **Installing, per platform** (measured on a Samsung tablet with Android 14 and on an iPhone):
  - *Android:* download the file, then Settings › Security and privacy › More security settings › Install from device storage › CA certificate; Android warns "Your privacy is at risk", then "Install anyway", the screen lock, and the file. Android requires a screen lock (PIN, pattern or password) for any installed certificate. Opening the file in Chrome only says it must be installed in Settings.
  - *iPhone and iPad:* open the file in Safari and allow the profile; Settings › General › VPN & Device Management › install; then Settings › General › About › Certificate Trust Settings › switch on full trust. Without the last step Safari keeps warning.
  - *Windows:* double-click the file, "Install Certificate", and pick "Trusted Root Certification Authorities" by hand. *macOS:* Keychain Access and "Always Trust". Firefox may need its own import; managed fleets push it with Group Policy, Intune or an MDM, which also covers managed tablets.
- **On the hub PC itself** (Windows), `https://localhost` needs no installation.

A run on a tablet against a set-up hub is still to do.

## 5. Pointing client devices at the hub

### New devices

- **Every device, once:** install the hub's certificate authority, guided by `http://dhis2.local` (section 4, "Reaching the hub securely"). Then open `https://dhis2.local` and log in; install the Capture web app from there as an app where wanted.
- **Android Capture app:** on hold for deployment (it does not trust the hub's authority).
- **With an MDM** (Android Enterprise, for example Intune, Headwind or Google's), push the hub URL with the device. The Capture app's managed-configuration support needs checking before relying on this **(to check)**.
- **Laptops and desktops:** a browser bookmark or a desktop shortcut to the hub's address. Installing the Capture web app as a PWA from the hub's address gives an app-like icon.

### Existing devices already pointing at central

A device keeps one server URL per account, so moving it to the hub is a one-time step per device:

1. **Before switching, sync everything to central** while online. Unsynced data on a device must not be stranded under the old server. Capture keeps one database per account and does not warn about unsynced data when staff log out or add an account; the hub's account then looks complete without those records, and staff may register the people again. Logging in to the old account again where central is reachable and syncing recovers them.
2. **Android:** log out and log in again with the hub's address. The Capture app supports multiple accounts, so the central account can stay for fallback, but staff should use only one. Users must not end up entering data in both.
3. **Browsers:** replace the central bookmark with the hub's.
4. **Training point:** "At this facility, always open DHIS2 through the hub address." Using central directly still works while online, but that data does not appear on the hub until the hub's next pull, so other users will not see it if the internet then drops.

### Devices that move between facilities

A hub only serves its own facility. A community health worker's tablet that visits several facilities should keep central as its server. This is a limitation of hub-as-server, which the long-term client fallback in section 7 removes.

### Ongoing

- Android syncs with the hub every 30 minutes or by hand (spec 5.1). For quick hand-over between a tablet and another user, staff press "Sync" after finishing a patient. This needs training.
- Web users see each other's work immediately, because every web request goes to the hub.

### Images and files

The hub keeps no images or files ([FINDINGS.md](FINDINGS.md), "Images and files"). Online they work through the hub as on central, up to central's size limit (`max.file_upload_size`, 10 MB by default). Offline:

- **Capture web** cannot add an image or file. It shows "Async field update failed" and saves the form without it. No image shows offline, not even one seen online.
- **The Android app** keeps an image or file added offline on the tablet. The record it belongs to syncs once the hub is online again, and until then other devices do not see that record.
- **Training point:** "Photos and documents need the internet. Add them when the hub is online, or add them later to the record."

Where a programme relies on photos or documents (for example a scanned birth certificate), tell the country before rollout.

## 6. Running the fleet

Once hubs are set up, the national team and the districts run them from central, in the Facility Hub app. The details are in [OPERATIONS.md](OPERATIONS.md). In short:

- **Monitoring.** Every hub sends an hourly heartbeat with its status and 90 days of history. The app flags hubs that went silent, hold old pending data, have refused changes, low disk or failed upgrades.
- **Refused changes.** A save central refuses stays on the hub and is reported to central without its values. Districts follow up single ones; the national team fixes central and resends a whole group, or discards it.
- **Upgrades.** Each country chooses the hub version and stages it: 5 pilot hubs, then 10 %, 50 % and 100 %. Hubs upgrade themselves and roll back on failure. Before a DHIS2 upgrade, the hub release that supports the new version goes out first.
- **Support** starts from central. On site, staff restart the hub, log in after a restart, or plug in a spare. There is no remote access.
- **Settings to get right:** central's time zone (the facilities' own, or the easternmost), and, where uplinks are slow, the programmes hubs answer from their own copy while online.
- **Security tasks:** protect the dataStore namespaces, approve each hub's own account, revoke a lost hub's tokens, and enforce central's password policy for hub users.

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
- [ ] Network case identified (A/B/C/D); client isolation checked
- [ ] Hub set up by a staff member assigned to the facility
- [ ] Every staff member logged in once through the hub (this is when their data downloads), and opened Capture's working lists and the Data Entry app's forms once online
- [ ] Every tablet and browser switched to the hub address (synced first)
- [ ] Offline test: uplink unplugged, a registration on one device seen on another
- [ ] Heartbeat visible in the Facility Hub app on central

### One org unit per hub

A hub serves one org unit: the facility. Records owned by a community unit or ward below it pass through to central online and are not held offline, and users assigned only to such a unit cannot log in through the hub. Countries that put wards or departments below the facility org unit are not supported today; what supporting them would take is in [design/deployment-options.md](design/deployment-options.md).

### Longer term

The client-side fallback in the shared app-runtime would remove most of section 5. Devices would keep central as their server and use the facility's hub automatically when central is unreachable (spec 5.1 and open question in spec 14). Discovery could use the hub's address and key, published in the registry for the user's facility. With that in place, tablets that move between facilities would also work.
