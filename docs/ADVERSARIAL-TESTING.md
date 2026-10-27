# Adversarial testing

A plan to break the hub before a country does: every worst case we can think of, run against a real DHIS2, with clients working on the hub while central, the network, the power and the users change under them. It builds on the suites that exist (TESTING.md) and lists only what they do not cover, or cover only against the fake central.

Status: plan, 27 October 2026; sandbox wave 1 under way ("Progress", below). The "Today" column is the state before it: what was built since is under "What to build first" and "Progress". The way of working: as much as possible in the sandbox first, building the tools and running scenarios side by side, so results come in from the first days; the physical sessions follow, one at a time, repeating on hardware what the sandbox found or could not reach.

## What counts as broken

A scenario fails if any of these is false at its end. They are checked after every scenario, not only where a scenario names them.

| | Must hold | Checked by |
|---|---|---|
| I1 | **Nothing confirmed is lost.** Every save the hub confirmed to a client is on central with its values, author and dates, or is listed as a refused change in the Facility Hub app with its data kept on the hub. | The ledger (below) against central and the hub's refused changes |
| I2 | **Nothing is duplicated.** No person, enrollment, event or data value exists twice on central because of the hub. | The ledger; a duplicate search on central by the test's unique values |
| I3 | **Nothing leaks.** No user sees through the hub, online or offline, what central would not show them. | The read oracle (TestOracle) after the change, per affected user |
| I4 | **Nothing is overwritten silently**, apart from the documented case (aggregate: the last write wins, as on central). | The ledger, with central's audit where available |
| I5 | **The hub fails closed and keeps going.** A request it cannot answer correctly is refused with a clear message; one refused change does not stop unrelated ones; nothing retries for ever. | The outbox and refused counts in `/hub/status` and the heartbeat |
| I6 | **The hub recovers** by itself, or with a step written in RECOVERY.md or the hub page, without the supplier. The data key is never lost; the hub is never left unusable. | The scenario's own check |
| I7 | **Central is not harmed**: no request storm, no retry loop, nothing beyond what TestStrugglingCentral measured. | The requests the hub sends (Netcut, or central's access log) |
| I8 | **Staff can tell what happened.** What each app shows is recorded, with a screenshot, and judged: a clear refusal is fine; a spinner for ever, a wrong success or a silent loss is not. | Screenshots from Playwright and the emulator, and your notes |

## The test bed

### Central

- **For the sandbox track:** disposable DHIS2 instances from the broker, from the curated seeds: Sierra Leone 2.42 and 2.43, Laos 2.42 and 2.43, and empty ones for the synthetic profile (`cmd/hubtest`). Each scenario family gets a fresh instance, so a broken central costs nothing.
- **For the physical track:** an instance on the internet that the Pis can reach and the sandbox can change. The demo central (implement.im.dhis2.org/facility-hub) was stopped by an infrastructure upgrade on 27 October and has been restarted. A separate instance for these tests is still better, so the demo stays clean: same seed as the synthetic profile, with a snapshot to go back to after each session.

### Hubs

- Software hubs in the sandbox (testkit), several at once where a scenario needs a second hub.
- The Pi 5 (SSD) and Pi 4 (card), and the Windows VM, on your network.

### Clients

- **Continuous synthetic clients**: hubload's web and Android users, changed to work from a real instance's metadata (the hubtest manifest: programs, stages, data elements, option sets, data sets) instead of the fake's generated programs. They write tracker, event-program and aggregate data, and log every confirmed save to the ledger. They run during every scenario, so each one meets real traffic.
- **Real apps in a browser** (Playwright in the sandbox): Capture web, the Data Entry app and the Facility Hub app, through a sandbox hub. They save as well as read, and take a screenshot at every refusal.
- **Android Capture** on a real tablet connected to your Mac by USB, or an emulator (the sandbox's adb reaches the Mac's adb server).
- **You, on real tablets and browsers**, through the physical hub, for the scenarios where your eyes are the check.

### The ledger

Every client, synthetic or scripted, appends each save the hub confirms: user, object, values, time, and whether the hub was online. After a scenario, a checker reads central (as superuser, and as the author for createdBy) and the hub's refused changes, and reports, per save: on central as saved; on central but different; refused and kept; missing; duplicated. It extends hubload's `-confirmed` and `-verify`, which today read back from the hub only.

### Control

- **Central**, from the sandbox: metadata, users, sharing, org unit assignments, tokens, approvals and settings, through the API, as helpers that work on a real DHIS2. Stopping and starting the instance through the broker.
- **The sandbox hub's network**: the testkit proxy between the hub and central, extended to cut, slow down, drop answers after central acted on a request, hold connections half open, and flap.
- **The sandbox hub's process**: a hard kill (not the clean stop that `Reboot()` does today).
- **The physical hubs**: you, by pulling the network cable or the power when I ask, and by moving your Mac to another network when central must be changed while the hub is cut off.

The sandbox cannot reach your network by default; you can open ports or addresses for a session. Without that, the physical hubs are seen through central (their heartbeat), the Mac's clients and the files you copy from the hub.

## The scenarios

Where: **S** sandbox, automated, real DHIS2; **C** real apps (Playwright or the emulator) in the sandbox; **P** physical hub with you; **M** by hand, by you. Today: what exists, from the inventory of 27 October (fake = only against the fake central).

### A. Central's metadata changes while the hub holds offline work

The hub checks offline only what it can know; central decides on push. In each scenario, clients capture offline under the old rules, the change is made on central, then the uplink returns.

| ID | Scenario | Must hold, beyond I1–I8 | Where | Today |
|---|---|---|---|---|
| A1 | A data element becomes compulsory in a stage; offline events lack it | Refused on push with central's reason, kept, shown in the app; the event's later edits wait behind it | S, C | none |
| A2 | An option is removed from an option set; offline data uses it | As A1 | S | none |
| A3 | A data element is removed from a stage | Central's answer recorded; no repeated push | S | none |
| A4 | A stage is made non-repeatable; two offline events in it | The second refused, the first kept | S | none |
| A5 | A program is removed from the facility (org unit assignment) | Offline work refused or accepted as central decides; the program leaves the hub at the next pull; nothing leaks after | S | none |
| A6 | A program's access level changes (open to protected) | Ownership rules apply from the next pull; offline reads of others' people stop | S | fake (ownership) |
| A7 | An attribute becomes unique; two offline registrations share a value | One refused with E1064, the other accepted | S | unique already-taken: both |
| A8 | `onlyEnrollOnce` turned on with an enrollment made offline | As central decides | S | live (TestEnrollmentRulesOffline) |
| A9 | A category option's end date passes, or its sharing changes (event programs with attribute combos, aggregate) | Refused or accepted as central; FINDINGS says sharing changes reach the hub weekly: measure what happens in between | S | fake |
| A10 | A program's expiry days or a data set's expiry passes while offline | Refused with central's reason; the hub's own lock agrees with central's | S | fake (aggregate) |
| A11 | The facility org unit is closed (closed date) | Everything after the date refused; the hub says so; it keeps working for reads | S, P | none |
| A12 | The facility moves to another district (parent changes) | Users' scope recomputed; nothing leaks | S | none |
| A13 | A tracked entity type attribute is added, removed or made searchable | Searches and registrations follow at the next sync | S | none |
| A14 | Program rules change (a new rule hides a field or assigns a value) | Capture offline uses its cached rules; record what central does on push | C | none |
| A15 | Metadata changes many times during one outage | The hub's catalogue matches central after one sync; no stale cache | S | none |
| A16 | A large metadata import on central during a hub pull | The pull completes or retries cleanly | S | none |

### B. Users and access change

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| B1 | A user loses the facility (org unit removed) while working offline | Their offline saves: as central decides on push. Their offline reads: stop at the next sync | S, C | fake (TestAdminPageLostWhenMovedAway) |
| B2 | A user is disabled on central while offline | Can no longer log in once the hub knows; their queued saves: pushed with their token or refused and kept, never lost silently | S | fake (TestDisabledUserLosesAccess) |
| B3 | A user's password is changed on central | Offline login with the old password until the hub learns; documented | S, M | none |
| B4 | A user's personal access token is revoked or expires with saves queued | Saves wait, "needs online login" shown; pushed after they log in online | S | fake (RevokeToken) |
| B5 | A role loses an authority (for example tracked entity delete) | Refused offline at the next sync, as central | S | none |
| B6 | Program or stage sharing removed from a user group | Offline reads and writes stop at the next sync; queued writes refused and kept | S | fake (TestAccessChangeOfOneUser) |
| B7 | A user moves to another facility with a hub | Sees only the new facility's data, on each hub, after login | S | none |
| B8 | A user is deleted on central with saves queued | Saves refused and kept, visible to a manager; never pushed as someone else | S | none |
| B9 | A brand-new user arrives while the facility is offline | Cannot log in (first login must be online); clear message | S, C | e2e (unknown user) |
| B10 | Two users swap devices or share a tablet | Each sees only their own access; no cached data from the other (Capture's browser cache, Android's database) | C, M | per-user metadata: e2e |
| B11 | User group membership changes for many users at once | Access refreshed for all within the refresh interval; measure the gap | S | none |
| B12 | A user's search scope changes (search org units) | Offline search follows at the next sync | S | none |

### C. Ownership, conflicts and duplicates

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| C1 | The same person edited on central and on the hub offline | Recorded: which edit wins on central, and what the hub shows after the pull; no silent loss of either without a trace | S | none |
| C2 | A person deleted on central while edited offline | Edit refused and kept; the person disappears from the hub | S | tombstones: both |
| C3 | A person transferred to the sibling while the hub is offline, then edited offline | Refused on push, kept, shown | S | fake |
| C4 | Transferred through the hub while central transfers the person elsewhere | One owner at the end, the one central holds | S | none |
| C5 | The same person registered offline at two facilities (two hubs) | Both pushed or one refused by uniqueness; record which; no merge loss | S | none (two hubs) |
| C6 | A duplicate registered offline on one hub by two staff | As C5 on one hub | S | none |
| C7 | A relationship to a person the hub does not hold | Refused clearly or accepted as central | S | none |
| C8 | Break the glass online, then the uplink drops | The temporary access ends as central's does; nothing more is shown offline | S | fake (TestBreakTheGlass) |
| C9 | An enrollment completed on central while events are added offline | As central decides | S | none |

### D. The network

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| D1 | The uplink drops in the middle of a push | No duplicate on retry (I2), nothing lost (I1) | S | none on live |
| D2 | Central acts on a save but the answer is lost | The retry recognises it; no duplicate | S | fake (LoseResponse) |
| D3 | The uplink flaps every few seconds for an hour | The hub settles; the outbox drains; no storm (I7) | S, P | fake (hubload -flap) |
| D4 | A very slow link (2G, 20 kB/s, 2 s latency) | Pulls and pushes complete or resume; pages for staff stay local | S | none |
| D5 | Half-open connections (packets stop, no reset) | Timeouts free the hub; it goes offline and back | S | none |
| D6 | DNS fails while TCP works, or the reverse | Treated as offline; recovers | S | none |
| D7 | A captive portal or a hotel-style login page in front of the internet | Not mistaken for central (ping must say "pong") | S, P | ping check: unit |
| D8 | Central's certificate expires, or a wrong certificate is presented | The hub refuses; stays offline; says why | S | none |
| D9 | An outage of three weeks with continuous work | Pulls and pushes complete afterwards; tokens still valid or refreshed; disk and outbox sized | S (time-compressed), P | none |
| D10 | The facility network goes down but central stays up (devices cannot reach the hub) | Devices fall back as each app does; nothing is lost when they reach the hub again | M | none |
| D11 | The uplink returns while a client is mid-save | The save lands once | S | none |
| D12 | Physical: cable pulled at random moments during a working day | As D1–D11 on real hardware | P | none |

### E. Power, crashes and storage

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| E1 | Power cut during a save | Confirmed saves survive (I1); an unconfirmed save is either complete or absent | S (kill -9), P | Windows VM 9 759 saves; kill -9 in sandbox |
| E2 | Power cut during a push | No duplicate on central after restart | S, P | none |
| E3 | Power cut during the first pull | Resumes; no half-stored people | S, P | none |
| E4 | Power cut during a migration or an upgrade | The hub comes back on the old or the new release, never in between | S, P | none |
| E5 | Power cut while the hub copies to the USB stick | The stick's copy stays usable or is replaced | P | none (RECOVERY.md:82) |
| E6 | Ten power cuts in an hour | As E1; the data file never corrupt | P | Windows VM only |
| E7 | The disk fills up | Saves refused clearly, nothing corrupt, the hub warns; recovers when space returns | P (Windows VM), S if a size-limited folder is possible | none |
| E8 | The SD card fails or turns read-only | The hub says so; restore from the stick works | P | restore: fake |
| E9 | The USB stick is pulled during a copy, or a different stick is inserted | No secret leaks to the wrong stick; copy resumes | P | partly fake |
| E10 | The disk moved to a new board | The hub carries on as itself | P | fake TPM test |
| E11 | Windows: sleep, hibernate, Windows Update restart, antivirus quarantine of hub.exe | The service comes back, or the facility is told; the store unlocks at next login | P (VM), M | none |
| E12 | Memory pressure (other programs on the Windows PC use most of it) | The hub slows but does not die or lose data | P (VM) | none |

### F. The clock

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| F1 | The hub's clock hours or days behind central (Pi 4 without a clock battery, after a power cut with no network) | Saves still accepted by central; dates on records come from clients; periods and expiry judged correctly once synced | P (Pi 4 off overnight, then no network), S if the hub's clock can be offset | central's clock: fake |
| F2 | The hub's clock ahead | As F1 | P (Windows VM: set the clock) | none |
| F3 | The clock jumps while the hub runs (network time arrives) | No duplicate pulls, no lost watermarks, sessions survive | P | none |
| F4 | A client device's clock is wrong (tablet set to last year) | As central would decide | M | none |
| F5 | A month or year boundary while offline (aggregate periods open and close) | Forms lock and open as central would | S (central's date), P | fake |

### G. The hub's own lifecycle

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| G1 | Upgrade to a new release with saves queued and clients working | Nothing lost; clients see a short refusal at most | S, P | fake (scripts/test-upgrade.sh) |
| G2 | The new release fails its health check | Rolls back on its own; data intact | S, P | fake |
| G3 | An unsigned or tampered release offered | Refused | S | e2e |
| G4 | The country picks an older release (downgrade) | Refused, as designed | S | e2e |
| G5 | Upgrade while offline for weeks, then online | Upgrades after the outbox is pushed, not before | S | none |
| G6 | Reset the hub with refused changes kept | As designed (TestResetKeepsRefusedChanges) on a real central | S | fake |
| G7 | A replacement hub set up for the same facility while the old one still runs (two hubs) | The old one retires; nothing pushed twice; the new one pulls everything | S, P | planted entry only |
| G8 | Two hubs at sibling facilities, people moving between them | TESTING.md phase 6 scenarios | S | none |
| G9 | The hub's own account revoked on central | The hub keeps serving staff; housekeeping taken over; says so | S | e2e |
| G10 | Nobody logs in after a restart for days (store stays locked) | Clients told; nothing lost; first login unlocks | S, P | none |
| G11 | All staff key slots lost (every password changed on central) | Recovery as RECOVERY.md; or documented as the limit | S | none |

### H. Central's lifecycle

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| H1 | Central down for maintenance (nginx 503) for hours | Hub offline after two pings; staff keep working; no storm | S (broker stop), P | measured on the fake (TestStrugglingCentral) |
| H2 | Central very slow (analytics running, 30 s answers) | Hub stays usable; its sync completes later | S | fake |
| H3 | Central upgraded 2.42 → 2.43 with hubs holding queued saves | Hubs on a release that supports 2.43 push as before; a hub that does not support it says "needs upgrade" and passes through | S | none (Version field unused) |
| H4 | Central restored from yesterday's backup | The hub notices central went back in time; pushes are not lost; watermarks do not skip data | S | none |
| H5 | Central moved (announced, redirected, typed) | As move tests, on a real central | S | fake |
| H6 | A system setting changes (server time zone, allowed future dates, password policy) | Followed at the next sync | S | central's zone: fake |
| H7 | Central's PAT or session lifetime settings shortened | Hubs renew in time | S | fake |
| H8 | Central's database deadlocks or returns 500s for some imports | Those entries retried later, not lost, not looping | S | none |

### I. Clients

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| I1c | Capture web: what each refusal looks like (needs connection, refused, upgrading) | Recorded with screenshots (I8); nothing appears saved that is not | C | read-only script only |
| I2c | Capture web cached across users on one browser | No other user's data shown | C | none |
| I3c | Android: switch a tablet with unsynced data from central to the hub | Its data reaches central once | C, M | none |
| I4c | Android offline for weeks, then syncs through the hub | As above, with a large backlog | C, M | none |
| I5c | Android's reserved values (generated IDs) run out offline | Clear message; no duplicate IDs | C | e2e (reserved values) |
| I6c | Data Entry app: drafts, completion, validation offline | As FINDINGS; screenshots | C | live, never run |
| I7c | eRegisters: full pull and register offline | As FINDINGS | C | live, never run |
| I8c | A client sends malformed or very large payloads | Refused, the hub survives | S | partly |
| I9c | Many devices at once start their first download after a reset | The hub serves them; no device times out for ever | S, P | perf only |
| I10c | Capture app upgraded on central while the hub is offline | The cached app keeps working offline; the new one loads online | C | none |

### J. Aggregate data

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| J1 | Data approved on central while values were entered offline | Refused and kept on push; shown | S | fake (Approve) |
| J2 | The data set removed from the facility while offline | As central decides | S | none |
| J3 | The same cell edited offline by two staff and on central | Last write wins as on central; each write traceable | S | sequential only |
| J4 | Completion offline of a form that fails validation online | Refused and kept | S | both |
| J5 | A category option combo removed | Refused and kept | S | none |
| J6 | Period boundary and expiry days while offline | As F5 | S | fake |
| J7 | Data input periods changed on central | Followed at the next sync | S | none |

### K. Event programs

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| K1 | Events captured offline in a program closed on central | As central decides | S | none |
| K2 | Stage access lost | As B6 for events | S | fake |
| K3 | An event deleted on central while edited offline | Refused and kept | S | both (tombstones) |
| K4 | Attribute option combos: option sharing changed | As A9 | S | fake |
| K5 | Retention prunes events during a working day | Pending events never pruned; nothing a client holds disappears | S | fake |
| K6 | Android downloads event programs through the hub | Works as on central | C | never run (FINDINGS.md:409) |

### L. Security abuse

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| L1 | A user asks the hub for another user's data with crafted queries (filters, fields, paging) | Nothing beyond central (I3) | S | read oracle |
| L2 | Offline password guessing | Throttled; no key slot opened | S | e2e (throttle) |
| L3 | A forged entry in the hub registry or move announcement on central | Ignored; protected namespaces as DEPLOYMENT.md | S | partly |
| L4 | A stolen disk or stick read on another machine | No personal data in clear | S, P | grep tests |
| L5 | A device on the facility network that is not staff | Gets nothing without logging in | S | e2e |
| L6 | A fake central (DNS spoofed on the facility network) | The hub does not send tokens or data to it | S, P | move tests partly |

### M. Load and soak

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| M1 | 72 hours of continuous clients with an outage every few hours and a power cut a day | I1–I8; memory, disk and the outbox stay flat | P (Pi 5), S | none |
| M2 | Retention pruning, a full pull and an upgrade during a busy day | As above | S | none |
| M3 | The same against a Laos-sized real instance | As above | S | none |

### N. Updates: the hub, DHIS2 and the apps

Every part of the system changes under the others: the hub gets releases, central gets DHIS2 patches and new majors, and the apps the clients run are updated on central or in the Play Store, each on its own schedule. Groups G and H name a few of these; this group covers them all. Versions: the hub's last release and the next; DHIS2 2.42 and 2.43, each at its oldest and newest patch; each app at the version bundled with that DHIS2 and the newest on the App Hub; Android Capture at the version in use and the newest.

**The hub's releases**

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| N1 | Upgrade from the last release with a full outbox, refused changes, stubs and every kind of record present, clients working | Every record and setting carried over; the outbox pushes after; clients see at most a short refusal | S, P | fake, real binaries (scripts/test-upgrade.sh) |
| N2 | Upgrade skipping releases (the oldest still in use straight to the newest) | All migrations run in order; as N1 | S | none |
| N3 | A migration on a full store at the HC IV size, on a Pi and on Windows | Finishes within the time AGENTS.md asks; the hub serves staff or says it is upgrading | P | sandbox only (TestMigrationOnFullStore) |
| N4 | Power cut or kill during download, during the migration, and during the first start of the new release | Back on the old or the new release, never between; no data lost (E4) | S, P | none |
| N5 | The new release fails its health check after the migration ran | Rolls back to the old binary and the old store; nothing written in between is lost | S | fake |
| N6 | Two hubs of a country on different releases at once (a staged rollout) | Both work with central and with the Facility Hub app | S | none |
| N7 | A release that refuses something the old one accepted (stricter checks) with such saves queued | Queued saves still pushed or refused with a reason, never dropped | S | none |
| N8 | The release key changes (a new signing key) | Hubs on the old key accept the new one only through the documented path; nothing unsigned runs | S | none |
| N9 | Windows: the service upgraded while Windows Update restarts the PC; antivirus holds the new hub.exe | The service comes back on one of the two releases; the store stays unlocked or asks for a login | P (VM) | CI install test only |
| N10 | An upgrade offered while the hub has been offline for weeks | Downloaded and applied only after the outbox is pushed (G5) | S | none |

**DHIS2 core**

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| N11 | A patch release of central (2.42.x to 2.42.y) with hubs holding queued saves | Hubs carry on; the oracles still pass | S | none |
| N12 | A new major (2.42 to 2.43) while hubs are offline with queued saves, on a hub release that supports both | Saves pushed after; reads and the oracles pass on the new major | S | none |
| N13 | A new major the hub's release does not support | The hub stops answering offline, passes everything through and shows "needs upgrade" (SPEC); no queued save lost; after the hub is upgraded, they push | S | none (the fake's Version field is unused) |
| N14 | A new major changes an answer the hub relies on (a field renamed, an endpoint moved, an error code changed, the tracker import report reshaped) | Found by the oracles and the hub's own checks before a country upgrades; a hub that meets it refuses rather than misreads | S | 2.43 differences handled once |
| N15 | Central's personal access tokens or session handling change in a new major | Hubs keep their tokens or get new ones at the next login, as TESTING.md's token tests | S | none |
| N16 | Central upgraded and its metadata migrated (new uids for categories, options sorted differently) | The hub's catalogue matches central after one sync | S | none |
| N17 | Central rolled back to the previous major from a backup after a failed upgrade | As H4: nothing pushed is lost; watermarks do not skip data | S | none |

**The apps the clients run**

| ID | Scenario | Must hold | Where | Today |
|---|---|---|---|---|
| N18 | Capture web updated on central (App Management) while the hub is offline | Browsers keep the cached app offline; the new one loads online; a new endpoint the hub cannot answer gives "needs connection", never wrong data | C | none |
| N19 | Capture web at the newest App Hub version against the hub | The same checks as the bundled version (working lists, dashboard, saves offline) | C | none |
| N20 | The Data Entry app updated, as N18 and N19 | As N18 and N19 | C | none |
| N21 | Android Capture updated on the tablet (newer SDK) while it holds unsynced data from the hub | Its data reaches the hub and central once; the new SDK's requests are answered or refused clearly | C (tablet) | none |
| N22 | Android Capture at an older version than the hub was tested with | As N21 | C (tablet) | none |
| N23 | The Facility Hub app updated on central while hubs run | Hubs and the app still agree on the dataStore formats (registry, approvals, retention, retries) | S | none |
| N24 | eRegisters updated | As N19 | C | none |

How: the hub's old releases are the signed GitHub releases (or local builds of their tags); DHIS2 versions come from the broker's seeds (2.42 and 2.43), and an upgrade is a database dump of one instance restored into an instance of the next version, if the broker cannot upgrade an instance in place; app versions are installed from the App Hub into the test instance; Android versions are APKs installed on the tablet with adb.

## What to build first

1. **Central helpers for a real DHIS2**: metadata changes (compulsory, options, stage repeatable, program and data set assignment, closed dates, expiry, approvals), user changes (org units, disabled, password, groups, sharing, authorities), tokens, stopping and starting the instance. Today most of this exists only on the fake.
2. **The ledger and its checker**, for every client.
3. **The continuous clients on a real instance's metadata** (hubload with a manifest).
4. **Network faults in the testkit proxy**: slow, lossy, half open, answer lost after central acted, flapping. Built 27 Oct in `testkit.Netcut` (`SetDelay`, `SetRate`, `HalfOpen`, `LoseAnswer`, `CutAnswer`, `CutRequest`, `Flap`).
5. **A hard kill of the sandbox hub**, and a hub clock offset if it can be done without code in the hub that only tests use.
6. **A second sandbox hub** for two-hub scenarios.
7. **Playwright scripts that save** in Capture web and the Data Entry app, with a screenshot at every refusal; the existing scripts run again.
8. **An upgrade bench**: the last release and the next as real binaries against a real DHIS2, with data of every kind present (extends scripts/test-upgrade.sh); a DHIS2 upgrade by moving a database dump into an instance of the next version; app versions installed from the App Hub and APKs on the tablet.

Each finding becomes a failing test before its fix, as for every other bug (AGENTS.md).

## Order

Tools and scenarios are built side by side: each tool is used by a scenario as soon as it works, and results are recorded as they come.

1. **Wave 1, sandbox, highest risk of silent loss**: A1–A5, A7, B1, B2, B4, B6, B8, C1–C4, D1, D2, D5, E1–E4, G1, H1, H3, H4, J1, J3, K1, K3, and the updates most likely to lose data: N1, N4, N5, N12, N13.
2. **Wave 2, real apps in the sandbox**: I1c, I2c, I6c, I7c, the Android ones on the tablet, and the app updates N18–N22.
3. **Wave 3, the rest in the sandbox**, then the soak (M1) in the sandbox.
4. **Wave 4, physical sessions with you** (below), one at a time, when the Pis have an image with the latest changes.

## Sessions with the physical hubs

Each session takes about two hours of your time. Before it: the hub flashed and set up on the test central, the Mac's clients ready (I send the commands), and your Mac able to move to a second network (a phone's hotspot) so you can reach me while the hub's network is cut. During it, I tell you when to act, and check through central and the Mac's ledger. After it: you copy the card's `loadtest` folder or the hub's logs.

