// Command hubload is the load generator of the performance test (docs/PERFORMANCE.md):
// it sets up a hub against a generated fake central, fills it, cuts the uplink and
// has N users work on it offline at once, then reconnects and measures the push.
//
//	hubload -hub http://hub-pi:8080 -central http://perf-central:8090 -setup -fill -users 50 -duration 10m
//
// The fake central must run with generated data (fakecentral -tes … -users N).
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

var (
	hubURL   = flag.String("hub", "http://hub-pi:8080", "the hub")
	central  = flag.String("central", "http://perf-central:8090", "the fake central (for /fake/down and /fake/up)")
	password = flag.String("password", fc.Password, "password of all users")
	users    = flag.Int("users", 50, "concurrent users perf_001 …")
	webShare = flag.Float64("web", 0.7, "share of users that behave as Capture web; the rest as Android (15 of 50)")
	programs = flag.Int("programs", 6, "generated programs")
	duration = flag.Duration("duration", 10*time.Minute, "offline load duration")
	think    = flag.String("think", "2s-10s", `pause between a web user's actions ("0" for the hammer run)`)
	syncEach = flag.Duration("android-sync", 5*time.Minute, "interval of an Android user's incremental sync")
	andLimit = flag.Int("android-limit", 500, "people an Android device downloads per program, as the Android Settings App limits it (0: all)")
	andFirst = flag.String("android-first", "all", `when Android devices make their first download: "all" at the start at once (deployment day), "spread" over the first half of the run, "none" (done before: a normal working day)`)
	doSetup  = flag.Bool("setup", false, "set up the hub as hub_admin first")
	doFill   = flag.Bool("fill", false, "register the users online and wait for the first pull")
	reconn   = flag.Bool("reconnect", true, "after the load, restore the uplink and time the push")
	out      = flag.String("out", "", "also write the report to this file")
)

func main() {
	flag.Parse()
	lo, hi := thinkRange(*think)
	report := []string{fmt.Sprintf("hubload %s: %d users (%.0f%% web), %s offline, think %s, Android limit %d per program, first downloads %s",
		time.Now().Format(time.RFC3339), *users, *webShare*100, *duration, *think, *andLimit, *andFirst)}
	note := func(f string, a ...any) {
		s := fmt.Sprintf(f, a...)
		log.Print(s)
		report = append(report, s)
	}
	waitUp(3 * time.Minute)
	if *doSetup {
		setup()
		note("hub set up")
	}
	if *doFill {
		start := time.Now()
		for i := 0; i < *users; i++ {
			if st, b, _ := newUser(i).get("/api/me?fields=id"); st != 200 {
				log.Fatalf("register %s: %d %.300s", fc.GenUser(i), st, b)
			}
		}
		note("registered %d users online in %s", *users, time.Since(start).Round(time.Millisecond))
		start = time.Now()
		syncNow()
		note("first pull of all users: %s", time.Since(start).Round(time.Second))
	}
	note("hub status before the cut: %s", status())

	post(*central+"/fake/down", nil)
	waitStatus(func(s map[string]any) bool { return s["mode"] == "offline" }, 2*time.Minute)
	note("uplink cut")

	m := newMetrics()
	var wg sync.WaitGroup
	stop := time.Now().Add(*duration)
	nWeb := int(float64(*users) * *webShare)
	for i := 0; i < *users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u := newUser(i)
			if i < nWeb {
				u.web(m, stop, lo, hi)
			} else {
				u.android(m, stop)
			}
		}(i)
	}
	wg.Wait()
	report = append(report, m.report(*duration)...)
	note("hub status after the load: %s", status())

	if *reconn {
		start := time.Now()
		post(*central+"/fake/up", nil)
		restored := time.Now()
		waitStatus(func(s map[string]any) bool { return s["mode"] == "online" }, 5*time.Minute)
		online := time.Now()
		waitStatus(func(s map[string]any) bool { n, _ := s["pending"].(float64); return n == 0 }, time.Hour)
		// In steps: restoring the fake's uplink, the hub noticing, the push. The
		// Docker runs took minutes in total while the push took seconds.
		note("reconnect: uplink restored in %s, hub online %s later, outbox empty %s later; %s",
			restored.Sub(start).Round(time.Millisecond), online.Sub(restored).Round(time.Second),
			time.Since(online).Round(time.Second), status())
	}
	if *out != "" {
		os.WriteFile(*out, []byte(strings.Join(report, "\n")+"\n"), 0o644)
	}
}

