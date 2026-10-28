package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// result is what central holds of a ledger's saves (internal/adversarial's check).
type result struct {
	Saves, Confirmed int
	Checked          int      // records (registrations and added events) read on central
	Lost             []string // confirmed, not on central whole
	Partial          []string // a registration not confirmed, on central in part
	Duplicates       []string // a person or event of the run on central that the ledger does not have
	Wrong            []string // on central with other values or by another author
}

func (r result) ok() bool { return len(r.Lost)+len(r.Partial)+len(r.Duplicates)+len(r.Wrong) == 0 }

func (r result) String() string {
	return fmt.Sprintf("%d saves in the ledger, %d confirmed; %d records checked on central: lost %d, partial %d, duplicated %d, wrong %d",
		r.Saves, r.Confirmed, r.Checked, len(r.Lost), len(r.Partial), len(r.Duplicates), len(r.Wrong))
}

func checkMain(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	hf := addHubFlags(fs, 30*time.Second)
	central := fs.String("central", "https://implement.im.dhis2.org/facility-hub", "central DHIS2, read directly")
	admin := fs.String("admin", "fh_national", "a central account that sees the facility's records (ALL)")
	adminPw := fs.String("admin-password", env("HUBDRIVE_ADMIN_PASSWORD", ""), "its password (or HUBDRIVE_ADMIN_PASSWORD)")
	wait := fs.Duration("wait", 15*time.Minute, "with -hub: wait up to this long for the hub to be online with nothing pending")
	fs.Parse(args)
	if fs.NArg() == 0 || *adminPw == "" {
		return fmt.Errorf("check needs -admin-password and at least one ledger file")
	}
	var recs []record
	for _, path := range fs.Args() {
		rs, err := readLedger(path)
		if err != nil {
			return err
		}
		recs = append(recs, rs...)
	}
	l := log.New(os.Stdout, "", log.Ltime)
	var hub *http.Client
	if *hf.hub != "" {
		rt, err := hf.transport()
		if err != nil {
			return err
		}
		hub = &http.Client{Transport: rt, Timeout: *hf.timeout}
		waitDrained(l, hub, hf.url(), *wait)
	}
	res, err := check(&dhis{base: strings.TrimRight(*central, "/"), user: *admin, pw: *adminPw,
		http: &http.Client{Timeout: 2 * time.Minute}}, recs)
	if err != nil {
		return err
	}
	for _, s := range [][2]any{{"LOST", res.Lost}, {"PARTIAL", res.Partial}, {"DUPLICATE", res.Duplicates}, {"WRONG", res.Wrong}} {
		for _, x := range s[1].([]string) {
			l.Printf("%s %s", s[0], x)
		}
	}
	l.Print(res)
	if hub != nil {
		state, pending := hubState(hub, hf.url())
		l.Printf("hub now: %s, %s pending, %s refused by central", state, pending, rejected(hub, hf.url()))
		if len(res.Lost) > 0 && pending != "0" {
			l.Print("the hub still has saves to send: check again once it has sent them")
		}
	}
	if !res.ok() {
		return fmt.Errorf("check failed")
	}
	l.Print("OK: every confirmed save is on central once and whole")
	return nil
}

