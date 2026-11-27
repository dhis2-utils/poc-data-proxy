# Adversarial testing

The worst cases the hub is tested against: clients keep working through the hub while central, the network, the power, the clock and the users change under them. Each runs against a real DHIS2 (2.42 and 2.43), not only the fake central. It adds to the suites in TESTING.md; the automated scenarios are in `internal/adversarial`, and the ones with real apps in `internal/live`.

Findings and their fixes are in FINDINGS.md ("Found by adversarial testing"), each with the test that failed before the fix. The detailed plan, each scenario's status and the notes of the sessions on hardware are kept with the project's private notes.

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
| I8 | **Staff can tell what happened.** What each app shows is recorded, with a screenshot, and judged: a clear refusal is fine; a spinner for ever, a wrong success or a silent loss is not. | Screenshots from Playwright and the emulator, and the tester's notes |

## The test bed

- **Central:** disposable DHIS2 instances (2.42 and 2.43) with the Sierra Leone and Laos databases or the synthetic profile (`cmd/hubtest`), a fresh one per scenario family; for the hardware, an instance on the internet.
- **Hubs:** software hubs from the test kit, two at once where a scenario needs a second hub; a Raspberry Pi 5 (SSD), a Pi 4 (SD card) and a Windows PC.
- **Clients:** synthetic web and Android clients working from the instance's own metadata; Capture web, the Data Entry app and the Facility Hub app in a browser (Playwright); Android Capture on a tablet or an emulator; people on real devices where their eyes are the check.
- **The ledger:** every client records each save the hub confirms. After a scenario a checker compares it with central and with the hub's refused changes (I1, I2, I4).
- **Control:** central's metadata, users and sharing changed through its API; the hub's link to central cut, slowed, made lossy or half open, or made to lose answers (`testkit.Netcut`); the hub's process killed hard; on hardware, the cable and the power pulled.

## The scenarios

Where: **S** automated against a real DHIS2; **C** real apps (Playwright or Android); **P** on a physical hub; **M** by hand.

### A. Central's metadata changes while the hub holds offline work

The hub checks offline only what it can know; central decides on push. In each scenario, clients capture offline under the old rules, the change is made on central, then the uplink returns.

| ID | Scenario | Must hold, beyond I1–I8 | Where |
|---|---|---|---|
| A1 | A data element becomes compulsory in a stage; offline events lack it | Refused on push with central's reason, kept, shown in the app; the event's later edits wait behind it | S, C |
| A2 | An option is removed from an option set; offline data uses it | As A1 | S |
| A3 | A data element is removed from a stage | Central's answer recorded; no repeated push | S |
| A4 | A stage is made non-repeatable; two offline events in it | The second refused, the first kept | S |
| A5 | A program is removed from the facility (org unit assignment) | Offline work refused or accepted as central decides; the program leaves the hub at the next pull; nothing leaks after | S |
| A6 | A program's access level changes (open to protected) | Ownership rules apply from the next pull; offline reads of others' people stop | S |
| A7 | An attribute becomes unique; two offline registrations share a value | One refused with E1064, the other accepted | S |
| A8 | `onlyEnrollOnce` turned on with an enrollment made offline | As central decides | S |
| A9 | A category option's end date passes, or its sharing changes (event programs with attribute combos, aggregate) | Refused or accepted as central; FINDINGS says sharing changes reach the hub weekly: measure what happens in between | S |
| A10 | A program's expiry days or a data set's expiry passes while offline | Refused with central's reason; the hub's own lock agrees with central's | S |
| A11 | The facility org unit is closed (closed date) | Everything after the date refused; the hub says so; it keeps working for reads | S, P |
| A12 | The facility moves to another district (parent changes) | Users' scope recomputed; nothing leaks | S |
| A13 | A tracked entity type attribute is added, removed or made searchable | Searches and registrations follow at the next sync | S |
| A14 | Program rules change (a new rule hides a field or assigns a value) | Capture offline uses its cached rules; record what central does on push | C |
| A15 | Metadata changes many times during one outage | The hub's catalogue matches central after one sync; no stale cache | S |
| A16 | A large metadata import on central during a hub pull | The pull completes or retries cleanly | S |
| A17 | A data element's or attribute's value type changes (text to number, number to option set); offline data has the old type. First check whether central allows the change with data stored | As A1; the hub's offline answers follow the new type after the next sync | S |
| A18 | A program attribute is made mandatory; offline registrations, and edits of people registered before, lack it | As A1 | S, C |
| A19 | A stage's validation strategy changes from ON_COMPLETE to ON_UPDATE; offline events lack a compulsory data element and are not completed | As A1: with ON_UPDATE the active event itself is refused (A1 covered ON_COMPLETE only) | S |
| A20 | A program's access level changes to CLOSED (from open, and from protected) | As A6. The hub holds ownership and reads it offline: no reads or writes of others' people offline that central would refuse, and no break-the-glass offline | S |
| A21 | A program's "allow future enrollment/incident dates" changes during an outage | The hub checks these offline with the setting it last read (F4): turned off, central refuses at push (a refused change, kept); turned on, the hub refuses offline what central would now take. Record both, and how long the stale setting lasts | S |
| A22 | A generated attribute's pattern changes (RANDOM to SEQUENTIAL, a new prefix) while the hub and tablets hold values reserved under the old one | Registrations with old values accepted or refused as central decides; no duplicate IDs; the hub's pool follows the new pattern after the next sync | S, C |
| A23 | A relationship type changes: its from/to constraints, bidirectional, its sharing (write removed), or it is deleted | Offline relationships refused or accepted as central decides; nothing applied twice | S |

