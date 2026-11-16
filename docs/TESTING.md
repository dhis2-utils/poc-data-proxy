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
| `admin` | assigned to the facility; sets up the hub in most tests (any staff member assigned to the facility may) |
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

- **fake**: `internal/fakecentral`, in process, milliseconds per test, with fault injection (refusing tokens, disabled accounts, errors). Run before every commit (AGENTS.md).
- **live**: a real DHIS2 instance, provisioned by a profile. Any instance you can reach with a superuser account will do. Between hub and central sits an in-process **network switch** (`testkit.Netcut`): a proxy that can cut the line at the connection level, delay it or let it flap, so "offline" is a real network failure on any central.

### 3. Central is the oracle

Hand-written expectations do not scale across configurations. Instead, for each role user and each query, the **differential oracle** asks central directly (online, as that user) and the hub (offline, after a sync), and compares:

- the set of objects, with central's answer narrowed to what the facility owns (the hub holds only the facility's data);
- for objects in both, every field the query asked for;
- error status and code for queries the hub refuses or central rejects.

Queries come from a generator: per program and role, every search the Capture apps make (recorded endpoint lists in `testdata/<version>/`), crossed with org unit modes, attribute filters on values sampled from the data, ordering, paging and field selections, plus single-object reads for objects the user may and may not see. A difference is a finding, reported with the user, the request and both answers.

Search filters have a parity test of their own (`TestFilterOperatorParity`): every operator of the tracker API, on probe records with chosen values (case, accents, numbers written as text, missing values, lists), compared with central on 2.42 and 2.43 (FINDINGS.md, "A slow uplink and search filters").

**Candidate, not built:** DHIS2 core's own tests as a source of queries. `dhis-test-e2e` sends HTTP requests with fixtures of its own (`TrackerExportTest`, `EventFiltersTest`); `dhis-test-integration` lists the edge cases core cares about (`FilterExporterTest`, `OrderAndPaginationExporterTest`). Their requests and fixtures, not their assertions, would feed the oracle: import a fixture set into a disposable instance, send each read the hub answers offline to central and to the hub, compare. The work is mostly in mapping their org units and users onto a hub's facility and access.

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
| `HUB_MANIFEST=manifest.json go test ./internal/adversarial/` | the adversarial scenarios ([ADVERSARIAL-TESTING.md](ADVERSARIAL-TESTING.md)) against that instance; those that show a finding not fixed skip unless `HUB_FINDINGS=1` |

On GitHub, `.github/workflows/test.yml` runs on a push to any branch but main, when Go code, test data, the app or the workflow changed:
- `go vet` and every Go test, with `swtpm` for the TPM tests;
- the Facility Hub app's tests, lint and type check.

Main only gets commits that already ran on their branch, so each change runs once. Docs-only pushes run nothing. Started by hand (`gh workflow run test.yml --ref <branch>`), the workflow also runs the race detector on `internal/e2e`, `internal/live`, `internal/tracker` and `internal/store`. The organisation's Actions spending is limited. So `windows.yml`, `macos.yml` and `image.yml` stay by hand: run them before merging a change that touches those systems.

The Facility Hub app (`apps/facility-hub`) has its own tests. `pnpm test` there checks:
- the codes, sealing and approval steps, against the values the hub's Go tests pin;
- flags;
- who sees which hub.

`pnpm lint` (eslint and Prettier) and `pnpm exec tsc --noEmit` check the rest.

Live runs change their instance: use a disposable one, and restore it (or a fresh demo database) before running again.

**With an instance manager.** Where test instances come from d2-broker, a local tool that creates and resets disposable DHIS2 instances (`DHIS2_BROKER_URL`, `DHIS2_BROKER_TOKEN`), `hubtest` does the above in one step, writing results to `testresults/<profile>-<version>/`:

| Command | What |
|---|---|
| `go run ./cmd/hubtest run -profile synthetic -version 42` | a clean instance, provisioned, with the live suite run against it |
| `go run ./cmd/hubtest run -profile sierra-leone -version 42` | the same from the Sierra Leone demo database (reset if the instance exists) |
| `go run ./cmd/hubtest matrix -profiles synthetic,sierra-leone,laos -versions 42` | every profile × version, one instance at a time |

### The Pi image

