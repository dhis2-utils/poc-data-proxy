package live

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestSlowLinkPulls measures what a hub's pulls cost on a facility's uplink: the
// first fill, a sync in which nothing changed and the weekly full pull, at full
// speed, 3 000 KB/s and 31 KB/s (St Joseph Buyege HC, 29 October 2026), each slow
// link with 200 ms latency per request. Run against the Sierra Leone database at
// Ngelehun CHC (HUB_SLOWLINK=1, docs/PERFORMANCE.md, "A slow uplink"). One JSON line
// per measurement goes to HUB_REPORT.
func TestSlowLinkPulls(t *testing.T) {
	if os.Getenv("HUB_SLOWLINK") == "" {
		t.Skip("set HUB_SLOWLINK=1 to measure pulls on a slow uplink")
	}
	e := tk.Start(t)
	report := func(v map[string]any) {
		b, _ := json.Marshal(v)
		t.Log(string(b))
		if p := os.Getenv("HUB_REPORT"); p != "" {
			f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				f.Write(append(b, '\n'))
				f.Close()
			}
		}
	}
	// What one sync cycle (or set-up with its first sync) asks of central.
	measure := func(label string, rate int, delay time.Duration, run func()) {
		e.Net.SetRate(rate)
		e.Net.SetDelay(delay)
		before := len(e.Net.Calls())
		start := time.Now()
		run()
		took := time.Since(start)
		e.Net.SetRate(0)
		e.Net.SetDelay(0)
		calls := e.Net.Calls()[before:]
		kinds := map[string][2]int64{} // kind -> requests, bytes
		var total int64
		for _, c := range calls {
			k := callKind(c.URL.Path, c.URL.RawQuery)
			v := kinds[k]
			v[0]++
			v[1] += c.Bytes
			kinds[k] = v
			total += c.Bytes
		}
		byKind := map[string]string{}
		for k, v := range kinds {
			byKind[k] = fmt.Sprintf("%d requests, %.2f MB", v[0], float64(v[1])/1e6)
		}
		report(map[string]any{"measure": label, "rateBytesPerSec": rate, "delayMs": delay.Milliseconds(),
			"seconds": took.Seconds(), "requests": len(calls), "MB": float64(total) / 1e6, "byKind": byKind,
			"objects": e.App.Hub.Index.Len()})
	}
	full := func() {
		cfg := e.App.Hub.Cfg.FullPullInterval
		e.App.Hub.Cfg.FullPullInterval = time.Nanosecond
		defer func() { e.App.Hub.Cfg.FullPullInterval = cfg }()
		e.Sync()
	}
	slow := 200 * time.Millisecond

	// Compressed, as nginx in front of central usually answers; then uncompressed, as
	// the demo central answered on 5 November 2026, on the slow link only.
	e.Net.SetGzip(true)
	measure("first fill, gzip", 0, 0, func() { e.SetupAs(tk.RoleNurse) })
	for _, r := range []struct {
		rate  int
		delay time.Duration
		gzip  bool
	}{{0, 0, true}, {3_000_000, slow, true}, {31_000, slow, true}, {31_000, slow, false}} {
		e.Net.SetGzip(r.gzip)
		z := map[bool]string{true: ", gzip", false: ", uncompressed"}[r.gzip]
		measure("sync, nothing changed"+z, r.rate, r.delay, e.Sync)
		measure("weekly full pull, nothing changed"+z, r.rate, r.delay, full)
	}
	e.Net.SetGzip(false)

	// Central sends its JSON uncompressed unless a proxy in front of it compresses
	// (the demo central does not). How much would gzip save on what a pull reads?
	m := e.M
	p := slowProgram(m).ID
	for _, q := range []struct{ label, path string }{
		{"listing page (ids and times)", e.App.Hub.API().ListPath(m.Facility.ID, p, "", 1, 1000)},
		{"fetch of 50 people", "/api/tracker/trackedEntities?orgUnits=" + m.Facility.ID +
			"&orgUnitMode=SELECTED&program=" + p + "&fields=*,enrollments[*,events[*]],relationships[*]&pageSize=50"},
	} {
		st, body, err := e.Central.Do("GET", q.path, nil)
		if err != nil || st != 200 {
			t.Fatalf("%s: %d %v", q.label, st, err)
		}
		var z bytes.Buffer
		w := gzip.NewWriter(&z)
		w.Write(body)
		w.Close()
		report(map[string]any{"measure": "gzip on " + q.label, "plainKB": len(body) / 1000, "gzipKB": z.Len() / 1000,
			"ratio": float64(len(body)) / float64(z.Len())})
	}
}

