// Package live runs the hub's scenarios against the central in HUB_MANIFEST (a real
// DHIS2 provisioned by cmd/hubtest), or against the fake when it is unset
// (docs/TESTING.md). Tests use roles and manifest programs, never fixed UIDs.
package live

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

func uids(list any, key string) []string {
	var out []string
	arr, _ := list.([]any)
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok {
			if s, ok := m[key].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func username(o map[string]any, key string) string {
	m, _ := o[key].(map[string]any)
	s, _ := m["username"].(string)
	return s
}

// TestScenarios follows one hub through the acceptance scenarios (spec 13.3).
func TestScenarios(t *testing.T) {
	e := tk.Start(t)
	m := e.M
	p := m.Primary()
	search := func(name string) string {
		q := url.Values{"program": {p.ID}, "orgUnits": {m.Facility.ID}, "fields": {"*"}}
		if p.SearchAttr != "" {
			q.Set("filter", p.SearchAttr+":like:"+name)
		}
		return "/api/tracker/trackedEntities?" + q.Encode()
	}
	// Names unique to this run, so a live instance can be reused and grepped.
	runTag := "Zq" + strings.ToLower(tk.UID()[:6])

	t.Run("setup: the hub serves the facility of the staff member who sets it up", func(t *testing.T) {
		e.Setup()
		if e.App.Hub.Facility() != m.Facility.ID {
			t.Fatalf("facility %q", e.App.Hub.Facility())
		}
	})

	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	logins := map[string]*tk.Client{tk.RoleNurse: nurse}
	for _, role := range []string{tk.RoleRestricted, tk.RoleClerk, tk.RoleOutsider, tk.RoleSearchless} {
		if _, ok := m.User(role); ok {
			c := e.Web(role)
			c.Login()
			logins[role] = c
		}
	}
	e.Sync()

	t.Run("programs: every program the nurse sees at the facility is cached", func(t *testing.T) {
		if e.Live() {
			var res struct {
				Programs []struct {
					ID string `json:"id"`
				} `json:"programs"`
			}
			c := e.Central.As(nurse.User, m.Password)
			if err := c.JSON("GET", "/api/programs?paging=false&fields=id&filter=programType:eq:WITH_REGISTRATION&filter=organisationUnits.id:eq:"+m.Facility.ID, nil, &res); err != nil {
				t.Fatal(err)
			}
			cat := e.App.Hub.Catalog()
			for _, pr := range res.Programs {
				if cat.Programs[pr.ID] == nil {
					t.Errorf("program %s visible to the nurse is not cached", pr.ID)
				}
			}
		}
	})

	var online *tk.Person
	t.Run("online: a write passes through, attributed to its author", func(t *testing.T) {
		online = m.NewPerson(p, runTag+"online", p.Stages[0])
		nurse.Complete(p, online)
		nurse.JSON("POST", tk.SyncImport, online.Payload, 200)
		o := e.CentralObject("trackedEntities", online.TE)
		if o == nil {
			t.Fatal("not on central")
		}
		if got := username(o, "createdBy"); got != nurse.User {
			t.Fatalf("createdBy %q", got)
		}
	})

	var offline *tk.Person
	t.Run("offline: a registration is visible to colleagues by their access", func(t *testing.T) {
		e.Offline()
		offline = m.NewPerson(p, runTag+"offline", p.Stages...)
		nurse.Complete(p, offline) // from the hub's pool
		nurse.JSON("POST", tk.SyncImport, offline.Payload, 200)
		got := uids(nurse.JSON("GET", search(runTag+"offline"), nil, 200)["trackedEntities"], "trackedEntity")
		if !slices.Contains(got, offline.TE) {
			t.Fatalf("author does not find it: %v", got)
		}
		if r := logins[tk.RoleRestricted]; r != nil && p.RestrictedStage != "" {
			// The person's own events: a busy facility has more than a page of them.
			res := r.JSON("GET", "/api/tracker/events?program="+p.ID+"&orgUnit="+m.Facility.ID+"&trackedEntity="+offline.TE+"&paging=false", nil, 200)
			evs := uids(res["events"], "event")
			for i, st := range p.Stages {
				want := st != p.RestrictedStage
				if slices.Contains(evs, offline.Events[i]) != want {
					t.Errorf("restricted user, stage %s: visible=%v, want %v", st, !want, want)
				}
			}
		}
		if c := logins[tk.RoleClerk]; c != nil {
			if got := uids(c.JSON("GET", search(runTag+"offline"), nil, 200)["trackedEntities"], "trackedEntity"); !slices.Contains(got, offline.TE) {
				t.Errorf("clerk does not see it: %v", got)
			}
			ev, payload := m.NewEvent(p, p.Stages[0], offline.Enrollment)
			if st, b := c.Do("POST", tk.SyncImport, payload); st == 200 {
				t.Errorf("clerk wrote event %s offline: %s", ev, b)
			}
		}
		if o := logins[tk.RoleOutsider]; o != nil {
			if st, _ := o.Do("GET", search(runTag+"offline"), nil); st != 403 {
				t.Errorf("outsider: %d, want 403", st)
			}
		}
		if s := logins[tk.RoleSearchless]; s != nil {
			if st, b := s.Do("GET", search(runTag+"offline"), nil); st == 200 && bytes.Contains(b, []byte(offline.TE)) {
				t.Error("a user without search scope at the facility sees a local record")
			}
		}
	})

	var dup *tk.Person
	t.Run("offline: a unique value taken on central meanwhile", func(t *testing.T) {
		p := p // the first program with a unique attribute
		for _, q := range m.Programs {
			if p.UniqueAttr == "" && q.UniqueAttr != "" {
				p = q
			}
		}
		if p.UniqueAttr == "" {
			t.Skip("no program has a unique attribute")
		}
		value := "T-" + tk.UID()
		if e.Fake != nil {
			e.Fake.Seed(tk.UID(), tk.UID(), "Other", value, nil)
		} else {
			other := m.NewPerson(p, runTag+"other")
			e.CentralComplete(p, other)
			other.SetAttr(p.UniqueAttr, value)
			e.CentralImport(other.Payload)
		}
		dup = m.NewPerson(p, runTag+"dup", p.Stages[0])
		nurse.Complete(p, dup)
		dup.SetAttr(p.UniqueAttr, value)
		nurse.JSON("POST", tk.SyncImport, dup.Payload, 200) // the hub cannot know yet
	})

	t.Run("reconnect: queued data reaches central, by author, rejections reported", func(t *testing.T) {
		e.Online()
		e.Sync()
		o := e.CentralObject("trackedEntities", offline.TE)
		if o == nil {
			t.Fatal("offline registration not on central")
		}
		if got := username(o, "createdBy"); got != nurse.User {
			t.Errorf("createdBy %q", got)
		}
		for _, ev := range offline.Events {
			if e.CentralObject("events", ev) == nil {
				t.Errorf("event %s not on central", ev)
			}
		}
		if dup != nil {
			if e.CentralObject("trackedEntities", dup.TE) != nil {
				t.Error("duplicate unique value accepted")
			}
			if obj := e.App.Hub.Index.Get(dup.TE); obj == nil || obj.SyncState != store.SyncRejected {
				t.Error("duplicate not marked rejected on the hub")
			}
		}
		if n := e.App.Hub.Store.CountOutbox(store.OutboxPending); n != 0 {
			t.Errorf("%d entries still pending", n)
		}
	})

	t.Run("reboot: data locked until a known user logs in offline", func(t *testing.T) {
		e.Reboot()
		e.Offline()
		if e.App.Hub.Store.Unlocked() {
			t.Fatal("store unlocked at boot")
		}
		again := e.Web(tk.RoleNurse)
		again.Login()
		got := uids(again.JSON("GET", search(runTag+"offline"), nil, 200)["trackedEntities"], "trackedEntity")
		if !slices.Contains(got, offline.TE) {
			t.Fatalf("after reboot: %v", got)
		}
		e.Online()
	})

	t.Run("disk: nothing about this run's people in plain text", func(t *testing.T) {
		e.Reboot() // closes the store, checkpointing its files
		files, _ := filepath.Glob(filepath.Join(e.Dir, "*"))
		needles := []string{runTag, offline.TE, online.TE, p.ID, m.Facility.ID, m.Password}
		for _, f := range files {
			b, _ := os.ReadFile(f)
			for _, n := range needles {
				if bytes.Contains(b, []byte(n)) {
					t.Errorf("%q in plain text in %s", n, filepath.Base(f))
				}
			}
		}
	})
}
