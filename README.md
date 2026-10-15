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
| [DEPLOYMENT.md](docs/DEPLOYMENT.md) | Rolling out hubs: hardware, image, setup at the facility, networks, reaching the hub securely, running the fleet; options under consideration |
| [RECOVERY.md](docs/RECOVERY.md) | Keeping unsynced data safe: backup and recovery options (ideas, not decisions) |
| [docs/reviews/](docs/reviews/README.md) | Dated independent reviews (security and confidentiality), and the names the code cites them by |
| [AGENTS.md](AGENTS.md) | How to change the code: keep the structure, guards, checks before committing |

## Layout

| Path | What it is |
|---|---|
| `cmd/hub` | The binary: `serve`, `migrate`, `keygen`, `release`, `version` |
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
| `internal/proxy` | Pass-through with a per-user GET cache (spec 5.1) |
| `internal/sync` | Outbox push, issue reporting, pull, reserved value pool, heartbeat (spec 11) |
| `internal/ui` | `/hub/status`, setup flow, admin page (spec 6.2, 12) |
| `internal/upgrade` | Signed releases, store migration on a copy, binary swap, health check, rollback (spec 12a) |
| `internal/compat` | **All DHIS2-version-specific request and response handling**, one file per version (`v42.go`) |
| `internal/central` | HTTP client for central and the connectivity monitor (spec 5.2) |
| `internal/access` | Per-user access lists and the facility's program catalog (spec 8.1) |
| `internal/fakecentral` | Fake DHIS2 2.42 shaped after the recorded fixtures, for tests |
| `internal/e2e` | End-to-end tests for the acceptance scenarios |
| `internal/testkit` | Test framework: manifests, network switch, profiles (`provision`), oracles, Android driver |
| `internal/live` | The live suite: scenarios and oracles against a provisioned instance or the fake |
| `internal/perf` | In-process performance tests: pulls, queries, load on central |
| `perf` | Container runs on Pi-class profiles (`perf/pi.sh`) and their results |
| `clients` | Playwright drivers for the real web apps (Capture web, eRegisters) used by the live suite, and the eRegisters instance setup |
| `testdata/2.42` | Request/response fixtures and Capture endpoint lists recorded from DHIS2 2.42.6, plus the first round's manual test setup (`SETUP.md`, `setup_instance.py`) |
| `deploy` | Example image defaults file and systemd unit |
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
| `central_url` | Central DHIS2 base URL. Without `central_hosts`, setup always uses it |
| `central_hosts` | Hosts setup may point a hub at (`hmis.example.org, *.hmis.example.org`); setup then asks for an `https://` URL on them. With neither key, setup needs the token in `data_dir/setup-token` (printed on the label) |
| `password_fallback` | Store a user's central password, sealed, when a PAT cannot be created (spec 7.1). Default `false` |
| `setup_user_group` | UID of the user group whose members may set up, administer and reset a hub |
| `listen` | Listen address, e.g. `:8443` |
| `data_dir` | Store (`hub.db` key file, `hub.db.data` encrypted data file), TLS certificate (`ca.crt` for devices, also served at `/hub/ca.crt`), request log, upgrade files |
| `sync_interval`, `ping_interval`, `ping_timeout` | Sync cadence (default 15m, with ±10 % jitter) and connectivity probe (15s, 5s; spec 5.2) |
| `reconnect_delay` | After an outage, wait up to this long at random before syncing, so hubs do not reach central together (default 1m) |
| `access_refresh_interval` | How often each user's access list is re-read from central, besides at login (default 1h) |
| `pat_expiry_days` | Lifetime of the personal access tokens the hub creates |
| `release_channel`, `release_public_key` | Where to look for upgrades, and the ed25519 key releases must be signed with |
| `tls` | `true` (default): at first boot the hub creates a CA limited to its own names and private addresses, issues its server certificate and discards the CA key. `false`: plain HTTP for development |
| `reserved_pool_size` | Reserved values kept per generated attribute (default 150) |
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

