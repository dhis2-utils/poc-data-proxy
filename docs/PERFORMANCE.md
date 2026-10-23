# Performance: a Raspberry Pi-class hub under load

Question: can a hub on Raspberry Pi-class hardware serve **60 users working offline at once** at a busy urban HC IV, **300 000 people, about 0.5 million enrollments and 1.2 million events** (the sizing below, with retention by programme), and how far towards the pessimistic **700 000 people, 1.2 million enrollments and 3.1 million events** does it go? And what does a fleet of hubs ask of central?

## Where we stand (2026-10-14)

**Results** (`perf/pi.sh ladder`, the container profiles below; 60 users, 18 of them Android with 500 people per program; latencies offline, p50 / p95):

| | `pi5` (4 GB, 1.6 CPU), 100 k people | `pi5`, HC IV (300 k) | `pi4` (1 GB, 0.8 CPU, SD card), 30 k | `pi4`, 100 k |
|---|---|---|---|---|
| First pull, 60 users | 4 min | 15 min | 12 min | 48 min |
| Working list | 12 ms / 1.4 s | 13 ms / 7.8 s | 12 ms / 1.4 s | 0.5 s / 20 s |
| Dashboard, person | 5 ms / 2.4 s | 6 ms / 11 s | 5 ms / 2.2 s | 0.6 s / 19 s |
| New event | 8 ms / 1.0 s | 9 ms / 9 s | 9 ms / 1.4 s | 0.6 s / 26 s |
| Peak memory (mostly file cache) | 2.7 GB | 3.4 GB | 0.8 GB | 0.8 GB |
| Errors | none | none | none | none (hammer run could not start) |

The p95s in these runs come from 18 Android first downloads at once at the start of the run; outside that window answers take milliseconds. The ladder's realistic run now models a working day (devices downloaded before) and keeps the all-at-once downloads for the hammer run.

**Second ladder (2026-10-14).** `pi5` ran out of memory at 100 k and HC IV in the working-day run: with no first downloads, Android incremental syncs started at once, and their `updatedAfter` check decoded every enrollment and event of every candidate person and kept them for the request (History). Fixed, not yet re-run on the ladder. `pi4` (1 GB, SD card limits) with 20 users:

| `pi4`, 20 users | 10 k | 30 k | 60 k |
|---|---|---|---|
| First pull | 1 min 45 s | 5 min 54 s | 17 min |
| Working day: dashboard, person (p50 / p95 / p99) | 3 ms / 9 ms / 36 ms | 4 ms / 45 ms / 67 ms | 4 ms / 319 ms / 9.2 s |
| Working day: working list | 6 ms / 61 ms / 77 ms | 9 ms / 291 ms / 340 ms | 11 ms / 172 ms / 3.9 s |
| Working day: Android incremental sync (p50) | 7.7 s | 29 s | 1 min 37 s |
| Hammer: web pages (p50 / p95) | 70–100 ms / 120–290 ms | 75–100 ms / 160–290 ms | 75–100 ms / 125–280 ms |
| Peak memory | 227 MB | 478 MB | 866 MB |

The 1 GB knee is between 30 k and 60 k people; the incremental syncs at 60 k ran with the memory problem above.

**Conclusions so far**

- **Hub memory is solved.** The hub holds about 20 MB of its own whatever the data size; RAM serves as file cache. One path held records in proportion to the facility, Android's `updatedAfter` check, until 2026-10-14 (History).
- **Speed depends on whether the data file fits in RAM, or the disk is fast.** At 100 k people the file is about 2.5 GB, at the HC IV size 6–7 GB (about 3.3 KB per object). A Pi 5 with 4 GB serves 100 k people well and the HC IV size for normal use, with little headroom under heavy load. A 1 GB device with an SD card serves small facilities (30 k people worked well; the knee between 30 k and 100 k is still to find) but not 100 k.
- **Android first downloads are the heaviest load**: 18 at once saturate a Pi 5 for a minute or more. Pages are cheaper now (History, 2026-10-14); a deployment can spread first downloads.
- **Load on central** fell from about 214 000 requests a day per hub to about 9 400 (best case), about 54 requests a second for 500 hubs (section "Load on central").
- **Correctness** held: on 2.42 the oracles pass on all three profiles with every change through 2026-10-14 (shared listings, the type pull, the `updatedAfter` fix): no LEAK or DIFFERS in reads (MATCH 937 synthetic, 627 Sierra Leone, 776 Laos), and no DENIED in writes (OVER-REJECT only where the hub refuses offline what central would accept: 5 synthetic, 8 Sierra Leone events in an enrollment the sibling facility owns, by design).

