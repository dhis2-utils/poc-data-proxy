# Tracker Retention Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep each tracker program's data for the window the country sets (active enrollments in full, closed ones until their last change ages out, then a hidden stub), optionally remove tracked entities of types with a window, and refuse E1015/E1016 offline.

**Architecture:** The existing retention table gains `trackedEntityTypes`; the catalog learns `onlyEnrollOnce`. A stub is an enrollment row whose payload is cut to a few fields and carries `"hubStub": true`; a `stub` column (computed from the slim JSON in SQL, like `deleted`) lets reads and queries leave stubs out. Full pulls of a program with a window list active enrollments plus changes since the cut-off; stale checks treat what is listed under an unchanged stub as held. A pruning step per cycle turns aged enrollments into stubs, deletes their events, and removes tracked entities of types with a window that have nothing left in full.

**Tech Stack:** Go, SQLite (ncruces), the fake central (`internal/fakecentral`), the live harness (`internal/testkit`, `cmd/hubtest`).

**Spec:** `docs/superpowers/specs/2026-10-16-tracker-retention-design.md`

## Global Constraints

- Settings: `facility-hub-config/retention` = `{"defaultMonths": N, "programs": {"<program>": N}, "trackedEntityTypes": {"<type>": N}}`; 0 or no entry keeps everything; types have no default.
- `programs` and `defaultMonths` apply to tracker programs as well as event programs.
- Active enrollments: never pruned. Closed (COMPLETED, CANCELLED): old when `updatedAt` < the program's cut-off.
- Never pruned: rows not `synced` (pending, rejected), anything in a pending outbox entry, anything a pending change refers to.
- Stubs and pruned objects: hidden from every read; single reads answer "needs connection", never 404; never returned as deleted.
- Offline E1015 and E1016 use central's codes and messages.
- AGENTS.md: `go vet ./...`, `go test ./...`, `go test -race` on `internal/e2e internal/live internal/tracker internal/store` before each commit; live runs with `go run ./cmd/hubtest run -profile <p> -version <v>`; performance numbers from `internal/perf`.
- Go toolchain: `export PATH=$HOME/sdk/go/bin:$PATH`.

## Review Focus

1. A tracked entity whose only enrollment in a program is a stub, edited offline (attributes) and pushed: the edit must reach central and the stub must not be merged into a full payload (Task 5 test `TestStubNeverMerges`).
2. An enrollment that becomes active again on central after it was stubbed (reopened): the next incremental pull must replace the stub with the full enrollment and its events (Task 5 test `TestReopenedEnrollmentReplacesStub`).
3. A person with an active enrollment in one program and a stub in another: lists of the first program show them, lists of the second do not, the person's record shows only the active one (Task 3 test `TestStubHiddenFromReads`).
4. Pruning while a tablet edits an event of the enrollment being stubbed: the pending event keeps the enrollment out of pruning (Task 6 test `TestPruneKeepsReferenced`).
5. A country with no retention settings: no request, store or answer changes (Task 5 `TestNoRetentionUnchanged` plus the existing suites).

---

### Task 1: Settings and catalog: type windows and onlyEnrollOnce

**Files:**
- Modify: `internal/access/retention.go`
- Test: `internal/access/retention_test.go`
- Modify: `internal/access/access.go` (ProgramMeta)
- Modify: `internal/compat/v42.go` (`ProgramFields`), `internal/compat/v43.go` if it overrides `ProgramFields`
- Modify: `internal/core/refresh.go` (catalog build)
- Modify: `internal/fakecentral/fakecentral.go` (program metadata, `OnlyEnrollOnce` field on `Central`)
- Test: `internal/live/retention_test.go` (new)

