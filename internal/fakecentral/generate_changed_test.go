package fakecentral

import (
	"testing"
	"time"
)

// With Changed, only that share of active enrollments (and their people) carries the
// generation time; the rest were last changed between their enrollment and two days
// before it, outside the day the hub adds to an updatedAfter date (or at enrollment,
// when that was later). Without it, every
// active enrollment carries the generation time, as before.
func TestGeneratedChangedShare(t *testing.T) {
	count := func(changed float64) (active, now, people int, g *generated) {
		c := New()
		c.Generate(Scale{TEs: 10000, Enrollments: 16666, Events: 16666, Programs: 2, Users: 1, Years: 5, Changed: changed})
		g = c.gen
		limit := g.date(g.base.Add(-48 * time.Hour))
		for j := 0; j < g.Enrollments; j++ {
			if g.status(j) != "ACTIVE" {
				continue
			}
			active++
			switch u := g.enrollmentUpdated(j); {
			case u == g.updatedAt:
				now++
			case (u > limit && g.date(g.enrolled(j)) <= limit) || u < g.date(g.enrolled(j)):
				t.Fatalf("enrollment %d of an unchanged share updated %s: enrolled %s, generated %s", j, u, g.date(g.enrolled(j)), g.updatedAt)
			}
		}
		for i := 0; i < g.TEs; i++ {
			if g.teUpdated(i) == g.updatedAt {
				people++
			}
		}
		return
	}
	active, now, people, _ := count(0)
	if active == 0 || now != active {
		t.Fatalf("without Changed: %d of %d active enrollments changed now, want all", now, active)
	}
	all := people
	active, now, people, _ = count(0.01)
	if now < active/200 || now > active/50 {
		t.Fatalf("Changed 0.01: %d of %d active enrollments changed now, want about 1%%", now, active)
	}
	if people == 0 || people > now || people >= all {
		t.Fatalf("Changed 0.01: %d people changed now (%d enrollments; %d without Changed)", people, now, all)
	}
}
