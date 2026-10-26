package perf

import (
	"fmt"
	"slices"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestOnlineSave times saves through the hub while it is online, with every user
// registered, and checks that another user reads the saved event offline (the hub
// shares what it learns from an online save with every user the rules allow). On the
// 2-CPU Windows VM with a hard disk, saves stalled for up to 85 s after each
// reconnect, when the hub passes requests to central again (docs/PERFORMANCE.md).
func TestOnlineSave(t *testing.T) {
	s, users := scale(t)
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Generate(s)
	e.Setup()
	for i := 0; i < users; i++ {
		if st, b := e.AndroidUser(fc.GenUser(i)).Do("GET", "/api/me?fields=id", nil); st != 200 {
			t.Fatalf("%s: %d %s", fc.GenUser(i), st, b)
		}
	}
	e.Sync()
	var enr, te string
	if err := e.App.Hub.Store.DB().QueryRow(`SELECT uid, te_uid FROM objects WHERE kind = 'enrollment' AND program = ? LIMIT 1`,
		fc.GenProgram(0)).Scan(&enr, &te); err != nil {
		t.Fatal(err)
	}
	author := e.AndroidUser(fc.GenUser(0))
	var took []time.Duration
	var saved []string
	for range 20 {
		ev := tk.UID()
		start := time.Now()
		st, b := author.Do("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{
			"event": ev, "enrollment": enr, "trackedEntity": te, "program": fc.GenProgram(0),
			"programStage": fc.GenStage(0, 1), "orgUnit": fc.Facility, "status": "COMPLETED",
			"occurredAt": time.Now().Format("2006-01-02"), "dataValues": []any{}}}})
		took = append(took, time.Since(start))
		if st != 200 {
			t.Fatalf("save: %d %s", st, b)
		}
		saved = append(saved, ev)
	}
	slices.Sort(took)
	t.Log(fmt.Sprintf("%d users, online save: median %s, max %s", users,
		took[len(took)/2].Round(100*time.Microsecond), took[len(took)-1].Round(100*time.Microsecond)))

	e.Offline()
	other := e.AndroidUser(fc.GenUser(users - 1))
	for _, ev := range saved {
		if st, b := other.Do("GET", "/api/tracker/events/"+ev, nil); st != 200 {
			t.Fatalf("%s reads %s offline: %d %s", fc.GenUser(users-1), ev, st, b)
		}
	}
}