1. **Network**: cable pulled at random moments (D12), a long outage with central changed meanwhile (A1, A5, B1, B2 from the sandbox list, now on hardware), flapping by hand.
2. **Power**: ten cuts during saves, pushes and the first pull (E1–E3, E6); a cut during an upgrade (E4).
3. **Clock and storage**: the Pi 4 off overnight and started without network (F1); the Windows VM's clock moved (F2) and its disk filled (E7); the stick pulled during a copy (E9).
4. **Real devices**: your tablets with Android Capture and browsers with Capture web and the Data Entry app, through the hub, while I change access on central (B1, B6, I3c, I4c). You note what each app shows.
5. **Soak**: 72 hours on the Pi 5 (M1), with one outage and one power cut a day at times I give.

## Progress

Each scenario's result is recorded here: pass, fail with the finding, or decided as a limitation. Each finding also goes into FINDINGS.md with its fix and test, as before.

| Session | Status | Date | Result |
|---|---|---|---|
| Sandbox wave 1 | started | 27 Oct 2026 | A1, A2, A4, A5, B1, B2, B4, B6, B8 on synthetic 2.42 (`internal/adversarial`) |
| Sandbox wave 2 | not started | | |
| Sandbox wave 3 and soak | not started | | |
| Physical 1: network | not started | | |
| Physical 2: power | not started | | |
| Physical 3: clock and storage | not started | | |
| Physical 4: real devices | not started | | |
| Physical 5: soak | not started | | |