**Interfaces:**
- Produces: `access.Retention.TrackedEntityTypes map[string]int`; `func (r Retention) TypeCutoff(tet string, now time.Time) (string, bool)`; `access.ProgramMeta.OnlyEnrollOnce bool` (json `onlyEnrollOnce,omitempty`); `fakecentral.Central.OnlyEnrollOnce bool` (the fake's tracker program `Program` reports it).

- [ ] **Step 1: Write the failing unit test** in `internal/access/retention_test.go`:

```go
func TestTypeCutoff(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	r := Retention{DefaultMonths: 24, TrackedEntityTypes: map[string]int{"Sample": 6}}
	if c, ok := r.TypeCutoff("Sample", now); !ok || c != "2026-04-16" {
		t.Fatalf("Sample: %q %v", c, ok)
	}
	if _, ok := r.TypeCutoff("Person", now); ok {
		t.Fatal("types have no default: persons are kept")
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/access -run TestTypeCutoff` — Expected: FAIL (`TrackedEntityTypes` undefined).

- [ ] **Step 3: Implement** in `internal/access/retention.go`:

```go
type Retention struct {
	DefaultMonths      int            `json:"defaultMonths"`
	Programs           map[string]int `json:"programs"`
	TrackedEntityTypes map[string]int `json:"trackedEntityTypes,omitempty"`
}

// TypeCutoff is the oldest date kept for tracked entities of this type; ok is false
// when the type keeps everything (no entry or 0: there is no default for types, so
// persons stay unless a country names their type).
func (r Retention) TypeCutoff(tet string, now time.Time) (string, bool) {
	m := r.TrackedEntityTypes[tet]
	if m <= 0 {
		return "", false
	}
	return now.AddDate(0, -m, 0).Format(time.DateOnly), true
}
```

Update the type's doc comment: "Event and tracker programs use it (by occurredAt for events of event programs, by an enrollment's last change for tracker programs)".

- [ ] **Step 4: Run** `go test ./internal/access` — Expected: PASS.

- [ ] **Step 5: Write the failing catalog test** in `internal/live/retention_test.go`:

```go
package live

import (
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The catalog records which programs allow one enrollment ever.
func TestCatalogOnlyEnrollOnce(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.OnlyEnrollOnce = true
	e.Setup()
	if pm := e.App.Hub.Catalog().Programs[fc.Program]; pm == nil || !pm.OnlyEnrollOnce {
		t.Fatalf("onlyEnrollOnce not in the catalog: %+v", pm)
	}
}
```

(import `fc "github.com/dhis2/facility-hub/internal/fakecentral"`.)

- [ ] **Step 6: Run** `go test ./internal/live -run TestCatalogOnlyEnrollOnce` — Expected: FAIL to build (`OnlyEnrollOnce` undefined).

- [ ] **Step 7: Implement.**
  - `access.ProgramMeta`: `OnlyEnrollOnce bool \`json:"onlyEnrollOnce,omitempty"\`` with comment "one enrollment ever per tracked entity (central's E1016)".
  - `compat` `ProgramFields`: add `onlyEnrollOnce` to the field list: `"id,name,onlyEnrollOnce,trackedEntityType[...]..."` (both versions if v43 overrides).
  - `core/refresh.go` catalog struct: `OnlyEnrollOnce bool \`json:"onlyEnrollOnce"\``; set `pm.OnlyEnrollOnce = p.OnlyEnrollOnce`.
  - `fakecentral.Central`: field `OnlyEnrollOnce bool // the TB program allows one enrollment ever`; in the programs handler add `"onlyEnrollOnce": c.OnlyEnrollOnce` to `prog`.

