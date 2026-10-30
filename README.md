# Facility hub (proof of concept)

> ![Maturity: Experimental](https://img.shields.io/badge/maturity-Experimental-orange)  
> Intended use: PoC hub that temporarily forwards/stores data of all users in one facility while offline, allowing share of data locally.  
> Maintainers: HISP Centre implementation team.


A small device on a facility LAN that caches the facility's DHIS2 tracker data and relays tracker writes to the central DHIS2 server. When central is unreachable, the DHIS2 Capture web app and the Android Capture app keep working against the hub, and each user still sees only what central lets them see. Target DHIS2 versions: **2.42 and 2.43** (verified against 2.42.6 and 2.43.2; differences in `internal/compat`, one file per version).

## Documents

| Document | What it covers |
|---|---|
| [SPEC-facility-hub-poc.md](docs/SPEC-facility-hub-poc.md) | The original specification, as it was asked |
| [FINDINGS.md](docs/FINDINGS.md) | What we learned about DHIS2, the Capture apps and the spec: answers to its open questions, where it was wrong, what the tests found |
| [TESTING.md](docs/TESTING.md) | How the hub is tested: profiles, the live suite against real DHIS2, the differential oracles, phases |
| [PERFORMANCE.md](docs/PERFORMANCE.md) | How the hub scales: current results on Pi-class profiles, load on central, sizing, history |
| [HARDWARE-TESTING.md](docs/HARDWARE-TESTING.md) | Testing on real Raspberry Pis (Pi 4 1 GB, Pi 5 4 GB, SD card and SSD): set-up, calibration, ladder, full downloads, bytes written, power cuts |
| [ADVERSARIAL-TESTING.md](docs/ADVERSARIAL-TESTING.md) | Worst cases run against a real DHIS2 (central, network, power, users changing under offline work): the plan and its results |
| [INCREMENTAL-SYNC-FIX.md](docs/INCREMENTAL-SYNC-FIX.md) | Incremental syncs under load: the measured problem and its fix (built 23 Oct 2026) |
| [DEPLOYMENT.md](docs/DEPLOYMENT.md) | Rolling out hubs: hardware, image, setup at the facility, networks, reaching the hub securely, running the fleet; options under consideration |
| [FACILITY-HUB-APP.md](docs/FACILITY-HUB-APP.md) | The Facility Hub app on central: its screens, and what it must match on the hub's side (approving a hub's own account, move codes) |
| [RECOVERY.md](docs/RECOVERY.md) | Keeping unsynced data safe: backup and recovery options (ideas, not decisions) |
| [docs/reviews/](docs/reviews/README.md) | Dated independent reviews (security, confidentiality, data loss, KISS), and the names the code cites them by |
| [AGENTS.md](AGENTS.md) | How to change the code: keep the structure, guards, checks before committing |

## Layout

