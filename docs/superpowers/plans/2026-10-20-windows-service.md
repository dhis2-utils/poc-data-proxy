# The hub on a Windows PC: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the same hub as a Windows service (or a per-user program) on a facility PC, with a signed self-contained installer, `dhis2.local` from its own mDNS responder, the USB stick copy, and self-upgrade.

**Architecture:**
- **Windows-only code** sits in `_windows.go` files behind small interfaces. The logic around it is plain Go, tested on Linux.
- **A new `internal/mdns`** answers `dhis2.local`.
- **`hub init`** prepares a data folder: ports, `hub.yaml` and the certificate authority.
- **`hub service`** runs and installs the Windows service.
- **The installer:** an NSIS script built and signed on Linux.

**Tech Stack:** Go (`golang.org/x/sys/windows`, `windows/svc`, `windows/svc/mgr`), NSIS 3 (`makensis`), `osslsigncode`, GitHub Actions `windows-latest`.

**Spec:** `docs/superpowers/specs/2026-10-20-windows-service-design.md` (including "The builder's calls").

## Global Constraints

- `windows/amd64` only. Every package must build and vet with `GOOS=windows GOARCH=amd64`.
- **Folders:**
  - admin install: data in `C:\ProgramData\DHIS2 Facility Hub\`, binary in `...\bin\hub.exe`;
  - per-user install: `%LOCALAPPDATA%\DHIS2 Facility Hub\`.
- **Names:**
  - service `DHIS2FacilityHub`, running as the virtual account `NT SERVICE\DHIS2FacilityHub`;
  - mDNS name `dhis2.local` on UDP 5353, private IPv4 addresses only.
- **Ports:** 443/80, else 8443/8080, else a free pair; chosen once by `hub init`.
- **No unlock at boot by a Windows-held key.** DPAPI is used only for the upgrade handover: a file read once and refused after 2 minutes.
- **Signing:** self-signed test certificate (`osslsigncode`); no real certificate exists yet.
- **Repo rules** (AGENTS.md):
  - `go vet ./...` and `go test ./...`;
  - `go test -race` on `internal/e2e`, `internal/tracker`, `internal/store`;
  - commits without a `Claude-Session` trailer;
  - tests run under `env -u GITHUB_TOKEN`.
- **Go toolchain:** `export PATH=$HOME/sdk/go/bin:$PATH GOPROXY=direct GOSUMDB=off`. Work in `~/work/hub` and push the branch to the shared repo.

## Review Focus

1. **The upgrade handover file left behind or reused:** it must be deleted on first read, refused after 2 minutes, and never readable by another account. Pinned by `TestHandoverOnceAndFresh` (Task 1).
2. **A port pair where only one port is free** (443 taken, 80 free): both move to the fallback together, so the address has one port. Pinned by `TestPortsMoveTogether` (Task 3).
3. **An mDNS query for another name, or a malformed packet:** no answer, no crash. Pinned by `TestResponderIgnoresOthers` and `TestResponderMalformed` (Task 2).
4. **Set-up opened from another device on the LAN when the hub runs on a PC:** refused. Pinned by `TestSetupOnlyOnThePC` (Task 4).
5. **Two removable drives and none chosen:** no copy kept, and the page asks which. A drive that later disappears is reported missing, not silently replaced. Pinned by `TestStickChosenNotGuessed` (Task 6).

---

### Task 1: Build for Windows: disk space, the stick check, the upgrade swap and handover

**Files:**
- Create: `internal/sync/disk_unix.go`, `internal/sync/disk_windows.go`, `internal/stick/os_unix.go`, `internal/stick/os_windows.go`, `internal/upgrade/reexec_unix.go`, `internal/upgrade/reexec_windows.go`, `internal/upgrade/handover.go`, `internal/upgrade/handover_test.go`, `internal/upgrade/dpapi_windows.go`.
- Modify: `internal/sync/sync.go`, `internal/stick/stick.go`, `internal/upgrade/upgrade.go`, `cmd/hub/main.go`.

**Interfaces:**
- Produces:
  - `func diskFree(dir string) uint64`, per OS;
  - `func mountPoint(dir string) bool`, per OS (Windows: the path's volume is a removable drive);
  - `upgrade.WriteHandover(dir string, dk []byte, now time.Time, seal func([]byte) ([]byte, error)) error`;
  - `upgrade.ReadHandover(dir string, now time.Time, open func([]byte) ([]byte, error)) []byte`. It deletes the file and returns nil when the file is older than 2 minutes or doesn't open.
  - `Reexec(exe string, dk []byte) error` and `HandedOverDK() []byte`, per OS. Windows uses `upgrade.HandoverDir`, set by main, and DPAPI. It then exits with code 3 under the service manager (its recovery action restarts the service), or starts `exe` with the same arguments and exits when not a service.
  - `replaceBinary(src, dst string) error`, per OS. Windows renames `dst` to `dst.old`, then copies.

- [ ] **Step 1:** In `handover_test.go`, write `TestHandoverOnceAndFresh` with a fake seal (XOR) and a fake clock:
  - a key written and read within 2 minutes comes back, and the file is gone;
  - a second read gives nil;
  - a key read after 2 minutes gives nil, and the file is gone;
  - the file is mode 0600 on Unix.
- [ ] **Step 2:** Run `go test ./internal/upgrade -run Handover`. It fails (undefined).
- [ ] **Step 3:** Implement `handover.go`: `upgrade-key` holds JSON `{at, sealed}`. Split the per-OS files as above. The Unix files keep today's code unchanged.
- [ ] **Step 4:** Verify `go test ./internal/upgrade`, then `GOOS=windows GOARCH=amd64 go vet ./...` (clean), and `go vet ./... && env -u GITHUB_TOKEN go test ./...`.
- [ ] **Step 5:** Commit: "Windows: the hub builds; disk space, the stick check, the upgrade swap and key handover".

### Task 2: `internal/mdns`: answering `dhis2.local`

**Files:** create `internal/mdns/mdns.go` and `internal/mdns/mdns_test.go`. Modify `internal/config/config.go` (`mdns: true` turns the responder on; default off; the Pi has avahi) and `internal/app/app.go` (start it).

**Interfaces:**
- Produces:
  - `type Responder struct { Name string; Addrs func() []net.IP; Conn PacketConn }`;
  - `func (r *Responder) Answer(query []byte) []byte`, which returns nil when there's nothing to answer;
  - `func (r *Responder) Run(ctx)`, which listens on the multicast group 224.0.0.251:5353 with the address shared;
  - `func Conflict(ctx, ask func(q []byte) [][]byte, name string, own []net.IP) bool`;
  - `(*Responder).Silenced() bool`, for the hub's page.

- [ ] **Step 1: Tests**, with real DNS wire bytes built in the test:
  - `TestAnswersItsName`: an A query for `dhis2.local` (any letter case, QU bit or not) gets A records with the addresses, TTL 120 and the cache-flush bit;
  - `TestResponderIgnoresOthers`: queries for `other.local` or for AAAA only get no answer;
  - `TestResponderMalformed`: truncated packets, compression loops and a header claiming 1000 questions give no answer and don't panic;
  - `TestConflict`: an answer with another address means a conflict; an answer with one of our own does not;
  - `TestRunOverUDP`: the responder bound to `127.0.0.1:0` answers a unicast query sent to it.
- [ ] **Step 2:** Run the tests and watch them fail.
- [ ] **Step 3:** Implement. Name decoding follows compression pointers, capped at 10 jumps. Only private IPv4 addresses are answered. While `Silenced`, nothing is answered. `Run` checks for a conflict at start and every 5 minutes.
- [ ] **Step 4:** `go test ./internal/mdns` and `GOOS=windows go vet ./internal/mdns`.
- [ ] **Step 5:** Commit: "mdns: answer dhis2.local where no avahi does (Windows)".

### Task 3: `hub init`: ports, `hub.yaml`, the authority

**Files:** create `cmd/hub/init.go`, `internal/config/ports.go` and `internal/config/ports_test.go`. Modify `cmd/hub/main.go` (the `init` command) and `README.md`.

**Interfaces:**
- Produces:
  - `config.ChoosePorts(free func(port int) bool) (https, http int)`: 443/80, else 8443/8080, else a free pair from the system;
  - `hub init -data DIR [-per-user]`, which:
    - writes `DIR\hub.yaml` (`listen`, `http_listen`, `data_dir`, `usb_dir: auto`, `mdns: true`, `setup_local_only: true`) unless the file exists;
    - runs `hubtls.Load(DIR, nil)`;
    - prints the authority's path, for the installer.

- [ ] **Step 1: Tests:**
  - `TestPortsMoveTogether`: 443 taken and 80 free gives 8443/8080; all four taken gives a pair from `free`;
  - `TestInitKeepsExisting`: a second `init` doesn't change `hub.yaml` or the authority;
  - `TestInitWritesNothingCountrySpecific`: no `central_url`.
- [ ] **Steps 2–4:** RED, implement, GREEN.
- [ ] **Step 5:** Commit: "hub init: a data folder for the Windows installer".

### Task 4: Set-up and the address, on the PC itself

**Files:** modify `internal/config/config.go` (`setup_local_only`), `internal/ui/setup.go`, `internal/ui/ui.go`, `internal/ui/templates/login.html` and `internal/ui/templates/setup.html`. Test: `internal/e2e/windows_setup_test.go`.

**Interfaces:**
- Produces:
  - with `setup_local_only`, `/hub/setup` answers only requests from loopback or the PC's own addresses; others get 403 with "Set up this hub on the PC it runs on";
  - the login page shows the addresses devices use (`https://dhis2.local[:port]`, `https://<ip>[:port]`) when the request comes from the PC itself.
