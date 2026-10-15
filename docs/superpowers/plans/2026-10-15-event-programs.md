# Event Programs and Retention Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Event programs work offline through the hub (lists, single events, Android's download, offline capture), bounded by a retention table by program; offline writes with a non-default attribute option combination are accepted when the user may write every category option in it.

**Architecture:** Event-program events are tracker objects without a person or enrollment, in the existing index, visibility bitmaps, searches, import and outbox. The catalog gains event programs and the option combinations of attribute category combinations; the access list gains the category options each user may write; the pull gains an event listing per event program; a retention table read from central's dataStore bounds the pull and drives pruning.

**Tech Stack:** Go (standard library), SQLite through ncruces/go-sqlite3.

**Spec:** `docs/superpowers/specs/2026-10-15-event-programs-design.md`

## Global Constraints

- Event programs are `programType = WITHOUT_REGISTRATION`, assigned to the facility (`organisationUnits.id:eq:<facility>`).
- Retention: dataStore `facility-hub-config/retention`, `{"defaultMonths": 0, "programs": {"<program>": 12}}`; 0 or no entry keeps everything; read with the hub token at the catalog refresh; a missing key keeps everything.
- Retention applies to event programs only in this work (by `occurredAt`); tracker programs unchanged.
- Offline attribute option rule: a non-default attribute option combination is accepted when the user may write (data write sharing) every category option in it; otherwise refused with E1099 `User: `<uid>`, has no write access to CategoryOption: `<option>`.`; the combination must belong to the program's (enrollment) category combination, otherwise E1055.
- Deletes stay refused offline.
- Keep the structure (AGENTS.md): an index or cache is added only when a measurement in Task 9 shows the need, with numbers before and after.
- Before each commit: `go vet ./...` and `go test ./...`; `go test -race` on `internal/e2e`, `internal/live`, `internal/tracker`, `internal/store`.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; no `Claude-Session` trailer; on `main`, linear.
- Go toolchain: `export PATH=$HOME/sdk/go/bin:$PATH`.
- Broker etiquette: at most 2 instances running, and check `GET /instances` first (other sessions use the broker; delete only `agent-hubtest-*` instances this session created).

## Review Focus

