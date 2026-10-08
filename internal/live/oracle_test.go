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
	e.Sync()

	var programs, tets []string
	for _, p := range m.Programs {
		programs = append(programs, p.ID)
		if !slices.Contains(tets, p.TET) {
			tets = append(tets, p.TET)
		}
	}
	truth, err := tk.ReadTruth(e.Central, m.Facility.ID, programs, tets)
	if err != nil {
		t.Fatal(err)
	}
	var queries []tk.Query
	for _, p := range m.Programs {
		queries = append(queries, tk.Queries(m, p, samples(t, e, p, truth))...)
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
			if why := knownGap(o, q, dims[u.name]); why != "" {
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

// knownGap names a difference the hub accepts by design or has not closed yet, so
// it is reported but does not fail the run. Everything else does.
func knownGap(o tk.Outcome, q tk.Query, dims map[string]string) string {
	switch {
	case o.Verdict == tk.Lenient && strings.Contains(o.Detail, "attributes should be mentioned in the search criteria"):
		return "an ACCESSIBLE search with fewer attributes than the program requires: central refuses it (409), the hub answers from facility data"
	case o.Verdict == tk.Missing && q.Kind == "events" && dims["tet"] == "none":
		return "events for a user without tracked entity type access: central shows them, the hub pulls by tracked entity"
	}
	return ""
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
