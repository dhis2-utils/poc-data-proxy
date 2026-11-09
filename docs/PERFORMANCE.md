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


**With a hard disk** (2026-10-23 to 25): the same VM with its disk on a USB hard disk, the fake central and the users on the Mac (`perf/remote-load.sh` and `stress.ps1 -Queue`), 60 users, an hour's working day with the uplink 10 minutes down and 5 up. `winsat` on the VM: random reads 2.1 MB/s (about 130 a second, an ordinary hard disk), flushes 4.6–10 ms on average, 20 ms at p95, 809 ms at most. On the NVMe disk above the same VM pulled 100 k in 14 minutes.

| Hard disk, 60 users | 100 k, 3 Android, old build | 100 k, 18 Android, a day (`CHANGED=0.01`) | 300 k, 18 Android, a day | 500 k, 3 Android, old build |
|---|---|---|---|---|
| First pull | 1 h 6 min | 1 h 19 min | 8 h 2 min | 11 h 30 min |
| Working day: web pages (p50 / p95) | 9–62 ms / 2–17 s | 9–52 ms / 2.4–3.4 s | 0.2–1.4 s / timeouts | hub never online |
| Working day: Android incremental sync (p50) | 5 min 19 s | 41 s | 12 min (timeouts) | — |
| Working day: new event (p50 / p95) | 561 ms / 1 min 6 s | 135 ms / 54 s | 1.8 s / timeouts | — |
| Peak memory of `hub.exe` (working set) | 440 MB | 498 MB | 1.7 GB | 2.3 GB |
| Confirmed saves read back | all | all | all | — |

With 18 Android users and every person changed (the worst case), the old build reached 7.7 GB and exited (docs/INCREMENTAL-SYNC-FIX.md); the build with that fix stayed under 760 MB. Two causes of the slow working days were then found and fixed (History, 2026-10-26): every incremental sync read every enrollment of the program, and each save made online was shared with each user in a transaction of its own; saves stalled for up to 85 s after each reconnect, never while offline. Both cost disk reads or flushes, which a hard disk makes 10–100 times dearer than flash.

The hub needs flash storage: an SSD, eMMC or a good SD card. On a hard disk with 4 GB it serves web users at 100 k with slow tails, and its first pull takes hours from 300 k.


## On Raspberry Pis (2026-10-26)

**First round: the Pi 5 on the SSD, the Pi 4 on the card.**

The image's load-test mode (`hub-loadtest.txt`), image `0.1.5-local.bc1ad67` (main with the fixes of 2026-10-26), on two boards over Wi-Fi at 20 °C: `rpi5`, a Pi 5 with 4 GB on a SATA SSD (Intenso SATA III High Performance, 120 GB) through a USB adapter (StarTech USB 3.1 Gen 2 to SATA cable), and `rpi4`, a Pi 4 with 1 GB on a microSD card (SanDisk High Endurance, 64 GB) and no heatsink. The fake central and the users ran on the Mac (`perf/remote-queue.sh`): 60 users of whom 18 sync like Android (`WEB=0.7`), 1 % of active enrollments changed on central (`CHANGED=0.01`), an hour's working day with the uplink 10 minutes down and 5 up, then the hammer run. Medians and p95s; web pages are the dashboards, name search and working list.

| | `rpi4`, 10 k | `rpi4`, 30 k | `rpi4`, 60 k | `rpi5`, 100 k | `rpi5`, 300 k |
|---|---|---|---|---|---|
| First pull | 2 min 11 s | 11 min 13 s | 24 min 3 s | 13 min 23 s | 40 min 30 s |
| Web pages | 30–96 ms / 0.8–1.2 s | 33–96 ms / 0.8–1.1 s | 26–89 ms / 0.9–1.2 s | 22–49 ms / 0.19–0.45 s | 21–45 ms / 0.12–0.33 s |
| New event | 59 ms / 1.6 s | 61 ms / 1.3 s | 52 ms / 1.6 s | 35 ms / 0.30 s | 31 ms / 0.22 s |
| Android incremental sync | 1.1 s / 3.4 s | 1.6 s / 3.2 s | 2.3 s / 4.7 s | 1.0 s / 1.5 s | 3.1 s / 3.8 s |
| Hammer: 18 first downloads at once | 1 min 5 s | 1 min 3 s | 1 min 1 s | 1 min 1 s | 38 s |
| Reconnects: online after; outbox empty after | 15–16 s; 12–28 s | 14–15 s; 12–23 s | 15–17 s; 13–23 s | 16–17 s; 12–20 s | 14 s; 8–16 s |
| Peak memory of the hub; least free memory | 207 MB; 529 MB | 215 MB; 541 MB | 225 MB; 494 MB | 260 MB; 3.5 GB | 361 MB; 3.4 GB |
| Hub's CPU, share of one core | 58 % | 59 % | 62 % | 30 % | 41 % |
| Disk write, per operation | 18 ms | 21 ms | 23 ms | 1.9 ms | 2.3 ms |
| Hottest; throttled | 72.5 °C; no | 73.5 °C; no | 75.5 °C; no | 57.9 °C; no | 59.5 °C; no |
| Data folder | 0.2 GB | 0.7 GB | 1.3 GB | 2.2 GB | 6.5 GB |

Every confirmed save was read back (2 568–2 675 a run); one request failed in all five runs (a working list at 30 k). The longest gap between confirmed saves was 9.5–13 s a run, against 85 s on the hard disk.

- **A Pi 5 with an SSD serves the busy HC IV size** (300 k people) with pages in tens of milliseconds and Android syncs of 3 s, while its memory stayed under 400 MB. Its p95s at 300 k were lower than at 100 k: with flash storage the size hardly matters.
- **A Pi 4 with 1 GB and an SD card serves 60 k** with medians under 100 ms and tails about a second. Its limit is the card's writes (18–23 ms each, ten times the SSD's), which show in the save tails (p95 1.3–1.6 s). Memory is not the limit at 60 k (225 MB). Bytes written per day, for the card's endurance, were not measured in these runs: the samples counted operations, not bytes. The sampler records MB read and written from the next runs on.
- **The Pi 4 runs warm without a heatsink**: 75.5 °C at 60 k in a 20 °C room, 4.5 °C below the point where it slows itself down. A facility at 30–35 °C would push it past that; it needs a heatsink and a ventilated case.
- The Pi 4 booted with the image's date (6 October) until it found network time: a board without a clock battery starts at its last known time (DEPLOYMENT.md, "Clock").

**Second round: the storage swapped, and up to 500 k** (image `0.1.5-local.8019c36`, the same hub with the sampler counting bytes). The Pi 5 on the microSD card, the Pi 4 on the SSD, now with a heatsink; otherwise as above.

| | `rpi5` on the card, 100 k | 300 k | 500 k | `rpi4` on the SSD, 60 k | 100 k | 300 k | 500 k |
|---|---|---|---|---|---|---|---|
| First pull | 25 min 33 s | 1 h 31 min | 2 h 57 min | 16 min 34 s | 28 min 15 s | 1 h 29 min | 2 h 36 min |
| Web pages | 22–47 ms / 0.29–0.60 s | 20–45 ms / 0.29–0.63 s | 21–46 ms / 0.35–0.72 s | 30–95 ms / 0.26–0.58 s | 29–96 ms / 0.27–0.58 s | 32–108 ms / 0.33–0.68 s | 28–107 ms / 0.28–0.58 s |
| New event | 32 ms / 0.47 s | 34 ms / 0.48 s | 37 ms / 0.57 s | 54 ms / 0.52 s | 49 ms / 0.47 s | 59 ms / 0.56 s | 51 ms / 0.57 s |
| Android incremental sync | 1.0 s / 1.4 s | 3.1 s / 3.7 s | 4.9 s / 6.2 s | 2.2 s / 3.8 s | 3.4 s / 4.8 s | 12 s / 16 s | 19 s / 27 s |
| Peak memory of the hub; least free memory | 223 MB; 3.6 GB | 617 MB; 3.2 GB | 634 MB; 3.1 GB | 208 MB; 503 MB | 230 MB; 509 MB | 645 MB; 119 MB | 657 MB; 70 MB |
| Hub's CPU, share of one core | 25 % | 28 % | 27 % | 68 % | 73 % | 92 % | 95 % |
| Disk write, per operation | 24 ms | 31 ms | 39 ms | 1.7 ms | 1.8 ms | 2.2 ms | 2.4 ms |
| Hottest | 57 °C | 56 °C | 57 °C | 76 °C | 76 °C | 76 °C | 78 °C |
| Data folder | 2.2 GB | 6.5 GB | 11 GB | 1.3 GB | 2.2 GB | 6.5 GB | 11 GB |

Every confirmed save was read back (2 614–2 707 a run); one request failed in seven runs. Nothing throttled; reconnects took 14–15 s.

- **Storage sets the tails and the first pull; the board sets the rest.** On the same board, the card made each write 10–15 times slower than the SSD: the first pull took about twice as long and the p95s and p99s of pages and saves were two to three times higher. Medians and Android syncs did not change.
- **A Pi 5 with 4 GB is not at its limit at 500 k, even on the card.** Android syncs grow with the size (1 s at 100 k, 5 s at 500 k), its CPU stays under a third of a core.
- **A Pi 4 with 1 GB on an SSD serves 300 k for web users**, with Android syncs of 12 s. At 500 k it is at its edge: syncs of 19 s (p95 27 s), the hub near a full core, 70 MB of memory free.
- **The hub's memory rose to 620–660 MB in the working day from 300 k on**, against 130–230 MB below, and fell back afterwards: slower Android syncs overlap, each with its working memory. It is what leaves a 1 GB board short.
- **A heatsink is not enough for a Pi 4 under load**: 76–78 °C at 20 °C, 2 °C below throttling at 500 k. It needs a fan or a well-ventilated case. The Pi 5 stayed under 60 °C.
- **Writes.** A first pull writes about five times the data folder (11 GB at 100 k, 55 GB at 500 k), once per hub. In the working day the hub wrote 9 MB a minute while offline and 235–272 MB a minute while online, at every size: it stored every object of every online read anew, changed or not. Fixed (History, 2026-10-27).