// callKind groups the hub's requests to central for the report.
func callKind(path, query string) string {
	switch {
	case strings.HasSuffix(path, "/api/tracker/trackedEntities") && strings.Contains(query, "updatedAt%2Cenrollments"),
		strings.HasSuffix(path, "/api/tracker/trackedEntities") && strings.Contains(query, "trackedEntity%2CupdatedAt"):
		return "listing"
	case strings.HasSuffix(path, "/api/tracker/trackedEntities") && strings.Contains(query, "trackedEntities="):
		return "fetch"
	case strings.HasSuffix(path, "/api/tracker/trackedEntities"):
		return "trackedEntities other"
	case strings.HasSuffix(path, "/api/tracker/events"):
		return "events"
	case strings.Contains(path, "/api/dataValueSets") || strings.Contains(path, "/api/dataEntry"):
		return "aggregate"
	case strings.Contains(path, "/api/tracker"):
		return "tracker other"
	}
	return "other"
}

// TestSlowLinkWorkflow times the requests behind the facility workflow seen on 28-29
// October 2026 (Butaaka, Buyege): register a patient, find a returning one by name,
// open them, the working list, and the next room (another user) opening what the
// first just saved. Through the hub online, each passes to central over the uplink;
// answered by the hub (uplink off), none does: the case for serving local data while
// online (HUB_SLOWLINK=1, docs/PERFORMANCE.md, "A slow uplink").
func TestSlowLinkWorkflow(t *testing.T) {
	if os.Getenv("HUB_SLOWLINK") == "" {
		t.Skip("set HUB_SLOWLINK=1 to measure the workflow on a slow uplink")
	}
	e := tk.Start(t)
	e.Net.SetGzip(true)
	e.SetupAs(tk.RoleNurse)
	m, p := e.M, slowProgram(e.M)
	triage, lab := e.Web(tk.RoleNurse), e.Web(tk.RoleRestricted) // a second nurse here: no stage hidden
	triage.Login()
	lab.Login()
	e.Sync()
	report := func(v map[string]any) {
		b, _ := json.Marshal(v)
		t.Log(string(b))
		if f := os.Getenv("HUB_REPORT"); f != "" {
			if w, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				w.Write(append(b, '\n'))
				w.Close()
			}
		}
	}
	timed := func(c *tk.Client, method, path string, body any) (time.Duration, int, []byte) {
		start := time.Now()
		st, b := c.Do(method, path, body)
		return time.Since(start), st, b
	}
	slow := 200 * time.Millisecond
	tag := fmt.Sprint(time.Now().Unix() % 100000)
	for _, cond := range []struct {
		label string
		rate  int
		delay time.Duration
		local bool
	}{
		{"through to central, full speed", 0, 0, false},
		{"through to central, 3000 KB/s", 3_000_000, slow, false},
		{"through to central, 31 KB/s", 31_000, slow, false},
		{"answered by the hub, 31 KB/s uplink", 31_000, slow, true},
	} {
		if cond.local {
			if err := e.SetUplink(false); err != nil {
				t.Fatal(err)
			}
		}
		e.Net.SetRate(cond.rate)
		e.Net.SetDelay(cond.delay)
		steps := map[string][]float64{}
		add := func(step string, d time.Duration, st int, b []byte) {
			if st >= 300 {
				t.Errorf("%s, %s: %d %.300s", cond.label, step, st, b)
			}
			steps[step] = append(steps[step], d.Seconds())
		}
		for i := 0; i < 3; i++ {
			name := fmt.Sprintf("Slow%s%c%d", tag, 'a'+rune(len(cond.label)%20), i)
			pr := m.NewPerson(p, name)
			start := time.Now()
			triage.Complete(p, pr)
			_, st, b := timed(triage, "POST", tk.SyncImport, pr.Payload)
			add("register (reserve IDs, save)", time.Since(start), st, b)
			d, st, b := timed(triage, "GET", "/api/tracker/trackedEntities?program="+p.ID+
				"&orgUnitMode=ACCESSIBLE&filter="+p.SearchAttr+":like:"+name+"&fields=:all,!relationships,programOwners[orgUnit,program]&pageSize=15&page=1", nil)
			add("find by name", d, st, b)
			d, st, b = timed(triage, "GET", "/api/tracker/trackedEntities/"+pr.TE+"?program="+p.ID+"&fields=*", nil)
			add("open the person", d, st, b)
			d, st, b = timed(triage, "GET", "/api/tracker/trackedEntities?fields=:all,!relationships,programOwners[orgUnit,program]&orgUnits="+
				m.Facility.ID+"&orgUnitMode=SELECTED&program="+p.ID+"&order=createdAt:desc&page=1&pageSize=15", nil)
			add("working list (15 newest)", d, st, b)
			ev, payload := m.NewEvent(p, p.Stages[0], pr.Enrollment)
			d, st, b = timed(triage, "POST", tk.SyncImport, payload)
			add("save an event", d, st, b)
			d, st, b = timed(lab, "GET", "/api/tracker/events/"+ev+"?fields=*", nil)
			add("next room opens it (another user)", d, st, b)
		}
		e.Net.SetRate(0)
		e.Net.SetDelay(0)
		if cond.local {
			if err := e.SetUplink(true); err != nil {
				t.Fatal(err)
			}
			e.Sync() // the local saves go to central before the next condition
		}
		med := map[string]float64{}
		for k, v := range steps {
			sort.Float64s(v)
			med[k] = v[len(v)/2]
		}
		report(map[string]any{"workflow": cond.label, "medianSeconds": med, "all": steps})
	}
}

