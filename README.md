# Facility hub (proof of concept)

A small device on a facility LAN that caches the facility's DHIS2 tracker data and relays tracker writes to the central DHIS2 server. When central is unreachable, the DHIS2 Capture web app and the Android Capture app keep working against the hub, and each user still sees only what central lets them see. The specification is [SPEC-facility-hub-poc.md](docs/SPEC-facility-hub-poc.md). Answers to its open questions, and the places where the spec turned out wrong, are in [FINDINGS.md](docs/FINDINGS.md).

Target DHIS2 version: **2.42** (verified against 2.42.6). 2.43 is mapped to the same request and response handling but has not been tested.

## Layout

| Path | What it is |
|---|---|
| `cmd/hub` | The binary: `serve`, `migrate`, `keygen`, `release`, `version` |
| `cmd/fakecentral` | A stand-in DHIS2 used by the upgrade test script |
| `internal/app` | Wiring: store, handlers, sync worker, upgrader |
| `internal/auth` | Online and offline login, Basic auth (Android), key slots, PATs (spec 7) |
| `internal/store` | SQLite store: encrypted payloads, outbox, cache, issues, migrations (spec 9.2) |
| `internal/hubcrypto` | Data key, Argon2id key slots and password hashes, AES-256-GCM (spec 9.1) |
| `internal/tracker` | Object model, in-memory index, visibility rules, offline queries and import validation (spec 8.2, 9.3, 10) |
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
| `testdata/2.42` | Request/response fixtures recorded from DHIS2 2.42.6, plus `SETUP.md` and `setup_instance.py` for the test instance |
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
| `central_url` | Central DHIS2 base URL. Optional; asked at setup if empty |
| `setup_user_group` | UID of the user group whose members may set up, administer and reset a hub |
| `listen` | Listen address, e.g. `:8443` |
| `data_dir` | Store, TLS certificate, request log, upgrade files |
| `sync_interval`, `ping_interval`, `ping_timeout` | Sync cadence and connectivity probe (spec 5.2) |
| `pat_expiry_days` | Lifetime of the personal access tokens the hub creates |
| `release_channel`, `release_public_key` | Where to look for upgrades, and the ed25519 key releases must be signed with |
| `tls` | `true` (default): a self-signed certificate is created at first boot. `false`: plain HTTP for development |
| `reserved_pool_size` | Reserved values kept per generated attribute (default 150) |
| `full_pull_interval` | How often a full, non-incremental pull runs per user (default 24h) |
| `log_requests` | Log every request and append it to `data_dir/requests.log` (spec 13.1) |

## Run

```bash
./hub serve -config /etc/hub/hub.yaml
```

1. **First boot.** The hub creates a hub ID and starts in setup mode. Open `https://<hub>:8443/` and you are sent to `/hub/setup`.
2. **Setup.** A member of `setup_user_group` logs in. The hub then:
   - picks the facility from the user's org units, with a picker if there are several;
   - checks that the DHIS2 version is supported;
   - finds the tracker programs assigned to the facility;
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

**Raspberry Pi.** Use 64-bit Raspberry Pi OS and set the hostname to `hub`, so `avahi-daemon` (installed by default) announces `hub.local` over mDNS. Install the binary at `/usr/local/bin/hub`, the defaults at `/etc/hub/hub.yaml`, and [`deploy/hub.service`](deploy/hub.service) as a systemd unit.

## Releases and self-upgrade

```bash
./hub keygen -out release                    # release.key (keep secret), release.pub (put in release_public_key)
./hub release -key release.key -dir channel -version 0.2.0 [-percent 25] [-hubs hub-abc,hub-def] \
    linux/arm64=hub-linux-arm64 linux/amd64=hub-linux-amd64
```

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
go test ./...               # unit, golden-fixture and end-to-end tests (scenarios 1-10, reset, 401 handling)
scripts/test-upgrade.sh     # scenario 11: unsigned release ignored, signed upgrade, failed health check rolls back
```

- **`internal/e2e`** runs a real hub against `internal/fakecentral`. The fake is shaped after the fixtures in `testdata/2.42`, including central's quirks: with `atomicMode=OBJECT` central answers 200/OK even when objects are rejected, and children of a failed parent are dropped without an error report. The uplink is cut by having the fake central drop connections, so the hub sees real network errors.
- **`internal/compat`** checks the 2.42 response shapes against the recorded fixtures.
- **Live DHIS2 instance.** `testdata/2.42/setup_instance.py` creates the test users (`hub_admin`, `nurse_a`, `nurse_b`, `clerk_c`, `outsider_d`), groups and sharing on a Sierra Leone 2.42 demo database. `testdata/2.42/SETUP.md` lists the UIDs.
- **Manual steps with the real Capture apps** (spec 13.3 scenarios 2 and 2a) and their results are in docs/FINDINGS.md.