| Scenario | Result | Date | Finding or note |
|---|---|---|---|
| A1 | pass | 27 Oct | Central checks compulsory data elements when an event is completed (validation strategy ON_COMPLETE, the default): the active event is accepted, its completion refused (E1303) and kept. A later edit of a refused event is sent and judged on its own, not held behind the first. |
| A2 | pass | 27 Oct | E1125, kept and reported; the event with an option that stays reaches central. I8: the refused event, read through the hub, is 404 online (passed to central) and 200 offline (the hub's copy): a client sees it come and go with the uplink, and nothing in Capture says it was refused; only the Facility Hub app lists it. |
| A4 | pass | 27 Oct | The second event refused (E1039) and kept; after the sync the hub refuses a third one offline itself. |
| A5 | pass | 27 Oct | Enrollment E1041, events E1029, kept; the offline person reaches central without the enrollment (atomicMode=OBJECT). Afterwards the hub answers the program's listings 503 offline, while central still lists the nurse 55 people there: a gap, not a leak. |
| B1 | pass after the hour; **fail** within it | 27 Oct | Central (2.42.7) caches a personal access token's authentication, with the user's access as it was, for an hour after its first use (`apiTokensCache`, `expireAfterWrite(1, HOURS)`; dropped only when the token is deleted, or by a cache clear). After the hour (`TestB1UserLosesFacility`, the cache cleared): every offline save refused (E1000) and kept, offline reads 403, online 409 as on central. Within it (`TestB1UserLosesFacilityWithinTheHour`, `HUB_FINDINGS=1`): the offline saves are accepted with the token though central refuses the same write to the user's password; online the hub passes the user's listing through with the token and shows 87 people at the facility, where central answers the user 409: a leak through the hub (I3) for up to an hour. |
| B2 | **fail** | 27 Oct | Within the hour (above), central accepts a disabled user's token (their password: "Account disabled"): the saves are pushed, and the user goes on working through the hub online and offline. After it, central answers the token 401; the hub then refuses the user's login online and offline, and holds their saves for a login that cannot come, as in B8. Both `HUB_FINDINGS=1`. |
| B4 | pass | 27 Oct | Saves wait, "needs online login"; after the user's online login, pushed by them. One push a cycle meanwhile, each answered 401. |
| B6 | pass | 27 Oct | Enrollment and events refused (E1069, E1010) and kept; the person (type sharing kept) reaches central. Offline reads 403, an offline write refused "needs connection". |
| B8 | **fail** | 27 Oct | Central deletes the user (their online data does not stop it) and answers their token 401. The hub holds the saves for an online login that cannot come, one pushed again every cycle; the Facility Hub app shows only "2 saves wait for their user to log in on the hub again". Nothing lost, nothing pushed as someone else; not listed as refused (I1, I5). |
