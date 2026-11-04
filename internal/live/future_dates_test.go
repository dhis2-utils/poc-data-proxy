package live

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// F4: where the program does not allow future dates (selectEnrollmentDatesInFuture,
// selectIncidentDatesInFuture), central refuses an enrollment dated tomorrow (E1020)
// and one whose incident date is tomorrow (E1021). The hub refuses both offline, with
// central's status, codes and texts, judging "tomorrow" by its clock in central's
// time zone. Once the program allows future dates, the hub takes them offline and
// central accepts them at push.
func TestFutureEnrollmentDates(t *testing.T) {
	e := tk.Start(t)
	p := e.M.Primary()
	allow := func(v bool) {
		if e.Live() {
			e.SetField("/api/programs/"+p.ID, "selectEnrollmentDatesInFuture", v)
			e.SetField("/api/programs/"+p.ID, "selectIncidentDatesInFuture", v)
		} else {
			e.Fake.FutureEnrollmentDates, e.Fake.FutureIncidentDates = v, v
		}
	}
	allow(false)
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	today := e.App.Hub.CentralNow()
	if today.Add(10*time.Minute).Day() != today.Day() {
		t.Skip("central's date changes within ten minutes")
	}
	tomorrow := today.AddDate(0, 0, 1).Format(time.DateOnly)

	type report struct{ Code, Message string }
	// person registers someone with one event, enrolled at enrolled and with the
	// incident at incident; send posts them and gives the status and the enrollment's
	// error reports.
	person := func(name, enrolled, incident string) *tk.Person {
		pr := e.M.NewPerson(p, name, p.Stages[0])
		nurse.Complete(p, pr)
		enr := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)["enrollments"].([]any)[0].(map[string]any)
		enr["enrolledAt"], enr["occurredAt"] = enrolled, incident
		return pr
	}
	send := func(pr *tk.Person) (int, []report, []string) {
		st, b := nurse.Do("POST", tk.SyncImport, pr.Payload)
		var rep struct {
			ValidationReport struct {
				ErrorReports []struct {
					ErrorCode, Message, UID string
				} `json:"errorReports"`
			} `json:"validationReport"`
		}
		json.Unmarshal(b, &rep)
		var enr []report
		var all []string
		for _, r := range rep.ValidationReport.ErrorReports {
			all = append(all, r.ErrorCode)
			if r.UID == pr.Enrollment {
				enr = append(enr, report{r.ErrorCode, r.Message})
			}
		}
		slices.Sort(all)
		return st, enr, all
	}
	cases := []struct{ what, enrolled, incident string }{
		{"enrolled tomorrow", tomorrow, today.Format(time.DateOnly)},
		{"incident tomorrow", today.Format(time.DateOnly), tomorrow},
	}
	type answer struct {
		st  int
		enr []report
		all []string
	}
	online := map[string]answer{}
	for _, c := range cases {
		st, enr, all := send(person("Future online", c.enrolled, c.incident))
		online[c.what] = answer{st, enr, all}
		t.Logf("central, %s: %d %v; every code %v", c.what, st, enr, all)
	}

	e.Offline()
	for _, c := range cases {
		st, enr, all := send(person("Future offline", c.enrolled, c.incident))
		want := online[c.what]
		if st != want.st || fmt.Sprint(enr) != fmt.Sprint(want.enr) {
			t.Errorf("hub offline, %s: %d %v; central said %d %v", c.what, st, enr, want.st, want.enr)
		}
		// Central refuses the enrollment's events with E5000 (2.42.7); the fake
		// reports nothing for them.
		if e.Live() && !slices.Equal(all, want.all) {
			t.Errorf("hub offline, %s: every code %v, central's %v", c.what, all, want.all)
		}
		if st/100 == 2 {
			t.Errorf("hub offline, %s: taken (%d), want refused", c.what, st)
		}
	}
	if st, enr, _ := send(person("Future today", today.Format(time.DateOnly), today.Format(time.DateOnly))); st != 200 {
		t.Errorf("hub offline, enrolled today: %d %v, want taken", st, enr)
	}

	// The program now allows future dates: after a sync the hub takes them offline.
	e.Online()
	allow(true)
	e.Sync()
	e.Offline()
	later := person("Future allowed", tomorrow, tomorrow)
	if st, enr, _ := send(later); st != 200 {
		t.Errorf("hub offline, future dates allowed: %d %v, want taken", st, enr)
	}
	e.Online()
	e.Sync()
	if e.CentralObject("enrollments", later.Enrollment) == nil {
		t.Error("central did not take the enrollment dated tomorrow at push, with future dates allowed")
	}
}
