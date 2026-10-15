# Aggregate Data Entry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Staff keep entering aggregate data in the Aggregate Data Entry app through the hub while the uplink is down, with every value reaching central under the user who entered it.

**Architecture:** A pure package `internal/aggregate` holds periods, windows, locks, the per-user access parsed from central's `dataEntry/metadata`, and edits. The store gets a table of forms (central's `dataEntry/dataValues` answers, sealed) and an outbox `kind` column. `internal/sync` pulls forms per access group and pushes aggregate outbox entries as their authors. `internal/aggregateapi` answers the app's five aggregate endpoints offline (or while the outbox waits), and passes them through to central otherwise.

**Tech Stack:** Go 1.24+ (standard library), SQLite through ncruces/go-sqlite3 (pure Go), Python Playwright for the real-client test.

**Spec:** `docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md`

## Global Constraints

- Online, and when the outbox is empty, every aggregate request passes through to central (`core.PassThrough`); the hub answers itself only offline or while the outbox waits.
- The hub never evaluates validation rules. Completion of a data set with `validCompleteOnly` is refused offline: 409, message `Completing needs a connection: this data set must pass validation first`.
- A refusal the app must show is a 409 in central's web message shape (`h.WebError`); never a 503 for an aggregate write, because the app then queues the write in the browser (shared-browser attribution bug).
- Error codes copied from central 2.42: locked period `E2017` "Data set is locked"; value type `E2030` "Data value must match data element value type: `<TYPE>`"; no access `E1100`.
- Window per data set: current period, `retention-1` periods before it, and `openFuturePeriods` after it, minus periods closed by expiry days or data input periods. Retention default 12 periods (config `aggregate_retention_periods`), per data set in dataStore `facility-hub-config/aggregate` as `{"retentionPeriods": 12, "dataSets": {"<id>": 6}}`.
- Period types held: Daily, Weekly (ISO, Monday), Monthly, BiMonthly, Quarterly, SixMonthly, SixMonthlyApril, Yearly, FinancialApril, FinancialJuly, FinancialOct. Data sets of any other type, or on a non-ISO calendar, are not held: their forms are answered locked offline.
- Forms outside the window, or not pulled, are answered `lockStatus: LOCKED` with no values.
- The app calls versioned paths (`/api/42/…`, `/api/43/…`); the hub routes both, and fetches `dataEntry/*` from central on the versioned path of central's minor version.
- Keep the structure (AGENTS.md): no cache or background job beyond what this plan names.
- Before each commit: `go vet ./...` and `go test ./...`; `go test -race` on `internal/e2e`, `internal/live`, `internal/tracker`, `internal/store` (and `internal/aggregate`, `internal/aggregateapi`).
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; never a `Claude-Session` trailer. Work on `main`, linear history.
- Go toolchain: `export PATH=$HOME/sdk/go/bin:$PATH` in the sandbox.

## Review Focus

1. **A cell edited, then its comment edited, then the value deleted, offline**: a comment edit never replaces a pending value edit, and a later delete is never folded into an entry before the comment, so central ends with the value deleted (Task 6 `TestAggregateCollapse`, Task 10 `TestAggregateCollapseKeepsFields`).
2. **Two users edit the same cell offline**: both edits are pushed, in the order they were made, each as its author; the last one wins on central (Task 9 test `TestAggregateSameCellTwoUsers`).
3. **A form with an attribute option combination the user may not use** (category option not shared): reads and writes refused, values never served (Task 7 test `TestAggregateAttributeAccess`).
4. **Period expires while edits wait**: push gets E2017, the value is kept as an issue, other cells and other forms keep pushing (Task 9 test `TestAggregateLockedOnPushKeepsValue`).
5. **Completion queued after values of the same form that cannot be pushed yet** (author needs online login): the completion waits for them (Task 9 test `TestAggregateCompletionWaitsForValues`).

---

## File structure

| File | Responsibility |
|---|---|
| `internal/proxy/proxy.go` (modify) | Task 1: `/apps/<app>` is an app file; GETs go to central without conditional headers |
| `internal/aggregate/period.go` | Periods of the supported types: current period, shift |
| `internal/aggregate/access.go` | `Access` parsed from `dataEntry/metadata`: data sets, elements, combos, attribute option combos |
| `internal/aggregate/lock.go` | Window, expiry, data input periods, lock status |
| `internal/aggregate/edit.go` | `Edit` (an outbox payload), applying edits to a form, the request that pushes one |
| `internal/aggregate/retention.go` | Retention table |
| `internal/store/aggregate.go` | Forms table; aggregate outbox entries (append with collapse, pending per form) |
| `internal/store/store.go` (modify) | Data migration 5 |
| `internal/store/outbox.go` (modify) | `Kind` on entries |
| `internal/core/aggregate.go` | Per-user `Access` (stored sealed), retention, central's minor version, form labels |
| `internal/core/refresh.go` (modify) | Access refresh also fetches `dataEntry/metadata`; catalog refresh reads retention |
| `internal/sync/aggregate.go` | Pull of forms; push of aggregate entries |
| `internal/sync/sync.go` (modify) | Cycle calls the pull; Push routes aggregate entries |
| `internal/aggregateapi/aggregateapi.go` | The five endpoints: pass-through, local answers, local writes |
| `internal/app/app.go` (modify) | Routing |
| `internal/ui/ui.go`, `templates/admin.html` (modify) | Issue labels, expiry warnings |
| `internal/fakecentral/aggregate.go` | Fake central's aggregate endpoints |
| `internal/testkit/*` (modify) | Manifest `Aggregate`, multipart client, central helpers |
| `internal/testkit/provision/*` (modify) | Synthetic data sets; data entry authorities; Sierra Leone sharing |
| `internal/live/aggregate_test.go` | Scenarios and oracle, fake and live |
| `internal/live/aggregate_client_test.go`, `clients/aggregate_data_entry.py` | Playwright real-client test |
| `internal/perf/aggregate_test.go`, `internal/fakecentral/generate.go` (modify) | Pull and form answer at HC IV size |
| `testdata/2.42/dataEntry_*.json` | Fixtures captured from central 2.42 |

---

### Task 1: Hub gap fixes for 2.42+ apps

**Files:**
- Modify: `internal/proxy/proxy.go` (`Forward`, `isStatic`)
- Modify: `internal/fakecentral/fakecentral.go` (serve `/apps/<app>`, answer 304 to `If-None-Match`)
- Test: `internal/e2e/e2e_test.go` (new `TestOfflineAppShellAndConditionalGets`)

**Interfaces:**
- Produces: nothing new; behaviour only.

- [ ] **Step 1: Make the fake answer like central for both cases.** In `fakecentral.go` `ServeHTTP`, in the first `switch` (before authentication), add:

```go
	case strings.HasPrefix(p, "/apps/"):
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><meta name="dhis2-base-url" content="http://` + r.Host + `">global shell ` + p + "</html>"))
		return
```

and in the authenticated `switch`, before `case p == "/api/system/info":`, add:

```go
	case p == "/api/userSettings":
		w.Header().Set("ETag", `"fake-settings"`)
		if r.Header.Get("If-None-Match") == `"fake-settings"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, 200, map[string]any{"keyUiLocale": "en"})
```

