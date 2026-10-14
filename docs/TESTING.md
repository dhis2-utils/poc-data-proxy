# Testing the facility hub

The hub stands in for DHIS2 when the uplink is down. Every place where its answer differs from what central would have answered is a bug: either a user sees something central would hide, or an app breaks. The test framework exists to find those differences across hub code, DHIS2 configurations, DHIS2 versions and client apps, and to keep finding them as each of those changes.

## Supported DHIS2 versions

The aim is to support the DHIS2 versions DHIS2 itself supports, from 2.42 on. DHIS2 releases yearly and supports three versions; when 2.44 is released the window becomes 2.42–2.44, then 2.43–2.45, and so on. Each supported version runs the full test matrix below. A hub release that adds the next version goes out before any country upgrades central. **2.42 and 2.43 are tested** (2.42.6, 2.43.2); their differences are in FINDINGS.md, "DHIS2 2.43".

## What has to be covered

| Axis | Values |
|---|---|
| Hub behaviour | online pass-through, offline reads, offline writes, sync, login and sessions, upgrades, security properties |
| DHIS2 configuration | synthetic edge cases built on an empty instance; Sierra Leone demo; Laos HMIS demo; later, country-like metadata snapshots |
| DHIS2 version | every supported version (42 now, 43 next) |
| Client | Capture web app, Android Capture app, and raw API calls |
| Conditions | network cuts and flapping, reboots, power loss mid-write, full disk, clock skew, scale (data volume and number of devices) |

## How

### 1. Tests are written against roles, not UIDs

Every DHIS2 setup is described by a **manifest**: the facility, a sibling facility and the district, the tracker programs and their stages and attributes, and one user per **role**:

| Role | Meaning |
|---|---|
| `admin` | in the setup user group; sets up and administers the hub |
| `nurse` | captures at the facility with full access to the programs |
| `restricted` | like `nurse`, but cannot see one program stage |
| `clerk` | reads the programs, cannot write events |
| `outsider` | captures at another facility |
| `district` | district officer: search scope over the district, no capture at the facility, not every program shared |
| `searchless` | captures at the facility but has no search scope there |

A **profile** turns a DHIS2 instance into that shape and writes the manifest:

- `synthetic` builds everything on an empty instance: org unit tree, tracked entity types, attributes (unique, generated, option sets), open and protected programs, a restricted stage, relationship types, users, sharing and generated data. Its size is a parameter, so the same profile also gives the scale test data. It is where edge cases live, because we control every detail.
- `sierra-leone` and `laos` start from the demo database seeds. They pick a facility that has tracker data, create the role users and the sharing they need, and keep the rest of the database as it is, with its program rules, option sets and quirks.

The same test code runs against any manifest.

### 2. Two kinds of central

- **fake**: `internal/fakecentral`, in process, milliseconds per test, with fault injection (refusing tokens, disabled accounts, errors). Runs on every commit.
- **live**: a real DHIS2 instance, provisioned by a profile. Any instance you can reach with a superuser account will do. Between hub and central sits an in-process **network switch** (`testkit.Netcut`): a proxy that can cut the line at the connection level, delay it or let it flap, so "offline" is a real network failure on any central.

### 3. Central is the oracle

Hand-written expectations do not scale across configurations. Instead, for each role user and each query, the **differential oracle** asks central directly (online, as that user) and the hub (offline, after a sync), and compares:

- the set of objects, with central's answer narrowed to what the facility owns (the hub holds only the facility's data);
- for objects in both, every field the query asked for;
- error status and code for queries the hub refuses or central rejects.

Queries come from a generator: per program and role, every search the Capture apps make (recorded endpoint lists in `testdata/<version>/`), crossed with org unit modes, attribute filters on values sampled from the data, ordering, paging and field selections, plus single-object reads for objects the user may and may not see. A difference is a finding, reported with the user, the request and both answers.

Writes are checked the same way after the fact: what reaches central through the hub's outbox must equal what the same user writing online would have produced, including author, dates and rejections.

### 4. Real clients

Scripted Capture web runs (Playwright) and Android Capture runs (adb and the UI automator) against hub and live central: log in online, work offline, hand over between devices, reconnect. These are the tests that found most real bugs in the POC; they are now in the live suite (`TestCaptureWeb`, `TestCaptureAndroid`, below).

### 5. Conditions

- Network: cut, restore, flap during a push, slow uplink.
- Process: kill the hub during an import, a push and an upgrade; restart and check that nothing is lost or duplicated.
- Disk: full data partition.
- Time: hub clock ahead of or behind central.
- Scale: a load generator (`cmd/hubload`) simulating N web and Android clients against a synthetic facility of N thousand tracked entities, measuring latency, memory and correctness.

## Running

| Command | What |
|---|---|
| `go test ./...` | unit tests, fake-central end-to-end tests, and the live suite against the fake |
| `go run ./cmd/hubtest provision -profile laos -url https://dhis2.example.org -admin user:password -out manifest.json` | turn a DHIS2 instance into a profile's shape and write its manifest. `synthetic` needs an empty instance; `sierra-leone` and `laos` need the matching demo database |
| `HUB_MANIFEST=manifest.json HUB_REPORT=oracle.json go test ./internal/live/` | the live suite against that instance |

Live runs change their instance: use a disposable one, and restore it (or a fresh demo database) before running again.

**With an instance manager.** Where test instances come from d2-broker, a local tool that creates and resets disposable DHIS2 instances (`DHIS2_BROKER_URL`, `DHIS2_BROKER_TOKEN`), `hubtest` does the above in one step, writing results to `testresults/<profile>-<version>/`:

