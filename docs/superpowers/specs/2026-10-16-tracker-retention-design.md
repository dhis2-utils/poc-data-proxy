# Retention for tracker programs and tracked entity types

Status: design approved in conversation 2026-10-16; spec for review.

## Goal

A hub keeps each tracker program's data for a window the country sets, not five years of everything, so its data file and first fill fit Pi-class storage (docs/PERFORMANCE.md: the busy HC IV sizing is 3.1 million events with everything kept, 1.2 million with retention by programme). People are kept, so search and duplicate checks work offline. Optionally, tracked entities of other types (lab samples, commodities) are not kept forever.

Event programs already have retention (docs/superpowers/specs/2026-10-15-event-programs-design.md); this extends the same table and the same pattern (a window in the pull, plus pruning) to tracker programs and tracked entity types.

## Decisions (from the conversation)

- Active enrollments are kept in full, with all their events, whatever their age.
- A completed or cancelled enrollment is old when its last change (`updatedAt`) is before the program's cut-off.
- An old enrollment is kept as a stub (no events, no data), used only for the "enrol once" check.
- The window is applied in the pull (approach B), not only by pruning, so a first fill downloads less.
- Retention by tracked entity type is included; persons stay kept by default.
- Two offline checks come with it: E1015 and E1016.

## Settings

Central's dataStore, `facility-hub-config/retention`, read at the catalog refresh with the hub token (as today):

```json
{"defaultMonths": 0, "programs": {"<program>": 24}, "trackedEntityTypes": {"<type>": 6}}
```

- `programs` and `defaultMonths` now apply to tracker programs as well as event programs. 0 or no entry keeps everything.
- `trackedEntityTypes`: months per tracked entity type. No entry or 0 keeps that type's entities forever; there is no default for types, so persons are kept unless a country names their type.
- A country that sets nothing sees no change.

## What is kept

- **People** (and every tracked entity of a type without a window): all, always.
- **Enrollments:**
  - active: in full, with all events, any age;
  - completed or cancelled: in full while their `updatedAt` is within the program's window; after that, a stub.
- **A stub** is the enrollment's row with its program, tracked entity, status, `enrolledAt` and `updatedAt`, and nothing else: no payload data, no events, no notes. A column marks it. Stubs are hidden from every read and used only for E1016.
- **Tracked entities of a type with a window** are removed when their own `updatedAt` is before the type's cut-off and none of their enrollments is held in full (only stubs or none); their stubs and relationships go with them.
- **Never pruned:** anything with changes waiting in the outbox, or rejected and not resolved, and anything a pending change refers to (its tracked entity, enrollment, relationship ends).

## Pull

- **Full pulls** (the first, and the weekly `full_pull_interval`) of a program with a window list, per access group as today:
  1. people with an active enrollment in the program (`enrollmentStatus=ACTIVE`);
  2. people changed since the cut-off (`updatedAfter=<cut-off>`).

  The union is fetched as today (only what the hub lacks or holds older). Visibility follows the union.
- **Incremental pulls** are unchanged: what they list changed since the last pull, so it is within the window.
- **No re-fetch of what was pruned.** A listing returns each person's enrollments and events in the program, old ones included, and today the hub fetches a person again when the listing shows something it does not hold. After pruning, that is every person with a stub: each full pull would fetch them again, store them as stubs again, and repeat. So anything listed under an enrollment the hub holds as a stub, with the same `updatedAt`, counts as held; a person is fetched again only if the stub's enrollment changed on central, or something else about them is newer.
- **On storing,** a completed or cancelled enrollment whose `updatedAt` is before the cut-off is stored as a stub, without its events (central may send one, e.g. for a person changed recently through another program).
- **Type pulls** (`tet:<type>`, people enrolled in nothing) of a type with a window list only entities changed since its cut-off.
- **Pruning,** each sync cycle, after the pull: enrollments that aged out become stubs (their events deleted), then tracked entities of types with a window that have nothing left in full are removed. In transactions of 1 000 rows, as event pruning (docs/PERFORMANCE.md). The index cache is dropped after pruning, as today.

## Answering offline

- Stubs and pruned objects never appear in lists, searches, a person's enrollments or Android's download. A pruned enrollment is not a deletion: the download never returns it as deleted, so a device that holds it keeps it.
- A person whose enrollment in a program was pruned is not listed in that program; they are found by attribute search and in other programs.
- A single read of a pruned enrollment, one of its events, or a pruned tracked entity answers "needs connection" (it exists on central), not 404.
- Relationships whose other end is pruned are hidden (the existing rule: both ends must be readable).
- **Writes offline:**
  - E1015: a new active enrollment for a tracked entity that already has an active enrollment in that program (held in full, so the hub knows) is refused with central's code and message.
  - E1016: a new enrollment in a program with `onlyEnrollOnce` for a tracked entity with any enrollment in it, stub included, is refused likewise. The catalog records `onlyEnrollOnce` per program.
  - An edit of something pruned (a device editing an old event it still holds) answers "needs connection"; it stays on the device until it syncs online through the hub.
- The 2.42 and 2.43 messages and codes are taken from central (checked live).

## Load on central

- First fill: smaller, since old closed enrollments and their events are not sent; this is the heaviest moment, during rollout waves.
- Incremental pulls: unchanged. Pruning and the E1015 and E1016 checks: on the hub only.
- Full pulls: two id listings per program and access group instead of one; the second is limited to changes since the cut-off. The active-enrollment listing makes central filter on enrollment status; its time on central is measured live (Sierra Leone).
- A full pull repeated after pruning must fetch nothing new (the rule above); the performance test checks it.

## Testing

- **Fake central:**
  - the full pull's two listings and the type pull's window, against the fake's support for `enrollmentStatus` and `updatedAfter`;
  - a full pull after pruning fetches none of the pruned people again;
  - stubs written instead of full old enrollments, without events;
  - pruning: enrollments to stubs; entities of types with a window removed with stubs and relationships; nothing pending or referenced by a pending change pruned;
  - answers: stubs hidden, "needs connection" for pruned objects, Android's download without them and without deleted markers;
  - E1015 and E1016 offline, including E1016 against a stub;
  - no settings: nothing changes (the existing suites).
- **Live, 2.42 and 2.43:**
  - central accepts the listings' parameters with a program (`enrollmentStatus=ACTIVE`, `updatedAfter`), and the hub's full pull with a window set reads the same people as central lists; the active-enrollment listing's time on central is recorded (Sierra Leone);
  - the write oracle adds offline E1015 and E1016 cases, which must match central's refusals;
  - the read oracle narrows central's answer to each program's window when a window is set. Data on central cannot be aged, so pruning itself is tested on the fake.
- **Performance** (`internal/perf`): the busy HC IV sizing, first fill with and without retention, and a pruning run; central's requests and bytes for a full pull with and without retention, and for a second full pull after pruning (which must fetch no tracked entity again); numbers in docs/PERFORMANCE.md, which replace its "Retention by programme (planned)" section.

## Out of scope

- Retention for persons by default, and any per-person rule (persons are kept; a type window for persons is possible but documented as risking duplicate registrations offline).
- Wards or departments below the facility (DEPLOYMENT.md, possible future enhancement).
- Aggregate retention (its own key, unchanged).