- [ ] **Step 2: Write the failing test** in `internal/e2e/e2e_test.go`, next to `TestOfflineStaticCache` (use that test's env helpers: `newEnv`, `e.setup()`, `e.login(user)`, `e.offline()`; read `TestOfflineStaticCache` first and use the same helper names it uses):

```go
// TestOfflineAppShellAndConditionalGets: on 2.42+ apps open at /apps/<app>, a page
// without a file extension; offline it is served to any user, like other app files.
// A browser that holds another user's copy asks conditionally: the hub must still
// get (and cache) a full answer for this user.
func TestOfflineAppShellAndConditionalGets(t *testing.T) {
	e := newEnv(t)
	e.setup()
	nurse := e.login("nurse_a")
	clerk := e.login("clerk_c")
	if st, _ := nurse.get("/apps/aggregate-data-entry"); st != 200 {
		t.Fatalf("online shell page: %d", st)
	}
	req := map[string]string{"If-None-Match": `"fake-settings"`}
	if st, _ := clerk.getWith("/api/42/userSettings", req); st != 200 {
		t.Fatalf("conditional GET through the hub: %d, want 200 (the hub asks without the condition)", st)
	}
	e.offline()
	if st, b := clerk.get("/apps/aggregate-data-entry"); st != 200 || !strings.Contains(string(b), "global shell") {
		t.Fatalf("offline, a user who never opened the shell page: %d %s", st, b)
	}
	if st, b := clerk.get("/api/42/userSettings"); st != 200 || !strings.Contains(string(b), "keyUiLocale") {
		t.Fatalf("offline userSettings for the clerk: %d %s", st, b)
	}
}
```

If the e2e client has no `getWith(path, headers)`, add it next to its `get` method in `e2e_test.go`:

```go
func (c *client) getWith(path string, hdr map[string]string) (int, []byte) {
	req, _ := http.NewRequest("GET", c.base+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.send(req)
}
```

(match the field and method names of the existing client type: if its base URL field or send helper is named differently, use those names.)

- [ ] **Step 3: Run it and see it fail.**

Run: `go test ./internal/e2e -run TestOfflineAppShellAndConditionalGets -count=1`
Expected: FAIL on "conditional GET through the hub: 304".

- [ ] **Step 4: Implement.** In `proxy.go` `Forward`, after the `hopHeaders` loop:

```go
	// A 304 cannot be cached for offline use: a browser holding another user's copy
	// (one browser, two staff) asks conditionally, and this user would get nothing
	// offline. Ask central for the full answer.
	if r.Method == http.MethodGet {
		hdr.Del("If-None-Match")
		hdr.Del("If-Modified-Since")
	}
```

In `isStatic`, before the extension `switch`:

```go
	// 2.42+ opens apps at /apps/<app> in its global shell: an app file without an
	// extension.
	if rest, ok := strings.CutPrefix(p, "/apps/"); ok && rest != "" && !strings.Contains(rest, "/") {
		return true
	}
```

- [ ] **Step 5: Run the test and the package's tests.**

Run: `go test ./internal/e2e ./internal/proxy -count=1`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/proxy/proxy.go internal/fakecentral/fakecentral.go internal/e2e/e2e_test.go
git commit -m "Offline: serve /apps/<app> to any user; ask central without conditional headers

Found driving the Aggregate Data Entry app through the hub on 2.42: a user who
never opened an app online could not open it offline, and a browser holding
another user's copy got 304s the hub cannot cache.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Periods

**Files:**
- Create: `internal/aggregate/period.go`
- Test: `internal/aggregate/period_test.go`

**Interfaces:**
- Produces:
  - `type Period struct { ID string; Start, End time.Time }` (dates at 00:00 UTC; `End` is the last day)
  - `func Current(periodType string, t time.Time) (Period, bool)` (false for unsupported types)
  - `func Shift(periodType string, p Period, n int) Period`
  - `func day(t time.Time) time.Time` (the calendar date of `t`, as 00:00 UTC; used by Task 4)

- [ ] **Step 1: Write the failing test.**

```go
package aggregate

import (
	"testing"
	"time"
)

func TestPeriods(t *testing.T) {
	at := time.Date(2026, 2, 15, 13, 0, 0, 0, time.FixedZone("EAT", 3*3600))
	cases := []struct{ typ, id, start, end string }{
		{"Daily", "20260215", "2026-02-15", "2026-02-15"},
		{"Weekly", "2026W7", "2026-02-09", "2026-02-15"},
		{"Monthly", "202602", "2026-02-01", "2026-02-28"},
		{"BiMonthly", "202601B", "2026-01-01", "2026-02-28"},
		{"Quarterly", "2026Q1", "2026-01-01", "2026-03-31"},
		{"SixMonthly", "2026S1", "2026-01-01", "2026-06-30"},
		{"SixMonthlyApril", "2025AprilS2", "2025-10-01", "2026-03-31"},
		{"Yearly", "2026", "2026-01-01", "2026-12-31"},
		{"FinancialApril", "2025April", "2025-04-01", "2026-03-31"},
		{"FinancialJuly", "2025July", "2025-07-01", "2026-06-30"},
		{"FinancialOct", "2025Oct", "2025-10-01", "2026-09-30"},
	}
	for _, c := range cases {
		p, ok := Current(c.typ, at)
		if !ok || p.ID != c.id || p.Start.Format(time.DateOnly) != c.start || p.End.Format(time.DateOnly) != c.end {
			t.Errorf("%s: %+v %v, want %s %s..%s", c.typ, p, ok, c.id, c.start, c.end)
		}
	}
	if _, ok := Current("WeeklyWednesday", at); ok {
		t.Error("WeeklyWednesday is not supported, Current must say so")
	}
	m, _ := Current("Monthly", at)
	if got := Shift("Monthly", m, -2).ID; got != "202512" {
		t.Errorf("two months before 202602: %s", got)
	}
	if got := Shift("Monthly", m, 11).ID; got != "202701" {
		t.Errorf("eleven months after 202602: %s", got)
	}
	w, _ := Current("Weekly", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if w.ID != "2026W1" || Shift("Weekly", w, -1).ID != "2025W52" {
		t.Errorf("ISO weeks around new year: %s, %s", w.ID, Shift("Weekly", w, -1).ID)
	}
}
```

- [ ] **Step 2: Run it and see it fail.**

Run: `go test ./internal/aggregate -run TestPeriods`
Expected: FAIL (package does not compile: `Current` undefined).

- [ ] **Step 3: Implement.**

```go
// Package aggregate holds what the hub needs to answer the Aggregate Data Entry app
// offline: periods, windows and locks, the access central gives each user
// (dataEntry/metadata), and edits as they are queued and pushed. It knows nothing of
// the store or of HTTP (docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md).
package aggregate

import (
	"fmt"
	"time"
)

// Period is one period of a period type, in the ISO calendar. Start and End are
// dates (00:00 UTC); End is the period's last day.
type Period struct {
	ID         string
	Start, End time.Time
}

type periodType struct {
	start func(d time.Time) time.Time // first day of the period holding the date d
	next  func(s time.Time) time.Time // first day of the next period
	id    func(s time.Time) string
}

// months: periods of n months, the year's first starting in month first (1 = January).
func months(n, first int, id func(s time.Time) string) periodType {
	return periodType{
		start: func(d time.Time) time.Time {
			since := (int(d.Month()) - first + 12) % 12
			return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(since % n), 0)
		},
		next: func(s time.Time) time.Time { return s.AddDate(0, n, 0) },
		id:   id,
	}
}

var periodTypes = map[string]periodType{
	"Daily": {
		start: func(d time.Time) time.Time { return d },
		next:  func(s time.Time) time.Time { return s.AddDate(0, 0, 1) },
		id:    func(s time.Time) string { return s.Format("20060102") },
	},
	"Weekly": {
		start: func(d time.Time) time.Time { return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) },
		next:  func(s time.Time) time.Time { return s.AddDate(0, 0, 7) },
		id: func(s time.Time) string {
			y, w := s.ISOWeek()
			return fmt.Sprintf("%dW%d", y, w)
		},
	},
	"Monthly":   months(1, 1, func(s time.Time) string { return s.Format("200601") }),
	"BiMonthly": months(2, 1, func(s time.Time) string { return fmt.Sprintf("%d%02dB", s.Year(), (int(s.Month())-1)/2+1) }),
	"Quarterly": months(3, 1, func(s time.Time) string { return fmt.Sprintf("%dQ%d", s.Year(), (int(s.Month())-1)/3+1) }),
	"SixMonthly": months(6, 1, func(s time.Time) string { return fmt.Sprintf("%dS%d", s.Year(), (int(s.Month())-1)/6+1) }),
	"SixMonthlyApril": months(6, 4, func(s time.Time) string {
		if s.Month() == time.April {
			return fmt.Sprintf("%dAprilS1", s.Year())
		}
		return fmt.Sprintf("%dAprilS2", s.Year())
	}),
	"Yearly":         months(12, 1, func(s time.Time) string { return fmt.Sprint(s.Year()) }),
	"FinancialApril": months(12, 4, func(s time.Time) string { return fmt.Sprintf("%dApril", s.Year()) }),
	"FinancialJuly":  months(12, 7, func(s time.Time) string { return fmt.Sprintf("%dJuly", s.Year()) }),
	"FinancialOct":   months(12, 10, func(s time.Time) string { return fmt.Sprintf("%dOct", s.Year()) }),
}

// day is the calendar date of t, in t's own time zone, as 00:00 UTC.
func day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

func period(pt periodType, s time.Time) Period {
	return Period{ID: pt.id(s), Start: s, End: pt.next(s).AddDate(0, 0, -1)}
}

// Current is the period of this type holding t (a time in central's time zone).
// ok is false for period types the hub does not hold.
func Current(typ string, t time.Time) (Period, bool) {
	pt, ok := periodTypes[typ]
	if !ok {
		return Period{}, false
	}
	return period(pt, pt.start(day(t))), true
}

// Shift is the period n periods after p (before it for negative n).
func Shift(typ string, p Period, n int) Period {
	pt := periodTypes[typ]
	s := p.Start
	for ; n > 0; n-- {
		s = pt.next(s)
	}
	for ; n < 0; n++ {
		s = pt.start(s.AddDate(0, 0, -1))
	}
	return period(pt, s)
}
```

- [ ] **Step 4: Run the test.**

Run: `go test ./internal/aggregate -run TestPeriods -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/aggregate/period.go internal/aggregate/period_test.go
git commit -m "aggregate: periods of the types the hub holds

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Access from `dataEntry/metadata`, value types, retention

**Files:**
- Create: `testdata/2.42/dataEntry_metadata.json`, `testdata/2.42/dataEntry_dataValues.json`, `testdata/2.42/dataValueSets.json`, `testdata/2.42/completeDataSetRegistrations.json`
- Create: `internal/aggregate/access.go`, `internal/aggregate/retention.go`
- Test: `internal/aggregate/access_test.go`

**Interfaces:**
- Produces:
  - `type Access struct { DataSets map[string]*DataSet; Elements map[string]*Element; Combos map[string]*Combo; Options map[string]bool; Bypass bool }`
  - `type DataSet struct { ID, Name, PeriodType, CategoryCombo string; ExpiryDays float64; OpenFuturePeriods int; InputPeriods []InputPeriod; ValidCompleteOnly, CompulsoryFieldsCompleteOnly bool; Compulsory []Operand; Elements map[string]string /* element -> its category combo in this data set */; OrgUnits []string }`
  - `type InputPeriod struct { Period, Opening, Closing string }`; `type Operand struct { DataElement, COC string }`
  - `type Element struct { ID, Name, ValueType, CategoryCombo string }`; `type Combo struct { ID string; Default bool; COCs []COC }`; `type COC struct { ID, Name string; Options []string }`
  - `func ParseMetadata(b []byte, authorities []string) (*Access, error)`
  - `func (a *Access) AOC(ds *DataSet, cc string, cp []string) (string, bool)`
  - `func (a *Access) Attribute(ds *DataSet, aoc string) (combo string, options []string, ok bool)`
  - `func (a *Access) MayUse(ds *DataSet, aoc string) bool`
  - `func (a *Access) COCName(id string) string`
  - `func (a *Access) Key() string`
  - `func (d *DataSet) Assigned(ou string) bool`
  - `func ValidValue(valueType, v string) bool`
  - `type Retention struct { Default int; DataSets map[string]int }`, `func (r Retention) For(ds string) int`

- [ ] **Step 1: Capture the fixtures** from the spike instance (`agent-agg-42`, Sierra Leone 2.42, users provisioned in the spike; start it with the broker if stopped):

```bash
C=http://dhis2-agent-agg-42:8080; P='hsl_nurse:Hub-Test-2026!'
curl -s -u "$P" "$C/api/42/dataEntry/metadata" | python3 -m json.tool > testdata/2.42/dataEntry_metadata.json
curl -s -u "$P" "$C/api/42/dataEntry/dataValues?ds=BfMAe6Itzgt&pe=202610&ou=DiszpKrYNg8" | python3 -m json.tool > testdata/2.42/dataEntry_dataValues.json
curl -s -u "$P" "$C/api/dataValueSets?dataSet=BfMAe6Itzgt&orgUnit=DiszpKrYNg8&startDate=2026-05-01&endDate=2026-10-31&includeDeleted=true" | python3 -m json.tool > testdata/2.42/dataValueSets.json
curl -s -u "$P" "$C/api/completeDataSetRegistrations?dataSet=BfMAe6Itzgt&orgUnit=DiszpKrYNg8&startDate=2026-01-01&endDate=2026-12-31" | python3 -m json.tool > testdata/2.42/completeDataSetRegistrations.json
grep -c BfMAe6Itzgt testdata/2.42/dataEntry_metadata.json   # > 0
```

Child Health (`BfMAe6Itzgt`) in that fixture: Monthly, default attribute combo, `openFuturePeriods` 10, one compulsory operand `O05mAByOgAv.V6L425pT3A0`, assigned to `DiszpKrYNg8`; element `fClA2Erf6IO` (Penta1, NUMBER, combo `dzjKKQq0cSO`) with option combo `V6L425pT3A0` ("Outreach, <1y").

- [ ] **Step 2: Write the failing test.**

```go
package aggregate

import (
	"os"
	"testing"
)

func metadataFixture(t *testing.T) *Access {
	t.Helper()
	b, err := os.ReadFile("../../testdata/2.42/dataEntry_metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := ParseMetadata(b, []string{"M_dhis-web-aggregate-data-entry"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestParseMetadata(t *testing.T) {
	a := metadataFixture(t)
	ds := a.DataSets["BfMAe6Itzgt"]
	if ds == nil || ds.PeriodType != "Monthly" || ds.OpenFuturePeriods != 10 || !ds.Assigned("DiszpKrYNg8") {
		t.Fatalf("Child Health: %+v", ds)
	}
	if len(ds.Compulsory) != 1 || ds.Compulsory[0] != (Operand{"O05mAByOgAv", "V6L425pT3A0"}) {
		t.Errorf("compulsory: %+v", ds.Compulsory)
	}
	if ds.Elements["fClA2Erf6IO"] != "dzjKKQq0cSO" || a.Elements["fClA2Erf6IO"].ValueType != "NUMBER" {
		t.Errorf("Penta1: %q %+v", ds.Elements["fClA2Erf6IO"], a.Elements["fClA2Erf6IO"])
	}
	aoc, ok := a.AOC(ds, "", nil)
	if !ok || aoc != "HllvX50cXC0" {
		t.Errorf("default attribute option combo: %q %v", aoc, ok)
	}
	if combo, opts, ok := a.Attribute(ds, aoc); !ok || combo != "bjDvmb4bfuf" || len(opts) != 1 || opts[0] != "xYerKDKCefk" {
		t.Errorf("default attribute: %q %v %v", combo, opts, ok)
	}
	if !a.MayUse(ds, aoc) || a.MayUse(ds, "NoSuchAoc01") {
		t.Error("MayUse")
	}
	if a.COCName("V6L425pT3A0") != "Outreach, <1y" {
		t.Errorf("COCName: %q", a.COCName("V6L425pT3A0"))
	}
	if a.Bypass {
		t.Error("no F_EDIT_EXPIRED, no bypass")
	}
	if b, _ := ParseMetadataFile(t, "F_EDIT_EXPIRED"); !b.Bypass {
		t.Error("F_EDIT_EXPIRED bypasses expiry")
	}
}

func ParseMetadataFile(t *testing.T, auth string) (*Access, error) {
	b, _ := os.ReadFile("../../testdata/2.42/dataEntry_metadata.json")
	return ParseMetadata(b, []string{auth})
}

func TestAccessKeyIgnoresOrder(t *testing.T) {
	a, b := metadataFixture(t), metadataFixture(t)
	if a.Key() != b.Key() || a.Key() == "" {
		t.Fatal("same metadata, same key")
	}
	delete(b.Options, "xYerKDKCefk")
	if a.Key() == b.Key() {
		t.Fatal("other options, other key")
	}
}

func TestValidValue(t *testing.T) {
	for _, c := range []struct {
		typ, v string
		ok     bool
	}{
		{"NUMBER", "1.5", true}, {"NUMBER", "x1", false}, {"INTEGER", "-3", true}, {"INTEGER", "1.5", false},
		{"INTEGER_POSITIVE", "0", false}, {"INTEGER_ZERO_OR_POSITIVE", "0", true}, {"INTEGER_NEGATIVE", "-1", true},
		{"PERCENTAGE", "101", false}, {"PERCENTAGE", "50", true}, {"BOOLEAN", "true", true}, {"BOOLEAN", "yes", false},
		{"TRUE_ONLY", "true", true}, {"TRUE_ONLY", "false", false}, {"DATE", "2026-02-30", false},
		{"DATE", "2026-02-28", true}, {"TEXT", "anything", true}, {"UNIT_INTERVAL", "0.5", true}, {"UNIT_INTERVAL", "2", false},
	} {
		if got := ValidValue(c.typ, c.v); got != c.ok {
			t.Errorf("%s %q: %v, want %v", c.typ, c.v, got, c.ok)
		}
	}
}

func TestRetention(t *testing.T) {
	r := Retention{Default: 12, DataSets: map[string]int{"A": 3}}
	if r.For("A") != 3 || r.For("B") != 12 || (Retention{}).For("B") != 12 {
		t.Fatal("retention")
	}
}
```

- [ ] **Step 3: Run it and see it fail.**

Run: `go test ./internal/aggregate -run 'TestParseMetadata|TestAccessKey|TestValidValue|TestRetention'`
Expected: FAIL (undefined `ParseMetadata`).

- [ ] **Step 4: Implement `access.go`.**

```go
package aggregate

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Access is what one user may enter, from central's dataEntry/metadata: the data sets
// with data write sharing for the user, their elements and category combinations,
// and the category options the user may enter data for. The hub matches against it;
// central decided it.
type Access struct {
	DataSets map[string]*DataSet `json:"dataSets"`
	Elements map[string]*Element `json:"elements"`
	Combos   map[string]*Combo   `json:"combos"`
	Options  map[string]bool     `json:"options"`
	// Bypass: F_EDIT_EXPIRED or ALL, so expiry days do not lock periods for this user.
	Bypass bool `json:"bypass"`
}

type DataSet struct {
	ID                           string            `json:"id"`
	Name                         string            `json:"name"`
	PeriodType                   string            `json:"periodType"`
	CategoryCombo                string            `json:"categoryCombo"`
	ExpiryDays                   float64           `json:"expiryDays"`
	OpenFuturePeriods            int               `json:"openFuturePeriods"`
	InputPeriods                 []InputPeriod     `json:"inputPeriods,omitempty"`
	ValidCompleteOnly            bool              `json:"validCompleteOnly"`
	CompulsoryFieldsCompleteOnly bool              `json:"compulsoryFieldsCompleteOnly"`
	Compulsory                   []Operand         `json:"compulsory,omitempty"`
	Elements                     map[string]string `json:"elements"` // element -> its category combo in this data set
	OrgUnits                     []string          `json:"orgUnits"`
}

type InputPeriod struct{ Period, Opening, Closing string }

type Operand struct{ DataElement, COC string }

type Element struct{ ID, Name, ValueType, CategoryCombo string }

type Combo struct {
	ID      string `json:"id"`
	Default bool   `json:"default"`
	COCs    []COC  `json:"cocs"`
}

type COC struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Options []string `json:"options"`
}

type ref struct {
	ID string `json:"id"`
}

// ParseMetadata reads central's dataEntry/metadata answer for a user with these
// authorities.
func ParseMetadata(b []byte, authorities []string) (*Access, error) {
	var md struct {
		DataSets []struct {
			ID                           string  `json:"id"`
			DisplayName                  string  `json:"displayName"`
			PeriodType                   string  `json:"periodType"`
			CategoryCombo                ref     `json:"categoryCombo"`
			ExpiryDays                   float64 `json:"expiryDays"`
			OpenFuturePeriods            int     `json:"openFuturePeriods"`
			ValidCompleteOnly            bool    `json:"validCompleteOnly"`
			CompulsoryFieldsCompleteOnly bool    `json:"compulsoryFieldsCompleteOnly"`
			DataInputPeriods             []struct {
				Period      ref    `json:"period"`
				OpeningDate string `json:"openingDate"`
				ClosingDate string `json:"closingDate"`
			} `json:"dataInputPeriods"`
			Compulsory []struct {
				DataElement         ref `json:"dataElement"`
				CategoryOptionCombo ref `json:"categoryOptionCombo"`
			} `json:"compulsoryDataElementOperands"`
			DataSetElements []struct {
				DataElement   ref  `json:"dataElement"`
				CategoryCombo *ref `json:"categoryCombo"`
			} `json:"dataSetElements"`
			OrganisationUnits []any `json:"organisationUnits"` // ids, or objects with an id
		} `json:"dataSets"`
		DataElements []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"displayName"`
			ValueType     string `json:"valueType"`
			CategoryCombo ref    `json:"categoryCombo"`
		} `json:"dataElements"`
		CategoryCombos []struct {
			ID                   string `json:"id"`
			IsDefault            bool   `json:"isDefault"`
			CategoryOptionCombos []struct {
				ID              string   `json:"id"`
				DisplayName     string   `json:"displayName"`
				CategoryOptions []string `json:"categoryOptions"`
			} `json:"categoryOptionCombos"`
		} `json:"categoryCombos"`
		CategoryOptions []ref `json:"categoryOptions"`
	}
	if err := json.Unmarshal(b, &md); err != nil {
		return nil, err
	}
	a := &Access{DataSets: map[string]*DataSet{}, Elements: map[string]*Element{}, Combos: map[string]*Combo{},
		Options: map[string]bool{}, Bypass: slices.Contains(authorities, "F_EDIT_EXPIRED") || slices.Contains(authorities, "ALL")}
	for _, e := range md.DataElements {
		a.Elements[e.ID] = &Element{ID: e.ID, Name: e.DisplayName, ValueType: e.ValueType, CategoryCombo: e.CategoryCombo.ID}
	}
	for _, c := range md.CategoryCombos {
		cc := &Combo{ID: c.ID, Default: c.IsDefault}
		for _, coc := range c.CategoryOptionCombos {
			cc.COCs = append(cc.COCs, COC{ID: coc.ID, Name: coc.DisplayName, Options: coc.CategoryOptions})
		}
		a.Combos[c.ID] = cc
	}
	for _, o := range md.CategoryOptions {
		a.Options[o.ID] = true
	}
	for _, d := range md.DataSets {
		ds := &DataSet{ID: d.ID, Name: d.DisplayName, PeriodType: d.PeriodType, CategoryCombo: d.CategoryCombo.ID,
			ExpiryDays: d.ExpiryDays, OpenFuturePeriods: d.OpenFuturePeriods, ValidCompleteOnly: d.ValidCompleteOnly,
			CompulsoryFieldsCompleteOnly: d.CompulsoryFieldsCompleteOnly, Elements: map[string]string{}}
		for _, ip := range d.DataInputPeriods {
			ds.InputPeriods = append(ds.InputPeriods, InputPeriod{Period: ip.Period.ID, Opening: ip.OpeningDate, Closing: ip.ClosingDate})
		}
		for _, op := range d.Compulsory {
			ds.Compulsory = append(ds.Compulsory, Operand{op.DataElement.ID, op.CategoryOptionCombo.ID})
		}
		for _, dse := range d.DataSetElements {
			cc := ""
			if e := a.Elements[dse.DataElement.ID]; e != nil {
				cc = e.CategoryCombo
			}
			if dse.CategoryCombo != nil && dse.CategoryCombo.ID != "" {
				cc = dse.CategoryCombo.ID
			}
			ds.Elements[dse.DataElement.ID] = cc
		}
		for _, ou := range d.OrganisationUnits {
			switch v := ou.(type) {
			case string:
				ds.OrgUnits = append(ds.OrgUnits, v)
			case map[string]any:
				if id, _ := v["id"].(string); id != "" {
					ds.OrgUnits = append(ds.OrgUnits, id)
				}
			}
		}
		a.DataSets[d.ID] = ds
	}
	return a, nil
}

// Assigned reports whether the data set is assigned to the org unit.
func (d *DataSet) Assigned(ou string) bool { return slices.Contains(d.OrgUnits, ou) }

// AOC is the attribute option combination the app means by cc and cp (the
// attribute combination and its options): the data set's only combination when it
// has the default one and the app sent none.
func (a *Access) AOC(ds *DataSet, cc string, cp []string) (string, bool) {
	combo := a.Combos[ds.CategoryCombo]
	if combo == nil || (cc != "" && cc != ds.CategoryCombo) {
		return "", false
	}
	if cc == "" {
		if !combo.Default || len(combo.COCs) != 1 {
			return "", false
		}
		return combo.COCs[0].ID, true
	}
	want := slices.Sorted(slices.Values(cp))
	for _, coc := range combo.COCs {
		if slices.Equal(slices.Sorted(slices.Values(coc.Options)), want) {
			return coc.ID, true
		}
	}
	return "", false
}

// Attribute is the combination and options of an attribute option combination of the
// data set, as dataEntry/dataValues writes it ("attribute": {"combo", "options"}).
func (a *Access) Attribute(ds *DataSet, aoc string) (combo string, options []string, ok bool) {
	c := a.Combos[ds.CategoryCombo]
	if c == nil {
		return "", nil, false
	}
	for _, coc := range c.COCs {
		if coc.ID == aoc {
			return c.ID, coc.Options, true
		}
	}
	return "", nil, false
}

// MayUse reports whether the user may enter data for this attribute option
// combination of the data set: it is the data set's, and every option of it is one
// central lists for the user.
func (a *Access) MayUse(ds *DataSet, aoc string) bool {
	_, opts, ok := a.Attribute(ds, aoc)
	if !ok {
		return false
	}
	for _, o := range opts {
		if !a.Options[o] {
			return false
		}
	}
	return true
}

// COCName is a category option combination's display name, or its ID.
func (a *Access) COCName(id string) string {
	for _, c := range a.Combos {
		for _, coc := range c.COCs {
			if coc.ID == id {
				return coc.Name
			}
		}
	}
	return id
}

// Key is the same for users whose metadata grants the same data sets and options:
// the hub pulls their forms once (as with tracker listings, access.List.GroupKey).
func (a *Access) Key() string {
	if a == nil || len(a.DataSets) == 0 {
		return ""
	}
	ds := make([]string, 0, len(a.DataSets))
	for id := range a.DataSets {
		ds = append(ds, id)
	}
	opts := make([]string, 0, len(a.Options))
	for id := range a.Options {
		opts = append(opts, id)
	}
	sort.Strings(ds)
	sort.Strings(opts)
	return strings.Join(ds, ",") + "|" + strings.Join(opts, ",")
}

// ValidValue reports whether central accepts v for a data element of this value type
// (E2030 otherwise). Types the hub does not check are accepted: central decides on
// push.
func ValidValue(valueType, v string) bool {
	num := func() (float64, bool) {
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	}
	integer := func() (int64, bool) {
		i, err := strconv.ParseInt(v, 10, 64)
		return i, err == nil
	}
	switch valueType {
	case "NUMBER":
		_, ok := num()
		return ok
	case "UNIT_INTERVAL":
		f, ok := num()
		return ok && f >= 0 && f <= 1
	case "PERCENTAGE":
		f, ok := num()
		return ok && f >= 0 && f <= 100
	case "INTEGER":
		_, ok := integer()
		return ok
	case "INTEGER_POSITIVE":
		i, ok := integer()
		return ok && i > 0
	case "INTEGER_NEGATIVE":
		i, ok := integer()
		return ok && i < 0
	case "INTEGER_ZERO_OR_POSITIVE":
		i, ok := integer()
		return ok && i >= 0
	case "BOOLEAN":
		return v == "true" || v == "false"
	case "TRUE_ONLY":
		return v == "true"
	case "DATE":
		_, err := time.Parse(time.DateOnly, v)
		return err == nil
	}
	return true
}
```

`retention.go`:

```go
package aggregate

// Retention is how many periods of each data set the hub keeps (the current one
// included), read from central's dataStore (facility-hub-config/aggregate).
type Retention struct {
	Default  int            `json:"retentionPeriods"`
	DataSets map[string]int `json:"dataSets"`
}

// DefaultRetention is used when neither the dataStore nor the configuration says.
const DefaultRetention = 12

func (r Retention) For(ds string) int {
	if n := r.DataSets[ds]; n > 0 {
		return n
	}
	if r.Default > 0 {
		return r.Default
	}
	return DefaultRetention
}
```

- [ ] **Step 5: Run the tests.**

Run: `go test ./internal/aggregate -v`
Expected: PASS. If `TestParseMetadata` fails on `organisationUnits`, print `string(b)` around `"organisationUnits"` in the fixture and fix the decoding to its shape.

- [ ] **Step 6: Commit.**

```bash
git add testdata/2.42/dataEntry_*.json testdata/2.42/dataValueSets.json testdata/2.42/completeDataSetRegistrations.json internal/aggregate/
git commit -m "aggregate: access from dataEntry/metadata, value types, retention; 2.42 fixtures

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Window and lock status

**Files:**
- Create: `internal/aggregate/lock.go`
- Test: `internal/aggregate/lock_test.go`

**Interfaces:**
- Consumes: `Period`, `Current`, `Shift`, `day` (Task 2); `DataSet` (Task 3).
- Produces:
  - `const (Open = "OPEN"; Locked = "LOCKED"; Approved = "APPROVED")`
  - `func Window(ds *DataSet, now time.Time, retention int) []Period` (oldest first)
  - `func InWindow(ds *DataSet, now time.Time, retention int, pe string) (Period, bool)`
  - `func Expired(ds *DataSet, p Period, now time.Time) bool`
  - `func LocksAt(ds *DataSet, p Period) (time.Time, bool)` (when expiry locks it)
  - `func InputOpen(ds *DataSet, p Period, now time.Time) bool`
  - `func LockStatus(ds *DataSet, p Period, now time.Time, bypass bool, central string) string`

- [ ] **Step 1: Write the failing test.**

```go
package aggregate

import (
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	ds := &DataSet{ID: "A", PeriodType: "Monthly", OpenFuturePeriods: 1}
	var ids []string
	for _, p := range Window(ds, now, 3) {
		ids = append(ids, p.ID)
	}
	if got := ids; len(got) != 4 || got[0] != "202608" || got[3] != "202611" {
		t.Fatalf("window, 3 periods kept and one future: %v", got)
	}
	ds.ExpiryDays = 10 // September locks on 2026-10-10
	ids = nil
	for _, p := range Window(ds, now, 3) {
		ids = append(ids, p.ID)
	}
	if len(ids) != 2 || ids[0] != "202610" {
		t.Fatalf("expired periods leave the window: %v", ids)
	}
	if _, ok := InWindow(ds, now, 3, "202609"); ok {
		t.Fatal("202609 has expired")
	}
	if _, ok := InWindow(&DataSet{PeriodType: "WeeklyWednesday"}, now, 3, "2026WedW42"); ok {
		t.Fatal("unsupported period type: nothing held")
	}
}

func TestLockStatus(t *testing.T) {
	ds := &DataSet{ID: "A", PeriodType: "Monthly", ExpiryDays: 10}
	sep, _ := Current("Monthly", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	before := time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC)
	after := time.Date(2026, 10, 10, 0, 1, 0, 0, time.UTC)
	if at, ok := LocksAt(ds, sep); !ok || !at.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("LocksAt: %v %v", at, ok)
	}
	for _, c := range []struct {
		now     time.Time
		bypass  bool
		central string
		want    string
	}{
		{before, false, "", Open},
		{before, false, Open, Open},
		{after, false, Open, Locked},  // expired since central answered
		{after, true, Open, Open},     // F_EDIT_EXPIRED
		{before, false, Approved, Approved},
		{before, true, Locked, Locked}, // central's lock stands (closed org unit, ...)
	} {
		if got := LockStatus(ds, sep, c.now, c.bypass, c.central); got != c.want {
			t.Errorf("%v bypass=%v central=%q: %s, want %s", c.now, c.bypass, c.central, got, c.want)
		}
	}
	ds.InputPeriods = []InputPeriod{{Period: "202609", Opening: "2026-10-01T00:00:00.000", Closing: "2026-10-05T00:00:00.000"}}
	ds.ExpiryDays = 0
	if LockStatus(ds, sep, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), false, "") != Open ||
		LockStatus(ds, sep, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), false, "") != Locked {
		t.Error("data input period")
	}
	aug, _ := Current("Monthly", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if InputOpen(ds, aug, before) {
		t.Error("a period without a data input period is closed when the data set has any")
	}
}
```

- [ ] **Step 2: Run it and see it fail.**

Run: `go test ./internal/aggregate -run 'TestWindow|TestLockStatus'`
Expected: FAIL (undefined `Window`).

- [ ] **Step 3: Implement.**

```go
package aggregate

import (
	"time"
)

// Lock states, as dataEntry/dataValues answers them.
const (
	Open     = "OPEN"
	Locked   = "LOCKED"
	Approved = "APPROVED"
)

// Window is the periods of the data set the hub holds at now (central's time): the
// current one, retention-1 before it and the open future periods, oldest first,
// without the periods expiry days or data input periods close (central refuses
// writes there, so the hub holds nothing for them).
func Window(ds *DataSet, now time.Time, retention int) []Period {
	cur, ok := Current(ds.PeriodType, now)
	if !ok {
		return nil
	}
	if retention < 1 {
		retention = 1
	}
	var out []Period
	for n := -(retention - 1); n <= ds.OpenFuturePeriods; n++ {
		p := Shift(ds.PeriodType, cur, n)
		if Expired(ds, p, now) || !InputOpen(ds, p, now) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// InWindow finds the period pe in the data set's window.
func InWindow(ds *DataSet, now time.Time, retention int, pe string) (Period, bool) {
	for _, p := range Window(ds, now, retention) {
		if p.ID == pe {
			return p, true
		}
	}
	return Period{}, false
}

// LocksAt is when expiry days lock the period: its last day plus that many days, as
// central counts (ok false without expiry days).
func LocksAt(ds *DataSet, p Period) (time.Time, bool) {
	if ds.ExpiryDays <= 0 {
		return time.Time{}, false
	}
	return p.End.Add(time.Duration(ds.ExpiryDays * float64(24*time.Hour))), true
}

// Expired: expiry days have locked the period at now.
func Expired(ds *DataSet, p Period, now time.Time) bool {
	at, ok := LocksAt(ds, p)
	return ok && naive(now).After(at)
}

// InputOpen: data input periods, if the data set has any, allow writes to p at now.
func InputOpen(ds *DataSet, p Period, now time.Time) bool {
	if len(ds.InputPeriods) == 0 {
		return true
	}
	n := naive(now)
	for _, ip := range ds.InputPeriods {
		if ip.Period != p.ID {
			continue
		}
		if t, err := parseDHIS2Time(ip.Opening); err == nil && n.Before(t) {
			return false
		}
		if t, err := parseDHIS2Time(ip.Closing); err == nil && n.After(t) {
			return false
		}
		return true
	}
	return false
}

// LockStatus is a form's lock for a user at now. central is the lock status central
// answered when the hub fetched the form ("" if it never did): approval and central's
// own locks stand; expiry days and data input periods are applied at now, because a
// period can expire after the hub fetched it.
func LockStatus(ds *DataSet, p Period, now time.Time, bypass bool, central string) string {
	switch {
	case central == Approved:
		return Approved
	case central == Locked:
		return Locked
	case !InputOpen(ds, p, now):
		return Locked
	case Expired(ds, p, now) && !bypass:
		return Locked
	}
	return Open
}

// naive is now's wall clock in its own time zone, as UTC: periods are dates.
func naive(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// parseDHIS2Time reads central's timestamps ("2026-10-05T00:00:00.000", with or
// without an offset) as wall-clock times.
func parseDHIS2Time(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05", time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return naive(t), nil
		}
	}
	return time.Parse(time.RFC3339, s)
}
```

Note `Window` with `ExpiryDays` treats "bypass" users like everyone: a period expired for most users is not held (spec: "expiry closes periods"). Users with `F_EDIT_EXPIRED` enter expired periods online.

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/aggregate -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/aggregate/lock.go internal/aggregate/lock_test.go
git commit -m "aggregate: window per data set and lock status

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Edits and forms

**Files:**
- Create: `internal/aggregate/edit.go`
- Test: `internal/aggregate/edit_test.go`

**Interfaces:**
- Consumes: `Access`, `DataSet` (Task 3).
- Produces:
  - `type FormKey struct { DataSet, Period, OrgUnit, AOC string }`, `func (k FormKey) String() string` (`"agg:ds|pe|ou|aoc"`)
  - `type Edit struct { Op string; Form FormKey; CC string; CP []string; DataElement, COC string; Value, Comment *string; FollowUp *bool; Completed bool; By string; At string }` with ops `OpValue = "value"`, `OpDelete = "delete"`, `OpComplete = "complete"`
  - `func (e *Edit) Field() string` (`"value"`, `"comment"`, `"followUp"`, `"complete"`; delete counts as `"value"`)
  - `func (e *Edit) CellKey() string` (`FormKey.String()+"|"+de+"|"+coc+"|"+Field()`; for a completion `FormKey.String()+"|complete"`)
  - `type Form map[string]any` (central's dataEntry/dataValues answer)
  - `func EmptyForm() Form`
  - `func (f Form) Apply(e *Edit, combo string, options []string)`
  - `func (f Form) Value(de, coc string) (string, bool)`
  - `func (e *Edit) Request() (method, path string, body []byte)`

- [ ] **Step 1: Write the failing test.**

```go
package aggregate

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

func formFixture(t *testing.T) Form {
	b, err := os.ReadFile("../../testdata/2.42/dataEntry_dataValues.json")
	if err != nil {
		t.Fatal(err)
	}
	var f Form
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func ptr[T any](v T) *T { return &v }

func TestFormApply(t *testing.T) {
	f := formFixture(t)
	k := FormKey{"BfMAe6Itzgt", "202610", "DiszpKrYNg8", "HllvX50cXC0"}
	f.Apply(&Edit{Op: OpValue, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", Value: ptr("44"), By: "nurse",
		At: "2026-10-15T10:00:00.000"}, "bjDvmb4bfuf", []string{"xYerKDKCefk"})
	if v, ok := f.Value("fClA2Erf6IO", "V6L425pT3A0"); !ok || v != "44" {
		t.Fatalf("value after edit: %q %v", v, ok)
	}
	f.Apply(&Edit{Op: OpValue, Form: k, DataElement: "NewDeAaaa01", COC: "V6L425pT3A0", Comment: ptr("seen"), By: "nurse",
		At: "2026-10-15T10:00:00.000"}, "bjDvmb4bfuf", []string{"xYerKDKCefk"})
	b, _ := json.Marshal(f)
	if !strings.Contains(string(b), `"comment":"seen"`) || !strings.Contains(string(b), `"options":["xYerKDKCefk"]`) {
		t.Fatalf("new cell with a comment: %s", b)
	}
	f.Apply(&Edit{Op: OpDelete, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", By: "nurse"}, "", nil)
	if _, ok := f.Value("fClA2Erf6IO", "V6L425pT3A0"); ok {
		t.Fatal("deleted value still there")
	}
	f.Apply(&Edit{Op: OpComplete, Form: k, Completed: true, By: "clerk", At: "2026-10-15T11:00:00.000"}, "", nil)
	cs := f["completeStatus"].(map[string]any)
	if cs["complete"] != true || cs["lastUpdatedBy"] != "clerk" {
		t.Fatalf("completeStatus: %v", cs)
	}
}

func TestEditKeysAndRequest(t *testing.T) {
	k := FormKey{"BfMAe6Itzgt", "202610", "DiszpKrYNg8", "HllvX50cXC0"}
	v := &Edit{Op: OpValue, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", Value: ptr("5")}
	c := &Edit{Op: OpValue, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", Comment: ptr("x")}
	d := &Edit{Op: OpDelete, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0"}
	if v.CellKey() == c.CellKey() || v.CellKey() != d.CellKey() {
		t.Fatalf("value and comment are separate fields; delete replaces the value: %s %s %s", v.CellKey(), c.CellKey(), d.CellKey())
	}
	m, p, body := v.Request()
	u, _ := url.Parse(p)
	q := u.Query()
	if m != "POST" || u.Path != "/api/dataValues" || q.Get("value") != "5" || q.Get("ds") != "BfMAe6Itzgt" || body != nil ||
		q.Has("cc") {
		t.Fatalf("value request: %s %s", m, p)
	}
	att := &Edit{Op: OpValue, Form: k, CC: "SynCcDonor1", CP: []string{"b", "a"}, DataElement: "x", COC: "y", Value: ptr("1")}
	_, p, _ = att.Request()
	u, _ = url.Parse(p)
	if u.Query().Get("cc") != "SynCcDonor1" || u.Query().Get("cp") != "b;a" {
		t.Fatalf("attribute request: %s", p)
	}
	if m, p, _ := d.Request(); m != "DELETE" || !strings.HasPrefix(p, "/api/dataValues?") {
		t.Fatalf("delete: %s %s", m, p)
	}
	m, p, body = (&Edit{Op: OpComplete, Form: k, Completed: true}).Request()
	if m != "POST" || p != "/api/dataEntry/dataSetCompletion" || !strings.Contains(string(body), `"completed":true`) {
		t.Fatalf("completion: %s %s %s", m, p, body)
	}
}
```

- [ ] **Step 2: Run it and see it fail.**

Run: `go test ./internal/aggregate -run 'TestFormApply|TestEditKeys'`
Expected: FAIL (undefined `Form`).

- [ ] **Step 3: Implement.**

```go
package aggregate

import (
	"encoding/json"
	"net/url"
	"strings"
)

// FormKey names one form: a data set, period, org unit and attribute option
// combination.
type FormKey struct{ DataSet, Period, OrgUnit, AOC string }

func (k FormKey) String() string {
	return "agg:" + k.DataSet + "|" + k.Period + "|" + k.OrgUnit + "|" + k.AOC
}

// Edit ops.
const (
	OpValue    = "value" // POST dataValues: value, comment or followUp
	OpDelete   = "delete"
	OpComplete = "complete" // dataEntry/dataSetCompletion, completed true or false
)

// Edit is one change a user made offline, as queued in the outbox. CC and CP are the
// attribute combination and options as the app sent them (empty for the default).
type Edit struct {
	Op          string   `json:"op"`
	Form        FormKey  `json:"form"`
	CC          string   `json:"cc,omitempty"`
	CP          []string `json:"cp,omitempty"`
	DataElement string   `json:"de,omitempty"`
	COC         string   `json:"co,omitempty"`
	Value       *string  `json:"value,omitempty"`
	Comment     *string  `json:"comment,omitempty"`
	FollowUp    *bool    `json:"followUp,omitempty"`
	Completed   bool     `json:"completed,omitempty"`
	By          string   `json:"by"` // username, for the form shown offline
	At          string   `json:"at"` // central's time, "2006-01-02T15:04:05.000"
}

// Field is what part of a cell the edit sets: edits of the same field replace each
// other in the outbox (store.AppendAggregate); a delete replaces the value.
func (e *Edit) Field() string {
	switch {
	case e.Op == OpComplete:
		return "complete"
	case e.Op == OpDelete || e.Value != nil:
		return "value"
	case e.Comment != nil:
		return "comment"
	}
	return "followUp"
}

func (e *Edit) CellKey() string {
	if e.Op == OpComplete {
		return e.Form.String() + "|complete"
	}
	return e.Form.String() + "|" + e.DataElement + "|" + e.COC + "|" + e.Field()
}

// Form is central's dataEntry/dataValues answer for one form.
type Form map[string]any

// EmptyForm is a form central has no data for.
func EmptyForm() Form {
	return Form{"dataValues": []any{}, "minMaxValues": []any{}, "lockStatus": Open,
		"completeStatus": map[string]any{"complete": false}}
}

func (f Form) values() []any {
	vs, _ := f["dataValues"].([]any)
	return vs
}

func (f Form) find(de, coc string) (int, map[string]any) {
	for i, v := range f.values() {
		if m, ok := v.(map[string]any); ok && m["dataElement"] == de && m["categoryOptionCombo"] == coc {
			return i, m
		}
	}
	return -1, nil
}

// Value is a cell's value in the form.
func (f Form) Value(de, coc string) (string, bool) {
	_, m := f.find(de, coc)
	if m == nil {
		return "", false
	}
	v, ok := m["value"].(string)
	return v, ok
}

// Apply puts an edit into the form as central would show it afterwards. combo and
// options are the form's attribute ("attribute": {"combo", "options"}), for new cells.
func (f Form) Apply(e *Edit, combo string, options []string) {
	switch e.Op {
	case OpComplete:
		cs, _ := f["completeStatus"].(map[string]any)
		if cs == nil {
			cs = map[string]any{}
		}
		if _, ok := cs["created"]; !ok {
			cs["created"], cs["createdBy"] = e.At, e.By
		}
		cs["complete"], cs["lastUpdated"], cs["lastUpdatedBy"] = e.Completed, e.At, e.By
		f["completeStatus"] = cs
	case OpDelete:
		if i, _ := f.find(e.DataElement, e.COC); i >= 0 {
			vs := f.values()
			f["dataValues"] = append(vs[:i:i], vs[i+1:]...)
		}
	case OpValue:
		_, m := f.find(e.DataElement, e.COC)
		if m == nil {
			m = map[string]any{"dataElement": e.DataElement, "period": e.Form.Period, "orgUnit": e.Form.OrgUnit,
				"categoryOptionCombo": e.COC, "attribute": map[string]any{"combo": combo, "options": options},
				"followUp": false, "created": e.At}
			f["dataValues"] = append(f.values(), m)
		}
		if e.Value != nil {
			m["value"] = *e.Value
		}
		if e.Comment != nil {
			m["comment"] = *e.Comment
		}
		if e.FollowUp != nil {
			m["followUp"] = *e.FollowUp
		}
		m["storedBy"], m["lastUpdated"] = e.By, e.At
	}
}

// Request is how the edit is pushed to central, as its author.
func (e *Edit) Request() (method, path string, body []byte) {
	if e.Op == OpComplete {
		req := map[string]any{"dataSet": e.Form.DataSet, "period": e.Form.Period, "orgUnit": e.Form.OrgUnit,
			"completed": e.Completed}
		if e.CC != "" {
			req["attribute"] = map[string]any{"combo": e.CC, "options": e.CP}
		}
		b, _ := json.Marshal(req)
		return "POST", "/api/dataEntry/dataSetCompletion", b
	}
	q := url.Values{"de": {e.DataElement}, "co": {e.COC}, "ds": {e.Form.DataSet}, "ou": {e.Form.OrgUnit}, "pe": {e.Form.Period}}
	if e.CC != "" {
		q.Set("cc", e.CC)
		q.Set("cp", strings.Join(e.CP, ";"))
	}
	if e.Op == OpDelete {
		return "DELETE", "/api/dataValues?" + q.Encode(), nil
	}
	switch {
	case e.Value != nil:
		q.Set("value", *e.Value)
	case e.Comment != nil:
		q.Set("comment", *e.Comment)
	case e.FollowUp != nil:
		q.Set("followUp", map[bool]string{true: "true", false: "false"}[*e.FollowUp])
	}
	return "POST", "/api/dataValues?" + q.Encode(), nil
}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/aggregate -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/aggregate/edit.go internal/aggregate/edit_test.go
git commit -m "aggregate: edits, forms and the requests that push them

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Store: forms and aggregate outbox entries

**Files:**
- Modify: `internal/store/store.go` (append data migration 5; `legacyTables` unchanged)
- Modify: `internal/store/outbox.go` (`Kind` on `OutboxEntry`, scan it)
- Create: `internal/store/aggregate.go`
- Test: `internal/store/aggregate_test.go`

**Interfaces:**
- Produces:
  - `const OutboxTracker = "tracker"; const OutboxAggregate = "aggregate"`; `OutboxEntry.Kind string`
  - `type AggForm struct { Key, DataSet, Period, OrgUnit, AOC string; Body []byte; Reg string; FetchedAt string; Stale bool }`
  - `func (s *Store) PutAggForm(f *AggForm) error`
  - `func (s *Store) GetAggForm(key string) (*AggForm, error)` (nil, nil if none)
  - `func (s *Store) AggForms() ([]*AggForm, error)` (no bodies)
  - `func (s *Store) AnyAggForm(dataSet string) (*AggForm, error)` (latest fetched, with body)
  - `func (s *Store) MarkAggFormStale(key string) error`
  - `func (s *Store) DeleteAggForm(key string) error`
  - `func (s *Store) AppendAggregate(tx *sql.Tx, userID int64, payload []byte, cell, form string, completion bool) (int64, error)`
  - `func (s *Store) PendingAggregate(form string) ([]*OutboxEntry, error)` (seq order, with payloads)
  - `func (s *Store) PendingAggregateAll() ([]*OutboxEntry, error)` (with payloads; dashboard)

- [ ] **Step 1: Write the failing test.**

```go
package store

import (
	"context"
	"database/sql"
	"testing"
)

func aggStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t) // the helper store_test.go uses; if it is named differently, use that name
	return s
}

func TestAggFormsRoundTrip(t *testing.T) {
	s := aggStore(t)
	f := &AggForm{Key: "agg:A|202610|OU|AOC", DataSet: "A", Period: "202610", OrgUnit: "OU", AOC: "AOC",
		Body: []byte(`{"dataValues":[]}`), Reg: "true|nurse|2026-10-15", FetchedAt: Now()}
	if err := s.PutAggForm(f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAggForm(f.Key)
	if err != nil || got == nil || string(got.Body) != string(f.Body) || got.Reg != f.Reg || got.Stale {
		t.Fatalf("get: %+v %v", got, err)
	}
	s.MarkAggFormStale(f.Key)
	all, _ := s.AggForms()
	if len(all) != 1 || !all[0].Stale || all[0].Body != nil {
		t.Fatalf("list: %+v", all)
	}
	if any, _ := s.AnyAggForm("A"); any == nil || string(any.Body) != string(f.Body) {
		t.Fatalf("any form of A: %+v", any)
	}
	s.DeleteAggForm(f.Key)
	if got, _ := s.GetAggForm(f.Key); got != nil {
		t.Fatal("deleted")
	}
}

func TestAggregateCollapse(t *testing.T) {
	s := aggStore(t)
	app := func(user int64, payload, cell string, completion bool) int64 {
		var seq int64
		err := s.Tx(context.Background(), func(tx *sql.Tx) error {
			var err error
			seq, err = s.AppendAggregate(tx, user, []byte(payload), cell, "agg:F", completion)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	a := app(1, `{"v":1}`, "agg:F|de|co|value", false)
	if b := app(1, `{"v":2}`, "agg:F|de|co|value", false); b != a {
		t.Fatalf("same user, same cell and field: replaced (%d, %d)", a, b)
	}
	if c := app(1, `{"c":"x"}`, "agg:F|de|co|comment", false); c == a {
		t.Fatal("a comment does not replace a value edit")
	}
	if x := app(1, `{"v":5}`, "agg:F|de|co|value", false); x == a {
		t.Fatal("a value edit after a comment of the cell is a new entry: the order of the cell's edits holds")
	}
	if d := app(2, `{"v":3}`, "agg:F|de|co|value", false); d == a {
		t.Fatal("another user's edit is its own entry")
	}
	done := app(1, `{"complete":true}`, "agg:F|complete", true)
	if e := app(1, `{"v":4}`, "agg:F|de|co|value", false); e == a || e < done {
		t.Fatal("an edit after a queued completion is not folded into an entry before it")
	}
	es, err := s.PendingAggregate("agg:F")
	if err != nil || len(es) != 6 || string(es[0].Payload) != `{"v":2}` || es[0].Kind != OutboxAggregate {
		t.Fatalf("pending for the form: %d %v", len(es), err)
	}
	if es[4].DependsOnSeq == 0 {
		t.Fatal("a completion depends on the form's earlier entries")
	}
}
```

- [ ] **Step 2: Run it and see it fail.**

Run: `go test ./internal/store -run 'TestAggForms|TestAggregateCollapse'`
Expected: FAIL (undefined `AggForm`).

- [ ] **Step 3: Add the migration** to `dataMigrations` in `store.go` (after the `params` one):

```go
	// Aggregate data entry (docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md):
	// central's answer per form, and the kind of each outbox entry.
	`
CREATE TABLE agg_forms(
  key TEXT PRIMARY KEY,
  data_set TEXT NOT NULL,
  period TEXT NOT NULL,
  org_unit TEXT NOT NULL,
  aoc TEXT NOT NULL,
  body_enc BLOB NOT NULL,
  reg TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL,
  stale INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX agg_forms_ds ON agg_forms(data_set, fetched_at);
ALTER TABLE outbox ADD COLUMN kind TEXT NOT NULL DEFAULT 'tracker';
`,
```

- [ ] **Step 4: `Kind` in `outbox.go`.** Add the constants and field, and read the column:

```go
// Outbox entry kinds.
const (
	OutboxTracker   = "tracker"
	OutboxAggregate = "aggregate"
)
```

In `OutboxEntry` add `Kind string // OutboxTracker or OutboxAggregate`. Change `outboxCols` to end with `, params, kind` and the `rows.Scan` in `scanOutbox` to end with `&e.Params, &e.Kind)`.

- [ ] **Step 5: Implement `aggregate.go`.**

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AggForm is central's dataEntry/dataValues answer for one form (Body, sealed), with
// the completion registration seen when it was fetched (Reg) and whether it must be
// fetched again (Stale).
type AggForm struct {
	Key, DataSet, Period, OrgUnit, AOC string
	Body                               []byte
	Reg                                string
	FetchedAt                          string
	Stale                              bool
}

func (s *Store) PutAggForm(f *AggForm) error {
	ct, err := s.Seal("agg_form:"+f.Key, f.Body)
	if err != nil {
		return err
	}
	_, err = s.write(`INSERT INTO agg_forms(key, data_set, period, org_unit, aoc, body_enc, reg, fetched_at, stale)
 VALUES(?,?,?,?,?,?,?,?,0) ON CONFLICT(key) DO UPDATE SET body_enc=excluded.body_enc, reg=excluded.reg,
 fetched_at=excluded.fetched_at, stale=0`, f.Key, f.DataSet, f.Period, f.OrgUnit, f.AOC, ct, f.Reg, f.FetchedAt)
	return err
}

func (s *Store) scanAggForm(sc interface{ Scan(...any) error }, withBody bool) (*AggForm, error) {
	f := &AggForm{}
	var ct []byte
	if err := sc.Scan(&f.Key, &f.DataSet, &f.Period, &f.OrgUnit, &f.AOC, &ct, &f.Reg, &f.FetchedAt, &f.Stale); err != nil {
		return nil, err
	}
	if withBody {
		b, err := s.OpenBlob("agg_form:"+f.Key, ct)
		if err != nil {
			return nil, err
		}
		f.Body = b
	}
	return f, nil
}

const aggFormCols = `key, data_set, period, org_unit, aoc, body_enc, reg, fetched_at, stale`

// GetAggForm returns a form with its body, or nil.
func (s *Store) GetAggForm(key string) (*AggForm, error) {
	f, err := s.scanAggForm(s.DB().QueryRow(`SELECT `+aggFormCols+` FROM agg_forms WHERE key=?`, key), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

// AnyAggForm is the data set's most recently fetched form (its min-max values serve
// the data set's forms central has nothing for).
func (s *Store) AnyAggForm(dataSet string) (*AggForm, error) {
	f, err := s.scanAggForm(s.DB().QueryRow(`SELECT `+aggFormCols+` FROM agg_forms WHERE data_set=?
 ORDER BY fetched_at DESC LIMIT 1`, dataSet), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

// AggForms lists every form, without bodies.
func (s *Store) AggForms() ([]*AggForm, error) {
	rows, err := s.DB().Query(`SELECT ` + aggFormCols + ` FROM agg_forms ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AggForm
	for rows.Next() {
		f, err := s.scanAggForm(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) MarkAggFormStale(key string) error {
	_, err := s.write(`UPDATE agg_forms SET stale=1 WHERE key=?`, key)
	return err
}

func (s *Store) DeleteAggForm(key string) error {
	_, err := s.write(`DELETE FROM agg_forms WHERE key=?`, key)
	return err
}

// AppendAggregate queues an aggregate edit of a form. cell names what it sets
// (aggregate.Edit.CellKey: the form, data element, option combination and field). An
// edit replaces the same user's pending edit of the same cell and field when that is
// the latest pending entry touching the cell (any field, any user) and no completion
// of the form was queued after it: repeated edits push once, and the order of edits
// to one cell holds (a delete never moves before a later comment). A completion
// depends on the form's pending entries before it, so it is pushed after them.
func (s *Store) AppendAggregate(tx *sql.Tx, userID int64, payload []byte, cell, form string, completion bool) (int64, error) {
	base := cell // the cell without its field
	if i := strings.LastIndex(cell, "|"); i > 0 && !completion {
		base = cell[:i]
	}
	if !completion {
		var seq int64
		err := tx.QueryRow(`SELECT o.seq FROM outbox o JOIN outbox_objects oo ON oo.seq=o.seq
 WHERE o.state=? AND o.kind=? AND o.user_id=? AND oo.uid=?
   AND o.seq > coalesce((SELECT max(c.seq) FROM outbox c JOIN outbox_objects cc ON cc.seq=c.seq
     WHERE c.state=? AND cc.uid=? AND cc.kind='aggregate-completion'), 0)
   AND o.seq >= (SELECT max(b.seq) FROM outbox b JOIN outbox_objects bb ON bb.seq=b.seq WHERE b.state=? AND bb.uid=?)
 ORDER BY o.seq DESC LIMIT 1`, OutboxPending, OutboxAggregate, userID, cell, OutboxPending, form, OutboxPending, base).Scan(&seq)
		if err == nil {
			ct, err := s.Seal(fmt.Sprintf("outbox:%d", seq), payload)
			if err != nil {
				return 0, err
			}
			_, err = tx.Exec(`UPDATE outbox SET payload_enc=?, created_at=? WHERE seq=?`, ct, Now(), seq)
			return seq, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var dep any
	if completion {
		if d := s.LatestPendingSeqFor(tx, []string{form}); d > 0 {
			dep = d
		}
	}
	res, err := tx.Exec(`INSERT INTO outbox(user_id, payload_enc, created_at, state, depends_on_seq, params, kind)
 VALUES(?,?,?,?,?,'',?)`, userID, []byte{}, Now(), OutboxPending, dep, OutboxAggregate)
	if err != nil {
		return 0, err
	}
	seq, _ := res.LastInsertId()
	ct, err := s.Seal(fmt.Sprintf("outbox:%d", seq), payload)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE outbox SET payload_enc=? WHERE seq=?`, ct, seq); err != nil {
		return 0, err
	}
	kind := "aggregate-cell"
	if completion {
		kind = "aggregate-completion"
	}
	refs := []OutboxRef{{UID: cell, Kind: kind}, {UID: form, Kind: kind}}
	if !completion {
		refs = append(refs, OutboxRef{UID: base, Kind: "aggregate-cellbase"})
	}
	for _, r := range refs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO outbox_objects(seq, uid, kind) VALUES(?,?,?)`, seq, r.UID, r.Kind); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

// PendingAggregate returns the form's pending entries in seq order, with payloads.
func (s *Store) PendingAggregate(form string) ([]*OutboxEntry, error) {
	rows, err := s.DB().Query(`SELECT `+outboxCols+` FROM outbox WHERE state=? AND kind=? AND seq IN
 (SELECT seq FROM outbox_objects WHERE uid=?) ORDER BY seq`, OutboxPending, OutboxAggregate, form)
	if err != nil {
		return nil, err
	}
	return s.scanOutbox(rows, true)
}

// PendingAggregateAll returns every pending aggregate entry, with payloads.
func (s *Store) PendingAggregateAll() ([]*OutboxEntry, error) {
	rows, err := s.DB().Query(`SELECT `+outboxCols+` FROM outbox WHERE state=? AND kind=? ORDER BY seq`,
		OutboxPending, OutboxAggregate)
	if err != nil {
		return nil, err
	}
	return s.scanOutbox(rows, true)
}
```

`LatestPendingSeqFor` takes a `QueryRow` interface; `*sql.Tx` satisfies it.

- [ ] **Step 6: Run the store tests (old and new).**

Run: `go test ./internal/store -count=1 && go test -race ./internal/store -count=1`
Expected: PASS. A pre-existing test that counts migrations or lists outbox columns may need the new count: update it to the new number.

- [ ] **Step 7: Commit.**

```bash
git add internal/store/
git commit -m "store: aggregate forms; aggregate outbox entries, edits of a cell folded

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Fake central's aggregate endpoints

**Files:**
- Create: `internal/fakecentral/aggregate.go`
- Modify: `internal/fakecentral/fakecentral.go` (fields in `Central`, `New`, dispatch in `ServeHTTP`)
- Test: `internal/fakecentral/aggregate_test.go`

**Interfaces:**
- Produces (used by Tasks 8–13):
  - constants `AggDataSet = "FakeDsMon01"` (Monthly, default attribute combo), `AggValidDS = "FakeDsVal01"` (`validCompleteOnly`), `AggDonorDS = "FakeDsDon01"` (attribute combo `FakeCcDon01`, options `FakeCoDonA1` shared with nurse_a only, `FakeCoDonB1` with everyone), `AggDeNum = "FakeDeNum01"` (NUMBER), `AggDeInt = "FakeDeInt01"` (INTEGER), `AggDeReq = "FakeDeReq01"` (NUMBER, compulsory in `AggDataSet`), `AggCOC = DefaultCOC = "HllvX50cXC0"` used as both COC and default AOC (as on central), `AggCC = "bjDvmb4bfuf"`, `AggOption = "xYerKDKCefk"`, `AggDonorAOCA = "FakeAocDonA"`, `AggDonorAOCB = "FakeAocDonB"`
  - `func (c *Central) AggValue(pe, de, coc, aoc string) (value, storedBy string, ok bool)`
  - `func (c *Central) SetAggValue(pe, de, coc, aoc, value, storedBy string)`
  - `func (c *Central) Approve(ds, pe string)`
  - `func (c *Central) Complete(ds, pe, aoc string) bool`
  - field `AggExpiryDays float64` (applies to `AggDataSet`)
  - every aggregate user: `hub_admin`, `nurse_a`, `clerk_c` (not `nurse_b`, `outsider_d`)

- [ ] **Step 1: Write the failing test.**

```go
package fakecentral

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFakeAggregate(t *testing.T) {
	c := New()
	srv := httptest.NewServer(c)
	defer srv.Close()
	do := func(user, method, path string, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.SetBasicAuth(user, Password)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	pe := time.Now().Format("200601")
	if st, md := do("nurse_a", "GET", "/api/42/dataEntry/metadata", ""); st != 200 || len(md["dataSets"].([]any)) != 3 {
		t.Fatalf("metadata for nurse_a: %d %v", st, md)
	}
	if st, md := do("nurse_b", "GET", "/api/42/dataEntry/metadata", ""); st != 200 || len(md["dataSets"].([]any)) != 0 {
		t.Fatalf("nurse_b enters no aggregate data: %d %v", st, md)
	}
	q := url.Values{"de": {AggDeNum}, "co": {AggCOC}, "ds": {AggDataSet}, "ou": {Facility}, "pe": {pe}, "value": {"7"}}
	if st, _ := do("nurse_a", "POST", "/api/42/dataValues?"+q.Encode(), ""); st != 201 {
		t.Fatalf("POST value: %d", st)
	}
	if v, by, ok := c.AggValue(pe, AggDeNum, AggCOC, AggCOC); !ok || v != "7" || by != "nurse_a" {
		t.Fatalf("stored: %q %q %v", v, by, ok)
	}
	q.Set("value", "x")
	if st, out := do("nurse_a", "POST", "/api/dataValues?"+q.Encode(), ""); st != 409 || out["errorCode"] != "E2030" {
		t.Fatalf("bad number: %d %v", st, out)
	}
	_, form := do("clerk_c", "GET", "/api/42/dataEntry/dataValues?ds="+AggDataSet+"&pe="+pe+"&ou="+Facility, "")
	if form["lockStatus"] != "OPEN" || len(form["dataValues"].([]any)) != 1 {
		t.Fatalf("form: %v", form)
	}
	_, set := do("clerk_c", "GET", "/api/dataValueSets?dataSet="+AggDataSet+"&orgUnit="+Facility+"&startDate=2000-01-01&endDate=2100-01-01", "")
	if len(set["dataValues"].([]any)) != 1 {
		t.Fatalf("dataValueSets: %v", set)
	}
	if st, _ := do("nurse_a", "POST", "/api/dataEntry/dataSetCompletion",
		`{"dataSet":"`+AggDataSet+`","period":"`+pe+`","orgUnit":"`+Facility+`","completed":true}`); st != 409 {
		t.Fatalf("completion without the compulsory field: %d", st)
	}
	c.Approve(AggDataSet, pe)
	q.Set("value", "8")
	if st, out := do("nurse_a", "POST", "/api/dataValues?"+q.Encode(), ""); st != 409 || out["errorCode"] != "E2017" {
		t.Fatalf("approved: %d %v", st, out)
	}
}
```

- [ ] **Step 2: Run it and see it fail.**

Run: `go test ./internal/fakecentral -run TestFakeAggregate`
Expected: FAIL (undefined `AggDataSet`).

- [ ] **Step 3: Implement `aggregate.go`.**

```go
package fakecentral

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Aggregate metadata: three monthly data sets at the facility.
const (
	AggDataSet   = "FakeDsMon01" // default attribute combo; AggDeReq compulsory for completion
	AggValidDS   = "FakeDsVal01" // validCompleteOnly
	AggDonorDS   = "FakeDsDon01" // attribute combo AggDonorCC
	AggDeNum     = "FakeDeNum01"
	AggDeInt     = "FakeDeInt01"
	AggDeReq     = "FakeDeReq01"
	AggCOC       = DefaultAOC // the default category option combo, also the default AOC
	AggCC        = "bjDvmb4bfuf"
	AggOption    = "xYerKDKCefk"
	AggDonorCC   = "FakeCcDon01"
	AggDonorOptA = "FakeCoDonA1" // shared with nurse_a only
	AggDonorOptB = "FakeCoDonB1"
	AggDonorAOCA = "FakeAocDonA"
	AggDonorAOCB = "FakeAocDonB"
)

type aggValue struct {
	Value, Comment, StoredBy, Created, LastUpdated string
	FollowUp, Deleted                              bool
}

type aggReg struct {
	Completed               bool
	StoredBy, Date, Created string
}

// aggUsers enter aggregate data; aggOptionsOf lists the donor options each may use.
var aggUsers = map[string][]string{"hub_admin": {AggDonorOptA, AggDonorOptB}, "nurse_a": {AggDonorOptA, AggDonorOptB},
	"clerk_c": {AggDonorOptB}}

var versioned = regexp.MustCompile(`^/api/[0-9]{2}/`)

// aggregate serves the aggregate endpoints; ok is false for other paths.
func (c *Central) aggregate(w http.ResponseWriter, r *http.Request, u *User) bool {
	p := r.URL.Path
	if m := versioned.FindString(p); m != "" {
		p = "/api/" + p[len(m):]
	}
	opts, enters := aggUsers[u.Username]
	switch p {
	case "/api/dataEntry/metadata":
		if !enters {
			writeJSON(w, 200, map[string]any{"dataSets": []any{}, "dataElements": []any{}, "categoryCombos": []any{},
				"categories": []any{}, "categoryOptions": []any{}, "optionSets": []any{}, "indicators": []any{}})
			return true
		}
		writeJSON(w, 200, c.aggMetadata(opts))
	case "/api/dataEntry/dataValues":
		q := r.URL.Query()
		aoc, ok := aggAOC(q.Get("ds"), q.Get("cc"), q.Get("cp"))
		if !enters || !ok {
			webMessage(w, 409, "Data set not found or not accessible", "E1102")
			return true
		}
		writeJSON(w, 200, c.aggForm(q.Get("ds"), q.Get("pe"), aoc))
	case "/api/dataValues":
		c.aggWrite(w, r, u, enters, opts)
	case "/api/dataEntry/dataSetCompletion":
		c.aggComplete(w, r, u, enters)
	case "/api/dataValueSets":
		c.aggValueSets(w, r)
	case "/api/completeDataSetRegistrations":
		c.aggRegistrations(w, r)
	default:
		return false
	}
	return true
}

func aggAOC(ds, cc, cp string) (string, bool) {
	if ds == AggDonorDS {
		if cc != AggDonorCC {
			return "", false
		}
		switch cp {
		case AggDonorOptA:
			return AggDonorAOCA, true
		case AggDonorOptB:
			return AggDonorAOCB, true
		}
		return "", false
	}
	return AggCOC, cc == "" || cc == AggCC
}

func (c *Central) aggMetadata(opts []string) map[string]any {
	ref := func(id string) map[string]any { return map[string]any{"id": id} }
	dse := func(ids ...string) []any {
		var out []any
		for _, id := range ids {
			out = append(out, map[string]any{"dataElement": ref(id)})
		}
		return out
	}
	ds := func(id, name, cc string, extra map[string]any) map[string]any {
		m := map[string]any{"id": id, "displayName": name, "periodType": "Monthly", "categoryCombo": ref(cc),
			"expiryDays": 0.0, "openFuturePeriods": 1, "dataInputPeriods": []any{}, "compulsoryDataElementOperands": []any{},
			"dataSetElements": dse(AggDeNum, AggDeInt, AggDeReq), "organisationUnits": []any{Facility}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	c.mu.Lock()
	expiry := c.AggExpiryDays
	c.mu.Unlock()
	donorCOCs := []any{}
	options := []any{ref(AggOption)}
	for _, o := range opts {
		aoc := map[string]string{AggDonorOptA: AggDonorAOCA, AggDonorOptB: AggDonorAOCB}[o]
		donorCOCs = append(donorCOCs, map[string]any{"id": aoc, "displayName": "Donor " + o, "categoryOptions": []string{o}})
		options = append(options, ref(o))
	}
	return map[string]any{
		"dataSets": []any{
			ds(AggDataSet, "Fake monthly", AggCC, map[string]any{"expiryDays": expiry, "compulsoryFieldsCompleteOnly": true,
				"compulsoryDataElementOperands": []any{map[string]any{"dataElement": ref(AggDeReq), "categoryOptionCombo": ref(AggCOC)}}}),
			ds(AggValidDS, "Fake validated", AggCC, map[string]any{"validCompleteOnly": true}),
			ds(AggDonorDS, "Fake donor", AggDonorCC, nil),
		},
		"dataElements": []any{
			map[string]any{"id": AggDeNum, "displayName": "Fake number", "valueType": "NUMBER", "categoryCombo": ref(AggCC)},
			map[string]any{"id": AggDeInt, "displayName": "Fake integer", "valueType": "INTEGER", "categoryCombo": ref(AggCC)},
			map[string]any{"id": AggDeReq, "displayName": "Fake required", "valueType": "NUMBER", "categoryCombo": ref(AggCC)},
		},
		"categoryCombos": []any{
			map[string]any{"id": AggCC, "isDefault": true, "categoryOptionCombos": []any{
				map[string]any{"id": AggCOC, "displayName": "default", "categoryOptions": []string{AggOption}}}},
			map[string]any{"id": AggDonorCC, "isDefault": false, "categoryOptionCombos": donorCOCs},
		},
		"categories": []any{}, "categoryOptions": options, "optionSets": []any{}, "indicators": []any{},
	}
}

func aggKey(pe, de, coc, aoc string) string { return pe + "|" + de + "|" + coc + "|" + aoc }

func (c *Central) aggLocked(ds, pe string) bool {
	if c.approved[ds+"|"+pe] {
		return true
	}
	if ds != AggDataSet || c.AggExpiryDays <= 0 {
		return false
	}
	start, err := time.Parse("200601", pe)
	if err != nil {
		return false
	}
	end := start.AddDate(0, 1, -1)
	return time.Now().After(end.Add(time.Duration(c.AggExpiryDays * float64(24*time.Hour))))
}

func (c *Central) aggForm(ds, pe, aoc string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	combo, options := AggCC, []string{AggOption}
	if ds == AggDonorDS {
		combo, options = AggDonorCC, []string{map[string]string{AggDonorAOCA: AggDonorOptA, AggDonorAOCB: AggDonorOptB}[aoc]}
	}
	vals := []any{}
	keys := make([]string, 0, len(c.aggValues))
	for k := range c.aggValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.Split(k, "|")
		v := c.aggValues[k]
		if parts[0] != pe || parts[3] != aoc || v.Deleted || !aggInDataSet(parts[1]) {
			continue
		}
		m := map[string]any{"dataElement": parts[1], "period": pe, "orgUnit": Facility, "categoryOptionCombo": parts[2],
			"attribute": map[string]any{"combo": combo, "options": options}, "value": v.Value, "followUp": v.FollowUp,
			"storedBy": v.StoredBy, "created": v.Created, "lastUpdated": v.LastUpdated}
		if v.Comment != "" {
			m["comment"] = v.Comment
		}
		vals = append(vals, m)
	}
	lock := "OPEN"
	if c.approved[ds+"|"+pe] {
		lock = "APPROVED"
	} else if c.aggLocked(ds, pe) {
		lock = "LOCKED"
	}
	cs := map[string]any{"complete": false}
	if reg, ok := c.aggRegs[ds+"|"+pe+"|"+aoc]; ok {
		cs = map[string]any{"complete": reg.Completed, "created": reg.Created, "createdBy": reg.StoredBy,
			"lastUpdated": reg.Created, "lastUpdatedBy": reg.StoredBy}
	}
	return map[string]any{"dataValues": vals, "minMaxValues": []any{map[string]any{"dataElement": AggDeNum,
		"orgUnit": Facility, "categoryOptionCombo": AggCOC, "minValue": 0, "maxValue": 1000}}, "lockStatus": lock,
		"completeStatus": cs}
}

func aggInDataSet(de string) bool { return de == AggDeNum || de == AggDeInt || de == AggDeReq }

// params merges the query with a urlencoded or multipart body, as central reads them.
func params(r *http.Request) url.Values {
	q := r.URL.Query()
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "multipart/form-data":
		if r.ParseMultipartForm(1<<20) == nil {
			for k, v := range r.MultipartForm.Value {
				q[k] = v
			}
		}
	case "application/x-www-form-urlencoded":
		if r.ParseForm() == nil {
			for k, v := range r.PostForm {
				q[k] = v
			}
		}
	}
	return q
}

func (c *Central) aggWrite(w http.ResponseWriter, r *http.Request, u *User, enters bool, opts []string) {
	q := params(r)
	ds, pe, de, coc := q.Get("ds"), q.Get("pe"), q.Get("de"), q.Get("co")
	aoc, ok := aggAOC(ds, q.Get("cc"), q.Get("cp"))
	if !enters || !ok || !aggInDataSet(de) || q.Get("ou") != Facility {
		webMessage(w, 409, "Data element not found or not accessible: `"+de+"`", "E1100")
		return
	}
	if ds == AggDonorDS && !strings.Contains(strings.Join(opts, ","), q.Get("cp")) {
		webMessage(w, 409, "User does not have write access for category option combo: `"+aoc+"`", "E2010")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.aggLocked(ds, pe) {
		webMessage(w, 409, "Data set is locked", "E2017")
		return
	}
	k := aggKey(pe, de, coc, aoc)
	v := c.aggValues[k]
	now := c.now()
	if v == nil || v.Deleted {
		v = &aggValue{Created: now}
		c.aggValues[k] = v
	}
	if r.Method == http.MethodDelete {
		v.Deleted, v.LastUpdated = true, now
		w.WriteHeader(204)
		return
	}
	if q.Has("value") {
		typ := map[string]string{AggDeNum: "NUMBER", AggDeInt: "INTEGER", AggDeReq: "NUMBER"}[de]
		val := q.Get("value")
		_, ferr := strconv.ParseFloat(val, 64)
		_, ierr := strconv.Atoi(val)
		if (typ == "NUMBER" && ferr != nil) || (typ == "INTEGER" && ierr != nil) {
			webMessage(w, 409, "Data value must match data element value type: `"+typ+"`", "E2030")
			return
		}
		v.Value = val
	}
	if q.Has("comment") {
		v.Comment = q.Get("comment")
	}
	if q.Has("followUp") {
		v.FollowUp = q.Get("followUp") == "true"
	}
	v.StoredBy, v.LastUpdated, v.Deleted = u.Username, now, false
	w.WriteHeader(201)
}

func (c *Central) aggComplete(w http.ResponseWriter, r *http.Request, u *User, enters bool) {
	var req struct {
		DataSet, Period, OrgUnit string
		Attribute                *struct {
			Combo   string   `json:"combo"`
			Options []string `json:"options"`
		} `json:"attribute"`
		Completed bool `json:"completed"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || !enters {
		webMessage(w, 409, "Bad completion", "")
		return
	}
	cc, cp := "", ""
	if req.Attribute != nil {
		cc, cp = req.Attribute.Combo, strings.Join(req.Attribute.Options, ";")
	}
	aoc, ok := aggAOC(req.DataSet, cc, cp)
	if !ok {
		webMessage(w, 409, "Attribute option combo not found", "")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.Completed && req.DataSet == AggDataSet {
		if v := c.aggValues[aggKey(req.Period, AggDeReq, AggCOC, aoc)]; v == nil || v.Deleted || v.Value == "" {
			webMessage(w, 409, "All compulsory data element operands need to be filled: [Fake required default]", "")
			return
		}
	}
	c.aggRegs[req.DataSet+"|"+req.Period+"|"+aoc] = &aggReg{Completed: req.Completed, StoredBy: u.Username,
		Date: c.now()[:10], Created: c.now()}
	w.WriteHeader(200)
}

func (c *Central) aggValueSets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := q.Get("lastUpdated")
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []any{}
	for k, v := range c.aggValues {
		parts := strings.Split(k, "|")
		if since != "" && v.LastUpdated < since {
			continue
		}
		if v.Deleted && q.Get("includeDeleted") != "true" {
			continue
		}
		m := map[string]any{"dataElement": parts[1], "period": parts[0], "orgUnit": Facility, "categoryOptionCombo": parts[2],
			"attributeOptionCombo": parts[3], "value": v.Value, "storedBy": v.StoredBy, "created": v.Created + "+0000",
			"lastUpdated": v.LastUpdated + "+0000", "followup": v.FollowUp}
		if v.Deleted {
			m["deleted"] = true
		}
		out = append(out, m)
	}
	writeJSON(w, 200, map[string]any{"dataValues": out})
}

func (c *Central) aggRegistrations(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []any{}
	for k, reg := range c.aggRegs {
		parts := strings.Split(k, "|")
		out = append(out, map[string]any{"period": parts[1], "dataSet": parts[0], "organisationUnit": Facility,
			"attributeOptionCombo": parts[2], "date": reg.Date, "storedBy": reg.StoredBy, "completed": reg.Completed})
	}
	writeJSON(w, 200, map[string]any{"completeDataSetRegistrations": out})
}

// AggValue reads a stored value (not deleted).
func (c *Central) AggValue(pe, de, coc, aoc string) (value, storedBy string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.aggValues[aggKey(pe, de, coc, aoc)]
	if v == nil || v.Deleted {
		return "", "", false
	}
	return v.Value, v.StoredBy, true
}

// SetAggValue stores a value as if a user had entered it on central.
func (c *Central) SetAggValue(pe, de, coc, aoc, value, storedBy string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.aggValues[aggKey(pe, de, coc, aoc)] = &aggValue{Value: value, StoredBy: storedBy, Created: now, LastUpdated: now}
}

// Approve approves a data set's period: writes are refused, forms say APPROVED.
func (c *Central) Approve(ds, pe string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.approved[ds+"|"+pe] = true
}

// Complete reports whether the form is registered complete.
func (c *Central) Complete(ds, pe, aoc string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	reg := c.aggRegs[ds+"|"+pe+"|"+aoc]
	return reg != nil && reg.Completed
}
```

In `fakecentral.go`: add to `Central`:

```go
	// Aggregate data (aggregate.go).
	aggValues     map[string]*aggValue
	aggRegs       map[string]*aggReg
	approved      map[string]bool
	AggExpiryDays float64 // expiry days of AggDataSet
```

initialise `aggValues: map[string]*aggValue{}, aggRegs: map[string]*aggReg{}, approved: map[string]bool{}` in `New`, and in `ServeHTTP` right after `u := c.user(r)` and its nil check:

```go
	if c.aggregate(w, r, u) {
		return
	}
```

`aggLocked` is called with `c.mu` held by its callers in `aggForm`/`aggWrite`; it reads `c.approved` and `c.AggExpiryDays` without locking. In `aggMetadata` read `AggExpiryDays` under the lock as shown.

- [ ] **Step 4: Run the fake's tests.**

Run: `go test ./internal/fakecentral -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/fakecentral/
git commit -m "fakecentral: aggregate data entry endpoints

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Per-user aggregate access, retention, refresh

**Files:**
- Create: `internal/core/aggregate.go`
- Modify: `internal/core/refresh.go` (`RefreshAccessWith`, `RefreshCatalog`)
- Modify: `internal/core/core.go` (`aggAccess map[int64]*aggregate.Access` field next to `access`; clear it where `access` is cleared in `Lock`)
- Modify: `internal/config/config.go` (`AggregateRetention int`, key `aggregate_retention_periods`, default 12)
- Test: `internal/live/aggregate_test.go` (new file; first test)

**Interfaces:**
- Consumes: `aggregate.ParseMetadata`, `aggregate.Retention` (Task 3).
- Produces:
  - `func (h *Hub) AggAccess(userID int64) *aggregate.Access` (nil when none)
  - `func (h *Hub) RefreshAggAccess(ctx context.Context, userID int64, auth central.Auth, authorities []string) error`
  - `func (h *Hub) AggRetention() aggregate.Retention`
  - `func (h *Hub) APIMinor() string` (`"42"` for `2.42.6`)
  - `func (h *Hub) AggLabel(cell string) string` (for issues and warnings)
  - testkit: `Manifest.Aggregate *Aggregate` with `type Aggregate struct { DataSet, ValidDataSet, DonorDataSet string; Number, Integer, Required, COC, DefaultAOC, DonorCC, DonorOptionNurse, DonorAOCNurse string }`

- [ ] **Step 1: Manifest fields.** In `internal/testkit/manifest.go` add to `Manifest`:

```go
	// Aggregate: data sets at the facility for the aggregate tests; nil when this
	// setup has none (the tests skip).
	Aggregate *Aggregate `json:"aggregate,omitempty"`
```

and

```go
// Aggregate names what the aggregate tests use. Number and Integer are data elements
// of DataSet (value types NUMBER and INTEGER), Required its compulsory one, COC their
// category option combination and DefaultAOC the default attribute option combination.
// ValidDataSet has validCompleteOnly. DonorDataSet has an attribute combination
// (DonorCC) with an option only the nurse may use (DonorOptionNurse, its AOC
// DonorAOCNurse); the clerk may not.
type Aggregate struct {
	DataSet          string `json:"dataSet"`
	ValidDataSet     string `json:"validDataSet"`
	DonorDataSet     string `json:"donorDataSet,omitempty"`
	Number           string `json:"number"`
	Integer          string `json:"integer"`
	Required         string `json:"required"`
	COC              string `json:"coc"`
	DefaultAOC       string `json:"defaultAoc"`
	DonorCC          string `json:"donorCc,omitempty"`
	DonorOptionNurse string `json:"donorOptionNurse,omitempty"`
	DonorAOCNurse    string `json:"donorAocNurse,omitempty"`
}
```

In `internal/testkit/fake.go`, set it in the fake's manifest:

```go
		Aggregate: &Aggregate{DataSet: fc.AggDataSet, ValidDataSet: fc.AggValidDS, DonorDataSet: fc.AggDonorDS,
			Number: fc.AggDeNum, Integer: fc.AggDeInt, Required: fc.AggDeReq, COC: fc.AggCOC, DefaultAOC: fc.AggCOC,
			DonorCC: fc.AggDonorCC, DonorOptionNurse: fc.AggDonorOptA, DonorAOCNurse: fc.AggDonorAOCA},
```

- [ ] **Step 2: Write the failing test** (`internal/live/aggregate_test.go`):

```go
package live

import (
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// aggregateEnv starts a hub set up with the nurse and clerk logged in, or skips when
// this setup has no aggregate data sets.
func aggregateEnv(t *testing.T) (*tk.Env, *tk.Aggregate, *tk.Client, *tk.Client) {
	t.Helper()
	e := tk.Start(t)
	if e.M.Aggregate == nil {
		t.Skip("this setup has no aggregate data sets")
	}
	e.Setup()
	nurse, clerk := e.Web(tk.RoleNurse), e.Web(tk.RoleClerk)
	nurse.Login()
	clerk.Login()
	e.Sync()
	return e, e.M.Aggregate, nurse, clerk
}

// The access refresh reads each user's dataEntry/metadata: what central lets them
// enter, kept for offline use.
func TestAggregateAccessRefreshed(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	h := e.App.Hub
	u, _ := h.Store.UserByName(nurse.User)
	acc := h.AggAccess(u.ID)
	if acc == nil || acc.DataSets[ag.DataSet] == nil || acc.DataSets[ag.ValidDataSet] == nil {
		t.Fatalf("nurse's aggregate access: %+v", acc)
	}
	e.Offline()
	// The app's own metadata request is answered offline from what the refresh fetched.
	nurse.JSON("GET", "/api/"+h.APIMinor()+"/dataEntry/metadata", nil, 200)
}
```

- [ ] **Step 3: Run it and see it fail.**

Run: `go test ./internal/live -run TestAggregateAccessRefreshed -count=1`
Expected: FAIL (undefined `AggAccess`).

- [ ] **Step 4: Implement `internal/core/aggregate.go`.**

```go
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
)

func aggAccessKey(userID int64) string { return fmt.Sprintf("agg_access:%d", userID) }

// AggAccess is what the user may enter in the Aggregate Data Entry app (their
// dataEntry/metadata at the last access refresh), or nil.
func (h *Hub) AggAccess(userID int64) *aggregate.Access {
	h.mu.RLock()
	a, ok := h.aggAccess[userID]
	h.mu.RUnlock()
	if ok {
		return a
	}
	b, err := h.Store.State(aggAccessKey(userID))
	if err != nil || b == nil {
		return nil
	}
	a = &aggregate.Access{}
	if json.Unmarshal(b, a) != nil {
		return nil
	}
	h.mu.Lock()
	h.aggAccess[userID] = a
	h.mu.Unlock()
	return a
}

// APIMinor is the version path segment the apps use for central's version ("42").
func (h *Hub) APIMinor() string {
	parts := strings.Split(h.ServerVersion(), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// RefreshAggAccess fetches the user's dataEntry/metadata: the data sets they may
// enter. The answer is also cached for the app, which asks for it at start-up, so
// the app opens offline for users who never opened it online through the hub. A
// user without the app's authority gets 403 or an empty list: no aggregate access.
func (h *Hub) RefreshAggAccess(ctx context.Context, userID int64, auth central.Auth, authorities []string) error {
	path := "/api/" + h.APIMinor() + "/dataEntry/metadata"
	resp, err := h.Central.Do(ctx, "GET", path, nil, auth, nil)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	a := &aggregate.Access{}
	if resp.StatusCode == http.StatusOK {
		if a, err = aggregate.ParseMetadata(b, authorities); err != nil {
			return fmt.Errorf("dataEntry/metadata: %w", err)
		}
		hdr := resp.Header.Clone()
		hdr.Del("Set-Cookie")
		if err := h.Store.PutCache(userID, "GET", path, &store.CachedResponse{Status: 200, Header: hdr, Body: b}); err != nil {
			return err
		}
	}
	js, _ := json.Marshal(a)
	if err := h.Store.SetState(aggAccessKey(userID), js); err != nil {
		return err
	}
	h.mu.Lock()
	h.aggAccess[userID] = a
	h.mu.Unlock()
	return nil
}

const aggRetentionKey = "agg_retention"

// AggRetention is the retention table read from central at the last catalog refresh,
// with the configured default.
func (h *Hub) AggRetention() aggregate.Retention {
	r := aggregate.Retention{}
	if b, err := h.Store.State(aggRetentionKey); err == nil && b != nil {
		json.Unmarshal(b, &r)
	}
	if r.Default <= 0 {
		r.Default = h.Cfg.AggregateRetention
	}
	return r
}

// refreshAggRetention reads facility-hub-config/aggregate from central's dataStore;
// a missing key keeps the configured default.
func (h *Hub) refreshAggRetention(ctx context.Context) error {
	var r aggregate.Retention
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/aggregate", nil, h.HubAuth(), &r)
	if central.Status(err) == http.StatusNotFound {
		r, err = aggregate.Retention{}, nil
	}
	if err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return h.Store.SetState(aggRetentionKey, b)
}

// AggLabel names an aggregate outbox cell ("agg:ds|pe|ou|aoc|de|coc|field") for people:
// data set, period, data element and category option combination, from any user's
// access.
func (h *Hub) AggLabel(cell string) string {
	parts := strings.Split(strings.TrimPrefix(cell, "agg:"), "|")
	if len(parts) < 4 {
		return cell
	}
	users, _ := h.Store.Users()
	for _, u := range users {
		a := h.AggAccess(u.ID)
		if a == nil || a.DataSets[parts[0]] == nil {
			continue
		}
		label := a.DataSets[parts[0]].Name + ", " + parts[1]
		if len(parts) >= 6 {
			if e := a.Elements[parts[4]]; e != nil {
				label += ", " + e.Name + " " + a.COCName(parts[5])
			}
		}
		return label
	}
	return cell
}
```

In `core.go`: add `aggAccess map[int64]*aggregate.Access` to `Hub` next to `access`, initialise it in `New` like `access`, and reset it wherever `h.access` is reset (search `h.access =`).

In `refresh.go` `RefreshAccessWith`, after `l, err := h.FetchAccess(ctx, auth)` succeeds and before `return h.SetAccess(userID, l)`:

```go
	if err := h.RefreshAggAccess(ctx, userID, auth, l.Authorities); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		slog.Warn("aggregate access refresh", "user", l.Username, "err", err)
	}
```

In `RefreshCatalog`, at its end before the final `return`, add (keeping its existing return value):

```go
	if err := h.refreshAggRetention(ctx); err != nil && central.IsNetwork(err) {
		return err
	}
```

In `config.go`: field `AggregateRetention int` (comment: "how many periods of each aggregate data set the hub keeps when central's dataStore does not say (facility-hub-config/aggregate)"), default `aggregate.DefaultRetention` (write `12` to avoid an import of `aggregate` from `config`), and the key:

```go
	case "aggregate_retention_periods":
		c.AggregateRetention, err = strconv.Atoi(v)
```

Add `aggregate_retention_periods: 12` with a comment to `hub.example.yaml` next to `reconnect_delay`.

- [ ] **Step 5: Run the test and the existing suites.**

Run: `go test ./internal/live -run TestAggregateAccessRefreshed -count=1 -v && go test ./internal/core ./internal/e2e ./internal/live -count=1`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/core/ internal/config/ hub.example.yaml internal/testkit/ internal/live/aggregate_test.go
git commit -m "Access refresh reads each user's dataEntry/metadata; retention from the dataStore

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

(If `hub.example.yaml` lives elsewhere, `git ls-files | grep example` and add that path.)

---

### Task 9: Pull forms, push edits

**Files:**
- Create: `internal/sync/aggregate.go`
- Modify: `internal/sync/sync.go` (`cycle`: call `w.pullAggregate` after `w.Pull`; `Push`: route aggregate entries)
- Test: `internal/live/aggregate_test.go` (add tests)

**Interfaces:**
- Consumes: Tasks 3–8.
- Produces:
  - `func (w *Worker) pullAggregate(ctx context.Context) error`
  - `func (w *Worker) pushAggregate(ctx context.Context, e *store.OutboxEntry) (pushResult, error)`
  - `func (h *Hub) ApplyPushed(e *aggregate.Edit)` is not needed: `pushAggregate` applies the edit to the stored form itself (helper `applyToStoredForm`).
  - testkit helpers (in `internal/testkit/aggregate.go`, new): `func (c *Client) PostDataValue(path string, fields map[string]string) (int, []byte)` (multipart, as the app sends), `func (e *Env) CentralDataValue(ag *Aggregate, ds, pe, de, aoc string) (value, storedBy string, ok bool)` (reads central's `dataValueSets` as the admin; fake or live), `func (e *Env) SetCentralDataValue(ds, pe, de, coc, value string)` (as the admin), `func CurrentMonth(e *Env) string` (`yyyyMM` in central's time zone)

- [ ] **Step 1: testkit helpers** (`internal/testkit/aggregate.go`):

```go
package testkit

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
)

// PostDataValue posts one cell as the Aggregate Data Entry app does: multipart form
// fields (de, co, ds, ou, pe, cc, cp, and value, comment or followUp).
func (c *Client) PostDataValue(path string, fields map[string]string) (int, []byte) {
	c.e.T.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.e.Srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.basic {
		req.SetBasicAuth(c.User, c.e.M.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.e.T.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// CentralDataValue reads one value from central as the superuser.
func (e *Env) CentralDataValue(ds, pe, de, coc, aoc string) (value, storedBy string, ok bool) {
	e.T.Helper()
	var res struct {
		DataValues []struct {
			DataElement, CategoryOptionCombo, AttributeOptionCombo, Value, StoredBy string
			Deleted                                                                 bool
		} `json:"dataValues"`
	}
	q := url.Values{"dataSet": {ds}, "period": {pe}, "orgUnit": {e.M.Facility.ID}}
	if err := e.Central.JSON("GET", "/api/dataValueSets?"+q.Encode(), nil, &res); err != nil {
		e.T.Fatal(err)
	}
	for _, v := range res.DataValues {
		if v.DataElement == de && v.CategoryOptionCombo == coc && v.AttributeOptionCombo == aoc && !v.Deleted {
			return v.Value, v.StoredBy, true
		}
	}
	return "", "", false
}

// SetCentralDataValue enters a value on central as the superuser (default attribute).
func (e *Env) SetCentralDataValue(ds, pe, de, coc, value string) {
	e.T.Helper()
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {e.M.Facility.ID}, "de": {de}, "co": {coc}, "value": {value}}
	if st, b, err := e.Central.Do("POST", "/api/dataValues?"+q.Encode(), nil); err != nil || st/100 != 2 {
		e.T.Fatalf("set central value: %d %s %v", st, b, err)
	}
}

// CurrentMonth is this month's period ID in central's time zone.
func CurrentMonth(e *Env) string { return e.App.Hub.CentralNow().Format("200601") }
```

`e.Central` must be the superuser client (`Manifest.AdminUser`); check `Start` in `env.go` and use whatever field it builds that client into.

- [ ] **Step 2: Write the failing tests** (append to `internal/live/aggregate_test.go`; add imports `net/url`, `strings`):

```go
func formPath(e *tk.Env, ds, pe string) string {
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {e.M.Facility.ID}}
	return "/api/" + e.App.Hub.APIMinor() + "/dataEntry/dataValues?" + q.Encode()
}

func cell(e *tk.Env, ds, pe, de, coc string, extra ...string) map[string]string {
	f := map[string]string{"ds": ds, "pe": pe, "ou": e.M.Facility.ID, "de": de, "co": coc}
	for i := 0; i+1 < len(extra); i += 2 {
		f[extra[i]] = extra[i+1]
	}
	return f
}

func formValue(form map[string]any, de, coc string) (string, bool) {
	vs, _ := form["dataValues"].([]any)
	for _, v := range vs {
		m := v.(map[string]any)
		if m["dataElement"] == de && m["categoryOptionCombo"] == coc {
			s, ok := m["value"].(string)
			return s, ok
		}
	}
	return "", false
}

// A value on central is served offline in a form this user never opened: the hub
// pulled it. Offline edits are attributed to their authors on central, whoever opens
// the app afterwards (the shared-browser case from the spike).
func TestAggregateOfflineAttribution(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.SetCentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, "11")
	e.Sync()
	e.Offline()
	st, hdr, b := clerk.DoHeader("GET", formPath(e, ag.DataSet, pe))
	if st != 200 || hdr.Get("X-Hub-Mode") != "offline" || !strings.Contains(string(b), `"11"`) {
		t.Fatalf("offline form for the clerk: %d %q %s", st, hdr.Get("X-Hub-Mode"), b)
	}
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "21")); st != 201 {
		t.Fatalf("nurse's offline value: %d %s", st, b)
	}
	form := clerk.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	if got, _ := formValue(form, ag.Number, ag.COC); got != "21" {
		t.Fatalf("the clerk sees the nurse's offline value: %q", got)
	}
	if st, b := clerk.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Integer, ag.COC, "value", "22")); st != 201 {
		t.Fatalf("clerk's offline value: %d %s", st, b)
	}
	e.Online()
	e.Sync()
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "21" || by != nurse.User {
		t.Errorf("number on central: %q by %q, want 21 by %s", val, by, nurse.User)
	}
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, ag.DefaultAOC); val != "22" || by != clerk.User {
		t.Errorf("integer on central: %q by %q, want 22 by %s", val, by, clerk.User)
	}
}

// Review focus 2: two users' offline edits of one cell are both pushed, in order.
func TestAggregateSameCellTwoUsers(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.Offline()
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "1"))
	clerk.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "2"))
	e.Online()
	e.Sync()
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "2" || by != clerk.User {
		t.Fatalf("last write wins: %q by %q", val, by)
	}
}

// Review focus 4: a period locked while edits wait: refused on push, kept as an issue;
// the other form's edit is pushed.
func TestAggregateLockedOnPushKeepsValue(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	e.NeedFake()
	pe := tk.CurrentMonth(e)
	e.Offline()
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "5"))
	nurse.PostDataValue(v, cell(e, ag.ValidDataSet, pe, ag.Number, ag.COC, "value", "6"))
	e.Fake.Approve(ag.DataSet, pe)
	e.Online()
	e.Sync()
	if _, _, ok := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); ok {
		t.Fatal("approved period: central must not have the value")
	}
	if val, _, _ := e.CentralDataValue(ag.ValidDataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "6" {
		t.Fatalf("the other form's value: %q", val)
	}
	issues, _ := e.App.Hub.Store.Issues(false, 10)
	if len(issues) != 1 || issues[0].Kind != "aggregate" || !strings.Contains(string(issues[0].Error), "E2017") ||
		!strings.Contains(string(issues[0].Error), `"value":"5"`) {
		t.Fatalf("issue: %+v", issues)
	}
}

// Review focus 5: a completion waits for the form's earlier entries.
func TestAggregateCompletionWaitsForValues(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	e.NeedFake()
	pe := tk.CurrentMonth(e)
	e.Offline()
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Required, ag.COC, "value", "3"))
	clerk.JSON("POST", "/api/"+e.App.Hub.APIMinor()+"/dataEntry/dataSetCompletion",
		map[string]any{"dataSet": ag.DataSet, "period": pe, "orgUnit": e.M.Facility.ID, "completed": true}, 200)
	u, _ := e.App.Hub.Store.UserByName(nurse.User)
	e.App.Hub.Store.SetNeedsLogin(u.ID, true) // the nurse's value cannot be pushed yet
	e.Online()
	e.Sync()
	if e.Fake.Complete(ag.DataSet, pe, ag.DefaultAOC) {
		t.Fatal("completion pushed before the value it needs")
	}
	e.App.Hub.Store.SetNeedsLogin(u.ID, false)
	e.Sync()
	if !e.Fake.Complete(ag.DataSet, pe, ag.DefaultAOC) {
		t.Fatal("completion not pushed after the value")
	}
}
```

- [ ] **Step 3: Run them and see them fail.**

Run: `go test ./internal/live -run 'TestAggregate(OfflineAttribution|SameCell|LockedOnPush|CompletionWaits)' -count=1`
Expected: FAIL (offline form answered by the GET cache or 503; writes 503). These tests also need Task 10's handler: keep them failing until Task 10, and in this task check the pull and push through the unit test in Step 4.

- [ ] **Step 4: Write a pull/push test that does not need the handler** (append to `internal/live/aggregate_test.go`):

```go
// The pull stores the forms of the window that have data; a pushed edit reaches
// central as its author.
func TestAggregatePullAndPush(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.SetCentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, "4")
	e.Sync()
	h := e.App.Hub
	key := aggregate.FormKey{DataSet: ag.DataSet, Period: pe, OrgUnit: e.M.Facility.ID, AOC: ag.DefaultAOC}.String()
	f, err := h.Store.GetAggForm(key)
	if err != nil || f == nil || !strings.Contains(string(f.Body), `"4"`) {
		t.Fatalf("pulled form: %+v %v", f, err)
	}
	u, _ := h.Store.UserByName(nurse.User)
	v := "9"
	ed := &aggregate.Edit{Op: aggregate.OpValue, Form: aggregate.FormKey{DataSet: ag.DataSet, Period: pe,
		OrgUnit: e.M.Facility.ID, AOC: ag.DefaultAOC}, DataElement: ag.Number, COC: ag.COC, Value: &v, By: nurse.User}
	b, _ := json.Marshal(ed)
	h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := h.Store.AppendAggregate(tx, u.ID, b, ed.CellKey(), ed.Form.String(), false)
		return err
	})
	e.Sync()
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "9" || by != nurse.User {
		t.Fatalf("pushed: %q by %q", val, by)
	}
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("pending after push: %d", n)
	}
}
```

(imports: `context`, `database/sql`, `encoding/json`, `github.com/dhis2/facility-hub/internal/aggregate`, `github.com/dhis2/facility-hub/internal/store`.)

Run: `go test ./internal/live -run TestAggregatePullAndPush -count=1`
Expected: FAIL (form not pulled).

- [ ] **Step 5: Implement `internal/sync/aggregate.go`.**

```go
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
)