- [ ] **Step 1: Tests:**
  - `TestSetupOnlyOnThePC`: from `192.168.1.50` → 403; from `127.0.0.1` → the form;
  - `TestLoginShowsAddressOnThePC`.
- [ ] **Steps 2–4:** RED, implement, GREEN.
- [ ] **Step 5:** Commit: "Windows: set-up on the PC itself; the address shown there".

### Task 5: `hub service`: the Windows service

**Files:** create `cmd/hub/service_windows.go`, `cmd/hub/service_other.go` and `cmd/hub/service_test.go` (argument handling only).

**Interfaces:**
- Produces:
  - `hub service run -config FILE`: the hub under the service manager, where stop and shutdown work like SIGTERM;
  - `hub service install -config FILE -exe PATH`: creates `DHIS2FacilityHub` (automatic start, delayed), with:
    - the account `NT SERVICE\DHIS2FacilityHub`;
    - recovery that restarts after 5 s, three times, with non-crash failures counted;
    - start;
  - `hub service uninstall`: stop, then delete;
  - on other systems, `hub service` prints "Windows only" and exits with 2.
- [ ] **Step 1:** A test of the argument parsing, which is plain Go. Then implement with `windows/svc` and `windows/svc/mgr`.
- [ ] **Step 2:** `GOOS=windows go vet ./cmd/hub` and `go test ./cmd/hub`.
- [ ] **Step 3: Notes for the VM:** the service account's rights on the data folder are set by the installer (Task 7), not here.
- [ ] **Step 4:** Commit: "hub service: run and install the Windows service".