- [ ] **Step 8: Run** `go test ./internal/live -run TestCatalogOnlyEnrollOnce ./internal/access` — Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/access internal/compat internal/core/refresh.go internal/fakecentral/fakecentral.go internal/live/retention_test.go
git commit -m "Retention: windows by tracked entity type; catalog records onlyEnrollOnce"
```

---

### Task 2: Store: the stub column and pruning queries

**Files:**
- Modify: `internal/store/store.go` (data migration)
- Modify: `internal/store/objects.go`
- Test: `internal/store/retention_test.go` (new)

**Interfaces:**
- Produces:
  - column `objects.stub INTEGER NOT NULL DEFAULT 0`, set in `sqlPutObject` from `json_extract(?18,'$.hubStub')`;
  - `store.Object.Stub bool` (scanned in `ScanObject` and `QueryObjectPairs`, `objectColumns` gets `"stub"` before `"slim"`);
  - `store.ObjectState.Stub bool` (from `ObjectStates`, index `objects_state` rebuilt as `(uid, updated_at, slot, stub)`);
  - `func (s *Store) AgedEnrollments(program, owner, cutoff string) ([]string, error)`: closed, synced, non-stub enrollments of the program owned by `owner` with `updated_at < cutoff` (cutoff a date; compare as text), none of them or their events in a pending outbox entry;
  - `func (s *Store) StubEnrollmentsEvents() ([]string, error)`: synced events whose enrollment row is a stub and that are not in a pending outbox entry;
  - `func (s *Store) PrunableEntities(tet, owner, cutoff string) ([]string, error)`: synced tracked entities of the type owned by `owner`, `updated_at < cutoff`, with no non-stub enrollment, not in a pending outbox entry, and no pending object referring to them (`te_uid`, `rel_from`, `rel_to`);
  - `func (s *Store) EntityLeftovers(tes []string) ([]string, error)`: the uids of stubs and relationships of these tracked entities (for deleting with them).

- [ ] **Step 1: Write the failing test** `internal/store/retention_test.go`. Use the existing store test helpers (see `internal/store/*_test.go` for `openTest`/`put` style; follow them exactly). Cases:
  - put an enrollment with slim `{"status":"COMPLETED","updatedAt":"2024-01-01T00:00:00.000"}` and one ACTIVE with the same date and one COMPLETED dated 2026-09-01; `AgedEnrollments(program, facility, "2025-10-16")` returns only the first;
  - put an enrollment whose slim has `"hubStub": true`; `GetObject` returns `Stub == true` and `ObjectStates` reports `Stub` for it;
  - an event under that stub enrollment is returned by `StubEnrollmentsEvents()`;
  - a tracked entity of type "Sample" updated 2024-01-01 with only that stub is returned by `PrunableEntities("Sample", facility, "2025-10-16")`; with an ACTIVE non-stub enrollment added, it is not;
  - a pending outbox entry naming the aged enrollment keeps it out of `AgedEnrollments`.

- [ ] **Step 2: Run** `go test ./internal/store -run TestRetention` — Expected: FAIL to build.

- [ ] **Step 3: Implement.**
  - Data migration appended to `dataMigrations` in `store.go`:

```sql
ALTER TABLE objects ADD COLUMN stub INTEGER NOT NULL DEFAULT 0;
DROP INDEX objects_state;
CREATE INDEX objects_state ON objects(uid, updated_at, slot, stub);
```

  with the comment "Stubs of pruned enrollments (tracker retention): hidden from reads, kept for the enrol-once check. The state index covers the stub flag, so a pull's stale check reads the index alone."
  - `sqlPutObject`: add `stub` to the column list, value `coalesce(json_extract(?18, '$.hubStub'), 0)`, and `stub=excluded.stub` on conflict.
  - `objectColumns`: `..., "rel_to", "deleted", "stub", "slim"`; scan `&o.Stub` after `&o.Deleted` in `ScanObject` and `QueryObjectPairs`.
  - `ObjectStates`: `SELECT uid, updated_at, slot, stub FROM objects INDEXED BY objects_state ...`.
  - The four queries, each with the outbox condition used by `OldProgramEvents`:

```go
const notPending = ` AND NOT EXISTS (SELECT 1 FROM outbox o JOIN outbox_objects oo ON oo.seq = o.seq WHERE o.state = 'pending' AND oo.uid = %s)`
```

  (use `OutboxPending` as a parameter, not a literal, as `OldProgramEvents` does). `AgedEnrollments`:

```sql
SELECT uid FROM objects e WHERE kind = 'enrollment' AND program = ? AND owner_org_unit = ? AND sync_state = ?
 AND stub = 0 AND status IN ('COMPLETED', 'CANCELLED') AND updated_at < ?
 AND NOT EXISTS (SELECT 1 FROM outbox o JOIN outbox_objects oo ON oo.seq = o.seq WHERE o.state = ? AND oo.uid = e.uid)
 AND NOT EXISTS (SELECT 1 FROM objects ev WHERE ev.enrollment_uid = e.uid AND ev.sync_state <> ?)
```

  (args: `program, owner, SyncSynced, cutoff, OutboxPending, SyncSynced`). `PrunableEntities` likewise with `kind = 'trackedEntity' AND tet = ?`, `NOT EXISTS (SELECT 1 FROM objects x WHERE x.te_uid = t.uid AND x.kind = 'enrollment' AND x.stub = 0)`, and `NOT EXISTS (SELECT 1 FROM objects x WHERE (x.te_uid = t.uid OR x.rel_from = t.uid OR x.rel_to = t.uid) AND x.sync_state <> ?)`.
  - Add `Stub bool` to `Object` with comment "an enrollment kept as a stub after pruning (tracker retention)".

- [ ] **Step 4: Run** `go test ./internal/store` — Expected: PASS. Then `go build ./...` and `go test ./internal/tracker` (the scans changed) — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "Store: stub column and the queries tracker retention prunes by"
```

---

### Task 3: Reads leave stubs out

**Files:**
- Modify: `internal/tracker/model.go` (Obj), `internal/tracker/index.go` (FromStore, CanRead), `internal/tracker/search.go` (row, SQL conditions), `internal/tracker/query.go` (visibleWith)
- Test: `internal/live/retention_test.go`

**Interfaces:**
- Consumes: `store.Object.Stub` (Task 2).
- Produces: `tracker.Obj.Stub bool`; `tracker.StubOf(enrollment M) M` (the stub payload: `enrollment, trackedEntity, program, orgUnit, status, enrolledAt, createdAt, updatedAt` and `"hubStub": true`).
- Fake helpers produced here, used by Tasks 4–7: `func (c *Central) SetStatus(enrollment, status string)`; `func (c *Central) Backdate(uid, updatedAt string)` (sets `updatedAt` on the object and, for a tracked entity, on its enrollments and their events; for an enrollment, on its events).

- [ ] **Step 1: Write the failing test** `TestStubHiddenFromReads` in `internal/live/retention_test.go`: seed with `e.Fake.Seed("RetTe000001", "RetEn000001", "Old", "NAT-9001", map[string]string{"RetEv000001": fc.Stage1})`, `e.Setup()`, Android nurse logs in, `e.Sync()`. Then rewrite the enrollment as a stub directly through the store in one transaction (`e.App.Hub.Store` `PutObject` with `tracker.StubOf(...)` payload, same slot), `e.App.Hub.Index.Forget()`, `e.Offline()`, and check:
  - `GET /api/tracker/enrollments?program=<fc.Program>&orgUnit=<facility>` does not list `RetEn000001`;
  - `GET /api/tracker/trackedEntities?program=<fc.Program>&orgUnits=<facility>&fields=trackedEntity` does not list `RetTe000001`;
  - `GET /api/tracker/trackedEntities/RetTe000001?fields=enrollments[enrollment]` answers 200 without `RetEn000001`;
  - `GET /api/tracker/enrollments/RetEn000001` answers 503 (needs connection), not 404;
  - the same list with `includeDeleted=true` does not list it either.

- [ ] **Step 2: Run** `go test ./internal/live -run TestStubHiddenFromReads` — Expected: FAIL (the stub is listed).

- [ ] **Step 3: Implement.**
  - `Obj.Stub bool // an enrollment pruned to a stub (tracker retention): never read`.
  - `FromStore` and `row` copy `Stub`.
  - `CanRead`: first condition gains `|| o.Stub`; `visibleWith` central branch gains `!o.Stub &&`.
  - `search.go`: where `e.deleted = 0` is added for the enrollment alias (TE pairs plan and enrollments search), also add `e.stub = 0`, unconditionally (not tied to `IncludeDeleted`).
  - `StubOf` in `model.go`:

```go
// StubOf is what the hub keeps of an enrollment pruned by retention: enough for the
// enrol-once check (E1016), nothing a person entered.
func StubOf(e M) M {
	out := M{"hubStub": true}
	for _, k := range []string{"enrollment", "trackedEntity", "program", "orgUnit", "status", "enrolledAt", "createdAt", "updatedAt"} {
		if v, ok := e[k]; ok {
			out[k] = v
		}
	}
	return out
}
```

  - Fake `SetStatus` and `Backdate` (lock `c.mu`; walk `c.order` as `SoftDelete` does).

- [ ] **Step 4: Run** `go test ./internal/live -run TestStubHiddenFromReads ./internal/tracker` — Expected: PASS; `TestQueryPlansUseIndexes`, `TestFirstPageReadsOnWithoutGaps`, `TestPagesReadOnFromLastPosition` still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tracker internal/fakecentral internal/live/retention_test.go
git commit -m "Retention: stubs are never read"
```

---

### Task 4: Offline E1015 and E1016

**Files:**
- Modify: `internal/tracker/import.go` (`validateEnrollment`)
- Modify: `internal/fakecentral/fakecentral.go` (`importTracker` enforces both, as central)
- Test: `internal/live/retention_test.go`

**Interfaces:**
- Consumes: `ProgramMeta.OnlyEnrollOnce` (Task 1), `Obj.Stub` (Task 3), fake `OnlyEnrollOnce` (Task 1).

- [ ] **Step 1: Write the failing test** `TestEnrollmentRulesOffline` (fake and live; live parts in Task 7). Fake part: `e.Fake.Seed("RetTe000002", "RetEn000002", "Active", "NAT-9002", nil)`, setup, nurse logs in, sync, offline. Post a new enrollment `RetEn000003` for `RetTe000002` in `fc.Program`, status ACTIVE: expect 409 with `E1015`. Then a second test case with `e.Fake.OnlyEnrollOnce = true` set before setup, the seeded enrollment completed (`e.Fake.SetStatus("RetEn000002","COMPLETED")` before setup): a new ACTIVE enrollment → 409 `E1016`; and the same with the existing enrollment turned into a stub (as in Task 3) → still `E1016`. Also: with `OnlyEnrollOnce` false and the old one COMPLETED, a new ACTIVE enrollment is accepted (200) and pushes (online `e.Sync()`; `e.CentralObject("enrollments","RetEn000003")` not nil).

- [ ] **Step 2: Run** `go test ./internal/live -run TestEnrollmentRulesOffline` — Expected: FAIL (accepted offline).

- [ ] **Step 3: Implement** in `validateEnrollment`, after the program write-access check and only for a new enrollment (`old == nil`):

```go
if old == nil {
	status := cmp.Or(Str(it.Data, "status"), "ACTIVE")
	for _, e := range im.enrollmentsIn(te, program) {
		if status == "ACTIVE" && !e.Stub && Str(withData(e).Data, "status") == "ACTIVE" {
			return nil, false, im.errf(it, "E1015", "TrackedEntity: `%s`, already has an active Enrollment in Program `%s`.", te, program)
		}
		if pm.OnlyEnrollOnce {
			return nil, false, im.errf(it, "E1016", "TrackedEntity: `%s`, already has an active enrollment in Program: `%s`, and this program only allows enrolling one time.", te, program)
		}
	}
}
```

  with `enrollmentsIn` returning the stored enrollments of `te` in `program` (stubs included, deleted excluded: use `im.Ix.EnrollmentsOf(te)` filtered by `Program` and `!Deleted`) plus those accepted earlier in this payload (`im.pending`). Status of a stored row comes from its slim data (`withData`). Fake `importTracker`, case `"enrollment"`, for a new enrollment: the same two checks against `c.objects` (`fail("E1015", ...)`, `fail("E1016", ...)` with the same messages).

- [ ] **Step 4: Run** `go test ./internal/live -run TestEnrollmentRulesOffline ./internal/tracker ./internal/e2e` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tracker/import.go internal/fakecentral/fakecentral.go internal/live/retention_test.go
git commit -m "Offline: E1015 (a second active enrollment) and E1016 (enrol once, stubs included)"
```

---

### Task 5: The pull's window, stubs on storing, no re-fetch

**Files:**
- Modify: `internal/compat/compat.go` (API interface), `internal/compat/v42.go` (`ActivePath`)
- Modify: `internal/sync/sync.go` (`pullFor`, `pullProgram`, `fetchStale`)
- Modify: `internal/core/objects.go` (`UpsertCentral`: a stub never merges)
- Modify: `internal/fakecentral/fakecentral.go` (`listTEs`: `enrollmentStatus`)
- Test: `internal/live/retention_test.go`

**Interfaces:**
- Consumes: `Retention.Cutoff`, `TypeCutoff` (Task 1); `ObjectState.Stub` (Task 2); `StubOf`, `Obj.Stub`, fake `SetStatus`, `Backdate` (Task 3).
- Produces: `compat.API.ActivePath(facility, program string, page, pageSize int) string` (the ID listing with `enrollmentStatus=ACTIVE`, `includeDeleted=true`).

- [ ] **Step 1: Write the failing tests** in `internal/live/retention_test.go`:
  - `TestRetentionPullWindow`: seed three people in `fc.Program`: `RetTe000010` ACTIVE backdated two years (`Backdate("RetTe000010", <2y ago>)`), `RetTe000011` COMPLETED backdated two years with an event, `RetTe000012` COMPLETED last month. Set `facility-hub-config/retention` to `{"programs": {"<fc.Program>": 12}}` in `e.Fake.DataStore` before `e.Setup()`. After setup, nurse login and `e.Sync()`: the hub holds `RetTe000010`'s enrollment in full, `RetTe000012`'s in full, and of `RetTe000011` the tracked entity (from the type pull) but its enrollment either not at all or as a stub (`Index.Get(...).Stub`), and not its event.
  - `TestNoRefetchAfterPrune`: same seeds, retention set; after the first sync, record `e.Fake.RequestCount("GET /api/tracker/trackedEntities")`, force a full pull (`e.App.Hub.Store.SetFullPullTime` to the zero time for every user and program, or set `FullPullInterval` to 1ns in `e.Config()` before start — use whichever `testkit` supports; read `testkit/env.go` `Config`), `e.Sync()`; the fetches (requests whose query has `trackedEntities=`) do not include `RetTe000011`. Use `e.Requests()` if it records central requests, else add a fake-side log of fetched UIDs.
  - `TestStubNeverMerges` (review focus 1): with `RetTe000011` stubbed, a nurse edits its first name offline and it pushes (`e.Sync()`): `e.CentralObject("trackedEntities","RetTe000011")` has the new name; the hub's row for `RetEn000011` is still a stub.
  - `TestReopenedEnrollmentReplacesStub` (review focus 2): after `RetTe000011`'s enrollment is a stub, `e.Fake.SetStatus("RetEn000011","ACTIVE")` and `Backdate` it to now; `e.Sync()`: the hub holds it in full (`!Stub`) with its event.
  - `TestNoRetentionUnchanged` (review focus 5): the same seeds without retention: everything held in full, no request carries `enrollmentStatus`.

- [ ] **Step 2: Run** `go test ./internal/live -run 'TestRetentionPullWindow|TestNoRefetchAfterPrune|TestStubNeverMerges|TestReopenedEnrollmentReplacesStub|TestNoRetentionUnchanged'` — Expected: FAIL (old enrollment held in full).

- [ ] **Step 3: Implement.**
  - `compat`: `ActivePath` = `pullQuery(facility, program, "", page, pageSize, true)` plus `enrollmentStatus=ACTIVE`.
  - fake `listTEs`: with `enrollmentStatus`, keep a person only if one of their enrollments in the program has that status.
  - `pullFor`: compute `cutoff, windowed := h.Retention().Cutoff(p, h.CentralNow())` for tracker programs, `TypeCutoff` for `tet:` scopes. When `after == ""` (first or full pull) and windowed:
    - tracker program: run the listing twice through `pullProgram`, once with the active listing and once with `after = cutoff`; `seen` collects both; `maxUpdated` is the max of the two;
    - `tet:` scope: `after = cutoff`.
    Give `pullProgram` a `list func(page int) string` parameter (the existing `api.ListPath(facility, program, after, page, listPageSize)` by default) so both listings share the code.
  - `pullProgram` stale check: for each flat item, skip it (not stale, not listed) when its enrollment (`it.Enrollment`, or `it.UID` for an enrollment) is held as a stub (`held[x].Stub`) with the listed enrollment's `updatedAt` equal to the stub's. The enrollment's listed `updatedAt` comes from the flat item of kind enrollment in the same tracked entity.
  - `fetchStale`: before `UpsertCentral`, for a windowed program, replace each fetched enrollment whose status is not ACTIVE and whose `updatedAt` < cutoff by `tracker.StubOf(enrollment)` (dropping its `events`). Pass the cutoff in (empty: no change).
  - `UpsertCentral`: when the old object is a stub or the incoming data has `hubStub`, set `o.Data = it.Data` (no merge), with the comment "a stub never merges: merging would keep the old fields or the stub mark".

- [ ] **Step 4: Run** the five tests, then `go test ./internal/live ./internal/e2e ./internal/sync` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/compat internal/sync internal/core/objects.go internal/fakecentral internal/live/retention_test.go
git commit -m "Retention: the full pull's window, stubs on storing, no re-fetch of stubs"
```

---

### Task 6: Pruning each cycle

**Files:**
- Modify: `internal/core/prune.go` (`PruneTracker`)
- Modify: `internal/store/objects.go` (`StubEnrollments`)
- Modify: `internal/sync/sync.go` (call after `PruneEvents`)
- Test: `internal/live/retention_test.go`

**Interfaces:**
- Consumes: `AgedEnrollments`, `StubEnrollmentsEvents`, `PrunableEntities`, `EntityLeftovers`, `DeleteObjects` (Task 2); `StubOf` (Task 3).
- Produces: `func (h *Hub) PruneTracker() (stubbed, deleted int, err error)`; `func (s *Store) StubEnrollments(uids []string, stub func(payload []byte) ([]byte, []byte, error)) (int, error)` (rewrites synced rows' payload and slim in transactions of 1 000; the callback returns the new payload and slim).

- [ ] **Step 1: Write the failing tests:**
  - `TestTrackerRetentionPrunes`: seeds as Task 5, setup without retention (all held in full), then set the retention state (`e.App.Hub.Store.SetState("retention", ...)`) and call `PruneTracker()`: `RetEn000011` becomes a stub, `RetEv000011` is gone, the others unchanged; a second call changes nothing.
  - `TestTypeRetentionRemovesEntities`: retention `{"trackedEntityTypes": {"<fc.TET>": 12}}`; `RetTe000011` (only a stub left after pruning, backdated) is removed with its stub; `RetTe000010` (active enrollment) stays.
  - `TestPruneKeepsReferenced` (review focus 4): an offline edit to `RetEv000011` pending: `PruneTracker()` leaves `RetEn000011` in full and the event in place.

- [ ] **Step 2: Run** them — Expected: FAIL to build (`PruneTracker` undefined).

- [ ] **Step 3: Implement** `PruneTracker` in `core/prune.go`, following `PruneEvents`:

```go
// PruneTracker applies retention to tracker programs and tracked entity types: closed
// enrollments whose last change fell out of their program's window become stubs (their
// events go); then tracked entities of types with a window that have nothing left in
// full go, with their stubs and relationships. Nothing pending is touched.
func (h *Hub) PruneTracker() (stubbed, deleted int, err error)
```

  Steps: for each catalog program that is not an event program with a cutoff: `AgedEnrollments` → `StubEnrollments` with a callback that decodes the payload, applies `tracker.StubOf`, and returns the new payload and its slim (`json.Marshal` of the stub; the stub is all scalars, so it is its own slim). Then `StubEnrollmentsEvents` → `DeleteObjects`. Then for each type in `Retention().TrackedEntityTypes` with a cutoff: `PrunableEntities` → `EntityLeftovers` → `DeleteObjects(leftovers)`, `DeleteObjects(entities)`. `h.Index.Forget()` when anything changed. Call it in `sync.go` after `PruneEvents`, logging `retention pruned tracker` with the counts.

- [ ] **Step 4: Run** the three tests and `go test ./internal/live ./internal/store ./internal/core` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/core/prune.go internal/store/objects.go internal/sync/sync.go internal/live/retention_test.go
git commit -m "Retention: tracker pruning each cycle (stubs, then entities of types with a window)"
```

---

### Task 7: Live checks against DHIS2

**Files:**
- Modify: `internal/testkit/env.go` (`CentralSetRetention`)
- Modify: `internal/live/retention_test.go` (live parts)
- Modify: `docs/TESTING.md`, `docs/FINDINGS.md`

**Interfaces:**
- Produces: `func (e *Env) CentralSetRetention(json string)` (fake: `DataStore`; live: PUT, or POST when missing, `/api/dataStore/facility-hub-config/retention` as the superuser; `t.Cleanup` deletes it live).

- [ ] **Step 1: Make `TestEnrollmentRulesOffline` live-capable**: in live mode, pick a tracked entity with an ACTIVE enrollment in `e.M.Programs[0]` (online list as the nurse, `enrollmentStatus=ACTIVE`, `pageSize=1`), go offline, post a new ACTIVE enrollment for it: hub 409 `E1015`; online, post the same through the hub (pass-through): central 409 `E1015`, and the hub's message equals central's. E1016 live only if the catalog has a program with `OnlyEnrollOnce` and a person enrolled in it at the facility; otherwise `t.Log` that it was skipped.
- [ ] **Step 2: Add `TestRetentionWindowLive`**: `e.CentralSetRetention` with `{"programs": {"<e.M.Programs[0].ID>": 1}}` before `e.Setup()`; after a nurse login and sync, the people the hub lists offline for that program equal what central lists online for the nurse with `enrollmentStatus=ACTIVE` plus `updatedAfter=<cutoff>`; the log has no `pull refused` (central accepted both listings); record the active listing's time (`t.Logf`).
- [ ] **Step 3: Run** `go test ./internal/live -run 'TestEnrollmentRulesOffline|TestRetentionWindowLive'` (fake) — PASS; then `go run ./cmd/hubtest run -profile synthetic -version 42`, `-version 43`, and `-profile sierra-leone -version 42` — Expected: passed; the oracle counts as before (synthetic 2.42: 977 MATCH). Adjust E1015/E1016 messages in `import.go` and the fake to central's exact text if they differ, and note it in FINDINGS.
- [ ] **Step 4: Docs**: TESTING.md row for the new tests; FINDINGS.md section "Retention for tracker programs" (what central does: the codes and messages, the listings accepted, the active listing's time on Sierra Leone; what the hub does).
- [ ] **Step 5: Commit**

```bash
git add internal/testkit/env.go internal/live/retention_test.go docs/TESTING.md docs/FINDINGS.md
git commit -m "Retention: live checks of the pull's listings and of E1015/E1016"
```

---

### Task 8: Performance at the busy HC IV size

**Files:**
- Modify: `internal/fakecentral/generate.go` (realistic update times; `updatedAfter` and `enrollmentStatus` in the generated listing)
- Create: `internal/perf/retention_test.go`
- Modify: `docs/PERFORMANCE.md`, the spec's status line

- [ ] **Step 1: Generator.** Completed enrollments get `updatedAt` = their enrollment date plus one year (their completion), their events the same; active ones keep `g.updatedAt`; a tracked entity's `updatedAt` is the newest of its enrollments'. The generated listing filters by `updatedAfter` (against the tracked entity's `updatedAt`) and by `enrollmentStatus` (the enrollment's status); keep the existing shortcut for `updatedAfter >= g.updatedAt`. Run `go test ./internal/fakecentral ./internal/perf` without `HUB_PERF_*` (the perf tests skip) — Expected: PASS.
- [ ] **Step 2: `TestTrackerRetention`** (skips without `HUB_PERF_SCALE`): generate the scale; hub A without retention: time the first fill, count objects, data dir size, the fake's tracked-entity requests; hub B with `{"defaultMonths": 12}` set before setup: the same numbers; on B, force a full pull and count the tracked entities fetched (must be 0 of the pruned); on A, set the retention state and time `PruneTracker()`. Report as `TestPull` does.
- [ ] **Step 3: Run** `HUB_PERF_SCALE="300000,500000,1200000" go test ./internal/perf -run TestTrackerRetention -timeout 2h -v` (or the `hc4` numbers in PERFORMANCE.md) — record the output.
- [ ] **Step 4: Docs**: replace PERFORMANCE.md's "Retention by programme (planned)" with the built design and the measured numbers (first fill with and without, pruning, second full pull); set the spec's status line to implemented with a pointer to this plan and FINDINGS.
- [ ] **Step 5: Commit**

```bash
git add internal/fakecentral/generate.go internal/perf/retention_test.go docs/PERFORMANCE.md docs/superpowers/specs/2026-10-16-tracker-retention-design.md
git commit -m "Retention: measured at the busy HC IV size"
```
