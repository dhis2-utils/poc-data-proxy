# Developing the hub

For people changing the code. How to change it (keep the structure, guards, checks before committing) is in [AGENTS.md](../AGENTS.md); how it is tested, in [TESTING.md](TESTING.md). Settings are in [CONFIGURATION.md](CONFIGURATION.md), releases in [RELEASES.md](RELEASES.md).

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

`go.sum` was produced in an environment without access to `proxy.golang.org`, from the modules' GitHub mirrors. `go mod verify` does not check it: it only compares the local module cache with `go.sum`. To check `go.sum` against the checksum database, regenerate it on a machine with normal access, with an empty module cache and the database on, and compare:

```sh
cp go.sum /tmp/go.sum.old && rm go.sum
GOMODCACHE=$(mktemp -d) GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= GOFLAGS= go mod tidy
diff /tmp/go.sum.old go.sum   # no output: go.sum holds what everyone else downloads
```

A difference means a module in the old `go.sum` is not what the checksum database records: find out why before building a release.

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
- `/hub/ui`: admin page, for staff assigned to the facility (log in at `/hub/login`). It shows status, outbox, issues and users with their access refresh times. Under "Your sync errors" the signed-in user sees their own refused changes: one per record as "<date> | <org unit>", central's message, the values they entered and who closes it. Other users' refusals show only in the issue table, with values masked. The page has these actions:
  - sync now;
  - **simulate uplink cut**, for demos, only with `uplink_switch: true`;
  - **use my account** for the hub's own housekeeping (heartbeat, issue reports, generated IDs), when the person whose token it uses leaves;
  - reset hub.
- `/hub/ui/endpoints`: every endpoint and parameter clients called, and every parameter refused offline (spec 13.1).

**macOS** (for the DHIS2 team to try the hub): `macos/install.sh` ([macos/README.md](../macos/README.md)).

**Windows.** `windows/build.sh <version> out/` builds the installer on Linux (`windows/README.md`); the guide for the person at the PC is [docs/WINDOWS-INSTALL.md](WINDOWS-INSTALL.md).

**Raspberry Pi.** Use 64-bit Raspberry Pi OS. Install the binary at `/usr/local/lib/hub/hub` (a folder the `hub` user owns, for self-upgrade), the defaults at `/etc/hub/hub.yaml`, and [`deploy/hub.service`](../deploy/hub.service) as a systemd unit. Set the host name to `dhis2` (`hostnamectl set-hostname dhis2`), so `avahi-daemon` announces `dhis2.local`. The full image, hardware and network set-ups, and how devices reach the hub securely, are in [DEPLOYMENT.md](DEPLOYMENT.md).

## Tests

```bash
go test ./...               # unit, golden-fixture, end-to-end and live-suite tests against the fake central
scripts/test-upgrade.sh     # scenario 11: the version chosen on central; unsigned release ignored, upgrade, failed health check rolls back
go run ./cmd/hubtest matrix -profiles synthetic,sierra-leone,laos -versions 42   # the live suite against real DHIS2
```

- **`internal/e2e`** runs a real hub against `internal/fakecentral`, shaped after the fixtures in `testdata/2.42` with central's quirks. The uplink is cut by having the fake central drop connections, so the hub sees real network errors.
- **`internal/compat`** checks the 2.42 response shapes against the recorded fixtures.
- **Live DHIS2**, the oracles and the real Capture apps: [TESTING.md](TESTING.md). Results so far: [FINDINGS.md](FINDINGS.md).
- **Performance:** [PERFORMANCE.md](PERFORMANCE.md).