func thinkRange(s string) (time.Duration, time.Duration) {
	if s == "0" || s == "" {
		return 0, 0
	}
	a, b, _ := strings.Cut(s, "-")
	lo, err := time.ParseDuration(a)
	if err != nil {
		log.Fatalf("-think: %v", err)
	}
	hi := lo
	if b != "" {
		if hi, err = time.ParseDuration(b); err != nil {
			log.Fatalf("-think: %v", err)
		}
	}
	return lo, hi
}

// --- hub administration ------------------------------------------------------------

var http0 = &http.Client{Timeout: 5 * time.Minute}

func post(u string, form url.Values) (*http.Response, []byte) {
	resp, err := http0.PostForm(u, form)
	if err != nil {
		log.Fatalf("POST %s: %v", u, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func status() string {
	resp, err := http0.Get(*hubURL + "/hub/status")
	if err != nil {
		return err.Error()
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

// waitUp waits until the hub answers at all (a container that has just started).
func waitUp(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if resp, err := http0.Get(*hubURL + "/hub/status"); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("hub at %s not answering after %s", *hubURL, timeout)
}

func waitStatus(ok func(map[string]any) bool, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	lastSeen := time.Now()
	for time.Now().Before(deadline) {
		var s map[string]any
		if json.Unmarshal([]byte(status()), &s) == nil {
			lastSeen = time.Now()
			if ok(s) {
				return
			}
		} else if time.Since(lastSeen) > 2*time.Minute {
			// The hub is gone (killed for memory, say): waiting out the timeout only
			// stalls the ladder.
			log.Fatalf("hub at %s not answering for 2 minutes: %s", *hubURL, status())
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("hub did not reach the expected state in %s: %s", timeout, status())
}

// admin is a browser session of hub_admin on the hub's own pages.
func admin() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 5 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func adminPost(c *http.Client, path string, form url.Values) []byte {
	req, _ := http.NewRequest("POST", *hubURL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", *hubURL)
	resp, err := c.Do(req)
	if err != nil {
		log.Fatalf("POST %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return b
}

var nonceRe = regexp.MustCompile(`name="nonce" value="([^"]+)"`)

func setup() {
	c := admin()
	b := adminPost(c, "/hub/setup", url.Values{"step": {"login"}, "username": {"hub_admin"}, "password": {*password}})
	if m := nonceRe.FindSubmatch(b); m != nil && bytes.Contains(b, []byte(`value="confirm"`)) {
		b = adminPost(c, "/hub/setup", url.Values{"step": {"confirm"}, "nonce": {string(m[1])}, "replace": {"yes"}})
	}
	if !bytes.Contains(b, []byte("This hub now serves")) {
		log.Fatalf("setup failed: %.1000s", b)
	}
}

// syncNow asks for a sync cycle and waits until one that began after the request has
// finished (a cycle already running when asked does not count).
func syncNow() {
	asked := time.Now()
	c := admin()
	adminPost(c, "/hub/login", url.Values{"username": {"hub_admin"}, "password": {*password}})
	adminPost(c, "/hub/ui/sync", nil)
	waitStatus(func(s map[string]any) bool {
		v, _ := s["syncStarted"].(string)
		started, err := time.Parse(time.RFC3339Nano, v)
		return err == nil && started.After(asked) && s["syncing"] == false
	}, 6*time.Hour)
}

// --- users ---------------------------------------------------------------------------

type user struct {
	name string
	c    *http.Client
}

func newUser(i int) *user {
	return &user{fc.GenUser(i), &http.Client{Timeout: 2 * time.Minute}}
}

func (u *user) do(method, path string, body any) (int, []byte, time.Duration) {
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, *hubURL+path, rd)
	req.SetBasicAuth(u.name, *password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := u.c.Do(req)
	if err != nil {
		return 0, []byte(err.Error()), time.Since(start)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, b, time.Since(start)
}

func (u *user) get(path string) (int, []byte, time.Duration) { return u.do("GET", path, nil) }

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func uid() string {
	b := make([]byte, 11)
	rand.Read(b)
	for i := range b {
		n := len(alnum)
		if i == 0 {
			n = 52
		}
		b[i] = alnum[int(b[i])%n]
	}
	return string(b)
}

func pause(lo, hi time.Duration) {
	if hi > 0 {
		time.Sleep(lo + time.Duration(mrand.Int64N(int64(hi-lo)+1)))
	}
}

var names = []string{"Amina", "Bongani", "Chen", "Dalia", "Emeka", "Farida", "Goran", "Hana"}

// web works like a Capture web user: working list, search, dashboard, sometimes a
// new event.
func (u *user) web(m *metrics, stop time.Time, lo, hi time.Duration) {
	ou := fc.Facility
	for time.Now().Before(stop) {
		p := mrand.IntN(*programs)
		prog := fc.GenProgram(p)
		st, b, d := u.get("/api/tracker/trackedEntities?program=" + prog + "&orgUnits=" + ou +
			"&page=1&pageSize=15&order=createdAt:desc&fields=trackedEntity,attributes[attribute,value],enrollments[enrollment,status,enrolledAt]")
		m.add("web: working list", st, d)
		var list struct {
			TEs []struct {
				TE          string `json:"trackedEntity"`
				Enrollments []struct {
					Enrollment string `json:"enrollment"`
				} `json:"enrollments"`
			} `json:"trackedEntities"`
		}
		json.Unmarshal(b, &list)
		pause(lo, hi)

		st, _, d = u.get("/api/tracker/trackedEntities?program=" + prog + "&orgUnits=" + ou +
			"&filter=" + fc.AttrFirstName + ":like:" + names[mrand.IntN(len(names))] + "&pageSize=15&fields=trackedEntity,attributes[attribute,value]")
		m.add("web: search by name", st, d)
		pause(lo, hi)

		if len(list.TEs) == 0 || len(list.TEs[0].Enrollments) == 0 {
			continue
		}
		pick := list.TEs[mrand.IntN(len(list.TEs))]
		if len(pick.Enrollments) == 0 {
			continue
		}
		enr := pick.Enrollments[0].Enrollment
		st, _, d = u.get("/api/tracker/trackedEntities/" + pick.TE + "?program=" + prog + "&fields=*")
		m.add("web: dashboard, person", st, d)
		st, _, d = u.get("/api/tracker/events?enrollments=" + enr + "&fields=*&paging=false")
		m.add("web: dashboard, events", st, d)
		pause(lo, hi)

		if mrand.IntN(3) == 0 {
			st, b, d = u.do("POST", "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=ALL", map[string]any{
				"events": []any{map[string]any{"event": uid(), "enrollment": enr, "trackedEntity": pick.TE, "program": prog,
					"programStage": fc.GenStage(p, 1), "orgUnit": ou, "status": "COMPLETED",
					"occurredAt": time.Now().Format("2006-01-02"), "dataValues": []any{}}}})
			m.add("web: new event", st, d)
			if st != 200 {
				m.sample("web: new event error", b)
			}
			pause(lo, hi)
		}
	}
}

// android works like the Android Capture app: a first download of every program, up to
// the configured number of people per program (a device at a facility with a hub
// does not need everyone), then incremental syncs with an upload.
func (u *user) android(m *metrics, stop time.Time) {
	ou := fc.Facility
	since := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000")
	download := func(after string) (int, string, string) {
		n, te, enr := 0, "", ""
		start := time.Now()
		for p := 0; p < *programs && time.Now().Before(stop); p++ {
			got := 0
			for page := 1; time.Now().Before(stop) && (*andLimit == 0 || got < *andLimit); page++ {
				path := "/api/tracker/trackedEntities?program=" + fc.GenProgram(p) + "&orgUnit=" + ou +
					"&orgUnitMode=DESCENDANTS&includeDeleted=true&page=" + fmt.Sprint(page) + "&pageSize=50&paging=true" +
					"&fields=trackedEntity,orgUnit,attributes[attribute,value],enrollments[enrollment,program,status,events[event,programStage,status,dataValues[dataElement,value]]]"
				if after != "" {
					path += "&updatedAfter=" + url.QueryEscape(after)
				}
				st, b, d := u.get(path)
				m.add("android: download page", st, d)
				var res struct {
					TEs []struct {
						TE          string `json:"trackedEntity"`
						Enrollments []struct {
							Enrollment string `json:"enrollment"`
						} `json:"enrollments"`
					} `json:"trackedEntities"`
				}
				if st != 200 || json.Unmarshal(b, &res) != nil {
					break
				}
				n += len(res.TEs)
				got += len(res.TEs)
				if te == "" && p == 0 && len(res.TEs) > 0 && len(res.TEs[0].Enrollments) > 0 {
					te, enr = res.TEs[0].TE, res.TEs[0].Enrollments[0].Enrollment
				}
				if len(res.TEs) < 50 {
					break
				}
			}
		}
		if after == "" {
			m.add("android: first download", 200, time.Since(start))
		}
		return n, te, enr
	}
	var n int
	var te, enr string
	switch *andFirst {
	case "none":
		// The device downloaded before the run: it only needs a person to add events to,
		// and its syncs ask for what changed since the run began.
		since = time.Now().UTC().Format("2006-01-02T15:04:05.000")
		st, b, d := u.get("/api/tracker/trackedEntities?program=" + fc.GenProgram(0) + "&orgUnit=" + ou +
			"&pageSize=1&fields=trackedEntity,enrollments[enrollment]")
		m.add("android: pick a person", st, d)
		var res struct {
			TEs []struct {
				TE          string `json:"trackedEntity"`
				Enrollments []struct {
					Enrollment string `json:"enrollment"`
				} `json:"enrollments"`
			} `json:"trackedEntities"`
		}
		if json.Unmarshal(b, &res) == nil && len(res.TEs) > 0 && len(res.TEs[0].Enrollments) > 0 {
			te, enr = res.TEs[0].TE, res.TEs[0].Enrollments[0].Enrollment
		}
	case "spread":
		if half := int64(time.Until(stop) / 2); half > 0 {
			time.Sleep(time.Duration(mrand.Int64N(half)))
		}
		fallthrough
	default:
		n, te, enr = download("")
		m.sample("android: people in a first download", []byte(fmt.Sprint(n)))
	}
	// Devices sync on their own schedules: the first after a random part of the interval.
	first := true
	for time.Now().Before(stop) {
		wait := time.Until(time.Now().Add(*syncEach))
		if first {
			wait, first = time.Duration(mrand.Int64N(int64(*syncEach))), false
		}
		if rest := time.Until(stop); rest < wait {
			wait = rest
		}
		time.Sleep(wait)
		if !time.Now().Before(stop) {
			return
		}
		if enr != "" {
			st, b, d := u.do("POST", "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=OBJECT", map[string]any{
				"events": []any{map[string]any{"event": uid(), "enrollment": enr, "trackedEntity": te, "program": fc.GenProgram(0),
					"programStage": fc.GenStage(0, 2), "orgUnit": ou, "status": "COMPLETED",
					"occurredAt": time.Now().Format("2006-01-02"), "dataValues": []any{}}}})
			m.add("android: upload", st, d)
			if st != 200 {
				m.sample("android: upload error", b)
			}
		}
		start := time.Now()
		download(since)
		m.add("android: incremental sync", 200, time.Since(start))
		since = start.Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000")
	}
}

// --- metrics -------------------------------------------------------------------------

type metrics struct {
	mu      sync.Mutex
	d       map[string][]time.Duration
	errs    map[string]map[int]int
	samples map[string]string
}

func newMetrics() *metrics {
	return &metrics{d: map[string][]time.Duration{}, errs: map[string]map[int]int{}, samples: map[string]string{}}
}

func (m *metrics) add(op string, status int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.d[op] = append(m.d[op], d)
	if status != 200 {
		if m.errs[op] == nil {
			m.errs[op] = map[int]int{}
		}
		m.errs[op][status]++
	}
}

func (m *metrics) sample(op string, b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.samples[op]; !ok {
		m.samples[op] = fmt.Sprintf("%.300s", b)
	}
}

func (m *metrics) report(over time.Duration) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ops := make([]string, 0, len(m.d))
	for op := range m.d {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	out := []string{fmt.Sprintf("%-28s %7s %7s %9s %9s %9s %9s  %s", "operation", "count", "per s", "p50", "p95", "p99", "max", "errors")}
	for _, op := range ops {
		ds := m.d[op]
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		q := func(f float64) time.Duration {
			return ds[min(len(ds)-1, int(f*float64(len(ds))))].Round(time.Millisecond)
		}
		out = append(out, fmt.Sprintf("%-28s %7d %7.1f %9s %9s %9s %9s  %v", op, len(ds), float64(len(ds))/over.Seconds(),
			q(0.5), q(0.95), q(0.99), ds[len(ds)-1].Round(time.Millisecond), m.errs[op]))
	}
	for op, s := range m.samples {
		out = append(out, fmt.Sprintf("%s (first): %s", op, s))
	}
	return out
}