// aggFullEvery is how often every held form is fetched again, for lock and min-max
// changes that leave values alone (approval, lock exceptions).
const aggFullEvery = 24 * time.Hour

type aggGroup struct {
	key    string
	reader int64
	auth   central.Auth
	acc    *aggregate.Access
}

// pullAggregate fetches the forms of each data set's window that changed on central
// since the last pull: values (dataValueSets, by lastUpdated) and completions
// (completeDataSetRegistrations). Users whose metadata grants the same data sets and
// options are one group, read as one of them. Forms that left every window are dropped
// unless edits for them wait.
func (w *Worker) pullAggregate(ctx context.Context) error {
	h := w.H
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	groups := map[string]*aggGroup{}
	for _, u := range users {
		if u.NeedsLogin || (!u.HasPAT && !u.HasPassword) || !h.Access(u.ID).InFacility(h.FacilityPath()) {
			continue
		}
		acc := h.AggAccess(u.ID)
		k := acc.Key()
		if k == "" || groups[k] != nil {
			continue
		}
		auth, err := h.UpstreamAuth(u.ID)
		if err != nil {
			continue
		}
		groups[k] = &aggGroup{key: k, reader: u.ID, auth: auth, acc: acc}
	}
	now, ret, facility := h.CentralNow(), h.AggRetention(), h.Facility()
	held := map[string]bool{}
	fetched := map[string]bool{}
	for _, g := range groups {
		if err := w.pullAggGroup(ctx, g, now, ret, facility, held, fetched); err != nil {
			if central.IsNetwork(err) {
				return err
			}
			slog.Warn("aggregate pull", "err", err)
		}
	}
	forms, err := h.Store.AggForms()
	if err != nil {
		return err
	}
	for _, f := range forms {
		if !held[f.Key] && !h.Store.HasPendingOutboxFor(f.Key, 0) {
			h.Store.DeleteAggForm(f.Key)
		}
	}
	return nil
}

