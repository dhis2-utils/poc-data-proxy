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
| C10 | A one-time referral (Capture's referral that leaves the owner as it is): a person referred from or to the facility, online and offline; the referred event made and then completed at the receiving facility | The receiving facility sees what central lets it see and no more, online and offline; the referral's event reaches central once; the owner does not change | S, C | none |

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
| Sandbox wave 1 | done | 27–28 Oct 2026 | All of wave 1 on synthetic 2.42 and 2.43 (`internal/adversarial`), and a real upgrade of central (N12): 39 of 40 pass, 4 of them after fixes (C4, H3, N5, N13); H4 and one oracle case are accepted by decision; J1 and C2 are questions for DHIS2. Real clients: Capture web, Android on a Galaxy Tab A7 Lite, the Data Entry app |
| Sandbox wave 2 | done | 28 Oct 2026 | Capture web (I1c, I2c, I10c/N18, N19, C10), Data Entry (I6c, N20), eRegisters (I7c) on 2.42 and 2.43, Android Capture 3.5.0, 3.4.2 and 3.2.1.2 on a Galaxy Tab A7 Lite (I3c, I4c, I5c, N21, N22): all pass, three after fixes (C10, I6c, I7c) |
| Sandbox wave 3 | done | 28–29 Oct 2026 | Central changes (A, B, C) on 2.42; the hub's and central's lifecycle and updates (G, H, N), security abuse (L) and two hubs (C5) on 2.43; network, aggregate and events (D, F, I, J, K) on 2.42: 70 scenarios, 48 pass (some in part), 22 with a finding (FINDINGS.md, "Open"); no hub code changed overnight. The 10-hour sandbox soak (M1) passed, with 2 findings |
| Physical 1: network | done | 28 Oct 2026 | Pi 5 set up on the online demo central (2.43.2), uplink a 4G router's Ethernet, load from a second Mac on the router's Wi-Fi (`hubdrive`, 3 staff × Capture web and Android): the SIM pulled twice (10 min 21 s, about 12 min): 1125 saves confirmed, 892 by the hub alone, up to 1753 objects waiting; on central 899 records, none lost, partial, duplicated or wrong. Between the outages, with the driver saving about 2 a second, the backlog kept growing: push speed over 4G being measured |
| Physical 2: power | done | 28 Oct 2026 | As session 1, the Pi's power cord pulled 6 times (and two short drops): back locked after 23–42 s, unlocked by the staff logins; 919 saves confirmed; on central 752 records, none lost, partial, duplicated or wrong; saves cut mid-request were resent with the same IDs and none duplicated. Found on the way: logging out online could take the hub offline for seconds (fixed, FINDINGS.md 11); the USB stick now has one file for people (`hub.txt`) |
| Physical 3: clock and storage | not started | | |
| Physical 4: real devices | not started | | |
| Physical 5: soak | not started | | |

| Scenario | Result | Date | Finding or note |
|---|---|---|---|
| A1 | pass | 27 Oct | Central checks compulsory data elements when an event is completed (validation strategy ON_COMPLETE, the default): the active event is accepted, its completion refused (E1303) and kept. A later edit of a refused event is sent and judged on its own, not held behind the first. |
| A2 | pass | 27 Oct | E1125, kept and reported; the event with an option that stays reaches central. I8: the refused event, read through the hub, is 404 online (passed to central) and 200 offline (the hub's copy): a client sees it come and go with the uplink, and nothing in Capture says it was refused; only the Facility Hub app lists it. |
| A3 | pass | 27 Oct | E1305, refused and kept; pushed once; the event without the element reaches central. As A2: through the hub the refused event is 404 online, 200 offline. |
| A4 | pass | 27 Oct | The second event refused (E1039) and kept; after the sync the hub refuses a third one offline itself. |
| A5 | pass | 27 Oct | Enrollment E1041, events E1029, kept; the offline person reaches central without the enrollment (atomicMode=OBJECT). Afterwards the hub answers the program's listings 503 offline, while central still lists the nurse 55 people there: a gap, not a leak. |
| A6 | pass | 28 Oct | Primary program open→protected with offline work: the facility's event on central; a sibling-owned person is 503 offline before and after (never held), 404 online after, as central; 5 379 people, the same lists online and offline. |
| A7 | pass | 27 Oct | `SynAtPhone1` made unique with two offline registrations sharing a value: one on central, one refused (E1064) and kept with its enrollment; central holds the value once. |
| A8 | pass | 28 Oct | onlyEnrollOnce turned on: the second offline enrollment refused (E1016) and kept; a first registration accepted; after the sync the hub refuses a second enrollment itself (E1016). |
| A9 | pass; within the hour accepted | 28 Oct | Option end date passed: events refused (E1057) and kept; the donor aggregate value accepted by central. Option sharing removed: events refused (E1099) and kept; until the hub's access refresh (hourly) it lists 38 events offline that central no longer shows the nurse; after it none, and it refuses E1099 itself. After the sync the hub still takes events under the ended option (refused at push). |
| A10 | pass; **finding** | 28 Oct | Expired event refused (E1047) and kept; last month's aggregate value refused (E2017) and kept, this month's on central; the aggregate lock agrees after the sync. The hub reads no program expiry: it still takes expired events offline (`TestA10HubLockFollowsProgramExpiry`). |
| A11 | pass | 28 Oct | Central 2.42.7 refuses nothing at a facility closed yesterday (tracker and aggregate, also straight as the nurse); the hub agrees, reads keep working offline. |
| A12 | pass after fix | 29 Oct | Facility moved North→South: offline the old district's officer saw 5 387 people where central answers 403 (E1006); the new district's officer was refused (gap). The facility's path was kept from set-up. Fixed: the catalog refresh reads it again (`TestFacilityMovesDistrict`; FINDINGS.md, "Found by adversarial testing" 13). Not yet run again on DHIS2. |
| A13 | pass | 28 Oct | Date of birth added to the person type, made searchable, removed: the catalog follows at each sync; reads and searches never show more than central; offline registrations accepted before and after. |
| A14 | pass (API); client to do | 28 Oct | Central 2.42.7 stores an event as sent despite new ASSIGN/HIDEFIELD rules, with no warning. The hub gives a client offline the rules it last read online. Capture's cached rules offline need a real client. |
| A15 | pass; **finding** | 28 Oct | 10 metadata changes in one outage: the catalog matches central after one sync and the hub's checks follow. A user's cached metadata read is replayed stale offline (`TestA15CachedMetadataFollowsCentral`); whether Capture reloads metadata offline is to measure. |
| A16 | pass | 28 Oct | 2 000 data elements imported 49 times (median 819 ms each) during set-up and the first pull (41 s): one cycle, no error; 5 418 people on the hub as on central. |
| B1 | pass; within the hour accepted | 27 Oct | Central (2.42.7) caches a personal access token's authentication, with the user's access as it was, for an hour after its first use (`apiTokensCache`, `expireAfterWrite(1, HOURS)`; dropped only when the token is deleted, or by a cache clear). After the hour (`TestB1UserLosesFacility`, the cache cleared): every offline save refused (E1000) and kept, offline reads 403, online 409 as on central. Within it (`TestB1UserLosesFacilityWithinTheHour`, `HUB_FINDINGS=1`): the offline saves are accepted with the token though central refuses the same write to the user's password; online the hub passes the user's listing through with the token and shows 87 people at the facility, where central answers the user 409. Accepted (FINDINGS.md): an access change reaches the hub within about two hours; deleting the user's tokens ends it at once. |
| B2 | pass; within the hour accepted | 27 Oct | Within the hour (as B1), central accepts a disabled user's token: accepted. After it, central answers the token 401; the hub refuses the user's login online and offline and holds their saves, sending one a cycle to learn whether central takes the token again. Fixed: 7 days after the hub marked the user (backdated in the test) the saves are refused (`NO_LOGIN`) and kept; Resend naming another nurse sends them, and central has them whole, with that nurse as `createdBy`. |
| B3 | pass; **finding** | 28 Oct | Offline, before the hub learns: old password in, new refused (as documented). Online: old refused, new in. Offline after: new only. Queued save pushed with the token. Online, Android's old password keeps working through the hub for up to 5 min (`TestB3OldPasswordOnlineThroughCache`). |
| B4 | pass | 27 Oct | Saves wait, "needs online login"; after the user's online login, pushed by them. One push a cycle meanwhile, each answered 401. |
| B5 | pass; **finding** | 28 Oct | Role loses F_UNCOMPLETE_EVENT: the offline reopening refused (E1083) and kept, the other event on central. After the sync the hub still takes reopenings offline (`TestB5HubRefusesAfterSync`). |
| B6 | pass | 27 Oct | Enrollment and events refused (E1069, E1010) and kept; the person (type sharing kept) reaches central. Offline reads 403, an offline write refused "needs connection". |
| B7 | pass | 28 Oct | Two hubs: the user moved to the sibling; the first hub refuses them offline; the sibling's hub shows 21 people online and offline as central; nothing of the old facility. |
| B8 | pass | 27 Oct | Central deletes the user (their online data does not stop it) and answers their token 401. The saves wait, then after 7 days are refused (`NO_LOGIN`) and kept; resent as another nurse, they reach central whole with that nurse as author. Before the fix they waited for ever, listed only as "waiting for their user to log in". |
| B9 | pass | 28 Oct | A new user offline: 401 "Bad credentials" (the same answer as a wrong password, review M3); after the first online login they work offline. |
| B11 | pass | 28 Oct | 10 users removed from the nurses group: until the access refresh (hourly, plus central's token cache) 10 of 10 still read offline; one cycle refreshed 11 users in 2.8 s, then 0 of 10; offline events refused (E1010/E1013) and kept. |
| B12 | pass | 28 Oct | Search scope district → other district: searches at the facility match central online and offline before and after; the sibling's person never shown offline. |
| C1 | pass | 27 Oct | The hub's offline edit wins whole on central: it sends every attribute, so central's earlier last-name change is overwritten by the stale value; the same event value likewise (70 → 65). Both edits are in central's change log; the hub shows central's values after the pull. |
| C2 | pass; **DHIS2 question** | 27 Oct | The edit of a deleted person refused (E1114) and kept; the person leaves search and program reads offline (a uid read without program still returns the refused copy, as A2). Central 2.42.7 accepts a new event into the deleted person's enrollment, also without the hub (curl, as superuser and as the nurse): the offline event is confirmed and seen by nobody, and in one run of three its change to the person's `updatedAt` closed the refused edit's issue. A deletion on central after a refusal closing it is intended; this one came from the event. To raise with DHIS2 (FINDINGS.md); no hub change (`TestC2EventForDeletedPerson`, `HUB_FINDINGS=1`). |
| C3 | pass | 27 Oct | Protected program, transferred to the sibling on central: the offline event refused (E1102) and kept, the name edit accepted (registered here); after the sync the person is hidden offline. |
| C4 | pass after fix | 28 Oct | Transfers that end away from the facility match central's visibility; a nurse's transfer through the hub of a person central already moved gets 404 and is not applied. Out through the hub and back on central, the person stayed hidden offline (503) even after a full pull: the type pull took the transfer's `updatedAt` first. Fixed: the hourly ownership check fetches again the people central lists as the facility's that the hub held as owned elsewhere: offline 503 before, 200 after (`TestTransferBackOnCentral`, `TestC4TransferredBackOnCentral` on 2.42). Up to an hour late. |
| C5 | pass | 29 Oct | The same person (same national ID) registered offline on two hubs: the facility's reached central whole; the sibling's refused by uniqueness (E1064, E5000 for its enrollment) and kept; the value once on central (`TestC5SamePersonTwoHubs`). |
| C6 | pass | 28 Oct | Same national ID offline by two nurses: the hub refuses the second at once (E1064); with different IDs both on central (central decides duplicates). |
| C7 | pass | 28 Oct | Relationship to a person the hub doesn't hold: offline E4012 "needs connection"; online passed through once the outbox is empty; between two held people on central once. |
| C8 | pass | 28 Oct | Glass broken through the hub, an event written online, two syncs: offline the person, enrollment and events are 503 and nothing listed; an offline event refused (E1033); owner stays the sibling. |
| C9 | pass | 28 Oct | Enrollment completed on central: central accepts offline events into it and the hub agrees; the enrollment stays COMPLETED. |
| C10 | pass after fix | 28 Oct | Referrals as Capture makes them (`TestC10OneTimeReferral`, `TestC10ProtectedAfterBreakingTheGlass`): online the hub matches central; offline only gaps (503); a referred-in event cannot be completed offline (E1000); a referral to the sibling online reaches central once with its relationship, offline it is refused (E1000/E5000); owners unchanged. A referred-in event the nurse opened or completed online was kept as the facility's: listed offline where central does not list it (open program), and after breaking the glass in a protected program served (200) and listed offline though central answers her 404. Fixed: a tracker event is the facility's only when its enrollment is (FINDINGS.md, "Found by adversarial testing" 12); offline it is now 503, as the person. |
| D1 | pass | 27 Oct | A push cut on its way, and a push whose answer is cut: nothing lost, partial, duplicated or wrong on central (226 and 236 registrations checked). Before the fix the cut answer was read as central's refusal (FINDINGS.md, "Found by adversarial testing" 1). |
| D2 | pass | 27 Oct | Central stores a push or an online save and the answer is lost: pushed again, nothing created twice (217 and 91 registrations). The one online save not confirmed was the hub's "needs connection" for a person it does not hold. |
| D3 | pass | 29 Oct | Link up and down every 5 s for an hour with clients saving: 10 612 saves, 10 240 pushes, hub pending 0–20 (430 at peak), about 260 requests a minute to central, up to 658; drained 2 min 30 s after; 6 961 registrations, none lost or twice; RSS 33–110 MB (`TestD3FlapForAnHour`). |
| D4 | pass | 27 Oct | 2G link (20 kB/s, 2 s latency): saves and reads online wait for central; after the outage the outbox drained in 8 min 10 s over it, nothing lost (140 registrations). |
| D5 | pass | 27 Oct | Half-open link: the hub's timeouts free it, it goes offline, saves locally, drains 4 s after the link returns (246 registrations). |
| D6 | **finding** | 29 Oct | Name lookup failing while TCP works: offline in 3.6 s, then 58 switches online and offline in 2 min and 2 752 requests to central (the ping reuses a kept-alive connection; new connections fail the lookup). TCP failing: offline in 200 ms, 1 switch. Nothing lost (1 161 registrations) (`TestD6DNSFails`). |
| D7 | **finding** | 29 Oct | Captive portal (200 login page, then 302): hub offline in 3–3.5 s; 2 871 registrations, none lost. Before the second failed ping the portal's answers are kept as central's: a cached read replaced by the portal page and served offline; an aggregate value the portal swallowed answered 200, shown offline as saved, never sent (`TestD7PortalAnswersKept`). |
| D8 | pass | 29 Oct | Expired, wrong-host and self-signed certificates: offline in ≤200 ms, 0 requests to central with each, drained 48–62 s after the good one returned; 1 723 registrations. The reason is in the log every time, on the hub page in one case of three (`TestD8BadCertificate`). |
| D9 | pass | 29 Oct | Time-compressed: 3 108 offline saves and 100 registrations on central, the hub's clocks moved back 21 days: one cycle pushed and pulled everything in 4 min 31 s; tokens valid; data directory 195 → 219 MB; 2 052 registrations correct (`TestD9ThreeWeeksOffline`). |
| D11 | pass | 27 Oct | Link up and down every 4 s for 90 s with 500 ms latency: 2 393 saves, median wait 8 ms, max 99 ms; 13 imports while flapping, drained 51 s after; 1 705 registrations, none lost or twice. Before the fix no push got through while it flapped (FINDINGS.md 2). |
| E1 | pass | 27 Oct | Killed (-9) during saves online and offline: every confirmed save on central, 757 registrations, none partial. Logins after a restart while saves come in: 0 failures in 10 restarts; before the fixes 5 saves failed with 500 `objects.slot` (FINDINGS.md 3). |
| E2 | pass | 27 Oct | Killed while central had stored a push the hub had not heard about, then at random moments of the drain: 808 saves, none lost or twice (542 registrations). |
| E3 | pass | 27 Oct | Killed during the first pull: after the restart the hub holds all 10 916 people at the facility, none missing or in part. |
| E4 | pass | 28 Oct | The release before (813761b^, so the upgrade migrates) killed with its children during the download, the migration, the store swap and at the re-exec: back on the old release three times and on the new one once, never between; 221 registrations, none lost. A swap cut short is now tried once more, and skipped after a second cut: on this tree the second try completed 2.5 s after the cut. |
| F5 | **finding** | 29 Oct | Data input periods as a stand-in for a period boundary (central's clock cannot be moved here): the hub closes and opens forms at the exact moment, central 2.42.7 by the day. Values not lost (`TestF5InputPeriodPassesWhileOffline`). A real month boundary needs a central with a shifted clock or a run at a month's end. |
| G1 | pass | 27 Oct | 620 saves queued, clients working: the outbox is pushed before the swap, the hub does not answer for 100 ms, `upgrade result=ok`; 487 registrations, none lost or twice. |
| G2 | **finding** | 28 Oct | A release failing its health check rolled back in 8 s, nothing lost, not fetched again. A release that exits at start is never rolled back: 15 starts in 2 min, none answered (`upgrade.Recover` runs in the new binary). Restored by hand; nothing lost (`TestG2NewReleaseFails`). |
| G3 | pass; **finding** (minor) | 28 Oct | Bad signature, another key, a tampered binary, a manifest naming another version: none ran, each recorded "ignored"; a good release afterwards was taken. A mislabelled manifest is recorded again every cycle; refused manifests are fetched every cycle (`TestG3G4ReleasesRefused`). |
| G4 | pass | 28 Oct | An older release than the running one: nothing fetched, nothing recorded. |
| G5 | pass | 28 Oct | 3 min offline, 10 317 objects queued, the release chosen meanwhile: drained 1 min 47 s after the uplink returned, swapped with 0 pending; 3 439 registrations, none lost or twice. |
| G6 | pass; **finding** | 28 Oct | Reset with a refused change: RESET refused, RESET AND DISCARD reset the hub; registry says retired. The reset rewrites the registry entry without 9 fields the app shows, `tokens` among them (`TestG6ResetWithRefusedChanges`). |
| G7 | pass; **finding** (aggregate) | 28 Oct | Replacement set up while the old hub was offline: the old hub retired 1 s after its uplink returned, pushed everything, refused new tracker saves (503 "replaced"); 396 registrations, none lost or twice. A replaced hub still takes aggregate values offline (`TestG7RetiredHubAggregate`). |
| G8 | pass | 28 Oct | Two hubs, protected programme, person transferred to the sibling through the facility's hub: the facility's hub 404 online, 503 offline; the sibling's 200; owner at the end the sibling; no hub listed more than central. |
| G9 | **finding** | 28 Oct | The hub's token deleted on central: every housekeeping call 401, not renewed with the account's password, heartbeat stopped, the page says nothing; staff saves unaffected. Take-over brought it back (`TestG9HubAccountRevoked`). |
| G10 | pass | 28 Oct | Killed with 393 objects queued, 3 min locked online: only pings, 0 pushes; the first login offline unlocked with 393 pending; 131 registrations, none lost. |
| G11 | pass; documentation | 28 Oct | Every key slot's password changed, then a restart: online logins with the new passwords answer SUCCESS but the hub stays locked. The old password offline unlocks; the new one binds at the next online login. Not in RECOVERY.md or on the hub page. |
| H1 | pass | 27 Oct | Central stopped for 10 min: hub offline in 6 s (two pings), 0 of 590 reads failed, saves waited at most 196 ms, 30 requests a minute (pings) while down; drained 2 min 39 s after central was back; 2 576 registrations, none lost. A stopping Tomcat answers 502; 8 online saves at that moment got it passed back, and the client saves them again. |
| H2 | pass | 28 Oct | Central 30 s an answer for 3 min: the hub answered staff itself (1 071 saves, median 5 ms); drained 47 s after; 1 286 registrations, none lost. |
| H3 | pass after fix | 28 Oct | Central's version made to read 2.44 (`Netcut.SetVersion`): `needsUpgrade`, queue kept and pushed after. Online the hub refused all tracker reads and saves (503 "hub needs upgrade"); fixed: it passes them through while online (`TestNeedsUpgradePassesThrough`; H3 on 2.43 passes). A real upgrade of central: N12. |
| H4 | pass (decided) | 28 Oct | Central's database restored from a dump taken during the run, while saves queued on the hub: every save the hub had not sent reached central; none made before the backup lost. What central accepted after the backup (216 from the hub's outbox, 96 passed through online) is lost with the restore and not sent again: central's to restore (decided 2026-10-28, RECOVERY.md). An edit lost in the restore stays on the hub (42, central 41); edits after the restore are pulled (43). |
| H5 | pass (in part) | 28 Oct | Announced move followed when the old address died; another server's announcement not followed; typed move with the code followed. Redirect not run (needs an https central). |
| H6 | pass (in part) | 28 Oct | Open future periods changed 1→3→1: the hub followed after one sync each time. Time zone not changeable on this instance; the hub reads no password policy. |
| H7 | pass | 28 Oct | 2.43 has no PAT or session lifetime setting. The country's token setting 7 days: the hub's token renewed to 7 days, the nurse's at her next online login. |
| H8 | **finding** | 28 Oct | Central answering 500 to every push carrying one registration: 24 pushes in 2 min, all 500; 0 of 126 other offline registrations reached central; every online save kept on the hub too. After it ended: drained 24 s, 1 008 registrations, none lost (`TestH8CentralFailsSomeImports`). |
| I1c | pass | 28 Oct | Capture web against the hub: the same rows online and offline (15/15/13/15/10); an offline save reaches central once. Each refusal (`TestCaptureWebRefusals`): needs connection (a note offline, 503) "Could not save enrollment note"; upgrading (503, Retry-After) and the hub's own refusal (E1039, 409) "Error saving the enrollment event". One request each, no retry; opened again nothing appears saved, none on central. Capture's alerts do not give the reason, and the typed values are gone. |
| I2c | pass | 28 Oct | One browser, nurse then restricted then outsider, each offline first, online, offline (`TestCaptureWebUsers`): nothing beyond what each sees alone in a fresh browser online; Capture keeps one IndexedDB per user; the restricted user never sees the hidden stage; the outsider's offline login is refused (401). |
| I3c | pass; procedure | 28 Oct | Android Capture 3.5.0 on the tablet: 3 registrations offline on central's account, then "Add new account" on the hub. The app keeps one database per account and warns about nothing: the hub's account shows its 458 records without the 3; 0 on the hub and central. Logging in to central's account again where central is reachable and syncing: 3 on central once, then through the hub online and offline. Staff must sync before switching (DEPLOYMENT.md, section 5) (`TestI3cAndroidSwitchToHub`). |
| I4c | pass | 28 Oct | 25 registrations on the tablet offline while 200 people were registered on central; the tablet synced through an offline hub (225 held in 19 s, nothing refused), one push: 25 on central once, by the nurse; the tablet went from 458 to 683 records (`TestI4cAndroidLongOffline`). |
| I5c | pass | 28 Oct | App data wiped while the hub was offline: at login the hub's pool gave 5 patient numbers (half of it). The 6th registration is refused: "There are no more reserved values. Contact your system administrator" under the field, below a sheet "Saved! / Some fields need your attention." 5 on central, 5 distinct numbers. Smaller points in FINDINGS.md, "Open" (`TestI5cAndroidReservedValuesRunOut`). |
| I6c | pass after fix | 28 Oct | Data Entry app on 2.43.2 (`TestDataEntryAppDrafts`): a form left incomplete offline survives a restart of the hub; its values and a cleared cell reach central as the nurse's, and the form stays incomplete. Completion offline as designed. Validation run offline replayed the hub's cached online answer ("No validation alerts" for values central flags); fixed: validation offline needs a connection, and the app says it could not be run (`TestValidationNeedsConnectionOffline`; FINDINGS.md 8). |
| I7c | pass after fix | 28 Oct | eRegisters 1.1.5 on 2.43.2 (`TestERegisters`, `agent-eregisters`): a client registered offline on one device was served to the second device offline but not stored: the hub's `createdBy`/`updatedBy` lacked first name and surname, which the app requires. Fixed in the hub's offline copy only, pushes unchanged (`TestOfflineAuthorAsCentralNamesIt`; FINDINGS.md 9). On central once, created by the first nurse. |
| I8c | **finding** | 29 Oct | Malformed payloads online and offline: all 4xx, hub up, nothing on central. A 60 MB payload (within the 64 MB limit) took the hub from 174 to 605 MB, six at once to 5.7 GB. Offline the hub confirms a million-digit number central refuses on push (E1302; kept) (`TestI8cLargePayloads`, `TestI8cMalformedPayloads`). |
| I9c | pass; **finding** | 29 Oct | 30 Android first downloads (20 users, about 5 200 people each) at once: online 2 min 33 s, offline 47 s, none failed or hung, peak RSS 348 MB. A new device whose first request comes while the store is locked is not registered once it is unlocked, and is refused offline (`TestI9cNewDeviceWhileStoreLocked`). |
| I10c | pass | 28 Oct | As N18. |
| J1 | **DHIS2 question** | 27 Oct | Central (2.43.2) accepts the nurse's `POST /api/dataValues` into an approved form (201, APPROVED_HERE), also without the hub (curl), so a value entered offline before the approval lands in approved data. The Data Entry app shows the form locked; after the sync the hub refuses new offline values (E2017). To explore on the DHIS2 side first; no hub change (`HUB_FINDINGS=1`). |
| J2 | pass | 29 Oct | Data set removed from the facility: central 2.42.7 still takes the value; the hub after the sync refuses it offline (409 E1100): a gap, not a leak. |
| J3 | pass | 27 Oct | Nurse and clerk offline and central edit one cell: the last write to reach central wins (pushes 3, 0, 0), every write is in central's audit with its author. |
| J4 | **finding** | 29 Oct | A data set that requires valid data cannot be completed offline. A completion queued while it did not, pushed after it does, is completed on central with a rule broken: central's completion endpoint does not validate (`TestJ4CompletionFailsValidationOnline`). |
| J5 | pass | 29 Oct | Data element moved to another category combination: central still accepts the default option combination; the hub after the sync does the same. |
| J6 | pass | 29 Oct | Expiry days set to 1 while last month's value waits: refused (E2017) and kept; after the sync the hub locks as central does. |
| J7 | pass | 29 Oct | Input period closed while a value waits: refused (E2025) and kept; after the sync the hub matches central. |
| K1 | pass | 27 Oct | Event program unassigned from the facility: two new offline events and an edit refused (E1029) and kept; pushes 3, 0, 0. The program leaves the catalog; offline listing answers 503, no leak. |
| K2 | pass | 29 Oct | Stage write sharing removed: 3 events refused (E1013) and kept; no leak online or offline; a new offline event refused (E1095). |
| K3 | pass | 27 Oct | Event deleted on central while edited offline: refused (E1082) and kept, the other edit on central. As A2: offline the hub still serves the event (200; Android download `deleted=false`), 404 online. |
| K4 | pass | 29 Oct | Attribute option sharing removed: events refused (E1099) and kept; after the next sync offline reads and writes follow central. |
| K5 | pass | 29 Oct | Retention set during work: pending events kept through the prune; pruned events read offline as 503, never 404, not listed as deleted. |
| L1 | pass | 29 Oct | A restricted user and one without the protected program sent crafted queries through the hub, online and offline: every org-unit mode, a 10 000 page, includeDeleted, a large fields list, by person type without a program, a filter on the unique attribute, single records by UID: 0 leaks. Where central refuses the query itself (409), the hub answers from facility data the same user sees on central (`TestL1CraftedQueriesForOthersData`). |
| L2 | **finding** | 29 Oct | Offline failure is uniform for known and unknown users (same status, 64–136 ms); one guess against a copied key slot costs 59–81 ms of Argon2id. Five wrong passwords lock the web login (429), even for the right password. But over Basic auth during the lockout, a wrong guess gets 429 at once and the right password 200 while the credential is cached: a fast password check from the facility network (`TestL2OfflinePasswordGuessing`). |
| L3 | pass after fix | 29 Oct | A nurse cannot write `central-move` as provisioned (403); with it writable by anyone the hub ignores it and contacts nothing. A forged "retired" costs offline service only and is undone by restoring the entry. A forged "revoked" in an unprotected registry entry made the hub delete its tokens and stop; recovery was a reset that discards unsent saves (`TestL3ForgedRegistryRevoked`). Fixed: "revoked" counts only once central refuses the hub's token (`TestForgedRevokedIgnored`; FINDINGS.md, "Found by adversarial testing" 14). Not yet run again on DHIS2. |
| L4 | pass | 29 Oct | A unique value pulled into the hub, then all 7 files of the data folder read: the value is in clear in none (`TestL4CopiedDiskReadElsewhere`). |
| L5 | pass | 29 Oct | Without a login, 13 data routes give no patient data online or offline; `/hub/setup` after set-up redirects to the login; `/hub/status` carries counts and UIDs only; 65 KiB anonymous bodies get 413 (`TestL5NoLoginGetsNothing`). |
| L6 | pass | 29 Oct | A trusted move announcement does not move a hub pinned to its central URL; a fake at central's address with a certificate the hub cannot verify gets 0 requests. With the transport fully subverted (plain http), the hub's token would reach it: verified TLS is the defence (`TestL6FakeCentral`). |
| M1 | pass (sandbox); 2 findings | 29 Oct | 10 h on synthetic 2.42.7 at about 4× a busy HC IV day, 5 uplink outages, 3 kill -9 restarts, housekeeping shortened: 10 211 saves confirmed; on central all 7 100 registrations once, whole, right values and authors. Memory 34–85 MB (peaks after restarts), files, goroutines and outbox flat; data folder grows with the data (6.6 MB/h). Found: concurrent logins leave an extra token on central; a person saved during a full pull is hidden offline from its author until the next pull (FINDINGS.md). A side test retired the hub for an hour, so a kill with a queued outbox ran only in the trial run (5 objects, fine). The 72-hour run on the Pi 5 is still to do (`TestM1Soak`, `HUB_SOAK`). |
| N1 | pass | 27 Oct | `2ab5f95` to this tree with registrations, an event-program event, an aggregate value, a refusal and a user's stuck save: all carried over; 287 records held offline before and after; 185 saves during the upgrade (max wait 1.4 s); 168 registrations, none lost or twice. |
| N2 | pass | 28 Oct | The oldest migratable release straight to this tree with 483 objects queued: upgraded in 14 s; 341 registrations, none lost or twice. |
| N4 | pass | 27 Oct | Killed during the download, the migration and the new release's first start: on the old, old and new release after each, never between; upgrade completed once; 144 registrations, none lost. |
| N5 | pass after fix | 28 Oct | Central hung as the new release started: its health check needed central, so it rolled back after 5.2 s and lost the 68–69 saves it had confirmed. Fixed: the health check tests the hub alone, and until it passes the new release takes no saves ("upgrading", clients retry) and pushes nothing. On 2.43: with central half-open the new release is kept (412 ms after the swap); a release built to fail its check rolls back with nothing lost. |
| N6 | pass | 28 Oct | Two hubs on different releases at once: 213 registrations on central, none lost or twice; both releases' heartbeat and registry match the app's types. |
| N7 | pass | 28 Oct | Saves stuck on the old release carried over the upgrade, refused with the reason a (backdated) week later; 159 other registrations, none lost. |
| N8 | pass; not documented | 28 Oct | Releases signed with a new key: refused until one signed with the old key carries the new key. No key rotation path is documented. |
| N10 | pass | 28 Oct | As G5. |
| N11 | pass (in part) | 28 Oct | Central's version read as 2.43.99 with saves queued: no "needs upgrade", queue pushed; a real patch upgrade needs its own instance. |
| N12 | pass | 28 Oct | Central upgraded in place from 2.42.7 to 2.43.2 (broker, 57 s on the synthetic database) with the uplink down and saves queuing: the queued saves reached the upgraded central, drained 8 s after; 460 saves, 302 registrations, none lost; the hub pulls central's edits on 2.43 and does not ask for an upgrade (`HUB_UPGRADE_CENTRAL=2.43`, an instance made for it). |
| N13 | pass after fix | 28 Oct | Central's version given as 2.44.0 on 2.43.2: 126 offline saves (249 objects) kept; online a read passes through (200), offline reads and saves 503 "hub needs upgrade"; the upgrade carries the queue over without pushing it (it crashed on a nil API before), and it drained 32 s after; 0 of 83 registrations lost. |
| N14 | pull: pass; push: **finding** | 28 Oct | Pull listing renamed: read as no changes, nothing skipped once central answers as before. Push report reshaped: 102 registrations stored on central marked refused, 48 later edits refused unsent (`TestN14PushReportReshaped`). |
| N15 | pass (in part) | 28 Oct | Tokens refused (401/404): the queue kept for their authors' online login; when central takes tokens again, drained, 232 registrations, none lost. |
| N16 | pass (in part) | 28 Oct | A data element taken out of the data set and back: the hub followed after one sync each. New uids need a real migration. |
| N17 | pass (in part) | 28 Oct | Version read as 2.44.0 then back to 2.43.2 with saves queued: 400 registrations, none lost. Restore part: H4. |
| N18 | pass | 28 Oct | Capture 107.8.5 installed on central while the hub was offline (`TestCaptureWebUpdate`): offline the cached 107.8.1 kept working (15 rows, dashboard); online 107.8.5 loaded; offline again 107.8.5 worked; no request failed. A patch release: no new endpoints to refuse. |
| N19 | pass | 28 Oct | Capture 107.8.5 (newest, from GitHub releases; the App Hub is not reachable from the sandbox) (`TestCaptureWebNewest`): 15/15/15/15/10 rows online and offline, dashboard, offline save on central once. |
| N20 | pass | 28 Oct | Data Entry 102.1.2 to 102.1.6 (GitHub release) installed on central while the hub was offline (`TestDataEntryAppUpdated`): old build kept and saving offline; new build on the second online load, then saving offline; all values on central as the nurse's. `TestAggregateDataEntryApp` passes on 102.1.6. |
| N21 | pass | 28 Oct | Capture 3.4.2 logged in to the hub, 3 registrations offline, updated in place to 3.5.0: opens on the home screen with them; synced through the hub, nothing refused; 3 on central once (`TestN21AndroidCaptureUpdated`). |
| N22 | pass | 28 Oct | Capture 3.2.1.2: 414 records straight from central and through the hub; 3 offline registrations reach central once; nothing refused but central's own 409s (`TestN22AndroidCaptureOlder`). |
| N23 | **finding** (in part) | 28 Oct | No newer app to install. The reset drops registry fields (G6); the app's type lacks `usb.unknown`, `usb.missing`, `usb.ask`. |