**What to deploy, from these runs.** Up to about 60 k people, a Pi 4 with 1 GB on a good SD card. Up to 300 k (a busy HC IV), a Pi 5 with 4 GB, on an SSD for the best tails and first pull or on a card if that is what is available; a Pi 4 with 1 GB on an SSD as a cheaper option for mostly web use. Above 300 k, a Pi 5. Every Pi 4 with active cooling or a ventilated case.

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

**When central struggles** (`TestStrugglingCentral`, 2026-10-26; the fake central's `Struggle` answers late or with 503). A hub's sync sends one request at a time, so 500 hubs have at most 500 requests open at central; a cycle against a slow central just takes longer (651 requests at 200 ms each: 2 min 13 s), and the next starts when it ends. A failed request is not retried within the cycle. When central answers every request with 503, the cycle that meets it carries on user by user (212 requests, all refused, against 651 when healthy); two failed pings, 30 s apart at most, then take the hub offline, and it sends nothing but a ping every 15 s until central answers again (2 requests in the measured cycle, both pings). When half the requests fail, the pings mostly pass and the hub keeps its rhythm (337 requests). No back-off was added: nothing measured shows hubs adding to central's trouble beyond their normal rate. Devices' requests pass through the hub while it is online, as they would go to central without it.

## Central's load from one facility, three ways (measured 9 November 2026)

Question: how much work does central do for one health facility without a hub, with a hub that passes everything through, and with a hub that answers staff from its copies? Counting requests does not answer it: a ping and a form's metadata are one request each. This section measures central's database work and its time per unit of work, and adds the units up to a facility-day.

**Set-up.** `agent-load-43`: DHIS2 2.43.2.1 with a 4 GB heap and Postgres 16, on the Docker host, with Uganda's eRegisters configuration (`clients/eregisters_setup.py`). Its programme, Medical Registers, has 795 programme rules, 404 rule variables, 5 218 data elements and 2 128 programme indicators; the Medical Visit stage alone has 935 data elements. There are 10 HMIS data sets. `clients/central_load_seed.py` gave one facility ten staff and 3 000 people with 11 745 visits over two years. The units added their own saves as they ran: 4 723 people and about 17 000 events at the end of the first run.

**How it was measured** (`internal/perf/central_work_test.go`, `TestCentralWork` and `TestCentralWorkReport`):

- Each unit of work ran alone. Before it, Postgres' `pg_stat_statements` was reset; after it, read. **DB time** is the sum of the statements' execution time, **statements** their number, **rows** the rows they returned or changed, **blocks** the 8 KB pages they touched in Postgres' cache or on disk.
- Every request passed an unthrottled Netcut in the sandbox, on the same Docker network as central. **Server time** is the sum of the time from each request to its full answer: central's time (Java and database), not the link's. **Requests** and **MB** (answers) are given for context.
- Idle, DHIS2's own scheduler costs 10 ms of DB time, 96 statements and 2 778 rows a minute. That rate is taken off each unit for its duration.
- Three runs, each unit 3 or 5 times; the tables give medians. Run 1: the hub's own units, the monthly form, idle. Run 2: eRegisters, 3 times, with the 5½-minute wait (below). Run 3: Capture web and Android, direct and through a hub, with the hub limited to one CPU (`GOMAXPROCS=1`, next point).
- The sandbox and central share one Docker VM with 4 CPUs. In run 1 a hub using several cores slowed central's answers to the browser's requests passing through it by 2–4 times (the same requests, the same rows). Limited to one CPU in run 3, a Capture visit's server time through the hub was 2.7–6.4 s, against 3.5–5.5 s direct. Times also wander: a Capture visit's median server time was 3.4 s in run 1 and 4.5 s in run 3. Statements, rows and blocks repeat more closely: for the hub's own units within 6 % from smallest to largest, where DB and server time spread by 40–100 %. Client units spread more in both, with single outliers (a hub refresh during one eRegisters save, the first pull of a new browser), which the medians leave out.

**The clients:**

- **Capture web and eRegisters 1.1.9**, the real apps in Chromium (`clients/central_load_visit.py`), each browser with a profile on disk. Its first start is a unit of its own; after it, one visit is made unmeasured, so the browser holds what it would on any later day. *App start*: log in and open the app. *Visit*: find a person by first name and surname, open them, add a Medical Visit (date, service type Outpatient, five vital signs; eRegisters also asks the type of patient and the outcome), save. *Registration*: a new person with the five mandatory attributes, enrolled, then a first visit. eRegisters searches its own copy in the browser. Version 1.1.9 did not send a save to central by itself, so the driver pressed Push Data after each save: one push per save, the most a device sends. *Pull*: eRegisters' Pull Data after a facility-day of other staff's saves on central (200 visits and 100 registrations), 5½ minutes later, because the app re-reads the 5 minutes before its last pull.
- **Android Capture**, approximated: no device was attached, so the test replays the requests of SDK 1.14, read from its source (`*Call.kt`, `*Fields.kt`) and from `testdata/2.42/capture_android_endpoints.txt`. The endpoints, filters, chunking and paging are the SDK's. The field lists are shortened by hand, but every nested collection the SDK asks for is kept (a stage's data elements in full, a rule's actions). Like the SDK, it sends the password until central gives it a session cookie, then the cookie. *Metadata sync*: 62 requests. *Daily data sync*: the people changed since the last sync, after a facility-day of saves (about 300 people, 50 a page, `orgUnitMode=DESCENDANTS`, `includeDeleted`, `updatedAfter`), and the values and completions of the 10 data sets. *Record sync* after a save: the upload (asynchronous), its job and report, and the person downloaded again.
- **The monthly form**, approximated: HMIS 105:01 opened (`dataValueSets` for the month), 150 values sent in one `dataValueSets` request and the form completed, as eRegisters sends its HMIS reports.
- **The hub**, the real code in process (the test kit, as `internal/live` uses it), set up by the facility's admin; the ten staff then log in through it. *First pull*: setup, the ten logins and the first sync, a fresh hub each time. *Sync cycle*: the 15-minute cycle after a quarter hour of the facility's saves on central (6 visits, 3 registrations). *Hourly refresh*: the same with the access refresh and the ownership check, which run hourly. *Weekly full pull*: a cycle with every user's full pull due. *Catch-up*: the cycle when the link returns after half a day offline, in which four staff saved 150 patients (a third of them registrations) through the hub. *60 pings*: online the hub pings central once a minute.
- **Through the hub** the same clients ran against a hub that passes everything through, then with `facility-hub-config/localReads` listing Medical Registers. What reached central is what was measured.

**The facility-day.** Ten staff, eight on a web app and two on Android, 30 patients each, a third of them new registrations; half a day offline; one monthly form a month. The counts per day:

| Unit | 1. No hub | 2. and 3. With a hub |
|---|---|---|
| Web app start | 8 | 8 |
| Web visit / registration | 160 / 80 | 80 / 40 online; the other half saved offline and pushed in the catch-up |
| eRegisters pull | 8 | 8 |
| Android record sync after a visit / a registration | 40 / 20 | 20 / 10 online |
| Android daily data sync / metadata sync | 2 / 2⁄7 | 2 / 2⁄7 |
| Monthly form | 1⁄22 | 1⁄22 |
| Hub sync cycle / hourly refresh | – | 24 / 8 (32 cycles in the working day, 8 of them hourly) |
| Hub weekly full pull / catch-up / pings | – | 1⁄7 / 1 / 1 440 (once a minute, day and night) |

Without a hub, the half day offline is counted as changing nothing for central: web staff enter those patients when the link returns (or from paper later), Android devices sync them later. In scenario 3, Capture could not open a person it had just registered (below), so its registration unit lacks the first visit; one visit is added for each registration.

**Per unit** (medians; database figures without DHIS2's idle background; KB are answers):

| Unit | Runs | DB time (ms) | Statements | Rows | Blocks | Requests | Server time (ms) | KB |
|---|---|---|---|---|---|---|---|---|
| direct: capture app start | 5 | 6 | 384 | 1 309 | 1 332 | 41 | 1 054 | 105 |
| direct: capture first start, once per browser | 1 | 83 | 1 268 | 10 391 | 14 891 | 130 | 5 650 | 10 618 |
| direct: capture registration | 5 | 224 | 1 576 | 10 602 | 32 351 | 47 | 5 309 | 1 860 |
| direct: capture visit | 5 | 373 | 1 117 | 6 998 | 28 366 | 44 | 4 508 | 1 902 |
| via hub: capture app start | 5 | 27 | 691 | 2 557 | 3 211 | 52 | 1 676 | 3 479 |
| via hub: capture first start, once per browser | 1 | 235 | 3 089 | 16 437 | 69 930 | 137 | 5 317 | 14 070 |
| via hub: capture registration | 5 | 258 | 1 841 | 11 729 | 37 286 | 47 | 4 959 | 10 |
| via hub: capture visit | 5 | 361 | 1 308 | 7 088 | 30 734 | 44 | 6 089 | 23 |
| via hub, local reads: capture app start | 5 | 8 | 167 | 1 067 | 1 319 | 16 | 1 522 | 3 466 |
| via hub, local reads: capture first start, once per browser | 1 | 13 | 380 | 1 100 | 1 653 | 70 | 1 928 | 3 466 |
| via hub, local reads: capture registration | 5 | 32 | 163 | 1 116 | 20 176 | 5 | 182 | 2 |
| via hub, local reads: capture visit | 5 | 116 | 189 | 902 | 19 518 | 2 | 423 | 9 |
| direct: eregisters app start | 3 | 1 | 14 | 0 | 41 | 18 | 1 263 | 4 |
| direct: eregisters first start, once per browser | 1 | 1 465 | 386 | 17 500 | 81 671 | 196 | 15 101 | 57 349 |
| direct: eregisters pull | 3 | 416 | 120 | 4 547 | 21 753 | 6 | 1 683 | 4 859 |
| direct: eregisters registration | 3 | 77 | 372 | 2 682 | 3 226 | 4 | 768 | 3 |
| direct: eregisters visit | 3 | 47 | 161 | 441 | 2 154 | 2 | 460 | 1 |
| via hub: eregisters app start | 3 | 15 | 231 | 1 020 | 895 | 29 | 1 725 | 3 456 |
| via hub: eregisters first start, once per browser | 1 | 2 451 | 2 336 | 46 287 | 140 837 | 196 | 35 397 | 52 329 |
| via hub: eregisters pull | 3 | 417 | 143 | 4 412 | 18 268 | 6 | 1 149 | 4 145 |
| via hub: eregisters registration | 3 | 83 | 450 | 3 000 | 5 557 | 4 | 491 | 4 |
| via hub: eregisters visit | 3 | 70 | 204 | 1 399 | 2 486 | 3 | 377 | 2 |
| via hub, local reads: eregisters first start, once per browser | 1 | 33 | 384 | 3 641 | 13 595 | 74 | 3 108 | 3 466 |
| direct: android daily data sync | 5 | 1 194 | 2 003 | 5 439 | 148 884 | 10 | 1 594 | 2 988 |
| direct: android metadata sync | 5 | 32 | 2 308 | 4 600 | 12 828 | 62 | 1 552 | 7 322 |
| direct: android registration, record sync | 5 | 34 | 213 | 1 054 | 2 669 | 5 | 87 | 9 |
| direct: android visit, record sync | 5 | 45 | 224 | 1 064 | 2 665 | 5 | 78 | 12 |
| via hub: android daily data sync | 5 | 1 106 | 1 739 | 5 155 | 151 355 | 10 | 1 375 | 2 240 |
| via hub: android metadata sync | 5 | 32 | 2 556 | 4 721 | 13 203 | 62 | 1 425 | 7 322 |
| via hub: android registration, record sync | 5 | 23 | 235 | 1 067 | 2 702 | 5 | 64 | 9 |
| via hub: android visit, record sync | 5 | 40 | 248 | 1 077 | 2 699 | 5 | 64 | 12 |
| via hub, local reads: android daily data sync | 5 | 1 118 | 27 | 1 079 | 3 607 | 2 | 1 191 | 295 |
| via hub, local reads: android metadata sync | 5 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| via hub, local reads: android registration, record sync | 5 | 38 | 234 | 1 063 | 2 702 | 5 | 92 | 9 |
| via hub, local reads: android visit, record sync | 5 | 57 | 246 | 1 074 | 2 703 | 5 | 102 | 12 |
| direct: aggregate monthly form | 5 | 13 | 360 | 617 | 14 021 | 3 | 300 | 2 |
| via hub: aggregate monthly form | 5 | 28 | 349 | 767 | 15 496 | 3 | 104 | 30 |
| via hub, local reads: aggregate monthly form | 5 | 21 | 349 | 880 | 16 025 | 3 | 206 | 52 |
| hub: 60 pings | 5 | 0 | 0 | 0 | 0 | 120 | 484 | 145 |
| hub: catch-up after half a day offline | 5 | 3 911 | 26 800 | 155 752 | 378 212 | 178 | 15 331 | 2 708 |
| hub: hourly refresh | 5 | 225 | 2 379 | 25 019 | 39 323 | 127 | 7 045 | 38 758 |
| hub: setup and first pull | 5 | 4 649 | 80 305 | 375 766 | 640 283 | 398 | 28 960 | 172 092 |
| hub: sync cycle | 5 | 117 | 448 | 2 089 | 21 541 | 27 | 601 | 671 |
| hub: weekly full pull | 5 | 1 755 | 31 809 | 70 308 | 182 571 | 38 | 8 348 | 4 307 |
| hub, local reads: hourly refresh | 5 | 254 | 2 409 | 24 781 | 40 709 | 130 | 7 383 | 38 772 |
| hub, local reads: metadata check alone | 5 | 6 | 43 | 3 | 1 198 | 2 | 40 | 0 |
| hub, local reads: sync cycle | 5 | 131 | 441 | 2 049 | 21 570 | 28 | 594 | 598 |
| idle, 60 s | 5 | 10 | 96 | 2 778 | 348 | 0 | 0 | 0 |

**Per facility-day:**

| Web app | Scenario | DB time (s) | Statements | Rows | Blocks | Server time (s) | Requests | MB |
|---|---|---|---|---|---|---|---|---|
| eRegisters | 1. No hub | 21.9 | 74 494 | 397 356 | 1 239 122 | 167 | 1 170 | 49 |
| eRegisters | 2. Hub passing through | 24.5 | 109 974 | 735 364 | 2 198 512 | 177 | 5 595 | 411 |
| eRegisters | 3. Hub with local reads | not measured: eRegisters did not start (below) | | | | | | |
| eRegisters | 3., eRegisters' units as passed through (an upper bound) | 25.5 | 105 842 | 722 904 | 1 911 132 | 180 | 5 610 | 403 |
| Capture web | 1. No hub | 82.5 | 325 774 | 2 054 172 | 7 599 346 | 1 163 | 11 466 | 474 |
| Capture web | 2. Hub passing through | 51.5 | 256 470 | 1 516 644 | 5 599 896 | 803 | 10 731 | 379 |
| Capture web | 3. Hub with local reads | 28.4 | 99 066 | 608 944 | 4 496 420 | 177 | 5 498 | 370 |

Where it comes from (the monthly form adds under 0.1 s of DB time a day in every scenario):

| Web app | Scenario | Part | DB time (s) | Rows | Server time (s) | MB |
|---|---|---|---|---|---|---|
| eRegisters | 1 | web staff | 17.0 | 321 496 | 159 | 40 |
| eRegisters | 1 | Android | 4.9 | 75 832 | 8.5 | 8.9 |
| eRegisters | 2 | web staff | 12.4 | 275 376 | 73 | 63 |
| eRegisters | 2 | Android | 3.3 | 43 869 | 5.1 | 7.1 |
| eRegisters | 2 | the hub's own work | 8.8 | 416 084 | 99 | 341 |
| Capture web | 1 | web staff | 77.6 | 1 978 312 | 1 154 | 465 |
| Capture web | 1 | Android | 4.9 | 75 832 | 8.5 | 8.9 |
| Capture web | 2 | web staff | 39.4 | 1 056 656 | 699 | 31 |
| Capture web | 2 | Android | 3.3 | 43 869 | 5.1 | 7.1 |
| Capture web | 2 | the hub's own work | 8.8 | 416 084 | 99 | 341 |
| Capture web | 3 | web staff | 15.3 | 161 416 | 70 | 30 |
| Capture web | 3 | Android | 3.8 | 34 268 | 5.3 | 0.9 |
| Capture web | 3 | the hub's own work | 9.3 | 413 220 | 102 | 339 |

The hub's own work in the working day: its 24 cycles 2.8 s of DB time and 14 s of server time, its 8 hourly refreshes 1.8 s and 56 s, the catch-up 3.9 s and 15 s, the pings 12 s of server time and no database work. Counted for 24 hours instead (72 cycles and 24 hourly refreshes, as the hub runs day and night), its own work is 18 s of DB time, 240 s of server time and about 1 GB a day.

**What the numbers say:**

- **With Capture web, a hub that answers from its copies cuts central's work most:** a third of the DB time, under a third of the rows and 15 % of the server time of the same facility without a hub. A Capture visit through to central is 44 requests, 7 000 rows and 4.5 s of server time; most of it goes to `GET /api/programs/{uid}` (6 a visit, 2.2 s) and `GET /api/tracker/trackedEntities/{uid}` (15, 1.1 s). With local reads, 2 requests reach central: the search by name and the save.
- **With eRegisters, a hub adds work.** The app already works from its own copy and sends little: a visit is a push of 2 requests, 440 rows. Scenarios 1 and 2 cost about the same DB and server time (22 and 25 s, 167 and 177 s); the hub adds rows (735 000 against 397 000) and bytes (411 MB against 49 MB). Its cycles, hourly refreshes and catch-up read 416 000 rows; the hourly refreshes alone fetch 310 of its 341 MB.
- **The hub's hourly refresh is the largest of its own costs:** 127 requests, 25 000 rows, 7 s of server time and 39 MB each time. Eleven of the requests are `dataEntry/metadata`, one per user (here ten staff and the admin), 3.5 MB and 0.5 s each with this configuration's ten HMIS data sets: 5.2 s of its 7 s and nearly all its bytes. The same per-user refresh runs at each login through the hub: a Capture app start through the hub is 52 requests and 3.5 MB, against 41 requests and 105 KB direct.
- **The metadata check itself is cheap** on this heavy metadata: 2 requests (two access groups), 43 statements, 6 ms of DB time, 40 ms of server time. Turning local reads on adds it to the hourly refresh and one `metadataReload` read to each cycle, within the runs' spread.
- **Android:** with the SDK's session cookie, a record sync is 5 requests and about 1 000 rows. The daily data sync's most expensive request is `dataValueSets` for the user's ten data sets (about 1 s of DB time on its own). With local reads it and the completions are all that reach central of that sync: the hub answers the people and every metadata request. (The hub had not yet pulled the day's changes, so it answered with none; at a facility it would hand them over after its next cycle, whose cost is the hub's.)
- **The catch-up after half a day offline** (150 patients, 178 requests) costs central 3.9 s of DB time, 156 000 rows and 15 s of server time: 109 ms of server time a save, against 310 ms for Capture's save online in run 3.
- **Central checks a password in about 100 ms** (BCrypt; 116 ms a request with Basic auth, 11 ms with a session cookie, 20 ms with a personal access token, measured on this instance). A client that sent Basic auth on every request would cost central more than anything above; the Android SDK sends the cookie once it has one (`PasswordAndCookieAuthenticator`), and the replay does the same.

**Found on the way** (no hub change in this work; to follow up):

- **With local reads on, Capture web did not open a person it had just registered:** after the save (passed to central, 200), the hub answered the new person's dashboard from its copy, and Capture showed "Akello 36 is not enrolled in this program" (5 of 5 registrations, `TestCentralWork`, run 1 and 3).
- **With local reads on, eRegisters 1.1.9 did not start** in a new browser: after 15 minutes without its start screen it showed "Something went wrong! … Cannot read properties of undefined (reading 'length')" (runs 1 and 2). Through a hub passing everything through, it worked.
- **eRegisters 1.1.9 does not push a save by itself:** the save stays in the browser until Push Data. Staff who push a few times a day send fewer, larger pushes; that was not measured.
- **eRegisters' first pull after a browser's first download re-read half the facility** (51 pages and 76 MB, against 6 pages on the following days), once per browser.

**For 500 facilities.** This is a sum of per-unit costs measured one at a time on an idle server, not a concurrency test. Multiplied by 500 and spread over an 8-hour working day:

| Web app | Scenario | DB time a day | DB time per second of the working day | Server time a day | Requests in progress on average |
|---|---|---|---|---|---|
| eRegisters | 1. No hub | 3.0 h | 0.38 s | 23 h | 2.9 |
| eRegisters | 2. Hub passing through | 3.4 h | 0.43 s | 25 h | 3.1 |
| Capture web | 1. No hub | 11.5 h | 1.4 s | 162 h | 20 |
| Capture web | 2. Hub passing through | 7.2 h | 0.9 s | 112 h | 14 |
| Capture web | 3. Hub with local reads | 3.9 h | 0.5 s | 25 h | 3.1 |

"DB time per second" is how many seconds of statement time Postgres runs per second on average, about how many of its cores are busy if the statements are CPU-bound. "Requests in progress" is server time divided by the working day's seconds. Counting the hubs' nights as well (above) adds about 140 s of server time and 9 s of DB time per hub a day: 19 h and 1.3 h for 500, outside working hours. Not covered: peaks (everyone opening the app at 8, 500 hubs catching up after a national outage, whose wait of up to 5 minutes at random spreads them, "Load on central"), a central with millions of people instead of 5 000 (each listing reads more), other programmes, analytics, and the heap and garbage collection of a busy JVM.

**Limits.** One configuration, one facility of 3 000 people at the start and 5 768 at the end, a server with nothing else to do. Android is a replay, not the app. The monthly form is a stand-in for eRegisters' HMIS reports. The visit is short (six or eight values); real visits record more, which adds to the save in every scenario alike. Capture's browser cached some answers between runs (a visit's answers were 22 KB to 3.8 MB); requests and bytes are context only. Server time includes waiting for the database and Java's own work; it does not separate CPU from waiting.

Run it again: set the instance up with `clients/eregisters_setup.py`, seed it with `clients/central_load_seed.py MANIFEST 3000`, enable `pg_stat_statements` (`shared_preload_libraries`, a restart, `CREATE EXTENSION`), then from `internal/perf`:

```sh
HUB_MANIFEST=… HUB_CENTRAL_PG=postgres://dhis:dhis@<db>:5432/dhis2 HUB_CENTRAL_RUNS=5 \
  HUB_CENTRAL_OUT=run1.jsonl go test -run 'TestCentralWork$' -v -timeout 180m .
HUB_CENTRAL_UNITS=web,android,via-hub HUB_CENTRAL_APPS=eregisters PULL_WAIT=330 HUB_CENTRAL_RUNS=3 \
  HUB_CENTRAL_OUT=run2.jsonl go test -run 'TestCentralWork$' …
GOMAXPROCS=1 HUB_CENTRAL_UNITS=web,android,via-hub HUB_CENTRAL_APPS=capture HUB_CENTRAL_OUT=run3.jsonl go test …
HUB_CENTRAL_OUT=run1.jsonl,run2.jsonl,run3.jsonl go test -run TestCentralWorkReport -v .
```

The report takes each unit from the last file that has it.

## Push over a slow uplink

Question (2026-10-28): how fast does the hub push a backlog to a real DHIS2 over a mobile uplink, and does the backlog shrink while staff keep saving? In the hardware session that day (`rpi5` on a 4G router, the demo central on 2.43.2) the backlog grew from 1 309 to 1 667 objects in the hub's first 2 minutes online, with 6 clients saving about 1.2 times a second.

`internal/adversarial.TestPushOverSlowUplink`, run by `perf/push-uplink.sh` against `agent-hubtest-synthetic-42` (2.42.7, on the Docker host, so central answers fast). Three clients save offline until about 1 550 objects are pending (about 780 outbox entries: two in three are registrations of a person, enrollment and event, one in three an update of an event). Then the link returns, and the clients go on saving at a set rate. The link is the test kit's `Netcut`: a latency added to every request and a rate each way. "4G" is 3 Mbit/s and 100 ms, "poor" 1 Mbit/s and 300 ms. The hub's `reconnect_delay` is 0 here (below).

| Link, saves while pushing | Backlog | Pushed: entries/s, objects/s | Backlog after |
|---|---|---|---|
| Unlimited, none | 1 557 | 36.6, 73.8 | drained in 21 s |
| 4G, none | 1 557 | 7.0, 14.2 | drained in 1 min 50 s |
| Poor, none | 1 563 | 2.8, 5.5 | drained in 4 min 46 s |
| 4G, 0.4 saves/s (1.0 objects/s) | 1 518 | 5.7, 11.6 | drained in 2 min 24 s |
| 4G, 1.2 saves/s (2.8 objects/s) | 1 602 | 5.8, 12.2 | drained in 2 min 51 s |
| 4G, 2.0 saves/s (4.6 objects/s) | 1 629 | 3.3, 7.3 | 16 objects at 4 min, then 7–57 until 10 min; never 0 |
| Poor, 2.0 saves/s (4.6 objects/s) | 1 656 | 2.5, 5.5 | 1 157 at 10 min (lowest 1 016) |

**Where the time goes.** The push sends one outbox entry (one save) per request, one request at a time. A request is small: 731 bytes for a registration, 299 for an update, and 756–892 bytes back. Each entry costs the link's latency plus central's import (median 21–47 ms here) plus 2 ms in the hub. On the 4G link, 770 entries took 77 s of latency, 28 s of central's answers and 2.5 s of the hub's own time. Bandwidth plays no part: 3 Mbit/s would carry about 500 such requests a second. So the rate is 1 / (latency + central's import time). With the demo central over the session's 4G router, an online save took 152 ms at the median (p90 about 220 ms, 239 saves in `sim.jsonl`), which puts that link at about 6.5 entries a second, close to the "4G" row.

**With staff saving.** Saves made while the outbox is not empty are answered by the hub and join the outbox. Each push sends what was pending when it started. The rest of the sync cycle then runs (access, issues, the pull, the aggregate pull) before the next push (`cycle` triggers the next cycle at once when the push made progress). Those waits between pushes were 29 s of 144 s at 0.4 saves/s and 40 s of 171 s at 1.2 saves/s. At 2 saves/s on 4G they were 322 s of 597 s, in 61 back-to-back cycles of 6–11 s each. Test hubs re-read access every minute, against hourly in the field, so the field's housekeeping is shorter; the pull and the aggregate pull run in every cycle in both. The backlog fell to a few dozen objects within 4 minutes, but new saves arrived during every push and housekeeping step. The outbox was therefore never empty when a push ended, and the hub never went back to passing saves through to central. Saves reached central 10–20 s after they were made, and the hub sent about 2 700 other requests in those 10 minutes (1 159 of them tracker listings), about 4.5 a second. "Load on central" assumes about 0.1 a second per hub. On the poor link at 2 saves/s, the push capacity (2.9 entries a second) is just above the incoming rate. One push of about 800 entries took 4.5 minutes, and the housekeeping after it took 59 s. The backlog shrank by about 1 object a second.

**The session's growing backlog.** The Pi's image does not set `reconnect_delay`, so after the outage it waited a random time of up to 5 minutes before its first sync ("Load on central"). Meanwhile, it answered saves itself. The measured push (4G, 1.2 saves/s) drains a 1 600-object backlog in under 3 minutes. The session's numbers fit a hub that had not started pushing in its 2 minutes 12 seconds online: 1 309 + 2.6 objects a second × 132 s ≈ 1 650. Its later drain of about 1 750 objects within 6 minutes fits the same picture: up to 5 minutes of waiting and about 2 minutes of push. Not confirmed: the Pi's hub log ("pushed outbox entry" after 15:28:41) shows when the first push went out.

**Daily volume.** The busy HC IV sizing ("Sizing for a busy urban HC IV") gives about 3 000 saves a day (2 600 events and 240–560 new people, some saved together), 0.06–0.09 a second on average and perhaps 0.4 at a peak. That is the 0.4 row, which drained in about the time of the push alone. After a whole working day offline, those 3 000 entries take about 7 minutes to push over 4G and about 18 minutes over the poor link, plus up to 5 minutes of `reconnect_delay`. The largest daily volume whose full-day backlog drains within 30 minutes of the link's return (`reconnect_delay` aside) is about 1 700 × the entries a second the link carries. Over 4G this is about 12 000 saves a day, four times the HC IV sizing. Over the poor link it is about 5 000. Between pushes at those rates, waits are short (the 0.4 row). At rates near 2 saves a second all day, which is about 20 times the HC IV sizing, the hub keeps up over 4G, but it does so by running sync cycles back to back (above).

**Proposed, not built.** In `sync.cycle`, push again at once while a push makes progress, and run the housekeeping once after that, rather than once per push through `TriggerSync`. At 2 saves/s on 4G this would remove the 322 s of waits and the 61 cycles. The following pushes would each hold a few entries, so the outbox would empty and saves would pass through again. That outcome is estimated from the numbers above, not measured.

## A slow uplink (2026-11-05)

What links at facilities look like, and which conditions the tests should use: docs/CONNECTIVITY.md.

Question: the field visits of 28–29 October (Butaaka and St Joseph Buyege HC, Uganda) found staff entering data on paper first and typing it in later. Registering a patient took about a minute, and 3 minutes on a poor network; Buyege measured 31 KB/s. Do the hub's pulls fit such a link, and how much would answering staff from the hub while online save?

`internal/live.TestSlowLinkPulls` and `TestSlowLinkWorkflow` (`HUB_SLOWLINK=1`) ran against `agent-slowlink-43` (2.43, Sierra Leone database) as the nurse at Ngelehun CHC. `TestSlowLinkSeed` first gave the facility 19 800 people in the Child Programme, each with an event in both stages, the size of Ngelehun on the demo central (20 841): 80 844 objects on the hub. The test kit's link (`Netcut`) set the rate and added 200 ms to each request on the slow links. It compressed answers as nginx in front of a DHIS2 usually does (`SetGzip`); one run went without, as the demo central answered on 5 November (a 212 KB listing and `/api/me` came back uncompressed when asked for gzip).

| What one sync cycle asked of central | Full speed | 3 000 KB/s | 31 KB/s | 31 KB/s, uncompressed |
|---|---|---|---|---|
| First fill (set-up and first pull) | 84 s, 3.9 MB | | | |
| Sync, nothing changed (50 requests) | 1.4 s, 0.23 MB | 14 s | 22 s | 39 s, 0.79 MB |
| Weekly full pull, nothing changed (90 requests) | 19 s, 1.5 MB | 49 s | 2 min 3 s | 5 min 44 s, 9.2 MB |

The weekly full pull lists identifiers and update times only (`compat.pullValues`, `idsOnly`) and fetches what changed, so at this size it costs little even at 31 KB/s. Compression matters more than anything the hub does: a page of 1 000 listed people is 307 KB plain and 47 KB compressed, and 50 people in full 148 KB and 5 KB. On the 3 000 KB/s link the 200 ms per request costs more than the bandwidth. Six requests of each test sync (0.2 MB) are the ownership check, which the test kit runs every cycle; in the field it runs hourly (`ownership_check_interval`).

| One request of the workflow (median of 3) | Through to central, 3 000 KB/s | Through to central, 31 KB/s | Answered by the hub |
|---|---|---|---|
| Register a person (reserve IDs, save) | 0.25 s | 0.28 s | 4 ms |
| Find by name | 0.24 s | 0.27 s | 174 ms, then 11 ms after the fix below |
| Open the person | 0.24 s | 0.27 s | 1 ms |
| Working list (15 newest) | 0.27 s | 0.36 s | 3 ms |
| Save an event | 0.24 s | 0.28 s | 3 ms |
| The next room opens it (another user) | 0.28 s | 0.31 s | under 1 ms |

On a clean link a single compressed request takes about a quarter of a second, mostly latency, whatever the bandwidth. That does not explain minutes per registration. Screens make many requests each (below), and real links lose packets and vary; this link did neither.

`TestSlowLinkScreens` (`HUB_CLIENTS=1`, `clients/slowlink_screens.py`) timed Capture web in Chromium through the hub, as the nurse, with a browser profile on disk as staff's browsers have. Each screen after the app's start was reached inside the app, as staff move. Medians of 3:

| Capture web | Through to central, full speed | 3 000 KB/s | 31 KB/s | Answered by the hub |
|---|---|---|---|---|
| Opening the app, the browser holding its files (55 requests, 8 KB) | 1.3 s | 4.4 s | 5.0 s | 0.9 s |
| Opening the app, the browser's cache empty (7.7 MB) | 1.4 s | 15 s | 77 s, now 6.4 s | 1.1 s |
| Working list (5 requests) | 0.10 s | 0.34 s | 0.35 s | 0.09 s |
| A person's dashboard (14–19 requests) | 0.14 s | 1.3 s | 1.3 s | 0.08 s |
| The registration form, ready to fill (4–5 requests) | 0.29 s | 0.8 s | 0.8 s | 0.24 s |

Almost all of an app start's bytes are one file, Capture's 6.9 MB bundle (1.6 MB compressed): 62 s at 31 KB/s. A browser holding it opens the app in 5 s, the 54 other requests mostly waiting on the link's latency. Its cache is empty on a new tablet, after the cache is cleared, and once after every Capture update, and then it downloaded the bundle through the uplink although the hub held a copy. Now, when the browser asks without a copy of its own, the hub asks central whether its copy is current (a 304 of a few hundred bytes, as the user, so central still decides) and serves the copy over the facility network: 77 s to 6.4 s (`proxy.revalidate`, `TestAppFileFromHubCopyWhenCurrent`). An updated file comes through in full once and replaces the copy.

A first measurement on 5 November gave 67 s for every app start: the test's browser profile was in memory, and such a profile does not keep a file that large, so it downloaded the bundle at each start. Chromium with a profile on disk keeps it, as the table shows.

The screens inside the app cost a second at most, and a warm app start 5 s. Registering a patient online through the hub is then a few seconds of waiting plus the typing, on a clean link. The minutes reported from the field fit an empty browser cache (before this change), a central that does not compress (almost 4 minutes for the bundle at 31 KB/s), and links that lose packets, which this test did not model.

**Answering staff from the hub while online** (built 6 November; docs/DEPLOYMENT.md). The same screens under docs/CONNECTIVITY.md's conditions: "typical rural" is 31 KB/s, 1 s per request and 3 % loss, "poor" 2 s and 8 % (the test link stalls a request 1 s when it loses a packet, `Netcut.SetLoss`). Medians of 5, with the setting off, with patient data only from the hub (its first version), and with patient data and metadata from the hub:

| Capture web | Typical: to central | Typical: data from hub | Typical: data and metadata from hub | Poor: to central | Poor: data and metadata from hub | Offline |
|---|---|---|---|---|---|---|
| Opening the app | 19 s | 20 s | 4.8 s | 36 s | 9.4 s | 1.0–1.5 s |
| Working list | 1.4 s | 1.4 s | 0.10 s | 3.4 s | 0.09 s | 0.1–0.2 s |
| A person's dashboard | 4.4 s | 1.3 s | 0.12 s | 9.6 s | 0.11 s | 0.1 s |
| The registration form | 4.6 s | 3.9 s | 1.4 s | 8.6 s | 3.4 s | 0.3–0.4 s |

Patient data alone helped only the dashboard: each screen also asks for the user, the program, option groups, settings and icons, and those set the pace. With metadata answered from the user's copies too, what still waits on central is one reserved ID on the registration form, and at the app's start the check that Capture's page is current and a search across the district (this nurse searches the district; the hub answers such searches itself only for users whose search scope is their facility). One offline run took 23 minutes and is left out: the hub's request log shows nothing at all for 1 392 s, the laptop running the test asleep.

**Name searches.** The hub's own answer to a name search was the slowest step: 174 ms. A filter that few people match read every enrollment of the program and checked each, so the time grew with the facility (`internal/perf.TestQueries`, "nobody (full scan)": 7.5 ms at 20 000 generated people, 151 ms at 100 000). Common names were fast because the first page filled early. Now the matches are looked up first in a covering index on the lowercased value (`te_attrs_lc`, migration 6), and up to 1 000 of them drive the search; the filter itself is unchanged. At 100 000 people: nobody 151 → 20 ms, a common name 1.7 → 2.0 ms, first and last name 4.8 → 21.5 ms (each filter reads its attribute's index entries). On the Sierra Leone setup every variant took 6–11 ms, from 170. The migration took 5.4 s on 2.4 million attribute values (`internal/store.TestAttrIndexMigrationOnFullStore`, `HUB_PERF_ATTRS`, the hc4-5y size).

## Ten hours in the sandbox (2026-10-29)

Question: does a hub stay flat over hours of work with outages and power cuts, and does every confirmed save reach central? This is the sandbox part of M1 (docs/ADVERSARIAL-TESTING.md). The 72 hours on the Pi 5 follow in the next section.

`internal/adversarial.TestM1Soak` (`HUB_SOAK=10h`), from 21:41 on 28 October. It ran a hub of this tree as its own process against `agent-adv-network-42` (DHIS2 2.42.7, synthetic profile, a fresh instance). Three clients saved through it: the nurse and the restricted user in Capture web, and the nurse on Android. Each saved every 5–15 s, about 0.30 saves a second, two in three a registration (person, enrollment, event) and one in three an update of an event. That is about 4 times a busy HC IV's rate (3 000 saves over a 12-hour day, "Sizing"). A reader asked every 3 s for a working list or a person the nurse had saved, about half the HC IV's average read rate. The hub's housekeeping ran at shortened intervals: sync every 5 minutes, access refresh and ownership check every 15, a full pull every 2 hours, sessions at most 3 hours, `reconnect_delay` 1 minute, and retention of 12 months for every program. The token lifetime on central was switched between 7 and 365 days at 1 h 30, 4 h, 6 h 30 and 9 h, so the hub had to renew its token each time.

Faults: the uplink was cut at 1 h 01 for 14 minutes, at 2 h 51 for 21, at 4 h 57 for 23, at 7 h 02 for 11 and at 9 h 02 for 16. The hub was killed (`kill -9`, 20 s down, then a login) at 2 h 00 and 8 h 04 while online, and at 5 h 04 during an outage. Every 15 minutes the test sampled the process, the data folder, the outbox, the clients' latencies, the requests to central and the API tokens in central's database. The sandbox was shared with two other agents' DHIS2 instances and test suites. The load average was 4–10 on 4 CPUs, and by the morning the Docker VM's memory and swap were full. Online latencies below include that.

**Saves.** 10 634 saves, 10 211 confirmed: 9 027 answered by central through the hub, 1 184 by the hub while offline. Central held all 7 100 registrations as the ledger expects: none lost, partial, duplicated or with a wrong value or author. Of the 423 saves not confirmed, 18 were cut by the kills and 8 met an expired session and were made again. The other 397 were refused with "needs connection: this hub was replaced". A side test of mine had set up a second hub for the same facility on the same central at 4 h 22. The soak's hub then retired itself, as designed (G7), until I set its registry entry back to active (5 h 20). So the third outage and the kill during it ran with nothing in the outbox.

| Measure | First hour | Last hour | Trend |
|---|---|---|---|
| Hub memory (RSS), working | 34–42 MB | 59–85 MB | 21–101 MB throughout; 152, 217, 280 and 303 MB just after each start, then down |
| Live heap after a collection | 2.6–3.2 MB | 7.1–7.5 MB | grows after each start to about 7.5 MB, the payload cache's 4 096 entries, then flat |
| Goroutines, open files, threads | 20, 23–26, 9–10 | 20–21, 26, 11 | flat (13–14 goroutines while offline) |
| Hub CPU, share of one core | 0.6–0.7 % | 1.1–3.1 % | follows the pulls (below) |
| Data folder | 3.7 → 13.8 MB | 68.9 → 76.1 MB | +6.6 MB an hour, with the data: 7 100 people, 21 000 objects |
| Save, online (p50 / p90) | 53–70 / 107–134 ms | 75–143 / 143–572 ms | follows central and the machine |
| Save, offline (p50) | 9 ms | 15 ms | flat |
| Working list, online / offline (p50) | 35–50 / 20 ms | 82–155 / 20 ms | online follows central |
| Person, online / offline (p50) | 84–105 / 4 ms | 130–234 / 9 ms | online follows central |
| Requests to central per 15 minutes | 748–788 | 706–919 | flat: 363 tracker reads, 272 saves passed on, 60 pings |

**Memory, disk and the outbox stayed flat.** The live heap stopped at the payload cache's bound, and RSS stayed under 105 MB between starts. The goroutines and open files did not grow. The data folder grew with the data only: the WAL stayed under 9 MB, `requests.log` reached 2.1 MB (it rotates at 10 MB) and the hub's log 4.7 MB. After each outage the hub was online 7–15 s after the uplink returned. Its outbox (481–876 objects) was empty 31 s–1 min 26 s later, `reconnect_delay` included. After each kill the first login unlocked the store within 2 s. There were 125 sync cycles with no errors, no ERROR lines and 18 warnings: the unprotected registry entry, the requests cut by kills and outages, and the retirement above.

**What crept, and why.** Online answers rose from about 60 to about 140 ms over the night. Central's own answers and the machine's load rose with them. At 01:52 the same working list took 98 ms through the hub and 87 ms from central directly. Answers the hub gave itself stayed at 1–3 ms in its log. Sync cycles went from 1.9 s to 4–8 s. Most of a cycle is three users each pulling back the 240–370 objects saved through the hub in the last 5 minutes; that follows the save rate and central's speed. The full pull grew with the program, as it must. Every 2 hours it took 33 s at 2 h, 49 s at 4 h, 70 s at 6 h and 86 s at 8 h (about 1 600–5 700 people in the program, three users), and 217 s while the final check read central. In the field it runs weekly.

**Housekeeping.** The hub renewed its own token 4 times and deleted the old token each time. Retention ran in every cycle and pruned nothing, since all data was younger than 12 months. The ownership check listed the facility's people 177 times. Two things were found (FINDINGS.md, "Open" from the soak; tests in `internal/adversarial/soak_test.go`, gated with `HUB_FINDINGS`):

- **Concurrent logins leave tokens on central.** After the 2 h restart the nurse's clients all logged in again within the same second (the web writer, the reader and the restart's login). The hub created two tokens; it keeps one, and the other stays on central until it expires. The other token is missing from the hub's registry entry, so the Facility Hub app cannot revoke it. `core.EnsurePAT`, called from `auth.registerOnline`, reads the user and creates a token with no lock per user. `TestM1ConcurrentLoginsMakeOneToken`: 4 concurrent logins made 2 tokens, twice out of two.
- **A person saved during a full pull is hidden from its author offline until the next pull.** Each full pull "removed visibility" of objects for each user: 6 at the 2 h restart, then 11, 16 and 17. A save passed on to central while a full pull lists the program is marked visible to its author. It is missing from the listing, which was taken before the save. `RemoveVisibilityExcept` (`sync.pullGroup`) then removes it. `TestM1SaveDuringFullPullStaysVisible`: 6 of 107 people registered during a full pull were 503 offline afterwards, and all were visible after the next pull. With nothing saved during the full pull (`TestM1FullPullKeepsSavedVisible`, the soak's three writers, online and offline) none was hidden. In the field this lasts up to a sync interval after each weekly full pull. The last full pull removed 51 objects (14, 12 and 25 for the three users) while the writers were still saving: the VM was suspended for 20 minutes during the night, so the test's 10 hours (monotonic) ended at 08:01 wall time, after that pull (FINDINGS.md, "Open" from the soak, explained 3 November).

Raw data: `testresults/soak/20261028-2141-Zqadvjz6vfl/` (git-ignored): samples, ledger, reads, events, the hub's log and the summary. `HUB_SOAK_CHECK=<folder>` checks a ledger against central again.

## 72 hours on the Pi 5 (2026-10-30 to 11-02)

Question: does a hub on real hardware stay flat for three days of work, with an outage and a power cut a day, and does every confirmed save reach a real central? This is the hardware part of M1 (docs/ADVERSARIAL-TESTING.md).

**Set-up.** `rpi5` (Pi 5, 4 GB, SATA SSD over USB) with image `0.1.5-local.e3e956e` (main with the W1 fix), set up as Ngelehun CHC on the demo central (2.43.2). `cmd/hubdrive` on a server on the same LAN saved as 3 staff × Capture web and Android, one save per client every 40 s: about 13 000 saves a day, 4 times a busy HC IV. Outages were Firewalla blocking the Pi's internet (new connections refused). A soak-only sampler, added to the image for this run, recorded the hub once a minute. Housekeeping ran at its normal intervals.

| | Day 1 (from 10:41) | Day 2 | Day 3 | Day 4 (to 07:05) |
|---|---|---|---|---|
| Hub memory (RSS), median; largest | 79 MB; 364 MB | 80; 218 MB | 84; 226 MB | 83; 150 MB |
| Hub CPU, mean share of one core | 0.5 % | 0.4 % | 0.5 % | 0.4 % |
| Data folder at the end of the day | 111 MB | 198 MB | 285 MB | 307 MB |
| Hottest | 50 °C | 52 °C | 50 °C | 50 °C |

Never throttled. The data folder grew by the data, about 87 MB a day. The disk took 18 GB of writes in 69 hours, **6.2 GB a day** at 4 times an HC IV's day.

**Outages and power cuts.** 4 outages (3, 30, 60 and 29 minutes) and 5 power cuts, each pulled for about a minute. After each cut the hub answered locked after 50 s, or 2 min 10 s once, and the driver's logins unlocked it 20–28 s later. At 3 of the 4 outages the hub went back online briefly before staying offline (2 s, 1 min and 12 s), as in D6, fixed since (FINDINGS.md, "Found by adversarial testing" 30; this image predates the fix). After each outage the backlog reached central within the reconnect wait (up to 5 minutes, `ReconnectDelay`) plus a minute: 139 objects pending at 10:56 on day 1, none at 10:57.

**Every save on central.** 36 757 saves in the ledger, 35 801 confirmed. `hubdrive check` read 29 769 records on central: none lost, partial, duplicated or wrong, and nothing left pending or refused on the hub. Of the 956 saves not confirmed, 398 were refused by central between midnight and 1 a.m. on the driver's clock: the driver dates registrations by its own clock (UTC+1), and central (UTC) refused the next day's dates as in the future (E1020), with the registrations' events after them (E5000). A central hosted in another time zone than its facilities does the same to real clients near midnight: an open follow-up (FINDINGS.md, "Open", central's time zone). The rest were 401s and dropped connections while the hub was down or locked. A first check stopped on one 500 from central ("Could not commit JPA transaction"), which the same read answered 200 right after; `hubdrive check` now tries a 5xx again.

Raw data: `windows/out/soak/` (git-ignored): the driver's log and ledger, the check, `soak-samples.csv` and `hub-diag.txt`.

**The Pi 4's card in the same days (run A, not completed).** The same image in load-test mode on `rpi4` (1 GB, microSD), a fake central at 60 k people and `hubload -paced -duration 3h` from the Mac: one busy HC IV day spread over 3 hours, offline. The first pull took 18 min 48 s and wrote **6.3 GB** to the card. The paced load then wrote 133 MB in 42 minutes (about 190 MB an hour) until the Pi restarted on its own, 46 minutes in, at 48 °C and not throttled. After the restart, load-test mode started a fresh hub, and the rest of the run got 401 and 429 answers, so its latencies and its bytes per day do not count. The cause of the restart is not known. The hub's memory was not sampled (the sampler read `hub.service`, which load-test mode does not use), and the system log from before the restart was lost. Bytes written per day on a card are still to measure: rerun with the sampler fixed.

## Android syncs on the Pi 4's card (2026-11-03)

Question: in the 72-hour run on `rpi4` (1 GB, microSD) from 3 November 13:49, each of the 6 Android incremental syncs got 220 people and took about 58 s, six hours into the outage. All six ended in the same second. Where does the time go, and does it grow with the people a sync gets?

**Reproduced in the sandbox** (4 cores, data file in the page cache). The fake central and the hub ran as processes at the run's size: 60 000 people, 100 000 enrollments, 240 000 events, 20 users. `hubload -setup -fill` filled the hub, and paced writes at the run's daily rates followed offline: 140 people and 650 events for 6 hours, 560 and 2 600 for 24 hours. A driver then made `hubload`'s Android download with a watermark 6 minutes old: 6 programs, 50 people a page, each device alone and six at once. Times are the median of three runs alone, and the range of the six at once.

| Fake central | Hours of writes | People a sync gets | Before: alone | Six at once | After: alone | Six at once |
|---|---|---|---|---|---|---|
| As in the Pi run (`-changed 0`) | 6 | 222 | 5.4 s | 8.1–8.3 s | 0.37 s | 0.8–1.0 s |
| As in the Pi run | 24 | 642 | 6.4 s | 10.0–12.7 s | 1.0 s | 1.9–2.0 s |
| Central's changes over a day old (`-changed 0.0001`) | 6 | 221 | 0.12 s | 0.26–0.28 s | 0.08 s | 0.25–0.43 s |
| Central's changes over a day old | 24 | 641 | 0.34 s | 0.79–0.86 s | 0.31 s | 0.61–0.63 s |

**Where the time went: central's people in the day's margin, not the people sent.** With `-changed 0` (the default), the fake central dates every active enrollment and its person at the time it generated its data. An incremental sync takes everything updated in the day before its watermark as a candidate (`updatedFloor`), so for the first 24 hours after the fake started, about 20 000 enrollments of central's are candidates. Each failed the exact check in `updatedMatch` only after `newestChild` had read its children: two queries per person, about 3 300 per program. They come first in creation order, so page 1 of each program checked them all and took 0.9–1.1 s; pages after the first read on past them and took 60–90 ms. In the CPU profile `newestChild` took 67 % of the hub's time, most of it reading and decrypting table pages. The six syncs ran in parallel: six at once took 1.5–2 times one alone, about what 4 cores give six CPU-bound requests, which leaves little time for waits on locks or connections.

**The fix.** `changedTEs` now also returns the newest update time among each person's rows, from the same covering index. It leaves out a person who has nothing written on the hub and whose newest row is older than `updatedAfter`: whatever children the user may read, `updatedMatch` cannot count them as changed. People with anything written on the hub are checked as before. `TestIncrementalSearchAsReadingEveryChild` compares every incremental search with reading every child. After the fix, page 1 takes about 70 ms, as the pages after it do. What is left is `changedTEs` reading every row past the day's margin for each page: 68 % of the CPU at 24 hours with central's people in the margin, and little once they are out of it.

**Growth with the people sent.** Once central's changes are more than a day older than the watermark, as in the field after the first day of an outage, a sync costs about 0.5 ms per person here, before and after the fix: 0.12 s for 221 people, 0.34 s for 641. The count stops growing at a day's work.

**On the Pi 4.** The run's 58 s against 8.1–8.3 s here for the same case makes the Pi 4 on its card about 7 times slower for this work. This is one point, on a path that reads many table pages. A path that reads fewer may scale between 5 and 10 times. From that:

- Until about 13:50 on 4 November, 24 hours after the fake central started, syncs with the run's image stay near a minute, six at once. With about 650 people they would reach 70–90 s.
- After that, central's people leave the day's margin, and syncs should drop to about 6 s, six at once, at about 650 people. The fix does not change this plateau.
- With the fix, the first 24 hours would take about 6–14 s, six at once.
- In the field, the margin holds what central changed in the day before the watermark. During the first day of an outage that is the facility's last day online, so the fix shortens those syncs in proportion.

**The six ending together.** Each device waits 5 minutes after its sync ends before the next one. Six syncs that once overlap and end together therefore stay in step for the rest of the run. Here, six that start together end within 0.1–2.7 s of each other. What first put them in step on the Pi is not known from here.

Rerun: `fakecentral -tes 60000 -enrollments 100000 -events 240000 -users 20` (add `-changed 0.0001` for central's changes over a day old); `hubload -setup -fill -users 20 -paced -reconnect=false -duration 5m -day-people 140 -day-events 650 -day-reads 2000`; then `hubload -users 6 -web 0 -android-first none -android-sync 1m -reconnect=false -duration 5m` logs each sync's people and time.

## 72 hours offline on the Pi 4's card (2026-11-03 to 11-06)

The run of windows/out/pi4-week/RUN.md: `rpi4` (1 GB, microSD), image 0.1.5-local.dad9c4d in load-test mode, a fake central at 60 000 people, 100 000 enrollments and 240 000 events, 20 users. The uplink was cut on 3 November at 13:49 for exactly 72 hours, under three busy HC IV days at field rate (`hubload -paced`: 1 680 registrations, 7 800 events and 81 000 working lists), with 6 Android devices syncing every 5 minutes from a second `hubload`.

| | |
|---|---|
| Restarts | none (the Pi 4 restarted on its own on 30 October; not again) |
| Hub memory | 285 MB at most, during the first pull; 77–145 MB offline. Least free memory 470 MB |
| Temperature | 65.7 °C at most, never throttled |
| Written to the card | 6.8 GB by the first pull (60 000 people in 25 minutes); then **1.1–1.3 GB a day** offline (1 105, 1 278 and 1 288 MB) |
| Answers offline | working list 27 ms median, 41 ms p95; new event 56 ms; new person 87 ms |
| Errors | 1 connection error in 7 800 events, 7 in 81 000 working lists |
| Reconnect | online 6 s after the uplink came back; 15 593 pending objects pushed in 3 min 57 s, none refused |

**Long waits.** One new event waited 44 s and one working list 60 s. The card's own counters show 10-second intervals in which writes queued for 30–43 s in total, spread over all three days: the card stalling now and then. Single requests cannot be matched to them, as load-test mode does not log each request. At 1.1–1.3 GB a day, a card rated for 100 TB written lasts far longer than the board; what the stalls mean for staff is an answer that occasionally takes tens of seconds.

**Not answered: W1 on hardware.** Whether Android's incremental syncs levelled off at a day's work after the first day of the outage (section "Android syncs on the Pi 4's card"). The Android `hubload`'s log was not in RUN.md's list of files to copy and was deleted with the server's run folder. The fix is checked in the sandbox (`TestWeekAndroidSyncGetsTheLastDay`). Nor could every confirmed save be checked on central: the run's `confirmed.tsv` is empty. The push's count (none refused, the outbox empty) is the evidence.

## A week offline at HC IV size (2026-10-29)

Question: a large facility works a whole week offline, then the link returns. Does the hub stay responsive while the backlog grows, how long does the backlog take to reach central, and does every save get there?

**Size and speed** (`internal/perf.TestWeekOffline`, `HUB_WEEK=1`, in the sandbox). The hub runs as its own process against the generated fake central at the full HC IV size: 300 000 people, 0.5 M enrollments, 1.2 M events, 2.0 M objects held and a 6.6 GB data folder after the first fill (12 minutes, set-up included). It has 20 users, 6 of them Android devices. The uplink is cut, and seven days follow, each compressed into 30 minutes. Each day has 3 000 saves spread evenly (about 560 registrations of a person, enrollment and event; 360 enrollments of a person registered that week; 1 680 new events; 400 edits). That makes 21 000 saves and 28 441 pending objects at the end of the week. Throughout, staff read 2 times a second (working lists, dashboards, name searches) and each Android device runs an incremental sync every 5 minutes. Those are field rates and are not compressed. A trial with reads and syncs compressed as much as the saves, 24 times the field's, saturated the hub at 100 000 people: an incremental sync took 57 s at the median and the day took 8.7 minutes instead of 5. Then the link returns over 4G (3 Mbit/s, 100 ms, as in "Push over a slow uplink") while staff go on working, and also save 0.4 times a second. A second drain starts from a copy of the same week's data folder and runs over an unlimited link.

| Measure | Day 1 | Day 7 | Draining over 4G |
|---|---|---|---|
| Hub memory (RSS), largest sample; peak | 176 MB; 327 MB at start | 207 MB | 263 MB; peak 530 MB |
| Live heap | 92 MB | 94 MB | 134 MB |
| Hub CPU, mean share of one core | 9 % | 22 % (21–29 % from day 2) | 43 % |
| Data folder (WAL under 10 MB) | 6 896 MB | 6 966 MB (+11.5 MB a day) | 6 807 MB after the drain |
| Objects pending | 4 114 | 28 441 | 0 |
| Save (p50 / p95 / p99) | 10–15 / 24–35 / 50–55 ms | 9–14 / 16–29 / 70–80 ms | 62–70 / 96–109 / 163–208 ms |
| Working list, dashboard, search (p50 / p95) | 9–19 / 16–28 ms | 8–18 / 12–27 ms | 48–87 / 93–139 ms |
| Android incremental sync (p50 / p95) | 3.4 / 5.1 s | 8.3 / 12.0 s | 12.4 / 13.2 s |

**Answers stayed flat through the week**, apart from Android's incremental syncs (below). Memory stayed between 150 and 210 MB, and the data folder grew by the week's data only (about 80 MB). No request failed, and none of the load's slots were skipped.

**The drain.** Over 4G the 28 441 objects (about 21 000 outbox entries) took **43 min 14 s**, 8.5 pushes a second. Central's answers took 2 min 18 s of that; the rest was the link's 100 ms on each push. Staff saved 1 049 times meanwhile, and reads took about 50–90 ms, since the hub answers itself while it has a backlog and shares the CPU with the push. Over the unlimited link the same backlog took **49 s** (433 pushes a second against the fake central). After each drain, every confirmed save was on central: 22 049 after the 4G drain and 21 030 after the unlimited one, none lost, partial, with a wrong value or author, or sent more often than saved. Extrapolated with "Push over a slow uplink", the poor link (300 ms) would take about 2 hours.

**Found and fixed (30 October): Android's incremental syncs re-downloaded the whole outage.** During the week each sync received every person written on the hub since the link went down. The count grew from 72 on day 1 to 3 000 (the device's limit of 500 per program, in 6 programs) from day 2 on. Its time grew with it, from 3.4 s to 8–12 s. The cause is that the hub treats an object written on it as updated at any time (`Index.updatedMatch`, `changedTEs`), because a device's watermark may come from central's clock. Without a device limit (eRegisters) each sync would have got everyone written that week. An object written on the hub now counts as updated by its hub write time, at most a day older than `updatedAfter`, the same floor central's times get (`updatedFloor`): a sync gets the last day's work, not the whole outage (`TestWeekAndroidSyncGetsTheLastDay`; FINDINGS.md, "Found by adversarial testing" 21). The week has not been run again with the fix.

**Limits of this run.** The fake central lists only generated people per program. So the pull after the push does not fetch back the week's people, and a weekly full pull, which a real week would make due at reconnection, would hide them; it was left off here (the real DHIS2 run below does both). All clients use Basic auth, as `cmd/hubload` does. The sandbox was shared with another agent's browser tests. The 4G drain's numbers include a hub restart from the copied folder (unlocked in 2–3 s).

**Correctness on a real DHIS2** (`internal/adversarial.TestWeekOfflineCentral`, `HUB_WEEK=1`; D9's test with 7 days and 21 000 saves). Against `agent-adv-network-42` (2.42.7, synthetic profile), three clients (two Capture web, one Android) saved 21 020 times offline as fast as the hub answered, in 1 min 53 s. Two in three were registrations (14 029 people) and one in three an update (6 991): 42 087 objects pending, a 195 MB data folder. Offline saves took 11 ms (median) in the first tenth and 12 ms in the last. Central got 100 people of its own meanwhile. The hub's clocks were then moved back a week, so the full pull was due. On reconnection one sync cycle pushed and pulled everything in **12 min 36 s**. The push took 8 min 27 s, 41.4 entries a second. Central answered a registration in 19 ms and an update in 18 ms (median; p90 31 and 29 ms), and its answers were as fast at the end of the push as at the start (20 ms against 19). The full pull, 445 listings, took 3 min 22 s of central's time. Central held all 14 029 registrations whole, by their authors, with the last confirmed values: none lost, partial, duplicated or wrong. The tokens still worked, and the 100 people registered on central were read offline afterwards. Reading them all back as the superuser took 1 h 20 min, at about 150 ms a request. A first attempt was cut off when the instance was stopped during its push (2 823 entries pushed), and is not counted.

Rerun: `HUB_WEEK=1 go test ./internal/perf -run TestWeekOffline -timeout 12h -v` (about 4.5 hours; `HUB_PERF_SCALE`, `HUB_WEEK_DAY`, `HUB_WEEK_DAYS`, `HUB_WEEK_SAVES`, `HUB_WEEK_READ_RATE`, `HUB_WEEK_SYNC_EVERY`, `HUB_WEEK_LINKS` change it), and `HUB_WEEK=1 HUB_MANIFEST=… go test ./internal/adversarial -run TestWeekOfflineCentral -timeout 4h -v` (`HUB_WEEK_LIVE_SAVES`). Raw data: `testresults/week/20261029-1743/` (samples, ops, ledger, summary, the hub's log) and `testresults/week-live/` (git-ignored).

## Running the tests

In process on a development machine (unconstrained), with `HUB_PERF_SCALE="tes,enrollments,events"` and `HUB_PERF_USERS`:

- `TestPull`: first pull, heap, disk, reboot to first login.
- `TestQueries`: working lists, searches, Android pages, dashboards (`HUB_PERF_ONLY` and `HUB_PERF_RUNS` to profile one kind).
- `TestCentralLoad`: what the hub asks of central.
- `TestCentralWork` and `TestCentralWorkReport`: central's database work and time per unit of work, against a live eRegisters instance (section "Central's load from one facility, three ways").
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

**On real hardware, with the load from the Mac** (`perf/remote-load.sh`): the fake central and the users run on the Mac, so they take nothing from the machine under test, and the hub there points at the Mac's fake central. `perf/remote-load.sh <hub URL> <size> [users] [duration] [flap]` waits for the hub, sets it up, times the first pull, runs a working day with the uplink flapping, a hammer run, and reads every confirmed save back. On Windows the hub's half is `stress.ps1 -Size <size> -Central http://<Mac>:8090` (kit: `perf/windows/kit.sh`, `ARCH=arm64` for Windows on ARM), which also samples the disk's latency and queue. Unattended, for a series of sizes: `stress.ps1 -Queue -Central …` on the PC starts a fresh hub whenever a fake central appears at that address (`GET /fake/run` says which run and size) and stops it when the fake central goes away; `perf/remote-queue.sh <hub URL> "100k 300k 500k 700k" …` on the Mac runs the sizes one after another. On a Pi the same is the image's load-test mode, switched on by `hub-loadtest.txt` on the boot partition (image/README.md); several machines run at once with a `LABEL` and `PORT` per queue.

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

Sent outbox entries are deleted 30 days after central accepted them (docs/DEPLOYMENT.md). Until 2026-10-28 none were deleted, so the first sync cycle after that upgrade deletes the backlog. In process (x86 sandbox, `internal/store`), 295 000 sent entries of 1.5 KB took 24 s in one transaction, with every save waiting behind it; in transactions of 1 000 entries they took 4.9 s, 16 ms each. A cycle with nothing to delete spends 6 ms on it.

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
- **2026-10-23, incremental syncs again.** On the 2-CPU Windows VM at 100 k, with 18 Android users, memory grew to 7.7 GB and the hub exited: each incremental sync read every child of every changed person, and searches whose clients had given up kept running. The check now reads central's children newest first, five columns, and stops at the first the user may read; searches stop when their client has gone (docs/INCREMENTAL-SYNC-FIX.md). In the sandbox, one core per hub: the check's allocations 7.6 GB → 1.6 GB in 3 minutes, incremental sync 1 min 19 s → 34 s (p50), Android page 10.6 s → 5.2 s.
- **2026-10-26, incremental syncs on a hard disk; online saves.** At 300 k on the hard-disk VM (above) incremental syncs took 12 minutes and starved everything else. The list of changed people narrowed nothing: SQLite walked every enrollment of the program and read each one's row and its person's before checking the list. The list is now one JSON value checked on the person's identifier in the index the search walks, at any size (the switch at 500 people and its check in Go are gone). Saves made online were shared with each user in a transaction of its own, each flushed to disk, and stalled for up to 85 s after reconnects; now one transaction. In the sandbox at 100 k, 18 Android users, one core: incremental sync 3.9 s → 0.33 s (p50, about 330 people changed) and 3.9 s → 0.72 s (about 1 000), the hub's CPU 25–37 s → 11 s in 2 minutes; an online save with 60 users 12.6 ms → 4.3 ms.
- **2026-10-26, Raspberry Pis.** The first runs on the boards (section "On Raspberry Pis"): a Pi 5 with an SSD at 100 k and 300 k, a Pi 4 with 1 GB and an SD card at 10 k, 30 k and 60 k, with this day's fixes. Both served every size; the Pi 4's SD card makes its saves' tails ten times the Pi 5's.
- **2026-10-27, writes while online.** The Pis' second round counted bytes: while online the hub wrote 235–272 MB a minute, against 9 MB offline, because it stored every object of every online read anew, changed or not (section "On Raspberry Pis"). An object whose merged data equals what the hub holds is now left as it is; only the user's visibility is recorded. In the sandbox at 100 k, 60 users of whom 18 Android, online: 233 MB → 66 MB a minute over the same 9 minutes.
- **2026-10-28, push over a slow uplink.** One request per save, one at a time: 7 entries a second over a 4G-like link (100 ms), 2.8 over a poor one (300 ms), 37 unlimited; latency, not bandwidth, sets the rate (section "Push over a slow uplink"). With saves at 2 a second the hub keeps up over 4G but runs sync cycles back to back and never passes saves through again; a change is proposed there. The backlog that grew in the hardware session fits the 5-minute `reconnect_delay`.
- **2026-10-29, ten hours in the sandbox.** M1's sandbox part (section "Ten hours in the sandbox"): 10 211 confirmed saves through five outages and three kills, none lost or duplicated on central; memory, open files and the outbox flat, the data folder growing with the data only. Found: concurrent logins of one user leave extra tokens on central, and a person saved during a full pull is hidden from its author offline until the next pull.
- **2026-10-29, a week offline at HC IV size.** 21 000 saves offline at 300 000 people: memory 150–210 MB, answers flat; drained in 43 minutes over 4G and 49 s unlimited; every save on central, and on a real DHIS2 21 020 saves in a 12.5-minute cycle (section "A week offline at HC IV size"). Found: Android incremental syncs re-downloaded every person written during the outage (fixed 30 October).
- **2026-10-30 to 11-02, 72 hours on the Pi 5.** 36 757 saves at 4 times an HC IV's rate, 4 outages and 5 power cuts: memory median 80 MB, the data folder 87 MB a day, 6.2 GB written a day; every confirmed save on central (section "72 hours on the Pi 5").
- **2026-11-03, Android syncs on the Pi 4's card.** Incremental syncs of 220 people took 58 s on `rpi4`: the fake central's people, dated at its start, were candidates for 24 hours, and each was checked by reading its children. `changedTEs` now leaves out people with nothing written on the hub whose newest row is older than `updatedAfter`. In the sandbox at 60 k: 5.4 s → 0.37 s alone, 8.2 → 0.9 s six at once (section "Android syncs on the Pi 4's card").
- **2026-11-05, a slow uplink; name searches.** At Ngelehun CHC size (20 000 people) the weekly full pull took 2 minutes at 31 KB/s compressed and 6 minutes uncompressed; single requests through to central took about 0.3 s, answered by the hub 1–4 ms (section "A slow uplink"). A name search that few people match read every enrollment of the program: 151 ms at 100 000 people. Its matches now drive the search from an index: 20 ms.
- **2026-11-05, app files from the hub's copy.** A browser with an empty cache downloaded Capture's 6.9 MB bundle through the uplink although the hub held a copy: 77 s for the app's start at 31 KB/s. The hub now serves its copy once central confirms it is current: 6.4 s.
- **2026-11-06, 72 hours offline on the Pi 4's card.** No restart; memory 285 MB at most; 1.1–1.3 GB written to the card a day; 15 593 objects pushed in 4 minutes at reconnect, none refused. W1 not confirmed on hardware: the Android log was lost (section "72 hours offline on the Pi 4's card").
- **2026-11-06, answering staff from the hub while online.** On a typical rural link (31 KB/s, 1 s per request, 3 % loss) Capture's dashboard took 4.4 s through to central and 0.12 s from the hub, its start 19 s and 4.8 s; a setting per programme (section "A slow uplink").
- **2026-11-09, central's load from one facility.** Central's database work and server time per unit, on 2.43 with Uganda's eRegisters configuration, added up to a facility-day: with Capture web, a hub answering from its copies takes central from 82 s of DB time and 1 163 s of server time a day to 28 s and 177 s; with eRegisters, which works from its own copy, a hub adds about 10 % of time and nearly doubles the rows, and its hourly per-user refresh of `dataEntry/metadata` fetches 39 MB each time (section "Central's load from one facility, three ways"). Found: with local reads, Capture does not open a person it has just registered, and eRegisters does not start.
