# The hub on a Windows PC (design)

Status: from the conversation of 2026-10-19/20. The Pi image work (`docs/superpowers/specs/2026-10-19-pi-image-design.md`) is done; this is the next deployment target. No Windows machine is available yet: everything is built and tested as far as Linux allows (cross-compiled, unit-tested, packaged and signed here), and run on Windows when the user has a VM.

## Intent

Facilities that should have a hub but get no Pi kit run the same hub on a Windows PC they already have:
- a desktop or laptop staff use for their normal work, in the records room for example;
- or a small server that is already there for another programme, such as OpenMRS for ART.

Same hub, same store, same app and releases; only the packaging, the service around it and a few platform details differ. DEPLOYMENT.md, "Options under consideration", lists what was known before this design.

**Expectations.**
- The placement guidance says to pick a PC that stays on while staff work.
- When it is off anyway (shut down, asleep, a laptop taken home), staff fall back to working online, the apps' own offline cache, or paper. That is degraded service, as for a Pi without power, not a failure to design against.

**Who installs it.** Often not IT staff: facility staff, or a district focal point on a visit. The installer arrives downloaded on the PC or on a USB stick.

## Goals

1. **One signed installer for every country**, self-contained (nothing downloaded while installing), the same file whether downloaded or carried on a stick.
2. **Two installs, both supported:**
   - **With administrator rights (the main case).** A Windows service under its own restricted account, started at boot whether or not anyone is logged in. The installer also sets up:
     - the firewall rules;
     - a desktop shortcut;
     - an antivirus exclusion where it can;
     - an offer to stop the PC sleeping.
   - **Without administrator rights.** Installed for one Windows user, started when that user logs in, stopped when they log out. No firewall rules: Windows may then ask the user to allow it, or devices cannot reach it (the hub's page says which). For locked-down PCs where nobody on site has administrator rights.
3. **Devices use `dhis2.local`, as on the Pi.**
   - Windows only announces the PC's own name, so the hub runs a small mDNS responder of its own that answers `dhis2.local` (and only that) with the PC's private IPv4 addresses.
   - It shares UDP 5353 with Windows' own mDNS and with Bonjour, if installed.
   - Fallbacks where lookups are blocked: the PC's name (`<pc>.local`) and its IP.
   - The certificate covers `dhis2.local`, the PC's name and its addresses (`internal/hubtls`, unchanged).
4. **The hub's page on the PC itself** (`https://localhost`, the desktop shortcut) shows, without a login, the address devices use: `dhis2.local`, and the IP as fallback, with the port when it is not the default.
5. **Set-up only on the PC itself.** The hub's set-up page answers only requests from the PC (loopback): being at the PC proves presence at the facility, so no setup code is needed. Otherwise as on the Pi: central's address from the installation card, then a staff member assigned to the facility logs in.
6. **Ports.** 443 and 80 when free; otherwise 8443 and 8080. On a shared server something else may hold 80 or 443 (a web server in front of OpenMRS, for example). The chosen ports are kept, so the address does not change between restarts.
7. **Restarts.**
   - After any restart, including Windows Update, the hub is locked until the first staff login through it. Offline too: logins check the user's key slot on the hub.
   - Until then queued saves are not sent and nothing is pulled; that is accepted (user, 2026-10-20). No key held by Windows to unlock at boot.
8. **USB stick copy, if a stick is plugged in.**
   - The hub keeps its copy (RECOVERY.md, C2) on a USB stick when one is present, as on the Pi; without one it relies on the PC's disk.
   - Drive letters change, so the hub recognises its stick by its own folder on it (`facility-hub\<hub ID>`). When it has none yet and several removable drives are plugged in, a staff member picks one on the admin page.
   - The page and the heartbeat show whether a copy is kept, as now.
9. **Upgrades from GitHub releases, staged from the app**, as on the Pi (`facility-hub-admin/release`), with the Windows binary in each release and the installer as a release asset.
10. **Uninstall from Windows' "Apps" list.** It stops the service; the hub first pushes waiting saves if it is unlocked and online. The data folder stays unless the person ticks "remove data" (a reinstall then finds the hub again).

## Non-goals

- No hub-provided network (case C), no setup Wi-Fi, no `nmcli`: the PC's network is the facility's.
- No unlock at boot by a Windows-held key (DPAPI or TPM), decided above. The upgrade handover below is not this: it only bridges one planned restart.
- No support VPN or remote control.
- No several PCs sharing one hub (DEPLOYMENT.md explains why).
- No Windows before 10 or 32-bit Windows. `windows/amd64` only; `windows/arm64` can be added later.

## Anti-goals (a failure even if it works)

- **Disturbing other software on the PC:** taking a port another program uses, or heavy disk or memory use on a shared server. The hub uses ports 8443 and 8080 rather than fighting for 443 and 80, and keeps its memory limit (`setMemoryLimit`) as on the Pi.
- **Patient data readable by the PC's other users:** the data folder is the service account's alone; the store is encrypted as everywhere.
- **An installer that SmartScreen or antivirus blocks or warns about.** It is Authenticode-signed (DEPLOYMENT.md, "Signing").
- **An upgrade that leaves the hub locked until someone logs in.** It must hand the data key to the new version, as on the Pi (below).

## Decided in the conversation

- The host PCs and the expectation when they are off (above).
- Both installs, with and without administrator rights.
- `dhis2.local` on Windows too, with the IP shown on the PC.
- Unlocking after a restart by the first normal login, nothing more.
- The USB stick copy when a stick is present, chosen on the admin page if several are present.
- Testing: as much as possible without Windows now; the user sets up a Windows VM later.

## The builder's calls (made here, open to correction)

Settled after the design review on 2026-10-20 while the user was away, to be checked:

1. **Where `hub.exe` runs: the data folder.**
   - The installer puts it at `ProgramData\DHIS2 Facility Hub\bin\hub.exe` (per-user: `%LOCALAPPDATA%\DHIS2 Facility Hub\bin\hub.exe`), a folder only the service account and administrators may write. Self-upgrade then replaces it without opening Program Files to the service account.
   - Program Files keeps only the uninstaller.
   - The installer refuses to put back an older `hub.exe` than the one there (it asks `hub.exe version`), so re-running an old installer never downgrades a migrated store.
   - Windows' Apps list shows the version installed, not the one the hub upgraded itself to; the hub's page shows the running one.
2. **Signing: no certificate exists yet.**
   - Everything is signed with a self-signed test certificate (`osslsigncode`) here and in CI.
   - The CI step is one script, so it can move to a cloud signing service (Azure Trusted Signing or DigiCert KeyLocker, with `jsign`) when the supplier has a certificate. DEPLOYMENT.md, "Signing", is unchanged.
3. **Trust and the address on the PC itself.**
   - The installer runs `hub.exe init`, which makes the hub's certificate authority (as the first start would) and its settings.
   - The admin installer then:
     - adds the authority to the PC's trusted roots (`certutil -addstore Root`);
     - maps `dhis2.local` to `127.0.0.1` in the hosts file;
     - creates a desktop shortcut to `https://dhis2.local`.
   - So staff on the PC use the same address as every other device, and browsers keep one set of offline data.
   - The per-user installer adds the authority to the user's own store (Windows asks once), and its shortcut also opens `https://dhis2.local`, found through the hub's own responder.
   - `https://localhost` keeps working, for the set-up page.
   - Set-up accepts requests from the PC itself: loopback, or one of the PC's own addresses.
4. **The USB stick is never adopted without a person.** Even with one removable drive plugged in, a staff member confirms it on the admin page ("Keep the hub's copy on drive E: (KINGSTON, 15 GB)"). The folder layout is the Pi's, `facility-hub\<hub ID>\`, so a copy from either can restore the other's disk.
5. **From a per-user install to an admin install.** When the admin installer finds the installing user's per-user hub, it offers to move it:
   1. stop it;
   2. move its data folder to `ProgramData`;
   3. set the access list;
   4. remove the per-user start entry.

   The hub keeps its ID, approval and authority, so devices notice nothing.
6. **Conflicts.**
   - **The name:** at start and every few minutes, the responder asks whether anyone else answers `dhis2.local` with an address that is not the PC's. If one does, it stays silent, and the hub's page says so ("Another device on this network answers dhis2.local: devices use \\<pc>.local or the IP").
   - **Ports:** 443 and 80, else 8443 and 8080, else any free pair the system gives. Chosen once by `hub.exe init` and kept in `hub.yaml`.

- **Installer: NSIS, built on Linux with `makensis`** (`wixl` cannot run the custom steps needed: the service, the firewall, the hosts file, the authority, the power plan). The single installer chooses an admin or a per-user install (NSIS MultiUser). `/S` installs silently, for CI and ministry software deployment.
- *(Superseded: an MSI built with `wixl`.)*
  - The admin install puts the hub in `C:\Program Files\DHIS2 Facility Hub\` and its data in `C:\ProgramData\DHIS2 Facility Hub\`, which it creates with an access list for the service account only.
  - The per-user install puts both under `%LOCALAPPDATA%\DHIS2 Facility Hub\`.
  - If `wixl` cannot express something needed (per-user and per-machine from one package, custom actions), an NSIS installer (`makensis`) is the fallback.
  - Signed with `osslsigncode`, as is `hub.exe` (key from CI secrets; a self-signed test certificate here).
- **The service account: a virtual service account** (`NT SERVICE\DHIS2FacilityHub`). No password to manage; network access as the PC.
- **The service runs the same `hub.exe`** with `hub service` (golang.org/x/sys/windows/svc): it answers stop and shutdown from the Service Control Manager with the same clean stop as SIGTERM on Linux. Recovery actions restart it after a crash. The per-user install starts `hub serve` from the user's Startup entry, with no console window.
- **Upgrade on Windows.** A running `.exe` can be renamed but not overwritten, and a service cannot re-exec itself in place. So the hub:
  1. renames `hub.exe` to `hub.exe.old`;
  2. puts the new one in its place;
  3. seals the data key with DPAPI (scoped to the service account, or the user) into `upgrade-key` in the data folder, deleted on first read and refused after 2 minutes;
  4. exits with a code the recovery action restarts on.

  The new version reads the key, deletes the file, runs its health check, and on failure puts `hub.exe.old` back and exits again. Without the handover (the file missing or too old), the hub starts locked: an upgrade never loses data, it only waits for a login. The per-user install does the same, starting the new process itself before exiting.
- **Free disk space** through `GetDiskFreeSpaceExW` (`internal/sync`, `internal/stick`); the stick's "is it really mounted" check becomes "is it a removable drive" (`GetDriveTypeW`).
- **Paths and settings.** `hub.yaml` sits next to the data, written by the installer with the ports chosen and `data_dir`; nothing country-specific.
- **Antivirus:** the admin installer adds a Microsoft Defender exclusion for the data folder (`Add-MpPreference`, best effort; other antivirus is listed in the guide).
- **Sleep:** the installer's checkbox sets the power plan's sleep timeout on AC to never (`powercfg`).
- **Firewall:** inbound TCP for the chosen ports and UDP 5353 for the hub's program, all profiles. The hub's page warns when the network is marked Public, as Windows may still block it there.

## Testing

- **Cross-compile** `windows/amd64` in CI and here; `go vet` for Windows.
- **Unit tests for the Windows-only parts:**
  - the parts that can run anywhere, behind small interfaces: the upgrade sequence with a fake file system and a fake DPAPI, port choice, picking the stick, the mDNS answers;
  - the Windows-only code is compiled here, run in the VM later.
- **The mDNS responder:**
  - its packet handling is tested on Linux against real DNS messages;
  - on a Linux host with `avahi` also running on port 5353, a test checks that both answer;
  - on Windows, the same test later.
- **The installer:**
  - built and signed here;
  - `msiinfo` checks its tables: files, the service, its account, firewall rules, shortcuts, per-user and per-machine;
  - `osslsigncode verify` checks the signatures.
- **A Windows CI job** (`windows-latest`) that runs `go test ./...` and installs the MSI silently, then checks that the service runs and answers on the chosen port. Written now, run when the user pushes; the VM repeats it by hand.
- **Listed for the VM:**
  - install with and without administrator rights;
  - reboot and log in;
  - an upgrade with the key handover;
  - a USB stick;
  - `dhis2.local` from a tablet;
  - Defender, sleep and the firewall on a Public network;
  - uninstall.

## Docs

- DEPLOYMENT.md: the Windows option becomes built (with what is unverified until the VM).
- README: building the installer.
- TESTING.md: the Windows checks.
- A one-page install guide for the person at the PC (docs/WINDOWS-INSTALL.md).
