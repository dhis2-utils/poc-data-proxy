# Incremental sync under load: measured problem and proposed fix

Status: proposed, 23 October 2026. Not built.

On a small Windows PC, the hub ran out of memory during a load test at 100,000 people. The cause is the offline answer to incremental syncs (`updatedAfter`), which Android Capture and eRegisters send. Two changes are proposed: a cheaper check for "changed since", and stopping a search when its client has gone. Both keep the hub's answers exactly as they are now.

## The measurement

The test ran on 23 October 2026 with `perf/remote-queue.sh` on a Mac against `perf/windows/stress.ps1 -Queue` on a Windows 11 VM: 2 CPUs, 4 GB of memory, its disk on a USB hard disk. The size was 100,000 people, 166,666 enrollments and 400,000 events. There were 60 users, of whom 18 synced incrementally (hubload's default, `-web 0.7`). The working day lasted an hour, with the uplink 10 minutes down and 5 minutes up.

The first pull of all users took 1 h 4 min. From the first cut at 20:41, the hub's memory grew without stopping:

| Time | Private memory | Free memory | Disk read latency |
| --- | --- | --- | --- |
| 20:35, before the cut | 151 MB | 1,502 MB | 0 ms |
| 20:52 | 1,071 MB | 623 MB | 5 ms |
| 21:14 | 2,754 MB | 73 MB | 2 ms |
| 21:48 | 6,178 MB | 219 MB | 277 ms |
| 22:00 | 7,742 MB | 401 MB | up to 610 ms |

At 22:01 the hub exited with code 2. For the first ten minutes the disk was not slow, but the CPU was: about 1.5 of the 2 cores were busy. Once memory ran out, Windows paged it to the USB disk. Pushes then slowed to one outbox entry every 5–25 seconds, and the outbox grew from 156 to 691 entries waiting across three up windows. In the last window the hub did not go online at all.

## The cause

The same load ran in the sandbox with Go's heap profiler on (`HUB_PPROF`). On 4 cores the hub kept up, and its memory stayed between 90 and 175 MB. On one core it rose to 365 MB and came back to 110–150 MB, with 14–28 goroutines. The profile taken near the peak shows where the work goes: in 3 minutes, `(*Index).childStates` allocated 5.7 GB, through `QueryObjectPairs` and `ScanObject`. One call held about 44 MB while it ran.

`childStates` is called from `updatedMatch` in `internal/tracker/search.go`. An incremental sync asks for people changed since a date, and a person counts as changed when they, or any enrollment or event of theirs the user may see, changed after it. When at most 500 people have changed, the search narrows to that list in SQL. Above 500, it checks each candidate person in Go, and for each one it reads all their enrollments and events and builds an object per row. Every page of every incremental sync repeats this.

On the VM, those calls ran long enough to overlap. Each syncing device starts a new sync every 5 minutes, and the load tool gives up on a request after 2 minutes. Nothing stops the work the hub has already started: in `internal/store` and `internal/tracker`, 5 database calls take a context and 105 do not. Abandoned searches therefore keep their memory and their CPU, so the next ones run slower still. Memory grows until the process dies.

The fake central makes the worst case constant: it gives every person with an active enrollment an update time of "now", so most of the 100,000 people count as changed in every sync. Real data is gentler. But a busy facility after a day's work can pass 500 changed people, and eRegisters asks with an older date offline (its `updatedAfter` comes from a cached `serverDate`, `docs/FINDINGS.md`, "eRegisters through the hub").

## Proposed change

### 1. A cheaper "changed since" check

Whether a user may see an enrollment or event is decided per object (`visibleWith` in `internal/tracker/query.go`). The check looks at whether central listed that object to that user (the per-user bitmap), whether the facility owns it, whether it is deleted or a retention stub, and the user's program and stage access. A summary per person or per program stage would lose that, and the answer could then differ from central's. So the per-object check stays as it is. What changes is how the children are read:

- Read only the columns the check needs (slot, owner, origin, deleted, stub, program, stage, update time), without the payload and without building a full object per row.
- Read them in the order that decides fastest: hub-written objects first, then the rest newest first. Stop at the first child the user may see. A hub-written child the user may see means "changed", as today. Otherwise the first visible child is the newest one the user may see, and its time is compared with the date, as today.

The result is the same as today's by construction: the same check, on the same objects, giving the same newest time. Most people then cost one or two rows instead of all their events. In the worst case it is still one small query per candidate person, so the cost still grows with the number of changed people, but with far less memory and CPU.

### 2. Stopping abandoned searches

The request's context is passed into offline searches, and the per-row loop checks it. When the client has gone, the search stops and nothing is written. This applies to every offline search, not only incremental syncs: on a slow machine, web searches can pile up the same way.

### Not proposed

A limit on how many searches run at once is left out. AGENTS.md asks for a measured problem before a new mechanism, and the two changes above may be enough. If the measurement after them still shows memory growing on the VM, a limit is the next step, with its own numbers.

## Guards and measurement

Searches are one of the areas AGENTS.md guards: the SQL part and the Go part must mean exactly what central means. The change needs:

- a unit test that compares the new check with the current one on a fixture where children differ per object: not in the user's bitmap, owned by another facility, deleted, a stub, written on the hub, in a stage the user cannot read;
- `TestQueryPlansUseIndexes` with the new query, so the children are read through `objects_te` and not by a table scan;
- the live read oracles against DHIS2 (`go run ./cmd/hubtest run -profile <synthetic|sierra-leone|laos> -version 42`, and 43), with no LEAK or DIFFERS;
- the race detector on `internal/e2e`, `internal/live`, `internal/tracker` and `internal/store`.

The numbers before and after come from the sandbox reproduction on one core: 100,000 people, 60 users of whom 18 sync incrementally, 10 minutes down and 5 up. Before the change, `childStates` allocated 5.7 GB in 3 minutes, and memory peaked at 365 MB. After it, the same run gives the allocations, the peak memory and the latency of an incremental sync. The VM run is then repeated at the same size and settings.

## Meanwhile

Until this is built, `perf/remote-load.sh` runs with few incremental-sync devices: `WEB=0.95` by default, 3 of 60 users. Android Capture is on hold for hubs, and a facility has a few eRegisters browsers at most. The weekend runs on the VM and the Pis measure the hardware with that mix.

## Open questions

- Should the fake central give realistic update times, so that an incremental sync sees a day's changes rather than the whole facility? The worst case stays useful as a separate setting.
- Is 500 the right point to switch from the SQL list to the per-person check, once the check is cheap? To measure, not to guess.
