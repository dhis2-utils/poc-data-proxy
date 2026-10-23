// Command fakecentral serves the test stand-in for DHIS2 (internal/fakecentral) so the
// hub can be exercised end to end without a DHIS2 instance (scripts/test-upgrade.sh).
// POST /fake/down and /fake/up cut and restore the "uplink"; POST /fake/revoke?user=
// revokes a user's personal access tokens. GET /fake/run says which run this is (when
// it started, its size), also while the uplink is down: a hub's machine left to run a
// series of load tests by itself starts a fresh hub for each run and stops it when the
// run's fake central goes away (perf/windows/stress.ps1 -Queue).
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/dhis2/facility-hub/internal/fakecentral"
)

func main() {
	addr := flag.String("listen", ":8090", "listen address")
	var s fakecentral.Scale
	flag.IntVar(&s.TEs, "tes", 0, "generate this many tracked entities at the facility (performance test); 0: the small seed")
	flag.IntVar(&s.Enrollments, "enrollments", 0, "generated enrollments")
	flag.IntVar(&s.Events, "events", 0, "generated events")
	flag.IntVar(&s.Programs, "programs", 6, "generated tracker programs")
	flag.IntVar(&s.Users, "users", 50, "generated users perf_001 … (password "+fakecentral.Password+")")
	flag.IntVar(&s.Years, "years", 5, "years the enrollment dates spread over")
	flag.Parse()
	fc := fakecentral.New()
	if s.TEs > 0 {
		fc.Generate(s)
		log.Printf("generated %d tracked entities, %d enrollments, %d events in %d programs; users perf_001-perf_%03d",
			s.TEs, s.Enrollments, s.Events, s.Programs, s.Users)
	}
	fc.Seed("SeedTe00001", "SeedEn00001", "Seeded", "NAT-0001", map[string]string{"SeedEv00001": fakecentral.Stage1})
	mux := http.NewServeMux()
	mux.HandleFunc("/fake/down", func(w http.ResponseWriter, r *http.Request) { fc.SetDown(true) })
	mux.HandleFunc("/fake/up", func(w http.ResponseWriter, r *http.Request) { fc.SetDown(false) })
	mux.HandleFunc("/fake/revoke", func(w http.ResponseWriter, r *http.Request) { fc.RevokeTokensOf(r.URL.Query().Get("user")) })
	run := map[string]any{"started": time.Now().UTC().Format(time.RFC3339), "tes": s.TEs, "enrollments": s.Enrollments,
		"events": s.Events, "users": s.Users}
	mux.HandleFunc("/fake/run", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(run)
	})
	mux.Handle("/", fc)
	log.Printf("fake central on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