| Command | What |
|---|---|
| `go run ./cmd/hubtest run -profile synthetic -version 42` | a clean instance, provisioned, with the live suite run against it |
| `go run ./cmd/hubtest run -profile sierra-leone -version 42` | the same from the Sierra Leone demo database (reset if the instance exists) |
| `go run ./cmd/hubtest matrix -profiles synthetic,sierra-leone,laos -versions 42` | every profile × version, one instance at a time |

### What the live suite contains

| Test | What it checks |
|---|---|
| `TestScenarios` | the acceptance scenarios (spec 13.3) on the profile's own programs: setup, online and offline writes, access per role, unique conflicts, reconnection, reboot, nothing in plain text on disk |
| `TestOracle` | every role and matrix user × generated read queries, hub offline against central online; verdicts LEAK, MISSING, DIFFERS, LENIENT, REFUSED, MATCH. Known gaps are named in `knownGap` with their reason |
| `TestWriteOracle` | every user tries the same writes offline; after reconnection central judges them: DENIED (the hub accepted what central refuses this user) fails the run, OVER-REJECT and LATE are reported |
| `TestOwnershipLive` | a facility-owned person (in a protected program if the profile has one) is transferred to the sibling facility on central: after a sync the hub shows offline no more than central shows the nurse; breaking the glass online through the hub leaves nothing visible offline; ownership is restored |
| `TestTransferOnCentral`, `TestTransferThroughHub`, `TestBreakTheGlass`, `TestAccessChangeOfOneUser`, `TestCaptureAboveFacility` | the same situations, and access changes, against the fake central (no DHIS2 needed) |
| `TestCaptureWeb` | (`HUB_CLIENTS=1`) the real Capture web app with Playwright: working lists and the enrollment dashboard online, then offline; fails if the offline working list does not render, and reports every request the hub refused |
| `TestCaptureAndroid` | (`HUB_CLIENTS=1`, an emulator on adb) the real Android Capture app: first login and download online, then with the app's data wiped and the uplink cut, a first login and full download offline from the hub; compares the record counts and reports refused requests |
| `TestERegisters` | (`HUB_CLIENTS=1`, the `eregisters` profile) the eRegisters app (HISP Uganda), one device at a time with its own browser profile: the second nurse pulls online; the first pulls online, then with the uplink cut registers a client and pushes to the hub; the second opens the app offline, pulls from the hub and finds the client; after reconnection central has the client, created by the first nurse. Reports every request the hub could not answer offline |

### Profiles

| Profile | Source | What it adds |
|---|---|---|
| `synthetic` | empty instance | full control: open, protected, closed, public and event programs, a restricted stage, unique and generated attributes, two tracked entity types, an attribute category combo whose category options are shared per user, ownership moved in and out, visits elsewhere, deleted and scheduled records, cross-facility relationships |
| `sierra-leone` | the Sierra Leone demo database of that version | real metadata and data; the facility with the most tracker data, its four busiest programs |
| `laos` | the Lao HMIS demo database | a second real configuration, with its own conventions |
| `eregisters` | an empty 2.43 instance, set up by `clients/eregisters_setup.py` from the eRegisters configuration export and an app build (neither in this repository) | the eRegisters app as a client: its one tracker program on two facilities and one in another district, an admin, two nurses and an outsider. Only `TestERegisters` and the scenarios use it; it has no access matrix |

The **access matrix** (27–30 users per profile) covers every pair of: capture scope (facility, district, sibling, two facilities, country), search scope (none, facility, district, other district, country), access to the open, protected and closed programs, to the restricted stage, to the tracked entity type and to a category option, granted directly or through a group. Three more users test the hub's shared listings (users with the same access list are pulled once for all): a twin of the nurse (same groups and scopes), and two users with identical direct grants that differ only in the shared category option, which the hub's grouping leaves out and handles per user.

## Phases

| Phase | Status |
|---|---|
| 1. **Framework on 2.42**: manifest, network switch, hub runner, synthetic profile, live scenario suite, differential read oracle, `hubtest` CLI | done |
| 2. **Seeded profiles**: Sierra Leone and Laos | done |
| 3. **Real clients**: scripted Capture web and Android runs | done (`HUB_CLIENTS=1`); not yet re-run on the SQLite store |
| 4. **Conditions**: network, process, disk, time, scale | network and scale in progress ([PERFORMANCE.md](PERFORMANCE.md)); power cuts planned on real hardware; process, disk and time to do |
| 5. **2.43**: diff its shapes against 2.42, implement the differences, add it to the matrix | done: the three profiles and eRegisters pass on 2.43.2; the recorded 2.42 reads were replayed against 2.43 instead of recording a second set of fixtures |
| 6. **Two hubs** | to do |

The scale test (phase 4) runs Pi-class hardware with 60 users, 18 of them on Android, up to a busy HC IV (300 000 people, about 0.5 million enrollments and 1.2 million events) and a pessimistic 700 000 / 1.2 million / 3.1 million.

Two hubs (phase 6): the facility and its sibling each get a hub on the same central, each with several devices of different kinds (Capture web, Android, raw API clients). Scenarios: a person transferred from one hub's facility to the other while either hub or both are offline, in open and protected programs; breaking the glass online at one hub for a person the other owns; referrals back and forth; both hubs reconnecting in either order. Checks: every device and hub shows no more than central would, nothing is lost or duplicated, and queued transfers (the Android SDK queues them, the hubs refuse them offline) end where central says. It needs the sibling facility's roles in the manifest and a second hub in the test environment.