func (w *Worker) pullAggGroup(ctx context.Context, g *aggGroup, now time.Time, ret aggregate.Retention, facility string,
	held, fetched map[string]bool) error {
	h := w.H
	windows := map[string][]aggregate.Period{}
	var start, end time.Time
	for id, ds := range g.acc.DataSets {
		if !ds.Assigned(facility) {
			continue
		}
		win := aggregate.Window(ds, now, ret.For(id))
		if len(win) == 0 {
			continue
		}
		windows[id] = win
		if start.IsZero() || win[0].Start.Before(start) {
			start = win[0].Start
		}
		if last := win[len(win)-1].End; last.After(end) {
			end = last
		}
	}
	if len(windows) == 0 {
		return nil
	}
	stateKey := "agg_pull:" + g.key
	since := h.Store.Private(stateKey)
	full := since == "" || time.Since(store.ParseTime(h.Store.Private(stateKey+":full"))) > aggFullEvery
	cycleStart := time.Now()

	byPeriod := func(ds, pe string) bool {
		for _, p := range windows[ds] {
			if p.ID == pe {
				return true
			}
		}
		return false
	}
	changed := map[aggregate.FormKey]string{} // form -> registration fingerprint ("" if none)
	mark := func(k aggregate.FormKey) {
		if _, ok := changed[k]; !ok {
			changed[k] = ""
		}
	}
	dsIDs := make([]string, 0, len(windows))
	for id := range windows {
		dsIDs = append(dsIDs, id)
	}
	q := url.Values{"dataSet": dsIDs, "orgUnit": {facility}, "startDate": {start.Format(time.DateOnly)},
		"endDate": {end.Format(time.DateOnly)}, "includeDeleted": {"true"}}
	if !full {
		q.Set("lastUpdated", h.CentralTime(store.ParseTime(since)))
	}
	var vs struct {
		DataValues []struct {
			DataElement          string `json:"dataElement"`
			Period               string `json:"period"`
			AttributeOptionCombo string `json:"attributeOptionCombo"`
		} `json:"dataValues"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/dataValueSets?"+q.Encode(), nil, g.auth, &vs); err != nil {
		return err
	}
	for _, v := range vs.DataValues {
		for id, ds := range g.acc.DataSets {
			if _, in := ds.Elements[v.DataElement]; in && byPeriod(id, v.Period) {
				mark(aggregate.FormKey{DataSet: id, Period: v.Period, OrgUnit: facility, AOC: v.AttributeOptionCombo})
			}
		}
	}
	q.Del("lastUpdated")
	q.Del("includeDeleted")
	var regs struct {
		Registrations []struct {
			Period               string `json:"period"`
			DataSet              string `json:"dataSet"`
			AttributeOptionCombo string `json:"attributeOptionCombo"`
			Date                 string `json:"date"`
			StoredBy             string `json:"storedBy"`
			Completed            bool   `json:"completed"`
		} `json:"completeDataSetRegistrations"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/completeDataSetRegistrations?"+q.Encode(), nil, g.auth, &regs); err != nil {
		return err
	}
	regOf := map[aggregate.FormKey]string{}
	for _, r := range regs.Registrations {
		if !byPeriod(r.DataSet, r.Period) {
			continue
		}
		k := aggregate.FormKey{DataSet: r.DataSet, Period: r.Period, OrgUnit: facility, AOC: r.AttributeOptionCombo}
		regOf[k] = fmt.Sprintf("%v|%s|%s", r.Completed, r.StoredBy, r.Date)
	}
	forms, err := h.Store.AggForms()
	if err != nil {
		return err
	}
	stored := map[string]*store.AggForm{}
	for _, f := range forms {
		stored[f.Key] = f
	}
	for k, reg := range regOf {
		if f := stored[k.String()]; f == nil || f.Reg != reg {
			mark(k)
		}
	}
	for _, f := range forms {
		k := aggregate.FormKey{DataSet: f.DataSet, Period: f.Period, OrgUnit: f.OrgUnit, AOC: f.AOC}
		if windows[f.DataSet] == nil || !byPeriod(f.DataSet, f.Period) {
			continue
		}
		held[f.Key] = true
		if f.Stale || full {
			mark(k)
		}
	}
	if full { // each data set's current form, for its min-max values
		for id, win := range windows {
			ds := g.acc.DataSets[id]
			cur, _ := aggregate.Current(ds.PeriodType, now)
			if aoc, ok := g.acc.AOC(ds, "", nil); ok && byPeriod(id, cur.ID) && win != nil {
				mark(aggregate.FormKey{DataSet: id, Period: cur.ID, OrgUnit: facility, AOC: aoc})
			}
		}
	}
	for k := range changed {
		ds := g.acc.DataSets[k.DataSet]
		if ds == nil || !g.acc.MayUse(ds, k.AOC) {
			continue
		}
		held[k.String()] = true
		if fetched[k.String()] {
			continue
		}
		if err := w.fetchForm(ctx, g, ds, k, regOf[k]); err != nil {
			return err
		}
		fetched[k.String()] = true
	}
	h.Store.SetPrivate(stateKey, cycleStart.UTC().Format(time.RFC3339Nano))
	if full {
		h.Store.SetPrivate(stateKey+":full", store.Now())
	}
	return nil
}