| Path | What it is |
|---|---|
| `cmd/hub` | The binary: `serve`, `migrate`, `keygen`, `release`, `init` (a data folder for the Windows installer), `service` (the Windows service), `version` |
| `apps/facility-hub` | The Facility Hub app, a DHIS2 app on central: hubs and their flags, approvals, central moves (docs/FACILITY-HUB-APP.md) |
| `cmd/fakecentral` | A stand-in DHIS2, with generated data at any size, for the upgrade script and the performance runs |
| `cmd/hubtest` | Live tests against real DHIS2 instances: provisions a profile and runs the live suite (docs/TESTING.md) |
| `cmd/hubload` | Load generator: web and Android users against a hub (docs/PERFORMANCE.md) |
| `internal/app` | Wiring: store, handlers, sync worker, upgrader |
| `internal/core` | The hub's shared state: runtime, central time zone, sessions, applying central's objects |
| `internal/auth` | Online and offline login, Basic auth (Android), key slots, PATs (spec 7) |
| `internal/store` | SQLite store in two files: a plaintext key file (hub identity, key slots) and an AES-256-XTS encrypted data file (everything else, including the query index); migrations (spec 9.2) |
| `internal/hubcrypto` | Data key, Argon2id key slots, AES-256-GCM (spec 9.1) |
| `internal/tracker` | Object model, the query index over the store, visibility bitmaps, offline searches and import validation (spec 8.2, 9.3, 10) |
| `internal/trackerapi` | `/api/tracker/*` and generated attribute values, online and offline (spec 10) |
| `internal/aggregate` | Aggregate data entry: periods, windows and locks, access from `dataEntry/metadata`, edits |
| `internal/aggregateapi` | The Aggregate Data Entry app's data endpoints, online and offline |
| `internal/proxy` | Pass-through with a per-user GET cache (spec 5.1) |
| `internal/sync` | Outbox push, issue reporting, pull, reserved value pool, heartbeat (spec 11) |
| `internal/ui` | `/hub/status`, setup flow, admin page (spec 6.2, 12) |
| `internal/hubaccount` | The hub's own account: the sealed box it arrives in, the account and move codes, the approval steps (the reference the app matches) |
| `internal/stick` | The USB stick: setup code, the copy a new disk is restored from (docs/RECOVERY.md) |
| `internal/upgrade` | Signed releases, store migration on a copy, binary swap, health check, rollback (spec 12a) |
| `internal/compat` | DHIS2-version-specific requests and responses, one file per version (`v42.go`, `v43.go`). A difference that is simpler to read in both shapes stays in place (the 2.42 and 2.43 `dataValueSets`, 2.43's `enrollmentCategoryCombo`) |
| `internal/config` | `hub.yaml`, its defaults, and free ports for `hub init` |
| `internal/hubtls` | The hub's certificate authority and server certificate (`dhis2.local`, its names and private addresses), the start page's file |
| `internal/devkey` | The device key sealed by the TPM, where there is one: TLS keys and setup files sealed, key slots bound |
| `internal/atomicfile` | Replacing a file so a power cut leaves the old one or the new one |
| `internal/mdns` | Answers `dhis2.local` where the system does not (Windows, macOS) |
| `internal/netsetup` | NetworkManager on the Pi: the setup Wi-Fi, joining a Wi-Fi, the hub as the facility's Wi-Fi (case C); `nmfake` stands in for `nmcli` in tests |
| `internal/logfile` | A size-capped log file: the hub's log where a service has no console, and `requests.log` |
| `internal/diskfree` | Free disk space, per system |
| `internal/central` | HTTP client for central and the connectivity monitor (spec 5.2) |
| `internal/access` | Per-user access lists and the facility's program catalog (spec 8.1) |
| `internal/fakecentral` | Fake DHIS2 2.42 shaped after the recorded fixtures, for tests |
| `internal/e2e` | End-to-end tests for the acceptance scenarios |
| `internal/testkit` | Test framework: manifests, network switch, profiles (`provision`), oracles, Android driver |
| `internal/live` | The live suite: scenarios and oracles against a provisioned instance or the fake |
| `internal/perf` | In-process performance tests: pulls, queries, load on central |
| `perf` | Container runs on Pi-class profiles (`perf/pi.sh`) and their results |
| `clients` | Playwright drivers for the real web apps (Capture web, eRegisters, Aggregate Data Entry, the Facility Hub app) used by the live suite, and the eRegisters instance setup |
| `testdata/2.42` | Request/response fixtures and Capture endpoint lists recorded from DHIS2 2.42.6, plus the first round's manual test setup (`SETUP.md`, `setup_instance.py`) |
| `deploy` | Example image defaults file and systemd unit |
| `scripts/demo-facility-hubs.py` | Adds demo hubs to a test instance, for trying the Facility Hub app |
| `scripts/test-upgrade.sh` | Scenario 11: self-upgrade, rollback, unsigned release |

## Build

You need Go 1.26 or newer. The build needs no C toolchain: SQLite is a pure Go build ([`ncruces/go-sqlite3`](https://github.com/ncruces/go-sqlite3); see docs/FINDINGS.md for why this replaces `modernc.org/sqlite`).

```bash
go build -o hub ./cmd/hub                                    # this machine
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o hub-linux-arm64 ./cmd/hub   # Raspberry Pi 4/5, 64-bit OS
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o hub-linux-amd64 ./cmd/hub   # laptop / VM
```

Each binary is a single static file of about 16 MB. `-X main.version` sets the version that self-upgrade compares against.

`go.sum` was produced in an environment without access to `proxy.golang.org`, from the modules' GitHub mirrors. On a machine with normal access, run `go mod verify` once. If it reports a mismatch, delete `go.sum` and run `go mod tidy`.

## Configure

The hub has no facility-specific configuration. It reads one small image-defaults file, the same for every hub in a country (spec 6.1). See [`deploy/hub.example.yaml`](deploy/hub.example.yaml):

| Key | Meaning |
|---|---|
| `central_url` | Central DHIS2 base URL; optional. Without it (the generic image), set-up asks for central's address and the setup code first (in `facility-hub/hub.txt` on the USB stick, or `data_dir/setup-token`), checks the server (https, a trusted certificate) and shows its name before anyone logs in. With it and without `central_hosts`, set-up always uses it |
| `central_hosts` | Hosts setup may point a hub at (`hmis.example.org, *.hmis.example.org`); setup then asks for an `https://` URL on them. With neither key, setup needs the token in `data_dir/setup-token` (printed on the label) |
| `network` | `networkmanager` (the Pi image): the hub manages its network through NetworkManager, with the setup Wi-Fi and case C (`internal/netsetup`). Empty (default): the machine's network as it is |
| `mdns` | `true`: the hub answers `dhis2.local` itself (Windows, macOS; the Pi image has avahi). Default `false` |
| `setup_local_only` | `true`: set-up only from the machine itself, and no setup code (Windows, macOS: being at the PC proves presence at the facility). Default `false` |
| `uplink_switch` | `true`: "Simulate uplink cut" on the admin page, for demonstrations. Default `false`: in the field a hub that looks offline lets staff type in a new central |
| `tpm` | Seal the hub's device key with the machine's TPM 2.0 where it has one (`internal/devkey`). Default `true`. `false` keeps a new hub off a TPM that misbehaves; it cannot take a hub that already has `device.key` off its TPM, whose sealed files then cannot be opened |
| `password_fallback` | Store a user's central password, sealed, when a PAT cannot be created (spec 7.1). Default `false` |
| `listen` | Listen address, e.g. `:8443`; `:443` on the image, for `https://dhis2.local` |
| `http_listen` | Plain-HTTP address for the start page and the hub's certificate (`:80` on the image, for `http://dhis2.local`); off when empty (the default). Every other request there gets the start page, which moves on to HTTPS once the device trusts the hub |
| `data_dir` | Store (`hub.db` key file, `hub.db.data` encrypted data file), TLS certificates (the hub's authority `ca.crt` for devices, also served at `/hub/ca.crt`, with its key `ca.key`; the server's `tls.crt` and `tls.key`, renewed by the hub), request log, upgrade files |
| `sync_interval`, `ping_interval`, `ping_timeout` | Sync cadence (default 15m, with ±10 % jitter) and connectivity probe (15s, 5s; spec 5.2) |
| `reconnect_delay` | After an outage, wait up to this long at random before syncing, so hubs do not reach central together (default 5m). Until the outbox is pushed, the hub keeps answering tracker and aggregate requests itself |
| Retention by program and tracked entity type | Central's dataStore key `facility-hub-config/retention`, read by every hub: `{"defaultMonths": 0, "programs": {"<program>": 12}, "trackedEntityTypes": {"<type>": 6}}`. Months of data kept, 0 or no entry keeps everything. Event programs keep events by event date. Tracker programs keep active enrollments in full and closed ones until their last change is older than the window, then as hidden stubs (for the "enrol once" check). Tracked entity types have no default: people are kept unless their type is named; an entity of a type with a window goes when it is old and has nothing left in full |
| `access_refresh_interval` | How often each user's access list is re-read from central, besides at login (default 1h) |
| `pat_expiry_days` | Lifetime of the personal access tokens the hub creates |
| `tls` | `true` (default): at first boot the hub creates a CA limited to its own names and private addresses, issues its server certificate, and keeps the CA's key (`ca.key`) to renew it; both private keys are sealed with the device key where the machine has a TPM. `false`: plain HTTP for development |
| `reserved_pool_size` | Default reserved values kept per generated attribute (default 500); the Facility Hub app can override it per attribute in `facility-hub-config/reserved` |
| `usb_dir` | Where the box's USB stick is mounted (none by default; the image uses `/media/hub-usb`). Before set-up the hub writes its setup code there; after, each minute, what a restore needs: the pending saves, the key file and the hub's state (docs/RECOVERY.md, C2). Used only when it is a mount point |
| `full_pull_interval` | How often a full, non-incremental pull runs per access group (default 168h, a week). A changed access list gives a full pull at the next sync anyway |
| `ownership_check_interval` | How often the hub checks which of its people the facility still owns, to catch transfers out (default 1h) |
| `offline_search_facility` | Answer offline searches with `orgUnitMode=ACCESSIBLE` from the facility's data instead of refusing them (default `true`; FINDINGS.md) |
| `log_requests` | Log every request and append it to `data_dir/requests.log` (spec 13.1) |
| `hub_hosts` | Extra host names clients may use for the hub (comma separated; no wildcard). The hub answers 421 to any other `Host`; its own names and addresses are always accepted |
| `session_idle`, `session_max` | End a hub session after this long without requests (default 1h) or after login (default 12h) |
| `key_slot_max_age` | Remove the key slot of a user who has not logged in for this long (default 2160h, 90 days); the three most recently used slots are always kept |

## Run

```bash
./hub serve -config /etc/hub/hub.yaml
```

1. **First boot.** The hub creates a hub ID and starts in setup mode. Open `https://dhis2.local/` (or `https://<hub>:8443/` with the default `listen`) and you are sent to `/hub/setup`.
2. **Setup.** A staff member assigned to the facility logs in. The hub then:
   - picks the facility from the user's org units, with a picker if there are several;
   - checks that the DHIS2 version is supported;
   - finds the tracker programs assigned to the facility that the setup user can see. Each facility user's online login, and each sync, adds the programs that user can see, since a district officer may not see them all;
   - registers itself in the `facility-hub-registry` data store namespace, offering to retire another active hub for the same facility;
   - creates its hub-level token;
   - starts the first full pull.
3. **Staff log in once online.** Every staff member must log in through the hub once while it is online:
   - web: the normal login page at the hub's address;
   - Android: enter the hub's address as the server URL.

   Offline login only works after that. Each staff member then opens Capture and the Data Entry app once online; at each online login the hub also loads, for that person, the screens others at the facility opened online (docs/DEPLOYMENT.md, section 3).
4. **Daily use.** Clients use the hub's address. While central is reachable, everything passes through to central with the user's own PAT. When it is not, the hub serves cached GETs and handles `/api/tracker/*` and generated values itself.

Hub endpoints:

- `GET /hub/status`: no auth. `{mode, hubId, lastPush, lastPull, pending, rejected, version, locked, setup}`.
- `/hub/ui`: admin page, for staff assigned to the facility (log in at `/hub/login`). It shows status, outbox, issues and users with their access refresh times, and has these actions:
  - sync now;
  - **simulate uplink cut**, for demos, only with `uplink_switch: true`;
  - **use my account** for the hub's own housekeeping (heartbeat, issue reports, generated IDs), when the person whose token it uses leaves;
  - reset hub.
- `/hub/ui/endpoints`: every endpoint and parameter clients called, and every parameter refused offline (spec 13.1).

**macOS** (for the DHIS2 team to try the hub): `macos/install.sh` ([macos/README.md](macos/README.md)).

**Windows.** `windows/build.sh <version> out/` builds the installer on Linux (`windows/README.md`); the guide for the person at the PC is [docs/WINDOWS-INSTALL.md](docs/WINDOWS-INSTALL.md).

**Raspberry Pi.** Use 64-bit Raspberry Pi OS. Install the binary at `/usr/local/lib/hub/hub` (a folder the `hub` user owns, for self-upgrade), the defaults at `/etc/hub/hub.yaml`, and [`deploy/hub.service`](deploy/hub.service) as a systemd unit. Set the host name to `dhis2` (`hostnamectl set-hostname dhis2`), so `avahi-daemon` announces `dhis2.local`. The full image, hardware and network set-ups, and how devices reach the hub securely, are in [DEPLOYMENT.md](docs/DEPLOYMENT.md).

## Releases and self-upgrade

Releases come from the supplier's GitHub releases (`dhis2-utils/poc-data-proxy`, tag `v<version>`), signed with an ed25519 key. The release source and the public key are built into the binary, not set in `hub.yaml`:

```bash
./hub keygen -out release        # release.key (the release workflow's secret), release.pub
go build -ldflags "-X github.com/dhis2/facility-hub/internal/upgrade.ReleaseKey=$(cat release.pub)" ./cmd/hub
./hub release -key release.key -dir out -version 0.3.0 [-image dhis2-hub-0.3.0-arm64.img.xz] \
    linux/arm64=hub-linux-arm64 linux/amd64=hub-linux-amd64 windows/amd64=hub-windows-amd64.exe
```

**The version** is in `VERSION`: the next release, like an app's `package.json`. Every build reads it:
- a release is `VERSION` itself, tagged `v<VERSION>`; the release workflow refuses any other tag;
- CI builds (the Windows installer, macOS, a Pi image run by hand) are `<VERSION>-ci.<run number>`, which sort above the previous release and below this one, so each installs over the one before;
- a plain `go build` is `0.0.0-dev`, older than all of them.

After a release, the next commit raises `VERSION`.

**On GitHub**, `.github/workflows/release.yml` does this for a tag `v<version>`:
1. tests;
2. builds `linux/arm64` and `linux/amd64` (`windows/amd64` joins with the Windows service);
3. builds and checks the Pi image (`image.yml`);
4. signs with the secret `RELEASE_SIGNING_KEY`;
5. publishes the release.

The public key is the repository variable `RELEASE_PUBLIC_KEY`. Tags cannot be moved or deleted, so run the workflow by hand first: a dry run on the same commit, which publishes nothing and signs with a throwaway key.

`out/` holds the release's assets: the binaries (`hub-linux-arm64`, …), the image if given, `release.json` (version, file names and SHA-256 per platform, the image) and `release.json.sig`. A build without a key never upgrades.

Each country chooses the version on central, in the Facility Hub app's "Hub version" page, which writes `facility-hub-admin/release`: `{"version": "0.3.0", "pilotHubs": [...], "percent": 10}`. After each online sync cycle a hub reads it; if the version is newer than its own, has not failed here before, and the hub is a pilot hub or its ID hashes under the percentage, it fetches that release's signed `release.json`, then:

1. downloads the binary in the background, beside the sync cycle, and verifies it against `release.json`;
2. at a later cycle, once the binary is there: pushes its outbox;
3. blocks tracker writes;
4. runs the new binary's migrations on a copy of the store;
5. swaps binary and store;
6. re-execs in place, handing the in-memory data key over a pipe so the store stays unlocked;
7. health-checks: store, central and `/hub/status`.

If the health check fails, it restores the previous binary and store and re-execs again. The result (`ok`, `failed` or `ignored`) goes into the heartbeat as `lastUpgrade`.

## Tests

```bash
go test ./...               # unit, golden-fixture, end-to-end and live-suite tests against the fake central
scripts/test-upgrade.sh     # scenario 11: the version chosen on central; unsigned release ignored, upgrade, failed health check rolls back
go run ./cmd/hubtest matrix -profiles synthetic,sierra-leone,laos -versions 42   # the live suite against real DHIS2
```

- **`internal/e2e`** runs a real hub against `internal/fakecentral`, shaped after the fixtures in `testdata/2.42` with central's quirks. The uplink is cut by having the fake central drop connections, so the hub sees real network errors.
- **`internal/compat`** checks the 2.42 response shapes against the recorded fixtures.
- **Live DHIS2**, the oracles and the real Capture apps: [TESTING.md](docs/TESTING.md). Results so far: [FINDINGS.md](docs/FINDINGS.md).
- **Performance:** [PERFORMANCE.md](docs/PERFORMANCE.md).

## License

BSD 3-Clause. Copyright (c) 2026, University of Oslo. See [LICENSE](LICENSE).
