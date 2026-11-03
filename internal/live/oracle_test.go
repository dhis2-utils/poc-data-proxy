package live

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestOracle asks central and the offline hub the same questions as every role and
// matrix user, and judges each answer (docs/TESTING.md). Live only: the fake does not
// implement enough of the tracker API to be an oracle.
func TestOracle(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	m := e.M
	e.Setup()

	type person struct {
		name string
		c    *tk.Client
	}
	var users []person
	for role := range m.Users {
		users = append(users, person{m.Users[role].Username, e.Android(role)})
	}
	for _, mu := range m.Matrix {
		users = append(users, person{mu.Username, e.AndroidUser(mu.Username)})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].name < users[j].name })
	for _, u := range users {
		u.c.Do("GET", "/api/me?fields=id", nil) // online login: registers facility staff
	}
	var writer *tk.Client
	if u, ok := m.User(tk.RoleNurse); ok {
		for _, x := range users {
			if x.name == u.Username {
				writer = x.c
			}
		}
	}
	if writer == nil && len(users) > 0 {
		writer = users[0].c
	}
	seedOrderTies(t, e, m, writer)
	e.Sync()

	var programs, tets []string
	for _, p := range m.Programs {
		programs = append(programs, p.ID)
		if !slices.Contains(tets, p.TET) {
			tets = append(tets, p.TET)
		}
	}
	var eventPrograms []string
	for _, p := range m.EventPrograms {
		eventPrograms = append(eventPrograms, p.ID)
	}
	truth, err := tk.ReadTruth(e.Central, m.Facility.ID, programs, tets, eventPrograms)
	if err != nil {
		t.Fatal(err)
	}
	var queries []tk.Query
	for _, p := range m.Programs {
		queries = append(queries, tk.Queries(m, p, samples(t, e, p, truth))...)
	}
	for _, p := range m.EventPrograms {
		sample := ""
		for uid := range truth.Owned[p.ID] {
			if sample == "" || uid < sample {
				sample = uid
			}
		}
		queries = append(queries, tk.EventQueries(m, p, sample)...)
	}
	e.Offline()

	dims := map[string]map[string]string{}
	for _, mu := range m.Matrix {
		dims[mu.Username] = mu.Dims
	}
	counts := map[string]int{}
	known := map[string]int{}
	var bad []tk.Outcome
	var report []tk.Outcome
	for _, u := range users {
		direct := e.Central.As(u.name, m.Password)
		for _, q := range queries {
			cs, cb, err := direct.Do("GET", q.Path, nil)
			if err != nil {
				t.Fatal(err)
			}
			hs, hb := u.c.Do("GET", q.Path, nil)
			o := tk.Judge(truth, q, u.name, cs, cb, hs, hb)
			counts[o.Verdict]++
			report = append(report, o)
			if why := knownGap(o, q, dims[u.name], m); why != "" {
				known[why]++
				continue
			}
			if why := shownWithoutProgram(direct, o, q, hb); why != "" {
				known[why]++
				continue
			}
			if why := createdTies(direct, o, q, cb, hb); why != "" {
				known[why]++
				continue
			}
			if o.Verdict == tk.Leak || o.Verdict == tk.Missing || o.Verdict == tk.Differs || o.Verdict == tk.Lenient {
				bad = append(bad, o)
			}
		}
	}
	t.Logf("%d users x %d queries: %v", len(users), len(queries), counts)
	gaps := map[string]int{}
	for _, o := range report {
		if o.Verdict == tk.Refused && len(o.Objects) > 0 {
			gaps[o.Detail]++
		}
	}
	for why, n := range known {
		t.Logf("known gap (%d answers): %s", n, why)
	}
	for why, n := range gaps {
		t.Logf("coverage gap: %d answers refused while central shows facility data: %q", n, why)
	}
	if p := os.Getenv("HUB_REPORT"); p != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(p, b, 0o644)
	}
	sort.SliceStable(bad, func(i, j int) bool { return rank(bad[i].Verdict) < rank(bad[j].Verdict) })
	for i, o := range bad {
		if i == 40 {
			t.Errorf("... and %d more", len(bad)-40)
			break
		}
		t.Errorf("%s %s %s (central %d, hub %d) %v %s", o.Verdict, o.User, o.Path, o.Central, o.Hub,
			short(o.Objects), o.Detail)
	}
}