// fetchForm stores central's dataEntry/dataValues answer for a form. A form central
// refuses to show (409: locked, or its "could not extract ResultSet" for expired
// periods nobody used) is stored as locked and empty.
func (w *Worker) fetchForm(ctx context.Context, g *aggGroup, ds *aggregate.DataSet, k aggregate.FormKey, reg string) error {
	h := w.H
	q := url.Values{"ds": {k.DataSet}, "pe": {k.Period}, "ou": {k.OrgUnit}}
	if combo, opts, ok := g.acc.Attribute(ds, k.AOC); ok && !g.acc.Combos[ds.CategoryCombo].Default {
		q.Set("cc", combo)
		q.Set("cp", strings.Join(opts, ";"))
	}
	resp, err := h.Central.Do(ctx, "GET", "/api/"+h.APIMinor()+"/dataEntry/dataValues?"+q.Encode(), nil, g.auth, nil)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusConflict:
		f := aggregate.EmptyForm()
		f["lockStatus"] = aggregate.Locked
		b, _ = json.Marshal(f)
	case resp.StatusCode != http.StatusOK:
		return &central.HTTPError{Status: resp.StatusCode, Body: b}
	}
	return h.Store.PutAggForm(&store.AggForm{Key: k.String(), DataSet: k.DataSet, Period: k.Period, OrgUnit: k.OrgUnit,
		AOC: k.AOC, Body: b, Reg: reg, FetchedAt: store.Now()})
}

