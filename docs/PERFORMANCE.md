# Performance test plan: a Raspberry Pi-class hub under load

Question: can a hub on Raspberry Pi-class hardware serve **50 users working offline at once** after it has pulled **5 years of data: 100 000 tracked entities, 150 000 enrollments in several programs and 500 000 events**?

## The load in numbers

| | Assumption | Count |
|---|---|---|
| Tracked entities | some people are enrolled in more than one program | 100 k |
| Enrollments | over 5 years, 6 programs (about 80 new per day: a busy facility) | 150 k |
| Events | about 3.3 per enrollment | 500 k |
| Payload | ~1 KB per TE and event, ~0.5 KB per enrollment, as JSON | ~0.7 GB raw |
| Users | 30 Capture web, 20 Android, all registered with the hub while online | 50 |

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

`ladder` runs, for each data size (10, 25, 50, 100 and 200 % of the target; `SIZES` to change), a fresh constrained hub, fills it from the generated central, runs the realistic 50-user load (`DURATION`, 5 minutes) and then the hammer run (`HAMMER`, 2 minutes) on it, samples the hub's memory and CPU every 10 seconds, and appends the key numbers per size to `summary.txt`: first pull, latencies per operation, Android download, reconnect, peak memory, and whether the hub was killed for memory. `up`, `load`, `stats` and `reset` remain for single runs.

The hub's data lives on a Docker volume (`hub-pi-data`), not a bind mount, so the disk limits apply to it. Docker Desktop needs memory for the profile's limit plus the fake central (about 10 GB for `pi5`), and the broker's DHIS2 instances share that VM.

| Profile | Models | `--cpus` | `--memory` | Disk |
|---|---|---|---|---|
| `pi4` | Pi 4, 4 GB, SD card | 0.8 | 4g | 10 MB/s and 500 IOPS write, 40 MB/s and 1 500 IOPS read |
| `pi5` | Pi 5, 8 GB, USB SSD | 1.6 | 8g | unrestricted |

`--cpus` caps total CPU time, not per-core speed, and one Apple Silicon core is roughly 2.5–3 times as fast as a Pi 5 core. The numbers above are starting points. **Calibrate them:** run the same benchmarks (`perf/pi.sh bench`: sealing, writing, reading and opening encrypted objects from `internal/store/bench_test.go`, plus `fio` 4k random write and read) on a real Pi and in the container, then adjust `--cpus` and the I/O limits until they match. One real Pi run settles any remaining doubt. Device I/O limits in Docker Desktop apply to the VM's disk (`/dev/vda`); if `fio` shows they have no effect, rely on the real Pi for the disk numbers.

A quicker first pass needs no container: `GOMAXPROCS=1 GOMEMLIMIT=3GiB taskset -c 0` in the sandbox. Those are soft limits, but enough to find the knee early.

## Filling the hub

**Real pull path, fake central.** `fakecentral` gets a generator mode: a deterministic seed produces the 100 k tracked entities, 150 k enrollments and 500 k events for 6 programs over 5 years. It renders each page on request instead of holding the data in memory. The hub pulls the data through its normal sync code, so the run also measures **initial sync time and throughput** on Pi hardware. Afterwards the data directory is snapshotted (`hub.db` + `hub.db.data`), and each load run starts from a copy.

Importing this volume into DHIS2 for one facility would take hours. Instead, a run against a live synthetic instance at 20 k enrollments checks that the fake's payload shapes and sizes match DHIS2's.

## Load generator (`cmd/hubload`)

The 50 virtual users log in while the hub is online, then the uplink is cut (Netcut, as in the live suite). Request patterns come from what the real apps send (recorded by `clients/capture_web.py` and the Android exploration):

- **Web user** (30): open a working list (page 1, newest first), search by name or ID, open the enrollment dashboard (TE, enrollments, events, relationships), add an event, then edit data values. Think time is 2–10 s in the *realistic* run and zero in the *hammer* run.
- **Android user** (20): a first full download of the user's programs (the heaviest request set), then incremental syncs every 5 minutes and uploads of new events.
- **Reconnect**: after the offline period, the uplink comes back with the backlog of all 50 users' writes, and the push must finish without loss or duplicates.

## What is measured

- Latency p50, p95 and p99 per endpoint; throughput; errors and timeouts.
- Hub RSS and CPU (`docker stats`, Go runtime metrics); store size on disk.
- Startup time after a reboot, until the first login is served.
- Android full download time per user.
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

Run 10 %, 25 %, 50 %, 100 % and 200 % of the target (100 % = 100 k TEs, 150 k enrollments, 500 k events) with 50 users, on both profiles (`perf/pi.sh ladder`); fewer users (`USERS=10`) where a size fails, to tell data size from concurrency. The output is one table per profile showing where each criterion first fails, which says what a Pi can carry today and how much headroom the target leaves.

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