### B. Users and access change

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| B1 | A user loses the facility (org unit removed) while working offline | Their offline saves: as central decides on push. Their offline reads: stop at the next sync | S, C |
| B2 | A user is disabled on central while offline | Can no longer log in once the hub knows; their queued saves: pushed with their token or refused and kept, never lost silently | S |
| B3 | A user's password is changed on central | Offline login with the old password until the hub learns; documented | S, M |
| B4 | A user's personal access token is revoked or expires with saves queued | Saves wait, "needs online login" shown; pushed after they log in online | S |
| B5 | A role loses an authority (for example tracked entity delete) | Refused offline at the next sync, as central | S |
| B6 | Program or stage sharing removed from a user group | Offline reads and writes stop at the next sync; queued writes refused and kept | S |
| B7 | A user moves to another facility with a hub | Sees only the new facility's data, on each hub, after login | S |
| B8 | A user is deleted on central with saves queued | Saves refused and kept, visible to a manager; never pushed as someone else | S |
| B9 | A brand-new user arrives while the facility is offline | Cannot log in (first login must be online); clear message | S, C |
| B10 | Two users swap devices or share a tablet | Each sees only their own access; no cached data from the other (Capture's browser cache, Android's database) | C, M |
| B11 | User group membership changes for many users at once | Access refreshed for all within the refresh interval; measure the gap | S |
| B12 | A user's search scope changes (search org units) | Offline search follows at the next sync | S |

