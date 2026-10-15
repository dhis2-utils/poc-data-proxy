package main

import (
	"encoding/json"
	"flag"
	mrand "math/rand/v2"
	"sync"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// The paced run spreads one working day of a busy HC IV over -duration: the volumes
// from docs/PERFORMANCE.md ("Sizing for a busy urban HC IV"), at an even rate instead
// of users clicking as fast as they think. With the storage's write counters read
// before and after (docs/HARDWARE-TESTING.md), it gives the bytes the hub writes in a
// day.
var (
	paced     = flag.Bool("paced", false, "spread one day's volume (-day-people, -day-events, -day-reads) evenly over -duration")
	dayPeople = flag.Int("day-people", 560, "paced: new people a day, each enrolled in one program")
	dayEvents = flag.Int("day-events", 2600, "paced: new events a day")
	dayReads  = flag.Int("day-reads", 27000, "paced: reads a day (a working list or a dashboard)")
)

type enrolled struct {
	te, enr string
	program int
}

// pacedDay runs the day's operations, shuffled, one every duration/total, on the
// users in turn.
func pacedDay(m *metrics, users []*user, d time.Duration) {
	ops := make([]byte, 0, *dayPeople+*dayEvents+*dayReads)
	for _, o := range []struct {
		kind byte
		n    int
	}{{'p', *dayPeople}, {'e', *dayEvents}, {'r', *dayReads}} {
		for i := 0; i < o.n; i++ {
			ops = append(ops, o.kind)
		}
	}
	mrand.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
	every := d / time.Duration(max(len(ops), 1))
	var mu sync.Mutex
	var known []enrolled // people the run has seen, to add events to
	remember := func(e enrolled) {
		mu.Lock()
		if len(known) < 5000 {
			known = append(known, e)
		} else {
			known[mrand.IntN(len(known))] = e
		}
		mu.Unlock()
	}
	pick := func() (enrolled, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(known) == 0 {
			return enrolled{}, false
		}
		return known[mrand.IntN(len(known))], true
	}
	var wg sync.WaitGroup
	tick := time.NewTicker(every)
	defer tick.Stop()
	for i, op := range ops {
		<-tick.C
		u := users[i%len(users)]
		wg.Add(1)
		go func(op byte) {
			defer wg.Done()
			ou, p := fc.Facility, mrand.IntN(*programs)
			prog := fc.GenProgram(p)
			switch op {
			case 'r':
				st, b, dur := u.get("/api/tracker/trackedEntities?program=" + prog + "&orgUnits=" + ou +
					"&page=1&pageSize=15&order=createdAt:desc&fields=trackedEntity,enrollments[enrollment]")
				m.add("paced: working list", st, dur)
				var list struct {
					TEs []struct {
						TE          string `json:"trackedEntity"`
						Enrollments []struct {
							Enrollment string `json:"enrollment"`
						} `json:"enrollments"`
					} `json:"trackedEntities"`
				}
				if json.Unmarshal(b, &list) == nil {
					for _, t := range list.TEs {
						if len(t.Enrollments) > 0 {
							remember(enrolled{t.TE, t.Enrollments[0].Enrollment, p})
						}
					}
				}
			case 'p':
				te, en := uid(), uid()
				st, b, dur := u.do("POST", "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=ALL", map[string]any{
					"trackedEntities": []any{map[string]any{"trackedEntity": te, "trackedEntityType": fc.TET, "orgUnit": ou,
						"attributes": []any{
							map[string]any{"attribute": fc.AttrFirstName, "value": names[mrand.IntN(len(names))]},
							map[string]any{"attribute": fc.AttrLastName, "value": "Paced"}},
						"enrollments": []any{map[string]any{"enrollment": en, "program": prog, "orgUnit": ou, "status": "ACTIVE",
							"enrolledAt": time.Now().Format("2006-01-02"), "occurredAt": time.Now().Format("2006-01-02")}}}}})
				m.add("paced: new person", st, dur)
				if st != 200 {
					m.sample("paced: new person error", b)
				}
				confirm(u.name, st, b, [2]string{"trackedEntities", te}, [2]string{"enrollments", en})
				if accepted(st, b) {
					remember(enrolled{te, en, p})
				}
			case 'e':
				e, ok := pick()
				if !ok {
					return
				}
				ev := uid()
				st, b, dur := u.do("POST", "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=ALL", map[string]any{
					"events": []any{map[string]any{"event": ev, "enrollment": e.enr, "trackedEntity": e.te,
						"program": fc.GenProgram(e.program), "programStage": fc.GenStage(e.program, 1), "orgUnit": ou,
						"status": "COMPLETED", "occurredAt": time.Now().Format("2006-01-02"), "dataValues": []any{}}}})
				m.add("paced: new event", st, dur)
				if st != 200 {
					m.sample("paced: new event error", b)
				}
				confirm(u.name, st, b, [2]string{"events", ev})
			}
		}(op)
	}
	wg.Wait()
}