**Open**

- The ladder again with the `updatedAfter` fix: `pi5` at 100 k and HC IV; `pi4` at 60 k.
- The HC IV size with 8 GB (`MEM=8g`), to confirm that RAM limits the hammer run there.
- A smaller data file (about twice the in-memory design's) and a faster first fill on SD cards (a larger cache during pulls, fewer indexes).
- The paced run: writes and reads at the per-hour rates below (peak 5 × average), with bytes written per day for SD card wear.
- Calibration on a real Pi (`perf/pi.sh bench`).
- Tracker retention on `pi5` (measured in process at 100 k and at the HC IV size: "Retention for tracker programs" below).

## On a Windows PC (2026-10-22)

The hub as a Windows program, on a small PC: a Windows 11 VM with **2 CPUs and 4 GB**, on an NVMe disk, no TPM. It is **Windows 11 on ARM** (UTM on an Apple Silicon Mac), so the hub, built for x86-64 like the installer, ran under Windows' x64 emulation: the times include its cost, and a real x86-64 PC of the same size is likely faster. Errors, memory, the reconnect and the power-cut results do not depend on it. `perf/windows/stress.ps1` runs the ladder's two runs there with 20 users: a second hub beside the installed one, the fake central and `hubload` on the same PC (they take CPU from the hub; on the Pis they run elsewhere), the data folder excluded from Defender as the installer does. Hub `0.1.5` (`376ce5a`); latencies offline, p50 / p95.

| Windows VM, 20 users | 10 k | 30 k | 100 k |
|---|---|---|---|
| First pull of all users | 1 min 49 s | 4 min 0 s | 14 min 18 s |
| Working day: dashboard, person | 10 ms / 37 ms | 8 ms / 19 ms | 13 ms / 94 ms |
| Working day: working list | 23 ms / 261 ms | 22 ms / 243 ms | 33 ms / 205 ms |
| Working day: Android incremental sync (p50) | 7.2 s | 22 s | 1 min 38 s |
| Hammer: web pages (p50 / p95) | 69–228 ms / 243–614 ms | 75–212 ms / 199–577 ms | 90–258 ms / 236–643 ms |
| Hammer: 6 Android first downloads at once (3 000 people each) | 28 s | 21 s | 22 s |
| Reconnect: online after the uplink returns; outbox empty after | 29–32 s; 11–13 s | 4–12 s; 0–2 s | 4–10 s; 0–2 s |
| Peak memory of `hub.exe` (working set) | 225 MB | 228 MB | 300 MB |
| Data folder | 0.3 GB | 0.7 GB | 2.2 GB |
| Errors, warnings in the hub's log | none | none | none |

The 10 k column ran before the index on unsynced objects (History, 2026-10-22); its reconnect includes the slow status count. Against `pi4` (1 GB, the ladder above) the VM is about the same in the first pull, a little slower at the median and faster at the tail; it serves 100 k people well for web users. As on the Pis, Android incremental syncs are the slow part from 30 k on (open above).

**Power cuts on Windows** (2026-10-22): `perf/windows/powercut.ps1` on the same VM with `powercut-host.sh` on the Mac, which cut the VM 4–8 minutes after each start, 10 times (8 times UTM's "Force kill" of the VM's process, twice "Force shut down"), while 20 users saved offline with no pauses. At each start-up every save the hub had confirmed so far was read back, offline, as its author: **9 759 confirmed saves, none missing**, in every one of the 10 verifies. The log of confirmed saves stayed on the VM's own disk, written through with each save (on the Mac's shared folder a killed VM lost what it had last written there). A VM's cut is milder than a real one: what the Mac had already received from the VM survives it. Real power cuts are for the Pis and a physical PC (HARDWARE-TESTING.md, "Power cuts").

## How the hub scales