1. **An event deleted on central** while the hub holds it: it disappears from offline answers after the next incremental pull, not only at the weekly full pull (Task 5 test `TestEventProgramDeletedOnCentral`).
2. **An event with pending offline edits older than the retention window**: never pruned before it is pushed (Task 6 test `TestEventRetentionKeepsPending`).
3. **A user who may read an attribute option but not write it**: sees the events offline, cannot write them (E1099) (Task 7 test `TestAttributeOptionWriteOffline`).
4. **Event programs in the ownership check**: the hub must not ask central for program ownership of event programs (Task 5, `readers` excludes them; covered by the live suite's `TestOwnershipLive` passing).
5. **A user's program list mixing tracker and event programs** in one access group: the group pulls both, per user where an attribute combination decides (Task 5 test `TestEventProgramPerUserListing`).

---

### Task 1: Retention table

**Files:**
- Create: `internal/access/retention.go`, `internal/access/retention_test.go`
- Modify: `internal/core/refresh.go` (`RefreshCatalog`), create `internal/core/retention.go`
- Test: `internal/live/eventprograms_test.go` (new)

**Interfaces:**
- Produces: `type access.Retention struct { DefaultMonths int; Programs map[string]int }`, `func (r access.Retention) Months(program string) int`, `func (r access.Retention) Cutoff(program string, now time.Time) (string, bool)` (`"2006-01-02"`, ok false = keep everything); `func (h *core.Hub) Retention() access.Retention`.

- [ ] **Step 1: Failing unit test** (`internal/access/retention_test.go`):

```go
package access

import (
	"testing"
	"time"
)

func TestRetention(t *testing.T) {
	r := Retention{DefaultMonths: 24, Programs: map[string]int{"opd": 12, "keep": 0}}
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	if c, ok := r.Cutoff("opd", now); !ok || c != "2025-10-15" {
		t.Errorf("opd: %q %v", c, ok)
	}
	if c, ok := r.Cutoff("other", now); !ok || c != "2024-10-15" {
		t.Errorf("default: %q %v", c, ok)
	}
	if _, ok := r.Cutoff("keep", now); ok {
		t.Error("0 for a program keeps everything")
	}
	if _, ok := (Retention{}).Cutoff("any", now); ok {
		t.Error("no table keeps everything")
	}
}
```

Run: `go test ./internal/access -run TestRetention` — Expected: FAIL (undefined).

- [ ] **Step 2: Implement** `internal/access/retention.go`:

```go
package access

import "time"

// Retention is how long the hub keeps each program's data, in months, from central's
// dataStore (facility-hub-config/retention). 0 keeps everything; a program without an
// entry uses the default. Event programs use it now (by occurredAt), tracker programs
// will (docs/superpowers/specs/2026-10-15-event-programs-design.md).
type Retention struct {
	DefaultMonths int            `json:"defaultMonths"`
	Programs      map[string]int `json:"programs"`
}

func (r Retention) Months(program string) int {
	if n, ok := r.Programs[program]; ok {
		return n
	}
	return r.DefaultMonths
}

// Cutoff is the oldest date kept for the program at now ("2006-01-02"); ok is false
// when the program keeps everything.
func (r Retention) Cutoff(program string, now time.Time) (string, bool) {
	m := r.Months(program)
	if m <= 0 {
		return "", false
	}
	return now.AddDate(0, -m, 0).Format(time.DateOnly), true
}
```

Run: `go test ./internal/access -run TestRetention` — Expected: PASS.

- [ ] **Step 3: Failing live test** (`internal/live/eventprograms_test.go`):

```go
package live

import (
	"context"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The retention table is read from central's dataStore at the catalog refresh.
func TestRetentionTableRead(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"retention": json.RawMessage(`{"defaultMonths": 24, "programs": {"X": 6}}`)}
	e.Setup()
	if err := e.App.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := e.App.Hub.Retention()
	if r.Months("X") != 6 || r.Months("Y") != 24 {
		t.Fatalf("retention: %+v", r)
	}
}
```

(imports: `"context"`, `"encoding/json"`, `"testing"`, `tk`.) Run: `go test ./internal/live -run TestRetentionTableRead` — Expected: FAIL (undefined `Retention`).

- [ ] **Step 4: Implement** `internal/core/retention.go`:

```go
package core

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/central"
)

const retentionKey = "retention"

// Retention is the retention table read from central at the last catalog refresh.
func (h *Hub) Retention() access.Retention {
	var r access.Retention
	if b, err := h.Store.State(retentionKey); err == nil && b != nil {
		json.Unmarshal(b, &r)
	}
	return r
}

// refreshRetention reads facility-hub-config/retention; a missing key keeps everything.
func (h *Hub) refreshRetention(ctx context.Context) error {
	var r access.Retention
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/retention", nil, h.HubAuth(), &r)
	if central.Status(err) == http.StatusNotFound {
		r, err = access.Retention{}, nil
	}
	if err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return h.Store.SetState(retentionKey, b)
}
```

In `RefreshCatalog`, next to the `refreshAggRetention` call:

```go
	if rerr := h.refreshRetention(ctx); rerr != nil && central.IsNetwork(rerr) {
		return rerr
	}
```

Run: `go test ./internal/live -run TestRetentionTableRead && go test ./internal/access` — Expected: PASS.

- [ ] **Step 5: Commit** — `git add internal/access internal/core internal/live/eventprograms_test.go && git commit -m "Retention table by program from central's dataStore (facility-hub-config/retention)"` (with the trailer).

---

### Task 2: Fake central: event programs, events, category option access

**Files:**
- Create: `internal/fakecentral/eventprograms.go`, `internal/fakecentral/eventprograms_test.go`
- Modify: `internal/fakecentral/fakecentral.go` (`programs`, dispatch, `importTracker` event branch)

**Interfaces:**
- Produces: constants `EventProgram = "FakePrgEvt1"`, `EventStage = "FakeStEvt01"`, `EventDonorProgram = "FakePrgEvD1"`, `EventDonorStage = "FakeStEvD01"` (category combination `AggDonorCC`); `func (c *Central) SeedProgramEvent(uid, program, stage, occurredAt, aoc string)`; `func (c *Central) DeleteProgramEvent(uid string)`. Event program write access: the users in `aggUsers` (hub_admin, nurse_a, clerk_c); read: everyone with `ProgramRead`. Donor events: readable and writable only with the event's option in the user's `aggUsers` options (nurse_a: A and B; clerk_c: B).

- [ ] **Step 1: Failing test** (`internal/fakecentral/eventprograms_test.go`): with `httptest.NewServer(New())` and Basic auth as in `TestFakeAggregate`:
  1. `GET /api/programs?filter=programType:eq:WITHOUT_REGISTRATION&filter=organisationUnits.id:eq:<Facility>&fields=...` as nurse_a → exactly `EventProgram` and `EventDonorProgram`; `programType:eq:WITH_REGISTRATION` → the TB program only (not the event programs).
  2. `GET /api/programs?filter=id:in:[<Program>,<EventProgram>]&fields=id,access[data[read,write]]` → both, with access.
  3. `SeedProgramEvent("FakeEv00001", EventProgram, EventStage, "2026-10-01", DefaultAOC)`, `SeedProgramEvent("FakeEvD0001", EventDonorProgram, EventDonorStage, "2026-10-01", AggDonorAOCA)`; `GET /api/tracker/events?program=<EventDonorProgram>&orgUnit=<Facility>` as clerk_c → 0 events, as nurse_a → 1.
  4. `POST /api/tracker?async=false` as clerk_c with a donor event under `AggDonorAOCA` → 409 with an error report `E1099`; under `AggDonorAOCB` → 200.
  5. `DeleteProgramEvent("FakeEv00001")`; `GET ...events?program=<EventProgram>&orgUnit=<Facility>&includeDeleted=true` → the event with `"deleted": true`; without `includeDeleted` → none.
  6. `GET /api/categoryCombos?filter=id:in:[<AggDonorCC>]&fields=id,categories[categoryOptions[id,access[data[write]]]]` as clerk_c → option A write false, B write true.

Run: `go test ./internal/fakecentral -run TestFakeEventPrograms` — Expected: FAIL (undefined constants).

- [ ] **Step 2: Implement** `internal/fakecentral/eventprograms.go`:

```go
package fakecentral

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// Event programs (WITHOUT_REGISTRATION) at the facility.
const (
	EventProgram      = "FakePrgEvt1" // default category combination
	EventStage        = "FakeStEvt01"
	EventDonorProgram = "FakePrgEvD1" // category combination AggDonorCC
	EventDonorStage   = "FakeStEvD01"
)

func eventProgramMeta(u *User) []any {
	_, writes := aggUsers[u.Username]
	stage := func(id string) []any {
		return []any{map[string]any{"id": id, "sortOrder": 1, "repeatable": true, "access": dataPerm(u.ProgramRead, writes)}}
	}
	return []any{
		map[string]any{"id": EventProgram, "name": "Fake event program", "programType": "WITHOUT_REGISTRATION",
			"categoryCombo": map[string]any{"id": AggCC, "isDefault": true}, "access": dataPerm(u.ProgramRead, writes),
			"programStages": stage(EventStage)},
		map[string]any{"id": EventDonorProgram, "name": "Fake donor events", "programType": "WITHOUT_REGISTRATION",
			"categoryCombo": map[string]any{"id": AggDonorCC, "isDefault": false}, "access": dataPerm(u.ProgramRead, writes),
			"programStages": stage(EventDonorStage)},
	}
}

func isEventProgram(p string) bool { return p == EventProgram || p == EventDonorProgram }

// donorOption is the category option of a donor attribute option combination.
func donorOption(aoc string) string {
	return map[string]string{AggDonorAOCA: AggDonorOptA, AggDonorAOCB: AggDonorOptB}[aoc]
}

// mayUseEvent: the event's attribute option (donor program) is one of the user's.
func mayUseEvent(u *User, ev tracker.M) bool {
	if tracker.Str(ev, "program") != EventDonorProgram {
		return true
	}
	return slices.Contains(aggUsers[u.Username], donorOption(tracker.Str(ev, "attributeOptionCombo")))
}

// SeedProgramEvent adds an event of an event program at the facility.
func (c *Central) SeedProgramEvent(uid, program, stage, occurredAt, aoc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.now()
	c.put("event", uid, tracker.M{"event": uid, "program": program, "programStage": stage, "orgUnit": Facility,
		"status": "ACTIVE", "occurredAt": occurredAt, "attributeOptionCombo": aoc, "createdAt": ts, "updatedAt": ts,
		"dataValues": []any{}})
}

// DeleteProgramEvent marks an event deleted, as central's soft delete does.
func (c *Central) DeleteProgramEvent(uid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev := c.objects[uid]; ev != nil {
		ev["deleted"], ev["updatedAt"] = true, c.now()
	}
}

// listProgramEvents answers /api/tracker/events for event programs.
func (c *Central) listProgramEvents(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	program := q.Get("program")
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	page, size = max(page, 1), max(size, 50)
	c.mu.Lock()
	var out []any
	for _, uid := range c.order {
		ev := c.objects[uid]
		if c.kinds[uid] != "event" || tracker.Str(ev, "program") != program || tracker.Str(ev, "orgUnit") != Facility {
			continue
		}
		if ev["deleted"] == true && q.Get("includeDeleted") != "true" {
			continue
		}
		if a := q.Get("updatedAfter"); a != "" && tracker.Str(ev, "updatedAt") < a {
			continue
		}
		if a := q.Get("occurredAfter"); a != "" && tracker.Str(ev, "occurredAt") < a {
			continue
		}
		if !u.ProgramRead || !mayUseEvent(u, ev) {
			continue
		}
		out = append(out, ev)
	}
	c.mu.Unlock()
	start := (page - 1) * size
	pageItems := []any{}
	if start < len(out) {
		pageItems = out[start:min(start+size, len(out))]
	}
	pager := map[string]any{"page": page, "pageSize": size}
	if start+size < len(out) {
		pager["nextPage"] = "next"
	}
	writeJSON(w, 200, map[string]any{"pager": pager, "events": pageItems})
}

// categoryCombos answers the access refresh's question: which options of these
// combinations the user may write.
func (c *Central) categoryCombos(w http.ResponseWriter, r *http.Request, u *User) {
	if !strings.Contains(r.URL.Query().Get("filter"), AggDonorCC) {
		writeJSON(w, 200, map[string]any{"categoryCombos": []any{}})
		return
	}
	opt := func(id string) map[string]any {
		return map[string]any{"id": id, "access": map[string]any{"data": map[string]any{
			"read": true, "write": slices.Contains(aggUsers[u.Username], id)}}}
	}
	writeJSON(w, 200, map[string]any{"categoryCombos": []any{map[string]any{"id": AggDonorCC,
		"categories": []any{map[string]any{"categoryOptions": []any{opt(AggDonorOptA), opt(AggDonorOptB)}}}}}})
}
```

In `fakecentral.go`:
- `programs`: build `progs` from the tracker program (plus `c.gen.programMeta()`) **and** `eventProgramMeta(u)`, mark the tracker ones `"programType": "WITH_REGISTRATION"`; keep only those matching a `filter=programType:eq:X` if given, and only those named in a `filter=id:in:[...]` if given (parse the bracket list).
- dispatch, before `case p == "/api/tracker/trackedEntities":`

```go
	case p == "/api/tracker/events" && isEventProgram(r.URL.Query().Get("program")):
		c.listProgramEvents(w, r, u)
	case p == "/api/categoryCombos":
		c.categoryCombos(w, r, u)
```

- `importTracker`, `case "event":` first lines:

```go
			if prog := tracker.Str(it.Data, "program"); isEventProgram(prog) {
				if _, writes := aggUsers[u.Username]; !writes {
					fail("E1091", "User has no data write access to Program")
				} else if !mayUseEvent(u, it.Data) {
					fail("E1099", "User: `"+u.UID+"`, has no write access to CategoryOption: `"+donorOption(tracker.Str(it.Data, "attributeOptionCombo"))+"`.")
				}
				break
			}
```

(the import stores accepted events with `c.put`, as for tracker events; check that the store step does not require an enrollment for them, and skip the enrollment lookup when `it.Enrollment == ""`.)

Run: `go test ./internal/fakecentral -count=1 -race && go test ./internal/e2e ./internal/live -count=1` — Expected: PASS (the hub ignores event programs until Task 3).

- [ ] **Step 3: Commit** — "fakecentral: event programs, their events, category option write access".

---

### Task 3: Catalog with event programs and attribute option combinations

**Files:**
- Modify: `internal/access/access.go` (`ProgramMeta`, `Catalog`, `Merge`), `internal/core/refresh.go` (`FetchCatalog`), `internal/compat/v42.go`, `internal/compat/v43.go` (`ProgramFields`)
- Test: `internal/live/eventprograms_test.go`

**Interfaces:**
- Produces: `ProgramMeta.Event bool`, `ProgramMeta.CategoryCombo string`, `ProgramMeta.EnrollmentCategoryCombo string`; `Catalog.AOCs map[string][]string` (attribute option combination → its category options), `Catalog.ComboAOCs map[string][]string` (category combination → its option combinations); `func (c *Catalog) AttributeCombos() []string` (non-default combinations of catalog programs, events and enrollments, sorted).

- [ ] **Step 1: Failing test** (append):

```go
// The catalog holds the facility's event programs and the option combinations of
// attribute category combinations.
func TestEventProgramsInCatalog(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	cat := e.App.Hub.Catalog()
	ev, donor := cat.Programs[fc.EventProgram], cat.Programs[fc.EventDonorProgram]
	if ev == nil || !ev.Event || ev.TET != "" || len(ev.Stages) != 1 || ev.AttributeCombo {
		t.Fatalf("event program: %+v", ev)
	}
	if donor == nil || !donor.AttributeCombo || donor.CategoryCombo != fc.AggDonorCC {
		t.Fatalf("donor event program: %+v", donor)
	}
	if opts := cat.AOCs[fc.AggDonorAOCA]; len(opts) != 1 || opts[0] != fc.AggDonorOptA {
		t.Fatalf("option combinations: %+v", cat.AOCs)
	}
	if tb := cat.Programs[fc.Program]; tb == nil || tb.Event {
		t.Fatalf("tracker program: %+v", tb)
	}
}
```

(import `fc "github.com/dhis2/facility-hub/internal/fakecentral"`.) Expected: FAIL (no event programs in the catalog).

- [ ] **Step 2: Implement.**
  - `ProgramMeta`: add

```go
	// Event: an event program (WITHOUT_REGISTRATION): events without a person or
	// enrollment, owned by their org unit.
	Event bool `json:"event,omitempty"`
	// CategoryCombo and EnrollmentCategoryCombo: the program's (2.43: its
	// enrollments') category combination, when not the default.
	CategoryCombo           string `json:"categoryCombo,omitempty"`
	EnrollmentCategoryCombo string `json:"enrollmentCategoryCombo,omitempty"`
```

  - `Catalog`: add `AOCs map[string][]string \`json:"aocs,omitempty"\`` and `ComboAOCs map[string][]string \`json:"comboAocs,omitempty"\``; in `Merge`, copy entries `c` lacks; and:

```go
// AttributeCombos lists the non-default category combinations of the catalog's
// programs (events) and enrollments (2.43), sorted.
func (c *Catalog) AttributeCombos() []string {
	var out []string
	for _, p := range c.Programs {
		for _, cc := range []string{p.CategoryCombo, p.EnrollmentCategoryCombo} {
			if cc != "" && !slices.Contains(out, cc) {
				out = append(out, cc)
			}
		}
	}
	slices.Sort(out)
	return out
}
```

  - compat `ProgramFields`: `categoryCombo[isDefault]` → `categoryCombo[id,isDefault]` (v42) and v43's `enrollmentCategoryCombo[isDefault]` → `enrollmentCategoryCombo[id,isDefault]`.
  - `FetchCatalog`: add `ID string \`json:"id"\`` to the program struct's `CategoryCombo` and `EnrollmentCategoryCombo`; set `pm.CategoryCombo`/`pm.EnrollmentCategoryCombo` to the ID when not default. After the tracker programs, fetch event programs:

```go
	var evResp struct {
		Programs []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			CategoryCombo struct {
				ID        string `json:"id"`
				IsDefault *bool  `json:"isDefault"`
			} `json:"categoryCombo"`
			ProgramStages []struct {
				ID string `json:"id"`
			} `json:"programStages"`
		} `json:"programs"`
	}
	if err := h.Central.JSON(ctx, "GET", central.Q("/api/programs", "filter", "programType:eq:WITHOUT_REGISTRATION",
		"filter", "organisationUnits.id:eq:"+facility, "fields", "id,name,categoryCombo[id,isDefault],programStages[id]",
		"paging", "false"), nil, auth, &evResp); err != nil {
		return nil, err
	}
	for _, p := range evResp.Programs {
		pm := &access.ProgramMeta{ID: p.ID, Name: p.Name, Event: true, Attributes: []access.AttrMeta{},
			AttributeCombo: p.CategoryCombo.IsDefault != nil && !*p.CategoryCombo.IsDefault}
		if pm.AttributeCombo {
			pm.CategoryCombo = p.CategoryCombo.ID
		}
		for _, s := range p.ProgramStages {
			pm.Stages = append(pm.Stages, s.ID)
		}
		c.Programs[p.ID] = pm
	}
	if combos := c.AttributeCombos(); len(combos) > 0 {
		var cocs struct {
			COCs []struct {
				ID            string `json:"id"`
				CategoryCombo idRef  `json:"categoryCombo"`
				Options       []idRef `json:"categoryOptions"`
			} `json:"categoryOptionCombos"`
		}
		if err := h.Central.JSON(ctx, "GET", central.Q("/api/categoryOptionCombos", "filter",
			"categoryCombo.id:in:["+strings.Join(combos, ",")+"]", "fields", "id,categoryCombo[id],categoryOptions[id]",
			"paging", "false"), nil, auth, &cocs); err != nil {
			return nil, err
		}
		c.AOCs, c.ComboAOCs = map[string][]string{}, map[string][]string{}
		for _, coc := range cocs.COCs {
			for _, o := range coc.Options {
				c.AOCs[coc.ID] = append(c.AOCs[coc.ID], o.ID)
			}
			c.ComboAOCs[coc.CategoryCombo.ID] = append(c.ComboAOCs[coc.CategoryCombo.ID], coc.ID)
		}
	}
```

  (`idRef` is the `{ID string}` type `refresh.go` already uses; if its JSON tag differs, use it as declared there.)
  - `TETAttributes`: skip `""` (event programs have no type).
  - Everything that loops catalog programs assuming a type: `FetchAccess` reads `trackedEntityType[...]` for every program: skip setting `l.TETs[""]` when the ID is empty.

Run: `go test ./internal/live -run 'TestEventProgramsInCatalog' -count=1 && go test ./... -count=1` — Expected: PASS. (`pullGroup` treats a program without a type as readable when its program is; pulls of event programs still go through `pullProgram` until Task 5 and fail harmlessly against the fake's tracked entity listing: if a test breaks on that, add the `pm.Event` skip in `pullFor` now with a TODO-free comment and move on.)

- [ ] **Step 3: Commit** — "Catalog: the facility's event programs; option combinations of attribute category combinations".

---

### Task 4: Category options each user may write

**Files:**
- Modify: `internal/access/access.go` (`List`), `internal/core/refresh.go` (`FetchAccess`)
- Test: `internal/live/eventprograms_test.go`

**Interfaces:**
- Produces: `List.CategoryOptions map[string]bool` (options of the catalog's attribute category combinations the user may write); `func (l *List) MayWriteOptions(opts []string) (bool, string)` (ok, first missing option).

- [ ] **Step 1: Failing test:**

```go
// The access refresh reads which category options of attribute category combinations
// each user may write.
func TestCategoryOptionWriteAccess(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	for _, c := range []struct{ user string; a, b bool }{{"nurse_a", true, true}, {"clerk_c", false, true}} {
		cl := e.Web(roleOf(e, c.user))
		cl.Login()
		u, _ := e.App.Hub.Store.UserByName(c.user)
		acc := e.App.Hub.Access(u.ID)
		if acc.CategoryOptions[fc.AggDonorOptA] != c.a || acc.CategoryOptions[fc.AggDonorOptB] != c.b {
			t.Errorf("%s: %+v", c.user, acc.CategoryOptions)
		}
	}
}

func roleOf(e *tk.Env, username string) string {
	for role, u := range e.M.Users {
		if u.Username == username {
			return role
		}
	}
	e.T.Fatalf("no role for %s", username)
	return ""
}
```

Expected: FAIL (no `CategoryOptions`).

- [ ] **Step 2: Implement.** In `List`:

```go
	// CategoryOptions: the options of the catalog's attribute category combinations
	// (events, and 2.43 enrollments) the user may write data for. Offline writes with
	// an attribute option combination need each of its options (E1099).
	CategoryOptions map[string]bool `json:"categoryOptions,omitempty"`
```

and

```go
// MayWriteOptions reports whether the user may write every option; missing is the
// first one they may not.
func (l *List) MayWriteOptions(opts []string) (ok bool, missing string) {
	for _, o := range opts {
		if l == nil || !l.CategoryOptions[o] {
			return false, o
		}
	}
	return true, ""
}
```

In `FetchAccess`, after the relationship types:

```go
	if combos := h.Catalog().AttributeCombos(); len(combos) > 0 {
		var cc struct {
			Combos []struct {
				Categories []struct {
					Options []struct {
						ID string `json:"id"`
						dataAccess
					} `json:"categoryOptions"`
				} `json:"categories"`
			} `json:"categoryCombos"`
		}
		if err := h.Central.JSON(ctx, "GET", central.Q("/api/categoryCombos", "filter", "id:in:["+strings.Join(combos, ",")+"]",
			"fields", "id,categories[categoryOptions[id,access[data[read,write]]]]", "paging", "false"), nil, auth, &cc); err != nil {
			if central.IsNetwork(err) {
				return nil, err
			}
			l.Ungroupable = true // unknown: no offline writes with an option combination, pulled alone
		} else {
			l.CategoryOptions = map[string]bool{}
			for _, c := range cc.Combos {
				for _, cat := range c.Categories {
					for _, o := range cat.Options {
						if o.Access.Data.Write {
							l.CategoryOptions[o.ID] = true
						}
					}
				}
			}
		}
	}
```

Update `GroupKey`'s comment: category option *write* access is now in the list; *read* sharing (what central lists) still is not, so programs with an attribute combination stay listed per user.

Run: `go test ./internal/live -run TestCategoryOptionWriteAccess -count=1 && go test ./internal/access ./internal/e2e -count=1` — Expected: PASS.

- [ ] **Step 3: Live probe of the field.** On a live 2.42 instance with the synthetic profile (Task 8 provisions it; if none is running yet, defer this step to Task 8 Step 4), as a matrix user with the donor option read-only: `GET /api/categoryCombos?filter=id:in:[SynCcDonor1]&fields=id,categories[categoryOptions[id,access[data[read,write]]]]` must show `write: false` for that option and `true` for a user with write. If `access` is not computed per user there, use `/api/categoryOptions?filter=categories.categoryCombos.id:in:[...]&fields=id,access[data[write]]` instead and record the ruling.

- [ ] **Step 4: Commit** — "Access refresh: category options each user may write".

---

### Task 5: Pulling event programs

**Files:**
- Modify: `internal/compat/compat.go` (interface), `internal/compat/v42.go` (`EventsPath`; v43 embeds v42)
- Create: `internal/sync/events.go`
- Modify: `internal/sync/sync.go` (`pullFor`: branch; `readers`: exclude event programs)
- Modify: `internal/core/objects.go` (`HideSlots`)
- Test: `internal/live/eventprograms_test.go`

**Interfaces:**
- Produces: `API.EventsPath(facility, program, updatedAfter, occurredAfter string, page, pageSize int) string`; `func (w *Worker) pullEvents(ctx, api, g []*puller, facility, program, after string, seen map[uint32]bool) (string, int, error)`; `func (h *Hub) HideSlots(userID int64, slots []uint32) error`.

- [ ] **Step 1: Failing tests** (append; add `"net/url"`, `"strings"` imports):

```go
func eventPath(e *tk.Env, program string, extra url.Values) string {
	q := url.Values{"program": {program}, "orgUnit": {e.M.Facility.ID}, "fields": {"event,programStage,occurredAt,attributeOptionCombo,status"}}
	for k, v := range extra {
		q[k] = v
	}
	return "/api/tracker/events?" + q.Encode()
}

func eventUIDs(body map[string]any) []string {
	var out []string
	for _, x := range body["events"].([]any) {
		out = append(out, x.(map[string]any)["event"].(string))
	}
	return out
}

// Event programs are pulled and answered offline: working lists, single events and
// Android's download.
func TestEventProgramOffline(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.SeedProgramEvent("FakeEv00001", fc.EventProgram, fc.EventStage, "2026-10-01", fc.DefaultAOC)
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Offline()
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, nil), nil, 200)); len(got) != 1 || got[0] != "FakeEv00001" {
		t.Fatalf("working list offline: %v", got)
	}
	android := url.Values{"orgUnitMode": {"DESCENDANTS"}, "includeDeleted": {"true"}, "paging": {"true"}, "page": {"1"}, "pageSize": {"50"}}
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, android), nil, 200)); len(got) != 1 {
		t.Fatalf("Android's download offline: %v", got)
	}
	nurse.JSON("GET", "/api/tracker/events/FakeEv00001?fields=event", nil, 200)
}

// Review focus 1: an event deleted on central disappears offline after the next
// incremental pull.
func TestEventProgramDeletedOnCentral(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.SeedProgramEvent("FakeEv00002", fc.EventProgram, fc.EventStage, "2026-10-01", fc.DefaultAOC)
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Fake.DeleteProgramEvent("FakeEv00002")
	e.Sync()
	e.Offline()
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, nil), nil, 200)); len(got) != 0 {
		t.Fatalf("deleted on central, still offline: %v", got)
	}
}

// Review focus 5: where an attribute combination decides, each user sees only events
// under their options (listed per user).
func TestEventProgramPerUserListing(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.SeedProgramEvent("FakeEvD0001", fc.EventDonorProgram, fc.EventDonorStage, "2026-10-01", fc.AggDonorAOCA)
	e.Fake.SeedProgramEvent("FakeEvD0002", fc.EventDonorProgram, fc.EventDonorStage, "2026-10-01", fc.AggDonorAOCB)
	e.Setup()
	nurse, clerk := e.Android(tk.RoleNurse), e.Android(tk.RoleClerk)
	nurse.Do("GET", "/api/me?fields=id", nil)
	clerk.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Offline()
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventDonorProgram, nil), nil, 200)); len(got) != 2 {
		t.Errorf("nurse (options A and B): %v", got)
	}
	if got := eventUIDs(clerk.JSON("GET", eventPath(e, fc.EventDonorProgram, nil), nil, 200)); len(got) != 1 || got[0] != "FakeEvD0002" {
		t.Errorf("clerk (option B only): %v", got)
	}
}
```

Expected: FAIL (503 "not cached" or empty lists).

- [ ] **Step 2: Implement.** compat interface:

```go
	// EventsPath lists an event program's events at the facility, whole, with deleted
	// ones; occurredAfter bounds them by retention ("" for all).
	EventsPath(facility, program, updatedAfter, occurredAfter string, page, pageSize int) string
```

v42:

```go
func (v42) EventsPath(facility, program, updatedAfter, occurredAfter string, page, pageSize int) string {
	q := url.Values{"program": {program}, "orgUnit": {facility}, "orgUnitMode": {"SELECTED"}, "includeDeleted": {"true"},
		"fields": {"*"}, "page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}, "order": {"createdAt:asc"}}
	if updatedAfter != "" {
		q.Set("updatedAfter", updatedAfter)
	}
	if occurredAfter != "" {
		q.Set("occurredAfter", occurredAfter)
	}
	return "/api/tracker/events?" + q.Encode()
}
```

`internal/core/objects.go`:

```go
// HideSlots removes objects from a user's visibility (an event deleted on central).
func (h *Hub) HideSlots(userID int64, slots []uint32) error {
	changes := map[uint32]bool{}
	for _, s := range slots {
		if s != 0 {
			changes[s] = false
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return h.setVisibility(userID, changes)
}
```

`internal/sync/events.go`:

```go
package sync

import (
	"context"

	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// pullEvents pulls an event program for users with the same access (g), listing as the
// first of them: whole events, a page at a time (events are small: no ID listing
// first, as tracker needs). An event central lists as deleted is hidden from the
// group at once, not at the weekly full pull. With retention set for the program,
// only events since its cutoff are listed. It returns the newest updatedAt listed.
func (w *Worker) pullEvents(ctx context.Context, api compat.API, g []*puller, facility, program, after string,
	seen map[uint32]bool) (string, int, error) {
	h := w.H
	rep := g[0]
	cutoff, _ := h.Retention().Cutoff(program, h.CentralNow())
	maxUpdated, total := "", 0
	for page := 1; ; page++ {
		var res struct {
			Events []tracker.M `json:"events"`
		}
		if err := h.Central.JSON(ctx, "GET", api.EventsPath(facility, program, after, cutoff, page, listPageSize), nil,
			rep.auth, &res); err != nil {
			return "", total, err
		}
		var keep []any
		var gone []string
		for _, ev := range res.Events {
			maxUpdated = max(maxUpdated, tracker.Str(ev, "updatedAt"))
			if ev["deleted"] == true {
				gone = append(gone, tracker.Str(ev, "event"))
				continue
			}
			keep = append(keep, ev)
		}
		if len(keep) > 0 {
			uids, err := h.UpsertCentral(rep.u.ID, tracker.M{"events": keep}, true)
			if err != nil {
				return "", total, err
			}
			st, err := h.Store.ObjectStates(uids)
			if err != nil {
				return "", total, err
			}
			for _, o := range st {
				seen[uint32(o.Slot)] = true
			}
			total += len(uids)
		}
		if len(gone) > 0 {
			st, err := h.Store.ObjectStates(gone)
			if err != nil {
				return "", total, err
			}
			var slots []uint32
			for _, o := range st {
				slots = append(slots, uint32(o.Slot))
			}
			for _, m := range g {
				if err := h.HideSlots(m.u.ID, slots); err != nil {
					return "", total, err
				}
			}
		}
		if len(res.Events) < listPageSize {
			break
		}
	}
	return maxUpdated, total, nil
}
```

`pullFor`: replace the `pullProgram` call with

```go
	var maxUpdated string
	var n int
	var perr error
	pm := h.Catalog().Programs[p]
	if pm != nil && pm.Event {
		maxUpdated, n, perr = w.pullEvents(ctx, h.API(), g, h.Facility(), p, after, seen)
	} else {
		maxUpdated, n, perr = w.pullProgram(ctx, h.API(), rep.auth, rep.u.ID, h.Facility(), p, after, seen)
	}
```

and the `readers` line: `if _, ok := readers[p]; !ok && !strings.HasPrefix(p, "tet:") && (pm == nil || !pm.Event) {` (event programs have no program ownership to check).

Check `UpsertCentral` for events without an enrollment with `forceFacility`: the `case store.KindEvent:` branch leaves `o.TE`, `o.Enrollment` empty and stores the event owned by the facility; nothing else needed. Check `tracker.Flatten` accepts a top-level `"events"` list (it does for the import path).

Run: `go test ./internal/live -run 'TestEventProgram' -count=1 -v` — Expected: PASS. Then `go test ./... -count=1`.

- [ ] **Step 3: Commit** — "Pull event programs: whole events per access group; deleted on central hidden at once".

---

### Task 6: Pruning by retention

**Files:**
- Modify: `internal/store/objects.go` (`OldProgramEvents`, `DeleteObjects`), `internal/tracker/index.go` (`Forget`)
- Create: `internal/core/prune.go`
- Modify: `internal/sync/sync.go` (`cycle`: call after the pulls)
- Test: `internal/live/eventprograms_test.go`

**Interfaces:**
- Produces: `func (s *Store) OldProgramEvents(program, cutoff string) ([]string, error)`; `func (s *Store) DeleteObjects(uids []string) error`; `func (ix *Index) Forget()`; `func (h *Hub) PruneEvents() (int, error)`.

- [ ] **Step 1: Failing tests:**

```go
// Events older than their program's retention are not pulled, and are pruned once they
// fall out of it.
func TestEventRetention(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	old := e.App.Hub.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	recent := e.App.Hub.CentralNow().AddDate(0, -1, 0).Format(time.DateOnly)
	e.Fake.SeedProgramEvent("FakeEvOld01", fc.EventProgram, fc.EventStage, old, fc.DefaultAOC)
	e.Fake.SeedProgramEvent("FakeEvNew01", fc.EventProgram, fc.EventStage, recent, fc.DefaultAOC)
	e.Setup() // keeps everything: no retention yet
	if e.App.Hub.Index.Get("FakeEvOld01") == nil {
		t.Fatal("without retention the old event is held")
	}
	e.Fake.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"retention": json.RawMessage(`{"programs": {"` + fc.EventProgram + `": 12}}`)}
	e.App.Hub.RefreshCatalog(context.Background())
	e.Sync()
	if e.App.Hub.Index.Get("FakeEvOld01") != nil || e.App.Hub.Index.Get("FakeEvNew01") == nil {
		t.Fatal("pruning: the old event must go, the recent one stay")
	}
}

// Review focus 2: an event with pending edits is kept past its retention.
func TestEventRetentionKeepsPending(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	old := e.App.Hub.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	e.Fake.SeedProgramEvent("FakeEvOld02", fc.EventProgram, fc.EventStage, old, fc.DefaultAOC)
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Offline()
	nurse.JSON("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{"event": "FakeEvOld02",
		"program": fc.EventProgram, "programStage": fc.EventStage, "orgUnit": e.M.Facility.ID, "occurredAt": old,
		"status": "COMPLETED"}}}, 200)
	e.App.Hub.Store.SetState("retention", []byte(`{"defaultMonths": 12}`)) // as a refresh would store it
	n, err := e.App.Hub.PruneEvents()
	if err != nil || n != 0 || e.App.Hub.Index.Get("FakeEvOld02") == nil {
		t.Fatalf("pruned %d (%v) with an edit pending", n, err)
	}
}
```

(imports: `"encoding/json"`, `"time"`.) Expected: FAIL (undefined `PruneEvents`).

- [ ] **Step 2: Implement.** Store:

```go
// OldProgramEvents lists synced events of an event program (no person) that central
// gave the hub and whose occurredAt (or, without one, createdAt) is before cutoff.
func (s *Store) OldProgramEvents(program, cutoff string) ([]string, error) {
	rows, err := s.DB().Query(`SELECT uid FROM objects WHERE kind = ? AND program = ? AND te_uid = '' AND origin = ?
 AND sync_state = ? AND coalesce(json_extract(slim, '$.occurredAt'), created_at) < ?`,
		KindEvent, program, OriginCentral, SyncSynced, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

// DeleteObjects removes objects (pruning). Their slots are not reused.
func (s *Store) DeleteObjects(uids []string) error {
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, u := range uids {
			if _, err := tx.Exec(`DELETE FROM objects WHERE uid = ?`, u); err != nil {
				return err
			}
		}
		return nil
	})
}
```

Index:

```go
// Forget drops the caches after objects were removed from the store (pruning).
func (ix *Index) Forget() {
	ix.payloads.clear()
	ix.pages.clear()
}
```

`internal/core/prune.go`:

```go
package core

// PruneEvents removes synced events of event programs whose date fell out of their
// program's retention; events with edits waiting in the outbox are kept.
func (h *Hub) PruneEvents() (int, error) {
	cat, ret, now := h.Catalog(), h.Retention(), h.CentralNow()
	n := 0
	for _, id := range cat.ProgramIDs() {
		cutoff, ok := ret.Cutoff(id, now)
		if pm := cat.Programs[id]; !pm.Event || !ok {
			continue
		}
		uids, err := h.Store.OldProgramEvents(id, cutoff)
		if err != nil {
			return n, err
		}
		var drop []string
		for _, u := range uids {
			if !h.Store.HasPendingOutboxFor(u, 0) {
				drop = append(drop, u)
			}
		}
		if len(drop) == 0 {
			continue
		}
		if err := h.Store.DeleteObjects(drop); err != nil {
			return n, err
		}
		n += len(drop)
	}
	if n > 0 {
		h.Index.Forget()
	}
	return n, nil
}
```

In `cycle`, after `w.pullAggregate`:

```go
	if n, err := h.PruneEvents(); err != nil {
		errs = append(errs, fmt.Errorf("retention: %w", err))
	} else if n > 0 {
		slog.Info("retention pruned events", "events", n)
	}
```

An event the hub holds with `sync_state` pending (the offline edit) is not `synced`, so the query skips it even before the outbox check; the outbox check covers a synced row with a newer pending entry.

Run: `go test ./internal/live -run 'TestEventRetention' -count=1 -v && go test ./... -count=1` — Expected: PASS.

- [ ] **Step 3: Commit** — "Retention: prune event-program events older than their program's window".

---

### Task 7: Offline writes: event programs and attribute option combinations

**Files:**
- Modify: `internal/tracker/import.go` (`validateEvent`, `validateEnrollment`)
- Test: `internal/live/eventprograms_test.go`

**Interfaces:**
- Consumes: `ProgramMeta.Event/CategoryCombo/EnrollmentCategoryCombo`, `Catalog.AOCs/ComboAOCs` (Task 3), `List.MayWriteOptions` (Task 4).
- Produces: `func (im *Importer) checkAOC(it *Item, aoc, combo string) []ErrorReport`.

- [ ] **Step 1: Failing tests:**

```go
// Events of event programs are captured offline and pushed as their author's.
func TestEventProgramWriteOffline(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Offline()
	ev := tk.UID()
	nurse.JSON("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{"event": ev,
		"program": fc.EventProgram, "programStage": fc.EventStage, "orgUnit": e.M.Facility.ID,
		"occurredAt": e.App.Hub.CentralNow().Format(time.DateOnly), "status": "ACTIVE"}}}, 200)
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, nil), nil, 200)); !slices.Contains(got, ev) {
		t.Fatalf("offline list after capture: %v", got)
	}
	e.Online()
	e.Sync()
	if e.CentralObject("events", ev) == nil {
		t.Fatal("not on central after the push")
	}
}

// Review focus 3: an attribute option combination is written offline only by users who
// may write each of its options.
func TestAttributeOptionWriteOffline(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	nurse, clerk := e.Web(tk.RoleNurse), e.Web(tk.RoleClerk)
	nurse.Login()
	clerk.Login()
	e.Sync()
	e.Offline()
	event := func(aoc string) map[string]any {
		return map[string]any{"events": []any{map[string]any{"event": tk.UID(), "program": fc.EventDonorProgram,
			"programStage": fc.EventDonorStage, "orgUnit": e.M.Facility.ID, "attributeOptionCombo": aoc,
			"occurredAt": e.App.Hub.CentralNow().Format(time.DateOnly), "status": "ACTIVE"}}}
	}
	nurse.JSON("POST", tk.SyncImport, event(fc.AggDonorAOCA), 200)
	if st, b := clerk.Do("POST", tk.SyncImport, event(fc.AggDonorAOCA)); st != 409 || !strings.Contains(string(b), "E1099") {
		t.Fatalf("clerk with option A offline: %d %s", st, b)
	}
	clerk.JSON("POST", tk.SyncImport, event(fc.AggDonorAOCB), 200)
	if st, b := clerk.Do("POST", tk.SyncImport, event("NoSuchAoc01")); st != 409 || !strings.Contains(string(b), "E1055") {
		t.Fatalf("an option combination of another combination: %d %s", st, b)
	}
	e.Online()
	e.Sync()
	if n := e.App.Hub.Store.CountIssues(); n != 0 {
		t.Fatalf("%d issues: central refused what the hub accepted", n)
	}
}
```

(imports: `"slices"`.) Expected: FAIL (E1055 "not supported offline" / E1033 enrollment not found).

- [ ] **Step 2: Implement.** In `import.go`:

```go
// checkAOC applies central's rule for a non-default attribute option combination
// offline: it must be one of combo's (E1055), and the user must be allowed to write
// each of its category options (E1099). combo is "" for a default combination, which
// takes no attribute option combination but the default.
func (im *Importer) checkAOC(it *Item, aoc, combo string) []ErrorReport {
	if aoc == "" || aoc == im.DefaultAOC {
		if combo != "" {
			return im.errf(it, "E1055", "Default AttributeOptionCombo is not allowed since program has non-default CategoryCombo.")
		}
		return nil
	}
	if combo == "" || !slices.Contains(im.V.Catalog.ComboAOCs[combo], aoc) {
		return im.errf(it, "E1055", "AttributeOptionCombo `%s` is not part of the program's CategoryCombo.", aoc)
	}
	if ok, missing := im.V.Access.MayWriteOptions(im.V.Catalog.AOCs[aoc]); !ok {
		return im.errf(it, "E1099", "User: `%s`, has no write access to CategoryOption: `%s`.", im.UserUID, missing)
	}
	return nil
}
```

`validateEvent`: replace the two E1055 blocks with

```go
	pm := im.V.Catalog.Programs[program]
	if errs := im.checkAOC(it, Str(it.Data, "attributeOptionCombo"), pm.CategoryCombo); errs != nil {
		return nil, false, errs
	}
	if pm.Event {
		return im.eventProgramEvent(it, old, program, stage)
	}
```

and add:

```go
// eventProgramEvent validates an event of an event program: no person or enrollment;
// the facility as org unit; data write access to the program and its stage.
func (im *Importer) eventProgramEvent(it *Item, old *Obj, program, stage string) (*Obj, bool, []ErrorReport) {
	if errs := im.orgUnitOK(it, old); errs != nil {
		return nil, false, errs
	}
	if !im.V.Access.Program(program).Write {
		return nil, false, im.errf(it, "E1091", "User: `%s`, has no data write access to Program: `%s`.", im.UserUID, program)
	}
	if !im.V.Access.Stage(stage).Write {
		return nil, false, im.errf(it, "E1095", "User: `%s` has no data write access to ProgramStage: `%s`.", im.UserUID, stage)
	}
	o := im.newObj(it, old)
	o.TE, o.Enrollment, o.Program, o.Stage = "", "", program, stage
	o.Data["program"], o.Data["programStage"] = program, stage
	delete(o.Data, "enrollment")
	delete(o.Data, "trackedEntity")
	if Str(o.Data, "status") == "" {
		o.Data["status"] = "ACTIVE"
	}
	return o, old == nil, nil
}
```

(match the return shape of the code after it in `validateEvent`: if `validateEvent` sets more fields on `o` before returning — `OwnerOrgUnit`, dates — do the same here.)

`validateEnrollment`: replace the E1055 block with

```go
	if errs := im.checkAOC(it, Str(it.Data, "attributeOptionCombo"), pm.EnrollmentCategoryCombo); errs != nil {
		return nil, false, errs
	}
```

Check the outbox push: an event without an enrollment goes to central as is (`tracker.Flatten` / `parents` return nothing for it); and that `orgUnitOK` does not look up an enrollment.

Run: `go test ./internal/live -run 'TestEventProgramWriteOffline|TestAttributeOptionWriteOffline' -count=1 -v && go test ./... -count=1` — Expected: PASS. Existing tests that expected the old E1055 "not supported offline" (search `not supported offline` in `internal/`) change to the new rule: update them to expect acceptance for a writable option.

- [ ] **Step 3: Commit** — "Offline writes: event-program events; attribute option combinations when the user may write their options (events and enrollments)".

---

### Task 8: Profiles and oracles on real DHIS2

**Files:**
- Modify: `internal/testkit/manifest.go` (`Program.Event`, `Program.AOCs`, `Program.EnrollmentAOCs`; `Manifest.EventPrograms`)
- Modify: `internal/testkit/fake.go` (fake event programs in the manifest)
- Modify: `internal/testkit/oracle.go` (`ReadTruth` event programs, `teOf`, `EventQueries`)
- Modify: `internal/testkit/provision/synthetic.go`, `synthetic_data.go` (donor event program, more events, manifest), `seeded.go` (the facility's event programs, grants, manifest)
- Modify: `internal/live/oracle_test.go`, `internal/live/writes_test.go`

- [ ] **Step 1: Manifest.** `Program` gains:

```go
	// Event: an event program (no tracked entity type, no enrollments).
	Event bool `json:"event,omitempty"`
	// AOCs: option combinations of the program's attribute category combination (its
	// events); EnrollmentAOCs: of its enrollment category combination (2.43). At most
	// two each: the write oracle tries every user with each.
	AOCs           []string `json:"aocs,omitempty"`
	EnrollmentAOCs []string `json:"enrollmentAocs,omitempty"`
```

`Manifest` gains `EventPrograms []Program \`json:"eventPrograms,omitempty"\``. The fake's manifest lists `{ID: fc.EventProgram, Name: "Fake event program", Event: true, Stages: []string{fc.EventStage}}` and `{ID: fc.EventDonorProgram, ..., AOCs: []string{fc.AggDonorAOCA, fc.AggDonorAOCB}}`.

- [ ] **Step 2: Oracle.** `ReadTruth(c, facility, programs, tets, eventPrograms []string)`: for each event program, list `/api/tracker/events?program&orgUnit=<facility>&paging=false&fields=event` as the superuser into `Owned[p][eventUID]`. `teOf`: for kind `events` without a `trackedEntity`, return the event's UID. New generator:

```go
// EventQueries is what the Capture apps ask of an event program: the working list
// (newest first, paged), by stage, Android's download, and one event.
func EventQueries(m *Manifest, p Program, sampleEvent string) []Query {
	ev := "event,programStage,orgUnit,status,occurredAt,attributeOptionCombo,dataValues[dataElement,value]"
	enc := func(v url.Values) string { return "/api/tracker/events?" + v.Encode() }
	base := func() url.Values { return url.Values{"program": {p.ID}, "orgUnit": {m.Facility.ID}, "fields": {ev}} }
	var qs []Query
	add := func(v url.Values) { qs = append(qs, Query{Kind: "events", Program: p.ID, Path: enc(v)}) }
	v := base()
	v.Set("paging", "false")
	add(v)
	v = base()
	v.Set("order", "occurredAt:desc")
	v.Set("page", "1")
	v.Set("pageSize", "20")
	add(v)
	for _, st := range p.Stages {
		v = base()
		v.Set("programStage", st)
		v.Set("paging", "false")
		add(v)
	}
	v = base()
	for k, x := range map[string]string{"orgUnitMode": "DESCENDANTS", "includeDeleted": "true", "paging": "true", "page": "1", "pageSize": "50"} {
		v.Set(k, x)
	}
	add(v)
	if sampleEvent != "" {
		qs = append(qs, Query{Kind: "events", Program: p.ID, Single: true,
			Path: "/api/tracker/events/" + sampleEvent + "?" + url.Values{"fields": {ev}}.Encode()})
	}
	return qs
}
```

`TestOracle`: pass the event program IDs to `ReadTruth`; append `tk.EventQueries(m, p, <any Owned event of p>)` for each of `m.EventPrograms`. `TestWriteOracle`: for each event program, a write "event in an event program" (default option combination, or for each of `p.AOCs`: "event under option combination X"), built as

```go
func programEvent(m *tk.Manifest, p tk.Program, aoc string) (string, map[string]any) {
	uid := tk.UID()
	ev := map[string]any{"event": uid, "program": p.ID, "programStage": p.Stages[0], "orgUnit": m.Facility.ID,
		"occurredAt": time.Now().UTC().Format(time.DateOnly), "status": "ACTIVE"}
	if aoc != "" {
		ev["attributeOptionCombo"] = aoc
	}
	if vals := p.EventValues[p.Stages[0]]; len(vals) > 0 {
		var dv []any
		for de, v := range vals {
			dv = append(dv, map[string]any{"dataElement": de, "value": v})
		}
		ev["dataValues"] = dv
	}
	return uid, map[string]any{"events": []any{ev}}
}
```

and for each tracker program with `EnrollmentAOCs`, a write per option combination: "register a person enrolled under option combination X" (`m.NewPerson` with the enrollment's `attributeOptionCombo` set). Add `E1091`, `E1099` to `accessCodes` if not there, so the hub accepting what central refuses for access is DENIED.

- [ ] **Step 3: Profiles.**
  - Synthetic: add a second event program `prEvD = "SynPrgEvD01"`, stage `stEvD = "SynStEvD001"` (data element `deNote`), `categoryCombo: ref(ccDonor)`, in the access layout's `Programs` (TET `""`), with 10×size events per donor option at the facility (option combinations from the existing `categoryOptionCombos` lookup); `prEvt` gets 20×size events spread over 3 years (`day(i*30)`) so retention has something to prune. Manifest: `EventPrograms` = both (`AOCs` of `prEvD` = both donor option combinations; `EventValues` as `prEvt`'s data elements need); `prHhs` on 2.43: `EnrollmentAOCs` = the donor option combinations.
  - Seeded: list the facility's event programs (`programType:eq:WITHOUT_REGISTRATION`, assigned to it) with their event counts at the facility (as the superuser); take up to three with events, preferring one with a non-default category combination; add them to the access layout's `Programs` (TET `""`) so the role and matrix users get program and stage grants; `AOCs`: the first two option combinations of its combination; `EventValues`: a valid value for each compulsory data element of its stage (as for tracker stages: reuse `sampleValue`).

- [ ] **Step 4: Live runs.** Check `GET /instances` (at most two running; other sessions may use the broker). Then, one at a time:

```bash
go run ./cmd/hubtest run -profile synthetic -version 42
go run ./cmd/hubtest run -profile sierra-leone -version 42
go run ./cmd/hubtest run -profile laos -version 42
go run ./cmd/hubtest run -profile synthetic -version 43
go run ./cmd/hubtest run -profile sierra-leone -version 43
go run ./cmd/hubtest run -profile laos -version 43
```

Expected: PASS; tracker read/write numbers as in docs/PERFORMANCE.md plus the event-program queries and writes; no DENIED. Do Task 4 Step 3 on the first synthetic instance. Probe and record central's codes for an event-program event by a user without program write and without option write (E1091 / E1099 as assumed in Task 7; if they differ, change `eventProgramEvent`/`checkAOC` to central's codes and messages). Any LEAK, MISSING or DENIED is a finding: fix it in the hub with a failing test first, or record a known gap with its reason.

- [ ] **Step 5: Android.** With the emulator (`ADB_SERVER_SOCKET` set) and a live instance: `HUB_CLIENTS=1 HUB_MANIFEST=... go test ./internal/live -run TestCaptureAndroid -count=1 -v`. Expected: PASS, the offline download's counts now including the event programs' events (compare the device's event count to central's for the facility's event programs).

- [ ] **Step 6: Commit** — "Event programs on real DHIS2: profiles, read and write oracles".

---

### Task 9: Size

**Files:**
- Modify: `internal/fakecentral/generate.go` (`Scale.EventPrograms`, `Scale.ProgramEvents`; generated event programs and their listing)
- Create: `internal/perf/events_test.go`
- Possibly modify: `internal/store/store.go` (an index migration), `internal/tracker/plans_test.go`

- [ ] **Step 1: Generator.** `Scale` gains `EventPrograms, ProgramEvents int`. Generated event programs `fmt.Sprintf("GenEvP%05d", p)` (stage `GenEvS%05d`, default combination, every generated user may read and write) are added to `programs` answers for `WITHOUT_REGISTRATION`; their events (`fmt.Sprintf("GvEv%09d", i)`, `occurredAt` spread evenly over 36 months back from today, `createdAt`/`updatedAt` the generation time, two data values) are answered by `/api/tracker/events?program=GenEvP…` with `updatedAfter`, `occurredAfter`, paging, rendered on the fly like the generated tracked entities (no per-event map).

- [ ] **Step 2: The test** (`internal/perf/events_test.go`): `scale(t)` with `EventPrograms = 2`, `ProgramEvents = 150000` (env override `HUB_PERF_EVENTS`); measure and log (and write to `HUB_PERF_OUT`): set-up with the first pull; an incremental cycle; offline as one user, a working list (`order=occurredAt:desc&page=1&pageSize=20`) page 1 and page 50; Android's full download of one program (`orgUnitMode=DESCENDANTS&includeDeleted=true&paging=true&pageSize=50`, every page, total time); then retention of 12 months set and one cycle: events pruned and the cycle's time; heap and data file size.

Run: `go test ./internal/perf -run TestEventPrograms -count=1 -v -timeout 60m`.

- [ ] **Step 3: Decide on an index from the numbers.** If the working list's page 50 or Android's pages take more than 100 ms (in process), the events of a program are being read and sorted in full per page: add a data migration

```sql
CREATE INDEX objects_events ON objects(kind, program, owner_org_unit, created_at, uid) WHERE te_uid = '';
```

(and, if the working list's `occurredAt` ordering is the slow one, an `occurred_at` column filled from the payload like `created_at`, with read-on paging keyed on it as for `createdAt` in `simplePlan`), add the query to `TestQueryPlansUseIndexes`, and record the numbers before and after. Otherwise record that none was needed.

- [ ] **Step 4: Commit** — "perf: event programs at size" (and the index, if any, in its own commit with its numbers).

---

### Task 10: Docs

**Files:** `docs/FINDINGS.md`, `docs/TESTING.md`, `docs/PERFORMANCE.md`, `README.md`, `docs/DEPLOYMENT.md`, `docs/SPEC-facility-hub-poc.md`, the spec (status)

- [ ] **Step 1:**
  - FINDINGS: item 20 ("Event programs … on hold") replaced by a section "Event programs through the hub": what central does (events without `enrollment`/`trackedEntity`; the codes found in Task 8), what the hub does (pull, deletes hidden at once, retention, offline writes with attribute option combinations), Android's and Capture's behaviour, known gaps (deletes offline refused; enrollments hidden by category options, as before).
  - TESTING: the event-program tests, `EventQueries`, the manifest's `eventPrograms`, the profiles' event programs.
  - PERFORMANCE: Task 9's numbers and the index decision; requests per cycle per event program and access group.
  - README: the dataStore key `facility-hub-config/retention` and its shape.
  - DEPLOYMENT: storage sizing note: event programs are held in full unless retention is set; recommend setting it for large registers.
  - SPEC: "Event programs" no longer outside scope (pointer to the spec).
  - The spec: status "implemented", the open question answered with Task 9's numbers.
- [ ] **Step 2:** `go vet ./... && go test ./... -count=1`, then commit "Event programs and retention: docs".