func readLedger(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		var r record
		if len(strings.TrimSpace(sc.Text())) == 0 {
			continue
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			// The last line of a ledger on a machine that lost power may be cut short.
			log.Printf("%s line %d skipped: %v", path, n, err)
			continue
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// waitDrained waits until the hub is online and unlocked with nothing pending.
func waitDrained(l *log.Logger, c *http.Client, hub string, wait time.Duration) {
	end := time.Now().Add(wait)
	for {
		state, pending := hubState(c, hub)
		if state == "up, online" && pending == "0" {
			l.Printf("hub: %s, nothing pending", state)
			return
		}
		if time.Now().After(end) {
			l.Printf("hub: %s, %s pending after waiting %s; checking anyway", state, pending, wait)
			return
		}
		l.Printf("hub: %s, %s pending; waiting", state, pending)
		time.Sleep(10 * time.Second)
	}
}

func rejected(c *http.Client, hub string) string {
	resp, err := c.Get(hub + "/hub/status")
	if err != nil {
		return "?"
	}
	defer resp.Body.Close()
	var s map[string]any
	json.NewDecoder(resp.Body).Decode(&s)
	return fmt.Sprint(s["rejected"])
}

// dhis reads central directly.
type dhis struct {
	base, user, pw string
	http           *http.Client
}

// get returns the object, or nil if central does not have it.
func (d *dhis) get(path string) (map[string]any, error) {
	req, _ := http.NewRequest("GET", d.base+path, nil)
	req.SetBasicAuth(d.user, d.pw)
	req.Header.Set("Accept", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == 404:
		return nil, nil
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("GET %s: %d %.300s", path, resp.StatusCode, b)
	}
	var o map[string]any
	return o, json.Unmarshal(b, &o)
}

// check reads every record of the ledger from central: each confirmed one is there
// whole, by its author, with the value of the last confirmed save (or of a later
// unconfirmed one, which may have landed); each unconfirmed registration is there whole
// or not at all; and central holds nobody and no event of the run the ledger does not.
func check(d *dhis, recs []record) (result, error) {
	var out result
	byEvent := map[string][]record{}
	var order []string
	for _, r := range recs {
		out.Saves++
		if r.Confirmed {
			out.Confirmed++
		}
		if byEvent[r.Event] == nil {
			order = append(order, r.Event)
		}
		byEvent[r.Event] = append(byEvent[r.Event], r)
	}
	people := map[string]map[string]any{} // read once per person, with enrollments and events
	for _, ev := range order {
		saves := byEvent[ev]
		first := saves[0] // the save that creates the event comes first
		if first.Kind == kindUpdate {
			continue
		}
		out.Checked++
		created := false
		for _, s := range saves {
			created = created || (s.Confirmed && s.Kind == first.Kind)
		}
		te, ok := people[first.TE]
		if !ok {
			q := url.Values{"program": {first.Program}, "fields": {"trackedEntity,createdBy,deleted,attributes[attribute,value]," +
				"enrollments[enrollment,deleted,events[event,createdBy,deleted,dataValues[dataElement,value]]]"}}
			var err error
			if te, err = d.get("/api/tracker/trackedEntities/" + first.TE + "?" + q.Encode()); err != nil {
				return out, err
			}
			people[first.TE] = te
		}
		enr := find(te, "enrollments", "enrollment", first.Enr)
		evo := find(enr, "events", "event", ev)
		objs := []map[string]any{evo}
		if first.Kind == kindRegister {
			objs = []map[string]any{te, enr, evo}
		} else {
			te = nil
		}
		have := 0
		for _, o := range objs {
			if o != nil && o["deleted"] != true {
				have++
			}
		}
		what := fmt.Sprintf("%s %s (person %s) by %s on %s at %s", first.Kind, ev, first.TE, first.User, first.Client, first.At.Format("15:04:05"))
		switch {
		case created && have < len(objs):
			out.Lost = append(out.Lost, fmt.Sprintf("%s: %d of %d on central", what, have, len(objs)))
			continue
		case !created && have > 0 && have < len(objs):
			out.Partial = append(out.Partial, fmt.Sprintf("%s: not confirmed, %d of %d on central", what, have, len(objs)))
			continue
		case have == 0:
			continue
		}
		author := evo
		if te != nil {
			author = te
			for k, want := range first.Attrs {
				if got := value(te, "attributes", "attribute", k); got != want {
					out.Wrong = append(out.Wrong, fmt.Sprintf("%s: attribute %s %q on central, saved %q", what, k, got, want))
				}
			}
		}
		if u := username(author); u != first.User {
			out.Wrong = append(out.Wrong, fmt.Sprintf("%s: created by %q on central", what, u))
		}
		want := possible(saves)
		last := saves[len(saves)-1]
		for i := len(saves) - 1; i >= 0; i-- {
			if saves[i].Confirmed {
				last = saves[i]
				break
			}
		}
		for k, v := range last.Values {
			got := value(evo, "dataValues", "dataElement", k)
			if k == first.DE && !want[got] || k != first.DE && got != v {
				var hist []string
				for _, s := range saves {
					hist = append(hist, fmt.Sprintf("%s %s=%s confirmed=%v", s.At.Format("15:04:05"), k, s.Values[k], s.Confirmed))
				}
				out.Wrong = append(out.Wrong, fmt.Sprintf("%s: %s %q on central; saves: %s", what, k, got, strings.Join(hist, ", ")))
			}
		}
	}
	dups, err := duplicates(d, recs)
	out.Duplicates = dups
	return out, err
}

// duplicates lists the run's people on central (by the tag in their name): each must
// be in the ledger, and so must each of their events.
func duplicates(d *dhis, recs []record) ([]string, error) {
	known := map[string]bool{}
	type run struct{ tag, program, ou, attr string }
	runs := map[run]bool{}
	for _, r := range recs {
		known[r.TE], known[r.Event] = true, true
		for k, v := range r.Attrs {
			if strings.HasPrefix(v, r.Tag+"-") {
				runs[run{r.Tag, r.Program, r.OrgUnit, k}] = true
			}
		}
	}
	var out []string
	for rn := range runs {
		q := url.Values{"program": {rn.program}, "orgUnits": {rn.ou}, "filter": {rn.attr + ":like:" + rn.tag + "-"},
			"fields": {"trackedEntity,attributes[attribute,value],enrollments[enrollment,events[event]]"}, "paging": {"false"}}
		res, err := d.get("/api/tracker/trackedEntities?" + q.Encode())
		if err != nil {
			return out, err
		}
		seen := map[string]bool{}
		for _, x := range list(res["trackedEntities"]) {
			te := fmt.Sprint(x["trackedEntity"])
			if !strings.HasPrefix(value(x, "attributes", "attribute", rn.attr), rn.tag+"-") {
				continue // central's filter is a substring match
			}
			if seen[te] || !known[te] {
				out = append(out, "person "+te+" on central, not in the ledger")
			}
			seen[te] = true
			for _, en := range list(x["enrollments"]) {
				for _, ev := range list(en["events"]) {
					if e := fmt.Sprint(ev["event"]); !known[e] {
						out = append(out, fmt.Sprintf("event %s of person %s on central, not in the ledger", e, te))
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// possible is the set of values central may hold for an event's number: the last
// confirmed save's, or any later unconfirmed one's (which may have landed).
func possible(saves []record) map[string]bool {
	de := saves[0].DE
	last := -1
	for i, s := range saves {
		if s.Confirmed {
			last = i
		}
	}
	out := map[string]bool{}
	start := 0
	if last >= 0 {
		out[saves[last].Values[de]] = true
		start = last + 1
	}
	for _, s := range saves[start:] {
		out[s.Values[de]] = true
	}
	return out
}

// find is the object in o's list under key whose idKey is id, or nil.
func find(o map[string]any, key, idKey, id string) map[string]any {
	for _, m := range list(o[key]) {
		if m[idKey] == id {
			return m
		}
	}
	return nil
}

func list(x any) []map[string]any {
	var out []map[string]any
	xs, _ := x.([]any)
	for _, y := range xs {
		if m, ok := y.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func value(o map[string]any, listKey, idKey, id string) string {
	for _, m := range list(o[listKey]) {
		if m[idKey] == id {
			s, _ := m["value"].(string)
			return s
		}
	}
	return ""
}

func username(o map[string]any) string {
	m, _ := o["createdBy"].(map[string]any)
	s, _ := m["username"].(string)
	return s
}
