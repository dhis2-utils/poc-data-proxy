# Event programs through the hub, and retention by program

Status: implemented 2026-10-15 (plan: docs/plans/2026-10-15-event-programs.md in the private notes; results in docs/FINDINGS.md, "Event programs through the hub").

## Goal

Event programs (`WITHOUT_REGISTRATION`) work offline through the hub as tracker programs do: staff see the facility's events in working lists and Android's download, and capture and edit events offline; every event reaches central as its author's. Today the hub does not hold event programs at all and answers 503 for them offline (FINDINGS item 20): Android tolerates it and loses them offline; Capture web has no working list.

The hub holds every event of a facility's event programs, bounded by a retention table by program that this work builds and that tracker programs will use later.

## What central does (checked on Sierra Leone 2.43)

- Events of event programs have no `enrollment` and no `trackedEntity`; their org unit is `orgUnit`.
- Ngelehun CHC has 7 event programs (0 to 422 events each); one has a non-default attribute category combination ("Implementing Partner"), so which events a user sees and may write depends on category option sharing.
- Writing an event or enrollment with an attribute option combination needs data write access to each of its category options (E1099 otherwise).

## Scope

In:

- Event programs assigned to the facility, for Capture web and Android: working lists, single events and Android's paged download (`orgUnitMode=DESCENDANTS`, `includeDeleted`, `updatedAfter`) offline; new and edited events offline through the tracker import and outbox.
- Offline writes with a non-default attribute option combination, for events of any program and for enrollments (2.43's `enrollmentCategoryCombo`).
- A retention table by program in central's dataStore, applied to event programs.

Out:

- Deleting events offline (refused, as for every client).
- Retention for tracker programs (it will read the same table; nothing changes for them now).
- Reading enrollments hidden by category option sharing (the existing 2.43 known gap stays).

## Design

### Catalog and access

- The catalog refresh also fetches the facility's event programs (`programType:eq:WITHOUT_REGISTRATION`), with their stages and category combination, marked as event programs (no tracked entity type).
- The access refresh already reads program and stage sharing for every catalog program, so event programs' sharing comes with it.
- The access refresh also reads, for each attribute category combination used by catalog programs (events and 2.43 enrollments), which of its category options the user may write (data write sharing). Stored in the user's access list.
- Programs with an attribute category combination are pulled per user, as tracker programs with one already are: category option sharing decides what each user sees.

### Pull

- Per event program and access group: `/api/tracker/events?program&orgUnit=<facility>&orgUnitMode=SELECTED&updatedAfter&includeDeleted=true`, paged, whole events (small: no ID listing first, as tracker needs).
- Each event is stored as an event without person or enrollment, owned by its `orgUnit`, visible to the group's users; a deleted one is removed.
- The weekly full pull (`full_pull_interval`) removes visibility for events no longer listed, as for people.
- With retention set for the program, the pull asks only for events whose `occurredAt` is within it (`occurredAfter`).

### Retention table

- Central's dataStore, `facility-hub-config/retention`: `{"defaultMonths": 0, "programs": {"<program>": 12}}`. 0 or no entry: keep everything. Read at the catalog refresh with the hub token, as `facility-hub-config/aggregate` is.
- Pruning, each cycle: synced event-program events whose `occurredAt` fell out of their program's window are removed; events with pending edits are kept.
- Aggregate keeps its own key (it counts periods, not months).

### Answering offline

- The existing searches and single-event endpoints answer for event programs. Where checks assume a person (search scope, ownership, protected programs), events without one follow event program rules: the event's org unit and program and stage access. Which events a user sees where category option sharing decides it comes from the per-user pull, as for tracker programs with an attribute category combination.
- Working lists sort by event date or creation: an index is added only if the measurement below calls for it.

### Writes offline

- Event-program events are validated without an enrollment: program and stage write access, the facility as org unit, value types as today.
- **Attribute option combinations:** an event or enrollment with a non-default attribute option combination is accepted offline when the user has data write access to every category option in that combination; otherwise refused with central's E1099. This replaces today's blanket refusal (E1055 "not supported offline") for events of programs with an attribute category combination and for 2.43 enrollments with an enrollment category combination. The combination must be one of the program's category combination's (central's E1055 for a missing or wrong one stands).
- Deletes stay refused offline.

## Testing

- Profiles: the synthetic event program gets more data, and a second event program with the donor attribute category combination (options shared per user) is added; Sierra Leone brings its 7 event programs; Laos whatever it has at its facility.
- Oracles: the read and write oracles generate queries and writes per program, event programs included; the twin users that differ only in a category option's sharing check the attribute option rule both ways (central accepts / E1099), for events and, on 2.43, enrollments.
- Retention: the pull's window and pruning, against the fake and live.
- Android: the download test compares record counts with event programs included.

## Performance

The fake central generates event programs at size (e.g. two programs with 150 000 events each). Measured (`internal/perf`): first pull with and without retention, a working list's first and later pages, Android's event download, pruning.

## Open questions

- Default retention when a country sets none: keep everything (as decided). Measured: 2 × 150 000 events pull in under 30 s and page in milliseconds with the indexes (docs/PERFORMANCE.md), so keeping everything is affordable in time; storage (about 560 MB for 300 000 events) is what argues for retention on microSD.