- **One store.** Objects live only in the encrypted SQLite data file: each row has the columns lists filter and sort on, its slim JSON, and its sealed payload; attribute values are in `te_attrs`. Searches put every condition columns decide into SQL, read in growing chunks in the requested order where SQL can give it (creation date) and stop at a full page; other orders sort in Go. `TestQueryPlansUseIndexes` fails if a query scans a table.
- **Visibility as bitmaps** over stable object slots, per user, in memory and in 512-byte chunks on disk: under 5 MB for 60 users and 750 000 objects.
- **ID-list pulls.** A pull lists identifiers and update times; only people the hub lacks, holds older, or has not fetched for that program are fetched in full, 50 per request, each record once. Users whose access lists are the same apart from who they are share one listing (`access.List.GroupKey`; programs with an attribute category combination stay per user). Ordering a first pull most recent first, across programs, was tried and removed: with shared listings the first pull is short enough (15 minutes at HC IV size on `pi5`) not to need it.
- **Paging.** Every page stops reading at its end, and a page that follows the one just served reads on from that page's last row (its creation key), so a page costs the same at any depth. A page that does not follow one reads from the start up to its end.
- **Rendering** reads payloads in batches, copies only their top level, and keeps decoded payloads (4 096, keyed by uid and nonce, so a rewritten object always misses).
- **Writes** are serialised in the hub (one write lock), prepared once, and synced at each commit.

## A device's full download

eRegisters downloads every person of every program, 50 per page, with no limit (docs/FINDINGS.md); Android Capture stops at 500 per program by default. `internal/perf.TestFullDownload` does the same against an offline hub, 6 programs, in process on a development machine (unconstrained; a Pi is several times slower):

| People | Pages | Page 1 | Middle page | Last page | One device's download |
|---|---|---|---|---|---|
| 10 k | 336 | 17 ms | 24 ms | 19 ms | 7 s |
| 30 k | 1 002 | 17 ms | 36 ms | 93 ms | 36 s |
| 100 k | 3 342 | 22 ms | 160 ms | 310 ms | 8 min 40 s |
| 300 k (`hc4`) | 10 002 | 25 ms | 450 ms | 0.9 s | 1 h 14 min |

Each page read every row before it (page *n* reads *n* × 50 matches, since visibility is decided in Go row by row), so a download grew with the square of the facility. The hub now remembers, per user and query, the last row of the page it served, and reads the next page on from that row's creation key (`pagePositions`, History 2026-10-15):

| People | Before: one device's download | After | Every page |
|---|---|---|---|
| 100 k | 8 min 40 s | 1 min 2 s | 11–23 ms |
| 300 k (`hc4`) | 1 h 14 min | 4 min 10 s | 17–36 ms |

## Load on central

