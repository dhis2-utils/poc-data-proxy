# Performance test plan: a Raspberry Pi-class hub under load

Question: can a hub on Raspberry Pi-class hardware serve **60 users working offline at once** at a busy urban HC IV: **300 000 people, about 0.5 million enrollments and 1.2 million events** (the sizing below, with retention by programme), and how far towards the pessimistic **700 000 people, 1.2 million enrollments and 3.1 million events** does it go?

The first target was 50 users over 100 000 tracked entities, 150 000 enrollments and 500 000 events; the results sections below dated before the sizing quote percentages of that target.

## The load in numbers

| | Assumption | Count |
|---|---|---|
| Tracked entities | some people are enrolled in more than one program | 100 k |
| Enrollments | over 5 years, 6 programs (about 80 new per day: a busy facility) | 150 k |
| Events | about 3.3 per enrollment | 500 k |
| Payload | ~1 KB per TE and event, ~0.5 KB per enrollment, as JSON | ~0.7 GB raw |
| Users | 35 Capture web, 15 Android (30 %), all registered with the hub while online | 50 |

## Expected bottlenecks (from reading the code; the test confirms or refutes them)

1. **Everything is decoded in RAM.** `tracker.Index` holds every object as a parsed map, so 750 k objects at 2–4 KB each in Go is 1.5–3 GB. That is tight on a 4 GB Pi and workable on 8 GB. Startup (`Index.Load`) decrypts and parses all of them.
2. **Visibility is stored per user and per object.** The `visibility` table and `Obj.Visible` keep one entry per object per user: 750 k × 50 ≈ 37 M entries, about 1.5 GB in Go maps plus about 1 GB in SQLite, as large as the data itself. Users with the same access hold identical rows.
3. **Searches scan linearly.** A working list or attribute search walks `All(kind)` under one `RWMutex`, so 50 concurrent searches over 100 k TEs contend.
4. **A Pi's disk is slow.** SD-card random writes run at around 1–3 MB/s, so the initial pull, the outbox and the WAL could be I/O-bound.

Points 1 and 2 together probably exceed a 4 GB Pi and leave little headroom on 8 GB. The test finds the knee (see "Scale ladder"), and the fix (visibility per access class, SQLite-backed queries with blind indexes for the encrypted attributes, an LRU of decoded payloads) gets its own design.

## Mimicking a Pi

The sandbox has no Docker, so the constrained hub runs on the **host** as a container on `dev-net`, and the load generator runs outside it (in the sandbox), so that it does not steal the hub's CPU:

```bash
perf/pi.sh build          # image: hub, fake central, hubload, benchmarks (perf/Dockerfile)
perf/pi.sh bench pi5      # calibration numbers into perf/results/
perf/pi.sh ladder pi5     # the scale ladder (below): results and summary.txt in perf/results/ladder-pi5-*
perf/pi.sh ladder pi4     # the same with the Pi 4 profile
```