`sudo image/check.sh <image>` reads a built image without booting it. It checks:
- the partitions and the identifiers the boot configuration names;
- a clean file system and setuid bits;
- the hub binary, its user, its service and `hub.yaml`;
- the host name, packages, users and firewall;
- the polkit rule, the watchdog, the USB stick mount and the machine ID.

It runs after every image build, in `image.yml` too (image/README.md). The hub's network logic is tested against a fake `nmcli` (`internal/netsetup`, `internal/netsetup/nmfake`). Booting and real Wi-Fi need a Pi.

### Windows

- **Here, on Linux:**
  - every package builds and vets for windows/amd64 (`GOOS=windows GOARCH=amd64 go vet ./...`);
  - the Windows-only logic runs behind plain-Go tests: the upgrade's key handover, port choice, choosing the USB stick, the `dhis2.local` responder, and the service's arguments;
  - `windows/check.sh` checks a built installer: its signatures, `hub.exe` (64-bit, no console window), and what its script does.
- **On GitHub** (`.github/workflows/windows.yml`), run by hand:
  - the Go tests on `windows-latest`;
  - the installer, installed silently for the whole PC, checked (the service runs, answers over HTTPS at `localhost` and `dhis2.local` with the hub's own authority), then uninstalled.
- **On a Windows VM, by hand** (not done yet):
  1. install for the whole PC, and for one user without administrator rights;
  2. set up the hub on the PC; open it from a tablet at `dhis2.local`, and at the IP;
  3. restart the PC; check the hub stays locked until the first staff login, also offline;
  4. upgrade through `facility-hub-admin/release`; check the hub comes back unlocked;
  5. plug in two USB sticks, choose one, pull it out, and check the page reports it missing;
  6. with Windows' own mDNS, and Bonjour installed, check `dhis2.local` still resolves;
  7. check Defender's exclusion, the sleep setting, and the firewall on a network marked Public;
  8. move a per-user hub to a whole-PC install;
  9. uninstall, with and without removing the data;
  10. on a PC with a TPM: `device.key` appears in the data folder and `tls.key` starts with `HUBDEV1`; the service (a virtual account) still starts after a restart, and staff log in (Windows TPM Base Services, `internal/devkey`). A fresh install with `tpm: false` in hub.yaml makes no `device.key` and works as before.

#### Windows versions

The hub targets 64-bit Windows 10 and 11 (WINDOWS-INSTALL.md). Go 1.26 needs Windows 10 or later. All testing so far has been on Windows 11.

Test Windows 10 22H2 too, on an x86-64 machine such as an old laptop. Apple Silicon cannot run Windows 10 usefully, and GitHub has no Windows 10 runners. On Windows 10, check:

- both install modes: for the whole PC, and for one user;
- the service starts at boot;
- the firewall rules, the root certificate install and Defender's exclusion;
- `dhis2.local` from tablets;
- an upgrade, including the key handover;
- a PC with TPM 1.2, or none;
- Firefox, which keeps its own certificate store.

#### The self-signed installer

Releases are signed with a self-signed test certificate. A publicly trusted certificate is left for later than the pilot. Find out whether the self-signed installer can be installed where it will be used:

1. Download the installer from the release page on an ordinary Windows 10 PC and an ordinary Windows 11 PC, each with Defender and SmartScreen as they come. Note what each shows (SmartScreen's "unknown publisher", a Defender detection) and whether the install completes.
2. Ask the ministry's IT, for each pilot country: which antivirus and application control (AppLocker, WDAC) facility PCs run, and whether software is deployed centrally. Central deployment skips SmartScreen; a policy that allows only trusted publishers blocks a self-signed installer.
3. If Defender flags the installer, submit it as a false positive at Microsoft's submission portal.

### macOS

`.github/workflows/macos.yml`, run by hand:
- the Go tests on `macos-latest`;
- `macos/install.sh` with a binary built there, checked: the launchd agent runs and answers over HTTPS at `localhost` and `dhis2.local` (the hub's own responder next to macOS's), and the authority is in the System keychain;
- the uninstall.

A Mac is a test hub for the DHIS2 team, not a facility hub (macos/README.md).

### What the live suite contains

| Test | What it checks |
|---|---|
| `TestScenarios` | the acceptance scenarios (spec 13.3) on the profile's own programs: setup, online and offline writes, access per role, unique conflicts, reconnection, reboot, nothing in plain text on disk |
| `TestOracle` | every role and matrix user × generated read queries, hub offline against central online; verdicts LEAK, MISSING, DIFFERS, LENIENT, REFUSED, MATCH. Known gaps are named in `knownGap` with their reason. Android's incremental syncs (`SinceQueries`, `EventSinceQueries`) are asked at three watermarks from central's own update times: the hub must return all central returns, and may add only what the user sees without the watermark (`JudgeSince`; the hub re-sends a day's margin, W1). Lists are also filtered (`filterAttributes`, `!null` and `sw:`) and ordered on attributes of another program only (the manifest's `onlyAttrs`), which a user who cannot read that program must learn nothing from (R-03). Per user, matches may not drop below and refusals may not rise above the baseline in `internal/live/testdata/oracle/<profile>-<version>.json`, taken from `hubtest run` on a fresh instance; `HUB_ORACLE_BASELINE=write` rewrites it from a run |
| `TestWriteOracle` | every user tries the same writes offline; after reconnection central judges them: DENIED (the hub accepted what central refuses this user), LOST (accepted offline, absent on central, no issue recorded) and ALTERED (on central with another author, or without an attribute or data value as written) fail the run; OVER-REJECT and LATE are reported |
| `TestOwnershipLive` | a facility-owned person (in a protected program if the profile has one) is transferred to the sibling facility on central: after a sync the hub shows offline no more than central shows the nurse; breaking the glass online through the hub leaves nothing visible offline; ownership is restored |
| `TestTransferOnCentral`, `TestTransferThroughHub`, `TestBreakTheGlass`, `TestAccessChangeOfOneUser`, `TestCaptureAboveFacility` | the same situations, and access changes, against the fake central (no DHIS2 needed) |
| `TestCaptureWeb` | (`HUB_CLIENTS=1`) the real Capture web app with Playwright: working lists and the enrollment dashboard online, then offline; fails if the offline working list does not render, and reports every request the hub refused |
| `TestOfflineScreens` | (`HUB_CLIENTS=1`) the real Capture and Data Entry apps (`clients/offline_screens.py`), each user in a fresh browser: the nurse opens every screen online (front page, working lists, a dashboard, registration and event forms, each data set's form); her twin only logs in online; the restricted nurse opens Capture's front page once. Offline, each opens every screen: the twin and the restricted nurse must open what the nurse opens, with no "needs connection" for metadata (the hub's load at login, FINDINGS.md "Shared metadata cache"). The result, with the hub's misses by endpoint per user and what it loaded, is in `offline-screens/result.json` next to `HUB_REPORT` |
| `TestCaptureWebRefusals` | (`HUB_CLIENTS=1`) adversarial I1c: what Capture web shows, with screenshots, when the hub refuses a save offline as "needs connection" (a note), "upgrading" (local writes frozen) and by its own validation (a stage made non-repeatable on central after Capture cached it: E1039); opened again, nothing may appear saved that central does not have |
| `TestRefusedFixedInCapture` | (`HUB_CLIENTS=1`, internal/adversarial) the author fixes her own refused change in Capture web (`clients/refused_capture.py`): a person's attribute refused after its type became NUMBER; from the hub page she follows "Open in Capture", the hub answers the reads from its copy, she corrects and saves online; central has the new value and the refusal and its link are gone |
| `TestCaptureWebUsers` | (`HUB_CLIENTS=1`) adversarial I2c: the nurse, the restricted user and the outsider in turn on one browser, online and offline; nothing shown that the same user does not see alone in a fresh browser online |
| `TestCaptureWebUpdate` | (`HUB_CLIENTS=1`, `HUB_CAPTURE_ZIP`) adversarial I10c, N18: the Capture release in `HUB_CAPTURE_ZIP` installed on central while the hub is offline; the cached version keeps working offline, the new one loads online and works offline; central's Capture is put back after |
| `TestCaptureWebNewest` | (`HUB_CLIENTS=1`, `HUB_CAPTURE_ZIP`) adversarial N19: `TestCaptureWeb`'s checks with that release installed on central |
| `TestCaptureAndroid` | (`HUB_CLIENTS=1`, an emulator on adb) the real Android Capture app: first login and download online, then with the app's data wiped and the uplink cut, a first login and full download offline from the hub; compares the record counts and reports refused requests |
| `TestI3cAndroidSwitchToHub`, `TestI4cAndroidLongOffline`, `TestI5cAndroidReservedValuesRunOut`, `TestN21AndroidCaptureUpdated`, `TestN22AndroidCaptureOlder` | (`HUB_CLIENTS=1`, a device on USB) the adversarial Android scenarios ([ADVERSARIAL-TESTING.md](ADVERSARIAL-TESTING.md), I and N): registrations entered in the app's forms with Wi-Fi off, then synced; each must be on central once, whole, by its author. The device reaches central at `http://127.0.0.1:<port>` and the hub at `http://localhost:<port>` (one port, routed by host name), so it can hold an account on each. N21 and N22 install other Capture releases from `HUB_CAPTURE_APKS` (`dhis2-v<version>.apk`) and leave the newest installed |
| `TestEventProgram*`, `TestEventRetention*`, `TestAttributeOptionWriteOffline`, `TestRetentionTableRead`, `TestCategoryOptionWriteAccess` | event programs against the fake: catalog, access, pull, events deleted on central, per-user listing under an attribute combination, retention (pull window and pruning, pending edits kept), offline capture, the attribute option rule. Live, the read oracle adds `EventQueries` per event program (working list, by stage, Android's download compared whole, one event) and the write oracle adds event-program events and writes under each option combination (`aocs`, `enrollmentAocs` in the manifest) |
| `TestTombstonesOffline`, `TestTypePullWithoutIncludeDeleted` | deletions on central kept as tombstones: left out of lists, a person's events and single reads (404) offline, returned as deleted to `includeDeleted=true`, edits refused with central's code; live too (it deletes an event of each kind on central). The type pull without `includeDeleted` (2.42's E7145) |
| `TestRetentionPullWindow`, `TestNoRefetchAfterPrune`, `TestStubNeverMerges`, `TestReopenedEnrollmentReplacesStub`, `TestNoRetentionUnchanged`, `TestTrackerRetentionPrunes`, `TestTypeRetentionRemovesEntities`, `TestPruneKeepsReferenced`, `TestStubHiddenFromReads` | tracker retention on the fake (its data can be aged): a full pull lists active enrollments and changes since the cut-off; old closed enrollments are stored and pruned to hidden stubs, never merged, never fetched again while unchanged, replaced when reopened; entities of a type with a window removed; nothing pending pruned; no settings, no change |
| `TestEnrollmentRulesOffline`, `TestRetentionWindowLive`, `TestCatalogOnlyEnrollOnce` | E1015 and E1016 offline, against a stub too; live, the hub's refusal equals central's (E1016 where a program allows one enrollment only). With a window set live, the people listed offline equal what central lists for the nurse as active or changed since the cut-off, and central accepts both listings (the active one's time is logged) |
| `TestStaffSetUpAndAdminister`, `TestReplacementRecorded`, `TestHubTokenTakeOver` | set-up once a hub owners group is named (provisioning names the setup group, with the hub accounts group and role, in `facility-hub-admin/accounts`): staff assigned to the facility set the hub up and open its admin page, others cannot; a replacement is recorded on both registry entries; a hub owner takes the hub's own token over (old one deleted, owner in the heartbeat) |
| `TestListingBytesLive`, `TestEventEditMovesUpdatedAtLive` | live only, measurements: the pull's ID listing on the wire (plain, gzip, per listed object, without events), and which `updatedAt` an event update moves |
| `TestAggregate*` | aggregate data entry, on the fake and live: offline forms against central's (`TestAggregateOracle`), attribution on a shared browser, two users editing one cell, edits of a cell folded without reordering, refusals with central's codes, attribute option access, completion offline, a period locked while edits wait (fake), a completion waiting for its form's values (fake), expiry warnings (fake), online writes shown offline, central's clock, 2.43's compact answers. `TestAggregateOracle` compares every form of the latest open period each user may enter, with a value under each category combination a data set uses (overrides included). Uses the manifest's `aggregate` block: the seeded profiles pick a monthly data set at the facility (Child Health in Sierra Leone) |
| `TestAggregateDataEntryApp` | (`HUB_CLIENTS=1`) the real Aggregate Data Entry app (`clients/aggregate_data_entry.py`), one shared browser: the nurse enters a value online, then offline another and completes; the clerk opens the same browser offline and online; both values reach central as the nurse's. Then the same on a data set with mandatory validation, where the app refuses to complete offline |
| `TestDataEntryAppDrafts` | (`HUB_CLIENTS=1`; adversarial I6c) the Data Entry app leaves a form incomplete offline across a restart of the hub, with a validation rule on central (removed at the end): the draft's values and a cleared cell reach central as the nurse's, the form stays incomplete. Validation run offline gets "needs connection" and the app says it could not be run; before the fix the hub replayed the online answer (FINDINGS.md, "Found by adversarial testing" 8) |
| `TestDataEntryAppUpdated` | (`HUB_CLIENTS=1`, `HUB_DATAENTRY_ZIP`; adversarial N20) a newer release of the Data Entry app installed on central while the hub is offline: the browser keeps the cached build offline and saves; online it loads the new build, which then works offline for both users. The build central ships is put back at the end (deleting what was installed over it). `HUB_DATAENTRY_ZIP` also runs `TestAggregateDataEntryApp` against that release (N19). Releases: github.com/dhis2/aggregate-data-entry-app/releases (the sandbox cannot reach the App Hub) |
| `TestFacilityHubApp` | (live, once the app is built: `cd apps/facility-hub && pnpm install && pnpm build`) the Facility Hub app on central (`clients/facility_hub_app.py`): installed from its zip, a hub manager of the hub's district (not a superuser, set up as FACILITY-HUB-APP.md describes) approves the hub with the code from its page and the hub takes its account up, and announces and withdraws a planned move over one a staff member planted; the app's move code for an address equals the hub's; a user of the hub's district sees it, one of another district does not |
| `TestFileValues`, `TestFileSizes`, `TestCaptureWebFiles` | (`HUB_FILES=1`; Capture also `HUB_CLIENTS=1`) images and files (FINDINGS.md, "Images and files"): an IMAGE and a FILE_RESOURCE data element and an IMAGE attribute added to the primary program, then taken out (`HUB_FILES_KEEP=1` leaves them, for a tablet). Uploads, saves and reads through the hub online and offline; a save offline naming a file uploaded online, and one naming a file central does not have (E1302 at push); central's 10 MB limit and the hub's 64 MB; Capture web viewing an image and picking one, online and offline. `TestFileUploadOffline` is the finding: skipped unless `HUB_FINDINGS=1` |
| `TestERegisters` | (`HUB_CLIENTS=1`, the `eregisters` profile) the eRegisters app, one device at a time with its own browser profile: the second nurse pulls online; the first pulls online, then with the uplink cut registers a client and pushes to the hub; the second opens the app offline, pulls from the hub and finds the client; after reconnection central has the client, created by the first nurse. Reports every request the hub could not answer offline. The driver saves each pull it gets (`pull-<user>-<phase>.json`). The hub's offline copy names the author with first name and surname, which eRegisters requires (FINDINGS.md, "Found by adversarial testing" 9) |
| `TestERegistersUpdate` | (`HUB_CLIENTS=1`, the `eregisters` profile, `HUB_EREGISTERS_ZIP` and `HUB_EREGISTERS_OLD`; adversarial N24) the eRegisters build in `HUB_EREGISTERS_ZIP` installed on central while the hub is offline, two devices on the old build: each works offline on the build it has, pulls and pushes through the hub across the update, and finds the other's client; both on central once, by their authors. The old build is put back at the end. Releases: github.com/HISP-Uganda/eregisters/releases |

### Profiles

| Profile | Source | What it adds |
|---|---|---|
| `synthetic` | empty instance | full control: open, protected, closed, public and event programs, a restricted stage, unique and generated attributes, two tracked entity types, an attribute category combo whose category options are shared per user, ownership moved in and out, visits elsewhere, deleted and scheduled records, cross-facility relationships; three monthly aggregate data sets (a compulsory field, mandatory validation, the donor attribute combination); two event programs (one with the donor attribute combination; events over three years) |
| `sierra-leone` | the Sierra Leone demo database of that version | real metadata and data; the facility with the most tracker data, its four busiest programs; Child Health for the aggregate tests (data write for the role users, a compulsory field); up to three event programs with events at the facility, one with an attribute combination first |
| `laos` | the Lao HMIS demo database | a second real configuration, with its own conventions; a monthly data set without open future periods for the aggregate tests |
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