// pushAggregate sends one aggregate edit as its author. Central's refusal (a 4xx)
// makes it an issue that keeps the edit; the stored form shows the edit until the
// next pull fetches the form again.
func (w *Worker) pushAggregate(ctx context.Context, e *store.OutboxEntry) (pushResult, error) {
	h := w.H
	var ed aggregate.Edit
	if err := json.Unmarshal(e.Payload, &ed); err != nil {
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, "corrupt payload: "+err.Error())
	}
	auth, err := h.UpstreamAuth(e.UserID)
	if err != nil {
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{userBlocked: true}, nil
	}
	method, path, body := ed.Request()
	var hdr http.Header
	if body != nil {
		hdr = http.Header{"Content-Type": {"application/json"}}
	}
	resp, err := h.Central.Do(ctx, method, path, body, auth, hdr)
	if err != nil {
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{stop: true}, err
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, "401 from central: needs online login")
		return pushResult{userBlocked: true}, nil
	case resp.StatusCode >= 500:
		h.Store.NoteAttempt(e.Seq, fmt.Sprintf("central returned %d", resp.StatusCode))
		return pushResult{stop: true}, nil
	case resp.StatusCode/100 == 2:
		w.applyToStoredForm(&ed)
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxSynced, "")
	}
	var msg struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	json.Unmarshal(rb, &msg)
	if msg.Message == "" {
		msg.Message = fmt.Sprintf("central returned %d", resp.StatusCode)
	}
	issue, _ := json.Marshal(map[string]any{"message": msg.Message,
		"errorReports": []map[string]string{{"errorCode": msg.ErrorCode, "message": msg.Message}}, "edit": ed})
	if err := h.Store.AddIssue(e.Seq, ed.CellKey(), store.OutboxAggregate, e.UserID, issue); err != nil {
		slog.Error("add issue", "err", err)
	}
	slog.Warn("aggregate edit refused", "seq", e.Seq, "cell", ed.CellKey(), "code", msg.ErrorCode)
	h.Store.MarkAggFormStale(ed.Form.String())
	return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, msg.Message)
}

// applyToStoredForm puts a pushed edit into the stored form, which no longer
// overlays it once the entry left the outbox, and marks the form for fetching.
func (w *Worker) applyToStoredForm(ed *aggregate.Edit) {
	h := w.H
	f, err := h.Store.GetAggForm(ed.Form.String())
	if err != nil || f == nil {
		return
	}
	var form aggregate.Form
	if json.Unmarshal(f.Body, &form) != nil {
		return
	}
	combo, opts := "", []string(nil)
	if ed.CC != "" {
		combo, opts = ed.CC, ed.CP
	}
	form.Apply(ed, combo, opts)
	f.Body, _ = json.Marshal(form)
	h.Store.PutAggForm(f)
	h.Store.MarkAggFormStale(f.Key)
}
```

The issue's `"edit"` holds the value (`"value":"5"`): aggregate values are not patient data, and the dashboard needs them to re-enter a refused value. `ReportIssues` sends only `message` and `errorReports` to central, as for tracker.

In `sync.go`:

- In `Push`, replace `res, err := w.pushEntry(ctx, e)` with:

```go
		var res pushResult
		if e.Kind == store.OutboxAggregate {
			res, err = w.pushAggregate(ctx, e)
		} else {
			res, err = w.pushEntry(ctx, e)
		}
```

(and declare `err` accordingly: the loop already has `err` from `PendingOutbox`; use `var err error` inside the loop if the shadowing differs.)

- In `cycle`, after the `w.Pull(ctx)` block:

```go
	if err := w.pullAggregate(ctx); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		errs = append(errs, fmt.Errorf("aggregate pull: %w", err))
	}
```

- `StuckOutbox`, `OutboxWaiting` and `PassThrough` count aggregate entries already (they read the whole outbox).

- [ ] **Step 6: Run the pull/push test.**

Run: `go test ./internal/live -run TestAggregatePullAndPush -count=1 -v`
Expected: PASS.

- [ ] **Step 7: Run everything.**

Run: `go vet ./... && go test ./... -count=1`
Expected: PASS, except the four tests of Step 2, which need Task 10. Mark them with `t.Skip("needs the aggregate handler (Task 10)")` as their first line now, and remove the skips in Task 10.

- [ ] **Step 8: Commit.**

```bash
git add internal/sync/ internal/testkit/aggregate.go internal/live/aggregate_test.go
git commit -m "Sync: pull aggregate forms per access group; push aggregate edits as their authors

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Answering the app offline

**Files:**
- Create: `internal/aggregateapi/aggregateapi.go`
- Modify: `internal/app/app.go` (routing, versioned paths)
- Test: `internal/live/aggregate_test.go` (remove the Task 9 skips; add tests)

**Interfaces:**
- Consumes: Tasks 3–9; `core.Hub.PassThrough`, `proxy.Proxy.Forward/Localize/Write`, `auth.Auth.Identify`.
- Produces:
  - `type API struct { H *core.Hub; Auth *auth.Auth; Proxy *proxy.Proxy }`, `func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request)`
  - `func IsAggregatePath(p string) bool` (unversioned paths)

- [ ] **Step 1: Write the failing tests** (append):