// seedOrderTies writes the ties the oracle's sequence comparison judges: five events of
// each event program sharing a scheduledAt, and four persons of the primary program
// sharing the search attribute's value, each in its own request a second apart so
// central's created times differ and the tie is broken by creation time, not UID. One
// client writes them all, before the sync that pulls them into the hub.
func seedOrderTies(t *testing.T, e *tk.Env, m *tk.Manifest, writer *tk.Client) {
	t.Helper()
	if writer == nil {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	write := func(payload map[string]any) {
		t.Helper()
		if st, b := writer.Do("POST", tk.SyncImport, payload); st != 200 {
			t.Fatalf("seeding a tie: %d %.300s", st, b)
		}
		time.Sleep(time.Second) // created a second apart, so creation time breaks the tie
	}
	for _, p := range m.EventPrograms {
		if len(p.AOCs) > 0 {
			continue // its events need a category option the writer may not have access to
		}
		for i := 0; i < 5; i++ {
			_, payload := m.NewEvent(p, p.Stages[0], "")
			ev := payload["events"].([]any)[0].(map[string]any)
			delete(ev, "enrollment") // an event program's events have none
			ev["scheduledAt"] = today
			write(payload)
		}
	}
	if p := m.Primary(); p.SearchAttr != "" {
		for i := 0; i < 4; i++ {
			pr := m.NewPerson(p, "TieName")
			writer.Complete(p, pr)
			write(pr.Payload)
		}
	}
}

// shownWithoutProgram accepts one leak by design (decided 2026-10-28): a person opened
// with a program central does not show them in for this user (owned elsewhere), whom
// the hub answers with no enrollment or owner in that program and only attributes
// central gives the same user without the program. The hub cannot tell "enrolled
// elsewhere" from "not enrolled" for a person it holds without that enrollment, and
// refusing would stop enrolling a known person offline. Central is asked once more,
// without the program, only for such an answer.
func shownWithoutProgram(direct *tk.Central, o tk.Outcome, q tk.Query, hubBody []byte) string {
	if o.Verdict != tk.Leak || !q.Single || q.Kind != "trackedEntities" || strings.HasPrefix(q.Program, "tet:") {
		return ""
	}
	var hub struct {
		Attributes    []struct{ Attribute, Value string }
		Enrollments   []struct{ Program string }
		ProgramOwners []struct{ Program string }
	}
	if json.Unmarshal(hubBody, &hub) != nil {
		return ""
	}
	for _, e := range hub.Enrollments {
		if e.Program == q.Program {
			return ""
		}
	}
	for _, po := range hub.ProgramOwners {
		if po.Program == q.Program {
			return ""
		}
	}
	u, err := url.Parse(q.Path)
	if err != nil {
		return ""
	}
	v := u.Query()
	v.Del("program")
	u.RawQuery = v.Encode()
	st, b, err := direct.Do("GET", u.String(), nil)
	if err != nil || st != 200 {
		return ""
	}
	var central struct {
		Attributes []struct{ Attribute, Value string }
	}
	if json.Unmarshal(b, &central) != nil {
		return ""
	}
	shown := map[string]string{}
	for _, a := range central.Attributes {
		shown[a.Attribute] = a.Value
	}
	for _, a := range hub.Attributes {
		if v, ok := shown[a.Attribute]; !ok || v != a.Value {
			return ""
		}
	}
	return "a person owned elsewhere opened with that program: the hub shows what central shows without the program, no enrollment (decided 2026-10-28)"
}

// createdTies accepts a list ordered by createdAt whose order differs from central's
// only among records created in the same millisecond: central breaks those ties in the
// order's direction, the hub by UID descending (seen on 2.43, where the seed import
// creates several people and events in one millisecond; decided 2026-11-03 to
// accept, as such ties come from imports).
func createdTies(direct *tk.Central, o tk.Outcome, q tk.Query, centralBody, hubBody []byte) string {
	if o.Verdict != tk.Differs || !strings.Contains(o.Detail, "order differs") || orderField(q) != "createdAt" ||
		(q.Kind != "trackedEntities" && q.Kind != "events") {
		return ""
	}
	type row struct{ TrackedEntity, Event, CreatedAt string }
	rows := func(b []byte) []row {
		var l struct{ TrackedEntities, Events []row }
		json.Unmarshal(b, &l)
		return append(l.TrackedEntities, l.Events...)
	}
	ids := func(b []byte) []string {
		var out []string
		for _, r := range rows(b) {
			out = append(out, r.TrackedEntity+r.Event)
		}
		return out
	}
	hub, central := ids(hubBody), ids(centralBody)
	if len(hub) == 0 || len(hub) != len(central) {
		return ""
	}
	// Every record's creation time, from the whole list: a first page of the hub's
	// may hold records central puts on its second.
	u, err := url.Parse(q.Path)
	if err != nil {
		return ""
	}
	v := u.Query()
	v.Set("fields", "trackedEntity,event,createdAt")
	v.Set("paging", "false")
	v.Del("page")
	v.Del("pageSize")
	u.RawQuery = v.Encode()
	st, b, err := direct.Do("GET", u.String(), nil)
	if err != nil || st != 200 {
		return ""
	}
	created := map[string]string{}
	for _, r := range rows(b) {
		created[r.TrackedEntity+r.Event] = r.CreatedAt
	}
	desc := strings.HasSuffix(strings.Split(v.Get("order"), ",")[0], ":desc")
	times := func(l []string) []string {
		var out []string
		for i, id := range l {
			t := created[id]
			if t == "" || (i > 0 && ((!desc && out[i-1] > t) || (desc && out[i-1] < t))) {
				return nil
			}
			out = append(out, t)
		}
		return out
	}
	// Both sorted by creation time, with the same times in the same places: they
	// differ only in the order of records created in the same millisecond.
	ht, ct := times(hub), times(central)
	if ht == nil || !slices.Equal(ht, ct) {
		return ""
	}
	return "a list ordered by createdAt with records created in the same millisecond: central breaks the ties in the order's direction, the hub by UID descending (2.43's seed import; decided 2026-11-03)"
}

// knownGap names a difference the hub accepts by design or has not closed yet, so
// it is reported but does not fail the run. Everything else does.
func knownGap(o tk.Outcome, q tk.Query, dims map[string]string, m *tk.Manifest) string {
	enrollmentCombo := false
	for _, p := range m.Programs {
		enrollmentCombo = enrollmentCombo || (p.ID == q.Program && p.EnrollmentCombo)
	}
	switch {
	case o.Verdict == tk.Missing && enrollmentCombo:
		return "a program with an enrollment category combination (2.43): central lists a person whose enrollment the user may not see without it, and shows the enrollment's events the user may see; the hub pulls people with their enrollments and holds neither"
	case o.Verdict == tk.Lenient && strings.Contains(o.Detail, "attributes should be mentioned in the search criteria"):
		return "an ACCESSIBLE search with fewer attributes than the program requires: central refuses it (409), the hub answers from facility data"
	case o.Verdict == tk.Missing && q.Kind == "events" && dims["tet"] == "none":
		return "events for a user without tracked entity type access: central shows them, the hub pulls by tracked entity"
	case o.Verdict == tk.Differs && q.Single && q.Kind == "events" && strings.HasPrefix(m.DHIS2Version, "2.42") &&
		onlyMoreDataValues(o.Detail):
		return "2.42's single-event endpoint leaves out data values its event lists show the same user (Laos); the hub answers as the lists"
	case o.Verdict == tk.Differs && strings.Contains(o.Detail, "order differs") && orderField(q) == searchAttr(m, q):
		return "an order on the program's search attribute: central collates text in the database's locale (Aïcha sorts as Aicha), the hub compares bytes; ties on the same value break by creation time, newest first, as central does (measured 2026-11-02)"
	case o.Verdict == tk.Lenient && orderField(q) == "scheduledAt" && strings.Contains(o.Detail, "Cannot order by 'scheduledDate'"):
		return "an event program's events ordered by scheduledAt: 2.43 refuses the order (400), the hub answers it as 2.42 does (seen 2026-11-03)"
	case o.Verdict == tk.Leak && orderField(q) == "scheduledAt":
		return "a paged list ordered by scheduledAt: events with no scheduled date sort first when descending on central, last on the hub, so the two first pages hold different events"
	}
	return ""
}

// orderField is the first field of the request's order parameter ("" when none).
func orderField(q tk.Query) string {
	u, err := url.Parse(q.Path)
	if err != nil {
		return ""
	}
	f, _, _ := strings.Cut(strings.Split(u.Query().Get("order"), ",")[0], ":")
	return f
}

// searchAttr is the search attribute of the query's program, if it has one.
func searchAttr(m *tk.Manifest, q tk.Query) string {
	for _, p := range m.Programs {
		if p.ID == q.Program {
			return p.SearchAttr
		}
	}
	return ""
}

// onlyMoreDataValues reports whether a DIFFERS detail ("hub: {...}\ncentral: {...}")
// differs only by data values the hub shows and central's answer leaves out.
func onlyMoreDataValues(detail string) bool {
	hubPart, centralPart, ok := strings.Cut(strings.TrimPrefix(detail, "hub: "), "\ncentral: ")
	if !ok {
		return false
	}
	var hub, central map[string]any
	if json.Unmarshal([]byte(hubPart), &hub) != nil || json.Unmarshal([]byte(centralPart), &central) != nil {
		return false
	}
	hv, _ := hub["dataValues"].([]any)
	cv, _ := central["dataValues"].([]any)
	for _, v := range cv {
		if !slices.Contains(hv, v) {
			return false
		}
	}
	delete(hub, "dataValues")
	delete(central, "dataValues")
	a, _ := json.Marshal(hub)
	b, _ := json.Marshal(central)
	return string(a) == string(b) && len(hv) > len(cv)
}

func rank(v string) int { return strings.Index(tk.Leak+tk.Missing+tk.Differs, v) }

func short(s []string) []string {
	if len(s) > 5 {
		return append(s[:5:5], fmt.Sprintf("+%d", len(s)-5))
	}
	return s
}

// samples picks values from the data for the query generator: a search attribute
// value and a unique value of an owned tracked entity, the entity itself, and one
// the facility does not own.
func samples(t *testing.T, e *tk.Env, p tk.Program, truth *tk.Truth) map[string]string {
	out := map[string]string{}
	var owned []string
	for uid := range truth.Owned[p.ID] {
		owned = append(owned, uid)
	}
	sort.Strings(owned)
	for _, uid := range owned {
		var te struct {
			Attributes []struct{ Attribute, Value string } `json:"attributes"`
		}
		if err := e.Central.JSON("GET", "/api/tracker/trackedEntities/"+uid+"?program="+p.ID+"&fields=attributes", nil, &te); err != nil {
			continue
		}
		for _, a := range te.Attributes {
			if a.Attribute == p.SearchAttr && out["search"] == "" {
				out["search"], out["owned"] = a.Value, uid
			}
			if a.Attribute == p.UniqueAttr && out["unique"] == "" {
				out["unique"] = a.Value
			}
		}
		if out["search"] != "" && (p.UniqueAttr == "" || out["unique"] != "") {
			break
		}
	}
	var res struct {
		TEs []struct {
			TrackedEntity string `json:"trackedEntity"`
		} `json:"trackedEntities"`
	}
	q := url.Values{"program": {p.ID}, "orgUnits": {e.M.Sibling.ID}, "pageSize": {"1"}, "fields": {"trackedEntity"}}
	if e.Central.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &res) == nil && len(res.TEs) > 0 {
		out["foreign"] = res.TEs[0].TrackedEntity
	}
	return out
}

func TestOnlyMoreDataValues(t *testing.T) {
	more := "hub: {\"event\":\"E\",\"dataValues\":[\"a\",\"b\"]}\ncentral: {\"event\":\"E\",\"dataValues\":[\"a\"]}"
	other := "hub: {\"event\":\"E\",\"status\":\"ACTIVE\",\"dataValues\":[\"a\",\"b\"]}\ncentral: {\"event\":\"E\",\"status\":\"COMPLETED\",\"dataValues\":[\"a\"]}"
	fewer := "hub: {\"event\":\"E\",\"dataValues\":[\"a\"]}\ncentral: {\"event\":\"E\",\"dataValues\":[\"a\",\"b\"]}"
	if !onlyMoreDataValues(more) || onlyMoreDataValues(other) || onlyMoreDataValues(fewer) {
		t.Fatal("onlyMoreDataValues")
	}
}
