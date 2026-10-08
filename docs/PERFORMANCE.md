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
perf/pi.sh build          # image with the hub, the fake central and the benchmarks (perf/Dockerfile)
perf/pi.sh bench pi5      # calibration numbers into perf/results/
perf/pi.sh up pi5         # fake central + constrained hub on dev-net: http://hub-pi:8080
perf/pi.sh stats          # CPU and memory, once
perf/pi.sh down           # remove the containers; reset also deletes the data volume
```

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

Run 10 %, 25 %, 50 %, 100 % and 200 % of the target (100 % = 100 k TEs, 150 k enrollments, 500 k events), each with 5, 10, 25 and 50 users, on both profiles. The output is one table per profile showing where each criterion first fails, which says what a Pi can carry today and how much headroom the target leaves.

## Order of work

1. `fakecentral` generator mode and snapshotting; micro-benchmarks.
2. `cmd/hubload` with the web and Android user models; a sandbox soft-limit pass up the ladder, to find the knee early.
3. Dockerfile and host container profiles; calibration against a real Pi if one is available.
4. The full ladder on both profiles; findings go to `docs/FINDINGS.md`.
5. Design and fix whatever breaks first (likely the in-memory index and per-user visibility), then re-run.