### C. Ownership, conflicts and duplicates

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| C1 | The same person edited on central and on the hub offline | Recorded: which edit wins on central, and what the hub shows after the pull; no silent loss of either without a trace | S |
| C2 | A person deleted on central while edited offline | Edit refused (E1114) and closed; the person disappears from the hub | S |
| C3 | A person transferred to the sibling while the hub is offline, then edited offline | Refused on push, kept, shown | S |
| C4 | Transferred through the hub while central transfers the person elsewhere | One owner at the end, the one central holds | S |
| C5 | The same person registered offline at two facilities (two hubs) | Both pushed or one refused by uniqueness; record which; no merge loss | S |
| C6 | A duplicate registered offline on one hub by two staff | As C5 on one hub | S |
| C7 | A relationship to a person the hub does not hold | Refused clearly or accepted as central | S |
| C8 | Break the glass online, then the uplink drops | The temporary access ends as central's does; nothing more is shown offline | S |
| C9 | An enrollment completed on central while events are added offline | As central decides | S |
| C10 | A one-time referral (Capture's referral that leaves the owner as it is): a person referred from or to the facility, online and offline; the referred event made and then completed at the receiving facility | The receiving facility sees what central lets it see and no more, online and offline; the referral's event reaches central once; the owner does not change | S, C |

### D. The network

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| D1 | The uplink drops in the middle of a push | No duplicate on retry (I2), nothing lost (I1) | S |
| D2 | Central acts on a save but the answer is lost | The retry recognises it; no duplicate | S |
| D3 | The uplink flaps every few seconds for an hour | The hub settles; the outbox drains; no storm (I7) | S, P |
| D4 | A very slow link (2G, 20 kB/s, 2 s latency) | Pulls and pushes complete or resume; pages for staff stay local | S |
| D5 | Half-open connections (packets stop, no reset) | Timeouts free the hub; it goes offline and back | S |
| D6 | DNS fails while TCP works, or the reverse | Treated as offline; recovers | S |
| D7 | A captive portal or a hotel-style login page in front of the internet | Not mistaken for central (ping must say "pong") | S, P |
| D8 | Central's certificate expires, or a wrong certificate is presented | The hub refuses; stays offline; says why | S |
| D9 | An outage of three weeks with continuous work | Pulls and pushes complete afterwards; tokens still valid or refreshed; disk and outbox sized | S (time-compressed), P |
| D10 | The facility network goes down but central stays up (devices cannot reach the hub) | Devices fall back as each app does; nothing is lost when they reach the hub again | M |
| D11 | The uplink returns while a client is mid-save | The save lands once | S |
| D12 | Physical: cable pulled at random moments during a working day | As D1–D11 on real hardware | P |

### E. Power, crashes and storage

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| E1 | Power cut during a save | Confirmed saves survive (I1); an unconfirmed save is either complete or absent | S (kill -9), P |
| E2 | Power cut during a push | No duplicate on central after restart | S, P |
| E3 | Power cut during the first pull | Resumes; no half-stored people | S, P |
| E4 | Power cut during a migration or an upgrade | The hub comes back on the old or the new release, never in between | S, P |
| E5 | Power cut while the hub copies to the USB stick | The stick's copy stays usable or is replaced | P |
| E6 | Ten power cuts in an hour | As E1; the data file never corrupt | P |
| E7 | The disk fills up | Saves refused clearly, nothing corrupt, the hub warns; recovers when space returns | P (Windows VM), S if a size-limited folder is possible |
| E8 | The SD card fails or turns read-only | The hub says so; restore from the stick works | P |
| E9 | The USB stick is pulled during a copy, or a different stick is inserted | No secret leaks to the wrong stick; copy resumes | P |
| E10 | The disk moved to a new board | The hub carries on as itself | P |
| E11 | Windows: sleep, hibernate, Windows Update restart, antivirus quarantine of hub.exe | The service comes back, or the facility is told; the store unlocks at next login | P (VM), M |
| E12 | Memory pressure (other programs on the Windows PC use most of it) | The hub slows but does not die or lose data | P (VM) |

### F. The clock

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| F1 | The hub's clock hours or days behind central (Pi 4 without a clock battery, after a power cut with no network) | Saves still accepted by central; dates on records come from clients; periods and expiry judged correctly once synced | P (Pi 4 off overnight, then no network), S if the hub's clock can be offset |
| F2 | The hub's clock ahead | As F1 | P (Windows VM: set the clock) |
| F3 | The clock jumps while the hub runs (network time arrives) | No duplicate pulls, no lost watermarks, sessions survive | P |
| F4 | A client device's clock is wrong (tablet set to last year, or a day ahead) | As central would decide; the hub's page names the user whose device runs ahead | M |
| F5 | A month or year boundary while offline (aggregate periods open and close) | Forms lock and open as central would | S (central's date), P |

### G. The hub's own lifecycle

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| G1 | Upgrade to a new release with saves queued and clients working | Nothing lost; clients see a short refusal at most | S, P |
| G2 | The new release fails its health check | Rolls back on its own; data intact | S, P |
| G3 | An unsigned or tampered release offered | Refused | S |
| G4 | The country picks an older release (downgrade) | Refused, as designed | S |
| G5 | Upgrade while offline for weeks, then online | Upgrades after the outbox is pushed, not before | S |
| G6 | Reset the hub with refused changes kept | As designed (TestResetKeepsRefusedChanges) on a real central, with a unique ID that central gave another person meanwhile | S |
| G7 | A replacement hub set up for the same facility while the old one still runs (two hubs) | The old one retires; nothing pushed twice; the new one pulls everything | S, P |
| G8 | Two hubs at sibling facilities, people moving between them | TESTING.md phase 6 scenarios | S |
| G9 | The hub's own account revoked on central | The hub keeps serving staff; housekeeping taken over; says so | S |
| G10 | Nobody logs in after a restart for days (store stays locked) | Clients told; nothing lost; first login unlocks | S, P |
| G11 | All staff key slots lost (every password changed on central) | Recovery as RECOVERY.md; or documented as the limit | S |

### H. Central's lifecycle

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| H1 | Central down for maintenance (nginx 503) for hours | Hub offline after two pings; staff keep working; no storm | S (broker stop), P |
| H2 | Central very slow (analytics running, 30 s answers) | Hub stays usable; its sync completes later | S |
| H3 | Central upgraded 2.42 → 2.43 with hubs holding queued saves | Hubs on a release that supports 2.43 push as before; a hub that does not support it says "needs upgrade" and passes through | S |
| H4 | Central restored from yesterday's backup | The hub notices central went back in time; pushes are not lost; watermarks do not skip data | S |
| H5 | Central moved (announced, redirected, typed) | As move tests, on a real central | S |
| H6 | A system setting changes (server time zone, allowed future dates, password policy) | Followed at the next sync | S |
| H7 | Central's PAT or session lifetime settings shortened | Hubs renew in time | S |
| H8 | Central's database deadlocks or returns 500s for some imports | Those entries retried later, not lost, not looping | S |

### I. Clients

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| I1c | Capture web: what each refusal looks like (needs connection, refused, upgrading) | Recorded with screenshots (I8); nothing appears saved that is not | C |
| I2c | Capture web cached across users on one browser | No other user's data shown | C |
| I3c | Android: switch a tablet with unsynced data from central to the hub | Its data reaches central once | C, M |
| I4c | Android offline for weeks, then syncs through the hub | As above, with a large backlog | C, M |
| I5c | Android's reserved values (generated IDs) run out offline | Clear message; no duplicate IDs | C |
| I6c | Data Entry app: drafts, completion, validation offline | As FINDINGS; screenshots | C |
| I7c | eRegisters: full pull and register offline | As FINDINGS | C |
| I8c | A client sends malformed or very large payloads | Refused, the hub survives | S |
| I9c | Many devices at once start their first download after a reset | The hub serves them; no device times out for ever | S, P |
| I10c | Capture app upgraded on central while the hub is offline | The cached app keeps working offline; the new one loads online | C |
| I11c | Images and files: added and viewed online and offline, in Capture web and on Android | As FINDINGS, "Images and files": nothing saved that central does not get, nothing silently lost | C |

### J. Aggregate data

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| J1 | Data approved on central while values were entered offline | Refused and kept on push; shown | S |
| J2 | The data set removed from the facility while offline | As central decides | S |
| J3 | The same cell edited offline by two staff and on central | Last write wins as on central; each write traceable | S |
| J4 | Completion offline of a form that fails validation online | Refused and kept | S |
| J5 | A category option combo removed | Refused and kept | S |
| J6 | Period boundary and expiry days while offline | As F5 | S |
| J7 | Data input periods changed on central | Followed at the next sync | S |

### K. Event programs

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| K1 | Events captured offline in a program closed on central | As central decides | S |
| K2 | Stage access lost | As B6 for events | S |
| K3 | An event deleted on central while edited offline | Refused (E1082) and closed: the event leaves the lists, and Android's download marks it deleted | S |
| K4 | Attribute option combos: option sharing changed | As A9 | S |
| K5 | Retention prunes events during a working day | Pending events never pruned; nothing a client holds disappears | S |
| K6 | Android downloads event programs through the hub | Works as on central | C |

### L. Security abuse

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| L1 | A user asks the hub for another user's data with crafted queries (filters, fields, paging) | Nothing beyond central (I3) | S |
| L2 | Offline password guessing | Throttled; no key slot opened | S |
| L3 | A forged entry in the hub registry or move announcement on central | Ignored; protected namespaces as OPERATIONS.md | S |
| L4 | A stolen disk or stick read on another machine | No personal data in clear | S, P |
| L5 | A device on the facility network that is not staff | Gets nothing without logging in | S |
| L6 | A fake central (DNS spoofed on the facility network) | The hub does not send tokens or data to it | S, P |

### M. Load and soak

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| M1 | 72 hours of continuous clients with an outage every few hours and a power cut a day | I1–I8; memory, disk and the outbox stay flat | P (Pi 5), S |
| M2 | Retention pruning, a full pull and an upgrade during a busy day | As above | S |
| M3 | The same against a Laos-sized real instance | As above | S |

### N. Updates: the hub, DHIS2 and the apps

Every part of the system changes under the others: the hub gets releases, central gets DHIS2 patches and new majors, and the apps the clients run are updated on central or in the Play Store, each on its own schedule. Groups G and H name a few of these; this group covers them all. Versions: the hub's last release and the next; DHIS2 2.42 and 2.43, each at its oldest and newest patch; each app at the version bundled with that DHIS2 and the newest on the App Hub; Android Capture at the version in use and the newest.

**The hub's releases**

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| N1 | Upgrade from the last release with a full outbox, refused changes, stubs and every kind of record present, clients working | Every record and setting carried over; the outbox pushes after; clients see at most a short refusal | S, P |
| N2 | Upgrade skipping releases (the oldest still in use straight to the newest) | All migrations run in order; as N1 | S |
| N3 | A migration on a full store at the HC IV size, on a Pi and on Windows | Finishes within the time AGENTS.md asks; the hub serves staff or says it is upgrading | P |
| N4 | Power cut or kill during download, during the migration, and during the first start of the new release | Back on the old or the new release, never between; no data lost (E4) | S, P |
| N5 | The new release fails its health check after the migration ran | Rolls back to the old binary and the old store; nothing written in between is lost | S |
| N6 | Two hubs of a country on different releases at once (a staged rollout) | Both work with central and with the Facility Hub app | S |
| N7 | A release that refuses something the old one accepted (stricter checks) with such saves queued | Queued saves still pushed or refused with a reason, never dropped | S |
| N8 | The release key changes (a new signing key) | Hubs on the old key accept the new one only through the documented path; nothing unsigned runs | S |
| N9 | Windows: the service upgraded while Windows Update restarts the PC; antivirus holds the new hub.exe | The service comes back on one of the two releases; the store stays unlocked or asks for a login | P (VM) |
| N10 | An upgrade offered while the hub has been offline for weeks | Downloaded and applied only after the outbox is pushed (G5) | S |

**DHIS2 core**

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| N11 | A patch release of central (2.42.x to 2.42.y) with hubs holding queued saves | Hubs carry on; the oracles still pass | S |
| N12 | A new major (2.42 to 2.43) while hubs are offline with queued saves, on a hub release that supports both | Saves pushed after; reads and the oracles pass on the new major | S |
| N13 | A new major the hub's release does not support | The hub stops answering offline, passes everything through and shows "needs upgrade" (SPEC); no queued save lost; after the hub is upgraded, they push | S |
| N14 | A new major changes an answer the hub relies on (a field renamed, an endpoint moved, an error code changed, the tracker import report reshaped) | Found by the oracles and the hub's own checks before a country upgrades; a hub that meets it refuses rather than misreads | S |
| N15 | Central's personal access tokens or session handling change in a new major | Hubs keep their tokens or get new ones at the next login, as TESTING.md's token tests | S |
| N16 | Central upgraded and its metadata migrated (new uids for categories, options sorted differently) | The hub's catalogue matches central after one sync | S |
| N17 | Central rolled back to the previous major from a backup after a failed upgrade | As H4: nothing pushed is lost; watermarks do not skip data | S |

**The apps the clients run**

| ID | Scenario | Must hold | Where |
|---|---|---|---|
| N18 | Capture web updated on central (App Management) while the hub is offline | Browsers keep the cached app offline; the new one loads online; a new endpoint the hub cannot answer gives "needs connection", never wrong data | C |
| N19 | Capture web at the newest App Hub version against the hub | The same checks as the bundled version (working lists, dashboard, saves offline) | C |
| N20 | The Data Entry app updated, as N18 and N19 | As N18 and N19 | C |
| N21 | Android Capture updated on the tablet (newer SDK) while it holds unsynced data from the hub | Its data reaches the hub and central once; the new SDK's requests are answered or refused clearly | C (tablet) |
| N22 | Android Capture at an older version than the hub was tested with | As N21 | C (tablet) |
| N23 | The Facility Hub app updated on central while hubs run | Hubs and the app still agree on the dataStore formats (registry, approvals, retention, retries) | S |
| N24 | eRegisters updated | As N19 | C |

How: the hub's old releases are the signed GitHub releases (or local builds of their tags); DHIS2 versions come from the broker's seeds (2.42 and 2.43), and an upgrade is a database dump of one instance restored into an instance of the next version, if the broker cannot upgrade an instance in place; app versions are installed from the App Hub into the test instance; Android versions are APKs installed on the tablet with adb.