1. **First boot.** The hub creates a hub ID and starts in setup mode. Open `https://<hub>:8443/` and you are sent to `/hub/setup`.
2. **Setup.** A member of `setup_user_group` logs in. The hub then:
   - picks the facility from the user's org units, with a picker if there are several;
   - checks that the DHIS2 version is supported;
   - finds the tracker programs assigned to the facility that the setup user can see. Each facility user's online login, and each sync, adds the programs that user can see, since a district officer may not see them all;
   - registers itself in the `facility-hub-registry` data store namespace, offering to retire another active hub for the same facility;
   - creates its hub-level token;
   - starts the first full pull.
3. **Staff log in once online.** Every staff member must log in through the hub once while it is online:
   - web: the normal login page at the hub's address;
   - Android: enter the hub's address as the server URL.

   Offline login only works after that.
4. **Daily use.** Clients use the hub's address. While central is reachable, everything passes through to central with the user's own PAT. When it is not, the hub serves cached GETs and handles `/api/tracker/*` and generated values itself.

Hub endpoints:

- `GET /hub/status`: no auth. `{mode, hubId, lastPush, lastPull, pending, rejected, version, locked, setup}`.
- `/hub/ui`: admin page, for members of `setup_user_group` only (log in at `/hub/login`). It shows status, outbox, issues and users with their access refresh times, and has these actions:
  - sync now;
  - **simulate uplink cut**, for demos;
  - reset hub.
- `/hub/ui/endpoints`: every endpoint and parameter clients called, and every parameter refused offline (spec 13.1).

**Raspberry Pi.** Use 64-bit Raspberry Pi OS. Install the binary at `/usr/local/bin/hub`, the defaults at `/etc/hub/hub.yaml`, and [`deploy/hub.service`](deploy/hub.service) as a systemd unit. With the host name `hub`, `avahi-daemon` announces `hub.local`, which laptops resolve and Android does not reliably. The full image, hardware and network set-ups, and how devices reach the hub securely, are in [DEPLOYMENT.md](docs/DEPLOYMENT.md).

## Releases and self-upgrade

```bash
./hub keygen -out release                    # release.key (keep secret), release.pub (put in release_public_key)
./hub release -key release.key -dir channel -version 0.2.0 [-percent 25] [-hubs hub-abc,hub-def] [-central-url https://new.example.org] \
    linux/arm64=hub-linux-arm64 linux/amd64=hub-linux-amd64
```

`-central-url` moves every hub that reads the manifest to a new central URL (a domain migration); a hub moves once the new server has its registry entry and accepts its token.

`channel/` is a static folder. Serve it over HTTP(S) and point `release_channel` at it. It contains `release.json` (version, file names, SHA-256 per platform, rollout stage), `release.json.sig` (ed25519 over `release.json`) and the binaries.

After each online sync cycle the hub checks the channel. If it finds a newer, correctly signed release whose stage includes this hub, it:

1. pushes its outbox;
2. downloads and verifies the binary;
3. blocks tracker writes;
4. runs the new binary's migrations on a copy of the store;
5. swaps binary and store;
6. re-execs in place, handing the in-memory data key over a pipe so the store stays unlocked;
7. health-checks: store, central and `/hub/status`.

If the health check fails, it restores the previous binary and store and re-execs again. The result (`ok`, `failed` or `ignored`) goes into the heartbeat as `lastUpgrade`.

## Tests

```bash
go test ./...               # unit, golden-fixture, end-to-end and live-suite tests against the fake central
scripts/test-upgrade.sh     # scenario 11: unsigned release ignored, signed upgrade, failed health check rolls back
go run ./cmd/hubtest matrix -profiles synthetic,sierra-leone,laos -versions 42   # the live suite against real DHIS2
```

- **`internal/e2e`** runs a real hub against `internal/fakecentral`, shaped after the fixtures in `testdata/2.42` with central's quirks. The uplink is cut by having the fake central drop connections, so the hub sees real network errors.
- **`internal/compat`** checks the 2.42 response shapes against the recorded fixtures.
- **Live DHIS2**, the oracles and the real Capture apps: [TESTING.md](docs/TESTING.md). Results so far: [FINDINGS.md](docs/FINDINGS.md).
- **Performance:** [PERFORMANCE.md](docs/PERFORMANCE.md).

## License

BSD 3-Clause. Copyright (c) 2026, University of Oslo. See [LICENSE](LICENSE).