```go
// Review focus 1: value, comment and delete of one cell, offline: each is kept.
func TestAggregateCollapseKeepsFields(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	e.Offline()
	for _, val := range []string{"1", "2", "3"} {
		nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", val))
	}
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "comment", "checked twice"))
	if n := e.App.Hub.Store.CountOutbox(store.OutboxPending); n != 2 {
		t.Fatalf("three value edits and a comment: %d outbox entries, want 2", n)
	}
	q := url.Values{"de": {ag.Number}, "co": {ag.COC}, "ds": {ag.DataSet}, "ou": {e.M.Facility.ID}, "pe": {pe}}
	if st, b := nurse.Do("DELETE", v+"?"+q.Encode(), nil); st != 204 {
		t.Fatalf("offline delete: %d %s", st, b)
	}
	if n := e.App.Hub.Store.CountOutbox(store.OutboxPending); n != 3 {
		t.Fatalf("the delete comes after the comment: %d entries, want 3", n)
	}
	form := nurse.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	if _, ok := formValue(form, ag.Number, ag.COC); ok {
		t.Fatal("deleted value still shown")
	}
	e.Online()
	e.Sync()
	if _, _, ok := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); ok {
		t.Fatal("value on central after the delete")
	}
}

// Writes central would refuse are refused offline with central's codes; a period
// outside the window is locked.
func TestAggregateOfflineRefusals(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	e.Offline()
	if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Integer, ag.COC, "value", "1.5")); st != 409 ||
		!strings.Contains(string(b), "E2030") {
		t.Fatalf("1.5 for an integer: %d %s", st, b)
	}
	old := e.App.Hub.CentralNow().AddDate(-3, 0, 0).Format("200601")
	form := nurse.JSON("GET", formPath(e, ag.DataSet, old), nil, 200)
	if form["lockStatus"] != "LOCKED" || len(form["dataValues"].([]any)) != 0 {
		t.Fatalf("a period outside the window: %v", form)
	}
	if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, old, ag.Number, ag.COC, "value", "1")); st != 409 ||
		!strings.Contains(string(b), "E2017") {
		t.Fatalf("value outside the window: %d %s", st, b)
	}
	if st, _ := nurse.Do("POST", "/api/"+e.App.Hub.APIMinor()+"/dataValues/file", nil); st != 409 {
		t.Fatalf("file value offline: %d, want 409 (never 503: the app would queue it in the browser)", st)
	}
}

// Review focus 3: an attribute option combination the user may not use is neither
// served nor written.
func TestAggregateAttributeAccess(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	if ag.DonorDataSet == "" {
		t.Skip("no data set with an attribute combination in this setup")
	}
	pe := tk.CurrentMonth(e)
	e.Offline()
	q := url.Values{"ds": {ag.DonorDataSet}, "pe": {pe}, "ou": {e.M.Facility.ID}, "cc": {ag.DonorCC}, "cp": {ag.DonorOptionNurse}}
	path := "/api/" + e.App.Hub.APIMinor() + "/dataEntry/dataValues?" + q.Encode()
	nurse.JSON("GET", path, nil, 200)
	if st, _ := clerk.Do("GET", path, nil); st != 409 {
		t.Fatalf("the clerk may not use the nurse's donor option: %d", st)
	}
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	if st, _ := clerk.PostDataValue(v, cell(e, ag.DonorDataSet, pe, ag.Number, ag.COC, "cc", ag.DonorCC, "cp",
		ag.DonorOptionNurse, "value", "1")); st != 409 {
		t.Fatalf("clerk writes with the nurse's option: %d", st)
	}
}

// Online with an empty outbox, the app talks to central.
func TestAggregatePassesThroughOnline(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	st, hdr, _ := nurse.DoHeader("GET", formPath(e, ag.DataSet, tk.CurrentMonth(e)))
	if st != 200 || hdr.Get("X-Hub-Mode") != "" {
		t.Fatalf("online form: %d %q", st, hdr.Get("X-Hub-Mode"))
	}
}
```

Remove the `t.Skip("needs the aggregate handler (Task 10)")` lines from Task 9's tests.

- [ ] **Step 2: Run them and see them fail.**

Run: `go test ./internal/live -run 'TestAggregate' -count=1`
Expected: FAIL (offline answers from the GET cache / 503).

- [ ] **Step 3: Implement `internal/aggregateapi/aggregateapi.go`.**

```go
// Package aggregateapi serves the Aggregate Data Entry app's data endpoints: forms
// (dataEntry/dataValues), cell edits (dataValues), completion
// (dataEntry/dataSetCompletion), and refuses file values and min-max edits offline.
// Online with an empty outbox it passes them to central; otherwise it answers from
// the store and queues edits as their authors (core.PassThrough).
package aggregateapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/proxy"
)

type API struct {
	H     *core.Hub
	Auth  *auth.Auth
	Proxy *proxy.Proxy
}

// IsAggregatePath reports whether p (unversioned) is one of the endpoints served here.
func IsAggregatePath(p string) bool {
	switch p {
	case "/api/dataEntry/dataValues", "/api/dataValues", "/api/dataValues/file", "/api/dataEntry/dataSetCompletion",
		"/api/dataEntry/minMaxValues":
		return true
	}
	return false
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sess, f := a.Auth.Identify(r)
	if f != nil {
		auth.WriteFailure(w, f)
		return
	}
	body, err := core.ReadBody(r, core.MaxBody)
	if err != nil {
		a.H.WebError(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable", "")
		return
	}
	if sess == nil || a.H.PassThrough() {
		var up central.Auth
		if sess != nil {
			up = a.Proxy.Upstream(sess)
		}
		res, err := a.Proxy.Forward(r.Context(), r, body, up)
		if err == nil {
			proxy.Write(w, a.Proxy.Localize(r, res))
			return
		}
		if !central.IsNetwork(err) {
			a.H.WebError(w, http.StatusBadGateway, err.Error(), "")
			return
		}
		if sess == nil {
			a.H.WebError(w, http.StatusUnauthorized, "Unauthorized", "")
			return
		}
	}
	core.MarkOffline(w)
	acc := a.H.AggAccess(sess.UserID)
	if sess.UserID == 0 || acc == nil || !a.H.Access(sess.UserID).InFacility(a.H.FacilityPath()) {
		a.H.WebError(w, http.StatusConflict, "needs connection: this user has no aggregate data at this hub", "")
		return
	}
	p := r.URL.Path
	switch {
	case p == "/api/dataEntry/dataValues" && r.Method == http.MethodGet:
		a.form(w, r.URL.Query(), acc)
	case p == "/api/dataValues" && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		a.edit(w, r, body, sess, acc)
	case p == "/api/dataEntry/dataSetCompletion" && r.Method == http.MethodPost:
		a.complete(w, body, sess, acc)
	default:
		// File values, min-max edits: refused with a message the app shows. Never a
		// 503: the app would queue the request in the browser.
		a.H.WebError(w, http.StatusConflict, "needs connection: "+r.Method+" "+p+" is not available offline", "")
	}
}

// formOf finds the data set, window period and attribute option combination a
// request is about, or writes central's refusal.
func (a *API) formOf(w http.ResponseWriter, acc *aggregate.Access, ds, pe, ou, cc string, cp []string) (
	*aggregate.DataSet, aggregate.FormKey, aggregate.Period, bool, bool) {
	d := acc.DataSets[ds]
	if d == nil || ou != a.H.Facility() || !d.Assigned(ou) {
		a.H.WebError(w, http.StatusConflict, "Data set not found or not accessible: `"+ds+"`", "E1100")
		return nil, aggregate.FormKey{}, aggregate.Period{}, false, false
	}
	aoc, ok := acc.AOC(d, cc, cp)
	if !ok || !acc.MayUse(d, aoc) {
		a.H.WebError(w, http.StatusConflict, "User does not have write access for attribute option combo", "E2010")
		return nil, aggregate.FormKey{}, aggregate.Period{}, false, false
	}
	k := aggregate.FormKey{DataSet: ds, Period: pe, OrgUnit: ou, AOC: aoc}
	per, inWindow := aggregate.InWindow(d, a.H.CentralNow(), a.H.AggRetention().For(ds), pe)
	return d, k, per, inWindow, true
}

func splitCP(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ";")
}

// current is the form as the user sees it: central's answer (or an empty form), with
// every pending edit on top, and the lock for this user now.
func (a *API) current(acc *aggregate.Access, d *aggregate.DataSet, k aggregate.FormKey, per aggregate.Period) (aggregate.Form, error) {
	form := aggregate.EmptyForm()
	centralLock := ""
	if f, err := a.H.Store.GetAggForm(k.String()); err != nil {
		return nil, err
	} else if f != nil {
		if err := json.Unmarshal(f.Body, &form); err != nil {
			return nil, err
		}
		centralLock, _ = form["lockStatus"].(string)
	} else if any, err := a.H.Store.AnyAggForm(k.DataSet); err == nil && any != nil {
		var other aggregate.Form
		if json.Unmarshal(any.Body, &other) == nil {
			form["minMaxValues"] = other["minMaxValues"]
		}
	}
	combo, opts, _ := acc.Attribute(d, k.AOC)
	pending, err := a.H.Store.PendingAggregate(k.String())
	if err != nil {
		return nil, err
	}
	for _, e := range pending {
		var ed aggregate.Edit
		if json.Unmarshal(e.Payload, &ed) == nil {
			form.Apply(&ed, combo, opts)
		}
	}
	form["lockStatus"] = aggregate.LockStatus(d, per, a.H.CentralNow(), acc.Bypass, centralLock)
	return form, nil
}

func (a *API) form(w http.ResponseWriter, q url.Values, acc *aggregate.Access) {
	d, k, per, inWindow, ok := a.formOf(w, acc, q.Get("ds"), q.Get("pe"), q.Get("ou"), q.Get("cc"), splitCP(q.Get("cp")))
	if !ok {
		return
	}
	if !inWindow {
		f := aggregate.EmptyForm()
		f["lockStatus"] = aggregate.Locked
		core.WriteJSON(w, http.StatusOK, f)
		return
	}
	form, err := a.current(acc, d, k, per)
	if err != nil {
		a.H.WebError(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	core.WriteJSON(w, http.StatusOK, form)
}

// params merges the query with a multipart or urlencoded body, as central reads them.
func params(r *http.Request, body []byte) url.Values {
	q := r.URL.Query()
	ct, ps, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "multipart/form-data":
		if f, err := multipart.NewReader(bytes.NewReader(body), ps["boundary"]).ReadForm(1 << 20); err == nil {
			for k, v := range f.Value {
				q[k] = v
			}
		}
	case "application/x-www-form-urlencoded":
		if f, err := url.ParseQuery(string(body)); err == nil {
			for k, v := range f {
				q[k] = v
			}
		}
	}
	return q
}

func (a *API) edit(w http.ResponseWriter, r *http.Request, body []byte, sess *core.Session, acc *aggregate.Access) {
	q := params(r, body)
	d, k, per, inWindow, ok := a.formOf(w, acc, q.Get("ds"), q.Get("pe"), q.Get("ou"), q.Get("cc"), splitCP(q.Get("cp")))
	if !ok {
		return
	}
	de, coc := q.Get("de"), q.Get("co")
	if _, in := d.Elements[de]; !in {
		a.H.WebError(w, http.StatusConflict, "Data element not found or not accessible: `"+de+"`", "E1100")
		return
	}
	if !inWindow || a.lock(acc, d, k, per) != aggregate.Open {
		a.H.WebError(w, http.StatusConflict, "Data set is locked", "E2017")
		return
	}
	ed := &aggregate.Edit{Op: aggregate.OpValue, Form: k, DataElement: de, COC: coc, By: sess.Username,
		At: a.H.CentralTime(a.H.CentralNow())}
	if q.Get("cc") != "" {
		ed.CC, ed.CP = q.Get("cc"), splitCP(q.Get("cp"))
	}
	switch {
	case r.Method == http.MethodDelete:
		ed.Op = aggregate.OpDelete
	case q.Has("value"):
		v := q.Get("value")
		if e := acc.Elements[de]; e != nil && v != "" && !aggregate.ValidValue(e.ValueType, v) {
			a.H.WebError(w, http.StatusConflict, "Data value must match data element value type: `"+e.ValueType+"`", "E2030")
			return
		}
		ed.Value = &v
	case q.Has("comment"):
		c := q.Get("comment")
		ed.Comment = &c
	case q.Has("followUp"):
		f := q.Get("followUp") == "true"
		ed.FollowUp = &f
	default:
		a.H.WebError(w, http.StatusConflict, "value, comment or followUp is required", "")
		return
	}
	if !a.queue(w, sess, ed, false) {
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// lock is the form's lock for the user now (central's answer, if fetched, then
// expiry and input periods).
func (a *API) lock(acc *aggregate.Access, d *aggregate.DataSet, k aggregate.FormKey, per aggregate.Period) string {
	centralLock := ""
	if f, _ := a.H.Store.GetAggForm(k.String()); f != nil {
		var form aggregate.Form
		if json.Unmarshal(f.Body, &form) == nil {
			centralLock, _ = form["lockStatus"].(string)
		}
	}
	return aggregate.LockStatus(d, per, a.H.CentralNow(), acc.Bypass, centralLock)
}

// queue appends the edit to the outbox as the session's user.
func (a *API) queue(w http.ResponseWriter, sess *core.Session, ed *aggregate.Edit, completion bool) bool {
	if !a.H.BeginLocalWrite() {
		a.H.UpgradingError(w)
		return false
	}
	defer a.H.EndLocalWrite()
	b, _ := json.Marshal(ed)
	err := a.H.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := a.H.Store.AppendAggregate(tx, sess.UserID, b, ed.CellKey(), ed.Form.String(), completion)
		return err
	})
	if err != nil {
		slog.Error("queue aggregate edit", "err", err)
		a.H.WebError(w, http.StatusInternalServerError, "could not save the edit", "")
		return false
	}
	return true
}
```

Completion goes in Task 11; until then make `complete` a stub that refuses:

```go
func (a *API) complete(w http.ResponseWriter, body []byte, sess *core.Session, acc *aggregate.Access) {
	a.H.WebError(w, http.StatusConflict, "Completing needs a connection", "")
}
```

`core.Session` has `Username`; if the field is named differently, use it (see `internal/core/sessions.go`, `type Session`). `h.UpgradingError` exists in `core.go`.

- [ ] **Step 4: Route it** in `app.go`: build it next to `tr`:

```go
	ag := &aggregateapi.API{H: h, Auth: a, Proxy: px}
```

extend the versioned-path rewrite condition with `|| aggregateapi.IsAggregatePath(canon)`, and add a case before `default:`:

```go
		case aggregateapi.IsAggregatePath(p):
			ag.ServeHTTP(w, r)
```

- [ ] **Step 5: Run the aggregate tests and everything.**

Run: `go test ./internal/live -run TestAggregate -count=1 -v && go vet ./... && go test ./... -count=1 && go test -race ./internal/live ./internal/e2e ./internal/store ./internal/tracker -count=1`
Expected: PASS. `TestAggregateCompletionWaitsForValues` still needs Task 11: give it `t.Skip("completion: Task 11")` until then.

- [ ] **Step 6: Commit.**

```bash
git add internal/aggregateapi/ internal/app/app.go internal/live/aggregate_test.go
git commit -m "Aggregate Data Entry offline: forms from the store, edits queued as their authors

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Completion offline

**Files:**
- Modify: `internal/aggregateapi/aggregateapi.go` (`complete`)
- Test: `internal/live/aggregate_test.go`

**Interfaces:**
- Consumes: Task 10's `formOf`, `current`, `queue`, `lock`.

- [ ] **Step 1: Write the failing test** (and remove the Task 10 skip from `TestAggregateCompletionWaitsForValues`):

```go
// Completion offline: queued for an ordinary data set; refused where validation is
// mandatory (central does not check it, so a queued completion would bypass it) or
// a compulsory field is empty.
func TestAggregateCompletionOffline(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	path := "/api/" + e.App.Hub.APIMinor() + "/dataEntry/dataSetCompletion"
	req := func(ds string) map[string]any {
		return map[string]any{"dataSet": ds, "period": pe, "orgUnit": e.M.Facility.ID, "completed": true}
	}
	e.Offline()
	if st, b := nurse.Do("POST", path, req(ag.ValidDataSet)); st != 409 || !strings.Contains(string(b), "Completing needs a connection") {
		t.Fatalf("validCompleteOnly offline: %d %s", st, b)
	}
	if st, b := nurse.Do("POST", path, req(ag.DataSet)); st != 409 || !strings.Contains(string(b), "compulsory") {
		t.Fatalf("compulsory field empty: %d %s", st, b)
	}
	nurse.PostDataValue("/api/"+e.App.Hub.APIMinor()+"/dataValues", cell(e, ag.DataSet, pe, ag.Required, ag.COC, "value", "1"))
	nurse.JSON("POST", path, req(ag.DataSet), 200)
	form := nurse.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	if cs := form["completeStatus"].(map[string]any); cs["complete"] != true {
		t.Fatalf("form after offline completion: %v", cs)
	}
	e.Online()
	e.Sync()
	var regs struct {
		Registrations []struct {
			Period    string `json:"period"`
			Completed bool   `json:"completed"`
		} `json:"completeDataSetRegistrations"`
	}
	q := url.Values{"dataSet": {ag.DataSet}, "orgUnit": {e.M.Facility.ID}, "period": {pe}}
	e.Central.JSON("GET", "/api/completeDataSetRegistrations?"+q.Encode(), nil, &regs)
	if len(regs.Registrations) != 1 || !regs.Registrations[0].Completed {
		t.Fatalf("on central: %+v", regs)
	}
}
```

- [ ] **Step 2: Run it and see it fail.**

Run: `go test ./internal/live -run TestAggregateCompletion -count=1`
Expected: FAIL (every completion refused by the stub).

- [ ] **Step 3: Implement `complete`.**

```go
// Completing offline: refused where the data set must pass validation first
// (validCompleteOnly: the hub does not evaluate validation rules, and central does
// not check them on completion) and where a compulsory field is empty (central
// refuses that too); otherwise queued after the form's earlier edits.
func (a *API) complete(w http.ResponseWriter, body []byte, sess *core.Session, acc *aggregate.Access) {
	var req struct {
		DataSet   string `json:"dataSet"`
		Period    string `json:"period"`
		OrgUnit   string `json:"orgUnit"`
		Attribute *struct {
			Combo   string   `json:"combo"`
			Options []string `json:"options"`
		} `json:"attribute"`
		Completed bool `json:"completed"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		a.H.WebError(w, http.StatusBadRequest, "invalid completion: "+err.Error(), "")
		return
	}
	cc, cp := "", []string(nil)
	if req.Attribute != nil {
		cc, cp = req.Attribute.Combo, req.Attribute.Options
	}
	d, k, per, inWindow, ok := a.formOf(w, acc, req.DataSet, req.Period, req.OrgUnit, cc, cp)
	if !ok {
		return
	}
	if !inWindow || a.lock(acc, d, k, per) != aggregate.Open {
		a.H.WebError(w, http.StatusConflict, "Data set is locked", "E2017")
		return
	}
	if req.Completed && d.ValidCompleteOnly {
		a.H.WebError(w, http.StatusConflict, "Completing needs a connection: this data set must pass validation first", "")
		return
	}
	if req.Completed && d.CompulsoryFieldsCompleteOnly {
		form, err := a.current(acc, d, k, per)
		if err != nil {
			a.H.WebError(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
		var missing []string
		for _, op := range d.Compulsory {
			if v, ok := form.Value(op.DataElement, op.COC); !ok || v == "" {
				name := op.DataElement
				if e := acc.Elements[op.DataElement]; e != nil {
					name = e.Name
				}
				missing = append(missing, name+" "+acc.COCName(op.COC))
			}
		}
		if len(missing) > 0 {
			a.H.WebError(w, http.StatusConflict, "All compulsory data element operands need to be filled: ["+
				strings.Join(missing, ", ")+"]", "")
			return
		}
	}
	ed := &aggregate.Edit{Op: aggregate.OpComplete, Form: k, CC: cc, CP: cp, Completed: req.Completed, By: sess.Username,
		At: a.H.CentralTime(a.H.CentralNow())}
	if a.queue(w, sess, ed, true) {
		w.WriteHeader(http.StatusOK)
	}
}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/live -run TestAggregate -count=1 -v`
Expected: PASS, including `TestAggregateCompletionWaitsForValues`.

- [ ] **Step 5: Commit.**

```bash
git add internal/aggregateapi/ internal/live/aggregate_test.go
git commit -m "Completion offline: queued, except where validation is mandatory or a compulsory field is empty

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Dashboard: aggregate issues and expiry warnings

**Files:**
- Modify: `internal/ui/ui.go` (`handleAdmin`), `internal/ui/templates/admin.html`
- Create: `internal/core/aggwarn.go`
- Test: `internal/live/aggregate_test.go`

**Interfaces:**
- Produces: `type AggWarning struct { Label string; Pending int; LocksAt time.Time }`, `func (h *Hub) AggWarnings(within time.Duration) []AggWarning`

- [ ] **Step 1: Write the failing test.**

```go
// Pending edits whose period locks soon are listed on the dashboard.
func TestAggregateExpiryWarning(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	e.NeedFake()
	// Last month locks within a day: its last day plus whole days just past now.
	now := e.App.Hub.CentralNow()
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	naiveNow := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), 0, 0, time.UTC)
	e.Fake.AggExpiryDays = float64(int(naiveNow.Sub(last).Hours()/24) + 1)
	pe := last.Format("200601")
	nurse.Login() // the access refresh at login reads the new expiry
	e.Sync()
	e.Offline()
	if st, b := nurse.PostDataValue("/api/"+e.App.Hub.APIMinor()+"/dataValues",
		cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "1")); st != 201 {
		t.Fatalf("value for last month: %d %s", st, b)
	}
	ws := e.App.Hub.AggWarnings(3 * 24 * time.Hour)
	if len(ws) != 1 || ws[0].Pending != 1 || !strings.Contains(ws[0].Label, pe) {
		t.Fatalf("warnings: %+v", ws)
	}
}
```

Also check the admin page shows it: find how an existing test fetches `/hub/ui` as the admin (search `"/hub/ui"` in `internal/e2e` and `internal/live`), do the same here, and assert the page contains `Locks soon`.

- [ ] **Step 2: Run it and see it fail.**

Run: `go test ./internal/live -run TestAggregateExpiryWarning -count=1`
Expected: FAIL (undefined `AggWarnings`).

- [ ] **Step 3: Implement `internal/core/aggwarn.go`.**

```go
package core

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
)