// slowProgram is the Child Programme where the setup has it (the Sierra Leone
// database), else the manifest's first program.
func slowProgram(m *tk.Manifest) tk.Program {
	for _, p := range m.Programs {
		if p.ID == "IpHINAT79UW" {
			return p
		}
	}
	return m.Primary()
}

// TestSlowLinkSeed gives the facility HUB_SLOWLINK_SEED people in the slow-link
// program, each with an event in every stage, written on central directly (a
// facility the size of Ngelehun CHC on the demo central: 20 841 people).
func TestSlowLinkSeed(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("HUB_SLOWLINK_SEED"))
	if n == 0 {
		t.Skip("set HUB_SLOWLINK_SEED to the number of people to add")
	}
	m, _ := tk.Target(t)
	c := tk.NewCentral(m.CentralURL, m.AdminUser, m.AdminPassword)
	p := slowProgram(m)
	names := []string{"Aminata", "Fatmata", "Mohamed", "Ibrahim", "Mariama", "Abu", "Isatu", "Alhaji", "Kadiatu", "Sorie",
		"Hawa", "Foday", "Adama", "Musa", "Zainab", "Sahr", "Jeneba", "Lamin", "Christiana", "Joseph"}
	const batch = 100
	jobs := make(chan int)
	var wg sync.WaitGroup
	var failed atomic.Int64
	start := time.Now()
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				var tes []any
				for i := 0; i < batch; i++ {
					pr := m.NewPerson(p, fmt.Sprintf("%s %d", names[(b*batch+i)%len(names)], b*batch+i), p.Stages...)
					tes = append(tes, pr.Payload["trackedEntities"].([]any)...)
				}
				st, body, err := c.Do("POST", "/api/tracker?async=false&importStrategy=CREATE&atomicMode=OBJECT",
					map[string]any{"trackedEntities": tes})
				if err != nil || st != 200 {
					failed.Add(1)
					t.Logf("batch %d: %d %v %.500s", b, st, err, body)
				}
			}
		}()
	}
	for b := 0; b < n/batch; b++ {
		jobs <- b
		if b%20 == 19 {
			t.Logf("%d people sent, %s", (b+1)*batch, time.Since(start).Round(time.Second))
		}
	}
	close(jobs)
	wg.Wait()
	if failed.Load() > 0 {
		t.Errorf("%d batches failed", failed.Load())
	}
	t.Logf("seeded %d people in %s", n, time.Since(start).Round(time.Second))
}