`internal/perf.TestCentralLoad` counts what one hub asks of central (every request through the test kit's uplink, by kind, with answer sizes) during setup and first fill, a sync cycle in which nothing changed and a full pull, and projects a day at the deployed intervals and N hubs (`HUB_PERF_HUBS`, default 500). At 10 000 people, 6 programs and 60 users:

| Per hub per day | Requests | Data | 500 hubs |
|---|---|---|---|
| Sync every 5 min, access refreshed every cycle, daily full pull, per-user listings | 214 000 | 1 GB | 1 240 requests/s |
| Sync every 15 min (±10 %), access refreshed hourly and at login, weekly full pull | 53 000 | 153 MB | 310 requests/s |
| Listing once per access group | 9 400 | 32 MB | 54 requests/s |

Aggregate data adds, per access group and cycle, one `dataValueSets` and one `completeDataSetRegistrations` request and one request per form that changed on central, and once a day one per held form (a few hundred at a facility with 26 data sets and 12 months kept).

The generated users share one access, so the last row is the best case; a real facility has a handful of access groups. What remains is mostly the hourly access refresh (four reads per user). After an outage each hub waits up to five minutes at random before syncing, so hubs do not reach central in the same second: 500 hubs start under two pushes a second. Until its outbox is pushed, a hub keeps answering tracker requests itself, so the wait is invisible to staff (FINDINGS.md, "Reconnecting").

## Running the tests

In process on a development machine (unconstrained), with `HUB_PERF_SCALE="tes,enrollments,events"` and `HUB_PERF_USERS`:

- `TestPull`: first pull, heap, disk, reboot to first login.
- `TestQueries`: working lists, searches, Android pages, dashboards (`HUB_PERF_ONLY` and `HUB_PERF_RUNS` to profile one kind).
- `TestCentralLoad`: what the hub asks of central.
- `HUB_PPROF=127.0.0.1:6060` on a running hub serves Go's profiler there (loopback only), for looking into a slow case.

In containers on the Docker host (`perf/pi.sh`, from the repository root):

```sh
sh perf/pi.sh build                 # image: hub, fake central, hubload, benchmarks
sh perf/pi.sh ladder pi5            # per size: a fresh hub, filled, a realistic and a hammer run
sh perf/pi.sh bench pi4             # calibration: storage benchmarks and fio in the profile's limits
```

`SIZES="name:tes:enrollments:events …"` picks the sizes (default 30 k, 100 k, `hc4`, `hc4-5y`, `pessimistic`), `USERS` the users (60), `MEM` a different memory limit, `IO=0` no disk limits, `RUN=name` a run alongside others. Each run writes `perf/results/ladder-<profile>-…/` with `summary.txt`, the load reports, memory and CPU samples, and each size's hub log.

| Profile | Models | `--cpus` | `--memory` | Disk |
|---|---|---|---|---|
| `pi4` | Pi 4, 1 GB, SD card | 0.8 | 1g | 10 MB/s and 500 IOPS write, 40 MB/s and 1 500 IOPS read |
| `pi5` | Pi 5, 4 GB, USB SSD | 1.6 | 4g | unrestricted |

`--cpus` caps CPU time, not per-core speed (the results so far come from an Apple Silicon Mac, whose cores are about 2.5–3 times a Pi 5's), and with Docker Desktop the disk limits apply to its VM's disk and may have no effect: calibrate both against a real Pi with `bench`. The data lives on a Docker volume so the limits apply to it.

**The load** (`cmd/hubload`): the users log in online, the uplink is cut (the fake central refuses connections), they work offline, the uplink returns and the push is timed in steps (uplink restored, hub online, outbox empty).

- **Web users** (70 %): working list, search by name, dashboard, a new event; 2–10 s between actions in the realistic run, none in the hammer run.
- **Paced** (`-paced`): instead of users clicking, one day's volume of a busy HC IV spread evenly over `-duration`, for the bytes written per day (docs/HARDWARE-TESTING.md). With `-confirmed FILE`, every write the hub confirmed is logged, and `-verify FILE` reads them back after a power cut.
- **Android users** (30 %): a first download of up to 500 people per program (`-android-limit`), then incremental syncs every 5 minutes (`-android-sync`) with an upload. `-android-first` sets when first downloads happen: `all` at the start at once (deployment day), `spread` over the first half of the run, `none` (done before: a working day). The ladder's realistic run uses `none`, its hammer run `all`.
- **A flapping uplink** (`-flap 10m/5m`): during the load the uplink goes down and up, starting down. While it is up the users' requests pass through to the fake central and the hub syncs; each restore is timed (hub online, outbox empty) while the users go on.

**On real hardware, with the load from the Mac** (`perf/remote-load.sh`): the fake central and the users run on the Mac, so they take nothing from the machine under test, and the hub there points at the Mac's fake central. `perf/remote-load.sh <hub URL> <size> [users] [duration] [flap]` waits for the hub, sets it up, times the first pull, runs a working day with the uplink flapping, a hammer run, and reads every confirmed save back. On Windows the hub's half is `stress.ps1 -Size <size> -Central http://<Mac>:8090` (kit: `perf/windows/kit.sh`, `ARCH=arm64` for Windows on ARM), which also samples the disk's latency and queue. A Pi needs a way to point its hub at the Mac's fake central first (no shell on the image).

**Proposed pass criteria** (realistic run, `pi5`): working list and search p95 < 1 s; dashboard p95 < 500 ms; event write p95 < 300 ms; no errors; hub memory < 70 % of the limit, stable over an hour; startup after a reboot < 2 min; an Android first download < 10 min. The hammer run has none: it shows where the hub saturates and that it slows rather than fails.

## Sizing for a busy urban HC IV

An effective catchment of about 250 000 people, 250 working days a year, a 5-year horizon, each service its own tracker program:

| Program | Peak clients / day | Enrollments / year | Events / year | Enrollments, 5 yrs | Events, 5 yrs |
|---|---|---|---|---|---|
| OPD | 800 | 120 000 | 200 000 | 600 000 | 1 000 000 |
| Inpatient | 30 admissions | 7 000 | 30 000 | 35 000 | 150 000 |
| ANC | 150 | 6 000 | 36 000 | 30 000 | 180 000 |
| Maternity / delivery | 25 | 4 000 | 12 000 | 20 000 | 60 000 |
| Postnatal | 60 | 4 000 | 12 000 | 20 000 | 60 000 |
| Immunisation | 400 | 7 000 | 50 000 | 35 000 | 250 000 |
| Family planning | 100 | 5 000 | 20 000 | 25 000 | 100 000 |
| HIV testing | 150 | 20 000 | 20 000 | 100 000 | 100 000 |
| HIV care / ART | 300 | 800 new + 6 000 existing | 48 000 → 72 000 | 10 000 | 300 000 |
| TB | 40 | 500 | 5 000 | 2 500 | 25 000 |
| Nutrition | 80 | 4 000 | 16 000 | 20 000 | 80 000 |
| NCD (hypertension, diabetes) | 150 | 1 500 new + 3 000 existing | 40 000 → 100 000 | 10 500 | 400 000 |
| Laboratory | 400 requests | 50 000 | 80 000 | 250 000 | 400 000 |
| **Total** | ~2 700 contacts (~1 500 unique clients) | ~230 000 | ~570 000 → 650 000 | ~1.2 M | ~3.1 M |

- **Backlogs:** ART and NCD start with existing patients, and their visits grow each year. **Pharmacy** is folded into the other programs. **Episodes:** OPD, HIV testing and the laboratory enrol per episode. **Peak day:** summing the per-program peaks is deliberately pessimistic. **Comparison:** the busiest HC IV in the 27 September use report (Butenga) averaged 175 clients per recorded day, so these bounds are 5–10 times what eRegisters shows today.
- **People:** 300 000 (catchment plus migration, lightly duplicated) and 700 000 (systematic duplication of OPD walk-ins). Duplication changes only the number of people (a duplicate is a second person with the same visits); retention only the enrollments and events.
- **Load per hour,** dividing the daily volumes over 9–15 core hours: about 240–560 new people, 920 enrollments and 2 600 events a day, and about 27 000 reads (10 per contact); about one request a second on average, perhaps five in a morning peak, and 72 Android incremental syncs an hour. The realistic run (about 10 requests a second) and the hammer run are stress tests above that.

| Ladder size | People | Enrollments | Events |
|---|---|---|---|
| `30k`, `100k` | 30 000, 100 000 | 50 000, 167 000 | 120 000, 400 000 |
| `hc4`: busy HC IV with retention by programme | 300 000 | 0.5 M | 1.2 M |
| `hc4-5y`: the same today, all five years kept | 300 000 | 1.2 M | 3.1 M |
| `pessimistic` | 700 000 | 1.2 M | 3.1 M |

## Retention for tracker programs

Built (docs/superpowers/specs/2026-10-16-tracker-retention-design.md, docs/FINDINGS.md "Retention for tracker programs"). People are kept, so search and duplicate checks work offline. For each program with a window (`facility-hub-config/retention`, the same table as event programs, `defaultMonths` included):

- active enrollments are kept in full, whatever their age;
- a completed or cancelled enrollment is kept in full until its last change is older than the cut-off, then as a stub. A stub has no data or events, is never read, and is kept for the "enrol once" check (E1016).

Tracked entity types can also have windows (`trackedEntityTypes`), with no default.

`internal/perf.TestTrackerRetention`, in process (unconstrained development machine), at 100 000 people, 400 000 enrollments and 1 000 000 events, 6 programs, 20 users. The generator spreads enrollments over five years; one over a year old is completed a year after it began, which is when it last changed.

| | Without retention | `{"defaultMonths": 12}` |
|---|---|---|
| First fill (set-up and the users' first pulls) | 8 min 56 s | 5 min 22 s |
| Data file | 5.2 GB | 2.2 GB |
| Held | 400 k enrollments, 1.0 M events | 160 k enrollments in full plus 240 k stubs, 0.32 M events |
| Sent by central, first fill | 9 681 requests, 2.6 GB | 9 851 requests, 2.7 GB |
| Sent by central, a full pull | 1 273 requests, 493 MB | 1 443 requests, 574 MB; no one fetched again |
| Pruning a full hub to the window | | 7 min: 240 k enrollments stubbed, 680 k events deleted, in transactions of 1 000 rows |

At the HC IV size kept for five years (`hc4-5y`: 300 000 people, 1.2 million enrollments, 3.1 million events), 60 users:

| | Without retention | `{"defaultMonths": 12}` |
|---|---|---|
| First fill | 34 min 43 s | 19 min 52 s |
| Data file | 16.2 GB | 6.5 GB |
| Held | 1.2 M enrollments, 3.1 M events | 0.48 M enrollments in full plus 0.72 M stubs, 0.96 M events (about the `hc4` sizing) |
| Sent by central, first fill | 28 829 requests, 8.0 GB | 29 311 requests, 8.3 GB |
| Sent by central, a full pull | 3 747 requests, 1.5 GB, 5 min 22 s | 4 229 requests, 1.75 GB, 4 min 3 s; no one fetched again |
| Pruning a full hub to the window | | 29 min: 719 k enrollments stubbed, 2.1 M events deleted, in transactions of 1 000 rows |

The tables show four things, the same at both sizes.

- **The hub.** The data file shrinks by 58–60 % and the first fill by 40–43 %. At the HC IV size this brings the file from 16 GB to 6.5 GB, within reach of a Pi 5 with the 8 GB the sizing above asks for.
- **Central sends as much as before.** In this data nearly every person had a recent enrollment in some program, so the listing "changed since the cut-off" returns nearly everyone. Fetching a person for a program returns all their enrollments there, and every event of the old ones, which the hub then keeps only as stubs.
  - Central's first fill shrinks only by the people with no change anywhere within the window. On this data there are almost none; on real data it depends on how many patients are episodic.
  - Asking central for less (for example no events of closed enrollments) is not possible in one tracker query.
- **A full pull sends 16 % more** than without retention, because it lists twice (active, and changed since the cut-off). It is still quicker on the hub, which holds less.
- **No re-fetch.** A full pull after pruning fetched no tracked entity again: what is listed under an unchanged stub counts as held.

**What a full pull costs on the wire.** In the HC IV run, a weekly full pull was almost all ID listings:

- 81 % "changed since the cut-off";
- 14 % active enrollments;
- 3 % people by type;
- 2 % the ownership check.

Events are 3.1 M of the 5.8 M listed entries, about 60 % of the bytes. The run overstates real traffic in three ways, measured live on Sierra Leone 2.42 and 2.43 (`TestListingBytesLive`):

- **Each listing ran twice.** The setup account and the 60 users are two access groups. A real hub lists once per distinct access group.
- **The fake's entries are larger.** It writes 137 bytes per listed event (it repeats the enrollment and the person); DHIS2's listing takes 78–105 bytes per listed person, enrollment or event. A listing without events (`enrollments[enrollment,updatedAt]`) is about half the size.
- **The bytes are uncompressed.** DHIS2's Tomcat on its own answers uncompressed, and so did the test instances. In production DHIS2 sits behind nginx (or another reverse proxy) that compresses; the hub asks for gzip. gzip at level 1 makes these listings 2.7–4.3 times smaller, the larger ones compressing best.

Estimate for the HC IV size with a 12-month window, from these figures and not measured as a whole: about 0.6 GB per access group per full pull uncompressed, about 0.15 GB compressed as production serves it.

**Decision (2026-10-17):** full pulls stay as they are. With the proxy compressing, the estimate is about 0.15 GB per access group per week at the HC IV size (500 hubs: about 75 GB a week on central). If facility uplinks turn out tight, run full pulls less often (`full_pull_interval`, for example monthly). Listing without events was considered and set aside: the listing also tells the hub which events each user may see (central leaves out stages the user cannot read), so leaving events out would move that decision into the hub's guarded visibility logic.

Changing an event moves its own and its tracked entity's `updatedAt`, not its enrollment's (2.42 and 2.43, `TestEventEditMovesUpdatedAtLive`). A listing without events would therefore have to fetch a person whenever their own `updatedAt` moved.

**Upgrading a large hub.** Its data migrations run when the store unlocks, and sorts stay in memory (`temp_store=memory`, so no plaintext reaches the disk) inside SQLite's 4 GB. As first written, the retention migration rebuilt `objects_state` with the stub flag. That ran out of memory at 4.6 million objects (the `hc4-5y` size) and worked at 2.5 million (`internal/perf.TestRetentionMigration`). It then added a partial index of stubs instead (part of the first schema since the migrations were squashed before the first release, 2026-10-21), which is empty at upgrade and took 5.8 s to build over 4.6 million objects (a 3.8 GB data file). Since 2026-10-17 migrations no longer hit that limit. The migration runner sorts through temporary files instead of memory while it migrates. The temporary files sit beside the store (on its disk, not a RAM `/tmp`) and are encrypted by the xts VFS, each with a random key. A full rebuild of `objects_state` over 4.6 million objects, the one that ran out of memory, then took 26 s (`internal/store.TestMigrationOnFullStore`, `HUB_PERF_MIGRATION=4600000`). It needs free disk for the sort, roughly the size of the index being built. Normal queries keep sorting in memory.

Still to measure: the same on `pi5`. Pruning a hub that already holds five years is a one-off of about half an hour at the HC IV size. It runs at the first sync cycle after a window is set, and offline writes wait for one transaction of 1 000 rows at most.

## Security of the SQLite index

- **Confidentiality unchanged.** The data file is encrypted page by page (AES-256-XTS, key derived from the data key, which exists only in memory), so the index columns are as encrypted at rest as everything else in it.
- **What changed.** Payloads also carry their own authenticated encryption (AES-GCM); index columns and slim JSON have only the file's encryption, which hides them but does not detect tampering. Someone able to write to the disk could already alter the visibility bitmaps and object columns, so the threat model does not change.
- **No plaintext on disk.** Sorting stays in memory (`temp_store=memory`), except in migrations, which sort through temporary files encrypted like the store ("Upgrading a large hub"); the journal and WAL go through the encrypting layer. The scenario test that greps the disk for plaintext covers it.
- **Plaintext in memory.** Decrypted pages sit in SQLite's page cache, and up to 4 096 decoded payloads in the hub's payload cache, dropped when the store locks.

## Aggregate data

`internal/perf.TestAggregate`, in process: 12 months of 4 000 values each (48 000 values; every fake form holds its month's 4 000, far larger forms than real ones). Set-up with the first pull 1.4 s, the next cycle 0.2 s; each offline form answered in 25–55 ms; an offline edit 30 ms; heap 12 MB, data file 31 MB. Aggregate data is small next to tracker: it does not change the sizing.

## Event programs

`internal/perf.TestEventPrograms` (`HUB_PERF_EVENTS=150000`), in process: two event programs of 150 000 events each, dates over 36 months.

| | Before the indexes | With them |
|---|---|---|
| Set-up with the first pull; a user's first pull; the next cycle | 25 s; 34 s; 2 s | 28 s; 35 s; 2 s |
| Working list offline (event date, newest first, 20 a page): page 1, page 50 | 1.6 s, 1.5 s | 3 ms, 8 ms |
| One device's download of a program (Android's paging, 3 001 pages of 50) | 28 min | 55 s |
| Retention of 12 months: 198 000 events pruned | 13 s (one transaction) | 16 s, in transactions of 1 000 rows (offline writes wait for one at most) |

Each page read and sorted the program's events; `objects_events` (creation order, Android's download) and `objects_events_occurred` (event date, newest first, read on by key) let a page read only itself. Per cycle and access group, an event program costs one listing of the events changed since the last pull.

## History

- **2026-10-08, in-memory design.** At 10 % of the first target (10 000 people, 50 users) the hub used about 1 GB and the first pull took 12 minutes, both growing with users × objects. The redesign: visibility as per-user bitmaps, ID-list pulls, a slim in-memory index; then a fixed set-up hub that kept every payload in memory, batched payload reads, paged results kept for a minute, cheaper sorts, and a garbage-collector limit at 70 % of RAM. The first container ladder still ran out of memory at 50 % of the first target.
- **2026-10-08, sizing.** The busy HC IV sizing above, with retention by programme and a SQLite query index as the two changes it called for.
- **2026-10-09, SQLite index.** A prototype answered the same pages as the in-memory index at 100 % of the first target with a working list in 0.03 ms instead of 57 ms and 4 MB of heap instead of 1.2 GB; the index was then moved into the store. Fixes found on the way: two indexes the planner did not use, statements prepared per object (twice the write cost), a full scan per user in each first pull, people fetched twice by the type pull, and "database is locked" under load.
- **2026-10-10, load on central.** Measured and reduced 23 times, as in the table above.
- **2026-10-13, ladder.** The results above; Android first downloads and a data file larger than RAM set the limits.
- **2026-10-14, pages.** Rendering copied each object through JSON; decoded payloads are now kept by version. An Android page of 50 people: 20 ms → 7 ms when cached; a working list 3.2 → 1.6 ms (in process, 10 % of the first target).
- **2026-10-14, later pages.** Only page 1 stopped reading at a full page; later pages read the whole program, and a one-minute cache of results hid it. Every page now stops at its end and the cache is gone. At 100 000 people, Android page 10 (the last with the 500 limit): 1.03 s the first time, 13 ms cached → 36 ms the first time, 9 ms after; a working list's page 3: 606 ms → 2 ms. A download without the limit costs more per page the deeper it goes (page 251: 141 ms).
- **2026-10-14, incremental syncs.** The second ladder's working-day run put `pi5` out of memory at 100 k: Android's `updatedAfter` check decoded every enrollment and event of each candidate person, kept for the whole request, and with the fake central's data (every object updated at its start) every person of a program is a candidate. The check now reads the stored update times only. Locally at 100 k, 60 users: peak memory 2.7 GB → 260 MB, incremental sync 44 s → 25 s (p50), Android page 7.8 s → 4.0 s. What remains of the 25 s is each page checking every candidate, which real data, with update times spread over years, makes far fewer.
- **2026-10-15, full downloads.** eRegisters downloads every person per program (Android stops at 500): one device's download grew with the square of the facility, 1 h 14 min at HC IV size in process. The hub now keeps, per user and query, the last row of the page it served for ten minutes, and the next page reads on from that row's creation key (`pagePositions`): 4 min 10 s at HC IV size, every page 17–36 ms. A first try kept the SQL row offset instead; SQLite still stepped through the skipped rows (3 min 35 s at 100 k instead of 1 min 2 s). `TestQueryPlansUseIndexes` checks that reading on keeps the index's order.
- **2026-10-15, aggregate data.** The Aggregate Data Entry app offline: a store of forms per data set window, pulled per access group (the numbers above).
- **2026-10-15, event programs.** Held offline with retention by program; two indexes for event lists (the numbers above).
- **2026-10-16, tracker retention.** Old closed enrollments are kept as stubs, with windows by program and by tracked entity type. With a 12-month window, at the HC IV size kept for five years, the data file went from 16.2 to 6.5 GB and the first fill from 35 to 20 minutes. Central's bytes did not shrink on this data, where nearly everyone had a recent enrollment somewhere.
- **2026-10-17, migrations at the HC IV size.** Rebuilding an index over 4.6 million objects at upgrade ran out of SQLite's memory; the retention migration builds a partial index instead (5.8 s).
- **2026-10-22, Windows; counting unsynced objects.** The ladder on a 2-CPU Windows VM (section "On a Windows PC"). It found the status page, the heartbeat and each sync cycle counting pending and refused objects by reading every object: at 30 k a `/hub/status` took 9–15 s here and about a minute on the VM, and the data folder reached 1.8 GB, probably a write-ahead log that the long reads kept from being checkpointed (not checked). A partial index of the unsynced objects: 1–8 ms, the 30 k first pull 5 min 48 s → 4 min, the data folder 0.7 GB; built at the HC IV size in 15 s.