// AggWarning: edits of a form wait while its period locks (expiry days) soon.
type AggWarning struct {
	Label   string
	Pending int
	LocksAt time.Time
}

// AggWarnings lists forms with pending edits whose period locks within the duration.
func (h *Hub) AggWarnings(within time.Duration) []AggWarning {
	es, err := h.Store.PendingAggregateAll()
	if err != nil {
		return nil
	}
	now := h.CentralNow()
	byForm := map[string]*AggWarning{}
	for _, e := range es {
		var ed aggregate.Edit
		if json.Unmarshal(e.Payload, &ed) != nil {
			continue
		}
		acc := h.AggAccess(e.UserID)
		if acc == nil || acc.DataSets[ed.Form.DataSet] == nil {
			continue
		}
		ds := acc.DataSets[ed.Form.DataSet]
		per, ok := aggregate.InWindow(ds, now, h.AggRetention().For(ds.ID), ed.Form.Period)
		if !ok {
			continue
		}
		at, ok := aggregate.LocksAt(ds, per)
		naiveNow := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), 0, time.UTC)
		if !ok || at.Sub(naiveNow) > within {
			continue
		}
		w := byForm[ed.Form.String()]
		if w == nil {
			w = &AggWarning{Label: ds.Name + ", " + per.ID, LocksAt: at}
			byForm[ed.Form.String()] = w
		}
		w.Pending++
	}
	out := make([]AggWarning, 0, len(byForm))
	for _, w := range byForm {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocksAt.Before(out[j].LocksAt) })
	return out
}
```

In `ui.go` `handleAdmin`: for issues of kind `store.OutboxAggregate`, label the object with `h.AggLabel(is.ObjectUID)` and show the refused value: extend `issueRow` with `Object string`, set `Object: is.ObjectUID` by default and, for aggregate issues,

```go
		if is.Kind == store.OutboxAggregate {
			var x struct {
				Edit struct {
					Value *string `json:"value"`
				} `json:"edit"`
			}
			json.Unmarshal(is.Error, &x)
			row.Object = h.AggLabel(is.ObjectUID)
			if x.Edit.Value != nil {
				row.Object += ": " + *x.Edit.Value
			}
		}
```

and pass `"AggWarnings": h.AggWarnings(3 * 24 * time.Hour)` to the template. In `admin.html`, replace `{{.ObjectUID}}` with `{{.Object}}` in the issues table, and add before the issues card:

```html
{{if .AggWarnings}}<div class="card"><h2>Locks soon: aggregate data not yet sent</h2><table>
<tr><th>Form</th><th>Edits waiting</th><th>Locks at</th></tr>
{{range .AggWarnings}}<tr><td>{{.Label}}</td><td>{{.Pending}}</td><td>{{.LocksAt.Format "2006-01-02 15:04"}}</td></tr>{{end}}
</table><p class="muted">Connect the hub before then, or ask for a lock exception on central.</p></div>{{end}}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/live ./internal/ui ./internal/e2e -count=1`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/core/aggwarn.go internal/ui/ internal/live/aggregate_test.go
git commit -m "Dashboard: refused aggregate values with their labels; warnings before a period locks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Profiles on real DHIS2, and the read oracle

**Files:**
- Modify: `internal/testkit/provision/access.go` (role authorities)
- Modify: `internal/testkit/provision/synthetic.go` (aggregate data elements, data sets, manifest)
- Modify: `internal/testkit/provision/seeded.go` (Sierra Leone: Child Health; manifest)
- Test: `internal/live/aggregate_test.go` (oracle)

**Interfaces:**
- Consumes: `Manifest.Aggregate` (Task 8).

- [ ] **Step 1: Authorities.** In `access.go` where the role is built (`"authorities": []string{"M_dhis-web-capture", ...}`), add `"M_dhis-web-aggregate-data-entry", "F_DATAVALUE_ADD", "F_DATAVALUE_DELETE"`.

- [ ] **Step 2: Synthetic data sets.** In `synthetic.go`, add constants next to the others:

```go
	// Aggregate (docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md).
	deAggNum = "SynDeAggNm1"
	deAggInt = "SynDeAggIn1"
	deAggReq = "SynDeAggRq1"
	dsMonth  = "SynDsMonth1" // compulsory field
	dsValid  = "SynDsValid1" // validCompleteOnly
	dsDonor  = "SynDsDonor1" // attribute combination ccDonor (category options shared per user)
```

and in `syntheticMetadata` append to `dataElements` (keep the existing tracker ones):

```go
	aggDE := func(id, name, vt string) map[string]any {
		return map[string]any{"id": id, "name": name, "shortName": name, "valueType": vt, "domainType": "AGGREGATE",
			"aggregationType": "SUM", "sharing": meta}
	}
	dataElements = append(dataElements, aggDE(deAggNum, "Syn agg number", "NUMBER"),
		aggDE(deAggInt, "Syn agg integer", "INTEGER"), aggDE(deAggReq, "Syn agg required", "NUMBER"))
	dataWrite := map[string]any{"public": "rwrw----", "external": false}
	aggDS := func(id, name string, extra map[string]any) map[string]any {
		m := map[string]any{"id": id, "name": name, "shortName": name, "periodType": "Monthly", "openFuturePeriods": 1,
			"dataSetElements": []any{
				map[string]any{"dataElement": ref(deAggNum), "dataSet": ref(id)},
				map[string]any{"dataElement": ref(deAggInt), "dataSet": ref(id)},
				map[string]any{"dataElement": ref(deAggReq), "dataSet": ref(id)}},
			"organisationUnits": refs(ouFac, ouSib), "sharing": dataWrite}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	dataSets := []any{
		aggDS(dsMonth, "Syn monthly", map[string]any{"compulsoryFieldsCompleteOnly": true,
			"compulsoryDataElementOperands": []any{map[string]any{"dataElement": ref(deAggReq)}}}),
		aggDS(dsValid, "Syn validated", map[string]any{"validCompleteOnly": true}),
		aggDS(dsDonor, "Syn donor", map[string]any{"categoryCombo": ref(ccDonor)}),
	}
```

and add `"dataSets": dataSets,` to `metadata`. (`meta` is the metadata-only sharing the file already uses for categories; `ref`/`refs` are its helpers.) The compulsory operand needs the default category option combination: after import, read it (`GET /api/categoryOptionCombos?filter=name:eq:default&fields=id`) in the provisioner and PATCH it into `dsMonth`'s `compulsoryDataElementOperands`, or import the operand with `"categoryOptionCombo": ref(<default COC id>)` when the provisioner already knows that ID (search the file for `HllvX50cXC0` or `default` COC lookups and reuse them).

Fill the manifest after provisioning (the default COC and AOC are the default category option combination; the donor AOCs are central's, read them):

```go
	m.Aggregate = &testkit.Aggregate{DataSet: dsMonth, ValidDataSet: dsValid, DonorDataSet: dsDonor,
		Number: deAggNum, Integer: deAggInt, Required: deAggReq, COC: defaultCOC, DefaultAOC: defaultCOC,
		DonorCC: ccDonor, DonorOptionNurse: coDonorA, DonorAOCNurse: donorAOCA}
```

where `defaultCOC` is the default category option combination's ID and `donorAOCA` the combination of `coDonorA` (`GET /api/categoryOptionCombos?filter=categoryOptions.id:eq:<coDonorA>&filter=categoryCombo.id:eq:<ccDonor>&fields=id`). Category option `coDonorA` is shared with the nurse role and not the clerk (check `sh(coDonorA)` in this file; if it is shared differently, choose the option shared with the nurse but not the clerk, and name it in `DonorOptionNurse`).

- [ ] **Step 3: Sierra Leone.** In `seeded.go`, after users are imported and only when the facility is Ngelehun CHC's data sets include Child Health (`BfMAe6Itzgt` assigned to the facility): set Child Health's sharing to public data write with `addSharing` (or a `POST /api/sharing?type=dataSet&id=BfMAe6Itzgt` keeping its user group accesses), and set:

```go
	m.Aggregate = &testkit.Aggregate{DataSet: "BfMAe6Itzgt", ValidDataSet: "", Number: "fClA2Erf6IO",
		Integer: "s46m5MS0hxu", Required: "O05mAByOgAv", COC: "V6L425pT3A0", DefaultAOC: "HllvX50cXC0"}
```

Child Health has no `validCompleteOnly` and no compulsory operand on a fresh seed: tests needing `ValidDataSet` skip when it is empty (add `if ag.ValidDataSet == "" { t.Skip(...) }` to `TestAggregateCompletionOffline`'s validation step and `TestAggregateLockedOnPushKeepsValue` uses the fake only). Set `compulsoryFieldsCompleteOnly` and the operand `O05mAByOgAv.V6L425pT3A0` on Child Health in the provisioner, so `Required` means what it says.

- [ ] **Step 4: The read oracle** (append to `aggregate_test.go`):

```go
// The hub's offline forms match central's, for each aggregate user and each form of
// the window that has values: values (with comment, follow-up, stored by), lock and
// completion.
func TestAggregateOracle(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.SetCentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, "17")
	e.Sync()
	type cellState struct{ Value, Comment, StoredBy string; FollowUp bool }
	norm := func(form map[string]any) (map[string]cellState, string, bool) {
		out := map[string]cellState{}
		for _, v := range form["dataValues"].([]any) {
			m := v.(map[string]any)
			c, _ := m["comment"].(string)
			f, _ := m["followUp"].(bool)
			out[m["dataElement"].(string)+"."+m["categoryOptionCombo"].(string)] = cellState{m["value"].(string), c,
				m["storedBy"].(string), f}
		}
		cs, _ := form["completeStatus"].(map[string]any)
		done, _ := cs["complete"].(bool)
		lock, _ := form["lockStatus"].(string)
		return out, lock, done
	}
	online := map[string]map[string]any{}
	for _, c := range []*tk.Client{nurse, clerk} {
		online[c.User] = c.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	}
	e.Offline()
	for _, c := range []*tk.Client{nurse, clerk} {
		off := c.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
		cv, cl, cd := norm(online[c.User])
		hv, hl, hd := norm(off)
		for k, v := range cv {
			if hv[k] != v {
				t.Errorf("%s %s: MISSING or DIFFERS: central %+v, hub %+v", c.User, k, v, hv[k])
			}
		}
		for k, v := range hv {
			if _, ok := cv[k]; !ok {
				t.Errorf("%s %s: LEAK: hub shows %+v, central nothing", c.User, k, v)
			}
		}
		if cl != hl || cd != hd {
			t.Errorf("%s: lock %s/%s, complete %v/%v (central/hub)", c.User, cl, hl, cd, hd)
		}
	}
}
```

- [ ] **Step 5: Run against the fake, then live.**

Run: `go test ./internal/live -run TestAggregate -count=1`
Expected: PASS.

Run on 2.42 and 2.43 (docs/TESTING.md; each creates or resets an instance):

```bash
go run ./cmd/hubtest run -profile synthetic -version 42 -tests 'TestAggregate'
go run ./cmd/hubtest run -profile sierra-leone -version 42 -tests 'TestAggregate'
go run ./cmd/hubtest run -profile synthetic -version 43 -tests 'TestAggregate'
```

(check `hubtest run -h` for the flag that selects tests; if there is none, run the whole suite.) Expected: PASS. A failure on live is a finding: fix the hub (or record a known gap in FINDINGS with the reason) before continuing.

- [ ] **Step 6: Commit.**

```bash
git add internal/testkit/provision/ internal/live/aggregate_test.go
git commit -m "Aggregate on real DHIS2: synthetic data sets, Sierra Leone's Child Health, read oracle

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: The real app (Playwright), and how it shows refusals

**Files:**
- Create: `clients/aggregate_data_entry.py`
- Create: `internal/live/aggregate_client_test.go`

**Interfaces:**
- Consumes: the spike driver (scratchpad `agg/ade.py`) as a starting point; `TestERegisters` for the harness pattern.

- [ ] **Step 1: The driver.** Write `clients/aggregate_data_entry.py` from the spike's `ade.py` (scratchpad), keeping: one shared persistent browser profile; cells found by row label and column (`//tr[td[normalize-space()='<name>']]/td[@data-test='dhis2-dataentryapp-dataentrycell'][<n>]//input`); the app's frame found by URL (`aggregate-data-entry/index.html`), since 2.42+ wraps it in the global shell; login by `POST /api/auth/login`. Inputs from the environment: `HUB_URL`, `CONTROL_URL` (`/offline`, `/online`, `/sync`), `USER_A`, `USER_B`, `HUB_PASSWORD`, `DATASET`, `ORGUNIT`, `PERIOD`, `ROW_A`, `ROW_B` (row labels), `COLUMN` (1-based column of the cell), `OUT`. Steps:
  1. A online: open the form, enter `ROW_A` = 31; close the browser.
  2. Offline; A reopens, logs in, enters `ROW_B` = 32, clicks "Mark complete" (validation offline is skipped by the app): record the request's status and the text the app shows (screenshot `complete-offline.png`, and the bottom bar's text).
  3. B (same browser profile) opens the form offline: record both cells' values.
  4. Online, sync (`/sync`); B opens the app and waits 20 s.
  5. Print JSON: steps, every write's user, phase, status and body, and the texts shown after a refused completion.

- [ ] **Step 2: The Go harness** `internal/live/aggregate_client_test.go`, after `TestERegisters`: skip unless `HUB_CLIENTS=1` and `e.M.Aggregate != nil`; start the hub (`aggregateEnv`), serve the control endpoints (`/offline`, `/online`, `/sync` calling `e.SetUplink` and `e.Sync()`), run `python3 -I ../../clients/aggregate_data_entry.py` with the env above (row labels from the profile: add `NumberLabel`, `IntegerLabel` and `Column` to `tk.Aggregate` and fill them in the provisioners: Sierra Leone "Penta1 doses given", "BCG doses given", column 3; synthetic "Syn agg number", "Syn agg integer", column 1), then assert on central with `e.CentralDataValue`: `ROW_A` by `USER_A` = 31 and `ROW_B` by `USER_A` = 32 (not `USER_B`), and that every offline write got 201 from the hub (no 503).

- [ ] **Step 3: Run it against Sierra Leone 2.42** (`agent-agg-42`, provisioned by `hubtest provision -profile sierra-leone`):

```bash
HUB_CLIENTS=1 HUB_MANIFEST=<manifest> go test ./internal/live -run TestAggregateDataEntryApp -count=1 -v -timeout 40m
```

Expected: PASS. Look at `complete-offline.png`: on Child Health (no `validCompleteOnly`) the completion is queued; then set `validCompleteOnly` on Child Health (`PATCH /api/dataSets/BfMAe6Itzgt` as `local_admin`), rerun, and look at how the app shows the 409 "Completing needs a connection" (spec open question). Record what the app shows in FINDINGS (Task 15). If the app hides the message or gets stuck, note it as a finding with the screenshot; changing the hub's answer is a follow-up for the user to decide.

- [ ] **Step 4: Close every browser** (`pgrep -a chrom` shows none left), then commit.

```bash
git add clients/aggregate_data_entry.py internal/live/aggregate_client_test.go internal/testkit/ internal/testkit/provision/
git commit -m "TestAggregateDataEntryApp: the Aggregate Data Entry app through the hub, offline, one shared browser

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 15: Size, and the docs

**Files:**
- Modify: `internal/fakecentral/generate.go` (aggregate values at scale)
- Create: `internal/perf/aggregate_test.go`
- Modify: `docs/FINDINGS.md`, `docs/TESTING.md`, `docs/PERFORMANCE.md`, `README.md`, `docs/SPEC-facility-hub-poc.md`, `AGENTS.md`, the spec (status)

- [ ] **Step 1: Generator.** In `generate.go`, add `func (c *Central) GenerateAggregate(periods, cells int)`: for each of the last `periods` months and `cells` (data element × COC pairs, using `AggDeNum`, `AggDeInt`, `AggDeReq` and synthetic COC IDs `fmt.Sprintf("FakeCoc%04d", i)`), store a value by `nurse_a` with `SetAggValue`. HC IV size: 26 data sets × 12 periods × ~150 cells ≈ 47 000 values; the fake has 3 data sets, so use `periods=12, cells=1300` (≈ 47 000 values, mostly in `AggDataSet`). Add those COCs to `aggMetadata`'s default combo is not needed: the pull keys forms by data set and period; the form answer filters by `aggInDataSet(de)` only.

- [ ] **Step 2: The perf test** (`internal/perf/aggregate_test.go`): start with `tk.Start`, `e.NeedFake()`, `e.Fake.GenerateAggregate(12, 1300)`, `e.Setup()`, log in the nurse, time `e.Sync()` (first pull), time a second `e.Sync()` (incremental), go offline, time 12 form GETs and one offline POST; log the numbers with `t.Logf` and write them to `HUB_PERF_OUT` like `TestFullDownload`. No pass/fail threshold; the numbers go to PERFORMANCE.md.

Run: `go test ./internal/perf -run TestAggregate -count=1 -v`
Expected: PASS with a table of timings.

- [ ] **Step 3: Docs.**
  - FINDINGS.md: a section "Aggregate Data Entry through the hub": the spike's findings (spec "What the spike found"), what the hub now does, how the app showed a refused completion (Task 14), central's 409 "could not extract ResultSet" bug, known gaps (lock exceptions on expired periods are online-only; period types and calendars not held; forms outside the window answered locked).
  - TESTING.md: the aggregate tests, the manifest's `aggregate` block, `TestAggregateDataEntryApp`.
  - PERFORMANCE.md: Task 15's numbers; requests per cycle per access group (one `dataValueSets`, one `completeDataSetRegistrations`, changed forms; a daily full pass).
  - README.md: documents table unchanged; config key `aggregate_retention_periods`; the dataStore key `facility-hub-config/aggregate`.
  - SPEC: in "Decisions already made" and "Non-goals", replace "No aggregate data" with a pointer to the aggregate spec.
  - AGENTS.md: under "Caches", nothing new (forms are stored data, not a cache); under the guarded parts, add "Aggregate forms (`internal/sync/aggregate.go`, `internal/aggregateapi`): `TestAggregateOracle` and the aggregate scenarios".
  - The spec: status line "implemented 2026-10-xx (plan docs/superpowers/plans/2026-10-15-aggregate-data-entry.md)" and the answers to its open questions as found.

- [ ] **Step 4: Full check.**

Run: `go vet ./... && go test ./... -count=1 && go test -race ./internal/e2e ./internal/live ./internal/tracker ./internal/store ./internal/aggregate ./internal/aggregateapi -count=1`
Expected: PASS. Then the live suites on 2.42 (synthetic, sierra-leone, laos) and 2.43 (synthetic): tracker results unchanged from PERFORMANCE.md's latest numbers.

- [ ] **Step 5: Commit.**

```bash
git add internal/fakecentral/generate.go internal/perf/aggregate_test.go docs/ README.md AGENTS.md
git commit -m "Aggregate data entry: size at HC IV scale; docs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