### Task 6: The USB stick on Windows: chosen, never guessed

**Files:** create `internal/stick/drives.go` (choice logic), `internal/stick/drives_windows.go` (enumeration: `GetLogicalDrives`, `GetDriveTypeW`, `GetVolumeInformationW`, `GetDiskFreeSpaceExW`), `internal/stick/drives_other.go` and `internal/stick/drives_test.go`. Modify `internal/stick/stick.go` (`usb_dir: auto`), `internal/ui/ui.go` and `internal/ui/templates/admin.html`.

**Interfaces:**
- Produces:
  - `type Drive struct { Root, Label string; Size uint64 }`;
  - `func Removable() []Drive`, per OS;
  - `func Locate(drives []Drive, hubID, chosenLabel string, has func(root string) bool) (root string, ask []Drive)`:
    - a drive holding `facility-hub\<hubID>` wins;
    - else the chosen label;
    - else no root, and the drives to ask about;
  - meta `usb_choice` (the volume's label and serial);
  - `POST /hub/ui/usb` (`root`): writes the hub's folder there, and records the choice.
- [ ] **Step 1: Tests:**
  - `TestStickChosenNotGuessed`: two drives and no choice: no root, ask both. One drive and no choice: no root, ask it. The chosen one present: its root. The chosen one gone: no root, reported missing;
  - `TestStickFoundByFolder`: the drive letter changed, and the folder finds it.
- [ ] **Steps 2–4:** RED, implement, GREEN. In `auto` mode the stick's status gets `Ask []Drive`, and the admin page shows "Keep the hub's copy on drive E: (KINGSTON, 15 GB)".
- [ ] **Step 5:** Commit: "USB stick on Windows: found by its folder, chosen by a person".

### Task 7: The installer

**Files:** create `windows/installer.nsi`, `windows/build.sh` (cross-compiles `hub.exe`, signs it, runs `makensis`, signs the installer), `windows/sign.sh` (`osslsigncode` with a key and certificate from the environment, or a self-signed test pair it makes), `windows/check.sh` and `windows/README.md`.

**Interfaces:**
- Consumes: `hub init` (Task 3) and `hub service install/uninstall` (Task 5).
- Produces: `windows/build.sh <version> <out>` → `out/dhis2-hub-<version>-setup.exe`. The installer does the following.

  **Admin install** (MultiUser, chosen at start; `/S /AllUsers` for silent):
  1. copy `hub.exe` to `ProgramData\DHIS2 Facility Hub\bin`, refusing an older version than the one there;
  2. `icacls` on the data folder: the service account and Administrators only;
  3. `hub init`;
  4. `certutil -addstore Root <ca.crt>`;
  5. a hosts entry `127.0.0.1 dhis2.local` (marked);
  6. `netsh advfirewall firewall add rule` for the two TCP ports and UDP 5353, program-bound;
  7. a Defender exclusion (best effort);
  8. sleep on AC to never, if the checkbox is ticked (default on);
  9. `hub service install`;
  10. a desktop shortcut to `https://dhis2.local[:port]`;
  11. an uninstall entry in Apps.

  If the installing user has a per-user hub, the installer offers to move it.

  **Per-user install** (`/S /CurrentUser`):
  - files under `%LOCALAPPDATA%`;
  - `hub init -per-user`;
  - `certutil -user -addstore Root`;
  - a Startup entry (`hub.exe serve -config …`, no window: `-windowsgui` build? no, a hidden start through the uninstaller-free `wscript` stub);
  - the shortcut and the uninstall entry.

  **Uninstall:** `hub service uninstall`; removes the firewall rules, the hosts line and the authority; keeps the data unless "remove data" is ticked.

- [ ] **Step 1: `windows/check.sh <setup.exe>` first.** It checks:
  - `osslsigncode verify` (test certificate accepted with `-CAfile`);
  - `7z l` lists `hub.exe`;
  - the script's strings hold the service name, the firewall rule names and `dhis2.local`.

  Run it against nothing (fails).
- [ ] **Step 2:** Write the script and build it. `check.sh` passes. Install `p7zip-full` if needed.
- [ ] **Step 3:** Commit: "Windows installer: NSIS, built and signed on Linux".

### Task 8: CI and releases

**Files:** create `.github/workflows/windows.yml`. Modify `.github/workflows/release.yml` and `README.md`.

- [ ] **Step 1: `windows.yml`** (push to branches, and by hand), on `windows-latest`:
  - `go test ./internal/...`, without `internal/live`;
  - build `windows/build.sh`'s inputs: either build on `ubuntu-latest` and pass them as an artifact, or `makensis` through Chocolatey;
  - install silently with `/S /AllUsers`;
  - check that the service `DHIS2FacilityHub` is running;
  - check that `https://localhost:<port>/hub/status` answers, with the authority from the data folder;
  - uninstall, and check the service is gone.
- [ ] **Step 2: `release.yml`:** adds `windows/amd64` to the binaries and runs `windows/build.sh`. `hub-windows-amd64.exe` and the setup file become release assets.
- [ ] **Step 3:** Run the Linux steps by hand: cross-compile, `windows/build.sh`, `check.sh`, `hub release` with the Windows binary. Parse the YAML.
- [ ] **Step 4:** Commit: "CI: Windows tests and installer; Windows in releases".

### Task 9: Docs

- [ ] Write the following:
  - **DEPLOYMENT.md:** the Windows option becomes built (with what waits for the VM), and the earlier description moves in under "Built" with the decisions from the spec;
  - **docs/WINDOWS-INSTALL.md:** one page for the person at the PC;
  - **TESTING.md:** the Windows checks, and a VM checklist;
  - **README.md:** building the installer.
- [ ] Run the full verification:
  - `go vet ./...` and `GOOS=windows go vet ./...`;
  - `env -u GITHUB_TOKEN go test ./...`;
  - the race runs;
  - the app's `pnpm test`;
  - `windows/check.sh`.
- [ ] Commit, then push.