`ladder` runs, for each data size (30 000 and 100 000 people; the HC IV target; the same HC IV with all five years, as today's hub keeps them until retention by programme exists; and the pessimistic one; `SIZES` to change), a fresh constrained hub, fills it from the generated central, runs the realistic 60-user load (`DURATION`, 5 minutes) and then the hammer run (`HAMMER`, 2 minutes) on it, samples the hub's memory and CPU every 10 seconds, and appends the key numbers per size to `summary.txt`: first pull, latencies per operation, Android download, reconnect, peak memory, and whether the hub was killed for memory. `up`, `load`, `stats` and `reset` remain for single runs.

The hub's data lives on a Docker volume (`hub-pi-data`), not a bind mount, so the disk limits apply to it. Docker Desktop needs memory for the profile's limit plus the fake central (about 6 GB for `pi5`), and the broker's DHIS2 instances share that VM.

| Profile | Models | `--cpus` | `--memory` | Disk |
|---|---|---|---|---|
| `pi4` | Pi 4, 1 GB, SD card | 0.8 | 1g | 10 MB/s and 500 IOPS write, 40 MB/s and 1 500 IOPS read |
| `pi5` | Pi 5, 4 GB, USB SSD | 1.6 | 4g | unrestricted |

`--cpus` caps total CPU time, not per-core speed, and one Apple Silicon core is roughly 2.5–3 times as fast as a Pi 5 core. The numbers above are starting points. **Calibrate them:** run the same benchmarks (`perf/pi.sh bench`: sealing, writing, reading and opening encrypted objects from `internal/store/bench_test.go`, plus `fio` 4k random write and read) on a real Pi and in the container, then adjust `--cpus` and the I/O limits until they match. One real Pi run settles any remaining doubt. Device I/O limits in Docker Desktop apply to the VM's disk (`/dev/vda`); if `fio` shows they have no effect, rely on the real Pi for the disk numbers.

A quicker first pass needs no container: `GOMAXPROCS=1 GOMEMLIMIT=3GiB taskset -c 0` in the sandbox. Those are soft limits, but enough to find the knee early.

## Filling the hub

**Real pull path, fake central.** `fakecentral` gets a generator mode: a deterministic seed produces the 100 k tracked entities, 150 k enrollments and 500 k events for 6 programs over 5 years. It renders each page on request instead of holding the data in memory. The hub pulls the data through its normal sync code, so the run also measures **initial sync time and throughput** on Pi hardware. Afterwards the data directory is snapshotted (`hub.db` + `hub.db.data`), and each load run starts from a copy.

Importing this volume into DHIS2 for one facility would take hours. Instead, a run against a live synthetic instance at 20 k enrollments checks that the fake's payload shapes and sizes match DHIS2's.

## Load generator (`cmd/hubload`)

The 50 virtual users log in while the hub is online, then the uplink is cut (Netcut, as in the live suite). Request patterns come from what the real apps send (recorded by `clients/capture_web.py` and the Android exploration):

- **Web user** (35): open a working list (page 1, newest first), search by name or ID, open the enrollment dashboard (TE, enrollments, events, relationships), add an event, then edit data values. Think time is 2–10 s in the *realistic* run and zero in the *hammer* run.
- **Android user** (15): a first download of the user's programs, up to 500 people per program as the Android Settings App limits it (`-android-limit`, 0 for everyone: the worst case), then incremental syncs every 5 minutes and uploads of new events. A device at a facility with a hub does not need everyone.
- **Reconnect**: after the offline period, the uplink comes back with the backlog of all 50 users' writes, and the push must finish without loss or duplicates.

## What is measured

- Latency p50, p95 and p99 per endpoint; throughput; errors and timeouts.
- Hub RSS and CPU (`docker stats`, Go runtime metrics); store size on disk.
- Startup time after a reboot, until the first login is served.
- Android first download time per user.
- Push throughput on reconnect.
- **Correctness under load**: a sample of the read oracle's queries runs during the hammer run, and each answer must match what the same query returns on an idle hub.

## Proposed pass criteria (realistic run, `pi5`)

| | Target |
|---|---|
| Working list, search | p95 < 1 s |
| Enrollment dashboard | p95 < 500 ms |
| Event write | p95 < 300 ms |
| Errors | none |
| Hub memory | < 70 % of the container limit, stable over 1 hour |
| Startup after reboot | < 2 min |
| Android first download, one user | < 10 min |

The hammer run has no pass criteria; it reports where the hub saturates and how it degrades (slower answers, not errors).

## Scale ladder

Run the five sizes (30 k and 100 k people; 300 k with retention, `hc4`; 300 k with five years, `hc4-5y`; 700 k, `pessimistic`) with 60 users, 18 of them Android, on both profiles (`perf/pi.sh ladder`); fewer users (`USERS=10`) where a size fails, to tell data size from concurrency. The output is one table per profile showing where each criterion first fails, which says what a Pi can carry today and how much headroom the target leaves.

## Order of work

1. **Done:** the generated central (`fakecentral -tes -enrollments -events -users`, `internal/fakecentral/generate.go`), the storage benchmarks, `cmd/hubload` (web and Android users, the uplink cut, the reconnect), `internal/perf` (`HUB_PERF_SCALE=… go test ./internal/perf`: first pull, memory, disk, reboot in process) and the container (`perf/`).
2. A sandbox pass up the ladder with soft limits, to find the knee early.
3. Calibration against a real Pi, if one is available; the full ladder in the container on both profiles. Findings go to `docs/FINDINGS.md`.
4. Design and fix whatever breaks first, then re-run.

## Found so far

- **Concurrent offline writes failed** with `500 database is locked` (6 % of writes with 6 users and no think time). SQLite's busy timeout cannot help a deferred transaction that reads and then needs the write lock; write transactions now begin immediate. Fixed.
- **"Sync now" in the admin page** ran its own cycle with a 10-minute limit, next to the scheduler's. A first pull at the target size takes longer, so it would have been cut off; it now asks the scheduler, which runs one cycle at a time without a limit. `/hub/status` reports `syncing` and `syncStarted`.
- **Every user pulls everything they can read.** With 50 users, the first sync moves 50 copies of the facility's data through the uplink and the hub, and so does each daily full pull (`full_pull_interval`). To be measured; a shared pull with per-user filtering would move one.

## First numbers: 10 % in the sandbox (2026-10-08)

10 000 tracked entities, 15 000 enrollments, 50 000 events (75 000 objects), 50 users (30 web with 2–10 s think time, 20 Android), 5 minutes offline. The hub ran unconstrained on 4 Apple Silicon cores, next to the fake central and the load generator; a Pi will be slower.

| | Result | Extrapolated to 100 % |
|---|---|---|
| First pull, 50 users | 11 min 50 s | about 2 hours, and more if per-page cost grows with size |
| Hub memory (RSS) after the pull / under load | 940 MB / 1.13 GB | about 9–11 GB: does not fit a Pi |
| Store on disk | 311 MB | about 3 GB |
| Working list, p50 / p95 | 42 / 428 ms | |
| Search by name, p50 / p95 | 46 / 381 ms | |
| Dashboard (person, events), p95 | 130 / 209 ms | |
| Offline event write, p95 | 125 ms, no errors | |
| Android full download (15 000 people, 300 pages), 20 at once | 82 s each; page p95 459 ms | each page scans the facility, so pages and their cost both grow: hours |
| Reconnect, 153 queued events | 7 s, none rejected | |

Read with the expected bottlenecks: memory grows with objects × users (the per-user visibility) and every pull and query works on the whole in-memory index. **At 10 % the hub works; at the full target it would not fit a Pi's memory, and the first sync and Android downloads would take hours.** The fixes, in order of effect: visibility per access class instead of per user (memory, and one pull instead of fifty), indexed queries instead of scans (Android pages, working lists), then the container runs to confirm.

## The redesign (branch `id-list-pull`)

Three changes, each verified with the oracles on all profiles:

1. **Visibility as bitmaps.** Each object has a stable slot; each user a bitmap over slots, stored in 512-byte chunks written in the same transaction as the objects. 50 users × 750 000 objects is under 5 MB, against about 37 million rows and map entries before.
2. **ID-list pull.** A user's pull lists what central shows them: identifiers and update times only. The hub then fetches in full only tracked entities it lacks, holds older, or has not fetched for that program's attributes (central shows a program's attributes only when asked with it), 50 per request. Central still decides visibility per user; each record crosses the uplink once.
3. **Slim index and candidate lists.** The index keeps what queries filter and sort on; rendering reads the full payload from the store. Searches start from the objects of the requested program, type, tracked entity or enrollment.

In-process, 10 % of the target, 10 users (the sandbox, unconstrained):

| | Before | After 1–2 | After 1–3 |
|---|---|---|---|
| First pull | 2 min 28 s | 13 s | 16 s |
| Index memory after a reboot | 418 MB | 390 MB | 203 MB |
| Store on disk | 155 MB | 119 MB | 119 MB |
| Working list / name search, offline | 28 / 31 ms | (noisy run) | 14 / 16 ms |

Next: the 50-user hubload run at 10 % and up the ladder, then your container runs for Pi numbers.

## First ladder run and the fixes it led to (2026-10-08)

`perf/pi.sh ladder pi5` (6 GB limit, the Docker VM being 8 GB) on the redesign: 10 % and 25 % ran without errors, every offline write reached central on reconnect, but memory grew about 0.11 GB per percent (1.25 GB at 10 %, 2.8 GB at 25 %) and 50 % and 100 % were killed for memory. At 25 % the working list and search exceeded 1 s at p95 and an Android first download did not finish in 5 minutes.

Profiling (`HUB_PPROF`) found:

1. **A hub that was set up and not yet restarted kept every full payload in memory.** Setup unlocks the store without loading the index, so the index never had the store and never went slim. The index now has its store from the start: live heap under load 2.5 times smaller.
2. **Rendering read each object's payload with its own query.** A page now loads the payloads its fields need in one query; the data file's page cache is 16 MB per connection.
3. **Each page of a paged query searched and sorted everything again**, so an Android download cost the square of the facility's size. Page 1 is computed fresh and kept for a minute; later pages of the same query by the same user reuse it, after checking visibility again.
4. **Searches spent their time in sort comparisons, date parsing and enrollment lookups.** Sort keys are computed once, dates are parsed only when a query has a date bound, and a program search checks the program's enrollments in one pass.
5. **Go let the heap grow to twice what is live.** The hub now sets the garbage collector's soft limit to 70 % of its container's memory limit, or of the device's RAM.

Hammer run in the sandbox (unconstrained, 10 %, 50 users, no think time), before and after 2–4:

| | Before | After |
|---|---|---|
| Web requests per second | 42 | 100 |
| Working list p50 / p95 | 150 / 430 ms | 44 / 212 ms |
| Android full download, 20 at once | 80 s | 56 s |
| Errors | none | none |

Searches remain linear in the facility's size (about half of a search is now the sort); keeping each program's people pre-sorted is the next step if the ladder shows it is needed.

## Sizing for a busy urban HC IV, and what follows (2026-10-08)

Sizing for one very busy HC IV: an effective catchment of about 250 000 people, 250 working days a year, a 5-year horizon, each service its own tracker program.

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

Notes on the sizing:

- **Backlogs:** ART and NCD start with existing patients, and their visits grow each year (year 1 → year 5).
- **Pharmacy** is folded into the other programs.
- **Episodes:** OPD, HIV testing and the laboratory enrol per episode, so their enrollments come close to their visits.
- **Peak day:** summing the per-program peaks into one day is deliberately pessimistic.
- **Comparison:** the busiest HC IV in the 27 September use report (Butenga) averaged 175 clients per recorded day, so these bounds are 5–10 times what eRegisters shows today.

**People.** About one person per enrollment (1.2 M) would mean nobody ever searches before registering; OPD alone would make most of it. Even with poor deduplication most returning patients are found. The targets are **300 000** people (catchment plus migration, lightly duplicated) and **700 000** (systematic duplication of OPD walk-ins).

**Load per hour.** Dividing the daily volumes over 9–15 core hours:

| | Per day | Per hour | Per second |
|---|---|---|---|
| New people (300 k / 700 k over 5 years) | 240 / 560 | 16–62 | ≤ 0.02 |
| New enrollments | 920 | 61–102 | ~0.03 |
| New events (year 5) | 2 600 | 173–289 | ~0.08 |
| Reads (about 10 per service contact) | 27 000 | 1 800–3 000 | 0.5–0.8 |
| Android incremental syncs (18 devices, every 15 min) | | 72 | 0.02 |

That is about one request per second on average, perhaps five in a morning peak. The "realistic" load run (50 users, 2–10 s between actions, about 10 requests per second) and the hammer run (about 100 per second) remain stress tests above that. The ladder gets a third, **paced** run: writes and reads at these per-hour rates (peak = 5 times the average), which is the one the pass criteria apply to.

**Android downloads.** With the Android Settings App, devices download a limited number of people per program and org unit (the default is 500), not the whole facility. The load runs should use the limit the deployment configures. "Every device downloads everything" stays as the worst case.

**What this means for the hub.** 1.2 M enrollments, 3.1 M events and 0.3–0.7 M people are about 5 M records; at about 2 KB per record in memory under load (measured after today's fixes) that is around 10 GB, beyond a 4 or 8 GB device. Two changes, in this order.

### 1. Retention by programme

Keeping five years of everything locally is not needed. All people are kept, so search and deduplication work offline; each program keeps its enrollments and events for a window of its own.

- **The table.** Program → months (for example OPD 3, laboratory 6, maternal 24, HIV care and NCD "while active"), with a default. It lives in central's dataStore next to the hub registry, so a country sets it once; the hub reads it at each sync, like the catalog.
- **Pulls.** A program pull asks for enrollments that are active (any age) or enrolled within that program's window (`enrollmentStatus=ACTIVE`, `enrollmentEnrolledAfter=…`). An active ART patient keeps their full history.
- **People.** Everyone registered at the facility is already pulled without enrollments (the per-type pull), so keeping all people needs no new mechanism.
- **Pruning.** A periodic job deletes synced enrollments, with their events, that fell out of their window: completed or cancelled, enrolled before the cut-off, nothing pending.
- **Offline behaviour.** A returning patient is found; their old enrollment answers "needs connection"; a new episode can be enrolled as usual. In a program that allows only one enrollment ever, Capture cannot see the old completed one offline, and central rejects a second one on push, unless the hub keeps a list of who was ever enrolled in such programs. To decide.
- **Tests.** The oracle narrows central's answer to each program's window, as it narrows it to the facility's ownership today.

### 2. The query index in SQLite (prototype first)

Today a working list scans all of a program's people in memory and sorts them on every request, which is linear in the facility's size and about half of a search's cost in today's profiles. With indexed columns in the data file (program, owner, status, dates, attribute values), the same list reads its page from an index whatever the size. Single-record lookups become a fraction of a millisecond slower; free-text name search is a scan either way unless restricted to prefix matches. Memory stays flat (about the page cache), and the hub has one store instead of a store plus an in-memory mirror. The visibility bitmaps stay in memory: they are small.

This is an expectation, so it is settled by measurement: a prototype of the two queries that matter (working lists, paged downloads) in SQLite, run on the same ladder against the current in-memory version.

**Security.**

- **Confidentiality unchanged.** The data file is encrypted page by page (AES-256-XTS, key derived from the data key, which exists only in memory), so indexed columns are as encrypted at rest as everything else in it. The objects table already keeps program, stage and owner as such columns.
- **What changes.** Today each record's payload also has its own authenticated encryption (AES-GCM) inside the encrypted file; attribute values copied into index columns would have only the file's encryption, which hides them but does not detect tampering. Someone able to write to the disk can already alter the visibility bitmaps and object columns, which have the same protection, so the threat model does not change.
- **No plaintext on disk.** Sorting stays in memory (`temp_store=memory`), and the journal and WAL go through the same encrypting layer. The scenario test that greps the disk for plaintext covers this, and gets a search on attribute values added.
- **Plaintext in memory.** Decrypted pages sit in SQLite's page cache, as decrypted objects sit in today's index.

### Load on central (2026-10-10)

What one hub asks of central, measured by `internal/perf.TestCentralLoad` (every request through the test kit's uplink, by kind, with answer sizes), at 10 000 people, 15 000 enrollments, 50 000 events, 6 programs and 60 users, projected to a day at the default intervals (sync every 5 minutes, ownership check hourly, full pull daily):

| Per hub per day | Requests | Data |
|---|---|---|
| Incremental listings ("changed since", per user and program, every cycle) | 140 500 | 31 MB |
| Access and catalog refresh (`/api/me`, `/api/programs`, `/api/trackedEntityTypes`, per user, every cycle) | 71 000 | 200 MB |
| Full identifier listings (per user and program, daily) | 1 800 | 806 MB |
| Ownership checks | 170 | 12 MB |
| **Total** | **214 000** | **1 GB** |

500 such hubs: about 1 200 requests a second to central around the clock, and 520 GB a day, almost all of it while nothing changes. For comparison, 18 Android devices syncing directly every 15 minutes make in the order of tens of thousands of requests a day. Most of the hub's requests repeat per user what is the same for users with the same access.

Reductions, largest first: listing once per distinct access instead of per user (the generated users share one access, so here 60 times fewer listings; real facilities have a handful of access groups); refreshing access and the catalog hourly or on login instead of every cycle; a longer sync interval; the full pull weekly; jitter on every schedule and a back-off after outages, so hubs do not act in step.

**After the first changes** (sync every 15 minutes with ±10 % jitter, access and catalog refreshed hourly and at login, full pull weekly, a random wait of up to a minute after an outage): 53 000 requests and 153 MB a day per hub, 300 requests a second for 500 hubs. What remains is mostly the per-user incremental listings (47 000 a day), which listing once per access group addresses.

The fake central was corrected for this measurement: "updated after" now means strictly after (all generated objects share one update time), and the ownership listing returns identifiers only, as DHIS2 does for those fields.

### Next ladder targets

| Target | People | Enrollments | Events | Users | Devices |
|---|---|---|---|---|---|
| Busy HC IV, with retention by programme | 300 000 | about 0.5 M (windows plus active) | about 1.2 M | 60 | 18 Android (30 %), with download limits |
| Busy HC IV, today (no retention yet) | 300 000 | 1.2 M | 3.1 M | 60 | 18 Android |
| Pessimistic | 700 000 | 1.2 M | 3.1 M | 60 | 18 Android |

The two axes are independent: duplication changes only the number of people (a duplicated walk-in is a second person with the same visits), retention only the enrollments and events. `hc4` is the target once retention by programme exists; `hc4-5y` is what today's hub would hold at the same facility.