// TestSlowLinkSearchProfile narrows down the hub's name search on the slow-link setup
// (174 ms on 20 000 people, against central's 42 ms): variants of the query, each
// answered by the hub offline, and a CPU profile of the slowest to HUB_PROFILE.
func TestSlowLinkSearchProfile(t *testing.T) {
	if os.Getenv("HUB_SLOWLINK") == "" {
		t.Skip("set HUB_SLOWLINK=1")
	}
	e := tk.Start(t)
	e.SetupAs(tk.RoleNurse)
	m, p := e.M, slowProgram(e.M)
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	if err := e.SetUplink(false); err != nil {
		t.Fatal(err)
	}
	base := "/api/tracker/trackedEntities?program=" + p.ID
	full := "&fields=:all,!relationships,programOwners[orgUnit,program]&pageSize=15&page=1"
	variants := []struct{ label, path string }{
		{"like, ACCESSIBLE, Capture's fields", base + "&orgUnitMode=ACCESSIBLE&filter=" + p.SearchAttr + ":like:Aminata%20140" + full},
		{"like, no match", base + "&orgUnitMode=ACCESSIBLE&filter=" + p.SearchAttr + ":like:Nobody" + full},
		{"like, common name (1 000 match)", base + "&orgUnitMode=ACCESSIBLE&filter=" + p.SearchAttr + ":like:Aminata" + full},
		{"eq, exact name", base + "&orgUnitMode=ACCESSIBLE&filter=" + p.SearchAttr + ":eq:Aminata%20140" + full},
		{"like, SELECTED facility", base + "&orgUnits=" + m.Facility.ID + "&orgUnitMode=SELECTED&filter=" + p.SearchAttr + ":like:Aminata%20140" + full},
		{"like, identifiers only", base + "&orgUnitMode=ACCESSIBLE&filter=" + p.SearchAttr + ":like:Aminata%20140&fields=trackedEntity&pageSize=15"},
		{"no filter, working list", base + "&orgUnits=" + m.Facility.ID + "&orgUnitMode=SELECTED&order=createdAt:desc" + full},
	}
	slowest, worst := "", time.Duration(0)
	for _, v := range variants {
		var ds []float64
		for i := 0; i < 5; i++ {
			start := time.Now()
			st, b := nurse.Do("GET", v.path, nil)
			d := time.Since(start)
			if st != 200 {
				t.Fatalf("%s: %d %.300s", v.label, st, b)
			}
			ds = append(ds, d.Seconds()*1000)
			if d > worst {
				slowest, worst = v.path, d
			}
		}
		sort.Float64s(ds)
		t.Logf("%-38s median %6.1f ms  %v", v.label, ds[2], ds)
	}
	if f := os.Getenv("HUB_PROFILE"); f != "" {
		w, _ := os.Create(f)
		pprof.StartCPUProfile(w)
		for i := 0; i < 30; i++ {
			nurse.Do("GET", slowest, nil)
		}
		pprof.StopCPUProfile()
		w.Close()
		t.Logf("profiled %s", slowest)
	}
}

// TestSlowLinkScreens times Capture web screens through the hub on a slow uplink,
// passed through to central and answered by the hub (clients/slowlink_screens.py;
// HUB_SLOWLINK=1 and HUB_CLIENTS=1).
func TestSlowLinkScreens(t *testing.T) {
	if os.Getenv("HUB_SLOWLINK") == "" || os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_SLOWLINK=1 and HUB_CLIENTS=1 to time Capture web screens")
	}
	e := tk.Start(t)
	e.Net.SetGzip(true)
	e.SetupAs(tk.RoleNurse)
	m, p := e.M, slowProgram(e.M)
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	var enr struct {
		Enrollments []struct{ Enrollment string } `json:"enrollments"`
	}
	if err := e.Central.JSON("GET", "/api/tracker/enrollments?program="+p.ID+"&orgUnits="+m.Facility.ID+
		"&orgUnitMode=SELECTED&pageSize=1&fields=enrollment", nil, &enr); err != nil || len(enr.Enrollments) == 0 {
		t.Fatalf("an enrollment to open: %v", err)
	}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.URL.Path {
		case "/link":
			rate, _ := strconv.Atoi(r.URL.Query().Get("rate"))
			delay, _ := strconv.Atoi(r.URL.Query().Get("delay"))
			loss, _ := strconv.ParseFloat(r.URL.Query().Get("loss"), 64)
			e.Net.SetRate(rate)
			e.Net.SetDelay(time.Duration(delay) * time.Millisecond)
			e.Net.SetLoss(loss)
		case "/localreads":
			// The program in facility-hub-config/localReads, or none; the hub reads it
			// with its catalog.
			body := map[string]any{"programs": []string{}}
			if r.URL.Query().Get("on") == "1" {
				body["programs"] = []string{p.ID}
			}
			b, _ := json.Marshal(body)
			e.PutSigned("facility-hub-config/localReads", string(b))
			err = e.App.Hub.RefreshCatalog(r.Context())
		case "/offline":
			err = e.SetUplink(false)
		case "/online":
			err = e.SetUplink(true)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer control.Close()
	out := t.TempDir()
	if f := os.Getenv("HUB_REPORT"); f != "" {
		out = filepath.Join(filepath.Dir(f), "screens")
		os.MkdirAll(out, 0o755)
	}
	cmd := exec.Command("python3", "-I", "../../clients/slowlink_screens.py")
	cmd.Env = append(os.Environ(), "HUB_URL="+e.Srv.URL, "CONTROL_URL="+control.URL, "HUB_USER="+nurse.User,
		"HUB_PASSWORD="+m.Password, "ORG_UNIT="+m.Facility.ID, "PROGRAM="+p.ID,
		"ENROLLMENT="+enr.Enrollments[0].Enrollment, "OUT="+out)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("slowlink_screens.py: %v\n%s", err, b)
	}
	t.Log(string(b))
	if f := os.Getenv("HUB_REPORT"); f != "" {
		if w, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			w.Write(append(bytes.TrimSpace(b), '\n'))
			w.Close()
		}
	}
}
